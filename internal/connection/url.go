package connection

import (
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
)

// A web address read the way a browser's URL parser reads it, which is what
// version 1 stored. Only what CleanApiUrl needs is kept.
type parsedURL struct {
	scheme      string
	host        string // lower case; an IPv6 address keeps its brackets
	port        string // empty when absent or the scheme's default
	path        string // always starts with /
	query       string
	fragment    string
	credentials bool
}

func (u parsedURL) origin() string {
	if u.port != "" {
		return u.scheme + "://" + u.host + ":" + u.port
	}
	return u.scheme + "://" + u.host
}

var defaultPorts = map[string]string{"http": "80", "https": "443", "ftp": "21", "ws": "80", "wss": "443"}

func isAlpha(c byte) bool { return (c|0x20) >= 'a' && (c|0x20) <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// parseURL follows the WHATWG URL rules for an absolute address. A host with
// letters outside ASCII is refused rather than converted to its xn-- form.
func parseURL(input string) (parsedURL, bool) {
	var u parsedURL
	input = strings.TrimFunc(input, func(r rune) bool { return r <= 0x20 })
	input = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(input)

	colon := strings.IndexByte(input, ':')
	if colon < 1 || !isAlpha(input[0]) {
		return u, false
	}
	for i := 1; i < colon; i++ {
		c := input[i]
		if !isAlpha(c) && !isDigit(c) && c != '+' && c != '-' && c != '.' {
			return u, false
		}
	}
	u.scheme = strings.ToLower(input[:colon])
	if _, special := defaultPorts[u.scheme]; !special {
		// Not a web address this app accepts; the caller turns it away by scheme.
		return u, true
	}

	rest := strings.TrimLeft(input[colon+1:], `/\`)
	end := strings.IndexAny(rest, `/\?#`)
	if end < 0 {
		end = len(rest)
	}
	authority, rest := rest[:end], rest[end:]

	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		user, password, _ := strings.Cut(authority[:at], ":")
		u.credentials = user != "" || password != ""
		authority = authority[at+1:]
	}

	hostText, portText := authority, ""
	inBrackets := false
	for i := 0; i < len(authority); i++ {
		switch c := authority[i]; {
		case c == '[':
			inBrackets = true
		case c == ']':
			inBrackets = false
		case c == ':' && !inBrackets:
			hostText, portText = authority[:i], authority[i+1:]
			i = len(authority)
		}
	}
	host, ok := parseHost(hostText)
	if !ok {
		return u, false
	}
	u.host = host
	if portText != "" {
		for i := 0; i < len(portText); i++ {
			if !isDigit(portText[i]) {
				return u, false
			}
		}
		port, err := strconv.ParseUint(portText, 10, 64)
		if err != nil || port > 65535 {
			return u, false
		}
		if text := strconv.FormatUint(port, 10); text != defaultPorts[u.scheme] {
			u.port = text
		}
	}

	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest, u.fragment = rest[:i], rest[i+1:]
	}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, u.query = rest[:i], rest[i+1:]
	}
	u.path = cleanPath(rest)
	return u, true
}

func parseHost(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	if text[0] == '[' {
		if text[len(text)-1] != ']' {
			return "", false
		}
		return parseIPv6(text[1 : len(text)-1])
	}
	host := strings.ToLower(percentDecode(text))
	for i := 0; i < len(host); i++ {
		c := host[i]
		if c <= 0x20 || c >= 0x7F || strings.IndexByte(`#/:<>?@[\]^|%`, c) >= 0 {
			return "", false
		}
	}
	if endsInNumber(host) {
		return parseIPv4(host)
	}
	return host, true
}

func isHex(c byte) bool { return isDigit(c) || ((c|0x20) >= 'a' && (c|0x20) <= 'f') }

func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			out = append(out, byte(b))
			i += 2
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// ipv4Number reads one part of a dotted address: decimal, 0x hex or 0 octal.
func ipv4Number(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	base := 10
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s, base = s[2:], 16
	} else if len(s) >= 2 && s[0] == '0' {
		s, base = s[1:], 8
	}
	if s == "" {
		return 0, true
	}
	n, err := strconv.ParseUint(s, base, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return math.MaxUint64, true
		}
		return 0, false
	}
	return n, true
}

func endsInNumber(host string) bool {
	parts := strings.Split(host, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true
	}
	_, ok := ipv4Number(last)
	return ok
}

func parseIPv4(host string) (string, bool) {
	parts := strings.Split(host, ".")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return "", false
	}
	numbers := make([]uint64, len(parts))
	for i, part := range parts {
		n, ok := ipv4Number(part)
		if !ok {
			return "", false
		}
		numbers[i] = n
	}
	last := len(numbers) - 1
	for _, n := range numbers[:last] {
		if n > 255 {
			return "", false
		}
	}
	if numbers[last] >= uint64(1)<<(8*uint(4-last)) {
		return "", false
	}
	address := numbers[last]
	for i, n := range numbers[:last] {
		address += n << (8 * uint(3-i))
	}
	return fmt.Sprintf("%d.%d.%d.%d", address>>24, address>>16&255, address>>8&255, address&255), true
}

func parseIPv6(text string) (string, bool) {
	if strings.Contains(text, "%") {
		return "", false
	}
	address, err := netip.ParseAddr(text)
	if err != nil || !address.Is6() {
		return "", false
	}
	raw := address.As16()
	var pieces [8]uint16
	for i := range pieces {
		pieces[i] = uint16(raw[2*i])<<8 | uint16(raw[2*i+1])
	}
	// The first longest run of two or more zero pieces is written as ::.
	start, length := -1, 1
	for i := 0; i < 8; i++ {
		j := i
		for j < 8 && pieces[j] == 0 {
			j++
		}
		if j-i > length {
			start, length = i, j-i
		}
		if j > i {
			i = j
		}
	}
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < 8; i++ {
		if i == start {
			if i == 0 {
				b.WriteByte(':')
			}
			b.WriteByte(':')
			i += length - 1
			continue
		}
		b.WriteString(strconv.FormatUint(uint64(pieces[i]), 16))
		if i < 7 {
			b.WriteByte(':')
		}
	}
	b.WriteByte(']')
	return b.String(), true
}

// cleanPath resolves . and .. and escapes what a browser would escape.
func cleanPath(path string) string {
	if path == "" {
		return "/"
	}
	var parts []string
	start := 1
	for i := 1; i <= len(path); i++ {
		if i == len(path) || path[i] == '/' || path[i] == '\\' {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	var kept []string
	for i, part := range parts {
		last := i == len(parts)-1
		switch strings.ToLower(part) {
		case "..", ".%2e", "%2e.", "%2e%2e":
			if len(kept) > 0 {
				kept = kept[:len(kept)-1]
			}
			if last {
				kept = append(kept, "")
			}
		case ".", "%2e":
			if last {
				kept = append(kept, "")
			}
		default:
			kept = append(kept, escapePath(part))
		}
	}
	return "/" + strings.Join(kept, "/")
}

func escapePath(part string) string {
	var b strings.Builder
	for i := 0; i < len(part); i++ {
		c := part[i]
		if c <= 0x20 || c >= 0x7F || strings.IndexByte("\"#<>?^`{}", c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}
