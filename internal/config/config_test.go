package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadFromMissingReturnsDefaults(t *testing.T) {
	cfg, err := loadFrom(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IntervalSeconds != DefaultIntervalSeconds {
		t.Errorf("interval = %d, want %d", cfg.IntervalSeconds, DefaultIntervalSeconds)
	}
	if cfg.Enabled == nil {
		t.Error("Enabled map should be non-nil")
	}
}

func TestDefaultIntervalIs30(t *testing.T) {
	if Default().IntervalSeconds != 30 {
		t.Errorf("default interval = %d, want 30", Default().IntervalSeconds)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{
		IntervalSeconds: 30,
		Enabled: map[string]bool{
			"ping-1.1.1.1": true,
			"openai":       false,
		},
		CustomTargets: []string{"example.com", "10.0.0.1"},
	}
	if err := saveTo(path, want); err != nil {
		t.Fatalf("saveTo: %v", err)
	}
	got, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if got.IntervalSeconds != want.IntervalSeconds {
		t.Errorf("interval = %d, want %d", got.IntervalSeconds, want.IntervalSeconds)
	}
	for k, v := range want.Enabled {
		if got.Enabled[k] != v {
			t.Errorf("Enabled[%q] = %v, want %v", k, got.Enabled[k], v)
		}
	}
	if len(got.CustomTargets) != 2 || got.CustomTargets[0] != "example.com" || got.CustomTargets[1] != "10.0.0.1" {
		t.Errorf("CustomTargets = %v, want [example.com 10.0.0.1]", got.CustomTargets)
	}
}

func TestCustomTargetHelpers(t *testing.T) {
	var c Config

	if !c.AddCustomTarget("example.com") {
		t.Fatal("AddCustomTarget should add a new host")
	}
	if c.AddCustomTarget("example.com") {
		t.Error("AddCustomTarget should not add a duplicate")
	}
	if c.AddCustomTarget("") {
		t.Error("AddCustomTarget should reject empty host")
	}
	if !c.HasCustomTarget("example.com") {
		t.Error("HasCustomTarget should find the added host")
	}
	if !c.AddCustomTarget("10.0.0.1") {
		t.Fatal("second host should be added")
	}

	if !c.RemoveCustomTarget("example.com") {
		t.Fatal("RemoveCustomTarget should remove an existing host")
	}
	if c.RemoveCustomTarget("example.com") {
		t.Error("RemoveCustomTarget should be a no-op for a missing host")
	}
	if c.HasCustomTarget("example.com") {
		t.Error("host should be gone after removal")
	}
	if !c.HasCustomTarget("10.0.0.1") {
		t.Error("other host should remain")
	}
}

func TestRegionHelpers(t *testing.T) {
	var c Config

	if !c.AddRegion("us-east-1") {
		t.Fatal("AddRegion should add a new region")
	}
	if c.AddRegion("us-east-1") {
		t.Error("AddRegion should reject an exact duplicate")
	}
	if c.AddRegion("  US-EAST-1 ") {
		t.Error("AddRegion should reject a case/whitespace duplicate")
	}
	if c.AddRegion("   ") {
		t.Error("AddRegion should reject a blank region")
	}
	if !c.HasRegion("US-East-1") {
		t.Error("HasRegion should match case-insensitively")
	}
	if !c.AddRegion("West Europe") {
		t.Fatal("second region should be added")
	}
	if len(c.Regions) != 2 {
		t.Fatalf("Regions = %v, want 2", c.Regions)
	}

	if !c.RemoveRegion("us-east-1") {
		t.Fatal("RemoveRegion should remove an existing region")
	}
	if c.RemoveRegion("us-east-1") {
		t.Error("RemoveRegion should be a no-op for a missing region")
	}
	if c.HasRegion("us-east-1") {
		t.Error("region should be gone after removal")
	}
	if !c.HasRegion("West Europe") {
		t.Error("other region should remain")
	}
}

func TestRegionsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := Config{
		IntervalSeconds: DefaultIntervalSeconds,
		Enabled:         map[string]bool{},
		Regions:         []string{"us-east-1", "West Europe"},
	}
	if err := saveTo(path, want); err != nil {
		t.Fatalf("saveTo: %v", err)
	}
	got, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if len(got.Regions) != 2 || got.Regions[0] != "us-east-1" || got.Regions[1] != "West Europe" {
		t.Errorf("Regions = %v, want [us-east-1 West Europe]", got.Regions)
	}
}

func TestLoadFromRepairsInvalidInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveTo(path, Config{IntervalSeconds: 0, Enabled: nil}); err != nil {
		t.Fatalf("saveTo: %v", err)
	}
	got, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if got.IntervalSeconds != DefaultIntervalSeconds {
		t.Errorf("interval = %d, want repaired to %d", got.IntervalSeconds, DefaultIntervalSeconds)
	}
	if got.Enabled == nil {
		t.Error("Enabled should be repaired to non-nil")
	}
}

func TestDefaultHasCurrentSchemaAndNotificationsOn(t *testing.T) {
	d := Default()
	if d.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", d.SchemaVersion, CurrentSchemaVersion)
	}
	if !d.Notifications {
		t.Error("Notifications should default to true")
	}
	if d.Mutes == nil {
		t.Error("Mutes map should be non-nil")
	}
}

func TestFullRoundTripPreservesNewFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := Config{
		SchemaVersion:    CurrentSchemaVersion,
		IntervalSeconds:  42,
		Enabled:          map[string]bool{"openai": true},
		Mutes:            map[string]bool{"openai": true},
		MutedUntil:       map[string]int64{"openai": 1893456000},
		CustomTargets:    []string{"example.com"},
		Regions:          []string{"us-east-1"},
		Window:           WindowState{Width: 800, Height: 600},
		Sort:             SortPref{Column: "severity", Ascending: true},
		Notifications:    false,
		DoNotDisturb:     true,
		ReducedMotion:    true,
		StartOnLogin:     true,
		FirstRunComplete: true,
	}
	if err := saveTo(path, want); err != nil {
		t.Fatalf("saveTo: %v", err)
	}
	got, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if got.Window != want.Window {
		t.Errorf("Window = %+v, want %+v", got.Window, want.Window)
	}
	if got.Sort != want.Sort {
		t.Errorf("Sort = %+v, want %+v", got.Sort, want.Sort)
	}
	if got.Notifications != want.Notifications || got.DoNotDisturb != want.DoNotDisturb {
		t.Errorf("notification prefs = (%v,%v), want (%v,%v)",
			got.Notifications, got.DoNotDisturb, want.Notifications, want.DoNotDisturb)
	}
	if got.ReducedMotion != want.ReducedMotion || got.StartOnLogin != want.StartOnLogin || got.FirstRunComplete != want.FirstRunComplete {
		t.Errorf("flags = (%v,%v,%v), want (%v,%v,%v)",
			got.ReducedMotion, got.StartOnLogin, got.FirstRunComplete,
			want.ReducedMotion, want.StartOnLogin, want.FirstRunComplete)
	}
	if !got.IsMuted("openai") {
		t.Error("openai should be muted after round-trip")
	}
	if got.MutedUntil["openai"] != want.MutedUntil["openai"] {
		t.Errorf("MutedUntil[openai] = %d, want %d", got.MutedUntil["openai"], want.MutedUntil["openai"])
	}
}

// TestMigrateFromUnversionedFile ensures a Phase 1 file (no schema_version,
// no notifications field) loads with notifications defaulted ON and bumped to
// the current schema version, without losing its existing settings.
func TestMigrateFromUnversionedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	// A pre-versioning file: only the four Phase 1 fields, no schema_version.
	legacy := `{
  "interval_seconds": 20,
  "enabled": {"openai": true},
  "custom_targets": ["1.2.3.4"],
  "regions": ["us-east-1"]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	got, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want migrated to %d", got.SchemaVersion, CurrentSchemaVersion)
	}
	if !got.Notifications {
		t.Error("migration should default Notifications to true for a legacy file")
	}
	if got.IntervalSeconds != 20 || !got.IsEnabled("openai", false) {
		t.Error("migration should preserve existing Phase 1 settings")
	}
	if got.Mutes == nil {
		t.Error("Mutes should be initialized after migration")
	}
}

func TestSaveStampsSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	// Save a Config that forgot to set the version; saveTo must stamp it.
	if err := saveTo(path, Config{IntervalSeconds: 15, Enabled: map[string]bool{}, Notifications: true}); err != nil {
		t.Fatalf("saveTo: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, _ := raw["schema_version"].(float64); int(v) != CurrentSchemaVersion {
		t.Errorf("on-disk schema_version = %v, want %d", raw["schema_version"], CurrentSchemaVersion)
	}
}

// TestSaveIsAtomic verifies that the temp file is not left behind and the final
// file is valid JSON after a successful save (an interrupted save would leave the
// original intact, which we approximate by confirming no .tmp residue remains).
func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := saveTo(path, Default()); err != nil {
		t.Fatalf("saveTo: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("unexpected residue file after atomic save: %q", e.Name())
		}
	}
}

// TestConcurrentSavesSucceed reproduces the original "config save failed:
// Access is denied" bug: many goroutines saving to the same config.json at once.
// On Windows, concurrent MoveFileEx replaces onto the same target raced into a
// sharing/access violation (200+ failures in a single burst in the field). With
// writeMu serializing the temp+rename, every save must now succeed and leave no
// orphaned config.json.tmp-* residue.
func TestConcurrentSavesSucceed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	const writers = 50
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := Default()
			cfg.IntervalSeconds = 10 + i
			if err := saveTo(path, cfg); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent saveTo failed: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("orphaned temp residue after concurrent saves: %q", e.Name())
		}
	}
	// The surviving file must still be a valid, parseable config.
	if _, err := loadFrom(path); err != nil {
		t.Errorf("config unreadable after concurrent saves: %v", err)
	}
}

func TestMuteHelpers(t *testing.T) {
	var c Config
	if c.IsMuted("openai") {
		t.Error("nothing should be muted initially")
	}
	c.SetMute("openai", true)
	if !c.IsMuted("openai") {
		t.Error("openai should be muted")
	}
	c.SetMute("openai", false)
	if c.IsMuted("openai") {
		t.Error("openai should be unmuted")
	}
}

func TestIsEnabled(t *testing.T) {
	c := Config{Enabled: map[string]bool{"on": true, "off": false}}
	if !c.IsEnabled("on", false) {
		t.Error(`"on" should be enabled`)
	}
	if c.IsEnabled("off", true) {
		t.Error(`"off" should be disabled`)
	}
	if !c.IsEnabled("unknown", true) {
		t.Error("unknown id should fall back to default true")
	}
	if c.IsEnabled("unknown", false) {
		t.Error("unknown id should fall back to default false")
	}
}
