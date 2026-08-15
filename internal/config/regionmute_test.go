package config

import (
	"slices"
	"testing"
)

func TestRegionMuteSetClear(t *testing.T) {
	c := Default()
	c.SetRegionMuteUntil("  us-east-1  ", 1000) // trimmed key
	if got, ok := c.RegionMutedUntil["us-east-1"]; !ok || got != 1000 {
		t.Fatalf("SetRegionMuteUntil: want 1000 under trimmed key, got %d ok=%v", got, ok)
	}
	c.SetRegionMuteUntil("", 5) // empty is a no-op
	if _, ok := c.RegionMutedUntil[""]; ok {
		t.Fatal("empty region should not be stored")
	}
	c.ClearRegionMute("us-east-1")
	if _, ok := c.RegionMutedUntil["us-east-1"]; ok {
		t.Fatal("ClearRegionMute did not remove the entry")
	}
}

func TestCloneDetachesMaps(t *testing.T) {
	c := Default()
	c.SetRegionMuteUntil("r", 100)
	c.Enabled["check"] = true
	c.Regions = []string{"us-east-1"}

	clone := c.Clone()
	// Mutating the original's maps/slices must not bleed into the clone — the
	// whole point of Clone is a snapshot safe to marshal off-thread.
	c.RegionMutedUntil["r"] = 999
	c.Enabled["check"] = false
	c.Regions[0] = "changed"

	if clone.RegionMutedUntil["r"] != 100 {
		t.Errorf("clone RegionMutedUntil leaked: got %d, want 100", clone.RegionMutedUntil["r"])
	}
	if clone.Enabled["check"] != true {
		t.Error("clone Enabled leaked")
	}
	if clone.Regions[0] != "us-east-1" {
		t.Errorf("clone Regions leaked: got %q", clone.Regions[0])
	}
}

func TestPruneExpiredRegionMutes(t *testing.T) {
	c := Default()
	c.SetRegionMuteUntil("expired", 100)
	c.SetRegionMuteUntil("future", 10_000)
	c.SetRegionMuteUntil("forever", RegionMuteForever)

	expired := c.PruneExpiredRegionMutes(5_000)

	if !slices.Contains(expired, "expired") || len(expired) != 1 {
		t.Fatalf("expired set = %v, want exactly [expired]", expired)
	}
	if _, ok := c.RegionMutedUntil["expired"]; ok {
		t.Fatal("expired entry should have been deleted")
	}
	if _, ok := c.RegionMutedUntil["future"]; !ok {
		t.Fatal("future entry must be retained")
	}
	if v, ok := c.RegionMutedUntil["forever"]; !ok || v != RegionMuteForever {
		t.Fatal("forever entry must never be pruned (RegionMuteForever must not read as expired)")
	}
}
