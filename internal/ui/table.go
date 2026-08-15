package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// Sort keys for the status table. The default is sortBySeverity so DOWN
// providers float to the top and the most urgent rows are seen first.
const (
	sortBySeverity    = "severity"
	sortByName        = "name"
	sortByLatency     = "latency"
	sortByLastChecked = "lastchecked"
	sortByUptime      = "uptime"
	sortByCategory    = "category"
)

// sortKeys are the sort keys in display order. The user-facing labels come from
// the active i18n catalog (see sortLabelFor), so only the key is persisted and the
// dropdown text follows the chosen language.
var sortKeys = []string{
	sortBySeverity, sortByName, sortByLatency, sortByLastChecked, sortByUptime, sortByCategory,
}

// sortLabels returns the localized labels for sortKeys, in display order.
func sortLabels() []string {
	out := make([]string, len(sortKeys))
	for i, k := range sortKeys {
		out[i] = sortLabelFor(k)
	}
	return out
}

// sortLabelFor returns the localized display label for a sort key.
func sortLabelFor(key string) string {
	t := i18n.T()
	switch key {
	case sortByName:
		return t.SortName
	case sortByLatency:
		return t.SortLatency
	case sortByLastChecked:
		return t.SortLastChecked
	case sortByUptime:
		return t.SortUptime
	case sortByCategory:
		return t.SortCategory
	default:
		return t.SortSeverity
	}
}

// sortKeyFor returns the sort key for a (localized) display label.
func sortKeyFor(label string) string {
	for _, k := range sortKeys {
		if sortLabelFor(k) == label {
			return k
		}
	}
	return sortBySeverity
}

// tableColWidths are the MINIMUM per-column widths (in px) for the status table.
// A column never renders narrower than this; the surplus a wider window brings is
// shared out by tableColWeights. Order: Name, Status, Last checked, Next poll,
// Latency, Uptime, Regions, Active incidents.
//
// The SUM is a hard budget, not a free choice: the table sits in a scroll, so
// every pixel over what the window can show becomes a horizontal scrollbar. At
// the 1920px-wide screen this app targets as its floor, the side panel takes 24%
// and leaves ~1450px, and columnsLayout adds a padding between every pair — so
// the widths must total well under that. They now come to 1318px (1354 laid
// out), where the old set came to 1426 (1462 laid out) and overflowed.
//
// Per-column floors, all measured rather than guessed (see the header/content
// measurements): every column is at least as wide as the longest translation of
// its own centered title, which is what sets Latency (94px) and Uptime (140px).
// Uptime was 250px for a "100% · últimos 10m" suffix that has since moved into
// the section heading, so it only has to hold the 84px sparkline plus the
// percentage now. Status is wide enough for a one-line state label in every
// catalog that has one ("Nicht erreichbar" is the longest at 107px); the two
// long unknown-states wrap to a second line inside the column rather than
// overflowing it. Active incidents wraps within its width.
var tableColWidths = []float32{190, 160, 124, 88, 96, 150, 190, 320}

// regionsColIndex / incidentsColIndex are indices into tableColWidths, used by
// the region-chip flow layout and the incidents wrap layout to know their widths.
const (
	regionsColIndex   = 6
	incidentsColIndex = 7
)

// tableColWeights is each column's share of the space LEFT OVER once every
// column has its minimum. It is what makes the table fill the window instead of
// parking the surplus in one blank strip at the right edge.
//
// The weights are deliberately NOT equal. Only three columns get anything, and
// they are the three whose content wraps: Name (a long provider name plus a URL
// subtitle), Regions (chips that flow onto a second row), and Active incidents
// (multi-sentence update notes). Widening Status, Last checked, Next poll,
// Latency or Uptime does nothing but push their centered, fixed-size content
// further apart — it would ADD whitespace, which is the complaint this exists to
// answer, not spread it.
//
// Incidents gets the largest share because it is the only column whose content
// is unbounded prose, and the one that reads worst when narrow.
var tableColWeights = []float32{3, 0, 0, 0, 0, 0, 2, 5}

// tableColMaxes stops a column growing past the width at which it stops helping.
// Prose set much wider than ~600px is harder to read, not easier — the eye loses
// the line start on the way back — so past these the surplus is shared evenly
// among ALL columns instead, which keeps the table balanced on a very wide
// screen rather than reopening the same hole somewhere else.
var tableColMaxes = []float32{340, 220, 170, 130, 140, 210, 420, 620}

