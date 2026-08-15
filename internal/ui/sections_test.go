package ui

import (
	"testing"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// TestEverySectionKeyIsRendered is the regression for rows that silently
// disappeared.
//
// Section keys were bare arithmetic restated in two files, and the two
// disagreed: categoryGroup's fallback for an unrecognized category returned
// len(CategoryOrder)+1, which sectionOrder never listed. A provider whose
// category is not in CategoryOrder was therefore assigned to a section that is
// never drawn — the row vanished from the table entirely, with nothing logged.
// For a monitoring app, a check you cannot see is worse than one that reads
// wrong.
func TestEverySectionKeyIsRendered(t *testing.T) {
	rendered := map[int]bool{}
	for _, s := range sectionOrder() {
		if rendered[s.key] {
			t.Errorf("section key %d is listed twice", s.key)
		}
		rendered[s.key] = true
	}

	// Every key any row can be assigned must be one sectionOrder renders.
	assigned := map[int]string{
		sectionConnectivity: "connectivity",
		sectionCustomURLs(): "custom URLs",
		sectionOther():      "unrecognized provider category",
	}
	for i, cat := range providers.CategoryOrder {
		assigned[sectionForCategoryIndex(i)] = string(cat)
	}
	for key, what := range assigned {
		if !rendered[key] {
			t.Errorf("rows for %s land in section %d, which sectionOrder never renders — they would be invisible", what, key)
		}
	}
}

// TestUnknownCategoryLandsInARenderedSection states the same thing from the
// row's side, which is how the bug would actually be hit: registering a provider
// with a new category and forgetting to add it to CategoryOrder.
func TestUnknownCategoryLandsInARenderedSection(t *testing.T) {
	key := categoryGroup(providers.Category("SomeCategoryNobodyAddedToCategoryOrder"))
	if key != sectionOther() {
		t.Errorf("categoryGroup(unknown) = %d, want the Other bucket %d", key, sectionOther())
	}
	for _, s := range sectionOrder() {
		if s.key == key {
			if s.title == "" {
				t.Error("the Other section has no title")
			}
			return
		}
	}
	t.Errorf("section %d is not rendered", key)
}

// TestKnownCategoriesKeepTheirOwnSection is the counterweight: the catch-all
// must not swallow categories that have a real home.
func TestKnownCategoriesKeepTheirOwnSection(t *testing.T) {
	for i, cat := range providers.CategoryOrder {
		got := categoryGroup(cat)
		if want := sectionForCategoryIndex(i); got != want {
			t.Errorf("categoryGroup(%s) = %d, want %d", cat, got, want)
		}
		if got == sectionOther() {
			t.Errorf("category %s fell into the catch-all", cat)
		}
	}
}

// TestSectionKeysAreDistinct guards the arithmetic itself: connectivity, the
// provider categories, the catch-all and the custom URLs must never collide, or
// two unrelated groups merge into one section.
func TestSectionKeysAreDistinct(t *testing.T) {
	seen := map[int]string{}
	add := func(key int, what string) {
		if prev, ok := seen[key]; ok {
			t.Errorf("section key %d is shared by %s and %s", key, prev, what)
		}
		seen[key] = what
	}
	add(sectionConnectivity, "connectivity")
	for i, cat := range providers.CategoryOrder {
		add(sectionForCategoryIndex(i), string(cat))
	}
	add(sectionOther(), "other")
	add(sectionCustomURLs(), "custom URLs")
}
