package ui

import (
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// The Show button carries only the alert's check id, so every alert the app can
// raise must resolve to the panel that explains it — otherwise the click still
// drops the user on the table, which is the behaviour this replaced.

func testController() *Controller {
	return &Controller{
		providers: []providers.Provider{
			{ID: "anthropic", Name: "Anthropic"},
			{ID: "mistral", Name: "Mistral"},
		},
		provStates: map[string]provState{
			"anthropic": {checked: true, when: time.Now(), attempts: 1},
		},
		urlStates: map[string]urlState{
			"url-1365c6b0": {checked: true, up: false},
		},
		cfg: config.Config{CustomURLChecks: []config.URLCheck{
			{ID: "url-1365c6b0", Name: "portaldev", URL: "https://example.test/healthcheck"},
		}},
	}
}

func TestResolveAlertTargetByKind(t *testing.T) {
	c := testController()

	cases := []struct {
		name string
		id   string
		want alertTargetKind
	}{
		{"provider with a polled state", "anthropic", alertTargetProvider},
		{"provider never polled yet", "mistral", alertTargetProvider},
		{"custom URL check", "url-1365c6b0", alertTargetURL},
		{"app-wide connectivity alert carries no id", "", alertTargetNone},
		{"id of a check that no longer exists", "deleted-check", alertTargetNone},
	}
	for _, tc := range cases {
		if got := c.resolveAlertTarget(tc.id).kind; got != tc.want {
			t.Errorf("%s: resolveAlertTarget(%q).kind = %v, want %v", tc.name, tc.id, got, tc.want)
		}
	}
}

// The panel's ok argument is "has this check ever been polled". Getting it
// wrong renders a live provider as "not checked yet" — the drill-down would open
// blank on exactly the incident the card was raised for.
func TestResolveAlertTargetCarriesPolledState(t *testing.T) {
	c := testController()

	polled := c.resolveAlertTarget("anthropic")
	if !polled.provSeen {
		t.Error("anthropic has a provState but provSeen is false — the panel would render it as never checked")
	}
	if polled.prov.Name != "Anthropic" {
		t.Errorf("resolved the wrong provider: %q", polled.prov.Name)
	}

	unpolled := c.resolveAlertTarget("mistral")
	if unpolled.provSeen {
		t.Error("mistral has no provState but provSeen is true")
	}

	u := c.resolveAlertTarget("url-1365c6b0")
	if !u.urlSeen || u.url.Name != "portaldev" {
		t.Errorf("URL check resolved wrong: seen=%v name=%q", u.urlSeen, u.url.Name)
	}
}

// Connectivity alerts name a ping target, whose state lives in the engine rather
// than on the Controller — a separate lookup, and the one most likely to be
// forgotten.
func TestResolveAlertTargetFindsConnectivityTarget(t *testing.T) {
	c := testController()
	c.engine = monitor.NewEngine(
		[]monitor.Target{{ID: "ping-cloudflare", Name: "Cloudflare DNS", Host: "1.1.1.1"}},
		10, time.Second, monitor.NotifierFunc(func(_, _ string) {}),
	)

	got := c.resolveAlertTarget("ping-cloudflare")
	if got.kind != alertTargetConn {
		t.Fatalf("kind = %v, want alertTargetConn", got.kind)
	}
	if got.conn.ID != "ping-cloudflare" {
		t.Errorf("conn.ID = %q, want ping-cloudflare", got.conn.ID)
	}
}

// A nil engine is the pre-Run state; resolving must not panic there.
func TestResolveAlertTargetNilEngine(t *testing.T) {
	c := &Controller{}
	if got := c.resolveAlertTarget("ping-google").kind; got != alertTargetNone {
		t.Errorf("kind = %v, want alertTargetNone", got)
	}
}
