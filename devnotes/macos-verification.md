# macOS verification guide

**Audience:** whoever is validating this branch on a Mac.
**Goal:** confirm the app builds, its tests pass, and the platform-specific
paths that are *only* exercisable on macOS actually work.

Everything is on `main`. Windows is the primary development platform, so a large
amount of this code has **never run on macOS** — that is exactly what this pass
is for, and it blocks the first release (see §6).

---

## 1. Prerequisites

```bash
xcode-select --install          # Clang + headers; Fyne needs cgo
brew install go                 # or download from go.dev
go version                      # must be >= 1.24
```

Fyne draws through OpenGL and cgo, so `CGO_ENABLED=0` will **not** build the
GUI. There is no cross-compilation shortcut: each platform must build natively.

```bash
git clone https://github.com/AlgoraLabs-AI/AI-Cloud-Status.git
cd AI-Cloud-Status
go mod download
```

## 2. Build and test

```bash
go build ./...
go vet ./...
go test ./...
```

All three must be clean. On Windows they are; if anything fails here it is a
genuine macOS-specific finding — **please report it rather than working around
it.**

Then build the app itself and run it:

```bash
CGO_ENABLED=1 go build -o acs .
./acs
```

## 3. Run the full test suite against real captured data

The repo ships one real feed capture per provider under
`internal/providers/testdata/captures/`, and `TestCommittedCapturesStillParse`
runs against them automatically. If you also have a populated feed archive, the
deeper sweep is env-gated:

```bash
ACS_FEED_SAMPLES="$HOME/Library/Application Support/AI-Cloud-Status/feed-samples" \
  go test ./internal/providers/ -run Replay -v
```

It skips cleanly when the directory does not exist. On the Windows dev machine it
replays 449 real captures.

### 3.1 The live check — run this one

```bash
ACS_LIVE=1 go test ./internal/providers/ -run TestLiveFeedsParse -v
```

It fetches every non-optional provider's real status endpoint and fails on a
parse error, logging one line per provider. On Windows: 12/12 parse.

Run it because the offline corpus has a structural blind spot. `FeedCapture`
archives a payload only when a feed reads NON-OPERATIONAL, so **no healthy
payload can ever appear in it** — and a guard that rejected Azure's healthy shape
passed the entire offline sweep and then broke every healthy poll in production.
The live check is the only thing that would have caught it. If a provider fails
here on macOS but not on Windows, that is a TLS/proxy/DNS difference worth
knowing about before a release.

## 4. What actually needs a Mac to verify

These are the paths with `_other.go` / non-Windows implementations. They compile
for darwin (verified by cross-compilation) but have **never been executed** there.

### 4.1 Single-instance locking — `internal/singleton`

Recently rewritten. Ownership used to be decided by reading a PID from a
lockfile and asking whether that process was alive; it is now an `flock(2)`
exclusive lock held by the kernel on the open file descriptor
(`internal/singleton/flock_other.go`).

```bash
go test ./internal/singleton/ -v
```

Then by hand:

1. Launch `./acs`. Launch it a second time from another terminal.
   **Expect:** the second instance exits immediately saying it is already
   running. It must NOT open a second window.
2. Kill the first hard: `kill -9 $(pgrep acs)`.
3. Launch again. **Expect:** it starts normally.
   This is the case that used to lock the user out forever on Windows — the
   lockfile survives a `kill -9`, and the old code asked whether the recorded PID
   was alive. `flock` is released by the kernel, so there is nothing stale to
   reclaim. Confirm `~/Library/Application Support/AI-Cloud-Status/instance.lock`
   is still present but does not block the launch.
4. While one instance runs, `cat` that lockfile. **Expect:** it prints the PID.
   (On Windows the lock had to be moved to a high byte offset to keep the file
   readable; `flock` is whole-file and advisory, so reads are unaffected — worth
   confirming rather than assuming.)

Note `MutexName` is Windows-only; on macOS the lockfile is the sole guard
(`mutex_other.go` is a no-op). That makes this test **more** important here, not
less.

### 4.2 ICMP probing — `internal/monitor/ping_other.go`

macOS uses `pro-bing` rather than the Windows IP Helper API. Unprivileged ICMP
(`SOCK_DGRAM`) generally works on macOS without sudo, but confirm:

- The Connectivity rows for `1.1.1.1` and `8.8.8.8` show latency, not
  "unreachable".
- The footer bar does **not** show "ICMP unavailable without elevated
  privileges — using TCP:443 fallback probes". If it does, ICMP fell back and
  that is worth reporting with the value of `sysctl net.inet.ip.ttl` and whether
  you are on a VPN.

