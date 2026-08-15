package providers

import (
	"encoding/xml"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

// init self-registers the Azure adapter and the Azure provider.
func init() {
	RegisterParser(KindAzure, func(_ Provider, data []byte) (Result, error) {
		return parseAzure(data)
	})
	Register(Provider{
		ID: "azure", Name: "Azure", Category: CategoryCloud, Kind: KindAzure,
		URL: azureFeedURL,
	})
}

// azureFeedURL is the canonical Azure status RSS feed — the one the Azure status
// page itself links to.
//
// It is deliberately NOT "https://status.azure.com/en-us/status/feed/". That
// host redirects to a GEO-ROUTED instance of the status service, and a degraded
// instance serves a structurally valid feed containing ZERO items. That is
// indistinguishable from "all clear", so the app reported Azure healthy straight
// through a real outage: on 2026-07-23 the West US network incident "Issues
// connecting to resources in West US" was live on this feed while
// status.azure.com (routed to the West US instance — the very region that was
// down) returned an empty channel for hours.
const azureFeedURL = "https://rssfeed.azure.status.microsoft/en-us/status/feed/"

// azureRSS models the Azure status RSS feed. Azure carries the incident's
// service area and region as <category> elements alongside the free-text title
// and HTML description, and its last update time as pubDate.
// XMLName constrains the root element. Without it encoding/xml happily
// unmarshals ANY well-formed document into this struct — an S3-style
// <Error><Code>AccessDenied</Code></Error>, an HTML error page, a WAF
// interstitial — yielding zero items and a nil error, which Checker.Check reads
// as "feed parsed fine, everything operational". That is the failure mode that
// let ACS report Azure healthy straight through the 2026-07-23 West US outage;
// the URL was fixed then, the parser was not.
type azureRSS struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		// Title is the structural anchor: a real Azure feed always carries
		// "<title>Azure Status</title>", and an ITEM count cannot be used for
		// this because zero items is Azure's normal healthy state.
		Title string      `xml:"title"`
		Items []azureItem `xml:"item"`
	} `xml:"channel"`
}

type azureItem struct {
	Title       string   `xml:"title"`
	Description string   `xml:"description"`
	Categories  []string `xml:"category"`
	PubDate     string   `xml:"pubDate"`
	Link        string   `xml:"link"`
}

// parseAzure treats any open item in the feed as a degradation. Items whose
// title indicates resolution are ignored. Each open item is tagged with any
// Azure region named in its title, description or <category> elements, and
// carries the service-area categories, last update time and latest note so the
// detail panel can show them.
func parseAzure(data []byte) (Result, error) {
	return parseAzureAt(data, time.Now())
}

