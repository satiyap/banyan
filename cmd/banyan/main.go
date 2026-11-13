// The Banyan desktop app: one window onto the cheque desk.
//
// The desk's pages and its HTTP API are served to the window in-process. No
// port is opened, so nothing else on the Mac can reach the register.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	banyan "github.com/satiyap/banyan"
	"github.com/satiyap/banyan/internal/connection"
	"github.com/satiyap/banyan/internal/desk"
	"github.com/satiyap/banyan/internal/desksync"
	"github.com/satiyap/banyan/internal/webpdf"
)

// version is set by the build. One ending in "-dev" is a development build:
// it keeps its own data folder and never reaches the printer unless told to.
var version = "0.0.0-dev"

func isDev() bool { return strings.HasSuffix(version, "-dev") }

func main() {
	paths, err := preparePaths()
	if err != nil {
		log.Fatal(err)
	}
	// A register fetched from the Banyan server goes in before anything opens the old one.
	if restored, err := desksync.ApplyPendingRestore(
		desksync.SyncPaths{DbPath: paths.register, ConnectionPath: paths.connection}, time.Now,
	); err != nil {
		log.Printf("the register from the Banyan server could not be put in place: %v", err)
	} else if restored {
		log.Print("Restored the register from the Banyan server.")
	}

	// Development builds never reach the printer: a cheque leaf is usually loaded.
	printDisabled := os.Getenv("BANYAN_PRINT_DISABLED") == "1"
	if isDev() {
		printDisabled = os.Getenv("BANYAN_ALLOW_PRINT") != "1"
	}

	var app *application.App
	bar := &titleBar{}
	var restarting, quitting atomic.Bool
	theDesk, err := desk.New(desk.Config{
		DBPath:         paths.register,
		CalPath:        paths.calibration,
		PDFDir:         paths.pdfDir,
		Assets:         banyan.Assets,
		DataStaticDir:  paths.dataStatic,
		ConnectionPath: paths.connection,
		PrintDisabled:  printDisabled,
	}, desk.Options{
		// The new register can only be put in place by starting again.
		OnRestoreReady: func() {
			restarting.Store(true)
			time.AfterFunc(300*time.Millisecond, func() {
				relaunch()
				quitting.Store(true)
				app.Quit()
			})
		},
	})
	if err != nil {
		log.Fatalf("the cheque desk did not start: %v", err)
	}

	app = application.New(application.Options{
		Name:        "Banyan",
		Description: "Writes, prints and logs cheques",
		Assets:      application.AssetOptions{Handler: trace(files(theDesk, paths, bar.setPage)), DisableLogging: true},
		// Closing the window leaves the app in the Dock, as version 1 did.
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		ShouldQuit: func() bool {
			quitting.Store(true)
			return true
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.banyanrwa.chequedesk" + map[bool]string{true: ".dev"}[isDev()],
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				if w := app.Window.Current(); w != nil {
					w.Show().Focus()
				}
			},
		},
		// The register is sent on the way out, so the server's copy is level.
		OnShutdown: func() {
			if !restarting.Load() {
				flush(theDesk, 15*time.Second)
			}
			theDesk.Close()
		},
	})

	pageJS := shellJS
	// Development: BANYAN_DEV_JS=script.js also runs a script in each page, to
	// exercise the window without a hand on the mouse.
	if script := os.Getenv("BANYAN_DEV_JS"); script != "" && isDev() {
		if js, err := os.ReadFile(script); err == nil {
			pageJS += "\n" + string(js)
		}
	}
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "desk",
		Title:     windowTitle,
		Width:     1360,
		Height:    880,
		MinWidth:  1024,
		MinHeight: 640,
		URL:       firstPage(theDesk, paths.connection),
		JS:        pageJS,
	})
	// The close button puts the window away rather than destroying it, so a
	// click on the Dock icon brings the desk back as it was left.
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if quitting.Load() || restarting.Load() {
			return
		}
		event.Cancel()
		window.Hide()
	})
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		window.Show().Focus()
	})
	setupMenu(app, window, paths)
	bar.update(func() { bar.window = window })
	go bar.watchSync(theDesk)

	// Development: BANYAN_RENDER_CHECK=page.html:out.pdf renders one cheque-sized
	// page with the app's own renderer and quits. Nothing is printed.
	if check := os.Getenv("BANYAN_RENDER_CHECK"); check != "" {
		html, pdf, _ := strings.Cut(check, ":")
		go func() {
			if err := webpdf.Render(html, pdf, webpdf.Page{W: 92, H: 204}); err != nil {
				log.Printf("render check failed: %v", err)
			} else {
				log.Printf("render check wrote %s", pdf)
			}
			app.Quit()
		}()
	}
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

