// Package atomicfile writes a file so that a crash, a power loss, or a
// concurrent writer can never leave a truncated or half-written result at the
// destination.
//
// It exists because the same write had to be correct in two places and only one
// of them was. internal/config learned the hard way — its own comments record
// field reports of "config save failed: Access is denied" and orphaned temp
// files on Windows — and grew a process-wide mutex plus a rename retry.
// internal/history, which persists a multi-megabyte file from three different
// goroutines (the 60s ticker, quit, and the watchdog's relaunch, the last of
// which calls os.Exit immediately afterwards), had neither. Rather than copy the
// hard-won version into the second caller and let the two drift, both now share
// this one.
package atomicfile

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// writeMu serializes every atomic write in the process, across all paths.
//
// Process-wide rather than per-path on purpose: the Windows failures this guards
// against come from the OS and other agents (antivirus, the Search indexer, file
// sync) reacting to activity in the directory, not only from two writers racing
// on one name. Saves are infrequent and small; the contention does not matter.
var writeMu sync.Mutex

// tempPattern is the temp-file name pattern for path's directory.
func tempPattern(path string) string { return filepath.Base(path) + ".tmp-*" }

// Write replaces path with data atomically: the bytes go to a temp file in the
// same directory, are fsync'd, and only then renamed over the destination.
//
// The fsync is not optional. Without it the data sits in the page cache while
// the rename — a metadata operation — can reach disk first, so a power loss just
// after Write returns can publish a zero-length or partial file. That turns
// "atomic" into "atomic against a crash mid-write, but not against a crash just
// after", which is not what any caller assumes.
func Write(path string, data []byte, perm os.FileMode) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// We hold writeMu, so no other legitimate write is mid-flight: any surviving
	// temp for this path is a leftover from a past failed rename or a process
	// that died between CreateTemp and its cleanup. Clear it so they cannot
	// accumulate — a crashed history save leaves a multi-megabyte orphan.
	SweepStaleTemps(path)

	tmp, err := os.CreateTemp(dir, tempPattern(path))
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil && !os.IsPermission(err) {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameWithRetry(tmpName, path); err != nil {
		return fmt.Errorf("atomic replace %s: %w", filepath.Base(path), err)
	}
	return nil
}

// renameWithRetry performs the atomic replace, retrying on the transient
// failures Windows raises when an antivirus, the Search indexer, a file-sync
// agent, or a concurrent reader holds the destination (or the temp) open for a
// moment: ERROR_ACCESS_DENIED (5) and ERROR_SHARING_VIOLATION (32) on the
// MoveFileEx replace. Those holders release within tens to a few hundred
// milliseconds, so the delay backs off exponentially (10, 20, 40 … ms) up to a
// bounded total of roughly a second before giving up — long enough to clear a
// real scan window, short enough to never stall a user action. A permanent error
// (e.g. a read-only file) still fails after the budget rather than being masked.
func renameWithRetry(oldPath, newPath string) error {
	const attempts = 8 // 10+20+40+80+160+320+640+1280 capped → ~1.3s worst case
	delay := 10 * time.Millisecond
	var err error
	for i := range attempts {
		if err = os.Rename(oldPath, newPath); err == nil {
			if i > 0 {
				slog.Debug("atomic rename succeeded after retry", "path", newPath, "attempt", i+1)
			}
			return nil
		}
		if i == attempts-1 {
			break // don't sleep after the final attempt
		}
		time.Sleep(delay)
		if delay < 250*time.Millisecond {
			delay *= 2
		}
	}
	return err
}

// SweepStaleTemps removes orphaned temp files left beside path by past failed
// renames or by a process that died mid-write. Exported so a caller can sweep at
// startup, before its first save.
//
// Safe only because Write holds writeMu across the whole create-write-rename, so
// a temp file visible here is never one this process is actively using. That
// reasoning is process-local: it does not protect against a second instance,
// which is what internal/singleton is for.
func SweepStaleTemps(path string) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), tempPattern(path)))
	if err != nil {
		return
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
			slog.Debug("could not remove stale temp file", "path", m, "err", err)
		}
	}
}
