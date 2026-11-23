package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

func signed(t *testing.T, private ed25519.PrivateKey, version string, image []byte) Manifest {
	sum := sha256.Sum256(image)
	m := Manifest{
		Version: version,
		URL:     downloadPrefix + "v" + version + "/Banyan-mac-arm64.dmg",
		SHA256:  hex.EncodeToString(sum[:]),
		Size:    int64(len(image)),
		Notes:   "- An invented change.",
	}
	m.Sign(private)
	return m
}

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2.1.2", "2.1.1", true},
		{"2.10.0", "2.9.9", true},
		{"3.0.0", "2.99.99", true},
		{"2.1.1", "2.1.1", false},
		{"2.1.0", "2.1.1", false},
		{"2.1.1", "2.1.1-dev", false},
		{"2.1.2", "2.1.1-dev", true},
		{"nonsense", "2.1.1", false},
		{"2.1", "2.0.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestOnlyASignedManifestIsBelieved(t *testing.T) {
	public, private := testKey(t)
	good := signed(t, private, "2.2.0", []byte("image"))
	if err := good.Verify(public); err != nil {
		t.Fatalf("a good manifest refused: %v", err)
	}
	other, _ := testKey(t)
	if good.Verify(other) == nil {
		t.Error("believed under another key")
	}
	for name, change := range map[string]func(*Manifest){
		"version": func(m *Manifest) { m.Version = "9.9.9" },
		"url":     func(m *Manifest) { m.URL = "https://example.invalid/Banyan.dmg" },
		"hash":    func(m *Manifest) { m.SHA256 = strings.Repeat("0", 64) },
		"size":    func(m *Manifest) { m.Size++ },
		"notes":   func(m *Manifest) { m.Notes = "something else" },
	} {
		m := good
		change(&m)
		if m.Verify(public) == nil {
			t.Errorf("a changed %s still verified", name)
		}
	}
	// Signed, but pointing somewhere other than the release page.
	elsewhere := good
	elsewhere.URL = "https://example.invalid/Banyan.dmg"
	elsewhere.Sign(private)
	if elsewhere.Verify(public) == nil {
		t.Error("an image off the release page was accepted")
	}
}

func TestTheShippedKeyIsAKey(t *testing.T) {
	if len(PublicKey()) != ed25519.PublicKeySize {
		t.Fatal("the app carries no usable update key")
	}
}

func serve(t *testing.T, manifest any, image []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/update.json":
			_ = json.NewEncoder(w).Encode(manifest)
		default:
			_, _ = w.Write(image)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func updater(t *testing.T, public ed25519.PublicKey, url string) *Updater {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "Apps", "Banyan.app")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	return New("2.1.1", url+"/update.json", public, filepath.Join(dir, "updates"), bundle)
}

func TestCheckFindsANewerRelease(t *testing.T) {
	public, private := testKey(t)
	srv := serve(t, signed(t, private, "2.2.0", []byte("image")), nil)
	u := updater(t, public, srv.URL)
	s := u.Check(context.Background())
	if s.Status != StatusAvailable || s.Available != "2.2.0" || s.Notes == "" || s.Blocked != "" {
		t.Fatalf("%+v", s)
	}
}

func TestCheckSaysWhenCurrent(t *testing.T) {
	public, private := testKey(t)
	srv := serve(t, signed(t, private, "2.1.1", []byte("image")), nil)
	if s := updater(t, public, srv.URL).Check(context.Background()); s.Status != StatusCurrent {
		t.Fatalf("%+v", s)
	}
}

func TestATamperedManifestIsIgnored(t *testing.T) {
	public, private := testKey(t)
	m := signed(t, private, "2.2.0", []byte("image"))
	m.Version = "2.3.0"
	srv := serve(t, m, nil)
	s := updater(t, public, srv.URL).Check(context.Background())
	if s.Status != StatusError || s.Available != "" {
		t.Fatalf("%+v", s)
	}
}

func TestADownloadThatIsNotTheNamedFileIsRefused(t *testing.T) {
	public, private := testKey(t)
	m := signed(t, private, "2.2.0", []byte("the real image"))
	srv := serve(t, m, []byte("something else!"))
	u := updater(t, public, srv.URL)
	wrong := m
	wrong.URL = srv.URL + "/x.dmg"
	err := u.download(context.Background(), &wrong, filepath.Join(t.TempDir(), "x.dmg"))
	if err == nil || !strings.Contains(err.Error(), "isn't the file") {
		t.Fatalf("accepted: %v", err)
	}
	right := serve(t, m, []byte("the real image"))
	m2 := m
	m2.URL = right.URL + "/x.dmg"
	if err := u.download(context.Background(), &m2, filepath.Join(t.TempDir(), "y.dmg")); err != nil {
		t.Fatalf("the right file refused: %v", err)
	}
}

func TestAnAppThatCannotReplaceItselfSaysWhy(t *testing.T) {
	public, _ := testKey(t)
	for bundle, want := range map[string]string{
		"/Volumes/Banyan/Banyan.app":                             "disk image",
		"/private/var/folders/x/AppTranslocation/y/d/Banyan.app": "temporary place",
		"": "not an app bundle",
	} {
		u := New("2.1.1", "", public, t.TempDir(), bundle)
		if !strings.Contains(u.State().Blocked, want) {
			t.Errorf("%q: %q", bundle, u.State().Blocked)
		}
	}
	if u := New("2.1.1", "", nil, t.TempDir(), "/Applications/Banyan.app"); !strings.Contains(u.State().Blocked, "no update key") {
		t.Errorf("no key: %q", u.State().Blocked)
	}
}

func TestBundleOf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Mac app bundles only")
	}
	if got := BundleOf("/Applications/Banyan.app/Contents/MacOS/Banyan"); got != "/Applications/Banyan.app" {
		t.Errorf("%q", got)
	}
	if got := BundleOf("/usr/local/bin/banyan"); got != "" {
		t.Errorf("%q", got)
	}
}
