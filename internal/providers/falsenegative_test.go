package providers

import (
	"testing"
	"time"
)

// This file collects regressions for one bug class: the feed says something is
// wrong and ACS reports it as healthy. A false positive here is noise; a false
// negative is the app failing at the only job it has, so each case below pins a
// path where an outage used to be silently converted into "operational".

// TestSeverityInRegionsKeepsAggregateOnlyOutage pins the aggregate-only escape
// hatch. Several adapters legitimately report a page-level severity with no
// incident list at all — Statuspage flips its indicator before an incident is
// posted, Instatus answers HASISSUES with no detail, Google Cloud raises
// severity for an incident with an empty description. Region filtering narrows
// WHICH incidents count; with no incidents to filter it must not be able to
// turn an outage into silence.
func TestSeverityInRegionsKeepsAggregateOnlyOutage(t *testing.T) {
	res := Result{Severity: SevCritical} // no Incidents at all

	if got := res.SeverityInRegions(nil); got != SevCritical {
		t.Errorf("no interest: got %v, want SevCritical", got)
	}
	if got := res.SeverityInRegions([]string{"us-east-1"}); got != SevCritical {
		t.Errorf("with interest: got %v, want SevCritical — a configured region must not silence an undetailed outage", got)
	}
}

// TestSeverityInRegionsStillFiltersWhenIncidentsExist guards the other side: the
// escape hatch must not disable region scoping when there IS something to scope.
func TestSeverityInRegionsStillFiltersWhenIncidentsExist(t *testing.T) {
	res := Result{
		Severity:  SevCritical,
		Incidents: []Incident{{Summary: "eu only", Severity: SevCritical, Regions: []string{"eu-west-1"}}},
	}
	if got := res.SeverityInRegions([]string{"us-east-1"}); got != SevNone {
		t.Errorf("got %v, want SevNone — an incident confined to an uninteresting region must not count", got)
	}
	if got := res.SeverityInRegions([]string{"eu-west-1"}); got != SevCritical {
		t.Errorf("got %v, want SevCritical", got)
	}
}

// TestStatuspageAggregateOnlyCriticalSurvivesRegionScoping is the end-to-end
// version: the parser is already pinned to keep SevCritical for this shape
// (providers_test.go says it "must keep alerting"), and it has to survive the
// region-scoped read the UI actually performs.
func TestStatuspageAggregateOnlyCriticalSurvivesRegionScoping(t *testing.T) {
	res, err := parseStatuspage([]byte(`{"status":{"indicator":"critical","description":"Major outage"},"incidents":[]}`))
	if err != nil {
		t.Fatalf("parseStatuspage: %v", err)
	}
	if res.Severity != SevCritical {
		t.Fatalf("parser dropped the aggregate: %v", res.Severity)
	}
	if got := res.SeverityInRegions([]string{"us-east-1"}); got != SevCritical {
		t.Errorf("got %v, want SevCritical", got)
	}
}

// TestXMLAdaptersRejectNonRSSDocuments pins the root-element constraint. Without
// it, encoding/xml unmarshals ANY well-formed document into a struct that has no
// XMLName field, yielding zero items and a nil error — which Checker.Check reads
// as a healthy feed. That is the shape that let ACS report Azure green through a
// real outage when the endpoint answered 200 with something that was not the
// feed.
func TestXMLAdaptersRejectNonRSSDocuments(t *testing.T) {
	notFeeds := map[string]string{
		"s3 style error":  `<?xml version="1.0"?><Error><Code>AccessDenied</Code></Error>`,
		"html error page": `<html><body><p>Service Unavailable</p></body></html>`,
		"empty root":      `<?xml version="1.0"?><foo/>`,
		"atom":            `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>x</title></feed>`,
	}
	for name, doc := range notFeeds {
		if _, err := parseAzure([]byte(doc)); err == nil {
			t.Errorf("parseAzure(%s): got nil error — a non-feed document must not read as an empty healthy feed", name)
		}
		if _, err := parseXAI([]byte(doc)); err == nil {
			t.Errorf("parseXAI(%s): got nil error — a non-feed document must not read as an empty healthy feed", name)
		}
	}
}

