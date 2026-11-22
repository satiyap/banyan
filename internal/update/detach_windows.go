package update

import "os/exec"

// detach does nothing on Windows. The updater swaps Mac app bundles only, and
// the Windows build does not use it.
func detach(*exec.Cmd) {}
