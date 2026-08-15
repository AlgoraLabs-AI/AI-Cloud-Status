package history

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// incidentCap bounds the retained incident entries per provider so the journal
// stays fixed-size by construction, like the sample ring buffer.
const incidentCap = 50

// incidentsVersion is the on-disk format version for the persisted journal.
const incidentsVersion = 1

// IncidentEntry is one distinct incident observed on a provider's status feed,
// retained after it resolves so the detail panel can show a recent-incident
// history even once the feed no longer lists it. Severity is the worst level
// observed across the incident's lifetime, stored as the providers.Severity
// integer so this package stays decoupled from the providers package.
type IncidentEntry struct {
	Summary   string    `json:"s"`
	Severity  int       `json:"sev"`
	Regions   []string  `json:"r,omitempty"`
	URL       string    `json:"u,omitempty"`
	FirstSeen time.Time `json:"fs"`
	LastSeen  time.Time `json:"ls"`
	// Started is the feed's own reported start time, when the adapter exposes
	// one (zero otherwise — FirstSeen is the fallback for "when did this
	// begin" in that case). Immutable once set: the feed's own start time
	// doesn't change across updates to the same incident.
	Started time.Time `json:"st"`
}

// Key identifies an incident across polls: its own page URL when the feed
// provides one (stable even if the title is edited), else its summary.
//
// It is exported because the identity has a SECOND reader: the drill-down
// subtracts the incidents it already drew from the current feed out of the
// last-24h history, and it can only do that if both sides agree on what "the
// same incident" means. A second, hand-written notion of identity over there
// would drift from this one and the panel would list an open incident twice.
func (e IncidentEntry) Key() string {
	return IncidentKey(e.URL, e.Summary)
}

// IncidentKey is Key for a caller holding the two fields but not an entry —
// the providers.Incident side of the same comparison.
func IncidentKey(url, summary string) string {
	if url != "" {
		return url
	}
	return summary
}

// IncidentLog is a concurrency-safe, bounded journal of the incidents each
// provider's feed has listed, keyed by provider check id.
type IncidentLog struct {
	mu     sync.Mutex
	series map[string][]IncidentEntry // provider id → entries, oldest first
}

// NewIncidentLog returns an empty journal.
func NewIncidentLog() *IncidentLog {
	return &IncidentLog{series: map[string][]IncidentEntry{}}
}

// Observe merges the incidents currently listed by provider id's feed into the
// journal at time now: a previously-seen incident refreshes its LastSeen,
// summary, and regions (keeping the worst severity observed), a new one is
// appended. Entries with no summary and no URL are unidentifiable and skipped.
func (l *IncidentLog) Observe(id string, incs []IncidentEntry, now time.Time) {
	if id == "" || len(incs) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	buf := l.series[id]
	for _, in := range incs {
		if in.Key() == "" {
			continue
		}
		found := false
		for i := range buf {
			if buf[i].Key() == in.Key() {
				buf[i].LastSeen = now
				buf[i].Summary = in.Summary
				buf[i].Regions = in.Regions
				if in.Severity > buf[i].Severity {
					buf[i].Severity = in.Severity
				}
				if buf[i].Started.IsZero() {
					buf[i].Started = in.Started
				}
				found = true
				break
			}
		}
		if !found {
			in.FirstSeen, in.LastSeen = now, now
			buf = append(buf, in)
		}
	}
	if len(buf) > incidentCap {
		trimmed := make([]IncidentEntry, incidentCap)
		copy(trimmed, buf[len(buf)-incidentCap:])
		buf = trimmed
	}
	l.series[id] = buf
}

// Recent returns provider id's entries last seen after since, newest first.
func (l *IncidentLog) Recent(id string, since time.Time) []IncidentEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []IncidentEntry
	for _, e := range l.series[id] {
		if e.LastSeen.After(since) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// Prune drops entries last seen before now-maxAge, or first seen further than
// an hour into the future (clock changes / corrupt persisted data). Called
// after restoring the persisted journal at launch.
func (l *IncidentLog) Prune(maxAge time.Duration, now time.Time) {
	const futureSlack = time.Hour
	cutoff := now.Add(-maxAge)
	limit := now.Add(futureSlack)
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, buf := range l.series {
		kept := buf[:0:0]
		for _, e := range buf {
			if !e.LastSeen.Before(cutoff) && !e.FirstSeen.After(limit) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(l.series, id)
		} else {
			l.series[id] = kept
		}
	}
}

// incidentsOnDisk is the serialized form of an IncidentLog.
type incidentsOnDisk struct {
	Version int                        `json:"version"`
	Series  map[string][]IncidentEntry `json:"series"`
}

// Save atomically writes the journal to path (temp file + rename).
func (l *IncidentLog) Save(path string) error {
	l.mu.Lock()
	snap := incidentsOnDisk{Version: incidentsVersion, Series: map[string][]IncidentEntry{}}
	for id, buf := range l.series {
		cp := make([]IncidentEntry, len(buf))
		copy(cp, buf)
		snap.Series[id] = cp
	}
	l.mu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// LoadIncidents reads a persisted incident journal, returning an empty log
// (not an error) when the file does not yet exist. A corrupt file starts fresh:
// the journal is best-effort, not authoritative state.
func LoadIncidents(path string) (*IncidentLog, error) {
	l := NewIncidentLog()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return l, err
	}
	var snap incidentsOnDisk
	if err := json.Unmarshal(data, &snap); err != nil {
		return NewIncidentLog(), nil
	}
	for id, buf := range snap.Series {
		if len(buf) > incidentCap {
			buf = buf[len(buf)-incidentCap:]
		}
		cp := make([]IncidentEntry, len(buf))
		copy(cp, buf)
		l.series[id] = cp
	}
	return l, nil
}
