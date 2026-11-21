// Package update keeps the desktop app current.
//
// Each release publishes update.json beside its disk image. It names the
// version, where the disk image is, and the image's size and SHA-256, and is
// signed with an Ed25519 key held only by the release workflow. The app
// carries the public half. A manifest that does not verify is ignored, so a
// compromised download location can at worst withhold an update, never
// supply one.
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// ManifestURL always names the newest release's manifest.
const ManifestURL = "https://github.com/satiyap/banyan/releases/latest/download/update.json"

// downloadPrefix is the only place a disk image may come from.
const downloadPrefix = "https://github.com/satiyap/banyan/releases/download/"

// Manifest describes one release.
type Manifest struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Notes     string `json:"notes"`
	Signature string `json:"signature"`
}

// message is what is signed: every field but the signature, one per line,
// with the notes last since they may hold newlines.
func (m Manifest) message() []byte {
	return []byte("banyan-update/1\n" + m.Version + "\n" + m.URL + "\n" + m.SHA256 + "\n" +
		strconv.FormatInt(m.Size, 10) + "\n" + m.Notes)
}

// Sign fills in the signature.
func (m *Manifest) Sign(private ed25519.PrivateKey) {
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, m.message()))
}

// Verify checks the signature and that the manifest is one the app could act on.
func (m Manifest) Verify(public ed25519.PublicKey) error { return m.VerifyFrom(public, downloadPrefix) }

// VerifyFrom is Verify with the disk image allowed under prefix instead of the
// release page. Only a development build testing an update uses it.
func (m Manifest) VerifyFrom(public ed25519.PublicKey, prefix string) error {
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(public) != ed25519.PublicKeySize || !ed25519.Verify(public, m.message(), sig) {
		return errors.New("the update's signature does not match")
	}
	if _, ok := parseVersion(m.Version); !ok {
		return errors.New("the update names no version")
	}
	if prefix == "" || !strings.HasPrefix(m.URL, prefix) || strings.Contains(m.URL[len(prefix):], "..") {
		return errors.New("the update is not on the Banyan release page")
	}
	if len(m.SHA256) != 64 || m.Size <= 0 || m.Size > 500<<20 {
		return errors.New("the update does not say what to expect")
	}
	return nil
}

// PublicKeyFrom decodes a base64 Ed25519 public key.
func PublicKeyFrom(b64 string) ed25519.PublicKey {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil
	}
	return key
}

// parseVersion reads x.y.z, ignoring a -suffix such as -dev.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Newer is true when candidate is a later release than current.
func Newer(candidate, current string) bool {
	a, ok1 := parseVersion(candidate)
	b, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
