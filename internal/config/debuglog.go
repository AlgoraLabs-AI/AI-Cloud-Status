package config

import (
	"os"
	"sync/atomic"
)

// DebugEnv forces debug logging on regardless of the saved setting. It is the
// escape hatch for the one case the Settings toggle cannot serve: a build that
// fails before the window exists, where there is no Settings dialog to open.
// It is NOT the intended way in — that is the checkbox.
const DebugEnv = "ACS_DEBUG"

// debugLogging mirrors Config.DebugLogging for readers that have no Config in
// hand. Three of them matter: the log writer runs before the UI exists, the
// window-title helper is a package function with no controller, and the feed
// capture and alert trail are written from poll goroutines. An atomic bool is
// the honest shape — the Settings checkbox flips it on the UI thread while those
// goroutines read it.
var debugLogging atomic.Bool

// DebugLogging reports whether diagnostic logging is currently on.
//
// It gates the three things this app can write that are diagnostics rather than
// the user's own state:
//
//   - acs.log / acs.log.1 — the runtime trace
//   - alert-log.jsonl     — every alert raised or deliberately suppressed
//   - feed-samples/       — RAW third-party status payloads, archived verbatim
//
// Off by default, none of them are ever created: a status monitor should not
// leave a stranger's HTTP response bodies on your disk because you installed it.
// On, they are exactly what makes a bug report diagnosable instead of a guess,
// which is why any user can turn it on from Settings rather than it being a
// developer-only switch.
func DebugLogging() bool { return debugLogging.Load() }

// SetDebugLogging updates the process-wide mirror. Persisting the choice is the
// caller's job (see Config.DebugLogging); this only changes what the running
// process does from the next write onward.
func SetDebugLogging(on bool) { debugLogging.Store(on || os.Getenv(DebugEnv) != "") }
