// Package config loads and persists user settings to a versioned JSON file in
// the OS user-config directory. The on-disk schema carries a SchemaVersion so
// the app can migrate older files forward gracefully as fields are added, and
// every save is atomic (written to a temp file and renamed into place) so a
// crash mid-write can never corrupt the user's settings.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/atomicfile"
)

const (
	appDir   = "AI-Cloud-Status"
	fileName = "config.json"

	// DefaultIntervalSeconds is used when no interval has been persisted.
	DefaultIntervalSeconds = 30

	// DefaultConnIntervalSeconds is the connectivity (ICMP/TCP ping) probe cadence
	// when none has been persisted. It is fast (1s) so offline detection and
	// packet-loss measurement stay responsive; the user may slow it down.
	DefaultConnIntervalSeconds = 1

	// RegionMuteForever is the RegionMutedUntil sentinel for a deactivation that
	// never expires on its own ("until I reactivate"). It is negative so it sorts
	// before any real Unix timestamp; expiry checks and GC must special-case it
	// (it must NOT be treated as already-expired).
	RegionMuteForever int64 = -1

	// CurrentSchemaVersion is the schema version this build writes. Loading a
	// file with a lower version triggers migration (see migrate); a higher
	// version is read best-effort (unknown fields are ignored by encoding/json).
	CurrentSchemaVersion = 1

	// DefaultWindowWidth / DefaultWindowHeight are the initial main-window size,
	// used when no size has been persisted yet.
	DefaultWindowWidth  = 1120
	DefaultWindowHeight = 640

	// DefaultSplitOffset is the initial share of the window given to the check
	// panel. It was 0.24, which on a 1920px screen handed 460px to a column of
	// checkboxes whose longest label is well under half that — space the status
	// table wanted and the panel did not. 0.18 is ~345px there, still comfortable
	// for the longest provider name, and the user's own drag is remembered from
	// then on (see WindowState.SplitOffset).
	DefaultSplitOffset = 0.18

	// MinSplitOffset / MaxSplitOffset bound a restored offset. A persisted value
	// outside them — from a hand-edited config, or a drag to the very edge —
	// would otherwise restore a window with one side collapsed to nothing and no
	// obvious way back.
	MinSplitOffset = 0.10
	MaxSplitOffset = 0.50
)

// WindowState persists the main window's last size so it is restored on the
// next launch. Position is intentionally omitted: Fyne does not expose a
// portable way to read or restore a window's screen coordinates.
type WindowState struct {
	Width  float32 `json:"width,omitempty"`
	Height float32 `json:"height,omitempty"`
	// SplitOffset is the check-panel / table divider position as a fraction of
	// the window width. Persisted because dragging it is how a user tells the app
	// how much of the screen the table deserves, and having that reset on every
	// launch makes the adjustment feel like it did not take. Zero means "never
	// set" and falls back to DefaultSplitOffset.
	SplitOffset float64 `json:"split_offset,omitempty"`
}

// SortPref persists the status-table sort order so the user's choice survives a
// restart. Column is one of the SortBy* identifiers in the ui package; an empty
// Column means "the default" (sort by severity, worst first).
type SortPref struct {
	Column    string `json:"column,omitempty"`
	Ascending bool   `json:"ascending,omitempty"`
}

// URLMode selects how a custom URL check decides the endpoint is "up".
type URLMode string

const (
	// URLModeReachable follows redirects and passes when the endpoint responds —
	// the final status is 2xx (or a redirect that resolves), i.e. "200 or redirect".
	URLModeReachable URLMode = "reachable"
	// URLModeContains follows redirects and passes when the final status is a
	// success AND the response body contains Expect (case-insensitive). Requiring
	// a non-error status stops an error page that happens to contain the string
	// from false-passing.
	URLModeContains URLMode = "contains"
)

// URLCheck is a user-defined HTTP endpoint monitor (e.g. "is my site up?").
type URLCheck struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	URL    string  `json:"url"`
	Mode   URLMode `json:"mode"`
	Expect string  `json:"expect,omitempty"` // substring to look for in URLModeContains
}

// UpdateRecord is what an applied automatic update leaves behind: which version
// was installed, the sha256 that was verified before installing it, and when.
// The digest is recorded because it is the only thing that makes the record
// checkable — a user can hash their binary and compare, and a maintainer can
// tell a legitimate release apart from something else wearing its version.
type UpdateRecord struct {
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	At      int64  `json:"at,omitempty"` // Unix seconds
}

