// Package history maintains a bounded, per-check ring buffer of recent health
// samples (up/down + response latency) for the uptime sparkline and latency
// trend shown in the UI. It is deliberately tiny: an in-memory ring capped at a
// fixed number of samples per check, persisted to a single capped JSON file. No
// embedded database is used — the on-disk footprint is bounded by construction.
package history

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/atomicfile"
)

// DefaultCapacity is the number of samples retained per check, sized so the
// 24-hour provider uptime strip can always fill: 24h at the fastest provider
// cadence (15s) is 5760 samples, plus headroom. High-frequency checks that only
// display a short window (connectivity pings) get a smaller per-check cap via
// SetCap so they don't bloat memory or the persisted file. The bound is on
// count (not time) so the cost stays fixed by construction.
const DefaultCapacity = 6000

// snapshotVersion is the on-disk format version for the persisted ring buffer.
const snapshotVersion = 1

// Sample is one observation of a check at a point in time.
//
// Unknown marks an observation where the check itself could not judge service
// health: a provider status FEED that could not be fetched/parsed, or a
// skipped check. It mirrors the app's core FeedState principle — an unreadable
// feed is unknown, NEVER an outage — into the history layer: unknown samples
// paint grey on the strip and are excluded from the uptime percentage (they
// are neither up nor down). Connectivity pings and custom URL checks never set
// it: there, failing to respond IS the monitored signal. Old persisted samples
// lack the field (false), so a pre-field feed failure keeps reading as down
// until it ages out of the window.
//
// DownRegions records, for a NOT-up provider sample, the feed regions whose
// MAJOR incidents drove the sample down, so the uptime strip can be recomputed
// against the user's current region de-selections (muting) WITHOUT re-polling.
// Its meaning is three-way:
//   - Up == true                          → up.
//   - Up == false && len(DownRegions)==0  → unscoped down: a GLOBAL major
//     incident, a connectivity/URL failure, or an old pre-field sample. Always
//     counts as down regardless of muting.
//   - Up == false && len(DownRegions)>0   → down iff at least one listed region
//     is still active (not muted); once every listed region is muted the sample
//     reads as up again.
//
// A mixed global+regional major outage is recorded as unscoped (empty
// DownRegions) so muting the regional parts can never falsely green it.
//
// Degraded / DegradedRegions mirror the same idea one severity down: Degraded
// marks that the sample had a non-outage (Minor) degradation, and DegradedRegions
// are the regions driving it (empty = unscoped/global degradation). They paint the
// strip amber and recompute against muting exactly like the down fields, but never
// count against the uptime percentage — a degraded service is still reachable.
// Responded marks a sample whose round-trip COMPLETED even though the check
// judged it not up — an HTTP 500, or a 200 whose body lacked the expected text.
// It exists because Latency alone cannot tell those apart from a DNS failure or
// a timeout: urlcheck.Probe records the elapsed time in every case, so a
// transport failure carries a latency that measures nothing. Without this flag
// an "average response time" must either exclude a slow, failing server
// entirely — hiding exactly the slowness the user opened the panel to see — or
// average in timeouts as if they were responses. Connectivity pings never set
// it: an unanswered ping produced no round-trip at all.
type Sample struct {
	Time            time.Time     `json:"t"`
	Up              bool          `json:"up"`
	Unknown         bool          `json:"un,omitempty"`
	Responded       bool          `json:"rsp,omitempty"`
	Latency         time.Duration `json:"ms"`
	DownRegions     []string      `json:"dr,omitempty"`
	Degraded        bool          `json:"dg,omitempty"`
	DegradedRegions []string      `json:"dgr,omitempty"`
}

// Outcome values for CoverageEntry.LastFailureClass.
//
// Plain strings rather than a typed enum borrowed from providers, for the same
// reason Incident stores a bare severity int: this package stays decoupled from
// the providers package.
const (
	OutcomeReadable  = "readable"
	OutcomeTransport = "transport"
	OutcomeHTTP      = "http"
	OutcomeParse     = "parse"
	OutcomeSkipped   = "skipped"
)

