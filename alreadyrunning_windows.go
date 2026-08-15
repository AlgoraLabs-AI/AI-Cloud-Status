//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// notifyAlreadyRunning shows a native Windows message box telling the user that
// another copy is already running (the new instance then exits). Used instead of
// exiting silently so a double-launch is not confusing.
func notifyAlreadyRunning() {
	const (
		mbOK              = 0x00000000
		mbIconInformation = 0x00000040
		mbSetForeground   = 0x00010000
	)
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")

	title, _ := syscall.UTF16PtrFromString("AI-Cloud-Status")
	text, _ := syscall.UTF16PtrFromString(
		"AI-Cloud-Status is already running.\n\nLook for its icon in the system tray (near the clock).")
	messageBox.Call(0,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		mbOK|mbIconInformation|mbSetForeground)
}