// TestXMLAdaptersStillAcceptRealFeeds is the counterweight to the test above:
// the constraint must not reject the documents these providers actually serve.
// It uses the captured live payloads rather than a synthetic empty channel,
// because an empty channel is itself a rejected shape (see the next test).
func TestXMLAdaptersStillAcceptRealFeeds(t *testing.T) {
	if _, err := parseAzure(readFixture(t, "azure_live_westus.xml")); err != nil {
		t.Errorf("parseAzure rejected the captured live feed: %v", err)
	}
	if _, err := parseXAI(readFixture(t, "xai_active.xml")); err != nil {
		t.Errorf("parseXAI rejected the captured live feed: %v", err)
	}
}

// TestAzureEmptyFeedIsHealthy pins the REAL all-clear payload, captured verbatim
// from the live feed: 593 bytes, a channel with a title and zero items.
//
// This is the regression for a fix that broke the thing it was protecting. An
// earlier guard rejected any Azure feed with no items, on the assumption — mine,
// and an independent reviewer's — that it was a rolling history like xAI's. It
// is not: Azure lists only OPEN incidents, so zero items is what a healthy
// Azure looks like, and the guard turned every healthy poll into "Status feed
// unavailable". The assumption was never tested against a healthy capture
// because FeedCapture only archives NON-operational readings, so the whole
// replay corpus structurally could not contain this case.
func TestAzureEmptyFeedIsHealthy(t *testing.T) {
	res, err := parseAzure(readFixture(t, "azure_healthy_empty.xml"))
	if err != nil {
		t.Fatalf("the live all-clear feed must parse: %v", err)
	}
	if res.Severity != SevNone {
		t.Errorf("severity = %v, want SevNone", res.Severity)
	}
	if len(res.Incidents) != 0 {
		t.Errorf("got %d incidents from an all-clear feed", len(res.Incidents))
	}
	if got := res.SeverityInRegions([]string{"West US"}); got != SevNone {
		t.Errorf("region-scoped read of an all-clear feed = %v, want SevNone", got)
	}
}

