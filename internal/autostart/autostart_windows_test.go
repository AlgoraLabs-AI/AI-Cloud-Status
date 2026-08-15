//go:build windows

package autostart

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestWinRunKeyRoundTrip exercises the real registry-backed runKey against a
// throwaway subkey under HKCU (NOT the actual Run key) so the Windows code path
// is covered without affecting the user's real autostart entries.
func TestWinRunKeyRoundTrip(t *testing.T) {
	const testPath = `Software\AI-Cloud-Status-Test\autostart`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, testPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatalf("create test key: %v", err)
	}
	t.Cleanup(func() {
		_ = k.Close()
		_ = registry.DeleteKey(registry.CURRENT_USER, testPath)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\AI-Cloud-Status-Test`)
	})

	rk := &winRunKey{k: k}
	name := "acs-test-value"

	// Initially absent.
	if _, present, err := rk.get(name); err != nil || present {
		t.Fatalf("initial get: present=%v err=%v, want absent", present, err)
	}
	// Reconcile to enabled.
	if err := reconcile(rk, name, `"C:\acs.exe"`, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	v, present, err := rk.get(name)
	if err != nil || !present || v != `"C:\acs.exe"` {
		t.Fatalf("after enable: v=%q present=%v err=%v", v, present, err)
	}
	// Reconcile to disabled.
	if err := reconcile(rk, name, `"C:\acs.exe"`, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, present, _ := rk.get(name); present {
		t.Fatal("value should be removed after disable")
	}
	// Deleting an absent value is a no-op (not an error).
	if err := rk.delete(name); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
}

func TestExecutableCommandQuoted(t *testing.T) {
	cmd, err := executableCommand()
	if err != nil {
		t.Fatalf("executableCommand: %v", err)
	}
	if len(cmd) < 2 || cmd[0] != '"' || cmd[len(cmd)-1] != '"' {
		t.Errorf("command not quoted: %q", cmd)
	}
}

func TestSupportedOnWindows(t *testing.T) {
	if !Supported() {
		t.Error("autostart should be supported on Windows")
	}
}
