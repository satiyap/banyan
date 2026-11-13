// Package desksync keeps the Banyan server's copy of the register level with
// this desk's.
//
// There is no separate backup. After every change to the register the whole
// file is sent to the server, which numbers each copy it accepts. The desk
// remembers the last number the two agreed on and sends it with the next
// copy, so the server can refuse a desk that has fallen behind another.
//
// Whenever the desk and the server disagree about whose register is the
// newer, nothing is sent and nothing is fetched. The treasurer is asked.
package desksync

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	_ "modernc.org/sqlite"

	"github.com/satiyap/banyan/internal/connection"
)

// Revision is one copy of the register the server accepted.
type Revision struct {
	Revision int64  `json:"revision"`
	Sha256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	SyncedAt string `json:"synced_at"`
}

// The last copy the desk and the server agreed on.
type agreed struct {
	apiUrl   string
	revision int64
	sha256   string
	syncedAt string
}

// SyncStatus is what the page is shown.
type SyncStatus struct {
	Connected bool `json:"connected"`
	// True for a reports-only app: connected, but with no register of its own to keep level.
	ReadOnly bool   `json:"read_only"`
	ApiUrl   string `json:"api_url"`
	// The last time the server took this desk's register, or the desk took the server's.
	SyncedAt string `json:"synced_at"`
	Revision *int64 `json:"revision"`
	// True while this desk holds changes the server has not accepted.
	Pending bool `json:"pending"`
	// Why the last attempt failed, in words for the treasurer. Empty when it worked.
	Error string `json:"error"`
	// Set when the server holds a register this desk did not start from. The treasurer must choose.
	Choice *Revision `json:"choice"`
	// True when the server says another desk has taken over. This desk can no longer send.
	Retired bool `json:"retired"`
}

// SyncPaths says where the register and the connection file are.
type SyncPaths struct {
	DbPath         string
	ConnectionPath string
}

// DeskApiVersion is the newest desk-to-server API this desk understands.
const DeskApiVersion = 1

// ServerInfo is what a Banyan server says about itself at /api/v1/info.
type ServerInfo struct {
	Name     string   `json:"name"`
	Api      float64  `json:"api"`
	Features Features `json:"features"`
}

// Discovery is a server's answer, with the address as it will be stored.
type Discovery struct {
	ApiUrl string `json:"api_url"`
	ServerInfo
}

