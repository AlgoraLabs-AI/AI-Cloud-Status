package history

import (
	"encoding/json"
	"errors"
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
	// It still records that somebody looked...
	if !cov.LastAttempt.After(t0) {
		t.Error("LastAttempt did not advance; a blind poll is still an attempt")
	}
	// ...but it must NOT advance the last thing we LEARNED. Staleness is measured
	// from that, so if blind attempts moved it, a machine offline for months
	// would keep a months-old verdict looking current forever.
	if !cov.LastInformative.Equal(t0) {
		t.Errorf("LastInformative = %v, want it pinned at the last real observation %v", cov.LastInformative, t0)
	}
}

// TestSkippedDoesNotAdvanceLastInformative: same rule for the other
// non-informative case.
func TestSkippedDoesNotAdvanceLastInformative(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)
	h.RecordCoverage("p", CoverageObservation{At: t0, OK: true})
	h.RecordCoverage("p", CoverageObservation{At: t0.Add(time.Hour), Skipped: true})

	cov, _ := h.Coverage("p")
	if !cov.LastInformative.Equal(t0) {
		t.Errorf("LastInformative = %v, want %v — a skipped poll teaches nothing", cov.LastInformative, t0)
	}
	if !cov.LastSuccess.Equal(t0) {
		t.Errorf("LastSuccess = %v, want %v", cov.LastSuccess, t0)
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

	// Spaced like real rounds, with no round numbers: the recorder then has only
	// time to go on, and 20 minutes apart is well inside the hour after which
	// attempts count as stopped polling.
	for i := range 30 {
		h.RecordCoverage("p", CoverageObservation{
			At: worked.Add(time.Duration(i+1) * 20 * time.Minute), FailureClass: OutcomeHTTP,
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

// TestRapidRepeatsCannotManufactureARun: the counter advances per polling ROUND,
// not per call. Anything that re-enters the poll on demand — Retry, "Refresh
// now", any future trigger — re-polls inside the round in progress, and would
// otherwise let a user click their way to the sustained run the verdict is
// supposed to require.
func TestRapidRepeatsCannotManufactureARun(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)

	// Twenty failures one second apart, as fast as someone can click — all of
	// them observations of the one round the scheduler is in.
	for i := range 20 {
		h.RecordCoverage("p", CoverageObservation{
			At: t0.Add(time.Duration(i) * time.Second), FailureClass: OutcomeHTTP, Round: 1,
		})
	}

	cov, ok := h.Coverage("p")
	if !ok {
		t.Fatal("the attempts must still be recorded — someone did look")
	}
	if cov.ConsecutiveFailures > 1 {
		t.Errorf("ConsecutiveFailures = %d after 20 clicks in 20 seconds; a run counts rounds, not clicks",
			cov.ConsecutiveFailures)
	}
	if !cov.LastAttempt.After(t0) {
		t.Error("LastAttempt did not advance; the looking still happened")
	}
}

// TestSaveThenLoadCoverageRoundTrip pins the writer and the coverage-only reader
// against each other.
//
// Save and LoadCoverage are two code paths over one JSON key. Nothing else makes
// them agree — and if they drift, LoadCoverage finds nothing, returns an empty
// map with NO error, and the audit reports the coverage tier as present while
// every verdict resolves to "unknown". The feature would be dead and silent.
func TestSaveThenLoadCoverageRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	at := time.Now().Truncate(time.Second)

	h := New(100)
	h.Add("p", Sample{Time: at, Up: true}) // a series too, so the reader must skip it
	h.RecordCoverage("p", CoverageObservation{At: at, FailureClass: OutcomeHTTP})
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}

	cov, found, err := LoadCoverage(path)
	if err != nil || !found {
		t.Fatalf("LoadCoverage = (_, %v, %v), want (map, true, nil)", found, err)
	}
	got, ok := cov["p"]
	if !ok {
		t.Fatal("LoadCoverage found no entry that Save had just written — the writer and this reader have drifted")
	}
	if got.ConsecutiveFailures != 1 || got.LastFailureClass != OutcomeHTTP {
		t.Errorf("entry = %+v, want 1 failure of class %q", got, OutcomeHTTP)
	}
}

// TestLoadCoverageDistinguishesAbsentFromUnreadable: a corrupt file must not
// report as a missing one. They are different facts calling for different
// responses, and reporting the loss as a gap is how evidence loss goes unseen.
func TestLoadCoverageDistinguishesAbsentFromUnreadable(t *testing.T) {
	dir := t.TempDir()

	_, found, err := LoadCoverage(filepath.Join(dir, "nope.json"))
	if found || err != nil {
		t.Errorf("missing file = (found %v, err %v), want (false, nil)", found, err)
	}

	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte(`{"version":1,"series":`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err = LoadCoverage(corrupt)
	if !found {
		t.Error("a corrupt file must report as FOUND; calling it absent hides an evidence loss")
	}
	if err == nil {
		t.Error("a corrupt file must report its error, not return an empty map silently")
	}
}

// TestLoadCoverageDropsFutureDatedEntries proves the future-timestamp guard can
// actually FIRE.
//
// It is at Load and not at record time for a reason worth pinning: obs.At comes
// from the same process and the same clock as any check inside RecordCoverage
// would, so a guard there can never trigger — it reads as protection and gives
// none. The damage arrives from DISK, written by a run whose clock was wrong.
//
// And the entry must be DROPPED rather than kept, because coverage is monotonic:
// a future stamp refuses every later observation, successes included, so keeping
// it freezes that provider's record permanently.
func TestLoadCoverageDropsFutureDatedEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	future := time.Now().Add(48 * time.Hour)

	h := New(100)
	h.RecordCoverage("skewed", CoverageObservation{At: future, OK: true})
	h.RecordCoverage("sane", CoverageObservation{At: time.Now().Add(-time.Hour), OK: true})
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}

	cov, _, err := LoadCoverage(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cov["skewed"]; present {
		t.Error("a future-dated entry survived Load; it would refuse every later observation forever")
	}
	if _, present := cov["sane"]; !present {
		t.Error("the guard took a sane entry with it")
	}
}

// TestBlindAttemptsDoNotBreakAFailureRun: the contiguity rule asks "did the
// looking stop", and a blind attempt is still someone looking.
//
// Keying it on the last INFORMATIVE observation made a connectivity blip read as
// a break and silently reset a genuine failure streak — blind polls counting
// against the feed through the back door, which is the opposite of what marking
// them blind is for.
func TestBlindAttemptsDoNotBreakAFailureRun(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-8 * time.Hour)
	gap := time.Hour         // attempts further apart than this are stopped polling
	step := 20 * time.Minute // well inside it, and the blind stretch stays inside the 6h informative window

	// Five real failures, a stretch of blind polls, then more real failures. The
	// blind stretch never interrupts the ATTEMPTS, so the run continues.
	at := t0
	for range 5 {
		h.RecordCoverage("p", CoverageObservation{At: at, FailureClass: OutcomeHTTP, RunGap: gap})
		at = at.Add(step)
	}
	for range 8 {
		h.RecordCoverage("p", CoverageObservation{At: at, Blind: true, RunGap: gap})
		at = at.Add(step)
	}
	for range 5 {
		h.RecordCoverage("p", CoverageObservation{At: at, FailureClass: OutcomeHTTP, RunGap: gap})
		at = at.Add(step)
	}

	cov, _ := h.Coverage("p")
	if cov.ConsecutiveFailures != 10 {
		t.Errorf("ConsecutiveFailures = %d, want 10 — the blind stretch never stopped the polling, so the run continued",
			cov.ConsecutiveFailures)
	}
}

