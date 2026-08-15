// Package ui implements the Fyne desktop UI: the main window with the
// enable/disable check list and the live status table, the Settings window, the
// system-tray integration, and close-to-tray behavior. Phase 2 adds per-row
// liveness (last-checked, next-poll countdown, latency), an uptime sparkline,
// reliability (retry with backoff and a single "you're offline" banner),
// incident drill-down, and a filterable/sortable table.
package ui

import (
	"context"
	_ "embed"
	"fmt"
	"image/color"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"fyne.io/systray"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/alertlog"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/autostart"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// App identity, shown in the Help > About dialog. Version may be overridden at
// build time with -ldflags "-X .../internal/ui.Version=v1.2.3".
const (
	AppName        = "AI-Cloud-Status"
	AppDescription = "Real-time status & ping monitor for AI, cloud & dev services — region-aware, from your system tray."
	AppOrg         = "AlgoraLabs"
	AppLicense     = "MIT License"
	AppRepo        = "https://github.com/AlgoraLabs-AI/AI-Cloud-Status"
)

// Version is the displayed version string. It is a var (not const) so release
// builds can stamp it via -ldflags -X, where it is set to the git tag ("v1.0.0").
// The literal here is what a build from source reports, and it is also what the
// updater compares against GitHub's latest tag — so leaving it behind a release
// would make every source build believe an update is available forever.
var Version = "1.0.0"

// appIcon is the ACS brand icon (assets/icon, copied into this package so the
// embed directive below can reach it — embed cannot cross a package boundary).
// Used for the app, the window, and the taskbar.
//
//go:embed appicon.png
var appIconPNG []byte
var appIcon = fyne.NewStaticResource("acs-icon.png", appIconPNG)

// connMinIntervalSeconds is the floor for the connectivity probe cadence. It
// stays small so offline detection and packet-loss measurement remain responsive
// (the rolling loss window is a fixed sample COUNT, so a slower cadence just
// widens the time it spans). The user-facing default is DefaultConnIntervalSeconds.
const connMinIntervalSeconds = 1

// lossWindowSize is the rolling per-target probe window for partial-loss
// detection. At the 1s connectivity cadence this is ~60s, long enough that a
// couple of transient blips don't read as "packet loss" (a human running ping
// for a minute is the reference) while still catching sustained degradation.
const lossWindowSize = 60

// startupDelay is how long the app waits after launch before the first round of
// checks, so the window settles (and a splash can show) before network activity.
const startupDelay = 5 * time.Second

// offlineBannerDelay is how long the offline condition must hold continuously
// before the "You're offline" banner appears. The connectivity probe runs every
// second, so without this a single dropped probe would flash the banner for ~1s
// and vanish. Recovery is immediate (no delay on the way back online).
const offlineBannerDelay = 5 * time.Second

// offlineBannerVisible reports whether the offline banner should show: the raw
// offline state must be true AND have held continuously since `since` for at
// least `delay`. A zero `since` (not currently offline, or just flipped) hides it.
func offlineBannerVisible(rawOffline bool, since, now time.Time, delay time.Duration) bool {
	if !rawOffline || since.IsZero() {
		return false
	}
	return now.Sub(since) >= delay
}

// sleepCtx waits for d or until ctx is cancelled; it returns false if ctx was
// cancelled (so a caller can abort its loop on shutdown).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// providerRetryAttempts is the number of times a single provider feed fetch is
// attempted (1 try + retries) before a poll cycle gives up. Retries are spaced
// by exponential backoff with full jitter so a flaky endpoint or a brief network
// blip is tolerated without a tight retry loop.
const providerRetryAttempts = 3

// historySaveEvery is how many countdown ticks (seconds) elapse between
// background saves of the on-disk history ring buffer.
const historySaveEvery = 60

// connHistoryCap bounds each connectivity target's history series. The ping
// strip only displays interval×20 samples, so retaining the provider-sized
// 24h ring (history.DefaultCapacity) at a 1s cadence would bloat memory and
// the persisted file for data that never renders.
const connHistoryCap = 600

// Controller owns the application state and wiring.
type Controller struct {
	app    fyne.App
	window fyne.Window

	builtinTargets []monitor.Target
	providers      []providers.Provider
	checker        *providers.Checker
	failures       *monitor.FailureTracker
	outages        *monitor.OutageTracker
	totalLoss      *monitor.TotalLossDetector
	lossEscalator  *monitor.LossEscalator
	// coreLoss is the CONSOLIDATED partial-loss window: one sample per round,
	// counted as lost only when BOTH built-in default resolvers missed the SAME
	// round. A single lossy resolver (a remote-path blip) never raises the
	// "losing packets" alert on its own. Mutated solely by runConnectivity so it
	// stays single-writer (no snapshot race between Count and LossPercent).
	coreLoss    *monitor.LossWindow
	offline     *monitor.OfflineDetector
	backoff     monitor.Backoff
	history     *history.History
	historyPath string
	// incidents journals every incident observed on a provider feed (bounded,
	// persisted) so the detail panel can show a last-24h incident history after
	// the feed stops listing them.
	incidents     *history.IncidentLog
	incidentsPath string
	// outageStatePath holds the outages latched down when the process ended,
	// so a restart cannot swallow their recovery alerts. See saveOutageState.
	outageStatePath string
	// outageSaveMu serializes snapshot+write of the outage state. c.mu cannot
	// serve: saveOutageState takes it to read downSuppressed.
	outageSaveMu sync.Mutex
	// totalLossSince is when the current app-wide connectivity outage latched,
	// so its age can be judged after a restart.
	totalLossSince time.Time
	// alerts is the durable audit trail of every alert raised or suppressed
	// (alert-log.jsonl, pruned to ~90 days) — the record that lets "this check
	// never alerted in weeks" be told apart from "its feed is parsed wrong and
	// its alerts never fire". See internal/alertlog.
	alerts *alertlog.Log

	mu         sync.Mutex
	cfg        config.Config
	engine     *monitor.Engine
	provStates map[string]provState
	connDown   bool // every enabled connectivity target is currently unreachable
	// connDownSince is when the current total-connectivity blackout began
	// (zero when connectivity is up). It bounds which provider observations
	// still count as evidence that the machine has internet.
	connDownSince time.Time
	// downSuppressed holds the check IDs whose OUTAGE alert was swallowed, so the
	// matching recovery can be swallowed with it. See recoveryIsOrphaned.
	downSuppressed map[string]bool
	// offlineSince is when the raw offline state last became (and stayed) true,
	// zero when online. The banner shows only once it has held offlineBannerDelay,
	// so a one-second probe blip no longer flashes it. See offlineBannerVisible.
	offlineSince time.Time

	// sessionDisabledRegions holds regions deactivated "until restart" (#7) — their
	// provider alerts are muted for this run only, never persisted. Keyed by the
	// normalized region string the user clicked; matched against incident regions
	// with providers.MatchRegion, like the persisted RegionMutedUntil.
	sessionDisabledRegions map[string]bool

	// split is the check-panel / table divider, held so its position can be read
	// back and persisted (Fyne exposes no drag callback for it).
	split *container.Split

	// urlStates is the latest probe result for each custom URL check.
	urlStates map[string]urlState

	// offeredUpdate is the release version already offered this run, so the
	// periodic re-check does not reopen a dialog the user dismissed with "Later".
	offeredUpdate string

	// popupSlots tracks which bottom-right alert-card stacking positions are
	// occupied (see showAlertPopup); slot i sits i card-heights above the corner.
	popupSlots map[int]bool

	// next-poll schedule, used for the per-row countdown.
	nextProviderPoll time.Time
	nextConnPoll     time.Time

	// triggers force an immediate poll, bypassing the sleep (Refresh now).
	providerTrigger chan struct{}
	connTrigger     chan struct{}

	// filter/sort state for the status table.
	filter  string
	sortKey string
	sortAsc bool

	// UI widgets rebuilt on refresh.
	tableBody     *fyne.Container
	noteLabel     *widget.Label
	footer        *fyne.Container
	offlineBanner *fyne.Container
	connChecks    *fyne.Container
	connCheckRefs []*widget.Check // connectivity checkboxes, for the section Enable/Disable-all
	// categoryToggleRefreshers re-evaluate each section's Enable-all/Disable-all
	// highlight (all / none / mixed) after any check changes. Reset on panel rebuild.
	categoryToggleRefreshers []func()
	urlChecks                *fyne.Container
	regionList               *fyne.Container
	loading                  *fyne.Container
	provCountdowns           []*widget.Label // provider-row next-poll labels (shared schedule)
	connCountdowns           []*widget.Label // connectivity-row next-poll labels

	// prevStates remembers each row's last rendered status state so a change can
	// be animated (subject to the reduced-motion preference).
	prevStates map[string]statusState

	// rowCache holds one persistent, in-place-updatable table row per check id,
	// and sectionBoxes the persistent per-section group boxes — so the per-second
	// refresh mutates existing widgets instead of recreating the whole tree
	// (which leaked renderer/texture cache at gigabytes over hours; see
	// rowWidget). Both are (re)initialized by buildContent so a full content
	// rebuild (e.g. language change) starts from scratch with fresh texts.
	rowCache     map[string]*rowWidget
	sectionBoxes map[int]*sectionBox
	// emptyBox / noMatchBox cache the empty-table placeholders, which the
	// per-second refresh would otherwise recreate while the table has no rows.
	emptyBox      fyne.CanvasObject
	noMatchBox    fyne.CanvasObject
	noMatchFilter string

	// desk and trayMenu are retained so the tray icon/label can be updated to
	// reflect the worst current severity at a glance.
	desk         desktop.App
	trayMenu     *fyne.Menu
	lastTray     statusState
	lastTraySumm string
	traySet      bool

	settingsDlg dialog.Dialog // the open Settings dialog (nil when closed)

	// windowHidden tracks whether the main window is currently hidden to the
	// tray (fyne.Window exposes no visibility getter), so the tray icon's
	// left-click can TOGGLE show/hide. Guarded by mu.
	windowHidden bool

	// --- UI liveness watchdog ---
	// startedAt is when this process began running (set in Run). The watchdog
	// uses it for the startup grace period and to decide whether the app ran long
	// enough to earn a fresh auto-relaunch budget.
	startedAt time.Time
	// lastUIBeat is the unix-nano timestamp of the most recent heartbeat that
	// actually executed on the Fyne thread. The watchdog posts a beat via fyne.Do
	// and watches this advance; if it stalls, the Fyne event loop is wedged.
	lastUIBeat atomic.Int64
	// lastTrayOpen is the unix-nano timestamp of the most recent tray-menu open.
	// An open menu blocks the beat on macOS and Linux, so the watchdog reads this
	// to tell "the user is looking at the menu" from "the loop is wedged".
	lastTrayOpen atomic.Int64
	// relaunching guards the one-shot relaunch so a stall and a user Restart can't
	// both fire os.Exit.
	relaunching atomic.Bool
}

// provState is the latest known state of a provider check.
type provState struct {
	checked  bool
	skipped  bool
	err      error
	res      providers.Result
	when     time.Time
	latency  time.Duration
	attempts int
	// lastOK is the last time the feed was actually READ, which is not `when`
	// (that advances on every attempt, failed ones included). It survives a
	// failed poll so the last-24h history can still say whether an incident was
	// open the last time anyone could see — "resolved 1m ago" would otherwise be
	// inferred from an absence that is our own blindness.
	lastOK time.Time
}

// carryLastOK folds the previous state's lastOK into a fresh observation: a
// readable feed stamps it now, an unreadable one inherits it. Without the
// inherit, one failed poll erases the app's memory of when it last saw the
// truth.
func carryLastOK(prev, st provState) provState {
	if st.err == nil && !st.skipped && st.res.Reachable() {
		st.lastOK = st.when
		return st
	}
	st.lastOK = prev.lastOK
	return st
}

// defaultTargets are the two connectivity ping targets.
func defaultTargets() []monitor.Target {
	return []monitor.Target{
		{ID: "ping-cloudflare", Name: "Cloudflare DNS (1.1.1.1)", Host: "1.1.1.1"},
		{ID: "ping-google", Name: "Google DNS (8.8.8.8)", Host: "8.8.8.8"},
	}
}

// New constructs the Controller, loading persisted config and history.
func New() *Controller {
	slog.Info("New: creating Fyne app")
	a := fyneapp.NewWithID("com.algoralabs.acs")
	a.Settings().SetTheme(newAppTheme())
	a.SetIcon(appIcon)

	slog.Info("New: loading config")
	cfg, _ := config.Load()
	setReducedMotionPref(cfg.ReducedMotion)
	i18n.Set(cfg.Language) // select UI language before any string is built

	// Reconcile the OS start-on-login registration to the persisted preference so
	// an externally-removed entry (or a moved executable) is corrected on launch.
	if err := autostart.Apply(cfg.StartOnLogin); err != nil {
		slog.Warn("autostart reconcile failed", "err", err)
	}

	// RESTORE persisted history on launch: the provider uptime strip spans a
	// fixed 24h window, and starting empty would render a misleading all-fresh
	// strip after every restart (5 minutes of data reading as a day). Samples
	// outside the plausible range — older than the window, or future-dated from
	// a clock change — are pruned; the time the app was OFF simply has no
	// samples and paints as grey/unknown in the bucketed strip.
	histPath := ""
	if dir, err := config.Dir(); err == nil {
		histPath = filepath.Join(dir, "history.json")
	}
	hist, err := history.Load(histPath, history.DefaultCapacity)
	if err != nil {
		slog.Warn("history restore failed; starting empty", "err", err)
	}
	hist.Prune(providerUptimeWindow+time.Hour, time.Now())

	// Restore the incident journal alongside the sample history so the last-24h
	// incident list in the detail panel survives restarts too.
	incPath := ""
	if dir, err := config.Dir(); err == nil {
		incPath = filepath.Join(dir, "incidents.json")
	}
	incLog, err := history.LoadIncidents(incPath)
	if err != nil {
		slog.Warn("incident journal restore failed; starting empty", "err", err)
	}
	incLog.Prune(providerUptimeWindow+time.Hour, time.Now())

	// Outages that were still open when the last run ended. Restored below,
	// once the Controller (and its alert log) exist.
	outStatePath := ""
	if dir, err := config.Dir(); err == nil {
		outStatePath = filepath.Join(dir, "open-outages.json")
	}
	openOutages := loadOutageState(outStatePath)

	// Archive raw feed payloads whenever a provider reports an incident or its
	// feed fails to parse: real incident payloads are the evidence base for
	// improving the per-feed parsers (several feeds have never been observed
	// mid-incident). Bounded per provider; see providers.FeedCapture.
	//
	// Both this and the alert trail below are wired unconditionally but write
	// NOTHING unless diagnostic logging is on — each checks config.DebugLogging
	// at the point of writing. Wiring them conditionally here would mean the
	// Settings toggle only took effect after a restart, which is the wrong way
	// round: someone ticks it precisely because a problem is happening NOW.
	checker := providers.NewChecker()
	var alerts *alertlog.Log
	if dir, err := config.Dir(); err == nil {
		capture := providers.NewFeedCapture(filepath.Join(dir, "feed-samples"))
		capture.Enabled = config.DebugLogging
		checker.Capture = capture

		alerts = alertlog.Open(alertlog.Path(dir))
		alerts.Enabled = config.DebugLogging
	}

	c := &Controller{
		app:                    a,
		builtinTargets:         defaultTargets(),
		providers:              providers.Default(),
		checker:                checker,
		failures:               monitor.NewFailureTracker(monitor.DefaultFailureThreshold),
		outages:                monitor.NewOutageTracker(),
		totalLoss:              monitor.NewTotalLossDetector(),
		lossEscalator:          monitor.NewLossEscalator(),
		coreLoss:               monitor.NewLossWindow(lossWindowSize),
		offline:                monitor.NewOfflineDetector(),
		backoff:                monitor.DefaultBackoff(),
		history:                hist,
		historyPath:            histPath,
		incidents:              incLog,
		incidentsPath:          incPath,
		outageStatePath:        outStatePath,
		alerts:                 alerts,
		cfg:                    cfg,
		provStates:             map[string]provState{},
		prevStates:             map[string]statusState{},
		sessionDisabledRegions: map[string]bool{},
		urlStates:              map[string]urlState{},
		providerTrigger:        make(chan struct{}, 1),
		connTrigger:            make(chan struct{}, 1),
		sortKey:                cfg.Sort.Column,
		sortAsc:                cfg.Sort.Ascending,
	}
	if c.sortKey == "" {
		c.sortKey = sortBySeverity
	}
	// Re-latch whatever was still down when the last run ended, now that the
	// alert log exists to journal the ones too old to announce.
	c.restoreOutageState(openOutages, time.Now(), c.isKnownCheck)
	// Connectivity targets tick every ~1s but only display the short interval×20
	// strip — cap their series well below the provider-sized 24h ring so they
	// don't dominate memory and the persisted history file.
	for _, t := range c.allTargets() {
		hist.SetCap(t.ID, connHistoryCap)
	}
	slog.Info("New: rebuilding engine")
	c.rebuildEngine()
	slog.Info("New: done")
	return c
}

// allTargets returns the built-in plus user-added custom connectivity targets.
func (c *Controller) allTargets() []monitor.Target {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.allTargetsLocked()
}

func (c *Controller) allTargetsLocked() []monitor.Target {
	out := append([]monitor.Target(nil), c.builtinTargets...)
	for _, h := range c.cfg.CustomTargets {
		out = append(out, monitor.CustomTarget(h))
	}
	return out
}

// Run builds the window, starts background polling, and blocks until quit.
func (c *Controller) Run() {
	slog.Info("Run: creating window")
	w := c.app.NewWindow(windowTitle())
	c.window = w
	w.SetIcon(appIcon)
	c.applyWindowSize(w)

	slog.Info("Run: building menu")
	w.SetMainMenu(c.buildMainMenu())
	slog.Info("Run: setting up tray")
	c.setupTray()

	// The close (and minimize) buttons hide the window to the tray instead of
	// exiting the process; the app keeps monitoring in the background. Only the
	// tray "Quit" item exits.
	w.SetCloseIntercept(c.hideToTray)

	slog.Info("Run: building content")
	w.SetContent(c.buildContent())

	slog.Info("Run: starting background goroutines")
	c.startedAt = time.Now()
	c.lastUIBeat.Store(c.startedAt.UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.runConnectivity(ctx)
	go c.runProviders(ctx)
	go c.runCountdownTicker(ctx)
	go c.runUIWatchdog(ctx)
	go c.runTrayOpenWatcher(ctx)
	go c.runDeadCanvasWatchdog(ctx)
	go c.runUpdateChecker(ctx)

	// Show the splash for a few seconds, then reveal + maximize the main window on
	// the primary display (Fyne has no maximize API, so that is a best-effort OS
	// call once the window is realized). Blocks on the Fyne event loop.
	slog.Info("Run: splash + entering event loop")
	c.runWithSplash(w)
	slog.Info("Run: event loop returned")
}

// applyWindowSize restores the persisted window size, falling back to defaults.
func (c *Controller) applyWindowSize(w fyne.Window) {
	c.mu.Lock()
	ws := c.cfg.Window
	c.mu.Unlock()
	width, height := ws.Width, ws.Height
	if width < 400 || height < 300 {
		width, height = config.DefaultWindowWidth, config.DefaultWindowHeight
	}
	w.Resize(fyne.NewSize(width, height))
}

// saveWindowState persists the current window size so it is restored next launch.
func (c *Controller) saveWindowState() {
	if c.window == nil || c.window.Canvas() == nil {
		return
	}
	sz := c.window.Canvas().Size()
	if sz.Width < 400 || sz.Height < 300 {
		return
	}
	// The divider goes with the size, because Fyne's Split has no drag callback
	// to hook — reading its current offset when the window state is saved is the
	// available way to remember where the user put it.
	offset := c.splitOffset()
	if c.split != nil {
		offset = c.split.Offset
	}
	c.mu.Lock()
	c.cfg.Window = config.WindowState{Width: sz.Width, Height: sz.Height, SplitOffset: offset}
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	if err := config.Save(cfg); err != nil {
		slog.Warn("config save failed", "err", err)
	}
}

// quit persists state (window size + history) and exits the app.
func (c *Controller) quit() {
	c.saveWindowState()
	c.saveHistory()
	c.app.Quit()
}

// saveHistory persists the in-memory ring buffer and incident journal (best
// effort).
func (c *Controller) saveHistory() {
	if c.history != nil && c.historyPath != "" {
		if err := c.history.Save(c.historyPath); err != nil {
			slog.Warn("saveHistory failed", "path", c.historyPath, "err", err)
		}
	}
	if c.incidents != nil && c.incidentsPath != "" {
		if err := c.incidents.Save(c.incidentsPath); err != nil {
			slog.Warn("incident journal save failed", "path", c.incidentsPath, "err", err)
		}
	}
}

// hideToTray hides the window and, the FIRST TIME EVER (persisted in the
// config, so it never repeats across runs), tells the user the app is still
// running in the tray so the behavior is discoverable.
func (c *Controller) hideToTray() {
	slog.Info("window hidden to tray")
	c.saveWindowState()
	c.window.Hide()
	c.mu.Lock()
	c.windowHidden = true
	shown := c.cfg.TrayNoticeShown
	c.mu.Unlock()
	if !shown {
		t := i18n.T()
		c.app.SendNotification(fyne.NewNotification(
			fmt.Sprintf(t.TrayStillRunningTitle, AppName),
			t.TrayStillRunningBody,
		))
		c.updateCfg(func(cfg *config.Config) { cfg.TrayNoticeShown = true })
	}
}

// showFromTray restores the main window and marks it visible.
func (c *Controller) showFromTray() {
	slog.Info("window shown from tray")
	c.window.Show()
	c.window.RequestFocus()
	c.mu.Lock()
	c.windowHidden = false
	c.mu.Unlock()
}

// toggleWindow shows the window when hidden and hides it to the tray when
// visible — the tray icon's left-click behavior. Runs on the Fyne thread.
func (c *Controller) toggleWindow() {
	c.mu.Lock()
	hidden := c.windowHidden
	c.mu.Unlock()
	if hidden {
		c.showFromTray()
	} else {
		c.hideToTray()
	}
}

// buildMainMenu builds the application menu bar with Hide-to-tray, a Settings
// entry, and a Help > About entry.
func (c *Controller) buildMainMenu() *fyne.MainMenu {
	t := i18n.T()
	file := fyne.NewMenu("App",
		fyne.NewMenuItem(t.TraySettings, c.showSettings),
		fyne.NewMenuItem(t.RefreshNow, c.forceRefreshNow),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(t.TrayHide, c.hideToTray),
	)
	help := fyne.NewMenu("Help",
		fyne.NewMenuItem(t.CheckForUpdates, func() {
			go c.checkForUpdate(context.Background(), true)
		}),
		fyne.NewMenuItem(t.ReportBug, c.reportBug),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(t.About, c.showAbout),
	)
	return fyne.NewMainMenu(file, help)
}

// showAbout opens the About dialog.
func (c *Controller) showAbout() {
	logo := canvas.NewImageFromResource(appIcon)
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(96, 96))

	t := i18n.T()
	name := widget.NewLabelWithStyle(AppName, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	ver := widget.NewLabelWithStyle(fmt.Sprintf(t.AboutVersion, Version), fyne.TextAlignCenter, fyne.TextStyle{})
	desc := widget.NewLabel(AppDescription)
	desc.Wrapping = fyne.TextWrapWord
	by := widget.NewLabelWithStyle(fmt.Sprintf(t.AboutBy, AppOrg, AppLicense), fyne.TextAlignCenter, fyne.TextStyle{Italic: true})

	repoURL, _ := url.Parse(AppRepo)
	link := widget.NewHyperlink(AppRepo, repoURL)
	link.Alignment = fyne.TextAlignCenter

	content := container.NewVBox(container.NewCenter(logo), name, ver)
	// Diagnostic logging gets a banner, not a footnote: it means the app is
	// writing files it does not normally write, and the answer to "is it on?"
	// should be unmissable rather than inferred.
	if config.DebugLogging() {
		content.Add(debugLogBanner())
	}
	// If this install was replaced by an automatic update, say so here, with the
	// digest that was verified before installing it. An unsigned updater that
	// rewrites its own executable should be able to show its work — see
	// config.UpdateRecord.
	c.mu.Lock()
	last := c.cfg.LastUpdate
	c.mu.Unlock()
	if last.Version != "" {
		when := time.Unix(last.At, 0).Format("2006-01-02 15:04")
		upd := widget.NewLabelWithStyle(fmt.Sprintf(t.AboutLastUpdate, last.Version, when),
			fyne.TextAlignCenter, fyne.TextStyle{})
		content.Add(upd)
		if last.SHA256 != "" {
			sum := widget.NewLabelWithStyle("sha256 "+last.SHA256, fyne.TextAlignCenter,
				fyne.TextStyle{Monospace: true})
			sum.Wrapping = fyne.TextWrapBreak
			sum.Selectable = true
			content.Add(sum)
		}
	}
	content.Add(widget.NewSeparator())
	content.Add(desc)
	// About is where someone looks once something has already gone wrong, so the
	// way to report it belongs here as well as in Settings.
	content.Add(container.NewCenter(
		widget.NewButtonWithIcon(t.ReportBug, theme.MailComposeIcon(), c.reportBug)))
	content.Add(widget.NewSeparator())
	content.Add(by)
	content.Add(container.NewCenter(link))
	dialog.ShowCustom(fmt.Sprintf(t.AboutTitle, AppName), t.Close, content, c.window)
}

// setupTray installs the system-tray icon and menu when running on a desktop
// driver that supports it.
func (c *Controller) setupTray() {
	desk, ok := c.app.(desktop.App)
	if !ok {
		return
	}
	c.desk = desk
	t := i18n.T()
	status := fyne.NewMenuItem(t.TrayStatusStart, nil)
	status.Disabled = true
	m := fyne.NewMenu(AppName,
		status,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(t.TrayShow, c.showFromTray),
		fyne.NewMenuItem(t.RefreshNow, c.forceRefreshNow),
		fyne.NewMenuItem(t.TraySettings, c.showSettings),
		fyne.NewMenuItemSeparator(),
		// Restart recovers a wedged/blank window without the user having to kill the
		// process: the system-tray menu is drawn by the OS, so it stays clickable
		// even when the OpenGL canvas has stopped presenting.
		fyne.NewMenuItem(t.TrayRestart, c.restart),
		quitMenuItem(t.TrayQuit, c.quit),
	)
	c.trayMenu = m
	desk.SetSystemTrayMenu(m)
	// The tray icon is ALWAYS the app logo; severity is conveyed by the menu's
	// "Status: …" line, not by recoloring the icon.
	desk.SetSystemTrayIcon(appIcon)
	// Left-click on the tray icon TOGGLES the app window (show when hidden,
	// hide to tray when visible); only right-click shows the menu. Fyne's
	// public SetSystemTrayWindow can't express a toggle for decorated windows
	// (it wires plain Show), so this taps the systray library Fyne drives
	// directly. The callback arrives on the tray's OS thread — hop to the Fyne
	// thread.
	systray.SetOnTapped(func() { fyne.Do(c.toggleWindow) })
}

// updateTray refreshes the tray status line to reflect the worst current
// severity across all enabled checks. The tray ICON stays the app logo at all
// times; severity is shown only in the menu's "Status: …" line. Must run on the
// Fyne thread.
func (c *Controller) updateTray(worst statusState, summary string) {
	if c.desk == nil {
		return
	}
	if c.traySet && worst == c.lastTray && summary == c.lastTraySumm {
		return
	}
	c.lastTray, c.lastTraySumm, c.traySet = worst, summary, true
	if c.trayMenu != nil && len(c.trayMenu.Items) > 0 {
		// Through the catalog, not a hardcoded prefix: summary is already
		// localized by traySummary, so gluing it to a literal "Status: " left
		// every non-English tray reading half-translated the moment the first
		// poll replaced the catalog's starting line.
		c.trayMenu.Items[0].Label = fmt.Sprintf(i18n.T().TrayStatus, summary)
		c.desk.SetSystemTrayMenu(c.trayMenu)
	}
}

// ---- configuration helpers -------------------------------------------------

func (c *Controller) currentInterval() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.IntervalSeconds <= 0 {
		return config.DefaultIntervalSeconds
	}
	return c.cfg.IntervalSeconds
}

func (c *Controller) providerInterval() time.Duration {
	return time.Duration(c.currentInterval()) * time.Second
}

// connIntervalSeconds is the configured connectivity probe interval in seconds
// (clamped to the floor), for the settings entry display.
func (c *Controller) connIntervalSeconds() int {
	return int(c.connInterval() / time.Second)
}

// connInterval is the connectivity (ping) probe cadence, clamped to the floor.
// Like providerInterval it must be resolved BEFORE taking c.mu — it locks
// internally and c.mu is not reentrant.
func (c *Controller) connInterval() time.Duration {
	c.mu.Lock()
	secs := c.cfg.ConnIntervalSeconds
	c.mu.Unlock()
	if secs <= 0 {
		secs = config.DefaultConnIntervalSeconds
	}
	if secs < connMinIntervalSeconds {
		secs = connMinIntervalSeconds
	}
	return time.Duration(secs) * time.Second
}

// setConnInterval persists the connectivity probe interval (seconds).
func (c *Controller) setConnInterval(seconds int) {
	if seconds < connMinIntervalSeconds {
		seconds = connMinIntervalSeconds
	}
	c.mu.Lock()
	c.cfg.ConnIntervalSeconds = seconds
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	c.saveCfg(cfg)
}

func (c *Controller) setInterval(seconds int) {
	c.mu.Lock()
	c.cfg.IntervalSeconds = seconds
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	c.saveCfg(cfg)
}

// saveCfg persists cfg, logging (not surfacing) any write error.
func (c *Controller) saveCfg(cfg config.Config) {
	if err := config.Save(cfg); err != nil {
		slog.Warn("config save failed", "err", err)
	}
}

func (c *Controller) setEnabled(id string, on, isTarget bool) {
	c.mu.Lock()
	if c.cfg.Enabled == nil {
		c.cfg.Enabled = map[string]bool{}
	}
	c.cfg.Enabled[id] = on
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	if err := config.Save(cfg); err != nil {
		slog.Warn("config save failed", "err", err)
	}

	if !on {
		c.failures.Reset(id)
		c.outages.Reset(id)
		c.saveOutageState()
		c.forgetAlertOutcome(id)
	}
	if isTarget {
		c.rebuildEngine()
	} else if !on {
		c.mu.Lock()
		delete(c.provStates, id)
		c.mu.Unlock()
	}
	c.refresh()
}

// silenceCheck suppresses a check's notifications until now+d (the "Silence
// 1h/4h/8h" outage actions). The check keeps polling and stays in the table; only
// its desktop alerts are paused, and the silence self-expires and survives a
// restart.
func (c *Controller) silenceCheck(id string, d time.Duration) {
	now := time.Now().Unix()
	until := time.Now().Add(d).Unix()
	c.mu.Lock()
	if c.cfg.MutedUntil == nil {
		c.cfg.MutedUntil = map[string]int64{}
	}
	// Drop already-expired silences so the persisted map can't grow unbounded as
	// checks are silenced over the app's lifetime. The same kernel runs per poll
	// tick (pruneExpiredSilences) — this call is what keeps a fresh silence from
	// being written alongside stale siblings in the very same save.
	c.cfg.PruneExpiredSilences(now)
	c.cfg.MutedUntil[id] = until
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	if err := config.Save(cfg); err != nil {
		slog.Warn("config save failed", "err", err)
	}
	c.refresh()
}

// pruneExpiredSilences drops per-check silences that have elapsed, once per
// provider poll tick.
//
// Collecting the garbage only inside silenceCheck was not enough: that path runs
// only when the user silences ANOTHER check, so a one-off "Silence 1h" left its
// entry in config.json indefinitely — one was found still there 18 days after it
// expired. It never suppressed anything (every read compares against now), but
// the file then misreports the app's own state to anyone reading it, including a
// later audit. Region mutes were already pruned on this tick; silences now are
// too. Unlike regions there is no "monitoring resumed" toast: a silence the user
// set for a fixed hour ending on schedule is not news.
func (c *Controller) pruneExpiredSilences() {
	c.mu.Lock()
	expired := c.cfg.PruneExpiredSilences(time.Now().Unix())
	cfg := c.cfg.Clone() // runs on the provider goroutine — must not share the live map
	c.mu.Unlock()
	if len(expired) == 0 {
		return
	}
	slog.Info("expired silences pruned", "ids", expired)
	c.saveCfg(cfg)
}

// setMute persists whether a check's notifications are muted.
func (c *Controller) setMute(id string, muted bool) {
	c.mu.Lock()
	c.cfg.SetMute(id, muted)
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	if err := config.Save(cfg); err != nil {
		slog.Warn("config save failed", "err", err)
	}
	c.refresh()
}

func (c *Controller) isMuted(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.IsMuted(id)
}

// notificationsAllowed reports whether a desktop alert for check id should fire,
// honoring the global notifications switch, Do-Not-Disturb, the per-check mute,
// and any active timed silence ("Silence 1h/4h/8h").
func (c *Controller) notificationsAllowed(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cfg.Notifications || c.cfg.DoNotDisturb || c.cfg.IsMuted(id) {
		return false
	}
	if until, ok := c.cfg.MutedUntil[id]; ok && time.Now().Unix() < until {
		return false
	}
	return true
}

// notifySuppressedReason returns why an alert for check id must NOT be
// delivered, or "" when it may fire: the global offline banner already covers
// it, notifications are off, Do-Not-Disturb, the check is muted, or a timed
// silence is active. It is the single decision point behind the gates that used
// to be inline boolean chains, so the reason a transition was swallowed can be
// journaled instead of vanishing (the edge detectors advance either way, so a
// suppressed transition never recurs).
func (c *Controller) notifySuppressedReason(id string) string {
	if c.offline.Offline() {
		return "offline"
	}
	return c.notifyGatesReason(id)
}

// notifyGatesReason is notifySuppressedReason WITHOUT the offline gate, for
// callers that hold direct evidence the machine is online.
func (c *Controller) notifyGatesReason(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case !c.cfg.Notifications:
		return "notifications-disabled"
	case c.cfg.DoNotDisturb:
		return "do-not-disturb"
	case c.cfg.IsMuted(id):
		return "muted"
	}
	if until, ok := c.cfg.MutedUntil[id]; ok && time.Now().Unix() < until {
		return "silenced-until-" + time.Unix(until, 0).Format(time.RFC3339)
	}
	return ""
}

