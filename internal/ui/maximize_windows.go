//go:build windows

package ui

import (
	"syscall"
	"time"
	"unsafe"
)

// maximizeOnPrimary maximizes the window with the given title on the PRIMARY
// display. Fyne has no portable maximize API and GLFW may open the window on any
// monitor in a multi-monitor setup, so a bare SW_SHOWMAXIMIZED would maximize on
// whatever monitor the window happened to land on (with that monitor's DPI/scale).
// Instead we explicitly park the window inside the primary monitor's work area
// FIRST, then maximize it there — so it always starts maximized on the main
// screen with the main screen's scaling. It retries briefly because the window
// may not be realized yet right after ShowAndRun.
func maximizeOnPrimary(title string) {
	const (
		swRestore       = 9
		swShowMaximized = 3
		spiGetWorkArea  = 0x0030
		swpNoZorder     = 0x0004
		swpNoActivate   = 0x0010
	)
	user32 := syscall.NewLazyDLL("user32.dll")
	findWindow := user32.NewProc("FindWindowW")
	showWindow := user32.NewProc("ShowWindow")
	setPos := user32.NewProc("SetWindowPos")
	spi := user32.NewProc("SystemParametersInfoW")

	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}

	// Work area of the PRIMARY display (its bounds minus the taskbar). The primary
	// monitor's origin is (0,0), and SPI_GETWORKAREA reports exactly that monitor —
	// which is what "open on the main screen" means regardless of how many monitors
	// are attached.
	var wa rect
	haveWA := false
	if ret, _, _ := spi.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0); ret != 0 &&
		wa.right > wa.left && wa.bottom > wa.top {
		haveWA = true
	}

	for i := 0; i < 60; i++ {
		hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(titlePtr)))
		if hwnd != 0 {
			if haveWA {
				// Restore first so the move applies (a maximized window ignores
				// SetWindowPos), relocate+size it onto the primary work area, then
				// maximize — which now lands on the primary because that's where the
				// window sits.
				showWindow.Call(hwnd, swRestore)
				setPos.Call(hwnd, 0,
					uintptr(wa.left), uintptr(wa.top),
					uintptr(wa.right-wa.left), uintptr(wa.bottom-wa.top),
					swpNoZorder|swpNoActivate)
			}
			showWindow.Call(hwnd, swShowMaximized)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// rect mirrors the Win32 RECT for GetWindowRect.
type rect struct{ left, top, right, bottom int32 }