// CoverageEntry is the per-check attendance record: was anyone watching, and did
// the look succeed.
//
// It answers the one question no event store can. Every other artifact this app
// writes is edge- or failure-triggered, and silence in an event store is not
// evidence — "nothing happened" and "nobody was watching" produce the identical
// empty result. cmd/alertaudit names the missing primitive in its own source,
// "a per-provider last-successful-poll", and this is it.
//
// It is deliberately NOT a Sample: it is a high-water mark, not an observation,
// so it is bounded by the number of checks rather than by time, and it must
// survive Prune. See onDisk, where it is a sibling of Series for exactly that
// reason — the same idiom, and the same justification, as TotalLoss sitting
// beside Checks in the outage state file.
type CoverageEntry struct {
	LastAttempt time.Time `json:"la"`
	// omitzero, not omitempty: omitempty has no effect on a struct field, so a
	// never-succeeded check would serialize a zero time rather than omit it.
	LastSuccess      time.Time `json:"ls,omitzero"`
	LastFailureClass string    `json:"lfc,omitempty"`
	// LastInformative is the last attempt that actually TOLD us something — a
	// real read or a real failure. Blind and skipped attempts advance LastAttempt
	// but not this.
	//
	// The distinction is load-bearing twice over. "Was the last thing we learned
	// a success?" is what health means, and without this field a run of blind
	// polls after an old success is indistinguishable from a fresh success. And
	// staleness has to be measured from the last thing we LEARNED: a machine
	// offline for two months still records an attempt every round, so a freshness
	// rule keyed on LastAttempt would call a two-month-old verdict current.
	LastInformative time.Time `json:"li,omitzero"`
	// ConsecutiveFailures counts failed attempts since the last success. It is
	// the field that makes a verdict possible at all: LastSuccess alone says only
	// that the feed worked ONCE, and since a working launch stamps it within a
	// minute, "has ever succeeded" is true for every provider almost immediately
	// and can never again indicate a fault. What distinguishes a feed that is
	// broken right now is that attempts keep failing AFTER the last success.
	//
	// It does NOT advance while the machine itself is offline — see
	// CoverageObservation.Blind. Counting those would blame every provider for
	// the user's own router.
	ConsecutiveFailures int `json:"cf,omitempty"`
	// LastRound is the polling round of the last recorded attempt — see
	// CoverageObservation.Round. It is what lets a failure run be judged by the
	// fact it is about ("was this provider observed in every round between?")
	// instead of by wall-clock distances that merely proxy for it.
	//
	// Zero means the entry predates round numbering, or was last touched by a
	// caller that has none; the round rules then abstain for that one judgement.
	LastRound uint64 `json:"lr,omitempty"`
	// LastFailureRound is the round whose failure the run last COUNTED. A round
	// counts at most one failure however many times it is polled, and this is
	// the fact that rule is checked against — not LastRound, which a blind or
	// skipped attempt also advances: a round that opened blind and then saw a
	// real failure on a manual refresh has exactly one failure to count.
	LastFailureRound uint64 `json:"lfr,omitempty"`
}

// failureRunGap is the floor on how long attempts may be apart before the
// polling is considered to have STOPPED — the app closed or the machine asleep.
// Within a running process that question is answered by round numbers, exactly;
// this bound exists for the stretch no round number can see, between one
// process and the next.
//
// It is a floor, not a value: a caller whose poll cadence is slower supplies a
// larger RunGap, because the cadence is user-configurable with no ceiling and a
// constant alone cannot be "longer than a normal poll interval" for a user who
// chose a longer one. Erring long merely delays a verdict; erring short prevents
// one entirely.
const failureRunGap = time.Hour

// informativeRunGapFactor is how many run-gaps of learning NOTHING may pass
// before a failure run is no longer one episode. Looser than the attempt gap
// because a blind stretch is an expected, legitimate state — the app polling
// through a network outage — and only long enough of it means the failures on
// either side are separate events rather than one.
const informativeRunGapFactor = 6

// minInformativeWindow floors that window. At the shipped defaults the derived
// value is only a few minutes, which any ordinary connectivity drop exceeds — so
// a flaky link would wipe every in-progress run and the sustained-failure
// verdict would be unreachable on exactly the networks where a feed is hardest
// to read. Episodes are separated by hours, not by minutes.
const minInformativeWindow = 2 * time.Hour

// CoverageFutureSlack is how far ahead of now a PERSISTED timestamp may be
// before it is treated as clock damage rather than an observation.
//
// Exported so the audit uses THIS value rather than declaring its own. Two
// packages judging the same file against two constants can disagree about what
// counts as damage, and the disagreement would show up as the app and the audit
// reporting different things about one record.
//
// Coverage needs the guard more than the sample ring does: it is monotonic, so a
// future stamp does not merely look odd, it refuses every later observation.
const CoverageFutureSlack = time.Hour

