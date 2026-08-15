package i18n

import (
	"reflect"
	"strings"
	"testing"
)

// TestEveryLanguageIsComplete is the safety net for the "one file per language"
// design: every registered language must supply a non-empty value for every
// Catalog field. A new language file with a forgotten key fails here instead of
// silently rendering blank text at runtime.
func TestEveryLanguageIsComplete(t *testing.T) {
	if len(registry) == 0 {
		t.Fatal("no languages registered")
	}
	for code, cat := range registry {
		v := reflect.ValueOf(cat)
		ty := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if f.Kind() != reflect.String {
				t.Fatalf("Catalog field %q is not a string; the completeness check only handles strings", ty.Field(i).Name)
			}
			if f.String() == "" {
				t.Errorf("language %q is missing a value for %q", code, ty.Field(i).Name)
			}
		}
	}
}

// TestRegionResumedBodyVerb guards the one format string: every language must keep
// exactly one %s (the region name) so fmt.Sprintf renders correctly.
func TestRegionResumedBodyVerb(t *testing.T) {
	for code, cat := range registry {
		if n := strings.Count(cat.RegionResumedBody, "%s"); n != 1 {
			t.Errorf("language %q: RegionResumedBody must contain exactly one %%s, found %d", code, n)
		}
	}
}

// TestIncidentRangeVerbs guards the incident-history parentheticals: every
// language must keep exactly the verb count recentIncidentCard passes, or
// fmt.Sprintf renders a stray %!s(MISSING) / %!(EXTRA …) in the UI.
func TestIncidentRangeVerbs(t *testing.T) {
	for code, cat := range registry {
		if n := strings.Count(cat.IncidentResolvedRange, "%s"); n != 3 {
			t.Errorf("language %q: IncidentResolvedRange must contain exactly three %%s (started, resolved, timezone), found %d", code, n)
		}
		if n := strings.Count(cat.IncidentOngoingRange, "%s"); n != 2 {
			t.Errorf("language %q: IncidentOngoingRange must contain exactly two %%s (started, timezone), found %d", code, n)
		}
		if n := strings.Count(cat.IncidentStartedAgo, "%s"); n != 1 {
			t.Errorf("language %q: IncidentStartedAgo must contain exactly one %%s (elapsed), found %d", code, n)
		}
		if n := strings.Count(cat.IncidentResolvedAgo, "%s"); n != 1 {
			t.Errorf("language %q: IncidentResolvedAgo must contain exactly one %%s (elapsed), found %d", code, n)
		}
		if n := strings.Count(cat.AgeAgo, "%s"); n != 1 {
			t.Errorf("language %q: AgeAgo must contain exactly one %%s (elapsed), found %d", code, n)
		}
	}
}

// TestIncidentCountVerbs guards the two incident-section headings: each takes
// exactly one %d, so a translation that drops or doubles it renders a stray
// %!d(MISSING) where the count belongs.
func TestIncidentCountVerbs(t *testing.T) {
	for code, cat := range registry {
		if n := strings.Count(cat.ActiveIncidentsN, "%d"); n != 1 {
			t.Errorf("language %q: ActiveIncidentsN must contain exactly one %%d (count), found %d", code, n)
		}
		if n := strings.Count(cat.StaleIncidentsN, "%d"); n != 1 {
			t.Errorf("language %q: StaleIncidentsN must contain exactly one %%d (count), found %d", code, n)
		}
	}
}

// TestTrayStatusVerb guards the tray's status line: it takes exactly one %s
// (the already-localized summary). This string exists because the summary used
// to be glued to a hardcoded English "Status: " prefix, which left every
// non-English tray half-translated after the first poll.
func TestTrayStatusVerb(t *testing.T) {
	for code, cat := range registry {
		if n := strings.Count(cat.TrayStatus, "%s"); n != 1 {
			t.Errorf("language %q: TrayStatus must contain exactly one %%s (summary), found %d", code, n)
		}
	}
}

// TestRegionDeactivationVerbs guards the region-popup strings that were
// hardcoded English literals until they moved into the catalog. They were
// invisible to the completeness check precisely because they were not fields,
// so nine of ten languages rendered them in English with nothing failing.
func TestRegionDeactivationVerbs(t *testing.T) {
	for code, cat := range registry {
		if n := strings.Count(cat.RegionDeactivatedFor, "%s"); n != 1 {
			t.Errorf("language %q: RegionDeactivatedFor must contain exactly one %%s (the remaining span), found %d", code, n)
		}
		if n := strings.Count(cat.RegionIncidentLastUpdate, "%s"); n != 1 {
			t.Errorf("language %q: RegionIncidentLastUpdate must contain exactly one %%s (timestamp), found %d", code, n)
		}
		if n := strings.Count(cat.RegionIncidentLastUpdate, "%d"); n != 1 {
			t.Errorf("language %q: RegionIncidentLastUpdate must contain exactly one %%d (days), found %d", code, n)
		}
		if n := strings.Count(cat.TrayStillRunningTitle, "%s"); n != 1 {
			t.Errorf("language %q: TrayStillRunningTitle must contain exactly one %%s (app name), found %d", code, n)
		}
	}
}