// TestRunBreaksWhenPollingActuallyStops is the other half: a gap in ATTEMPTS is
// a real break, and the run must not span it.
func TestRunBreaksWhenPollingActuallyStops(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-72 * time.Hour)
	gap := 10 * time.Minute

	for range 9 {
		h.RecordCoverage("p", CoverageObservation{At: t0, FailureClass: OutcomeHTTP, RunGap: gap})
		t0 = t0.Add(time.Minute)
	}
	// The app is closed for a month, then one failure on relaunch.
	h.RecordCoverage("p", CoverageObservation{At: t0.Add(30 * 24 * time.Hour), FailureClass: OutcomeHTTP, RunGap: gap})

	cov, _ := h.Coverage("p")
	if cov.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1 — nine failures and a month of silence are not a ten-poll run",
			cov.ConsecutiveFailures)
	}
}

// TestFastFailureAfterSlowRoundStillCounts pins the defect the round number
// exists to remove. Under the clock heuristics, the rate limit was a fraction of
// the PREVIOUS round's measured cost, so a provider that went from timing out
// (the round's bottleneck) to failing instantly produced two observations closer
// together than the limit — and a genuine failure was discarded as a click,
// precisely on the transition the record exists to catch.
func TestFastFailureAfterSlowRoundStillCounts(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)

	// Round 1: the provider was the slow one and answered at t0+45s. Round 2
	// started 30s later and the provider failed instantly — 30s after its
	// previous observation, with the configured cadence being 30s.
	h.RecordCoverage("p", CoverageObservation{
		At: t0.Add(45 * time.Second), FailureClass: OutcomeTransport, Round: 1, RunGap: time.Minute,
	})
	h.RecordCoverage("p", CoverageObservation{
		At: t0.Add(75 * time.Second), FailureClass: OutcomeHTTP, Round: 2, RunGap: time.Minute,
	})

	cov, _ := h.Coverage("p")
	if cov.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2 — two rounds, two failures; how long the first round took is irrelevant",
			cov.ConsecutiveFailures)
	}
	if cov.LastFailureClass != OutcomeHTTP {
		t.Errorf("LastFailureClass = %q, want the second round's %q", cov.LastFailureClass, OutcomeHTTP)
	}
}

