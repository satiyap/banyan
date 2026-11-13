package desksync

// A desk syncing with a stand-in for the Banyan server. The stand-in answers
// as src/backend/app.ts does, for the routes a desk calls. Every register
// here is invented, or a copy of the golden sample.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/satiyap/banyan/internal/connection"
)

// ------------------------------------------------------------ the stand-in

type fakeToken struct {
	role    string
	revoked bool
}

type fakeBackend struct {
	t      *testing.T
	server *httptest.Server
	url    string

	mu        sync.Mutex
	clock     time.Time
	keys      map[string]string // normalised key -> role; removed once spent
	tokens    map[string]*fakeToken
	revisions []Revision
	files     [][]byte
	seen      []string // "METHOD path" of every request, in order
	puts      []http.Header
	nextKey   int
	damage    bool // answer GET /register with a body that does not match its hash
}

func newBackend(t *testing.T) *fakeBackend {
	b := &fakeBackend{
		t:      t,
		clock:  time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
		keys:   map[string]string{},
		tokens: map[string]*fakeToken{},
	}
	b.server = httptest.NewServer(http.HandlerFunc(b.serve))
	b.url = b.server.URL
	t.Cleanup(b.server.Close)
	return b
}

func (b *fakeBackend) stamp() string {
	b.clock = b.clock.Add(time.Second)
	return b.clock.Format("2006-01-02T15:04:05.000Z")
}

var notKey = regexp.MustCompile(`[^A-Z0-9]`)

func normalKey(key string) string { return notKey.ReplaceAllString(strings.ToUpper(key), "") }

func (b *fakeBackend) issue(role string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextKey++
	key := fmt.Sprintf("ABCDE-FGHJK-MNPQR-STUVW-%05d", b.nextKey)
	b.keys[normalKey(key)] = role
	return key
}

func (b *fakeBackend) latest() *Revision {
	if len(b.revisions) == 0 {
		return nil
	}
	latest := b.revisions[len(b.revisions)-1]
	return &latest
}

func (b *fakeBackend) latestRevision() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if latest := b.latest(); latest != nil {
		return latest.Revision
	}
	return 0
}

func (b *fakeBackend) count(request string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, seen := range b.seen {
		if seen == request {
			n++
		}
	}
	return n
}

// accept records a register as the next revision, as another desk's send would.
func (b *fakeBackend) accept(data []byte) Revision {
	revision := Revision{Revision: int64(len(b.revisions) + 1), Sha256: sha(data), Bytes: int64(len(data)), SyncedAt: b.stamp()}
	b.revisions = append(b.revisions, revision)
	b.files = append(b.files, data)
	return revision
}

func (b *fakeBackend) reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type detail struct {
	Detail string `json:"detail"`
}

var bearerHeader = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

// session is the role of the desk asking, or "" with the refusal already sent.
func (b *fakeBackend) session(w http.ResponseWriter, r *http.Request, write bool) string {
	token := ""
	if m := bearerHeader.FindStringSubmatch(r.Header.Get("authorization")); m != nil {
		token = strings.TrimSpace(m[1])
	}
	held := b.tokens[token]
	if held == nil || held.revoked {
		b.reply(w, 401, detail{"This desk is not signed in to the server, or has been replaced by another desk."})
		return ""
	}
	if write && held.role != "write" {
		b.reply(w, 403, detail{"Only the treasurer’s desk can do that."})
		return ""
	}
	return held.role
}

