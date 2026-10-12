package providers

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// init self-registers the Rootly adapter and the Mistral provider it backs.
//
// Mistral's status page (status.mistral.ai) moved from Checkly to Rootly. The
// Checkly JSON endpoint the previous adapter read answers 404 now, and the Rootly
// page offers no public JSON or RSS (index.json 404, feed.rss and feed.atom 404,
// history.rss 401, all measured). What it does serve is a server-rendered
// HTML page with every service's status in it, so the feed IS the page.
//
// The host sits behind a Cloudflare managed challenge that keys on how the
// page is asked for, not on who asks: an API-shaped request (a JSON Accept, no
// Fetch Metadata) answers 403 "Just a moment..." on every path, a document
// request with this app's own User-Agent answers the page. Page: true is what
// makes Check send the latter (see there for the measurement). A challenge
// page is HTML too, which is why parseRootly anchors on the service rows and
// the banner: a page without them is an error, never a clean all-clear.
//
// Optional stays: if the challenge is tightened again a 403 is "no public feed",
// the row reads "feed unavailable" rather than an outage, and the alert audit
// does not fail the run over a feed nobody outside a browser can read.
func init() {
	RegisterParser(KindRootly, func(_ Provider, data []byte) (Result, error) {
		return parseRootly(data)
	})
	Register(Provider{
		ID: "mistral", Name: "Mistral", Category: CategoryAI, Kind: KindRootly,
		URL:      "https://status.mistral.ai/",
		Optional: true,
		Page:     true,
	})
}

var (
	// rootlyBanner is the page-level verdict above the service list, e.g.
	// "All Systems Operational": the one span styled text-4xl, wherever that
	// token sits in its class list.
	rootlyBanner = regexp.MustCompile(`(?is)<span\b[^>]*class="[^"]*\btext-4xl\b[^"]*"[^>]*>\s*(.*?)\s*</span>`)
	// rootlySummary isolates one collapsible row's header. Every service is one
	// such row: a status icon, an <h2> with the name, and a status label that
	// closes the header. The "Services" section header is a summary too, with
	// a heading but no icon, which is how it is told apart.
	rootlySummary = regexp.MustCompile(`(?is)<summary\b[^>]*>(.*?)</summary>`)
	rootlyName    = regexp.MustCompile(`(?is)<h2\b[^>]*>(.*?)</h2>`)
	// rootlyTextSpan matches a span holding bare text; the LAST one in a row's
	// header is its status label. The label closes the header on the captured
	// page; should an affected row carry something after it (a count, a
	// badge), that is read instead and the icon colour decides what an
	// unfamiliar label means, which keeps a row readable during the very
	// outage it reports. A header with no text span at all is unreadable.
	rootlyTextSpan = regexp.MustCompile(`(?is)<span\b[^>]*>\s*([^<>]+?)\s*</span>`)
	// rootlyDocumentEnd is the closing tag of a whole document.
	rootlyDocumentEnd = regexp.MustCompile(`(?i)</html>\s*$`)
	// rootlyIcon reads the status icon's colour class, the page's second
	// channel for the same fact: its legend pairs sp-success-bg with
	// "Operational", sp-error-bg with "Affected" and bg-yellow-100 with
	// "Maintenance".
	rootlyIcon = regexp.MustCompile(`(?is)class="[^"]*\b(sp-success-bg|sp-error-bg|bg-yellow-100)\b`)
)

// rootlyService is one row of the page's service list.
type rootlyService struct {
	Name   string
	Status string
	Icon   string
}

