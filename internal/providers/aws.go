package providers

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// init self-registers the AWS adapter and the AWS provider. AWS is 1:1 with its
// adapter, so both live here.
func init() {
	RegisterParser(KindAWS, func(_ Provider, data []byte) (Result, error) {
		return parseAWS(data)
	})
	Register(Provider{
		ID: "aws", Name: "AWS", Category: CategoryCloud, Kind: KindAWS,
		URL: "https://health.aws.amazon.com/public/currentevents",
	})
}

// awsFeed models the AWS Health public current-events feed. The live feed is a
// bare array of events; AWS has also historically wrapped it in {"current":[...]}
// so both shapes are accepted.
type awsFeed struct {
	Current []awsEvent `json:"current"`
}

// awsEvent models one entry in the AWS Health public events feed. The live feed
// uses service_name + summary for the human label, a numeric status code, and
// carries the region both as a human region_name and (reliably) as the region
// code embedded in the event ARN, e.g.
// "arn:aws:health:me-central-1::event/...".
type awsEvent struct {
	ServiceName string `json:"service_name"`
	Service     string `json:"service"`
	Summary     string `json:"summary"`
	// Status is AWS's numeric severity code as a string: "0" normal,
	// "1" informational, "2" degradation, "3" disruption/outage.
	Status     string `json:"status"`
	RegionName string `json:"region_name"`
	ARN        string `json:"arn"`
	// Region is an explicit region code when present (older/wrapped feeds);
	// otherwise it is derived from the ARN or service slug.
	Region string `json:"region"`
	// Impact is a legacy free-form severity hint kept for backward compatibility
	// with the older feed shape; Status takes precedence when set.
	Impact string `json:"impact"`
	// Date is the event's start time as a Unix-epoch string.
	Date string `json:"date"`
	// EventLog carries the per-update entries; the newest timestamp is the
	// event's last update, used for the stale-event filter, and its message is
	// the incident's latest note.
	EventLog []struct {
		Timestamp int64  `json:"timestamp"`
		Message   string `json:"message"`
	} `json:"event_log"`
}

// awsStaleAfter is how long an open AWS event may go without an update before
// it is demoted to a minor degradation. AWS's public feed has been observed
// carrying months-old "Disrupted" events (e.g. me-central-1/me-south-1 from a
// February power issue still open in July) that AWS never closes; a real
// disruption is updated far more often than this. They used to be dropped
// entirely so they couldn't pin the row red forever — but AWS's own health page
// keeps listing them as open issues, and hiding them made ACS show a flat green
// "Operational" that contradicted the page. Demoting keeps the row honest
// (amber, incidents listed) without the perpetual-outage noise or alerts.
const awsStaleAfter = StaleAfter

// awsRegionInName extracts an AWS region code such as "us-east-1" from text
// (an ARN, or a service slug like "ec2-us-east-1" / "EC2 (us-east-1)").
var awsRegionInName = regexp.MustCompile(`[a-z]{2}-[a-z]+-\d+`)

// parseAWS treats any open current event as a degradation, mapping the event's
// status code (or legacy impact string) onto a Severity and tagging each
// incident with its affected region. The feed is served as UTF-16, so the bytes
// are normalized to UTF-8 first.
func parseAWS(data []byte) (Result, error) {
	return parseAWSAt(data, time.Now())
}

// parseAWSAt is parseAWS with an injectable clock for the stale-event filter.
func parseAWSAt(data []byte, now time.Time) (Result, error) {
	events, err := awsEvents(data)
	if err != nil {
		return Result{}, err
	}

	res := Result{Severity: SevNone}
	for _, ev := range events {
		sev := awsEventSeverity(ev)
		if sev == SevNone {
			continue // status "0" / normal — not an open issue
		}
		started, updated := awsEventTimes(ev)
		// Demote zombie events: still open but silent for over awsStaleAfter.
		// Only a KNOWN last-update time can declare an event stale — an event
		// with no timestamps at all keeps its full severity (fail open, never
		// downplay a real disruption on missing data).
		if !updated.IsZero() && now.Sub(updated) > awsStaleAfter {
			sev = SevMinor
		}
		if sev > res.Severity {
			res.Severity = sev
		}
		if label := awsLabel(ev); label != "" {
			res.Incidents = append(res.Incidents, Incident{
				Summary:  label,
				Severity: sev,
				Regions:  awsRegions(ev),
				Started:  started,
				Updated:  updated,
				Note:     plainNote(awsLatestNote(ev)),
			})
		}
	}
	return res, nil
}

