package applog

import (
	"compress/gzip"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This package had no test file at all, which mattered more than it looks: an
// O_APPEND accidentally becoming O_TRUNC, or the size comparison flipping, would
// silently destroy the log on every launch — and the loss is only discovered the
// next time someone needs the log to diagnose a crash, which is exactly when it
// is gone.

// TestSetupPreservesAnExistingLog is the append guarantee. The log exists to
// survive across runs; a launch that wipes it defeats the purpose.
func TestSetupPreservesAnExistingLog(t *testing.T) {
	dir := t.TempDir()
	const prior = "earlier run said something important\n"
	if err := os.WriteFile(Path(dir), []byte(prior), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Setup(dir)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	slog.Info("new line")
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), prior) {
		t.Errorf("existing content was not preserved; log now starts %q", head(string(got), 60))
	}
	if !strings.Contains(string(got), "new line") {
		t.Error("the new line was not appended")
	}
}

// TestOversizedLogIsRotatedAtSetup pins the startup half of the cap, and that
// rotation KEEPS one generation — the run that crashed is usually the one you
// need, so rotating straight to nothing would be worse than not rotating.
func TestOversizedLogIsRotatedAtSetup(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", maxSize+1024)
	if err := os.WriteFile(Path(dir), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Setup(dir)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer c.Close()

	fi, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > maxSize {
		t.Errorf("log is still %d bytes after Setup, want it rotated below %d", fi.Size(), maxSize)
	}
	got, err := readGeneration(dir, 1)
	if err != nil {
		t.Fatalf("previous log was discarded rather than rotated: %v", err)
	}
	if len(got) != len(big) {
		t.Errorf("rotated log holds %d bytes, want the original %d", len(got), len(big))
	}
}

// readGeneration reads a rotated archive back through gzip, which is how anyone
// investigating a bug report will read it.
func readGeneration(dir string, n int) ([]byte, error) {
	f, err := os.Open(genPath(dir, n))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// TestLogRotatesDuringASingleRun is the regression for the real defect: the cap
// used to be checked only in Setup. This is a tray app meant to run for weeks,
// so between launches nothing bounded the file — a flapping provider at the 30s
// poll cadence, or ACS_DEBUG left on, grew it without limit until a restart that
// might never come.
func TestLogRotatesDuringASingleRun(t *testing.T) {
	dir := t.TempDir()
	r, err := newRotatingFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	chunk := []byte(strings.Repeat("y", 64*1024))
	for written := 0; written < 4*maxSize; written += len(chunk) {
		if _, err := r.Write(chunk); err != nil {
			t.Fatalf("write: %v", err)
		}
		fi, err := os.Stat(Path(dir))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > maxSize {
			t.Fatalf("log reached %d bytes mid-run, past the %d cap", fi.Size(), maxSize)
		}
	}
}

// TestSetupFallsBackWhenTheFileCannotBeOpened pins the never-block-startup
// contract: logging degrades to stderr and Setup still returns a Closer that is
// safe to call.
func TestSetupFallsBackWhenTheFileCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	// Make the log path a directory so opening it as a file must fail.
	if err := os.MkdirAll(Path(dir), 0o755); err != nil {
		t.Fatal(err)
	}

	c, err := Setup(dir)
	if err == nil {
		t.Error("Setup should report why file logging is unavailable")
	}
	if c == nil {
		t.Fatal("Setup returned a nil Closer; callers defer Close on it")
	}
	_ = c.Close() // must not panic
	slog.Info("still logging to stderr")
}

// TestLevelHonorsDebugEnv pins the one knob the log has.
func TestLevelHonorsDebugEnv(t *testing.T) {
	t.Setenv("ACS_DEBUG", "")
	if got := level(); got != slog.LevelInfo {
		t.Errorf("level() = %v without ACS_DEBUG, want Info", got)
	}
	t.Setenv("ACS_DEBUG", "1")
	if got := level(); got != slog.LevelDebug {
		t.Errorf("level() = %v with ACS_DEBUG set, want Debug", got)
	}
}

// TestPathsAreInsideDir keeps the two filenames from drifting apart.
func TestPathsAreInsideDir(t *testing.T) {
	dir := filepath.Join("some", "config", "dir")
	if got := Path(dir); filepath.Dir(got) != dir {
		t.Errorf("Path(%q) = %q, want it inside dir", dir, got)
	}
	if Path(dir) == genPath(dir, 1) {
		t.Error("the rotated log must not share the active log's name")
	}
	for n := 1; n <= keptGenerations; n++ {
		if filepath.Dir(genPath(dir, n)) != dir {
			t.Errorf("genPath(%d) = %q, want it inside dir", n, genPath(dir, n))
		}
	}
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestRotationKeepsABoundedNumberOfGenerations is the disk-budget guarantee for
// the log. Before this, one uncompressed generation was kept and nothing said
// what the ceiling was; the point of the change is that the ceiling is now
// stated and enforced — maxSize live plus keptGenerations compressed archives,
// and never a file more.
func TestRotationKeepsABoundedNumberOfGenerations(t *testing.T) {
	dir := t.TempDir()
	r, err := newRotatingFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Rotate more times than we retain, so the oldest must be evicted.
	chunk := strings.Repeat("x", 64<<10) + "\n"
	for round := 0; round < keptGenerations+3; round++ {
		for written := 0; written < maxSize+1; written += len(chunk) {
			if _, err := r.Write([]byte(chunk)); err != nil {
				t.Fatal(err)
			}
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	archives := 0
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += fi.Size()
		if strings.HasSuffix(e.Name(), ".gz") {
			archives++
		}
		if e.Name() == "acs.log.rotating" {
			t.Error("a staging file was left behind by rotation")
		}
	}
	if archives > keptGenerations {
		t.Errorf("kept %d archives, want at most %d", archives, keptGenerations)
	}
	// Live file + compressed archives. Slog text compresses hard, so the real
	// figure is far below this; the assertion is that a ceiling EXISTS.
	if ceiling := int64(maxSize) * int64(keptGenerations+2); total > ceiling {
		t.Errorf("log directory is %d bytes, want under the stated ceiling %d", total, ceiling)
	}
}

// TestRotatedGenerationsAreReadable: an archive nobody can read is the same as
// no archive. Every retained generation must gunzip cleanly.
func TestRotatedGenerationsAreReadable(t *testing.T) {
	dir := t.TempDir()
	r, err := newRotatingFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	chunk := strings.Repeat("y", 64<<10) + "\n"
	for written := 0; written < maxSize+1; written += len(chunk) {
		if _, err := r.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := readGeneration(dir, 1)
	if err != nil {
		t.Fatalf("generation 1 does not gunzip: %v", err)
	}
	if len(got) == 0 || !strings.HasPrefix(string(got), "y") {
		t.Errorf("generation 1 holds %d bytes starting %q, want the rotated content",
			len(got), head(string(got), 8))
	}
}
