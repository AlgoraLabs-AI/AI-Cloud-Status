package providers

import (
	"strings"
	"testing"
)

// TestChallengedStatusErrorLeadsWithTheChallenge pins the drill-down line: the
// challenge first, the code after it, so "unexpected status 200" never appears
// for an answer whose status was not the problem.
func TestChallengedStatusErrorLeadsWithTheChallenge(t *testing.T) {
	msg := (&StatusError{Provider: "Mistral", Code: 200, Challenged: true}).Error()
	if !strings.Contains(msg, "Cloudflare challenge") || !strings.Contains(msg, "200") || strings.Contains(msg, "unexpected status") {
		t.Errorf("challenged StatusError reads %q, want the challenge named, the code kept, no 'unexpected status'", msg)
	}
	plain := (&StatusError{Provider: "Mistral", Code: 503}).Error()
	if !strings.Contains(plain, "unexpected status 503") || strings.Contains(plain, "challenge") {
		t.Errorf("plain StatusError reads %q, want the status and no challenge", plain)
	}
}
