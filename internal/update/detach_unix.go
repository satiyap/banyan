//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

// detach puts a helper in a session of its own.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
