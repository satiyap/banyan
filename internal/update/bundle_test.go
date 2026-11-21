package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeApp is a minimal signed bundle with the given identifier and version.
func fakeApp(t *testing.T, id, version string) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Banyan.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>` +
		`<key>CFBundleIdentifier</key><string>` + id + `</string>` +
		`<key>CFBundleShortVersionString</key><string>` + version + `</string>` +
		`<key>CFBundleExecutable</key><string>Banyan</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "Banyan"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
		t.Fatalf("codesign: %v %s", err, out)
	}
	return app
}

func TestAnUpdateMayCarryEitherBanyanIdentifier(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("bundles and codesign are macOS's")
	}
	for _, id := range BundleIDs {
		if err := checkBundle(fakeApp(t, id, "2.5.1"), "2.5.1"); err != nil {
			t.Errorf("%s refused: %v", id, err)
		}
	}
	if err := checkBundle(fakeApp(t, "com.example.other", "2.5.1"), "2.5.1"); err == nil || !strings.Contains(err.Error(), "not Banyan") {
		t.Errorf("another app accepted: %v", err)
	}
	if err := checkBundle(fakeApp(t, "com.banyan.desk", "2.5.0"), "2.5.1"); err == nil {
		t.Error("the wrong version accepted")
	}
}