// alertSuppressedReason is notifySuppressedReason plus the provider-only
// region-deactivation gate.
//
// The offline gate applies only when the feed was NOT readable. A reachable
// result means we fetched this provider's status page over HTTPS and parsed it
// this round — direct, first-hand proof the machine is online, which outranks
// the offline detector's inference from ICMP and TCP:443 probes to 1.1.1.1 and
// 8.8.8.8. Those two disagree permanently on any network that blocks direct
// egress but allows HTTPS through a proxy: the detector latches offline for the
// life of the process, and testing it first meant rows turned red and the tray
// read "Outage" while every single provider alert was silently journaled and
// never shown. The offline banner exists to stop a storm of feed-unreachable
// noise, and an unreadable feed does not reach this path at all.
func (c *Controller) alertSuppressedReason(id string, res providers.Result, interest []string) string {
	if !res.Reachable() && c.offline.Offline() {
		return "offline"
	}
	if reason := c.notifyGatesReason(id); reason != "" {
		return reason
	}
	if c.regionAlertSuppressed(res, interest) {
		return "regions-deactivated"
	}
	return ""
}

// deliver routes one edge-detector notification: either shown to the user, or
// journaled with the reason it was swallowed. reason is the caller's gate result
// ("" to show); the orphan-recovery rule is applied here so it cannot be
// forgotten at one of the three call sites.
//
// Collapsed into one function on purpose. Providers, connectivity targets and
// custom URL checks each had their own hand-copy of "if suppressed, log +
// record; else alert", and the pairing logic below has to hold for all three —
// three copies is exactly how the audit's D1/D4 findings started.
func (c *Controller) deliver(id string, n *monitor.Notification, link string, dialog bool, reason string) {
	if reason == "" && n.Recovery && c.recoveryIsOrphaned(id) {
		reason = "outage-alert-was-suppressed"
	}
	c.noteAlertOutcome(id, n.Recovery, reason != "")
	if reason != "" {
		// The edge detector already advanced, so this transition will not come
		// round again — record that it was swallowed and why. Journaling the
		// suppressed RECOVERY matters as much as the outage: internal/audit
		// reconstructs outage windows from these lines and carries Suppressed
		// through without filtering, so dropping it entirely would leave a
		// permanently-open window in the audit.
		slog.Info("alert suppressed", "id", id, "reason", reason,
			"title", n.Title, "recovery", n.Recovery)
		c.alerts.Record(alertlog.Entry{ID: id, Title: n.Title,
			Body: n.Body, Recovery: n.Recovery, Suppressed: reason})
		return
	}
	c.alert(id, dialog, n.Title, n.Body, link, n.Recovery)
}

