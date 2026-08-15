package ui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/updater"
)

// updateCheckDelay holds the first update check back until after startup has
// settled. Launch is the busiest moment in this app's life — splash, first
// poll, history restore — and an update dialog racing the splash for the
// foreground is how an app gets closed before it is ever seen.
const updateCheckDelay = 30 * time.Second

// updateCheckEvery re-checks for a release on a long cadence, so an instance
// left running for weeks (the intended way to use a tray monitor) still learns
// about a release without being restarted.
const updateCheckEvery = 6 * time.Hour

// updateCheckTimeout bounds a single check. Reaching GitHub is optional; hanging
// on it is not.
const updateCheckTimeout = 30 * time.Second

// runUpdateChecker polls for a newer release in the background for the life of
// the app and offers it once. It is deliberately quiet: a failed check is logged
// and forgotten, never surfaced, because "I could not reach GitHub" is not news
// to someone running a connectivity monitor.
func (c *Controller) runUpdateChecker(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(updateCheckDelay):
	}
	for {
		c.checkForUpdate(ctx, false)
		select {
		case <-ctx.Done():
			return
		case <-time.After(updateCheckEvery):
		}
	}
}

// revealForAnswer brings the window back before a MANUAL check's answer is
// shown. A Fyne dialog is a canvas overlay on its parent window, so an answer
// parented to a window hidden in the tray is drawn where nobody can see it.
//
// This is reachable on macOS and not on Windows: there the menu bar lives inside
// the window and is gone while it is hidden, but a macOS app keeps its menu bar
// with no window at all — so "Check for updates" is invocable in exactly the
// state where its answer would be invisible, and the menu item reads as dead.
// Must run on the Fyne thread.
func (c *Controller) revealForAnswer() {
	c.mu.Lock()
	hidden := c.windowHidden
	c.mu.Unlock()
	if hidden {
		c.showFromTray()
	}
}

// checkForUpdate runs one check. When manual is true it was asked for from the
// menu, so the "you are up to date" and failure answers are shown — silence
// would read as the menu item doing nothing.
func (c *Controller) checkForUpdate(ctx context.Context, manual bool) {
	cctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()

	rel, err := updater.Check(cctx, Version)
	if err != nil {
		slog.Info("update check failed", "err", err)
		if manual {
			fyne.Do(func() {
				c.revealForAnswer()
				dialog.ShowError(err, c.window)
			})
		}
		return
	}
	if rel == nil {
		if manual {
			fyne.Do(func() {
				c.revealForAnswer()
				dialog.ShowInformation(i18n.T().CheckForUpdates,
					fmt.Sprintf(i18n.T().UpdateUpToDate, Version), c.window)
			})
		}
		return
	}
	// Offer a given version once per run. Without this the 6-hourly re-check
	// would reopen a dialog the user already dismissed with "Later".
	c.mu.Lock()
	already := c.offeredUpdate == rel.Version
	c.offeredUpdate = rel.Version
	c.mu.Unlock()
	if already && !manual {
		return
	}
	fyne.Do(func() { c.showUpdateDialog(rel) })
}

// showUpdateDialog presents the available release. Must run on the Fyne thread.
func (c *Controller) showUpdateDialog(rel *updater.Release) {
	t := i18n.T()
	body := container.NewVBox(
		widget.NewLabel(fmt.Sprintf(t.UpdateAvailableMsg, rel.Version, Version)),
	)
	if notes := trimNotes(rel.Notes); notes != "" {
		l := widget.NewLabel(notes)
		l.Wrapping = fyne.TextWrapWord
		body.Add(widget.NewSeparator())
		body.Add(container.NewVScroll(l))
	}
	// The page URL comes from the release API and ends up at the OS protocol
	// handler, so it goes through the updater's transport rule before it is made
	// clickable — see updater.SafePageURL.
	if u, ok := updater.SafePageURL(rel.PageURL); ok {
		body.Add(widget.NewHyperlink(t.UpdateOpenPage, u))
	}

	// Where the app cannot install for itself, say so plainly instead of
	// offering a button that would half-work. See updater.SelfReplaceSupported.
	if !updater.SelfReplaceSupported() || rel.AssetURL == "" || rel.Sum == "" {
		note := widget.NewLabel(t.UpdateManualNote)
		note.Wrapping = fyne.TextWrapWord
		body.Add(note)
		d := dialog.NewCustom(t.UpdateAvailable, t.Close, body, c.window)
		d.Resize(fyne.NewSize(460, 320))
		d.Show()
		return
	}

	d := dialog.NewCustomConfirm(t.UpdateAvailable, t.UpdateNow, t.UpdateLater, body,
		func(ok bool) {
			if ok {
				c.applyUpdate(rel)
			}
		}, c.window)
	d.Resize(fyne.NewSize(460, 320))
	d.Show()
}

