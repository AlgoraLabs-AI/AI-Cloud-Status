package ui

import (
	"image/color"
	"math"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	recoveryBadgeSide = 84 // fixed footprint of the badge area
	recoveryIconSide  = 36 // green check icon in the middle
	recoveryPulsePer  = 1600 * time.Millisecond
)

// newRecoveryBadge returns an animated "service recovered" badge: a green
// check with two soft green rings rippling outward, like a radar ping calming
// down. The returned stop func must be called when the badge's container
// closes so the animation doesn't keep ticking.
func newRecoveryBadge() (fyne.CanvasObject, func()) {
	return newPulseBadge(theme.NewSuccessThemedResource(theme.ConfirmIcon()), theme.ColorNameSuccess)
}

// newOutageBadge is the bad-news counterpart of newRecoveryBadge: a red
// warning triangle with red rings pulsing outward.
func newOutageBadge() (fyne.CanvasObject, func()) {
	return newPulseBadge(theme.NewErrorThemedResource(theme.WarningIcon()), theme.ColorNameError)
}

// newPulseBadge builds the shared ringed-icon animation behind both badges.
func newPulseBadge(res fyne.Resource, colName fyne.ThemeColorName) (fyne.CanvasObject, func()) {
	icon := widget.NewIcon(res)
	icon.Resize(fyne.NewSize(recoveryIconSide, recoveryIconSide))
	icon.Move(fyne.NewPos((recoveryBadgeSide-recoveryIconSide)/2, (recoveryBadgeSide-recoveryIconSide)/2))

	ring1 := canvas.NewCircle(color.Transparent)
	ring2 := canvas.NewCircle(color.Transparent)
	for _, r := range []*canvas.Circle{ring1, ring2} {
		r.StrokeWidth = 3
	}

	inner := container.NewWithoutLayout(ring1, ring2, icon)
	holder := container.NewGridWrap(fyne.NewSize(recoveryBadgeSide, recoveryBadgeSide), inner)

	sr, sg, sb, _ := theme.Color(colName).RGBA()
	place := func(ring *canvas.Circle, p float32) {
		// p in [0,1): the ring grows from the icon out to the badge edge, fading.
		d := recoveryIconSide + p*(recoveryBadgeSide-recoveryIconSide)
		off := (recoveryBadgeSide - d) / 2
		ring.Resize(fyne.NewSize(d, d))
		ring.Move(fyne.NewPos(off, off))
		ring.StrokeColor = color.NRGBA{
			R: uint8(sr >> 8), G: uint8(sg >> 8), B: uint8(sb >> 8),
			A: uint8((1 - p) * 200),
		}
		ring.Refresh()
	}

	anim := fyne.NewAnimation(recoveryPulsePer, func(p float32) {
		place(ring1, p)
		place(ring2, float32(math.Mod(float64(p)+0.5, 1)))
	})
	anim.RepeatCount = fyne.AnimationRepeatForever
	anim.Start()

	return container.NewCenter(holder), anim.Stop
}
