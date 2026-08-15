//go:build windows

package singleton

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive, non-blocking lock on the first byte of f.
//
// The lock belongs to the open handle, so Windows drops it when the process
// exits — including a hard kill, a power loss, or the runtime fatal that a
// deferred Release can never run through. That is the whole point: it replaces
// asking "is the PID in this file still alive?", a question with no correct
// answer after a reboot recycles the number.
// The locked byte sits at offset 2^32, far past any content this file will ever
// hold. Windows byte-range locks block READS of the locked region, and locking
// byte 0 made the PID unreadable to anyone else — including this package's own
// readPID — which defeats the point of writing a human-inspectable file. Locking
// past EOF is legal and gives the same mutual exclusion.
func lockFile(f *os.File) (bool, error) {
	ol := windows.Overlapped{OffsetHigh: 1}
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &ol,
	)
	if err == nil {
		return true, nil
	}
	// Someone else holds it — that is an answer, not a failure.
	if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING {
		return false, nil
	}
	return false, err
}