// TestABlindRoundThenARealFailureInItCountsOnce: a round that opened blind and
// then saw a real failure on a manual refresh has exactly one failure to count.
// Keying "already counted this round" on the last ATTEMPT's round discarded that
// failure — the round's only informative observation — and left the staleness
// clock running from an older timestamp.
func TestABlindRoundThenARealFailureInItCountsOnce(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)

	h.RecordCoverage("p", CoverageObservation{At: t0, Blind: true, Round: 1})
	h.RecordCoverage("p", CoverageObservation{
		At: t0.Add(10 * time.Second), FailureClass: OutcomeHTTP, Round: 1,
	})
	// A second refresh in the same round changes nothing more.
	h.RecordCoverage("p", CoverageObservation{
		At: t0.Add(20 * time.Second), FailureClass: OutcomeTransport, Round: 1,
	})

	cov, _ := h.Coverage("p")
	if cov.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1 — one round, one failure, however many polls", cov.ConsecutiveFailures)
	}
	if !cov.LastInformative.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("LastInformative = %v, want the counted failure at +10s", cov.LastInformative)
	}
	if cov.LastFailureClass != OutcomeHTTP {
		t.Errorf("LastFailureClass = %q, want the counted failure's %q", cov.LastFailureClass, OutcomeHTTP)
	}
}

// TestABlindRoundThenARealFailureContinuesTheRun is the same shape with a run
// already standing, which is what tells "counted" from "reset, then counted":
// the contiguity rule compared the round against prevRound+1 with !=, so a
// failure in the SAME round as a blind attempt read as a skipped round and
// reset nine failures to one. Every first round after a relaunch is blind.
func TestABlindRoundThenARealFailureContinuesTheRun(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)
	for r := range uint64(9) {
		h.RecordCoverage("p", CoverageObservation{
			At: t0.Add(time.Duration(r) * time.Minute), FailureClass: OutcomeHTTP, Round: r + 1,
		})
	}
	h.RecordCoverage("p", CoverageObservation{At: t0.Add(9 * time.Minute), Blind: true, Round: 10})
	h.RecordCoverage("p", CoverageObservation{
		At: t0.Add(9*time.Minute + 10*time.Second), FailureClass: OutcomeHTTP, Round: 10,
	})

	cov, _ := h.Coverage("p")
	if cov.ConsecutiveFailures != 10 {
		t.Errorf("ConsecutiveFailures = %d, want 10 — the blind attempt and the failure are one round, not a gap",
			cov.ConsecutiveFailures)
	}
}

