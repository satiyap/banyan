// Package desk is the Banyan cheque desk's HTTP API and static frontend.
//
// It prints onto genuine bank-issued SBI leaves. Two rules shape the whole design:
//
//  1. Nothing reaches the printer that has not been rendered at the measured
//     geometry and had its page box verified at 1:1.
//  2. A cheque is not recorded as issued because a job was queued. The print
//     system accepting a job says nothing about whether toner landed correctly
//     on a hand-fed leaf, so the register waits for the treasurer to confirm
//     the physical result.
//
// The page in static/ is written against these routes: every path, status
// code, response key and error message is part of that contract, and the
// recorded sessions in tests/golden/http.json hold each one in place.
package desk

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/satiyap/banyan/internal/calibration"
	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/printing"
	"github.com/satiyap/banyan/internal/pycompat"
	"github.com/satiyap/banyan/internal/render"
)

// SessionCookie is the cookie the desktop window is handed.
const SessionCookie = "banyan_session"

const versionHeader = "X-Desk-Version"

// Config says where the desk finds its files.
type Config struct {
	// The register. Financial records: never seeded, reset or bulk-edited.
	DBPath string
	// Measured field geometry. Re-read whenever the file changes.
	CalPath string
	// Rendered cheque and report PDFs. Disposable.
	PDFDir string
	// The frontend and the print template: static/... and templates/...
	Assets fs.FS
	// Where a prepared leaf photo may live. It is served in preference to the shipped one.
	DataStaticDir string
	// The API URL and user key entered on first launch, or the fact that it was skipped.
	ConnectionPath string
	// When set, every request must carry this value in the session cookie.
	Token string
	// When true, nothing is ever handed to the print queue.
	PrintDisabled bool
}

// Options are the parts of the desk a caller may replace.
type Options struct {
	// The desk's calendar date, 'YYYY-MM-DD'. Only tests replace it.
	Today func() string
	// The register's clock. Only tests replace it.
	Now func() time.Time
	// Called once a register fetched from the server is on disk, waiting for a
	// restart to be put in place.
	OnRestoreReady func()
	// What renders PDFs. Default: the Mac's own WebKit over cfg.Assets.
	Renderer *render.Renderer
	// The link to the Banyan server.
	Sync Syncer
}

// resolvePrinter picks the queue a job goes to. Tests stand a queue name in for it.
var resolvePrinter = printing.ResolvePrinter

// App is the desk: an http.Handler over one register.
type App struct {
	cfg      Config
	opts     Options
	db       *db.Database
	renderer *render.Renderer
	syncer   Syncer
	// Set when the desk made its own Syncer, and so is the one to stop it.
	ownSync bool
	version string
	routes  []route

	calMu       sync.Mutex
	cal         *calibration.Calibration
	calMtime    time.Time
	calMtimeSet bool
	calError    string

	pageMu     sync.Mutex
	indexHTML  string
	readerHTML string
	setupHTML  string

	// Requests that change the register run one at a time, as they did when
	// the server had a single thread.
	writing sync.Mutex
}

// New opens the register and loads the calibration. It fails at once when the
// calibration cannot be loaded: no geometry, no app.
func New(cfg Config, opts Options) (*App, error) {
	if cfg.Assets == nil {
		return nil, errors.New("desk: Config.Assets is not set")
	}
	if cfg.PrintDisabled {
		printing.SetPrintingDisabled(true)
	}
	database, err := db.Open(cfg.DBPath, db.Options{Now: opts.Now})
	if err != nil {
		return nil, err
	}
	cal, err := calibration.Load(cfg.CalPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.PDFDir, 0o755); err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, opts: opts, db: database, cal: cal, renderer: opts.Renderer, syncer: opts.Sync}
	if a.renderer == nil {
		a.renderer = &render.Renderer{Assets: cfg.Assets}
	}
	if a.syncer == nil {
		a.syncer, a.ownSync = defaultSyncer(cfg), true
	}
	a.version, err = deskVersion(cfg.Assets)
	if err != nil {
		return nil, err
	}
	// The desk's page gets one more script as it is served: the way into Settings.
	index, err := a.Page("index.html")
	if err != nil {
		return nil, err
	}
	a.indexHTML = strings.Replace(index, "</body>",
		`<script src="/static/js/desk-extras.js?v=`+a.version+`"></script>`+"\n</body>", 1)
	a.routes = a.routeTable()

	// Catch up on anything that did not reach the server before the last quit.
	a.syncer.Schedule()
	return a, nil
}

