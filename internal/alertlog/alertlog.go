// Package alertlog keeps a durable, append-only JSONL audit trail of every
// alert the app raised — or deliberately suppressed — one JSON object per line.
// Unlike acs.log (truncated on startup once it passes its size cap), this file
// survives restarts for months, so it can answer "which monitors NEVER alerted
// over the last weeks?": a check absent here for that long is more likely a
// mis-parsed feed than a genuinely silent one (cross-check feed-samples/).
package alertlog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// keepWindow is how far back entries are retained; older lines are pruned on
// Open. Generous on purpose — the whole point is looking back weeks, and
// alert entries are tiny and rare.
const keepWindow = 90 * 24 * time.Hour

// maxBytes is this journal's share of the diagnostics disk budget, and a
// backstop the time window alone does not provide.
//
// keepWindow bounds AGE, not SIZE, and it is only applied at Open — so an app
// left running for weeks (which is how a tray monitor is meant to be used) had
// nothing bounding this file at all within a run. In practice entries are tiny
// and rare: a real install measured 20 KB after a month, so 4 MiB is ~200x
// headroom and will normally never be reached. It exists for the case that is
// not normal — a provider flapping every poll for days — where the honest
// behaviour is to keep the newest evidence and drop the oldest, rather than to
// grow without limit.
const maxBytes = 4 << 20

// Path returns the audit log path inside dir.
func Path(dir string) string { return filepath.Join(dir, "alert-log.jsonl") }

// Entry is one audited alert. Suppressed carries the reason delivery was
// swallowed (muted, do-not-disturb, regions-deactivated, …); empty means the
// alert was actually shown to the user.
type Entry struct {
	Time       time.Time `json:"ts"`
	ID         string    `json:"id,omitempty"` // check id; empty for app-wide connectivity alerts
	Title      string    `json:"title"`
	Body       string    `json:"body,omitempty"`
	Recovery   bool      `json:"recovery,omitempty"`
	Suppressed string    `json:"suppressed,omitempty"`
}

// Log appends entries to a JSONL file. A nil *Log is a no-op, so callers can
// wire it unconditionally. Safe for concurrent use.
type Log struct {
	// Enabled gates every write. It is a per-instance predicate rather than a
	// package-level flag so this stays a plain, testable writer: a nil Enabled
	// means "always write", which is what a test constructing a Log directly
	// wants, while the app wires it to the user's diagnostics setting. Consulted
	// at WRITE time, so toggling that setting applies to the next alert instead
	// of the next launch.
	Enabled func() bool

	mu   sync.Mutex
	path string
}

// Open returns a Log writing to path, first pruning entries older than
// keepWindow (and any unparsable lines). Failures never block startup.
func Open(path string) *Log {
	prune(path, time.Now().Add(-keepWindow))
	return &Log{path: path}
}

// Record appends e (stamping Time if zero). Failures are logged, never fatal —
// an audit-trail problem must not affect alert delivery itself.
func (l *Log) Record(e Entry) {
	if l == nil || l.path == "" {
		return
	}
	if l.Enabled != nil && !l.Enabled() {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		slog.Warn("alertlog marshal failed", "err", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// 0600, matching config/history/applog: this journal records which providers
	// the user monitors and when each went down. On a shared POSIX machine 0644
	// handed that to every other local account for nothing in return.
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		slog.Warn("alertlog open failed", "path", l.path, "err", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		slog.Warn("alertlog write failed", "path", l.path, "err", err)
		return
	}
	// Enforce the size cap HERE, not only at Open: a tray monitor can run for
	// weeks, and a bound that is only checked at startup is not a bound during
	// the run that actually matters. The stat is one syscall per alert, and
	// alerts are rare.
	if fi, err := f.Stat(); err == nil && fi.Size() > maxBytes {
		f.Close()
		trimToSize(l.path, maxBytes)
	}
}

// trimToSize drops whole lines from the FRONT of the journal until it fits,
// keeping the newest. Oldest-first because this file answers "has this check
// alerted recently?" — the recent end is the end that answers it.
func trimToSize(path string, limit int64) {
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) <= limit {
		return
	}
	// Cut to the limit from the end, then advance to the next line boundary so
	// the file never starts with half a JSON object.
	cut := data[int64(len(data))-limit:]
	if i := bytes.IndexByte(cut, '\n'); i >= 0 {
		cut = cut[i+1:]
	}
	if err := atomicReplace(path, cut); err != nil {
		slog.Warn("alertlog trim failed", "path", path, "err", err)
	}
}

// prune rewrites path keeping only lines whose entry timestamp is at or after
// cutoff. A missing file or read error is left alone; the file is rewritten
// only when something was actually dropped.
func prune(path string, cutoff time.Time) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var kept []byte
	dropped := false
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) != nil || e.Time.Before(cutoff) {
			dropped = true
			continue
		}
		kept = append(kept, line...)
		kept = append(kept, '\n')
	}
	if !dropped {
		return
	}
	if err := atomicReplace(path, kept); err != nil {
		slog.Warn("alertlog prune failed", "path", path, "err", err)
	}
}

// atomicReplace rewrites path with data via write-then-rename, so a crash
// mid-rewrite can never destroy the trail — this is the one file meant to
// survive for months, and os.WriteFile truncates first.
//
// The temp file is fsync'd BEFORE the rename. Without it the claim above is only
// half true: the bytes sit in the page cache while a rename is a metadata
// operation that can reach disk first, so a power loss just after the rename
// could publish a zero-length or truncated alert-log — losing the entire 90-day
// trail in the name of trimming a few old lines from it. A unique temp name also
// keeps two processes from colliding on the same ".tmp" (main.go fails OPEN on a
// lock error, so two instances can coexist).
func atomicReplace(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename succeeds
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
