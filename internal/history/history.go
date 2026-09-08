// Package history maintains a bounded, per-check ring buffer of recent health
// samples (up/down + response latency) for the uptime sparkline and latency
// trend shown in the UI. It is deliberately tiny: an in-memory ring capped at a
// fixed number of samples per check, persisted to a single capped JSON file. No
// embedded database is used — the on-disk footprint is bounded by construction.
package history

import (
	"encoding/json"
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
	// Outcome says WHY Unknown is set, which the bool alone cannot: Unknown
	// collapses an unreadable feed and a deliberately skipped check into one
	// value, so a reader that treats every Unknown as a broken feed misreads an
	// Optional provider that has no feed at all. Empty on samples written before
	// this field existed, and on the ping and custom-URL paths, which never set
	// Unknown — there, failing to respond IS the monitored signal.
	Outcome string `json:"oc,omitempty"`
}

// Outcome values for Sample.Outcome, and for CoverageEntry.LastFailureClass.
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
// always, and either the success time or the failure class.
//
// failureClass is one of the Outcome constants, or "" on success. Callers on the
// ping and custom-URL paths do not use this — coverage answers "could the feed be
// read", which is only a question for checks that HAVE a feed.
func (h *History) RecordCoverage(id string, at time.Time, ok bool, failureClass string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.coverage == nil { // a History built by a zero literal rather than New
		h.coverage = map[string]CoverageEntry{}
	}
	cov := h.coverage[id]
	cov.LastAttempt = at
	if ok {
		cov.LastSuccess = at
		cov.LastFailureClass = ""
	} else {
		cov.LastFailureClass = failureClass
	}
	h.coverage[id] = cov
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
	const futureSlack = time.Hour
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
	up, known := 0, 0
	for _, s := range buf {
		// Unknown samples are neither up nor down, so they belong in neither
		// half of the ratio. Counting them in the denominator — which this did
		// until 2026-09-08 — reports a check whose feed was unreadable all
		// window as though it had been DOWN all window, contradicting the
		// Sample doc above and inverting the one signal a reader wants.
		if s.Unknown {
			continue
		}
		known++
		if s.Up {
			up++
		}
	}
	if known == 0 {
		return 0, false // nothing was actually observed: no percentage is honest
	}
	return float64(up) / float64(known), true
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
	// omitempty so a build that has never recorded coverage writes a file a
	// previous version reads unchanged.
	Coverage map[string]CoverageEntry `json:"coverage,omitempty"`
}

// Save atomically writes the history to path (temp file + rename), creating the
// parent directory if needed.
func (h *History) Save(path string) error {
	h.mu.Lock()
	snap := onDisk{
		Version:  snapshotVersion,
		Cap:      h.cap,
		Series:   map[string][]Sample{},
		Coverage: map[string]CoverageEntry{},
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
	// the History itself, or the next Save rebuilds the file without it.
	maps.Copy(h.coverage, snap.Coverage)
	return h, nil
}
