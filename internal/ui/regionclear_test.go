package ui

import (
	"testing"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
)

// The invariant, stated once so it cannot drift again:
//
//	clearing must remove every entry that could make regionMutedLocked(region)
//	return true.
//
// Matching is deliberately tolerant — providers.MatchRegion is case-insensitive
// and substring-tolerant in BOTH directions, so a user-typed "us-east-1" covers
// a feed's "US East (us-east-1)". Clearing was an exact map delete. Wherever two
// providers name the same region differently, that mismatch made a mute
// PERMANENT: the chip renders dimmed, its popup offers Reactivate, and Reactivate
// deletes a key that does not exist. The dialog closes, the chip stays dimmed,
// and every major incident scoped to that region stays suppressed with no way to
// undo it from that chip.

// mutePair is a (stored key, chip text) pair that regionMuted() treats as the
// same region.
type mutePair struct{ stored, chip string }

var tolerantPairs = []mutePair{
	{"us-east-1", "US East (us-east-1)"}, // stored ⊂ chip — the reported case
	{"US East (us-east-1)", "us-east-1"}, // chip ⊂ stored
	{"US-EAST-1", "us-east-1"},           // case only
	{"west europe", "West Europe"},       // case, with a space
	{"me-central-1", "ME Central (me-central-1)"},
}

// TestReactivateClearsEveryMatchingPersistedMute is the regression: for every
// pair the matcher considers equal, reactivating via the chip text must actually
// un-mute it.
func TestReactivateClearsEveryMatchingPersistedMute(t *testing.T) {
	for _, p := range tolerantPairs {
		c := &Controller{cfg: config.Config{
			RegionMutedUntil: map[string]int64{config.NormalizeRegion(p.stored): config.RegionMuteForever},
		}}

		if !c.regionMuted(p.chip) {
			t.Errorf("stored %q: chip %q does not read as muted — the pair is not tolerant after all", p.stored, p.chip)
			continue
		}
		c.mu.Lock()
		c.clearRegionMutesLocked(p.chip)
		c.mu.Unlock()
		if c.regionMuted(p.chip) {
			t.Errorf("stored %q: chip %q is STILL muted after reactivating it — the mute is unreachable", p.stored, p.chip)
		}
		if len(c.cfg.RegionMutedUntil) != 0 {
			t.Errorf("stored %q: leftover entries %v", p.stored, c.cfg.RegionMutedUntil)
		}
	}
}

// TestReactivateClearsEveryMatchingSessionMute is the same invariant over the
// until-restart map, which had the identical exact-delete bug.
func TestReactivateClearsEveryMatchingSessionMute(t *testing.T) {
	for _, p := range tolerantPairs {
		c := &Controller{
			cfg:                    config.Config{},
			sessionDisabledRegions: map[string]bool{config.NormalizeRegion(p.stored): true},
		}
		if !c.regionMuted(p.chip) {
			t.Errorf("stored %q: chip %q does not read as muted", p.stored, p.chip)
			continue
		}
		c.mu.Lock()
		c.clearRegionMutesLocked(p.chip)
		c.mu.Unlock()
		if c.regionMuted(p.chip) {
			t.Errorf("stored %q: session mute for chip %q survived reactivation", p.stored, p.chip)
		}
	}
}

// TestReactivateLeavesUnrelatedRegionsAlone is the counterweight: tolerant
// clearing must not become a blanket clear.
func TestReactivateLeavesUnrelatedRegionsAlone(t *testing.T) {
	c := &Controller{cfg: config.Config{RegionMutedUntil: map[string]int64{
		"us-east-1":  config.RegionMuteForever,
		"eu-west-1":  config.RegionMuteForever,
		"ap-south-1": config.RegionMuteForever,
	}}}

	c.mu.Lock()
	c.clearRegionMutesLocked("US East (us-east-1)")
	c.mu.Unlock()

	if c.regionMuted("us-east-1") {
		t.Error("the targeted region is still muted")
	}
	for _, other := range []string{"eu-west-1", "ap-south-1"} {
		if !c.regionMuted(other) {
			t.Errorf("reactivating us-east-1 also un-muted %q", other)
		}
	}
}

// TestRegionMuteStatusFindsATolerantlyMatchedEntry pins the popup text. An exact
// lookup fell through to the bare "Deactivated." for any chip that merely
// matched a stored key — dropping the "until you reactivate it" wording exactly
// when the user needs to know which kind of mute they are looking at.
func TestRegionMuteStatusFindsATolerantlyMatchedEntry(t *testing.T) {
	c := &Controller{cfg: config.Config{
		RegionMutedUntil: map[string]int64{"us-east-1": config.RegionMuteForever},
	}}
	got := c.regionMuteStatus("US East (us-east-1)")
	if got == "Deactivated." {
		t.Error("status fell back to the generic text for a tolerantly-matched mute")
	}
	if got != "Deactivated until you reactivate it." {
		t.Errorf("status = %q, want the forever wording", got)
	}
}

// TestSetRegionMuteSupersedesATolerantlyMatchedSessionEntry pins the third site.
// Without a tolerant supersede, a persisted mute and an unreachable session mute
// coexist for the same region, and clearing the persisted one leaves the chip
// silent with nothing in the UI to explain why.
func TestSetRegionMuteSupersedesATolerantlyMatchedSessionEntry(t *testing.T) {
	c := &Controller{
		cfg:                    config.Config{},
		sessionDisabledRegions: map[string]bool{"us-east-1": true},
	}
	c.mu.Lock()
	c.clearRegionMutesLocked("US East (us-east-1)")
	c.cfg.SetRegionMuteUntil("US East (us-east-1)", config.RegionMuteForever)
	c.mu.Unlock()

	if len(c.sessionDisabledRegions) != 0 {
		t.Errorf("session entry survived being superseded: %v", c.sessionDisabledRegions)
	}
	if !c.regionMuted("us-east-1") {
		t.Error("the region should still be muted, now by the persisted entry")
	}
}