// forgetAlertOutcome drops any remembered suppression for id. Called wherever
// the edge detectors are reset (a check disabled, removed, or rebuilt): without
// it, disable-then-re-enable leaves a stale flag that swallows a LEGITIMATE
// later recovery — a false negative created by the false-negative fix.
func (c *Controller) forgetAlertOutcome(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.downSuppressed, id)
}

// noteAlertOutcome remembers whether a check's OUTAGE alert was swallowed, so
// its later recovery can be swallowed with it.
func (c *Controller) noteAlertOutcome(id string, recovery, suppressed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.downSuppressed == nil {
		c.downSuppressed = map[string]bool{}
	}
	if suppressed && !recovery {
		c.downSuppressed[id] = true
		return
	}
	delete(c.downSuppressed, id)
}

// recoveryIsOrphaned reports whether a recovery would announce the end of an
// outage the user was never told about.
//
// This is not merely untidy — for the region-deactivation gate it is guaranteed.
// regionAlertSuppressed is a pure function of the CURRENT incident list, and by
// the time the recovery edge fires the muted incident is gone from the feed, so
// the gate that swallowed the outage cannot possibly still fire. Deactivating a
// region you don't run in therefore produced no outage card and then a green
// "recovered" card with nothing to explain it. The same holds for a mute or a
// Do-Not-Disturb window that ends mid-incident.
func (c *Controller) recoveryIsOrphaned(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.downSuppressed[id]
}

