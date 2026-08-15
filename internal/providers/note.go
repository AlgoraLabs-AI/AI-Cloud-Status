package providers

import (
	"html"
	"regexp"
	"strings"
)

// noteBreakTag matches the block-level tags that end a line of prose. They are
// turned into newlines BEFORE the generic tag strip so sentences don't run
// together into one wall of text.
var noteBreakTag = regexp.MustCompile(`(?i)</p>|<br\s*/?>|</div>|</li>`)

// noteTagOrEntity matches any remaining tag, plus any HTML entity, so the two
// can be handled differently: tags are dropped, entities are decoded.
var noteTagOrEntity = regexp.MustCompile(`<[^>]*>|&[a-zA-Z#0-9]+;`)

// plainNote renders a provider-authored update message as plain text: block
// tags become paragraph breaks, remaining markup is dropped, entities are
// decoded and whitespace is collapsed.
//
// Every adapter runs its Note through this, not just the RSS ones. Statuspage's
// incident_updates[].body is HTML too — GitHub's "…IDE surfaces.<br /><br />We
// will continue monitoring…" rendered the literal tags in both the table row
// and the detail dialog, because Fyne labels draw text verbatim. Passing plain
// text through is a no-op, so applying it uniformly costs nothing and removes
// the need to know which feed happens to embed markup this week.
func plainNote(desc string) string {
	if strings.TrimSpace(desc) == "" {
		return ""
	}
	s := noteBreakTag.ReplaceAllString(desc, "\n")
	s = noteTagOrEntity.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "<") {
			return ""
		}
		return html.UnescapeString(m)
	})
	// Collapse runs of blank space, keeping paragraph breaks as single newlines.
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
