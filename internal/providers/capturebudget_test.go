package providers

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSample plants a capture of the given size with the given age.
func writeSample(t *testing.T, root, provider string, size int, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, provider)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Incompressible content, so the sizes the test reasons about are the sizes
	// on disk — the point here is the eviction policy, not the compressor.
	blob := make([]byte, size)
	for i := range blob {
		blob[i] = byte(i * 7919 % 251)
	}
	name := fmt.Sprintf("%s-%d.bin", provider, time.Now().Add(-age).UnixNano())
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

func dirBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(root, func(_ string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		total += fi.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

// TestOneChattyProviderCannotEvictTheOthers is the regression for what was
// measured on a real install: after one month the corpus held 202 MB, and
// Cloudflare alone was 166 MB of it across 828 captures. Age was the only bound,
// and age does not bound size.
//
// The per-provider cap is what makes this a fair corpus rather than a Cloudflare
// archive: the whole value of these captures is COVERAGE of distinct feed
// shapes, and several providers have never been observed mid-incident at all.
// A purely global cap would let the loudest feed evict exactly the evidence that
// is hardest to collect.
func TestOneChattyProviderCannotEvictTheOthers(t *testing.T) {
	root := t.TempDir()

	// The chatty one: far more than its share, oldest first.
	for i := 0; i < 40; i++ {
		writeSample(t, root, "cloudflare", 256<<10, time.Duration(100-i)*time.Hour)
	}
	// The rare, precious ones: a couple of small captures each.
	quiet := []string{"azure", "googlecloud", "betterstack"}
	for _, p := range quiet {
		writeSample(t, root, p, 8<<10, 2*time.Hour)
		writeSample(t, root, p, 8<<10, time.Hour)
	}

	pruneToBudget(root)

	for _, p := range quiet {
		entries, err := os.ReadDir(filepath.Join(root, p))
		if err != nil {
			t.Fatalf("%s directory is gone entirely: %v", p, err)
		}
		if len(entries) != 2 {
			t.Errorf("%s kept %d captures, want both — a chatty provider evicted a quiet one", p, len(entries))
		}
	}

	if got := dirBytes(t, filepath.Join(root, "cloudflare")); got > perProviderBytes {
		t.Errorf("cloudflare holds %d bytes, want at most %d", got, perProviderBytes)
	}
	if got := dirBytes(t, root); got > totalBytes {
		t.Errorf("corpus holds %d bytes, want at most %d", got, totalBytes)
	}
}

// TestBudgetEvictsOldestFirst: the newest capture of a feed is the one most
// likely to still match the parser someone is debugging, so eviction must take
// from the old end.
func TestBudgetEvictsOldestFirst(t *testing.T) {
	root := t.TempDir()
	oldest := writeSample(t, root, "aws", 3<<20, 72*time.Hour)
	newest := writeSample(t, root, "aws", 3<<20, time.Hour)

	pruneToBudget(root)

	if _, err := os.Stat(newest); err != nil {
		t.Errorf("the newest capture was evicted: %v", err)
	}
	if _, err := os.Stat(oldest); err == nil {
		t.Error("the oldest capture survived while the budget was exceeded")
	}
}

// TestGlobalCapBoundsTheWholeCorpus: the per-provider cap alone leaves the total
// growing with the number of providers, and providers get added over time.
func TestGlobalCapBoundsTheWholeCorpus(t *testing.T) {
	root := t.TempDir()
	// Enough providers, each just under its own cap, to blow the global budget.
	for i := 0; i < 16; i++ {
		p := fmt.Sprintf("provider%02d", i)
		for j := 0; j < 3; j++ {
			writeSample(t, root, p, 1<<20, time.Duration(i*10+j)*time.Hour)
		}
	}
	if before := dirBytes(t, root); before <= totalBytes {
		t.Fatalf("fixture is %d bytes, needs to exceed the %d budget to test it", before, totalBytes)
	}

	pruneToBudget(root)

	if got := dirBytes(t, root); got > totalBytes {
		t.Errorf("corpus holds %d bytes after pruning, want at most %d", got, totalBytes)
	}
}

// TestCapturesAreStoredCompressed pins the compression that makes the budget
// affordable: status feeds are JSON/XML and compress ~10:1, which is what lets a
// 32 MiB corpus hold about as much evidence as the old unbounded 200 MB did.
func TestCapturesAreStoredCompressed(t *testing.T) {
	root := t.TempDir()
	// A realistic status payload: repetitive JSON, which is what these are.
	payload := []byte(`{"incidents":[` +
		string(bytes.Repeat([]byte(`{"id":"abc","status":"investigating","name":"Elevated errors"},`), 400)) +
		`{"id":"z","status":"resolved"}]}`)

	c := NewFeedCapture(root)
	c.Capture(Provider{ID: "acme", Name: "Acme"}, payload, Result{Severity: SevMajor}, nil)

	var found string
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			found = p
		}
		return err
	})
	if err != nil || found == "" {
		t.Fatalf("no capture written: %v", err)
	}
	fi, err := os.Stat(found)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() >= int64(len(payload)) {
		t.Errorf("capture is %d bytes for a %d byte payload — it is not compressed", fi.Size(), len(payload))
	}

	// And it must still be readable, or the corpus is worthless.
	raw, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("capture does not gunzip: %v", err)
	}
	defer zr.Close()
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("decompressed capture does not match the payload that was archived")
	}
}