// TestCheckDetailVerbs guards the connectivity/custom-URL panel strings. The
// completeness check cannot see a verb MISMATCH — it only knows a field is
// non-empty — and TestNoStrayVerbsInPlainStrings only knows whether a '%' is
// present at all. So a translation writing "%s probes" where the code passes an
// int compiles, passes both, and renders "%!s(int=587)" in the panel. This pins
// the exact verb count and type of every field those panels format.
func TestCheckDetailVerbs(t *testing.T) {
	cases := []struct {
		name string
		get  func(Catalog) string
		verb string
		want int
	}{
		{"PingDetailsTitle", func(c Catalog) string { return c.PingDetailsTitle }, "%s", 1},
		{"CheckDetailsTitle", func(c Catalog) string { return c.CheckDetailsTitle }, "%s", 1},
		{"ConnDetailMeta", func(c Catalog) string { return c.ConnDetailMeta }, "%s", 3},
		{"URLDetailMeta", func(c Catalog) string { return c.URLDetailMeta }, "%s", 3},
		{"LatencyTrend", func(c Catalog) string { return c.LatencyTrend }, "%s", 1},
		{"StatsHeader", func(c Catalog) string { return c.StatsHeader }, "%s", 1},
		{"StatAvgLatency", func(c Catalog) string { return c.StatAvgLatency }, "%s", 1},
		{"StatAvgResponse", func(c Catalog) string { return c.StatAvgResponse }, "%s", 1},
		{"StatMinMax", func(c Catalog) string { return c.StatMinMax }, "%s", 2},
		// The percentage is formatted in Go and passed as a string, so a
		// translation never has to carry a literal '%' next to a verb.
		{"StatPacketLoss", func(c Catalog) string { return c.StatPacketLoss }, "%s", 1},
		{"StatPacketLoss", func(c Catalog) string { return c.StatPacketLoss }, "%d", 2},
		{"StatLongestLoss", func(c Catalog) string { return c.StatLongestLoss }, "%d", 1},
		{"ExpectedTextLabel", func(c Catalog) string { return c.ExpectedTextLabel }, "%s", 1},
		{"OutagesHeader", func(c Catalog) string { return c.OutagesHeader }, "%s", 1},
		{"OutagesHeader", func(c Catalog) string { return c.OutagesHeader }, "%d", 1},
		{"NoOutagesWindow", func(c Catalog) string { return c.NoOutagesWindow }, "%s", 1},
		{"OutageOngoing", func(c Catalog) string { return c.OutageOngoing }, "%s", 2},
		{"OutageResolved", func(c Catalog) string { return c.OutageResolved }, "%s", 3},
		{"OutageAtLeast", func(c Catalog) string { return c.OutageAtLeast }, "%s", 3},
		{"OutageResumedUp", func(c Catalog) string { return c.OutageResumedUp }, "%s", 4},
	}
	for code, cat := range registry {
		for _, tc := range cases {
			if n := strings.Count(tc.get(cat), tc.verb); n != tc.want {
				t.Errorf("language %q: %s must contain exactly %d %s, found %d", code, tc.name, tc.want, tc.verb, n)
			}
		}
	}
}

// TestNoStrayVerbsInPlainStrings is the other half of the format-string guard:
// a field with no placeholder must not have acquired one, or fmt renders a
// literal "%!s(MISSING)" where the text should be. It walks every field by
// reflection, so a NEW plain field is covered without anyone remembering to
// add it here.
func TestNoStrayVerbsInPlainStrings(t *testing.T) {
	// Fields that legitimately carry format verbs.
	formatted := map[string]bool{}
	v := reflect.ValueOf(en)
	ty := v.Type()
	for i := 0; i < v.NumField(); i++ {
		if strings.ContainsAny(v.Field(i).String(), "%") {
			formatted[ty.Field(i).Name] = true
		}
	}

	for code, cat := range registry {
		cv := reflect.ValueOf(cat)
		for i := 0; i < cv.NumField(); i++ {
			name := ty.Field(i).Name
			s := cv.Field(i).String()
			if !formatted[name] && strings.Contains(s, "%") {
				t.Errorf("language %q: %s has no placeholder in English but contains %%: %q", code, name, s)
			}
			// A '%%' escape is fine; a lone '%' followed by a letter is a verb.
			if formatted[name] && !strings.Contains(s, "%") {
				t.Errorf("language %q: %s dropped its format placeholder: %q", code, name, s)
			}
		}
	}
}

// TestSetFallsBackToEnglish verifies an unknown language code yields English, not
// an empty catalog.
func TestSetFallsBackToEnglish(t *testing.T) {
	Set("zz-not-a-language")
	if T().RefreshNow != en.RefreshNow {
		t.Fatalf("unknown language should fall back to English; got RefreshNow=%q", T().RefreshNow)
	}
	Set("es")
	if T().RefreshNow != es.RefreshNow {
		t.Fatalf("Set(es) should activate Spanish; got %q", T().RefreshNow)
	}
	Set("en") // restore default for other tests
}

// TestLanguagesRegistered confirms both shipped languages self-registered.
func TestLanguagesRegistered(t *testing.T) {
	want := map[string]bool{"en": false, "es": false}
	for _, l := range Languages() {
		if _, ok := want[l.Code]; ok {
			want[l.Code] = true
		}
	}
	for code, seen := range want {
		if !seen {
			t.Errorf("language %q did not self-register", code)
		}
	}
}
