package connection

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func file(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nested", "connection.json")
}

func isConnectionError(err error) bool {
	var ce *ConnectionError
	return errors.As(err, &ce)
}

func mustSave(t *testing.T, path, apiUrl, token string) {
	t.Helper()
	if _, err := SaveConnection(path, apiUrl, token, "", ""); err != nil {
		t.Fatal(err)
	}
}

func mustSkip(t *testing.T, path string, remember *Remembered) {
	t.Helper()
	if _, err := SkipConnection(path, remember); err != nil {
		t.Fatal(err)
	}
}

func expect(t *testing.T, got, want Connection) {
	t.Helper()
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNeverAskedIsUnset(t *testing.T) {
	expect(t, ReadConnection(file(t)), Connection{Status: "unset"})
}

func TestSavedConnectionIsReadBackTidied(t *testing.T) {
	path := file(t)
	mustSave(t, path, "  https://api.example.org/banyan/  ", "token-123")
	expect(t, ReadConnection(path), Connection{
		Status:     "connected",
		ApiUrl:     "https://api.example.org/banyan",
		Token:      "token-123",
		ServerName: "",
		Role:       "write",
	})
	raw, _ := os.ReadFile(path)
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	savedAt, _ := saved["saved_at"].(string)
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT`).MatchString(savedAt) {
		t.Fatalf("saved_at = %q", savedAt)
	}
}

func TestSkippingIsRememberedAndRemovesASavedConnection(t *testing.T) {
	path := file(t)
	mustSave(t, path, "https://api.example.org", "key-123")
	mustSkip(t, path, nil)
	expect(t, ReadConnection(path), Connection{Status: "skipped"})
}

func TestResettingTheKeyCanKeepTheAddress(t *testing.T) {
	path := file(t)
	mustSkip(t, path, &Remembered{ApiUrl: "https://api.example.org", ServerName: "Invented Association"})
	expect(t, ReadConnection(path), Connection{
		Status:     "skipped",
		ApiUrl:     "https://api.example.org",
		ServerName: "Invented Association",
	})
	got := DescribeConnection(ReadConnection(path))
	want := Description{Status: "skipped", ApiUrl: "https://api.example.org", HasKey: false, ServerName: "Invented Association", Role: ""}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "token") {
		t.Fatal("a skipped connection kept a token")
	}
}

func TestDamagedFileCountsAsNeverAsked(t *testing.T) {
	path := file(t)
	mustSkip(t, path, nil)
	for _, damaged := range []string{
		"{ not json",
		`{"status":"connected","api_url":"ftp://x","token":"t"}`,
		`{"status":"connected","api_url":"https://api.example.org","token":""}`,
		`{"status":"connected","api_url":"https://api.example.org"}`,
		`null`, `[]`, `42`, `"skipped"`, ``,
	} {
		if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
			t.Fatal(err)
		}
		expect(t, ReadConnection(path), Connection{Status: "unset"})
	}
}

func TestAPIURLMustBeHTTPSExceptOnThisMachine(t *testing.T) {
	for input, want := range map[string]string{
		"https://api.example.org":   "https://api.example.org",
		"http://localhost:8787/v1/": "http://localhost:8787/v1",
	} {
		got, err := CleanApiUrl(input)
		if err != nil || got != want {
			t.Fatalf("CleanApiUrl(%q) = %q, %v", input, got, err)
		}
	}
	for _, bad := range []any{"", "   ", "api.example.org", "http://api.example.org", "https://me:pw@api.example.org", "https://api.example.org/?a=1", 42, nil} {
		if _, err := CleanApiUrl(bad); !isConnectionError(err) {
			t.Fatalf("CleanApiUrl(%v) = %v, want a ConnectionError", bad, err)
		}
	}
}

func TestUserKeyMustBeOneUnbrokenValue(t *testing.T) {
	if got, err := CleanUserKey(" abc "); err != nil || got != "abc" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := CleanUserKey(strings.Repeat("x", 512)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []any{"", "  ", "two words", "line\nbreak", strings.Repeat("x", 513), nil} {
		if _, err := CleanUserKey(bad); !isConnectionError(err) {
			t.Fatalf("CleanUserKey(%v) = %v, want a ConnectionError", bad, err)
		}
	}
}

func TestRefusedSaveLeavesWhatWasOnFileAlone(t *testing.T) {
	path := file(t)
	mustSave(t, path, "https://api.example.org", "token-123")
	if _, err := SaveConnection(path, "http://other.example.org", "token-456", "", ""); !isConnectionError(err) {
		t.Fatalf("got %v, want a ConnectionError", err)
	}
	got := ReadConnection(path)
	if got.ApiUrl != "https://api.example.org" || got.Token != "token-123" {
		t.Fatalf("got %+v", got)
	}
}

func TestPageIsToldADeskIsConnectedNeverItsToken(t *testing.T) {
	got := DescribeConnection(Connection{Status: "connected", ApiUrl: "https://api.example.org", Token: "token-123", ServerName: "Invented Association", Role: "read"})
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"status":"connected","api_url":"https://api.example.org","has_key":true,"server_name":"Invented Association","role":"read"}`; string(raw) != want {
		t.Fatalf("got %s", raw)
	}
	raw, _ = json.Marshal(DescribeConnection(Connection{Status: "skipped"}))
	if want := `{"status":"skipped","api_url":"","has_key":false,"server_name":"","role":""}`; string(raw) != want {
		t.Fatalf("got %s", raw)
	}
	raw, _ = json.Marshal(DescribeConnection(Connection{Status: "unset"}))
	if want := `{"status":"unset","api_url":"","has_key":false,"server_name":"","role":""}`; string(raw) != want {
		t.Fatalf("got %s", raw)
	}
}

// The file must be what version 1 wrote, byte for byte.
func TestFileIsWrittenAsVersionOneWroteIt(t *testing.T) {
	Now = func() time.Time { return time.Date(2026, 11, 12, 10, 0, 1, 5e6, time.FixedZone("IST", 19800)) }
	t.Cleanup(func() { Now = time.Now })
	path := file(t)

	if _, err := SaveConnection(path, "https://api.example.org/", "token-123", "Flat \"12\" <Owners> & Co ", "read"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	want := "{\n  \"status\": \"connected\",\n  \"api_url\": \"https://api.example.org\",\n  \"token\": \"token-123\",\n" +
		"  \"server_name\": \"Flat \\\"12\\\" <Owners> & Co \",\n  \"role\": \"read\",\n  \"saved_at\": \"2026-11-12T04:30:01.005Z\"\n}\n"
	if string(raw) != want {
		t.Fatalf("got %q", raw)
	}

	mustSkip(t, path, &Remembered{ApiUrl: "https://api.example.org", ServerName: "Invented Association"})
	raw, _ = os.ReadFile(path)
	want = "{\n  \"status\": \"skipped\",\n  \"api_url\": \"https://api.example.org\",\n  \"server_name\": \"Invented Association\",\n" +
		"  \"saved_at\": \"2026-11-12T04:30:01.005Z\"\n}\n"
	if string(raw) != want {
		t.Fatalf("got %q", raw)
	}

	mustSkip(t, path, nil)
	raw, _ = os.ReadFile(path)
	if want = "{\n  \"status\": \"skipped\",\n  \"saved_at\": \"2026-11-12T04:30:01.005Z\"\n}\n"; string(raw) != want {
		t.Fatalf("got %q", raw)
	}
	if _, err := os.Stat(path + ".partial"); !os.IsNotExist(err) {
		t.Fatal("the partial file was left behind")
	}
}

// A file exactly as version 1 left it.
func TestVersionOneFileIsRead(t *testing.T) {
	path := file(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	v1 := "{\n  \"status\": \"connected\",\n  \"api_url\": \"https://api.example.org/banyan\",\n  \"token\": \"token-123\",\n" +
		"  \"server_name\": \"Invented Association\",\n  \"role\": \"read\",\n  \"saved_at\": \"2026-11-12T10:00:01.000Z\"\n}\n"
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	expect(t, ReadConnection(path), Connection{Status: "connected", ApiUrl: "https://api.example.org/banyan", Token: "token-123", ServerName: "Invented Association", Role: "read"})

	// Written before roles existed: a desk.
	if err := os.WriteFile(path, []byte(`{"status":"connected","api_url":"https://api.example.org","token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	expect(t, ReadConnection(path), Connection{Status: "connected", ApiUrl: "https://api.example.org", Token: "t", Role: "write"})

	// A remembered address that no longer passes is forgotten.
	if err := os.WriteFile(path, []byte(`{"status":"skipped","api_url":"http://api.example.org","server_name":"X"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	expect(t, ReadConnection(path), Connection{Status: "skipped"})
}

// Expected values are what version 1 (Node's URL) gives for the same input.
func TestAddressesAreReadAsVersionOneReadThem(t *testing.T) {
	const (
		notAddress = "That is not a web address. It should look like https://api.example.org"
		notHTTPS   = "The API URL must start with https://"
		hasUser    = "Leave any user name or password out of the API URL."
		hasQuery   = "Leave anything after ? or # out of the API URL."
	)
	cases := []struct{ input, want, message string }{
		{"https://api.example.org", "https://api.example.org", ""},
		{"http://localhost:8787/v1/", "http://localhost:8787/v1", ""},
		{"api.example.org", "", notAddress},
		{"http://api.example.org", "", notHTTPS},
		{"https://me:pw@api.example.org", "", hasUser},
		{"https://api.example.org/?a=1", "", hasQuery},
		{"https://api.example.org/?", "https://api.example.org", ""},
		{"https://api.example.org/#", "https://api.example.org", ""},
		{"https://api.example.org/#x", "", hasQuery},
		{"HTTPS://API.Example.ORG:443/Banyan//", "https://api.example.org/Banyan", ""},
		{"https://api.example.org:8443/a/../b/./c/", "https://api.example.org:8443/b/c", ""},
		{"https:api.example.org", "https://api.example.org", ""},
		{"https:/api.example.org", "https://api.example.org", ""},
		{"https:\\\\api.example.org\\x\\", "https://api.example.org/x", ""},
		{"https://@api.example.org", "https://api.example.org", ""},
		{"https://:@api.example.org", "https://api.example.org", ""},
		{"https://u@api.example.org", "", hasUser},
		{"https://", "", notAddress},
		{"https://:443", "", notAddress},
		{"https://api.example.org:", "https://api.example.org", ""},
		{"https://api.example.org:99999", "", notAddress},
		{"https://api.example.org:8x", "", notAddress},
		{"http://127.1", "http://127.0.0.1", ""},
		{"http://127.0.0.1:80/", "http://127.0.0.1", ""},
		{"http://0x7f.0.0.1", "http://127.0.0.1", ""},
		{"http://2130706433", "http://127.0.0.1", ""},
		{"http://[::1]:8787", "http://[::1]:8787", ""},
		{"http://[0:0:0:0:0:0:0:1]/x", "http://[::1]/x", ""},
		{"https://[2001:DB8::0:1]/", "https://[2001:db8::1]", ""},
		{"https://[::ffff:1.2.3.4]", "https://[::ffff:102:304]", ""},
		{"http://LOCALHOST", "http://localhost", ""},
		{"http://localhost.", "", notHTTPS},
		{"localhost:8787", "", notHTTPS},
		{"ftp://x", "", notHTTPS},
		{"ftp://", "", notAddress},
		{"mailto:a@b", "", notHTTPS},
		{"file:///tmp/x", "", notHTTPS},
		{"https://exa mple.org", "", notAddress},
		{"https://ex%61mple.org", "https://example.org", ""},
		{"https://a..b/", "https://a..b", ""},
		{"https://1.2.3.4.5", "", notAddress},
		{"https://api.example.org/a b/c^d/{e}/`f`/\"g\"/<h>", "https://api.example.org/a%20b/c%5Ed/%7Be%7D/%60f%60/%22g%22/%3Ch%3E", ""},
		{"https://api.example.org/%7euser/%2e%2e/x", "https://api.example.org/x", ""},
		{"https://api.example.org/a/..", "https://api.example.org", ""},
		{"https://api.example.org/a/.", "https://api.example.org/a", ""},
		{"https://api.example.org/a/%2E", "https://api.example.org/a", ""},
		{"https://api.example.org/caf\u00e9", "https://api.example.org/caf%C3%A9", ""},
		{"https://a_b.example.org", "https://a_b.example.org", ""},
		{"https://a!$&'()*+,;=~.example.org", "https://a!$&'()*+,;=~.example.org", ""},
		{"https://a\"b{c}`.example.org", "https://a\"b{c}`.example.org", ""},
		{"https://a|b", "", notAddress},
		{"https://a^b", "", notAddress},
		{"https://256.256.256.256", "", notAddress},
		{"https://1.2.3", "https://1.2.0.3", ""},
		{"https://0x.0x", "https://0.0.0.0", ""},
		{"https://api.example.org\t/x\n", "https://api.example.org/x", ""},
		{"  https://api.example.org  ", "https://api.example.org", ""},
		{"\u0001https://api.example.org\u0002", "https://api.example.org", ""},
		{"1https://x", "", notAddress},
		{"ws://x", "", notHTTPS},
		{"wss://x:443/", "", notHTTPS},
		{"https://[::1", "", notAddress},
		{"https://[1.2.3.4]", "", notAddress},
		{"https://[::1%25eth0]", "", notAddress},
		{"https://api.example.org//", "https://api.example.org", ""},
		{"https://api.example.org/a//b//", "https://api.example.org/a//b", ""},
		{"https://[1:0:0:2:0:0:0:3]", "https://[1:0:0:2::3]", ""},
		{"https://[0:0:1:0:0:1:0:0]", "https://[::1:0:0:1:0:0]", ""},
		{"https://[::]", "https://[::]", ""},
		{"https://[1::]", "https://[1::]", ""},
		{"https://x:0443", "https://x", ""},
		{"https://x:0", "https://x:0", ""},
		{"http://127.0.0.1.", "http://127.0.0.1", ""},
		{"https://09.1.1.1", "", notAddress},
		{"https://1.2.3.08", "", notAddress},
	}
	for _, c := range cases {
		got, err := CleanApiUrl(c.input)
		message := ""
		if err != nil {
			message = err.Error()
		}
		if got != c.want || message != c.message {
			t.Errorf("CleanApiUrl(%q) = %q, %q; want %q, %q", c.input, got, message, c.want, c.message)
		}
		if err == nil {
			if again, err := CleanApiUrl(got); err != nil || again != got {
				t.Errorf("CleanApiUrl(%q) is not stable: %q, %v", got, again, err)
			}
		}
	}
}
