//go:build !windows

package ui

// maximizeOnPrimary is a no-op on non-Windows platforms (Fyne exposes no portable
// maximize API; the window opens at its configured size there).
func maximizeOnPrimary(string) {}
