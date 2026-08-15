package ui

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/applog"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
)

// setDebugLogging turns the diagnostic trail on or off, immediately and for
// good: it persists the choice, updates the process-wide mirror the poll
// goroutines read, opens or closes the log file, and re-titles the window.
//
// It applies live rather than on next launch because of who uses it and why.
// The person ticking this box is not configuring a preference — they are trying
// to capture something that is going wrong now, and "restart to start logging"
// asks them to reproduce a bug across a restart they may not be able to
// reproduce it across.
//
// Turning it OFF stops new writes but deliberately deletes nothing. The files
// exist to be attached to a bug report; erasing them the moment the box is
// unticked would throw away the evidence the user just finished collecting.
func (c *Controller) setDebugLogging(on bool) {
	c.updateCfg(func(cfg *config.Config) { cfg.DebugLogging = on })
	config.SetDebugLogging(on)

	dir, err := config.Dir()
	switch {
	case err != nil:
		slog.Warn("diagnostics: no config directory", "err", err)
	case config.DebugLogging():
		if err := applog.Enable(dir); err != nil {
			slog.Warn("diagnostics: could not open the log file", "err", err)
		}
		slog.Info("diagnostic logging enabled", "version", Version, "dir", dir)
	default:
		slog.Info("diagnostic logging disabled")
		applog.Disable()
	}

	// Keep the caption in step with the state it reports. Every Win32 lookup of
	// this window goes through windowTitle(), so re-titling here keeps the
	// dead-canvas watchdog able to find the window (see windowTitle).
	if c.window != nil {
		c.window.SetTitle(windowTitle())
	}
}

// diagnosticFiles are the artifacts the diagnostics setting produces — the ones
// that may be deleted without touching anything the user would miss. Everything
// else in the data folder (config, history, the incident journal, open outages,
// the lock) is the user's own state and is never touched here.
//
// Named rather than "everything except a keep-list" so a file added later is
// excluded by DEFAULT. Getting that inversion wrong would delete a user's
// settings to reclaim a few megabytes.
var diagnosticFiles = []string{
	"acs.log", "acs.log.1", "acs.log.1.gz", "acs.log.2.gz", "acs.log.3.gz",
	"acs.log.rotating", "alert-log.jsonl",
}

// diagnosticDirs are the diagnostic directories, removed whole.
var diagnosticDirs = []string{"feed-samples"}

// diagnosticsUsage returns how many bytes the diagnostic artifacts currently
// occupy. Shown in Settings because a number is the only honest answer to "how
// much is this costing me?" — and because the figure that motivated the budget
// (202 MB of archived feed payloads on a one-month-old install) was invisible
// until somebody went looking for it.
func diagnosticsUsage() int64 {
	dir, err := config.Dir()
	if err != nil {
		return 0
	}
	var total int64
	for _, name := range diagnosticFiles {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			total += fi.Size()
		}
	}
	for _, sub := range diagnosticDirs {
		_ = filepath.WalkDir(filepath.Join(dir, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable entry just doesn't count
			}
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
			return nil
		})
	}
	return total
}

// deleteDiagnostics removes the diagnostic artifacts and returns how many bytes
// were reclaimed. It never touches the user's own state.
//
// This exists because turning the setting OFF deliberately keeps the files — they
// are there to be attached to a bug report, and sweeping them up the moment the
// box is unticked would throw away the evidence the user just collected. That
// leaves them needing a way to say "I'm done with these", and "go delete files
// out of %AppData% yourself" is not one.
func deleteDiagnostics() (int64, error) {
	dir, err := config.Dir()
	if err != nil {
		return 0, err
	}
	freed := diagnosticsUsage()
	var firstErr error
	for _, name := range diagnosticFiles {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	for _, sub := range diagnosticDirs {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// The live log is one of the files just deleted, so reopen it if logging is
	// still on — otherwise the app would keep writing into a removed handle and
	// the user would think logging had silently stopped.
	if config.DebugLogging() {
		applog.Disable()
		if err := applog.Enable(dir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	slog.Info("diagnostic data deleted", "freed_bytes", freed)
	return freed, firstErr
}

// humanBytes renders a byte count the way a person reads it.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// openDataFolder opens the app's data directory in the platform file manager.
//
// It is next to the diagnostics toggle because the two are one workflow: turn
// logging on, reproduce the problem, then GET the files to attach. Without this
// the last step is "navigate to %AppData%", which is exactly the instruction
// that loses a bug report.
func openDataFolder() {
	dir, err := config.Dir()
	if err != nil {
		slog.Warn("could not resolve the data folder", "err", err)
		return
	}
	// The folder may not exist yet on a first run that has saved nothing.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Warn("could not create the data folder", "dir", dir, "err", err)
		return
	}
	// A file:// URL, so this goes through the same OS handler as every other link
	// the app opens rather than shelling out to a per-platform file manager.
	fyne.CurrentApp().OpenURL(&url.URL{Scheme: "file", Path: filepathToURLPath(dir)})
}

// filepathToURLPath makes an OS path usable as the Path of a file:// URL:
// backslashes become forward slashes (url.URL would otherwise percent-escape
// them into a path no file manager resolves), and a Windows drive letter gets
// the leading slash a file:// URL requires — "C:\Users\x" → "/C:/Users/x".
func filepathToURLPath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) > 1 && p[1] == ':' {
		return "/" + p
	}
	return p
}
