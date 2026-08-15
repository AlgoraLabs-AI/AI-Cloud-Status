package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCorruptConfigIsQuarantinedNotOverwritten pins the recovery path for the
// one file holding authoritative user state.
//
// loadFrom returns Default() on an unmarshal error and its only production
// caller discards that error, so the app runs on defaults — and the first save
// of the session atomically replaces the only copy of the user's real settings
// with them. Every enabled check, every mute, every region of interest, every
// custom URL check, gone with nothing to recover from. History gets to be
// cavalier about corruption because history is best-effort data; config is not.
func TestCorruptConfigIsQuarantinedNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	const broken = `{"interval_seconds": 30, "enabled": {"openai": tru`
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadFrom(path)
	if err == nil {
		t.Error("loadFrom should report that the file was unreadable")
	}
	if cfg.IntervalSeconds != DefaultIntervalSeconds {
		t.Errorf("expected defaults, got interval %d", cfg.IntervalSeconds)
	}

	saved, rerr := os.ReadFile(path + ".corrupt")
	if rerr != nil {
		t.Fatalf("the unreadable config was not preserved: %v", rerr)
	}
	if string(saved) != broken {
		t.Errorf("quarantined copy = %q, want the original bytes verbatim", saved)
	}

	// The next save must not clobber the quarantined copy.
	if err := saveTo(path, cfg); err != nil {
		t.Fatalf("saveTo: %v", err)
	}
	again, _ := os.ReadFile(path + ".corrupt")
	if string(again) != broken {
		t.Error("the quarantined copy was overwritten by a later save")
	}
}

// TestQuarantineKeepsTheFirstCopy: repeatedly overwriting it would discard the
// ORIGINAL corruption in favour of whatever was written afterwards — the version
// nobody needs.
func TestQuarantineKeepsTheFirstCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte("first-corruption"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := quarantine(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second-corruption"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := quarantine(path); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path + ".corrupt")
	if string(got) != "first-corruption" {
		t.Errorf("quarantined copy = %q, want the first one", got)
	}
}

// TestNewerSchemaIsNotRelabelledAsCurrent pins the downgrade guard. migrate used
// to stamp CurrentSchemaVersion unconditionally, and encoding/json has already
// dropped every field this build does not know — so running an older build once
// turned a v2 file into something indistinguishable from a genuine v1 file. The
// next v2 run would then re-apply v1→v2 defaults over already-migrated data and
// the user's newer settings would be gone for good.
func TestNewerSchemaIsNotRelabelledAsCurrent(t *testing.T) {
	future := CurrentSchemaVersion + 1
	cfg := migrate(Config{SchemaVersion: future})
	if cfg.SchemaVersion != future {
		t.Errorf("SchemaVersion = %d, want %d — a file from the future must stay recognisably from the future",
			cfg.SchemaVersion, future)
	}
	// It must still be structurally usable.
	if cfg.Enabled == nil || cfg.IntervalSeconds <= 0 {
		t.Error("a newer-schema config was not repaired into a runnable state")
	}
}

// TestNewerSchemaFileIsPreserved: keeping the version label is not enough on its
// own, because json already discarded the unknown fields. A verbatim copy is
// what actually makes the newer build's settings recoverable.
func TestNewerSchemaFileIsPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	future := CurrentSchemaVersion + 1
	raw := `{"schema_version":` + itoa(future) + `,"interval_seconds":45,"a_field_this_build_never_heard_of":true}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := loadFrom(path); err != nil {
		t.Fatalf("a newer-schema file should still load: %v", err)
	}
	backup := path + ".v" + itoa(future)
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("newer-schema config was not preserved at %s: %v", backup, err)
	}
	if !strings.Contains(string(got), "a_field_this_build_never_heard_of") {
		t.Error("the preserved copy lost the very fields it exists to keep")
	}
}

// TestMigrateStillUpgradesOlderFiles is the counterweight: guarding against
// downgrades must not stop normal forward migration.
func TestMigrateStillUpgradesOlderFiles(t *testing.T) {
	cfg := migrate(Config{}) // pre-versioning file
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", cfg.SchemaVersion, CurrentSchemaVersion)
	}
	if !cfg.Notifications {
		t.Error("migration from a pre-versioning file must default notifications on")
	}
}

// TestSaveRoundTripsAfterRecovery makes sure the recovery paths leave a config
// that can still be written and read back.
func TestSaveRoundTripsAfterRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	cfg := Default()
	cfg.Regions = []string{"us-east-1"}
	if err := saveTo(path, cfg); err != nil {
		t.Fatal(err)
	}
	back, err := loadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Regions) != 1 || back.Regions[0] != "us-east-1" {
		t.Errorf("regions = %v, want [us-east-1]", back.Regions)
	}
	var onDisk map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
