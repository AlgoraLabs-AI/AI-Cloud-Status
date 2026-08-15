//go:build windows

package ui

import "testing"

func TestPopupOrigin(t *testing.T) {
	wa := rect{left: 0, top: 0, right: 1920, bottom: 1040} // 1080p minus taskbar
	const w, h = 420, 172

	x, y := popupOrigin(wa, w, h, 0)
	if x != 1920-w-popupMargin {
		t.Errorf("slot 0 x = %d, want flush right minus margin", x)
	}
	if y != 1040-h-popupMargin {
		t.Errorf("slot 0 y = %d, want flush bottom minus margin", y)
	}

	// Each slot moves one card-height (plus margin) up.
	_, y1 := popupOrigin(wa, w, h, 1)
	if y1 != y-(h+popupMargin) {
		t.Errorf("slot 1 y = %d, want %d", y1, y-(h+popupMargin))
	}

	// A slot past the top of the work area clamps instead of going off-screen.
	_, yTall := popupOrigin(wa, w, h, 50)
	if yTall != wa.top {
		t.Errorf("clamped y = %d, want work-area top %d", yTall, wa.top)
	}

	// Secondary-monitor work areas can have negative origins; the clamp must
	// respect them rather than assume 0.
	waNeg := rect{left: -1920, top: -200, right: 0, bottom: 880}
	_, yNeg := popupOrigin(waNeg, w, h, 50)
	if yNeg != waNeg.top {
		t.Errorf("negative-origin clamped y = %d, want %d", yNeg, waNeg.top)
	}
}
