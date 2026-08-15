//go:build !windows

package singleton

import "syscall"

// processAlive reports whether a process with the given PID exists. Signal 0
// performs the kernel's permission/existence check without delivering a signal;
// EPERM means the process exists but is owned by another user (still "alive").
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