// parseAzureAt is parseAzure with an injectable clock for the stale-item
// demotion, mirroring parseAWSAt / parseXAIAt.
func parseAzureAt(data []byte, now time.Time) (Result, error) {
	var feed azureRSS
	if err := xml.Unmarshal(decodeFeed(data), &feed); err != nil {
		return Result{}, err
	}

	// Require the CHANNEL, not items.
	//
	// Zero items is Azure's normal all-clear state — the live feed is 593 bytes
	// with an empty channel whenever nothing is wrong (captured verbatim in
	// testdata/azure_healthy_empty.xml). An earlier version of this guard
	// rejected that, which turned every healthy poll into "Status feed
	// unavailable".
	//
	// It is worth being precise about why, because azureFeedURL's comment above
	// reads like it argues the opposite. It does not: it says an empty channel is
	// "indistinguishable from all clear", and that the 2026-07-23 miss came from
	// a GEO-ROUTED instance serving one — a wrong-URL problem, fixed by pinning
	// the canonical host. The emptiness was the symptom, not a detectable signal.
	// Nothing in the payload can separate "healthy" from "wrong instance", so the
	// parser must not pretend otherwise.
	//
	// What the channel title CAN catch is the case this guard is actually for: a
	// document that is not this feed at all. Together with the XMLName root
	// constraint, an error page or WAF interstitial would have to be
	// <rss><channel><title>… — i.e. an actual RSS feed — to slip through.
	if strings.TrimSpace(feed.Channel.Title) == "" {
		return Result{}, errors.New("azure: response is not an Azure status feed (no channel title)")
	}

	res := Result{Severity: SevNone}
	for _, item := range feed.Channel.Items {
		if azureItemResolved(item.Title) {
			continue
		}
		t := strings.TrimSpace(item.Title)
		if t == "" {
			continue
		}
		// Demote a straggler, like the AWS and xAI adapters do. Azure had no such
		// rule, so any item this parser kept open pinned the row red forever —
		// and resolution is decided from prose in the title (see
		// azureItemResolved), which is deliberately biased toward keeping items
		// open. Without a demotion that bias had no floor: one closing title
		// phrased "Resolved - root cause analysis ongoing" was a permanent
		// outage. Only a KNOWN timestamp can declare an item stale; a missing
		// pubDate fails open and keeps full severity.
		sev := SevMajor
		updated := azureTime(item.PubDate)
		if !updated.IsZero() && now.Sub(updated) > StaleAfter {
			sev = SevMinor
		}
		if res.Severity < sev {
			res.Severity = sev
		}
		// Title and <category> only — NOT the description. Azure descriptions are
		// multi-paragraph prose that routinely names regions for context
		// ("unrelated to the earlier event in West US", "traffic rerouted via
		// North Europe"), and a region tag is not a neutral annotation: a
		// region-LESS incident is unconditionally in scope, while a tagged one is
		// in scope only on a match. So a region merely MENTIONED in the body took
		// a global incident and hid it from everyone who configured a different
		// region. The title carries the region in Azure's own
		// "Service - Region - Description" format and <category> carries it
		// structurally; those are assertions about the incident rather than prose
		// that happens to contain a place name.
		scope := item.Title + " " + strings.Join(item.Categories, " ")
		regions := azureRegions(scope)
		res.Incidents = append(res.Incidents, Incident{
			Summary:    t,
			Severity:   sev,
			Regions:    regions,
			Components: azureComponents(item.Categories, regions),
			Updated:    updated,
			Note:       plainNote(item.Description),
			URL:        strings.TrimSpace(item.Link),
		})
	}
	return res, nil
}

// azureComponents returns the item's categories that are NOT region names —
// i.e. the affected service areas ("Network Infrastructure"), so the region is
// not duplicated as a component.
func azureComponents(categories, regions []string) []string {
	isRegion := make(map[string]bool, len(regions))
	for _, r := range regions {
		isRegion[strings.ToLower(r)] = true
	}
	var out []string
	for _, c := range categories {
		c = strings.TrimSpace(c)
		if c != "" && !isRegion[strings.ToLower(c)] {
			out = append(out, c)
		}
	}
	return out
}

// azureTimeLayouts are the pubDate shapes Azure has been observed emitting. The
// live feed uses a bare "Z" zone ("Thu, 23 Jul 2026 16:29:09 Z") rather than the
// numeric offset or named zone RFC 1123 specifies, so it is tried explicitly.
var azureTimeLayouts = []string{
	"Mon, 02 Jan 2006 15:04:05 Z",
	time.RFC1123Z,
	time.RFC1123,
	time.RFC3339,
}

// azureTime parses an Azure pubDate, returning the zero time when absent or in
// an unrecognized shape (the incident is still surfaced — only the timestamp is
// missing).
func azureTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range azureTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// azureTagOrEntity matches an HTML tag or a character entity in a description.
// azureRegionNames are the Azure region display names recognized in feed text.
// Azure status items carry the region in free text (e.g. "Virtual Machines -
// East US - Service degradation") rather than a structured field, so detection
// is by known-name match — robust to title format changes and avoiding false
// region extraction. The list covers the common public-cloud regions; an
// unrecognized region simply falls back to global.
var azureRegionNames = []string{
	"East US 2", "East US", "West US 3", "West US 2", "West US",
	"Central US", "North Central US", "South Central US", "West Central US",
	"Canada Central", "Canada East", "Brazil South",
	"North Europe", "West Europe", "UK South", "UK West",
	"France Central", "Germany West Central", "Norway East", "Sweden Central",
	"Switzerland North", "Poland Central", "Italy North", "Spain Central",
	"Southeast Asia", "East Asia", "Australia East", "Australia Southeast",
	"Central India", "South India", "West India",
	"Japan East", "Japan West", "Korea Central", "Korea South",
	"UAE North", "South Africa North", "Qatar Central", "Israel Central",
}

