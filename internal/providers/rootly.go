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
// request with this app's own User-Agent answers the page. KindRootly is
// registered as a page Kind (RegisterPageParser, Kind.Page), which is what
// makes Check send the latter (see there for the measurement). A challenge
// page is HTML too, which is why parseRootly anchors on the service rows and
// the banner: a page without them is an error, never a clean all-clear.
//
// Optional stays: if the challenge is tightened again a 403 is "no public feed",
// the row reads "feed unavailable" rather than an outage, and the alert audit
// does not fail the run over a feed nobody outside a browser can read.
func init() {
	RegisterPageParser(KindRootly, func(_ Provider, data []byte) (Result, error) {
		return parseRootly(data)
	})
	Register(Provider{
		ID: "mistral", Name: "Mistral", Category: CategoryAI, Kind: KindRootly,
		URL:      "https://status.mistral.ai/",
		Optional: true,
	})
}

var (
	// rootlyBanner is the page-level verdict above the service list, e.g.
	// "All Systems Operational": the one span styled text-4xl, wherever that
	// token sits in its class list.
	// One level of inline nesting is allowed inside it (an sr-only prefix, an
	// emphasis), so the verdict is the whole phrase, never a truncated one.
	rootlyBanner = regexp.MustCompile(`(?is)<span\b[^>]*class="[^"]*\btext-4xl\b[^"]*"[^>]*>((?:[^<]|<(?:span|strong|em|b)\b[^>]*>[^<]*</(?:span|strong|em|b)>)*)</span>`)
	// rootlyRow isolates one service row's header: the <summary> of a
	// <details class="group …"> container. Every service is one such row: a
	// status icon, an <h2> with the name, and a status label that closes the
	// header. The "Services" section header is a <details> too, but not a
	// "group" one, so it is never a row candidate whatever icon it gains.
	rootlyRow  = regexp.MustCompile(`(?is)<details\b[^>]*class="[^"]*\bgroup\b[^"]*"[^>]*>\s*<summary\b[^>]*>(.*?)</summary>`)
	rootlyName = regexp.MustCompile(`(?is)<h2\b[^>]*>(.*?)</h2>`)
	// rootlyTextSpan matches a span holding bare text; the LAST one in a row's
	// header is its status label. The label closes the header on the captured
	// page; should an affected row carry something after it (a count, a
	// badge), that is read instead and the icon colour decides what an
	// unfamiliar label means, which keeps a row readable during the very
	// outage it reports. A header with no text span at all is unreadable.
	rootlyTextSpan = regexp.MustCompile(`(?is)<span\b[^>]*>\s*([^<>]+?)\s*</span>`)
	// rootlyDocumentEnd is the closing tag of a whole document. Anywhere in
	// the body, not at its very end: an edge may append a marker after it,
	// and a body cut by the size limit has lost it altogether.
	rootlyDocumentEnd = regexp.MustCompile(`(?i)</html>`)
	// rootlyServices is the heading of the service list; rows are read from
	// there on, so an incident card above the list is never a row.
	rootlyServices = regexp.MustCompile(`(?is)<h2\b[^>]*>\s*Services\s*</h2>`)
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

	// Rows are read from the service list's heading on, so a card the page
	// gains above it during an incident is never mistaken for a service.
	list := page
	if loc := rootlyServices.FindStringIndex(page); loc != nil {
		list = page[loc[1]:]
	}
	var services []rootlyService
	for _, m := range rootlyRow.FindAllStringSubmatch(list, -1) {
		block := strings.TrimSpace(m[1])
		name := rootlyName.FindStringSubmatch(block)
		if name == nil {
			continue // not a row: no heading
		}
		// A group container with a heading IS a service row. An icon class
		// the legend does not show (a fourth state, a restyle) leaves Icon
		// empty, and rootlySeverity then reads the label alone, flooring an
		// unfamiliar one at minor; the row is never dropped from the verdict.
		svc := rootlyService{Name: plainNote(name[1])}
		if icon := rootlyIcon.FindStringSubmatch(block); icon != nil {
			svc.Icon = icon[1]
		}
		for _, span := range rootlyTextSpan.FindAllStringSubmatch(block, -1) {
			if s := plainNote(span[1]); s != "" {
				svc.Status = s // the last span with words is the label
			}
		}
		// A row with no readable label is still a row with an icon: the
		// legend's colour is a verdict on its own, so the row is read from
		// it rather than failing the page during the outage it reports, and
		// its label is the legend's word for that colour. Only a row that
		// says nothing either way is unreadable.
		if svc.Status == "" {
			if svc.Icon == "" {
				return Result{}, fmt.Errorf("rootly: service row %q has no readable status", svc.Name)
			}
			svc.Status = rootlyLegend[svc.Icon]
		}
		services = append(services, svc)
	}
	if len(services) == 0 {
		return Result{}, errors.New("rootly: page carries no service rows")
	}

	// The banner is the headline span nearest ABOVE the service list: a card
	// the page gains during an incident may carry the same headline style,
	// but it sits above the banner, so the last match before the list wins.
	above := page[:len(page)-len(list)]
	if len(above) == 0 {
		above = page
	}
	ms := rootlyBanner.FindAllStringSubmatch(above, -1)
	if len(ms) == 0 {
		return Result{}, errors.New("rootly: page carries no status banner")
	}
	banner := plainNote(ms[len(ms)-1][1])
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

// rootlyLegend is the page's own word for each status icon colour, used as a
// row's label when the row carries an icon and no readable words.
var rootlyLegend = map[string]string{
	"sp-success-bg": "Operational",
	"sp-error-bg":   "Affected",
	"bg-yellow-100": "Maintenance",
}

// rootlyAllClear reports whether the page banner is an all-clear verdict: it
// says "operational" and nothing that takes it back. "Not Operational" and
// "Partially Operational" must not pass for it; "All Systems Are Operational"
// and "All services operational" must, or a reworded all-clear becomes a
// minor incident that never closes.
func rootlyAllClear(banner string) bool {
	words := rootlyWords(banner)
	return containsAny(words, "operational") && !rootlyQualified(words)
}

// rootlyQualified reports whether a label's words take an "operational" back.
// ONE vocabulary serves the banner and the rows, so the two readings cannot
// drift apart. "no", "issues" and "incidents" are deliberately absent: "All
// Systems Operational. No incidents reported" is an all-clear, and reading
// its negation as trouble would make it a minor incident that never closes.
func rootlyQualified(words []string) bool {
	return containsAny(words, rootlyQualifiers...)
}

var rootlyQualifiers = []string{
	"not", "partial", "partially", "degraded", "outage", "down", "unavailable",
	"maintenance", "affected", "major", "minor",
}

// containsAny reports whether words holds any of the wanted words.
func containsAny(words []string, wanted ...string) bool {
	for _, w := range wanted {
		if slices.Contains(words, w) {
			return true
		}
	}
	return false
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
// keeps "Major Outage" above "Partial Outage" and a minor qualifier
// ("Operational (Degraded Performance)") below a major one. An unqualified
// "operational" ("Operational", "Fully Operational") is healthy, by the same
// test the banner is read with; a qualified one ("Not Operational",
// "Partially Operational") is a degradation whatever the icon says. When the
// words are unfamiliar, or there are none, the icon colour decides, per the
// page's own legend; when both are unfamiliar the row is an open degradation
// of unknown size, floored at minor and logged so a new vocabulary does not
// collapse silently.
func rootlySeverity(status, icon string) Severity {
	words := rootlyWords(status)
	switch {
	case rootlyAllClear(status):
		return SevNone
	case containsAny(words, "major", "down", "unavailable"):
		return SevCritical
	case containsAny(words, "degraded", "maintenance", "minor"):
		// Before the qualified-operational case: "Operational (Degraded
		// Performance)", "Operational – Under Maintenance" and "Operational
		// (minor issues)" are minor.
		return SevMinor
	case containsAny(words, "partial", "partially", "outage", "affected", "operational"):
		return SevMajor
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
