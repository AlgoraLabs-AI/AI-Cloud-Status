package ui

import (
	"fmt"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// buildContent assembles the window layout: a header (title, controls, offline
// banner, filter/sort), a left selection panel, and the live status table.
func (c *Controller) buildContent() fyne.CanvasObject {
	title := widget.NewLabelWithStyle(AppName, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	t := i18n.T()
	refreshBtn := widget.NewButtonWithIcon(t.RefreshNow, theme.ViewRefreshIcon(), c.forceRefreshNow)
	settingsBtn := widget.NewButtonWithIcon(t.Settings, theme.SettingsIcon(), c.showSettings)
	controls := container.NewHBox(refreshBtn, settingsBtn)

	c.offlineBanner = c.buildOfflineBanner()
	c.offlineBanner.Hide()

	// Footer status bar: only appears when ICMP is unavailable and probing fell
	// back to TCP:443 (now rare — ICMP works unprivileged). It replaces the old
	// always-on top banner, which wasted vertical space.
	c.noteLabel = widget.NewLabel("")
	c.noteLabel.Wrapping = fyne.TextWrapWord
	c.footer = container.NewBorder(nil, nil, widget.NewIcon(theme.WarningIcon()), nil, c.noteLabel)
	c.footer.Hide()

	// Filter + sort controls.
	search := widget.NewEntry()
	search.SetPlaceHolder(t.FilterChecks)
	search.OnChanged = func(s string) {
		c.mu.Lock()
		c.filter = s
		c.mu.Unlock()
		c.refreshLocked()
	}

	sortSel := widget.NewSelect(sortLabels(), func(label string) {
		c.setSort(sortKeyFor(label), c.sortAsc)
	})
	sortSel.SetSelected(sortLabelFor(c.sortKey))

	revChk := c.blurOnToggle(widget.NewCheck(t.Reverse, func(b bool) {
		c.setSort(c.sortKey, b)
	}))
	revChk.SetChecked(c.sortAsc)

	filterRow := container.NewBorder(nil, nil,
		widget.NewLabel(t.SortLabel),
		container.NewHBox(sortSel, revChk),
		search)

	top := container.NewVBox(
		container.NewBorder(nil, nil, title, controls),
		c.offlineBanner,
		filterRow,
		widget.NewSeparator(),
	)

	side := c.buildSidePanel()

	bar := widget.NewProgressBarInfinite()
	c.loading = container.NewBorder(nil, nil,
		widget.NewLabelWithStyle(t.CheckingProviders, fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		nil, bar)
	c.loading.Hide()

	// Reset the row/section widget caches alongside the table container: a full
	// content rebuild (startup, language change) must re-render every cached
	// widget so localized texts and theme colours are rebuilt from scratch.
	c.rowCache = map[string]*rowWidget{}
	c.sectionBoxes = map[int]*sectionBox{}
	c.emptyBox, c.noMatchBox, c.noMatchFilter = nil, nil, ""
	c.tableBody = container.NewVBox()
	c.refreshLocked()

	// Header + rows live in ONE bidirectional scroll so (a) they stay
	// column-aligned (both share the scroll content's width), (b) the user can
	// scroll horizontally to reach the last column when the window is narrow or
	// the columns are wide, and (c) the table's min size stays small so the
	// window can be shrunk freely. The loading bar is pinned above the scroll.
	table := container.NewVBox(c.tableHeader(), c.tableBody)
	tableArea := container.NewBorder(c.loading, nil, nil, nil, container.NewScroll(table))

	split := container.NewHSplit(side, tableArea)
	split.Offset = c.splitOffset()
	// Held so saveWindowState can read back where the user dragged it: Fyne's
	// Split exposes no drag callback, so the offset is captured with the rest of
	// the window state rather than the moment it changes.
	c.split = split
	return container.NewBorder(top, c.footer, nil, nil, split)
}

// splitOffset returns the divider position to open with: the user's remembered
// one, or the default. A persisted value outside the sane range is ignored
// rather than restoring a window with one side collapsed to nothing.
func (c *Controller) splitOffset() float64 {
	c.mu.Lock()
	off := c.cfg.Window.SplitOffset
	c.mu.Unlock()
	if off < config.MinSplitOffset || off > config.MaxSplitOffset {
		return config.DefaultSplitOffset
	}
	return off
}

// setSort persists and applies a new table sort order.
func (c *Controller) setSort(key string, rev bool) {
	c.mu.Lock()
	c.sortKey = key
	c.sortAsc = rev
	c.cfg.Sort = config.SortPref{Column: key, Ascending: rev}
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	_ = config.Save(cfg)
	c.refreshLocked()
}

// buildOfflineBanner builds the single "You're offline" banner (hidden until the
// offline detector trips), which replaces the per-provider feed-unreachable
// alert storm when connectivity drops.
func (c *Controller) buildOfflineBanner() *fyne.Container {
	rect := canvas.NewRectangle(color.NRGBA{R: 0xd7, G: 0x2c, B: 0x2c, A: 0x33})
	rect.CornerRadius = 4
	rect.StrokeColor = color.NRGBA{R: 0xd7, G: 0x2c, B: 0x2c, A: 0xff}
	rect.StrokeWidth = 1
	icon := widget.NewIcon(theme.WarningIcon())
	lbl := widget.NewLabelWithStyle(
		i18n.T().OfflineBanner,
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	lbl.Wrapping = fyne.TextWrapWord
	content := container.NewBorder(nil, nil, icon, nil, lbl)
	return container.NewStack(rect, container.NewPadded(content))
}

// buildSidePanel builds the provider/connectivity selection list as a collapsible
// accordion — one section per category (Connectivity, Custom URLs, then each
// provider category), each with Enable-all / Disable-all controls. Interval,
// regions, and notification preferences live in the Settings window.
func (c *Controller) buildSidePanel() fyne.CanvasObject {
	t := i18n.T()
	title := widget.NewLabelWithStyle(t.MonitoredChecks, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	hint := widget.NewLabelWithStyle(t.PanelHint,
		fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	hint.Wrapping = fyne.TextWrapWord

	c.categoryToggleRefreshers = nil // rebuilt below; drop stale closures from a prior build
	c.connChecks = container.NewVBox()
	c.populateConnChecks()

	targetEntry := widget.NewEntry()
	targetEntry.SetPlaceHolder(t.AddTargetPlaceholder)
	addTarget := func() {
		host := targetEntry.Text
		targetEntry.SetText("")
		c.addCustomTarget(host)
	}
	targetEntry.OnSubmitted = func(string) { addTarget() }
	addBtn := widget.NewButton(t.Add, addTarget)
	addRow := container.NewBorder(nil, nil, nil, addBtn, targetEntry)

	acc := widget.NewAccordion()
	acc.MultiOpen = true // sections are independent; collapsing one keeps the rest

	// Connectivity. Its checkboxes are rebuilt on add/remove, so Enable/Disable-all
	// reads the live c.connCheckRefs slice rather than a captured snapshot.
	connDetail := container.NewVBox(
		c.categoryToggle(func() []*widget.Check { return c.connCheckRefs }),
		c.connChecks,
		addRow,
	)
	connItem := widget.NewAccordionItem(t.SectionConnectivity, connDetail)
	connItem.Open = true // the core check — expanded by default
	acc.Append(connItem)

	// Custom URL checks (user-defined; their own add/test/remove UI).
	acc.Append(widget.NewAccordionItem(t.SectionCustomURLs, c.buildURLSection()))

	// One section per provider category, each with its own Enable/Disable-all.
	for _, cat := range providers.CategoryOrder {
		var refs []*widget.Check
		group := container.NewVBox()
		for _, p := range c.providers {
			if p.Category == cat {
				chk := c.newCheck(p.ID, p.Name, true, false)
				refs = append(refs, chk)
				group.Add(chk)
			}
		}
		if len(refs) == 0 {
			continue
		}
		captured := refs // provider set is static, so a snapshot is safe here
		detail := container.NewVBox(c.categoryToggle(func() []*widget.Check { return captured }), group)
		item := widget.NewAccordionItem(string(cat), detail)
		item.Open = true
		acc.Append(item)
	}

	// Wrap the accordion in a VBox so it stays at its natural (collapsed/expanded)
	// height and top-aligns: without it, the scroll/border stretches the accordion
	// and the open section's detail balloons to fill, leaving a gap above the
	// collapsed sections. The VBox pins it to MinSize; the scroll handles overflow.
	return container.NewBorder(
		container.NewVBox(title, hint, widget.NewSeparator()),
		nil, nil, nil,
		container.NewVScroll(container.NewVBox(acc)),
	)
}

// categoryToggle builds the "Enable all / Disable all" control pair for a section.
// The button matching the section's current state is highlighted (all checks on →
// Enable all; all off → Disable all; a mixed/hand-edited section → neither), and
// that highlight is re-evaluated whenever any check changes. checks() is read
// lazily so it tracks a list rebuilt later (e.g. connectivity). Toggling a Check
// fires its OnChanged only on a real change, so already-correct checks are left be.
func (c *Controller) categoryToggle(checks func() []*widget.Check) fyne.CanvasObject {
	enable := widget.NewButton(i18n.T().EnableAll, nil)
	disable := widget.NewButton(i18n.T().DisableAll, nil)
	refresh := func() {
		enableActive, disableActive := toggleHighlight(checks())
		enable.Importance, disable.Importance = widget.MediumImportance, widget.MediumImportance
		if enableActive {
			enable.Importance = widget.HighImportance // whole category on → "Enable all" is the active state
		} else if disableActive {
			disable.Importance = widget.HighImportance // whole category off → "Disable all" is active
		}
		enable.Refresh()
		disable.Refresh()
	}
	enable.OnTapped = func() {
		for _, ch := range checks() {
			ch.SetChecked(true)
		}
		c.refreshCategoryToggles()
	}
	disable.OnTapped = func() {
		for _, ch := range checks() {
			ch.SetChecked(false)
		}
		c.refreshCategoryToggles()
	}
	c.categoryToggleRefreshers = append(c.categoryToggleRefreshers, refresh)
	refresh()
	return container.NewHBox(enable, disable)
}

// refreshCategoryToggles re-evaluates every section's Enable-all/Disable-all
// highlight. Called after any check toggles so a hand-edit clears the highlight.
func (c *Controller) refreshCategoryToggles() {
	for _, r := range c.categoryToggleRefreshers {
		r()
	}
}

// toggleHighlight decides which of a section's Enable-all / Disable-all buttons is
// the "active" state: enable when every check is on, disable when every check is
// off, and neither when the section is empty or hand-edited to a mixed state.
func toggleHighlight(checks []*widget.Check) (enableActive, disableActive bool) {
	if len(checks) == 0 {
		return false, false
	}
	all, none := true, true
	for _, ch := range checks {
		if ch.Checked {
			none = false
		} else {
			all = false
		}
	}
	return all, none
}

// populateConnChecks (re)builds the connectivity check list, giving custom
// targets a Remove button, and refreshes c.connCheckRefs for the section's
// Enable/Disable-all. Must run on the Fyne thread.
func (c *Controller) populateConnChecks() {
	if c.connChecks == nil {
		return
	}
	c.connChecks.Objects = c.connChecks.Objects[:0]
	c.connCheckRefs = c.connCheckRefs[:0]
	for _, t := range c.allTargets() {
		chk := c.newCheck(t.ID, t.Name, true, true)
		c.connCheckRefs = append(c.connCheckRefs, chk)
		if strings.HasPrefix(t.ID, "custom:") {
			host := t.Host
			remove := widget.NewButton(i18n.T().Remove, func() { c.removeCustomTarget(host) })
			c.connChecks.Add(container.NewBorder(nil, nil, nil, remove, chk))
		} else {
			c.connChecks.Add(chk)
		}
	}
	c.connChecks.Refresh()
	c.refreshCategoryToggles() // the connectivity set changed → re-evaluate its toggle
}

// newCheck builds a checkbox bound to config for the given check id.
func (c *Controller) newCheck(id, name string, def, isTarget bool) *widget.Check {
	chk := c.blurOnToggle(widget.NewCheck(name, func(on bool) {
		c.setEnabled(id, on, isTarget)
		c.refreshCategoryToggles() // a hand-edit may flip a section to mixed/all/none
	}))
	c.mu.Lock()
	checked := c.cfg.IsEnabled(id, def)
	c.mu.Unlock()
	chk.SetChecked(checked)
	return chk
}

// addCustomTarget validates and persists a user-entered connectivity target.
func (c *Controller) addCustomTarget(host string) {
	host = monitor.NormalizeHost(host)
	if !monitor.ValidHost(host) {
		dialog.ShowInformation(i18n.T().InvalidTarget,
			fmt.Sprintf(i18n.T().InvalidTargetMsg, host), c.window)
		return
	}

	c.mu.Lock()
	duplicate := c.cfg.HasCustomTarget(host)
	for _, t := range c.builtinTargets {
		if t.Host == host {
			duplicate = true
		}
	}
	if duplicate {
		c.mu.Unlock()
		dialog.ShowInformation(i18n.T().AlreadyMonitored,
			fmt.Sprintf(i18n.T().AlreadyMonitoredMsg, host), c.window)
		return
	}
	c.cfg.AddCustomTarget(host)
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	_ = config.Save(cfg)

	// New connectivity target: bound its series like the built-in pings.
	c.history.SetCap(monitor.CustomTargetID(host), connHistoryCap)
	c.rebuildEngine()
	c.populateConnChecks()
	c.refresh()
}

// removeCustomTarget removes a user-added connectivity target.
func (c *Controller) removeCustomTarget(host string) {
	id := monitor.CustomTargetID(host)
	c.mu.Lock()
	c.cfg.RemoveCustomTarget(host)
	delete(c.cfg.Enabled, id)
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	_ = config.Save(cfg)

	c.failures.Reset(id)
	c.forgetAlertOutcome(id)
	c.rebuildEngine()
	c.populateConnChecks()
	c.refresh()
}

// maybeShowFirstRun shows the first-run guidance once, after the window appears.
func (c *Controller) maybeShowFirstRun() {
	c.mu.Lock()
	done := c.cfg.FirstRunComplete
	c.mu.Unlock()
	if done {
		return
	}
	go func() { fyne.Do(c.showFirstRun) }()
}

// showFirstRun presents the empty-state welcome that guides provider selection,
// and marks the first run complete so it is not shown again.
func (c *Controller) showFirstRun() {
	c.mu.Lock()
	c.cfg.FirstRunComplete = true
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	_ = config.Save(cfg)

	t := i18n.T()
	intro := widget.NewLabelWithStyle(fmt.Sprintf(t.WelcomeTitle, AppName), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	body := widget.NewLabel(t.WelcomeBody)
	body.Wrapping = fyne.TextWrapWord
	openSettings := widget.NewButtonWithIcon(t.OpenSettings, theme.SettingsIcon(), c.showSettings)
	content := container.NewVBox(intro, body, openSettings)
	dialog.ShowCustom(t.GetStarted, t.StartMonitoring, content, c.window)
}