func (b *fakeBackend) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = append(b.seen, r.Method+" "+r.URL.Path)

	switch r.Method + " " + r.URL.Path {
	case "GET /api/v1/info":
		b.reply(w, 200, json.RawMessage(`{"service":"banyan","name":"Invented Association","api":1,"features":{"register_sync":true,"restore":true,"member_reports":true},"sign_in":"/login"}`))

	case "POST /api/v1/redeem":
		if got := r.Header.Get("content-type"); got != "application/json" {
			b.t.Errorf("redeem content-type = %q", got)
		}
		var sent struct {
			Key string `json:"key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		role, ok := b.keys[normalKey(sent.Key)]
		if !ok {
			b.reply(w, 401, detail{"That key is not valid, or it has already been used."})
			return
		}
		delete(b.keys, normalKey(sent.Key))
		// One desk writes at a time. The desk that held the pen before is retired.
		if role == "write" {
			for _, held := range b.tokens {
				if held.role == "write" {
					held.revoked = true
				}
			}
		}
		token := fmt.Sprintf("%064x", len(b.tokens)+1)
		b.tokens[token] = &fakeToken{role: role}
		b.reply(w, 200, map[string]string{"token": token, "role": role, "label": ""})

	case "GET /api/v1/register/status":
		if role := b.session(w, r, false); role != "" {
			b.reply(w, 200, struct {
				Role   string    `json:"role"`
				Latest *Revision `json:"latest"`
			}{role, b.latest()})
		}

	case "PUT /api/v1/register":
		if b.session(w, r, true) == "" {
			return
		}
		b.puts = append(b.puts, r.Header.Clone())
		base, err := strconv.ParseInt(r.Header.Get("x-base-revision"), 10, 64)
		if err != nil || base < 0 {
			b.reply(w, 400, detail{"X-Base-Revision is missing."})
			return
		}
		data, _ := io.ReadAll(r.Body)
		if len(data) < 100 || !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
			b.reply(w, 400, detail{"That is not a register file."})
			return
		}
		latest := b.latest()
		var held int64
		if latest != nil {
			held = latest.Revision
		}
		if held != base {
			b.reply(w, 409, struct {
				Detail string    `json:"detail"`
				Latest *Revision `json:"latest"`
			}{"The server holds a newer register than the one this desk started from. Nothing was replaced.", latest})
			return
		}
		// The same register again: nothing to record.
		if latest == nil || latest.Sha256 != sha(data) {
			accepted := b.accept(data)
			latest = &accepted
		}
		b.reply(w, 200, struct {
			Latest *Revision `json:"latest"`
		}{latest})

	case "GET /api/v1/register":
		if b.session(w, r, true) == "" {
			return
		}
		latest := b.latest()
		if latest == nil {
			b.reply(w, 404, detail{"The server has no register yet."})
			return
		}
		w.Header().Set("content-type", "application/vnd.sqlite3")
		w.Header().Set("cache-control", "no-store")
		w.Header().Set("x-revision", strconv.FormatInt(latest.Revision, 10))
		w.Header().Set("x-sha256", latest.Sha256)
		w.Header().Set("x-synced-at", latest.SyncedAt)
		body := b.files[len(b.files)-1]
		if b.damage {
			body = append([]byte("x"), body...)
		}
		_, _ = w.Write(body)

	case "GET /api/v1/reports":
		if b.session(w, r, false) != "" {
			b.reply(w, 200, map[string]bool{"ok": true})
		}

	default:
		b.reply(w, 404, detail{"Not Found"})
	}
}

// ---------------------------------------------------------------- the desk

type desk struct {
	t     *testing.T
	paths SyncPaths
	sync  *Sync
}

// newRegister makes an invented register: the two tables the server looks for.
func newRegister(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE TABLE entities (id INTEGER PRIMARY KEY, payee_name TEXT NOT NULL)",
		"CREATE TABLE cheques (id INTEGER PRIMARY KEY, payee TEXT NOT NULL, amount_paise INTEGER NOT NULL)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func payees(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT payee_name FROM entities ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func newDesk(t *testing.T) *desk {
	t.Helper()
	// A folder name with characters a careless SQLite URI would trip on.
	dir := filepath.Join(t.TempDir(), "desk #1 ?50%")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := SyncPaths{DbPath: filepath.Join(dir, "banyan.db"), ConnectionPath: filepath.Join(dir, "connection.json")}
	newRegister(t, paths.DbPath)
	d := &desk{t: t, paths: paths, sync: NewSync(paths, Options{})}
	t.Cleanup(d.sync.Close)
	return d
}

func (d *desk) addPayee(name string) {
	d.t.Helper()
	db, err := sql.Open("sqlite", sqliteURI(d.paths.DbPath))
	if err != nil {
		d.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO entities (payee_name) VALUES (?)", name); err != nil {
		d.t.Fatal(err)
	}
}

func (d *desk) connect(b *fakeBackend, role string) SyncStatus {
	d.t.Helper()
	status, err := d.sync.Connect(b.url, b.issue(role))
	if err != nil {
		d.t.Fatal(err)
	}
	return status
}

func revisionOf(status SyncStatus) int64 {
	if status.Revision == nil {
		return -1
	}
	return *status.Revision
}

func wantMessage(t *testing.T, err error, pattern string) {
	t.Helper()
	var known *connection.ConnectionError
	if !errors.As(err, &known) {
		t.Fatalf("got %v, want a ConnectionError", err)
	}
	if !regexp.MustCompile(pattern).MatchString(known.Message) {
		t.Fatalf("got %q, want it to match %q", known.Message, pattern)
	}
}

// transport answers every request itself, and notes what was asked.
type transport struct {
	seen  []string
	reply func() (*http.Response, error)
}

func (f *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.seen = append(f.seen, r.Method+" "+r.URL.String())
	return f.reply()
}

func answering(status int, contentType, body string) func() (*http.Response, error) {
	return func() (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{contentType}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
}

func offline() (*http.Response, error) { return nil, errors.New("offline") }

// ------------------------------------------------------------------- tests

func TestDeskChecksTheAddressBeforeSpendingAKey(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)

	found, err := a.sync.Discover(b.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(found)
	want := `{"api_url":"` + b.url + `","name":"Invented Association","api":1,"features":{"register_sync":true,"restore":true,"member_reports":true}}`
	if string(raw) != want {
		t.Fatalf("got %s\nwant %s", raw, want)
	}

	// Somewhere that answers, but is not a Banyan server: the key never leaves the desk.
	key := b.issue("write")
	elsewhere := &transport{reply: answering(200, "text/html", "<html>hello</html>")}
	_, err = NewSync(a.paths, Options{Transport: elsewhere}).Connect("https://example.org", key)
	wantMessage(t, err, `no Banyan server at that address`)
	if len(elsewhere.seen) != 1 || elsewhere.seen[0] != "GET https://example.org/api/v1/info" {
		t.Fatalf("asked %v", elsewhere.seen)
	}

	// A server that speaks a newer API than this desk knows.
	newer := &transport{reply: answering(200, "application/json", `{"service":"banyan","name":"X","api":99,"features":{"register_sync":true}}`)}
	_, err = NewSync(a.paths, Options{Transport: newer}).Connect("https://example.org", key)
	wantMessage(t, err, `newer than this app`)

	// One that takes no register from a desk.
	reports := &transport{reply: answering(200, "application/json", `{"service":"banyan","api":1,"features":{"member_reports":true}}`)}
	_, err = NewSync(a.paths, Options{Transport: reports}).Connect("https://example.org", key)
	wantMessage(t, err, `does not take a register`)

	// One that answers, but not with 200.
	missing := &transport{reply: answering(404, "application/json", `{"detail":"Not Found"}`)}
	_, err = NewSync(a.paths, Options{Transport: missing}).Discover("https://example.org")
	wantMessage(t, err, `no Banyan server at that address`)

	// A bad key or address is turned away before anything is asked.
	_, err = a.sync.Connect(b.url, "two words")
	wantMessage(t, err, `space or line break`)
	_, err = a.sync.Connect("http://example.org", key)
	wantMessage(t, err, `must start with https://`)

	// The key was never used up, and the name the server gave is remembered.
	if _, err := a.sync.Connect(b.url, key); err != nil {
		t.Fatal(err)
	}
	if got := connection.ReadConnection(a.paths.ConnectionPath); got.ServerName != "Invented Association" {
		t.Fatalf("got %+v", got)
	}
}

func TestDeskSendsItsRegisterAfterConnectingAndAfterEachChange(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)

	raw, _ := json.Marshal(a.sync.Status())
	if want := `{"connected":false,"read_only":false,"api_url":"","synced_at":"","revision":null,"pending":false,"error":"","choice":null,"retired":false}`; string(raw) != want {
		t.Fatalf("got %s", raw)
	}
	a.sync.Flush() // not connected: nothing happens
	if b.latestRevision() != 0 || len(b.seen) != 0 {
		t.Fatalf("an unconnected desk asked %v", b.seen)
	}

	status := a.connect(b, "write")
	raw, _ = json.Marshal(status)
	want := `{"connected":true,"read_only":false,"api_url":"` + b.url + `","synced_at":"2026-11-12T10:00:01.000Z","revision":1,"pending":false,"error":"","choice":null,"retired":false}`
	if string(raw) != want {
		t.Fatalf("got %s\nwant %s", raw, want)
	}
	if got := strings.Join(b.seen, ", "); got != "GET /api/v1/info, POST /api/v1/redeem, GET /api/v1/register/status, PUT /api/v1/register" {
		t.Fatalf("asked %s", got)
	}
	if put := b.puts[0]; put.Get("content-type") != "application/vnd.sqlite3" || put.Get("x-base-revision") != "0" {
		t.Fatalf("sent %v", put)
	}

	// The key is spent; what is kept is the token.
	saved := connection.ReadConnection(a.paths.ConnectionPath)
	if saved.Status != "connected" || saved.ApiUrl != b.url || saved.Role != "write" || len(saved.Token) != 64 {
		t.Fatalf("kept status %q, address %q, role %q", saved.Status, saved.ApiUrl, saved.Role)
	}
	file, _ := os.ReadFile(a.paths.ConnectionPath)
	if strings.Contains(string(file), "user_key") || strings.Contains(string(file), "ABCDE") {
		t.Fatal("the key was kept")
	}

	// What the two agreed on is kept beside the connection, as version 1 wrote it.
	local, err := Snapshot(a.paths.DbPath)
	if err != nil {
		t.Fatal(err)
	}
	agreement, _ := os.ReadFile(filepath.Join(filepath.Dir(a.paths.ConnectionPath), "sync.json"))
	wantAgreement := "{\n  \"api_url\": \"" + b.url + "\",\n  \"revision\": 1,\n  \"sha256\": \"" + sha(local) + "\",\n  \"synced_at\": \"2026-11-12T10:00:01.000Z\"\n}"
	if string(agreement) != wantAgreement {
		t.Fatalf("got %q\nwant %q", agreement, wantAgreement)
	}

	a.sync.Flush() // unchanged: no new copy, and nothing asked
	if b.latestRevision() != 1 || b.count("PUT /api/v1/register") != 1 || len(b.seen) != 4 {
		t.Fatalf("an unchanged register was sent again: %v", b.seen)
	}

	a.addPayee("Invented Electricals")
	if got := revisionOf(a.sync.Flush()); got != 2 {
		t.Fatalf("revision = %d", got)
	}
	// A desk with a starting point sends straight away, saying where it started.
	if b.count("GET /api/v1/register/status") != 1 || b.puts[1].Get("x-base-revision") != "1" {
		t.Fatalf("asked %v", b.seen)
	}
}

