package ui

import (
	"image/color"
	"os"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// appTheme wraps the active Fyne theme, overriding only the typography scale and
// the spacing grid so the rest of the look (colors, fonts) still follows the
// user's chosen Fyne theme. Headers are made larger than table content and
// padding snaps to a 4/8px grid for consistent rhythm.
type appTheme struct {
	fyne.Theme
}

func newAppTheme() fyne.Theme {
	return &appTheme{Theme: theme.DefaultTheme()}
}

func (t *appTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 4 // base grid unit
	case theme.SizeNameInnerPadding:
		return 8 // comfortable row / control padding (2x grid)
	case theme.SizeNameHeadingText:
		return 20 // section headers, clearly larger than body
	case theme.SizeNameSubHeadingText:
		return 16 // column headers
	case theme.SizeNameText:
		return 13 // body text
	default:
		return t.Theme.Size(name)
	}
}

// Font serves the embedded pan-CJK face while a CJK language is active — the
// bundled fonts would render Chinese/Japanese/Korean as tofu. Monospace text
// (latency/uptime figures) keeps the default mono face for digit alignment; the
// CJK font ships a single weight, so bold styles render regular, which the size
// hierarchy already compensates for.
func (t *appTheme) Font(style fyne.TextStyle) fyne.Resource {
	if needsCJKFont() && !style.Monospace {
		return notoCJK
	}
	return t.Theme.Font(style)
}

func (t *appTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	switch name {
	case colorNameSevMajor:
		return stripDownColor
	case colorNameSevMinor:
		return stripDegradedColor
	case colorNameSevNone:
		return stripOKColor
	case theme.ColorNameForeground:
		// Guarantee strong body-text contrast (>= 4.5:1) regardless of the base
		// theme's foreground, which can be a mid-grey on some variants.
		if v == theme.VariantLight {
			return color.NRGBA{R: 0x1a, G: 0x1a, B: 0x1a, A: 0xff}
		}
		return color.NRGBA{R: 0xf2, G: 0xf2, B: 0xf2, A: 0xff}
	case theme.ColorNameFocus:
		// A clearly visible focus ring for keyboard navigation.
		return color.NRGBA{R: 0x1f, G: 0x6f, B: 0xeb, A: 0xff}
	default:
		return t.Theme.Color(name, v)
	}
}

// reducedMotionPref mirrors the persisted Settings preference. The environment
// variable still takes precedence (see reducedMotion). It is a package var so
// the Controller can update it when the user toggles the setting.
var reducedMotionPref bool

// setReducedMotionPref records the persisted reduced-motion preference.
func setReducedMotionPref(on bool) { reducedMotionPref = on }

// reducedMotion reports whether animations should be suppressed. It honors the
// AI_STATUS_PINGER_REDUCED_MOTION environment variable (any truthy value) so the
// preference can be set without a settings round-trip; this mirrors how OS-level
// "reduce motion" accessibility settings are typically surfaced to apps. When the
// env var is unset, the persisted Settings preference applies.
func reducedMotion() bool {
	v := os.Getenv("AI_STATUS_PINGER_REDUCED_MOTION")
	if v == "" {
		return reducedMotionPref
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	// Non-empty, non-bool (e.g. "yes") counts as enabled.
	return true
}

// statusState is the coarse, color-independent health state shown in the UI. It
// gives at least four differentiated states, each paired with an icon and a
// text label so the status reads without relying on color.
type statusState int

const (
	stateOK          statusState = iota // operational
	stateDegraded                       // partial / minor degradation
	stateOutage                         // major / critical outage
	stateUnreachable                    // a probe target is unreachable (no connectivity)
	stateNoFeed                         // provider status feed could not be read — health UNKNOWN
	statePending                        // not yet checked (loading)
	// stateOfflineUnknown is stateNoFeed's sibling for a check that failed while
	// the MACHINE had no internet. It ranks and paints identically — grey,
	// question mark, never an outage — but says so in its own words, because
	// "status feed unavailable" is not what happened to a custom URL check and
	// blaming the endpoint for the user's dead wifi is the exact error this
	// state exists to prevent.
	stateOfflineUnknown
)

// statusVisual is the full affordance for a status state: a text label, a
// distinct hue, and an icon/shape — so the state is legible to color-blind users
// and in greyscale.
type statusVisual struct {
	Label string
	Color color.Color
	Icon  fyne.Resource
}

var (
	hueOK          = color.NRGBA{R: 0x2e, G: 0xa0, B: 0x43, A: 0xff} // green
	hueDegraded    = color.NRGBA{R: 0xbf, G: 0x87, B: 0x00, A: 0xff} // amber
	hueOutage      = color.NRGBA{R: 0xd7, G: 0x2c, B: 0x2c, A: 0xff} // red
	hueUnreachable = color.NRGBA{R: 0x6e, G: 0x6e, B: 0x6e, A: 0xff} // grey
	huePending     = color.NRGBA{R: 0x1f, G: 0x6f, B: 0xeb, A: 0xff} // blue
)

// The four hues the 24h uptime strip paints with (paintUptimeImage). They are
// package-level and not private to the painter because the severity labels on
// incident cards are read AGAINST the strip sitting a few pixels away — "major"
// and the red band it produced have to be the same red, or the two disagree
// about what the colour means. Sharing the constant is what makes that true by
// construction instead of by two literals staying in sync by hand.
var (
	stripOKColor       = color.NRGBA{R: 0x2e, G: 0xa0, B: 0x43, A: 0xff} // green
	stripDegradedColor = color.NRGBA{R: 0xe6, G: 0xa2, B: 0x3c, A: 0xff} // amber
	stripDownColor     = color.NRGBA{R: 0xd7, G: 0x2c, B: 0x2c, A: 0xff} // red
	// Unknown is a faint grey: time the app was not observing (off, or the
	// check hadn't started). Distinct from green on purpose — unknown ≠ up.
	stripUnknownColor = color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0x55}
)