// rebuildEngine reconstructs the connectivity engine from the enabled targets.
func (c *Controller) rebuildEngine() {
	c.mu.Lock()
	var enabled []monitor.Target
	for _, t := range c.allTargetsLocked() {
		if c.cfg.IsEnabled(t.ID, true) {
			enabled = append(enabled, t)
		}
	}
	// The engine's built-in loss notifier is disabled (no-op): connectivity
	// notifications are owned by the UI's escalating LossEscalator and the
	// TotalLossDetector so they can re-notify on a backoff and pop a dialog.
	// 3s per-probe timeout: a slow-but-successful ICMP reply (Wi-Fi wake, CPU
	// scheduling, resolver ICMP deprioritization) under ~2s was being counted as a
	// lost packet while a real `ping` (which waits ~4s) saw none. 3s keeps total-
	// blackout detection snappy while removing that false-loss source.
	c.engine = monitor.NewEngine(enabled, lossWindowSize, 3*time.Second, monitor.NotifierFunc(func(_, _ string) {}))
	c.mu.Unlock()
}

func (c *Controller) currentEngine() *monitor.Engine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.engine
}

func (c *Controller) enabledProviders() []providers.Provider {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []providers.Provider
	for _, p := range c.providers {
		if c.cfg.IsEnabled(p.ID, true) {
			out = append(out, p)
		}
	}
	return out
}

