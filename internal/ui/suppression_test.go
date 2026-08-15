package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

func onlineController() *Controller {
	c := &Controller{cfg: config.Config{Notifications: true}, offline: monitor.NewOfflineDetector()}
	return c
}

func offlineController() *Controller {
	c := onlineController()
	// Probes down and every checked feed unreachable — the only combination that
	// still means offline now that positive feed evidence outranks the probes.
	c.offline.Update(true, 3, 3)
	return c
}

// TestReachableFeedIsNotGatedByOffline is A10. The provider alert path only runs
// for a feed that was fetched AND parsed this round, which is first-hand proof
// the machine is online — it outranks an inference drawn from ICMP and TCP:443
// probes to two fixed IPs. On a network that blocks direct egress but allows
// HTTPS through a proxy, the two disagree for the life of the process, and the
// inference used to win: rows turned red, the tray read "Outage", and every
// single provider alert was journaled instead of shown.
func TestReachableFeedIsNotGatedByOffline(t *testing.T) {
	c := offlineController()
	if !c.offline.Offline() {
		t.Fatal("test setup: detector should be offline")
	}

	reachable := providers.Result{Feed: providers.FeedReachable, Severity: providers.SevMajor}
	if reason := c.alertSuppressedReason("openai", reachable, nil); reason != "" {
		t.Errorf("alert for a successfully parsed feed was suppressed as %q", reason)
	}

	// An UNREADABLE feed is different: there is no positive evidence, so the
	// offline banner legitimately covers it.
	unreachable := providers.Result{Feed: providers.FeedUnreachable}
	if reason := c.alertSuppressedReason("openai", unreachable, nil); reason != "offline" {
		t.Errorf("reason for an unreadable feed = %q, want %q", reason, "offline")
	}
}

// TestNotifyGatesReasonKeepsTheOtherGates guards against the offline split
// accidentally disabling mute / DND / notifications-off along with it.
func TestNotifyGatesReasonKeepsTheOtherGates(t *testing.T) {
	reachable := providers.Result{Feed: providers.FeedReachable, Severity: providers.SevMajor}

	c := offlineController()
	c.cfg.DoNotDisturb = true
	if reason := c.alertSuppressedReason("openai", reachable, nil); reason != "do-not-disturb" {
		t.Errorf("reason = %q, want do-not-disturb", reason)
	}

	c = offlineController()
	c.cfg.Notifications = false
	if reason := c.alertSuppressedReason("openai", reachable, nil); reason != "notifications-disabled" {
		t.Errorf("reason = %q, want notifications-disabled", reason)
	}

	c = offlineController()
	c.cfg.SetMute("openai", true)
	if reason := c.alertSuppressedReason("openai", reachable, nil); reason != "muted" {
		t.Errorf("reason = %q, want muted", reason)
	}
}

// TestRecoveryIsOrphanedTracksTheOutageEdge is A9. A recovery announcing the end
// of an outage the user was never told about is a message about nothing — and
// for the region-mute gate it was guaranteed rather than incidental:
// regionAlertSuppressed reads the CURRENT incident list, and by the time the
// recovery edge fires the muted incident is gone from the feed, so the gate that
// swallowed the outage cannot possibly still fire.
func TestRecoveryIsOrphanedTracksTheOutageEdge(t *testing.T) {
	c := onlineController()

	// Outage swallowed → its recovery must be swallowed too.
	c.noteAlertOutcome("azure", false, true)
	if !c.recoveryIsOrphaned("azure") {
		t.Error("a recovery following a suppressed outage should be orphaned")
	}

	// Delivering the recovery clears the flag, so the NEXT outage/recovery pair
	// is judged on its own.
	c.noteAlertOutcome("azure", true, true)
	if c.recoveryIsOrphaned("azure") {
		t.Error("the orphan flag survived the recovery it applied to")
	}
}

// TestDeliveredOutageLeavesRecoveryAlone is the counterweight: an outage the
// user actually saw must still produce its recovery.
func TestDeliveredOutageLeavesRecoveryAlone(t *testing.T) {
	c := onlineController()
	c.noteAlertOutcome("azure", false, false) // outage shown
	if c.recoveryIsOrphaned("azure") {
		t.Error("a recovery for a DELIVERED outage was marked orphaned — the user would never be told it ended")
	}
}

// TestForgetAlertOutcomeClearsTheFlag pins the reset wiring. Without it,
// disabling and re-enabling a check between the two edges leaves a stale flag
// that swallows a legitimate later recovery — a false negative created by the
// false-negative fix.
func TestForgetAlertOutcomeClearsTheFlag(t *testing.T) {
	c := onlineController()
	c.noteAlertOutcome("azure", false, true)
	c.forgetAlertOutcome("azure")
	if c.recoveryIsOrphaned("azure") {
		t.Error("forgetAlertOutcome left the suppression flag in place")
	}
}

// sharedCfgSnapshot matches a config snapshot taken WITHOUT Clone immediately
// before the lock is released.
var sharedCfgSnapshot = regexp.MustCompile(`(?m)^\s*\w+\s*:?=\s*c\.cfg\s*$\n\s*c\.mu\.Unlock\(\)`)

// TestEveryConfigSnapshotIsCloned is a static guard for the crash that three
// independent reviewers found and one reproduced.
//
// A Go value copy of config.Config SHARES its four maps. Every save site takes a
// snapshot under c.mu, releases the lock, and then hands the copy to
// config.Save, which marshals it — so a shallow copy meant json walking maps that
// the poll goroutine was still deleting from (pruneExpiredRegionMutes). That is
// "fatal error: concurrent map iteration and map write", which is NOT a panic:
// main.go's recover cannot catch it, so the app vanishes from the tray with no
// log line and loses up to a minute of history.
//
// Config.Clone exists and its doc states the invariant, but only 4 of 19 sites
// honoured it — the invariant was documented and unenforced. This test enforces
// it: the pattern is trivially greppable, and nothing else in the package will
// notice a 20th site added without Clone.
func TestEveryConfigSnapshotIsCloned(t *testing.T) {
	// A guard that cannot fail proves nothing, so first confirm the pattern
	// actually recognizes the bad shape and tolerates the good one.
	bad := "\tc.cfg.Sort = pref\n\tcfg := c.cfg\n\tc.mu.Unlock()\n"
	if !sharedCfgSnapshot.MatchString(bad) {
		t.Fatal("the guard no longer recognizes an un-cloned snapshot; it would pass silently")
	}
	good := "\tc.cfg.Sort = pref\n\tcfg := c.cfg.Clone()\n\tc.mu.Unlock()\n"
	if sharedCfgSnapshot.MatchString(good) {
		t.Fatal("the guard flags a correctly cloned snapshot")
	}

	files, err := filepath.Glob(filepath.Join(".", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, m := range sharedCfgSnapshot.FindAllString(string(src), -1) {
			t.Errorf("%s: config snapshot taken without Clone():\n\t%s\n"+
				"A value copy shares Config's maps; the unlocked marshal in config.Save "+
				"then races the poll goroutine and kills the process with a runtime fatal "+
				"that recover() cannot catch. Use `c.cfg.Clone()`.", f, strings.TrimSpace(m))
		}
	}
	if checked == 0 {
		t.Fatal("no source files scanned — the guard proved nothing")
	}
}
