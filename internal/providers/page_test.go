package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckAsksForAPageAsADocument pins the request shape that gets a Page
// provider past Cloudflare's managed challenge at status.mistral.ai: the Fetch
// Metadata of a top-level navigation and an HTML Accept, under this app's own
// User-Agent. Every request without the Sec-Fetch headers was challenged
// (measured); drop one of them here and the row goes grey again
// without any test noticing, which is why this one exists. The API providers
// keep the feed Accept and send no Fetch Metadata: a JSON endpoint is not a
// document.
func TestCheckAsksForAPageAsADocument(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if r.URL.Path == "/api" {
			_, _ = w.Write(readFixture(t, "statuspage_none.json"))
			return
		}
		_, _ = w.Write(readFixture(t, "rootly_operational.html"))
	}))
	defer srv.Close()

	ck := &Checker{Client: srv.Client()}
	page := Provider{ID: "p", Name: "P", Kind: KindRootly, URL: srv.URL + "/page"}
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

	api := Provider{ID: "a", Name: "A", Kind: KindStatuspage, URL: srv.URL + "/api"}
	if _, err := ck.Check(context.Background(), api); err != nil {
		t.Fatal(err)
	}
	if got.Get("Sec-Fetch-Dest") != "" || !strings.HasPrefix(got.Get("Accept"), "application/json") {
		t.Errorf("API request carries document headers: Accept=%q Sec-Fetch-Dest=%q",
			got.Get("Accept"), got.Get("Sec-Fetch-Dest"))
	}
}

// TestFeedCaptureNamesAPageAsHTML: the archive is self-describing. A page
// Kind's capture is the page's HTML and is named .html.gz; a feed keeps the
// .json.gz name every reader of the archive already knows.
func TestFeedCaptureNamesAPageAsHTML(t *testing.T) {
	dir := t.TempDir()
	fc := NewFeedCapture(dir)
	res := Result{Severity: SevMajor, Incidents: []Incident{{Summary: "x", Severity: SevMajor}}}
	fc.Capture(Provider{ID: "page", Kind: KindRootly}, readFixture(t, "rootly_degraded.html"), res, nil)
	fc.Capture(Provider{ID: "feed", Kind: KindStatuspage}, readFixture(t, "statuspage_major.json"), res, nil)
	for id, want := range map[string]string{"page": "-major.html.gz", "feed": "-major.json.gz"} {
		files, err := os.ReadDir(filepath.Join(dir, id))
		var names []string
		for _, f := range files {
			names = append(names, f.Name())
		}
		if err != nil || len(names) != 1 || !strings.HasSuffix(names[0], want) {
			t.Errorf("%s capture named %v (%v), want one file ending in %s", id, names, err, want)
		}
	}
}

// TestFeedCaptureDedupsAPageWhoseTokensRotate: a Rails page mints a fresh CSRF
// token and fresh CSP nonces per response, so two polls of the SAME open
// incident differ byte for byte. Hashed raw, every poll would archive the
// whole page again and an incident would evict its own onset from the budget;
// for a page Kind the dedup key is what the parser read (the Result), so the
// same state is one capture whatever the markup minted, while a feed's bytes
// stay its own key.
func TestFeedCaptureDedupsAPageWhoseTokensRotate(t *testing.T) {
	dir := t.TempDir()
	fc := NewFeedCapture(dir)
	// The full live page: it carries the csrf-token and csp-nonce metas and
	// the per-script nonce attributes the trimmed fixtures dropped.
	whole := string(readFixture(t, "rootly_live_page.html.gz"))
	rotate := func(tag string) string {
		s := strings.Replace(whole, `csp-nonce" content="`, `csp-nonce" content="`+tag, 1)
		s = strings.Replace(s, `csrf-token" content="`, `csrf-token" content="`+tag, 1)
		return strings.ReplaceAll(s, ` nonce="`, ` nonce="`+tag)
	}
	first, second := rotate("AAAA"), rotate("BBBB")
	if !strings.Contains(whole, ` nonce="`) || !strings.Contains(whole, `csrf-token" content="`) || first == second {
		t.Fatal("capture lacks the rotating parts this test is about")
	}
	res := Result{Severity: SevCritical, Incidents: []Incident{{Summary: "x", Severity: SevCritical}}}

	page := Provider{ID: "page", Kind: KindRootly}
	fc.Capture(page, []byte(first), res, nil)
	fc.Capture(page, []byte(second), res, nil)
	files, _ := os.ReadDir(filepath.Join(dir, "page"))
	if len(files) != 1 {
		t.Errorf("page captured %d times, want 1: the rotating token and nonces are not a new state", len(files))
	}

	// A page the parser could not read is keyed on WHY, so a markup change
	// is one capture per failure shape, not one per poll of rotating tokens.
	broken := Provider{ID: "broken", Kind: KindRootly}
	fc.Capture(broken, []byte(first), Result{Feed: FeedUnreachable}, errFake)
	fc.Capture(broken, []byte(second), Result{Feed: FeedUnreachable}, errFake)
	files, _ = os.ReadDir(filepath.Join(dir, "broken"))
	if len(files) != 1 {
		t.Errorf("unreadable page captured %d times, want 1: the same failure is not a new one", len(files))
	}

	// The control: for a feed the bytes ARE the key, so the same two bodies
	// are two captures. Two states, because the capture name has one-second
	// resolution and a same-second same-state pair would share a file.
	feed := Provider{ID: "feed"}
	fc.Capture(feed, []byte(first), res, nil)
	fc.Capture(feed, []byte(second), Result{Severity: SevMajor, Incidents: res.Incidents}, nil)
	files, _ = os.ReadDir(filepath.Join(dir, "feed"))
	if len(files) != 2 {
		t.Errorf("feed captured %d times, want 2: a feed's bytes are its own key", len(files))
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
	optional := Provider{ID: "p", Name: "P", Kind: KindRootly, URL: challenged.URL, Optional: true}
	_, err := ck.Check(context.Background(), optional)
	if !errors.Is(err, ErrChallenged) || !errors.Is(err, ErrNoFeed) {
		t.Errorf("challenged Optional provider: err = %v, want ErrChallenged, which is also ErrNoFeed", err)
	}

	optional.URL = forbidden.URL
	if _, err := ck.Check(context.Background(), optional); !errors.Is(err, ErrNoFeed) || errors.Is(err, ErrChallenged) {
		t.Errorf("plain 403 on an Optional provider: err = %v, want ErrNoFeed and not a challenge", err)
	}

	required := Provider{ID: "r", Name: "R", Kind: KindRootly, URL: challenged.URL}
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