// columnsLayout lays children out left-to-right, giving every column at least
// its minimum and sharing whatever the window has spare among the columns that
// can use it (see tableColWeights). Every row and the header run this same
// computation against the same width, so they arrive at identical boundaries
// without needing to coordinate.
type columnsLayout struct{ widths []float32 }

func (l columnsLayout) minWidth(i int) float32 {
	if i < len(l.widths) {
		return l.widths[i]
	}
	return 100
}

// resolve returns the effective width of each column for a row of the given
// total width. Below the sum of the minimums it returns the minimums unchanged —
// the table then overflows into its scroll rather than squeezing text until
// columns collide, because a horizontal scrollbar is a far better outcome than
// unreadable overlap.
func (l columnsLayout) resolve(n int, total float32) []float32 {
	out := make([]float32, n)
	pad := theme.Padding()
	sum := pad
	for i := range out {
		out[i] = l.minWidth(i)
		sum += out[i] + pad
	}
	surplus := total - sum
	if surplus <= 0 {
		return out
	}

	// Share by weight, respecting each column's maximum.
	//
	// budget is captured BEFORE the loop and never changes. Computing each share
	// against the running surplus instead — which is the obvious way to write
	// this — makes every column after the first take its fraction of an already
	// reduced number, so the shares silently come up short and a remainder is
	// left over that the weights were supposed to have spent.
	var weight float32
	for i := range out {
		weight += colWeight(i)
	}
	if weight > 0 {
		budget := surplus
		for i := range out {
			w := colWeight(i)
			if w == 0 {
				continue
			}
			give := budget * w / weight
			if room := colMax(i) - out[i]; give > room {
				give = room
			}
			if give > 0 {
				out[i] += give
				surplus -= give
			}
		}
	}
	// Anything still left (every weighted column is at its maximum) is spread
	// evenly, so a very wide window widens the whole table rather than leaving
	// the leftover pooled at one edge.
	if surplus > 0 && n > 0 {
		each := surplus / float32(n)
		for i := range out {
			out[i] += each
		}
	}
	return out
}

func colWeight(i int) float32 {
	if i < len(tableColWeights) {
		return tableColWeights[i]
	}
	return 0
}

func colMax(i int) float32 {
	if i < len(tableColMaxes) {
		return tableColMaxes[i]
	}
	return 1 << 20
}

func (l columnsLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	pad := theme.Padding()
	w, h := pad, float32(0)
	for i, o := range objs {
		w += l.minWidth(i) + pad
		if ms := o.MinSize(); ms.Height > h {
			h = ms.Height
		}
	}
	return fyne.NewSize(w, h)
}

func (l columnsLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	pad := theme.Padding()
	widths := l.resolve(len(objs), size.Width)
	publishColumnWidths(widths)
	x := pad
	for i, o := range objs {
		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(widths[i], size.Height))
		x += widths[i] + pad
	}
}

// tableHeader returns the fixed header row.
func (c *Controller) tableHeader() fyne.CanvasObject {
	t := i18n.T()
	return headerRow(t.ColName, t.ColStatus, t.ColLastChecked, t.ColNextPoll, t.ColLatency, t.ColUptime, t.ColRegions, t.ColIncidents)
}

// rowSpec is the rendering-agnostic description of one status-table row.
type rowSpec struct {
	id        string
	name      string
	subtitle  string // type / endpoint, shown under the name
	linkURL   string // when set, the name is a hyperlink to this (a status page)
	state     statusState
	when      string
	latency   string
	regions   []string
	incidents string // plain-text incidents rendering (filtering + change detection)
	// incidentData carries the structured incidents behind `incidents` so the
	// cell can render them with mixed styling (bold title / bold field labels);
	// nil for rows whose incidents cell is plain text (custom URL checks).
	incidentData []providers.Incident
	retry        func() // optional Retry action (error states)
	activate     func() // optional row-click action (incident drill-down)
}

// rowEntry pairs a rowSpec with the sort keys used to order the flat table.
type rowEntry struct {
	spec   rowSpec
	sev    int           // severity rank, higher = needs more attention
	lat    time.Duration // response latency (0 = unknown)
	when   time.Time     // last-checked time (zero = never)
	uptime float64       // fraction up in [0,1], or -1 when unknown
	group  int           // category group for sortByCategory (0 = connectivity)
	isConn bool
}

