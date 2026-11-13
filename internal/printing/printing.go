// Package printing is print queue submission and job tracking.
//
// The cheque goes to the printer through `lp` with scaling and fit-to-page pinned
// off, because any resizing at this stage would invalidate every calibrated
// millimetre upstream of it.
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
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

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

// dial reports whether host:port accepts a TCP connection in time.
var dial = func(address string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

func which(name string) string {
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if syscall.Access(candidate, 0x1) == nil {
			return candidate
		}
		// not executable; keep looking
	}
	return ""
}

func CupsAvailable() bool {
	return which("lp") != "" && which("lpstat") != ""
}

func deviceUris() map[string]string {
	uris := map[string]string{}
	proc, err := Run("lpstat", []string{"-v"}, runTimeout)
	if err != nil {
		return uris
	}
	for _, m := range deviceRe.FindAllStringSubmatch(proc.Stdout, -1) {
		uris[m[1]] = m[2]
	}
	return uris
}

func tcpReachable(uri string) *bool {
	parts, err := url.Parse(uri)
	if err != nil {
		return nil
	}
	defaultPort, ok := networkPorts[parts.Scheme]
	host := parts.Hostname()
	if !ok || host == "" {
		return nil
	}
	port := defaultPort
	if p := parts.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n > 65535 {
			return nil
		}
		if n != 0 {
			port = n
		}
	}
	answer := dial(net.JoinHostPort(host, strconv.Itoa(port)), probeTimeout)
	return &answer
}

// resolveDnssd turns a Bonjour device URI into the ipp:// URI the printer
// advertises.
//
// Returns "" when nothing by that name is on the network, ok=false when there
// is no `ippfind` to ask.
func resolveDnssd(uri string) (resolved string, ok bool) {
	if which("ippfind") == "" {
		return "", false
	}
	// The authority of a dnssd:// URI is a percent-encoded service name, which
	// a URL parser will not accept as a host, so it is cut out by hand.
	netloc := authorityRe.Split(schemeRe.ReplaceAllString(uri, ""), 2)[0]
	decoded, err := url.PathUnescape(netloc)
	if err != nil || !utf8.ValidString(decoded) {
		return "", false
	}
	m := serviceRe.FindStringSubmatch(decoded)
	if m == nil {
		return "", false
	}
	name, regtype := m[1], m[2]
	args := []string{"-T", strconv.Itoa(dnssdTimeoutS), regtype, "--name", "^" + regexp.QuoteMeta(name) + "$", "--print"}
	proc, err := Run("ippfind", args, runTimeout)
	if err != nil {
		return "", false
	}
	out := jsTrim(proc.Stdout)
	if out == "" {
		return "", true
	}
	return strings.Split(out, "\n")[0], true
}

// DeviceOnline reports whether the device behind a queue answers right now;
// nil when there is nothing to probe.
//
// A print queue reports "idle" for a printer that is switched off: the queue
// is local and only notices when a job fails to go out. So the queue state
// alone cannot say the printer is there; this opens a connection to find out.
func DeviceOnline(uri string) *bool {
	if !strings.HasPrefix(uri, "dnssd://") {
		return tcpReachable(uri)
	}
	dnssdMu.Lock()
	cached := dnssdResolved[uri]
	dnssdMu.Unlock()
	if cached != "" {
		if online := tcpReachable(cached); online != nil && *online {
			return online
		}
	}
	// Never resolved, or the cached address stopped answering: look again.
	// A stale Bonjour record can outlive the printer, so a name that resolves
	// still has to accept a connection.
	dnssdMu.Lock()
	delete(dnssdResolved, uri)
	dnssdMu.Unlock()
	resolved, ok := resolveDnssd(uri)
	if !ok {
		return nil
	}
	if resolved == "" {
		no := false
		return &no
	}
	online := tcpReachable(resolved)
	if online != nil && *online {
		dnssdMu.Lock()
		dnssdResolved[uri] = resolved
		dnssdMu.Unlock()
	}
	return online
}

