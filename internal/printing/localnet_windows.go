package printing

import (
	"net"
	"time"
)

// Windows has no local-network permission to ask for: an app reaches the
// printer's address like anything else, so a failed probe means the printer.

// LocalNetwork is what the desk knows about the permission.
type LocalNetwork string

const (
	LocalNetworkAllowed LocalNetwork = "allowed"
	LocalNetworkDenied  LocalNetwork = "denied"
	LocalNetworkUnknown LocalNetwork = "unknown"
)

// LocalNetworkState is always allowed here.
func LocalNetworkState() (LocalNetwork, time.Time) { return LocalNetworkAllowed, time.Time{} }

// CheckLocalNetwork is always allowed here.
func CheckLocalNetwork() LocalNetwork { return LocalNetworkAllowed }

func refusedByPermission(string) bool { return false }

func realDial(address string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if conn != nil {
		conn.Close()
	}
	return err == nil
}
