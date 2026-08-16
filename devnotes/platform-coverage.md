# Platform coverage

What has actually been **run** where, versus what has only been **compiled**.
Kept honest deliberately: "it compiles" is not "it works", and the gap between
the two is where the platform bugs live.

Legend: **RUN** = executed with its tests passing · **BUILT** = compiles for that
target, never executed · **—** = not applicable (no code for that platform).

macOS was hand-verified on **2026-08-15**, macOS 26.5.2 (build 25F84), Apple
Silicon (arm64), Go 1.26.5, Apple clang 21.0.0. Linux is still untouched.

| Area | Windows | macOS | Linux |
|---|---|---|---|
| `internal/providers` (feed parsers) | RUN | RUN | BUILT |
| `internal/config`, `internal/history`, `internal/atomicfile` | RUN | RUN | BUILT |
| `internal/audit`, `cmd/alertaudit` | RUN | RUN | BUILT |
| `internal/alertlog`, `internal/applog` | RUN | RUN | BUILT |
| `internal/monitor` (loss/offline/outage logic) | RUN | RUN | BUILT |
| `internal/monitor` ICMP probe | RUN (`ping_windows.go`, IP Helper API) | RUN (`ping_other.go`, pro-bing) | BUILT (same) |
| `internal/singleton` lock | RUN (`flock_windows.go`, LockFileEx) | RUN (`flock_other.go`, flock(2)) | BUILT (same) |
| `internal/singleton` OS mutex | RUN (named mutex) | — (no-op) | — (no-op) |
| `internal/autostart` | RUN (HKCU Run key) | — (unimplemented, degrades cleanly) | — (unimplemented) |
| `internal/ui` (Fyne GUI) | RUN | RUN | **not built** |
| UI-watchdog relaunch | RUN (`relaunch_windows.go`) | RUN (`relaunch_other.go`, `Setsid`) | BUILT (same) |
| Dead-canvas watchdog | RUN (`deadcanvas_windows.go`) | — | — |
| File permissions (0600) | not observable | RUN | **unverified** |
| Release artifact shape | `.exe` | `.app` bundle (`scripts/package-darwin.sh`) | bare binary in a tar.gz |

### What the macOS pass actually covered

Green: `go build`, `go vet`, `go test`, `go test -race` all clean — including
`internal/ui`, which had never been compiled on a Mac. `ACS_LIVE=1` parsed 12/12
provider feeds. Unprivileged ICMP works with no `sudo` (13 ms to `1.1.1.1`) with
VPN tunnels up, so the TCP:443 fallback never engaged. Every file in the data
folder is `-rw-------` and the folder itself `drwx------`. The single-instance
`flock(2)` was exercised across real processes: a second launch exits at once
without a window, and — the case that used to lock a Windows user out forever —
`kill -9` on the holder leaves the lockfile behind but the next launch takes it
and rewrites the PID. Diagnostic logging is genuinely off by default and applies
live when ticked. The update check answers HTTP 404 (no release exists yet)
without crashing or offering anything.

The tray menu was reached too (§4.4): right-click gives
`Status: … · Show · Refresh now · Settings… · Restart · Quit`, the status line
updates after the first poll, and there is **exactly one Quit** — in English and
with the app switched to Italian. `quitMenuItem`'s `IsQuit` flag does suppress
Fyne's injected duplicate here, so that finding does not reproduce on macOS.

Driving it needed real `CGEvent`s (a 40-line C helper posting
`CGEventCreateMouseEvent` / `CGEventCreateScrollWheelEvent`). Neither System
Events' `click`, nor `AXPress`, nor `click at` reaches a Fyne canvas or an
`NSStatusItem`; the accessibility tree exposes only the window's three title-bar
buttons, because Fyne paints its widgets rather than making them AX objects.
Anyone repeating this pass should start there instead of fighting AppleScript.

### The macOS-only defect this pass found — and fixed

**Holding the tray menu open for more than 40 s made the app relaunch itself.**
Reproduced deterministically twice before the fix:

```
level=ERROR msg="UI watchdog: Fyne event loop STALLED (heartbeat not draining) — relaunching" stale=40.008074s gen=0
level=INFO  msg="relaunch: replacement started; exiting" child_pid=18158 gen=1
```

`runUIWatchdog` posts its heartbeat with `fyne.Do` and calls a stall at
`watchdogStallAfter` (40 s). An open `NSMenu` on macOS runs a nested modal run
loop that does not drain that queue, so an idle-but-healthy app looked wedged.
Windows' tray menu does not starve the loop the same way, which is why 40 s
looked generous when it was chosen.

The fix is `trayMenuHoldsTheBeat`, and the signal it needs already existed:
`systray.TrayOpenedCh` is written from the OS callback that fires as the menu
appears (`menuWillOpen:` on macOS, its equivalent on Linux). It is declared on
every platform, written on only those two, and nothing in Fyne consumes it.

What makes it self-releasing is that the beats are QUEUED, not lost: the instant
the menu closes they all flush and `lastUIBeat` jumps past the recorded open. So
"the open is newer than the last drained beat" means the menu is up *right now*,
and stops being true on its own with no close event to listen for. It is bounded
at `trayMenuGraceMax` anyway, because the signal never says "closed" and a loop
that wedged *while* the menu was up would otherwise be excused forever.

Verified against the same reproduction that broke it: menu held open 60 s, same
PID afterwards, zero watchdog lines. Windows is untouched — nothing ever writes
that channel there, so the guard is inert.

The episode had one useful side effect: it exercised `relaunch_other.go`'s
`Setsid` path, which was BUILT and is now RUN. The replacement came up cleanly
with `launchd` as its parent, and no data was lost — `open-outages.json` was
written in the same second as the relaunch.

