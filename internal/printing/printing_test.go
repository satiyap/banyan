package printing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/satiyap/banyan/internal/pycompat"
)

// Nothing here can reach a printer: PATH is an empty folder for the whole test
// process, and the runner and the network probe panic unless a test installs
// a fake.
func guardRun(cmd string, args []string, _ time.Duration) (Completed, error) {
	panic(fmt.Sprintf("a test reached the real command runner: %s %v", cmd, args))
}

func guardDial(address string, _ time.Duration) bool {
	panic("a test reached the real network probe: " + address)
}

func TestMain(m *testing.M) {
	empty, err := os.MkdirTemp("", "banyan-printing-path")
	if err != nil {
		panic(err)
	}
	os.Setenv("PATH", empty)
	Run = guardRun
	dial = guardDial
	code := m.Run()
	os.RemoveAll(empty)
	os.Exit(code)
}

type call struct {
	Cmd     string
	Args    []string
	Timeout time.Duration
}

type fake struct {
	calls []call
	dials []string
}

type reply func(cmd string, args []string) (Completed, error)

// install replaces the runner for one test and resets the module's state after it.
func install(t *testing.T, r reply) *fake {
	t.Helper()
	f := &fake{}
	Run = func(cmd string, args []string, timeout time.Duration) (Completed, error) {
		f.calls = append(f.calls, call{cmd, args, timeout})
		if r == nil {
			t.Errorf("unexpected command: %s %v", cmd, args)
			return Completed{}, errors.New("unexpected command")
		}
		return r(cmd, args)
	}
	t.Cleanup(func() {
		Run = guardRun
		dial = guardDial
		SetPrintingDisabled(false)
		dnssdMu.Lock()
		dnssdResolved = map[string]string{}
		dnssdMu.Unlock()
	})
	return f
}

func (f *fake) answer(online map[string]bool) {
	dial = func(address string, timeout time.Duration) bool {
		if timeout != 1500*time.Millisecond {
			panic("probe timeout changed")
		}
		f.dials = append(f.dials, address)
		return online[address]
	}
}

