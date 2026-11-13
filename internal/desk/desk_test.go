package desk

// The HTTP layer against its recorded answers.
//
// tests/golden/http.json holds scripted sessions: every request with the
// response the desk gave. Here each session is replayed against the server, on
// a fresh temporary register, with the same frozen date, the same stepping
// clock and the same time zone, and every response must come out the same:
// status, content type, download name, JSON (values and key order), CSV text.
//
// Nothing here can reach a printer: PATH is an empty folder for the whole test
// process, so lp, lpstat, ippfind and cancel cannot be found; printing is
// switched off in the printing package; and the function that runs a
// print-system command is replaced by one that panics.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/printing"
	"github.com/satiyap/banyan/internal/render"
	"github.com/satiyap/banyan/internal/webpdf"
)

const root = "../.."

var (
	fixture  obj
	work     string
	emptyBin string
	// How many recorded requests the sessions replayed, all told.
	replayedTotal atomic.Int64
)

// WebKit works on the main thread, so the tests run beside it.
func init() { runtime.LockOSThread() }

// ------------------------------------------------------------ safety setup
// Before any request: no print system can be found or run, and the time zone
// is the one the sessions were recorded in (the register's timestamps are read
// as local dates).
func TestMain(m *testing.M) {
	text, err := os.ReadFile(filepath.Join(root, "tests", "golden", "http.json"))
	if err != nil {
		panic(err)
	}
	parsed, err := parseJSON(text)
	if err != nil {
		panic(err)
	}
	fixture = parsed.(obj)

	if work, err = os.MkdirTemp("", "banyan-http-test-"); err != nil {
		panic(err)
	}
	emptyBin = filepath.Join(work, "empty-bin")
	if err := os.Mkdir(emptyBin, 0o755); err != nil {
		panic(err)
	}
	os.Setenv("PATH", emptyBin)
	os.Unsetenv(printing.DefaultPrinterEnv)
	zone, err := time.LoadLocation(fixture.text("tz"))
	if err != nil {
		panic(err)
	}
	os.Setenv("TZ", fixture.text("tz"))
	time.Local = zone
	printing.SetPrintingDisabled(true)
	printing.Run = func(cmd string, args []string, timeout time.Duration) (printing.Completed, error) {
		panic("a test tried to run the print-system command " + cmd)
	}

	done := make(chan struct{})
	code := 0
	go func() {
		code = m.Run()
		close(done)
	}()
	webpdf.ServeMainThread(done)
	os.RemoveAll(work)
	os.Exit(code)
}

// -- reading the fixture ------------------------------------------------------

func (o obj) text(key string) string {
	s, _ := o.value(key).(string)
	return s
}

func (o obj) value(key string) any {
	v, _ := o.get(key)
	return v
}

func (o obj) has(key string) bool {
	_, found := o.get(key)
	return found
}

func (o obj) object(key string) obj {
	v, _ := o.value(key).(obj)
	return v
}

func (o obj) items(key string) []any {
	v, _ := o.value(key).([]any)
	return v
}

func stringify(t testing.TB, v any) string {
	t.Helper()
	text, err := encodeJSON(v)
	if err != nil {
		t.Fatalf("cannot write %v as JSON: %v", v, err)
	}
	return string(text)
}

func parse(t testing.TB, text string) any {
	t.Helper()
	v, err := parseJSON([]byte(text))
	if err != nil {
		t.Fatalf("not JSON: %v: %.200s", err, text)
	}
	return v
}

// The Python refuses a leaf with "CUPS is not available on this machine". Here
// the same request is refused one line earlier, by the switch this test turns
// on as a second safeguard, and says so in its own words.
const recordedRefusal = "CUPS is not available on this machine"
const switchedOff = "printing is switched off in this build, so nothing was sent to the printer"

const wrongVersion = "feedfacecafe"
const fakePDF = "%PDF-1.4\n% an invented placeholder, not a cheque\n%%EOF\n"

