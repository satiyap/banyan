// Package webpdf turns a local HTML file into a PDF. On Windows that is
// Microsoft Edge, which every Windows 10 and 11 computer has, run headless with
// the flags the first desk was verified with against a ruler. Edge is
// Chromium, the engine Banyan's geometry was first measured through. Nothing is
// sent to a printer and no print dialog is involved.
//
// The page's own @page size is honoured as is: a cheque declares 92 x 204mm
// with no margin, and the page box of every result is checked by the caller.
package webpdf

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Timeout is how long one page gets: Edge starting cold on a slow computer
// takes a few seconds before it has drawn anything.
const Timeout = 90 * time.Second

// BrowserEnv names a Chromium-based browser to render with instead of Edge.
// For development; the page box check applies whatever renders.
const BrowserEnv = "BANYAN_BROWSER"

// Available reports whether this computer can render at all.
var Available = Browser() != ""

// Page is the sheet a document is rendered onto, in millimetres.
type Page struct {
	W, H float64
	// Paged lets the document break across sheets (a report). When false the
	// document is one sheet at exact scale (a cheque), sized by its own @page.
	Paged bool
	// Margins of a paged document: top, right, bottom, left.
	Margins [4]float64
}

// Browser is the browser that renders, or "" when there is none.
func Browser() string {
	if b := os.Getenv(BrowserEnv); b != "" {
		if _, err := os.Stat(b); err == nil {
			return b
		}
		return ""
	}
	var candidates []string
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LocalAppData")} {
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
	}
	// Edge first wherever it is, then Chrome.
	for _, edgeFirst := range []bool{true, false} {
		for _, c := range candidates {
			if strings.HasSuffix(c, "msedge.exe") != edgeFirst {
				continue
			}
			if info, err := os.Stat(c); err == nil && info.Mode().IsRegular() {
				return c
			}
		}
	}
	return ""
}

var headRe = regexp.MustCompile(`(?i)<head[^>]*>`)

func mm(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "mm" }

// pagedCopy writes the report beside itself with its sheet and margins
// declared, since a headless print takes them from the page's own @page.
func pagedCopy(htmlPath string, page Page) (string, error) {
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		return "", err
	}
	m := page.Margins
	style := "<style>@page{size:" + mm(page.W) + " " + mm(page.H) + ";margin:" +
		mm(m[0]) + " " + mm(m[1]) + " " + mm(m[2]) + " " + mm(m[3]) + "}</style>"
	text := string(html)
	if loc := headRe.FindStringIndex(text); loc != nil {
		text = text[:loc[1]] + style + text[loc[1]:]
	} else {
		text = style + text
	}
	out := strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)) + ".paged.html"
	return out, os.WriteFile(out, []byte(text), 0o600)
}

// Render writes htmlPath as a PDF at pdfPath.
func Render(htmlPath, pdfPath string, page Page) error {
	browser := Browser()
	if browser == "" {
		return errors.New("Microsoft Edge is needed to draw cheques and was not found")
	}
	source, err := filepath.Abs(htmlPath)
	if err != nil {
		return err
	}
	if page.Paged {
		copyPath, err := pagedCopy(source, page)
		if err != nil {
			return errors.New("the report page could not be prepared: " + err.Error())
		}
		defer os.Remove(copyPath)
		source = copyPath
	}
	out, err := filepath.Abs(pdfPath)
	if err != nil {
		return err
	}
	_ = os.Remove(out)
	target := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(source)}).String()

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	// The flags the first desk was verified with. None may touch page size or
	// scaling: the page box check after every render would refuse it.
	cmd := exec.CommandContext(ctx, browser,
		"--headless=new", "--disable-gpu", "--no-pdf-header-footer",
		"--print-to-pdf="+out, "--virtual-time-budget=6000", target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	var stderr strings.Builder
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return errors.New("the renderer timed out")
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		tail := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(tail, "\n"); i >= 0 {
			tail = tail[i+1:]
		}
		if tail == "" && runErr != nil {
			tail = runErr.Error()
		}
		return errors.New("no PDF was produced: " + tail)
	}
	return nil
}

// ServeMainThread waits for done. Rendering here runs in its own process, so
// nothing needs the main thread.
func ServeMainThread(done <-chan struct{}) { <-done }
