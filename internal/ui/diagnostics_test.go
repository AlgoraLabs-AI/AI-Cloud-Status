package ui

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
)

// setDebug flips the process-wide diagnostic-logging mirror for one test and
// restores it afterwards.
func setDebug(t *testing.T, on bool) {
	t.Helper()
	prev := config.DebugLogging()
	t.Setenv(config.DebugEnv, "") // the env override must not decide the outcome
	config.SetDebugLogging(on)
	t.Cleanup(func() { config.SetDebugLogging(prev) })
}

// TestWindowTitleMarksDiagnosticLogging pins the title-bar marker. Logging
// changes what the app writes to disk, so the answer to "is it on?" has to be
// visible rather than remembered — and the title bar is the one piece of chrome
// that survives being backgrounded, minimised to the taskbar, and cropped into a
// screenshot attached to a bug report.
func TestWindowTitleMarksDiagnosticLogging(t *testing.T) {
	setDebug(t, false)
	if got := windowTitle(); got != AppName {
		t.Errorf("a normal run titled the window %q, want plain %q", got, AppName)
	}

	setDebug(t, true)
	got := windowTitle()
	if !strings.Contains(got, DebugLogLabel) {
		t.Errorf("a logging run titled the window %q, want it to carry %q", got, DebugLogLabel)
	}
	if !strings.HasPrefix(got, AppName) {
		t.Errorf("title %q no longer starts with the app name", got)
	}
}

// TestWindowLookupsUseTheRealTitle is the regression for the bug the marker
// introduced. The title bar is this app's ONLY handle on its own window on
// Windows (Fyne exposes no HWND), so maximizeOnPrimary and the dead-canvas
// watchdog find it with FindWindowW, which matches the caption exactly. Passing
// AppName once the caption carried a suffix made both lookups return 0 — and
// both treat "not found" as "skip", so nothing errored: the window simply opened
// on whatever monitor GLFW chose, unmaximized, at that monitor's DPI scale.
//
// It reads the source because the failure is a syscall against live window
// state, which a unit test cannot observe. What it CAN pin is that no Win32
// lookup names the app constant instead of the real title.
func TestWindowLookupsUseTheRealTitle(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	lookups := []string{"maximizeOnPrimary(", "UTF16PtrFromString("}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			for _, call := range lookups {
				idx := strings.Index(line, call)
				if idx < 0 {
					continue
				}
				if strings.HasPrefix(line[idx+len(call):], "AppName") {
					t.Errorf("%s:%d passes AppName to %s — it must pass windowTitle(), "+
						"or the lookup silently misses whenever the caption carries a suffix:\n\t%s",
						f, i+1, strings.TrimSuffix(call, "("), strings.TrimSpace(line))
				}
			}
		}
	}
}

// TestBugReportBodyCarriesTheEnvironment: the environment block is the part of a
// bug report that is tedious to gather and most often wrong when typed from
// memory, so it must actually be filled in — and the two states must give
// different instructions, because "attach the log" is useless advice to someone
// who has no log.
func TestBugReportBodyCarriesTheEnvironment(t *testing.T) {
	setDebug(t, false)
	plain := bugReportBody()
	if !strings.Contains(plain, Version) {
		t.Errorf("bug report body does not carry the version %q", Version)
	}
	if !strings.Contains(plain, "Settings") {
		t.Error("with logging off, the report should point at the Settings toggle")
	}

	setDebug(t, true)
	on := bugReportBody()
	if !strings.Contains(on, "acs.log") {
		t.Error("with logging on, the report should name the log file to attach")
	}
	if !strings.Contains(on, dataFolderHint()) {
		t.Error("the report should name this platform's data folder")
	}
	// The captures can hold a monitored URL's query string, so the warning to
	// skim before attaching is load-bearing, not decoration.
	if !strings.Contains(strings.ToLower(on), "token") {
		t.Error("the report omits the warning that attachments may carry a token")
	}
}

