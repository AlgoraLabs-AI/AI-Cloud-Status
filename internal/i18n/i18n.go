// Package i18n provides the app's user-facing strings in a typed, per-language
// catalog. Each language lives in ONE Go file that self-registers via init() —
// so adding a language is a single new file (e.g. fr.go) and nothing else: no
// edits here, no switch to extend, no list to update. The app reads strings by
// typed key (i18n.T().RefreshNow), which loads the value for the active language.
//
// Completeness (every language defines every key) is enforced by a reflection
// test (see i18n_test.go) that fails if any registered catalog leaves a field
// empty — the practical equivalent of a compile-time guarantee for keyed literals.
//
// The active catalog is process-global, swapped by Set when the user changes
// language. It is held in an atomic pointer so a read via T() (which can happen on
// a background goroutine, e.g. the poll loop's "monitoring resumed" notice) never
// races the UI-thread Set — both are atomic.
package i18n

import "sync/atomic"

// Catalog holds every translatable UI string, grouped by area. Keep fields
// alphabetical within a group so languages stay easy to diff against each other.
type Catalog struct {
	// Window chrome & controls.
	RefreshNow   string
	Settings     string
	FilterChecks string
	SortLabel    string
	Reverse      string

	// Sort option labels (must align 1:1 with the sort keys in the ui package).
	SortSeverity    string
	SortName        string
	SortLatency     string
	SortLastChecked string
	SortUptime      string
	SortCategory    string

	// Side panel.
	MonitoredChecks      string
	PanelHint            string
	EnableAll            string
	DisableAll           string
	SectionConnectivity  string
	SectionCustomURLs    string
	SectionOther         string // catch-all section for a provider whose category is not in CategoryOrder
	AddTargetPlaceholder string
	Add                  string

	// Table headers.
	ColName        string
	ColStatus      string
	ColLastChecked string
	ColNextPoll    string
	ColLatency     string
	ColUptime      string
	CheckEvery     string // section cadence label, takes one %s (humanized interval), e.g. "every 30s"
	UptimeRange    string // section uptime-strip range label, takes one %s (humanized span), e.g. "uptime: last 20m"
	ColRegions     string
	ColIncidents   string

	// Status states (badge labels + tray summaries).
	StatusOperational  string
	TrayAllOperational string // tray-only "all good" phrasing, distinct from a single badge
	StatusDegraded     string
	StatusOutage       string
	StatusUnreachable  string
	StatusNoFeed       string
	// StatusOfflineUnknown is shown when a check failed because THIS machine
	// had no internet. It must never read like a fault at the far end.
	StatusOfflineUnknown string
	StatusChecking       string

	// Region deactivation popup (#7).
	RegionTitlePrefix     string // "Region: "
	RegionDeactivateIntro string
	RegionDeactivateFor   string
	Region1Hour           string
	Region4Hours          string
	Region24Hours         string
	RegionUntilReactivate string
	RegionUntilRestart    string
	RegionReactivateNow   string
	RegionResumedTitle    string
	RegionResumedBody     string // takes one %s (region)
	Close                 string
	CloseAndMinimize      string

	// Settings window.
	SecLanguage             string
	SecPolling              string
	Apply                   string
	CheckInterval           string
	ConnCheckInterval       string
	SecRegions              string
	RegionsHint             string
	RegionExample           string
	SecNotifications        string
	EnableNotifications     string
	DoNotDisturb            string
	SecAccessibility        string
	ReduceMotion            string
	SecStartup              string
	StartOnLogin            string // takes one %s (app name)
	StartOnLoginUnavailable string

	// Tray menu.
	TrayShow        string
	TraySettings    string // with trailing ellipsis
	TrayHide        string
	TrayRestart     string
	TrayQuit        string
	TrayStatusStart string
	TrayStatus      string // the tray's status line once polling has results; takes one %s (localized summary)
	About           string

	// About dialog.
	AboutTitle   string // takes one %s (app name)
	AboutVersion string // takes one %s (version)
	AboutBy      string // takes two %s (org, license)

	// Provider-outage alert dialog.
	OpenStatusPage      string
	SilenceLabel        string
	Hours8              string
	DisableUntilRestart string
	DisableForever      string

	// Empty / loading states.
	CheckingProviders string
	NoChecksEnabled   string
	NoChecksHint      string
	NoMatch           string // takes one %s (filter)
	Retry             string
	Remove            string

	// Offline banner.
	OfflineBanner string

	// First-run welcome.
	WelcomeTitle    string // takes one %s (app name)
	WelcomeBody     string
	OpenSettings    string
	GetStarted      string
	StartMonitoring string

	// Add-target validation dialogs.
	InvalidTarget       string
	InvalidTargetMsg    string // takes one %q
	AlreadyMonitored    string
	AlreadyMonitoredMsg string // takes one %q

	// Regions list (settings).
	NoneAllRegions string

	// Custom URL form.
	NamePlaceholder   string
	ExpectPlaceholder string
	Test              string
	AddURL            string
	URLReachAlways    string // reachability is always checked
	URLStringCheck    string // optional "also check the page contains text" toggle
	URLModeReachable  string // existing-check subtitle: reachability only
	URLModeContains   string // existing-check subtitle: reachability + text
	URLEnterFirst     string
	URLTesting        string
	URLEnterURL       string
	URLInvalid        string
	URLEnterText      string
	URLExists         string

	// Incident detail panel.
	NotCheckedYet     string
	NoPublicFeed      string
	RetryNow          string
	NoActiveIncidents string
	// NoActionableIncidents is NoActiveIncidents' careful sibling, used when the
	// feed still carries something — a stale/zombie alert, or incidents in
	// regions the user silenced. Nothing needs attention, but "operational" is
	// a stronger claim than the panel is entitled to make there.
	NoActionableIncidents string
	// AlsoRecentIncidents24h titles the history when incidents were already
	// drawn above it. Those are subtracted from the list, so calling what
	// remains "the incident history" would promise a completeness it no longer
	// has — this word says "and also these".
	AlsoRecentIncidents24h string
	// IncidentLastSeenAgo closes a history entry whose CURRENT state is
	// unknown — the journal was restored from disk and no poll has succeeded
	// since, so "resolved" would be inferred from an absence that is only our
	// own downtime. One %s: the elapsed time.
	IncidentLastSeenAgo   string
	RecentIncidents24h    string // header above the last-24h incident history
	IncidentDetailsTitle  string // takes one %s (provider name)
	SeverityLabel         string // takes one %s
	AffectedRegionsLabel  string // takes one %s
	AffectedRegionsGlobal string
	AffectedComponents    string // takes one %s
	StartedLabel          string // takes one %s
	UpdatedLabel          string // takes one %s
	IncidentResolvedRange string // parenthetical on a history card's title; takes three %s (started time, resolved time, timezone)
	IncidentOngoingRange  string // parenthetical on a history card's title; takes two %s (started time, timezone)
	IncidentStartedAgo    string // parenthetical closing an incident title; takes one %s (elapsed since start, e.g. "2h 15m")
	IncidentResolvedAgo   string // parenthetical closing a resolved history card's title; takes one %s (elapsed since resolution)
	AgeAgo                string // bare age parenthetical qualifying a field label ("Started (2h 15m ago):"); takes one %s (elapsed)
	OpenIncident          string
	DetailMeta            string // takes %s (when), %s (latency), %d (attempts)
	DetailUptime          string // takes one %.0f (percent), with leading spaces
	MuteProvider          string
	ActiveIncidentsN      string // takes one %d (count)
	StaleIncidentsN       string // header above incidents still open but silent past the zombie horizon; takes one %d (count)
	MutedIncidentsN       string // one %d: how many incidents your region mutes silence
	IncidentFallback      string

	// Connectivity (ping) and custom-URL drill-down panels. These checks have no
	// status feed, so their panels are built from the recorded samples instead:
	// latency figures, packet loss, and outages reconstructed from runs of failed
	// probes. Every window-scoped heading takes the REAL covered span, because a
	// ping ring holds only minutes while a URL ring holds a day — a hardcoded
	// "24 h" would be a lie on half of them.
	PingDetailsTitle    string // takes one %s (target name)
	CheckDetailsTitle   string // takes one %s (check name)
	ConnDetailMeta      string // takes %s (when), %s (latency), %s (probe mode)
	URLDetailMeta       string // takes %s (when), %s (response time), %s (result detail)
	ProbeFallbackNote   string
	LatencyTrend        string // header above the latency chart; takes one %s (covered span)
	StatsHeader         string // takes one %s (covered span)
	StatAvgLatency      string // takes one %s
	StatAvgResponse     string // takes one %s
	StatMinMax          string // takes two %s (fastest, slowest)
	StatPacketLoss      string // takes %s (percent), %d (lost), %d (observed)
	StatLongestLoss     string // takes one %d (probes)
	NoMeasurementsYet   string
	ExpectedTextLabel   string // takes one %s
	OutagesHeader       string // takes %s (covered span), %d (count)
	NoOutagesWindow     string // takes one %s (covered span)
	OutageOngoing       string // takes two %s (start clock, elapsed)
	OutageResolved      string // takes three %s (start clock, end clock, duration)
	OutageAtLeast       string // takes three %s (start clock, end clock, duration) when the recovery was never observed
	OutageResumedUp     string // takes four %s (start clock, end clock, duration, the time monitoring came back to a healthy check)
	OutageTruncatedNote string // suffix when the outage began before the ring's horizon

	// Strings that used to be hardcoded English literals in internal/ui. They
	// were invisible to the completeness test precisely because they were not
	// catalog fields, so nine of the ten languages rendered them in English with
	// nothing failing. Moving them here is what makes the test able to see them.
	LatencyUnreachable    string // latency cell when the check could not be reached
	NoPublicStatusFeed    string
	StatusFeedUnavailable string // generic fallback: we could not read the feed and cannot say why

	// Why the status feed could not be read. Each of these ends in the same
	// state — health UNKNOWN, never an outage — but they point at different
	// ends of the wire, and the generic sentence above pointed at none of them.
	FeedErrCertificate    string
	FeedErrDNS            string
	FeedErrTimeout        string
	FeedErrHTTP           string // one %d: the status code the page answered with
	FeedErrUnparseable    string
	FeedErrDetail         string // one %s: the raw transport error, for the drill-down
	TrayStillRunningTitle string // "%s is still running"
	TrayStillRunningBody  string

	// Region deactivation status, shown in the region popup.
	RegionDeactivatedUntilRestart string
	RegionDeactivatedForever      string
	RegionDeactivatedUnderMinute  string
	RegionDeactivatedFor          string // one %s: the remaining span
	RegionDeactivatedPlain        string
	RegionIncidentLastUpdate      string // "%s" timestamp + "%d" days
	RegionStaleZombieSuffix       string

	// Automatic updates.
	CheckForUpdates    string
	UpdateAvailable    string // dialog title
	UpdateAvailableMsg string // two %s: the new version, then the running one
	UpdateNow          string
	UpdateLater        string
	UpdateDownloading  string // one %s: the version being downloaded
	UpdateInstalling   string
	UpdateManualNote   string // shown where the app cannot install for you
	UpdateOpenPage     string
	UpdateFailed       string // dialog title
	UpdateFailedMsg    string // one %s: the reason; the running version is untouched
	UpdateUpToDate     string // one %s: the running version
	AboutLastUpdate    string // two %s: the version installed by an update, then when
	ReportBug          string

	// Diagnostics (Settings section + the About banner while logging is on).
	SecDiagnostics           string
	DiagnosticsHint          string
	SaveDiagnosticLogs       string
	OpenDataFolder           string
	DiagnosticsUsage         string // one %s: a human-readable byte size
	DeleteDiagnostics        string
	DeleteDiagnosticsConfirm string
	DebugLogActive           string // the About banner's subtitle

	// Common.
	NoData string // "—"
}