const windowTitle = "Banyan"

// shellJS runs in every page. A web view on its own neither opens a new
// window nor saves a download, so both are handed to the app: a link that
// would open a new window (a cheque PDF) is opened beside the app, and a
// download link is saved to Downloads. They are asked for with fetch, so the
// page itself never navigates away. The page also says what it is called, for
// the title bar.
const shellJS = `
window.open = function (url) { fetch('/shell/open?u=' + encodeURIComponent(url)); return null; };
if (!window.__banyanShell) {
  window.__banyanShell = true;
  var told = null;
  var tell = function () {
    if (document.title === told) return;
    told = document.title;
    fetch('/shell/title?t=' + encodeURIComponent(told));
  };
  tell();
  new MutationObserver(tell).observe(document.head, { subtree: true, childList: true, characterData: true });
  document.addEventListener('click', function (event) {
    var link = event.target.closest && event.target.closest('a[download]');
    if (!link) return;
    event.preventDefault();
    fetch('/shell/save?u=' + encodeURIComponent(link.getAttribute('href')));
  }, true);
}`

// firstPage is the desk, or the server connection screen if this desk has
// never been asked. Skipping counts as an answer, so the question is put once.
// Also when the desk and the server disagree: that needs an answer before any
// cheque is written.
func firstPage(d *desk.App, connectionPath string) string {
	status := d.Sync().Status()
	if connection.ReadConnection(connectionPath).Status == "unset" || status.Choice != nil {
		return "/setup"
	}
	return "/"
}

// describeSync says, in the title bar's words, what is wrong with the server's
// copy of the register. It says nothing while all is well, so the bar stays
// still; Settings shows the routine state.
func describeSync(s desk.SyncStatus) string {
	switch {
	case !s.Connected:
		return ""
	case s.Retired:
		return "Another desk has taken over. This desk is not syncing"
	case s.Choice != nil:
		return "The server holds a different register. Open Banyan ▸ Settings"
	}
	when := "never"
	if at, err := time.Parse(time.RFC3339, s.SyncedAt); err == nil {
		when = at.Local().Format("02 Jan 15:04")
	}
	if s.Error != "" {
		return fmt.Sprintf("Not synced since %s: %s", when, s.Error)
	}
	return ""
}

// titleBar is what the window's title bar says: the app's name, and a note
// only when the Banyan server's copy of the register needs attention. It does
// not follow the page.
type titleBar struct {
	mu     sync.Mutex
	window *application.WebviewWindow
	page   string
	note   string
	shown  string
}

func (t *titleBar) update(change func()) {
	t.mu.Lock()
	change()
	title := windowTitle
	if t.note != "" {
		title += " — " + t.note
	}
	window, changed := t.window, title != t.shown
	t.shown = title
	t.mu.Unlock()
	if changed && window != nil {
		window.SetTitle(title)
	}
}

// setPage is called when the page names itself.
func (t *titleBar) setPage(title string) { t.update(func() { t.page = title }) }

// watchSync keeps the note about the Banyan server current.
func (t *titleBar) watchSync(d *desk.App) {
	for {
		note := describeSync(d.Sync().Status())
		t.update(func() { t.note = note })
		time.Sleep(10 * time.Second)
	}
}

// flush sends the register now and waits, but not for ever.
func flush(d *desk.App, limit time.Duration) {
	done := make(chan struct{})
	go func() {
		_ = d.Sync().Flush()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
	}
}

// relaunch starts the app again once this copy has gone.
func relaunch() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = exec.Command("/bin/sh", "-c", `sleep 1; exec "$0"`, exe).Start()
}

// trace logs each request and its status when BANYAN_TRACE is set. For development.
func trace(next http.Handler) http.Handler {
	if os.Getenv("BANYAN_TRACE") == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(seen, r)
		log.Printf("%d %s %s", seen.status, r.Method, r.URL.RequestURI())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