// regionsOfInterest returns a copy of the configured regions of interest.
func (c *Controller) regionsOfInterest() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.cfg.Regions...)
}

// ---- alerts ----------------------------------------------------------------

// notify delivers a best-effort OS toast notification on the UI thread.
func (c *Controller) notify(title, body string) {
	slog.Info("toast shown", "title", title, "body", body)
	n := fyne.NewNotification(title, body)
	fyne.Do(func() { c.app.SendNotification(n) })
}

// openURL opens raw in the default browser (best effort).
func (c *Controller) openURL(raw string) {
	if raw == "" {
		return
	}
	if u, err := url.Parse(raw); err == nil {
		_ = c.app.OpenURL(u)
	}
}

// quitMenuItem builds the tray's Quit entry, flagged as THE quit item.
//
// Fyne's addMissingQuitForMenu appends its own Quit unless the last item is
// already marked IsQuit — and it only sets that flag itself when the label
// happens to equal lang.L("Quit"), which Fyne resolves from the OS LOCALE, not
// from this app's language setting. Nine of the ten shipped catalogs have a
// non-English TrayQuit, so on an English-locale machine set to Italian or
// Korean the tray grew a SECOND Quit bound straight to driver.Quit — skipping
// c.quit and therefore saveWindowState and saveHistory, losing the window size
// and up to a minute of samples.
//
// Setting the flag ourselves suppresses the injection. Fyne's final pass only
// assigns its own action to a quit item whose Action is nil, so ours survives.
func quitMenuItem(label string, action func()) *fyne.MenuItem {
	item := fyne.NewMenuItem(label, action)
	item.IsQuit = true
	return item
}

// redactURL strips credentials from a URL before it is logged or displayed.
//
// Monitoring an endpoint that needs Basic auth is a supported configuration —
// net/http turns userinfo into an Authorization header, so
// https://user:token@internal.example/health simply works. Logging it verbatim
// put that token in acs.log, which is the file a user is most likely to attach
// to a GitHub issue on a public repository ("my URL check keeps flapping").
// Returns the input unchanged when there is nothing to redact or it does not
// parse, so a plain status-page URL is untouched.
func redactURL(raw string) string {
	if raw == "" || !strings.Contains(raw, "@") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("xxxxx")
	return u.String()
}

// alert delivers an alert as a toast-style card at the bottom-right of the
// screen (see showAlertPopup) — it never raises the main window or steals
// focus. Safe to call from any goroutine.
func (c *Controller) alert(id string, isTarget bool, title, body, openURL string, recovery bool) {
	// Journal every alert actually raised. This is the counterpart to the raw
	// feed samples in feed-samples/: a captured payload with no matching
	// "alert shown" line here means the alert path — not the parser — dropped it
	// (and vice versa). Every caller of alert() funnels through this one line.
	slog.Info("alert shown", "id", id, "target", isTarget, "recovery", recovery,
		"title", title, "body", body, "url", redactURL(openURL))
	c.alerts.Record(alertlog.Entry{ID: id, Title: title, Body: body, Recovery: recovery})
	fyne.Do(func() { c.showAlertPopup(id, title, body, openURL, recovery) })
}

// ---- polling loops ---------------------------------------------------------

// logPanic recovers a panic in a long-lived background goroutine and records it
// to the log. Without this a panic in a poll loop dies silently on the GUI build
// (no console), and the user just sees a row stop updating with nothing logged.
func logPanic(where string) {
	if r := recover(); r != nil {
		slog.Error("background goroutine panicked", "where", where, "value", r, "stack", string(debug.Stack()))
	}
}