// TestXAIEmptyFeedIsNotAllClear is the opposite case, and the two must not be
// "unified". xAI's feed is a rolling incident history — 105 items live when last
// measured, nearly all resolved — so it never empties. An empty channel there
// means a truncated body or an error document, and reading it as healthy is the
// silent-green failure this app cannot have.
func TestXAIEmptyFeedIsNotAllClear(t *testing.T) {
	empty := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>xAI Status</title></channel></rss>`
	if _, err := parseXAI([]byte(empty)); err == nil {
		t.Error("parseXAI accepted a zero-item feed as all-clear")
	}
}

// TestAzureRejectsADocumentThatIsNotTheFeed keeps the hole the root constraint
// was for actually closed, now that item count can no longer be the signal: a
// response with no channel title is not this feed.
func TestAzureRejectsADocumentThatIsNotTheFeed(t *testing.T) {
	for name, doc := range map[string]string{
		"bare rss":      `<?xml version="1.0"?><rss version="2.0"></rss>`,
		"empty channel": `<?xml version="1.0"?><rss version="2.0"><channel></channel></rss>`,
		"blank title":   `<?xml version="1.0"?><rss version="2.0"><channel><title>  </title></channel></rss>`,
	} {
		if _, err := parseAzure([]byte(doc)); err == nil {
			t.Errorf("parseAzure(%s) accepted a document that is not the feed", name)
		}
	}
}

// TestJSONAdaptersRejectResponsesMissingTheirAnchor closes the same hole on the
// JSON side. `{"message":"Forbidden"}` used to unmarshal into a zero struct
// whose empty indicator maps to SevNone — a 403 rendered as "operational". The
// header of instatus.go records that this class already bit once: Perplexity
// migrated off the Statuspage schema and "made the Statuspage parser silently
// read always operational".
func TestJSONAdaptersRejectResponsesMissingTheirAnchor(t *testing.T) {
	notFeeds := []string{
		`{"message":"Forbidden"}`,
		`{}`,
		`{"error":{"code":503,"reason":"upstream unavailable"}}`,
	}
	for _, doc := range notFeeds {
		if _, err := parseStatuspage([]byte(doc)); err == nil {
			t.Errorf("parseStatuspage(%s) accepted a non-feed response as healthy", doc)
		}
		if _, err := parseInstatus([]byte(doc)); err == nil {
			t.Errorf("parseInstatus(%s) accepted a non-feed response as healthy", doc)
		}
	}
}

// TestAzureItemResolvedNeedsAWholeWord pins the resolution test. A dropped item
// contributes neither severity nor an incident, so an over-eager match is a pure
// false negative — the worst outcome available.
func TestAzureItemResolvedNeedsAWholeWord(t *testing.T) {
	stillOpen := []string{
		"Unresolved connectivity issues - East US",
		"Storage - West Europe - Partially resolved, impact continues in North Europe",
		"Networking - not yet resolved",
		"Virtual Machines - East US - Service degradation",
		"Multiple services - resolved for some regions, ongoing in others",
	}
	for _, title := range stillOpen {
		if azureItemResolved(title) {
			t.Errorf("azureItemResolved(%q) = true, want false — the item is still open", title)
		}
	}

	closed := []string{
		"Virtual Machines - East US - Issue Resolved",
		"[RESOLVED] Storage - West Europe",
		"Networking - resolved",
	}
	for _, title := range closed {
		if !azureItemResolved(title) {
			t.Errorf("azureItemResolved(%q) = false, want true", title)
		}
	}
}

// TestAzureMixedScopeTitlesStayOpen pins the cases an independent reviewer
// demonstrated the first word-boundary attempt still got wrong. Every one of
// these announces a PARTIAL resolution while impact continues elsewhere, and
// each was silently dropped from the feed — the provider then reported
// operational mid-outage.
func TestAzureMixedScopeTitlesStayOpen(t *testing.T) {
	for _, title := range []string{
		"Resolved in North Europe. Impact continues in West Europe",
		"Resolved for Storage; mitigation underway for Compute",
		"Resolved in West US — investigating continued impact in East US",
		"Resolved for most customers — monitoring remaining affected tenants",
		"Storage - Resolved in East US - degraded performance persists in West US",
	} {
		if azureItemResolved(title) {
			t.Errorf("azureItemResolved(%q) = true — impact is still live in another scope", title)
		}
	}
}

// TestAzureResolutionIsDecidedByTheFinalClause states the rule directly: Azure
// appends the current status last, so an early "resolved" is history, not a
// verdict. The bracketed prefix form is the documented exception.
func TestAzureResolutionIsDecidedByTheFinalClause(t *testing.T) {
	cases := map[string]bool{
		"Storage - West Europe - Resolved":                 true,
		"[RESOLVED] Storage - West Europe":                 true,
		"(Resolved) Networking - East US":                  true,
		"Resolved in West US - investigating in East US":   false,
		"Networking - East US - investigating":             false,
		"Virtual Machines - East US - Service degradation": false,
	}
	for title, want := range cases {
		if got := azureItemResolved(title); got != want {
			t.Errorf("azureItemResolved(%q) = %v, want %v", title, got, want)
		}
	}
}

// TestInstatusOpenIncidentIsNeverSevNone pins the severity floor. It matters
// because effectiveSeverity prefers per-incident severities whenever the list is
// non-empty: a SevNone incident does not merely contribute nothing, it discards
// the HASISSUES aggregate bump and paints the provider operational.
func TestInstatusOpenIncidentIsNeverSevNone(t *testing.T) {
	feed := `{"page":{"status":"HASISSUES"},
	  "activeIncidents":[{"name":"Search degraded","status":"INVESTIGATING"}]}`

	res, err := parseInstatus([]byte(feed))
	if err != nil {
		t.Fatalf("parseInstatus: %v", err)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("got %d incidents, want 1", len(res.Incidents))
	}
	if res.Incidents[0].Severity == SevNone {
		t.Error("an incident Instatus still lists as active must not be SevNone")
	}
	if got := res.SeverityInRegions([]string{"us-east-1"}); got == SevNone {
		t.Error("region-scoped read collapsed an open HASISSUES incident to operational")
	}
}

// TestInstatusMaintenanceIncidentStaysNone is the false-positive counterweight
// to the floor above: planned maintenance is announced, not an outage, so an
// incident that explicitly declares UNDERMAINTENANCE must stay SevNone rather
// than being floored up to Degraded.
func TestInstatusMaintenanceIncidentStaysNone(t *testing.T) {
	feed := `{"page":{"status":"UNDERMAINTENANCE"},
	  "activeIncidents":[{"name":"Scheduled database upgrade","status":"INPROGRESS","impact":"UNDERMAINTENANCE"}]}`

	res, err := parseInstatus([]byte(feed))
	if err != nil {
		t.Fatalf("parseInstatus: %v", err)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("got %d incidents, want 1", len(res.Incidents))
	}
	if res.Incidents[0].Severity != SevNone {
		t.Errorf("severity = %v, want SevNone — declared maintenance is not a degradation", res.Incidents[0].Severity)
	}
}

// TestXAICategoryFallbackTakesWorstSeverity pins the unordered-category rule.
// xai.go's own doc says the two <category> values "arrive in no declared order
// and cannot be told apart positionally"; every status word (investigating,
// identified, monitoring) maps to SevMinor while the severity words reach
// SevMajor, so picking the first one under-reported a full outage as Degraded
// whenever the status category happened to be emitted first. SevMajor is the
// alert threshold, so that difference is the difference between a notification
// and silence.
func TestXAICategoryFallbackTakesWorstSeverity(t *testing.T) {
	for _, order := range [][]string{
		{"unavailable", "investigating"},
		{"investigating", "unavailable"}, // the order the old code got wrong
	} {
		got := xaiSeverityOf("no labelled severity here", order)
		if got != SevMajor {
			t.Errorf("categories %v: got %v, want SevMajor", order, got)
		}
	}
}

// TestXAIActiveFixtureStillMajor guards against the worst-of rule accidentally
// changing the real fixture's verdict.
//
// Parsed at xaiNow, not the wall clock: the fixture's newest item is dated
// 2026-07-31, so StaleAfter (15 days) would demote it to minor from 2026-08-15
// onward and fail this test every run thereafter — a red suite caused by the
// calendar rather than by the worst-of rule it exists to pin.
func TestXAIActiveFixtureStillMajor(t *testing.T) {
	res, err := parseXAIAt(readFixture(t, "xai_active.xml"), xaiNow)
	if err != nil {
		t.Fatalf("parseXAI: %v", err)
	}
	if res.Severity < SevMajor {
		t.Errorf("severity = %v, want at least SevMajor", res.Severity)
	}
	if len(res.Incidents) == 0 {
		t.Error("no incidents parsed from the active fixture")
	}
}

// TestNoAdapterSilentlyAcceptsGarbage is a cross-adapter sweep: a payload that
// is not this provider's feed must produce an error (so the row reads
// "unreachable / unknown"), never a clean SevNone (which reads "operational").
func TestNoAdapterSilentlyAcceptsGarbage(t *testing.T) {
	garbage := []string{
		`<?xml version="1.0"?><Error><Code>AccessDenied</Code></Error>`,
		`<html><body>nope</body></html>`,
	}
	parsers := map[string]func([]byte) (Result, error){
		"azure": parseAzure,
		"xai":   parseXAI,
	}
	for name, parse := range parsers {
		for _, g := range garbage {
			res, err := parse([]byte(g))
			if err == nil && res.Severity == SevNone && len(res.Incidents) == 0 {
				t.Errorf("%s: %q parsed as a clean healthy feed", name, truncate(g, 40))
			}
		}
	}
}

// TestAzureRegionTagsIgnoreDescriptionProse pins the asymmetry that makes
// over-tagging dangerous: a region-LESS incident is unconditionally in scope
// while a region-TAGGED one is in scope only on a match, so a region merely
// mentioned in the body used to hide a global incident from everyone who
// configured a different region.
func TestAzureRegionTagsIgnoreDescriptionProse(t *testing.T) {
	feed := `<?xml version="1.0"?><rss version="2.0"><channel><title>Azure Status</title><item>
	  <title>Azure Portal - Users may be unable to sign in</title>
	  <description>We are investigating. This is unrelated to the earlier networking event in West US.</description>
	  <pubDate>Sat, 01 Aug 2026 12:00:00 Z</pubDate>
	</item></channel></rss>`

	// Fixed clock, one hour into the item's own window: on the wall clock the
	// stale demotion would take this below SevMajor from 2026-08-16 and break the
	// global-visibility assertion below, which is about REGION TAGGING and has
	// nothing to say about incident age.
	now := time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)
	res, err := parseAzureAt([]byte(feed), now)
	if err != nil {
		t.Fatalf("parseAzure: %v", err)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("got %d incidents, want 1", len(res.Incidents))
	}
	inc := res.Incidents[0]
	if !inc.IsGlobal() {
		t.Errorf("regions = %v, want none — the only region name is prose in the body", inc.Regions)
	}
	if got := res.SeverityInRegions([]string{"North Europe"}); got < SevMajor {
		t.Errorf("severity for a North Europe user = %v, want >= SevMajor: a global Portal outage must stay visible", got)
	}
}

// TestAzureRegionStillReadFromTitleAndCategory is the counterweight: dropping
// the description must not cost the region tags Azure states structurally.
func TestAzureRegionStillReadFromTitleAndCategory(t *testing.T) {
	feed := `<?xml version="1.0"?><rss version="2.0"><channel><title>Azure Status</title>
	  <item><title>Virtual Machines - West Europe - Service degradation</title>
	    <description>Prose with no place name.</description></item>
	  <item><title>Storage - Service degradation</title>
	    <category>North Europe</category>
	    <description>Prose with no place name.</description></item>
	</channel></rss>`

	res, err := parseAzure([]byte(feed))
	if err != nil {
		t.Fatalf("parseAzure: %v", err)
	}
	if len(res.Incidents) != 2 {
		t.Fatalf("got %d incidents, want 2", len(res.Incidents))
	}
	for i, want := range []string{"West Europe", "North Europe"} {
		if len(res.Incidents[i].Regions) == 0 || res.Incidents[i].Regions[0] != want {
			t.Errorf("incident %d regions = %v, want [%s]", i, res.Incidents[i].Regions, want)
		}
	}
}

// TestAWSNeverTagsWithAHumanRegionName pins the removal of the RegionName
// fallback. "Global" and "N. Virginia" match no region code a user would type,
// so tagging with them converted an always-visible event into an invisible one.
func TestAWSNeverTagsWithAHumanRegionName(t *testing.T) {
	globalEvent := awsEvent{
		Service:    "cloudfront",
		RegionName: "Global",
		ARN:        "arn:aws:health:global::event/CLOUDFRONT/x",
	}
	if got := awsRegions(globalEvent); got != nil {
		t.Errorf("awsRegions = %v, want nil (global) — a display name filters the event out instead of scoping it", got)
	}

	named := awsEvent{Service: "ec2", RegionName: "N. Virginia"}
	if got := awsRegions(named); got != nil {
		t.Errorf("awsRegions = %v, want nil — %q matches no user-entered region code", got, "N. Virginia")
	}

	// A real region code must still win.
	coded := awsEvent{Region: "us-east-1", RegionName: "N. Virginia"}
	if got := awsRegions(coded); len(got) != 1 || got[0] != "us-east-1" {
		t.Errorf("awsRegions = %v, want [us-east-1]", got)
	}
}

// TestGlobalIncidentSurvivesAnyRegionInterest states the invariant the two tests
// above depend on, so a future change to inScope cannot quietly break them.
func TestGlobalIncidentSurvivesAnyRegionInterest(t *testing.T) {
	res := Result{
		Severity:  SevMajor,
		Incidents: []Incident{{Summary: "global outage", Severity: SevMajor}},
	}
	for _, interest := range [][]string{nil, {"us-east-1"}, {"eu-west-1", "ap-south-1"}} {
		if got := res.SeverityInRegions(interest); got != SevMajor {
			t.Errorf("interest %v: got %v, want SevMajor", interest, got)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
