//go:build !windows

package main

// notifyAlreadyRunning is a no-op on non-Windows platforms (the single-instance
// guard simply exits quietly there).
func notifyAlreadyRunning() {}