func configFor(t testing.TB, folder string) Config {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(folder, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	calPath := filepath.Join(folder, fixture.text("cal_name"))
	copyFile(t, filepath.Join(root, "cheque-calibration.json"), calPath)
	return Config{
		DBPath:         filepath.Join(folder, "register.db"),
		CalPath:        calPath,
		PDFDir:         filepath.Join(folder, "pdf"),
		Assets:         os.DirFS(root),
		DataStaticDir:  filepath.Join(folder, "static"),
		ConnectionPath: filepath.Join(folder, "connection.json"),
		PrintDisabled:  true,
	}
}

func copyFile(t testing.TB, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newApp(t testing.TB, cfg Config, opts Options) *App {
	t.Helper()
	app, err := New(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// steppingClock starts at the recorded moment and steps one second per
// reading, as the recording's did.
func steppingClock(t testing.TB) func() time.Time {
	base, err := time.Parse(time.RFC3339, fixture.text("clock_base"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	return func() time.Time {
		return base.Add(time.Duration(calls.Add(1)-1) * time.Second).UTC()
	}
}

// send makes one request of the app, exactly as written.
func send(app *App, method, path string, headers map[string]string, body *string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(*body)
	}
	request := httptest.NewRequest(method, path, reader)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	return recorder
}

func get(app *App, path string, headers ...string) *httptest.ResponseRecorder {
	h := map[string]string{}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return send(app, "GET", path, h, nil)
}

func deskVersionOf(t testing.TB, app *App) string {
	t.Helper()
	res := get(app, "/api/health")
	version := parse(t, res.Body.String()).(obj).text("desk_version")
	if version != app.Version() {
		t.Fatalf("the health check says %q, the app %q", version, app.Version())
	}
	return version
}

// sub is a placeholder in the recording and this run's value for it. A
// verbatim one is JSON text swapped for JSON text; any other is a piece of a string.
type sub struct {
	from, to string
	verbatim bool
}

// fill puts this run's values in for a recorded value's placeholders.
func fill(t testing.TB, value any, subs []sub) any {
	t.Helper()
	text := stringify(t, value)
	for _, s := range subs {
		to := s.to
		if !s.verbatim {
			escaped := stringify(t, s.to)
			to = escaped[1 : len(escaped)-1]
		}
		text = strings.ReplaceAll(text, s.from, to)
	}
	return parse(t, text)
}

// drop removes a dotted key from a value.
func drop(value any, dotted string) any {
	o, isObject := value.(obj)
	if !isObject {
		return value
	}
	key, rest, nested := strings.Cut(dotted, ".")
	out := obj{}
	for _, p := range o {
		switch {
		case p.K != key:
			out = append(out, p)
		case nested:
			out = append(out, kv{p.K, drop(p.V, rest)})
		}
	}
	return out
}

func decode[T any](t testing.TB, value any) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(stringify(t, value)), &out); err != nil {
		t.Fatalf("setup value %s: %v", stringify(t, value), err)
	}
	return out
}

// setupDB makes one register call the recording made between requests.
func setupDB(t testing.TB, d *db.Database, method string, args []any, kwargs obj) (any, error) {
	t.Helper()
	switch method {
	case "add_cheque":
		return d.AddCheque(decode[db.NewCheque](t, kwargs))
	case "update_cheque":
		columns := decode[db.Fields](t, kwargs)
		changes := db.ChequeChanges{}
		if v, found := columns.Get("event"); found {
			changes.Event, _ = v.(string)
			columns.Delete("event")
		}
		if v, found := columns.Get("detail"); found {
			changes.Detail, _ = v.(string)
			columns.Delete("detail")
		}
		changes.Columns = columns
		return nil, d.UpdateCheque(decode[int64](t, args[0]), changes)
	case "create_batch":
		return d.CreateBatch(decode[[]db.BatchItem](t, args[0]), decode[string](t, args[1]), decode[string](t, args[2]))
	case "move_batch":
		var from []string
		if one, isText := args[1].(string); isText {
			from = []string{one}
		} else {
			from = decode[[]string](t, args[1])
		}
		return d.MoveBatch(decode[int64](t, args[0]), from, decode[string](t, args[2]), decode[db.BatchChanges](t, kwargs))
	case "save_batch_items":
		return nil, d.SaveBatchItems(decode[int64](t, args[0]), decode[[]db.BatchItem](t, args[1]), decode[string](t, args[2]))
	}
	t.Fatalf("the replay does not know the register call %s", method)
	return nil, nil
}

// writeCal rewrites the calibration file and makes sure its modification time moved.
func writeCal(t testing.TB, path, text string) {
	t.Helper()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	for bump := 1; ; bump++ {
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !after.ModTime().Equal(before.ModTime()) {
			return
		}
		moved := before.ModTime().Add(time.Duration(bump) * time.Second)
		if err := os.Chtimes(path, moved, moved); err != nil {
			t.Fatal(err)
		}
	}
}

func isJSONInvalid(expected any) bool {
	o, _ := expected.(obj)
	detail := o.items("detail")
	if len(detail) != 1 {
		return false
	}
	first, _ := detail[0].(obj)
	return first.text("type") == "json_invalid"
}

// firstDifference is both strings from a little before where they part, for a readable failure.
func firstDifference(got, expected string) string {
	i := 0
	for i < len(got) && i < len(expected) && got[i] == expected[i] {
		i++
	}
	from := max(0, i-80)
	clip := func(s string) string { return s[min(from, len(s)):min(i+160, len(s))] }
	return "…" + clip(got) + "\n    recorded …" + clip(expected)
}

var originRE = regexp.MustCompile(`^http://[^/]+`)

// Chrome made the recording's PDFs and WebKit makes this desk's. Each lays the
// same 92 x 204mm page out to within a fraction of a point, and the size read
// back from the PDF differs in the second decimal. What the recording says
// Chrome measured is replaced by what this desk's renderer measures.
const recordedPageW, recordedPageH = 92.12, 203.88

func measuredPage(t testing.TB, cfg Config) (w, h float64) {
	t.Helper()
	app := newApp(t, cfg, Options{})
	cal, err := app.calibration()
	if err != nil {
		t.Fatal(err)
	}
	renderer := &render.Renderer{Assets: cfg.Assets}
	result, err := renderer.RenderPDF(cal, chequePayload{TestPattern: true}, filepath.Join(cfg.PDFDir, "measure.pdf"), false)
	if err != nil {
		t.Fatal(err)
	}
	dict := result.AsDict()
	return dict.PageWMM, dict.PageHMM
}

func replay(t *testing.T, session obj) (replayed int, failures []string) {
	folder := filepath.Join(work, "sessions", session.text("name"))
	cfg := configFor(t, folder)
	calGood, err := os.ReadFile(cfg.CalPath)
	if err != nil {
		t.Fatal(err)
	}
	now := steppingClock(t)
	app := newApp(t, cfg, Options{Today: func() string { return fixture.text("today") }, Now: now})
	defer app.Close()
	// Shares the app's clock, as the setup calls did when the session was recorded.
	register, err := db.Open(cfg.DBPath, db.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	version := deskVersionOf(t, app)
	subs := []sub{
		{from: "{PDF_DIR}", to: cfg.PDFDir},
		{from: "{DESK_VERSION}", to: version},
		{from: recordedRefusal, to: switchedOff},
	}
	if session.value("needs_renderer") == true {
		w, h := measuredPage(t, configFor(t, filepath.Join(folder, "measure")))
		subs = append(subs,
			sub{
				from:     fmt.Sprintf(`"page_w_mm":%s,"page_h_mm":%s`, stringify(t, recordedPageW), stringify(t, recordedPageH)),
				to:       fmt.Sprintf(`"page_w_mm":%s,"page_h_mm":%s`, stringify(t, w), stringify(t, h)),
				verbatim: true,
			},
			sub{
				from: fixed(recordedPageW, 1) + "x" + fixed(recordedPageH, 1) + "mm",
				to:   fixed(w, 1) + "x" + fixed(h, 1) + "mm",
			},
		)
	}

	for index, raw := range session.items("steps") {
		step := raw.(obj)
		if step.has("setup") {
			switch step.text("setup") {
			case "db":
				args, _ := fill(t, step.value("args"), subs).([]any)
				kwargs, _ := fill(t, step.value("kwargs"), subs).(obj)
				result, err := setupDB(t, register, step.text("method"), args, kwargs)
				if err != nil {
					failures = append(failures, fmt.Sprintf("#%d setup %s: %v", index, step.text("method"), err))
				} else if got, want := stringify(t, result), stringify(t, step.value("result")); got != want {
					failures = append(failures, fmt.Sprintf("#%d setup %s: returned %s, recorded %s", index, step.text("method"), got, want))
				}
			case "write_cal":
				text := string(calGood)
				if given, isText := step.value("text").(string); isText {
					text = given
				}
				writeCal(t, cfg.CalPath, text)
			default:
				if err := os.WriteFile(filepath.Join(cfg.PDFDir, step.text("name")), []byte(fakePDF), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			continue
		}

		request, want := step.object("request"), step.object("response")
		headers := map[string]string{}
		for _, h := range request.object("headers") {
			headers[h.K], _ = h.V.(string)
		}
		switch request.text("version") {
		case "ok":
			headers["X-Desk-Version"] = version
		case "wrong":
			headers["X-Desk-Version"] = wrongVersion
		}
		var body *string
		if request.has("raw") {
			text := request.text("raw")
			body = &text
		} else if request.has("body") {
			text := stringify(t, fill(t, request.value("body"), subs))
			body = &text
		}
		if body != nil && request.text("content_type") != "" {
			headers["Content-Type"] = request.text("content_type")
		}

		res := send(app, request.text("method"), request.text("path"), headers, body)
		replayed++
		label := fmt.Sprintf("#%d %s %s", index, request.text("method"), request.text("path"))
		if request.has("body") {
			label += " " + fmt.Sprintf("%.120s", stringify(t, request.value("body")))
		}
		fail := func(format string, args ...any) {
			failures = append(failures, label+"\n    "+fmt.Sprintf(format, args...))
		}

		if wantStatus := int(want.value("status").(float64)); res.Code != wantStatus {
			fail("status %d, recorded %d: %.300s", res.Code, wantStatus, res.Body.String())
			continue
		}
		if kind := res.Header().Get("Content-Type"); kind != want.text("content_type") {
			fail("content-type %s, recorded %s", kind, want.text("content_type"))
		}
		if disposition := res.Header().Get("Content-Disposition"); disposition != want.text("content_disposition") {
			fail("content-disposition %s, recorded %s", disposition, want.text("content_disposition"))
		}
		if location := originRE.ReplaceAllString(res.Header().Get("Location"), ""); location != want.text("location") {
			fail("redirected to %s, recorded %s", location, want.text("location"))
		}

		got := res.Body.String()
		switch {
		case want.value("empty") == true:
			if len(got) != 0 {
				fail("a body of %d bytes where the recording has none", len(got))
			}
		case want.has("json"):
			expected := fill(t, want.value("json"), subs)
			answered, err := parseJSON([]byte(got))
			if err != nil {
				fail("the body is not JSON: %v: %.200s", err, got)
				continue
			}
			// The body is written as JSON.stringify writes it: nothing escaped
			// that JavaScript leaves alone, no number in another form.
			if canonical := stringify(t, answered); got != canonical {
				fail("the JSON is not as JavaScript writes it\n    got    %s", firstDifference(got, canonical))
			}
			for _, key := range step.items("ignore") {
				expected, answered = drop(expected, key.(string)), drop(answered, key.(string))
			}
			if want.value("status") == float64(422) && isJSONInvalid(expected) {
				// Unparseable JSON: where the parser gave up, and its own words
				// for why, are the parser's (Python's json there, Go's here).
				e := expected.(obj).items("detail")[0].(obj)
				var g obj
				if o, isObject := answered.(obj); isObject && len(o.items("detail")) > 0 {
					g, _ = o.items("detail")[0].(obj)
				}
				if g.text("type") != e.text("type") || g.text("msg") != e.text("msg") || len(g.items("loc")) == 0 || g.items("loc")[0] != "body" {
					fail("422 %s, recorded %s", stringify(t, g), stringify(t, e))
				}
			} else if g, e := stringify(t, answered), stringify(t, expected); g != e {
				// Same text means same values in the same key order.
				fail("JSON differs\n    got    %s", firstDifference(g, e))
			}
		case want.has("text"):
			expected, _ := fill(t, want.value("text"), subs).(string)
			if got != expected {
				fail("text differs\n    got    %s", firstDifference(got, expected))
			}
		case want.has("sha256"):
			sum := sha256.Sum256(res.Body.Bytes())
			if hex.EncodeToString(sum[:]) != want.text("sha256") {
				fail("the file served is not the file recorded: if it was changed on purpose, put its new sha256 in tests/golden/http.json")
			}
		case want.value("pdf") == true:
			if !strings.HasPrefix(got, "%PDF") {
				fail("not a PDF")
			}
		case want.value("page") == true:
			if !strings.Contains(got, `<meta name="desk-version"`) {
				fail("the page is not stamped")
			}
		}
	}
	return replayed, failures
}

func requestsIn(session obj) int {
	n := 0
	for _, step := range session.items("steps") {
		if step.(obj).has("request") {
			n++
		}
	}
	return n
}

func TestCannotFindAPrintSystem(t *testing.T) {
	if os.Getenv("PATH") != emptyBin {
		t.Fatalf("PATH is %q", os.Getenv("PATH"))
	}
	if printing.CupsAvailable() {
		t.Fatal("a print system was found")
	}
}

func TestRecordedSessions(t *testing.T) {
	total := 0
	for _, raw := range fixture.items("sessions") {
		total += requestsIn(raw.(obj))
	}
	if want := int(fixture.value("requests").(float64)); total != want || total < 250 {
		t.Fatalf("the fixture holds %d requests and says %d", total, want)
	}

	for _, raw := range fixture.items("sessions") {
		session := raw.(obj)
		requests := requestsIn(session)
		title := fmt.Sprintf("%s: %s (%d requests)", session.text("name"), session.text("note"), requests)
		t.Run(title, func(t *testing.T) {
			if session.value("needs_renderer") == true && !webpdf.Available {
				t.Skip("rendering a PDF needs the Mac's WebKit")
			}
			// The look-up of a queue name was stood in for the same way when the session was recorded.
			if stub := session.text("stub_printer"); stub != "" {
				real := resolvePrinter
				resolvePrinter = func(preferred string) (string, error) {
					if preferred != "" {
						return preferred, nil
					}
					return stub, nil
				}
				defer func() { resolvePrinter = real }()
			}
			replayed, failures := replay(t, session)
			replayedTotal.Add(int64(replayed))
			if printing.CupsAvailable() {
				t.Error("a print system was found")
			}
			if len(failures) > 0 {
				t.Errorf("%d of %d responses differ from the recording\n\n%s", len(failures), replayed, strings.Join(failures, "\n\n"))
			}
			if replayed != requests {
				t.Errorf("replayed %d of %d requests", replayed, requests)
			}
		})
	}

	if got := int(replayedTotal.Load()); got != total {
		t.Errorf("replayed %d of the fixture's %d requests", got, total)
	}
}

// ----------------------------------------------------------- the session cookie

func TestSessionCookie(t *testing.T) {
	const token = "an-invented-session-token"
	cfg := configFor(t, filepath.Join(work, "token"))
	cfg.Token = token
	app := newApp(t, cfg, Options{})
	cookie := SessionCookie + "=" + token

	t.Run("refuses the page, its files and the API without it", func(t *testing.T) {
		for _, path := range []string{"/", "/static/base.css", "/api/health", "/api/cheques", "/nope"} {
			res := get(app, path)
			if res.Code != 401 || res.Header().Get("Content-Type") != "application/json" || res.Body.String() != `{"detail":"Not authorised"}` {
				t.Errorf("%s: %d %s %s", path, res.Code, res.Header().Get("Content-Type"), res.Body)
			}
		}
	})

	t.Run("refuses a wrong or empty value, and a write, before anything else is looked at", func(t *testing.T) {
		for _, bad := range []string{SessionCookie + "=wrong", SessionCookie + "=", "other=" + token, cookie + "x"} {
			if res := get(app, "/api/health", "Cookie", bad); res.Code != 401 {
				t.Errorf("%s: %d", bad, res.Code)
			}
		}
		body := `{"payee_name":"Greenleaf Gardening"}`
		res := send(app, "POST", "/api/entities", map[string]string{"Content-Type": "application/json"}, &body)
		if res.Code != 401 { // not the 409 a page without a version would get
			t.Errorf("a write without the cookie: %d", res.Code)
		}
		listed := get(app, "/api/entities", "Cookie", cookie)
		if listed.Body.String() != `{"entities":[]}` {
			t.Errorf("the refused write left %s", listed.Body)
		}
	})

	t.Run("lets everything through with it", func(t *testing.T) {
		for _, path := range []string{"/", "/static/base.css", "/api/health", "/api/cheques"} {
			if res := get(app, path, "Cookie", cookie); res.Code != 200 {
				t.Errorf("%s: %d", path, res.Code)
			}
		}
		if res := get(app, "/nope", "Cookie", "a=b; "+cookie+"; c=d"); res.Code != 404 {
			t.Errorf("/nope: %d", res.Code)
		}
	})

	t.Run("asks for nothing when no token is configured", func(t *testing.T) {
		open := newApp(t, configFor(t, filepath.Join(work, "no-token")), Options{})
		for _, path := range []string{"/api/health", "/"} {
			if res := get(open, path); res.Code != 200 {
				t.Errorf("%s: %d", path, res.Code)
			}
		}
	})
}

// --------------------------------------------------------- the page and its files

func TestPageAndFiles(t *testing.T) {
	folder := filepath.Join(work, "static-files")
	cfg := configFor(t, folder)
	app := newApp(t, cfg, Options{})

	t.Run("stamps the page with the desk version", func(t *testing.T) {
		version := deskVersionOf(t, app)
		if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(version) {
			t.Fatalf("version %q", version)
		}
		res := get(app, "/")
		if res.Code != 200 || res.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("%d %s", res.Code, res.Header().Get("Content-Type"))
		}
		html := res.Body.String()
		meta := "\n" + `<meta name="desk-version" content="` + version + `">`
		if !strings.Contains(html, "<head>"+meta) || strings.Count(html, `<meta name="desk-version"`) != 1 {
			t.Fatal("the page is not stamped exactly once")
		}

		// Every /static/ address in the page carries the version, and nothing else changed.
		data, err := os.ReadFile(filepath.Join(root, "static", "index.html"))
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		var addresses []string
		for _, m := range regexp.MustCompile(`(?:src|href)="(/static/[^"?]+)"`).FindAllStringSubmatch(source, -1) {
			addresses = append(addresses, m[1])
		}
		if len(addresses) <= 3 {
			t.Fatalf("only %d addresses in the page", len(addresses))
		}
		css := ""
		for _, address := range addresses {
			if !strings.Contains(html, `"`+address+"?v="+version+`"`) {
				t.Errorf("%s is not stamped", address)
			}
			if css == "" && strings.HasSuffix(address, ".css") {
				css = address
			}
		}
		// The one addition the desktop app makes: a script that adds the way into Settings.
		extras := `<script src="/static/js/desk-extras.js"></script>` + "\n"
		unstamped := strings.Replace(strings.ReplaceAll(html, "?v="+version, ""), meta, "", 1)
		if strings.Count(unstamped, extras) != 1 {
			t.Fatal("the extras script is not there exactly once")
		}
		if strings.Replace(unstamped, extras, "", 1) != source {
			t.Fatal("the page differs from its source by more than the stamps")
		}

		// The stamped addresses are served.
		served := get(app, css+"?v="+version)
		if served.Code != 200 || served.Header().Get("Content-Type") != "text/css; charset=utf-8" {
			t.Fatalf("%s: %d %s", css, served.Code, served.Header().Get("Content-Type"))
		}
	})

	t.Run("gives the same version for the same files, and a new one when a file changes", func(t *testing.T) {
		copied := filepath.Join(folder, "frontend")
		for _, f := range []string{"static/index.html", "static/base.css", "static/js/boot.js", "templates/cheque_print.html.tpl"} {
			copyFile(t, filepath.Join(root, f), filepath.Join(copied, f))
		}
		versionOf := func() string {
			c := configFor(t, filepath.Join(folder, "v"))
			c.Assets = os.DirFS(copied)
			return deskVersionOf(t, newApp(t, c, Options{}))
		}
		before := versionOf()
		if versionOf() != before {
			t.Fatal("the same files gave two versions")
		}
		boot := filepath.Join(copied, "static", "js", "boot.js")
		data, err := os.ReadFile(boot)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(boot, append(data, "\n// changed\n"...), 0o644); err != nil {
			t.Fatal(err)
		}
		changed := versionOf()
		if changed == before {
			t.Fatal("a changed file kept the version")
		}
		// The leaf photo is data, not a release: it never changes the version.
		if err := os.WriteFile(filepath.Join(copied, "static", "leaf-photo.jpg"), []byte("not a real photograph"), 0o644); err != nil {
			t.Fatal(err)
		}
		if versionOf() != changed {
			t.Fatal("the leaf photo changed the version")
		}
	})

	t.Run("serves files with their types and lets the browser revalidate", func(t *testing.T) {
		types := map[string]string{
			"/static/cheque-layout.js":                        "text/javascript; charset=utf-8",
			"/static/favicon.svg":                             "image/svg+xml",
			"/static/fonts/geist-sans-latin-400-normal.woff2": "font/woff2",
			"/static/index.html":                              "text/html; charset=utf-8",
		}
		for path, kind := range types {
			res := get(app, path)
			if res.Code != 200 || res.Header().Get("Content-Type") != kind {
				t.Errorf("%s: %d %s", path, res.Code, res.Header().Get("Content-Type"))
			}
			data, err := os.ReadFile(filepath.Join(root, path[1:]))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(res.Body.Bytes(), data) || res.Header().Get("Content-Length") != fmt.Sprint(len(data)) {
				t.Errorf("%s is not served as it is on disk", path)
			}
		}
		first := get(app, "/static/base.css")
		etag := first.Header().Get("ETag")
		if !regexp.MustCompile(`^"[0-9a-f]{32}"$`).MatchString(etag) {
			t.Fatalf("etag %q", etag)
		}
		again := get(app, "/static/base.css", "If-None-Match", etag)
		if again.Code != 304 || again.Body.Len() != 0 {
			t.Errorf("if-none-match: %d with %d bytes", again.Code, again.Body.Len())
		}
		if since := get(app, "/static/base.css", "If-Modified-Since", first.Header().Get("Last-Modified")); since.Code != 304 {
			t.Errorf("if-modified-since: %d", since.Code)
		}
		if other := get(app, "/static/base.css", "If-None-Match", `"something-else"`); other.Code != 200 {
			t.Errorf("another etag: %d", other.Code)
		}
	})

	t.Run("refuses to climb out of the static folder", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(folder, "secret.txt"), []byte("outside the static folder"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Each address as written, the way a hand-made request reaches the
		// server, and as a browser sends it once its URL parser has tidied the dots away.
		outside := [][2]string{
			{"/static/../package.json", "/package.json"},
			{"/static/..%2Fpackage.json", "/static/..%2Fpackage.json"},
			{"/static/%2e%2e/package.json", "/package.json"},
			{"/static/%2e%2e%2fpackage.json", "/static/%2e%2e%2fpackage.json"},
			{"/static/js/../../package.json", "/package.json"},
			{"/static/js/..%2f..%2fpackage.json", "/static/js/..%2f..%2fpackage.json"},
			{"/static//etc/passwd", "/static//etc/passwd"},
			{"/static/%2Fetc%2Fpasswd", "/static/%2Fetc%2Fpasswd"},
			{"/static/..", "/"},
			{"/static/js/%00", "/static/js/%00"},
			{"/static/..%5cpackage.json", "/static/..%5cpackage.json"},
		}
		for _, paths := range outside {
			written, tidied := paths[0], paths[1]
			res := get(app, tidied)
			wantStatus := 404
			if written == "/static/.." { // simply '/'
				wantStatus = 200
			}
			if res.Code != wantStatus || (res.Code == 404 && strings.Contains(res.Body.String(), `"name"`)) {
				t.Errorf("%s: %d", tidied, res.Code)
			}
			raw := get(app, written)
			if raw.Code != 404 || strings.Contains(raw.Body.String(), `"name"`) {
				t.Errorf("%s (as written): %d", written, raw.Code)
			}
		}
		// The same request shape does reach a file that is inside the folder.
		if res := get(app, "/static/js/../base.css"); res.Code != 200 {
			t.Errorf("/static/js/../base.css: %d", res.Code)
		}
		for _, path := range []string{"/static/", "/static/js", "/static/fonts/"} {
			if res := get(app, path); res.Code != 404 {
				t.Errorf("%s: %d", path, res.Code)
			}
		}
		if res := get(app, "/static"); res.Code != 307 { // to /static/, which is not a file
			t.Errorf("/static: %d", res.Code)
		}
	})

	t.Run("ships the redacted leaf photo, and prefers the one kept with the data", func(t *testing.T) {
		// The shipped backdrop has the account, the account name and the MICR
		// band blacked out, the anti-copy patch blurred, and half the
		// resolution of the photograph it came from. Its hash is pinned so a
		// different photograph cannot replace it unnoticed: look at any new
		// one before changing this.
		shipped, err := os.ReadFile(filepath.Join(root, "static", "leaf-photo.jpg"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(shipped)
		if hex.EncodeToString(sum[:]) != "3bb42345fe75719730cf5cc368cffc6064c72adb5fb2b38d52f3e0df6aaaa53f" {
			t.Fatal("the shipped leaf photo is not the redacted one")
		}
		packaged := get(app, "/static/leaf-photo.jpg")
		if packaged.Code != 200 || packaged.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(packaged.Body.Bytes(), shipped) {
			t.Fatalf("the shipped photo: %d %s", packaged.Code, packaged.Header().Get("Content-Type"))
		}

		// An invented stand-in, not a photograph of anything.
		standIn := filepath.Join(cfg.DataStaticDir, "leaf-photo.jpg")
		if err := os.WriteFile(standIn, []byte("stand-in bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		found := get(app, "/static/leaf-photo.jpg")
		if found.Code != 200 || found.Header().Get("Content-Type") != "image/jpeg" || found.Body.String() != "stand-in bytes" {
			t.Fatalf("the photo kept with the data: %d %s", found.Code, found.Header().Get("Content-Type"))
		}
		if err := os.Remove(standIn); err != nil {
			t.Fatal(err)
		}
		if again := get(app, "/static/leaf-photo.jpg"); !bytes.Equal(again.Body.Bytes(), shipped) {
			t.Fatal("the shipped photo did not come back")
		}
	})
}

// ------------------------------------------------------------------ starting up

func TestStartingUp(t *testing.T) {
	t.Run("fails at once when the calibration cannot be loaded", func(t *testing.T) {
		cfg := configFor(t, filepath.Join(work, "no-cal"))
		if err := os.Remove(cfg.CalPath); err != nil {
			t.Fatal(err)
		}
		if _, err := New(cfg, Options{}); err == nil || !strings.Contains(err.Error(), "not found — cannot print without measured geometry") {
			t.Fatalf("with no calibration: %v", err)
		}
		if err := os.WriteFile(cfg.CalPath, []byte("{ not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := New(cfg, Options{}); err == nil || !strings.Contains(err.Error(), "is not valid JSON") {
			t.Fatalf("with a broken calibration: %v", err)
		}
	})

	t.Run("uses the machine date when none is injected", func(t *testing.T) {
		app := newApp(t, configFor(t, filepath.Join(work, "today")), Options{})
		local := time.Now().Format("2006-01-02")
		if today := parse(t, get(app, "/api/reminders").Body.String()).(obj).text("today"); today != local {
			t.Fatalf("today is %s, the machine says %s", today, local)
		}
	})
}

// ------------------------------------------------------- the server connection

// fakeSync stands in for the link to the Banyan server. Nothing leaves this process.
type fakeSync struct {
	readOnly  bool
	scheduled int
	asked     []string
}

func (f *fakeSync) ReadOnly() bool     { return f.readOnly }
func (f *fakeSync) Status() SyncStatus { return SyncStatus{Connected: true, ReadOnly: f.readOnly} }
func (f *fakeSync) Discover(any) (ServerInfo, error) {
	return ServerInfo{Name: "Invented", APIURL: "https://banyan.invalid"}, nil
}
func (f *fakeSync) Connect(any, any) error { return notConnected("That key has already been used.") }
func (f *fakeSync) Disconnect(bool) error  { return nil }
func (f *fakeSync) Schedule()              { f.scheduled++ }
func (f *fakeSync) Flush() error           { return nil }
func (f *fakeSync) Overwrite() error       { return nil }
func (f *fakeSync) FetchForRestore() error { return nil }
func (f *fakeSync) Read(path string) (*http.Response, error) {
	f.asked = append(f.asked, path)
	if strings.HasPrefix(path, "/api/v1/reports/entities.csv") {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/csv; charset=utf-8"}, "Content-Disposition": {`attachment; filename="x.csv"`}, "X-Other": {"dropped"}},
			Body:       io.NopCloser(strings.NewReader("\"Entity\"\r\n")),
		}, nil
	}
	if path == "/api/v1/register/status" {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"latest":{"revision":7}}`))}, nil
	}
	return nil, notConnected("The Banyan server could not be reached. Check the internet connection.")
}

func TestServerConnection(t *testing.T) {
	t.Run("sends the register after a write that worked, and only then", func(t *testing.T) {
		link := &fakeSync{}
		app := newApp(t, configFor(t, filepath.Join(work, "sync")), Options{Sync: link})
		if link.scheduled != 1 {
			t.Fatalf("starting up scheduled %d sends", link.scheduled)
		}
		versioned := map[string]string{"X-Desk-Version": app.Version(), "Content-Type": "application/json"}
		good, bad, skip := `{"payee_name":"Greenleaf Gardening"}`, `{}`, `{}`
		steps := []struct {
			method, path string
			body         *string
			status, sent int
		}{
			{"GET", "/api/entities", nil, 200, 1},
			{"POST", "/api/entities", &bad, 422, 1},
			{"POST", "/api/entities", &good, 201, 2},
			{"POST", "/api/connection/skip", &skip, 200, 2},
			{"POST", "/api/sync/flush", nil, 200, 2},
		}
		for _, step := range steps {
			res := send(app, step.method, step.path, versioned, step.body)
			if res.Code != step.status || link.scheduled != step.sent {
				t.Errorf("%s %s: %d with %d sends scheduled, expected %d with %d", step.method, step.path, res.Code, link.scheduled, step.status, step.sent)
			}
		}
		checked := send(app, "POST", "/api/connection/check", versioned, &skip)
		if checked.Body.String() != `{"ok":true,"name":"Invented","api_url":"https://banyan.invalid"}` {
			t.Errorf("check answered %s", checked.Body)
		}
		refused := send(app, "PUT", "/api/connection", versioned, &skip)
		if refused.Code != 400 || refused.Body.String() != `{"detail":"That key has already been used."}` {
			t.Errorf("a refused key: %d %s", refused.Code, refused.Body)
		}
		for _, path := range []string{"/api/sync/overwrite", "/api/sync/restore"} {
			if res := send(app, "POST", path, versioned, &skip); res.Code != 400 {
				t.Errorf("%s without a confirmation: %d", path, res.Code)
			}
		}
		ready := 0
		restoring := newApp(t, configFor(t, filepath.Join(work, "restore")), Options{Sync: link, OnRestoreReady: func() { ready++ }})
		yes := `{"confirm":true}`
		versioned["X-Desk-Version"] = restoring.Version()
		if res := send(restoring, "POST", "/api/sync/restore", versioned, &yes); res.Code != 200 || ready != 1 {
			t.Errorf("a confirmed restore: %d, told the app %d times", res.Code, ready)
		}
	})

	t.Run("keeps what was skipped, with no server behind it", func(t *testing.T) {
		cfg := configFor(t, filepath.Join(work, "skip"))
		app := newApp(t, cfg, Options{})
		defer app.Close()
		versioned := map[string]string{"X-Desk-Version": app.Version()}
		before := parse(t, get(app, "/api/connection").Body.String()).(obj)
		if before.text("status") != "unset" || before.object("sync").value("connected") != false || before.text("desk_version") != app.Version() {
			t.Fatalf("before: %s", stringify(t, before))
		}
		after := parse(t, send(app, "POST", "/api/connection/skip", versioned, nil).Body.String()).(obj)
		if after.text("status") != "skipped" {
			t.Fatalf("after: %s", stringify(t, after))
		}
		if res := send(app, "POST", "/api/sync/flush", versioned, nil); res.Code != 200 {
			t.Errorf("flush with no server: %d %s", res.Code, res.Body)
		}
	})

	t.Run("a reports-only app shows reports and nothing else", func(t *testing.T) {
		link := &fakeSync{readOnly: true}
		app := newApp(t, configFor(t, filepath.Join(work, "reader")), Options{Sync: link})
		versioned := map[string]string{"X-Desk-Version": app.Version(), "Content-Type": "application/json"}
		body := `{"payee_name":"Greenleaf Gardening"}`
		for _, step := range []struct{ method, path string }{{"GET", "/api/cheques"}, {"POST", "/api/entities"}, {"GET", "/api/health"}} {
			res := send(app, step.method, step.path, versioned, &body)
			if res.Code != 403 || res.Body.String() != `{"detail":"This app is connected with a read key. It shows reports only."}` {
				t.Errorf("%s %s: %d %s", step.method, step.path, res.Code, res.Body)
			}
		}
		if link.scheduled != 1 {
			t.Errorf("a refused write scheduled a send")
		}
		csv := get(app, "/api/reports/entities.csv?fy=2031", "X-Desk-Version", app.Version())
		if csv.Code != 200 || csv.Body.String() != "\"Entity\"\r\n" || csv.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
			csv.Header().Get("Content-Disposition") != `attachment; filename="x.csv"` || csv.Header().Get("Cache-Control") != "no-store" || csv.Header().Get("X-Other") != "" {
			t.Errorf("the server's report: %d %v %s", csv.Code, csv.Header(), csv.Body)
		}
		if link.asked[len(link.asked)-1] != "/api/v1/reports/entities.csv?fy=2031" {
			t.Errorf("asked the server for %v", link.asked)
		}
		if reader := get(app, "/api/reader"); reader.Body.String() != `{"app":true,"role":"read","latest":{"revision":7}}` {
			t.Errorf("/api/reader: %s", reader.Body)
		}
		if down := get(app, "/api/reports/monthly"); down.Code != 502 || !strings.Contains(down.Body.String(), "could not be reached") {
			t.Errorf("an unreachable server: %d %s", down.Code, down.Body)
		}
		if res := get(app, "/api/connection"); res.Code != 200 {
			t.Errorf("/api/connection: %d", res.Code)
		}
		page := get(app, "/")
		if page.Code != 200 || !strings.Contains(page.Body.String(), `<body class="reader">`) || !strings.Contains(page.Body.String(), `<meta name="desk-version"`) {
			t.Errorf("the reader's page: %d", page.Code)
		}
	})
}

// ---------------------------------------------------------- JSON as the page reads it

func TestJSONAsJavaScript(t *testing.T) {
	written := stringify(t, obj{
		{"text", "<a href=\"x\">&</a>\u2028\u2029\n"},
		{"big", 1e21},
		{"small", 1.5e-7},
		{"whole", 92.0},
		{"zero", math.Copysign(0, -1)},
		{"endless", math.Inf(1)},
		{"nested", json.RawMessage(`{"escaped":"\u003cb\u003e \u0026 \\u003c"}`)},
		{"list", []any{}},
		{"none", nil},
	})
	want := `{"text":"<a href=\"x\">&</a>` + "\u2028\u2029" + `\n","big":1e+21,"small":1.5e-7,"whole":92,"zero":0,"endless":null,` +
		`"nested":{"escaped":"<b> & \\u003c"},"list":[],"none":null}`
	if written != want {
		t.Errorf("wrote %s\n wanted %s", written, want)
	}

	// A key sent twice keeps its first place and its last value; key order is kept.
	read := parse(t, `{"b": 1, "a": [true, null, 1e999, -0], "b": {"z": 1, "y": "é"}}`)
	if got := stringify(t, read); got != `{"b":{"z":1,"y":"é"},"a":[true,null,null,0]}` {
		t.Errorf("read back %s", got)
	}
	for _, bad := range []string{``, `{not json`, `{"a": 1,}`, `[1 2]`, `{"a": 1} x`, `nul`} {
		if _, err := parseJSON([]byte(bad)); err == nil {
			t.Errorf("%q was read as JSON", bad)
		}
	}
}

var _ http.Handler = (*App)(nil)