func TestWrongKeyConnectsNothingAndNothingIsSaved(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)
	_, err := a.sync.Connect(b.url, "WRONG-WRONG")
	wantMessage(t, err, `not valid`)
	if got := connection.ReadConnection(a.paths.ConnectionPath); got.Status != "unset" {
		t.Fatalf("got %+v", got)
	}
	// A key works once.
	key := b.issue("write")
	if _, err := a.sync.Connect(b.url, key); err != nil {
		t.Fatal(err)
	}
	_, err = newDesk(t).sync.Connect(b.url, key)
	wantMessage(t, err, `already been used`)
}

func TestReadKeyMakesTheAppReportsOnly(t *testing.T) {
	b := newBackend(t)
	// The treasurer's desk has sent a register.
	treasurer := newDesk(t)
	treasurer.connect(b, "write")
	treasurer.addPayee("Invented Electricals")
	treasurer.sync.Flush()
	before := b.latestRevision()
	puts := b.count("PUT /api/v1/register")

	a := newDesk(t)
	a.addPayee("On The Reader Mac Only")
	status := a.connect(b, "read")
	if !status.Connected || !status.ReadOnly || status.Pending || status.Choice != nil || status.Revision != nil {
		t.Fatalf("got %+v", status)
	}
	if !a.sync.ReadOnly() || treasurer.sync.ReadOnly() {
		t.Fatal("ReadOnly is wrong")
	}
	if got := connection.ReadConnection(a.paths.ConnectionPath); got.Role != "read" {
		t.Fatalf("role = %q", got.Role)
	}

	// Nothing it does reaches the server's register, and the treasurer is not retired.
	a.sync.Schedule()
	a.sync.Flush()
	a.sync.Overwrite()
	if b.latestRevision() != before || b.count("PUT /api/v1/register") != puts || b.count("GET /api/v1/register/status") != 1 {
		t.Fatalf("a reports-only app asked %v", b.seen)
	}
	_, err := a.sync.FetchForRestore()
	wantMessage(t, err, `This desk is not connected to the server\.`)
	if exists(a.paths.DbPath + ".restore") {
		t.Fatal("a register was fetched")
	}
	if treasurer.sync.Flush().Retired {
		t.Fatal("the treasurer was retired")
	}

	// It can read the reports, and only the reports.
	for path, want := range map[string]int{"/api/v1/reports": 200, "/api/v1/reports/monthly.pdf": 404, "/api/v1/register": 403} {
		reply, err := a.sync.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		reply.Body.Close()
		if reply.StatusCode != want {
			t.Fatalf("%s answered %d, want %d", path, reply.StatusCode, want)
		}
	}
	_, err = newDesk(t).sync.Read("/api/v1/reports")
	wantMessage(t, err, `This app is not connected to the server\.`)
}

