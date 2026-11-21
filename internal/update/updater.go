package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Statuses, as Settings shows them.
const (
	StatusIdle        = "idle"        // not checked yet
	StatusChecking    = "checking"    // asking for the manifest
	StatusCurrent     = "current"     // this is the newest release
	StatusAvailable   = "available"   // a newer release can be installed
	StatusDownloading = "downloading" // fetching and checking the disk image
	StatusReady       = "ready"       // the new app is staged; quitting swaps it in
	StatusError       = "error"       // the last step failed; Error says why
)

// BundleID is the identifier every release carries. A disk image holding
// anything else is not installed.
const BundleID = "com.banyanrwa.chequedesk"

// State is what Settings is told.
type State struct {
	Status    string  `json:"status"`
	Current   string  `json:"current"`
	Available string  `json:"available,omitempty"`
	Notes     string  `json:"notes,omitempty"`
	CheckedAt string  `json:"checked_at,omitempty"`
	Progress  float64 `json:"progress,omitempty"`
	Error     string  `json:"error,omitempty"`
	// Blocked says why this copy of the app cannot replace itself, when it can't.
	Blocked string `json:"blocked,omitempty"`
}

// Updater checks for, fetches and stages a new release of the app.
type Updater struct {
	Current     string            // this app's version
	ManifestURL string            // where update.json is
	PublicKey   ed25519.PublicKey // the release workflow's signing key
	Dir         string            // a folder of the app's own, for downloads and the previous app
	Bundle      string            // the running Banyan.app
	// DownloadPrefix is where a disk image may come from: the release page,
	// unless a development build is testing against its own server.
	DownloadPrefix string
	Client         *http.Client
	Now            func() time.Time

	mu       sync.Mutex
	state    State
	manifest *Manifest
	staged   string
}

// New prepares an updater for the app at bundle.
func New(current, manifestURL string, key ed25519.PublicKey, dir, bundle string) *Updater {
	u := &Updater{
		Current: current, ManifestURL: manifestURL, PublicKey: key, Dir: dir, Bundle: bundle,
		DownloadPrefix: downloadPrefix,
		Client:         &http.Client{Timeout: 10 * time.Minute},
		Now:            time.Now,
	}
	u.state = State{Status: StatusIdle, Current: current, Blocked: u.blocked()}
	return u
}

