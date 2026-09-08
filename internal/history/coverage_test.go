package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCoverageSurvivesPrune pins the property the whole mechanism rests on.
//
// Coverage answers "when did this feed last work", and the answer is only useful
// once the outage has outlived the display window — which is exactly when Prune
// has thrown the samples away. If Prune ever starts walking coverage too, this
// test is the one that must fail, because nothing else would: the audit would
// simply start reporting "never polled" for providers it had polled for months.
func TestCoverageSurvivesPrune(t *testing.T) {
	h := New(100)
	ancient := time.Now().Add(-90 * 24 * time.Hour)

	h.Add("p", Sample{Time: ancient, Up: true})
	h.RecordCoverage("p", CoverageObservation{At: ancient, OK: true})

	h.Prune(24*time.Hour, time.Now())

	if got := h.Series("p"); len(got) != 0 {
		t.Fatalf("precondition: Prune should have dropped the ancient sample, got %d", len(got))
	}
	cov, ok := h.Coverage("p")
	if !ok {
		t.Fatal("Coverage was pruned away; it must outlive the display window")
	}
	if !cov.LastSuccess.Equal(ancient) {
		t.Errorf("LastSuccess = %v, want %v", cov.LastSuccess, ancient)
	}
}

// TestCoverageRoundTripsThroughLoadThenSave is the regression test for the
// defect that would have made this feature silently useless.
//
// Save rebuilds the on-disk snapshot from the in-memory History on every call.
// A coverage field that existed only in the serialized struct would be read into
// the snapshot at Load, dropped on the floor, and then erased by the very next
// Save — with every existing test still green, because they only assert on
// Series. The app does exactly this sequence: it Loads at startup and Saves on a
// 60s ticker, so the erasure would happen within a minute of every launch.
func TestCoverageRoundTripsThroughLoadThenSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	at := time.Now().Add(-time.Hour).Truncate(time.Second)

	h := New(100)
	h.RecordCoverage("p", CoverageObservation{At: at, OK: true})
	h.RecordCoverage("broken", CoverageObservation{At: at, FailureClass: OutcomeHTTP})
	if err := h.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := Load(path, 100)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cov, ok := reloaded.Coverage("p"); !ok || !cov.LastSuccess.Equal(at) {
		t.Fatalf("after Load: coverage = %+v ok=%v, want LastSuccess=%v", cov, ok, at)
	}

	// The load-bearing half: save again WITHOUT touching coverage, exactly as the
	// app's periodic save does, then read the file back.
	if err := reloaded.Save(path); err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	final, err := Load(path, 100)
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	cov, ok := final.Coverage("p")
	if !ok {
		t.Fatal("coverage was erased by a Save that never touched it")
	}
	if !cov.LastSuccess.Equal(at) {
		t.Errorf("LastSuccess = %v, want %v", cov.LastSuccess, at)
	}
	broken, ok := final.Coverage("broken")
	if !ok || !broken.LastSuccess.IsZero() || broken.LastFailureClass != OutcomeHTTP {
		t.Errorf("broken coverage = %+v ok=%v, want zero LastSuccess and class %q", broken, ok, OutcomeHTTP)
	}
}

