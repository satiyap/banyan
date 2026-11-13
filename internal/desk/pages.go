package desk

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The media type of each kind of file the desk serves. Anything it cannot
// name goes out as text/plain.
var mediaTypes = map[string]string{
	".html":  "text/html",
	".htm":   "text/html",
	".css":   "text/css",
	".js":    "text/javascript",
	".mjs":   "text/javascript",
	".json":  "application/json",
	".txt":   "text/plain",
	".csv":   "text/csv",
	".xml":   "text/xml",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".ico":   "image/vnd.microsoft.icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".pdf":   "application/pdf",
	".wasm":  "application/wasm",
}

// served is a file about to be sent: its name, bytes and modification time.
type served struct {
	name string
	data []byte
	mod  time.Time // zero for a file compiled into the app
}

func osFile(path string) (*served, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &served{name: path, data: data, mod: info.ModTime()}, nil
}

func assetFile(assets fs.FS, name string) (*served, error) {
	info, err := fs.Stat(assets, name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fs.ErrNotExist
	}
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		return nil, err
	}
	return &served{name: name, data: data, mod: info.ModTime()}, nil
}

type fileOptions struct {
	mediaType string
	filename  string
	// Set for the static mount: a browser that already holds this copy is
	// told so instead of being sent it again.
	request *http.Request
}

// fileResponse is Starlette's FileResponse: the file, its type, and the
// validators a browser caches by.
func fileResponse(f *served, o fileOptions) *response {
	kind := o.mediaType
	if kind == "" {
		kind = mediaTypes[strings.ToLower(path.Ext(f.name))]
	}
	if kind == "" {
		kind = "text/plain"
	}
	if strings.HasPrefix(kind, "text/") {
		kind += "; charset=utf-8"
	}
	// A file on disk is known by when it changed and its size; one compiled
	// into the app has no time of its own and is known by its bytes.
	var tag [md5.Size]byte
	lastModified := ""
	if f.mod.IsZero() {
		tag = md5.Sum(f.data)
	} else {
		tag = md5.Sum([]byte(fmt.Sprintf("%d-%d", f.mod.UnixNano(), len(f.data))))
		lastModified = f.mod.UTC().Truncate(time.Second).Format(http.TimeFormat)
	}
	etag := `"` + hex.EncodeToString(tag[:]) + `"`

	validators := [][2]string{}
	if lastModified != "" {
		validators = append(validators, [2]string{"Last-Modified", lastModified})
	}
	validators = append(validators, [2]string{"ETag", etag})
	if o.filename != "" {
		validators = append(validators, [2]string{"Content-Disposition", `attachment; filename="` + o.filename + `"`})
	}

	if r := o.request; r != nil {
		unchanged := false
		if noneMatch := r.Header.Get("If-None-Match"); noneMatch != "" {
			for _, candidate := range strings.Split(noneMatch, ",") {
				if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == etag {
					unchanged = true
				}
			}
		} else if since := r.Header.Get("If-Modified-Since"); since != "" && lastModified != "" {
			if at, err := http.ParseTime(since); err == nil {
				unchanged = !at.Before(f.mod.UTC().Truncate(time.Second))
			}
		}
		if unchanged {
			return &response{status: http.StatusNotModified, header: validators}
		}
	}
	header := append([][2]string{
		{"Content-Type", kind},
		{"Content-Length", fmt.Sprint(len(f.data))},
	}, validators...)
	// ServeHTTP leaves the body out of the answer to a HEAD.
	return &response{status: http.StatusOK, header: header, body: f.data}
}

// extname is the suffix a file's type is read from; a name that is only a
// dot-name has none.
func extname(name string) string {
	base := path.Base(name)
	ext := path.Ext(base)
	if ext == base {
		return ""
	}
	return ext
}

var selfHash struct {
	once sync.Once
	sum  []byte
}

// ownCode is a digest of the program this code is running from, or nil where
// its file cannot be read.
func ownCode() []byte {
	selfHash.once.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		file, err := os.Open(exe)
		if err != nil {
			return
		}
		defer file.Close()
		h := sha256.New()
		if _, err := io.Copy(h, file); err == nil {
			selfHash.sum = h.Sum(nil)
		}
	})
	return selfHash.sum
}

