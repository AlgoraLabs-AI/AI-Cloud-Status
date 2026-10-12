package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// TestAChallengedPollIsRetriedBeforeItIsFiledAsNoFeed pins the poll's answer
// to a Cloudflare managed challenge whose verdict changes from one connection
// to the next (measured on status.mistral.ai): a challenge is
// retried like an unreadable feed, so a page that answers on the second
// connection is READ, and only a challenge that outlasts every attempt is
// filed as the skip an Optional provider without a feed gets. A plain 403 is
// still not retried: that one is stable, and retrying it would only cost time.
func TestAChallengedPollIsRetriedBeforeItIsFiledAsNoFeed(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "providers", "testdata", "rootly_operational.html"))
	if err != nil {
		t.Fatal(err)
	}
	challenge := func(w http.ResponseWriter) {
		w.Header().Set("Cf-Mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<html><title>Just a moment...</title></html>"))
	}

	cases := []struct {
		name         string
		challenges   int // connections challenged before the page answers; a large number never answers
		wantOutcome  string
		wantAttempts int
	}{
		{"answers on the second connection", 1, history.OutcomeReadable, 2},
		{"answers on the last allowed attempt", providerRetryAttempts - 1, history.OutcomeReadable, providerRetryAttempts},
		{"never answers: no public feed", 1 << 20, history.OutcomeSkipped, providerRetryAttempts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if int(hits.Add(1)) <= tc.challenges {
					challenge(w)
					return
				}
				_, _ = w.Write(page)
			}))
			defer srv.Close()

			c := &Controller{
				checker: &providers.Checker{Client: srv.Client()},
				backoff: monitor.Backoff{}, // no waiting between attempts
			}
			p := providers.Provider{ID: "p", Name: "P", Kind: providers.KindRootly, URL: srv.URL, Optional: true}
			st := c.checkProviderWithRetry(context.Background(), p)
			if st.outcome != tc.wantOutcome || st.attempts != tc.wantAttempts {
				t.Errorf("outcome=%v attempts=%d, want %v after %d attempts (err=%v)",
					st.outcome, st.attempts, tc.wantOutcome, tc.wantAttempts, st.err)
			}
		})
	}

	t.Run("a plain 403 is not retried", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		c := &Controller{checker: &providers.Checker{Client: srv.Client()}, backoff: monitor.Backoff{}}
		p := providers.Provider{ID: "p", Name: "P", Kind: providers.KindRootly, URL: srv.URL, Optional: true}
		st := c.checkProviderWithRetry(context.Background(), p)
		if st.outcome != history.OutcomeSkipped || hits.Load() != 1 {
			t.Errorf("outcome=%v after %d requests, want skipped after exactly 1", st.outcome, hits.Load())
		}
	})
}
