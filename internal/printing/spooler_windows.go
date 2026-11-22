package printing

// The print system on Windows: the spooler, driven directly.
//
// Windows has no `lp -o scaling=100`. The equivalent is to open a device
// context on the printer with the page size and scale set in its DEVMODE, ask
// the driver what sheet it actually gave us, and draw the verified PDF onto
// that sheet at exactly 1:1 with PDFium. The driver's answer is checked before
// a job exists: a driver that would not take a 92 x 204mm page (or quietly
// rotated or substituted one) is refused here, never printed on.
//
// The page is drawn from the sheet's physical corner, not the printable
// area's, so a millimetre in the PDF lands where it does through CUPS: the
// printer's non-printable margin clips, it does not shift.

import (
	"errors"
	"math"
	"net"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winspool = windows.NewLazySystemDLL("winspool.drv")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	procOpenPrinter        = winspool.NewProc("OpenPrinterW")
	procClosePrinter       = winspool.NewProc("ClosePrinter")
	procDocumentProperties = winspool.NewProc("DocumentPropertiesW")
	procEnumPrinters       = winspool.NewProc("EnumPrintersW")
	procGetDefaultPrinter  = winspool.NewProc("GetDefaultPrinterW")
	procGetJob             = winspool.NewProc("GetJobW")
	procSetJob             = winspool.NewProc("SetJobW")
	procAddForm            = winspool.NewProc("AddFormW")

	procCreateDC      = gdi32.NewProc("CreateDCW")
	procDeleteDC      = gdi32.NewProc("DeleteDC")
	procGetDeviceCaps = gdi32.NewProc("GetDeviceCaps")
	procStartDoc      = gdi32.NewProc("StartDocW")
	procEndDoc        = gdi32.NewProc("EndDoc")
	procAbortDoc      = gdi32.NewProc("AbortDoc")
	procStartPage     = gdi32.NewProc("StartPage")
	procEndPage       = gdi32.NewProc("EndPage")
)

const (
	printerEnumLocal       = 0x2
	printerEnumConnections = 0x4

	printerStatusPaused       = 0x1
	printerStatusError        = 0x2
	printerStatusPaperJam     = 0x8
	printerStatusPaperOut     = 0x10
	printerStatusPaperProblem = 0x40
	printerStatusOffline      = 0x80
	printerStatusIOActive     = 0x100
	printerStatusBusy         = 0x200
	printerStatusPrinting     = 0x400
	printerStatusNotAvailable = 0x1000
	printerStatusProcessing   = 0x4000
	printerAttrWorkOffline    = 0x400

	jobStatusDeleting = 0x4
	jobStatusPrinting = 0x10
	jobStatusPrinted  = 0x80
	jobStatusDeleted  = 0x100
	jobStatusComplete = 0x1000

	jobControlDelete = 5

	dmOrientation = 0x1
	dmPaperSize   = 0x2
	dmPaperLength = 0x4
	dmPaperWidth  = 0x8
	dmScale       = 0x10
	dmCopies      = 0x100
	dmFormName    = 0x10000

	dmPaperUser      = 256
	dmOrientPortrait = 1

	dmOutBuffer = 2
	dmInBuffer  = 8

	capLogPixelsX      = 88
	capLogPixelsY      = 90
	capPhysicalWidth   = 110
	capPhysicalHeight  = 111
	capPhysicalOffsetX = 112
	capPhysicalOffsetY = 113
)

// FormName is the paper size Banyan adds to Windows when a driver will not
// take a custom size on its own.
const FormName = "Banyan cheque 92 x 204 mm"

// The public part of DEVMODEW; the driver's private part follows it in the buffer.
type devMode struct {
	DeviceName       [32]uint16
	SpecVersion      uint16
	DriverVersion    uint16
	Size             uint16
	DriverExtra      uint16
	Fields           uint32
	Orientation      int16
	PaperSize        int16
	PaperLength      int16
	PaperWidth       int16
	Scale            int16
	Copies           int16
	DefaultSource    int16
	PrintQuality     int16
	Color            int16
	Duplex           int16
	YResolution      int16
	TTOption         int16
	Collate          int16
	FormName         [32]uint16
	LogPixels        uint16
	BitsPerPel       uint32
	PelsWidth        uint32
	PelsHeight       uint32
	DisplayFlags     uint32
	DisplayFrequency uint32
	ICMMethod        uint32
	ICMIntent        uint32
	MediaType        uint32
	DitherType       uint32
	Reserved1        uint32
	Reserved2        uint32
	PanningWidth     uint32
	PanningHeight    uint32
}