// downRank maps a status state to a sort weight so that DOWN rows float to the
// top under the default severity sort.
func downRank(s statusState) int {
	switch s {
	case stateOutage:
		return 5
	case stateUnreachable:
		return 4
	case stateDegraded:
		return 3
	case stateNoFeed:
		return 2
	case stateOK:
		return 1
	default: // pending
		return 0
	}
}

// buildRows assembles, filters, and sorts the flat status table, and refreshes
// the loading indicator and tray aggregate. Must run on the Fyne thread. Row
// widgets are cached per check id and updated in place (see rowWidget); the
// returned objects are the section boxes to display, and layoutChanged reports
// whether any reused row swapped a layout-affecting cell.
func (c *Controller) buildRows() (objs []fyne.CanvasObject, layoutChanged bool) {
	interest := c.regionsOfInterest()
	// Snapshot the active region de-selections ONCE per rebuild: sampleUp consults
	// it for every history sample of every row, and c.mu is non-reentrant, so a
	// per-sample lock would contend with the pollers and risk self-deadlock. The
	// sorted key snapshot doubles as the muting fingerprint for the uptime cache.
	mKeys := c.muteKeys()
	isMuted := mutePredicate(mKeys)
	mSig := muteSig(mKeys)
	// Uptime windows: providers (and custom URLs) show a fixed day of history in
	// time buckets; connectivity keeps the short interval×20 "what just
	// happened" slice. The raw intervals also feed the section cadence label.
	provInterval := c.providerInterval()
	connInterval := c.connInterval()
	provWindow := providerUptimeWindow
	connWindow := connInterval * uptimeWindowSamples

	c.mu.Lock()
	filter := strings.ToLower(strings.TrimSpace(c.filter))
	sortKey := c.sortKey
	sortRev := c.sortAsc
	nextProv := c.nextProviderPoll
	nextConn := c.nextConnPoll
	states := make(map[string]provState, len(c.provStates))
	for k, v := range c.provStates {
		states[k] = v
	}
	c.provCountdowns = c.provCountdowns[:0]
	c.connCountdowns = c.connCountdowns[:0]
	c.mu.Unlock()

	var entries []rowEntry
	worst := statePending
	anyProviderChecked := false
	enabledProviderCount := 0

	// --- Connectivity entries ---
	if eng := c.currentEngine(); eng != nil {
		for _, s := range eng.Snapshot() {
			checked := !s.LastChecked.IsZero()
			st := connState(checked, s.Reachable, s.LossPercent)
			subtitle := "ICMP · " + s.Host
			if s.Mode == monitor.ModeTCP {
				subtitle = "TCP:443 · " + s.Host
			}
			latency := "—"
			if checked {
				if s.Reachable {
					latency = formatLatency(s.RTT)
				} else {
					latency = i18n.T().LatencyUnreachable
				}
				worst = worst.worse(st)
			}
			up, n := c.regionUptime(s.ID, connWindow, false, isMuted, time.Now())
			// Capture this round's status for the drill-down. rowWidget.update
			// reassigns activate on every refresh, so the panel always opens with
			// the CURRENT reading rather than the one from row creation.
			target := s
			entries = append(entries, rowEntry{
				spec: rowSpec{
					id: s.ID, name: s.Name, subtitle: subtitle, state: st,
					when: formatTime(s.LastChecked), latency: latency,
					activate: func() { c.showConnDetail(target) },
				},
				sev: downRank(st), lat: s.RTT, when: s.LastChecked,
				uptime: uptimeOrUnknown(up, n), group: 0, isConn: true,
			})
		}
	}

	// --- Provider entries ---
	for _, p := range c.enabledProviders() {
		enabledProviderCount++
		st, ok := states[p.ID]
		if ok && st.checked {
			anyProviderChecked = true
		}
		state := providerStatusState(st, ok, interest, isMuted)
		switch state {
		case stateOK, stateDegraded, stateOutage:
			worst = worst.worse(state)
		}
		spec := c.providerSpec(p, st, ok, interest, isMuted)
		up, n := c.regionUptime(p.ID, provWindow, true, isMuted, time.Now())
		entries = append(entries, rowEntry{
			spec:   spec,
			sev:    downRank(state),
			lat:    st.latency,
			when:   st.when,
			uptime: uptimeOrUnknown(up, n),
			group:  categoryGroup(p.Category),
		})
	}

	// --- Custom URL check entries ---
	for _, e := range c.urlEntries(provWindow, isMuted) {
		switch e.spec.state {
		case stateOK, stateDegraded, stateOutage, stateUnreachable:
			worst = worst.worse(e.spec.state)
		}
		entries = append(entries, e)
	}

	// Filter, then sort. The chosen sort orders rows WITHIN each section; the
	// sections themselves always render in the same fixed order (below).
	entries = filterEntries(entries, filter)
	sortEntries(entries, sortKey, sortRev)

	// Resolve each row against the per-id widget cache (created once, then
	// updated in place), bucketed by its section group. Countdowns are collected
	// in render order for the ticker, exactly as before.
	now := time.Now()
	provCtx := rowCtx{
		interest: interest, isMuted: isMuted, muteSig: mSig, now: now,
		nextPoll: nextProv, interval: provInterval, window: provWindow, bucketed: true,
	}
	connCtx := rowCtx{
		interest: interest, isMuted: isMuted, muteSig: mSig, now: now,
		nextPoll: nextConn, interval: connInterval, window: connWindow, bucketed: false,
	}
	byGroup := map[int][]fyne.CanvasObject{}
	visible := make(map[string]bool, len(entries))
	for _, e := range entries {
		ctx := provCtx
		if e.isConn {
			ctx = connCtx
		}
		rw, swapped := c.ensureRow(e.spec, ctx)
		layoutChanged = layoutChanged || swapped
		visible[e.spec.id] = true
		c.mu.Lock()
		if e.isConn {
			c.connCountdowns = append(c.connCountdowns, rw.countdown)
		} else {
			c.provCountdowns = append(c.provCountdowns, rw.countdown)
		}
		c.mu.Unlock()
		byGroup[e.group] = append(byGroup[e.group], rw.row)
	}
	// Evict rows that are no longer displayed (check disabled/removed, filtered
	// out) so the cache tracks the visible set instead of growing.
	for id := range c.rowCache {
		if !visible[id] {
			delete(c.rowCache, id)
		}
	}

	rows := c.groupedRows(byGroup, provInterval, connInterval)

	if len(rows) == 0 {
		// Cache the placeholder too: the refresh loop still ticks every second in
		// an empty table, and recreating the placeholder each tick would churn.
		if filter != "" {
			if c.noMatchBox == nil || filter != c.noMatchFilter {
				c.noMatchBox, c.noMatchFilter = noMatchState(filter), filter
			}
			rows = append(rows, c.noMatchBox)
		} else {
			if c.emptyBox == nil {
				c.emptyBox = emptyState()
			}
			rows = append(rows, c.emptyBox)
		}
	}

	c.setLoading(enabledProviderCount > 0 && !anyProviderChecked)
	c.updateTray(worst, traySummary(worst))
	return rows, layoutChanged
}

