package ui

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// splashDuration is how long the startup splash is shown; it matches the
// startup poll delay so checks begin right as the main window appears.
const splashDuration = startupDelay

// uiLang returns the configured UI language ("en" or "es"); defaults to English.
func (c *Controller) uiLang() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.Language == "es" {
		return "es"
	}
	return "en"
}

// showSplash displays a borderless splash window (logo + a random playful
// loading line) and returns it so the caller can Close it. It returns nil when
// the driver has no splash support (e.g. headless test runs), in which case the
// caller should just show the main window directly.
func (c *Controller) showSplash() fyne.Window {
	drv, ok := c.app.Driver().(desktop.Driver)
	if !ok {
		return nil
	}
	sp := drv.CreateSplashWindow()

	logo := canvas.NewImageFromResource(appIcon)
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(132, 132))

	name := widget.NewLabelWithStyle(AppName, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	line := widget.NewLabelWithStyle(randomLoadingPhrase(c.uiLang()), fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
	line.Wrapping = fyne.TextWrapWord
	bar := widget.NewProgressBarInfinite()

	body := container.NewPadded(container.NewVBox(
		container.NewCenter(logo),
		name,
		line,
		bar,
	))
	sp.SetContent(body)
	sp.Resize(fyne.NewSize(460, 340))
	sp.CenterOnScreen()
	sp.Show()
	return sp
}

// runWithSplash shows the splash for splashDuration, then reveals and maximizes
// the main window and starts first-run guidance, blocking on the Fyne loop. It
// falls back to showing the main window immediately when there is no splash.
func (c *Controller) runWithSplash(w fyne.Window) {
	splash := c.showSplash()
	if splash == nil {
		c.maybeShowFirstRun()
		go maximizeOnPrimary(windowTitle())
		w.ShowAndRun()
		return
	}
	go func() {
		time.Sleep(splashDuration)
		fyne.Do(func() {
			splash.Close()
			w.Show()
			c.maybeShowFirstRun()
			go maximizeOnPrimary(windowTitle())
		})
	}()
	c.app.Run()
}
