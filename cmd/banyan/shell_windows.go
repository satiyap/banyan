package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// closeQuits: Windows has no Dock to bring a hidden window back from, so
// closing the window quits the app.
const closeQuits = true

// hasUpdater: the updater swaps Mac app bundles only. A Windows release is
// installed by running its installer.
const hasUpdater = false

// hasLocalNetworkPermission: Windows asks nothing before the app may reach the printer.
const hasLocalNetworkPermission = false

// fileManager is what the Help menu calls the place files are shown.
const fileManager = "Explorer"

// afterFlag carries the process id of the copy a relaunch waits for.
const afterFlag = "--after"

// awaitPrevious waits for the copy that relaunched this one to quit, so the
// two never hold the register at once and the single-instance lock is free.
func awaitPrevious() {
	if len(os.Args) < 3 || os.Args[1] != afterFlag {
		return
	}
	pid, err := strconv.ParseUint(os.Args[2], 10, 32)
	if err != nil {
		return
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	_, _ = windows.WaitForSingleObject(h, 30_000)
}

// webviewDataPath keeps the web view's cache in the local (not roaming) profile.
func webviewDataPath() string {
	base, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil {
		return ""
	}
	name := "Banyan"
	if isDev() {
		name = "Banyan Dev"
	}
	return filepath.Join(base, name, "WebView2")
}

// relaunch starts the app again once this copy has gone.
func relaunch() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = exec.Command(exe, afterFlag, strconv.Itoa(os.Getpid())).Start()
}

// reveal selects a file in Explorer. Explorer reads its own command line, and
// needs the path quoted after the comma, not the whole argument.
func reveal(path string) {
	explorer := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	cmd := exec.Command(explorer)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	_ = cmd.Start()
}

// openFile opens a file in its own app (a PDF in the default PDF viewer).
func openFile(path string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	_ = windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// openNetworkSettings: there is no such permission on Windows.
func openNetworkSettings() error { return errors.New("not on Windows") }