func (c *Controller) runConnectivity(ctx context.Context) {
	defer logPanic("runConnectivity")
	first := time.Now().Add(startupDelay)
	c.mu.Lock()
	c.nextConnPoll = first
	c.mu.Unlock()
	if !sleepCtx(ctx, startupDelay) {
		return
	}
	prevAllDown := false         // were all targets down on the previous round?
	lastEng := c.currentEngine() // detect engine REBUILDS; the first round is not one
	for {
		if eng := c.currentEngine(); eng != nil {
			// lastEng == nil is the FIRST iteration, not an engine rebuild. Resetting
			// there wiped the total-loss latch restored at startup ~5s after launch,
			// killing the very continuity the restore exists for.
			if lastEng != nil && eng != lastEng {
				// The engine was (re)built — the connectivity target set changed, so the
				// consolidated partial-loss accounting must start fresh for the new set.
				// Resetting HERE (on the connectivity goroutine, the sole mutator) rather
				// than inside rebuildEngine keeps coreLoss/lossEscalator/totalLoss
				// single-writer: an old round still in flight cannot repopulate freshly
				// reset state, and coreLoss.Count()/LossPercent() never race a concurrent
				// Reset.
				c.coreLoss.Reset()
				c.lossEscalator.Reset()
				c.totalLoss.Reset()
				prevAllDown = false
			}
			// Assigned every round, not just inside the reset: if currentEngine()
			// could ever be nil at init, a nil lastEng would otherwise stick and
			// every later rebuild would go undetected.
			lastEng = eng
			eng.RunOnce(ctx)
			snap := eng.Snapshot()

			byID := make(map[string]monitor.TargetStatus, len(snap))
			anyChecked := false
			allDown := true
			for _, s := range snap {
				if s.LastChecked.IsZero() {
					continue
				}
				anyChecked = true
				byID[s.ID] = s
				if s.Reachable {
					allDown = false
				}
				// Record a history sample per target (latency = RTT when reachable).
				c.history.Add(s.ID, history.Sample{Time: s.LastChecked, Up: s.Reachable, Latency: s.RTT})

				// Repeated-failure alerts, suppressed while globally offline (the
				// banner covers it) and when notifications are muted/disabled —
				// journaled either way so the audit trail can tell a muted target
				// apart from one that never alerts.
				if n := c.failures.Record(s.ID, s.Name, s.Reachable); n != nil {
					// A target that ANSWERED is proof the machine is online, so a
					// recovery is never gated on the offline banner.
					reason := ""
					if !s.Reachable {
						reason = c.notifySuppressedReason(s.ID)
					} else {
						reason = c.notifyGatesReason(s.ID)
					}
					c.deliver(s.ID, n, "", true, reason)
				}
			}

			c.mu.Lock()
			down := anyChecked && allDown
			// Stamp when the blackout STARTED. recomputeOffline needs it to tell a
			// provider fetch that proves we are online from one that merely
			// succeeded before the network dropped — see the freshness rule there.
			if down {
				if c.connDownSince.IsZero() {
					c.connDownSince = time.Now()
				}
			} else {
				c.connDownSince = time.Time{}
			}
			c.connDown = down
			c.mu.Unlock()

			c.checkTotalLoss(byID)

			// Per-target display hygiene (unchanged): when a FULL blackout ends, clear
			// the engine's rolling per-target loss so the table doesn't keep showing a
			// lingering 95% from the just-ended outage. This no longer feeds any alert —
			// the consolidated partial-loss signal below owns alerting.
			if allDown {
				prevAllDown = true
			} else if anyChecked && prevAllDown {
				eng.ResetLoss()
				prevAllDown = false
			}

			// Consolidated PARTIAL packet-loss accounting. A round counts as lost ONLY
			// when BOTH built-in default resolvers (Cloudflare + Google) missed it
			// together — one lossy resolver is a transient remote-path blip and must not
			// raise a "losing packets" alert on its own. checkTotalLoss ran just above,
			// so c.totalLoss.Down() already reflects this round; that ordering is
			// load-bearing — it keeps the onset of a sustained blackout (rounds 1-2,
			// before confirmation) from leaking into a premature partial-loss alert.
			bothDown, judged := c.bothDefaultsDown(byID)
			switch {
			case !judged:
				// A default is disabled/missing this round, so the both-defaults signal is
				// UNAVAILABLE — not proven healthy. Reset (and suppress any recovery toast)
				// rather than record a sample.
				c.coreLoss.Reset()
				c.lossEscalator.Reset()
			case c.totalLoss.Down():
				// A CONFIRMED total-internet-loss episode is active; the total-loss popup
				// owns it. Keep the partial signal quiet and its window empty so recovery
				// can't read a stale ~90%.
				c.coreLoss.Reset()
				c.lossEscalator.Reset()
			case bothDown:
				// Both defaults down THIS round. Count it toward intermittent accounting,
				// but do NOT advance the escalator here: if this is the onset of a
				// sustained outage, firing now would beat the total-loss popup. Flapping
				// loss still surfaces on the interspersed not-both-down rounds below, where
				// the rolling window already reflects these samples.
				c.coreLoss.Add(false)
			default:
				// At least one default answered this round and judging is available — the
				// honest "losing packets" signal. Advance the escalator (so backoff timing
				// stays correct) but only deliver when alerts are allowed and the global
				// offline banner isn't already covering it.
				c.coreLoss.Add(true)
				lossPct := c.coreLoss.LossPercent()
				ready := c.coreLoss.Count() >= monitor.MinLossSamples
				if n, isFirst := c.lossEscalator.Update(lossPct, ready, time.Now()); n != nil &&
					!c.offline.Offline() && c.notificationsAllowed("") {
					if isFirst {
						c.alert("", false, n.Title, n.Body, "", n.Recovery) // full alert card on first detection
					} else {
						c.notify(n.Title, n.Body) // quiet toast for recurring reminders
					}
				}
			}
		}
		c.recomputeOffline()
		c.refresh()

		// Resolve the interval ONCE before locking — connInterval() takes c.mu and
		// c.mu is not reentrant (same discipline as runProviders).
		interval := c.connInterval()
		c.mu.Lock()
		c.nextConnPoll = time.Now().Add(interval)
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-c.connTrigger:
		}
	}
}

// bothDefaultsDown reports whether BOTH built-in default resolvers were probed
// this round and neither answered. judged is false when fewer than two defaults
// are configured or either one wasn't checked this round (disabled/missing), so
// callers can treat the both-defaults signal as UNAVAILABLE rather than guess.
func (c *Controller) bothDefaultsDown(byID map[string]monitor.TargetStatus) (down, judged bool) {
	if len(c.builtinTargets) < 2 {
		return false, false
	}
	d1, ok1 := byID[c.builtinTargets[0].ID]
	d2, ok2 := byID[c.builtinTargets[1].ID]
	if !ok1 || !ok2 {
		return false, false
	}
	return !d1.Reachable && !d2.Reachable, true
}

// checkTotalLoss evaluates whether BOTH built-in default targets are down and,
// on a state change, raises/recovers the total internet-loss alert — but only
// when not already showing the global offline banner.
func (c *Controller) checkTotalLoss(byID map[string]monitor.TargetStatus) {
	bothDown, judged := c.bothDefaultsDown(byID)
	if !judged {
		c.totalLoss.Reset()
		return
	}
	n := c.totalLoss.Update(bothDown)
	if n == nil {
		return
	}
	// Stamp/clear when this episode latched, so a restart can judge its age.
	c.mu.Lock()
	if n.Recovery {
		c.totalLossSince = time.Time{}
	} else {
		c.totalLossSince = time.Now()
	}
	c.mu.Unlock()

	// Routed through deliver, NOT alert. The gate used to sit after Update, which
	// had already consumed the edge — so a transition swallowed while the offline
	// banner was up left NO trace anywhere, and the edge never came round again.
	// deliver journals the suppression instead, which is what lets an auditor
	// pair the window closed rather than report it open forever.
	reason := ""
	if c.offline.Offline() {
		reason = "offline-banner-shown"
	}
	c.deliver("", n, "", false, reason)
	c.saveOutageState()
}

func (c *Controller) runProviders(ctx context.Context) {
	defer logPanic("runProviders")
	first := time.Now().Add(startupDelay)
	c.mu.Lock()
	c.nextProviderPoll = first
	c.mu.Unlock()
	if !sleepCtx(ctx, startupDelay) {
		return
	}
	for {
		c.pruneExpiredRegionMutes() // expire timed region deactivations + notify "resumed"
		c.pruneExpiredSilences()    // expire timed per-check silences (silently)
		c.pollProvidersOnce(ctx)
		c.pollURLChecksOnce(ctx)
		c.recomputeOffline()
		c.refresh()

		// providerInterval() takes c.mu, so resolve it ONCE, BEFORE locking —
		// sync.Mutex is not reentrant, and locking it twice on this goroutine
		// deadlocks (which then freezes the UI thread the moment it next waits on
		// c.mu). Reusing the same value also keeps the displayed countdown in sync
		// with the actual wait if the interval changes between calls.
		interval := c.providerInterval()
		c.mu.Lock()
		c.nextProviderPoll = time.Now().Add(interval)
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-c.providerTrigger:
		}
	}
}