type printerInfo2 struct {
	ServerName, PrinterName, ShareName, PortName, DriverName, Comment, Location *uint16
	DevMode                                                                     uintptr
	SepFile, PrintProcessor, Datatype, Parameters                               *uint16
	SecurityDescriptor                                                          uintptr
	Attributes, Priority, DefaultPriority, StartTime, UntilTime, Status         uint32
	Jobs, AveragePPM                                                            uint32
}

type jobInfo1 struct {
	JobID                                                       uint32
	PrinterName, MachineName, UserName, Document, Datatype, Msg *uint16
	Status, Priority, Position, TotalPages, PagesPrinted        uint32
	Submitted                                                   windows.Systemtime
}

type docInfo struct {
	Size     int32
	DocName  *uint16
	Output   *uint16
	Datatype *uint16
	Type     uint32
}

type formInfo1 struct {
	Flags uint32
	Name  *uint16
	Cx    int32 // thousandths of a millimetre
	Cy    int32
	Left  int32
	Top   int32
	Right int32
	Bot   int32
}

func CupsAvailable() bool {
	if hidden.Load() {
		return false
	}
	return procEnumPrinters.Find() == nil
}

func openPrinter(name string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	var h windows.Handle
	r, _, callErr := procOpenPrinter.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&h)), 0)
	if r == 0 {
		return 0, callErr
	}
	return h, nil
}

func closePrinter(h windows.Handle) { procClosePrinter.Call(uintptr(h)) }

