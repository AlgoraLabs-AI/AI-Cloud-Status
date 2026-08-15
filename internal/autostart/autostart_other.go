//go:build !windows

package autostart

// platformSupported is false off Windows; the public API short-circuits before
// calling the functions below, but they must exist for the package to compile.
const platformSupported = false

func currentRunKey() (runKey, error)     { return nil, nil }
func executableCommand() (string, error) { return "", nil }
