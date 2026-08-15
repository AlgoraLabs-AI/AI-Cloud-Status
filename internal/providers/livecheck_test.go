package providers

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveFeedsParse hits the real endpoints. Opt-in via ACS_LIVE=1 so the suite
// stays hermetic; it exists because the bug it was written for — a guard that
// rejected Azure's healthy shape — was invisible to every offline corpus.
func TestLiveFeedsParse(t *testing.T) {
	if os.Getenv("ACS_LIVE") == "" {
		t.Skip("set ACS_LIVE=1 to hit the real status feeds")
	}
	for _, p := range Default() {
		if p.Optional {
			continue
		}
		c := &http.Client{Timeout: 20 * time.Second}
		resp, err := c.Get(p.URL)
		if err != nil {
			t.Logf("%-12s FETCH ERROR %v", p.ID, err)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		res, perr := ParseFeed(p, b)
		if perr != nil {
			t.Errorf("%-12s http=%d bytes=%d PARSE ERROR: %v", p.ID, resp.StatusCode, len(b), perr)
			continue
		}
		t.Logf("%-12s http=%d bytes=%-7d severity=%-8v incidents=%d state=%v",
			p.ID, resp.StatusCode, len(b), res.Severity, len(res.Incidents), res.ServiceState())
	}
}