// sectionGroup pairs a section's group key with its localized title, in the
// fixed display order: Connectivity, Custom URLs, then each provider category.
type sectionGroup struct {
	key   int
	title string
}

// sectionOrder returns the section groups in their fixed render order. The group
// keys mirror the values assigned in buildRows/urlEntries (connectivity = 0,
// provider categories = CategoryOrder index + 1, custom URLs = len+2).
func sectionOrder() []sectionGroup {
	t := i18n.T()
	out := []sectionGroup{
		{key: sectionConnectivity, title: t.SectionConnectivity},
		{key: sectionOther(), title: t.SectionOther},
		{key: sectionCustomURLs(), title: t.SectionCustomURLs},
	}
	for i, cat := range providers.CategoryOrder {
		out = append(out, sectionGroup{key: sectionForCategoryIndex(i), title: string(cat)})
	}
	return out
}

// Section keys. They were bare arithmetic (`len(CategoryOrder)+2`) restated in
// two files, and the two disagreed in a way that made rows VANISH:
// categoryGroup's fallback for an unrecognized category returned
// len(CategoryOrder)+1, which sectionOrder never listed — so the row was
// assigned to a section that is never rendered. Naming them makes the buckets
// enumerable and gives the fallback somewhere real to land.
const sectionConnectivity = 0