// CoverageObservation is one poll's worth of attendance, as reported by the
// caller that actually made the request.
//
// Blind is the load-bearing field. It marks an attempt that tells us nothing
// about THIS feed because the machine had no connectivity at all. A blind
// attempt still records that someone was watching; it must never count as
// evidence against the provider. The caller decides blindness, and which
// witness it uses matters: the provider path keys it on the connectivity probe,
// while the custom-URL path in internal/ui still keys its own unknown-sample
// rule on the offline latch — the two are NOT the same rule, and the latch is
// the weaker witness because it folds in "every provider failed".
type CoverageObservation struct {
	At    time.Time
	OK    bool
	Blind bool
	// Skipped marks a check that was deliberately not made — an Optional provider
	// that publishes no feed answers 404 and is skipped by design. Like Blind it
	// is an attempt that carries no verdict about the feed, but the two are
	// different facts and a reader of the record deserves to keep them apart.
	//
	// Without this, a skipped poll falls to the failure branch and an Optional
	// provider accumulates a sustained "failure" run for working exactly as
	// intended.
	Skipped bool
	// FailureClass is one of the Outcome constants when the poll FAILED, and
	// empty otherwise — on a success, a skip and a blind attempt alike. The
	// recorder reads it on the failure branch alone today; the contract is
	// stated here so a caller that fills it on other branches is wrong by the
	// field's own terms, not merely unused.
	FailureClass string
	// Round is the scheduled polling round this observation belongs to, from
	// History.NextRound. It is the fact the failure-run rules are about: a run
	// counts ROUNDS, so two observations from one round count once however they
	// were triggered, and a run is contiguous only while no round went by without
	// this provider being observed. Both are exact, and neither cares how long a
	// round took.
	//
	// Zero means the caller has no round — a manual retry, or a reader older than
	// this field — and the round rules abstain for that observation.
	Round uint64
	// RunGap is how long attempts may be apart before the polling is considered
	// to have stopped — the app closed, the machine asleep. It only matters across
	// a stretch no round number can see; within a process the rounds decide.
	// It is floored at failureRunGap, so a caller supplies it to RAISE the bound
	// for a slow poll cadence, which is user-configurable with no ceiling: a
	// constant alone would call every poll of a slow poller a stopped one, reset
	// the counter each time, and make the sustained-failure verdict unreachable.
	// It also scales the blind-stretch window, which is floored at
	// minInformativeWindow.
	RunGap time.Duration
}

// History is a concurrency-safe collection of per-check ring buffers.
type History struct {
	mu     sync.Mutex
	cap    int
	caps   map[string]int // per-check cap overrides (see SetCap)
	series map[string][]Sample
	// coverage is NOT a ring buffer and is never pruned. It has to live on the
	// in-memory History rather than only in onDisk, because Save rebuilds the
	// snapshot from these fields on every call — a field that exists only in the
	// serialized form is read into the snapshot at Load, dropped, and then erased
	// by the next Save. Keeping the reader and the writer symmetric here is the
	// whole reason this is a struct field and not a local.
	coverage map[string]CoverageEntry
	// round is the last polling round number handed out by NextRound. Seeded at
	// Load from the highest LastRound on disk so numbering continues across
	// restarts: a counter that restarted at one would collide with persisted
	// entries and make the first failure after every relaunch look like a repeat
	// of the round before it.
	round uint64
}

// New returns an empty History retaining up to capacity samples per check.
// A non-positive capacity falls back to DefaultCapacity.
func New(capacity int) *History {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &History{
		cap:      capacity,
		caps:     map[string]int{},
		series:   map[string][]Sample{},
		coverage: map[string]CoverageEntry{},
	}
}

// Capacity returns the default per-check sample cap.
func (h *History) Capacity() int { return h.cap }

// NextRound hands out the number of the next scheduled polling round. Call it
// once per round, from the scheduler — NOT per poll, and NOT for an on-demand
// re-poll, which belongs to the round in progress: that is what makes a run
// count rounds rather than clicks.
func (h *History) NextRound() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.round++
	return h.round
}

