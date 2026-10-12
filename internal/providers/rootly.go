package providers

import (
	"errors"
	"log/slog"
	"regexp"
	"strings"
)

// init self-registers the Rootly adapter and the Mistral provider it backs.
//
// Mistral's status page (status.mistral.ai) moved from Checkly to Rootly. The
// Checkly JSON endpoint the previous adapter read answers 404 now, and the Rootly
// page offers no public JSON or RSS (index.json 404, feed.rss and feed.atom 404,
// history.rss 401; measured 2026-10-11). What it does serve is a server-rendered
// HTML page with every service's status in it, so the feed IS the page.
//
// The host sits behind a Cloudflare managed challenge that keys on how the
// page is asked for, not on who asks: an API-shaped request (a JSON Accept, no
// Fetch Metadata) answers 403 "Just a moment..." on every path, a document
// request with this app's own User-Agent answers the page. Page: true is what
// makes Check send the latter (see there for the measurement). A challenge
// page is HTML too, which is why parseRootly anchors on the service rows: a
// page with none is an error, never a clean all-clear.
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
	// "All Systems Operational".
	rootlyBanner = regexp.MustCompile(`(?is)<span class="text-4xl[^"]*">\s*(.*?)\s*</span>`)
	// rootlySummary isolates one collapsible row's header. Every service is one
	// such row: an icon, an <h2> with the name, and a status label closing it.
	rootlySummary = regexp.MustCompile(`(?is)<summary\b[^>]*>(.*?)</summary>`)
	rootlyName    = regexp.MustCompile(`(?is)<h2\b[^>]*>(.*?)</h2>`)
	rootlyStatus  = regexp.MustCompile(`(?is)<span\b[^>]*>([^<]*?)</span>$`)
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
func parseRootly(data []byte) (Result, error) {
	page := string(decodeFeed(data))

	var services []rootlyService
	for _, m := range rootlySummary.FindAllStringSubmatch(page, -1) {
		block := strings.TrimSpace(m[1])
		name := rootlyName.FindStringSubmatch(block)
		status := rootlyStatus.FindStringSubmatch(block)
		if name == nil || status == nil {
			continue // the "Services" section header has a heading but no status label
		}
		svc := rootlyService{Name: plainNote(name[1]), Status: plainNote(status[1])}
		if icon := rootlyIcon.FindStringSubmatch(block); icon != nil {
			svc.Icon = icon[1]
		}
		if svc.Name == "" || svc.Status == "" {
			continue
		}
		services = append(services, svc)
	}
	// The anchor. A Cloudflare challenge, an error page or a redesign that
	// drops the rows must read "unreadable", never "operational".
	if len(services) == 0 {
		return Result{}, errors.New("rootly: page carries no service rows")
	}

	banner := ""
	if m := rootlyBanner.FindStringSubmatch(page); m != nil {
		banner = plainNote(m[1])
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
		if summary == "" || rootlyAllClear(summary) {
			// The rows disagree with the banner (or there is none): say what
			// the rows say.
			summary = strings.Join(notes, ", ")
		}
		res.Incidents = append(res.Incidents, Incident{
			Summary:    summary,
			Severity:   res.Severity,
			Components: affected,
			Note:       strings.Join(notes, "\n"),
		})
	case banner != "" && !rootlyAllClear(banner):
		// Every row reads operational but the banner does not: something is
		// posted that the rows do not show. A real but unquantified
		// degradation, floored at minor rather than read as all clear.
		res.Severity = SevMinor
		res.Incidents = append(res.Incidents, Incident{Summary: banner, Severity: SevMinor})
	}
	return res, nil
}

// rootlyAllClear reports whether the page banner is the all-clear verdict
// ("All Systems Operational"). Every other banner Rootly renders names the
// trouble (an outage, a degradation, a maintenance) and carries no
// "operational".
func rootlyAllClear(banner string) bool {
	return strings.Contains(strings.ToLower(banner), "operational")
}

// rootlySeverity maps a service row's status label onto a Severity. The labels
// are prose rather than an enum, so the match is by keyword, in the order that
// keeps "Major Outage" above "Partial Outage". When the words are unfamiliar
// the icon colour decides, per the page's own legend; when both are
// unfamiliar the row is an open degradation of unknown size, floored at minor
// and logged so a new vocabulary does not collapse silently.
func rootlySeverity(status, icon string) Severity {
	s := strings.ToLower(status)
	switch {
	case strings.Contains(s, "operational"):
		return SevNone
	case strings.Contains(s, "major"), strings.Contains(s, "down"), strings.Contains(s, "unavailable"):
		return SevCritical
	case strings.Contains(s, "partial"), strings.Contains(s, "outage"), strings.Contains(s, "affected"):
		return SevMajor
	case strings.Contains(s, "degraded"), strings.Contains(s, "maintenance"):
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