// sectionForCategoryIndex maps a CategoryOrder index to its section key.
func sectionForCategoryIndex(i int) int { return i + 1 }

// sectionOther is the catch-all for a provider whose category is not in
// CategoryOrder. It is rendered, so such a provider is visible rather than lost.
func sectionOther() int { return len(providers.CategoryOrder) + 1 }

// sectionCustomURLs holds the user-defined URL checks, last.
func sectionCustomURLs() int { return len(providers.CategoryOrder) + 2 }

// groupedRows assembles the per-section row buckets into titled, bordered group
// boxes in the fixed section order. Each title carries the section's poll cadence
// ("· every 30s") once, instead of repeating it on every row. Empty sections are
// omitted. The boxes themselves are cached and updated in place (see
// ensureSectionBox) so the per-second refresh creates no new containers. Any
// rows whose group key isn't in the fixed order (e.g. a provider with an
// unrecognized category, categoryGroup's fallback bucket) are appended verbatim
// afterwards so nothing is ever silently dropped from the table.
func (c *Controller) groupedRows(byGroup map[int][]fyne.CanvasObject, provInterval, connInterval time.Duration) []fyne.CanvasObject {
	var out []fyne.CanvasObject
	rendered := map[int]bool{}
	shown := map[int]bool{}
	for _, sec := range sectionOrder() {
		rendered[sec.key] = true
		rows := byGroup[sec.key]
		if len(rows) == 0 {
			continue
		}
		interval := provInterval
		window := providerUptimeWindow
		if sec.key == 0 { // connectivity: faster cadence, short rolling window
			interval = connInterval
			window = connInterval * uptimeWindowSamples
		}
		shown[sec.key] = true
		out = append(out, c.ensureSectionBox(sec.key, sectionTitle(sec.title, interval, window), rows))
	}
	// Drop cached boxes for sections that vanished (every check in them disabled
	// or filtered out) so they don't pin their evicted rows in memory.
	for key := range c.sectionBoxes {
		if !shown[key] {
			delete(c.sectionBoxes, key)
		}
	}
	leftover := make([]int, 0)
	for key := range byGroup {
		if !rendered[key] {
			leftover = append(leftover, key)
		}
	}
	sort.Ints(leftover)
	for _, key := range leftover {
		out = append(out, byGroup[key]...)
	}
	return out
}

// sectionTitle appends the section's poll cadence and its uptime-strip range to
// its name, e.g. "Cloud · every 30s · uptime: last 10m" — stating both once per
// section instead of on every row's uptime cell. The range was previously
// stated nowhere, leaving the strip's meaning ambiguous.
func sectionTitle(name string, interval, window time.Duration) string {
	if span := history.HumanizeSpan(interval); span != "" {
		name += " · " + fmt.Sprintf(i18n.T().CheckEvery, span)
	}
	if span := history.HumanizeSpan(window); span != "" {
		name += " · " + fmt.Sprintf(i18n.T().UptimeRange, span)
	}
	return name
}

// uptimeOrUnknown returns the uptime fraction, or -1 when there are no samples.
func uptimeOrUnknown(up float64, samples int) float64 {
	if samples == 0 {
		return -1
	}
	return up
}

// categoryGroup maps a provider category to a sort group after connectivity (0).
func categoryGroup(cat providers.Category) int {
	for i, c := range providers.CategoryOrder {
		if c == cat {
			return sectionForCategoryIndex(i)
		}
	}
	return sectionOther()
}

// filterEntries keeps rows whose name, subtitle, incidents, or regions contain
// the (already lower-cased) filter text. An empty filter keeps everything.
func filterEntries(entries []rowEntry, filter string) []rowEntry {
	if filter == "" {
		return entries
	}
	out := entries[:0]
	for _, e := range entries {
		hay := strings.ToLower(e.spec.name + " " + e.spec.subtitle + " " + e.spec.incidents + " " + strings.Join(e.spec.regions, " "))
		if strings.Contains(hay, filter) {
			out = append(out, e)
		}
	}
	return out
}

