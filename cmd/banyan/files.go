package main

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// files stands between the window and the desk for what a web view cannot do
// on its own: save a download, open a PDF beside the app, and put the page's
// title in the title bar. The page is told to ask for them by shellJS. Everything else goes
// straight to the desk.
func files(desk http.Handler, p paths, pageTitle func(string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Settings asks which version of the app this is.
		if r.Method == http.MethodGet && r.URL.Path == "/shell/about" {
			note := ""
			if isDev() {
				note = "Development build. It keeps its own register, and printing is off unless it is switched on."
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(struct {
				Version string `json:"version"`
				Note    string `json:"note"`
			}{version, note})
			return
		}
		// The page says what it is called, for the title bar.
		if r.Method == http.MethodGet && r.URL.Path == "/shell/title" {
			title := strings.TrimSpace(r.URL.Query().Get("t"))
			if len(title) > 200 {
				title = title[:200]
			}
			pageTitle(strings.ToValidUTF8(title, ""))
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// window.open(url): the file is fetched from the desk and opened in
		// its own app (a PDF in Preview). Only the desk's own addresses.
		if r.Method == http.MethodGet && r.URL.Path == "/shell/open" {
			target := r.URL.Query().Get("u")
			if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
				http.Error(w, "not one of the desk's own files", http.StatusBadRequest)
				return
			}
			body, name, ok := fetch(desk, r, target)
			if !ok {
				http.Error(w, "the file could not be read", http.StatusBadGateway)
				return
			}
			if test := os.Getenv("BANYAN_DOWNLOADS_DIR"); test != "" {
				// Tests: the file is kept, and no other app is opened.
				_, _ = saveToDownloads(name, body)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			dir, err := os.MkdirTemp("", "banyan-open-")
			if err == nil {
				file := filepath.Join(dir, name)
				if os.WriteFile(file, body, 0o600) == nil {
					_ = exec.Command("/usr/bin/open", file).Start()
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// A download link (a CSV, the monthly PDF): the file is fetched from
		// the desk, saved to Downloads and shown there.
		if r.Method == http.MethodGet && r.URL.Path == "/shell/save" {
			target := r.URL.Query().Get("u")
			if !isDownload(target) {
				http.Error(w, "not one of the desk's exports", http.StatusBadRequest)
				return
			}
			body, name, ok := fetch(desk, r, target)
			if !ok {
				http.Error(w, "the file could not be read", http.StatusBadGateway)
				return
			}
			file, err := saveToDownloads(name, body)
			if err != nil {
				http.Error(w, "the file could not be saved to Downloads", http.StatusInternalServerError)
				return
			}
			if os.Getenv("BANYAN_DOWNLOADS_DIR") == "" {
				reveal(file)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// The web view must ask every time. It keeps its own cache between
		// launches, and a page kept from before an update would be the old app.
		// The desk is in this process, so asking costs nothing.
		w.Header().Set("Cache-Control", "no-cache")
		desk.ServeHTTP(w, r)
	})
}

// isDownload is true for the desk's export addresses, with or without a query.
func isDownload(target string) bool {
	p, _, _ := strings.Cut(target, "?")
	return strings.HasPrefix(p, "/api/") && !strings.Contains(p, "..") &&
		(strings.HasSuffix(p, ".csv") || strings.HasSuffix(p, ".pdf"))
}

// fetch asks the desk for one of its own addresses and returns the body and a
// file name for it. Only a 200 counts.
func fetch(desk http.Handler, from *http.Request, target string) (body []byte, name string, ok bool) {
	request, err := http.NewRequestWithContext(from.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil, "", false
	}
	reply := httptest.NewRecorder()
	desk.ServeHTTP(reply, request)
	if reply.Code != http.StatusOK {
		return nil, "", false
	}
	name = path.Base(request.URL.Path)
	if _, params, err := mime.ParseMediaType(reply.Header().Get("Content-Disposition")); err == nil && params["filename"] != "" {
		name = params["filename"]
	}
	// A name from a header is only ever a name, never a path.
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = "download"
	}
	return reply.Body.Bytes(), name, true
}

// saveToDownloads writes the file under a name that is not already taken.
func saveToDownloads(name string, body []byte) (string, error) {
	// BANYAN_DOWNLOADS_DIR is for tests: files go there and Finder is left alone.
	dir := os.Getenv("BANYAN_DOWNLOADS_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, "Downloads")
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 1; ; n++ {
		candidate := filepath.Join(dir, name)
		if n > 1 {
			candidate = filepath.Join(dir, stem+" "+itoa(n)+ext)
		}
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := file.Write(body)
		cerr := file.Close()
		if werr != nil {
			return "", werr
		}
		return candidate, cerr
	}
}

func itoa(n int) string {
	digits := ""
	for ; n > 0; n /= 10 {
		digits = string(rune('0'+n%10)) + digits
	}
	return digits
}