func TestResettingTheKeyKeepsTheAddressUnlessToldToForgetIt(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)
	a.connect(b, "write")
	status, err := a.sync.Disconnect(false)
	if err != nil || status.Connected || status.ApiUrl != "" || status.Revision != nil {
		t.Fatalf("got %+v, %v", status, err)
	}
	want := connection.Connection{Status: "skipped", ApiUrl: b.url, ServerName: "Invented Association"}
	if got := connection.ReadConnection(a.paths.ConnectionPath); got != want {
		t.Fatalf("got %+v", got)
	}
	file, _ := os.ReadFile(a.paths.ConnectionPath)
	if strings.Contains(string(file), "token") {
		t.Fatal("the token was kept")
	}
	// The register on this desk is untouched.
	if !exists(a.paths.DbPath) {
		t.Fatal("the register is gone")
	}
	// A second reset still remembers it; asking to forget does not.
	a.sync.Disconnect(false)
	if got := connection.ReadConnection(a.paths.ConnectionPath); got != want {
		t.Fatalf("got %+v", got)
	}
	a.sync.Disconnect(true)
	if got := connection.ReadConnection(a.paths.ConnectionPath); got != (connection.Connection{Status: "skipped"}) {
		t.Fatalf("got %+v", got)
	}
	// Nothing is sent once the key is forgotten.
	asked := len(b.seen)
	a.addPayee("After The Reset")
	a.sync.Schedule()
	a.sync.Flush()
	if len(b.seen) != asked {
		t.Fatalf("a disconnected desk asked %v", b.seen[asked:])
	}
}

