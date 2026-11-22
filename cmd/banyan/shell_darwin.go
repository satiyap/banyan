package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/satiyap/banyan/internal/update"
)

// closeQuits is false on a Mac: the window is put away and the app stays in the Dock.
const closeQuits = false

// hasUpdater: the direct download updates itself.
const hasUpdater = true

// hasLocalNetworkPermission: macOS asks before the app may reach the printer.
const hasLocalNetworkPermission = true

// fileManager is what the Help menu calls the place files are shown.
const fileManager = "Finder"

// awaitPrevious does nothing on a Mac; relaunch waits in its own helper.
func awaitPrevious() {}

// webviewDataPath is only used on Windows.
func webviewDataPath() string { return "" }

// relaunch starts the app again once this copy has gone.
func relaunch() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if appStore {
		// In the sandbox the app is opened again through LaunchServices, by a
		// helper in a session of its own: launchd ends the app's process
		// group when it quits.
		bundle := update.BundleOf(exe)
		if bundle == "" {
			return
		}
		cmd := exec.Command("/bin/sh", "-c",
			`while kill -0 "$0" 2>/dev/null; do sleep 0.1; done; open "$1" || { sleep 1; open "$1"; }`,
			strconv.Itoa(os.Getpid()), bundle)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		_ = cmd.Start()
		return
	}
	_ = exec.Command("/bin/sh", "-c", `sleep 1; exec "$0"`, exe).Start()
}

// reveal selects a file in Finder.
func reveal(path string) { _ = exec.Command("/usr/bin/open", "-R", path).Start() }

// openFile opens a file in its own app (a PDF in Preview).
func openFile(path string) { _ = exec.Command("/usr/bin/open", path).Start() }

// openNetworkSettings opens System Settings at Privacy & Security > Local Network.
func openNetworkSettings() error {
	return exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_LocalNetwork").Run()
}
