package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Replaying the REAL captures is the only check that speaks for the payloads
// providers actually serve. Every other test in this package is either a
// hand-written payload (which encodes what I ASSUME the feed looks like) or one
// of a handful of committed fixtures. FeedCapture archives the live body
// whenever a feed reads non-operational, and stamps the severity it derived into
// the filename — so the archive is a labelled corpus of exactly the inputs that
// matter, recorded by the running app.
//
// This matters most for guards that REJECT input. The empty-feed and
// missing-anchor errors added to the parsers are the kind of change that looks
// right against a synthetic payload and silently breaks a real one: reject a
// shape some provider genuinely serves and the row goes permanently grey.
//
// KNOWN BLIND SPOT, and it has already cost once. FeedCapture archives a payload
// only when the feed reads NON-OPERATIONAL, so a HEALTHY payload can never
// appear in this corpus — replaying all 448 captures says nothing about whether
// the parsers still accept an all-clear feed. A guard rejecting Azure's healthy
// shape (an empty channel, which is exactly what all-clear looks like there)
// passed this entire sweep and broke every healthy poll in production.
//
// The healthy path is therefore covered separately and deliberately, by
// committed fixtures: azure_healthy_empty.xml is the live all-clear body
// captured by hand. When adding a parser guard, test it against BOTH corpora —
// this one proves you did not break a broken feed, and it is the smaller half of
// the job.
//
// Point ACS_FEED_SAMPLES at a feed-samples directory to run it. Skipped when
// unset, so CI and a fresh clone stay green without the archive.
func replayDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("ACS_FEED_SAMPLES")
	if dir == "" {
		t.Skip("set ACS_FEED_SAMPLES to a feed-samples directory to replay real captures")
	}
	return dir
}

// providerByID returns the registered provider with the given ID.
func providerByID(id string) (Provider, bool) {
	for _, p := range Default() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// captureSeverity reads the severity FeedCapture stamped into a filename, e.g.
// "20260723-152634-major.json" -> SevMajor.
func captureSeverity(name string) (Severity, bool) {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	i := strings.LastIndex(base, "-")
	if i < 0 {
		return SevNone, false
	}
	switch base[i+1:] {
	case "none":
		return SevNone, true
	case "minor":
		return SevMinor, true
	case "major":
		return SevMajor, true
	case "critical":
		return SevCritical, true
	}
	return SevNone, false
}

// TestCommittedCapturesStillParse is the CI-visible half of the replay. The full
// archive is ~38 MB and lives outside the repo, so one real capture per provider
// is committed under testdata/captures — chosen smallest-first and preferring a
// capture the app recorded as major, i.e. a moment something was genuinely
// broken. Names carry the provider ID and the original timestamp+severity.
//
// Unlike the two tests above, this one never skips. It is what stops a future
// parser guard from rejecting a real payload on a machine that has no archive.
func TestCommittedCapturesStillParse(t *testing.T) {
	files, err := os.ReadDir(filepath.Join("testdata", "captures"))
	if err != nil {
		t.Fatalf("read testdata/captures: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no committed captures — this test proves nothing")
	}

	for _, f := range files {
		id, _, ok := strings.Cut(f.Name(), "-")
		if !ok {
			t.Errorf("capture %q does not start with a provider ID", f.Name())
			continue
		}
		p, ok := providerByID(id)
		if !ok {
			t.Errorf("capture %q names an unregistered provider %q", f.Name(), id)
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata", "captures", f.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", f.Name(), err)
		}
		res, err := ParseFeed(p, data)
		if err != nil {
			t.Errorf("%s: real captured payload no longer parses: %v", f.Name(), err)
			continue
		}
		want, ok := captureSeverity(f.Name())
		if !ok {
			continue
		}
		// The app recorded this moment as non-operational. The parser must still
		// see SOMETHING wrong — either a severity or at least one incident.
		// Not an equality check: FeedCapture's filename label reads the
		// page-level indicator while the parser derives severity per incident,
		// and provider.go:355 documents that the two legitimately disagree.
		if want > SevNone && res.Severity == SevNone && len(res.Incidents) == 0 {
			t.Errorf("%s: capture recorded %v, parser now reports fully operational", f.Name(), want)
		}
	}
}

// TestReplayRealCapturesNeverError is the guard that matters for the rejection
// rules: not one archived payload — every one of which the live app fetched,
// parsed and acted on — may now fail to parse. A capture that errors here is a
// row that would read "status page unavailable" forever in production.
func TestReplayRealCapturesNeverError(t *testing.T) {
	dir := replayDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	total, failed := 0, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, ok := providerByID(e.Name())
		if !ok {
			t.Logf("no registered provider for capture dir %q — skipping", e.Name())
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, f := range files {
			if f.IsDir() || strings.Contains(f.Name(), "parse-error") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name(), f.Name()))
			if err != nil {
				t.Fatalf("read capture: %v", err)
			}
			total++
			if _, err := ParseFeed(p, data); err != nil {
				failed++
				if failed <= 10 { // don't drown the log if a whole adapter breaks
					t.Errorf("%s/%s: real captured payload no longer parses: %v", e.Name(), f.Name(), err)
				}
			}
		}
	}
	if total == 0 {
		t.Fatalf("no captures found under %s — the replay proved nothing", dir)
	}
	t.Logf("replayed %d real captures, %d failed to parse", total, failed)
}

// TestReplayRealCapturesNeverSilentlyOperational is the false-negative sweep
// over production data. FeedCapture only writes a capture when the feed read
// NON-operational, so by construction every file here is a moment the app
// believed something was wrong. A payload that now parses to a clean SevNone
// with no incidents is a regression that would have painted that moment green.
func TestReplayRealCapturesNeverSilentlyOperational(t *testing.T) {
	dir := replayDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	type regression struct{ file, detail string }
	var lost []regression
	total := 0

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, ok := providerByID(e.Name())
		if !ok {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(dir, e.Name()))
		for _, f := range files {
			if f.IsDir() || strings.Contains(f.Name(), "parse-error") {
				continue
			}
			want, ok := captureSeverity(f.Name())
			if !ok || want == SevNone {
				continue // the capture itself recorded "operational"
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name(), f.Name()))
			if err != nil {
				continue
			}
			total++
			res, err := ParseFeed(p, data)
			if err != nil {
				continue // covered by the test above
			}
			if res.Severity == SevNone && len(res.Incidents) == 0 {
				lost = append(lost, regression{
					file:   e.Name() + "/" + f.Name(),
					detail: "capture recorded " + want.String() + ", parser now says operational with no incidents",
				})
			}
		}
	}

	if total == 0 {
		t.Fatalf("no non-operational captures found under %s", dir)
	}
	for i, r := range lost {
		if i >= 15 {
			t.Errorf("... and %d more", len(lost)-15)
			break
		}
		t.Errorf("%s: %s", r.file, r.detail)
	}
	t.Logf("replayed %d non-operational captures, %d now read as fully operational", total, len(lost))
}
