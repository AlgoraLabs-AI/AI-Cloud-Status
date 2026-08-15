package ui

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/autostart"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
)

// showSettings opens the Settings DIALOG inside the main window, holding the
// poll interval, regions of interest, notification/DND preferences, reduced
// motion, and the start-on-login toggle. Settings used to be its own OS window;
// on multi-monitor setups with differing DPI it spawned with one monitor's
// scale and was then Win32-moved over the main window WITHOUT Fyne rescaling —
// rendering every string comically oversized. An in-window dialog (like the
// incident detail panel) always uses the main window's scale, and needs no
// native centering hack at all.
func (c *Controller) showSettings() {
	if c.window == nil || c.settingsDlg != nil {
		return // no window yet, or already open
	}
	c.openSettingsDialog()
}

// openSettingsDialog builds and shows the settings dialog. Split from
// showSettings so a language change can rebuild it in place.
func (c *Controller) openSettingsDialog() {
	t := i18n.T()
	form := c.buildSettingsForm()
	scroll := container.NewVScroll(form)
	// Natural height up to a cap so the dialog never outgrows the window; the
	// scroll takes over beyond that. A minimum width keeps the fields readable.
	h := form.MinSize().Height
	if h > 620 {
		h = 620
	}
	scroll.SetMinSize(fyne.NewSize(560, h))
	dlg := dialog.NewCustom(t.Settings, t.Close, scroll, c.window)
	c.settingsDlg = dlg
	dlg.SetOnClosed(func() { c.settingsDlg = nil })
	dlg.Show()
}

