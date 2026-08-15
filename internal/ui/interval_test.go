package ui

import (
	"testing"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
)

// TestConnectivityIntervalSeparate locks the invariant that connectivity and
// provider cadences are DISTINCT knobs: connectivity defaults to a fast 1s (so
// the internet-health signal stays responsive) and is configured independently of
// the slower provider poll. The connectivity cadence is now user-tunable, but it
// must never silently inherit providerInterval() — they are separate config fields.
func TestConnectivityIntervalSeparate(t *testing.T) {
	if config.DefaultConnIntervalSeconds != 1 {
		t.Fatalf("DefaultConnIntervalSeconds = %d, want 1 (fast default for responsive offline detection)", config.DefaultConnIntervalSeconds)
	}
	if config.DefaultIntervalSeconds <= config.DefaultConnIntervalSeconds {
		t.Fatalf("DefaultIntervalSeconds = %d must be a distinct, slower cadence than the %ds connectivity probe", config.DefaultIntervalSeconds, config.DefaultConnIntervalSeconds)
	}
	// The connectivity floor keeps the probe responsive even if mis-set to 0.
	if connMinIntervalSeconds < 1 {
		t.Fatalf("connMinIntervalSeconds = %d, want >= 1", connMinIntervalSeconds)
	}
}
