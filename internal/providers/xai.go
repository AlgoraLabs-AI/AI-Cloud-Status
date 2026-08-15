package providers

import (
	"encoding/xml"
	"errors"
	"regexp"
	"strings"
	"time"
)

// init self-registers the xAI adapter and the xAI provider.
func init() {
	RegisterParser(KindXAI, func(_ Provider, data []byte) (Result, error) {
		return parseXAI(data)
	})
	Register(Provider{
		ID: "xai", Name: "xAI", Category: CategoryAI, Kind: KindXAI,
		URL: xaiFeedURL,
	})
}

// xaiFeedURL is xAI's status feed. status.x.ai is NOT an Atlassian Statuspage:
// its /api/v2/summary.json (and the page root) answer 403, which is why xAI
// previously showed a permanent "status feed unavailable". The RSS feed the
// page links to is public and unauthenticated.
const xaiFeedURL = "https://status.x.ai/feed.xml"

// xaiStaleAfter mirrors awsStaleAfter: an item still marked open but silent
// for this long is demoted to minor rather than pinning the row red forever.
// The feed is an incident HISTORY (100+ entries, nearly all resolved), so a
// perpetually-open straggler must not read as a live outage.
const xaiStaleAfter = StaleAfter

// xaiRSS models the xAI status feed. Each item carries its state twice: as
// <category> elements, and as labelled "Status:"/"Severity:" lines inside the
// HTML description. The labelled form is parsed first because it is
// self-describing — the two <category> values (one severity, one status)
// arrive in no declared order and cannot be told apart positionally.
// XMLName constrains the root element — see the note on azureRSS. Without it
// any well-formed document (an error page, a WAF interstitial) parses as an
// empty, healthy feed.
type xaiRSS struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []xaiItem `xml:"item"`
	} `xml:"channel"`
}

type xaiItem struct {
	Title       string   `xml:"title"`
	Description string   `xml:"description"`
	Categories  []string `xml:"category"`
	PubDate     string   `xml:"pubDate"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
}

var (
	// xaiStatusLine / xaiSeverityLine read the labelled state out of the
	// description CDATA (e.g. "<h3>Status: RESOLVED</h3>", "<p>Severity: available</p>").
	xaiStatusLine   = regexp.MustCompile(`(?i)Status:\s*([A-Za-z_]+)`)
	xaiSeverityLine = regexp.MustCompile(`(?i)Severity:\s*([A-Za-z_]+)`)
	// xaiScopePrefix captures the bracketed scope every title starts with,
	// e.g. "[API (us-east-1.api.x.ai)]" or "[Grok (Web)]".
	xaiScopePrefix = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*`)
	// xaiUpdateBlock isolates the newest update block in the description so
	// the note shows the latest message rather than the whole incident log.
	xaiUpdateBlock = regexp.MustCompile(`(?is)<div>(.*?)</div>`)
	// xaiUpdateStamp reads an update block's leading "<strong>date</strong>".
	xaiUpdateStamp = regexp.MustCompile(`(?is)<strong>\s*(.*?)\s*</strong>`)
)

// parseXAI surfaces every UNRESOLVED item in the feed as an incident. The feed
// is a rolling history whose entries are overwhelmingly resolved, so an item
// counts as active only when nothing marks it resolved — a positive
// resolved-signal test, not an enumeration of open states, so an unfamiliar
// open status ("investigating", "identified", …) can never be silently
// swallowed as if it were closed.
func parseXAI(data []byte) (Result, error) {
	return parseXAIAt(data, time.Now())
}

// parseXAIAt is parseXAI with an injectable clock for the stale-item filter.
func parseXAIAt(data []byte, now time.Time) (Result, error) {
	var feed xaiRSS
	if err := xml.Unmarshal(decodeFeed(data), &feed); err != nil {
		return Result{}, err
	}

	// Zero items IS anomalous here, unlike Azure — do not "unify" these two.
	//
	// xAI's feed is a rolling incident HISTORY: 105 items on the live feed when
	// last measured, nearly all resolved. It never empties, so an empty channel
	// means a truncated body, a schema change or an error document, not a healthy
	// service. Azure's feed is the opposite — it carries only OPEN items, so zero
	// is its normal all-clear state and rejecting it there broke every healthy
	// poll. Same shape, opposite meaning; the discriminator is what the feed is
	// FOR, which cannot be inferred from the XML.
	if len(feed.Channel.Items) == 0 {
		return Result{}, errors.New("xai: feed parsed but carries no items")
	}

	res := Result{Severity: SevNone}
	for _, item := range feed.Channel.Items {
		if xaiResolved(item) {
			continue
		}
		summary := strings.TrimSpace(xaiScopePrefix.ReplaceAllString(item.Title, ""))
		if summary == "" {
			summary = strings.TrimSpace(item.Title)
		}
		if summary == "" {
			continue // nothing identifiable to show
		}

		sev := xaiSeverityOf(item.Description, item.Categories)
		started := xaiTime(item.PubDate)
		updated, note := xaiLatestUpdate(item.Description)
		if updated.IsZero() {
			updated = started
		}
		// Demote a straggler: still open but untouched for xaiStaleAfter. Only
		// a KNOWN timestamp can declare it stale — missing times fail open and
		// keep full severity, never downplaying a real incident.
		if !updated.IsZero() && now.Sub(updated) > xaiStaleAfter {
			sev = SevMinor
		}
		if sev > res.Severity {
			res.Severity = sev
		}
		res.Incidents = append(res.Incidents, Incident{
			Summary:    summary,
			Severity:   sev,
			Regions:    xaiRegions(item.Title),
			Components: xaiComponents(item.Title),
			Started:    started,
			Updated:    updated,
			Note:       note,
			URL:        strings.TrimSpace(item.Link),
		})
	}
	return res, nil
}

