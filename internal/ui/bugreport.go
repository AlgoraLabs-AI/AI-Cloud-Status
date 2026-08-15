package ui

import (
	"fmt"
	"image/color"
	"log/slog"
	"net/url"
	"runtime"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
)

// DebugLogLabel marks a run that is writing diagnostics to disk. One spelling,
// used by the title bar and the About banner, so the two can never disagree.
//
// Deliberately NOT translated. It is a state marker whose whole job is to be
// recognisable in a screenshot attached to a bug report — including one sent by
// someone running the app in a language the maintainer does not read — and
// keeping it constant also keeps the window caption stable when the UI language
// changes at runtime, which matters for the reason below.
const DebugLogLabel = "DIAGNOSTIC LOGGING"

// windowTitle is the main window's caption: the app name, plus the diagnostic
// marker while logging is on.
//
// LOAD-BEARING, not cosmetic. On Windows this string is the app's only handle on
// its own window: Fyne exposes no HWND, so maximizeOnPrimary and the dead-canvas
// watchdog both locate the window with FindWindowW, which matches the caption
// EXACTLY. Anything that changes the title without changing those lookups breaks
// them QUIETLY — FindWindowW just returns 0, and every caller treats "not found"
// as "skip", by design, so nothing errors.
//
// That is not hypothetical: when the marker was first added, the lookups still
// passed AppName, so the window stopped being parked on the primary display
// before being maximized. It opened on whatever monitor GLFW chose, unmaximized,
// and rendered at that monitor's DPI scale — oversized text on a secondary
// screen, with no error anywhere. Every Win32 lookup of this app's window MUST
// go through this function; TestWindowLookupsUseTheRealTitle enforces it.
func windowTitle() string {
	if config.DebugLogging() {
		return AppName + " — " + DebugLogLabel
	}
	return AppName
}

// debugLogBanner is the About dialog's "logging is on" notice: the marker on a
// tinted, outlined strip with a warning icon, and under it the sentence naming
// what that means. It is a banner rather than a footnote because it reports that
// the app is writing files it does not normally write.
func debugLogBanner() fyne.CanvasObject {
	rect := canvas.NewRectangle(color.NRGBA{R: 0xf2, G: 0x99, B: 0x0b, A: 0x33})
	rect.CornerRadius = 4
	rect.StrokeColor = color.NRGBA{R: 0xf2, G: 0x99, B: 0x0b, A: 0xff}
	rect.StrokeWidth = 1

	label := widget.NewLabelWithStyle(DebugLogLabel, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	via := widget.NewLabelWithStyle(i18n.T().DebugLogActive, fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
	via.Wrapping = fyne.TextWrapWord

	body := container.NewVBox(container.NewHBox(widget.NewIcon(theme.WarningIcon()), label), via)
	return container.NewStack(rect, container.NewPadded(body))
}

// reportBug opens a pre-filled "new issue" form on the project's tracker.
//
// It exists because the people most able to find a real bug in this app — anyone
// running it on macOS or Linux, where the GUI has never been hand-verified — are
// also the least likely to know that a report is wanted, what belongs in it, or
// that a diagnostic trail can be turned on at all. Pre-filling the environment
// block removes the part of a bug report that is tedious to gather and most
// often wrong when typed from memory.
//
// Nothing is sent anywhere: this opens the user's browser on a form they read,
// edit and submit themselves. The app never posts on their behalf, so it cannot
// disclose a machine's details without the person seeing them first.
func (c *Controller) reportBug() {
	u, err := url.Parse(AppRepo + "/issues/new")
	if err != nil {
		slog.Error("bug report: could not build the issue URL", "err", err)
		return
	}
	q := u.Query()
	q.Set("title", "[bug] ")
	q.Set("labels", "bug")
	q.Set("body", bugReportBody())
	u.RawQuery = q.Encode()
	fyne.CurrentApp().OpenURL(u)
}

// dataFolderHint names the app's data folder for the platform this is running
// on, so a report says where the files actually are rather than listing three
// paths and leaving the reader to pick.
func dataFolderHint() string {
	switch runtime.GOOS {
	case "windows":
		return "%AppData%\\AI-Cloud-Status\\"
	case "darwin":
		return "~/Library/Application Support/AI-Cloud-Status/"
	default:
		return "~/.config/AI-Cloud-Status/"
	}
}

// bugReportBody is the pre-filled issue template. Deliberately in English rather
// than the UI language: it is read by whoever maintains the project, and a report
// nobody can read helps no one. The environment block is filled in; everything a
// human has to think about is left blank with a prompt.
func bugReportBody() string {
	var b strings.Builder
	b.WriteString("### What happened\n\n\n")
	b.WriteString("### What you expected instead\n\n\n")
	b.WriteString("### Steps to reproduce\n\n1.\n2.\n3.\n\n")
	b.WriteString("### Environment\n\n")
	fmt.Fprintf(&b, "- Version: %s\n", Version)
	fmt.Fprintf(&b, "- OS / arch: %s / %s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "- Go runtime: %s\n\n", runtime.Version())

	b.WriteString("### Logs\n\n")
	if config.DebugLogging() {
		// Logging is ON, so the trail exists — say where it is, rather than asking
		// for something the reporter would have to reproduce the problem to get.
		fmt.Fprintf(&b, "Diagnostic logging was on for this run, so the trail exists. "+
			"Please attach `acs.log` from `%s`.\n\n", dataFolderHint())
		b.WriteString("If a provider is reading wrong, `alert-log.jsonl` and the matching ")
		b.WriteString("file under `feed-samples/` are the two things that make it ")
		b.WriteString("diagnosable without guessing.\n\n")
		b.WriteString("> Please skim what you attach. A custom URL check you monitor appears ")
		b.WriteString("in these files, and its query string may contain a token.\n")
	} else {
		b.WriteString("This run wrote no diagnostic log — the app writes none by default.\n\n")
		b.WriteString("If you can reproduce the problem, please turn on **Settings → ")
		b.WriteString("Diagnostics → Save diagnostic logs**, reproduce it, and attach ")
		fmt.Fprintf(&b, "`acs.log` from `%s`. ", dataFolderHint())
		b.WriteString("It takes effect immediately — no restart needed.\n")
	}
	return b.String()
}