// SetCap overrides the retention cap for one check id (non-positive clears the
// override). Used to keep high-frequency checks that only display a short
// window (1s connectivity pings) from retaining the provider-sized ring.
func (h *History) SetCap(id string, capacity int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if capacity <= 0 {
		delete(h.caps, id)
		return
	}
	h.caps[id] = capacity
	if buf := h.series[id]; len(buf) > capacity {
		trimmed := make([]Sample, capacity)
		copy(trimmed, buf[len(buf)-capacity:])
		h.series[id] = trimmed
	}
}

// capFor returns the retention cap for check id (caller holds h.mu).
func (h *History) capFor(id string) int {
	if v, ok := h.caps[id]; ok {
		return v
	}
	return h.cap
}

// Add appends a sample for check id, evicting the oldest sample once the ring is
// full so the retained set is always the most recent h.cap observations.
func (h *History) Add(id string, s Sample) {
	if id == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	buf := h.series[id]
	buf = append(buf, s)
	if max := h.capFor(id); len(buf) > max {
		// Drop the oldest overflow, keeping the tail. Copy into a fresh slice so
		// the backing array does not grow unbounded across many evictions.
		over := len(buf) - max
		trimmed := make([]Sample, max)
		copy(trimmed, buf[over:])
		buf = trimmed
	}
	h.series[id] = buf
}

// Last returns the newest sample's timestamp and the retained count for check
// id, without copying the series. It is the cheap change-detection key used by
// the per-second UI refresh to decide whether the uptime cell must be
// recomputed at all.
func (h *History) Last(id string) (time.Time, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	buf := h.series[id]
	if len(buf) == 0 {
		return time.Time{}, 0
	}
	return buf[len(buf)-1].Time, len(buf)
}