// Custom theme colour names for incident severity. They exist because
// widget.RichText can only colour a segment by NAME, not by value, and appTheme
// is the only place a name can be resolved — routing severity through the theme
// is what lets a severity label be a real (padded, wrapping, theme-aware)
// widget instead of a bare canvas.Text nudged into alignment by hand.
const (
	colorNameSevMajor fyne.ThemeColorName = "acsSeverityMajor"
	colorNameSevMinor fyne.ThemeColorName = "acsSeverityMinor"
	colorNameSevNone  fyne.ThemeColorName = "acsSeverityNone"
)

// severityColorName maps an incident severity onto the strip hue that the same
// severity paints the uptime bar with: major/critical red, minor amber,
// anything else green.
func severityColorName(s providers.Severity) fyne.ThemeColorName {
	switch s {
	case providers.SevMajor, providers.SevCritical:
		return colorNameSevMajor
	case providers.SevMinor:
		return colorNameSevMinor
	default:
		return colorNameSevNone
	}
}

// visualFor maps a status state to its (localized) label, hue and icon.
func visualFor(s statusState) statusVisual {
	t := i18n.T()
	switch s {
	case stateOK:
		return statusVisual{Label: t.StatusOperational, Color: hueOK, Icon: theme.ConfirmIcon()}
	case stateDegraded:
		return statusVisual{Label: t.StatusDegraded, Color: hueDegraded, Icon: theme.WarningIcon()}
	case stateOutage:
		return statusVisual{Label: t.StatusOutage, Color: hueOutage, Icon: theme.ErrorIcon()}
	case stateUnreachable:
		return statusVisual{Label: t.StatusUnreachable, Color: hueUnreachable, Icon: theme.CancelIcon()}
	case stateNoFeed:
		// Distinct from an outage: we could not READ the status feed, so the
		// service's health is unknown. Grey + a question/help icon + "feed
		// unavailable" wording so it is never misread as the service being down.
		return statusVisual{Label: t.StatusNoFeed, Color: hueUnreachable, Icon: theme.QuestionIcon()}
	case stateOfflineUnknown:
		return statusVisual{Label: t.StatusOfflineUnknown, Color: hueUnreachable, Icon: theme.QuestionIcon()}
	default:
		return statusVisual{Label: t.StatusChecking, Color: huePending, Icon: theme.ViewRefreshIcon()}
	}
}

// providerState derives the UI status state from a provider's region-scoped
// SERVICE-HEALTH severity (feed reachability is handled separately). It is only
// called for a reachable, checked result.
func providerState(sev providers.Severity, checked bool) statusState {
	if !checked {
		return statePending
	}
	switch sev {
	case providers.SevNone:
		return stateOK
	case providers.SevMinor:
		return stateDegraded
	case providers.SevMajor, providers.SevCritical:
		return stateOutage
	default:
		return stateOK
	}
}

// providerStatusState maps a provider's poll outcome onto the three-state model
// as a UI status state: not-yet-checked → pending; no public feed or an
// unreadable feed → stateNoFeed (health unknown, NEVER an outage); otherwise the
// region-scoped service health. It is the single source of truth shared by the
// table rows and the tray aggregate so the two can never disagree.
func providerStatusState(st provState, ok bool, interest []string, isMuted func(region string) bool) statusState {
	switch {
	case !ok || !st.checked:
		return statePending
	case st.skipped, st.err != nil, !st.res.Reachable():
		return stateNoFeed
	default:
		return providerState(effectiveSeverity(st.res, interest, isMuted), true)
	}
}

// connState derives the UI status state for a connectivity target.
func connState(checked, reachable bool, lossPercent float64) statusState {
	if !checked {
		return statePending
	}
	if !reachable && lossPercent >= 100 {
		return stateUnreachable
	}
	switch {
	case lossPercent <= 10:
		return stateOK
	case lossPercent <= 50:
		return stateDegraded
	default:
		return stateOutage
	}
}

// worst returns the more severe of two states using the display ordering
// (pending < noFeed < ok < degraded < unreachable < outage). Outage outranks
// unreachable so a confirmed outage is what surfaces in the tray when both are
// present; an unreadable feed (noFeed, "unknown") ranks below ok so it never
// masks a known-good or known-bad reading.
func (s statusState) worse(other statusState) statusState {
	if s.rank() >= other.rank() {
		return s
	}
	return other
}

func (s statusState) rank() int {
	switch s {
	case statePending:
		return 0
	case stateNoFeed, stateOfflineUnknown:
		// "Unknown" — surfaced in the table but ranked low so it never masks a
		// known degradation/outage in the tray aggregate. Critically, this is
		// what keeps a local internet loss from being aggregated into the tray
		// as a cloud-provider outage.
		return 1
	case stateOK:
		return 2
	case stateDegraded:
		return 3
	case stateUnreachable:
		return 4
	case stateOutage:
		return 5
	default:
		return 0
	}
}

// A severity-hued tray icon used to live here (trayIcon/dotPNG, rendering a
// filled circle in the state's colour). It is deliberately gone: setupTray
// pins the tray icon to the app logo at all times and conveys severity through
// the menu's "Status: …" line instead, so the renderer had no caller left.