// buildSettingsForm assembles the settings controls.
func (c *Controller) buildSettingsForm() fyne.CanvasObject {
	t := i18n.T()
	box := container.NewVBox()

	// --- Language ---
	box.Add(sectionHeader(t.SecLanguage))
	langs := i18n.Languages()
	names := make([]string, len(langs))
	current := ""
	cur := c.cfgSnapshot().Language
	if cur == "" {
		cur = "en"
	}
	for i, l := range langs {
		names[i] = l.Name
		if l.Code == cur {
			current = l.Name
		}
	}
	langSel := widget.NewSelect(names, nil)
	langSel.SetSelected(current) // set BEFORE wiring OnChanged: SetSelected fires the
	// callback, and a callback here would rebuild this very form (SetContent) and
	// recurse infinitely. Assign the handler only after the initial selection.
	langSel.OnChanged = func(name string) {
		for _, l := range langs {
			if l.Name == name {
				c.setLanguage(l.Code)
				return
			}
		}
	}
	box.Add(langSel)

	// --- Poll interval ---
	box.Add(widget.NewSeparator())
	box.Add(sectionHeader(t.SecPolling))
	intervalEntry := widget.NewEntry()
	intervalEntry.SetText(strconv.Itoa(c.currentInterval()))
	applyInterval := widget.NewButton(t.Apply, func() {
		v, err := strconv.Atoi(strings.TrimSpace(intervalEntry.Text))
		if err != nil || v < 5 {
			intervalEntry.SetText(strconv.Itoa(c.currentInterval()))
			return
		}
		c.setInterval(v)
	})
	box.Add(container.NewBorder(nil, nil, widget.NewLabel(t.CheckInterval), applyInterval, intervalEntry))

	// Connectivity (ping) interval — a separate, faster knob from the status poll.
	connEntry := widget.NewEntry()
	connEntry.SetText(strconv.Itoa(c.connIntervalSeconds()))
	applyConn := widget.NewButton(t.Apply, func() {
		v, err := strconv.Atoi(strings.TrimSpace(connEntry.Text))
		if err != nil || v < connMinIntervalSeconds {
			connEntry.SetText(strconv.Itoa(c.connIntervalSeconds()))
			return
		}
		c.setConnInterval(v)
		connEntry.SetText(strconv.Itoa(c.connIntervalSeconds()))
	})
	box.Add(container.NewBorder(nil, nil, widget.NewLabel(t.ConnCheckInterval), applyConn, connEntry))

	// --- Regions of interest ---
	box.Add(widget.NewSeparator())
	box.Add(sectionHeader(t.SecRegions))
	box.Add(newWrappedLabel(t.RegionsHint))
	c.regionList = container.NewVBox()
	c.populateRegionList()
	box.Add(c.regionList)
	regionEntry := widget.NewEntry()
	regionEntry.SetPlaceHolder(t.RegionExample)
	addRegion := func() {
		r := regionEntry.Text
		regionEntry.SetText("")
		c.addRegion(r)
	}
	regionEntry.OnSubmitted = func(string) { addRegion() }
	box.Add(container.NewBorder(nil, nil, nil, widget.NewButton(t.Add, addRegion), regionEntry))

	// --- Notifications ---
	box.Add(widget.NewSeparator())
	box.Add(sectionHeader(t.SecNotifications))

	notifChk := c.blurOnToggle(widget.NewCheck(t.EnableNotifications, func(b bool) {
		c.updateCfg(func(cfg *config.Config) { cfg.Notifications = b })
	}))
	notifChk.SetChecked(c.cfgSnapshot().Notifications)
	box.Add(notifChk)

	dndChk := c.blurOnToggle(widget.NewCheck(t.DoNotDisturb, func(b bool) {
		c.updateCfg(func(cfg *config.Config) { cfg.DoNotDisturb = b })
	}))
	dndChk.SetChecked(c.cfgSnapshot().DoNotDisturb)
	box.Add(dndChk)

	// --- Accessibility ---
	box.Add(widget.NewSeparator())
	box.Add(sectionHeader(t.SecAccessibility))
	motionChk := c.blurOnToggle(widget.NewCheck(t.ReduceMotion, func(b bool) {
		setReducedMotionPref(b)
		c.updateCfg(func(cfg *config.Config) { cfg.ReducedMotion = b })
	}))
	motionChk.SetChecked(c.cfgSnapshot().ReducedMotion)
	box.Add(motionChk)

	// --- Startup ---
	box.Add(widget.NewSeparator())
	box.Add(sectionHeader(t.SecStartup))
	if autostart.Supported() {
		loginChk := c.blurOnToggle(widget.NewCheck(fmt.Sprintf(t.StartOnLogin, AppName), func(b bool) {
			if err := autostart.Apply(b); err != nil {
				// Reflect the actual on-disk state if the OS write failed.
				if en, e2 := autostart.Enabled(); e2 == nil {
					b = en
				}
			}
			c.updateCfg(func(cfg *config.Config) { cfg.StartOnLogin = b })
		}))
		loginChk.SetChecked(c.cfgSnapshot().StartOnLogin)
		box.Add(loginChk)
	} else {
		box.Add(newWrappedLabel(t.StartOnLoginUnavailable))
	}

	// --- Diagnostics ---
	//
	// This is here, in plain Settings, rather than behind an environment variable,
	// because the person who needs it is whoever just hit a bug — not whoever
	// builds the app. The default is off: a status monitor has no business
	// leaving third-party HTTP response bodies on someone's disk unasked. Turning
	// it on takes effect on the next write, with no restart, because the reason to
	// turn it on is that something is going wrong RIGHT NOW.
	box.Add(widget.NewSeparator())
	box.Add(sectionHeader(t.SecDiagnostics))
	box.Add(newWrappedLabel(t.DiagnosticsHint))
	diagChk := c.blurOnToggle(widget.NewCheck(t.SaveDiagnosticLogs, c.setDebugLogging))
	diagChk.SetChecked(c.cfgSnapshot().DebugLogging)
	box.Add(diagChk)

	// The number, stated. Every one of these files is bounded (see the budget
	// constants in applog / alertlog / providers), but a documented ceiling the
	// user cannot see is a promise they have to take on faith — and the 202 MB
	// that motivated the budget stayed invisible for a month precisely because
	// nothing ever showed it.
	usage := widget.NewLabel("")
	refreshUsage := func() { usage.SetText(fmt.Sprintf(t.DiagnosticsUsage, humanBytes(diagnosticsUsage()))) }
	refreshUsage()

	del := widget.NewButtonWithIcon(t.DeleteDiagnostics, theme.DeleteIcon(), func() {
		dialog.ShowConfirm(t.DeleteDiagnostics, t.DeleteDiagnosticsConfirm, func(ok bool) {
			if !ok {
				return
			}
			if _, err := deleteDiagnostics(); err != nil {
				dialog.ShowError(err, c.window)
			}
			refreshUsage()
		}, c.window)
	})
	del.Importance = widget.LowImportance

	box.Add(usage)
	box.Add(container.NewHBox(
		widget.NewButtonWithIcon(t.OpenDataFolder, theme.FolderOpenIcon(), openDataFolder),
		widget.NewButtonWithIcon(t.ReportBug, theme.MailComposeIcon(), c.reportBug),
		del,
	))

	// No explicit Close button: the dialog frame provides its own dismiss action.
	return container.NewPadded(box)
}