// State is a copy of where the updater is.
func (u *Updater) State() State {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

func (u *Updater) set(change func(*State)) {
	u.mu.Lock()
	change(&u.state)
	u.mu.Unlock()
}

// blocked says why the app cannot replace itself where it is, or "".
func (u *Updater) blocked() string {
	switch {
	case len(u.PublicKey) != ed25519.PublicKeySize:
		return "This build has no update key."
	case u.Bundle == "" || !strings.HasSuffix(u.Bundle, ".app"):
		return "This copy of Banyan is not an app bundle."
	case strings.Contains(u.Bundle, "/AppTranslocation/"):
		return "macOS is running Banyan from a temporary place. Move Banyan to Applications and open it from there."
	case strings.HasPrefix(u.Bundle, "/Volumes/"):
		return "Banyan is running from the disk image. Drag it to Applications and open it from there."
	}
	probe, err := os.CreateTemp(filepath.Dir(u.Bundle), ".banyan-write-check-")
	if err != nil {
		return "Banyan can't write to " + filepath.Dir(u.Bundle) + ", so it can't replace itself there."
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	return ""
}

// Check asks for the newest release. A newer one leaves the state Available.
func (u *Updater) Check(ctx context.Context) State {
	u.mu.Lock()
	if busy := u.state.Status; busy == StatusChecking || busy == StatusDownloading || busy == StatusReady {
		s := u.state
		u.mu.Unlock()
		return s
	}
	u.state.Status, u.state.Error = StatusChecking, ""
	u.mu.Unlock()

	m, err := u.fetchManifest(ctx)
	now := u.Now().UTC().Format(time.RFC3339)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.state.CheckedAt = now
	switch {
	case err != nil:
		u.state.Status, u.state.Error = StatusError, err.Error()
	case Newer(m.Version, u.Current):
		u.manifest = m
		u.state.Status, u.state.Available, u.state.Notes = StatusAvailable, m.Version, m.Notes
	default:
		u.manifest = nil
		u.state.Status, u.state.Available, u.state.Notes = StatusCurrent, "", ""
	}
	return u.state
}

func (u *Updater) fetchManifest(ctx context.Context) (*Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.ManifestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Banyan/"+u.Current)
	reply, err := u.Client.Do(req)
	if err != nil {
		return nil, errors.New("Couldn't reach the release page. Check the internet connection.")
	}
	defer reply.Body.Close()
	if reply.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("The release page answered %d.", reply.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(reply.Body, 1<<20)).Decode(&m); err != nil {
		return nil, errors.New("The release page sent something that isn't an update.")
	}
	if err := m.VerifyFrom(u.PublicKey, u.DownloadPrefix); err != nil {
		return nil, errors.New("The update didn't check out (" + err.Error() + "), so it was ignored.")
	}
	return &m, nil
}

// Prepare downloads the disk image, checks it, and stages the new app beside
// the running one. Nothing running is touched: after Prepare, Swap does that.
func (u *Updater) Prepare(ctx context.Context) error {
	u.mu.Lock()
	m := u.manifest
	if m == nil || u.state.Status != StatusAvailable {
		u.mu.Unlock()
		return errors.New("There is no update to install.")
	}
	if why := u.blocked(); why != "" {
		u.state.Blocked = why
		u.mu.Unlock()
		return errors.New(why)
	}
	u.state.Status, u.state.Progress, u.state.Error = StatusDownloading, 0, ""
	u.mu.Unlock()

	staged, err := u.prepare(ctx, m)
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		// The manifest is still good, so another try needs no new check.
		u.state.Status, u.state.Error = StatusAvailable, err.Error()
		return err
	}
	u.staged = staged
	u.state.Status, u.state.Progress = StatusReady, 1
	return nil
}

func (u *Updater) prepare(ctx context.Context, m *Manifest) (string, error) {
	if err := os.MkdirAll(u.Dir, 0o755); err != nil {
		return "", err
	}
	dmg := filepath.Join(u.Dir, "Banyan-"+m.Version+".dmg")
	defer os.Remove(dmg)
	if err := u.download(ctx, m, dmg); err != nil {
		return "", err
	}

	mount, err := os.MkdirTemp("", "banyan-update-")
	if err != nil {
		return "", err
	}
	defer os.Remove(mount)
	if out, err := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen",
		"-mountpoint", mount, dmg).CombinedOutput(); err != nil {
		return "", fmt.Errorf("The disk image couldn't be opened: %s", strings.TrimSpace(string(out)))
	}
	defer exec.Command("hdiutil", "detach", "-force", mount).Run()

	// Staged beside the running app, so the swap is a rename on one volume.
	staged := StagedPath(u.Bundle)
	os.RemoveAll(staged)
	if out, err := exec.CommandContext(ctx, "ditto", filepath.Join(mount, "Banyan.app"), staged).CombinedOutput(); err != nil {
		os.RemoveAll(staged)
		return "", fmt.Errorf("The new app couldn't be copied: %s", strings.TrimSpace(string(out)))
	}
	if err := checkBundle(staged, m.Version); err != nil {
		os.RemoveAll(staged)
		return "", err
	}
	return staged, nil
}

