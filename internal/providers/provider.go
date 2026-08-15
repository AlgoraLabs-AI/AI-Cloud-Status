// Package providers contains data-driven health-check adapters for AI/cloud
// status pages and the parsing logic that maps each provider's feed onto a
// common Severity. Parsing is decoupled from network I/O so it can be unit
// tested against sample feeds.
package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Severity is the normalized SERVICE-HEALTH level for a provider. It describes
// how degraded a service is *when its feed could be read*; it deliberately does
// NOT carry a "could not check" value. Whether the feed itself was reachable is
// an orthogonal axis modelled by FeedState — conflating the two (treating an
// unreadable feed as if the service were down) is exactly the bug this package
// is designed to avoid.
type Severity int

const (
	// SevNone means operational / no known issues.
	SevNone Severity = iota
	// SevMinor is a minor/partial degradation.
	SevMinor
	// SevMajor is a major outage affecting multiple users/services.
	SevMajor
	// SevCritical is a critical/full outage.
	SevCritical
)

func (s Severity) String() string {
	switch s {
	case SevNone:
		return "none"
	case SevMinor:
		return "minor"
	case SevMajor:
		return "major"
	case SevCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// FeedState is the orthogonal axis to Severity: whether we could read the
// provider's status feed at all. A feed we cannot reach tells us nothing about
// whether the service is actually up or down — it is "unknown", and must never
// be surfaced or alerted as a real outage.
type FeedState int

const (
	// FeedReachable means the feed was fetched and parsed successfully, so the
	// Severity/Incidents on the Result reflect real service health.
	FeedReachable FeedState = iota
	// FeedUnreachable means the feed could not be fetched or parsed; service
	// health is unknown.
	FeedUnreachable
)

// ServiceState is the three-state classification the application surfaces for a
// provider: the service is up, the service is down (degraded or outage), or its
// status feed could not be read so health is unknown. It is the union of the
// FeedState and Severity axes and is what the UI and alerting consume.
type ServiceState int

const (
	// ServiceUp: feed read, operational (no known incidents).
	ServiceUp ServiceState = iota
	// ServiceDown: feed read, the service is degraded or in outage.
	ServiceDown
	// ServiceFeedUnreachable: the feed could not be read — health is unknown.
	// This is NEVER the same as ServiceDown.
	ServiceFeedUnreachable
)

func (s ServiceState) String() string {
	switch s {
	case ServiceUp:
		return "up"
	case ServiceDown:
		return "down"
	case ServiceFeedUnreachable:
		return "feed-unreachable"
	default:
		return "unknown"
	}
}

// Kind selects which adapter parses a provider's feed.
type Kind string

const (
	KindStatuspage  Kind = "statuspage"  // Atlassian Statuspage summary.json
	KindGoogleCloud Kind = "googlecloud" // status.cloud.google.com incidents.json
	KindAWS         Kind = "aws"         // AWS Health public current events
	KindAzure       Kind = "azure"       // Azure status RSS feed
	KindCheckly     Kind = "checkly"     // Checkly status page (e.g. Mistral)
	KindBetterStack Kind = "betterstack" // Better Stack status page (e.g. Hugging Face)
	KindInstatus    Kind = "instatus"    // Instatus status page (e.g. Perplexity)
	KindXAI         Kind = "xai"         // xAI status RSS feed (status.x.ai/feed.xml)
)

// TypeLabel is the short, human-readable "check type" shown in the UI for a
// provider. All providers are queried against a machine-readable status/health
// API (not the marketing site), so the label is uniform.
const TypeLabel = "Status API"

// Category groups providers for display in the selection list and status table.
type Category string

const (
	CategoryCloud Category = "Cloud"
	CategoryAI    Category = "AI"
	CategoryDev   Category = "Dev"
)

// CategoryOrder is the display order of provider categories (the Connectivity
// category is handled separately in the UI, as it is not provider-backed).
var CategoryOrder = []Category{CategoryCloud, CategoryAI, CategoryDev}

// Provider is one data-driven health check.
type Provider struct {
	ID   string
	Name string
	Kind Kind
	URL  string
	// Category groups the provider in the UI (Cloud, AI, Dev).
	Category Category
	// Filter optionally narrows results by product/affected-component name
	// (used to derive Gemini health from the shared Google Cloud feed).
	Filter string
	// Optional providers are skipped gracefully (not surfaced as unreachable)
	// when they do not expose a public feed (e.g. a 404).
	Optional bool
}

// Incident is a single open issue reported by a provider feed. It carries its
// own severity and the set of regions it affects. An incident with no Regions is
// global (it affects every region / the feed exposes no region info), and a
// global incident is always considered relevant regardless of region filtering.
type Incident struct {
	// Summary is the human-readable description of the incident.
	Summary string
	// Severity is this incident's normalized health level.
	Severity Severity
	// Regions are the affected region/location identifiers (e.g. "us-east-1",
	// "West Europe"). Empty means global / region-agnostic.
	Regions []string
	// The fields below are OPTIONAL drill-down detail surfaced in the incident
	// detail panel. They are populated only by feeds that expose them (e.g.
	// Atlassian Statuspage); other adapters leave them zero, and the UI degrades
	// gracefully when they are absent.

	// Components are the affected sub-components/services named by the feed.
	Components []string
	// Started is when the incident began (zero if the feed does not say).
	Started time.Time
	// Updated is the last time the incident was updated (zero if unknown).
	Updated time.Time
	// Note is the latest human-readable update message posted on the incident
	// (empty when the feed exposes none).
	Note string
	// URL links to the incident's own page, when the feed provides one.
	URL string
}

// StaleAfter is how long an incident may stay open with no update before it is
// treated as a zombie: still listed on the feed, but not news.
//
// One constant, one meaning. This horizon used to exist three times — in the AWS
// adapter, in the xAI adapter, and again in the UI's stale/zombie split — with
// divergent rules for WHICH timestamp counts, so a row and its own drill-down
// could disagree about the same incident. Providers do forget to close events:
// AWS has carried region events open for months.
const StaleAfter = 15 * 24 * time.Hour

// IsStale reports whether the incident has gone silent long enough to be a
// zombie, as of now.
//
// Age is measured from the last UPDATE, falling back to the start time when the
// feed exposes no update. An incident with no usable timestamp at all is never
// stale: a missing time is not evidence of age, and guessing here would downplay
// a real, current outage — the one failure this app cannot have.
func (i Incident) IsStale(now time.Time) bool {
	last := i.Updated
	if last.IsZero() {
		last = i.Started
	}
	return !last.IsZero() && now.Sub(last) > StaleAfter
}

// IsGlobal reports whether the incident carries no region scoping.
func (i Incident) IsGlobal() bool { return len(i.Regions) == 0 }

// Label renders the incident for display, appending its affected regions when
// known: "Increased latency (us-east-1, us-west-2)". A global incident renders
// as its bare summary.
func (i Incident) Label() string {
	if i.IsGlobal() || i.Summary == "" {
		return i.Summary
	}
	return fmt.Sprintf("%s (%s)", i.Summary, strings.Join(i.Regions, ", "))
}

// inScope reports whether the incident is relevant to the given regions of
// interest. A global incident is always in scope; otherwise at least one of its
// regions must match. An empty interest set matches everything.
// InScope is the exported form, for callers outside this package that hold
// region lists of their own (the UI's journaled incident history).
func (i Incident) InScope(interest []string) bool { return i.inScope(interest) }

func (i Incident) inScope(interest []string) bool {
	if len(interest) == 0 || i.IsGlobal() {
		return true
	}
	for _, r := range i.Regions {
		if MatchRegion(r, interest) {
			return true
		}
	}
	return false
}

// Result is the normalized outcome of a provider health check. The Feed field is
// the source of truth for whether we could read the feed at all; Severity and
// Incidents are only meaningful when Feed == FeedReachable.
type Result struct {
	// Feed records whether the status feed could be read. The zero value
	// (FeedReachable) keeps result literals that only set Severity behaving as a
	// reachable, parsed result.
	Feed      FeedState
	Severity  Severity
	Summary   string
	Incidents []Incident
}

// Reachable reports whether the feed could be read (and therefore whether
// Severity/Incidents carry real information).
func (r Result) Reachable() bool { return r.Feed == FeedReachable }

// ServiceState collapses the Result onto the three-state model. A feed we could
// not read is ServiceFeedUnreachable (unknown), never ServiceDown.
func (r Result) ServiceState() ServiceState {
	return r.ServiceStateInRegions(nil)
}

// ServiceStateInRegions is ServiceState scoped to the user's regions of
// interest: an unreadable feed stays unknown; otherwise the service is up when
// no in-scope incident degrades it and down when one does.
func (r Result) ServiceStateInRegions(interest []string) ServiceState {
	if r.Feed == FeedUnreachable {
		return ServiceFeedUnreachable
	}
	if r.SeverityInRegions(interest) == SevNone {
		return ServiceUp
	}
	return ServiceDown
}

// Labels renders each incident with its affected regions, for compact display.
func (r Result) Labels() []string {
	out := make([]string, 0, len(r.Incidents))
	for _, inc := range r.Incidents {
		if l := inc.Label(); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// AffectedRegions returns the de-duplicated, sorted union of every region named
// across the result's incidents (global incidents contribute nothing).
func (r Result) AffectedRegions() []string {
	seen := map[string]bool{}
	var out []string
	for _, inc := range r.Incidents {
		for _, reg := range inc.Regions {
			if reg != "" && !seen[reg] {
				seen[reg] = true
				out = append(out, reg)
			}
		}
	}
	sort.Strings(out)
	return out
}

// IncidentsInRegions returns the incidents relevant to the given regions of
// interest (global incidents are always included). An empty interest set
// returns every incident.
func (r Result) IncidentsInRegions(interest []string) []Incident {
	if len(interest) == 0 {
		return r.Incidents
	}
	var out []Incident
	for _, inc := range r.Incidents {
		if inc.inScope(interest) {
			out = append(out, inc)
		}
	}
	return out
}

// SeverityInRegions returns the worst service-health severity relevant to the
// given regions of interest. An empty interest set returns the global Severity
// unchanged (no behavioral regression). Otherwise it is the worst severity among
// the in-scope incidents, falling back to SevNone when none are in scope.
// Feed reachability is a separate axis (see ServiceStateInRegions) and is not
// reflected here.
//
// A feed carrying NO incidents at all is the exception: several adapters
// legitimately report only a page-level aggregate — Statuspage flips its
// indicator to major/critical before an incident is posted, Instatus answers
// HASISSUES with no detail, Better Stack reports aggregate downtime with no
// `included` entries, Google Cloud raises severity for an incident whose
// description is empty. There are no incidents to region-filter, so filtering
// cannot narrow anything; returning SevNone there would silently convert
// "outage with no detail yet" into "operational" for every user who configured
// a region. Region scoping narrows WHICH incidents count, never whether an
// undetailed outage counts at all.
func (r Result) SeverityInRegions(interest []string) Severity {
	if len(interest) == 0 || len(r.Incidents) == 0 {
		return r.Severity
	}
	worst := SevNone
	for _, inc := range r.IncidentsInRegions(interest) {
		if inc.Severity > worst {
			worst = inc.Severity
		}
	}
	return worst
}

// MatchRegion reports whether a region string matches any region of interest.
// Matching is case-insensitive and substring-tolerant in both directions, so a
// user-entered "us-east-1" matches a feed region of "US East (us-east-1)" and
// vice-versa. An empty interest set matches every region.
func MatchRegion(region string, interest []string) bool {
	if len(interest) == 0 {
		return true
	}
	r := strings.ToLower(strings.TrimSpace(region))
	if r == "" {
		return false
	}
	for _, want := range interest {
		w := strings.ToLower(strings.TrimSpace(want))
		if w == "" {
			continue
		}
		if strings.Contains(r, w) || strings.Contains(w, r) {
			return true
		}
	}
	return false
}

// ErrNoFeed indicates an Optional provider does not expose a public feed and
// should be skipped rather than reported as unreachable.
var ErrNoFeed = errors.New("provider exposes no public status feed")

// parse dispatches raw feed bytes to the self-registered adapter for p.Kind.
// Adapters register themselves via RegisterParser in their own file's init, so
// adding a backend requires no edit here.
func parse(p Provider, data []byte) (Result, error) {
	fn, ok := parserFor(p.Kind)
	if !ok {
		return Result{Feed: FeedUnreachable}, fmt.Errorf("unknown provider kind %q", p.Kind)
	}
	return fn(p, data)
}

// ParseFeed re-parses raw feed bytes with the adapter currently registered for
// p.Kind — the exact same dispatch a live Checker uses. Exported so an
// external diagnostic tool (e.g. cmd/alertaudit) can re-derive a provider's
// true severity from an archived feed-samples/ payload using the REAL parser,
// rather than trusting FeedCapture's own filename label (see feedcapture.go —
// that label reads the page-level indicator, not each incident's own impact,
// and can disagree with it).
func ParseFeed(p Provider, data []byte) (Result, error) {
	return parse(p, data)
}

// Checker performs a live provider health check.
type Checker struct {
	Client *http.Client
	// Capture, when set, archives raw feed payloads for noteworthy checks
	// (active incidents or unparseable feeds) as parser-development evidence.
	Capture *FeedCapture
}

// NewChecker returns a Checker with a sensible default timeout.
func NewChecker() *Checker {
	return &Checker{Client: &http.Client{Timeout: 15 * time.Second}}
}

// Check fetches p's feed and parses it. For Optional providers a 404 yields
// ErrNoFeed; any other transport/HTTP/parse error yields a Result with
// Feed == FeedUnreachable (service health unknown — NOT an outage) so the UI can
// show a distinct "feed unavailable" state. The HTTP client transparently
// decompresses gzip responses (net/http adds Accept-Encoding when we don't).
func (c *Checker) Check(ctx context.Context, p Provider) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return Result{Feed: FeedUnreachable}, err
	}
	req.Header.Set("User-Agent", "AI-Cloud-Status/1.0 (+https://github.com/AlgoraLabs-AI/AI-Cloud-Status)")
	req.Header.Set("Accept", "application/json, application/rss+xml, text/xml;q=0.9, */*;q=0.8")

	resp, err := c.Client.Do(req)
	if err != nil {
		return Result{Feed: FeedUnreachable}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if p.Optional && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden) {
			return Result{}, ErrNoFeed
		}
		return Result{Feed: FeedUnreachable}, &StatusError{Provider: p.Name, Code: resp.StatusCode}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Result{Feed: FeedUnreachable}, err
	}
	res, err := parse(p, data)
	c.Capture.Capture(p, data, res, err)
	if err != nil {
		// We received bytes but could not understand them: the feed is, for our
		// purposes, unreadable — health is unknown, not an outage. Wrapping in
		// ErrUnparseable is what lets the UI say THAT, rather than blaming the
		// connection for a reply that arrived intact.
		return Result{Feed: FeedUnreachable}, fmt.Errorf("%w: %v", ErrUnparseable, err)
	}
	return res, nil
}