// sortEntries orders entries by the given key. Each key has a natural direction
// (e.g. severity worst-first); rev reverses it.
func sortEntries(entries []rowEntry, key string, rev bool) {
	natural := func(i, j int) bool {
		a, b := entries[i], entries[j]
		switch key {
		case sortByName:
			return a.spec.name < b.spec.name
		case sortByLatency:
			if a.lat != b.lat {
				return latencyRank(a.lat) > latencyRank(b.lat) // high → low, unknown last
			}
		case sortByLastChecked:
			if !a.when.Equal(b.when) {
				return a.when.After(b.when) // newest first; zero (never) sorts last
			}
		case sortByUptime:
			if a.uptime != b.uptime {
				return uptimeRank(a.uptime) < uptimeRank(b.uptime) // low → high, unknown last
			}
		case sortByCategory:
			if a.group != b.group {
				return a.group < b.group
			}
		default: // sortBySeverity
			if a.sev != b.sev {
				return a.sev > b.sev // worst first
			}
		}
		return a.spec.name < b.spec.name // stable tiebreak
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if rev {
			return natural(j, i)
		}
		return natural(i, j)
	})
}

// latencyRank treats an unknown latency (0) as the lowest so it sorts last under
// a high→low ordering.
func latencyRank(d time.Duration) time.Duration {
	if d <= 0 {
		return -1
	}
	return d
}

// uptimeRank treats unknown uptime (-1) as fully up so it sorts last under a
// low→high ordering (problematic rows surface first).
func uptimeRank(u float64) float64 {
	if u < 0 {
		return 1.1
	}
	return u
}

// providerSpec builds the rowSpec for a provider, scoped to the regions of
// interest, including the incident drill-down activation and any Retry action.
func (c *Controller) providerSpec(p providers.Provider, st provState, ok bool, interest []string, isMuted func(region string) bool) rowSpec {
	activate := func() { c.showProviderDetail(p, st, ok, interest) }
	base := rowSpec{
		id: p.ID, name: p.Name,
		linkURL: providers.StatusPageURL(p.ID), // name → real status page
		latency: formatLatency(st.latency), activate: activate,
	}
	switch {
	case !ok || !st.checked:
		base.state = statePending
		base.when = "—"
		base.latency = "—"
		return base
	case st.skipped:
		base.state = stateNoFeed
		base.when = formatTime(st.when)
		base.latency = "—"
		base.incidents = i18n.T().NoPublicStatusFeed
		return base
	case st.err != nil:
		base.state = stateNoFeed
		base.when = formatTime(st.when)
		base.incidents = feedFailureMessage(st.err)
		base.retry = func() { c.recheckProvider(p) }
		return base
	}

	base.state = providerState(effectiveSeverity(st.res, interest, isMuted), true)
	base.when = formatTime(st.when)
	base.regions = st.res.AffectedRegions()
	base.incidentData = effectivelyActiveIncidents(st.res.IncidentsInRegions(interest), isMuted)
	base.incidents = incidentCellText(base.incidentData, time.Now())
	return base
}

// feedFailureMessage is the sentence a failed provider check shows in the row
// and at the top of its drill-down. It names WHY the feed could not be read,
// because the four causes have four different answers: a rejected certificate
// is the reader's own network, an HTTP status is the provider, an unreadable
// body is this app, and a timeout could be either. The old single sentence
// ("Status feed unavailable — service health unknown.") is kept as the fallback
// for a cause we cannot name, which is the only case where it is honest.
//
// Every one of these still means health UNKNOWN — never an outage.
func feedFailureMessage(err error) string {
	t := i18n.T()
	switch kind, code := providers.ClassifyFeedFailure(err); kind {
	case providers.FeedFailTLS:
		return t.FeedErrCertificate
	case providers.FeedFailDNS:
		return t.FeedErrDNS
	case providers.FeedFailTimeout:
		return t.FeedErrTimeout
	case providers.FeedFailHTTP:
		return fmt.Sprintf(t.FeedErrHTTP, code)
	case providers.FeedFailUnparseable:
		return t.FeedErrUnparseable
	default:
		return t.StatusFeedUnavailable
	}
}

// incidentNoteMax caps the latest-update note shown in the table cell; feeds
// like AWS post multi-paragraph updates and the full text belongs in the
// click-through detail, not in a table row.
const incidentNoteMax = 280

