package server

// The routes. The server does three things.
//   1. Turns single-use keys into tokens (/api/v1/redeem, /login).
//   2. Takes the register from the writing desk after every change, and hands
//      it back to a desk that needs to restore (/api/v1/register).
//   3. Shows members the reports, read-only, in a browser (/).
//
// The report pages and their numbers are the desk's own: the same frontend
// files and the same report code, run against the latest register received.
// Nothing here can write a cheque, and nothing here can print.

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/satiyap/banyan/internal/desk"
)

// Config is what the server is started with.
type Config struct {
	// Where keys, tokens and registers are kept.
	DataDir string
	// The desk's frontend files and print template: static/... and templates/...
	Assets fs.FS
	// The calibration file the desk's code loads. Reports do not use it.
	CalPath string
	// Lets its holder issue keys. With none set, the admin routes do not exist.
	AdminToken string
	// False only for tests and local runs over plain http.
	SecureCookies bool
	// What this server calls itself to an app that asks, e.g. the association's name.
	Name string
}

// Options are the parts a test may replace.
type Options struct {
	// The clock. Nil means time.Now.
	Now func() time.Time
}

// APIVersion is the version of the desk-to-server API. Raised only when a
// change would break a desk built against the previous number; a desk refuses
// to connect to a server that speaks a newer one than it knows.
const APIVersion = 1

// MinAdminToken is the shortest admin token the server will run with.
const MinAdminToken = 32

const (
	readerCookie = "banyan_reader"
	cookieDays   = 180
)

// What a signed-in member may fetch from the desk's own API. Reports, and
// nothing that writes.
var readerAPI = []*regexp.Regexp{
	regexp.MustCompile(`^/api/reports$`),
	regexp.MustCompile(`^/api/reports/monthly$`),
	regexp.MustCompile(`^/api/reports/[a-z]+\.csv$`),
}

func readerMay(p string) bool {
	for _, allowed := range readerAPI {
		if allowed.MatchString(p) {
			return true
		}
	}
	return false
}

// attempts slows down guessing at keys: a handful of wrong tries per address,
// then a wait.
type attempts struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	limit    int
	window   time.Duration
	now      func() time.Time
}

func (a *attempts) recent(who string) []time.Time {
	since := a.now().Add(-a.window)
	var kept []time.Time
	for _, at := range a.failures[who] {
		if at.After(since) {
			kept = append(kept, at)
		}
	}
	if len(kept) > 0 {
		a.failures[who] = kept
	} else {
		delete(a.failures, who)
	}
	return kept
}

func (a *attempts) blocked(who string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.recent(who)) >= a.limit
}

func (a *attempts) failed(who string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures[who] = append(a.recent(who), a.now())
}

// Server is the Banyan server. It is an http.Handler.
type Server struct {
	cfg      Config
	Store    *Store
	desk     *desk.App
	attempts *attempts
}

// noLeafPhoto hides the cheque photo from the files the server can serve. It
// shows real banking details and is never for members.
type noLeafPhoto struct{ fs.FS }

func (f noLeafPhoto) Open(name string) (fs.File, error) {
	if strings.EqualFold(path.Base(name), "leaf-photo.jpg") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return f.FS.Open(name)
}