// notesMax caps the release body shown in the dialog. Release notes are
// generated from commit history and can run to hundreds of lines; the full text
// is one click away on the release page.
const notesMax = 1200

func trimNotes(s string) string {
	if r := []rune(s); len(r) > notesMax {
		return string(r[:notesMax]) + "…"
	}
	return s
}

// applyUpdate downloads, verifies, installs and restarts into the new version.
// Must be called from the Fyne thread; the work itself runs off it.
func (c *Controller) applyUpdate(rel *updater.Release) {
	t := i18n.T()
	bar := widget.NewProgressBar()
	status := widget.NewLabel(fmt.Sprintf(t.UpdateDownloading, rel.Version))
	prog := dialog.NewCustomWithoutButtons(t.UpdateAvailable,
		container.NewVBox(status, bar), c.window)
	prog.Resize(fyne.NewSize(420, 160))
	prog.Show()

	go func() {
		// Download NEXT TO the executable, not into a temp dir: the install is a
		// rename, and a rename across volumes fails. %TEMP% is routinely on a
		// different drive from a portable install on a USB stick or a D: partition.
		self, err := updater.SelfPath()
		if err != nil {
			c.finishUpdate(prog, err)
			return
		}
		dir := filepath.Dir(self)

		path, err := updater.Download(context.Background(), rel, dir, func(done, total int64) {
			if total <= 0 {
				return
			}
			frac := float64(done) / float64(total)
			fyne.Do(func() { bar.SetValue(frac) })
		})
		if err != nil {
			c.finishUpdate(prog, err)
			return
		}

		fyne.Do(func() { status.SetText(t.UpdateInstalling) })

		if _, err := updater.Swap(path, rel.Sum); err != nil {
			os.Remove(path) // the download is useless now; the old binary is back
			c.finishUpdate(prog, err)
			return
		}

		// Record what just replaced this binary, BEFORE handing over — after the
		// exec below this process is gone. See config.UpdateRecord: this is the
		// only trace an update leaves on a public build, and it is what lets the
		// user answer "was my copy replaced, when, and with what".
		c.mu.Lock()
		c.cfg.LastUpdate = config.UpdateRecord{
			Version: rel.Version, SHA256: rel.Sum, At: time.Now().Unix(),
		}
		saved := c.cfg.Clone()
		c.mu.Unlock()
		if err := config.Save(saved); err != nil {
			slog.Warn("could not record the applied update", "err", err)
		}

		// Hand over. The successor inherits the relaunch marker, so it waits for
		// this process to release the single-instance lock instead of seeing a
		// live predecessor and exiting (see acquireSingleInstance in main.go), and
		// it deletes the .old backup once we are gone.
		if err := c.restartInto(self); err != nil {
			c.finishUpdate(prog, err)
			return
		}
	}()
}

// finishUpdate closes the progress dialog and reports a failure. The message is
// explicit that nothing was lost, because "update failed" on a monitoring tool
// otherwise reads as "and now it is broken".
func (c *Controller) finishUpdate(prog dialog.Dialog, err error) {
	slog.Error("update failed", "err", err)
	fyne.Do(func() {
		prog.Hide()
		t := i18n.T()
		msg := widget.NewLabel(fmt.Sprintf(t.UpdateFailedMsg, err))
		msg.Wrapping = fyne.TextWrapWord
		dialog.ShowCustom(t.UpdateFailed, t.Close, msg, c.window)
	})
}

// restartInto starts the freshly installed binary and exits this process. It is
// doRelaunch's sibling and shares its handoff contract; it is separate because
// this one must not go through the relaunch-generation budget, which exists to
// stop a crash-looping build from relaunching forever and has nothing to say
// about a user-approved update.
func (c *Controller) restartInto(exe string) error {
	child := exec.Command(exe, os.Args[1:]...)
	child.Env = append(withRelaunchEnv(os.Environ(), 1), updater.UpdatedEnvVar+"=1")
	child.SysProcAttr = detachedSysProcAttr()
	if err := child.Start(); err != nil {
		return err
	}
	c.saveHistory()
	slog.Info("update installed; restarting", "child_pid", child.Process.Pid, "exe", exe)
	os.Exit(0)
	return nil // unreachable
}