// Config is the persisted application configuration.
type Config struct {
	// SchemaVersion is the on-disk schema version (see CurrentSchemaVersion).
	// A zero value denotes a pre-versioning (Phase 1) file that needs migration.
	SchemaVersion int `json:"schema_version"`
	// IntervalSeconds is the provider poll interval in seconds.
	IntervalSeconds int `json:"interval_seconds"`
	// ConnIntervalSeconds is the connectivity (ping) probe interval in seconds.
	// Separate from IntervalSeconds — connectivity probes run far more often than
	// provider status polls. Zero falls back to DefaultConnIntervalSeconds.
	ConnIntervalSeconds int `json:"conn_interval_seconds"`
	// Enabled maps a check ID to whether the user has enabled it.
	Enabled map[string]bool `json:"enabled"`
	// Mutes maps a check ID to whether the user has muted its notifications.
	// A muted check is still polled and shown in the table; only its desktop
	// alerts are suppressed.
	Mutes map[string]bool `json:"mutes,omitempty"`
	// MutedUntil maps a check ID to a Unix timestamp (seconds) until which its
	// notifications are silenced — a temporary, self-expiring mute set from the
	// "Silence 1h/4h/8h" actions on an outage alert. A past or absent value means
	// not silenced. Persisted so a silence survives a restart.
	MutedUntil map[string]int64 `json:"muted_until,omitempty"`
	// RegionMutedUntil maps a region identifier (the form shown on the badge the
	// user clicked, e.g. "US East (us-east-1)") to a Unix timestamp until which
	// provider alerts confined to that region are suppressed. The sentinel
	// RegionMuteForever (-1) means "until I reactivate" (never expires). Matching an
	// incident's region against these keys is substring-tolerant via
	// providers.MatchRegion — NOT exact key lookup — so the feed's region strings
	// and the key form need not be identical. The check keeps running; only the
	// region-scoped alert is muted. Persisted so a deactivation survives a restart.
	RegionMutedUntil map[string]int64 `json:"region_muted_until,omitempty"`
	// CustomTargets are user-added connectivity hosts (IPs or hostnames) in
	// addition to the built-in ping targets.
	CustomTargets []string `json:"custom_targets,omitempty"`
	// CustomURLChecks are user-defined HTTP endpoint monitors.
	CustomURLChecks []URLCheck `json:"custom_url_checks,omitempty"`
	// Regions are the region/location identifiers the user cares about (e.g.
	// "us-east-1", "West Europe"). When non-empty, provider alerts and UI
	// highlights are scoped to incidents touching these regions; an empty list
	// means "care about every region" (the global default).
	Regions []string `json:"regions,omitempty"`
	// DebugLogging turns on the diagnostic artifacts (acs.log, alert-log.jsonl,
	// feed-samples/). Off by default — see DebugLogging() for what each file is
	// and why the default is off. Any user can turn it on from Settings, because
	// the reason to turn it on is to produce a useful bug report.
	DebugLogging bool `json:"debug_logging,omitempty"`
	// LastUpdate records the most recent automatic update this install applied.
	// It is the ONLY residue the update path leaves on a public build, and it is
	// deliberately here rather than in a log file: an unsigned auto-updater that
	// replaces its own executable owes the user an answer to "was my copy
	// replaced, when, and with what", and a file that is already written for
	// other reasons can carry that without adding anything to their disk. Shown
	// in Help > About. Empty on an install that has never auto-updated.
	LastUpdate UpdateRecord `json:"last_update,omitempty"`
	// Window persists the main window's last size.
	Window WindowState `json:"window"`
	// Sort persists the status-table sort order.
	Sort SortPref `json:"sort"`
	// Notifications enables/disables all desktop notifications. It defaults to
	// true; migration from a pre-versioning file preserves that default.
	Notifications bool `json:"notifications"`
	// DoNotDisturb, when set, suppresses every desktop notification regardless
	// of the per-check mute state (a global, temporary quiet switch).
	DoNotDisturb bool `json:"do_not_disturb,omitempty"`
	// ReducedMotion, when set, disables UI state-change animations. The
	// AI_STATUS_PINGER_REDUCED_MOTION environment variable still overrides it.
	ReducedMotion bool `json:"reduced_motion,omitempty"`
	// StartOnLogin reflects the user's desired start-on-login state. The actual
	// OS registration is reconciled to this value at startup (Windows: a HKCU
	// Run-key entry, user scope, no admin).
	StartOnLogin bool `json:"start_on_login,omitempty"`
	// FirstRunComplete is set once the user has been shown the first-run guidance
	// so it is not shown again.
	FirstRunComplete bool `json:"first_run_complete,omitempty"`
	// TrayNoticeShown is set once the "still running in the tray" notification
	// has been shown after the first-ever hide, so it never repeats — not even
	// across app restarts.
	TrayNoticeShown bool `json:"tray_notice_shown,omitempty"`
	// Language is the UI language: "en" (default) or "es".
	Language string `json:"language,omitempty"`
}

