# devnotes

Working notes for people building and testing this repo during development.
Committed on purpose, unlike `local/`, which is gitignored and holds private
analysis.

| File | What it is |
|---|---|
| [macos-verification.md](macos-verification.md) | Build + test + hand-verification guide for macOS, focused on the paths that only a Mac can exercise. |
| [platform-coverage.md](platform-coverage.md) | Which platforms each part of the code has actually been RUN on, versus merely compiled for. |
| [2026-08-15-v1.0.0-release-and-soak.md](2026-08-15-v1.0.0-release-and-soak.md) | How v1.0.0 was cut, why the repo was deleted and recreated, and what the two memory soaks did and did not prove. |
| [data/](data/) | Raw measurement output kept alongside the notes that cite it. |

## Current state

The whole-repo audit is closed and merged to `main`. Every finding is fixed or
explicitly recorded as accepted; the suite is green across 15 packages, `go vet`
is clean, and both non-Windows targets cross-compile (everything except
`internal/ui`, which needs cgo and OpenGL and therefore cannot).

**`v1.0.0` is published** (2026-08-15) — Windows and macOS both hand-verified
first; see [the release note](2026-08-15-v1.0.0-release-and-soak.md). `main` is a
single commit: the repository was deleted and recreated rather than force-pushed,
because a force-push leaves the old objects publicly readable by SHA (measured,
not assumed).

**Linux is still the open platform.** Its GUI has never been compiled or run by
anyone; CI builds it and its tests pass, and that is all. The other open item
worth a session is a memory soak with the window VISIBLE — both soaks so far ran
with it hidden in the tray, which does not exercise the painting path the
renderer leak lives in.

Validated against production data, not only synthetic fixtures:

| Check | Result |
|---|---|
| Replay of the archived feed corpus | 449 captures, 0 parse failures |
| …restricted to non-operational captures | 411, 0 now reading as healthy |
| Live fetch of every non-optional provider (`ACS_LIVE=1`) | 12/12 parse |
| `cmd/alertaudit` against the real alert log | exit 0 |

### One lesson worth carrying

A guard that rejected Azure's healthy payload passed the ENTIRE offline sweep
and then broke every healthy poll in production. The corpus could not have
caught it: `FeedCapture` archives a body only when a feed reads
non-operational, so **a healthy payload can never appear in it**. Replaying 449
captures said nothing about the all-clear path.

The live check (`ACS_LIVE=1`) exists because of that, and it is the one test to
run first on a new platform.

## Why this exists

Windows is the primary development platform. Fyne needs cgo and OpenGL, so
nothing cross-compiles for the GUI — every platform must build natively, and a
change that is green on Windows has not been executed anywhere else. These notes
say exactly what is unverified so a second machine can close the gap instead of
re-deriving it.

## What is deliberately NOT here

The full repo audit (`local/dev-notes/`) stays out of the repository. It cites
commit authorship while diagnosing a git-history issue, which means it carries a
personal email address that should not land in a public repo. Ask the maintainer
directly if you need it.
