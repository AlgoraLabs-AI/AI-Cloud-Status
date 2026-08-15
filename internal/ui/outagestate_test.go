package ui

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/alertlog"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
)

// anyCheck accepts every id, for tests not exercising the known-check filter.
func anyCheck(string) bool { return true }

// TestOutageStateSurvivesARestart is the end-to-end of the field failure: latch
// an outage, write it, build a FRESH controller from the file, and confirm the
// recovery still arrives. Without persistence the second controller starts blank
// and the recovery is lost forever.
func TestOutageStateSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-outages.json")

	first := &Controller{outages: monitor.NewOutageTracker(), outageStatePath: path}
	first.outages.Update("url-1", "portaldev", "request failed", true)
	first.saveOutageState()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file was not written: %v", err)
	}

	// --- restart ---
	next := &Controller{outages: monitor.NewOutageTracker(), outageStatePath: path}
	next.restoreOutageState(loadOutageState(path), time.Now(), anyCheck)

	next.outages.Update("url-1", "portaldev", "", false)
	if n := next.outages.Update("url-1", "portaldev", "", false); n == nil {
		t.Fatal("the recovery did not survive the restart")
	}
}

// TestStaleEntriesAreNotRejournaledOnEveryLaunch is the regression for a defect
// two independent design reviews caught: restore consumed the stale entries but
// never rewrote the file, so EVERY subsequent launch re-read the same entry and
// appended another "stale-across-restart" recovery. For provider ids those lines
// feed internal/audit's window pairing, so the fix meant to protect the audit
// was corrupting it.
func TestStaleEntriesAreNotRejournaledOnEveryLaunch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "open-outages.json")
	logPath := filepath.Join(dir, "alert-log.jsonl")

	writeState(t, path, []monitor.DownState{
		{ID: "url-1", Name: "portaldev", Since: time.Now().Add(-96 * time.Hour)},
	})

	for i := 0; i < 3; i++ {
		c := &Controller{
			outages: monitor.NewOutageTracker(), outageStatePath: path,
			alerts: alertlog.Open(logPath),
		}
		c.restoreOutageState(loadOutageState(path), time.Now(), anyCheck)
	}

	if n := countLines(t, logPath); n != 1 {
		t.Errorf("alert log has %d stale-closure lines after 3 launches, want exactly 1", n)
	}
	if got := readState(t, path); len(got) != 0 {
		t.Errorf("state file still holds %+v after the stale entry was consumed", got)
	}
}

// TestRestoreDropsUnknownIDs pins that a check deleted while the app was CLOSED
// does not come back. Nothing polls it, so it could never transition its way out
// of the file — it would linger forever and, once past the age limit, journal a
// phantom recovery for a check the user removed.
func TestRestoreDropsUnknownIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "open-outages.json")
	logPath := filepath.Join(dir, "alert-log.jsonl")
	writeState(t, path, []monitor.DownState{
		{ID: "url-gone", Name: "deleted", Since: time.Now()},
		{ID: "url-kept", Name: "kept", Since: time.Now()},
	})

	c := &Controller{
		outages: monitor.NewOutageTracker(), outageStatePath: path,
		alerts: alertlog.Open(logPath),
	}
	c.restoreOutageState(loadOutageState(path), time.Now(),
		func(id string) bool { return id == "url-kept" })

	got := readState(t, path)
	if len(got) != 1 || got[0].ID != "url-kept" {
		t.Fatalf("state file = %+v, want only the still-known check", got)
	}
	// A check that no longer exists has no audit window worth closing.
	if n := countLines(t, logPath); n != 0 {
		t.Errorf("alert log got %d lines for a deleted check, want 0", n)
	}
}

// TestSuppressedOutageStaysSuppressedAcrossRestart pins that a recovery does not
// become an orphan: if the user never SAW the outage card (muted, DND, a
// deactivated region), the green "operational again" card explaining its end must
// be swallowed too — which is what recoveryIsOrphaned does inside a single run,
// and what the restart used to undo.
func TestSuppressedOutageStaysSuppressedAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-outages.json")

	first := &Controller{
		outages: monitor.NewOutageTracker(), outageStatePath: path,
		downSuppressed: map[string]bool{},
	}
	first.outages.Update("p1", "Provider", "boom", true)
	first.noteAlertOutcome("p1", false, true) // the outage card was swallowed
	first.saveOutageState()

	if got := readState(t, path); len(got) != 1 || !got[0].Suppressed {
		t.Fatalf("persisted state = %+v, want the suppression outcome recorded", got)
	}

	next := &Controller{outages: monitor.NewOutageTracker(), outageStatePath: path}
	next.restoreOutageState(loadOutageState(path), time.Now(), anyCheck)
	if !next.recoveryIsOrphaned("p1") {
		t.Error("after the restart the recovery would be shown for an outage the user never saw")
	}
}

// TestSaveOutageStateSerializesConcurrentSavers pins the lost-update window:
// provider and URL polling each fan out a goroutine per check, and snapshot and
// write used to be separate operations, so a slower goroutine's OLDER snapshot
// could land last and silently drop a check that had just gone down.
func TestSaveOutageStateSerializesConcurrentSavers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-outages.json")
	c := &Controller{outages: monitor.NewOutageTracker(), outageStatePath: path}

	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmtID(i)
			c.outages.Update(id, id, "boom", true)
			c.saveOutageState()
		}(i)
	}
	wg.Wait()

	if got := readState(t, path); len(got) != n {
		t.Errorf("state file holds %d of %d latched outages — a stale snapshot overwrote a newer one",
			len(got), n)
	}
}