// Clone returns a deep copy of the config: every map and slice field is copied
// into fresh backing storage so the result shares no mutable state with the
// receiver. Callers snapshot under the controller lock with Clone and then save
// the detached copy after unlocking, so a concurrent locked mutation can never
// race the (unlocked) JSON marshal — needed because a save can run on a
// background goroutine while the UI thread edits the live config.
func (c Config) Clone() Config {
	out := c // value copy handles scalars + struct fields
	out.Enabled = cloneBoolMap(c.Enabled)
	out.Mutes = cloneBoolMap(c.Mutes)
	out.MutedUntil = cloneInt64Map(c.MutedUntil)
	out.RegionMutedUntil = cloneInt64Map(c.RegionMutedUntil)
	if c.CustomTargets != nil {
		out.CustomTargets = append([]string(nil), c.CustomTargets...)
	}
	if c.CustomURLChecks != nil {
		out.CustomURLChecks = append([]URLCheck(nil), c.CustomURLChecks...)
	}
	if c.Regions != nil {
		out.Regions = append([]string(nil), c.Regions...)
	}
	return out
}

func cloneBoolMap(m map[string]bool) map[string]bool {
	if m == nil {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneInt64Map(m map[string]int64) map[string]int64 {
	if m == nil {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Default returns a Config with sane defaults and non-nil maps.
func Default() Config {
	return Config{
		SchemaVersion:       CurrentSchemaVersion,
		IntervalSeconds:     DefaultIntervalSeconds,
		ConnIntervalSeconds: DefaultConnIntervalSeconds,
		Enabled:             map[string]bool{},
		Mutes:               map[string]bool{},
		Notifications:       true,
	}
}

// Path returns the absolute path of the config file in the user-config dir.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, appDir, fileName), nil
}

// Dir returns the application's config directory (created on demand by Save).
func Dir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, appDir), nil
}

// Load reads the config from the default path, returning defaults if the file
// does not yet exist.
func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		cfg := Default()
		SetDebugLogging(cfg.DebugLogging)
		return cfg, err
	}
	cfg, err := loadFrom(p)
	// Sync the process-wide debug-logging mirror here, not at each call site: the
	// log writer, the window title, and the poll goroutines all read it, and they
	// run at moments where no Config is in hand. Doing it in Load means every path
	// that reads the user's settings — including a failed read falling back to
	// defaults — leaves the mirror agreeing with them.
	SetDebugLogging(cfg.DebugLogging)
	return cfg, err
}

// Save writes the config to the default path, creating the directory if needed.
func Save(cfg Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	return saveTo(p, cfg)
}

// IsEnabled reports whether the check id is enabled, falling back to def when
// the user has never expressed a preference for it.
func (c Config) IsEnabled(id string, def bool) bool {
	if v, ok := c.Enabled[id]; ok {
		return v
	}
	return def
}

// IsMuted reports whether the check id's notifications are muted.
func (c Config) IsMuted(id string) bool {
	return c.Mutes[id]
}

// SetMute records whether the check id's notifications are muted.
func (c *Config) SetMute(id string, muted bool) {
	if c.Mutes == nil {
		c.Mutes = map[string]bool{}
	}
	if muted {
		c.Mutes[id] = true
	} else {
		delete(c.Mutes, id)
	}
}

// HasCustomTarget reports whether host is already a custom target.
func (c Config) HasCustomTarget(host string) bool {
	return slices.Contains(c.CustomTargets, host)
}

// AddCustomTarget appends host if it is not already present. It returns true if
// the target was added.
func (c *Config) AddCustomTarget(host string) bool {
	if host == "" || c.HasCustomTarget(host) {
		return false
	}
	c.CustomTargets = append(c.CustomTargets, host)
	return true
}

