// deskdev serves the cheque desk over loopback HTTP so it can be opened in a
// browser and driven by tests. Development only: it is not part of the app,
// printing is always switched off, and it refuses the real data folder.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	banyan "github.com/satiyap/banyan"
	"github.com/satiyap/banyan/internal/desk"
	"github.com/satiyap/banyan/internal/webpdf"
)

// WebKit renders on the main thread.
func init() { runtime.LockOSThread() }

func main() {
	data := flag.String("data", "", "a scratch data folder (required)")
	addr := flag.String("addr", "127.0.0.1:8455", "loopback address to listen on")
	flag.Parse()
	if *data == "" {
		log.Fatal("give a scratch folder with -data")
	}
	abs, err := filepath.Abs(*data)
	if err != nil {
		log.Fatal(err)
	}
	if strings.Contains(abs, "Application Support") {
		log.Fatal("deskdev does not open an app's data folder: use a scratch folder")
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		log.Fatal("deskdev listens on loopback only")
	}
	if err := os.MkdirAll(filepath.Join(abs, "cheques"), 0o755); err != nil {
		log.Fatal(err)
	}
	cal := filepath.Join(abs, "cheque-calibration.json")
	if _, err := os.Stat(cal); os.IsNotExist(err) {
		shipped, _ := banyan.Assets.ReadFile("cheque-calibration.json")
		if err := os.WriteFile(cal, shipped, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	app, err := desk.New(desk.Config{
		DBPath:         filepath.Join(abs, "banyan.db"),
		CalPath:        cal,
		PDFDir:         filepath.Join(abs, "cheques"),
		Assets:         banyan.Assets,
		DataStaticDir:  filepath.Join(abs, "static"),
		ConnectionPath: filepath.Join(abs, "connection.json"),
		PrintDisabled:  true,
	}, desk.Options{})
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Cheque desk (development) on http://%s, data in %s, printing off", listener.Addr(), abs)
	go func() { log.Fatal(http.Serve(listener, app)) }()
	webpdf.ServeMainThread(make(chan struct{}))
}