// deskVersion is a fingerprint of the page and the API it was written against.
//
// Computed rather than hand-bumped, so it cannot be forgotten: any release
// that changes the frontend or the API gets a new one, and a tab still holding
// the previous page is refused and reloads instead of calling an API it no
// longer matches — which once turned every entity edit into a duplicate.
//
// What is hashed is the frontend files, the print template, and the server's
// own code: the app is one program, so a digest of the file this code is
// running from covers all of it.
func deskVersion(assets fs.FS) (string, error) {
	h := sha256.New()
	folder := func(name string, suffixes ...string) error {
		var files []string
		err := fs.WalkDir(assets, name, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				files = append(files, strings.TrimPrefix(p, name+"/"))
			}
			return nil
		})
		if err != nil {
			return err
		}
		sort.Strings(files)
		for _, rel := range files {
			// The leaf photo is data, not a release: it never changes the version.
			if path.Base(rel) == "leaf-photo.jpg" || !contains(suffixes, extname(rel)) {
				continue
			}
			data, err := fs.ReadFile(assets, name+"/"+rel)
			if err != nil {
				return err
			}
			h.Write([]byte(name + "/" + rel))
			h.Write(data)
		}
		return nil
	}
	if err := folder("static", ".js", ".css", ".html"); err != nil {
		return "", err
	}
	if err := folder("templates", ".tpl"); err != nil {
		return "", err
	}
	// Where this code's own file cannot be read, the frontend and template still count.
	if own := ownCode(); own != nil {
		h.Write([]byte("server"))
		h.Write(own)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

var staticAddress = regexp.MustCompile(`((?:src|href)="/static/[^"?]+)"`)

// Page is a page from the frontend folder, stamped with the version it belongs to.
//
// Its scripts and stylesheets are addressed with ?v=<version>, so a new
// release is a URL the browser has never cached — normal caching stays on, and
// a reload after an update cannot pick up the previous app.js.
func (a *App) Page(name string) (string, error) {
	data, err := fs.ReadFile(a.cfg.Assets, "static/"+name)
	if err != nil {
		return "", err
	}
	html := staticAddress.ReplaceAllString(string(data), `${1}?v=`+a.version+`"`)
	return strings.Replace(html, "<head>", "<head>\n"+`<meta name="desk-version" content="`+a.version+`">`, 1), nil
}

func htmlResponse(html string) *response {
	return &response{
		status: http.StatusOK,
		header: [][2]string{{"Content-Type", "text/html; charset=utf-8"}},
		body:   []byte(html),
	}
}

// Connected with a read key, the app is the members' report pages and nothing else.
func (a *App) indexPage(rq *req) (*response, error) {
	if !a.syncer.ReadOnly() {
		return htmlResponse(a.indexHTML), nil
	}
	a.pageMu.Lock()
	defer a.pageMu.Unlock()
	if a.readerHTML == "" {
		html, err := a.Page("reader.html")
		if err != nil {
			return nil, err
		}
		a.readerHTML = html
	}
	return htmlResponse(a.readerHTML), nil
}

// The desktop app asks once, on first launch, for the API URL and user key of
// the Banyan server, and can be told to skip.
func (a *App) setupPage(rq *req) (*response, error) {
	a.pageMu.Lock()
	defer a.pageMu.Unlock()
	if a.setupHTML == "" {
		html, err := a.Page("setup.html")
		if err != nil {
			return nil, refuse(http.StatusNotFound, "Not Found")
		}
		a.setupHTML = html
	}
	return htmlResponse(a.setupHTML), nil
}

// The preview backdrop is a photograph of a real leaf with the account, the
// account name and the MICR band blacked out, the anti-copy patch blurred and
// the resolution halved. A copy ships with the app; one kept with the desk's
// data is served in preference to it, and the request 404s (the frontend then
// draws its guide) when neither exists.
func (a *App) leafPhoto(rq *req) (*response, error) {
	if a.cfg.DataStaticDir != "" {
		candidate := filepath.Join(a.cfg.DataStaticDir, "leaf-photo.jpg")
		if isFile(candidate) {
			if f, err := osFile(candidate); err == nil {
				return fileResponse(f, fileOptions{}), nil
			}
		}
	}
	if f, err := assetFile(a.cfg.Assets, "static/leaf-photo.jpg"); err == nil {
		return fileResponse(f, fileOptions{}), nil
	}
	return nil, refuse(http.StatusNotFound, "no leaf photo prepared")
}

// staticFile serves the frontend's files. A path that climbs out of the
// folder is simply not found.
func (a *App) staticFile(r *http.Request, sub string) (*response, error) {
	if !reading(r.Method) {
		return nil, refuse(http.StatusMethodNotAllowed, "Method Not Allowed")
	}
	notFound := refuse(http.StatusNotFound, "Not Found")
	clean := path.Join(strings.Split(sub, "/")...)
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, notFound
	}
	name := "static/" + clean
	if !fs.ValidPath(name) {
		return nil, notFound
	}
	f, err := assetFile(a.cfg.Assets, name)
	if err != nil {
		return nil, notFound
	}
	return fileResponse(f, fileOptions{request: r}), nil
}