const (
	timeout  = 20 * time.Second
	retryGap = 60 * time.Second
	debounce = 400 * time.Millisecond
)

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// remove deletes a file; one that is not there is not an error.
func remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Snapshot gives a consistent copy of the register as it is right now, taken
// without disturbing it.
func Snapshot(dbPath string) ([]byte, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	copyPath := dbPath + ".sync-" + hex.EncodeToString(suffix)
	defer os.Remove(copyPath)

	db, err := sql.Open("sqlite", sqliteURI(dbPath)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("VACUUM INTO ?", copyPath); err != nil {
		return nil, err
	}
	return os.ReadFile(copyPath)
}

// sqliteURI names a file so that no character in its path is read as part of a query.
func sqliteURI(path string) string {
	var b strings.Builder
	b.WriteString("file:")
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.IndexByte("/-_.~", c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func restorePath(dbPath string) string { return dbPath + ".restore" }

func agreedPath(connectionPath string) string {
	return filepath.Join(filepath.Dir(connectionPath), "sync.json")
}

// ApplyPendingRestore puts a fetched register in place. Runs when the app
// starts, before the register is opened, because the file cannot be swapped
// under an open desk.
//
// The register being replaced is kept beside it, never deleted.
func ApplyPendingRestore(paths SyncPaths, now func() time.Time) (bool, error) {
	if now == nil {
		now = time.Now
	}
	incoming := restorePath(paths.DbPath)
	note := incoming + ".json"
	if !exists(incoming) || !exists(note) {
		return false, nil
	}
	// The note is read before anything is moved: a damaged one changes nothing.
	raw, err := os.ReadFile(note)
	if err != nil {
		return false, err
	}
	var agreement bytes.Buffer
	if err := json.Indent(&agreement, raw, "", "  "); err != nil {
		return false, fmt.Errorf("%s is not valid JSON: %w", note, err)
	}
	if exists(paths.DbPath) {
		stamp := strings.NewReplacer(":", "-", ".", "-").Replace(isoTime(now()))
		if err := os.Rename(paths.DbPath, paths.DbPath+".before-restore-"+stamp); err != nil {
			return false, err
		}
	}
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		if err := remove(paths.DbPath + sidecar); err != nil {
			return false, err
		}
	}
	if err := os.Rename(incoming, paths.DbPath); err != nil {
		return false, err
	}
	if err := os.WriteFile(agreedPath(paths.ConnectionPath), bytes.TrimSpace(agreement.Bytes()), 0o666); err != nil {
		return false, err
	}
	if err := remove(note); err != nil {
		return false, err
	}
	return true, nil
}

// Options are what a test, or the app, may replace.
type Options struct {
	// Client makes the requests. Nil means a plain client over Transport.
	Client *http.Client
	// Transport is used when Client is nil. Nil means http.DefaultTransport.
	Transport http.RoundTripper
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// Sync sends this desk's register to the server and fetches it back.
type Sync struct {
	paths  SyncPaths
	client *http.Client
	now    func() time.Time

	// One register is sent at a time, whoever asked.
	sending sync.Mutex

	mu      sync.Mutex
	err     string
	choice  *Revision
	retired bool
	pending bool
	running chan struct{}
	again   bool
	timer   *time.Timer
	closed  bool
}

// NewSync makes the desk's link to the server. Nothing is sent until asked.
func NewSync(paths SyncPaths, options Options) *Sync {
	client := options.Client
	if client == nil {
		client = &http.Client{Transport: options.Transport}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Sync{paths: paths, client: client, now: now}
}

// Close stops any send that is waiting on a timer. One under way finishes.
func (s *Sync) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.clearTimer()
}

// Callers hold s.mu.
func (s *Sync) clearTimer() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// Callers hold s.mu.
func (s *Sync) setTimer(after time.Duration) {
	if s.closed {
		return
	}
	s.timer = time.AfterFunc(after, func() { s.Flush() })
}

// The connection, whichever kind of key made it.
func (s *Sync) anyConnection() (connection.Connection, bool) {
	c := connection.ReadConnection(s.paths.ConnectionPath)
	return c, c.Status == connection.StatusConnected
}

// The connection, if it is a desk's. A reports-only app holds no register to send.
func (s *Sync) connection() (connection.Connection, bool) {
	c, ok := s.anyConnection()
	return c, ok && c.Role == "write"
}

// ReadOnly is true when this app was connected with a read key: it shows
// reports and nothing else.
func (s *Sync) ReadOnly() bool {
	c, ok := s.anyConnection()
	return ok && c.Role == "read"
}

func bearer(c connection.Connection) map[string]string {
	return map[string]string{"authorization": "Bearer " + c.Token}
}

// Read fetches something a reader may see from the Banyan server, as that
// reader. The caller closes the body.
func (s *Sync) Read(path string) (*http.Response, error) {
	c, ok := s.anyConnection()
	if !ok {
		return nil, &connection.ConnectionError{Message: "This app is not connected to the server."}
	}
	return s.call(c.ApiUrl, path, http.MethodGet, bearer(c), nil)
}

func (s *Sync) agreed(apiUrl string) *agreed {
	raw, err := os.ReadFile(agreedPath(s.paths.ConnectionPath))
	if err != nil {
		return nil
	}
	var saved map[string]any
	if json.Unmarshal(raw, &saved) != nil || saved == nil {
		return nil
	}
	revision, ok := saved["revision"].(float64)
	if saved["api_url"] != apiUrl || !ok || revision != math.Trunc(revision) || math.Abs(revision) > 1<<53 {
		return nil
	}
	a := &agreed{apiUrl: apiUrl, revision: int64(revision)}
	a.sha256, _ = saved["sha256"].(string)
	a.syncedAt, _ = saved["synced_at"].(string)
	return a
}

// agreedJSON is the agreement as it is kept on disk. A nil revision is one
// the server did not give as a whole number.
func agreedJSON(apiUrl string, revision *int64, digest, syncedAt string) []byte {
	number := "null"
	if revision != nil {
		number = strconv.FormatInt(*revision, 10)
	}
	return []byte("{\n  \"api_url\": " + jsQuote(apiUrl) + ",\n  \"revision\": " + number +
		",\n  \"sha256\": " + jsQuote(digest) + ",\n  \"synced_at\": " + jsQuote(syncedAt) + "\n}")
}

func (s *Sync) agree(apiUrl string, latest Revision, localSha string) error {
	if err := os.MkdirAll(filepath.Dir(s.paths.ConnectionPath), 0o777); err != nil {
		return err
	}
	if err := os.WriteFile(agreedPath(s.paths.ConnectionPath), agreedJSON(apiUrl, &latest.Revision, localSha, latest.SyncedAt), 0o666); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = ""
	s.choice = nil
	s.pending = false
	return nil
}

// Status says where the desk and the server stand.
func (s *Sync) Status() SyncStatus {
	c, desk := s.connection()
	var a *agreed
	if desk {
		a = s.agreed(c.ApiUrl)
	}
	known, connected := s.anyConnection()

	s.mu.Lock()
	defer s.mu.Unlock()
	status := SyncStatus{
		Connected: connected,
		ReadOnly:  connected && known.Role == "read",
		Pending:   desk && s.pending,
		Retired:   desk && s.retired,
	}
	if connected {
		status.ApiUrl = known.ApiUrl
	}
	if a != nil {
		status.SyncedAt = a.syncedAt
		revision := a.revision
		status.Revision = &revision
	}
	if desk {
		status.Error = s.err
		if s.choice != nil {
			choice := *s.choice
			status.Choice = &choice
		}
	}
	return status
}

// cancelOnClose ends a request's time limit when its body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

// call makes one request. The time limit covers the answer's body as well.
func (s *Sync) call(apiUrl, path, method string, headers map[string]string, body []byte) (*http.Response, error) {
	unreachable := &connection.ConnectionError{Message: "The Banyan server could not be reached. Check the internet connection."}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, apiUrl+path, reader)
	if err != nil {
		cancel()
		return nil, unreachable
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := s.client.Do(request)
	if err != nil {
		cancel()
		return nil, unreachable
	}
	response.Body = cancelOnClose{response.Body, cancel}
	return response, nil
}

// reply is an answer read whole.
type reply struct {
	status  int
	header  http.Header
	body    []byte
	bodyErr error
}

func (r *reply) ok() bool { return r.status >= 200 && r.status <= 299 }

func (r *reply) json(into any) error {
	if r.bodyErr != nil {
		return r.bodyErr
	}
	return json.Unmarshal(r.body, into)
}

// detail is the server's own explanation, or its status when it gave none.
func (r *reply) detail() string {
	var body map[string]any
	if r.json(&body) == nil {
		if detail, ok := body["detail"].(string); ok {
			return detail
		}
	}
	return fmt.Sprintf("The server answered %d.", r.status)
}

func (s *Sync) fetch(apiUrl, path, method string, headers map[string]string, body []byte) (*reply, error) {
	response, err := s.call(apiUrl, path, method, headers, body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	r := &reply{status: response.StatusCode, header: response.Header}
	r.body, r.bodyErr = io.ReadAll(response.Body)
	return r, nil
}

// Discover asks an address whether it is a Banyan server this desk can work
// with. Nothing is sent but the question, so a mistyped address costs no key.
func (s *Sync) Discover(apiUrlInput any) (Discovery, error) {
	apiUrl, err := connection.CleanApiUrl(apiUrlInput)
	if err != nil {
		return Discovery{}, err
	}
	notOne := &connection.ConnectionError{Message: "There is no Banyan server at that address. Check the API URL."}
	r, err := s.fetch(apiUrl, "/api/v1/info", http.MethodGet, nil, nil)
	if err != nil {
		return Discovery{}, err
	}
	if !r.ok() {
		return Discovery{}, notOne
	}
	var info map[string]json.RawMessage
	if r.json(&info) != nil || info == nil {
		return Discovery{}, notOne
	}
	var service, name string
	var api float64
	if json.Unmarshal(info["service"], &service) != nil || service != "banyan" || !isJSONNumber(info["api"]) || json.Unmarshal(info["api"], &api) != nil {
		return Discovery{}, notOne
	}
	if api > DeskApiVersion {
		return Discovery{}, &connection.ConnectionError{Message: "That server is newer than this app. Update Banyan, then connect again."}
	}
	var features Features
	if raw := bytes.TrimSpace(info["features"]); len(raw) > 0 && raw[0] == '{' {
		if err := features.UnmarshalJSON(raw); err != nil {
			return Discovery{}, notOne
		}
	}
	if !features.Has("register_sync") {
		return Discovery{}, &connection.ConnectionError{Message: "That server does not take a register from a desk."}
	}
	if raw := bytes.TrimSpace(info["name"]); len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &name) == nil {
		name = firstUnits(name, 80)
	} else {
		name = ""
	}
	return Discovery{ApiUrl: apiUrl, ServerInfo: ServerInfo{Name: name, Api: api, Features: features}}, nil
}

func isJSONNumber(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && (raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9'))
}

// firstUnits cuts a string where JavaScript's slice(0, n) would.
func firstUnits(text string, n int) string {
	units := utf16.Encode([]rune(text))
	if len(units) <= n {
		return text
	}
	return string(utf16.Decode(units[:n]))
}

// Connect redeems a key for this desk and remembers the token it buys.
func (s *Sync) Connect(apiUrlInput, keyInput any) (SyncStatus, error) {
	key, err := connection.CleanUserKey(keyInput)
	if err != nil {
		return SyncStatus{}, err
	}
	// Find out what is at the address before a single-use key is sent to it.
	info, err := s.Discover(apiUrlInput)
	if err != nil {
		return SyncStatus{}, err
	}
	r, err := s.fetch(info.ApiUrl, "/api/v1/redeem", http.MethodPost,
		map[string]string{"content-type": "application/json"}, []byte(`{"key":`+jsQuote(key)+`}`))
	if err != nil {
		return SyncStatus{}, err
	}
	if !r.ok() {
		return SyncStatus{}, &connection.ConnectionError{Message: r.detail()}
	}
	// The key decides what this app is: the cheque desk, or a reports-only reader.
	var bought map[string]any
	if err := r.json(&bought); err != nil {
		return SyncStatus{}, fmt.Errorf("the server's answer to the key could not be read: %w", err)
	}
	token, _ := bought["token"].(string)
	if token == "" {
		return SyncStatus{}, errors.New("the server's answer to the key held no token")
	}
	role := "write"
	if bought["role"] == "read" {
		role = "read"
	}
	if _, err := connection.SaveConnection(s.paths.ConnectionPath, info.ApiUrl, token, info.Name, role); err != nil {
		return SyncStatus{}, err
	}
	s.mu.Lock()
	s.retired = false
	s.err = ""
	s.choice = nil
	s.mu.Unlock()
	return s.Flush(), nil
}

// Disconnect forgets the key. The register on this desk is untouched.
//
// The server's address is kept unless forgetServer is set, so a new key can
// be entered without finding the server again.
func (s *Sync) Disconnect(forgetServer bool) (SyncStatus, error) {
	known := connection.ReadConnection(s.paths.ConnectionPath)
	var remember *connection.Remembered
	if !forgetServer && known.Status != connection.StatusUnset && known.ApiUrl != "" {
		remember = &connection.Remembered{ApiUrl: known.ApiUrl, ServerName: known.ServerName}
	}
	if _, err := connection.SkipConnection(s.paths.ConnectionPath, remember); err != nil {
		return SyncStatus{}, err
	}
	s.mu.Lock()
	s.err = ""
	s.choice = nil
	s.retired = false
	s.pending = false
	s.mu.Unlock()
	return s.Status(), nil
}

// Schedule notes that the register may have changed, and sends it shortly.
func (s *Sync) Schedule() {
	if _, ok := s.connection(); !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = true
	s.clearTimer()
	s.setTimer(debounce)
}

// Flush sends the register now if the server does not have it, and waits for
// the answer.
func (s *Sync) Flush() SyncStatus {
	for {
		s.mu.Lock()
		s.clearTimer()
		if running := s.running; running != nil {
			// A send is under way with an older copy: go round once more after it.
			s.again = true
			s.mu.Unlock()
			<-running
		} else {
			running = make(chan struct{})
			s.running = running
			s.mu.Unlock()
			s.push(false)
			s.mu.Lock()
			s.running = nil
			s.mu.Unlock()
			close(running)
		}
		s.mu.Lock()
		if s.again {
			s.again = false
			s.mu.Unlock()
			continue
		}
		failed := s.err != "" && s.choice == nil && !s.retired
		s.mu.Unlock()
		if failed {
			if _, ok := s.connection(); ok {
				s.mu.Lock()
				s.clearTimer()
				s.setTimer(retryGap)
				s.mu.Unlock()
			}
		}
		return s.Status()
	}
}

// wait returns once no send started by Flush is under way.
func (s *Sync) wait() {
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running != nil {
		<-running
	}
}

// Overwrite replaces the server's register with this desk's. Only ever after
// the treasurer chose it.
func (s *Sync) Overwrite() SyncStatus {
	s.wait()
	s.push(true)
	return s.Status()
}

func (s *Sync) push(force bool) {
	s.sending.Lock()
	defer s.sending.Unlock()
	c, ok := s.connection()
	if !ok || !exists(s.paths.DbPath) {
		return
	}
	if err := s.send(c, force); err != nil {
		message := "The register could not be sent: " + err.Error()
		var known *connection.ConnectionError
		if errors.As(err, &known) {
			message = known.Message
		}
		s.mu.Lock()
		s.err = message
		s.mu.Unlock()
	}
}

// send returns an error only for what went wrong on the way; a refusal by the
// server is recorded and is not one.
func (s *Sync) send(c connection.Connection, force bool) error {
	data, err := Snapshot(s.paths.DbPath)
	if err != nil {
		return err
	}
	digest := sha(data)
	a := s.agreed(c.ApiUrl)
	if !force && a != nil && a.sha256 == digest {
		// Nothing has changed since the server last took it.
		s.mu.Lock()
		s.pending = false
		s.err = ""
		s.mu.Unlock()
		return nil
	}
	s.mu.Lock()
	s.pending = true
	s.mu.Unlock()
	auth := bearer(c)

	var base int64
	if a != nil {
		base = a.revision
	}
	if force || a == nil {
		// No shared starting point yet: see what the server holds first.
		r, err := s.fetch(c.ApiUrl, "/api/v1/register/status", http.MethodGet, auth, nil)
		if err != nil {
			return err
		}
		if !r.ok() {
			s.refused(r)
			return nil
		}
		var held struct {
			Latest *Revision `json:"latest"`
		}
		if err := r.json(&held); err != nil {
			return err
		}
		if held.Latest != nil && held.Latest.Sha256 == digest {
			return s.agree(c.ApiUrl, *held.Latest, digest)
		}
		if held.Latest != nil && !force {
			s.offer(held.Latest)
			return nil
		}
		base = 0
		if held.Latest != nil {
			base = held.Latest.Revision
		}
	}

	auth["content-type"] = "application/vnd.sqlite3"
	auth["x-base-revision"] = strconv.FormatInt(base, 10)
	r, err := s.fetch(c.ApiUrl, "/api/v1/register", http.MethodPut, auth, data)
	if err != nil {
		return err
	}
	var answer struct {
		Latest *Revision `json:"latest"`
	}
	if r.status == http.StatusConflict {
		if err := r.json(&answer); err != nil {
			return err
		}
		s.offer(answer.Latest)
		return nil
	}
	if !r.ok() {
		s.refused(r)
		return nil
	}
	if err := r.json(&answer); err != nil {
		return err
	}
	if answer.Latest == nil {
		return errors.New("the server did not say which copy it now holds")
	}
	return s.agree(c.ApiUrl, *answer.Latest, digest)
}

// offer leaves the choice between the two registers to the treasurer.
func (s *Sync) offer(latest *Revision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.choice = latest
	s.err = ""
}

func (s *Sync) refused(r *reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = r.detail()
	// 401: the server no longer knows this desk. Another desk has taken over.
	if r.status == http.StatusUnauthorized {
		s.retired = true
	}
}

// FetchForRestore fetches the server's register and leaves it beside this
// desk's, to be put in place when the app restarts. Returns once it is safely
// on disk.
func (s *Sync) FetchForRestore() (Revision, error) {
	c, ok := s.connection()
	if !ok {
		return Revision{}, &connection.ConnectionError{Message: "This desk is not connected to the server."}
	}
	s.wait()
	r, err := s.fetch(c.ApiUrl, "/api/v1/register", http.MethodGet, bearer(c), nil)
	if err != nil {
		return Revision{}, err
	}
	if !r.ok() {
		return Revision{}, &connection.ConnectionError{Message: r.detail()}
	}
	if r.bodyErr != nil {
		return Revision{}, fmt.Errorf("the register could not be read from the server: %w", r.bodyErr)
	}
	digest := sha(r.body)
	if digest != r.header.Get("x-sha256") {
		return Revision{}, &connection.ConnectionError{Message: "The register arrived damaged. Nothing on this desk was changed. Try again."}
	}
	latest := Revision{Sha256: digest, Bytes: int64(len(r.body)), SyncedAt: isoTime(s.now())}
	if len(r.header.Values("x-synced-at")) > 0 {
		latest.SyncedAt = strings.Join(r.header.Values("x-synced-at"), ", ")
	}
	// A revision that is not a whole number is kept as none: the next send asks the server afresh.
	var revision *int64
	if text := strings.TrimSpace(strings.Join(r.header.Values("x-revision"), ", ")); text == "" {
		revision = new(int64)
	} else if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		revision = &n
	}
	if revision != nil {
		latest.Revision = *revision
	}
	incoming := restorePath(s.paths.DbPath)
	if err := os.WriteFile(incoming, r.body, 0o666); err != nil {
		return Revision{}, err
	}
	if err := os.WriteFile(incoming+".json", agreedJSON(c.ApiUrl, revision, digest, latest.SyncedAt), 0o666); err != nil {
		return Revision{}, err
	}
	return latest, nil
}
