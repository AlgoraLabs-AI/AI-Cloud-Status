package monitor

import "testing"

func TestValidHost(t *testing.T) {
	valid := []string{
		"1.1.1.1",
		"8.8.8.8",
		"2606:4700:4700::1111", // IPv6
		"example.com",
		"sub.example.co.uk",
		"a",
		"my-host123",
		"example.com.", // trailing dot FQDN
		"  1.1.1.1  ",  // surrounding whitespace trimmed
	}
	for _, h := range valid {
		if !ValidHost(h) {
			t.Errorf("ValidHost(%q) = false, want true", h)
		}
	}

	invalid := []string{
		"",
		"   ",
		"-bad.com",
		"bad-.com",
		"has space.com",
		"under_score.com",
		"exclaim!.com",
		"http://example.com", // scheme not allowed
	}
	for _, h := range invalid {
		if ValidHost(h) {
			t.Errorf("ValidHost(%q) = true, want false", h)
		}
	}
}

func TestCustomTarget(t *testing.T) {
	tg := CustomTarget("example.com")
	if tg.ID != "custom:example.com" {
		t.Errorf("ID = %q", tg.ID)
	}
	if tg.Host != "example.com" || tg.Name != "example.com" {
		t.Errorf("unexpected target %+v", tg)
	}
}

func TestNormalizeHost(t *testing.T) {
	if got := NormalizeHost("  host.example  "); got != "host.example" {
		t.Errorf("NormalizeHost = %q", got)
	}
}
