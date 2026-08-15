package providers

import (
	"encoding/json"
	"errors"
	"time"
)

// init self-registers the Atlassian Statuspage adapter. Statuspage-backed
// providers (OpenAI, Anthropic, Cloudflare, GitHub, Bitbucket, …) each live in
// their own file and only need a Register call.
func init() {
	RegisterParser(KindStatuspage, func(_ Provider, data []byte) (Result, error) {
		return parseStatuspage(data)
	})
}

// statuspageSummary models the relevant subset of an Atlassian Statuspage
// /api/v2/summary.json response.
//
// Status is a POINTER so its absence can be told apart from an empty value.
// Without that, `{"message":"Forbidden"}`, a WAF's JSON interstitial, or a page
// that quietly migrated off the Statuspage schema all unmarshal cleanly into a
// zero struct: indicator "" maps to SevNone and the provider paints green with
// no error. instatus.go's own header records that this already happened once —
// Perplexity moved to Instatus and "made the Statuspage parser silently read
// always operational".
type statuspageSummary struct {
	Status *struct {
		Indicator   string `json:"indicator"`
		Description string `json:"description"`
	} `json:"status"`
	Incidents []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Impact     string `json:"impact"`
		Shortlink  string `json:"shortlink"`
		CreatedAt  string `json:"created_at"`
		UpdatedAt  string `json:"updated_at"`
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
		// IncidentUpdates are the human update messages, newest first.
		IncidentUpdates []struct {
			Body      string `json:"body"`
			CreatedAt string `json:"created_at"`
		} `json:"incident_updates"`
	} `json:"incidents"`
}

// parseStatuspage reads status.indicator and the list of unresolved incidents.
func parseStatuspage(data []byte) (Result, error) {
	var s statuspageSummary
	if err := json.Unmarshal(decodeFeed(data), &s); err != nil {
		return Result{}, err
	}
	if s.Status == nil {
		return Result{}, errors.New("statuspage: response carries no status object")
	}

	res := Result{
		Severity: statuspageIndicatorSeverity(s.Status.Indicator),
		Summary:  s.Status.Description,
	}
	for _, inc := range s.Incidents {
		if statuspageIncidentUnresolved(inc.Status) {
			// Statuspage incidents are not region-scoped (the feed exposes no
			// region data), so they are recorded as global. The per-incident
			// impact uses the same vocabulary as the top-level indicator.
			var comps []string
			for _, comp := range inc.Components {
				if comp.Name != "" {
					comps = append(comps, comp.Name)
				}
			}
			// Latest update note: the feed orders updates newest-first, but pick
			// by timestamp so an out-of-order feed can't surface a stale note.
			note, noteAt := "", time.Time{}
			for _, u := range inc.IncidentUpdates {
				if at := parseStatuspageTime(u.CreatedAt); u.Body != "" && (note == "" || at.After(noteAt)) {
					note, noteAt = u.Body, at
				}
			}
			res.Incidents = append(res.Incidents, Incident{
				Summary:    inc.Name,
				Severity:   statuspageIndicatorSeverity(inc.Impact),
				Components: comps,
				Started:    parseStatuspageTime(inc.CreatedAt),
				Updated:    parseStatuspageTime(inc.UpdatedAt),
				Note:       plainNote(note),
				URL:        inc.Shortlink,
			})
		}
	}
	// A MINOR page indicator with ZERO unresolved incidents is component-state
	// noise, not an outage anyone is acting on: Cloudflare chronically reports
	// "minor" from its perpetually re-routed PoP components while posting no
	// incident, which showed a "Degraded" row whose own detail said "No active
	// incidents". Only minor is suppressed — a major/critical indicator without
	// an incident (components flipped before/without an incident post) is a real
	// outage signal and must keep alerting.
	if len(res.Incidents) == 0 && res.Severity <= SevMinor {
		res.Severity = SevNone
	}
	return res, nil
}

// parseStatuspageTime parses an Atlassian Statuspage timestamp (RFC 3339), or
// returns the zero time when the field is empty or unparseable.
func parseStatuspageTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func statuspageIndicatorSeverity(indicator string) Severity {
	switch indicator {
	case "none", "":
		return SevNone
	case "minor":
		return SevMinor
	case "major":
		return SevMajor
	case "critical":
		return SevCritical
	default:
		// Unknown indicators are treated conservatively as minor.
		return SevMinor
	}
}

// statuspageIncidentUnresolved reports whether an incident is still active.
func statuspageIncidentUnresolved(status string) bool {
	switch status {
	case "resolved", "postmortem", "completed":
		return false
	default:
		return true
	}
}