// Version is the fingerprint pages send back with every API call.
func (a *App) Version() string { return a.version }

// Sync is the desk's link to the Banyan server.
func (a *App) Sync() Syncer { return a.syncer }

// Close releases what the desk holds: a send to the server that is waiting on
// its timer. The register opens a connection for each call and closes it
// again, so there is nothing held open there.
func (a *App) Close() {
	if stopper, ok := a.syncer.(interface{ Close() }); ok && a.ownSync {
		stopper.Close()
	}
}

// today is today's date, wherever a route needs it.
func (a *App) today() string {
	if a.opts.Today != nil {
		return a.opts.Today()
	}
	return time.Now().Format("2006-01-02")
}

// calibration re-reads the calibration file when it changes on disk.
//
// Measurements are edited by hand between test prints; picking the change up
// without a restart keeps that loop tight. A broken edit keeps the last good
// copy and is surfaced through /api/health rather than crashing the app.
func (a *App) calibration() (*calibration.Calibration, error) {
	a.calMu.Lock()
	defer a.calMu.Unlock()
	current := a.cal
	info, err := os.Stat(a.cfg.CalPath)
	if err == nil && (!a.calMtimeSet || !info.ModTime().Equal(a.calMtime)) {
		var loaded *calibration.Calibration
		if loaded, err = calibration.Load(a.cfg.CalPath); err == nil {
			a.cal = loaded
			if info, err = os.Stat(a.cfg.CalPath); err == nil {
				a.calMtime, a.calMtimeSet = info.ModTime(), true
				a.calError = ""
			}
		}
	}
	if err != nil {
		var named *pycompat.Error
		var pathErr *fs.PathError
		switch {
		case errors.As(err, &named) && named.Type == calibration.ErrorType:
			a.calError = named.Message
		case errors.Is(err, fs.ErrNotExist):
			a.calError = "[Errno 2] No such file or directory: " + pycompat.Repr(a.cfg.CalPath)
		case errors.As(err, &pathErr):
			a.calError = err.Error()
		default:
			return nil, err
		}
		return current, nil
	}
	return a.cal, nil
}

func (a *App) calibrationError() string {
	a.calMu.Lock()
	defer a.calMu.Unlock()
	return a.calError
}

// ------------------------------------------------- Python formatting, mirrored

// rupee is `f'{paise // 100}.{paise % 100:02d}'`.
func rupee(paise int64) string {
	whole, rest := paise/100, paise%100
	if rest < 0 {
		whole, rest = whole-1, rest+100
	}
	return strconv.FormatInt(whole, 10) + "." + pad(rest, 2)
}

// fixed is Python's `{x:.Nf}`: the number's exact value, rounded half to even.
func fixed(x float64, digits int) string { return strconv.FormatFloat(x, 'f', digits, 64) }

func pad(n int64, width int) string {
	s := strconv.FormatInt(n, 10)
	if len(s) < width {
		s = strings.Repeat("0", width-len(s)) + s
	}
	return s
}

// csvRow is one row of `csv.writer(quoting=csv.QUOTE_ALL, lineterminator='\r\n')`.
func csvRow(cells ...string) string {
	quoted := make([]string, len(cells))
	for i, cell := range cells {
		quoted[i] = `"` + strings.ReplaceAll(cell, `"`, `""`) + `"`
	}
	return strings.Join(quoted, ",") + "\r\n"
}

// csvSafe keeps a cell as text. One starting with = + - or @ is executed as a
// formula by Excel and Numbers; prefixing with an apostrophe prevents that.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return "'" + s
	}
	return s
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// isType reports whether err is the named refusal.
func isType(err error, kind string) bool {
	var named *pycompat.Error
	return errors.As(err, &named) && named.Type == kind
}

func message(err error) string {
	var named *pycompat.Error
	if errors.As(err, &named) {
		return named.Message
	}
	return err.Error()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// jsNumber is String(n) for a whole JavaScript number.
func jsNumber(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// textOf is a stored value inside a message.
func textOf(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}
