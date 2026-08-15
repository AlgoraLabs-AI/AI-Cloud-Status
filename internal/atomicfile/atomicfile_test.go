package atomicfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteCreatesAndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "data.json")

	if err := Write(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first" {
		t.Fatalf("got %q err %v, want %q", got, err, "first")
	}

	if err := Write(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("Write (replace): %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}
}

// TestConcurrentWritesAllSucceed is the reason this package exists.
//
// internal/config's own comments record field reports of "config save failed:
// Access is denied" and orphaned temp files: on Windows, two concurrent
// os.Rename calls onto the same destination fail with a sharing/access
// violation. internal/history had three goroutines calling an unprotected copy
// of the same routine — the 60s ticker, quit, and the watchdog's relaunch, which
// calls os.Exit immediately afterwards, so a lost rename silently discarded up
// to a day of samples.
func TestConcurrentWritesAllSucceed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	const writers, rounds = 8, 25

	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte('a' + w)}, 4096)
			for i := 0; i < rounds; i++ {
				if err := Write(path, payload, 0o600); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)

	failed := 0
	for err := range errs {
		if failed < 3 {
			t.Errorf("concurrent write failed: %v", err)
		}
		failed++
	}
	if failed > 3 {
		t.Errorf("... and %d more failures", failed-3)
	}

	// Whatever landed must be ONE writer's payload, never a mix — that is what
	// "atomic" buys.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4096 {
		t.Fatalf("final file is %d bytes, want 4096 — a write was interleaved", len(got))
	}
	if bytes.Count(got, got[:1]) != len(got) {
		t.Error("final file mixes bytes from more than one writer")
	}
}

// TestNoTempFilesSurvive: a crashed or failed write must not leave debris. The
// history file is multi-megabyte, so an orphan per crash is not a rounding error.
func TestNoTempFilesSurvive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	for i := 0; i < 5; i++ {
		if err := Write(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file survived a successful write: %s", e.Name())
		}
	}
}

// TestSweepStaleTempsRemovesOrphansOnly pins the cleanup's blast radius: it must
// clear leftovers for ITS path and touch nothing else in a directory it shares
// with the config, the history and the alert log.
func TestSweepStaleTempsRemovesOrphansOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")

	orphan := filepath.Join(dir, "history.json.tmp-123456")
	other := filepath.Join(dir, "config.json.tmp-999")
	real := filepath.Join(dir, "config.json")
	for _, f := range []string{orphan, other, real} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	SweepStaleTemps(path)

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("an orphaned temp for this path survived the sweep")
	}
	for _, f := range []string{other, real} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("the sweep removed an unrelated file: %s", filepath.Base(f))
		}
	}
}

// TestWriteLeavesTheOldFileIntactOnFailure: if the write cannot complete, the
// previous contents must still be there. That is the whole promise.
func TestWriteLeavesTheOldFileIntactOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := Write(path, []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A directory where the destination should be cannot be replaced by a rename.
	blocked := filepath.Join(dir, "blocked.json")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(blocked, []byte("never lands"), 0o600); err == nil {
		t.Error("writing over a directory should fail rather than report success")
	}

	got, _ := os.ReadFile(path)
	if string(got) != "good" {
		t.Errorf("unrelated file changed to %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("a failed write left debris: %s", e.Name())
		}
	}
}