func (c *Controller) pollProvidersOnce(ctx context.Context) {
	list := c.enabledProviders()
	interest := c.regionsOfInterest()
	var wg sync.WaitGroup
	for _, p := range list {
		wg.Add(1)
		go func(p providers.Provider) {
			defer wg.Done()
			st := c.checkProviderWithRetry(ctx, p)

			c.mu.Lock()
			st = carryLastOK(c.provStates[p.ID], st)
			c.provStates[p.ID] = st
			c.mu.Unlock()

			// History: latency is the (last) fetch duration; "up" means we read the
			// feed AND the service is operational in the regions of interest. An
			// UNREADABLE feed records an Unknown sample — grey on the strip, outside
			// the uptime % — mirroring the Status column and alerting, which never
			// treat "could not check" as an outage (a 2-minute local internet blip
			// used to paint red bands across every provider). When genuinely down,
			// capture the regions whose MAJOR incidents drove it so the uptime strip
			// can be recomputed against later region de-selections (an empty set
			// means an unscoped/global down that muting can never green). Severity
			// comes from unmutedSeverity — the same incident-first kernel as the
			// Status column — so the strip can never show green during a displayed
			// outage.
			reachable := st.res.Reachable() && !st.skipped
			up := reachable && unmutedSeverity(st.res, interest) < providers.SevMajor
			sample := history.Sample{Time: st.when, Up: up, Unknown: !reachable, Latency: st.latency}
			if !up && reachable {
				sample.DownRegions, _ = sampleDownRegions(st.res, interest)
			}
			// Capture any degradation INDEPENDENTLY of the outage state: a provider
			// can have both a major (region A) and a minor (region B) incident, and
			// muting A must reveal the amber from B rather than going green.
			if reachable {
				degRegions, degUnscoped := sampleDegradedRegions(st.res, interest)
				if degUnscoped || len(degRegions) > 0 {
					sample.Degraded = true
					sample.DegradedRegions = degRegions
				}
			}
			c.history.Add(p.ID, sample)

			// Journal every incident the feed currently lists so the detail
			// panel can show a last-24h history after they resolve. Recorded
			// unfiltered; region scoping is applied at display time against the
			// then-current interest set.
			if reachable && len(st.res.Incidents) > 0 {
				c.incidents.Observe(p.ID, incidentEntries(st.res.Incidents), st.when)
			}

			// Alert on real, region-scoped service outages only. A feed we could not
			// read is "unknown" and never alerts. Suppressed while globally offline,
			// muted, notifications off, or when every region driving the outage has
			// been deactivated (#7). The edge detector still advances (delivery is
			// gated, not the state), so the outage re-alerts on its next real change.
			if st.res.Reachable() && !st.skipped {
				sev := unmutedSeverity(st.res, interest)
				down := sev >= providers.SevMajor
				// Journal what the feed actually said whenever it is not clean, so a
				// captured payload in feed-samples/ can always be matched against the
				// decision it produced. A silently-empty or mis-parsed feed then shows
				// up as "operational" here for hours next to a real outage, instead of
				// leaving no trace at all (the failure mode that hid the 2026-07-23
				// Azure West US incident for days).
				if sev > providers.SevNone || len(st.res.Incidents) > 0 {
					slog.Info("provider degraded", "id", p.ID, "severity", sev.String(),
						"down", down, "incidents", len(st.res.Incidents),
						"summaries", strings.Join(st.res.Labels(), " | "))
				}
				summary := ""
				if down {
					summary = unmutedWorstSummary(st.res, interest)
				}
				if n := c.outages.Update(p.ID, p.Name, summary, down); n != nil {
					c.deliver(p.ID, n, p.StatusPage(), false,
						c.alertSuppressedReason(p.ID, st.res, interest))
					// Persisted AFTER delivery, for two reasons. The state must
					// record whether the opening alert was actually SHOWN, which
					// only deliver decides; and on the recovery side a crash
					// between the two now leaves the latch on disk — a duplicate
					// recovery next launch, which is far better than the silent
					// loss the reverse order produced.
					c.saveOutageState()
				}
			}
		}(p)
	}
	wg.Wait()
}

// checkProviderWithRetry fetches p's feed, retrying a failed READ with
// exponential backoff and full jitter. ErrNoFeed (no public feed) and success
// both stop immediately; only an unreadable feed is retried. The measured
// latency is the duration of the final attempt.
func (c *Controller) checkProviderWithRetry(ctx context.Context, p providers.Provider) provState {
	st := provState{checked: true}
	var res providers.Result
	var err error
	tries, _ := monitor.Retry(ctx, providerRetryAttempts, c.backoff, rand.Float64, func(int) (bool, error) {
		start := time.Now()
		res, err = c.checker.Check(ctx, p)
		st.latency = time.Since(start)
		if err == nil {
			return false, nil
		}
		if err == providers.ErrNoFeed {
			return false, err // not retryable: no public feed
		}
		return true, err // retryable: feed could not be read
	})
	st.attempts = tries
	switch {
	case err == providers.ErrNoFeed:
		st.skipped = true
	case err != nil:
		st.err = err
		st.res = providers.Result{Feed: providers.FeedUnreachable}
		// A feed we cannot read left NO trace in the log until now: a provider
		// could sit on "status feed unavailable" for hours and acs.log would
		// show only the healthy providers around it, so the one question worth
		// asking after the fact — WHY — had no answer on disk. The classified
		// reason and the raw error both go in; the reason is what you grep for,
		// the error is what you send to whoever owns the network.
		kind, code := providers.ClassifyFeedFailure(err)
		slog.Warn("provider feed unreadable", "id", p.ID, "reason", kind.String(),
			"http_status", code, "attempts", tries, "err", err)
	default:
		st.res = res
	}
	st.when = time.Now()
	return st
}

// recomputeOffline folds the latest connectivity and provider observations into
// the offline detector and refreshes the banner on a transition.
func (c *Controller) recomputeOffline() {
	c.mu.Lock()
	connDown := c.connDown
	// Only FRESH provider observations count. provStates persists between
	// provider polls (a minute by default) while this runs from the 1s
	// connectivity loop, and Evaluate now treats a reachable feed as proof the
	// machine is online. Without a freshness bound, a successful poll from a
	// minute ago would keep vetoing the offline banner for a full provider
	// interval after the network actually dropped — trading one wrong answer for
	// a slower right one. Two intervals of slack absorbs a poll that ran long.
	fresh := 2 * time.Duration(c.cfg.IntervalSeconds) * time.Second
	if fresh <= 0 {
		fresh = 2 * time.Duration(config.DefaultIntervalSeconds) * time.Second
	}
	// ...and only observations made AFTER the connectivity probes went dark. A
	// successful HTTPS fetch is proof the machine was online WHEN IT HAPPENED,
	// not now. Freshness alone is a 2-minute window, so a poll that landed
	// seconds before the wifi dropped kept vetoing "offline" for the rest of it:
	// on 2026-08-02 that veto let a "Provider outage — portaldev: request
	// failed" alert through 51 seconds after this app had already announced
	// "Total internet loss", blaming a remote endpoint for the user's own dead
	// link. Connectivity probes run every second; a stale success cannot outrank
	// them. On a proxied network that blocks ICMP but allows HTTPS the provider
	// polls keep succeeding DURING the blackout, so they still land after this
	// mark and still win — which is the case the positive-evidence rule exists
	// for.
	blackoutStart := c.connDownSince
	now := time.Now()
	checked, unreachable := 0, 0
	for _, st := range c.provStates {
		if !st.checked || st.skipped {
			continue
		}
		if !st.when.IsZero() && now.Sub(st.when) > fresh {
			continue // too old to say anything about right now
		}
		if !blackoutStart.IsZero() && st.when.Before(blackoutStart) {
			continue // predates the blackout: says nothing about now
		}
		checked++
		if st.err != nil {
			unreachable++
		}
	}
	c.mu.Unlock()

	raw, changed := c.offline.Update(connDown, checked, unreachable)

	// Maintain the debounce anchor: stamp when offline first appears, clear it the
	// moment we're back online. The 1s connectivity loop re-renders every tick, so
	// the banner appears within ~1s of crossing offlineBannerDelay without extra
	// wiring; recovery clears it immediately.
	c.mu.Lock()
	if raw {
		if c.offlineSince.IsZero() {
			c.offlineSince = time.Now()
		}
	} else {
		c.offlineSince = time.Time{}
	}
	c.mu.Unlock()

	if changed {
		c.refresh()
	}
}

// forceRefreshNow triggers an immediate connectivity and provider poll.
func (c *Controller) forceRefreshNow() {
	select {
	case c.providerTrigger <- struct{}{}:
	default:
	}
	select {
	case c.connTrigger <- struct{}{}:
	default:
	}
}

