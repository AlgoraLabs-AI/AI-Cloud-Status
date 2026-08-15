package ui

import (
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// TestToggleHighlight covers the Enable-all/Disable-all active-state logic: all
// on → Enable active; all off → Disable active; mixed or empty → neither (so a
// hand-edit clears the highlight).
func TestToggleHighlight(t *testing.T) {
	test.NewApp()
	defer test.NewApp()

	mk := func(states ...bool) []*widget.Check {
		out := make([]*widget.Check, len(states))
		for i, on := range states {
			c := widget.NewCheck("", nil)
			c.SetChecked(on)
			out[i] = c
		}
		return out
	}

	cases := []struct {
		name            string
		checks          []*widget.Check
		wantEn, wantDis bool
	}{
		{"all on", mk(true, true, true), true, false},
		{"all off", mk(false, false, false), false, true},
		{"mixed clears both", mk(true, false, true), false, false},
		{"empty clears both", mk(), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			en, dis := toggleHighlight(tc.checks)
			if en != tc.wantEn || dis != tc.wantDis {
				t.Fatalf("toggleHighlight = (en=%v dis=%v), want (en=%v dis=%v)", en, dis, tc.wantEn, tc.wantDis)
			}
		})
	}
}
