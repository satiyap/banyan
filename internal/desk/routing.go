package desk

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/satiyap/banyan/internal/calibration"
)

// response is what a route answers: a status, headers in the order set, a body.
type response struct {
	status int
	header [][2]string
	body   []byte
}

func (r *response) set(name, value string) {
	for i := range r.header {
		if strings.EqualFold(r.header[i][0], name) {
			r.header[i][1] = value
			return
		}
	}
	r.header = append(r.header, [2]string{name, value})
}

func jsonResponse(status int, content any) (*response, error) {
	body, err := encodeJSON(content)
	if err != nil {
		return nil, err
	}
	return &response{status: status, header: [][2]string{{"Content-Type", "application/json"}}, body: body}, nil
}

func ok(content any) (*response, error) { return jsonResponse(http.StatusOK, content) }

// mustJSON is jsonResponse for content that cannot fail to encode.
func mustJSON(status int, content any) *response {
	res, err := jsonResponse(status, content)
	if err != nil {
		return internalError(err)
	}
	return res
}

// httpError is FastAPI's HTTPException: answered as `{"detail": ...}` with this status.
type httpError struct {
	status int
	detail string
	header [][2]string
}

func (e *httpError) Error() string { return e.detail }

func refuse(status int, detail string) error { return &httpError{status: status, detail: detail} }

// validationError is FastAPI's RequestValidationError: answered as 422 with
// the list of what was wrong.
type validationError struct {
	errors []errorDetail
}

func (e *validationError) Error() string { return "request validation failed" }

// internalError is anything a route did not expect. The Python answered these the same way.
func internalError(err error) *response {
	log.Printf("desk: %v", err)
	return &response{
		status: http.StatusInternalServerError,
		header: [][2]string{{"Content-Type", "text/plain; charset=utf-8"}},
		body:   []byte("Internal Server Error"),
	}
}

// unquote is urllib's unquote: %XX becomes its byte, anything else is left as written.
func unquote(text string) string {
	if !strings.Contains(text, "%") {
		return text
	}
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		if text[i] == '%' && i+2 < len(text) && isHex(text[i+1]) && isHex(text[i+2]) {
			out = append(out, unhex(text[i+1])<<4|unhex(text[i+2]))
			i += 2
			continue
		}
		out = append(out, text[i])
	}
	return strings.ToValidUTF8(string(out), "�")
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// target is the request's address exactly as sent: its origin when the client
// gave one, its path, and its query string with the '?'.
func target(r *http.Request) (origin, rawPath, rawQuery string) {
	uri := r.RequestURI
	if uri == "" {
		uri = r.URL.EscapedPath()
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			uri += "?" + r.URL.RawQuery
		}
	}
	if m := absoluteRE.FindStringSubmatch(uri); m != nil {
		origin, uri = m[1], m[2]
	}
	if i := strings.IndexByte(uri, '#'); i >= 0 {
		uri = uri[:i]
	}
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		return origin, uri[:i], uri[i:]
	}
	return origin, uri, ""
}

var absoluteRE = regexp.MustCompile(`(?is)^([a-z][a-z0-9+.-]*://[^/?#]*)(.*)$`)

// requestPath is the request's path exactly as sent, with %XX decoded.
func requestPath(r *http.Request) string {
	_, raw, _ := target(r)
	if raw == "" {
		raw = "/"
	}
	return unquote(raw)
}

// searchParams reads a query string as URLSearchParams does.
func searchParams(rawQuery string) [][2]string {
	var out [][2]string
	for _, part := range strings.Split(strings.TrimPrefix(rawQuery, "?"), "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		plus := func(s string) string { return unquote(strings.ReplaceAll(s, "+", " ")) }
		out = append(out, [2]string{plus(name), plus(value)})
	}
	return out
}

// ---------------------------------------------------------------- requests

// req is what a route reads from its request. Path and query problems are
// collected, and reported together with the body's.
type req struct {
	raw    *http.Request
	params map[string]string
	search [][2]string
	errors []errorDetail
	// Unparseable JSON is reported at once, on its own.
	fatal error
}

func (rq *req) take(f field, input any, loc []any) any {
	value, errs := validate(f, input, loc)
	rq.errors = append(rq.errors, errs...)
	return value
}

// path is a path parameter. Only read the value after check().
func (rq *req) path(name string, f field) any {
	return rq.take(f, rq.params[name], []any{"path", name})
}

var idField = integer(intOptions{})
var optionalInt = integer(intOptions{def: def(nil), nullable: true})

