package urlcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
)

// TestProbeReachabilityAndStringCheck verifies the reworked URL-check semantics
// end-to-end against a real (local) HTTP server: reachability (2xx/3xx) is the
// baseline, and the optional string check is an INDEPENDENT extra applied on top.
func TestProbeReachabilityAndStringCheck(t *testing.T) {
	// A server whose body contains "operational".
	okBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("<html>All systems operational</html>"))
	}))
	defer okBody.Close()

	// A reachable server whose body does NOT contain "operational".
	okNoBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("<html>maintenance in progress</html>"))
	}))
	defer okNoBody.Close()

	// A server that errors (not reachable in the 2xx sense).
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer down.Close()

	// A redirect that resolves to 200 (reachability must accept a resolved redirect).
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, okBody.URL, http.StatusFound)
	}))
	defer redirect.Close()

	cases := []struct {
		name   string
		check  config.URLCheck
		wantUp bool
	}{
		// --- Reachability only (mandatory baseline) ---
		{"reachable 200 → up", config.URLCheck{URL: okBody.URL, Mode: config.URLModeReachable}, true},
		{"reachable 503 → down", config.URLCheck{URL: down.URL, Mode: config.URLModeReachable}, false},
		{"reachable resolves redirect → up", config.URLCheck{URL: redirect.URL, Mode: config.URLModeReachable}, true},

		// --- Reachability + independent string check ---
		{"contains: reachable AND text present → up",
			config.URLCheck{URL: okBody.URL, Mode: config.URLModeContains, Expect: "operational"}, true},
		{"contains: reachable but text MISSING → down",
			config.URLCheck{URL: okNoBody.URL, Mode: config.URLModeContains, Expect: "operational"}, false},
		{"contains: NOT reachable → down (string never decides it up)",
			config.URLCheck{URL: down.URL, Mode: config.URLModeContains, Expect: "operational"}, false},
		{"contains is case-insensitive",
			config.URLCheck{URL: okBody.URL, Mode: config.URLModeContains, Expect: "OPERATIONAL"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Probe(context.Background(), tc.check)
			if out.Up != tc.wantUp {
				t.Fatalf("Up = %v, want %v (detail=%q err=%v)", out.Up, tc.wantUp, out.Detail, out.Err)
			}
		})
	}
}
