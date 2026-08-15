//go:build !windows

package singleton

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive, non-blocking lock on f. See the Windows
// implementation for why this replaces PID-based liveness: the lock is held by
// the open file description and released by the kernel when the process dies,
// however it dies.
func lockFile(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil // another instance holds it
	}
	return false, err
}