// id is a path parameter that is a row's id.
func (rq *req) id(name string) int64 {
	n, _ := rq.path(name, idField).(int64)
	return n
}

// query is a query parameter; when sent twice the last one counts.
func (rq *req) query(name string, f field) any {
	input := notSent
	for _, p := range rq.search {
		if p[0] == name {
			input = p[1]
		}
	}
	return rq.take(f, input, []any{"query", name})
}

func (rq *req) queryStr(name string, f field) string {
	s, _ := rq.query(name, f).(string)
	return s
}

func (rq *req) queryInt(name string, f field) *int64 {
	if n, ok := rq.query(name, f).(int64); ok {
		return &n
	}
	return nil
}

func (rq *req) text() string {
	if rq.raw.Body == nil {
		return ""
	}
	data, _ := io.ReadAll(rq.raw.Body)
	return strings.ToValidUTF8(string(data), "�")
}

var jsonTypeRE = regexp.MustCompile(`^application/(?:.+\+)?json$`)

// body is the JSON body, validated.
func (rq *req) body(f field) vals {
	text := rq.text()
	var input any
	if text != "" {
		kind, _, _ := strings.Cut(rq.raw.Header.Get("Content-Type"), ";")
		if jsonTypeRE.MatchString(strings.ToLower(strings.TrimSpace(kind))) {
			parsed, err := parseJSON([]byte(text))
			if err != nil {
				at, why := utf8.RuneCountInString(text), err.Error()
				var syntax *jsonSyntaxError
				if errors.As(err, &syntax) {
					at, why = syntax.offset, syntax.message
				}
				if rq.fatal == nil {
					rq.fatal = &validationError{[]errorDetail{{
						Type: "json_invalid", Loc: []any{"body", at}, Msg: "JSON decode error",
						Input: obj{}, Ctx: obj{{"error", why}},
					}}}
				}
				return nil
			}
			input = parsed
		} else {
			// Not declared as JSON, so it is not read as JSON: the model is handed the text.
			input = text
		}
	}
	if input == nil {
		rq.errors = append(rq.errors, errorDetail{Type: "missing", Loc: []any{"body"}, Msg: "Field required", Input: nil})
		return nil
	}
	out, _ := rq.take(f, input, []any{"body"}).(vals)
	return out
}

// loose is the body as JSON whatever the request calls it, or nil when it is
// not an object. The connection routes read their few fields this way.
func (rq *req) loose() obj {
	parsed, err := parseJSON([]byte(rq.text()))
	if err != nil {
		return nil
	}
	out, _ := parsed.(obj)
	return out
}

// check refuses the request if anything read so far was wrong.
func (rq *req) check() error {
	if rq.fatal != nil {
		return rq.fatal
	}
	if len(rq.errors) > 0 {
		return &validationError{rq.errors}
	}
	return nil
}

// ------------------------------------------------------------------ routes

type handler func(rq *req) (*response, error)

type route struct {
	methods []string
	regex   *regexp.Regexp
	names   []string
	handler handler
}

var paramRE = regexp.MustCompile(`\{\w+\}`)

// declare a route. Paths are written as FastAPI wrote them: `{name}` is one path segment.
func declare(method, path string, h handler) route {
	var pattern strings.Builder
	last := 0
	for _, at := range paramRE.FindAllStringIndex(path, -1) {
		pattern.WriteString(regexp.QuoteMeta(path[last:at[0]]))
		pattern.WriteString("(?P<" + path[at[0]+1:at[1]-1] + ">[^/]+)")
		last = at[1]
	}
	pattern.WriteString(regexp.QuoteMeta(path[last:]))
	re := regexp.MustCompile("^" + pattern.String() + "$")
	return route{methods: []string{method}, regex: re, names: re.SubexpNames(), handler: h}
}