// RecordCoverage updates check id's attendance record for one poll: the attempt
// always, and either the success time or the failure class — see
// CoverageObservation for what each field asserts.
//
// Callers on the ping and custom-URL paths do not use this — coverage answers
// "could the feed be read", which is only a question for checks that HAVE a
// feed.
func (h *History) RecordCoverage(id string, obs CoverageObservation) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.coverage == nil { // a History built by a zero literal rather than New
		h.coverage = map[string]CoverageEntry{}
	}
	cov := h.coverage[id]
	// Captured BEFORE LastAttempt is overwritten below. The contiguity rule needs
	// the distance from the PREVIOUS attempt, and comparing against a field this
	// function has already advanced measures zero every time.
	prevAttempt := cov.LastAttempt
	prevRound := cov.LastRound
	// High-water mark, as the field's own doc says — so enforce it. Two polls for
	// one provider can complete out of order: a scheduled round that started
	// earlier can finish AFTER a manual retry that started later, and recording
	// its older result would move LastInformative backward, resurrect a failure
	// run the user just watched clear, and drop the verdict from healthy to
	// inconclusive about a feed they are looking at working.
	if !obs.At.After(cov.LastAttempt) && !cov.LastAttempt.IsZero() {
		return
	}
	// The future-timestamp hazard is NOT guarded here, and the reason matters:
	// obs.At comes from the same process and the same clock as any check against
	// time.Now() would, so such a check can never fire. It would be a guard that
	// reads as protection and provides none.
	//
	// The damage arrives from DISK — a record written while the clock ran fast,
	// which then blocks every later observation once the clock is corrected. That
	// is a different clock, so it can only be judged where the file is read.
	// LoadCoverage does it.
	cov.LastAttempt = obs.At
	if obs.Round != 0 {
		cov.LastRound = obs.Round
	}
	switch {
	case obs.OK:
		cov.LastSuccess = obs.At
		cov.LastInformative = obs.At
		cov.LastFailureClass = ""
		cov.ConsecutiveFailures = 0
	case obs.Skipped:
		// Nothing was fetched because there was nothing to fetch. Unlike a blind
		// attempt, this DOES say something: whatever the feed used to fail with no
		// longer applies, so keeping the old class would report a failure the
		// provider does not have any more — and the run ends with it, because a
		// run is attempts that KEEP FAILING, and a poll that fetched nothing is
		// not a failing attempt. Clearing one without the other left a record
		// that was a sustained failure with no failure class, which the verdict
		// read as broken and the report printed with an empty cause. A skip is
		// not a success either: LastInformative stays where it was, so the
		// verdict falls to unknown rather than to healthy.
		cov.LastFailureClass = ""
		cov.ConsecutiveFailures = 0
	case obs.Blind:
		// Either the machine had no connectivity, or there was nothing to fetch.
		// Neither is evidence about this feed. Record that we looked; change
		// nothing else — in particular do NOT clear a real failure class, which is
		// still the best explanation we have for why this provider was failing
		// before the link dropped, and do NOT advance the failure run.
	default:
		// A "run" has to be contiguous to mean anything. Without this, nine
		// failures, a month with the app closed, and one failure on relaunch add
		// up to ten and read as sustained — a span with a working feed in the
		// middle of it. Only a success resets the counter otherwise, and a long
		// silence is not a success.
		// A run continues only while BOTH remain true: the looking never stopped,
		// AND something was still being learned. Either bound alone is wrong, and
		// each was tried:
		//
		//   keyed on LastInformative only — a connectivity blip is a stretch of
		//     attempts that deliberately teach nothing, so it read as a break and
		//     silently reset a genuine failure streak. Blind polls counting
		//     against the feed through the back door.
		//   keyed on LastAttempt only — blind attempts advance it, so an
		//     arbitrarily long blind window never breaks a run at all, and
		//     failures on either side of a week offline join into one.
		//
		// "The looking never stopped" is a fact about polling ROUNDS, and the
		// observation carries which round it belongs to, so it is judged on that
		// fact rather than on wall-clock distances proxying for it. The proxies —
		// a measured round cost, doubled, clamped, split into three thresholds —
		// were each retuned by any change to the others: a failure that arrived
		// fast after a slow round read as a click, and a long blind stretch could
		// reset a run the same call then declined to count. Two round rules,
		// both exact and both indifferent to how long a round took:
		//
		//   a round whose failure is already counted — the Refresh button, a
		//     Retry, any on-demand re-poll — is not counted again. Recorded above
		//     as an attempt, since someone did look; the run does not move.
		//     Guarding each button is a race the buttons win; the record refusing
		//     to count a round twice is not. Keyed on the round of the last
		//     COUNTED failure, not the last attempt: a round that opened blind
		//     and then saw a real failure on a refresh has one failure to count.
		//   a round went by with no attempt on this provider — it was disabled,
		//     or the record was written by another process — and the run is not
		//     one episode.
		//
		// What no round number can see stays time-bound: the stretch between one
		// process and the next (RunGap, floored at failureRunGap — within a
		// process the rounds decide, and a round can cost minutes, never an hour),
		// and a blind stretch so long that the failures on either side are
		// separate events. The latter is deliberately looser: a blind stretch is
		// an expected state, the app polling through a network outage, and only
		// becomes disqualifying when the run is no longer one continuous episode.
		if obs.Round != 0 && obs.Round == cov.LastFailureRound {
			break
		}
		roundSkipped := obs.Round != 0 && prevRound != 0 && obs.Round != prevRound+1
		stoppedGap := max(obs.RunGap, failureRunGap)
		informativeWindow := max(informativeRunGapFactor*obs.RunGap, minInformativeWindow)
		pollingStopped := roundSkipped ||
			(!prevAttempt.IsZero() && obs.At.Sub(prevAttempt) > stoppedGap)
		learnedNothingTooLong := !cov.LastInformative.IsZero() &&
			obs.At.Sub(cov.LastInformative) > informativeWindow
		// The reset and the increment are one decision, not two. Splitting them
		// let a call reset the run to zero and then decline to count, leaving a
		// record whose last informative observation is a FAILURE and whose run
		// reads zero — which the verdict resolves to healthy.
		if pollingStopped || learnedNothingTooLong {
			cov.ConsecutiveFailures = 0
		}
		cov.LastInformative = obs.At
		cov.LastFailureClass = obs.FailureClass
		cov.LastFailureRound = obs.Round
		cov.ConsecutiveFailures++
	}
	h.coverage[id] = cov
}

