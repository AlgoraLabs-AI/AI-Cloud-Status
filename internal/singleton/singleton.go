// Package singleton enforces that only one instance of the application runs at a
// time. It uses two complementary guards: a lockfile in the user-config
// directory (portable and inspectable) and, on Windows, a named mutex.
//
// Ownership of the lockfile is an OS-held lock on the open handle, not the
// file's existence and not the PID written inside it. The kernel releases that
// lock when the process dies however it dies, so a crash cannot lock the user
// out and a reboot recycling PIDs cannot make a dead instance look alive. The
// PID is still written, purely so someone reading the file by hand can see who
// holds it.
package singleton

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MutexName is the Windows named-mutex identity. It is unused on other platforms
// (the lockfile is the sole guard there).
//
// Local\, not Global\: this is a per-user tray app whose lockfile already lives
// in the user's own config directory. A machine-wide mutex made the two guards
// disagree about scope — on a terminal server or with fast user switching, user
// B could not launch at all because user A held it, while user B's lockfile sat
// unlocked in user B's profile. Local\ also avoids the SeCreateGlobalPrivilege
// failure that pushed the decision onto the lockfile fallback in the first place.
const MutexName = `Local\AICloudStatus`

// LockFileName is the lockfile's base name within the config directory.
const LockFileName = "instance.lock"

// Lock represents a held single-instance lock. Release it (typically via defer)
// before the process exits so a fresh launch is not treated as a duplicate.
type Lock struct {
	path  string
	f     *os.File
	osMtx osMutex // OS-level guard (named mutex on Windows; nil/no-op elsewhere)
}

// Acquire attempts to take the single-instance lock using the lockfile at path
// and, on Windows, the named mutex. It returns:
//
//	(lock, true, nil)  — this is the only instance; hold lock until exit
//	(nil, false, nil)  — another live instance already holds the lock
//	(nil, false, err)  — an I/O error prevented a decision
func Acquire(path string) (*Lock, bool, error) {
	return acquireNamed(path, MutexName)
}

// acquireNamed is Acquire with the named-mutex identity injected, mirroring the
// dependency seam the lockfile layer already has (acquire takes its own pid and
// liveness probe).
//
// It exists for the tests. Acquire's mutex name is a package constant, so a test
// exercising the full path collided with the REAL running app: this is a tray
// application with autostart on, so the developer's own instance is holding
// Local\AICloudStatus essentially always, and the suite went red on a machine
// where nothing was wrong. A test that fails because the product is running
// teaches people to ignore red.
func acquireNamed(path, mutexName string) (*Lock, bool, error) {
	// OS mutex first: on Windows a second instance fails here immediately even
	// before any file I/O. Elsewhere this is a no-op that always "succeeds".
	mtx, held, err := acquireOSMutex(mutexName)
	if err == nil && !held {
		return nil, false, nil
	}
	// err from the OS mutex is non-fatal: fall through to the lockfile, which is
	// the portable source of truth.

	l, ok, ferr := acquire(path, os.Getpid(), processAlive)
	if ferr != nil || !ok {
		if mtx != nil {
			_ = mtx.release()
		}
		return nil, ok, ferr
	}
	l.osMtx = mtx
	return l, true, nil
}

// Release relinquishes the lock: it closes and removes the lockfile and releases
// the OS mutex. It is safe to call on a nil *Lock.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	var firstErr error
	if l.f != nil {
		if err := l.f.Close(); err != nil {
			firstErr = err
		}
		l.f = nil
	}
	if l.path != "" {
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	if l.osMtx != nil {
		if err := l.osMtx.release(); err != nil && firstErr == nil {
			firstErr = err
		}
		l.osMtx = nil
	}
	return firstErr
}

// acquire is the portable, testable core. Ownership is decided by an OS-held
// lock on the open file handle, NOT by the file's existence and not by the PID
// written inside it. self is the current PID, recorded purely as a diagnostic
// for whoever reads the file by hand.
//
// The previous design asked "is the PID recorded here still alive?", which has
// no correct answer:
//
//   - After a hard exit (power loss, End Task, or a runtime fatal that skips the
//     deferred Release) the lockfile survives. A reboot then recycles low PIDs,
//     so the recorded number belongs to some unrelated process and reads as
//     alive — permanently. The app became unlaunchable with no self-heal and no
//     message naming the file to delete.
//   - The file was created by O_EXCL and the PID written a moment later, so a
//     second launcher landing in between read an empty file, classified it
//     "stale (unreadable)" and DELETED a live holder's lock.
//
// A kernel-held lock has neither problem: it is released when the process dies
// however it dies, it cannot be observed in a half-written state, and nobody
// ever removes a file they do not own.
func acquire(path string, self int, _ func(int) bool) (*Lock, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	// No O_EXCL: the file's PRESENCE is not ownership, the lock is. A leftover
	// file from a crashed run is simply reopened and re-locked.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	held, lerr := lockFile(f)
	if lerr != nil {
		_ = f.Close()
		return nil, false, lerr
	}
	if !held {
		_ = f.Close()
		return nil, false, nil // another live instance holds it
	}
	// Ours. Refresh the diagnostic PID; failing to do so is not worth losing a
	// lock we already hold.
	if err := f.Truncate(0); err == nil {
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", self)
			_ = f.Sync()
		}
	}
	return &Lock{path: path, f: f}, true, nil
}

// readPID reads and parses the PID recorded in a lockfile.
func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// osMutex is the OS-level single-instance guard abstraction. The Windows build
// provides a named-mutex implementation; other platforms use a no-op.
type osMutex interface {
	release() error
}