// TestAFailureAfterASuccessInTheSameRoundIsRecorded: a success ends the round's
// counted failure, so a later failure in that round is a new fact. Keeping
// LastFailureRound across the success dropped it entirely, and the verdict read
// healthy from a LastInformative that pointed at the older success.
func TestAFailureAfterASuccessInTheSameRoundIsRecorded(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)

	h.RecordCoverage("p", CoverageObservation{At: t0, FailureClass: OutcomeHTTP, Round: 5})
	h.RecordCoverage("p", CoverageObservation{At: t0.Add(10 * time.Second), OK: true, Round: 5})
	h.RecordCoverage("p", CoverageObservation{At: t0.Add(20 * time.Second), FailureClass: OutcomeHTTP, Round: 5})

	cov, _ := h.Coverage("p")
	if cov.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1", cov.ConsecutiveFailures)
	}
	if !cov.LastInformative.Equal(t0.Add(20 * time.Second)) {
		t.Errorf("LastInformative = %v, want the failure at +20s — the last thing learned was a failure", cov.LastInformative)
	}
	if cov.LastFailureClass != OutcomeHTTP {
		t.Errorf("LastFailureClass = %q, want %q", cov.LastFailureClass, OutcomeHTTP)
	}
}

// TestAnObservationWithNoOutcomeAssertsNothing: a zero-value observation names
// no outcome, and must not be counted as a failure with an empty class.
func TestAnObservationWithNoOutcomeAssertsNothing(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)
	for r := range uint64(10) {
		h.RecordCoverage("p", CoverageObservation{At: t0.Add(time.Duration(r) * time.Minute), Round: r + 1})
	}
	cov, ok := h.Coverage("p")
	if !ok || cov.LastAttempt.IsZero() {
		t.Fatal("the attempts must still be recorded")
	}
	if cov.ConsecutiveFailures != 0 || cov.LastFailureClass != "" || !cov.LastInformative.IsZero() {
		t.Errorf("an outcome-less observation moved the record: cf=%d lfc=%q li=%v",
			cov.ConsecutiveFailures, cov.LastFailureClass, cov.LastInformative)
	}
}

// TestLoadReportsACorruptFileAndLeavesItInPlace: Load is shared with a read-only
// reader, so it must name the loss and touch nothing; moving the file is the
// app's decision, through SetAside.
func TestLoadReportsACorruptFileAndLeavesItInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := Load(path, 10)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load() err = %v, want ErrCorrupt", err)
	}
	if h == nil || h.Len("p") != 0 {
		t.Fatal("Load must still return a usable, empty History")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("Load moved or removed the file: %v", statErr)
	}
	aside, mvErr := SetAside(path)
	if mvErr != nil {
		t.Fatal(mvErr)
	}
	if _, statErr := os.Stat(aside); statErr != nil {
		t.Fatalf("SetAside did not leave the file at %s: %v", aside, statErr)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("SetAside left the original in the way of the next save")
	}
}

// TestSkippedPollEndsAFailureRun: a run is attempts that keep failing, and a poll
// that fetched nothing by design is not a failing attempt. Clearing the class
// while keeping the count produced a "sustained failure" with no cause.
func TestSkippedPollEndsAFailureRun(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)
	for r := range uint64(5) {
		h.RecordCoverage("p", CoverageObservation{
			At: t0.Add(time.Duration(r) * time.Minute), FailureClass: OutcomeHTTP, Round: r + 1,
		})
	}
	h.RecordCoverage("p", CoverageObservation{At: t0.Add(5 * time.Minute), Skipped: true, Round: 6})

	cov, _ := h.Coverage("p")
	if cov.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d after a skip, want 0", cov.ConsecutiveFailures)
	}
	if cov.LastFailureClass != "" {
		t.Errorf("LastFailureClass = %q after a skip, want empty", cov.LastFailureClass)
	}
	if !cov.LastInformative.Equal(t0.Add(4 * time.Minute)) {
		t.Error("a skip is not a success: LastInformative must stay at the last real failure")
	}
}