// recheckProvider re-runs a single provider's status check immediately (used by
// the Retry affordance on error rows).
func (c *Controller) recheckProvider(p providers.Provider) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st := c.checkProviderWithRetry(ctx, p)
		c.mu.Lock()
		c.provStates[p.ID] = st
		c.mu.Unlock()
		c.refresh()
	}()
}

// runCountdownTicker updates the per-row next-poll countdowns once per second and
// periodically persists the history ring buffer.
func (c *Controller) runCountdownTicker(ctx context.Context) {
	defer logPanic("runCountdownTicker")
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		fyne.Do(c.updateCountdowns)
		ticks++
		if ticks%historySaveEvery == 0 {
			c.saveHistory()
		}
	}
}

// updateCountdowns refreshes the next-poll labels in place (no row rebuild, so
// keyboard focus is preserved). Must run on the Fyne thread.
func (c *Controller) updateCountdowns() {
	// Resolve the intervals BEFORE locking — both take c.mu, which is not
	// reentrant (same discipline as runProviders/runConnectivity).
	provInterval := c.providerInterval()
	connInterval := c.connInterval()
	c.mu.Lock()
	nextProv := c.nextProviderPoll
	nextConn := c.nextConnPoll
	prov := append([]*widget.Label(nil), c.provCountdowns...)
	conn := append([]*widget.Label(nil), c.connCountdowns...)
	c.mu.Unlock()

	provText := countdownText(nextProv, provInterval)
	connText := countdownText(nextConn, connInterval)
	for _, l := range prov {
		if l.Text != provText {
			l.SetText(provText)
		}
	}
	for _, l := range conn {
		if l.Text != connText {
			l.SetText(connText)
		}
	}
}

// ---- rendering -------------------------------------------------------------

// refresh schedules a UI rebuild on the Fyne thread.
func (c *Controller) refresh() {
	fyne.Do(c.refreshLocked)
}

// refreshLocked updates the status table in place. Must run on the Fyne thread.
// Row and section widgets are cached and mutated rather than recreated (see
// rowWidget), so the table container itself is only touched when the set or
// order of visible rows changed, or a row swapped a layout-affecting cell.
func (c *Controller) refreshLocked() {
	if c.tableBody == nil {
		return
	}
	rows, layoutChanged := c.buildRows()

	if !sameObjects(c.tableBody.Objects, rows) {
		c.tableBody.Objects = append(c.tableBody.Objects[:0], rows...)
		c.tableBody.Refresh()
	} else if layoutChanged {
		// Same rows, but a row swapped a cell whose height may differ (wrapped
		// incidents text, region chips) — relayout without restructuring.
		c.tableBody.Refresh()
	}

	c.updateOfflineBanner()

	// Surface the ICMP-fallback note in the footer only when applicable.
	if c.footer != nil {
		note := ""
		if eng := c.currentEngine(); eng != nil {
			note = eng.FallbackNote()
		}
		if note == "" {
			c.footer.Hide()
		} else {
			c.noteLabel.SetText(note)
			c.footer.Show()
		}
	}
}

// updateOfflineBanner shows or hides the single "You're offline" banner. Must
// run on the Fyne thread.
func (c *Controller) updateOfflineBanner() {
	if c.offlineBanner == nil {
		return
	}
	c.mu.Lock()
	since := c.offlineSince
	c.mu.Unlock()
	if offlineBannerVisible(c.offline.Offline(), since, time.Now(), offlineBannerDelay) {
		c.offlineBanner.Show()
	} else {
		c.offlineBanner.Hide()
	}
}

// setLoading toggles the non-freezing loading indicator above the table.
func (c *Controller) setLoading(on bool) {
	if c.loading == nil {
		return
	}
	if on {
		c.loading.Show()
	} else {
		c.loading.Hide()
	}
}

// animateStateChange tweens the status dot's hue when a row's state changes,
// honoring the reduced-motion preference. Runs on the Fyne thread.
func (c *Controller) animateStateChange(id string, now statusState, dot *canvas.Circle) {
	prev, had := c.prevStates[id]
	c.prevStates[id] = now
	if !had || prev == now || transitionDuration() == 0 {
		return
	}
	from := visualFor(prev).Color
	to := visualFor(now).Color
	dot.FillColor = from
	canvas.NewColorRGBAAnimation(from, to, transitionDuration(), func(col color.Color) {
		dot.FillColor = col
		dot.Refresh()
	}).Start()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("15:04:05")
}

// staticCountdownMax is the cadence at or below which the Next-poll cell shows
// the fixed interval ("1s") instead of a live countdown: a label re-ticking
// every second at a 1-5s cadence is visual noise that conveys nothing.
const staticCountdownMax = 5 * time.Second

// countdownText renders the Next-poll cell for a check polled every interval:
// the static interval for fast cadences, a ticking countdown for slow ones.
func countdownText(next time.Time, interval time.Duration) string {
	if interval > 0 && interval <= staticCountdownMax {
		return fmt.Sprintf("%ds", int(interval.Seconds()))
	}
	return formatCountdown(time.Until(next))
}

// formatCountdown renders a duration until the next poll as a short string.
func formatCountdown(d time.Duration) string {
	if d <= 0 {
		return "now…"
	}
	secs := int(d.Round(time.Second).Seconds())
	if secs <= 0 {
		return "now…"
	}
	return fmt.Sprintf("%ds", secs)
}

// formatIncidentTime renders a feed-reported timestamp in the OS-local
// timezone with an explicit UTC offset (e.g. "2026-07-18 13:46 (UTC-03:00)"),
// so a time the provider reported in UTC is unambiguous next to the system
// clock. Local() resolves the offset from the OS timezone database on Windows,
// Linux, and macOS alike, including DST.
func formatIncidentTime(ts time.Time) string {
	return ts.Local().Format("2006-01-02 15:04 (UTC-07:00)")
}

// formatSpanClock renders one end of a history card's time range compactly:
// the full date + UTC-offset form would be too long to repeat twice on one
// line, and the zone is stated once per card by formatZone instead. A bare
// local HH:MM is enough while the timestamp falls on the same local day as ref
// (the card's reference "now") — but the history window is a rolling 24 hours,
// so for half of every day it straddles midnight, and two bare clock times
// across that boundary read backwards: an incident that began at 12:20
// yesterday and cleared at 12:18 today renders as "started 12:20, resolved
// 12:18", which looks like it resolved before it began. Prefixing the
// off-day end with its date is what restores the order. The date is numeric on
// purpose — no month name to translate across the ten catalogs.
func formatSpanClock(ts, ref time.Time) string {
	if sameLocalDay(ts, ref) {
		return ts.Local().Format("15:04")
	}
	return ts.Local().Format("01-02 15:04")
}

// sameLocalDay reports whether two instants land on the same calendar day in
// the OS-local zone. Compared by calendar date rather than by a 24-hour
// distance so a DST shift can't move an afternoon across the boundary.
func sameLocalDay(a, b time.Time) bool {
	ay, am, ad := a.Local().Date()
	by, bm, bd := b.Local().Date()
	return ay == by && am == bm && ad == bd
}

// formatZone renders just a timestamp's local UTC offset (e.g. "UTC-03:00"),
// so the bare HH:MM clock times beside it are never ambiguous about which
// timezone they are expressed in.
func formatZone(ts time.Time) string {
	return ts.Local().Format("UTC-07:00")
}

// formatElapsed renders a duration as a compact, language-neutral age —
// "45m", "2h 15m", "18d 4h" — for the "… ago" parentheticals on incident
// titles. Two units is the most it ever shows: the finer one stops mattering
// once the coarser one is large, and the trailing unit is dropped when zero so
// a clean boundary reads "3h", not "3h 0m". A duration at or below zero (a
// feed timestamp in the future, or a clock change) reads as "0m" rather than a
// confusing negative age.
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return "0m"
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		if m := int(d.Minutes()) % 60; m > 0 {
			return fmt.Sprintf("%dh %dm", int(d.Hours()), m)
		}
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		days := int(d.Hours()) / 24
		if h := int(d.Hours()) % 24; h > 0 {
			return fmt.Sprintf("%dd %dh", days, h)
		}
		return fmt.Sprintf("%dd", days)
	}
}

// formatLatency renders a response latency, or an em dash when unknown.
func formatLatency(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d < time.Millisecond {
		return "<1ms"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// formatRTT renders the round-trip time of a probe already known to have
// SUCCEEDED. It differs from formatLatency in exactly one case, and that case is
// real: Windows' IcmpSendEcho reports whole milliseconds, so a LAN or gateway
// reply faster than 1ms comes back as a genuine success with an RTT of zero.
// formatLatency reads 0 as "unknown" and prints an em dash, which is right for a
// row cell (where 0 means the check never ran) and wrong for a measurement that
// did happen.
func formatRTT(d time.Duration) string {
	if d <= 0 {
		return "<1ms"
	}
	return formatLatency(d)
}

func newWrappedLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}
