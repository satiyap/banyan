package main

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// storeBundleID is the Mac App Store build's identifier, and the one the
// direct download is moving to. The store build's container, named by it, is
// where both builds keep the register once the store build has run on a Mac.
const storeBundleID = "com.banyan.desk"

// sandboxed is true inside the App Store build's sandbox, where the home
// folder is the app's own container.
func sandboxed() bool {
	home, err := os.UserHomeDir()
	return err == nil && strings.Contains(home, "/Library/Containers/")
}

// useContainerTemp points temporary files into the container. A sandboxed
// app can only write there, and every cheque and report is drawn through a
// temporary file; a TMPDIR inherited from outside would make rendering fail.
func useContainerTemp() {
	if !sandboxed() {
		return
	}
	home, _ := os.UserHomeDir()
	tmp := filepath.Join(home, "tmp")
	if os.MkdirAll(tmp, 0o700) == nil {
		os.Setenv("TMPDIR", tmp)
	}
}

// realHome is the person's own home folder, not the sandbox's container:
// exports go to their real Downloads.
func realHome() (string, error) {
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir, nil
	}
	return os.UserHomeDir()
}

// containerData is where the App Store build keeps its data folder. The
// direct download uses it too once it holds a register, so a Mac that moved
// between the two builds still has one register, never two.
func containerData() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, err := realHome()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Containers", storeBundleID, "Data", "Library", "Application Support", "Banyan")
}