// TestARoundWithNoObservationBreaksTheRun: a provider disabled for a few rounds
// and re-enabled was not watched in between, and the failures on either side are
// not one episode — regardless of how much wall-clock time that took.
func TestARoundWithNoObservationBreaksTheRun(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)

	for r := range uint64(3) {
		h.RecordCoverage("p", CoverageObservation{
			At: t0.Add(time.Duration(r) * time.Minute), FailureClass: OutcomeHTTP, Round: r + 1,
		})
	}
	// Rounds 4, 5 and 6 went by with the provider disabled; round 7 fails.
	h.RecordCoverage("p", CoverageObservation{
		At: t0.Add(7 * time.Minute), FailureClass: OutcomeHTTP, Round: 7,
	})

	cov, _ := h.Coverage("p")
	if cov.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1 — three rounds passed with nobody looking", cov.ConsecutiveFailures)
	}
}

// TestContiguousRoundsStillBreakAcrossASleep: the one thing a round number
// cannot see is time the process was not running — a laptop lid closed for an
// afternoon produces rounds N and N+1 hours apart. That is a stopped poll.
func TestContiguousRoundsStillBreakAcrossASleep(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-8 * time.Hour)

	h.RecordCoverage("p", CoverageObservation{At: t0, FailureClass: OutcomeHTTP, Round: 1, RunGap: time.Minute})
	h.RecordCoverage("p", CoverageObservation{
		At: t0.Add(3 * time.Hour), FailureClass: OutcomeHTTP, Round: 2, RunGap: time.Minute,
	})

	cov, _ := h.Coverage("p")
	if cov.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1 — consecutive round numbers three hours apart are not continuous polling",
			cov.ConsecutiveFailures)
	}
}

// TestRoundNumbersContinueAcrossRestart pins the counter's seed. A counter
// restarting at one would collide with the persisted LastRound, and the first
// failure after every relaunch would be refused as a repeat of the round before
// the restart — which is the opposite of what a relaunch means.
func TestRoundNumbersContinueAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	t0 := time.Now().Add(-10 * time.Minute)

	h := New(10)
	for range 3 {
		h.NextRound()
	}
	h.RecordCoverage("p", CoverageObservation{At: t0, FailureClass: OutcomeHTTP, Round: 3})
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}

	h2, err := Load(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	round := h2.NextRound()
	if round != 4 {
		t.Fatalf("NextRound() after reload = %d, want 4 — numbering continues from the persisted record", round)
	}
	// A quick restart is not a stopped poll: the run carries across it.
	h2.RecordCoverage("p", CoverageObservation{At: t0.Add(time.Minute), FailureClass: OutcomeHTTP, Round: round})
	cov, _ := h2.Coverage("p")
	if cov.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d after a one-minute restart, want 2", cov.ConsecutiveFailures)
	}
	if cov.LastRound != 4 {
		t.Errorf("LastRound = %d, want 4", cov.LastRound)
	}
}

// TestSkippedPollIsNotAFeedFailure: an Optional provider that publishes no feed
// answers 404 and is skipped by design. Counting that as a failure gives a
// provider working exactly as intended a sustained "broken feed" run.
func TestSkippedPollIsNotAFeedFailure(t *testing.T) {
	h := New(10)
	t0 := time.Now().Add(-time.Hour)
	for i := range 30 {
		h.RecordCoverage("groq", CoverageObservation{
			At: t0.Add(time.Duration(i) * time.Minute), Skipped: true,
		})
	}
	cov, ok := h.Coverage("groq")
	if !ok {
		t.Fatal("a skipped poll is still an attempt and must be recorded")
	}
	if cov.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d after 30 skipped polls, want 0", cov.ConsecutiveFailures)
	}
	if cov.LastFailureClass != "" {
		t.Errorf("LastFailureClass = %q, want empty — nothing failed", cov.LastFailureClass)
	}
}
