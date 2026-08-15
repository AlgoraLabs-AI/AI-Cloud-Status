//go:build !windows

package ui

// placeAlertPopup is Windows-only; elsewhere the alert card shows wherever the
// window manager puts the splash-style window (typically centered).
func placeAlertPopup(string, int) {}
