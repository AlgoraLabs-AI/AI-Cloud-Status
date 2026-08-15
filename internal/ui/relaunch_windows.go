//go:build windows

package ui

import "syscall"

// detachedSysProcAttr makes the replacement instance independent of this
// process so it survives our os.Exit. DETACHED_PROCESS gives the GUI child no
// inherited console; CREATE_NEW_PROCESS_GROUP keeps it clear of any Ctrl+C/close
// signals aimed at the departing instance's group.
func detachedSysProcAttr() *syscall.SysProcAttr {
	const (
		detachedProcess       = 0x00000008
		createNewProcessGroup = 0x00000200
	)
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}