// xaiResolved reports whether anything in the item marks it closed — the
// labelled "Status:" line or a <category>. Both are checked because either
// alone going missing must not resurrect a long-closed incident.
func xaiResolved(item xaiItem) bool {
	if isXAIResolvedWord(xaiField(xaiStatusLine, item.Description)) {
		return true
	}
	for _, c := range item.Categories {
		if isXAIResolvedWord(c) {
			return true
		}
	}
	return false
}

// isXAIResolvedWord reports whether a status word means the incident is over.
func isXAIResolvedWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "resolved", "completed", "closed", "postmortem":
		return true
	}
	return false
}

// xaiField pulls a labelled value out of the description. Returns "" when the
// pattern does not match. Description-only by design: there is no category
// fallback here, because ranking unordered categories requires knowing what the
// value MEANS, which only the severity path can do (see xaiSeverityOf).
func xaiField(re *regexp.Regexp, desc string) string {
	if m := re.FindStringSubmatch(desc); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// xaiSeverityOf reads an item's severity: the labelled "Severity:" line when the
// description carries one, otherwise the WORST severity among the <category>
// elements.
//
// Worst-of, not first: this file's own doc notes the two <category> values
// "arrive in no declared order and cannot be told apart positionally". For an
// open item they are one severity word and one status word, and every status
// word (investigating, identified, monitoring, mitigating) falls through
// xaiSeverity to SevMinor while the severity words reach SevMajor. Taking the
// first non-resolved category therefore under-reported a full outage as
// Degraded whenever the status category happened to be emitted first — below
// the alert threshold, so no notification at all. The active fixture orders
// them the other way, which is why the tests never caught it.
func xaiSeverityOf(desc string, categories []string) Severity {
	if labelled := xaiField(xaiSeverityLine, desc); labelled != "" {
		return xaiSeverity(labelled)
	}
	worst, sawAny := SevNone, false
	for _, c := range categories {
		if c = strings.TrimSpace(c); c == "" || isXAIResolvedWord(c) {
			continue
		}
		if sev := xaiSeverity(c); !sawAny || sev > worst {
			worst, sawAny = sev, true
		}
	}
	if !sawAny {
		// No labelled line and no usable category, but the item is unresolved —
		// xaiSeverity's contract is that an open item is never SevNone.
		return xaiSeverity("")
	}
	return worst
}

// xaiSeverity maps an xAI severity word onto a Severity. An unresolved item is
// never SevNone — it is an open incident by definition — so an "available"
// severity (the service still serves while something is being investigated)
// floors at minor rather than reading as fully operational. Unknown words are
// treated conservatively as minor, mirroring the Statuspage adapter.
func xaiSeverity(word string) Severity {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "unavailable", "outage", "major_outage", "major", "down", "critical":
		return SevMajor
	default:
		// available, degraded, degraded_performance, partial_outage,
		// maintenance, unknown — a real but non-fatal degradation.
		return SevMinor
	}
}

// xaiScope returns the bracketed scope label a title starts with, e.g.
// "API (us-east-1.api.x.ai)" or "Grok (Web)". Empty when the title has none.
func xaiScope(title string) string {
	if m := xaiScopePrefix.FindStringSubmatch(title); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// xaiRegions returns the region a title's scope names, if any. xAI encodes it
// as an AWS-style code inside the API host ("API (us-east-1.api.x.ai)"); the
// app-facing scopes ("Grok (Web)", "API Console") name no region and are
// therefore global, which is correct — they are not region-partitioned.
func xaiRegions(title string) []string {
	if r := awsRegionInName.FindString(xaiScope(title)); r != "" {
		return []string{r}
	}
	return nil
}

// xaiComponents returns the affected component for an item: its scope label,
// which is what xAI actually partitions status by. Nil when the title carries
// no scope, so the detail panel simply omits the row.
func xaiComponents(title string) []string {
	if s := xaiScope(title); s != "" {
		return []string{s}
	}
	return nil
}

// xaiLatestUpdate returns the newest update's timestamp and message from the
// description. The feed renders updates newest-first, but the newest is picked
// by timestamp so an out-of-order feed cannot surface a stale note.
func xaiLatestUpdate(desc string) (time.Time, string) {
	var bestAt time.Time
	bestNote, sawAny := "", false
	for _, block := range xaiUpdateBlock.FindAllStringSubmatch(desc, -1) {
		body := block[1]
		stamp := ""
		if m := xaiUpdateStamp.FindStringSubmatch(body); len(m) > 1 {
			stamp = m[1]
		}
		at := xaiTime(stamp)
		// The message is the block minus its timestamp line.
		note := plainNote(xaiUpdateStamp.ReplaceAllString(body, ""))
		if note == "" {
			continue
		}
		if !sawAny || (!at.IsZero() && at.After(bestAt)) {
			bestAt, bestNote, sawAny = at, note, true
		}
	}
	return bestAt, bestNote
}

// xaiTimeLayouts are the timestamp shapes seen in the feed: RFC 1123 with a
// named GMT zone in both pubDate and the per-update <strong> stamps.
var xaiTimeLayouts = []string{
	time.RFC1123,
	time.RFC1123Z,
	time.RFC3339,
}

// xaiTime parses an xAI feed timestamp, returning the zero time when absent or
// in an unrecognized shape (the incident is still surfaced — only the
// timestamp is missing).
func xaiTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range xaiTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