func TestUnreachableServerIsReportedAndChangesStayPending(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)
	a.connect(b, "write")

	gone := NewSync(a.paths, Options{Transport: &transport{reply: offline}})
	t.Cleanup(gone.Close)
	a.addPayee("Invented Electricals")
	status := gone.Flush()
	if !status.Pending || revisionOf(status) != 1 || status.Error != "The Banyan server could not be reached. Check the internet connection." {
		t.Fatalf("got %+v", status)
	}
	gone.mu.Lock()
	retrying := gone.timer != nil
	gone.mu.Unlock()
	if !retrying {
		t.Fatal("no retry was set")
	}
	// Back online: it catches up.
	status = a.sync.Flush()
	if status.Pending || revisionOf(status) != 2 || status.Error != "" {
		t.Fatalf("got %+v", status)
	}

	// A server that has really gone away.
	closed := httptest.NewServer(http.NotFoundHandler())
	address := closed.URL
	closed.Close()
	_, err := a.sync.Discover(address)
	wantMessage(t, err, `could not be reached`)
	_, err = newDesk(t).sync.Connect(address, b.issue("write"))
	wantMessage(t, err, `could not be reached`)
}

func TestHandingOverTheNewDeskRestoresAndTheOldDeskIsRetired(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)
	a.connect(b, "write")
	a.addPayee("Invented Electricals")
	a.sync.Flush()

	// A second Mac with a fresh, different register.
	second := newDesk(t)
	second.addPayee("Only On The Second Mac")
	status := second.connect(b, "write")
	// It does not send and does not fetch: the treasurer is asked.
	if status.Choice == nil || status.Choice.Revision != 2 || status.Error != "" || !status.Pending || status.Revision != nil {
		t.Fatalf("got %+v", status)
	}
	if b.latestRevision() != 2 || exists(second.paths.DbPath+".restore") {
		t.Fatal("the register was sent or fetched without asking")
	}

	// The old desk can no longer write to the server.
	a.addPayee("Written After Handover")
	status = a.sync.Flush()
	if !status.Retired || !status.Pending || status.Error != "This desk is not signed in to the server, or has been replaced by another desk." {
		t.Fatalf("got %+v", status)
	}
	a.sync.mu.Lock()
	retrying := a.sync.timer != nil
	a.sync.mu.Unlock()
	if retrying {
		t.Fatal("a retired desk keeps trying")
	}
	if b.latestRevision() != 2 {
		t.Fatal("a retired desk wrote to the server")
	}
	// Its token is still on file: only the treasurer forgets a key.
	if got := connection.ReadConnection(a.paths.ConnectionPath); got.Status != "connected" {
		t.Fatalf("got status %q", got.Status)
	}

	// The treasurer chooses the server's register. It goes in at the next start.
	fetched, err := second.sync.FetchForRestore()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(fetched)
	want := fmt.Sprintf(`{"revision":2,"sha256":"%s","bytes":%d,"synced_at":"2026-11-12T10:00:02.000Z"}`, fetched.Sha256, fetched.Bytes)
	if string(raw) != want {
		t.Fatalf("got %s", raw)
	}
	if got := payees(t, second.paths.DbPath); len(got) != 1 || got[0] != "Only On The Second Mac" {
		t.Fatalf("the open register changed: %v", got)
	}
	note, _ := os.ReadFile(second.paths.DbPath + ".restore.json")
	wantNote := "{\n  \"api_url\": \"" + b.url + "\",\n  \"revision\": 2,\n  \"sha256\": \"" + fetched.Sha256 + "\",\n  \"synced_at\": \"2026-11-12T10:00:02.000Z\"\n}"
	if string(note) != wantNote {
		t.Fatalf("got %q", note)
	}

	// Sidecars of the register being replaced must not be read against the new one.
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(second.paths.DbPath+sidecar, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := os.ReadFile(second.paths.DbPath)
	now := func() time.Time { return time.Date(2026, 11, 13, 9, 30, 15, 250e6, time.UTC) }
	if done, err := ApplyPendingRestore(second.paths, now); err != nil || !done {
		t.Fatalf("got %v, %v", done, err)
	}
	if done, err := ApplyPendingRestore(second.paths, now); err != nil || done {
		t.Fatalf("a second restore: %v, %v", done, err)
	}
	// The register that was replaced is kept beside it.
	kept, err := os.ReadFile(second.paths.DbPath + ".before-restore-2026-11-13T09-30-15-250Z")
	if err != nil || !bytes.Equal(kept, old) {
		t.Fatalf("the replaced register was not kept: %v", err)
	}
	for _, gone := range []string{"-wal", "-shm", "-journal", ".restore", ".restore.json"} {
		if exists(second.paths.DbPath + gone) {
			t.Fatalf("%s was left behind", gone)
		}
	}
	agreement, _ := os.ReadFile(filepath.Join(filepath.Dir(second.paths.ConnectionPath), "sync.json"))
	if string(agreement) != wantNote {
		t.Fatalf("got %q", agreement)
	}
	if got := payees(t, second.paths.DbPath); len(got) != 1 || got[0] != "Invented Electricals" {
		t.Fatalf("restored %v", got)
	}

	restarted := NewSync(second.paths, Options{})
	t.Cleanup(restarted.Close)
	// A fresh snapshot of the restored register may differ by a byte or two
	// from the one fetched, so the server may number it again: no choice arises.
	status = restarted.Flush()
	if status.Choice != nil || status.Error != "" || status.Pending || revisionOf(status) < 2 {
		t.Fatalf("got %+v", status)
	}
	held := revisionOf(status)
	second.addPayee("Made-up Plumbing")
	status = restarted.Flush()
	if revisionOf(status) != held+1 || status.Choice != nil {
		t.Fatalf("got %+v", status)
	}
}