// TestGlobalCapDoesNotEmptyAProvider is the regression for a bug found on a real
// install, not in a fixture: after the budget shipped, Hugging Face went from
// 223 captures to ZERO while Cloudflare and AWS sat at ~3.9 MiB each.
//
// The cause was the global pass sorting every surviving file by age and deleting
// oldest-first. Age is not distributed evenly across providers, so whichever
// provider's captures happen to be oldest gets emptied completely — undoing, one
// pass later, exactly the fairness the per-provider cap had just enforced.
//
// The earlier fairness test missed it because its fixture never exceeded the
// GLOBAL cap, so the second pass never ran. This one makes sure it does.
func TestGlobalCapDoesNotEmptyAProvider(t *testing.T) {
	root := t.TempDir()

	// The rare provider: small, and the OLDEST captures in the whole corpus —
	// which is what made it the victim.
	for i := 0; i < 6; i++ {
		writeSample(t, root, "huggingface", 64<<10, time.Duration(900+i)*time.Hour)
	}
	// Chatty providers: recent, and each right at its own per-provider cap.
	for _, p := range []string{"cloudflare", "aws", "github", "openai", "anthropic",
		"azure", "mistral", "cohere", "groq", "perplexity"} {
		for j := 0; j < 4; j++ {
			writeSample(t, root, p, 1<<20, time.Duration(j)*time.Hour)
		}
	}

	if before := dirBytes(t, root); before <= totalBytes {
		t.Fatalf("fixture is %d bytes; it must exceed the %d global cap to exercise pass 2", before, totalBytes)
	}

	pruneToBudget(root)

	entries, err := os.ReadDir(filepath.Join(root, "huggingface"))
	if err != nil {
		t.Fatalf("the rare provider's directory is gone entirely: %v", err)
	}
	if len(entries) == 0 {
		t.Error("the global cap emptied the rare provider — the per-provider cap it just survived meant nothing")
	}
	if got := dirBytes(t, root); got > totalBytes {
		t.Errorf("corpus holds %d bytes, want at most %d", got, totalBytes)
	}
}

// TestGlobalCapTakesFromTheBiggest pins the eviction ORDER of the global pass:
// pressure has to fall on whoever has the most to spare.
func TestGlobalCapTakesFromTheBiggest(t *testing.T) {
	root := t.TempDir()
	small := 2
	for i := 0; i < small; i++ {
		writeSample(t, root, "azure", 32<<10, time.Duration(500+i)*time.Hour) // old AND small
	}
	for _, p := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		for j := 0; j < 4; j++ {
			writeSample(t, root, p, 1<<20, time.Duration(j)*time.Hour)
		}
	}

	pruneToBudget(root)

	entries, err := os.ReadDir(filepath.Join(root, "azure"))
	if err != nil {
		t.Fatalf("azure directory gone: %v", err)
	}
	if len(entries) != small {
		t.Errorf("azure kept %d of %d captures; the smallest provider should not be touched first", len(entries), small)
	}
}
