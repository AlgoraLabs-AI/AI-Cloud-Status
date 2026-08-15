//go:build !windows

package singleton

// acquireOSMutex is a no-op on non-Windows platforms: the PID lockfile is the
// sole single-instance guard there. Returning held=true means "no contention
// from this guard", so Acquire proceeds to the lockfile.
func acquireOSMutex(string) (osMutex, bool, error) {
	return nil, true, nil
}
