//go:build windows

package singleton

import "golang.org/x/sys/windows"

// stillActive is the exit code Windows reports for a process that has not yet
// exited (STATUS_PENDING / STILL_ACTIVE = 259).
const stillActive = 259

// processAlive reports whether a process with the given PID is currently
// running. On Windows os.FindProcess always succeeds, so we open the process and
// check its exit code: a still-running process reports STILL_ACTIVE.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Most commonly ERROR_INVALID_PARAMETER: no such process.
		return false
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