// LoadCoverage reads ONLY the coverage map from a history file.
//
// A reader that wants the ~15 attendance entries should not pay for the ring:
// Load unmarshals every retained sample and then deep-copies each series into a
// fresh slice, which on a mature install is megabytes of parsing and copying for
// a map that fits in a kilobyte.
//
// KNOWN, and not fixed here: alertaudit still reads this file twice per run,
// because RunContinuity independently full-Loads it for the sample history it
// genuinely needs. So this saves the materialization, NOT the second read — the
// two audit sections should share one load, which is a change to their entry
// points rather than to this function.
//
// The three returns are deliberately distinct, because a reader that cannot tell
// them apart will report a corrupt file as an absent one:
//   - (nil, false, nil)  the file does not exist
//   - (nil, true, err)   it exists and could not be read or parsed
//   - (map, true, nil)   it was read; the map may still be empty
func LoadCoverage(path string) (map[string]CoverageEntry, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	// Only the coverage key is decoded; the series are skipped rather than
	// materialized. The envelope is the SAME declaration onDisk embeds, so the
	// writer and this reader cannot drift apart.
	var snap coverageEnvelope
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, true, err
	}
	if snap.Coverage == nil {
		snap.Coverage = map[string]CoverageEntry{}
	}
	// Drop entries stamped implausibly far in the FUTURE. This is the only place
	// the check can work: the timestamps were written by a different run, and
	// possibly a different clock, so here they can be compared against this one.
	//
	// A record written while the clock ran fast is not merely wrong, it is
	// absorbing: RecordCoverage only accepts observations newer than the last, so
	// once the clock is corrected every later poll — successes included — is
	// refused forever, and a frozen entry holding a failure run would fail the
	// audit permanently. Discarding the entry costs one provider's history and
	// restores a record that can move again.
	dropFutureDated(snap.Coverage)
	return snap.Coverage, true, nil
}

// dropFutureDated removes entries stamped implausibly far ahead of now.
//
// Shared by BOTH read paths on purpose. Guarding only the audit's reader left
// the app's own Load unprotected, which is the path that matters more: the app
// is what then refuses every later observation for that provider and rewrites
// the file. A guard that covers one of two readers of the same file is not a
// guard, it is a coin toss on which one sees the damage first.
//
// Each drop is logged: a discarded record is evidence LOST, and without a line
// here the next reader — the app's own save, the audit — reports it as evidence
// that never existed.
func dropFutureDated(m map[string]CoverageEntry) {
	cutoff := time.Now().Add(CoverageFutureSlack)
	for id, cov := range m {
		if cov.LastAttempt.After(cutoff) {
			slog.Warn("history: coverage entry stamped in the future, discarded as clock damage",
				"id", id, "last_attempt", cov.LastAttempt, "cutoff", cutoff)
			delete(m, id)
		}
	}
}

// Coverage returns check id's attendance record and whether one exists.
//
// A false second return means this check was never polled by a build that
// records coverage — which is NOT the same as "the feed never loaded", and
// callers must keep the two apart.
func (h *History) Coverage(id string) (CoverageEntry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cov, ok := h.coverage[id]
	return cov, ok
}

// Prune drops samples outside the plausible display range from every series:
// older than now-maxAge (they can never render in the uptime window again) and
// further than an hour into the future (clock changes / corrupt persisted
// data). Called after restoring persisted history at launch.
func (h *History) Prune(maxAge time.Duration, now time.Time) {
	const futureSlack = CoverageFutureSlack
	cutoff := now.Add(-maxAge)
	limit := now.Add(futureSlack)
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, buf := range h.series {
		kept := buf[:0:0]
		for _, s := range buf {
			if !s.Time.Before(cutoff) && !s.Time.After(limit) {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			delete(h.series, id)
		} else {
			h.series[id] = kept
		}
	}
}

// Series returns a copy of the retained samples for check id, oldest first.
func (h *History) Series(id string) []Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	buf := h.series[id]
	out := make([]Sample, len(buf))
	copy(out, buf)
	return out
}

// Len returns the number of retained samples for check id.
func (h *History) Len(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.series[id])
}

// Uptime returns the fraction of retained samples for check id that were "up",
// in [0,1]. It returns (0, false) when there are no samples.
func (h *History) Uptime(id string) (float64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	buf := h.series[id]
	if len(buf) == 0 {
		return 0, false
	}
	up := 0
	for _, s := range buf {
		if s.Up {
			up++
		}
	}
	return float64(up) / float64(len(buf)), true
}

// LastLatency returns the most recent recorded latency for check id.
func (h *History) LastLatency(id string) (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	buf := h.series[id]
	if len(buf) == 0 {
		return 0, false
	}
	return buf[len(buf)-1].Latency, true
}

