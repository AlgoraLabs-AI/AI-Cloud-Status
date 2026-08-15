// Package autostart manages the "start on login" registration in a user-scoped,
// no-admin way. On Windows that is a value under the HKCU Run key; other
// platforms report Supported() == false and the operations are no-ops. The
// reconcile logic is platform-independent and unit-tested against a fake store;
// the platform files only provide the concrete store and executable path.
package autostart

// ValueName is the registry value / entry name used for the autostart record.
const ValueName = "AI-Cloud-Status"

// runKey abstracts the OS autostart store so the reconcile logic can be tested
// without touching the real registry.
type runKey interface {
	// get returns the stored command for name, whether it was present, and any
	// error. A missing value is (", false, nil) — not an error.
	get(name string) (string, bool, error)
	set(name, value string) error
	delete(name string) error
	close() error
}

// Supported reports whether start-on-login is implemented on this platform.
func Supported() bool { return platformSupported }

// Enabled reports whether the autostart entry is currently registered. On
// unsupported platforms it returns (false, nil).
func Enabled() (bool, error) {
	if !Supported() {
		return false, nil
	}
	rk, err := currentRunKey()
	if err != nil {
		return false, err
	}
	defer rk.close()
	return isEnabled(rk, ValueName)
}

// Apply reconciles the OS registration to the desired state: it registers the
// current executable when desired is true and removes the entry when false. On
// unsupported platforms it is a no-op returning nil.
func Apply(desired bool) error {
	if !Supported() {
		return nil
	}
	rk, err := currentRunKey()
	if err != nil {
		return err
	}
	defer rk.close()
	cmd, err := executableCommand()
	if err != nil {
		return err
	}
	return reconcile(rk, ValueName, cmd, desired)
}

// isEnabled reports whether name is present in the store.
func isEnabled(rk runKey, name string) (bool, error) {
	_, present, err := rk.get(name)
	return present, err
}

// reconcile makes the store match desired: it writes value only when missing or
// changed (avoiding needless registry writes) and deletes only when present.
func reconcile(rk runKey, name, value string, desired bool) error {
	cur, present, err := rk.get(name)
	if err != nil {
		return err
	}
	if desired {
		if present && cur == value {
			return nil
		}
		return rk.set(name, value)
	}
	if present {
		return rk.delete(name)
	}
	return nil
}
