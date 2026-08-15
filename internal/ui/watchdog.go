package ui

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/systray"
)

// RelaunchEnvVar carries the relaunch "generation" from a departing instance to
// its replacement. Its mere presence tells the successor to wait for the
// single-instance lock (the predecessor is still exiting); its integer value
// caps runaway auto-relaunch loops (see maxRelaunchGen).
const RelaunchEnvVar = "ACS_RELAUNCH"

const (
	// watchdogProbeEvery is how often a heartbeat is posted onto the Fyne thread.
	watchdogProbeEvery = 5 * time.Second
	// watchdogStallAfter is how long the heartbeat may go unexecuted before the
	// Fyne event loop is judged wedged. Generous, so a heavy refresh or a GC pause
	// never triggers a false relaunch.
	watchdogStallAfter = 40 * time.Second
	// watchdogStartupGrace suppresses judgment until the splash + first paint have
	// settled (the loop isn't draining beats yet during startup).
	watchdogStartupGrace = 60 * time.Second
	// healthyResetDur is the uptime past which a stall counts as a genuine rare
	// freeze rather than part of an earlier relaunch chain, earning a fresh budget.
	healthyResetDur = 5 * time.Minute
	// maxRelaunchGen caps consecutive auto-relaunches within one unhealthy chain so
	// an app that wedges immediately on every launch can't relaunch forever.
	maxRelaunchGen = 4
	// trayMenuGraceMax bounds how long an open tray menu may excuse a silent
	// heartbeat. The signal below cannot tell "still open" from "the loop wedged
	// while it was open", so past this the wedge becomes the likelier reading and
	// the watchdog goes back to doing its job.
	trayMenuGraceMax = 10 * time.Minute
)

// runTrayOpenWatcher records when the system-tray menu is opened.
//
// systray signals TrayOpenedCh from the OS callback that fires as the menu is
// about to appear (menuWillOpen: on macOS, the equivalent on Linux). The channel
// is declared on every platform but only ever written on those two, and nothing
// in Fyne consumes it — so this receiver is free to take it, and on Windows it
// simply never fires.
func (c *Controller) runTrayOpenWatcher(ctx context.Context) {
	defer logPanic("runTrayOpenWatcher")
	for {
		select {
		case <-ctx.Done():
			return
		case <-systray.TrayOpenedCh:
			c.lastTrayOpen.Store(time.Now().UnixNano())
		}
	}
}

// trayMenuHoldsTheBeat reports whether an OPEN tray menu, rather than a wedged
// event loop, is why the heartbeat has gone quiet.
//
// On macOS an open NSMenu runs a nested modal run loop that does not drain the
// fyne.Do queue, so an idle-but-healthy app looks exactly like a stalled one: on
// 2026-08-15 holding the menu open for 41s made this app relaunch itself, twice,
// reproducibly. Linux has the same shape. Windows does not, which is why 40s
// looked like a generous bound when it was chosen.
//
// The tell is that the beats are QUEUED, not lost. The moment the menu closes
// they all flush and lastUIBeat jumps past the open. So an open recorded AFTER
// the last drained beat means the menu is still up right now — and that stops
// being true by itself, with no close event to listen for.
func (c *Controller) trayMenuHoldsTheBeat() bool {
	opened := c.lastTrayOpen.Load()
	if opened == 0 {
		return false // no menu has ever been opened (always so on Windows)
	}
	if time.Since(time.Unix(0, opened)) > trayMenuGraceMax {
		return false
	}
	return opened > c.lastUIBeat.Load()
}

// runUIWatchdog detects a wedged Fyne event loop and recovers by relaunching.
//
// It posts a heartbeat onto the Fyne thread every watchdogProbeEvery and watches
// c.lastUIBeat advance. If the beat stops draining for watchdogStallAfter, the
// loop is stalled (a deadlock or a blocked fyne.Do callback) and the process is
// relaunched.
//
// NOTE ON SCOPE: this catches a stalled *loop*. It does NOT catch a live loop
// whose OpenGL canvas has stopped presenting (a lost GL context after sleep or a
// GPU driver reset) — there the heartbeat keeps draining and this never fires,
// by design, so it never false-relaunches. The heartbeat log is therefore also
// the diagnostic: if the window is blank yet the heartbeat was still advancing,
// the fault is the GL surface, not the loop — and the tray Restart is the
// recovery for that case.
func (c *Controller) runUIWatchdog(ctx context.Context) {
	defer logPanic("runUIWatchdog")
	lastTick := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchdogProbeEvery):
		}
		// Post the heartbeat from a throwaway goroutine so this loop can never
		// block on the fyne.Do queue itself.
		go fyne.Do(func() { c.lastUIBeat.Store(time.Now().UnixNano()) })

		now := time.Now()
		slept := now.Sub(lastTick)
		lastTick = now

		if time.Since(c.startedAt) < watchdogStartupGrace {
			continue
		}
		if c.resumedFromSuspend(slept) {
			continue
		}
		if c.trayMenuHoldsTheBeat() {
			continue
		}
		stale := time.Since(time.Unix(0, c.lastUIBeat.Load()))
		if stale >= watchdogStallAfter {
			if !c.autoRelaunchOnStall(stale) {
				return
			}
			// The relaunch failed but may succeed later. Wait out a full stall
			// window before trying again so a persistent failure logs once per
			// window instead of once per probe.
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchdogStallAfter):
			}
		}
	}
}

