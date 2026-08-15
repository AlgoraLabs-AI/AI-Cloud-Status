package config

import (
	"slices"
	"testing"
)

// The field failure: a "Silence 1h" set once left its MutedUntil entry in
// config.json 18 days after it expired, because the only collector ran inside
// silenceCheck — i.e. only when the user silenced ANOTHER check. It suppressed
// nothing (every read compares against now), but the file misreported the app's
// own state to anyone reading it, an audit included.

func TestPruneExpiredSilences(t *testing.T) {
	c := Default()
	c.MutedUntil = map[string]int64{
		"mistral":   100,    // long expired
		"anthropic": 5_000,  // expires exactly at now — inclusive, must go
		"openai":    10_000, // still running
	}

	expired := c.PruneExpiredSilences(5_000)

	slices.Sort(expired)
	if !slices.Equal(expired, []string{"anthropic", "mistral"}) {
		t.Fatalf("expired set = %v, want [anthropic mistral]", expired)
	}
	if _, ok := c.MutedUntil["mistral"]; ok {
		t.Error("expired silence should have been deleted")
	}
	if _, ok := c.MutedUntil["anthropic"]; ok {
		t.Error("a silence expiring exactly at now must be pruned, not retained for one more tick")
	}
	if v, ok := c.MutedUntil["openai"]; !ok || v != 10_000 {
		t.Error("a live silence must be retained — pruning it would un-silence a check the user silenced")
	}
}

// Nothing to prune must report nothing, so the caller can skip the config write
// rather than re-saving an unchanged file on every poll tick.
func TestPruneExpiredSilencesReportsNothingWhenClean(t *testing.T) {
	c := Default()
	c.MutedUntil = map[string]int64{"openai": 10_000}

	if got := c.PruneExpiredSilences(5_000); len(got) != 0 {
		t.Errorf("expired = %v, want empty", got)
	}

	var nilMap Config
	if got := nilMap.PruneExpiredSilences(5_000); len(got) != 0 {
		t.Errorf("nil MutedUntil: expired = %v, want empty", got)
	}
}
