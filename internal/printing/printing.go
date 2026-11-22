// Package printing is print queue submission and job tracking.
//
// The cheque goes to the printer with scaling and fit-to-page pinned off,
// because any resizing at this stage would invalidate every calibrated
// millimetre upstream of it. On a Mac that is `lp` (cups.go); on Windows the
// spooler, with the page size checked on the printer's own device context
// before anything is sent (spooler_windows.go).
//
// A queued job is never recorded as a printed cheque on its own. The print system
// can tell us that it accepted and finished a job; it cannot tell us that the
// toner landed correctly on a bank leaf that was fed by hand. That last step is
// the treasurer's confirmation, and the register waits for it.
package printing

import (
	"errors"
	"math"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/satiyap/banyan/internal/pycompat"
)

const DefaultPrinterEnv = "CHEQUE_PRINTER"

// What JavaScript's \s and String.trim() treat as whitespace; the parsing
// below was written against those.
const (
	jsSpace    = `[\t\n\x0b\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`
	jsNotSpace = `[^\t\n\x0b\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`
)

var (
	jobRe       = regexp.MustCompile(`request id is (` + jsNotSpace + `+)`)
	deviceRe    = regexp.MustCompile(`(?m)^device for (` + jsNotSpace + `+): (` + jsNotSpace + `+)`)
	defaultRe   = regexp.MustCompile(`system default destination:` + jsSpace + `*(` + jsNotSpace + `+)`)
	printerRe   = regexp.MustCompile(`^printer (` + jsNotSpace + `+) ([^\n\r\x{2028}\x{2029}]+)$`)
	spacesRe    = regexp.MustCompile(jsSpace + `+`)
	schemeRe    = regexp.MustCompile(`^(?i)[a-z]+://`)
	authorityRe = regexp.MustCompile(`[/?#]`)
	serviceRe   = regexp.MustCompile(`(?s)^(.+)\.(_[a-z-]+\._tcp)\.`)
)

// Default ports for the device URI schemes that name a host we can knock on.
var networkPorts = map[string]int{"ipp": 631, "ipps": 631, "http": 80, "https": 443, "socket": 9100, "lpd": 515}

const (
	probeTimeout  = 1500 * time.Millisecond
	dnssdTimeoutS = 2
	runTimeout    = 15 * time.Second
	submitTimeout = 30 * time.Second
)

// dnssd:// device URI -> the ipp:// URI it last resolved to, so only the first
// probe of a Bonjour queue pays for a browse (ippfind runs its full timeout).
var (
	dnssdMu       sync.Mutex
	dnssdResolved = map[string]string{}
)

// printError says the job could not be handed to the print system.
func printError(message string) *pycompat.Error {
	return &pycompat.Error{Type: "PrintError", Message: message}
}

// IsPrintError reports whether err is a refusal from this package.
func IsPrintError(err error) bool {
	var e *pycompat.Error
	return errors.As(err, &e) && e.Type == "PrintError"
}

// Printer is one print queue.
type Printer struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	IsDefault bool   `json:"is_default"`
	// true/false when the device answered or did not; nil (null) when the
	// queue's device URI gives us nothing to probe (USB, an unknown backend).
	Online *bool `json:"online"`
}

// When set, nothing is ever handed to the print queue. The desktop app turns
// this on for development builds and tests: a cheque leaf is usually loaded.
var disabled atomic.Bool

func SetPrintingDisabled(value bool) { disabled.Store(value) }

// When set, the print system is reported missing. Tests that replay the
// desk's recorded answers use it: they were recorded with no print system.
var hidden atomic.Bool

func HideSystem(value bool) { hidden.Store(value) }

// Completed is a command that ran to the end, whatever its exit status.
type Completed struct {
	Returncode int
	Stdout     string
	Stderr     string
}

// RunFunc runs a command. An error means it could not be run at all, or ran
// out of time; a non-zero exit is a Completed, not an error.
type RunFunc func(cmd string, args []string, timeout time.Duration) (Completed, error)

// Run is how every print-system command is run. Tests replace it.
var Run RunFunc = execRun

// dial reports whether host:port accepts a TCP connection in time. Tests replace it.
var dial = realDial

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// probeAddress is the host:port a device URI is reached at, or "" when it is
// not a network device.
func probeAddress(uri string) string {
	parts, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	defaultPort, ok := networkPorts[parts.Scheme]
	host := parts.Hostname()
	if !ok || host == "" {
		return ""
	}
	port := defaultPort
	if p := parts.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n > 65535 {
			return ""
		}
		if n != 0 {
			port = n
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func tcpReachable(uri string) *bool {
	address := probeAddress(uri)
	if address == "" {
		return nil
	}
	answer := dial(address, probeTimeout)
	// Refused by the local-network permission: the printer may well be on.
	if !answer && refusedByPermission(address) {
		return nil
	}
	return &answer
}

// ResolvePrinter picks the printer to use: explicit choice, then the system
// default, then first.
func ResolvePrinter(preferred string) (string, error) {
	printers := ListPrinters(false)
	if preferred != "" {
		for _, p := range printers {
			if p.Name == preferred {
				return preferred, nil
			}
		}
		return "", printError("printer " + pycompat.Repr(preferred) + " is not available")
	}
	for _, p := range printers {
		if p.IsDefault {
			return p.Name, nil
		}
	}
	if len(printers) > 0 {
		return printers[0].Name, nil
	}
	return "", printError("no printer is configured")
}

// jsNumber is JavaScript's String(n) for a number.
func jsNumber(n float64) string {
	switch {
	case math.IsNaN(n):
		return "NaN"
	case math.IsInf(n, 1):
		return "Infinity"
	case math.IsInf(n, -1):
		return "-Infinity"
	case n == 0:
		return "0"
	}
	if abs := math.Abs(n); abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	s := strconv.FormatFloat(n, 'e', -1, 64)
	mantissa, exp, _ := strings.Cut(s, "e")
	return mantissa + "e" + exp[:1] + strings.TrimLeft(exp[1:], "0")
}

// sixFigures is String(Number(n.toPrecision(6))): six significant figures,
// trailing zeros dropped.
func sixFigures(n float64) string {
	if math.IsNaN(n) || math.IsInf(n, 0) || n == 0 {
		return jsNumber(n)
	}
	// toPrecision rounds an exact half away from zero; Go rounds it to even.
	exact := strconv.FormatFloat(math.Abs(n), 'e', 800, 64)
	mantissa, _, _ := strings.Cut(exact, "e")
	if tail := mantissa[7:]; tail[0] == '5' && strings.Trim(tail[1:], "0") == "" {
		n = math.Nextafter(n, math.Copysign(math.Inf(1), n))
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(n, 'e', 5, 64), 64)
	if err != nil {
		return jsNumber(n)
	}
	return jsNumber(rounded)
}
