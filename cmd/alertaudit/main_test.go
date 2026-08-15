package main

import (
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/audit"
)

func TestHumanAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Minute, "just now"}, // clock skew / future timestamp
		{0, "just now"},
		{30 * time.Second, "0m ago"},
		{5 * time.Minute, "5m ago"},
		{59 * time.Minute, "59m ago"},
		{2 * time.Hour, "2h ago"},
		{23 * time.Hour, "23h ago"},
		{25 * time.Hour, "1d ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, c := range cases {
		if got := humanAge(c.d); got != c.want {
			t.Errorf("humanAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestHasIssues(t *testing.T) {
	clean := audit.Report{Providers: []audit.ProviderReport{
		{ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 5},
	}}
	if hasIssues(clean) {
		t.Error("hasIssues(clean) = true, want false")
	}

	// Zero captures must NOT fail the run. Captures are only written when a feed
	// reads non-operational, so "no captures" is the intersection of "healthy all
	// window", "feed URL broken" and "never polled" — three states the archive
	// cannot distinguish. Failing on it made a clean machine exit 1 while the
	// same report printed "no uncovered major incidents found", which trains the
	// operator to ignore exit 1 entirely. It is still printed as advisory.
	neverCaptured := audit.Report{Providers: []audit.ProviderReport{
		{ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 0},
	}}
	if hasIssues(neverCaptured) {
		t.Error("hasIssues(never-captured provider) = true; zero captures cannot tell healthy from broken and must not fail the run")
	}
	if !neverCaptured.Providers[0].NeverCaptured() {
		t.Error("NeverCaptured() should still report the condition for the printed advisory")
	}

	allUnparseable := audit.Report{Providers: []audit.ProviderReport{
		{ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 3, ParseErrors: 3},
	}}
	if !hasIssues(allUnparseable) {
		t.Error("hasIssues(all captures unparseable) = false, want true")
	}

	missingRecovery := audit.Report{Providers: []audit.ProviderReport{
		{ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 2,
			MissingRecoveries: []audit.MissingRecovery{{Start: time.Now(), ClearedAt: time.Now()}}},
	}}
	if !hasIssues(missingRecovery) {
		t.Error("hasIssues(missing recovery) = false, want true")
	}
}
