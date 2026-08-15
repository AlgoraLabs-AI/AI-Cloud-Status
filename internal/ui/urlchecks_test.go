package ui

import "testing"

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"https://www.google.com":         "https://www.google.com", // scheme present → unchanged
		"www.google.com":                 "https://www.google.com", // no scheme → https added
		"http://x.com":                   "http://x.com",           // http preserved
		"  https://x.com  ":              "https://x.com",          // trimmed
		"https://https://www.google.com": "https://www.google.com", // doubled scheme collapsed
		"http://http://x.com":            "http://x.com",
		"":                               "",
	}
	for in, want := range cases {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
