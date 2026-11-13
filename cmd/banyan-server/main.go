// The Banyan server: takes the register from the treasurer's desk and shows
// members the reports. Everything it needs comes from the environment:
//
//	PORT                 where to listen (8322)
//	HOST                 the address to listen on (0.0.0.0)
//	DATA_DIR             keys, tokens and registers (./data/server)
//	BANYAN_ADMIN_TOKEN   lets its holder issue keys; unset switches that off.
//	                     At least 32 random characters (openssl rand -hex 32),
//	                     or the server refuses to start.
//	BANYAN_SERVER_NAME   what the server calls itself to a desk that connects
//	BANYAN_INSECURE_COOKIES=1   only for running over plain http on one machine
//	BANYAN_ASSETS        a folder holding static/, templates/ and
//	                     cheque-calibration.json, to use in place of the
//	                     copies built into the program
//
// `banyan-server healthcheck` asks a running server whether it is up, and
// exits 0 if it is: for a container's health check, which has no curl.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	banyan "github.com/satiyap/banyan"
	"github.com/satiyap/banyan/internal/server"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func healthcheck(port string) int {
	client := http.Client{Timeout: 3 * time.Second}
	reply, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	reply.Body.Close()
	if reply.StatusCode != 200 {
		return 1
	}
	return 0
}

// assets picks the pages and the calibration: a folder named by BANYAN_ASSETS
// if it holds them, otherwise the copies built into the program.
func assets(dataDir string) (fs.FS, string, error) {
	if dir := os.Getenv("BANYAN_ASSETS"); dir != "" {
		if info, err := os.Stat(filepath.Join(dir, "static")); err == nil && info.IsDir() {
			return os.DirFS(dir), filepath.Join(dir, "cheque-calibration.json"), nil
		}
	}
	// The desk's code reads the calibration from a file, so the built-in copy
	// is written out. Reports do not use it.
	data, err := banyan.Assets.ReadFile("cheque-calibration.json")
	if err != nil {
		return nil, "", err
	}
	cal := filepath.Join(dataDir, "tmp", "cheque-calibration.json")
	if err := os.WriteFile(cal, data, 0o644); err != nil {
		return nil, "", err
	}
	return banyan.Assets, cal, nil
}

func main() {
	port := env("PORT", "8322")
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(port))
	}

	dataDir, err := filepath.Abs(env("DATA_DIR", "./data/server"))
	if err != nil {
		log.Fatal(err)
	}
	// tmp holds nothing that matters: scratch files, and the calibration copy.
	if err := os.MkdirAll(filepath.Join(dataDir, "tmp"), 0o755); err != nil {
		log.Fatal(err)
	}
	files, calPath, err := assets(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	app, err := server.New(server.Config{
		DataDir:       dataDir,
		Assets:        files,
		CalPath:       calPath,
		AdminToken:    os.Getenv("BANYAN_ADMIN_TOKEN"),
		SecureCookies: os.Getenv("BANYAN_INSECURE_COOKIES") != "1",
		Name:          os.Getenv("BANYAN_SERVER_NAME"),
	}, server.Options{})
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", net.JoinHostPort(env("HOST", "0.0.0.0"), port))
	if err != nil {
		log.Fatal(err)
	}
	httpServer := &http.Server{Handler: app, ReadHeaderTimeout: 15 * time.Second}
	fmt.Printf("Banyan server listening on %d, data in %s\n", listener.Addr().(*net.TCPAddr).Port, dataDir)
	if os.Getenv("BANYAN_ADMIN_TOKEN") == "" {
		fmt.Println("No BANYAN_ADMIN_TOKEN: keys cannot be issued.")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()

	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			app.Close()
			log.Fatal(err)
		}
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = httpServer.Shutdown(ctx)
		cancel()
	}
	app.Close()
}