// routeTable is every route, in the order they are tried.
func (a *App) routeTable() []route {
	get := func(path string, h handler) route { return declare("GET", path, h) }
	post := func(path string, h handler) route { return declare("POST", path, h) }
	put := func(path string, h handler) route { return declare("PUT", path, h) }
	patch := func(path string, h handler) route { return declare("PATCH", path, h) }
	del := func(path string, h handler) route { return declare("DELETE", path, h) }
	return []route{
		get("/api/health", a.health),
		get("/api/calibration", a.getCalibration),
		get("/api/printers", a.printers),
		get("/api/next-cheque-no", a.nextChequeNo),
		post("/api/compose", a.composeRoute),
		get("/api/cheques", a.listCheques),
		get("/api/cheques/{cheque_id}", a.getCheque),
		post("/api/cheques", a.issueCheque),
		post("/api/cheques/{cheque_id}/confirm", a.confirmCheque),
		patch("/api/cheques/{cheque_id}", a.editCheque),
		post("/api/cheques/move", a.moveCheques),
		post("/api/cheques/{cheque_id}/void", a.voidCheque),
		get("/api/cheques/{cheque_id}/pdf", a.chequePDF),
		post("/api/test-print", a.testPrint),
		get("/api/test-sheet.pdf", a.testSheet),

		get("/api/batches/open", a.openBatch),
		post("/api/batches", a.createBatch),
		post("/api/batches/{batch_id}/print-first", a.batchPrintFirst),
		post("/api/batches/{batch_id}/first-outcome", a.batchFirstOutcome),
		post("/api/batches/{batch_id}/print-rest", a.batchPrintRest),
		post("/api/batches/{batch_id}/rest-outcome", a.batchRestOutcome),
		post("/api/batches/{batch_id}/cancel", a.batchCancel),

		get("/api/frequency", a.frequency),
		get("/api/entities", a.getEntities),
		post("/api/entities", a.createEntity),
		put("/api/entities/{entity_id}", a.updateEntity),
		del("/api/entities/{entity_id}", a.deleteEntity),

		get("/api/reports/monthly", a.monthlyReport),
		get("/api/reports/monthly.pdf", a.monthlyReportPDF),
		get("/api/reports", a.report),
		get("/api/reports/{kind}.csv", a.reportCSV),
		get("/api/export.csv", a.exportCSV),

		get("/api/desk", a.desk),
		get("/api/entities/{entity_id}/history", a.entityHistory),
		get("/api/register", a.register),
		get("/api/leaves/{cheque_no}", a.leaf),
		get("/api/events", a.events),
		post("/api/register/account-for", a.accountFor),
		post("/api/batches/{batch_id}/test-print", a.batchTestPrint),
		post("/api/test-print/cheque", a.chequeTestPrint),
		get("/api/books", a.getBooks),
		post("/api/books", a.addBook),
		patch("/api/books/{book_id}", a.updateBook),
		post("/api/books/{book_id}/alert", a.bookAlert),
		get("/api/reminders", a.getReminders),
		post("/api/cheques/{cheque_id}/remind-ack", a.remindAck),

		// Declared before the /static mount so it takes precedence over it.
		get("/static/leaf-photo.jpg", a.leafPhoto),
		get("/", a.indexPage),
		get("/setup", a.setupPage),

		get("/api/connection", a.getConnection),
		put("/api/connection", a.putConnection),
		post("/api/connection/check", a.checkConnection),
		post("/api/connection/skip", a.skipConnection),
		post("/api/sync/flush", a.syncFlush),
		post("/api/sync/overwrite", a.syncOverwrite),
		post("/api/sync/restore", a.syncRestore),
	}
}

var staticMount = regexp.MustCompile(`(?s)^/static/(.*)$`)
var connectionOrSync = regexp.MustCompile(`^/api/(connection|sync)\b`)
var trailingSlashes = regexp.MustCompile(`/+$`)

func reading(method string) bool { return method == http.MethodGet || method == http.MethodHead }

// ServeHTTP answers one request.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	res := a.respond(r)
	header := w.Header()
	for _, h := range res.header {
		header.Set(h[0], h[1])
	}
	w.WriteHeader(res.status)
	if r.Method != http.MethodHead && len(res.body) > 0 {
		_, _ = w.Write(res.body)
	}
}

