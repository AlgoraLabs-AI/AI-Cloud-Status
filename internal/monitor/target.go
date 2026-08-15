package monitor

import (
	"net"
	"strings"
)

// CustomTargetID returns the stable check ID used for a user-added host.
func CustomTargetID(host string) string { return "custom:" + host }

// CustomTarget builds a Target for a user-added host.
func CustomTarget(host string) Target {
	return Target{ID: CustomTargetID(host), Name: host, Host: host}
}

// NormalizeHost trims surrounding whitespace from a user-entered host.
func NormalizeHost(host string) string {
	return strings.TrimSpace(host)
}

// ValidHost reports whether host is a usable connectivity target: either a
// valid IP address or a syntactically valid hostname (RFC 1123).
func ValidHost(host string) bool {
	host = NormalizeHost(host)
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return validHostname(host)
}

// validHostname applies RFC 1123-style hostname rules.
func validHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	// A single trailing dot (FQDN) is allowed; strip it before checking labels.
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if !validLabel(label) {
			return false
		}
	}
	return true
}

func validLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 {
		return false
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}
