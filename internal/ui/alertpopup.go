package ui

import (
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
)

// Alert cards are borderless always-on-top windows parked at the bottom-right
// of the primary display (see placeAlertPopup), replacing the old raise-the-
// main-window-and-show-a-modal delivery. The size is FIXED so the stacking
// math stays trivial: slot i sits i card-heights above slot 0.
const (
	alertPopupWidth  float32 = 420
	alertPopupHeight float32 = 172

	// recoveryAutoClose is how long a good-news card stays before closing
	// itself. Outage cards are sticky: they wait for the user.
	recoveryAutoClose = time.Minute

	// maxAlertPopups caps how many cards can be on screen at once. Outage cards
	// are sticky, so an unattended broad incident would otherwise accumulate
	// unbounded always-on-top windows (each with a running animation) and stack
	// them unreadably once the work area fills. Beyond the cap the alert is
	// still journaled and reflected in the row/tray — only the card is skipped.
	maxAlertPopups = 5
)

// popupSeq numbers popup windows so each gets a unique OS window title for
// placeAlertPopup to find (splash windows have no visible title bar, but the
// Win32 window text is still set and searchable).
var popupSeq atomic.Int64

// nextFreeSlot returns the lowest stacking slot not currently occupied, so a
// new card fills the gap closest to the corner left by any closed one.
func nextFreeSlot(used map[int]bool) int {
	for i := 0; ; i++ {
		if !used[i] {
			return i
		}
	}
}

// showAlertPopup builds and shows one alert card: animated badge (red pulse
// for outages, the green recovery check for good news), bold title, wrapped
// body, and an action row — Open status page (when a URL is known), Silence 1h
// (when the alert belongs to a check), and Show (opens the main window). Must
// run on the Fyne thread. Headless drivers (tests) fall back to an OS toast.
func (c *Controller) showAlertPopup(id string, title, body, openURL string, recovery bool) {
	drv, ok := c.app.Driver().(desktop.Driver)
	if !ok {
		c.app.SendNotification(fyne.NewNotification(title, body))
		return
	}
	t := i18n.T()

	c.mu.Lock()
	if c.popupSlots == nil {
		c.popupSlots = map[int]bool{}
	}
	if len(c.popupSlots) >= maxAlertPopups {
		c.mu.Unlock()
		slog.Info("alert popup skipped: card cap reached", "id", id, "title", title)
		return
	}
	slot := nextFreeSlot(c.popupSlots)
	c.popupSlots[slot] = true
	c.mu.Unlock()

	win := drv.CreateSplashWindow()
	winTitle := fmt.Sprintf("%s-alert-%d", AppName, popupSeq.Add(1))
	win.SetTitle(winTitle)

	var badge fyne.CanvasObject
	var stopAnim func()
	if recovery {
		badge, stopAnim = newRecoveryBadge()
	} else {
		badge, stopAnim = newOutageBadge()
	}

	// closePopup is idempotent (the ✕, an action button, the recovery
	// auto-close timer, and the window's own close event can all fire); every
	// path runs on the Fyne thread.
	closed := false
	closePopup := func() {
		if closed {
			return
		}
		closed = true
		stopAnim()
		win.Close()
		c.mu.Lock()
		delete(c.popupSlots, slot)
		c.mu.Unlock()
	}
	// A card closed by the OS (e.g. Alt+F4 while focused) must release its slot
	// and stop its animation too — without this the slot leaks forever and the
	// repeat-forever animation keeps ticking against a dead canvas. The closed
	// guard makes the re-entry from closePopup's own win.Close() harmless.
	win.SetOnClosed(closePopup)

	titleLbl := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	titleLbl.Wrapping = fyne.TextWrapWord
	msg := widget.NewLabel(body)
	msg.Wrapping = fyne.TextWrapWord

	var actions []fyne.CanvasObject
	if openURL != "" {
		actions = append(actions, widget.NewButtonWithIcon(t.OpenStatusPage, theme.ComputerIcon(),
			func() { c.openURL(openURL) }))
	}
	if id != "" && !recovery {
		actions = append(actions, widget.NewButtonWithIcon(t.SilenceLabel+" "+t.Region1Hour, theme.VolumeMuteIcon(), func() {
			c.silenceCheck(id, time.Hour)
			closePopup()
		}))
	}
	// Show restores the main window AND opens the drill-down for the check this
	// card names, so the click lands on the incident instead of on the table with
	// the affected row somewhere in it. Order matters: the panel is a dialog over
	// the main window, so the window has to be up first.
	actions = append(actions, widget.NewButton(t.TrayShow, func() {
		closePopup()
		c.showFromTray()
		c.showDetailForAlert(id)
	}))

	closeBtn := widget.NewButtonWithIcon("", theme.CancelIcon(), closePopup)
	closeBtn.Importance = widget.LowImportance

	content := container.NewBorder(
		nil,
		container.NewHBox(actions...),
		badge,
		nil,
		container.NewVBox(
			container.NewBorder(nil, nil, nil, closeBtn, titleLbl),
			msg,
		),
	)
	win.SetContent(container.NewPadded(content))
	win.Resize(fyne.NewSize(alertPopupWidth, alertPopupHeight))
	win.Show()
	go placeAlertPopup(winTitle, slot)

	if recovery {
		time.AfterFunc(recoveryAutoClose, func() { fyne.Do(closePopup) })
	}
}