// TestRestoreRejectsAFutureTimestamp pins that a corrupt or clock-skewed Since
// cannot make an outage immortal: now.Sub(future) is negative, so a naive age
// check reads it as freshly latched forever.
func TestRestoreRejectsAFutureTimestamp(t *testing.T) {
	o := monitor.NewOutageTracker()
	stale := o.Restore([]monitor.DownState{
		{ID: "p1", Name: "P1", Since: time.Now().Add(48 * time.Hour)},
	}, time.Now(), 24*time.Hour)
	if len(stale) != 1 {
		t.Fatalf("a future-dated outage was accepted as fresh: stale=%+v", stale)
	}
	if got := o.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot = %+v, want nothing latched", got)
	}
}

// TestOutageStateRejectsAnUnknownVersion pins that a file from a future build is
// ignored whole rather than half-interpreted under today's field meanings.
func TestOutageStateRejectsAnUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-outages.json")
	data, err := json.Marshal(map[string]any{
		"version": 99,
		"checks":  []monitor.DownState{{ID: "p1", Name: "P1", Since: time.Now()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadOutageState(path); got.Checks != nil {
		t.Errorf("loadOutageState on version 99 = %+v, want nothing", got)
	}
}

// TestOutageStateSkipsStaleAcrossALongShutdown pins the freshness policy end to
// end: the app was off for days, so the check quietly closes instead of popping
// a toast about something that resolved long ago.
func TestOutageStateSkipsStaleAcrossALongShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-outages.json")
	c := &Controller{outages: monitor.NewOutageTracker(), outageStatePath: path}
	c.restoreOutageState(outageStateFile{Checks: []monitor.DownState{
		{ID: "url-1", Name: "portaldev", Since: time.Now().Add(-96 * time.Hour)},
	}}, time.Now(), anyCheck)

	if n := c.outages.Update("url-1", "portaldev", "", false); n != nil {
		t.Errorf("a four-day-old outage announced a recovery: %+v", n)
	}
}

// TestOutageStateHandlesAMissingOrCorruptFile pins that continuity is
// best-effort: neither absence nor garbage may stop the app from starting.
func TestOutageStateHandlesAMissingOrCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if got := loadOutageState(filepath.Join(dir, "nope.json")); got.Checks != nil {
		t.Errorf("missing file = %+v, want nil", got)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadOutageState(bad); got.Checks != nil {
		t.Errorf("corrupt file = %+v, want nil", got)
	}
	if got := loadOutageState(""); got.Checks != nil {
		t.Errorf("empty path = %+v, want nil", got)
	}
}

// TestSaveOutageStateIsSafeWithoutAPath guards the test/headless construction
// path, where no config dir could be resolved.
func TestSaveOutageStateIsSafeWithoutAPath(t *testing.T) {
	c := &Controller{outages: monitor.NewOutageTracker()}
	c.saveOutageState() // must not panic
	c.restoreOutageState(outageStateFile{}, time.Now(), anyCheck)
}

// --- helpers ---

func fmtID(i int) string {
	return "check-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}

func writeState(t *testing.T, path string, states []monitor.DownState) {
	t.Helper()
	data, err := json.Marshal(outageStateFile{Version: outageStateVersion, Checks: states})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readState(t *testing.T, path string) []monitor.DownState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var f outageStateFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("state file is not valid JSON: %v", err)
	}
	return f.Checks
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// TestEveryOutageTransitionIsPersisted is the test that would have caught the
// real omission, and the reason it is written this way.
//
// The feature was implemented, tested, and shipped while the CUSTOM URL poll
// path — the exact check class whose lost recovery motivated all of it — never
// called saveOutageState at all. Every unit test passed, because each one drove
// the mechanism by hand (latch, then call save) instead of going through the
// code that is supposed to call it. The mechanism was never the broken part; the
// WIRING was, and nothing looked at the wiring.
//
// So this walks the package source: wherever a transition is consumed
// (`c.outages.Update(...) != nil`), the enclosing block must also persist it.
func TestEveryOutageTransitionIsPersisted(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				stmt, ok := n.(*ast.IfStmt)
				if !ok || stmt.Init == nil {
					return true
				}
				if !mentions(fset, stmt.Init, "c.outages.Update") {
					return true
				}
				if !mentions(fset, stmt.Body, "c.saveOutageState") {
					t.Errorf("%s: a transition from c.outages.Update is consumed without "+
						"persisting it — a restart here silently swallows the recovery",
						fset.Position(stmt.Pos()))
				}
				return true
			})
		}
	}
}

// mentions reports whether the source text of a node contains needle.
func mentions(fset *token.FileSet, n ast.Node, needle string) bool {
	var b strings.Builder
	ast.Inspect(n, func(x ast.Node) bool {
		sel, ok := x.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			b.WriteString(id.Name + "." + sel.Sel.Name + " ")
		} else if inner, ok := sel.X.(*ast.SelectorExpr); ok {
			if id, ok := inner.X.(*ast.Ident); ok {
				b.WriteString(id.Name + "." + inner.Sel.Name + "." + sel.Sel.Name + " ")
			}
		}
		return true
	})
	return strings.Contains(b.String(), needle)
}