// ListPrinters returns the print queues. With probe, it also checks each
// device is reachable. The result is never nil.
func ListPrinters(probe bool) []Printer {
	printers := []Printer{}
	if !CupsAvailable() {
		return printers
	}
	listing, err := Run("lpstat", []string{"-p"}, runTimeout)
	if err != nil {
		return printers
	}
	defaultOut, err := Run("lpstat", []string{"-d"}, runTimeout)
	if err != nil {
		return printers
	}
	uris := map[string]string{}
	if probe {
		uris = deviceUris()
	}

	defaultName := ""
	if m := defaultRe.FindStringSubmatch(defaultOut.Stdout); m != nil {
		defaultName = m[1]
	}

	for _, line := range strings.Split(listing.Stdout, "\n") {
		m := printerRe.FindStringSubmatch(jsTrim(line))
		if m == nil {
			continue
		}
		name := m[1]
		rest := strings.ToLower(m[2])
		// lpstat has several shapes: "is idle.", "is busy.", "now printing job-1",
		// and, for a paused queue, "disabled since <date>". Reading only the
		// first of those makes a paused printer look like no printer at all,
		// which is exactly the state a queue is in while a leaf is being loaded.
		var state string
		switch {
		case strings.HasPrefix(rest, "is "):
			if words := spacesRe.Split(rest, -1); len(words) > 1 {
				state = strings.TrimRight(words[1], ".")
			}
		case strings.Contains(rest, "disabled"):
			state = "paused"
		case strings.Contains(rest, "now printing"):
			state = "printing"
		default:
			state = "unknown"
		}
		var online *bool
		if uri, ok := uris[name]; ok {
			online = DeviceOnline(uri)
		}
		if online != nil && !*online {
			state = "offline"
		}
		printers = append(printers, Printer{Name: name, State: state, IsDefault: name == defaultName, Online: online})
	}
	return printers
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

// Submit hands the PDF to the print system and returns the job id.
//
// Scaling and fit-to-page are pinned off. If the driver ever refuses these
// options the job fails here rather than printing a subtly resized cheque.
func Submit(pdf, printer string, pageWMm, pageHMm float64, title string) (string, error) {
	if _, err := os.Stat(pdf); err != nil {
		return "", printError("the rendered PDF is missing")
	}
	if disabled.Load() {
		return "", printError("printing is switched off in this build, so nothing was sent to the printer")
	}
	if !CupsAvailable() {
		return "", printError("CUPS is not available on this machine")
	}

	args := []string{
		"-d", printer,
		"-t", title,
		"-o", "media=Custom." + sixFigures(pageWMm) + "x" + sixFigures(pageHMm) + "mm",
		"-o", "fit-to-page=false",
		"-o", "scaling=100",
		"-o", "page-border=none",
		"--", pdf,
	}
	proc, err := Run("lp", args, submitTimeout)
	if err != nil {
		return "", printError("could not reach the print system: " + err.Error())
	}

	if proc.Returncode != 0 {
		message := proc.Stderr
		if message == "" {
			message = proc.Stdout
		}
		if message == "" {
			message = "lp failed"
		}
		return "", printError(jsTrim(message))
	}

	m := jobRe.FindStringSubmatch(proc.Stdout)
	if m == nil {
		return "", printError("the print system gave no job id: " + jsTrim(proc.Stdout))
	}
	return m[1], nil
}

// JobState says where a job has got to: "active", "finished", "cancelled" or
// "unknown".
//
// "finished" means the print system is done with the job, not that the cheque is correct.
func JobState(jobID string) string {
	if jobID == "" || !CupsAvailable() {
		return "unknown"
	}
	mentions, err := regexp.Compile(`(?m)^` + regexp.QuoteMeta(jobID) + jsSpace)
	if err != nil {
		return "unknown"
	}
	proc, err := Run("lpstat", []string{"-o"}, runTimeout)
	if err != nil {
		return "unknown"
	}
	if mentions.MatchString(proc.Stdout) {
		return "active"
	}
	proc, err = Run("lpstat", []string{"-W", "completed", "-o"}, runTimeout)
	if err != nil {
		return "unknown"
	}
	if mentions.MatchString(proc.Stdout) {
		return "finished"
	}
	return "cancelled"
}

func Cancel(jobID string) bool {
	if jobID == "" || !CupsAvailable() || disabled.Load() {
		return false
	}
	proc, err := Run("cancel", []string{jobID}, runTimeout)
	return err == nil && proc.Returncode == 0
}