A related bug that is **not** macOS-specific and is NOT fixed: switching language
re-translates the whole window live but leaves the tray menu in the old language,
because `updateTray` rewrites only `Items[0]` and `setupTray` is never called
again. Fyne's own dialog buttons are a second case of the same shape — they
resolve from the OS locale, so a Spanish-locale machine shows "No / Si" on a
confirm dialog in an app set to English.

Still NOT verified on macOS, and worth a second pass:

- **A soak with the window VISIBLE.** Two soaks have now run — 62 minutes on
  macOS, 45 on Windows — and both came out flat: macOS rose 13.5 MB in 20
  minutes then held inside a 1.3 MB band, Windows actually *fell* 234 MB → 212 MB
  with threads pinned at 29 and handles flat at ~756. Neither shows a leak.

  But **both ran with the window hidden to the tray**, and the leak these were
  chasing is the renderer/texture one described in `rowwidget.go` — the one the
  per-row widget cache was built to fix — which is fed by PAINTING. `refreshLocked`
  has no visibility guard, so the widget-tree half was exercised; the painting
  half was not. What is established is therefore the narrow claim: *no leak in
  the poll / update / log / capture path while idle in the tray*. Not "no leak".
  The soak that answers the real question keeps the window visible and in the
  foreground; instrument it to record window visibility per sample so one run
  captures both phases and the slopes can be compared on the same process.
- **`acs.log` rotation in the wild** — and it is now clear this can never be a
  verification-session item. Measured growth is **924 B/min**, so reaching the
  8 MiB ceiling takes **6.2 days**. Rotation is covered by unit tests only,
  including the platform-sensitive part (renaming a file open for append). This
  is closed as "not observable by design" rather than left as pending work.
- **"Delete diagnostic data" end to end.** `TestDeleteDiagnosticsNeverTouchesUserState`
  passes here (it does not skip — the redirected `HOME` resolves), but the button
  itself has not been clicked on a Mac.
- **Intel Macs.** Apple Silicon only.

## What changed recently, and therefore what is worth probing

Everything below landed in one sweep and is green on Windows only. Ordered by
what a macOS failure would cost.

| Change | Why a Mac could disagree |
|---|---|
| Feed-parser guards that REJECT input | The offline corpus cannot contain a healthy payload (see below), so these are the least-validated changes in the sweep. One already shipped broken. Run `ACS_LIVE=1` first. |
| Single-instance lock rewritten to `flock(2)` | Entirely different syscall from the Windows path. Windows needed the lock moved off byte 0 to keep the file readable; `flock` is whole-file and advisory, so it should not — worth confirming, not assuming. |
| `internal/atomicfile` now backs config + history | The rename retry exists for Windows sharing violations. On macOS it should be a no-op fast path; if writes are slow or retrying, that is new. |
| Files written 0600 | Windows ignores POSIX modes entirely, so macOS is the FIRST place this is observable at all. |
| `acs.log` rotates on write | Rotation renames a file that is open for append — semantics differ between platforms. |
| Fyne's injected Quit suppressed via `IsQuit` | Fyne resolves its own "Quit" string from the OS locale; the mismatch that triggers injection may behave differently on macOS. |
| ICMP via pro-bing | Windows uses the IP Helper API instead. Unprivileged ICMP is the whole question. |

## How the BUILT column was established

```bash
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./internal/... ./cmd/...
GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build ./internal/... ./cmd/...
```

Both clean, excluding `internal/ui`. That package needs cgo and OpenGL, so it
cannot be cross-compiled at all — its non-Windows behaviour is **entirely
unverified** and can only be closed by building on the target.

## The blind spot that already cost once

`FeedCapture` archives a feed body only when the reading is NON-OPERATIONAL.
Every one of the 449 archived captures is therefore a moment something was
wrong, and **no healthy payload can ever be in that corpus**. A parser guard
that rejected Azure's all-clear shape — an empty channel, which is exactly what
healthy looks like on that feed — replayed cleanly against all 449 and then made
Azure read "Status feed unavailable" on every poll.

Two things close it, and both should be run on any new platform:

- `ACS_LIVE=1 go test ./internal/providers/ -run TestLiveFeedsParse -v` — fetches
  every non-optional provider's real endpoint.
- `internal/providers/testdata/azure_healthy_empty.xml` — the live all-clear body
  captured verbatim, asserted to parse as SevNone.

Note the two RSS feeds have OPPOSITE semantics and must not be treated alike:
Azure lists only open incidents (empty = healthy), xAI is a rolling history of
~105 entries (empty = broken).

## Highest-value gaps

1. **`internal/ui` on macOS / Linux.** Never compiled there. Everything from
   window lifecycle to tray behaviour to the alert popup is unknown.
2. **`flock_other.go`.** Written recently to replace PID-based liveness. The
   Windows half is tested; the Unix half has only ever been compiled. It is the
   difference between "a crash locks you out forever" and "a crash is harmless",
   so it is worth exercising by hand — see `macos-verification.md` §4.1.
3. **0600 permissions.** Windows ignores POSIX modes, so no test on the primary
   platform can observe them.
4. **Unprivileged ICMP.** The Windows path uses a completely different API. On
   macOS/Linux a failure silently degrades to TCP:443 probes and the footer
   claims a *privilege* problem — which may be a misdiagnosis, since the same
   fallback fires for DNS failures and plain unreachability.

## Note on CI

`.github/workflows/release.yml` builds all three platforms natively, but it only
fires on a `v*` tag or a manual dispatch — it is not a per-push gate, and it runs
`go build`, **not** `go test`. So a green CI run does not mean the suite passes
on macOS or Linux. That is why this document exists.
