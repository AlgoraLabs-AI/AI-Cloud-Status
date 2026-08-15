package providers

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// init self-registers the Instatus adapter. Instatus pages expose a
// /summary.json (also served at /api/v2/summary.json) whose shape is NOT the
// Atlassian Statuspage one: {"page":{"status":"UP"|"HASISSUES"|
// "UNDERMAINTENANCE"},"activeIncidents":[...]} — Perplexity migrated to it,
// which made the Statuspage parser silently read "always operational".
func init() {
	RegisterParser(KindInstatus, func(_ Provider, data []byte) (Result, error) {
		return parseInstatus(data)
	})
}

// instatusSummary models the relevant subset of an Instatus summary.json.
// activeIncidents only lists open incidents; resolved ones disappear from the
// summary. The updates list is not documented for the summary endpoint but is
// modelled tolerantly in case a page includes it (FeedCapture will archive
// real incident payloads to refine this).
// Page is a POINTER so a response that simply does not carry it (an error body,
// a WAF interstitial, a schema migration) is an error rather than a silent
// SevNone. See the note on statuspageSummary.
type instatusSummary struct {
	Page *struct {
		Status string `json:"status"`
	} `json:"page"`
	ActiveIncidents []struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Impact  string `json:"impact"`
		Started string `json:"started"`
		URL     string `json:"url"`
		Updates []struct {
			Message string `json:"message"`
			Started string `json:"started"`
		} `json:"updates"`
	} `json:"activeIncidents"`
}

// parseInstatus reads page.status and the open incidents.
func parseInstatus(data []byte) (Result, error) {
	var s instatusSummary
	if err := json.Unmarshal(decodeFeed(data), &s); err != nil {
		return Result{}, err
	}
	if s.Page == nil {
		return Result{}, errors.New("instatus: response carries no page object")
	}

	var res Result
	for _, inc := range s.ActiveIncidents {
		if strings.EqualFold(inc.Status, "RESOLVED") {
			continue
		}
		note, noteAt := "", time.Time{}
		for _, u := range inc.Updates {
			if at := parseInstatusTime(u.Started); u.Message != "" && (note == "" || at.After(noteAt)) {
				note, noteAt = u.Message, at
			}
		}
		// An incident Instatus still lists as active is open by definition, so
		// it can never be SevNone — the summary endpoint's field names are
		// guessed (see the struct comment) and a missing/OPERATIONAL impact on
		// an open incident is far more likely a mapping miss than genuine
		// health. It matters because effectiveSeverity prefers per-incident
		// severities whenever the list is non-empty, so a SevNone incident does
		// not just contribute nothing — it DISCARDS the HASISSUES bump below
		// and paints the provider operational. Every other adapter floors an
		// open incident at minor for the same reason (xai.go, checkly.go,
		// googlecloud.go, aws.go).
		sev := instatusImpactSeverity(inc.Impact)
		if sev == SevNone && !strings.EqualFold(strings.TrimSpace(inc.Impact), "UNDERMAINTENANCE") {
			// Anything else that mapped to none — "", "OPERATIONAL", or a value
			// the mapper did not recognize — is floored.
			sev = SevMinor
		}
		if sev > res.Severity {
			res.Severity = sev
		}
		res.Incidents = append(res.Incidents, Incident{
			Summary:  inc.Name,
			Severity: sev,
			Started:  parseInstatusTime(inc.Started),
			Updated:  noteAt,
			Note:     plainNote(note),
			URL:      inc.URL,
		})
	}
	// HASISSUES with no parseable incident detail still means "not healthy".
	if res.Severity == SevNone && strings.EqualFold(s.Page.Status, "HASISSUES") {
		res.Severity = SevMinor
	}
	return res, nil
}

// instatusImpactSeverity maps Instatus impact/component vocabulary onto the
// normalized severity levels.
// TrimSpace here, not at the call sites: a trailing space made "UNDERMAINTENANCE "
// miss both this switch and the maintenance carve-out in parseInstatus.
func instatusImpactSeverity(impact string) Severity {
	switch strings.ToUpper(strings.TrimSpace(impact)) {
	case "", "OPERATIONAL", "UNDERMAINTENANCE":
		return SevNone
	case "DEGRADEDPERFORMANCE":
		return SevMinor
	case "PARTIALOUTAGE":
		return SevMinor
	case "MAJOROUTAGE", "FULLOUTAGE":
		return SevMajor
	default:
		// Unknown vocabulary is treated conservatively as minor.
		return SevMinor
	}
}

// parseInstatusTime parses Instatus timestamps, which appear either as RFC 3339
// or as a JavaScript Date string like
// "Sat Jul 09 2022 04:20:00 GMT+0000 (Coordinated Universal Time)".
// Unparseable or empty values yield the zero time.
func parseInstatusTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	// Strip a trailing "(Zone Name)" parenthetical before the JS-style layout.
	if i := strings.Index(s, " ("); i > 0 {
		s = s[:i]
	}
	if t, err := time.Parse("Mon Jan 02 2006 15:04:05 GMT-0700", s); err == nil {
		return t
	}
	return time.Time{}
}