func TestTreasurerCanChooseThisDesksRegisterOverTheServers(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)
	a.connect(b, "write")
	second := newDesk(t)
	second.addPayee("The Right One")
	if status := second.connect(b, "write"); status.Choice == nil {
		t.Fatalf("got %+v", status)
	}
	// Asking again changes nothing while the choice stands.
	if status := second.sync.Flush(); status.Choice == nil || b.latestRevision() != 1 {
		t.Fatalf("got %+v", status)
	}
	status := second.sync.Overwrite()
	if status.Choice != nil || status.Pending || revisionOf(status) != 2 {
		t.Fatalf("got %+v", status)
	}
	if got := b.puts[len(b.puts)-1].Get("x-base-revision"); got != "1" {
		t.Fatalf("overwrote from base %s", got)
	}
}

func TestDeskThatHasFallenBehindIsRefusedAndTheTreasurerIsAsked(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)
	a.connect(b, "write")

	// The server moves on without this desk knowing.
	elsewhere := filepath.Join(t.TempDir(), "other.db")
	newRegister(t, elsewhere)
	other, err := Snapshot(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.accept(append(other, make([]byte, 4096)...))
	b.mu.Unlock()

	a.addPayee("Invented Electricals")
	status := a.sync.Flush()
	if status.Choice == nil || status.Choice.Revision != 2 || status.Error != "" || !status.Pending || revisionOf(status) != 1 {
		t.Fatalf("got %+v", status)
	}
	if b.latestRevision() != 2 {
		t.Fatal("a stale register replaced a newer one")
	}
	a.sync.mu.Lock()
	retrying := a.sync.timer != nil
	a.sync.mu.Unlock()
	if retrying {
		t.Fatal("a desk waiting on the treasurer keeps trying")
	}

	status = a.sync.Overwrite()
	if status.Choice != nil || status.Pending || revisionOf(status) != 3 || status.Error != "" {
		t.Fatalf("got %+v", status)
	}
}