func defaultPrinter() string {
	var size uint32
	procGetDefaultPrinter.Call(0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return ""
	}
	buf := make([]uint16, size)
	r, _, _ := procGetDefaultPrinter.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

func enumPrinters() []printerInfo2 {
	flags := uintptr(printerEnumLocal | printerEnumConnections)
	var needed, returned uint32
	procEnumPrinters.Call(flags, 0, 2, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if needed == 0 {
		return nil
	}
	// Pointer-aligned: the buffer holds the structs and the strings they point into.
	buf := make([]uintptr, (needed+7)/8)
	r, _, _ := procEnumPrinters.Call(flags, 0, 2, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed),
		uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if r == 0 || returned == 0 {
		return nil
	}
	infos := unsafe.Slice((*printerInfo2)(unsafe.Pointer(&buf[0])), returned)
	out := make([]printerInfo2, len(infos))
	copy(out, infos)
	// The strings live in buf: keep it for as long as out is read.
	runtime.KeepAlive(buf)
	return out
}

var ipRe = regexp.MustCompile(`(?:^|[^0-9])((?:\d{1,3}\.){3}\d{1,3})(?:[^0-9]|$)`)

// portOnline knocks on a network printer named by its port. A Standard TCP/IP
// port is usually named after the printer's address; anything else (USB, WSD)
// gives nothing to probe and the answer is nil.
func portOnline(port string) *bool {
	m := ipRe.FindStringSubmatch(port)
	if m == nil || net.ParseIP(m[1]) == nil {
		return nil
	}
	// Raw (9100), IPP (631) and the printer's web page (80): one answering is enough.
	answers := make(chan bool, 3)
	for _, p := range []string{"9100", "631", "80"} {
		go func(p string) { answers <- dial(net.JoinHostPort(m[1], p), probeTimeout) }(p)
	}
	online := false
	for range 3 {
		if <-answers {
			online = true
		}
	}
	return &online
}

// ListPrinters returns the print queues. With probe, it also checks each
// device is reachable. The result is never nil.
func ListPrinters(probe bool) []Printer {
	printers := []Printer{}
	if !CupsAvailable() {
		return printers
	}
	def := defaultPrinter()
	for _, info := range enumPrinters() {
		name := windows.UTF16PtrToString(info.PrinterName)
		if name == "" {
			continue
		}
		state := "idle"
		var online *bool
		switch s := info.Status; {
		case s&(printerStatusOffline|printerStatusNotAvailable) != 0 || info.Attributes&printerAttrWorkOffline != 0:
			state = "offline"
			no := false
			online = &no
		case s&printerStatusPaused != 0:
			state = "paused"
		case s&(printerStatusError|printerStatusPaperJam|printerStatusPaperOut|printerStatusPaperProblem) != 0:
			state = "error"
		case s&(printerStatusPrinting|printerStatusProcessing|printerStatusBusy|printerStatusIOActive) != 0:
			state = "printing"
		}
		if probe && online == nil {
			online = portOnline(windows.UTF16PtrToString(info.PortName))
			if online != nil && !*online {
				state = "offline"
			}
		}
		printers = append(printers, Printer{Name: name, State: state, IsDefault: name == def, Online: online})
	}
	return printers
}

// Submit hands the PDF to the spooler and returns the job id.
//
// Scaling and fit-to-page are pinned off. If the driver will not give a page
// of exactly the PDF's size the job fails here rather than printing a subtly
// resized cheque.
func Submit(pdf, printer string, pageWMm, pageHMm float64, title string) (string, error) {
	if _, err := os.Stat(pdf); err != nil {
		return "", printError("the rendered PDF is missing")
	}
	if disabled.Load() {
		return "", printError("printing is switched off in this build, so nothing was sent to the printer")
	}
	if !CupsAvailable() {
		return "", printError("the Windows print spooler is not available on this computer")
	}
	job, err := spool(pdf, printer, pageWMm, pageHMm, title, "")
	if err != nil {
		return "", err
	}
	return printer + "-" + strconv.Itoa(job), nil
}

// lastSheet is the sheet the last job was drawn on, for tests to report.
var lastSheet sheet

// sheet is what the driver says a device context's page is, in device pixels.
type sheet struct {
	dpiX, dpiY       int
	physW, physH     int
	offsetX, offsetY int
}

func (s sheet) mm() (w, h float64) {
	return float64(s.physW) / float64(s.dpiX) * 25.4, float64(s.physH) / float64(s.dpiY) * 25.4
}

func deviceCaps(dc uintptr) sheet {
	c := func(i int) int { r, _, _ := procGetDeviceCaps.Call(dc, uintptr(i)); return int(int32(r)) }
	return sheet{
		dpiX: c(capLogPixelsX), dpiY: c(capLogPixelsY),
		physW: c(capPhysicalWidth), physH: c(capPhysicalHeight),
		offsetX: c(capPhysicalOffsetX), offsetY: c(capPhysicalOffsetY),
	}
}

// devModeFor asks the driver for its settings with the page set to wMM x hMM
// at 100%, either as a custom size or as the named Windows form.
func devModeFor(h windows.Handle, name *uint16, wMM, hMM float64, form bool) ([]uintptr, error) {
	size, _, _ := procDocumentProperties.Call(0, uintptr(h), uintptr(unsafe.Pointer(name)), 0, 0, 0)
	if int32(size) <= 0 {
		return nil, errors.New("the printer driver gave no settings")
	}
	buf := make([]uintptr, (int(int32(size))+7)/8+1)
	r, _, _ := procDocumentProperties.Call(0, uintptr(h), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&buf[0])), 0, dmOutBuffer)
	if int32(r) < 0 {
		return nil, errors.New("the printer driver's settings could not be read")
	}
	dm := (*devMode)(unsafe.Pointer(&buf[0]))
	dm.Fields |= dmOrientation | dmScale | dmCopies
	dm.Orientation = dmOrientPortrait
	dm.Scale = 100
	dm.Copies = 1
	if form {
		dm.Fields &^= dmPaperSize | dmPaperLength | dmPaperWidth
		dm.Fields |= dmFormName
		dm.PaperSize = 0
		formName, _ := windows.UTF16FromString(FormName)
		dm.FormName = [32]uint16{}
		copy(dm.FormName[:31], formName)
	} else {
		dm.Fields |= dmPaperSize | dmPaperLength | dmPaperWidth
		dm.PaperSize = dmPaperUser
		dm.PaperWidth = int16(math.Round(wMM * 10)) // tenths of a millimetre
		dm.PaperLength = int16(math.Round(hMM * 10))
	}
	// The driver merges the request into its own settings and says what it will do.
	out := make([]uintptr, len(buf))
	r, _, _ = procDocumentProperties.Call(0, uintptr(h), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&out[0])), uintptr(unsafe.Pointer(&buf[0])), dmInBuffer|dmOutBuffer)
	if int32(r) < 0 {
		return nil, errors.New("the printer driver refused the page settings")
	}
	return out, nil
}

