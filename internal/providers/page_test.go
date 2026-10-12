package providers

import (
	"context"
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