// cfgSnapshot returns a copy of the current config.
func (c *Controller) cfgSnapshot() config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// updateCfg applies mutate to the config under lock, persists it, and refreshes.
func (c *Controller) updateCfg(mutate func(*config.Config)) {
	c.mu.Lock()
	mutate(&c.cfg)
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	if err := config.Save(cfg); err != nil {
		slog.Warn("config save failed", "err", err)
	}
	c.refresh()
}

// setLanguage persists the UI language, swaps the active i18n catalog, and
// rebuilds the open windows so every string re-renders immediately. Runs on the
// Fyne thread (invoked from the settings dropdown).
func (c *Controller) setLanguage(code string) {
	// No-op if unchanged — guards against a redundant (and, via SetContent,
	// potentially re-entrant) rebuild when the dropdown re-selects the same value.
	if cur := c.cfgSnapshot().Language; cur == code || (cur == "" && code == "en") {
		return
	}
	c.updateCfg(func(cfg *config.Config) { cfg.Language = code })
	i18n.Set(code)
	// Close the settings dialog BEFORE rebuilding the window content, then
	// reopen it so its strings re-render in the new language.
	reopen := c.settingsDlg != nil
	if reopen {
		c.settingsDlg.Hide()
		c.settingsDlg = nil
	}
	if c.window != nil {
		c.window.SetContent(c.buildContent())
	}
	if reopen {
		c.openSettingsDialog()
	}
}

// populateRegionList (re)builds the regions-of-interest list with a Remove
// button per region. Must run on the Fyne thread.
func (c *Controller) populateRegionList() {
	if c.regionList == nil {
		return
	}
	c.mu.Lock()
	regions := append([]string(nil), c.cfg.Regions...)
	c.mu.Unlock()

	c.regionList.Objects = c.regionList.Objects[:0]
	if len(regions) == 0 {
		c.regionList.Add(widget.NewLabel(i18n.T().NoneAllRegions))
	}
	for _, r := range regions {
		region := r
		remove := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { c.removeRegion(region) })
		row := container.NewBorder(nil, nil, nil, remove, widget.NewLabel(region))
		c.regionList.Add(row)
	}
	c.regionList.Refresh()
}

// addRegion validates and persists a user-entered region of interest.
func (c *Controller) addRegion(region string) {
	region = config.NormalizeRegion(region)
	if region == "" {
		return
	}
	c.mu.Lock()
	added := c.cfg.AddRegion(region)
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	if !added {
		return
	}
	_ = config.Save(cfg)
	c.populateRegionList()
	c.refresh()
}

// removeRegion drops a region of interest.
func (c *Controller) removeRegion(region string) {
	c.mu.Lock()
	c.cfg.RemoveRegion(region)
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	_ = config.Save(cfg)
	c.populateRegionList()
	c.refresh()
}
