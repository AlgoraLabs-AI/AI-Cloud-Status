package singleton

import (
	"os"
	"path/filepath"
	"testing"
)

// These pin the property the old design could not provide: ownership of the
// lockfile is decided by a lock the KERNEL holds, not by the file existing and
// not by the PID written inside it.

// TestLiveHolderIsDetectedWithoutConsultingThePID is the B1 regression. The
// lockfile records a PID that is alive but belongs to something else entirely —
// the state a machine reaches whenever the app dies hard (power loss, End Task,
// or a runtime fatal that skips the deferred Release) and a reboot recycles low
// PIDs. The old code asked "is that PID alive?", got yes, and refused to launch
// forever, with no self-heal and no message naming the file to delete.
//
// alwaysAlive is passed deliberately: even a liveness oracle that says "yes" to
// everything must not be able to block a launch now.
func TestLiveHolderIsDetectedWithoutConsultingThePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	// PID 4 exists on every Windows box (System) and PID 1 on every Unix.
	if err := os.WriteFile(path, []byte("4\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	l, ok, err := acquire(path, os.Getpid(), alwaysAlive)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !ok {
		t.Fatal("a lockfile naming a live-but-unrelated PID blocked the launch; the app is unlaunchable until the file is deleted by hand")
	}
	_ = l.Release()
}

// TestEmptyLockfileIsNotTreatedAsStale is the B2 regression. The old code created
// the file with O_EXCL and wrote the PID a moment later, so a second launcher
// landing in that window read an empty file, classified it "stale (unreadable)"
// and DELETED a live holder's lock. Presence is not ownership now, so an empty
// file is simply a file.
func TestEmptyLockfileIsNotTreatedAsStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	l, ok, err := acquire(path, os.Getpid(), neverAlive)
	if err != nil || !ok {
		t.Fatalf("an empty lockfile should be claimable: ok=%v err=%v", ok, err)
	}
	defer l.Release()

	// And while we hold it, nobody else gets it — including a launcher that
	// arrives before we have written our PID.
	if _, ok2, err2 := acquire(path, os.Getpid()+1, neverAlive); err2 != nil || ok2 {
		t.Errorf("a second acquire succeeded against a held lock: ok=%v err=%v", ok2, err2)
	}
}

// TestHeldLockBlocksASecondAcquireInTheSameProcess is the core mutual-exclusion
// assertion, and it works cross-platform because both LockFileEx (per handle)
// and flock (per open file description) conflict between two separate opens
// even inside one process.
func TestHeldLockBlocksASecondAcquireInTheSameProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	first, ok, err := acquire(path, os.Getpid(), neverAlive)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}

	second, ok2, err2 := acquire(path, os.Getpid(), neverAlive)
	if err2 != nil {
		t.Fatalf("second acquire errored instead of reporting contention: %v", err2)
	}
	if ok2 {
		_ = second.Release()
		_ = first.Release()
		t.Fatal("two instances hold the lock at once")
	}

	// Releasing hands it over cleanly.
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	third, ok3, err3 := acquire(path, os.Getpid(), neverAlive)
	if err3 != nil || !ok3 {
		t.Fatalf("lock was not released: ok=%v err=%v", ok3, err3)
	}
	_ = third.Release()
}

// TestLockfileStaysReadableWhileHeld guards the package's inspectability claim.
// Windows byte-range locks block reads of the locked region, so locking byte 0
// would have made the PID unreadable to anyone — including readPID — which is
// exactly the diagnostic the file exists to provide.
func TestLockfileStaysReadableWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	l, ok, err := acquire(path, os.Getpid(), neverAlive)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	defer l.Release()

	pid, err := readPID(path)
	if err != nil {
		t.Fatalf("lockfile unreadable while held: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("lockfile PID = %d, want this process's %d", pid, os.Getpid())
	}
}
