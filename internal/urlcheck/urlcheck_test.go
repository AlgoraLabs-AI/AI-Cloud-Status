package urlcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
)

func TestProbeReachable(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	if g := Probe(context.Background(), config.URLCheck{URL: ok.URL, Mode: config.URLModeReachable}); !g.Up || g.Code != 200 {
		t.Errorf("200 should be reachable: up=%v code=%d", g.Up, g.Code)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if g := Probe(context.Background(), config.URLCheck{URL: bad.URL, Mode: config.URLModeReachable}); g.Up {
		t.Errorf("500 should be down")
	}
}

func TestProbeReachableFollowsRedirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusMovedPermanently) // 301
	}))
	defer redir.Close()

	// "200 or redirect": a 301→200 chain is up, and the reported code is the final 200.
	if g := Probe(context.Background(), config.URLCheck{URL: redir.URL, Mode: config.URLModeReachable}); !g.Up || g.Code != 200 {
		t.Errorf("301→200 should be up via the final 200: up=%v code=%d", g.Up, g.Code)
	}
}

func TestProbeContainsRequiresSuccessStatus(t *testing.T) {
	body := func(status int, text string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(text))
		}))
	}

	ok := body(http.StatusOK, "<html>All Systems Operational</html>")
	defer ok.Close()
	if g := Probe(context.Background(), config.URLCheck{URL: ok.URL, Mode: config.URLModeContains, Expect: "operational"}); !g.Up {
		t.Errorf("2xx body containing the string should be up: %q", g.Detail)
	}
	if g := Probe(context.Background(), config.URLCheck{URL: ok.URL, Mode: config.URLModeContains, Expect: "maintenance"}); g.Up {
		t.Errorf("2xx body missing the string should be down")
	}

	// An error page that HAPPENS to contain the string must NOT pass (non-2xx).
	errPage := body(http.StatusInternalServerError, "Operational dashboard error")
	defer errPage.Close()
	if g := Probe(context.Background(), config.URLCheck{URL: errPage.URL, Mode: config.URLModeContains, Expect: "operational"}); g.Up {
		t.Errorf("500 error page should be down even if it contains the string")
	}

	if g := Probe(context.Background(), config.URLCheck{URL: ok.URL, Mode: config.URLModeContains, Expect: ""}); g.Up {
		t.Errorf("empty expect string should never be up")
	}
}

func TestProbeBadHostIsDownNotPanic(t *testing.T) {
	g := Probe(context.Background(), config.URLCheck{URL: "http://127.0.0.1:0", Mode: config.URLModeReachable})
	if g.Up || g.Err == nil {
		t.Errorf("unreachable host should be down with an error, got up=%v err=%v", g.Up, g.Err)
	}
}

func TestProbeContextCancelIsDownNotHang(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan Outcome, 1)
	go func() { done <- Probe(ctx, config.URLCheck{URL: srv.URL, Mode: config.URLModeReachable}) }()
	select {
	case g := <-done:
		if g.Up || g.Err == nil {
			t.Errorf("cancelled probe should be down with an error, got up=%v err=%v", g.Up, g.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Probe did not return after context cancellation (hung)")
	}
}
