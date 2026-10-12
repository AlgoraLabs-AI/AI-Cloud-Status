package providers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// init self-registers the Checkly adapter as the LEGACY reader of Mistral's
// archived captures. No live provider uses it.
//
// Mistral's status page was a Checkly status page, and its server-proxied
// "unresolved-incidents" endpoint was the feed this adapter read. The page is
// Rootly now (see rootly.go) and that endpoint answers 404, but an archive
// outlives its feed: every Mistral capture recorded before the move is
// Checkly JSON, and cmd/alertaudit re-derives severities from the archive
// with ParseFeed. RegisterLegacy is what lets ParseFeed hand those captures
// to this adapter, so the archive stays readable without a live Checkly
// provider and without the audit reading a changed feed as a broken parser.
func init() {
	RegisterParser(KindCheckly, func(_ Provider, data []byte) (Result, error) {
		return parseCheckly(data)
	})
	RegisterLegacy("mistral", KindCheckly)
}

// checklyIncidents models the Checkly status-page "unresolved-incidents"
// response. Only currently-open incidents are returned, so any incident present
// is an active issue.
type checklyIncidents struct {
	// Raw so an absent key (empty) is told from a present one, null or a
	// list: see the anchor in parseCheckly.
	Incidents json.RawMessage `json:"incidents"`
}

type checklyIncident struct {
	Name      string `json:"name"`
	Severity  string `json:"severity"` // "MINOR" | "MAJOR" | "CRITICAL" | "MAINTENANCE"
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Services  []struct {
		Name string `json:"name"`
	} `json:"services"`
	IncidentUpdates []struct {
		Description string `json:"description"`
		CreatedAt   string `json:"created_at"`
	} `json:"incidentUpdates"`
}

// parseCheckly maps each open Checkly incident onto a Severity. Checkly incidents
// are tied to services rather than geographic regions, so they are recorded as
// global (region-agnostic). With no open incidents the service is operational.
func parseCheckly(data []byte) (Result, error) {
	var feed checklyIncidents
	if err := json.Unmarshal(decodeFeed(data), &feed); err != nil {
		return Result{}, err
	}
	// The anchor: the "incidents" key must be present. An empty list and a
	// null are both the all-clear; the key missing is not this feed. As the
	// legacy reader behind ParseFeed this adapter is handed every body the
	// Rootly parser rejects, and `{}` or `{"message":"Forbidden"}` must read
	// as unreadable, not as a quiet all-clear.
	if len(feed.Incidents) == 0 {
		return Result{}, errors.New("checkly: document carries no incidents key")
	}
	var incidents []checklyIncident
	if err := json.Unmarshal(feed.Incidents, &incidents); err != nil {
		return Result{}, err
	}

	res := Result{Severity: SevNone}
	for _, inc := range incidents {
		sev := checklySeverity(inc.Severity)
		if sev > res.Severity {
			res.Severity = sev
		}
		if summary := checklyLabel(inc); summary != "" {
			// Latest update note, picked by timestamp (defensive against feed order).
			note, noteAt := "", time.Time{}
			updated := parseChecklyTime(inc.UpdatedAt)
			for _, u := range inc.IncidentUpdates {
				at := parseChecklyTime(u.CreatedAt)
				if u.Description != "" && (note == "" || at.After(noteAt)) {
					note, noteAt = u.Description, at
				}
				if at.After(updated) {
					updated = at
				}
			}
			res.Incidents = append(res.Incidents, Incident{
				Summary:  summary,
				Severity: sev,
				Started:  parseChecklyTime(inc.CreatedAt),
				Updated:  updated,
				Note:     plainNote(note),
			})
		}
	}
	return res, nil
}

// parseChecklyTime parses a Checkly RFC 3339 timestamp, or returns the zero
// time when the field is empty or unparseable.
func parseChecklyTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// checklyLabel appends the affected service names to the incident name, e.g.
// "Deep Research is down. (Vibe)".
func checklyLabel(inc checklyIncident) string {
	name := strings.TrimSpace(inc.Name)
	var svcs []string
	for _, s := range inc.Services {
		if n := strings.TrimSpace(s.Name); n != "" {
			svcs = append(svcs, n)
		}
	}
	if name != "" && len(svcs) > 0 {
		return name + " (" + strings.Join(svcs, ", ") + ")"
	}
	if name != "" {
		return name
	}
	return strings.Join(svcs, ", ")
}

// checklySeverity maps a Checkly incident severity onto a Severity.
//
// Checkly's scale is three levels — MINOR < MEDIUM < MAJOR — plus MAINTENANCE.
// MEDIUM was previously unmapped and fell through to the SevMinor default, which
// silently capped Mistral below the SevMajor alert threshold: every one of the
// 59 Mistral incident payloads captured between 2026-07-18 and 2026-07-23 was
// MEDIUM, so Mistral could not raise an alert at all. MAJOR is Checkly's top
// level and maps to SevCritical; CRITICAL is accepted defensively in case the
// vocabulary grows.
func checklySeverity(severity string) Severity {
	switch strings.ToUpper(strings.TrimSpace(severity)) {
	case "CRITICAL":
		return SevCritical
	case "MAJOR":
		return SevCritical
	case "MEDIUM":
		return SevMajor
	case "MINOR":
		return SevMinor
	case "MAINTENANCE":
		// Planned maintenance is a minor, expected degradation.
		return SevMinor
	default:
		// An open incident with an unknown severity is still a degradation. Log
		// it: an unrecognized level silently collapsing to SevMinor is exactly
		// how MEDIUM went unnoticed.
		slog.Warn("checkly: unknown incident severity, treating as minor", "severity", severity)
		return SevMinor
	}
}
