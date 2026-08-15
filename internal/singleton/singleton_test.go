package singleton

import (
	"os"
	"path/filepath"
	"testing"
)

// alwaysAlive / neverAlive are injectable liveness stubs for the lock core.
func alwaysAlive(int) bool { return true }
func neverAlive(int) bool  { return false }

func TestAcquireFreshLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "instance.lock")
	l, ok, err := acquire(path, 1234, neverAlive)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !ok {
		t.Fatal("fresh lock should be acquired")
	}
	defer l.Release()

	pid, err := readPID(path)
	if err != nil {
		t.Fatalf("readPID: %v", err)
	}
	if pid != 1234 {
		t.Errorf("lockfile PID = %d, want 1234", pid)
	}
}

func TestSecondInstanceBlockedWhileHolderAlive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	// First instance (PID 1) acquires and is reported alive.
	l1, ok, err := acquire(path, 1, alwaysAlive)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	defer l1.Release()

	// Second instance (PID 2) must be refused because PID 1 is alive.
	l2, ok, err := acquire(path, 2, alwaysAlive)
	if err != nil {
		t.Fatalf("second acquire error: %v", err)
	}
	if ok {
		l2.Release()
		t.Fatal("second instance should be blocked while the holder is alive")
	}
}

func TestStaleLockReclaimed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	// Simulate a crashed previous instance: a lockfile naming a dead PID.
	if err := os.WriteFile(path, []byte("9999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, ok, err := acquire(path, 4242, neverAlive)
	if err != nil || !ok {
		t.Fatalf("stale lock should be reclaimed: ok=%v err=%v", ok, err)
	}
	defer l.Release()
	pid, _ := readPID(path)
	if pid != 4242 {
		t.Errorf("reclaimed lockfile PID = %d, want 4242", pid)
	}
}

func TestCorruptLockReclaimed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Even though the (injected) liveness check would say alive, an unparseable
	// PID is treated as stale and reclaimed.
	l, ok, err := acquire(path, 7, alwaysAlive)
	if err != nil || !ok {
		t.Fatalf("corrupt lock should be reclaimed: ok=%v err=%v", ok, err)
	}
	defer l.Release()
}

func TestReleaseRemovesLockfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	l, ok, err := acquire(path, 1, neverAlive)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lockfile should be gone after Release, stat err = %v", err)
	}
	// Release on a nil lock is safe.
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Errorf("nil Release should be a no-op, got %v", err)
	}
}

// TestAcquireAfterReleaseSucceeds confirms a fresh launch can re-acquire once
// the previous holder released, exercising the full Acquire path (including the
// OS mutex on Windows).
//
// Both the lockfile path and the mutex NAME are test-local. The path alone was
// not enough: Acquire uses the package-constant MutexName, so this test failed
// against the developer's own running tray instance — red for the one reason
// that is not a defect.
func TestAcquireAfterReleaseSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	name := `Local\AICloudStatus-test-AcquireAfterRelease`

	l1, ok, err := acquireNamed(path, name)
	if err != nil || !ok {
		t.Fatalf("first Acquire: ok=%v err=%v", ok, err)
	}
	if err := l1.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	l2, ok, err := acquireNamed(path, name)
	if err != nil || !ok {
		t.Fatalf("re-Acquire after release should succeed: ok=%v err=%v", ok, err)
	}
	l2.Release()
}

// TestAcquireRejectsASecondInstance is the guard the test above cannot give: a
// second Acquire while the first is STILL HELD must be refused. Same isolated
// name and path, so it pins the mechanism rather than the ambient machine.
func TestAcquireRejectsASecondInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	name := `Local\AICloudStatus-test-RejectsSecond`

	l1, ok, err := acquireNamed(path, name)
	if err != nil || !ok {
		t.Fatalf("first Acquire: ok=%v err=%v", ok, err)
	}
	defer l1.Release()

	l2, ok, err := acquireNamed(path, name)
	if ok || err != nil {
		if l2 != nil {
			l2.Release()
		}
		t.Fatalf("second Acquire while held: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}