// incidentCellText renders the Active-incidents cell: one fenced block per
// incident — an incidentRule, the incident label and its severity, then (when
// the feed provides them) the start / last-update times and the latest update
// note — so the row itself answers "since when, and what's the latest word"
// without opening the detail.
//
// It must stay a faithful transcript of what incidentBlocks DRAWS, because
// rowWidget.update diffs this string to decide whether to rebuild the cell: a
// field rendered here but not there (or vice versa) either rebuilds the cell
// for nothing or, worse, leaves a changed cell stale. That is why the ages are
// in here too — they are what makes the cell re-render as an incident gets
// older.
func incidentCellText(incidents []providers.Incident, now time.Time) string {
	t := i18n.T()
	var lines []string
	for _, inc := range incidents {
		label := inc.Label()
		if label == "" {
			continue
		}
		lines = append(lines, incidentRule, label, fmt.Sprintf(t.SeverityLabel, inc.Severity.String()))
		note := strings.TrimSpace(inc.Note)
		if r := []rune(note); len(r) > incidentNoteMax {
			note = strings.TrimSpace(string(r[:incidentNoteMax])) + "…"
		}
		// Newest information first: the last update (with its note inline),
		// then when the incident started.
		var detail []string
		switch {
		case !inc.Updated.IsZero() && note != "":
			detail = append(detail, agedPrefix(t.UpdatedLabel, inc.Updated, now)+" "+formatIncidentTime(inc.Updated)+" - "+note)
		case !inc.Updated.IsZero():
			detail = append(detail, agedPrefix(t.UpdatedLabel, inc.Updated, now)+" "+formatIncidentTime(inc.Updated))
		case note != "":
			detail = append(detail, note)
		}
		if !inc.Started.IsZero() {
			detail = append(detail, agedPrefix(t.StartedLabel, inc.Started, now)+" "+formatIncidentTime(inc.Started))
		}
		lines = append(lines, detail...)
	}
	if len(lines) > 0 {
		lines = append(lines, incidentRule) // closes the last fence
	}
	return strings.Join(lines, "\n")
}

// nameCell renders the name. For a provider (linkURL set) the name is a clickable
// hyperlink that opens the real status page; otherwise it's a bold label. An
// optional subtitle (endpoint/type) is shown beneath.
//
// Both are CENTERED in the column, like every other cell: columnsLayout resizes
// each cell to the full column width, so a centered label centers on the column
// axis its header is centered on.
func nameCell(name, subtitle, linkURL string) fyne.CanvasObject {
	var title fyne.CanvasObject = widget.NewLabelWithStyle(name, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	if linkURL != "" {
		if u, err := url.Parse(linkURL); err == nil {
			link := widget.NewHyperlink(name, u)
			link.TextStyle = fyne.TextStyle{Bold: true}
			link.Alignment = fyne.TextAlignCenter
			title = link
		}
	}
	if subtitle == "" {
		return title
	}
	sub := widget.NewLabelWithStyle(subtitle, fyne.TextAlignCenter, fyne.TextStyle{})
	sub.Wrapping = fyne.TextWrapWord
	return container.NewVBox(title, sub)
}

// traySummary renders the worst state as a short, localized tray status line.
func traySummary(s statusState) string {
	t := i18n.T()
	switch s {
	case stateOK:
		return t.TrayAllOperational
	case stateDegraded:
		return t.StatusDegraded
	case stateOutage:
		return t.StatusOutage
	case stateUnreachable:
		return t.StatusUnreachable
	case stateNoFeed:
		return t.StatusNoFeed
	default:
		return t.StatusChecking
	}
}

// headerRow builds a header spanning the table's columns, sized above body text.
func headerRow(cols ...string) fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, len(cols))
	for i, col := range cols {
		objs[i] = columnHeader(col)
	}
	return container.New(columnsLayout{tableColWidths}, objs...)
}

// emptyState renders the table's empty state: a message plus a next action.
func emptyState() fyne.CanvasObject {
	t := i18n.T()
	icon := widget.NewIcon(theme.InfoIcon())
	msg := widget.NewLabelWithStyle(t.NoChecksEnabled, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	hint := widget.NewLabel(t.NoChecksHint)
	hint.Wrapping = fyne.TextWrapWord
	return container.NewVBox(container.NewHBox(icon, msg), hint)
}

// noMatchState is shown when a filter matches no rows.
func noMatchState(filter string) fyne.CanvasObject {
	icon := widget.NewIcon(theme.SearchIcon())
	msg := widget.NewLabelWithStyle(fmt.Sprintf(i18n.T().NoMatch, filter), fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	return container.NewVBox(container.NewHBox(icon, msg))
}
