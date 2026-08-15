package alertlog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func readEntries(t *testing.T, path string) []Entry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var out []Entry
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("unparsable line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func TestRecordAppendsJSONLines(t *testing.T) {
	path := Path(t.TempDir())
	l := Open(path)
	l.Record(Entry{ID: "azure", Title: "Azure outage", Body: "West US down"})
	l.Record(Entry{ID: "azure", Title: "Azure recovered", Recovery: true})
	l.Record(Entry{ID: "aws", Title: "AWS outage", Suppressed: "muted"})

	got := readEntries(t, path)
	if len(got) != 3 {
		t.Fatalf("entries = %d, want 3", len(got))
	}
	if got[0].ID != "azure" || got[0].Title != "Azure outage" || got[0].Body != "West US down" {
		t.Errorf("first entry mismatch: %+v", got[0])
	}
	if !got[1].Recovery {
		t.Error("second entry should be a recovery")
	}
	if got[2].Suppressed != "muted" {
		t.Errorf("suppressed = %q, want muted", got[2].Suppressed)
	}
	for i, e := range got {
		if e.Time.IsZero() {
			t.Errorf("entry %d has no timestamp", i)
		}
	}
}

func TestOpenPrunesOldAndGarbageLines(t *testing.T) {
	path := Path(t.TempDir())
	l := Open(path)
	l.Record(Entry{Time: time.Now().Add(-keepWindow - time.Hour), ID: "old", Title: "ancient"})
	l.Record(Entry{ID: "new", Title: "recent"})
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	Open(path) // re-open triggers the prune

	got := readEntries(t, path)
	if len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("after prune got %+v, want only the recent entry", got)
	}
}

// A log with nothing to drop must not be rewritten on Open — the early return
// keeps startup cheap and leaves the file bytes untouched.
func TestOpenWithoutOldEntriesLeavesFileIntact(t *testing.T) {
	path := Path(t.TempDir())
	l := Open(path)
	l.Record(Entry{ID: "a", Title: "t1"})
	l.Record(Entry{ID: "b", Title: "t2"})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	Open(path)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("file was rewritten although nothing needed pruning")
	}
}

func TestNilLogIsSafe(t *testing.T) {
	var l *Log
	l.Record(Entry{Title: "ignored"}) // must not panic
}

func TestConcurrentRecords(t *testing.T) {
	path := Path(t.TempDir())
	l := Open(path)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Record(Entry{ID: "x", Title: "t"})
		}()
	}
	wg.Wait()
	if got := readEntries(t, path); len(got) != 20 {
		t.Fatalf("entries = %d, want 20", len(got))
	}
}

func TestPathJoins(t *testing.T) {
	if got := Path("dir"); got != filepath.Join("dir", "alert-log.jsonl") {
		t.Fatalf("Path = %q", got)
	}
}