// TestFilepathToURLPath: "Open data folder" hands a file:// URL to the OS. A
// Windows path needs its separators flipped and a leading slash, or url.URL
// percent-escapes the backslashes into something no file manager resolves.
func TestFilepathToURLPath(t *testing.T) {
	cases := map[string]string{
		`C:\Users\x\AppData\Roaming\AI-Cloud-Status`: "/C:/Users/x/AppData/Roaming/AI-Cloud-Status",
		"/home/x/.config/AI-Cloud-Status":            "/home/x/.config/AI-Cloud-Status",
		// macOS is the only one of the three whose data folder has a SPACE in it.
		"/Users/x/Library/Application Support/AI-Cloud-Status": "/Users/x/Library/Application Support/AI-Cloud-Status",
	}
	for in, want := range cases {
		if got := filepathToURLPath(in); got != want {
			t.Errorf("filepathToURLPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDeleteDiagnosticsNeverTouchesUserState is the safety net on the riskiest
// function in this package. "Delete diagnostic data" reclaims disk by removing
// files; if its list were ever expressed as "everything except these", a file
// added later would be deleted by DEFAULT — and the files sitting beside them
// are the user's settings, their 24h history, and their incident journal.
//
// The list is therefore an allow-list, and this test is what keeps it one.
func TestDeleteDiagnosticsNeverTouchesUserState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)         // Windows
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux
	t.Setenv("HOME", dir)            // macOS

	real, err := config.Dir()
	if err != nil {
		t.Skipf("config dir not resolvable under a redirected HOME: %v", err)
	}
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}

	userState := []string{"config.json", "history.json", "incidents.json", "open-outages.json", "instance.lock"}
	for _, name := range userState {
		if err := os.WriteFile(filepath.Join(real, name), []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range diagnosticFiles {
		if err := os.WriteFile(filepath.Join(real, name), []byte("diagnostic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	samples := filepath.Join(real, "feed-samples", "cloudflare")
	if err := os.MkdirAll(samples, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(samples, "x.json.gz"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := diagnosticsUsage()
	if before == 0 {
		t.Fatal("usage reported 0 with diagnostic files present — it is not counting them")
	}

	setDebug(t, false) // don't reopen a log file into the temp dir
	freed, err := deleteDiagnostics()
	if err != nil {
		t.Fatalf("deleteDiagnostics: %v", err)
	}
	if freed != before {
		t.Errorf("reported %d bytes freed, usage said %d", freed, before)
	}

	for _, name := range userState {
		if _, err := os.Stat(filepath.Join(real, name)); err != nil {
			t.Errorf("USER STATE DELETED: %s is gone (%v)", name, err)
		}
	}
	for _, name := range diagnosticFiles {
		if _, err := os.Stat(filepath.Join(real, name)); !os.IsNotExist(err) {
			t.Errorf("diagnostic file %s survived", name)
		}
	}
	if _, err := os.Stat(filepath.Join(real, "feed-samples")); !os.IsNotExist(err) {
		t.Error("feed-samples survived")
	}
	if got := diagnosticsUsage(); got != 0 {
		t.Errorf("usage is %d after deleting everything, want 0", got)
	}
}

// TestHumanBytes: the usage figure is the only place a user sees the budget
// working, so it has to read like a size and not like a raw integer.
func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0: "0 B", 512: "512 B", 2048: "2 KB",
		5 << 20: "5.0 MB", 3 << 30: "3.0 GB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestDataFolderURLEscapesTheSpace pins the half of "Open data folder" that
// filepathToURLPath deliberately does NOT do: macOS's data folder lives under
// "Application Support", and only url.URL.String() percent-escapes that space.
// Handing `open` a raw space silently opens the wrong thing — and macOS is the
// only supported platform where the default path contains one, so nothing else
// in this suite would notice a "simplification" that built the URL by hand.
func TestDataFolderURLEscapesTheSpace(t *testing.T) {
	const dir = "/Users/x/Library/Application Support/AI-Cloud-Status"
	const want = "file:///Users/x/Library/Application%20Support/AI-Cloud-Status"

	u := &url.URL{Scheme: "file", Path: filepathToURLPath(dir)}
	if got := u.String(); got != want {
		t.Errorf("file URL for %q = %q, want %q", dir, got, want)
	}
}