// RemoveCustomTarget removes host (and any enabled flag for its check id). It
// returns true if a target was removed.
func (c *Config) RemoveCustomTarget(host string) bool {
	idx := -1
	for i, h := range c.CustomTargets {
		if h == host {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	c.CustomTargets = append(c.CustomTargets[:idx], c.CustomTargets[idx+1:]...)
	return true
}

// AddURLCheck appends u if its ID is not already present. Returns true if added.
func (c *Config) AddURLCheck(u URLCheck) bool {
	for _, e := range c.CustomURLChecks {
		if e.ID == u.ID {
			return false
		}
	}
	c.CustomURLChecks = append(c.CustomURLChecks, u)
	return true
}

// RemoveURLCheck removes the URL check with the given ID. Returns true if one
// was removed; it also clears any enabled/mute/silence state keyed on that ID.
func (c *Config) RemoveURLCheck(id string) bool {
	idx := -1
	for i, e := range c.CustomURLChecks {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	c.CustomURLChecks = append(c.CustomURLChecks[:idx], c.CustomURLChecks[idx+1:]...)
	delete(c.Enabled, id)
	delete(c.Mutes, id)
	delete(c.MutedUntil, id)
	return true
}

// HasRegion reports whether region is already a region of interest
// (case-insensitive).
func (c Config) HasRegion(region string) bool {
	region = NormalizeRegion(region)
	for _, r := range c.Regions {
		if strings.EqualFold(NormalizeRegion(r), region) {
			return true
		}
	}
	return false
}

// AddRegion appends region (normalized) if it is not already present. It returns
// true if the region was added.
func (c *Config) AddRegion(region string) bool {
	region = NormalizeRegion(region)
	if region == "" || c.HasRegion(region) {
		return false
	}
	c.Regions = append(c.Regions, region)
	return true
}

// RemoveRegion removes region (case-insensitive). It returns true if a region
// was removed.
func (c *Config) RemoveRegion(region string) bool {
	region = NormalizeRegion(region)
	for i, r := range c.Regions {
		if strings.EqualFold(NormalizeRegion(r), region) {
			c.Regions = append(c.Regions[:i], c.Regions[i+1:]...)
			return true
		}
	}
	return false
}

// SetRegionMuteUntil records that region is deactivated until the given Unix
// timestamp (or RegionMuteForever). A no-op for an empty region.
func (c *Config) SetRegionMuteUntil(region string, until int64) {
	region = NormalizeRegion(region)
	if region == "" {
		return
	}
	if c.RegionMutedUntil == nil {
		c.RegionMutedUntil = map[string]int64{}
	}
	c.RegionMutedUntil[region] = until
}

// ClearRegionMute removes any persisted deactivation for region.
func (c *Config) ClearRegionMute(region string) {
	delete(c.RegionMutedUntil, NormalizeRegion(region))
}

// PruneExpiredSilences deletes every per-check silence whose expiry is at or
// before nowUnix, and returns the check ids whose silence just elapsed.
//
// MutedUntil is purely timed — a permanent mute lives in Mutes — so unlike
// PruneExpiredRegionMutes there is no forever sentinel to preserve here.
func (c *Config) PruneExpiredSilences(nowUnix int64) []string {
	var expired []string
	for id, until := range c.MutedUntil {
		if until <= nowUnix {
			expired = append(expired, id)
			delete(c.MutedUntil, id)
		}
	}
	return expired
}

// PruneExpiredRegionMutes deletes every timed region mute whose expiry is at or
// before nowUnix, leaving RegionMuteForever entries in place, and returns the
// regions whose mute just expired (for a "monitoring resumed" notice). Keeping
// the persisted map from growing unbounded mirrors the per-check silence GC.
func (c *Config) PruneExpiredRegionMutes(nowUnix int64) []string {
	var expired []string
	for region, until := range c.RegionMutedUntil {
		if until != RegionMuteForever && until <= nowUnix {
			expired = append(expired, region)
			delete(c.RegionMutedUntil, region)
		}
	}
	return expired
}

// NormalizeRegion trims surrounding whitespace from a region identifier. Region
// matching itself is case-insensitive (see providers.MatchRegion), so the cased
// form the user typed is preserved for display.
func NormalizeRegion(region string) string {
	return strings.TrimSpace(region)
}

// loadFrom is the file-path-injectable core of Load, kept separate so tests can
// exercise it without touching the real user-config directory.
func loadFrom(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	// Unmarshal onto a zero Config (not the populated Default) so we can detect
	// which version the file declares and migrate it deliberately, rather than
	// silently inheriting current defaults for fields the old file lacked.
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		// Quarantine before handing back defaults. The caller runs on those
		// defaults and the first save of the session atomically replaces the only
		// copy of the user's real settings — every enabled check, every mute,
		// every region of interest, every custom URL — with them. History is
		// deliberately best-effort about corruption because it is best-effort
		// data; config is authoritative user state and deserves a copy kept.
		if qerr := quarantine(path); qerr != nil {
			err = fmt.Errorf("%w (and the unreadable file could not be preserved: %v)", err, qerr)
		}
		return Default(), err
	}
	if loaded.SchemaVersion > CurrentSchemaVersion {
		// A file written by a NEWER build. encoding/json has already discarded
		// every field this build does not know, so whatever we save next is
		// lossy no matter how carefully we label it. Keep the original aside so
		// the newer build's settings are recoverable instead of gone.
		if berr := preserveNewer(path, loaded.SchemaVersion); berr != nil {
			slog.Warn("could not preserve a newer-schema config", "path", path, "err", berr)
		}
	}
	return migrate(loaded), nil
}

// preserveNewer keeps a verbatim copy of a config written by a newer build,
// named for the schema version it declares. Never overwrites an existing copy —
// the first one is the one closest to the user's real settings.
func preserveNewer(path string, version int) error {
	backup := fmt.Sprintf("%s.v%d", path, version)
	if _, err := os.Stat(backup); err == nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(backup, data, 0o600)
}

// quarantine renames an unreadable config aside so it can be inspected or
// recovered by hand instead of being overwritten by the next save.
func quarantine(path string) error {
	bad := path + ".corrupt"
	// One retained copy: repeatedly overwriting it would discard the ORIGINAL
	// corruption in favour of whatever defaults were written afterwards, which is
	// the version nobody needs.
	if _, err := os.Stat(bad); err == nil {
		return nil
	}
	return os.Rename(path, bad)
}

// migrate brings a loaded config up to CurrentSchemaVersion, filling in
// defaults for fields that did not exist in the version the file was written
// with, and repairing anything an externally-edited file may have left invalid.
func migrate(cfg Config) Config {
	// A file from a NEWER build is not ours to relabel. encoding/json has already
	// dropped every field this build does not know, so stamping it with the
	// current version would make a truncated v2 file indistinguishable from a
	// genuine v1 one — and the next v2 run would then re-apply v1→v2 defaults
	// over already-migrated data, losing the user's newer settings permanently.
	// Keep the declared version so the file is recognisably from the future, and
	// only fill in what is structurally required to run.
	if cfg.SchemaVersion > CurrentSchemaVersion {
		return repair(cfg)
	}
	if cfg.SchemaVersion < 1 {
		// Pre-versioning (Phase 1) files predate the Notifications field; its zero
		// value (false) would silently disable notifications, so default it on.
		cfg.Notifications = true
	}
	cfg.SchemaVersion = CurrentSchemaVersion
	return repair(cfg)
}

// repair fills in the structural minimum a Config needs to be usable, without
// touching its declared schema version.
func repair(cfg Config) Config {
	if cfg.Enabled == nil {
		cfg.Enabled = map[string]bool{}
	}
	if cfg.Mutes == nil {
		cfg.Mutes = map[string]bool{}
	}
	if cfg.IntervalSeconds <= 0 {
		cfg.IntervalSeconds = DefaultIntervalSeconds
	}
	if cfg.ConnIntervalSeconds <= 0 {
		cfg.ConnIntervalSeconds = DefaultConnIntervalSeconds
	}
	return cfg
}

// saveTo is the file-path-injectable core of Save. The write is atomic: the
// config is serialized to a temp file in the same directory and then renamed
// over the destination, so a crash or power loss mid-write cannot truncate or
// corrupt an existing config.
func saveTo(path string, cfg Config) error {
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = CurrentSchemaVersion
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 0600: this file records which services you watch and can carry credentials
	// inside a monitored URL.
	return atomicfile.Write(path, data, 0o600)
}