**Known limitation, do not treat as a bug unless it misbehaves badly:** DNS
resolution inside the probe path (`probing.NewPinger` → `Resolve()`) runs before
the context is consulted, so a *custom hostname* target can make a probe round
outrun its configured timeout while the resolver retries. Add a hostname target
(Settings → Connectivity → add e.g. `example.com`), then disable Wi-Fi, and note
roughly how long a round takes and whether quitting the app during that window
hangs. Numbers welcome.

### 4.3 Start-on-login — `internal/autostart`

There is no macOS implementation. **Expect:** the Settings toggle is disabled
with "Start on login is unavailable on this platform." Confirm it is disabled
rather than present-but-broken.

### 4.4 System tray + window lifecycle

- Closing the window keeps the app alive in the menu bar.
- The tray menu's first line reads `Status: …` and updates after the first poll.
- **Fyne may inject its own "Quit" item** when the app's tray label does not
  match its own localized string, which is resolved from the OS locale rather
  than the app's language setting. Switch the language (Settings → Language) to
  Italian or Korean, reopen the tray menu, and report whether a SECOND "Quit"
  appears at the bottom. If it does, quitting via that one skips the app's own
  save path and loses window size plus up to 60s of history. This is a known
  finding we have not fixed; a macOS confirmation would tell us whether it is
  Windows-only.

### 4.5 File permissions

Config, history and the log are written 0600 (they can contain a monitored URL's
credentials). Windows ignores POSIX modes, so macOS is the first place this is
observable:

```bash
ls -l ~/Library/Application\ Support/AI-Cloud-Status/
```

**Expect:** `-rw-------` on `config.json`, `history.json`, `acs.log`,
`instance.lock`.

### 4.6 A healthy feed is not the same shape as a broken one

Worth knowing before you interpret anything: **Azure's status feed is EMPTY when
everything is fine.** It lists only open incidents, so all-clear is a 593-byte
document with no items. xAI's feed is the opposite — a rolling history of ~105
entries that never empties, where an empty channel means something broke.

A guard that assumed both behaved like xAI shipped and made Azure read "Status
feed unavailable" on every poll. If you see a provider stuck on that message,
check whether its healthy payload is simply empty before assuming the endpoint
is down — and run §3.1.

### 4.7 Log rotation

`acs.log` is capped at 1 MiB and rotates to `acs.log.1` on write. Force it:

```bash
ACS_DEBUG=1 ./acs      # verbose; leave running a while
ls -l ~/Library/Application\ Support/AI-Cloud-Status/acs.log*
```

**Expect:** `acs.log` never exceeds ~1 MiB and `acs.log.1` appears once it has
rotated at least once.

## 5. Self-audit tool

GUI-free, so it runs anywhere:

```bash
go run ./cmd/alertaudit
echo "exit=$?"
```

**Expect:** exit 0 on a healthy machine. Providers with no captures are listed
as advisory ("no captures on record: healthy all window, or its feed never
loaded") and must NOT fail the run. A non-zero exit means an uncovered major
incident, a missing recovery, or a provider whose captures all fail to parse —
report the output verbatim if you get one.

## 6. Before the first release

This repo has never cut a release. `v1.0.0` is gated on this pass, because the
release workflow builds macOS and Linux binaries natively and would publish them
without anyone ever having run the app there.

The gate, concretely — all of these must be true:

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` clean on macOS
- [ ] §3.1 live check: every provider parses
- [ ] §4.1 single-instance lock, including the crash-then-relaunch case
- [ ] §4.2 ICMP works unprivileged, or the fallback note is at least accurate
- [ ] §4.5 files are `-rw-------`
- [ ] the app runs for a while without the window dying or the tray misbehaving

Tagging is what triggers the build (`git tag v1.0.0 && git push --tags`), and the
workflow now runs `go test ./...` on all three platforms BEFORE building — so a
macOS test failure stops the release rather than being discovered in a published
binary. That gate is only as good as the tests, which is why the hand-checks
above still matter.

## 7. Reporting back

For anything that fails, please include:

- `sw_vers` and `go version`
- `uname -m` (Apple Silicon vs Intel matters for the Fyne/GL path)
- the full command and its output
- for GUI issues, a screenshot and the tail of
  `~/Library/Application Support/AI-Cloud-Status/acs.log`

Most valuable results, in order:

1. `go test ./...` failing anywhere — the suite is green on Windows, so a
   failure is a real platform difference.
2. Section 4.1 step 3 — the crash-then-relaunch case.
3. Section 4.2 — whether ICMP works unprivileged.
4. Section 4.4 — the duplicate Quit item.

## 8. Context

`local/dev-notes/2026-08-01-repo-audit.md` holds the full audit this branch is
addressing (~45 findings). It is **not** committed — `local/` is gitignored by
convention — so ask if you want it; it explains why a given change exists and
which findings are still open.