// awsLatestNote returns the message of the newest event_log entry — the
// incident's latest human update. Empty when the log carries no messages.
func awsLatestNote(ev awsEvent) string {
	note, latest := "", int64(0)
	for _, e := range ev.EventLog {
		if strings.TrimSpace(e.Message) != "" && (note == "" || e.Timestamp > latest) {
			note, latest = strings.TrimSpace(e.Message), e.Timestamp
		}
	}
	return note
}

// awsEventTimes returns the event's start time (the date field) and last-update
// time (the newest event_log timestamp, falling back to the start time). Either
// is zero when the feed carries no usable timestamp.
func awsEventTimes(ev awsEvent) (started, updated time.Time) {
	var start, latest int64
	if n, err := strconv.ParseInt(strings.TrimSpace(ev.Date), 10, 64); err == nil && n > 0 {
		start = n
	}
	latest = start
	for _, e := range ev.EventLog {
		if e.Timestamp > latest {
			latest = e.Timestamp
		}
	}
	if start > 0 {
		started = time.Unix(start, 0)
	}
	if latest > 0 {
		updated = time.Unix(latest, 0)
	}
	return started, updated
}

// awsLabel builds a human label from the service name and summary, e.g.
// "Multiple services: Increased Error Rates".
func awsLabel(ev awsEvent) string {
	name := strings.TrimSpace(ev.ServiceName)
	if name == "" {
		name = strings.TrimSpace(ev.Service)
	}
	summary := strings.TrimSpace(ev.Summary)
	switch {
	case name != "" && summary != "":
		return name + ": " + summary
	case name != "":
		return name
	default:
		return summary
	}
}

// awsRegions returns the affected region CODE for an event: the explicit region
// field when present, otherwise the code parsed from the ARN, then the service
// slug. Returns nil (global) when nothing is found.
//
// Deliberately no fallback to ev.RegionName, which is a human display name
// ("N. Virginia", "UAE", "Global"). Tagging with it is strictly WORSE than
// returning nil, because the two are not symmetric downstream: Incident.inScope
// gives a region-LESS incident an unconditional pass, while a region-tagged one
// must match. No user types "N. Virginia" into the regions field, and nothing
// matches "Global" — so the fallback took an event that would always have been
// visible and made it invisible to anyone with regions configured. A global
// CloudFront/Route 53/IAM outage carries no region code by design, which is
// exactly the case that hit.
func awsRegions(ev awsEvent) []string {
	if r := strings.TrimSpace(ev.Region); r != "" {
		return []string{r}
	}
	if m := awsRegionInName.FindString(ev.ARN); m != "" {
		return []string{m}
	}
	if m := awsRegionInName.FindString(ev.Service); m != "" {
		return []string{m}
	}
	return nil
}

// awsEvents accepts either a bare [...] array (the live shape) or the wrapped
// {"current":[...]} object, after normalizing the UTF-16/BOM-encoded bytes.
func awsEvents(data []byte) ([]awsEvent, error) {
	data = decodeFeed(data)
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		var events []awsEvent
		if err := json.Unmarshal(data, &events); err != nil {
			return nil, err
		}
		return events, nil
	}
	var feed awsFeed
	if err := json.Unmarshal(data, &feed); err != nil {
		return nil, err
	}
	return feed.Current, nil
}

// awsEventSeverity maps an event onto a Severity, preferring AWS's numeric
// status code and falling back to the legacy impact string.
func awsEventSeverity(ev awsEvent) Severity {
	switch strings.TrimSpace(ev.Status) {
	case "0":
		return SevNone
	case "1":
		return SevMinor
	case "2":
		return SevMajor
	case "3":
		return SevCritical
	}
	return awsImpactSeverity(ev.Impact)
}

func awsImpactSeverity(impact string) Severity {
	switch strings.ToLower(strings.TrimSpace(impact)) {
	case "outage", "critical":
		return SevCritical
	case "degraded", "major", "error":
		return SevMajor
	case "informational", "info", "minor":
		return SevMinor
	case "":
		// An open event with no severity hint at all is still a degradation.
		return SevMinor
	default:
		return SevMinor
	}
}
