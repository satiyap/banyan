package printing

import (
	"errors"
	"net"
	"net/netip"
	"regexp"
	"sync"
	"syscall"
	"time"
)

// macOS asks the user before an app may reach other machines on the local
// network, and offers no way to read the answer back. It shows only in how a
// connection fails: a refused app gets "no route to host" at once, before a
// packet leaves the Mac. A printer that is switched off fails slowly (a
// timeout, or no route only after ARP gives up, seconds later), so a fast
// EHOSTUNREACH to a local address is read as the permission, not the printer.
//
// Printing itself does not need the permission: lp hands the job to the print
// system, which does its own networking. Only the desk's own probe of the
// printer does, so a refusal must make the printer "unknown", never "offline".

// LocalNetwork is what the desk knows about the permission.
type LocalNetwork string

const (
	LocalNetworkAllowed LocalNetwork = "allowed"
	LocalNetworkDenied  LocalNetwork = "denied"
	LocalNetworkUnknown LocalNetwork = "unknown"
)

// Faster than any real network can say a host is unreachable.
const deniedWithin = 250 * time.Millisecond

var (
	localMu     sync.Mutex
	localState  = LocalNetworkUnknown
	localSeenAt time.Time
	deniedAddrs = map[string]time.Time{} // address -> when a dial to it was refused by the permission
)

func noteLocal(state LocalNetwork) {
	localMu.Lock()
	localState, localSeenAt = state, time.Now()
	localMu.Unlock()
}

// LocalNetworkState is the last thing learnt, and when; zero time if nothing yet.
func LocalNetworkState() (LocalNetwork, time.Time) {
	localMu.Lock()
	defer localMu.Unlock()
	return localState, localSeenAt
}

// isLocal is a private, link-local or .local address: the ones the permission covers.
func isLocal(host string) bool {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// classify reads one dial's outcome for the permission. ok is whether the
// outcome said anything at all.
func classify(host string, err error, took time.Duration) (state LocalNetwork, ok bool) {
	if !isLocal(host) {
		return LocalNetworkUnknown, false
	}
	switch {
	case err == nil, errors.Is(err, syscall.ECONNREFUSED):
		// Something on the network answered, so packets are getting out.
		return LocalNetworkAllowed, true
	case errors.Is(err, syscall.EHOSTUNREACH) && took < deniedWithin:
		return LocalNetworkDenied, true
	}
	return LocalNetworkUnknown, false
}

// realDial is dial as the app runs it: it also notes what the outcome says
// about the permission.
func realDial(address string, timeout time.Duration) bool {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, timeout)
	took := time.Since(start)
	if conn != nil {
		conn.Close()
	}
	host, _, _ := net.SplitHostPort(address)
	if state, ok := classify(host, err, took); ok {
		noteLocal(state)
		localMu.Lock()
		if state == LocalNetworkDenied {
			deniedAddrs[address] = time.Now()
		} else {
			delete(deniedAddrs, address)
		}
		localMu.Unlock()
	}
	return err == nil
}

// refusedByPermission says the last dial to address failed because of the
// permission, so its "no" says nothing about the printer.
func refusedByPermission(address string) bool {
	localMu.Lock()
	defer localMu.Unlock()
	at, ok := deniedAddrs[address]
	return ok && time.Since(at) < time.Minute
}

var gatewayRe = regexp.MustCompile(`(?m)^\s*gateway:\s*(\S+)\s*$`)

// CheckLocalNetwork finds out whether the app may use the local network, by
// trying it: the router first, then the printers. The first such attempt is
// also what makes macOS show its question.
func CheckLocalNetwork() LocalNetwork {
	var targets []string
	if proc, err := Run("route", []string{"-n", "get", "default"}, runTimeout); err == nil {
		if m := gatewayRe.FindStringSubmatch(proc.Stdout); m != nil && isLocal(m[1]) {
			for _, port := range []string{"80", "443", "53"} {
				targets = append(targets, net.JoinHostPort(m[1], port))
			}
		}
	}
	for _, uri := range deviceUris() {
		dnssdMu.Lock()
		if cached := dnssdResolved[uri]; cached != "" {
			uri = cached
		}
		dnssdMu.Unlock()
		if addr := probeAddress(uri); addr != "" {
			targets = append(targets, addr)
		}
	}
	for _, target := range targets {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", target, time.Second)
		if conn != nil {
			conn.Close()
		}
		host, _, _ := net.SplitHostPort(target)
		if state, ok := classify(host, err, time.Since(start)); ok {
			noteLocal(state)
			return state
		}
	}
	state, _ := LocalNetworkState()
	return state
}