// addForm makes the cheque's paper size known to Windows. Adding a form can
// need an administrator; when it is refused, the custom size is all there is.
func addForm(h windows.Handle, wMM, hMM float64) {
	name, _ := windows.UTF16PtrFromString(FormName)
	w, ht := int32(math.Round(wMM*1000)), int32(math.Round(hMM*1000))
	form := formInfo1{Name: name, Cx: w, Cy: ht, Right: w, Bot: ht}
	procAddForm.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&form)))
}

// printerDC opens a device context whose sheet is wMM x hMM, or says what the
// driver offered instead.
func printerDC(printer string, wMM, hMM float64) (uintptr, sheet, error) {
	h, err := openPrinter(printer)
	if err != nil {
		return 0, sheet{}, printError("printer " + strconv.Quote(printer) + " is not available")
	}
	defer closePrinter(h)
	name, _ := windows.UTF16PtrFromString(printer)
	winspoolName, _ := windows.UTF16PtrFromString("WINSPOOL")

	fits := func(s sheet) bool {
		w, ht := s.mm()
		return s.dpiX > 0 && s.dpiY > 0 && math.Abs(w-wMM) <= 0.5 && math.Abs(ht-hMM) <= 0.5
	}
	var offered sheet
	for _, form := range []bool{false, true} {
		if form {
			addForm(h, wMM, hMM)
		}
		dm, err := devModeFor(h, name, wMM, hMM, form)
		if err != nil {
			return 0, sheet{}, printError(err.Error())
		}
		dc, _, _ := procCreateDC.Call(uintptr(unsafe.Pointer(winspoolName)), uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&dm[0])))
		runtime.KeepAlive(dm)
		if dc == 0 {
			return 0, sheet{}, printError("the printer driver could not be opened for printing")
		}
		s := deviceCaps(dc)
		if fits(s) {
			return dc, s, nil
		}
		offered = s
		procDeleteDC.Call(dc)
	}
	w, ht := offered.mm()
	return 0, sheet{}, printError("the printer driver would not take a " + sixFigures(wMM) + " x " + sixFigures(hMM) +
		"mm page (it offered " + strconv.FormatFloat(w, 'f', 1, 64) + " x " + strconv.FormatFloat(ht, 'f', 1, 64) +
		"mm), so nothing was sent. Add the paper size \"" + FormName + "\" in Windows printer settings, or use the printer's own driver")
}