func (a *App) respond(r *http.Request) (res *response) {
	defer func() {
		if caught := recover(); caught != nil {
			res = internalError(errors.New("panic: " + stringOf(caught)))
		}
	}()
	path := requestPath(r)
	api := strings.HasPrefix(path, "/api/")

	// Inside the desktop app only the window that was handed the session
	// cookie may talk to the server. Before everything else, pages and files included.
	if a.cfg.Token != "" {
		sent := ""
		if cookie, err := r.Cookie(SessionCookie); err == nil {
			sent = cookie.Value
		}
		got, want := sha256.Sum256([]byte(sent)), sha256.Sum256([]byte(a.cfg.Token))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			return mustJSON(http.StatusUnauthorized, obj{{"detail", "Not authorised"}})
		}
	}

	// Turn away API calls from a page older than this server.
	//
	// A page that sends a different version is out of date. One that sends
	// none predates versioning, so its writes are refused too; plain reads
	// without a version stay open for the health probe, the CSV and PDF
	// links, and curl. Nothing a refused request asked for is done.
	if api {
		sent, present := r.Header[http.CanonicalHeaderKey(versionHeader)]
		if (!present || strings.Join(sent, ", ") != a.version) && (present || !reading(r.Method)) {
			return mustJSON(http.StatusConflict, obj{
				{"detail", "The desk has been updated since this page was opened. Reload the page — nothing was saved."},
				{"reload", true},
				{"desk_version", a.version},
			})
		}
	}

	// A reports-only app (connected with a read key) has no desk behind it.
	// Its report pages are answered by the Banyan server, and every other part
	// of the desk's API is closed, so nothing here can be written or printed
	// and the register on this Mac, if there is one, is neither shown nor touched.
	if api && a.syncer.ReadOnly() && !connectionOrSync.MatchString(path) {
		return a.readOnly(r, path)
	}

	serialise := api && !reading(r.Method) && !connectionOrSync.MatchString(path)
	if serialise {
		a.writing.Lock()
	}
	res = a.dispatch(r, path)
	if serialise {
		a.writing.Unlock()
	}

	// Every change to the register goes to the Banyan server straight away.
	wrote := !reading(r.Method) && r.Method != http.MethodOptions && res.status < 400
	if wrote && api && !connectionOrSync.MatchString(path) {
		a.syncer.Schedule()
	}
	return res
}

func stringOf(v any) string {
	switch t := v.(type) {
	case error:
		return t.Error()
	case string:
		return t
	}
	return "unexpected failure"
}

// dispatch finds the route as Starlette does: the first whose path and method
// both match; failing that, 405 from the first whose path matches; else 404.
func (a *App) dispatch(r *http.Request, path string) (res *response) {
	defer func() {
		if caught := recover(); caught != nil {
			res = internalError(errors.New("panic: " + stringOf(caught)))
		}
	}()
	res, err := a.find(r, path)
	if err == nil {
		return res
	}
	var refused *httpError
	var invalid *validationError
	switch {
	case errors.As(err, &refused):
		res = mustJSON(refused.status, obj{{"detail", refused.detail}})
		for _, h := range refused.header {
			res.set(h[0], h[1])
		}
		return res
	case errors.As(err, &invalid):
		return mustJSON(http.StatusUnprocessableEntity, obj{{"detail", invalid.errors}})
	case isType(err, calibration.ErrorType):
		return mustJSON(http.StatusInternalServerError, obj{{"detail", message(err)}})
	}
	return internalError(err)
}

func (a *App) find(r *http.Request, path string) (*response, error) {
	origin, rawPath, rawQuery := target(r)
	var wrongMethod *route
	for i := range a.routes {
		rt := &a.routes[i]
		m := rt.regex.FindStringSubmatch(path)
		if m == nil {
			continue
		}
		if !contains(rt.methods, r.Method) {
			if wrongMethod == nil {
				wrongMethod = rt
			}
			continue
		}
		params := map[string]string{}
		for j, name := range rt.names {
			if name != "" {
				params[name] = m[j]
			}
		}
		return rt.handler(&req{raw: r, params: params, search: searchParams(rawQuery)})
	}
	if m := staticMount.FindStringSubmatch(path); m != nil {
		return a.staticFile(r, m[1])
	}
	if wrongMethod != nil {
		return nil, &httpError{
			status: http.StatusMethodNotAllowed, detail: "Method Not Allowed",
			header: [][2]string{{"Allow", strings.Join(wrongMethod.methods, ", ")}},
		}
	}
	// A stray or missing final slash is sent to the address that exists, whatever the method.
	if path != "/" {
		other := path + "/"
		if strings.HasSuffix(path, "/") {
			other = trailingSlashes.ReplaceAllString(path, "")
		}
		known := staticMount.MatchString(other)
		for i := 0; i < len(a.routes) && !known; i++ {
			known = a.routes[i].regex.MatchString(other)
		}
		if known {
			if origin == "" {
				origin = "http://" + r.Host
				if r.TLS != nil {
					origin = "https://" + r.Host
				}
			}
			if strings.HasSuffix(path, "/") {
				rawPath = trailingSlashes.ReplaceAllString(rawPath, "")
			} else {
				rawPath += "/"
			}
			return &response{
				status: http.StatusTemporaryRedirect,
				header: [][2]string{{"Location", origin + rawPath + rawQuery}},
			}, nil
		}
	}
	return nil, refuse(http.StatusNotFound, "Not Found")
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
