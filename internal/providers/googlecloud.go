package providers

import (
	"encoding/json"
	"strings"
)

// init self-registers the Google Cloud adapter and the two providers derived
// from its shared incidents.json feed: Google Cloud itself and Gemini (filtered
// to Gemini-affecting incidents).
func init() {
	RegisterParser(KindGoogleCloud, func(p Provider, data []byte) (Result, error) {
		return parseGoogleCloud(data, p.Filter)
	})
	Register(Provider{
		ID: "googlecloud", Name: "Google Cloud", Category: CategoryCloud, Kind: KindGoogleCloud,
		URL: "https://status.cloud.google.com/incidents.json",
	})
	Register(Provider{
		ID: "gemini", Name: "Gemini (Google AI)", Category: CategoryAI, Kind: KindGoogleCloud,
		URL: "https://status.cloud.google.com/incidents.json", Filter: "gemini",
	})
}

// gcLocation models an entry in a Google Cloud incident's affected-locations
// arrays (e.g. {"title":"Iowa (us-central1)","id":"us-central1"}).
type gcLocation struct {
	Title string `json:"title"`
	ID    string `json:"id"`
}

// gcIncident models the relevant subset of an entry in the Google Cloud
// status.cloud.google.com/incidents.json array.
type gcIncident struct {
	ExternalDesc     string `json:"external_desc"`
	Begin            string `json:"begin"`
	End              string `json:"end"`
	Severity         string `json:"severity"`
	AffectedProducts []struct {
		Title string `json:"title"`
	} `json:"affected_products"`
	// CurrentlyAffectedLocations are the locations the incident is still
	// impacting; Google Cloud reports these as {title, id} pairs.
	CurrentlyAffectedLocations []gcLocation `json:"currently_affected_locations"`
}

// parseGoogleCloud treats any open incident (no end time) as a degradation.
// When filter is non-empty only incidents touching a matching product (or whose
// description mentions it) are considered — used to derive Gemini health from
// the shared Google Cloud feed.
func parseGoogleCloud(data []byte, filter string) (Result, error) {
	var incidents []gcIncident
	if err := json.Unmarshal(decodeFeed(data), &incidents); err != nil {
		return Result{}, err
	}

	res := Result{Severity: SevNone}
	for _, inc := range incidents {
		if strings.TrimSpace(inc.End) != "" {
			continue // resolved
		}
		if filter != "" && !gcMatchesFilter(inc, filter) {
			continue
		}
		sev := gcSeverity(inc.Severity)
		if sev > res.Severity {
			res.Severity = sev
		}
		if inc.ExternalDesc != "" {
			res.Incidents = append(res.Incidents, Incident{
				Summary:  inc.ExternalDesc,
				Severity: sev,
				Regions:  gcRegions(inc),
			})
		}
	}
	return res, nil
}

// gcRegions returns the titles of an incident's currently-affected locations,
// or nil (global) when the incident is region-agnostic.
func gcRegions(inc gcIncident) []string {
	var out []string
	for _, loc := range inc.CurrentlyAffectedLocations {
		if t := strings.TrimSpace(loc.Title); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func gcMatchesFilter(inc gcIncident, filter string) bool {
	f := strings.ToLower(filter)
	if strings.Contains(strings.ToLower(inc.ExternalDesc), f) {
		return true
	}
	for _, p := range inc.AffectedProducts {
		if strings.Contains(strings.ToLower(p.Title), f) {
			return true
		}
	}
	return false
}

func gcSeverity(severity string) Severity {
	switch strings.ToLower(severity) {
	case "high":
		return SevMajor
	case "medium":
		return SevMinor
	case "low":
		return SevMinor
	default:
		// An open incident with an unknown severity is still a degradation.
		return SevMinor
	}
}
