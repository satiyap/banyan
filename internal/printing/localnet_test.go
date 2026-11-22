//go:build !windows

package printing

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestClassifyReadsThePermissionFromHowADialFails(t *testing.T) {
	cases := []struct {
		name  string
		host  string
		err   error
		took  time.Duration
		want  LocalNetwork
		known bool
	}{
		{"answered", "192.168.1.20", nil, 5 * time.Millisecond, LocalNetworkAllowed, true},
		{"port closed, host there", "192.168.1.1", syscall.ECONNREFUSED, 3 * time.Millisecond, LocalNetworkAllowed, true},
		{"refused before leaving the Mac", "192.168.1.20", syscall.EHOSTUNREACH, 1 * time.Millisecond, LocalNetworkDenied, true},
		{"link-local refused", "169.254.10.4", syscall.EHOSTUNREACH, 1 * time.Millisecond, LocalNetworkDenied, true},
		{"printer switched off: ARP gave up", "192.168.1.20", syscall.EHOSTUNREACH, 3 * time.Second, LocalNetworkUnknown, false},
		{"timed out", "10.0.0.7", errors.New("i/o timeout"), 1500 * time.Millisecond, LocalNetworkUnknown, false},
		{"not a local address", "203.0.113.9", syscall.EHOSTUNREACH, 1 * time.Millisecond, LocalNetworkUnknown, false},
		{"a name, not an address", "printer.local", syscall.EHOSTUNREACH, 1 * time.Millisecond, LocalNetworkUnknown, false},
	}
	for _, c := range cases {
		got, known := classify(c.host, c.err, c.took)
		if got != c.want || known != c.known {
			t.Errorf("%s: got %q/%v, want %q/%v", c.name, got, known, c.want, c.known)
		}
	}
}

func TestAPrinterBehindARefusedPermissionIsUnknownNotOffline(t *testing.T) {
	saved := dial
	defer func() { dial = saved }()
	address := "192.168.50.9:631"
	dial = func(a string, _ time.Duration) bool {
		localMu.Lock()
		deniedAddrs[a] = time.Now()
		localMu.Unlock()
		return false
	}
	defer func() { localMu.Lock(); delete(deniedAddrs, address); localMu.Unlock() }()
	if got := tcpReachable("ipp://192.168.50.9/ipp/print"); got != nil {
		t.Fatalf("got %v, want nil (cannot tell)", *got)
	}
}
