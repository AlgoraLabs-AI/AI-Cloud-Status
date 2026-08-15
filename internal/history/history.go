// Package history maintains a bounded, per-check ring buffer of recent health
// samples (up/down + response latency) for the uptime sparkline and latency
// trend shown in the UI. It is deliberately tiny: an in-memory ring capped at a
// fixed number of samples per check, persisted to a single capped JSON file. No
// embedded database is used — the on-disk footprint is bounded by construction.
package history

import (
	"encoding/json"
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

// History is a concurrency-safe collection of per-check ring buffers.
type History struct {
	mu     sync.Mutex
	cap    int
	caps   map[string]int // per-check cap overrides (see SetCap)
	series map[string][]Sample
}

// New returns an empty History retaining up to capacity samples per check.
// A non-positive capacity falls back to DefaultCapacity.
func New(capacity int) *History {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &History{cap: capacity, caps: map[string]int{}, series: map[string][]Sample{}}
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
}

// Save atomically writes the history to path (temp file + rename), creating the
// parent directory if needed.
func (h *History) Save(path string) error {
	h.mu.Lock()
	snap := onDisk{Version: snapshotVersion, Cap: h.cap, Series: map[string][]Sample{}}
	for id, buf := range h.series {
		cp := make([]Sample, len(buf))
		copy(cp, buf)
		snap.Series[id] = cp
	}
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
	return h, nil
}
