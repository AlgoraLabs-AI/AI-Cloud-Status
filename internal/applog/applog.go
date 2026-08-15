// Package applog sets up file-based logging for AI-Cloud-Status so launch
// problems, panics, and runtime errors are recoverable from a log file instead
// of vanishing (the GUI build has no console). The log lives next to the config.
package applog

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// The log's share of the diagnostics disk budget. The whole diagnostics folder
// is designed to stay under ~50 MiB (log + alert trail + feed samples); this is
// the log's ~10 MiB of it.
//
// maxSize is deliberately not larger. The log's PURPOSE is to be attached to a
// bug report, and GitHub refuses attachments over 25 MB, so a log that cannot be
// attached has failed at the only job it has. 8 MiB of slog text is roughly a
// day of debug-level output at this app's cadence — long enough to contain the
// problem, small enough to send.
//
// keptGenerations rotated files are retained, GZIPPED. Structured log text
// compresses hard — 24:1 measured on a real 1 MiB acs.log — so three generations
// cost about 1 MiB and buy several days of history instead of the single
// uncompressed generation kept before. That matters because the interesting
// moment is usually BEFORE the user noticed anything was wrong.
//
// The whole log therefore sits at roughly 9 MiB: 8 live plus ~1 archived.
const (
	maxSize         = 8 << 20 // 8 MiB per file
	keptGenerations = 3       // acs.log.1.gz … acs.log.3.gz
)

// Path returns the log file path inside dir.
func Path(dir string) string { return filepath.Join(dir, "acs.log") }

// genPath returns the path of rotated generation n (1 = most recent). Retained
// generations are gzipped: the log's whole purpose is post-mortem, and rotating
// straight to /dev/null would routinely discard the run that crashed.
func genPath(dir string, n int) string {
	return filepath.Join(dir, fmt.Sprintf("acs.log.%d.gz", n))
}

// legacyOldPath is the uncompressed acs.log.1 written by builds before rotated
// logs were compressed. It is deleted on the next rotation rather than left to
// sit forever outside the budget this package now promises.
func legacyOldPath(dir string) string { return filepath.Join(dir, "acs.log.1") }

// level reports the log level: Info by default, Debug when ACS_DEBUG is set, so
// the shipped log stays lean but a verbose trace is one env var away.
func level() slog.Level {
	if os.Getenv("ACS_DEBUG") != "" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// rotatingFile enforces maxSize on every write rather than only at startup.
//
// The cap used to be checked once, in Setup. This is a system-tray app intended
// to run for weeks, so between two launches nothing bounded the file at all: a
// provider flapping at the 30s poll cadence, or ACS_DEBUG left on, grew acs.log
// without limit until the next restart — which might be never. The documented
// guarantee simply was not one.
type rotatingFile struct {
	mu   sync.Mutex
	f    *os.File
	dir  string
	size int64
}

func newRotatingFile(dir string) (*rotatingFile, error) {
	f, err := os.OpenFile(Path(dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	size := int64(0)
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	r := &rotatingFile{f: f, dir: dir, size: size}
	if size > maxSize {
		r.rotateLocked()
	}
	return r, nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > maxSize {
		r.rotateLocked()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotateLocked moves the current log aside and starts a fresh one. Any failure
// leaves the existing handle in place: losing rotation is a great deal better
// than losing logging.
//
// The rename to a plain ".rotating" name happens FIRST and compression second,
// so the window in which the app has no log file open is one rename long rather
// than one compression long.
//
// If compression then fails, the uncompressed ".rotating" file is LEFT ON DISK
// rather than discarded — a readable log beats a tidy directory. It is bounded
// (one file, at most maxSize), the next rotation renames over it, and it is
// counted and removed by the UI's diagnostics usage/delete, so leaving it costs
// nothing the budget has not already accounted for.
func (r *rotatingFile) rotateLocked() {
	old := r.f
	if err := old.Close(); err != nil {
		return
	}
	staging := filepath.Join(r.dir, "acs.log.rotating")
	if err := os.Rename(Path(r.dir), staging); err != nil {
		// Rename failed (Windows sharing violation, AV holding the file). Reopen
		// the original and keep going rather than losing the logger entirely.
		if f, rerr := os.OpenFile(Path(r.dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); rerr == nil {
			r.f = f
		}
		return
	}
	f, err := os.OpenFile(Path(r.dir), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		// Cannot reopen; put the old file back so logging survives.
		_ = os.Rename(staging, Path(r.dir))
		if back, rerr := os.OpenFile(Path(r.dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); rerr == nil {
			r.f = back
		}
		return
	}
	r.f, r.size = f, 0
	shiftGenerations(r.dir)
	compressInto(staging, genPath(r.dir, 1))
	_ = os.Remove(legacyOldPath(r.dir)) // uncompressed leftover from older builds
}

// shiftGenerations ages the retained archives by one (.2.gz → .3.gz, …) and
// drops whatever falls off the end, so the count is bounded by keptGenerations
// rather than by how long the app has been installed.
func shiftGenerations(dir string) {
	_ = os.Remove(genPath(dir, keptGenerations))
	for n := keptGenerations - 1; n >= 1; n-- {
		_ = os.Rename(genPath(dir, n), genPath(dir, n+1))
	}
}

// compressInto gzips src to dst and removes src. On any failure the source is
// left where a reader can still find it: the whole point of a rotated log is
// that somebody can read it later.
func compressInto(src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		_ = os.Remove(dst)
		return
	}
	if err := zw.Close(); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return
	}
	in.Close()
	_ = os.Remove(src)
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// Setup points the default slog logger at <dir>/acs.log (and stderr, so console
// builds still print). It returns the file to Close on exit. Any failure to open
// the file falls back to stderr-only logging and is reported via the returned
// error — it never blocks startup.
//
// Mode 0600, not 0644: a monitored URL can legitimately carry credentials in its
// userinfo, and this file is the one a user is most likely to attach to a bug
// report. Matching the config file's permissions is the least it should do.
func Setup(dir string) (io.Closer, error) {
	_ = os.MkdirAll(dir, 0o700)
	opts := &slog.HandlerOptions{Level: level()}
	f, err := newRotatingFile(dir)
	if err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, opts)))
		return io.NopCloser(nil), err
	}
	w := io.MultiWriter(f, os.Stderr)
	slog.SetDefault(slog.New(slog.NewTextHandler(w, opts)))
	return f, nil
}

// active is the log file opened by Enable, held so Disable can close it. Both
// run on the UI thread when the user toggles the setting, but the mutex is not
// optional: Disable closes a writer that background goroutines are logging
// through.
var active struct {
	mu sync.Mutex
	c  io.Closer
}

// Enable starts file logging, or does nothing if it is already on. It exists
// because the log is now a user-facing setting rather than a launch-time
// decision: someone hits a problem, ticks "Save diagnostic logs" to capture it,
// and must get a log of what happens NEXT — not after a restart they may not be
// able to reproduce the problem across.
func Enable(dir string) error {
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.c != nil {
		return nil
	}
	c, err := Setup(dir)
	if err != nil {
		return err
	}
	active.c = c
	return nil
}

// Disable stops file logging and closes the file, returning the default logger
// to stderr (which a -H=windowsgui build discards, which is the point). Safe to
// call when logging was never enabled.
func Disable() {
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.c == nil {
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level()})))
	_ = active.c.Close()
	active.c = nil
}