// download fetches the disk image and refuses it unless its size and SHA-256
// are the ones the signed manifest named.
func (u *Updater) download(ctx context.Context, m *Manifest, to string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Banyan/"+u.Current)
	reply, err := u.Client.Do(req)
	if err != nil {
		return errors.New("The download didn't get through. Check the internet connection and try again.")
	}
	defer reply.Body.Close()
	if reply.StatusCode != http.StatusOK {
		return fmt.Errorf("The download answered %d.", reply.StatusCode)
	}
	file, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	var got int64
	buf := make([]byte, 256<<10)
	body := io.LimitReader(reply.Body, m.Size+1)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, err := file.Write(buf[:n]); err != nil {
				return err
			}
			hash.Write(buf[:n])
			got += int64(n)
			progress := float64(got) / float64(m.Size)
			u.set(func(s *State) { s.Progress = min(progress, 1) })
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return errors.New("The download was cut off. Try again.")
		}
	}
	if got != m.Size || hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(m.SHA256) {
		return errors.New("The download isn't the file the update named, so it wasn't used.")
	}
	return file.Close()
}

// checkBundle confirms the staged app is Banyan, at the version promised,
// with an intact code signature.
func checkBundle(app, version string) error {
	plist := filepath.Join(app, "Contents", "Info.plist")
	read := func(key string) string {
		out, _ := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :"+key, plist).Output()
		return strings.TrimSpace(string(out))
	}
	if id := read("CFBundleIdentifier"); id != BundleID {
		return fmt.Errorf("The disk image holds %q, not Banyan.", id)
	}
	if v := read("CFBundleShortVersionString"); v != version {
		return fmt.Errorf("The disk image holds Banyan %s, not %s.", v, version)
	}
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("The new app's signature is broken: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// StagedPath is where a new app waits beside the running one.
func StagedPath(bundle string) string {
	return filepath.Join(filepath.Dir(bundle), ".Banyan-update.app")
}

// PreviousPath is where the replaced app is kept, one version back.
func (u *Updater) PreviousPath() string { return filepath.Join(u.Dir, "Banyan-previous.app") }

// Swap starts a helper that waits for this process to end, puts the staged
// app in place of the running one, keeps the old one in the updates folder,
// and opens the new one. If the new app can't be moved in, the old one goes
// back and opens instead. Call it, then quit.
func (u *Updater) Swap() error {
	u.mu.Lock()
	staged := u.staged
	u.mu.Unlock()
	if staged == "" {
		return errors.New("No update is ready.")
	}
	cmd := exec.Command("/bin/sh", "-c", swapScript,
		"banyan-update", strconv.Itoa(os.Getpid()), staged, u.Bundle, u.PreviousPath())
	// A session of its own. When an app macOS launched exits, launchd kills
	// what is left in its process group; in the app's group the helper died
	// between moving the new app in and opening it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		u.set(func(s *State) { s.Status, s.Error = StatusError, "The new app couldn't be swapped in: "+err.Error() })
		return err
	}
	return nil
}

// swapScript runs after the app has quit. $1 pid, $2 staged, $3 bundle, $4 previous.
const swapScript = `
pid="$1"; staged="$2"; bundle="$3"; previous="$4"
i=0
while kill -0 "$pid" 2>/dev/null; do
  i=$((i + 1)); [ "$i" -gt 600 ] && exit 1
  sleep 0.1
done
reopen() {
  # LaunchServices can take a moment to let go of the app that just quit.
  open "$bundle" || { sleep 1; open "$bundle"; }
}
rm -rf "$previous"
if ! mv "$bundle" "$previous"; then
  rm -rf "$staged"; reopen; exit 1
fi
if mv "$staged" "$bundle"; then
  reopen
else
  mv "$previous" "$bundle"; reopen; exit 1
fi
`

// Tidy removes a staged app left behind by an update that never finished.
func Tidy(bundle string) {
	if bundle != "" && strings.HasSuffix(bundle, ".app") {
		os.RemoveAll(StagedPath(bundle))
	}
}

// BundleOf returns the .app holding the executable, or "".
func BundleOf(executable string) string {
	resolved, err := filepath.EvalSymlinks(executable)
	if err == nil {
		executable = resolved
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	if filepath.Base(filepath.Dir(executable)) != "MacOS" || !strings.HasSuffix(bundle, ".app") {
		return ""
	}
	return bundle
}