// LangInfo is a selectable language's code and its English/native display name.
type LangInfo struct{ Code, Name string }

var (
	registry   = map[string]Catalog{}
	order      []LangInfo
	active     atomic.Pointer[Catalog]
	activeCode atomic.Pointer[string]
)

// Register adds a language to the catalog. Each language file calls this from its
// init(), so a new language is a single self-contained file. English ("en") is
// treated as the default/fallback: registering it also seeds the active catalog
// (until Set chooses otherwise). Registration order is the display order.
func Register(code, name string, c Catalog) {
	if _, dup := registry[code]; !dup {
		order = append(order, LangInfo{Code: code, Name: name})
	}
	registry[code] = c
	if code == "en" {
		Set("en") // seed the active catalog as soon as English is available
	}
}

// Set selects the active language by code. An unknown or empty code falls back to
// English; a missing English catalog is a programming error (panics) rather than a
// silently blank UI. Safe to call from the UI thread; readers see it atomically.
func Set(lang string) {
	c, ok := registry[lang]
	if !ok {
		en, found := registry["en"]
		if !found {
			panic("i18n: no English catalog registered")
		}
		c = en
		lang = "en"
	}
	active.Store(&c) // &c escapes to the heap; each Set publishes a distinct snapshot
	activeCode.Store(&lang)
}

// ActiveCode returns the code of the active language ("en" before any Set).
// The UI theme consults it to decide whether the embedded CJK font is needed.
// Safe to call from any goroutine.
func ActiveCode() string {
	if p := activeCode.Load(); p != nil {
		return *p
	}
	return "en"
}

// T returns the active catalog (read-only). Call sites read fields directly:
// i18n.T().RefreshNow. The returned pointer is loaded atomically, so it is safe to
// call from any goroutine.
func T() *Catalog {
	if p := active.Load(); p != nil {
		return p
	}
	c := registry["en"] // pre-init fallback (init() normally seeds active first)
	return &c
}

// Languages lists the registered languages in registration order, for a picker.
// It returns a copy so callers can't mutate the registry's order.
func Languages() []LangInfo { return append([]LangInfo(nil), order...) }
