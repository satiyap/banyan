//go:build !darwin

// Package webpdf renders HTML to PDF with the Mac's own WebKit. Off the Mac
// there is no renderer, and every call says so.
package webpdf

import "errors"

// Available reports whether this build can render at all.
const Available = false

var errNone = errors.New("no PDF renderer on this system")

// Page is the sheet a document is rendered onto, in millimetres.
type Page struct {
	W, H    float64
	Paged   bool
	Margins [4]float64
}

func Render(htmlPath, pdfPath string, page Page) error { return errNone }

func ServeMainThread(done <-chan struct{}) { <-done }