// azureRegionNamesByLen is azureRegionNames sorted longest-first, so a more
// specific region (e.g. "North Central US") is matched and consumed before a
// shorter region it contains ("Central US") can spuriously match.
var azureRegionNamesByLen = func() []string {
	out := append([]string(nil), azureRegionNames...)
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}()

// azureRegions returns the Azure regions named in text (case-insensitive),
// de-duplicated. Returns nil (global) when none match.
func azureRegions(text string) []string {
	work := strings.ToLower(text)
	var out []string
	for _, name := range azureRegionNamesByLen {
		ln := strings.ToLower(name)
		if strings.Contains(work, ln) {
			out = append(out, name)
			// Blank the matched span so a shorter contained region name can't
			// also match the same text.
			work = strings.ReplaceAll(work, ln, strings.Repeat(" ", len(ln)))
		}
	}
	sort.Strings(out)
	return out
}

// azureResolvedWord matches "resolved" as a whole word. The `[^a-z]` guards are
// what keep "Unresolved" from matching; a plain strings.Contains dropped those
// items out of the loop entirely, and a dropped item contributes neither
// severity nor an incident, so the provider silently fell back to operational
// during a live outage.
var azureResolvedWord = regexp.MustCompile(`(?i)(^|[^a-z])resolved([^a-z]|$)`)

// azureResolvedTag matches the "[RESOLVED] …" prefix Azure puts at the FRONT of
// a title, which the terminal-clause rule below would otherwise miss.
var azureResolvedTag = regexp.MustCompile(`(?i)^\s*[\[(]\s*resolved\s*[\])]`)

// azureOngoing lists the words that assert live, current impact. Their presence
// in the status clause keeps the item open no matter what else the title says.
var azureOngoing = regexp.MustCompile(`(?i)(^|[^a-z])(investigating|identified|mitigating|mitigation|ongoing|continu\w*|remain\w*|impact\w*|affect\w*|degrad\w*|partial\w*|underway|monitoring|except)([^a-z]|$)`)

// azureNegatedResolution catches a resolution that is being DENIED rather than
// announced: "not yet resolved", "still awaiting resolution", "no resolution".
// These carry no ongoing-impact word, so azureOngoing alone lets them through.
var azureNegatedResolution = regexp.MustCompile(`(?i)(^|[^a-z])(not|no|never|nor|awaiting|pending|without)([^a-z][a-z]*){0,3}[^a-z]resol`)

// azureClauseSplit splits a title into its clauses. Azure titles are
// "Service - Region - Status", and multi-scope updates chain further clauses
// with the same separators plus ';' and '.'.
var azureClauseSplit = regexp.MustCompile(`\s+[-—–:;]\s+|\.\s+`)

// azureItemResolved reports whether a feed item's title says the incident is
// closed. Only the FINAL clause decides, because Azure appends the current
// status last and a mixed-scope update leads with the part that resolved:
// "Resolved in North Europe. Impact continues in West Europe" is an OPEN
// incident, and an unanchored search for "resolved" made it vanish.
//
// The rule is deliberately biased toward keeping items open. A false positive
// here (a closed incident still shown) is visible and self-correcting — Azure
// drops the item from the feed, and anything it does not drop is demoted by the
// stale/zombie split after staleIncidentDays. A false negative silently reports
// a live outage as operational, which is the one failure this app cannot have.
// The cost of that bias is that a title like "Resolved - root cause analysis
// ongoing" reads as open until the feed drops it; that is the trade accepted.
func azureItemResolved(title string) bool {
	if azureResolvedTag.MatchString(title) {
		return true
	}
	clauses := azureClauseSplit.Split(strings.TrimSpace(title), -1)
	last := clauses[len(clauses)-1]
	if azureOngoing.MatchString(last) || azureNegatedResolution.MatchString(last) {
		return false
	}
	return azureResolvedWord.MatchString(last)
}