// IDs returns the check IDs that currently have samples, sorted.
func (h *History) IDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.series))
	for id := range h.series {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// onDisk is the serialized form of a History.
type onDisk struct {
	Version int                 `json:"version"`
	Cap     int                 `json:"cap"`
	Series  map[string][]Sample `json:"series"`
	// Coverage is a SIBLING of Series, not a member of it, because the two carry
	// different lifetimes: Series is a display-scoped ring that Prune truncates
	// to the uptime window at every launch, while Coverage is a per-check
	// high-water mark that must outlive it. Prune walks h.series only, so a
	// sibling is pruning-exempt by construction — which is what lets this answer
	// "when did this feed last work" over months without storing months.
	//
	coverageEnvelope
}

// coverageEnvelope declares the coverage key ONCE, embedded in onDisk and
// decoded on its own by LoadCoverage.
//
// Two structs that had to agree about one JSON tag is precisely the shape that
// rots: a rename on the writer's side would leave a separate reader silently
// finding nothing, returning an empty map with no error, and reporting the
// coverage tier as present while every verdict resolved to "unknown" — with all
// tests green. Sharing the declaration makes that impossible rather than
// unlikely.
//
// omitempty so a build that has never recorded coverage writes a file a previous
// version reads unchanged.
type coverageEnvelope struct {
	Coverage map[string]CoverageEntry `json:"coverage,omitempty"`
}

// Save atomically writes the history to path (temp file + rename), creating the
// parent directory if needed.
func (h *History) Save(path string) error {
	h.mu.Lock()
	snap := onDisk{
		Version:          snapshotVersion,
		Cap:              h.cap,
		Series:           map[string][]Sample{},
		coverageEnvelope: coverageEnvelope{Coverage: map[string]CoverageEntry{}},
	}
	for id, buf := range h.series {
		cp := make([]Sample, len(buf))
		copy(cp, buf)
		snap.Series[id] = cp
	}
	// Coverage is copied here for the same reason Series is: this snapshot is
	// rebuilt from scratch on every Save, so anything not copied across is
	// silently erased on the next write.
	maps.Copy(snap.Coverage, h.coverage)
	h.mu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// writeFileAtomic writes data to path via a temp file + rename.
//
// Delegated to internal/atomicfile rather than kept local, because this file is
// saved from three goroutines — the 60s ticker, quit, and the watchdog's
// relaunch — and the local version had neither the process-wide write mutex nor
// the Windows rename retry that internal/config needed after hitting exactly
// those failures in the field. In the watchdog case os.Exit follows immediately,
// so a lost rename silently discarded up to a day of samples across a
// self-restart.
func writeFileAtomic(path string, data []byte) error {
	return atomicfile.Write(path, data, 0o600)
}

// Load reads a history file, returning an empty History (not an error) when the
// file does not yet exist. capacity overrides the persisted cap so changing the
// build's retention re-bounds existing data on the next save; each loaded series
// is trimmed to the new cap immediately.
func Load(path string, capacity int) (*History, error) {
	h := New(capacity)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return h, nil
		}
		return h, err
	}
	var snap onDisk
	if err := json.Unmarshal(data, &snap); err != nil {
		// A corrupt history file is non-fatal: start fresh rather than refuse to
		// launch. History is best-effort, not authoritative state.
		//
		// But starting fresh over the file DESTROYS it: the next Save rewrites it
		// without the coverage record, which is the one part of this file that
		// is never pruned and is meant to answer "when did this feed last work"
		// months later. A silent fresh start turns months of attendance into a
		// record that reads as if the install were new. So the damaged file is
		// moved aside, named for when it was found, and the loss is logged —
		// the evidence is kept for a human, and the audit can tell "no record
		// yet" from "a record was here and could not be read".
		aside := fmt.Sprintf("%s.corrupt-%s", path, time.Now().UTC().Format("20060102-150405"))
		renameErr := os.Rename(path, aside)
		slog.Warn("history: file unreadable, starting fresh",
			"path", path, "err", err, "moved_to", aside, "rename_err", renameErr)
		return New(capacity), nil
	}
	for id, buf := range snap.Series {
		if len(buf) > h.cap {
			buf = buf[len(buf)-h.cap:]
		}
		cp := make([]Sample, len(buf))
		copy(cp, buf)
		h.series[id] = cp
	}
	// Symmetric with Save: coverage read into the snapshot has to be copied onto
	// the History itself, or the next Save rebuilds the file without it. And the
	// same clock-damage guard the audit's reader applies — this is the path that
	// matters more, since the app is what refuses later observations and rewrites
	// the file.
	dropFutureDated(snap.Coverage)
	maps.Copy(h.coverage, snap.Coverage)
	for _, cov := range h.coverage {
		h.round = max(h.round, cov.LastRound)
	}
	return h, nil
}