// parseRootly reads the service list off a Rootly status page. The worst
// service status is the severity; the affected services are folded into ONE
// incident (the page carries no incident timeline, only the current state of
// each service), titled with the page's own banner when it says more than
// "all clear".
//
// It fails closed on three shapes, because every one of them would otherwise
// read as operational: a body that is not a whole document (the response is
// read through a size limit, and a truncated page has lost its last rows), a
// service row whose status label cannot be read (one redesigned row among
// intact ones is exactly how an outage would go unread), and a page with no
// rows or no banner at all (a challenge page, an error page, a redesign).
func parseRootly(data []byte) (Result, error) {
	page := string(decodeFeed(data))
	if !rootlyDocumentEnd.MatchString(page) {
		return Result{}, errors.New("rootly: body is not a whole document")
	}

	var services []rootlyService
	for _, m := range rootlySummary.FindAllStringSubmatch(page, -1) {
		block := strings.TrimSpace(m[1])
		name := rootlyName.FindStringSubmatch(block)
		if name == nil {
			continue // not a row: no heading
		}
		icon := rootlyIcon.FindStringSubmatch(block)
		if icon == nil {
			continue // a section header ("Services"): a heading, no status icon
		}
		svc := rootlyService{Name: plainNote(name[1]), Icon: icon[1]}
		if spans := rootlyTextSpan.FindAllStringSubmatch(block, -1); len(spans) > 0 {
			svc.Status = plainNote(spans[len(spans)-1][1])
		}
		if svc.Status == "" {
			return Result{}, fmt.Errorf("rootly: service row %q has no readable status label", svc.Name)
		}
		services = append(services, svc)
	}
	if len(services) == 0 {
		return Result{}, errors.New("rootly: page carries no service rows")
	}

	m := rootlyBanner.FindStringSubmatch(page)
	if m == nil {
		return Result{}, errors.New("rootly: page carries no status banner")
	}
	banner := plainNote(m[1])
	if banner == "" {
		// A banner with no words is the anchor missing, not a verdict: it
		// must not become a minor incident with an empty summary.
		return Result{}, errors.New("rootly: status banner carries no text")
	}

	res := Result{Severity: SevNone}
	var affected, notes []string
	for _, svc := range services {
		sev := rootlySeverity(svc.Status, svc.Icon)
		if sev == SevNone {
			continue
		}
		if sev > res.Severity {
			res.Severity = sev
		}
		affected = append(affected, svc.Name)
		notes = append(notes, svc.Name+": "+svc.Status)
	}

	switch {
	case len(affected) > 0:
		summary := banner
		if rootlyAllClear(summary) {
			// The rows disagree with the banner: say what the rows say.
			summary = strings.Join(notes, ", ")
		}
		res.Incidents = append(res.Incidents, Incident{
			Summary:    summary,
			Severity:   res.Severity,
			Components: affected,
			Note:       strings.Join(notes, "\n"),
		})
	case !rootlyAllClear(banner):
		// Every row reads operational but the banner does not: something is
		// posted that the rows do not show. A real but unquantified
		// degradation, floored at minor rather than read as all clear.
		res.Severity = SevMinor
		res.Incidents = append(res.Incidents, Incident{Summary: banner, Severity: SevMinor})
	}
	return res, nil
}

// rootlyAllClear reports whether the page banner is an all-clear verdict: it
// says "operational" and nothing that takes it back. "Not Operational" and
// "Partially Operational" must not pass for it; "All Systems Are Operational"
// and "All services operational" must, or a reworded all-clear becomes a
// minor incident that never closes.
func rootlyAllClear(banner string) bool {
	words := rootlyWords(banner)
	if !slices.Contains(words, "operational") {
		return false
	}
	for _, w := range words {
		if slices.Contains(rootlyNotClear, w) {
			return false
		}
	}
	return true
}

// rootlyNotClear are the words that qualify an "operational" into something
// less: a banner carrying any of them is not all clear.
var rootlyNotClear = []string{
	"not", "no", "partial", "partially", "degraded", "outage", "down", "unavailable",
	"maintenance", "issue", "issues", "affected", "major", "minor", "incident", "incidents",
}

// rootlyWords lower-cases a label and splits it into words, so a keyword is
// matched as a word: "Countdown" is not "down".
func rootlyWords(label string) []string {
	return strings.FieldsFunc(strings.ToLower(label), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// rootlySeverity maps a service row's status label onto a Severity. The labels
// are prose rather than an enum, so the match is by word, in the order that
// keeps "Major Outage" above "Partial Outage". Only the bare label
// "Operational" is healthy: a qualified one ("Not Operational", "Partially
// Operational") is a degradation whatever the icon says. When the words are
// unfamiliar the icon colour decides, per the page's own legend; when both
// are unfamiliar the row is an open degradation of unknown size, floored at
// minor and logged so a new vocabulary does not collapse silently.
func rootlySeverity(status, icon string) Severity {
	words := rootlyWords(status)
	has := func(ws ...string) bool {
		for _, w := range ws {
			if slices.Contains(words, w) {
				return true
			}
		}
		return false
	}
	switch {
	case len(words) == 1 && words[0] == "operational":
		return SevNone
	case has("major", "down", "unavailable"):
		return SevCritical
	case has("partial", "partially", "outage", "affected", "operational"):
		return SevMajor
	case has("degraded", "maintenance"):
		return SevMinor
	}
	switch icon {
	case "sp-success-bg":
		return SevNone
	case "sp-error-bg":
		return SevMajor
	case "bg-yellow-100":
		return SevMinor
	}
	slog.Warn("rootly: unknown service status, treating as minor", "status", status)
	return SevMinor
}
