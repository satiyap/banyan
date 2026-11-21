// updatesign writes the signed update.json a release publishes beside its
// disk image, and makes the key pair it is signed with.
//
//	go run ./cmd/updatesign -generate PRIVATE_KEY_FILE
//	    writes a new private key (base64) to the file, mode 0600, and prints
//	    the public key for internal/update/key.go.
//
//	UPDATE_SIGNING_KEY=... go run ./cmd/updatesign -version 2.2.0 \
//	    -dmg dist/Banyan-mac-arm64.dmg -notes notes.md -out dist/update.json
//
// The private key never enters the repository: the release workflow reads it
// from the UPDATE_SIGNING_KEY secret.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/satiyap/banyan/internal/update"
)

func main() {
	generate := flag.String("generate", "", "write a new private key to this file")
	version := flag.String("version", "", "the release's version, e.g. 2.2.0")
	dmg := flag.String("dmg", "", "the disk image")
	notesFile := flag.String("notes", "", "a file holding the release notes")
	out := flag.String("out", "update.json", "where to write the manifest")
	url := flag.String("url", "", "the disk image's address, for a local test (default: the release page)")
	flag.Parse()

	if *generate != "" {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			log.Fatal(err)
		}
		file, err := os.OpenFile(*generate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		if _, err := fmt.Fprintln(file, base64.StdEncoding.EncodeToString(private)); err != nil {
			log.Fatal(err)
		}
		if err := file.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Println(base64.StdEncoding.EncodeToString(public))
		return
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("UPDATE_SIGNING_KEY")))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		log.Fatal("UPDATE_SIGNING_KEY is not set to a base64 Ed25519 private key")
	}
	private := ed25519.PrivateKey(raw)
	if *version == "" || *dmg == "" {
		log.Fatal("-version and -dmg are needed")
	}

	file, err := os.Open(*dmg)
	if err != nil {
		log.Fatal(err)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	file.Close()
	if err != nil {
		log.Fatal(err)
	}
	notes := ""
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			log.Fatal(err)
		}
		notes = strings.TrimSpace(string(b))
	}

	address := *url
	if address == "" {
		address = "https://github.com/satiyap/banyan/releases/download/v" + *version + "/Banyan-mac-arm64.dmg"
	}
	m := update.Manifest{
		Version: *version,
		URL:     address,
		SHA256:  hex.EncodeToString(hash.Sum(nil)),
		Size:    size,
		Notes:   notes,
	}
	m.Sign(private)
	// The app will refuse anything that fails here, so this checks it first.
	if *url == "" {
		if err := m.Verify(update.PublicKey()); err != nil {
			log.Fatalf("the app would refuse this manifest: %v", err)
		}
	}
	body, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(*out, append(body, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s for %s (%d bytes)\n", *out, *version, size)
}
