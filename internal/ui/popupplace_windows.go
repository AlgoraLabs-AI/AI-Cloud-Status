//go:build windows

package ui

import (
	"syscall"
	"time"
	"unsafe"
)

// placeAlertPopup parks the borderless alert-card window named title at the
// bottom-right of the PRIMARY display's work area — slot card-heights up so
// concurrent cards stack — always on top and WITHOUT stealing focus (an alert
// must never yank the keyboard away mid-typing). Fyne cannot position windows,
// so this is the same Win32 route maximizeOnPrimary takes; it retries briefly
// because the window may not be realized yet right after Show.
func placeAlertPopup(title string, slot int) {
	const (
		spiGetWorkArea = 0x0030
		swpNoSize      = 0x0001
		swpNoActivate  = 0x0010
	)
	hwndTopmost := ^uintptr(0) // HWND_TOPMOST (-1)
	user32 := syscall.NewLazyDLL("user32.dll")
	findWindow := user32.NewProc("FindWindowW")
	getWindowRect := user32.NewProc("GetWindowRect")
	setPos := user32.NewProc("SetWindowPos")
	spi := user32.NewProc("SystemParametersInfoW")

	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}

	var wa rect
	if ret, _, _ := spi.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0); ret == 0 ||
		wa.right <= wa.left || wa.bottom <= wa.top {
		return
	}

	for range 60 {
		hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(titlePtr)))
		if hwnd == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		// The card's PHYSICAL size already includes the display's DPI scale, so
		// measuring the realized window (instead of trusting the logical Fyne
		// size) keeps the stacking correct on scaled displays.
		var r rect
		if ret, _, _ := getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ret == 0 {
			return
		}
		x, y := popupOrigin(wa, r.right-r.left, r.bottom-r.top, slot)
		setPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoActivate)
		return
	}
}

// popupMargin is the gap (physical pixels) between a card and the work-area
// edges, and between stacked cards.
const popupMargin = 12

// popupOrigin computes a card's top-left corner: bottom-right of the work area,
// slot card-heights up, clamped to the top edge so a very tall stack can never
// drift off-screen. w/h are the card's PHYSICAL size (DPI-scaled).
func popupOrigin(wa rect, w, h int32, slot int) (x, y int32) {
	x = wa.right - w - popupMargin
	y = max(wa.bottom-h-popupMargin-int32(slot)*(h+popupMargin), wa.top)
	return x, y
}
