package pairview

import "strings"

// ValidOrigin checks the app's fixed address that the QR is built on and
// returns it in its plain form: "https://" and a lower-case host name. It
// accepts only https, a host name of letters, digits, hyphens and dots with
// at least one dot and a last label that is not all digits (an address, an
// IP literal or a name such as "localhost" is refused: a phone cannot be
// sent to one safely), no userinfo, no path (not even "/"), no query, no
// fragment, no spaces, and no port but 443, which is dropped. Nothing is
// trimmed.
func ValidOrigin(s string) (string, bool) {
	const scheme = "https://"
	if len(s) <= len(scheme) || !strings.EqualFold(s[:len(scheme)], scheme) || len(s) > 255 {
		return "", false
	}
	host := s[len(scheme):]
	if i := strings.IndexByte(host, ':'); i >= 0 {
		if host[i+1:] != "443" {
			return "", false
		}
		host = host[:i]
	}
	if !validHost(host) {
		return "", false
	}
	return scheme + strings.ToLower(host), true
}

func validHost(h string) bool {
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
			default:
				return false
			}
		}
	}
	// A browser reads a name whose last label is a number, decimal or
	// 0x-hex, as an address (1.2.3.4, 0x7f.1): refused.
	return !numericLabel(labels[len(labels)-1])
}

func numericLabel(l string) bool {
	if len(l) >= 2 && l[0] == '0' && (l[1] == 'x' || l[1] == 'X') {
		l = l[2:]
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(l); i++ {
		if l[i] < '0' || l[i] > '9' {
			return false
		}
	}
	return true
}
