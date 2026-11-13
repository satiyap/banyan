//go:build darwin

// Package webpdf turns a local HTML file into a PDF with the Mac's own WebKit,
// the engine that also draws the desk's on-screen preview. Nothing is sent to
// a printer and no print dialog is involved.
//
// A single page (a cheque) is captured as vector PDF and redrawn at exactly
// 1 CSS px = 1/96 inch onto a page of the stated size, so a millimetre in the
// page is a millimetre on paper. A paged document (a report) goes through
// WebKit's own pagination onto sheets of the stated size.
package webpdf

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#include "render.h"
*/
import "C"

import (
	"errors"
	"sync"
	"time"
	"unsafe"
)

// Timeout is how long one page gets to load, settle and be written.
const Timeout = 20 * time.Second

// Available reports whether this build can render at all.
const Available = true

var (
	mu      sync.Mutex
	nextID  uintptr
	waiting = map[uintptr]chan error{}
)

//export banyanRenderDone
func banyanRenderDone(handle C.uintptr_t, msg *C.char) {
	var err error
	if msg != nil {
		err = errors.New(C.GoString(msg))
		C.free(unsafe.Pointer(msg))
	}
	mu.Lock()
	done := waiting[uintptr(handle)]
	delete(waiting, uintptr(handle))
	mu.Unlock()
	if done != nil {
		done <- err
	}
}

// Page is the sheet a document is rendered onto, in millimetres.
type Page struct {
	W, H float64
	// Paged lets WebKit break the document across sheets (a report). When
	// false the document is one sheet at exact scale (a cheque).
	Paged bool
	// Margins of a paged document: top, right, bottom, left.
	Margins [4]float64
}

// Render writes htmlPath as a PDF at pdfPath. It must be called off the main
// thread, in a process whose main run loop is running: the desktop app, or a
// program that calls ServeMainThread.
func Render(htmlPath, pdfPath string, page Page) error {
	done := make(chan error, 1)
	mu.Lock()
	nextID++
	id := nextID
	waiting[id] = done
	mu.Unlock()

	html, pdf := C.CString(htmlPath), C.CString(pdfPath)
	defer C.free(unsafe.Pointer(html))
	defer C.free(unsafe.Pointer(pdf))
	paged := C.int(0)
	if page.Paged {
		paged = 1
	}
	margins := [4]C.double{C.double(page.Margins[0]), C.double(page.Margins[1]), C.double(page.Margins[2]), C.double(page.Margins[3])}
	C.banyanRenderAsync(html, pdf, C.double(page.W), C.double(page.H), paged, &margins[0], C.uintptr_t(id))

	select {
	case err := <-done:
		return err
	case <-time.After(Timeout):
		mu.Lock()
		delete(waiting, id)
		mu.Unlock()
		return errors.New("the renderer timed out")
	}
}

// ServeMainThread runs the main run loop until done is closed, so Render works
// in a program that has no app of its own (a test, a command-line check). Call
// it on the main thread: lock it with runtime.LockOSThread in an init function.
func ServeMainThread(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
			C.banyanPumpMainLoop(0.02)
		}
	}
}
