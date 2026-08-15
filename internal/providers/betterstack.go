package providers

import (
	"encoding/json"
	"strings"
)

// init self-registers the Better Stack adapter and the Hugging Face provider it
// backs.
//
// Hugging Face's status page (status.huggingface.co) is a Better Stack status
// page, NOT an Atlassian Statuspage — the old /api/v2/summary.json endpoint
// 301-redirects away, which is why Hugging Face always showed up as
// feed-unreachable. Better Stack exposes the whole page as JSON at /index.json.
func init() {
	RegisterParser(KindBetterStack, func(_ Provider, data []byte) (Result, error) {
		return parseBetterStack(data)
	})
	Register(Provider{
		ID: "huggingface", Name: "Hugging Face", Category: CategoryAI, Kind: KindBetterStack,
		URL: "https://status.huggingface.co/index.json",
	})
}

// betterStackPage models the relevant subset of a Better Stack status page
// /index.json (a JSON:API document). The overall state lives on the page;
// per-resource states and open reports live in "included".
type betterStackPage struct {
	Data struct {
		Attributes struct {
			AggregateState string `json:"aggregate_state"`
		} `json:"attributes"`
	} `json:"data"`
	Included []struct {
		Type       string `json:"type"`
		Attributes struct {
			PublicName     string `json:"public_name"`
			Status         string `json:"status"`
			Title          string `json:"title"`
			AggregateState string `json:"aggregate_state"`
			EndsAt         string `json:"ends_at"`
		} `json:"attributes"`
	} `json:"included"`
}

// parseBetterStack derives overall severity from the page's aggregate_state and
// surfaces each non-operational resource and each unresolved report as an
// incident. Better Stack pages carry no region data, so incidents are global.
func parseBetterStack(data []byte) (Result, error) {
	var page betterStackPage
	if err := json.Unmarshal(decodeFeed(data), &page); err != nil {
		return Result{}, err
	}

	res := Result{Severity: betterStackSeverity(page.Data.Attributes.AggregateState)}
	for _, inc := range page.Included {
		a := inc.Attributes
		switch inc.Type {
		case "status_page_resource":
			if sev := betterStackSeverity(a.Status); sev != SevNone {
				if sev > res.Severity {
					res.Severity = sev
				}
				name := strings.TrimSpace(a.PublicName)
				res.Incidents = append(res.Incidents, Incident{
					Summary:  strings.TrimSpace(name + " — " + a.Status),
					Severity: sev,
				})
			}
		case "status_report":
			// Only unresolved reports (no end time, not resolved) are active.
			if strings.TrimSpace(a.EndsAt) != "" || strings.EqualFold(a.AggregateState, "resolved") {
				continue
			}
			sev := betterStackSeverity(a.AggregateState)
			if sev == SevNone {
				sev = SevMinor
			}
			if sev > res.Severity {
				res.Severity = sev
			}
			if t := strings.TrimSpace(a.Title); t != "" {
				res.Incidents = append(res.Incidents, Incident{Summary: t, Severity: sev})
			}
		}
	}
	return res, nil
}

// betterStackSeverity maps a Better Stack state string onto a Severity.
func betterStackSeverity(state string) Severity {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "operational", "resolved":
		return SevNone
	case "maintenance", "under_maintenance", "degraded", "degraded_performance":
		return SevMinor
	case "partial_outage", "partial":
		return SevMajor
	case "downtime", "outage", "major_outage":
		return SevMajor
	default:
		return SevMinor
	}
}
