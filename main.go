// Command AI-Cloud-Status is a cross-platform system-tray app that monitors
// internet connectivity and the status pages of AI/cloud providers.
package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/applog"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/singleton"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/ui"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/updater"
)

func main() {
	// Single-instance guard FIRST. A second copy must exit before it touches the
	// log file — otherwise its startup log-rotation could truncate the running
	// instance's live log out from under it. The lock is released on a clean exit;
	// a crashed previous instance leaves only a stale lockfile the next launch
	// reclaims.
	lock, ok := acquireSingleInstance()
	if !ok {
		notifyAlreadyRunning()
		return
	}
	if lock != nil {
		defer lock.Release()
	}

	// We hold the lock, so the instance that installed this binary has exited and
	// its image is no longer locked by the OS. Delete it now: a running Windows
	// executable cannot delete itself, which is why this is the successor's job
	// and not the updater's. Best-effort by design — see updater.CleanupBackup.
	if os.Getenv(updater.UpdatedEnvVar) != "" {
		updater.CleanupBackup()
	}

	// Now that we own the instance, set up file logging — the GUI build has no
	// console, so a log file is the only diagnostic trail. The panic-recover defer
	// is registered AFTER Close so, on a panic unwind, recovery logs first and the
	// file is still open.
	//
	// OPT-IN (Settings > "Save diagnostic logs", mirrored by config.DebugLogging).
	// A default install writes the user's own state and nothing else; the log is
	// there to make a bug report diagnosable, so it is switched on by the person
	// who wants to file one. Without it slog keeps its default stderr handler,
	// which a -H=windowsgui build simply discards.
	//
	// Loading the config here also syncs the process-wide mirror every later
	// reader depends on — the window title, the feed capture, the alert trail.
	if _, err := config.Load(); err == nil && config.DebugLogging() {
		if dir, err := config.Dir(); err == nil {
			_ = applog.Enable(dir)
			defer applog.Disable()
		}
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic during startup/run", "value", r, "stack", string(debug.Stack()))
			panic(r)
		}
	}()

	slog.Info("acs starting", "version", ui.Version)
	ui.New().Run()
	slog.Info("acs exiting cleanly")
}

// acquireSingleInstance attempts to take the single-instance lock. It returns
// ok=false only when another live instance already holds it; any other problem
// (e.g. an unreadable config dir) fails open so the user is never blocked from
// launching by an infrastructure error.
func acquireSingleInstance() (*singleton.Lock, bool) {
	dir, err := config.Dir()
	if err != nil {
		return nil, true
	}
	lockPath := filepath.Join(dir, singleton.LockFileName)

	// A relaunch (UI-watchdog recovery or the tray Restart) starts this successor
	// while the predecessor is still exiting and briefly still holds the lock.
	// Wait for it to release rather than immediately reporting "already running"
	// and quitting — which would leave the user with no window at all.
	if os.Getenv(ui.RelaunchEnvVar) != "" {
		deadline := time.Now().Add(20 * time.Second)
		for {
			lock, ok, err := singleton.Acquire(lockPath)
			if err != nil {
				return nil, true // fail open on I/O error
			}
			if ok {
				return lock, true
			}
			if time.Now().After(deadline) {
				return nil, false // predecessor never released; treat as already running
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	lock, ok, err := singleton.Acquire(lockPath)
	if err != nil {
		return nil, true // fail open on I/O error
	}
	return lock, ok
}
