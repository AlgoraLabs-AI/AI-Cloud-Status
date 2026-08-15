//go:build windows

package ui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// The dead-canvas watchdog covers the half of the blank-window problem space
// that runUIWatchdog deliberately leaves alone: a LIVE Fyne event loop whose
// OpenGL canvas has stopped presenting. Observed 2026-07-24 on Intel UHD 620 +
// DisplayLink virtual displays: after session lock/unlock cycles the window
// paints pure white (nothing is presented, not even the clear color) while the
// heartbeat, backend polling and the OS-drawn title bar all stay healthy — so
// no existing watchdog fires and the user stares at a white window.
//
// Detection reads the window's own on-screen pixels (a sparse grid across the
// client area) and calls the canvas dead when EVERY sampled pixel is the same
// color on consecutive probes. A real ACS frame — header, status table, uptime
// strip — is never one flat color in any theme, so uniformity is
// theme-independent evidence that nothing was drawn.
//
// False-positive shields, all of which must hold before a relaunch:
//   - the window is visible (not hidden to tray) AND is the foreground window,
//     so the sampled pixels are ours and a user is actually looking at it;
//   - the startup grace has passed (the splash is sparse enough to sample
//     near-uniform);
//   - the same uniform verdict on deadCanvasConfirmProbes consecutive probes;
//   - the shared auto-relaunch budget in autoRelaunch caps runaway loops.
const (
	// deadCanvasProbeEvery is how often the canvas is pixel-probed while the
	// window is visible and focused. Cheap: ~35 GetPixel calls per probe.
	deadCanvasProbeEvery = 5 * time.Second
	// deadCanvasConfirmProbes is how many consecutive uniform probes are needed
	// before the canvas is judged dead (worst-case ~15s of white before the app
	// heals itself).
	deadCanvasConfirmProbes = 2
	// deadCanvasGridCols/Rows define the sampling grid spread across the client
	// area (with margins, so window chrome/edges are never sampled).
	deadCanvasGridCols = 7
	deadCanvasGridRows = 5
	// deadCanvasMinDim skips probing tiny windows, where a grid this sparse
	// could legitimately land on one flat region.
	deadCanvasMinDim = 200
)

var (
	dcUser32               = syscall.NewLazyDLL("user32.dll")
	dcGdi32                = syscall.NewLazyDLL("gdi32.dll")
	procDcFindWindowW      = dcUser32.NewProc("FindWindowW")
	procDcGetWindowThreadP = dcUser32.NewProc("GetWindowThreadProcessId")
	procDcGetForegroundWnd = dcUser32.NewProc("GetForegroundWindow")
	procDcGetClientRect    = dcUser32.NewProc("GetClientRect")
	procDcClientToScreen   = dcUser32.NewProc("ClientToScreen")
	procDcGetDC            = dcUser32.NewProc("GetDC")
	procDcReleaseDC        = dcUser32.NewProc("ReleaseDC")
	procDcGetPixel         = dcGdi32.NewProc("GetPixel")
)

// runDeadCanvasWatchdog probes the visible window for the dead-GL-surface
// state and auto-relaunches (shared budget) when it is confirmed.
func (c *Controller) runDeadCanvasWatchdog(ctx context.Context) {
	defer logPanic("runDeadCanvasWatchdog")
	consecutive := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(deadCanvasProbeEvery):
		}
		if time.Since(c.startedAt) < watchdogStartupGrace {
			continue
		}
		c.mu.Lock()
		hidden := c.windowHidden
		c.mu.Unlock()
		if hidden {
			consecutive = 0
			continue
		}
		hwnd := findOwnMainWindow()
		if hwnd == 0 || !isForegroundWindow(hwnd) {
			consecutive = 0
			continue
		}
		clr, uniform, ok := sampleCanvasUniform(hwnd)
		if !ok || !uniform {
			consecutive = 0
			continue
		}
		consecutive++
		slog.Warn("dead-canvas probe: entire client area is one flat color",
			"colorref", fmt.Sprintf("0x%06x", clr), "consecutive", consecutive)
		if consecutive >= deadCanvasConfirmProbes {
			if !c.autoRelaunch("dead-canvas watchdog: GL canvas stopped presenting (uniform frame, event loop alive)",
				"colorref", fmt.Sprintf("0x%06x", clr)) {
				return // budget exhausted: a deliberate, permanent decision
			}
			// The relaunch failed (exe locked, path unreachable). Keep probing —
			// returning here left the window white for the rest of the process
			// lifetime with the recovery budget never spent. Reset the streak so
			// the next attempt needs fresh confirmation.
			consecutive = 0
		}
	}
}

// findOwnMainWindow resolves the main window's HWND by its title, accepting it
// only when it belongs to this process. Returns 0 when not found (probe is
// skipped — fail safe).
func findOwnMainWindow() uintptr {
	titlePtr, err := syscall.UTF16PtrFromString(windowTitle())
	if err != nil {
		return 0
	}
	hwnd, _, _ := procDcFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	procDcGetWindowThreadP.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if int(pid) != os.Getpid() {
		return 0
	}
	return hwnd
}

// isForegroundWindow reports whether hwnd currently has the foreground — the
// shield that guarantees the sampled pixels belong to our window, not to
// whatever overlaps it.
func isForegroundWindow(hwnd uintptr) bool {
	fg, _, _ := procDcGetForegroundWnd.Call()
	return fg == hwnd
}

// dcPoint mirrors the Win32 POINT for ClientToScreen.
type dcPoint struct{ x, y int32 }

// sampleCanvasUniform reads a sparse pixel grid over hwnd's client area from
// the screen. It returns the first color (COLORREF 0x00BBGGRR), whether every
// sampled pixel matched it, and ok=false when the probe could not run (tiny
// window, API failure, invalid pixel) — never a verdict on partial data.
func sampleCanvasUniform(hwnd uintptr) (uint32, bool, bool) {
	const clrInvalid = 0xFFFFFFFF
	var cr rect
	if ret, _, _ := procDcGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr))); ret == 0 {
		return 0, false, false
	}
	w, h := cr.right, cr.bottom // client rect origin is always (0,0)
	if w < deadCanvasMinDim || h < deadCanvasMinDim {
		return 0, false, false
	}
	var origin dcPoint
	if ret, _, _ := procDcClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&origin))); ret == 0 {
		return 0, false, false
	}
	screenDC, _, _ := procDcGetDC.Call(0)
	if screenDC == 0 {
		return 0, false, false
	}
	defer procDcReleaseDC.Call(0, screenDC)

	var first uint32
	got := false
	for _, fy := range gridOffsets(deadCanvasGridRows) {
		for _, fx := range gridOffsets(deadCanvasGridCols) {
			x := uintptr(int64(origin.x) + int64(w)*int64(fx)/100)
			y := uintptr(int64(origin.y) + int64(h)*int64(fy)/100)
			px, _, _ := procDcGetPixel.Call(screenDC, x, y)
			if uint32(px) == clrInvalid {
				return 0, false, false
			}
			if !got {
				first = uint32(px)
				got = true
			} else if uint32(px) != first {
				return first, false, true
			}
		}
	}
	return first, true, true
}

// gridOffsets spreads n sample positions evenly across 8%..92% of an axis, so
// probes stay well inside the client area.
func gridOffsets(n int) []int32 {
	out := make([]int32, n)
	for i := range out {
		out[i] = int32(8 + 84*i/(n-1))
	}
	return out
}
