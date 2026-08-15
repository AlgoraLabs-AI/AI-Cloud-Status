package ui

import (
	_ "embed"

	"fyne.io/fyne/v2"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
)

// Fyne's bundled fonts have no CJK glyphs, so Chinese/Japanese/Korean UIs would
// render as tofu boxes without an embedded CJK-capable font. Noto Sans CJK SC
// is a pan-CJK face: one file (~16MB, the deliberate one-time cost of wave 2 of
// the i18n expansion) covers Simplified Chinese han, Japanese kana/kanji, and
// Korean hangul — with SC-preferred variants for the handful of ideographs that
// differ regionally, an accepted tradeoff over shipping three 16MB fonts.
// License: SIL OFL 1.1 (fonts/OFL.txt).
//
//go:embed fonts/NotoSansCJKsc-Regular.otf
var notoCJKBytes []byte

// notoCJK is the embedded pan-CJK font as a Fyne resource. The theme serves it
// only while a CJK language is active (see appTheme.Font), so non-CJK users pay
// binary size but no render/memory cost.
var notoCJK = fyne.NewStaticResource("NotoSansCJKsc-Regular.otf", notoCJKBytes)

// cjkLanguages are the language codes whose glyphs need the embedded CJK font.
var cjkLanguages = map[string]bool{"zh-CN": true, "ja": true, "ko": true}

// needsCJKFont reports whether the active UI language requires CJK glyphs.
func needsCJKFont() bool { return cjkLanguages[i18n.ActiveCode()] }
