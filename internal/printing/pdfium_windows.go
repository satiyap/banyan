package printing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PDFium draws the rendered PDF onto the printer's device context. It is
// Chromium's own PDF engine, shipped beside Banyan.exe, and it is the build
// below and no other: the one the Windows checks ran against.
//
// Release: https://github.com/bblanchon/pdfium-binaries/releases/tag/chromium%2F8111
const (
	PdfiumVersion = "chromium/8111"
	pdfiumSHA256  = "a318c39c137482dc4687c2973b7ec1c27fc1c20253cf2dc31928f922eab89e8f"
)

// PdfiumEnv names a pdfium.dll to use instead of the one beside the app.
// For tests and development; it is checked against the same hash.
const PdfiumEnv = "BANYAN_PDFIUM"

const (
	fpdfAnnot    = 0x01
	fpdfPrinting = 0x800
)

type pdfium struct {
	loadMem, closeDoc, pageCount, pageSize, loadPage, closePage, renderPage, lastError *windows.LazyProc
	bitmapCreate, bitmapFill, bitmapBuffer, bitmapStride, bitmapDestroy, renderBitmap  *windows.LazyProc
}

var (
	pdfiumOnce sync.Once
	pdfiumLib  *pdfium
	pdfiumErr  error
	// PDFium is not thread-safe: one document at a time.
	pdfiumMu sync.Mutex
)

func pdfiumPath() string {
	if p := os.Getenv(PdfiumEnv); p != "" {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "pdfium.dll")
}

func loadPdfium() (*pdfium, error) {
	pdfiumOnce.Do(func() {
		path := pdfiumPath()
		data, err := os.ReadFile(path)
		if err != nil {
			pdfiumErr = errors.New("pdfium.dll is missing beside Banyan.exe; reinstall Banyan")
			return
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != pdfiumSHA256 {
			pdfiumErr = errors.New("pdfium.dll beside Banyan.exe is not the one Banyan was tested with; reinstall Banyan")
			return
		}
		dll := windows.NewLazyDLL(path)
		if err := dll.Load(); err != nil {
			pdfiumErr = errors.New("pdfium.dll could not be loaded: " + err.Error())
			return
		}
		p := &pdfium{
			loadMem:       dll.NewProc("FPDF_LoadMemDocument"),
			closeDoc:      dll.NewProc("FPDF_CloseDocument"),
			pageCount:     dll.NewProc("FPDF_GetPageCount"),
			pageSize:      dll.NewProc("FPDF_GetPageSizeByIndex"),
			loadPage:      dll.NewProc("FPDF_LoadPage"),
			closePage:     dll.NewProc("FPDF_ClosePage"),
			renderPage:    dll.NewProc("FPDF_RenderPage"),
			lastError:     dll.NewProc("FPDF_GetLastError"),
			bitmapCreate:  dll.NewProc("FPDFBitmap_Create"),
			bitmapFill:    dll.NewProc("FPDFBitmap_FillRect"),
			bitmapBuffer:  dll.NewProc("FPDFBitmap_GetBuffer"),
			bitmapStride:  dll.NewProc("FPDFBitmap_GetStride"),
			bitmapDestroy: dll.NewProc("FPDFBitmap_Destroy"),
			renderBitmap:  dll.NewProc("FPDF_RenderPageBitmap"),
		}
		initLib := dll.NewProc("FPDF_InitLibrary")
		if err := initLib.Find(); err != nil {
			pdfiumErr = errors.New("pdfium.dll is not usable: " + err.Error())
			return
		}
		initLib.Call()
		pdfiumLib = p
	})
	return pdfiumLib, pdfiumErr
}

// pdfDoc is one loaded PDF. PDFium reads from the buffer for as long as the
// document is open, so the bytes live outside Go's heap until it is closed.
type pdfDoc struct {
	lib    *pdfium
	handle uintptr
	buf    windows.Handle
}

func openPDF(lib *pdfium, data []byte) (*pdfDoc, error) {
	if len(data) == 0 {
		return nil, errors.New("the PDF is empty")
	}
	const lmemFixed = 0
	mem, err := windows.LocalAlloc(lmemFixed, uint32(len(data)))
	if err != nil {
		return nil, err
	}
	copy(foreign(mem, len(data)), data)
	h, _, _ := lib.loadMem.Call(mem, uintptr(len(data)), 0)
	if h == 0 {
		code, _, _ := lib.lastError.Call()
		windows.LocalFree(windows.Handle(mem))
		return nil, errors.New("the PDF could not be read (PDFium error " + itoa(int(code)) + ")")
	}
	return &pdfDoc{lib: lib, handle: h, buf: windows.Handle(mem)}, nil
}

func (d *pdfDoc) close() {
	d.lib.closeDoc.Call(d.handle)
	windows.LocalFree(d.buf)
}

func (d *pdfDoc) pages() int {
	n, _, _ := d.lib.pageCount.Call(d.handle)
	return int(int32(n))
}

// sizeMM is a page's size in millimetres.
func (d *pdfDoc) sizeMM(index int) (w, h float64, ok bool) {
	var wPt, hPt float64
	r, _, _ := d.lib.pageSize.Call(d.handle, uintptr(index), uintptr(unsafe.Pointer(&wPt)), uintptr(unsafe.Pointer(&hPt)))
	if r == 0 {
		return 0, 0, false
	}
	return wPt / 72 * 25.4, hPt / 72 * 25.4, true
}

func (d *pdfDoc) page(index int) (uintptr, error) {
	p, _, _ := d.lib.loadPage.Call(d.handle, uintptr(index))
	if p == 0 {
		return 0, errors.New("the PDF's page could not be read")
	}
	return p, nil
}

// grey renders one page to a greyscale raster, width x height pixels, for
// checks that measure where ink lands.
func (d *pdfDoc) grey(index, width, height int) ([]byte, error) {
	page, err := d.page(index)
	if err != nil {
		return nil, err
	}
	defer d.lib.closePage.Call(page)
	bmp, _, _ := d.lib.bitmapCreate.Call(uintptr(width), uintptr(height), 0)
	if bmp == 0 {
		return nil, errors.New("no bitmap")
	}
	defer d.lib.bitmapDestroy.Call(bmp)
	d.lib.bitmapFill.Call(bmp, 0, 0, uintptr(width), uintptr(height), 0xFFFFFFFF)
	d.lib.renderBitmap.Call(bmp, page, 0, 0, uintptr(width), uintptr(height), 0, fpdfAnnot)
	buf, _, _ := d.lib.bitmapBuffer.Call(bmp)
	stride, _, _ := d.lib.bitmapStride.Call(bmp)
	src := foreign(buf, int(stride)*height)
	out := make([]byte, width*height)
	for y := 0; y < height; y++ {
		row := src[y*int(stride):]
		for x := 0; x < width; x++ {
			b, g, r := int(row[x*4]), int(row[x*4+1]), int(row[x*4+2])
			out[y*width+x] = byte((r*299 + g*587 + b*114) / 1000)
		}
	}
	return out, nil
}

// foreign is n bytes of memory that Windows or PDFium allocated, outside
// Go's heap, so the garbage collector neither moves nor frees it.
func foreign(addr uintptr, n int) []byte {
	return unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&addr))), n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := ""
	for ; n > 0; n /= 10 {
		digits = string(rune('0'+n%10)) + digits
	}
	if neg {
		return "-" + digits
	}
	return digits
}
