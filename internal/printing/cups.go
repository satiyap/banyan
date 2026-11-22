//go:build !windows

// The print system on a Mac: CUPS, through lp, lpstat and cancel.

package printing

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

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
	if hidden.Load() {
		return false
	}
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
		// A Bonjour browse the permission blocks finds nothing either.
		if state, _ := LocalNetworkState(); state == LocalNetworkDenied {
			return nil
		}
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