// New opens the store and readies the report pages.
func New(cfg Config, opts Options) (*Server, error) {
	if cfg.AdminToken != "" && len(cfg.AdminToken) < MinAdminToken {
		return nil, fmt.Errorf("BANYAN_ADMIN_TOKEN is too short to be safe. Use at least %d random characters: openssl rand -hex 32", MinAdminToken)
	}
	if cfg.Assets == nil {
		return nil, errors.New("server: Config.Assets is not set")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	store, err := OpenStore(cfg.DataDir, now)
	if err != nil {
		return nil, err
	}
	if err := store.Republish(); err != nil {
		store.Close()
		return nil, err
	}

	// The desk's own app, pointed at the latest register and reachable only
	// through the short list of report routes below.
	nowhere := filepath.Join(cfg.DataDir, "no-such-folder")
	deskOpts := desk.Options{Sync: desk.NoSync(filepath.Join(nowhere, "connection.json"))}
	if opts.Now != nil {
		deskOpts.Now = opts.Now
		deskOpts.Today = func() string { return opts.Now().Format("2006-01-02") }
	}
	theDesk, err := desk.New(desk.Config{
		DBPath:         store.CurrentPath,
		CalPath:        cfg.CalPath,
		PDFDir:         filepath.Join(cfg.DataDir, "tmp"),
		Assets:         noLeafPhoto{cfg.Assets},
		DataStaticDir:  nowhere,
		ConnectionPath: filepath.Join(nowhere, "connection.json"),
		PrintDisabled:  true,
	}, deskOpts)
	if err != nil {
		store.Close()
		return nil, err
	}
	return &Server{
		cfg:   cfg,
		Store: store,
		desk:  theDesk,
		attempts: &attempts{
			failures: map[string][]time.Time{},
			limit:    8,
			window:   10 * time.Minute,
			now:      now,
		},
	}, nil
}

// Close releases the desk and the store.
func (s *Server) Close() {
	s.desk.Close()
	_ = s.Store.Close()
}

// ------------------------------------------------------------- responses

type detail struct {
	Detail string `json:"detail"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		status, out = 500, *bytes.NewBufferString(`{"detail":"The server could not do that."}` + "\n")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimRight(out.Bytes(), "\n"))
}

func notFound(w http.ResponseWriter) { writeJSON(w, 404, detail{"Not Found"}) }

// fail answers with a refusal, or with a plain 500 for anything unexpected.
func fail(w http.ResponseWriter, err error) {
	var refused *StoreError
	if errors.As(err, &refused) {
		if refused.stale {
			writeJSON(w, 409, struct {
				Detail string    `json:"detail"`
				Latest *Revision `json:"latest"`
			}{refused.Message, refused.Latest})
			return
		}
		writeJSON(w, refused.Status, detail{refused.Message})
		return
	}
	log.Printf("server: %v", err)
	writeJSON(w, 500, detail{"The server could not do that."})
}

func redirect(w http.ResponseWriter, location string, status int) {
	w.Header().Set("Location", location)
	w.WriteHeader(status)
}

func (s *Server) page(w http.ResponseWriter, name string) {
	html, err := s.desk.Page(name)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, html)
}

// -------------------------------------------------------------- requests

var bearerRE = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

func bearer(r *http.Request) string {
	if m := bearerRE.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func same(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// caller is who a wrong key is counted against: the address the proxy in
// front reports, or one bucket for everyone when there is no proxy.
func caller(r *http.Request) string {
	if ip := r.Header.Get("Cf-Connecting-Ip"); ip != "" {
		return ip
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	return "direct"
}

// rawPath is the request's path as sent, without the query.
func rawPath(r *http.Request) string {
	uri := r.RequestURI
	if uri == "" {
		uri = r.URL.EscapedPath()
	}
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		uri = uri[:i]
	}
	if uri == "" {
		return "/"
	}
	return uri
}

// sentObject reads a JSON object from the body. Anything else is no fields.
func sentObject(r *http.Request) map[string]any {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil
	}
	var sent map[string]any
	if json.Unmarshal(body, &sent) != nil {
		return nil
	}
	return sent
}

func textField(sent map[string]any, name string) string {
	text, _ := sent[name].(string)
	return text
}

// firstUnits is the first n UTF-16 units of text, as a label was always cut.
func firstUnits(text string, n int) string {
	units := utf16.Encode([]rune(text))
	if len(units) <= n {
		return text
	}
	return string(utf16.Decode(units[:n]))
}

// redeem spends a key, counting wrong tries against the caller.
func (s *Server) redeem(r *http.Request, key, kind, want string) (Redeemed, error) {
	who := caller(r)
	if s.attempts.blocked(who) {
		return Redeemed{}, refuse(429, "Too many wrong keys. Wait ten minutes and try again.")
	}
	got, err := s.Store.Redeem(key, kind, want)
	var refused *StoreError
	if errors.As(err, &refused) && refused.Status == 401 {
		s.attempts.failed(who)
	}
	return got, err
}

func (s *Server) deskSession(r *http.Request) (*Session, error) {
	session, err := s.Store.Session(bearer(r), "desk")
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, refuse(401, "This desk is not signed in to the server, or has been replaced by another desk.")
	}
	return session, nil
}

func (s *Server) writer(r *http.Request) (*Session, error) {
	session, err := s.deskSession(r)
	if err != nil {
		return nil, err
	}
	if session.Role != "write" {
		return nil, refuse(403, "Only the treasurer’s desk can do that.")
	}
	return session, nil
}

func (s *Server) reader(r *http.Request) (*Session, error) {
	cookie, err := r.Cookie(readerCookie)
	if err != nil {
		return nil, nil
	}
	return s.Store.Session(cookie.Value, "web")
}

// baseRevision reads X-Base-Revision: a whole number, zero or more.
func baseRevision(r *http.Request) (int64, bool) {
	values, present := r.Header["X-Base-Revision"]
	if !present {
		return 0, false
	}
	text := strings.TrimSpace(strings.Join(values, ", "))
	if text == "" {
		return 0, true
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(n, 0) || n != math.Trunc(n) || n < 0 || n > 1<<53 {
		return 0, false
	}
	return int64(n), true
}

var (
	reportsUnder = regexp.MustCompile(`^/api/v1/reports(/.*)?$`)
	revokeRoute  = regexp.MustCompile(`^/admin/tokens/([^/]+)/revoke$`)
)

// ServeHTTP answers one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")

	defer func() {
		if caught := recover(); caught != nil {
			fail(w, fmt.Errorf("panic: %v", caught))
		}
	}()

	p := rawPath(r)
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	post := r.Method == http.MethodPost

	switch {
	// Cheap, and depends on nothing but this process being up.
	case get && p == "/healthz":
		writeJSON(w, 200, struct {
			OK bool `json:"ok"`
		}{true})

	// Discovery. An app that has been given an address asks here whether it
	// is a Banyan server at all, and what it can do, before spending a key on
	// it. Open to anyone, so it says nothing about the register or who uses it.
	case get && p == "/api/v1/info":
		name := s.cfg.Name
		if name == "" {
			name = "Banyan server"
		}
		type features struct {
			RegisterSync  bool `json:"register_sync"`
			Restore       bool `json:"restore"`
			MemberReports bool `json:"member_reports"`
		}
		writeJSON(w, 200, struct {
			Service  string   `json:"service"`
			Name     string   `json:"name"`
			API      int      `json:"api"`
			Features features `json:"features"`
			SignIn   string   `json:"sign_in"`
		}{"banyan", name, APIVersion, features{true, true, true}, "/login"})

	// The desk exchanges its key for a token. It asks for a write key by
	// name, so a read key typed into a desk by mistake is not used up.
	case post && p == "/api/v1/redeem":
		sent := sentObject(r)
		want := textField(sent, "want")
		if want != "write" && want != "read" {
			want = ""
		}
		got, err := s.redeem(r, textField(sent, "key"), "desk", want)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, got)

	case get && p == "/api/v1/register/status":
		session, err := s.deskSession(r)
		if err != nil {
			fail(w, err)
			return
		}
		s.roleAndLatest(w, session)

	case r.Method == http.MethodPut && p == "/api/v1/register":
		s.putRegister(w, r)

	// Reports for an app connected with a key of either kind. The same
	// figures members see in a browser, and the same short list.
	case get && reportsUnder.MatchString(p):
		if _, err := s.deskSession(r); err != nil {
			fail(w, err)
			return
		}
		inner := "/api/" + strings.TrimPrefix(p, "/api/v1/")
		if !readerMay(inner) {
			notFound(w)
			return
		}
		forwarded := r.Clone(r.Context())
		forwarded.RequestURI = inner
		if r.URL.RawQuery != "" {
			forwarded.RequestURI += "?" + r.URL.RawQuery
		}
		forwarded.Header = http.Header{}
		accept := r.Header.Get("Accept")
		if accept == "" {
			accept = "*/*"
		}
		forwarded.Header.Set("Accept", accept)
		s.desk.ServeHTTP(w, forwarded)

	// Restoring a desk. The whole register leaves the server only for the writer.
	case get && p == "/api/v1/register":
		s.getRegister(w, r)

	// Issuing keys. There is no admin page: these are called with curl.
	case p == "/admin" || strings.HasPrefix(p, "/admin/"):
		s.admin(w, r, p)

	case get && p == "/login":
		session, err := s.reader(r)
		if err != nil {
			fail(w, err)
			return
		}
		if session != nil {
			redirect(w, "/", 302)
			return
		}
		s.page(w, "login.html")

	case post && p == "/login":
		s.signIn(w, r)

	case post && p == "/logout":
		if cookie, err := r.Cookie(readerCookie); err == nil && cookie.Value != "" {
			if err := s.Store.SignOut(cookie.Value); err != nil {
				fail(w, err)
				return
			}
		}
		http.SetCookie(w, &http.Cookie{Name: readerCookie, Value: "", Path: "/", MaxAge: -1})
		redirect(w, "/login", 303)

	case get && p == "/":
		session, err := s.reader(r)
		if err != nil {
			fail(w, err)
			return
		}
		if session == nil {
			redirect(w, "/login", 302)
			return
		}
		s.page(w, "reader.html")

	// When the register the reports are drawn from was last received.
	case get && p == "/api/reader":
		session, err := s.reader(r)
		if err != nil {
			fail(w, err)
			return
		}
		if session == nil {
			writeJSON(w, 401, detail{"Not signed in"})
			return
		}
		s.roleAndLatest(w, session)

	// Styles, scripts and fonts are the same for everyone and hold no data.
	case get && (p == "/static" || strings.HasPrefix(p, "/static/")):
		if strings.Contains(strings.ToLower(p), "leaf-photo") || strings.Contains(strings.ToLower(decoded(p)), "leaf-photo") {
			notFound(w)
			return
		}
		s.desk.ServeHTTP(w, r)

	case get && (p == "/api" || strings.HasPrefix(p, "/api/")):
		session, err := s.reader(r)
		if err != nil {
			fail(w, err)
			return
		}
		if session == nil {
			writeJSON(w, 401, struct {
				Detail string `json:"detail"`
				SignIn bool   `json:"sign_in"`
			}{"Not signed in", true})
			return
		}
		if !readerMay(p) {
			notFound(w)
			return
		}
		s.desk.ServeHTTP(w, r)

	default:
		notFound(w)
	}
}

// decoded is p with %XX undone, however many times it was wrapped.
func decoded(p string) string {
	for i := 0; i < 4; i++ {
		next, err := pathUnescape(p)
		if err != nil || next == p {
			return p
		}
		p = next
	}
	return p
}

func pathUnescape(p string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) {
			if n, err := strconv.ParseUint(p[i+1:i+3], 16, 8); err == nil {
				out.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		out.WriteByte(p[i])
	}
	return out.String(), nil
}

func (s *Server) roleAndLatest(w http.ResponseWriter, session *Session) {
	latest, err := s.Store.Latest()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Role   string    `json:"role"`
		Latest *Revision `json:"latest"`
	}{session.Role, latest})
}

func (s *Server) putRegister(w http.ResponseWriter, r *http.Request) {
	session, err := s.writer(r)
	if err != nil {
		fail(w, err)
		return
	}
	base, ok := baseRevision(r)
	if !ok {
		fail(w, refuse(400, "X-Base-Revision is missing."))
		return
	}
	tooLarge := refuse(413, "That file is too large to be a register.")
	if r.ContentLength > MaxRegisterBytes {
		fail(w, tooLarge)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, MaxRegisterBytes+1))
	if err != nil {
		fail(w, refuse(400, "That is not a register file."))
		return
	}
	if len(data) > MaxRegisterBytes {
		fail(w, tooLarge)
		return
	}
	latest, err := s.Store.PutRegister(data, base, session.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Latest *Revision `json:"latest"`
	}{latest})
}

func (s *Server) getRegister(w http.ResponseWriter, r *http.Request) {
	if _, err := s.writer(r); err != nil {
		fail(w, err)
		return
	}
	file, revision, err := s.Store.LatestFile()
	if err != nil {
		fail(w, err)
		return
	}
	if revision == nil {
		fail(w, refuse(404, "The server has no register yet."))
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		fail(w, err)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "application/vnd.sqlite3")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Revision", strconv.FormatInt(revision.Revision, 10))
	header.Set("X-Sha256", revision.SHA256)
	header.Set("X-Synced-At", revision.SyncedAt)
	header.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(200)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request, p string) {
	if s.cfg.AdminToken == "" {
		notFound(w)
		return
	}
	// Guessing at the admin token is slowed down the same way as guessing at keys.
	who := "admin:" + caller(r)
	if s.attempts.blocked(who) {
		writeJSON(w, 429, detail{"Too many wrong tries. Wait ten minutes."})
		return
	}
	if !same(bearer(r), s.cfg.AdminToken) {
		s.attempts.failed(who)
		writeJSON(w, 401, detail{"Not authorised"})
		return
	}
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	post := r.Method == http.MethodPost
	switch {
	case post && p == "/admin/keys":
		sent := sentObject(r)
		role, _ := sent["role"].(string)
		issued, err := s.Store.IssueKey(role, firstUnits(textField(sent, "label"), 80))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 201, issued)
	case get && p == "/admin/access":
		access, err := s.Store.ListAccess()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, access)
	case post && revokeRoute.MatchString(p):
		revoked := false
		if id, err := strconv.ParseInt(revokeRoute.FindStringSubmatch(p)[1], 10, 64); err == nil {
			if revoked, err = s.Store.Revoke(id); err != nil {
				fail(w, err)
				return
			}
		}
		writeJSON(w, 200, struct {
			Revoked bool `json:"revoked"`
		}{revoked})
	default:
		notFound(w)
	}
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	key := ""
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		if r.ParseMultipartForm(1<<20) == nil {
			key = r.PostFormValue("key")
		}
	} else if r.ParseForm() == nil {
		key = r.PostFormValue("key")
	}
	got, err := s.redeem(r, key, "web", "")
	if err != nil {
		var refused *StoreError
		if !errors.As(err, &refused) {
			fail(w, err)
			return
		}
		why := "invalid"
		switch {
		case refused.Status == 429:
			why = "wait"
		case strings.Contains(refused.Message, "expired"):
			why = "expired"
		}
		redirect(w, "/login?error="+why, 303)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     readerCookie,
		Value:    got.Token,
		Path:     "/",
		MaxAge:   cookieDays * 86400,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	redirect(w, "/", 303)
}
