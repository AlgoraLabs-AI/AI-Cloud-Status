package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCheckAsksForAPageAsADocument pins the request shape that gets a Page
// provider past Cloudflare's managed challenge at status.mistral.ai: the Fetch
// Metadata of a top-level navigation and an HTML Accept, under this app's own
// User-Agent. Every request without the Sec-Fetch headers was challenged
// (measured 2026-10-11); drop one of them here and the row goes grey again
// without any test noticing, which is why this one exists. The API providers
// keep the feed Accept and send no Fetch Metadata: a JSON endpoint is not a
// document.
func TestCheckAsksForAPageAsADocument(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write(readFixture(t, "rootly_operational.html"))
	}))
	defer srv.Close()

	ck := &Checker{Client: srv.Client()}
	page := Provider{ID: "p", Name: "P", Kind: KindRootly, URL: srv.URL, Page: true}
	if _, err := ck.Check(context.Background(), page); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"Sec-Fetch-Mode": "navigate",
		"Sec-Fetch-Dest": "document",
		"Sec-Fetch-Site": "none",
	} {
		if v := got.Get(k); v != want {
			t.Errorf("page request %s = %q, want %q", k, v, want)
		}
	}
	if a := got.Get("Accept"); !strings.HasPrefix(a, "text/html") {
		t.Errorf("page request Accept = %q, want an HTML document", a)
	}
	if ua := got.Get("User-Agent"); !strings.HasPrefix(ua, "AI-Cloud-Status/") {
		t.Errorf("page request User-Agent = %q, want our own", ua)
	}

	api := Provider{ID: "a", Name: "A", Kind: KindRootly, URL: srv.URL}
	if _, err := ck.Check(context.Background(), api); err != nil {
		t.Fatal(err)
	}
	if got.Get("Sec-Fetch-Dest") != "" || !strings.HasPrefix(got.Get("Accept"), "application/json") {
		t.Errorf("API request carries document headers: Accept=%q Sec-Fetch-Dest=%q",
			got.Get("Accept"), got.Get("Sec-Fetch-Dest"))
	}
}

// TestCheckTellsAChallengeFromAnAbsentFeed: a 403 that carries Cf-Mitigated:
// challenge on an Optional provider is ErrChallenged, which IS ErrNoFeed (the
// row and the audit treat it so) but can be told apart, so the poll retries it
// before filing it. A plain 403 stays ErrNoFeed, and a non-Optional provider
// keeps reading a challenge as an unreadable feed with its status code.
func TestCheckTellsAChallengeFromAnAbsentFeed(t *testing.T) {
	challenge := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cf-Mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<html><title>Just a moment...</title></html>"))
	}
	plain := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }

	challenged := httptest.NewServer(http.HandlerFunc(challenge))
	defer challenged.Close()
	forbidden := httptest.NewServer(http.HandlerFunc(plain))
	defer forbidden.Close()

	ck := &Checker{Client: challenged.Client()}
	optional := Provider{ID: "p", Name: "P", Kind: KindRootly, URL: challenged.URL, Optional: true, Page: true}
	_, err := ck.Check(context.Background(), optional)
	if !errors.Is(err, ErrChallenged) || !errors.Is(err, ErrNoFeed) {
		t.Errorf("challenged Optional provider: err = %v, want ErrChallenged, which is also ErrNoFeed", err)
	}

	optional.URL = forbidden.URL
	if _, err := ck.Check(context.Background(), optional); !errors.Is(err, ErrNoFeed) || errors.Is(err, ErrChallenged) {
		t.Errorf("plain 403 on an Optional provider: err = %v, want ErrNoFeed and not a challenge", err)
	}

	required := Provider{ID: "r", Name: "R", Kind: KindRootly, URL: challenged.URL, Page: true}
	res, err := ck.Check(context.Background(), required)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden || !se.Challenged || res.Feed != FeedUnreachable {
		t.Errorf("challenged required provider: res=%+v err=%v, want an unreachable feed with a 403 StatusError that says it was challenged", res, err)
	}

	// The challenge is read off its header, whatever status it rides on: a
	// 200-shaped challenge document is not a page to parse and fail on, and a
	// 503 one is not an outage of the provider.
	for _, code := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(code)
			_, _ = w.Write([]byte("<html><title>Just a moment...</title></html>"))
		}))
		optional.URL = srv.URL
		_, err := (&Checker{Client: srv.Client()}).Check(context.Background(), optional)
		srv.Close()
		if !errors.Is(err, ErrChallenged) {
			t.Errorf("challenge with status %d: err = %v, want ErrChallenged", code, err)
		}
	}
}