// TestLoadLegacyFileWithoutCoverage: a file written by the released build has no
// coverage key at all. It must load clean and simply report no coverage, never
// an error and never a fabricated entry.
func TestLoadLegacyFileWithoutCoverage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	legacy := `{"version":1,"cap":100,"series":{"p":[{"t":"2026-09-01T00:00:00Z","up":true,"ms":0}]}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	h, err := Load(path, 100)
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if len(h.Series("p")) != 1 {
		t.Errorf("legacy samples lost: %d", len(h.Series("p")))
	}
	if _, ok := h.Coverage("p"); ok {
		t.Error("Coverage reported for a legacy file that never recorded any")
	}
	// And a sample with no Outcome keeps the older, coarser meaning rather than
	// being read as some specific failure class.
	if got := h.Series("p")[0].Outcome; got != "" {
		t.Errorf("legacy Sample.Outcome = %q, want empty", got)
	}
}

// TestSaveOmitsEmptyCoverage keeps a build that has never recorded coverage
// writing a file the previous release reads unchanged.
func TestSaveOmitsEmptyCoverage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	h := New(100)
	h.Add("p", Sample{Time: time.Now(), Up: true})
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["coverage"]; present {
		t.Error("empty coverage was serialized; it must be omitted so an older build sees an unchanged file")
	}
}

// TestRecordCoverageKeepsLastSuccessAcrossFailures: a failing poll must not
// erase the memory of when the feed last worked. That memory IS the answer the
// audit needs during an outage.
func TestRecordCoverageKeepsLastSuccessAcrossFailures(t *testing.T) {
	h := New(10)
	good := time.Now().Add(-2 * time.Hour)
	bad := time.Now().Add(-time.Minute)

	h.RecordCoverage("p", CoverageObservation{At: good, OK: true})
	h.RecordCoverage("p", CoverageObservation{At: bad, FailureClass: OutcomeTransport})

	cov, ok := h.Coverage("p")
	if !ok {
		t.Fatal("no coverage recorded")
	}
	if !cov.LastSuccess.Equal(good) {
		t.Errorf("LastSuccess = %v, want the earlier success %v", cov.LastSuccess, good)
	}
	if !cov.LastAttempt.Equal(bad) {
		t.Errorf("LastAttempt = %v, want the later attempt %v", cov.LastAttempt, bad)
	}
	if cov.LastFailureClass != OutcomeTransport {
		t.Errorf("LastFailureClass = %q, want %q", cov.LastFailureClass, OutcomeTransport)
	}

	// A later success must clear the stale failure class, or the audit would keep
	// naming a failure that has since resolved.
	recovered := time.Now()
	h.RecordCoverage("p", CoverageObservation{At: recovered, OK: true})
	cov, _ = h.Coverage("p")
	if cov.LastFailureClass != "" {
		t.Errorf("LastFailureClass = %q after recovery, want empty", cov.LastFailureClass)
	}
	if cov.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d after recovery, want 0", cov.ConsecutiveFailures)
	}
}

// TestBlindAttemptIsNotAFeedFailure is the regression guard for blaming every
// provider for the user's own dead router.
//
// When the machine has no connectivity every provider poll fails identically.
// Counting those as per-feed failures would drive all fifteen to a "feed not
// readable" verdict — and that verdict now reaches the exit code. The custom-URL
// path already refuses to feed its tracker under the same condition; this is the
// provider path holding the same line.
func TestBlindAttemptIsNotAFeedFailure(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)

	// A real failure, then the link drops and ten polls fail blind.
	h.RecordCoverage("p", CoverageObservation{At: t0, FailureClass: OutcomeHTTP})
	for i := range 10 {
		h.RecordCoverage("p", CoverageObservation{
			At: t0.Add(time.Duration(i+1) * time.Minute), Blind: true, FailureClass: OutcomeTransport,
		})
	}

	cov, ok := h.Coverage("p")
	if !ok {
		t.Fatal("no coverage recorded")
	}
	if cov.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1 — blind attempts are not evidence about this feed", cov.ConsecutiveFailures)
	}
	if cov.LastFailureClass != OutcomeHTTP {
		t.Errorf("LastFailureClass = %q, want %q — a blind attempt must not overwrite the real explanation",
			cov.LastFailureClass, OutcomeHTTP)
	}
	// It still records that somebody looked.
	if !cov.LastAttempt.After(t0) {
		t.Error("LastAttempt did not advance; a blind poll is still an attempt")
	}
}

// TestConsecutiveFailuresIsWhatMakesAVerdictPossible pins the distinction that
// a verdict resting on LastSuccess alone cannot make: a feed that worked for
// months and then broke has a non-zero LastSuccess forever, so "has ever
// succeeded" can never again indicate a fault.
func TestConsecutiveFailuresIsWhatMakesAVerdictPossible(t *testing.T) {
	h := New(10)
	worked := time.Now().Add(-72 * time.Hour)
	h.RecordCoverage("p", CoverageObservation{At: worked, OK: true})

	for i := range 30 {
		h.RecordCoverage("p", CoverageObservation{
			At: worked.Add(time.Duration(i+1) * time.Minute), FailureClass: OutcomeHTTP,
		})
	}

	cov, _ := h.Coverage("p")
	if cov.LastSuccess.IsZero() {
		t.Fatal("precondition: this provider HAS succeeded before, which is the whole point")
	}
	if cov.ConsecutiveFailures != 30 {
		t.Errorf("ConsecutiveFailures = %d, want 30", cov.ConsecutiveFailures)
	}
}

// TestUptimeExcludesUnknown pins the contract Sample's own doc states: unknown
// samples are neither up nor down and belong in neither half of the ratio.
//
// Counting them in the denominator reported a provider whose feed was unreadable
// all window as though it had been DOWN all window — inverting the one signal a
// reader wants from this number.
func TestUptimeExcludesUnknown(t *testing.T) {
	h := New(100)
	now := time.Now()
	h.Add("p", Sample{Time: now, Up: true})
	h.Add("p", Sample{Time: now, Up: false, Unknown: true})
	h.Add("p", Sample{Time: now, Up: false, Unknown: true})

	got, ok := h.Uptime("p")
	if !ok {
		t.Fatal("Uptime reported no data despite one known sample")
	}
	if got != 1.0 {
		t.Errorf("Uptime = %v, want 1.0 — one known sample, up; the two unknowns are neither up nor down", got)
	}

	// All-unknown is not 0% uptime, it is "no percentage is honest".
	h2 := New(100)
	h2.Add("q", Sample{Time: now, Up: false, Unknown: true})
	if _, ok := h2.Uptime("q"); ok {
		t.Error("Uptime returned a percentage for an all-unknown series; nothing was actually observed")
	}
}
