package providers

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

// captureRetention bounds how long raw feed samples are kept per provider;
// older ones are pruned so the capture directory can never grow unbounded
// forever, while still keeping months of evidence to feed parser
// improvements (mirrors alertlog's 90-day window — a file-count cap pruned
// this evidence too aggressively for chatty providers, sometimes down to a
// few days).
const captureRetention = 90 * 24 * time.Hour

// The capture corpus's share of the diagnostics disk budget, and the reason it
// needs one at all.
//
// Age was the ONLY bound here, and age is not a bound: measured on a real
// install after one month, this directory held 1834 files and 202 MB, of which
// Cloudflare alone was 166 MB across 828 captures. Its status payload is ~200 KB
// and the dedup only compares against the IMMEDIATELY previous hash, so a feed
// that alternates between two shapes writes both, forever. Nothing anywhere said
// what the ceiling was, because there wasn't one.
//
// Two caps, because one is not enough:
//
//   - perProviderBytes stops a single chatty feed from evicting everyone else.
//     That matters more than the total, because the corpus's VALUE is diversity:
//     several feeds have never been observed mid-incident at all, and 828
//     Cloudflare captures do nothing to fix that.
//   - totalBytes is the backstop across every provider, including ones added
//     later.
//
// Captures are also gzipped now (see Capture). Status feeds are JSON and XML and
// compress roughly 10:1, so this budget holds about as much evidence as the
// uncompressed 200 MB did — in a sixth of the space, with a ceiling.
const (
	perProviderBytes = 4 << 20  // 4 MiB
	totalBytes       = 32 << 20 // 32 MiB across all providers
)

// captureTimeFormat is the timestamp layout embedded in each capture's
// filename (see Capture), used to age files out on prune.
const captureTimeFormat = "20060102-150405"

// FeedCapture archives RAW provider feed bytes whenever a check observed
// something noteworthy: an active incident / non-operational severity, or a
// feed that could not be parsed. Real incident payloads are rare and are
// exactly the material needed to build and verify the per-feed parsers (notes,
// timestamps, region scoping) — several feeds (Google Cloud, Azure, Better
// Stack) have never been observed mid-incident, so their parsers stay minimal
// until this directory collects evidence.
//
// Samples land in Dir/<provider-id>/<timestamp>Z-<state>.json.gz. Consecutive
// identical payloads are deduplicated by content hash; files older than
// captureRetention are pruned per provider, and the corpus is held under a
// stated size budget (see perProviderBytes / totalBytes) rather than growing
// with the calendar.
type FeedCapture struct {
	Dir string

	// Enabled gates every capture. Per-instance rather than a package-level flag,
	// for the same reason as alertlog.Log.Enabled: nil means "always capture"
	// (what a test wants), while the app wires it to the user's diagnostics
	// setting. Consulted at WRITE time, so switching that setting off stops
	// captures on the very next poll — a capture is a third-party HTTP response
	// body written verbatim to the user's disk, so "off" has to mean off now.
	Enabled func() bool

	mu       sync.Mutex
	lastHash map[string]string
}

// NewFeedCapture returns a FeedCapture writing under dir.
func NewFeedCapture(dir string) *FeedCapture {
	return &FeedCapture{Dir: dir, lastHash: map[string]string{}}
}

// Capture archives data for provider p when the check outcome is interesting
// (see FeedCapture doc). Safe for concurrent use; failures are logged and
// never affect the check itself.
func (f *FeedCapture) Capture(p Provider, data []byte, res Result, parseErr error) {
	if f == nil || f.Dir == "" || len(data) == 0 {
		return
	}
	if f.Enabled != nil && !f.Enabled() {
		return
	}
	state := ""
	switch {
	case parseErr != nil:
		state = "parse-error"
	case len(res.Incidents) > 0 || res.Severity > SevNone:
		state = res.Severity.String()
	default:
		return // operational, nothing to learn from
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:8])
	f.mu.Lock()
	if f.lastHash == nil {
		f.lastHash = map[string]string{}
	}
	dup := f.lastHash[p.ID] == hash
	if !dup {
		f.lastHash[p.ID] = hash
	}
	f.mu.Unlock()
	if dup {
		return // same payload as the last capture — nothing new to archive
	}

	dir := filepath.Join(f.Dir, p.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Warn("feed capture mkdir failed", "provider", p.ID, "err", err)
		return
	}
	// UTC, marked with a trailing Z. The filename IS the capture's timestamp,
	// and it used to be local wall clock with no offset — while alert-log
	// entries are absolute RFC3339. Correlating the two therefore drifted by the
	// zone offset whenever the audit ran in a different zone than the capture,
	// or across a DST fall-back, which is hours against a 5-minute tolerance:
	// enough to report a correctly-alerted incident as a miss, or worse, to slide
	// a capture into a DIFFERENT incident's window and call it matched. The Z
	// marker lets the reader tell new absolute names from legacy local ones
	// instead of guessing.
	//
	// The payload is GZIPPED: status feeds are JSON and XML and compress roughly
	// 10:1, so the corpus holds about ten times the evidence per byte of the
	// budget above. ".json.gz" keeps the old name visible so a reader can still
	// tell what the file is, and the audit tool reads both this and the legacy
	// plain ".json" left by earlier builds.
	name := time.Now().UTC().Format(captureTimeFormat) + "Z-" + state + ".json.gz"
	if err := writeGzip(filepath.Join(dir, name), data); err != nil {
		slog.Warn("feed capture write failed", "provider", p.ID, "err", err)
		return
	}
	slog.Info("feed sample captured", "provider", p.ID, "state", state, "file", name)
	f.prune(dir)
	pruneToBudget(f.Dir)
}

