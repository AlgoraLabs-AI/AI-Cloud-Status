//go:build !windows

package ui

import "syscall"

// detachedSysProcAttr puts the replacement instance in its own session so it is
// not torn down with the departing process's group. Setsid detaches it from our
// controlling terminal.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
