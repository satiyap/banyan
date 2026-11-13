// Package connection is the desk's link to the Banyan server: an API URL and
// a user key.
//
// Asked for once, on first launch, and skippable: the desk writes and prints
// cheques without it. This package only remembers the answer; desksync is
// what talks to the server.
//
// The key is single-use: the desk redeems it for a long-lived token and the
// key is spent, so the token is what is kept. It sits in a plain file beside
// the register on purpose. The server is what protects it: a token can be
// retired there, and is, the moment another desk takes over.
//
// No server address ships with the app. Anyone can download the app; only
// someone given the address and a key can reach the association's server.
package connection

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// The three states a Connection can be in.
const (
	// Never asked. The app shows the connection screen before the desk.
	StatusUnset = "unset"
	// Asked, and no key is on file: skipped, or the key was reset. A server
	// address that worked before is remembered, so only a new key is needed.
	StatusSkipped = "skipped"
	// A key was redeemed and its token is on file.
	StatusConnected = "connected"
)

// Connection is what is on file. ApiUrl and ServerName are set when connected,
// and when skipped with a remembered server. Token and Role only when
// connected: Role "write" makes this the cheque desk, "read" a reports-only app.
type Connection struct {
	Status     string
	ApiUrl     string
	Token      string
	ServerName string
	Role       string
}

// Remembered is the address of a server that has already answered.
type Remembered struct {
	ApiUrl     string
	ServerName string
}

// Description is what the page is shown. The token is never sent to it.
type Description struct {
	Status     string `json:"status"`
	ApiUrl     string `json:"api_url"`
	HasKey     bool   `json:"has_key"`
	ServerName string `json:"server_name"`
	Role       string `json:"role"`
}

// ConnectionError carries a message meant for the treasurer.
type ConnectionError struct {
	Message string
}

func (e *ConnectionError) Error() string { return e.Message }

func fail(message string) error { return &ConnectionError{Message: message} }

const maxKey = 512

// Now is the clock behind saved_at. Tests may replace it.
var Now = time.Now

// CleanApiUrl gives the API URL as it will be stored: https, no credentials,
// no query, no trailing slash.
func CleanApiUrl(input any) (string, error) {
	text, _ := input.(string)
	text = jsTrim(text)
	if text == "" {
		return "", fail("Enter the API URL.")
	}
	u, ok := parseURL(text)
	if !ok {
		return "", fail("That is not a web address. It should look like https://api.example.org")
	}
	local := u.host == "localhost" || u.host == "127.0.0.1" || u.host == "[::1]"
	if u.scheme != "https" && !(u.scheme == "http" && local) {
		return "", fail("The API URL must start with https://")
	}
	if u.credentials {
		return "", fail("Leave any user name or password out of the API URL.")
	}
	if u.query != "" || u.fragment != "" {
		return "", fail("Leave anything after ? or # out of the API URL.")
	}
	return u.origin() + strings.TrimRight(u.path, "/"), nil
}

// CleanUserKey gives the key as it will be sent: one unbroken value.
func CleanUserKey(input any) (string, error) {
	text, _ := input.(string)
	text = jsTrim(text)
	if text == "" {
		return "", fail("Enter the user key.")
	}
	if strings.IndexFunc(text, isJSSpace) >= 0 {
		return "", fail("The user key has a space or line break in it. Paste it again.")
	}
	if len(utf16.Encode([]rune(text))) > maxKey {
		return "", fail("That is too long to be a user key.")
	}
	return text, nil
}

// ReadConnection gives what is on file. A missing or unreadable file counts
// as never asked.
func ReadConnection(path string) Connection {
	unset := Connection{Status: StatusUnset}
	raw, err := os.ReadFile(path)
	if err != nil {
		return unset
	}
	var parsed any
	if json.Unmarshal(raw, &parsed) != nil {
		return unset
	}
	saved, ok := parsed.(map[string]any)
	if !ok {
		return unset
	}
	serverName, _ := saved["server_name"].(string)
	if saved["status"] == StatusSkipped {
		if address, _ := saved["api_url"].(string); address != "" {
			// An address that no longer passes is simply forgotten.
			if apiUrl, err := CleanApiUrl(address); err == nil {
				return Connection{Status: StatusSkipped, ApiUrl: apiUrl, ServerName: serverName}
			}
		}
		return Connection{Status: StatusSkipped}
	}
	if token, _ := saved["token"].(string); saved["status"] == StatusConnected && token != "" {
		apiUrl, err := CleanApiUrl(saved["api_url"])
		if err != nil {
			return unset
		}
		role := "write"
		if saved["role"] == "read" {
			role = "read"
		}
		return Connection{Status: StatusConnected, ApiUrl: apiUrl, Token: token, ServerName: serverName, Role: role}
	}
	return unset
}

type field struct{ key, value string }

func write(path string, fields []field) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	fields = append(fields, field{"saved_at", Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	var b strings.Builder
	b.WriteString("{\n")
	for i, f := range fields {
		b.WriteString("  " + jsQuote(f.key) + ": " + jsQuote(f.value))
		if i < len(fields)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("}\n")
	// Written whole and moved into place, so a crash cannot leave half a file.
	partial := path + ".partial"
	if err := os.WriteFile(partial, []byte(b.String()), 0o666); err != nil {
		return err
	}
	return os.Rename(partial, path)
}

// SaveConnection records a redeemed token. An empty role means "write".
func SaveConnection(path, apiUrl, token, serverName, role string) (Connection, error) {
	cleaned, err := CleanApiUrl(apiUrl)
	if err != nil {
		return Connection{}, err
	}
	if role == "" {
		role = "write"
	}
	c := Connection{Status: StatusConnected, ApiUrl: cleaned, Token: token, ServerName: serverName, Role: role}
	err = write(path, []field{
		{"status", c.Status},
		{"api_url", c.ApiUrl},
		{"token", c.Token},
		{"server_name", c.ServerName},
		{"role", c.Role},
	})
	if err != nil {
		return Connection{}, err
	}
	return c, nil
}

// SkipConnection carries on without a server, or forgets the key. remember
// keeps the address of a server that has already answered, so the key alone
// can be replaced.
func SkipConnection(path string, remember *Remembered) (Connection, error) {
	c := Connection{Status: StatusSkipped}
	fields := []field{{"status", c.Status}}
	if remember != nil {
		c.ApiUrl, c.ServerName = remember.ApiUrl, remember.ServerName
		fields = append(fields, field{"api_url", c.ApiUrl}, field{"server_name", c.ServerName})
	}
	if err := write(path, fields); err != nil {
		return Connection{}, err
	}
	return c, nil
}

// DescribeConnection gives what the page is shown. The token is never sent to it.
func DescribeConnection(c Connection) Description {
	if c.Status == StatusConnected {
		return Description{Status: StatusConnected, ApiUrl: c.ApiUrl, HasKey: true, ServerName: c.ServerName, Role: c.Role}
	}
	d := Description{Status: c.Status}
	if c.Status == StatusSkipped {
		d.ApiUrl, d.ServerName = c.ApiUrl, c.ServerName
	}
	return d
}

// isJSSpace reports what JavaScript's trim() and \s count as white space.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// jsQuote writes a string the way JSON.stringify does.
func jsQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
