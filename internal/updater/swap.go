package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// backupSuffix marks the outgoing executable while the replacement is moved into
// place. It is deleted by the SUCCESSOR (see CleanupBackup), because on Windows
// a running image cannot delete itself.
const backupSuffix = ".old"

// UpdatedEnvVar is set on the successor process an update starts, telling it
// that the binary it is running was just installed and the predecessor's image
// is waiting to be deleted.
const UpdatedEnvVar = "ACS_UPDATED"

// SelfPath returns the path of the running executable with symlinks resolved, so
// a swap replaces the real file rather than the link pointing at it.
func SelfPath() (string, error) { return selfPathFn() }

// selfPathFn is SelfPath's implementation, indirected so the swap tests can aim
// the rename dance at a fixture instead of the running test binary. Production
// never reassigns it.
var selfPathFn = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

// Swap installs newPath over the running executable and returns the path the
// outgoing binary was moved to.
//
// The order is what makes this safe on Windows, where a running image is locked
// against deletion and overwriting but CAN be renamed: the running binary moves
// aside first, freeing its name, and only then does the replacement take that
// name. If the second rename fails, the first is undone — so either the update
// is installed or the previous binary is exactly where it was. There is no
// ordering here in which the user ends up with neither.
//
// wantSum is the digest Download verified, RE-CHECKED here immediately before
// the rename. Verifying in Download and installing in Swap leaves a window in
// which the staged file is protected only by the install directory's ACL, and
// whatever occupies that path at rename time is what becomes the executable. On
// a portable install — an unzip-to-Downloads, a USB stick, a folder at a drive
// root — that directory is routinely writable by anything running as the user.
// Re-hashing ~45 MiB costs a few hundred milliseconds and closes the window
// completely, so the last thing that touches these bytes is also the thing that
// checked them. Pass "" only when there is nothing to check against.
func Swap(newPath, wantSum string) (backup string, err error) {
	self, err := SelfPath()
	if err != nil {
		return "", err
	}
	if wantSum != "" {
		got, err := fileSum(newPath)
		if err != nil {
			return "", err
		}
		if got != wantSum {
			return "", fmt.Errorf("staged update changed after verification: %s, expected %s", got, wantSum)
		}
	}
	if same, err := sameFile(newPath, self); err == nil && same {
		return "", errors.New("refusing to install a binary over itself")
	}

	backup = self + backupSuffix
	// A leftover backup from an update whose successor never got to clean up
	// would block the rename on Windows. It is already superseded, so removing it
	// is safe; failing to remove it is not fatal until the rename actually fails.
	_ = os.Remove(backup)

	if err := os.Rename(self, backup); err != nil {
		return "", fmt.Errorf("could not move the current version aside: %w", err)
	}
	if err := os.Rename(newPath, self); err != nil {
		// Put the user's working binary back before reporting the failure.
		if rerr := os.Rename(backup, self); rerr != nil {
			return "", fmt.Errorf("update failed (%w) AND the previous version could not be restored from %s: %v", err, backup, rerr)
		}
		return "", fmt.Errorf("could not move the new version into place: %w", err)
	}
	return backup, nil
}

// CleanupBackup deletes the previous version left behind by an update, if any.
// It runs in the SUCCESSOR process at startup: the predecessor could not delete
// its own running image, and by the time this runs that image has exited.
//
// Failure is ignored on purpose. A leftover .old file is inert — it is never
// executed and the next update overwrites it — so a locked or unwritable file is
// not worth telling the user about, and certainly not worth failing a launch for.
func CleanupBackup() {
	self, err := SelfPath()
	if err != nil {
		return
	}
	_ = os.Remove(self + backupSuffix)
}

// fileSum returns the lowercase hex sha256 of the file at path.
func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sameFile reports whether two paths refer to the same file on disk.
func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}