// resumedFromSuspend reports whether the machine was ASLEEP rather than the
// event loop being wedged, and prepares the app to carry on if so.
//
// The two are indistinguishable from the heartbeat alone, and confusing them is
// not theoretical: on 2026-08-02 this app logged "Fyne event loop STALLED,
// stale=43m32s" and relaunched itself 0.8s after the machine resumed. The
// Windows event log showed sleep at 10:31:00 and resume at 11:14:30 — the loop
// had never stalled at all. Every laptop lid-close longer than watchdogStallAfter
// killed and restarted the app on wake, silently, under an ERROR line naming a
// fault that never happened.
//
// The tell is a clock mismatch the watchdog can read directly. lastUIBeat is a
// WALL-clock instant, so its age includes suspended time; this loop's own ticker
// is MONOTONIC, which on Windows does not advance while the machine is in S3. So
// if far more wall time has passed than this loop actually waited, the whole
// PROCESS was frozen — nothing was executing, the event loop least of all.
//
// slept is the wall-clock time since the previous iteration.
func (c *Controller) resumedFromSuspend(slept time.Duration) bool {
	if slept < watchdogStallAfter {
		return false
	}
	slog.Info("watchdog: machine resumed from suspend — not a stalled loop",
		"gap", slept.String())
	// The heartbeat's age measures the sleep, not a wedged loop, so it is not
	// evidence of anything. A loop that genuinely IS stalled stays stalled and is
	// caught one full stall window later.
	c.lastUIBeat.Store(time.Now().UnixNano())
	// The rolling loss windows are full of probes that failed while the radio was
	// powering down for suspend. They describe a machine going to sleep, not a
	// lossy link, and leaving them in would fire a "you're losing packets" alert
	// seconds after the user opens the lid — the same reasoning ResetLoss already
	// applies when a total blackout ends.
	if eng := c.currentEngine(); eng != nil {
		eng.ResetLoss()
	}
	return true
}

// autoRelaunchOnStall relaunches after the watchdog observes a stalled loop,
// subject to the anti-loop budget. It reports whether the caller should keep
// watching (see autoRelaunch).
func (c *Controller) autoRelaunchOnStall(stale time.Duration) bool {
	return c.autoRelaunch("UI watchdog: Fyne event loop STALLED (heartbeat not draining)", "stale", stale.String())
}

// autoRelaunch relaunches the process once for the given reason, subject to the
// anti-loop budget shared by every automatic recovery path (loop stall,
// dead canvas).
//
// It returns whether the watchdog should KEEP WATCHING. A successful relaunch
// never returns at all (doRelaunch exits the process), so any return means the
// attempt failed — and the failures differ: a transient one (the exe is locked
// by antivirus, its path briefly unreachable, another recovery path already
// running) deserves another try, while an exhausted budget is a deliberate,
// permanent decision.
//
// The distinction matters because both watchdogs used to `return` unconditionally
// after calling this. Every failure path here already sets relaunching back to
// false — signalling "another attempt is allowed" — but the goroutine had
// already died, so no attempt could ever come. One antivirus-locked exe and the
// window stayed white for the rest of the process lifetime, with the recovery
// budget never even spent.
func (c *Controller) autoRelaunch(reason string, attrs ...any) bool {
	if !c.relaunching.CompareAndSwap(false, true) {
		return true // another recovery path owns it; keep watching
	}
	gen := inheritedRelaunchGen()
	// Ran healthily for a good while before failing → a genuine rare fault, not a
	// relaunch loop: grant a fresh budget.
	if time.Since(c.startedAt) > healthyResetDur {
		gen = 0
	}
	if gen >= maxRelaunchGen {
		slog.Error(reason+" — relaunch budget exhausted; not auto-relaunching, use the tray Restart",
			append(attrs, "gen", gen)...)
		c.relaunching.Store(false)
		return false // deliberate and permanent — stop watching
	}
	slog.Error(reason+" — relaunching", append(attrs, "gen", gen)...)
	c.doRelaunch(gen + 1)
	return true // only reached when the relaunch failed; it may work next time
}

// restart relaunches the app on explicit user request from the tray. It works
// even when the window is blank, because the tray menu is OS-drawn. A user
// restart begins a fresh relaunch chain (gen 1), independent of the auto-relaunch
// budget.
func (c *Controller) restart() {
	if !c.relaunching.CompareAndSwap(false, true) {
		return
	}
	slog.Info("restart requested from tray")
	c.doRelaunch(1)
}

// doRelaunch starts a replacement instance and exits this one. The successor
// blocks on the single-instance lock until this process exits and frees the OS
// mutex; because a stalled Fyne loop can't run app.Quit(), this exits hard
// (after a best-effort history save) rather than unwinding cleanly.
func (c *Controller) doRelaunch(gen int) {
	exe, err := os.Executable()
	if err != nil {
		slog.Error("relaunch: cannot resolve executable — aborting", "err", err)
		c.relaunching.Store(false)
		return
	}
	child := exec.Command(exe, os.Args[1:]...)
	child.Env = withRelaunchEnv(os.Environ(), gen)
	child.SysProcAttr = detachedSysProcAttr()
	if err := child.Start(); err != nil {
		slog.Error("relaunch: failed to start replacement instance — aborting", "err", err)
		c.relaunching.Store(false)
		return
	}
	c.saveHistory()
	slog.Info("relaunch: replacement started; exiting", "child_pid", child.Process.Pid, "gen", gen)
	os.Exit(0)
}

// inheritedRelaunchGen reads the relaunch generation this instance was started
// with (0 for a normal launch).
func inheritedRelaunchGen() int {
	n, err := strconv.Atoi(os.Getenv(RelaunchEnvVar))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// withRelaunchEnv returns env with any existing relaunch marker removed and the
// generation gen set, so a chain of relaunches carries a single, monotonic
// counter rather than accumulating duplicates.
func withRelaunchEnv(env []string, gen int) []string {
	out := make([]string, 0, len(env)+1)
	prefix := RelaunchEnvVar + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+strconv.Itoa(gen))
}
