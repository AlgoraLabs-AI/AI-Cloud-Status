package providers

import (
	"strings"
	"testing"
)

// TestPlainNoteStripsMarkup pins the conversion itself: block tags become line
// breaks, other tags vanish, entities decode, and already-plain text is left
// alone (so applying it to every adapter is safe).
func TestPlainNoteStripsMarkup(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"github br", "surfaces.<br /><br />We will continue.", "surfaces.\nWe will continue."},
		{"bare br", "one<br>two", "one\ntwo"},
		{"paragraphs", "<p>one</p><p>two</p>", "one\ntwo"},
		{"inline tags dropped", `see <a href="x">the page</a> now`, "see the page now"},
		{"entities decoded", "rate &amp; error &gt; 5%", "rate & error > 5%"},
		{"plain text untouched", "We are investigating elevated error rates.", "We are investigating elevated error rates."},
		{"whitespace collapsed", "  a   b \n\n  c  ", "a b\nc"},
		{"empty", "   ", ""},
	} {
		if got := plainNote(tc.in); got != tc.want {
			t.Errorf("%s: plainNote(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestStatuspageNoteIsPlainText is the regression for the reported bug: GitHub's
// incident_updates bodies are HTML, and the literal "<br />" was rendered in the
// table row and the detail dialog because only the RSS adapters stripped markup.
func TestStatuspageNoteIsPlainText(t *testing.T) {
	feed := `{"status":{"indicator":"minor","description":"Minor"},
	  "incidents":[{"name":"Incident with Copilot AI Model Providers","status":"monitoring","impact":"minor",
	    "created_at":"2026-08-01T15:03:00Z","updated_at":"2026-08-01T15:23:00Z",
	    "incident_updates":[{"created_at":"2026-08-01T15:23:00Z",
	      "body":"The issues have been resolved and Fable 5 is once again available in Copilot products and IDE surfaces.<br /><br />We will continue monitoring to ensure stability, but mitigation is complete."}]}]}`

	res, err := parseStatuspage([]byte(feed))
	if err != nil {
		t.Fatalf("parseStatuspage: %v", err)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("got %d incidents, want 1", len(res.Incidents))
	}
	note := res.Incidents[0].Note
	if strings.Contains(note, "<") || strings.Contains(note, "&") {
		t.Errorf("note still carries markup: %q", note)
	}
	if !strings.Contains(note, "IDE surfaces.\nWe will continue monitoring") {
		t.Errorf("note = %q, want the <br /> pair collapsed into one line break", note)
	}
}