// spool prints one single-page PDF at exactly 1:1 and returns the spooler's
// job id. output, when set, sends the job to that file instead (a virtual
// printer in tests).
func spool(pdf, printer string, wMM, hMM float64, title, output string) (int, error) {
	lib, err := loadPdfium()
	if err != nil {
		return 0, printError(err.Error())
	}
	data, err := os.ReadFile(pdf)
	if err != nil {
		return 0, printError("the rendered PDF is missing")
	}

	// GDI device contexts belong to the thread that made them.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pdfiumMu.Lock()
	defer pdfiumMu.Unlock()

	doc, err := openPDF(lib, data)
	if err != nil {
		return 0, printError(err.Error())
	}
	defer doc.close()
	if n := doc.pages(); n != 1 {
		return 0, printError("the rendered PDF has " + strconv.Itoa(n) + " pages; a cheque must be exactly one")
	}
	if w, h, ok := doc.sizeMM(0); !ok || math.Abs(w-wMM) > 0.5 || math.Abs(h-hMM) > 0.5 {
		return 0, printError("the rendered PDF is not " + sixFigures(wMM) + " x " + sixFigures(hMM) + "mm — not safe to print")
	}
	page, err := doc.page(0)
	if err != nil {
		return 0, printError(err.Error())
	}
	defer lib.closePage.Call(page)

	dc, s, err := printerDC(printer, wMM, hMM)
	if err != nil {
		return 0, err
	}
	defer procDeleteDC.Call(dc)
	lastSheet = s

	docName, _ := windows.UTF16PtrFromString(title)
	info := docInfo{DocName: docName}
	info.Size = int32(unsafe.Sizeof(info))
	if output != "" {
		info.Output, _ = windows.UTF16PtrFromString(output)
	}
	job, _, callErr := procStartDoc.Call(dc, uintptr(unsafe.Pointer(&info)))
	if int32(job) <= 0 {
		return 0, printError("the spooler would not start the job: " + callErr.Error())
	}
	fail := func(what string) (int, error) {
		procAbortDoc.Call(dc)
		return 0, printError(what)
	}
	if r, _, _ := procStartPage.Call(dc); int32(r) <= 0 {
		return fail("the spooler would not start the page")
	}
	// The whole physical sheet, origin at its corner: the device context's
	// origin is the printable area's corner, so it is stepped back by the
	// driver's offset. Size is the sheet's own pixels, so 1mm stays 1mm.
	lib.renderPage.Call(dc, page,
		uintptr(int64(-s.offsetX)), uintptr(int64(-s.offsetY)),
		uintptr(s.physW), uintptr(s.physH), 0, fpdfAnnot|fpdfPrinting)
	if r, _, _ := procEndPage.Call(dc); int32(r) <= 0 {
		return fail("the spooler would not finish the page")
	}
	if r, _, _ := procEndDoc.Call(dc); int32(r) <= 0 {
		return 0, printError("the spooler would not finish the job")
	}
	return int(int32(job)), nil
}

// splitJob is the printer and job number of an id from Submit.
func splitJob(jobID string) (string, uint32, bool) {
	i := strings.LastIndex(jobID, "-")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.ParseUint(jobID[i+1:], 10, 32)
	if err != nil {
		return "", 0, false
	}
	return jobID[:i], uint32(n), true
}

// The spooler forgets a job once it is done, finished or cancelled alike, so
// the last state seen is kept to say which.
var (
	seenMu sync.Mutex
	seen   = map[string]string{}
)

// JobState says where a job has got to: "active", "finished", "cancelled" or
// "unknown".
//
// "finished" means the spooler is done with the job, not that the cheque is correct.
func JobState(jobID string) string {
	printer, id, ok := splitJob(jobID)
	if !ok || !CupsAvailable() {
		return "unknown"
	}
	h, err := openPrinter(printer)
	if err != nil {
		return "unknown"
	}
	defer closePrinter(h)
	var needed uint32
	procGetJob.Call(uintptr(h), uintptr(id), 1, 0, 0, uintptr(unsafe.Pointer(&needed)))
	state := ""
	if needed > 0 {
		buf := make([]uintptr, (needed+7)/8)
		r, _, _ := procGetJob.Call(uintptr(h), uintptr(id), 1, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed), uintptr(unsafe.Pointer(&needed)))
		if r != 0 {
			status := (*jobInfo1)(unsafe.Pointer(&buf[0])).Status
			switch {
			case status&(jobStatusDeleting|jobStatusDeleted) != 0:
				state = "cancelled"
			case status&(jobStatusPrinted|jobStatusComplete) != 0:
				state = "finished"
			case status&jobStatusPrinting != 0:
				state = "printing"
			default:
				state = "active"
			}
		}
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if state != "" {
		seen[jobID] = state
		if state == "printing" {
			return "active"
		}
		return state
	}
	switch seen[jobID] {
	case "cancelled":
		return "cancelled"
	case "finished", "printing":
		return "finished"
	}
	return "unknown"
}

func Cancel(jobID string) bool {
	printer, id, ok := splitJob(jobID)
	if !ok || !CupsAvailable() || disabled.Load() {
		return false
	}
	h, err := openPrinter(printer)
	if err != nil {
		return false
	}
	defer closePrinter(h)
	r, _, _ := procSetJob.Call(uintptr(h), uintptr(id), 0, 0, jobControlDelete)
	return r != 0
}