// writeGzip writes data to path, gzipped, via a temp file and a rename so a
// crash mid-write cannot leave a truncated archive that later fails to read.
func writeGzip(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds

	zw := gzip.NewWriter(tmp)
	if _, err := zw.Write(data); err != nil {
		zw.Close()
		tmp.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// pruneToBudget enforces the two size caps: perProviderBytes within each
// provider's directory, then totalBytes across all of them. Both evict
// OLDEST-FIRST, because the newest capture of a feed is the one most likely to
// still match the parser being debugged.
//
// The per-provider pass runs first and matters most. Applying only a global cap
// would let the chattiest feed — Cloudflare, at 200 KB a capture — evict every
// other provider's evidence, which inverts what the corpus is for: coverage of
// DISTINCT feed shapes, including the several that have never been observed
// mid-incident at all.
func pruneToBudget(root string) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type sample struct {
		path string
		size int64
		mod  time.Time
	}
	kept := map[string][]sample{} // provider → surviving samples, oldest first
	used := map[string]int64{}
	var total int64

	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		sub := filepath.Join(root, d.Name())
		entries, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		var mine []sample
		var size int64
		for _, e := range entries {
			fi, err := e.Info()
			if err != nil || e.IsDir() {
				continue
			}
			mine = append(mine, sample{filepath.Join(sub, e.Name()), fi.Size(), fi.ModTime()})
			size += fi.Size()
		}
		sort.Slice(mine, func(i, j int) bool { return mine[i].mod.Before(mine[j].mod) })

		// Pass 1: this provider's own cap, oldest first.
		surviving := mine[:0]
		for _, s := range mine {
			if size > perProviderBytes {
				if os.Remove(s.path) == nil {
					size -= s.size
					continue
				}
			}
			surviving = append(surviving, s)
		}
		if len(surviving) > 0 {
			kept[d.Name()] = surviving
			used[d.Name()] = size
			total += size
		}
	}

	// Pass 2: the global cap, taken from whoever is currently BIGGEST.
	//
	// The obvious implementation — sort every surviving file by age and delete
	// oldest-first until it fits — is wrong, and wrong in the exact way the
	// per-provider cap exists to prevent. Age is not distributed evenly across
	// providers, so a provider whose captures happen to be the oldest gets
	// emptied COMPLETELY while a chattier one sits untouched at its full share.
	// That is not hypothetical: on a real install it deleted all 223 Hugging Face
	// captures and left Cloudflare and AWS at ~3.9 MiB each, which is precisely
	// the outcome pass 1 had just prevented.
	//
	// Taking from the largest keeps the corpus balanced, so pressure falls on the
	// providers with the most to spare, and a small provider's rare evidence
	// survives a squeeze it did not cause.
	for total > totalBytes {
		biggest, biggestSize := "", int64(-1)
		for name, n := range used {
			if len(kept[name]) > 0 && n > biggestSize {
				biggest, biggestSize = name, n
			}
		}
		if biggest == "" {
			return // nothing left to evict
		}
		oldest := kept[biggest][0]
		kept[biggest] = kept[biggest][1:]
		if os.Remove(oldest.path) == nil {
			used[biggest] -= oldest.size
			total -= oldest.size
		}
	}
}

// prune removes samples in dir older than captureRetention, aged off the
// timestamp embedded in each filename. A name that doesn't parse (unexpected
// shape) is left alone rather than guessed at.
func (f *FeedCapture) prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-captureRetention)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < len(captureTimeFormat) {
			continue
		}
		ts, err := time.ParseInLocation(captureTimeFormat, name[:len(captureTimeFormat)], time.Local)
		if err != nil {
			continue
		}
		if ts.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
