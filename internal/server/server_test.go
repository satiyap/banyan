package server

// The Banyan server, and a desk syncing with it. Every register here is
// invented: created empty by the desk's own code, with made-up payees.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	banyan "github.com/satiyap/banyan"
	"github.com/satiyap/banyan/internal/connection"
	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/desksync"
)

const adminToken = "test-admin-token-0123456789abcdef0123456789abcdef"

const origin = "https://banyan.test"

type world struct {
	t      *testing.T
	server *Server
	mu     sync.Mutex
	clock  time.Time
}

func (w *world) now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.clock = w.clock.Add(time.Second)
	return w.clock
}

func (w *world) advance(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.clock = w.clock.Add(d)
}

func calPath(t *testing.T) string {
	t.Helper()
	data, err := banyan.Assets.ReadFile("cheque-calibration.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cheque-calibration.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func config(t *testing.T, admin string) Config {
	return Config{
		DataDir:       t.TempDir(),
		Assets:        banyan.Assets,
		CalPath:       calPath(t),
		AdminToken:    admin,
		SecureCookies: true,
		Name:          "Invented Association",
	}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, clock: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	server, err := New(config(t, adminToken), Options{Now: w.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	w.server = server
	return w
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("not JSON: %v: %s", err, r.body)
	}
	return out
}

func (w *world) call(method, path string, headers map[string]string, body []byte) reply {
	w.t.Helper()
	req := httptest.NewRequest(method, origin+path, bytes.NewReader(body))
	req.RequestURI = path
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	w.server.ServeHTTP(rec, req)
	return reply{rec.Code, rec.Header(), rec.Body.Bytes()}
}

func (w *world) get(path string, headers map[string]string) reply {
	return w.call("GET", path, headers, nil)
}

func (w *world) postJSON(path string, body any, headers map[string]string) reply {
	w.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		w.t.Fatal(err)
	}
	all := map[string]string{"content-type": "application/json"}
	for name, value := range headers {
		all[name] = value
	}
	return w.call("POST", path, all, data)
}

func bearerOf(token string) map[string]string {
	return map[string]string{"authorization": "Bearer " + token}
}

func (w *world) issue(role, label string) string {
	w.t.Helper()
	got := w.postJSON("/admin/keys", map[string]string{"role": role, "label": label}, bearerOf(adminToken))
	if got.status != 201 {
		w.t.Fatalf("issuing a key: %d %s", got.status, got.body)
	}
	return got.json(w.t)["key"].(string)
}

func (w *world) deskToken(role string) string {
	w.t.Helper()
	got := w.postJSON("/api/v1/redeem", map[string]string{"key": w.issue(role, "")}, nil)
	if got.status != 200 {
		w.t.Fatalf("redeeming: %d %s", got.status, got.body)
	}
	return got.json(w.t)["token"].(string)
}

func openRegister(t *testing.T, path string) *db.Database {
	t.Helper()
	d, err := db.Open(path, db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func addEntity(t *testing.T, d *db.Database, payee string) int64 {
	t.Helper()
	id, err := d.CreateEntity(payee, db.EntityFields{})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// register is an invented register with the given payees, as bytes.
func register(t *testing.T, payees ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "register.db")
	d := openRegister(t, path)
	for _, payee := range payees {
		addEntity(t, d, payee)
	}
	data, err := desksync.Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (w *world) put(token string, data []byte, base string) reply {
	headers := bearerOf(token)
	headers["x-base-revision"] = base
	return w.call("PUT", "/api/v1/register", headers, data)
}

func revisionOf(t *testing.T, r reply) float64 {
	t.Helper()
	latest, ok := r.json(t)["latest"].(map[string]any)
	if !ok {
		t.Fatalf("no latest in %d %s", r.status, r.body)
	}
	return latest["revision"].(float64)
}

func want(t *testing.T, what string, got, expected any) {
	t.Helper()
	if got != expected {
		t.Errorf("%s: got %v, want %v", what, got, expected)
	}
}

// --------------------------------------------------------------------- keys

func TestIssuingKeysNeedsTheAdminToken(t *testing.T) {
	w := newWorld(t)
	want(t, "no token", w.postJSON("/admin/keys", map[string]string{"role": "read"}, nil).status, 401)
	want(t, "wrong token", w.postJSON("/admin/keys", map[string]string{"role": "read"}, bearerOf("wrong")).status, 401)
	want(t, "bad role", w.postJSON("/admin/keys", map[string]string{"role": "owner"}, bearerOf(adminToken)).status, 400)
	listed := w.get("/admin/access", bearerOf(adminToken))
	want(t, "access", string(listed.body), `{"keys":[],"tokens":[]}`)
}

func TestAKeyWorksOnceHoweverItIsTyped(t *testing.T) {
	w := newWorld(t)
	key := w.issue("write", "treasurer")
	if !regexp.MustCompile(`^([A-Z2-9]{5}-){4}[A-Z2-9]{5}$`).MatchString(key) {
		t.Fatalf("a key of the wrong shape")
	}
	typed := strings.ReplaceAll(strings.ToLower(key), "-", " ")
	first := w.postJSON("/api/v1/redeem", map[string]string{"key": typed}, nil).json(t)
	want(t, "role", first["role"], "write")
	want(t, "label", first["label"], "treasurer")
	want(t, "second use", w.postJSON("/api/v1/redeem", map[string]string{"key": key}, nil).status, 401)
}

func TestAKeyExpiresAfterAWeekUnused(t *testing.T) {
	w := newWorld(t)
	key := w.issue("read", "")
	w.advance(8 * 24 * time.Hour)
	got := w.postJSON("/api/v1/redeem", map[string]string{"key": key}, nil)
	want(t, "status", got.status, 401)
	if !strings.Contains(got.json(t)["detail"].(string), "expired") {
		t.Errorf("detail: %s", got.body)
	}
}

func TestAReadKeyOfferedToADeskIsNotUsedUp(t *testing.T) {
	w := newWorld(t)
	key := w.issue("read", "")
	want(t, "refused", w.postJSON("/api/v1/redeem", map[string]string{"key": key, "want": "write"}, nil).status, 403)
	want(t, "still good", w.postJSON("/api/v1/redeem", map[string]string{"key": key}, nil).status, 200)
}

func TestWrongKeysAreSlowedDown(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 8; i++ {
		want(t, "wrong key", w.postJSON("/api/v1/redeem", map[string]string{"key": "wrong-" + string(rune('0'+i))}, nil).status, 401)
	}
	want(t, "blocked", w.postJSON("/api/v1/redeem", map[string]string{"key": w.issue("read", "")}, nil).status, 429)
}

func TestAShortAdminTokenIsRefusedAndWrongOnesAreSlowedDown(t *testing.T) {
	w := newWorld(t)
	if _, err := New(config(t, "short"), Options{}); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Errorf("a short admin token was accepted: %v", err)
	}
	for i := 0; i < 8; i++ {
		want(t, "guess", w.get("/admin/access", bearerOf("guess-"+string(rune('0'+i)))).status, 401)
	}
	// Even the right token waits once the address has guessed too often.
	want(t, "blocked", w.get("/admin/access", bearerOf(adminToken)).status, 429)
}

func TestTheAdminRoutesDoNotExistWithoutAnAdminToken(t *testing.T) {
	bare, err := New(config(t, ""), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	req := httptest.NewRequest("POST", origin+"/admin/keys", strings.NewReader(`{"role":"write"}`))
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	bare.ServeHTTP(rec, req)
	want(t, "status", rec.Code, 404)
}

// ----------------------------------------------------------------- register

func TestTheRegisterIsNumberedAndADeskThatHasFallenBehindIsRefused(t *testing.T) {
	w := newWorld(t)
	token := w.deskToken("write")
	want(t, "status", string(w.get("/api/v1/register/status", bearerOf(token)).body), `{"role":"write","latest":null}`)

	one := register(t, "Invented Electricals")
	want(t, "first", revisionOf(t, w.put(token, one, "0")), 1.0)
	// The same register again is not a new copy.
	want(t, "again", revisionOf(t, w.put(token, one, "1")), 1.0)

	two := register(t, "Invented Electricals", "Made-up Plumbing")
	want(t, "second", revisionOf(t, w.put(token, two, "1")), 2.0)

	// Built on revision 1, which is no longer the latest.
	stale := w.put(token, register(t, "Somebody Else"), "1")
	want(t, "stale status", stale.status, 409)
	want(t, "stale latest", revisionOf(t, stale), 2.0)

	back := w.get("/api/v1/register", bearerOf(token))
	want(t, "x-revision", back.header.Get("X-Revision"), "2")
	if !bytes.Equal(back.body, two) {
		t.Errorf("the register came back different")
	}
	want(t, "x-sha256", back.header.Get("X-Sha256"), sha(two))
}

func TestOnlyAnIntactChequeRegisterIsAccepted(t *testing.T) {
	w := newWorld(t)
	token := w.deskToken("write")
	want(t, "not a database", w.put(token, bytes.Repeat([]byte("not a database at all"), 10), "0").status, 400)
	damaged := register(t, "Invented Electricals")
	for i := 100; i < 4000 && i < len(damaged); i++ {
		damaged[i] = 0xff
	}
	want(t, "damaged", w.put(token, damaged, "0").status, 400)
	want(t, "nothing kept", w.get("/api/v1/register", bearerOf(token)).status, 404)
	// A database that is not a register.
	want(t, "missing header", w.call("PUT", "/api/v1/register", bearerOf(token), register(t)).status, 400)
	left, _ := filepath.Glob(filepath.Join(w.server.cfg.DataDir, "registers", "*"))
	if len(left) != 0 {
		t.Errorf("refused uploads were left behind: %v", left)
	}
}

func TestAReadTokenCanNeitherSendNorFetchTheRegister(t *testing.T) {
	w := newWorld(t)
	writer := w.deskToken("write")
	w.put(writer, register(t, "Invented Electricals"), "0")
	reader := w.deskToken("read")
	want(t, "put", w.put(reader, register(t, "Other"), "1").status, 403)
	want(t, "get", w.get("/api/v1/register", bearerOf(reader)).status, 403)
	want(t, "anonymous", w.get("/api/v1/register", nil).status, 401)
}

func TestConnectingANewDeskRetiresTheOneBeforeIt(t *testing.T) {
	w := newWorld(t)
	old := w.deskToken("write")
	taken := w.deskToken("write")
	want(t, "old", w.get("/api/v1/register/status", bearerOf(old)).status, 401)
	want(t, "new", w.get("/api/v1/register/status", bearerOf(taken)).status, 200)
}

// ------------------------------------------------------------------ members

type signedIn struct {
	status   int
	location string
	cookie   string
}

func (w *world) signIn(key string) signedIn {
	got := w.call("POST", "/login", map[string]string{"content-type": "application/x-www-form-urlencoded"},
		[]byte(url.Values{"key": {key}}.Encode()))
	return signedIn{got.status, got.header.Get("Location"), strings.Split(got.header.Get("Set-Cookie"), ";")[0]}
}

func TestMembersSignInWithAKeyAndSeeReportsAndNothingElse(t *testing.T) {
	w := newWorld(t)
	stranger := w.get("/", nil)
	want(t, "stranger", stranger.status, 302)
	want(t, "stranger goes to", stranger.header.Get("Location"), "/login")
	want(t, "reports, unsigned", w.get("/api/reports", nil).status, 401)
	if !strings.Contains(string(w.get("/login", nil).body), "Banyan reports") {
		t.Errorf("the sign-in page is not the sign-in page")
	}

	want(t, "bad key", w.signIn("no-such-key"), signedIn{303, "/login?error=invalid", ""})

	session := w.signIn(w.issue("read", "flat 12"))
	want(t, "sign-in status", session.status, 303)
	want(t, "sign-in goes to", session.location, "/")
	if !regexp.MustCompile(`^banyan_reader=[0-9a-f]{64}$`).MatchString(session.cookie) {
		t.Fatalf("cookie: %q", session.cookie)
	}
	cookie := map[string]string{"cookie": session.cookie}

	home := string(w.get("/", cookie).body)
	if !strings.Contains(home, "/static/js/reader-boot.js") || strings.Contains(home, "desk.js") {
		t.Errorf("the members' page is not the reader")
	}
	version := regexp.MustCompile(`name="desk-version" content="([^"]+)"`).FindStringSubmatch(home)[1]
	page := map[string]string{"cookie": session.cookie, "x-desk-version": version}

	want(t, "reports", w.get("/api/reports", page).status, 200)
	want(t, "monthly", w.get("/api/reports/monthly", page).status, 200)
	want(t, "csv", w.get("/api/reports/entities.csv", cookie).status, 200)
	want(t, "reader", string(w.get("/api/reader", cookie).body), `{"role":"read","latest":null}`)
	want(t, "a stylesheet", w.get("/static/reader.css", nil).status, 200)

	// The rest of the desk is not there for a member, whatever the method.
	for _, path := range []string{"/api/health", "/api/cheques", "/api/desk", "/api/entities", "/api/reports/monthly.pdf", "/api/connection", "/setup"} {
		want(t, path, w.get(path, page).status, 404)
	}
	want(t, "writing", w.postJSON("/api/entities", map[string]string{"payee_name": "X"}, page).status, 404)
	want(t, "cookie as bearer", w.get("/api/v1/register", bearerOf(strings.Split(session.cookie, "=")[1])).status, 401)

	// The cheque photo is never served, however it is asked for.
	for _, path := range []string{"/static/leaf-photo.jpg", "/static/leaf%2Dphoto.jpg", "/static/LEAF-PHOTO.JPG", "/static/%6ceaf-photo.jpg", "/static/x/../leaf-photo.jpg"} {
		got := w.get(path, page)
		if got.status != 404 || bytes.HasPrefix(got.body, []byte{0xff, 0xd8}) {
			t.Errorf("%s: %d", path, got.status)
		}
	}

	w.call("POST", "/logout", cookie, nil)
	want(t, "after sign-out", w.get("/api/reports", page).status, 401)
}

func TestEveryAnswerCarriesTheSafetyHeadersAndTheCookieIsLocked(t *testing.T) {
	w := newWorld(t)
	for _, path := range []string{"/healthz", "/login", "/static/reader.css", "/nowhere", "/api/reports"} {
		got := w.get(path, nil)
		want(t, path+" nosniff", got.header.Get("X-Content-Type-Options"), "nosniff")
		want(t, path+" frame", got.header.Get("X-Frame-Options"), "DENY")
		want(t, path+" referrer", got.header.Get("Referrer-Policy"), "no-referrer")
	}
	got := w.call("POST", "/login", map[string]string{"content-type": "application/x-www-form-urlencoded"},
		[]byte(url.Values{"key": {w.issue("read", "")}}.Encode()))
	set := got.header.Get("Set-Cookie")
	for _, part := range []string{"HttpOnly", "Secure", "SameSite=Lax", "Path=/", "Max-Age=15552000"} {
		if !strings.Contains(set, part) {
			t.Errorf("the cookie lacks %s: %s", part, set)
		}
	}
}

func TestTheReportsAreDrawnFromTheLatestRegisterReceived(t *testing.T) {
	w := newWorld(t)
	cookie := map[string]string{"cookie": w.signIn(w.issue("read", "")).cookie}
	token := w.deskToken("write")
	path := filepath.Join(t.TempDir(), "register.db")
	d := openRegister(t, path)
	entity := addEntity(t, d, "Invented Electricals")
	if _, err := d.AddCheque(db.NewCheque{
		db.F("cheque_no", "000123"),
		db.F("payee", "Invented Electricals"),
		db.F("entity_id", entity),
		db.F("amount_paise", 1234500),
		db.F("amount_words", "Twelve Thousand Three Hundred Forty Five Only"),
		db.F("cheque_date", "2026-10-05"),
		db.F("memo", ""),
		db.F("status", "logged"),
		db.F("cal_version", 1),
	}); err != nil {
		t.Fatal(err)
	}
	data, err := desksync.Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	want(t, "put", revisionOf(t, w.put(token, data, "0")), 1.0)

	report := w.get("/api/reports?fy=2026", cookie)
	if report.status != 200 || !strings.Contains(string(report.body), "Invented Electricals") {
		t.Errorf("the report does not show the register just sent: %d", report.status)
	}
	want(t, "reader", revisionOf(t, w.get("/api/reader", cookie)), 1.0)

	// And after a restart on the same folder.
	cfg := w.server.cfg
	w.server.Close()
	again, err := New(cfg, Options{Now: w.now})
	if err != nil {
		t.Fatal(err)
	}
	w.server = again
	t.Cleanup(again.Close)
	report = w.get("/api/reports?fy=2026", cookie)
	if report.status != 200 || !strings.Contains(string(report.body), "Invented Electricals") {
		t.Errorf("after a restart the report lost the register: %d", report.status)
	}
	want(t, "desk token after restart", w.get("/api/v1/register/status", bearerOf(token)).status, 200)
}

// --------------------------------------------------------------- desk sync

func TestAnyoneMayAskWhatTheServerIs(t *testing.T) {
	w := newWorld(t)
	want(t, "info", string(w.get("/api/v1/info", nil).body),
		`{"service":"banyan","name":"Invented Association","api":1,"features":{"register_sync":true,"restore":true,"member_reports":true},"sign_in":"/login"}`)
}

// through sends a desk's requests straight into the server.
type through struct{ w *world }

func (r through) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	r.w.server.ServeHTTP(rec, req)
	return rec.Result(), nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type testDesk struct {
	paths desksync.SyncPaths
	db    *db.Database
	sync  *desksync.Sync
}

func (w *world) desk() *testDesk {
	dir := w.t.TempDir()
	paths := desksync.SyncPaths{DbPath: filepath.Join(dir, "banyan.db"), ConnectionPath: filepath.Join(dir, "connection.json")}
	return &testDesk{paths, openRegister(w.t, paths.DbPath), w.syncOver(paths)}
}

func (w *world) syncOver(paths desksync.SyncPaths) *desksync.Sync {
	s := desksync.NewSync(paths, desksync.Options{Transport: through{w}, Now: w.now})
	w.t.Cleanup(s.Close)
	return s
}

func (w *world) latest() int64 {
	w.t.Helper()
	latest, err := w.server.Store.Latest()
	if err != nil {
		w.t.Fatal(err)
	}
	if latest == nil {
		return 0
	}
	return latest.Revision
}

func rev(status desksync.SyncStatus) int64 {
	if status.Revision == nil {
		return -1
	}
	return *status.Revision
}

func TestADeskChecksTheAddressIsABanyanServerBeforeSpendingAKey(t *testing.T) {
	w := newWorld(t)
	a := w.desk()
	found, err := a.sync.Discover(origin + "/")
	if err != nil || found.Name != "Invented Association" || found.ApiUrl != origin {
		t.Fatalf("discover: %+v %v", found, err)
	}

	// Somewhere that answers, but is not a Banyan server: the key never leaves the desk.
	key := w.issue("write", "")
	var sent []string
	elsewhere := desksync.NewSync(a.paths, desksync.Options{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		sent = append(sent, req.Method+" "+req.URL.String())
		rec := httptest.NewRecorder()
		_, _ = io.WriteString(rec, "<html>hello</html>")
		return rec.Result(), nil
	})})
	if _, err := elsewhere.Connect("https://example.org", key); err == nil || !strings.Contains(err.Error(), "no Banyan server at that address") {
		t.Errorf("connected to something that is not a Banyan server: %v", err)
	}
	if len(sent) != 1 || sent[0] != "GET https://example.org/api/v1/info" {
		t.Errorf("sent %v", sent)
	}

	// The key was never used up, and the name the server gave is remembered.
	if _, err := a.sync.Connect(origin, key); err != nil {
		t.Fatal(err)
	}
	want(t, "server name", connection.ReadConnection(a.paths.ConnectionPath).ServerName, "Invented Association")
}

func TestADeskSendsItsRegisterAfterConnectingAndAfterEachChange(t *testing.T) {
	w := newWorld(t)
	a := w.desk()
	want(t, "connected at first", a.sync.Status().Connected, false)
	a.sync.Flush() // not connected: nothing happens
	want(t, "nothing sent", w.latest(), int64(0))

	status, err := a.sync.Connect(origin, w.issue("write", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !status.Connected || rev(status) != 1 || status.Pending || status.Error != "" || status.Choice != nil {
		t.Fatalf("after connecting: %+v", status)
	}
	// The key is spent; what is kept is the token.
	saved, _ := os.ReadFile(a.paths.ConnectionPath)
	if strings.Contains(string(saved), "user_key") || !strings.Contains(string(saved), origin) {
		t.Errorf("the connection file is not as expected")
	}

	a.sync.Flush() // unchanged: no new copy
	want(t, "unchanged", w.latest(), int64(1))

	addEntity(t, a.db, "Invented Electricals")
	want(t, "after a change", rev(a.sync.Flush()), int64(2))
}

func TestAWrongKeyConnectsNothing(t *testing.T) {
	w := newWorld(t)
	a := w.desk()
	if _, err := a.sync.Connect(origin, "WRONG-WRONG"); err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Errorf("a wrong key connected: %v", err)
	}
	want(t, "status", connection.ReadConnection(a.paths.ConnectionPath).Status, "unset")
}

func TestAReadKeyMakesTheAppReportsOnly(t *testing.T) {
	w := newWorld(t)
	// The treasurer's desk has sent a register.
	treasurer := w.desk()
	if _, err := treasurer.sync.Connect(origin, w.issue("write", "")); err != nil {
		t.Fatal(err)
	}
	addEntity(t, treasurer.db, "Invented Electricals")
	treasurer.sync.Flush()
	before := w.latest()

	a := w.desk()
	addEntity(t, a.db, "On The Reader Mac Only")
	status, err := a.sync.Connect(origin, w.issue("read", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !status.Connected || !status.ReadOnly || status.Pending || status.Choice != nil || status.Revision != nil {
		t.Fatalf("a reader's status: %+v", status)
	}
	want(t, "read only", a.sync.ReadOnly(), true)

	// Nothing it does reaches the server's register, and the treasurer is not retired.
	a.sync.Schedule()
	a.sync.Flush()
	a.sync.Overwrite()
	want(t, "server unchanged", w.latest(), before)
	if _, err := a.sync.FetchForRestore(); err == nil {
		t.Errorf("a reader fetched the register")
	}
	want(t, "treasurer retired", treasurer.sync.Flush().Retired, false)

	// It can read the reports, and only the reports.
	for path, expected := range map[string]int{
		"/api/v1/reports":              200,
		"/api/v1/reports/monthly":      200,
		"/api/v1/reports/entities.csv": 200,
		"/api/v1/reports/monthly.pdf":  404,
		"/api/v1/register":             403,
	} {
		got, err := a.sync.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		got.Body.Close()
		want(t, path, got.StatusCode, expected)
	}
	want(t, "reports, unsigned", w.get("/api/v1/reports", nil).status, 401)
}

func TestHandingOver(t *testing.T) {
	w := newWorld(t)
	a := w.desk()
	if _, err := a.sync.Connect(origin, w.issue("write", "")); err != nil {
		t.Fatal(err)
	}
	addEntity(t, a.db, "Invented Electricals")
	a.sync.Flush()

	// A second Mac with a fresh, different register.
	b := w.desk()
	status, err := b.sync.Connect(origin, w.issue("write", ""))
	if err != nil {
		t.Fatal(err)
	}
	// It does not send and does not fetch: the treasurer is asked.
	if status.Choice == nil || status.Choice.Revision != 2 {
		t.Fatalf("the new desk was not asked to choose: %+v", status)
	}
	want(t, "server untouched", w.latest(), int64(2))

	// The old desk can no longer write to the server.
	addEntity(t, a.db, "Written After Handover")
	after := a.sync.Flush()
	if !after.Retired || !after.Pending {
		t.Errorf("the old desk was not retired: %+v", after)
	}
	want(t, "server untouched by the old desk", w.latest(), int64(2))

	// The treasurer chooses the server's register. It goes in at the next start.
	if _, err := b.sync.FetchForRestore(); err != nil {
		t.Fatal(err)
	}
	applied, err := desksync.ApplyPendingRestore(b.paths, w.now)
	if err != nil || !applied {
		t.Fatalf("restore: %v %v", applied, err)
	}
	restored := openRegister(t, b.paths.DbPath)
	entities, err := restored.ListEntities()
	if err != nil || len(entities) != 1 || entities[0].PayeeName != "Invented Electricals" {
		t.Fatalf("the restored register: %+v %v", entities, err)
	}

	settled := w.syncOver(b.paths).Flush()
	if settled.Choice != nil || settled.Error != "" || settled.Pending {
		t.Errorf("after restoring: %+v", settled)
	}
	addEntity(t, restored, "Made-up Plumbing")
	next := w.syncOver(b.paths).Flush()
	if rev(next) <= 2 || next.Choice != nil {
		t.Errorf("the restored desk could not carry on: %+v", next)
	}
}

func TestTheTreasurerCanChooseThisDesksRegister(t *testing.T) {
	w := newWorld(t)
	a := w.desk()
	if _, err := a.sync.Connect(origin, w.issue("write", "")); err != nil {
		t.Fatal(err)
	}
	b := w.desk()
	addEntity(t, b.db, "The Right One")
	status, err := b.sync.Connect(origin, w.issue("write", ""))
	if err != nil || status.Choice == nil {
		t.Fatalf("no choice offered: %+v %v", status, err)
	}
	after := b.sync.Overwrite()
	if after.Choice != nil || after.Pending || rev(after) != 2 {
		t.Errorf("after overwriting: %+v", after)
	}
}

// ------------------------------------------------- the server it replaces

// A data folder written by the TypeScript server must keep working: the same
// tables, the same hashes, the same file names. This builds one by hand in
// that server's exact shapes and starts the Go server on it.
func TestADataFolderFromTheServerItReplacesKeepsWorking(t *testing.T) {
	w := newWorld(t)
	dir := t.TempDir()
	store, err := OpenStore(dir, w.now)
	if err != nil {
		t.Fatal(err)
	}
	// What the old server stored: sha256 of the token, and of the key with its dashes dropped.
	deskTok := strings.Repeat("ab", 32)
	webTok := strings.Repeat("cd", 32)
	key := "ABCDE-FGHJK-MNPQR-STUVW-XYZ23"
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO tokens (hash, role, kind, label, created_at) VALUES (?, 'write', 'desk', 'treasurer', '2026-11-12T16:40:00.000Z')", []any{sha([]byte(deskTok))}},
		{"INSERT INTO tokens (hash, role, kind, label, created_at) VALUES (?, 'read', 'web', 'flat 12', '2026-11-12T16:41:00.000Z')", []any{sha([]byte(webTok))}},
		{"INSERT INTO keys (hash, role, label, created_at, expires_at) VALUES (?, 'read', '', '2026-11-12T09:00:00.000Z', '2026-11-19T09:00:00.000Z')", []any{sha([]byte("ABCDEFGHJKMNPQRSTUVWXYZ23"))}},
	} {
		if _, err := store.db.Exec(stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	data := register(t, "Invented Electricals")
	if _, err := store.db.Exec("INSERT INTO revisions (sha256, bytes, synced_at, token_id) VALUES (?, ?, '2026-11-12T16:45:00.000Z', 1)", sha(data), len(data)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registers", "register-00000001.db"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	store.Close()

	cfg := config(t, adminToken)
	cfg.DataDir = dir
	server, err := New(cfg, Options{Now: w.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	w.server = server

	want(t, "the desk's token", string(w.get("/api/v1/register/status", bearerOf(deskTok)).body),
		`{"role":"write","latest":{"revision":1,"sha256":"`+sha(data)+`","bytes":`+itoa(len(data))+`,"synced_at":"2026-11-12T16:45:00.000Z"}}`)
	cookie := map[string]string{"cookie": "banyan_reader=" + webTok}
	want(t, "a member's cookie, reports", w.get("/api/reports", cookie).status, 200)
	want(t, "a member's cookie, register seen", revisionOf(t, w.get("/api/reader", cookie)), 1.0)
	want(t, "an unspent key", w.signIn(strings.ToLower(key)).status, 303)
	back := w.get("/api/v1/register", bearerOf(deskTok))
	if !bytes.Equal(back.body, data) {
		t.Errorf("the register came back different")
	}
	want(t, "the next copy is numbered on", revisionOf(t, w.put(deskTok, register(t, "Invented Electricals", "Made-up Plumbing"), "1")), 2.0)
}

func itoa(n int) string {
	data, _ := json.Marshal(n)
	return string(data)
}
