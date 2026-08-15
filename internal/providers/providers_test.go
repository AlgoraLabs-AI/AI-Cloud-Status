package providers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestParseStatuspageNone(t *testing.T) {
	res, err := parseStatuspage(readFixture(t, "statuspage_none.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone {
		t.Errorf("severity = %v, want none", res.Severity)
	}
	if len(res.Incidents) != 0 {
		t.Errorf("incidents = %v, want none", res.Incidents)
	}
}

// TestParseStatuspageIndicatorWithoutIncidents pins the component-noise rule:
// a non-none page indicator with ZERO unresolved incidents reads as
// operational. Regression for Cloudflare, whose perpetually re-routed PoP
// components keep the indicator at "minor" with no incident posted — the row
// showed "Degraded" while its own detail said "No active incidents".
func TestParseStatuspageIndicatorWithoutIncidents(t *testing.T) {
	const feed = `{
  "status": {"indicator": "minor", "description": "Minor Service Outage"},
  "incidents": [
    {"name": "Old problem", "status": "resolved", "impact": "minor"}
  ]
}`
	res, err := parseStatuspage([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone {
		t.Errorf("severity = %v, want none (indicator without incidents is noise)", res.Severity)
	}
	if len(res.Incidents) != 0 {
		t.Errorf("incidents = %v, want none", res.Incidents)
	}

	// Only MINOR is suppressed: a major/critical indicator with no incident
	// posted (components flipped before/without an incident) is a real outage
	// signal — silencing it would swallow the alert and record green uptime.
	const critical = `{
  "status": {"indicator": "critical", "description": "Major Outage"},
  "incidents": []
}`
	res, err = parseStatuspage([]byte(critical))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevCritical {
		t.Errorf("critical indicator without incidents = %v, want critical kept", res.Severity)
	}
}

// TestProviderStatusPage pins the human-page routing: user-facing "open the
// status page" actions get the curated page when known, and only fall back to
// the machine feed URL for providers without one (raw JSON is a last resort).
func TestProviderStatusPage(t *testing.T) {
	p := Provider{ID: "cloudflare", URL: "https://www.cloudflarestatus.com/api/v2/summary.json"}
	if got := p.StatusPage(); got != "https://www.cloudflarestatus.com" {
		t.Errorf("StatusPage = %q, want the curated human page", got)
	}
	unknown := Provider{ID: "no-such-provider", URL: "https://example.com/feed.json"}
	if got := unknown.StatusPage(); got != "https://example.com/feed.json" {
		t.Errorf("StatusPage fallback = %q, want the feed URL", got)
	}
	// Every registered provider must have a curated human page — a new provider
	// registered without one would silently open raw JSON from its row.
	for _, p := range Default() {
		if StatusPageURL(p.ID) == "" {
			t.Errorf("provider %q has no curated status page URL", p.ID)
		}
	}
}

func TestParseStatuspageMajor(t *testing.T) {
	res, err := parseStatuspage(readFixture(t, "statuspage_major.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major", res.Severity)
	}
	// Two unresolved incidents; the resolved one must be excluded.
	if len(res.Incidents) != 2 {
		t.Fatalf("incidents = %v, want 2 unresolved", res.Incidents)
	}
	if res.Incidents[0].Summary != "Elevated errors on the Messages API" {
		t.Errorf("first incident = %q", res.Incidents[0].Summary)
	}
}

// TestParseStatuspageDrillDownFields verifies the optional incident detail
// (components, started/updated times, source link) is extracted for the
// drill-down panel, and that a missing/unparseable timestamp yields the zero
// time rather than an error.
func TestParseStatuspageDrillDownFields(t *testing.T) {
	const feed = `{
  "status": {"indicator": "major", "description": "Partial outage"},
  "incidents": [
    {
      "name": "Elevated API errors",
      "status": "investigating",
      "impact": "major",
      "shortlink": "https://stspg.io/abc",
      "created_at": "2026-06-26T10:00:00Z",
      "updated_at": "2026-06-26T10:30:00Z",
      "components": [{"name": "API"}, {"name": "Dashboard"}, {"name": ""}]
    },
    {
      "name": "Timeless incident",
      "status": "monitoring",
      "impact": "minor",
      "created_at": "",
      "updated_at": "not-a-time"
    }
  ]
}`
	res, err := parseStatuspage([]byte(feed))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Incidents) != 2 {
		t.Fatalf("incidents = %d, want 2", len(res.Incidents))
	}
	first := res.Incidents[0]
	if first.URL != "https://stspg.io/abc" {
		t.Errorf("URL = %q, want shortlink", first.URL)
	}
	if !slices.Equal(first.Components, []string{"API", "Dashboard"}) {
		t.Errorf("Components = %v, want [API Dashboard] (blank dropped)", first.Components)
	}
	if first.Started.IsZero() || first.Updated.IsZero() {
		t.Errorf("Started/Updated should be parsed, got %v / %v", first.Started, first.Updated)
	}
	if !first.Updated.After(first.Started) {
		t.Errorf("Updated %v should be after Started %v", first.Updated, first.Started)
	}
	second := res.Incidents[1]
	if !second.Started.IsZero() || !second.Updated.IsZero() {
		t.Errorf("empty/invalid timestamps should be zero, got %v / %v", second.Started, second.Updated)
	}
}

func TestStatuspageIndicatorSeverity(t *testing.T) {
	cases := map[string]Severity{
		"none":     SevNone,
		"":         SevNone,
		"minor":    SevMinor,
		"major":    SevMajor,
		"critical": SevCritical,
		"weird":    SevMinor,
	}
	for in, want := range cases {
		if got := statuspageIndicatorSeverity(in); got != want {
			t.Errorf("indicator %q -> %v, want %v", in, got, want)
		}
	}
}

func TestParseGoogleCloudOpenIncidents(t *testing.T) {
	res, err := parseGoogleCloud(readFixture(t, "googlecloud_incidents.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	// Highest open severity is "high" -> major; resolved high is ignored.
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major", res.Severity)
	}
	if len(res.Incidents) != 2 {
		t.Errorf("incidents = %v, want 2 open", res.Incidents)
	}
}

func TestParseGoogleCloudGeminiFilter(t *testing.T) {
	res, err := parseGoogleCloud(readFixture(t, "googlecloud_incidents.json"), "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major (Gemini open)", res.Severity)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("incidents = %v, want only the Gemini one", res.Incidents)
	}
}

func TestParseGoogleCloudAllClear(t *testing.T) {
	res, err := parseGoogleCloud(readFixture(t, "googlecloud_allclear.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone {
		t.Errorf("severity = %v, want none", res.Severity)
	}
}

func TestParseAWSCurrent(t *testing.T) {
	res, err := parseAWS(readFixture(t, "aws_current.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major", res.Severity)
	}
	if len(res.Incidents) != 2 {
		t.Errorf("incidents = %v, want 2", res.Incidents)
	}
}

func TestParseAWSEmpty(t *testing.T) {
	res, err := parseAWS(readFixture(t, "aws_empty.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone {
		t.Errorf("severity = %v, want none", res.Severity)
	}
}

func TestParseAWSBareArray(t *testing.T) {
	res, err := parseAWS([]byte(`[{"service_name":"AWS Lambda","summary":"Invocation failures","status":"3"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevCritical {
		t.Errorf("severity = %v, want critical", res.Severity)
	}
}

// TestStatuspageIncidentNote pins the latest-update note: the newest
// incident_updates body by timestamp becomes Incident.Note.
func TestStatuspageIncidentNote(t *testing.T) {
	feed := `{"status":{"indicator":"minor","description":"Minor"},
	 "incidents":[{"name":"Workers AI degraded","status":"investigating","impact":"minor",
	  "created_at":"2026-07-17T10:00:00Z","updated_at":"2026-07-17T12:00:00Z",
	  "incident_updates":[
	   {"body":"We are continuing to investigate.","created_at":"2026-07-17T12:00:00Z"},
	   {"body":"Investigating degraded availability.","created_at":"2026-07-17T10:00:00Z"}
	  ]}]}`
	res, err := parseStatuspage([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(res.Incidents))
	}
	inc := res.Incidents[0]
	if inc.Note != "We are continuing to investigate." {
		t.Errorf("Note = %q, want the NEWEST update body", inc.Note)
	}
	if inc.Started.IsZero() || inc.Updated.IsZero() {
		t.Errorf("Started/Updated must be parsed: %v / %v", inc.Started, inc.Updated)
	}
}

// TestParseChecklyTimesAndNote pins the Checkly (Mistral) incident metadata:
// created_at → Started, newest update → Updated + Note.
func TestParseChecklyTimesAndNote(t *testing.T) {
	feed := `{"incidents":[{"name":"Audio API Degraded","severity":"MEDIUM",
	 "created_at":"2026-07-17T07:55:56.406Z","updated_at":"2026-07-17T07:55:56.428Z",
	 "services":[{"name":"Audio API"}],
	 "incidentUpdates":[{"status":"INVESTIGATING",
	  "description":"Requests to the Audio API are experiencing degraded service.",
	  "created_at":"2026-07-17T08:30:00.000Z"}]}]}`
	res, err := parseCheckly([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(res.Incidents))
	}
	inc := res.Incidents[0]
	if inc.Note == "" || inc.Started.IsZero() {
		t.Errorf("Note/Started missing: %+v", inc)
	}
	// Updated must advance to the newest update's timestamp (08:30 > 07:55).
	if inc.Updated.UTC().Hour() != 8 {
		t.Errorf("Updated = %v, want the newest update time", inc.Updated)
	}
}

// TestParseAWSStaleEvents pins the zombie-event demotion: an event still open
// ("Disrupted") whose last update is older than awsStaleAfter is KEPT but
// demoted to SevMinor. Regression for the real me-central-1 (UAE) /
// me-south-1 (Bahrain) events that AWS left open for months after a February
// power issue: dropping them entirely showed a green "Operational" row while
// AWS's own health page listed 2 open issues; keeping them at full severity
// would pin the row red forever. Amber mirrors the page without the noise.
func TestParseAWSStaleEvents(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fresh := now.Add(-2 * 24 * time.Hour).Unix()
	stale := now.Add(-16 * 24 * time.Hour).Unix()

	feed := fmt.Sprintf(`[
		{"service_name":"Multiple services","summary":"Increased Error Rates","status":"3",
		 "region_name":"UAE","arn":"arn:aws:health:me-central-1::event/x/y/z",
		 "date":"%d","event_log":[{"timestamp":%d},{"timestamp":%d}]},
		{"service_name":"AWS Lambda","summary":"Invocation failures","status":"3",
		 "date":"%d","event_log":[{"timestamp":%d}]}
	]`, stale, stale, stale, fresh, fresh)

	res, err := parseAWSAt([]byte(feed), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Incidents) != 2 {
		t.Fatalf("incidents = %v, want both (stale demoted, not dropped)", res.Incidents)
	}
	bySummary := map[string]Incident{}
	for _, inc := range res.Incidents {
		bySummary[inc.Summary] = inc
	}
	if got := bySummary["Multiple services: Increased Error Rates"].Severity; got != SevMinor {
		t.Errorf("stale incident severity = %v, want minor (demoted)", got)
	}
	if got := bySummary["AWS Lambda: Invocation failures"].Severity; got != SevCritical {
		t.Errorf("fresh incident severity = %v, want critical (untouched)", got)
	}
	if res.Severity != SevCritical {
		t.Errorf("severity = %v, want critical from the fresh event", res.Severity)
	}

	// A stale-only feed reads as a minor degradation — never green, never red.
	feed = fmt.Sprintf(`[{"service_name":"EC2","summary":"x","status":"3",
		"region_name":"UAE","date":"%d","event_log":[{"timestamp":%d}]}]`, stale, stale)
	res, err = parseAWSAt([]byte(feed), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Incidents) != 1 || res.Severity != SevMinor {
		t.Fatalf("stale-only feed = %v severity %v, want 1 incident at minor", res.Incidents, res.Severity)
	}

	// A newest event_log entry within the window keeps full severity even when
	// the start date is old — staleness is about the LAST UPDATE, not the start.
	feed = fmt.Sprintf(`[{"service_name":"EC2","summary":"x","status":"3",
		"date":"%d","event_log":[{"timestamp":%d},{"timestamp":%d}]}]`, stale, stale, fresh)
	res, err = parseAWSAt([]byte(feed), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Incidents) != 1 || res.Severity != SevCritical {
		t.Fatalf("recently-updated old event must keep full severity, got %v severity %v", res.Incidents, res.Severity)
	}
}

// An open event with NO timestamps at all can never be declared stale — the
// filter fails open so missing feed data never hides a real disruption.
func TestParseAWSStaleFailsOpen(t *testing.T) {
	res, err := parseAWSAt([]byte(`[{"service_name":"EC2","summary":"down","status":"3"}]`),
		time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Incidents) != 1 || res.Severity != SevCritical {
		t.Fatalf("timestamp-less open event must be kept: %+v", res)
	}
}

// TestParseAWSStatusCodes pins the numeric AWS status-code -> Severity mapping.
func TestParseAWSStatusCodes(t *testing.T) {
	cases := map[string]Severity{"0": SevNone, "1": SevMinor, "2": SevMajor, "3": SevCritical}
	for code, want := range cases {
		ev := awsEvent{Status: code}
		if got := awsEventSeverity(ev); got != want {
			t.Errorf("status %q -> %v, want %v", code, got, want)
		}
	}
	// status "0" (operational) must not produce an incident.
	res, err := parseAWS([]byte(`[{"service_name":"EC2","summary":"ok","status":"0"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone || len(res.Incidents) != 0 {
		t.Errorf("status 0 produced %v with %d incidents, want none/0", res.Severity, len(res.Incidents))
	}
}

// TestParseAWSUTF16 verifies the real-world fix: AWS serves currentevents as
// UTF-16 (BE) with a BOM, which a naive UTF-8/JSON parser cannot read. The bytes
// must be decoded before parsing.
func TestParseAWSUTF16(t *testing.T) {
	jsonUTF8 := `[{"service_name":"Amazon EC2","summary":"Increased Error Rates","status":"3","arn":"arn:aws:health:me-central-1::event/x/y/z"}]`
	res, err := parseAWS(encodeUTF16BE(jsonUTF8))
	if err != nil {
		t.Fatalf("parseAWS on UTF-16 feed: %v", err)
	}
	if res.Severity != SevCritical {
		t.Errorf("severity = %v, want critical", res.Severity)
	}
	inc := findIncident(res.Incidents, "Increased Error Rates")
	if !slices.Equal(inc.Regions, []string{"me-central-1"}) {
		t.Errorf("regions = %v, want [me-central-1] (parsed from ARN)", inc.Regions)
	}
}

// encodeUTF16BE encodes a UTF-8 string as UTF-16 big-endian with a BOM, matching
// how AWS serves its public events feed.
func encodeUTF16BE(s string) []byte {
	out := []byte{0xFE, 0xFF}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u>>8), byte(u))
	}
	return out
}

func TestParseAzureFeed(t *testing.T) {
	res, err := parseAzure(readFixture(t, "azure_feed.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major", res.Severity)
	}
	// The resolved item must be excluded.
	if len(res.Incidents) != 1 {
		t.Fatalf("incidents = %v, want 1 open", res.Incidents)
	}
}

// TestAzureFeedURL guards the host the Azure feed is read from.
// "status.azure.com" redirects to a GEO-ROUTED status instance, and a degraded
// instance serves a valid feed with ZERO items — indistinguishable from
// all-clear. On 2026-07-23 that host (routed to the West US instance) reported
// an empty channel for hours while the West US network outage was live on the
// canonical feed, so the app showed Azure green throughout.
func TestAzureFeedURL(t *testing.T) {
	if strings.Contains(azureFeedURL, "status.azure.com") {
		t.Fatalf("azureFeedURL = %q: the geo-routed host can serve an empty feed "+
			"during an outage; use the canonical rssfeed host", azureFeedURL)
	}
	var azure Provider
	for _, p := range Default() {
		if p.ID == "azure" {
			azure = p
		}
	}
	if azure.URL != azureFeedURL {
		t.Errorf("registered Azure URL = %q, want %q", azure.URL, azureFeedURL)
	}
}

// azureWestUSNow is a fixed "now" inside the azure_live_westus.xml fixture's
// window, mirroring xaiNow. Against the wall clock this test passed for exactly
// StaleAfter (15 days) from the capture and then began failing every run: the
// incident aged past the zombie horizon, parseAzure demoted it to minor, and the
// severity assertion broke — a red suite caused by the calendar, with nothing
// wrong in the parser it was written to pin.
var azureWestUSNow = time.Date(2026, 7, 23, 18, 0, 0, 0, time.UTC)

// TestParseAzureLiveWestUS parses the real payload captured during the
// 2026-07-23 West US network incident: the region must come off the <category>
// element, the service area must land in Components, and pubDate/description
// must populate Updated/Note.
func TestParseAzureLiveWestUS(t *testing.T) {
	res, err := parseAzureAt(readFixture(t, "azure_live_westus.xml"), azureWestUSNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Fatalf("severity = %v, want major", res.Severity)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(res.Incidents))
	}
	inc := res.Incidents[0]
	if !strings.Contains(inc.Summary, "Issues connecting to resources in West US") {
		t.Errorf("summary = %q", inc.Summary)
	}
	if len(inc.Regions) != 1 || inc.Regions[0] != "West US" {
		t.Errorf("regions = %v, want [West US]", inc.Regions)
	}
	// "West US" is also a <category>; it must not be duplicated as a component.
	if len(inc.Components) != 1 || inc.Components[0] != "Network Infrastructure" {
		t.Errorf("components = %v, want [Network Infrastructure]", inc.Components)
	}
	if inc.Updated.IsZero() {
		t.Error("Updated is zero, want the pubDate")
	}
	if strings.Contains(inc.Note, "<") || !strings.Contains(inc.Note, "networking issue") {
		t.Errorf("Note not stripped to plain text: %q", inc.Note)
	}
}

// TestAzureTime pins the pubDate shapes Azure emits — the live feed uses a bare
// "Z" zone, which neither RFC1123 nor RFC1123Z accepts.
func TestAzureTime(t *testing.T) {
	for _, in := range []string{
		"Thu, 23 Jul 2026 16:29:09 Z",
		"Thu, 23 Jul 2026 16:29:09 +0000",
		"2026-07-23T16:29:09Z",
	} {
		got := azureTime(in)
		if got.IsZero() || got.UTC().Hour() != 16 {
			t.Errorf("azureTime(%q) = %v, want 16:29:09 UTC", in, got)
		}
	}
	if !azureTime("").IsZero() || !azureTime("not a date").IsZero() {
		t.Error("unparseable pubDate should yield the zero time")
	}
}

func TestParseAzureAllClear(t *testing.T) {
	res, err := parseAzure(readFixture(t, "azure_allclear.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone {
		t.Errorf("severity = %v, want none", res.Severity)
	}
}

// findIncident returns the first incident whose summary contains sub, or a zero
// Incident if none match.
func findIncident(incs []Incident, sub string) Incident {
	for _, inc := range incs {
		if strings.Contains(inc.Summary, sub) {
			return inc
		}
	}
	return Incident{}
}

func TestParseAWSRegions(t *testing.T) {
	res, err := parseAWS(readFixture(t, "aws_regional.json"))
	if err != nil {
		t.Fatal(err)
	}
	// us-east-1 outage dominates -> critical overall.
	if res.Severity != SevCritical {
		t.Errorf("severity = %v, want critical", res.Severity)
	}
	cases := []struct {
		sub     string
		sev     Severity
		regions []string
	}{
		{"Instances unreachable", SevCritical, []string{"us-east-1"}},
		{"Elevated query latency", SevMajor, []string{"eu-west-1"}},
		{"Informational notice", SevMinor, nil}, // no region -> global
	}
	for _, tc := range cases {
		inc := findIncident(res.Incidents, tc.sub)
		if inc.Summary == "" {
			t.Errorf("incident %q not found", tc.sub)
			continue
		}
		if inc.Severity != tc.sev {
			t.Errorf("%q severity = %v, want %v", tc.sub, inc.Severity, tc.sev)
		}
		if !slices.Equal(inc.Regions, tc.regions) {
			t.Errorf("%q regions = %v, want %v", tc.sub, inc.Regions, tc.regions)
		}
	}
}

// TestParseAWSRegionFromServiceName exercises the fallback that extracts a
// region embedded in the service name when no region field is present.
func TestParseAWSRegionFromServiceName(t *testing.T) {
	res, err := parseAWS(readFixture(t, "aws_current.json"))
	if err != nil {
		t.Fatal(err)
	}
	inc := findIncident(res.Incidents, "Increased API error rates")
	if !slices.Equal(inc.Regions, []string{"us-east-1"}) {
		t.Errorf("regions = %v, want [us-east-1] (parsed from service name)", inc.Regions)
	}
}

func TestParseAzureRegions(t *testing.T) {
	res, err := parseAzure(readFixture(t, "azure_regional.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major", res.Severity)
	}
	// Four open items; the resolved North Europe item must be excluded.
	if len(res.Incidents) != 4 {
		t.Fatalf("incidents = %d, want 4 open", len(res.Incidents))
	}
	cases := []struct {
		sub     string
		regions []string
	}{
		{"Virtual Machines", []string{"East US"}}, // must NOT pick up "East US 2"
		{"App Service", []string{"East US 2"}},    // must NOT also tag "East US"
		{"Storage", []string{"West Europe"}},
		{"Azure DNS", nil}, // no known region -> global
	}
	for _, tc := range cases {
		inc := findIncident(res.Incidents, tc.sub)
		if inc.Summary == "" {
			t.Errorf("incident %q not found", tc.sub)
			continue
		}
		if !slices.Equal(inc.Regions, tc.regions) {
			t.Errorf("%q regions = %v, want %v", tc.sub, inc.Regions, tc.regions)
		}
	}
}

func TestParseGoogleCloudRegions(t *testing.T) {
	res, err := parseGoogleCloud(readFixture(t, "googlecloud_regional.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major", res.Severity)
	}
	if len(res.Incidents) != 2 {
		t.Fatalf("incidents = %d, want 2 open (resolved excluded)", len(res.Incidents))
	}
	multi := findIncident(res.Incidents, "Compute Engine VMs unreachable")
	wantRegions := []string{"Iowa (us-central1)", "Belgium (europe-west1)"}
	if !slices.Equal(multi.Regions, wantRegions) {
		t.Errorf("regions = %v, want %v", multi.Regions, wantRegions)
	}
	global := findIncident(res.Incidents, "Cloud IAM global")
	if !global.IsGlobal() {
		t.Errorf("global incident has regions %v, want none", global.Regions)
	}
}

func TestMatchRegion(t *testing.T) {
	cases := []struct {
		region   string
		interest []string
		want     bool
	}{
		{"us-east-1", nil, true},                             // empty interest matches all
		{"us-east-1", []string{"us-east-1"}, true},           // exact
		{"US East (us-east-1)", []string{"us-east-1"}, true}, // feed superset of interest
		{"us-east-1", []string{"US East (us-east-1)"}, true}, // interest superset of feed
		{"us-east-1", []string{"eu-west-1"}, false},          // no overlap
		{"", []string{"us-east-1"}, false},                   // empty region with interest
		{"EU-WEST-1", []string{"eu-west-1"}, true},           // case-insensitive
	}
	for _, tc := range cases {
		if got := MatchRegion(tc.region, tc.interest); got != tc.want {
			t.Errorf("MatchRegion(%q, %v) = %v, want %v", tc.region, tc.interest, got, tc.want)
		}
	}
}

func TestResultRegionScoping(t *testing.T) {
	res := Result{
		Severity: SevCritical,
		Incidents: []Incident{
			{Summary: "outage A", Severity: SevCritical, Regions: []string{"us-east-1"}},
			{Summary: "blip B", Severity: SevMinor, Regions: []string{"eu-west-1"}},
			{Summary: "global C", Severity: SevMajor}, // global
		},
	}

	// No interest -> global behavior preserved.
	if got := res.SeverityInRegions(nil); got != SevCritical {
		t.Errorf("SeverityInRegions(nil) = %v, want critical (global path unchanged)", got)
	}

	// Interest in eu-west-1: the eu-west-1 minor + the global major are in scope;
	// the us-east-1 critical is filtered out -> worst is major.
	if got := res.SeverityInRegions([]string{"eu-west-1"}); got != SevMajor {
		t.Errorf("SeverityInRegions(eu-west-1) = %v, want major", got)
	}
	inScope := res.IncidentsInRegions([]string{"eu-west-1"})
	if len(inScope) != 2 { // blip B + global C
		t.Errorf("IncidentsInRegions(eu-west-1) = %d incidents, want 2", len(inScope))
	}

	// Interest in us-east-1: critical regional + global major in scope -> critical.
	if got := res.SeverityInRegions([]string{"us-east-1"}); got != SevCritical {
		t.Errorf("SeverityInRegions(us-east-1) = %v, want critical", got)
	}

	want := []string{"eu-west-1", "us-east-1"} // sorted, global excluded
	if got := res.AffectedRegions(); !slices.Equal(got, want) {
		t.Errorf("AffectedRegions() = %v, want %v", got, want)
	}
}

// TestThreeStateModel pins the core three-state distinction: a feed we could not
// read is "feed-unreachable" (unknown) and must NEVER collapse to ServiceDown,
// regardless of region filtering.
func TestThreeStateModel(t *testing.T) {
	up := Result{Feed: FeedReachable, Severity: SevNone}
	if up.ServiceState() != ServiceUp || !up.Reachable() {
		t.Errorf("operational result = %v (reachable %v), want up/true", up.ServiceState(), up.Reachable())
	}

	down := Result{Feed: FeedReachable, Severity: SevMajor,
		Incidents: []Incident{{Summary: "outage", Severity: SevMajor, Regions: []string{"us-east-1"}}}}
	if down.ServiceState() != ServiceDown {
		t.Errorf("degraded result = %v, want down", down.ServiceState())
	}

	noFeed := Result{Feed: FeedUnreachable}
	if noFeed.ServiceState() != ServiceFeedUnreachable || noFeed.Reachable() {
		t.Errorf("unreadable feed = %v (reachable %v), want feed-unreachable/false", noFeed.ServiceState(), noFeed.Reachable())
	}
	// A feed-unreachable result stays feed-unreachable under ANY region filter —
	// it must not be reinterpreted as up or down.
	for _, interest := range [][]string{nil, {"us-east-1"}, {"eu-west-1"}} {
		if got := noFeed.ServiceStateInRegions(interest); got != ServiceFeedUnreachable {
			t.Errorf("ServiceStateInRegions(%v) = %v, want feed-unreachable", interest, got)
		}
	}

	// Region scoping flips a globally-down result to up when the only incident is
	// out of the regions of interest — but the feed is still reachable.
	if got := down.ServiceStateInRegions([]string{"eu-west-1"}); got != ServiceUp {
		t.Errorf("down result scoped to eu-west-1 = %v, want up (us-east-1 incident filtered out)", got)
	}
}

func TestServiceStateString(t *testing.T) {
	cases := map[ServiceState]string{
		ServiceUp: "up", ServiceDown: "down", ServiceFeedUnreachable: "feed-unreachable",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", s, got, want)
		}
	}
}

func TestIncidentLabel(t *testing.T) {
	global := Incident{Summary: "Auth delays"}
	if got := global.Label(); got != "Auth delays" {
		t.Errorf("global Label() = %q, want %q", got, "Auth delays")
	}
	regional := Incident{Summary: "VM outage", Regions: []string{"us-east-1", "us-west-2"}}
	want := "VM outage (us-east-1, us-west-2)"
	if got := regional.Label(); got != want {
		t.Errorf("regional Label() = %q, want %q", got, want)
	}
}

func TestSeverityString(t *testing.T) {
	cases := map[Severity]string{
		SevNone:     "none",
		SevMinor:    "minor",
		SevMajor:    "major",
		SevCritical: "critical",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", s, got, want)
		}
	}
}

func TestDefaultProvidersWellFormed(t *testing.T) {
	validCat := map[Category]bool{}
	for _, c := range CategoryOrder {
		validCat[c] = true
	}

	seen := map[string]bool{}
	for _, p := range Default() {
		if p.ID == "" || p.Name == "" || p.URL == "" || p.Kind == "" {
			t.Errorf("provider %+v missing required field", p)
		}
		if !validCat[p.Category] {
			t.Errorf("provider %q has category %q not in CategoryOrder", p.ID, p.Category)
		}
		if seen[p.ID] {
			t.Errorf("duplicate provider ID %q", p.ID)
		}
		seen[p.ID] = true
	}
}

// TestDefaultProvidersQueryStatusAPIs guards intent (2): every provider must
// hit a machine-readable status/health API (so results reflect true outage
// state), never a bare marketing domain.
func TestDefaultProvidersQueryStatusAPIs(t *testing.T) {
	apiMarker := []string{"summary.json", "incidents.json", "currentevents", "/status/feed", "unresolved-incidents", "index.json", "feed.xml"}
	for _, p := range Default() {
		ok := false
		for _, m := range apiMarker {
			if strings.Contains(p.URL, m) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("provider %q URL %q does not look like a status API endpoint", p.ID, p.URL)
		}
	}
}

func TestDefaultProvidersCategoryAssignment(t *testing.T) {
	want := map[string]Category{
		"openai": CategoryAI, "anthropic": CategoryAI, "gemini": CategoryAI,
		"mistral": CategoryAI, "cohere": CategoryAI,
		"perplexity": CategoryAI, "huggingface": CategoryAI, "groq": CategoryAI,
		"xai":        CategoryAI,
		"cloudflare": CategoryCloud, "googlecloud": CategoryCloud,
		"aws": CategoryCloud, "azure": CategoryCloud,
		"github": CategoryDev, "bitbucket": CategoryDev,
	}
	got := map[string]Category{}
	for _, p := range Default() {
		got[p.ID] = p.Category
	}
	for id, cat := range want {
		if got[id] != cat {
			t.Errorf("provider %q category = %q, want %q", id, got[id], cat)
		}
	}
	if len(got) != len(want) {
		t.Errorf("provider count = %d, want %d (update test if list changed)", len(got), len(want))
	}
}

// TestDefaultGroupedByCategory checks the self-registered providers come back
// grouped by category display order (Cloud, then AI, then Dev), so the modular
// registration does not scramble the UI layout.
func TestDefaultGroupedByCategory(t *testing.T) {
	rank := map[Category]int{}
	for i, c := range CategoryOrder {
		rank[c] = i
	}
	prev := -1
	for _, p := range Default() {
		r, ok := rank[p.Category]
		if !ok {
			t.Fatalf("provider %q has category %q outside CategoryOrder", p.ID, p.Category)
		}
		if r < prev {
			t.Errorf("provider %q (category %q) out of category order", p.ID, p.Category)
		}
		prev = r
	}
}

// TestModularSelfRegistration guards the modular architecture: a parser is
// registered for every Kind referenced by a provider, and Bitbucket — added as a
// single new file — is present.
func TestModularSelfRegistration(t *testing.T) {
	for _, p := range Default() {
		if _, ok := parserFor(p.Kind); !ok {
			t.Errorf("provider %q uses kind %q with no registered parser", p.ID, p.Kind)
		}
	}
	if _, ok := parserFor(KindCheckly); !ok {
		t.Error("Checkly adapter did not self-register")
	}
	if _, ok := parserFor(KindBetterStack); !ok {
		t.Error("Better Stack adapter did not self-register")
	}
	found := false
	for _, p := range Default() {
		if p.ID == "bitbucket" {
			found = true
		}
	}
	if !found {
		t.Error("bitbucket provider did not self-register")
	}
}

func TestDecodeFeed(t *testing.T) {
	const want = `[{"a":1}]`
	cases := map[string][]byte{
		"plain utf-8":     []byte(want),
		"utf-8 BOM":       append([]byte{0xEF, 0xBB, 0xBF}, []byte(want)...),
		"utf-16 BE w/BOM": encodeUTF16BE(want),
		"utf-16 LE w/BOM": encodeUTF16LE(want),
	}
	for name, in := range cases {
		if got := string(decodeFeed(in)); got != want {
			t.Errorf("%s: decodeFeed = %q, want %q", name, got, want)
		}
	}
}

func encodeUTF16LE(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func TestParseChecklyOpenIncidents(t *testing.T) {
	res, err := parseCheckly(readFixture(t, "checkly_incidents.json"))
	if err != nil {
		t.Fatal(err)
	}
	// MAJOR is the top of Checkly's MINOR < MEDIUM < MAJOR scale.
	if res.Severity != SevCritical {
		t.Errorf("severity = %v, want critical (worst open incident)", res.Severity)
	}
	if len(res.Incidents) != 2 {
		t.Fatalf("incidents = %d, want 2 open", len(res.Incidents))
	}
	inc := findIncident(res.Incidents, "Deep Research is down.")
	if !strings.Contains(inc.Summary, "Vibe") {
		t.Errorf("incident summary %q should name the affected service", inc.Summary)
	}
}

// TestChecklySeverityScale pins Checkly's three-level scale. MEDIUM used to be
// unmapped and fell through to the SevMinor default, capping Mistral below the
// SevMajor alert threshold: every Mistral incident payload captured over
// 2026-07-18..23 was MEDIUM, so Mistral could never raise an alert.
func TestChecklySeverityScale(t *testing.T) {
	cases := map[string]Severity{
		"MINOR":       SevMinor,
		"MEDIUM":      SevMajor,
		"MAJOR":       SevCritical,
		"CRITICAL":    SevCritical,
		"MAINTENANCE": SevMinor,
		"medium":      SevMajor, // case-insensitive
	}
	for in, want := range cases {
		if got := checklySeverity(in); got != want {
			t.Errorf("checklySeverity(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestParseChecklyMediumAlerts is the end-to-end guard on the above: the real
// captured Mistral payload must reach the SevMajor alert threshold.
func TestParseChecklyMediumAlerts(t *testing.T) {
	res, err := parseCheckly(readFixture(t, "checkly_medium.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity < SevMajor {
		t.Errorf("severity = %v, want >= major so a MEDIUM incident can alert", res.Severity)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(res.Incidents))
	}
}

func TestParseChecklyOperational(t *testing.T) {
	res, err := parseCheckly(readFixture(t, "checkly_operational.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone || len(res.Incidents) != 0 {
		t.Errorf("operational page = %v with %d incidents, want none/0", res.Severity, len(res.Incidents))
	}
}

func TestParseBetterStackOperational(t *testing.T) {
	res, err := parseBetterStack(readFixture(t, "betterstack_operational.json"))
	if err != nil {
		t.Fatal(err)
	}
	// aggregate_state operational + a RESOLVED report => no open issues.
	if res.Severity != SevNone {
		t.Errorf("severity = %v, want none", res.Severity)
	}
	if len(res.Incidents) != 0 {
		t.Errorf("incidents = %v, want none (resolved report excluded)", res.Labels())
	}
}

func TestParseBetterStackIncident(t *testing.T) {
	res, err := parseBetterStack(readFixture(t, "betterstack_incident.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major (downtime)", res.Severity)
	}
	// Two non-operational resources + one unresolved report; the operational
	// resource must be excluded.
	if findIncident(res.Incidents, "Spaces").Summary != "" {
		t.Error("operational resource 'Spaces' must not be reported as an incident")
	}
	if findIncident(res.Incidents, "Hub API returning elevated 5xx errors").Summary == "" {
		t.Error("unresolved report should be surfaced as an incident")
	}
}

// TestFeedCapture pins the raw-sample archive: noteworthy checks (incidents,
// parse errors) are saved, operational ones are not, identical consecutive
// payloads dedup, and retention ages out samples older than captureRetention
// regardless of count.
func TestFeedCapture(t *testing.T) {
	dir := t.TempDir()
	fc := NewFeedCapture(dir)
	p := Provider{ID: "prov"}

	// Operational result → nothing archived.
	fc.Capture(p, []byte(`{"ok":true}`), Result{Severity: SevNone}, nil)
	if _, err := os.Stat(filepath.Join(dir, "prov")); !os.IsNotExist(err) {
		t.Fatal("operational payload must not be captured")
	}

	// Incident → archived once; identical payload → deduped.
	res := Result{Severity: SevMinor, Incidents: []Incident{{Summary: "x", Severity: SevMinor}}}
	fc.Capture(p, []byte(`{"incident":1}`), res, nil)
	fc.Capture(p, []byte(`{"incident":1}`), res, nil)
	files, _ := os.ReadDir(filepath.Join(dir, "prov"))
	if len(files) != 1 {
		t.Fatalf("captured %d files, want 1 (dedup)", len(files))
	}

	// Parse error → archived even with an empty result.
	fc.Capture(p, []byte(`not json`), Result{Feed: FeedUnreachable}, errFake)
	files, _ = os.ReadDir(filepath.Join(dir, "prov"))
	if len(files) != 2 {
		t.Fatalf("captured %d files, want 2 (parse error archived)", len(files))
	}

	// Retention: many distinct fresh captures (fabricated one second apart, so
	// each gets its own filename) are never pruned by count alone.
	provDir := filepath.Join(dir, "prov")
	const burst = 60
	// Offset well clear of "now" so none of these synthetic names can collide
	// with the two real captures written above by Capture (filename
	// granularity is 1 second).
	base := time.Now().Add(-time.Hour)
	for i := 0; i < burst; i++ {
		name := base.Add(-time.Duration(i)*time.Second).Format(captureTimeFormat) + "-minor.json"
		if err := os.WriteFile(filepath.Join(provDir, name), fmt.Appendf(nil, `{"incident":%d}`, i), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fc.prune(provDir)
	files, _ = os.ReadDir(provDir)
	if len(files) != 2+burst {
		t.Fatalf("retained %d fresh files, want %d (no count cap)", len(files), 2+burst)
	}

	// A capture older than captureRetention is pruned.
	stale := time.Now().Add(-captureRetention-time.Hour).Format(captureTimeFormat) + "-minor.json"
	if err := os.WriteFile(filepath.Join(provDir, stale), []byte(`{"old":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fc.prune(provDir)
	if _, err := os.Stat(filepath.Join(provDir, stale)); !os.IsNotExist(err) {
		t.Fatal("capture older than captureRetention should have been pruned")
	}

	// A nil capture (not configured) must be a safe no-op.
	var none *FeedCapture
	none.Capture(p, []byte(`{"incident":1}`), res, nil)
}

var errFake = errors.New("boom")

// TestParseInstatusOperational pins the Perplexity fix: the live feed migrated
// from Atlassian Statuspage to Instatus, whose healthy summary is
// {"page":{"status":"UP"}} — the Statuspage parser read that as permanently
// operational with no way to ever report an incident.
func TestParseInstatusOperational(t *testing.T) {
	feed := `{"page":{"name":"Perplexity","url":"https://status.perplexity.com","status":"UP"}}`
	res, err := parseInstatus([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone || len(res.Incidents) != 0 {
		t.Errorf("UP page = %v with %d incidents, want none/0", res.Severity, len(res.Incidents))
	}
}

func TestParseInstatusIncident(t *testing.T) {
	feed := `{"page":{"name":"Perplexity","url":"https://status.perplexity.com","status":"HASISSUES"},
	 "activeIncidents":[
	  {"name":"API errors","status":"INVESTIGATING","impact":"MAJOROUTAGE",
	   "started":"Sat Jul 09 2022 04:20:00 GMT+0000 (Coordinated Universal Time)",
	   "url":"https://status.perplexity.com/incident/x",
	   "updates":[{"message":"We are investigating elevated error rates.",
	    "started":"Sat Jul 09 2022 05:00:00 GMT+0000 (Coordinated Universal Time)"}]},
	  {"name":"Old issue","status":"RESOLVED","impact":"PARTIALOUTAGE"}
	 ]}`
	res, err := parseInstatus([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major", res.Severity)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("incidents = %d, want 1 (RESOLVED filtered out)", len(res.Incidents))
	}
	inc := res.Incidents[0]
	if inc.Summary != "API errors" || inc.URL == "" {
		t.Errorf("incident fields wrong: %+v", inc)
	}
	if inc.Note != "We are investigating elevated error rates." {
		t.Errorf("Note = %q, want the update message", inc.Note)
	}
	// The JS-style date must parse: started 04:20 UTC, updated 05:00 UTC.
	if inc.Started.IsZero() || inc.Started.UTC().Hour() != 4 {
		t.Errorf("Started = %v, want 04:20 UTC parsed from JS-style date", inc.Started)
	}
	if inc.Updated.IsZero() || inc.Updated.UTC().Hour() != 5 {
		t.Errorf("Updated = %v, want the update's 05:00 UTC", inc.Updated)
	}
}

// TestParseInstatusHasIssuesWithoutDetail guards the conservative fallback: a
// page reporting HASISSUES whose incident list is empty or unparseable detail
// must still surface as degraded, never as operational.
func TestParseInstatusHasIssuesWithoutDetail(t *testing.T) {
	feed := `{"page":{"name":"Perplexity","url":"https://status.perplexity.com","status":"HASISSUES"}}`
	res, err := parseInstatus([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMinor {
		t.Errorf("severity = %v, want minor for HASISSUES without incident detail", res.Severity)
	}
}

// TestParseInstatusMaintenance: UNDERMAINTENANCE is planned work, not an outage.
func TestParseInstatusMaintenance(t *testing.T) {
	feed := `{"page":{"name":"Perplexity","url":"https://status.perplexity.com","status":"UNDERMAINTENANCE"}}`
	res, err := parseInstatus([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone {
		t.Errorf("severity = %v, want none for maintenance", res.Severity)
	}
}

// ---- xAI --------------------------------------------------------------------

// xaiNow is a fixed "now" inside the xai_active.xml fixture's window, so the
// stale-item demotion is exercised deterministically rather than against the
// wall clock.
var xaiNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// TestParseXAIAllResolved pins the load-bearing false-positive guard against
// the REAL live feed: it is a rolling history of 100+ entries that are almost
// always all resolved, and every one of them must read as operational. A
// regression here would report xAI as permanently down.
func TestParseXAIAllResolved(t *testing.T) {
	res, err := parseXAIAt(readFixture(t, "xai_resolved.xml"), xaiNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevNone {
		t.Errorf("severity = %v, want none — every item in this fixture is resolved", res.Severity)
	}
	if len(res.Incidents) != 0 {
		t.Errorf("incidents = %d (%v), want 0 — resolved history must never surface as active",
			len(res.Incidents), res.Labels())
	}
}

// TestParseXAIActive covers the open-incident path: severity mapping, the
// resolved item being excluded, region/component extraction, and the stale
// demotion — all from one fixture.
func TestParseXAIActive(t *testing.T) {
	res, err := parseXAIAt(readFixture(t, "xai_active.xml"), xaiNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Severity != SevMajor {
		t.Errorf("severity = %v, want major (an unavailable API incident is open)", res.Severity)
	}
	// 3 open items; the RESOLVED one must be excluded.
	if len(res.Incidents) != 3 {
		t.Fatalf("incidents = %d (%v), want 3 open (the resolved one excluded)", len(res.Incidents), res.Labels())
	}

	api := findIncident(res.Incidents, "Grok 4 requests returning errors")
	if api.Summary == "" {
		t.Fatal("open API incident missing")
	}
	if api.Severity != SevMajor {
		t.Errorf("API incident severity = %v, want major (severity: unavailable)", api.Severity)
	}
	if len(api.Regions) != 1 || api.Regions[0] != "us-east-1" {
		t.Errorf("API incident regions = %v, want [us-east-1] from the scope prefix", api.Regions)
	}
	if len(api.Components) != 1 || api.Components[0] != "API (us-east-1.api.x.ai)" {
		t.Errorf("API incident components = %v, want the bracketed scope", api.Components)
	}
	if !api.Updated.Equal(time.Date(2026, 7, 31, 11, 45, 0, 0, time.UTC)) {
		t.Errorf("API incident Updated = %v, want the NEWEST update (11:45), not the oldest", api.Updated)
	}
	if !strings.Contains(api.Note, "rolling out a fix") {
		t.Errorf("API incident note = %q, want the newest update's text", api.Note)
	}

	web := findIncident(res.Incidents, "Slower than usual response times")
	if web.Severity != SevMinor {
		t.Errorf("Grok Web severity = %v, want minor (severity: degraded)", web.Severity)
	}
	if len(web.Regions) != 0 {
		t.Errorf("Grok Web regions = %v, want none — app scopes are global, not region-partitioned", web.Regions)
	}

	// Open since 1 June but silent ever since: demoted so it cannot pin the
	// row red (and alert) forever.
	stale := findIncident(res.Incidents, "Console login failures")
	if stale.Summary == "" {
		t.Fatal("stale incident missing — it is still open and must stay visible")
	}
	if stale.Severity != SevMinor {
		t.Errorf("stale incident severity = %v, want minor (demoted: no update in %v)", stale.Severity, xaiStaleAfter)
	}

	if findIncident(res.Incidents, "Image generation was briefly unavailable").Summary != "" {
		t.Error("a RESOLVED item leaked into the incident list")
	}
}

// TestXAIResolvedDetection pins the resolution test itself: either signal
// (the labelled Status line or a <category>) is enough on its own, so one
// going missing cannot resurrect a closed incident.
func TestXAIResolvedDetection(t *testing.T) {
	cases := []struct {
		name string
		item xaiItem
		want bool
	}{
		{"status line only", xaiItem{Description: "<h3>Status: RESOLVED</h3>"}, true},
		{"category only", xaiItem{Categories: []string{"available", "resolved"}}, true},
		{"lowercase status", xaiItem{Description: "Status: resolved"}, true},
		{"completed counts as closed", xaiItem{Categories: []string{"completed"}}, true},
		{"investigating is open", xaiItem{Description: "<h3>Status: INVESTIGATING</h3>", Categories: []string{"unavailable", "investigating"}}, false},
		{"unknown open status stays open", xaiItem{Description: "<h3>Status: MITIGATING</h3>"}, false},
		{"no signal at all stays open", xaiItem{}, false},
	}
	for _, c := range cases {
		if got := xaiResolved(c.item); got != c.want {
			t.Errorf("%s: xaiResolved = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestXAISeverityMapping pins the severity vocabulary, including the rule that
// an OPEN incident never reads as fully operational.
func TestXAISeverityMapping(t *testing.T) {
	cases := map[string]Severity{
		"unavailable":   SevMajor,
		"outage":        SevMajor,
		"major_outage":  SevMajor,
		"critical":      SevMajor,
		"degraded":      SevMinor,
		"maintenance":   SevMinor,
		"available":     SevMinor, // open but still serving — real, not fatal
		"":              SevMinor, // unknown: conservative, never SevNone
		"something-new": SevMinor,
	}
	for word, want := range cases {
		if got := xaiSeverity(word); got != want {
			t.Errorf("xaiSeverity(%q) = %v, want %v", word, got, want)
		}
	}
}

// TestParseXAIMalformed confirms a non-XML payload errors (so the check reports
// feed-unreachable / unknown) rather than silently reading as operational.
func TestParseXAIMalformed(t *testing.T) {
	if _, err := parseXAI([]byte("this is not xml")); err == nil {
		t.Error("malformed feed should error, not report a clean bill of health")
	}
}
