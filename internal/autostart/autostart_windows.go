//go:build windows

package autostart

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

const platformSupported = true

// runKeyPath is the user-scoped (no admin) autostart location.
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// winRunKey is a registry-backed runKey over HKCU's Run key.
type winRunKey struct {
	k registry.Key
}

func currentRunKey() (runKey, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	return &winRunKey{k: k}, nil
}

func (w *winRunKey) get(name string) (string, bool, error) {
	v, _, err := w.k.GetStringValue(name)
	if err == registry.ErrNotExist {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (w *winRunKey) set(name, value string) error {
	return w.k.SetStringValue(name, value)
}

func (w *winRunKey) delete(name string) error {
	if err := w.k.DeleteValue(name); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

func (w *winRunKey) close() error { return w.k.Close() }

// executableCommand returns the quoted path of the running executable, suitable
// as Run-key command data (quoting guards against spaces in the path).
func executableCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return `"` + exe + `"`, nil
}
