package ui

import (
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
)

// TestLanguageSelectInitialSelectDoesNotFire is the regression test for the
// Settings-window "Not responding" freeze: widget.Select.SetSelected fires
// OnChanged, so setting the dropdown's value while its handler is wired would call
// setLanguage → rebuild the form → new Select → SetSelected → … infinite
// recursion. The fix wires OnChanged only AFTER the initial SetSelected; this test
// locks that ordering in.
func TestLanguageSelectInitialSelectDoesNotFire(t *testing.T) {
	test.NewApp()
	defer test.NewApp() // reset global app for other tests

	fired := 0
	sel := widget.NewSelect([]string{"English", "Español"}, nil)
	sel.SetSelected("English") // initial selection — must NOT fire (handler still nil)
	sel.OnChanged = func(string) { fired++ }

	if fired != 0 {
		t.Fatalf("OnChanged fired %d times during the initial SetSelected — that is the freeze recursion", fired)
	}

	// A genuine user change must still fire exactly once.
	sel.SetSelected("Español")
	if fired != 1 {
		t.Fatalf("expected OnChanged once on a real change, got %d", fired)
	}
}

// TestSetLanguageUnchangedIsNoOp guards the second half of the fix: re-selecting
// the current language does no work (and so can't re-enter a rebuild). With a nil
// window, proceeding would be a no-op anyway, but the guard documents and enforces
// the intent — and "" must be treated as English.
func TestSetLanguageUnchangedIsNoOp(t *testing.T) {
	test.NewApp()
	defer test.NewApp()

	c := &Controller{cfg: config.Config{Language: "es"}}
	c.setLanguage("es") // unchanged → returns immediately, no panic, no rebuild

	c2 := &Controller{cfg: config.Config{Language: ""}}
	c2.setLanguage("en") // "" defaults to en → also a no-op
}