func TestRegisterThatArrivesDamagedChangesNothing(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)

	_, err := a.sync.FetchForRestore()
	wantMessage(t, err, `This desk is not connected to the server\.`)

	// A server with no register yet.
	a.connect(b, "write")
	b.mu.Lock()
	b.revisions, b.files = nil, nil
	b.mu.Unlock()
	_, err = a.sync.FetchForRestore()
	wantMessage(t, err, `The server has no register yet\.`)

	a.sync.Overwrite()
	b.mu.Lock()
	b.damage = true
	b.mu.Unlock()
	_, err = a.sync.FetchForRestore()
	wantMessage(t, err, `arrived damaged\. Nothing on this desk was changed`)
	if exists(a.paths.DbPath+".restore") || exists(a.paths.DbPath+".restore.json") {
		t.Fatal("a damaged register was kept")
	}
	if done, err := ApplyPendingRestore(a.paths, nil); err != nil || done {
		t.Fatalf("got %v, %v", done, err)
	}
}

func TestRestoreIsNotStartedWithoutBothFilesOrWithADamagedNote(t *testing.T) {
	a := newDesk(t)
	before, _ := os.ReadFile(a.paths.DbPath)
	incoming := a.paths.DbPath + ".restore"

	// The register alone, without the note that says what it is.
	if err := os.WriteFile(incoming, []byte("incoming"), 0o600); err != nil {
		t.Fatal(err)
	}
	if done, err := ApplyPendingRestore(a.paths, nil); err != nil || done {
		t.Fatalf("got %v, %v", done, err)
	}
	// A note that cannot be read stops the restore before anything moves.
	if err := os.WriteFile(incoming+".json", []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if done, err := ApplyPendingRestore(a.paths, nil); err == nil || done {
		t.Fatalf("got %v, %v", done, err)
	}
	after, _ := os.ReadFile(a.paths.DbPath)
	if !bytes.Equal(before, after) || !exists(incoming) {
		t.Fatal("a damaged note moved the register")
	}
	matches, _ := filepath.Glob(a.paths.DbPath + ".before-restore-*")
	if len(matches) != 0 {
		t.Fatalf("left %v", matches)
	}
}

func TestScheduleSendsShortlyAfterAChange(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)
	newDesk(t).sync.Schedule() // not connected: nothing is noted
	a.connect(b, "write")
	a.addPayee("Invented Electricals")
	a.sync.Schedule()
	a.sync.Schedule()
	if status := a.sync.Status(); !status.Pending || revisionOf(status) != 1 {
		t.Fatalf("got %+v", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.latestRevision() != 2 || a.sync.Status().Pending {
		if time.Now().After(deadline) {
			t.Fatal("the change was never sent")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := b.count("PUT /api/v1/register"); got != 2 {
		t.Fatalf("sent %d times", got)
	}
}

func TestFlushesAskedForTogetherDoNotCollide(t *testing.T) {
	b := newBackend(t)
	a := newDesk(t)
	a.connect(b, "write")
	a.addPayee("Invented Electricals")
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			a.sync.Flush()
		}()
	}
	wait.Wait()
	if status := a.sync.Flush(); status.Pending || revisionOf(status) != 2 || status.Error != "" {
		t.Fatalf("got %+v", status)
	}
	if got := b.count("PUT /api/v1/register"); got != 2 {
		t.Fatalf("sent %d times", got)
	}
}

func TestSnapshotOfTheSampleRegisterLeavesItAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "register.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "tests", "golden", "register-sample.db"+suffix))
		if err != nil {
			if suffix == "" {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path+suffix, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(path)
	first, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(first, []byte("SQLite format 3\x00")) || !bytes.Equal(first, second) {
		t.Fatal("the snapshot is not a steady copy of the register")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("the register was changed")
	}
	left, _ := filepath.Glob(path + ".sync-*")
	if len(left) != 0 {
		t.Fatalf("left %v", left)
	}

	// The copy is a whole register: it opens and holds the same tables.
	copied := filepath.Join(dir, "copy.db")
	if err := os.WriteFile(copied, first, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteURI(copied)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('cheques', 'entities')").Scan(&tables); err != nil || tables != 2 {
		t.Fatalf("tables = %d, %v", tables, err)
	}

	if _, err := Snapshot(filepath.Join(dir, "no-such.db")); err == nil {
		t.Fatal("a missing register gave a snapshot")
	}
}