// withBinaries points PATH at a folder of empty stand-ins. They are only ever
// looked up, never run.
func withBinaries(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

func withCups(t *testing.T) { withBinaries(t, "lp", "lpstat") }

func stdout(s string) (Completed, error) { return Completed{Stdout: s}, nil }

const listing = `printer Office_Laser is idle.  enabled since Fri Nov 13 09:12:44 2026
printer Front_Desk_Inkjet disabled since Thu Nov 12 17:03:10 2026 -
	Paused
printer Label_Maker now printing Label_Maker-41.  enabled since Fri Nov 13 10:01:02 2026
printer Back_Room is Busy.  enabled since Fri Nov 13 08:00:00 2026
printer Odd_One has gone somewhere
scheduler is running
`

const devices = `device for Office_Laser: ipp://192.168.1.40/ipp/print
device for Front_Desk_Inkjet: usb://Acme/Inkjet%20200?serial=A1B2
device for Label_Maker: socket://10.0.0.9
device for Back_Room: lpd://backroom.example:1515/queue
`

func lpstat(defaultLine string) reply {
	return func(cmd string, args []string) (Completed, error) {
		if cmd != "lpstat" || len(args) != 1 {
			return Completed{}, fmt.Errorf("unexpected %s %v", cmd, args)
		}
		switch args[0] {
		case "-p":
			return stdout(listing)
		case "-d":
			return stdout(defaultLine)
		case "-v":
			return stdout(devices)
		}
		return Completed{}, fmt.Errorf("unexpected %s %v", cmd, args)
	}
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantCalls(t *testing.T, f *fake, want ...call) {
	t.Helper()
	if len(f.calls) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("commands run:\n got %v\nwant %v", f.calls, want)
	}
}

func wantPrintError(t *testing.T, err error, message string) {
	t.Helper()
	var e *pycompat.Error
	if !errors.As(err, &e) {
		t.Fatalf("got %v, want a PrintError %q", err, message)
	}
	if e.Type != "PrintError" || e.Message != message {
		t.Fatalf("got %s %q, want PrintError %q", e.Type, e.Message, message)
	}
	if !IsPrintError(err) {
		t.Fatal("IsPrintError is false for a PrintError")
	}
}

func TestCupsAvailable(t *testing.T) {
	if CupsAvailable() {
		t.Fatal("available with an empty PATH")
	}
	dir := withBinaries(t, "lp")
	if CupsAvailable() {
		t.Fatal("available with lp alone")
	}
	if err := os.WriteFile(filepath.Join(dir, "lpstat"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if CupsAvailable() {
		t.Fatal("available with an lpstat that is not executable")
	}
	// A later folder on PATH is still searched, and empty entries are skipped.
	other := withBinaries(t, "lpstat")
	t.Setenv("PATH", string(os.PathListSeparator)+dir+string(os.PathListSeparator)+other)
	if !CupsAvailable() {
		t.Fatal("not available with lp and lpstat on PATH")
	}
	t.Setenv("PATH", "")
	if CupsAvailable() {
		t.Fatal("available with PATH empty")
	}
}

func TestListPrinters(t *testing.T) {
	withCups(t)
	f := install(t, lpstat("system default destination: Office_Laser\n"))
	got := asJSON(t, ListPrinters(false))
	want := `[{"name":"Office_Laser","state":"idle","is_default":true,"online":null},` +
		`{"name":"Front_Desk_Inkjet","state":"paused","is_default":false,"online":null},` +
		`{"name":"Label_Maker","state":"printing","is_default":false,"online":null},` +
		`{"name":"Back_Room","state":"busy","is_default":false,"online":null},` +
		`{"name":"Odd_One","state":"unknown","is_default":false,"online":null}]`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	wantCalls(t, f,
		call{"lpstat", []string{"-p"}, 15 * time.Second},
		call{"lpstat", []string{"-d"}, 15 * time.Second})
}

func TestListPrintersNoDefault(t *testing.T) {
	withCups(t)
	install(t, lpstat("no system default destination\n"))
	for _, p := range ListPrinters(false) {
		if p.IsDefault {
			t.Fatalf("%s is marked default", p.Name)
		}
	}
}

func TestListPrintersProbe(t *testing.T) {
	withCups(t)
	f := install(t, lpstat("system default destination: Label_Maker\n"))
	f.answer(map[string]bool{"10.0.0.9:9100": true, "backroom.example:1515": true})
	got := asJSON(t, ListPrinters(true))
	want := `[{"name":"Office_Laser","state":"offline","is_default":false,"online":false},` +
		`{"name":"Front_Desk_Inkjet","state":"paused","is_default":false,"online":null},` +
		`{"name":"Label_Maker","state":"printing","is_default":true,"online":true},` +
		`{"name":"Back_Room","state":"busy","is_default":false,"online":true},` +
		`{"name":"Odd_One","state":"unknown","is_default":false,"online":null}]`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	wantCalls(t, f,
		call{"lpstat", []string{"-p"}, 15 * time.Second},
		call{"lpstat", []string{"-d"}, 15 * time.Second},
		call{"lpstat", []string{"-v"}, 15 * time.Second})
	if want := []string{"192.168.1.40:631", "10.0.0.9:9100", "backroom.example:1515"}; !reflect.DeepEqual(f.dials, want) {
		t.Fatalf("probed %v, want %v", f.dials, want)
	}
}

func TestListPrintersProbeWithoutDeviceList(t *testing.T) {
	withCups(t)
	inner := lpstat("")
	install(t, func(cmd string, args []string) (Completed, error) {
		if args[0] == "-v" {
			return Completed{}, errors.New("lpstat timed out")
		}
		return inner(cmd, args)
	})
	printers := ListPrinters(true)
	if len(printers) != 5 || printers[0].Online != nil || printers[0].State != "idle" {
		t.Fatalf("got %s", asJSON(t, printers))
	}
}

func TestListPrintersWithoutCups(t *testing.T) {
	f := install(t, nil)
	if got := asJSON(t, ListPrinters(true)); got != "[]" {
		t.Fatalf("got %s, want []", got)
	}
	wantCalls(t, f)
}

func TestListPrintersWhenLpstatFails(t *testing.T) {
	withCups(t)
	for _, failing := range []string{"-p", "-d"} {
		inner := lpstat("")
		install(t, func(cmd string, args []string) (Completed, error) {
			if args[0] == failing {
				return Completed{}, errors.New("lpstat timed out")
			}
			return inner(cmd, args)
		})
		if got := asJSON(t, ListPrinters(false)); got != "[]" {
			t.Fatalf("lpstat %s failing: got %s, want []", failing, got)
		}
	}
}

func TestDeviceOnline(t *testing.T) {
	f := install(t, nil)
	f.answer(map[string]bool{"[fe80::1]:8631": true, "printer.example:443": true})
	yes, no := true, false
	cases := []struct {
		uri  string
		want *bool
		dial string
	}{
		{"ipp://[fe80::1]:8631/ipp/print", &yes, "[fe80::1]:8631"},
		{"https://printer.example/ipp", &yes, "printer.example:443"},
		{"ipps://printer.example/ipp", &no, "printer.example:631"},
		{"http://printer.example:0/ipp", &no, "printer.example:80"},
		{"usb://Acme/Inkjet%20200?serial=A1B2", nil, ""},
		{"socket:10.0.0.9", nil, ""},
		{"ipp://host:port/x", nil, ""},
		{"ipp://host:70000/x", nil, ""},
		{"not a uri", nil, ""},
		{"", nil, ""},
	}
	for _, c := range cases {
		f.dials = nil
		got := DeviceOnline(c.uri)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%q: got %s, want %s", c.uri, asJSON(t, got), asJSON(t, c.want))
		}
		if c.dial == "" && len(f.dials) != 0 || c.dial != "" && !reflect.DeepEqual(f.dials, []string{c.dial}) {
			t.Errorf("%q: probed %v, want %q", c.uri, f.dials, c.dial)
		}
	}
	wantCalls(t, f)
}

const bonjour = "dnssd://Office%20Laser%20(2)._ipp._tcp.local./?uuid=0f1e2d3c"

func TestDeviceOnlineBonjour(t *testing.T) {
	withBinaries(t, "ippfind")
	found := "ipp://office-laser.local:631/ipp/print\nipp://other.local:631/ipp/print\n"
	f := install(t, func(cmd string, args []string) (Completed, error) { return stdout(found) })
	f.answer(map[string]bool{"office-laser.local:631": true})

	if got := DeviceOnline(bonjour); got == nil || !*got {
		t.Fatalf("got %s, want true", asJSON(t, got))
	}
	browse := call{"ippfind", []string{"-T", "2", "_ipp._tcp", "--name", `^Office Laser \(2\)$`, "--print"}, 15 * time.Second}
	wantCalls(t, f, browse)

	// The resolved address is remembered: no second browse.
	if got := DeviceOnline(bonjour); got == nil || !*got {
		t.Fatalf("second probe: got %s, want true", asJSON(t, got))
	}
	wantCalls(t, f, browse)

	// It stops answering, and so does the browse: looked up again, then offline.
	f.answer(nil)
	found = "\n"
	if got := DeviceOnline(bonjour); got == nil || *got {
		t.Fatalf("got %s, want false", asJSON(t, got))
	}
	wantCalls(t, f, browse, browse)

	// A name that resolves still has to accept a connection.
	found = "ipp://office-laser.local:631/ipp/print\n"
	if got := DeviceOnline(bonjour); got == nil || *got {
		t.Fatalf("got %s, want false", asJSON(t, got))
	}
}

func TestDeviceOnlineBonjourUnanswerable(t *testing.T) {
	f := install(t, nil)
	if got := DeviceOnline(bonjour); got != nil {
		t.Fatalf("without ippfind: got %v, want nil", *got)
	}
	wantCalls(t, f)

	withBinaries(t, "ippfind")
	for _, uri := range []string{"dnssd://Office%2", "dnssd://%ff._ipp._tcp.local./", "dnssd://no-service-here/"} {
		if got := DeviceOnline(uri); got != nil {
			t.Fatalf("%q: got %v, want nil", uri, *got)
		}
	}
	wantCalls(t, f)

	f = install(t, func(string, []string) (Completed, error) { return Completed{}, errors.New("ippfind timed out") })
	if got := DeviceOnline(bonjour); got != nil {
		t.Fatalf("browse failing: got %v, want nil", *got)
	}
}

func TestResolvePrinter(t *testing.T) {
	withCups(t)
	install(t, lpstat("system default destination: Label_Maker\n"))

	if got, err := ResolvePrinter("Front_Desk_Inkjet"); err != nil || got != "Front_Desk_Inkjet" {
		t.Fatalf("explicit choice: got %q, %v", got, err)
	}
	if got, err := ResolvePrinter(""); err != nil || got != "Label_Maker" {
		t.Fatalf("system default: got %q, %v", got, err)
	}
	_, err := ResolvePrinter("Ghost")
	wantPrintError(t, err, "printer 'Ghost' is not available")
	_, err = ResolvePrinter("Bob's")
	wantPrintError(t, err, `printer "Bob's" is not available`)

	install(t, lpstat("no system default destination\n"))
	if got, err := ResolvePrinter(""); err != nil || got != "Office_Laser" {
		t.Fatalf("first queue: got %q, %v", got, err)
	}
}

func TestResolvePrinterWithoutCups(t *testing.T) {
	f := install(t, nil)
	_, err := ResolvePrinter("")
	wantPrintError(t, err, "no printer is configured")
	_, err = ResolvePrinter("Office_Laser")
	wantPrintError(t, err, "printer 'Office_Laser' is not available")
	wantCalls(t, f)
}

func pdfFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cheque 12.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSubmit(t *testing.T) {
	withCups(t)
	pdf := pdfFile(t)
	f := install(t, func(string, []string) (Completed, error) {
		return stdout("request id is Office_Laser-57 (1 file(s))\n")
	})
	job, err := Submit(pdf, "Office_Laser", 203.2, 92.07500000000002, "cheque 12")
	if err != nil || job != "Office_Laser-57" {
		t.Fatalf("got %q, %v", job, err)
	}
	wantCalls(t, f, call{"lp", []string{
		"-d", "Office_Laser",
		"-t", "cheque 12",
		"-o", "media=Custom.203.2x92.075mm",
		"-o", "fit-to-page=false",
		"-o", "scaling=100",
		"-o", "page-border=none",
		"--", pdf,
	}, 30 * time.Second})
}

func TestSubmitDisabled(t *testing.T) {
	withCups(t)
	pdf := pdfFile(t)
	f := install(t, nil)
	SetPrintingDisabled(true)
	_, err := Submit(pdf, "Office_Laser", 203.2, 92, "cheque 12")
	wantPrintError(t, err, "printing is switched off in this build, so nothing was sent to the printer")
	wantCalls(t, f)

	// The missing file is reported first, as in the original.
	_, err = Submit(filepath.Join(t.TempDir(), "absent.pdf"), "Office_Laser", 203.2, 92, "cheque 12")
	wantPrintError(t, err, "the rendered PDF is missing")
	wantCalls(t, f)
}

func TestSubmitWithoutCups(t *testing.T) {
	pdf := pdfFile(t)
	f := install(t, nil)
	_, err := Submit(pdf, "Office_Laser", 203.2, 92, "cheque 12")
	wantPrintError(t, err, "CUPS is not available on this machine")

	withBinaries(t, "lp")
	_, err = Submit(pdf, "Office_Laser", 203.2, 92, "cheque 12")
	wantPrintError(t, err, "CUPS is not available on this machine")

	_, err = Submit("", "Office_Laser", 203.2, 92, "cheque 12")
	wantPrintError(t, err, "the rendered PDF is missing")
	wantCalls(t, f)
}

func TestSubmitFailures(t *testing.T) {
	withCups(t)
	pdf := pdfFile(t)
	cases := []struct {
		done Completed
		err  error
		want string
	}{
		{err: errors.New("lp timed out"), want: "could not reach the print system: lp timed out"},
		{err: errors.New("spawn lp ENOENT"), want: "could not reach the print system: spawn lp ENOENT"},
		{done: Completed{Returncode: 1, Stdout: "ignored\n", Stderr: "lp: The printer or class does not exist.\n"},
			want: "lp: The printer or class does not exist."},
		{done: Completed{Returncode: 1, Stdout: "  lp: Unsupported media value.\n"}, want: "lp: Unsupported media value."},
		{done: Completed{Returncode: 2}, want: "lp failed"},
		{done: Completed{Stdout: "job accepted, probably\n"}, want: "the print system gave no job id: job accepted, probably"},
		{done: Completed{}, want: "the print system gave no job id: "},
	}
	for _, c := range cases {
		f := install(t, func(string, []string) (Completed, error) { return c.done, c.err })
		_, err := Submit(pdf, "Office_Laser", 203.2, 92, "cheque 12")
		wantPrintError(t, err, c.want)
		if len(f.calls) != 1 || f.calls[0].Cmd != "lp" {
			t.Fatalf("%s: commands run: %v", c.want, f.calls)
		}
	}
}

func TestSixFigures(t *testing.T) {
	cases := map[float64]string{
		203.2:               "203.2",
		92:                  "92",
		88.9:                "88.9",
		177.80000001:        "177.8",
		0.1 + 0.2:           "0.3",
		215.9:               "215.9",
		123456.5:            "123457",
		1234565:             "1234570",
		123456.4:            "123456",
		0.00001234565:       "0.0000123456",
		0.000000123:         "1.23e-7",
		1e21:                "1e+21",
		123456789012345e10:  "1.23457e+24",
		-203.25:             "-203.25",
		0:                   "0",
		3.0000004:           "3",
		99999.95:            "99999.9",
		999999.5:            "1000000",
		209.97333333333333:  "209.973",
		3.6666666666666665:  "3.66667",
		100.00049999999999:  "100",
		0.30000000000000004: "0.3",
	}
	for n, want := range cases {
		if got := sixFigures(n); got != want {
			t.Errorf("sixFigures(%v) = %q, want %q", n, got, want)
		}
	}
}

const queue = `Office_Laser-57         treasurer         1024   Fri Nov 13 10:14:05 2026
Label_Maker-41          treasurer         2048   Fri Nov 13 10:01:02 2026
`

const completed = `Office_Laser-5          treasurer         1024   Thu Nov 12 16:40:11 2026
Office_Laser-56         treasurer         1024   Fri Nov 13 09:58:30 2026
`

func jobs(cmd string, args []string) (Completed, error) {
	if cmd == "lpstat" && reflect.DeepEqual(args, []string{"-o"}) {
		return stdout(queue)
	}
	if cmd == "lpstat" && reflect.DeepEqual(args, []string{"-W", "completed", "-o"}) {
		return stdout(completed)
	}
	return Completed{}, fmt.Errorf("unexpected %s %v", cmd, args)
}

func TestJobState(t *testing.T) {
	withCups(t)
	active := call{"lpstat", []string{"-o"}, 15 * time.Second}
	finished := call{"lpstat", []string{"-W", "completed", "-o"}, 15 * time.Second}

	f := install(t, jobs)
	if got := JobState("Office_Laser-57"); got != "active" {
		t.Fatalf("got %q, want active", got)
	}
	wantCalls(t, f, active)

	f = install(t, jobs)
	// Not a prefix match: Office_Laser-5 is not Office_Laser-57.
	if got := JobState("Office_Laser-5"); got != "finished" {
		t.Fatalf("got %q, want finished", got)
	}
	wantCalls(t, f, active, finished)

	for id, want := range map[string]string{
		"Office_Laser-56": "finished",
		"Office_Laser-99": "cancelled",
		"Office.Laser-57": "cancelled",
		"treasurer":       "cancelled",
		"":                "unknown",
	} {
		if got := JobState(id); got != want {
			t.Errorf("JobState(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestJobStateUnknown(t *testing.T) {
	f := install(t, nil)
	if got := JobState("Office_Laser-57"); got != "unknown" {
		t.Fatalf("without CUPS: got %q", got)
	}
	wantCalls(t, f)

	withCups(t)
	install(t, func(string, []string) (Completed, error) { return Completed{}, errors.New("lpstat timed out") })
	if got := JobState("Office_Laser-57"); got != "unknown" {
		t.Fatalf("lpstat failing: got %q", got)
	}
	install(t, func(_ string, args []string) (Completed, error) {
		if len(args) == 1 {
			return stdout("")
		}
		return Completed{}, errors.New("lpstat timed out")
	})
	if got := JobState("Office_Laser-57"); got != "unknown" {
		t.Fatalf("second lpstat failing: got %q", got)
	}
}

func TestCancel(t *testing.T) {
	f := install(t, nil)
	if Cancel("Office_Laser-57") {
		t.Fatal("cancelled without CUPS")
	}
	withCups(t)
	SetPrintingDisabled(true)
	if Cancel("Office_Laser-57") {
		t.Fatal("cancelled with printing switched off")
	}
	SetPrintingDisabled(false)
	if Cancel("") {
		t.Fatal("cancelled a job with no id")
	}
	wantCalls(t, f)

	f = install(t, func(string, []string) (Completed, error) { return Completed{}, nil })
	if !Cancel("Office_Laser-57") {
		t.Fatal("not cancelled")
	}
	wantCalls(t, f, call{"cancel", []string{"Office_Laser-57"}, 15 * time.Second})

	install(t, func(string, []string) (Completed, error) {
		return Completed{Returncode: 1, Stderr: "cancel: Job #57 is already completed - can't cancel.\n"}, nil
	})
	if Cancel("Office_Laser-57") {
		t.Fatal("cancelled although cancel exited 1")
	}
	install(t, func(string, []string) (Completed, error) { return Completed{}, errors.New("cancel timed out") })
	if Cancel("Office_Laser-57") {
		t.Fatal("cancelled although cancel never ran")
	}
}

func TestGuardIsInPlace(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("the default test runner did not refuse")
		}
	}()
	Run("lpstat", []string{"-p"}, time.Second)
}
