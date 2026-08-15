//go:build windows

package singleton

import "golang.org/x/sys/windows"

// winMutex holds an open handle to a named Windows mutex. The OS releases the
// named object once the last handle to it is closed, so closing on release lets
// a subsequent launch acquire it cleanly.
type winMutex struct {
	h windows.Handle
}

func (m *winMutex) release() error {
	if m == nil || m.h == 0 {
		return nil
	}
	err := windows.CloseHandle(m.h)
	m.h = 0
	return err
}

// acquireOSMutex creates the named mutex. The returns mirror the osMutex contract
// used by Acquire:
//
//	(mtx, true, nil)   — we created/own the mutex (no other instance via this path)
//	(nil, false, nil)  — the mutex already exists: another instance is running
//	(nil, true, err)   — creation failed (e.g. Global\ privilege denied); treated
//	                     as non-decisive so Acquire falls back to the lockfile
func acquireOSMutex(name string) (osMutex, bool, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, true, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if err != nil {
		// CreateMutex returns a valid handle AND ERROR_ALREADY_EXISTS when the
		// named object already exists; that means another instance owns it.
		if err == windows.ERROR_ALREADY_EXISTS {
			if h != 0 {
				_ = windows.CloseHandle(h)
			}
			return nil, false, nil
		}
		// Any other failure is non-decisive: let the lockfile be authoritative.
		return nil, true, err
	}
	return &winMutex{h: h}, true, nil
}
