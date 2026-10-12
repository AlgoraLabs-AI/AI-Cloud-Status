package providers

import (
	"sort"
	"sync"
)

// ParseFunc parses a provider's raw feed bytes into a Result. The Provider is
// passed so adapters can read per-provider configuration (e.g. Filter).
type ParseFunc func(p Provider, data []byte) (Result, error)

// The registry is the heart of the modular, self-registering provider system.
// Both provider definitions and feed adapters register themselves from their own
// file's init(), so:
//
//   - adding a provider on an existing backend = one new file with a Register
//     call (see bitbucket.go), and
//   - adding a whole new backend = one new adapter file with a RegisterParser
//     call (see rootly.go / betterstack.go),
//
// with NO edit to any central list. init() runs single-threaded at package load,
// but the mutex keeps Register/RegisterParser safe if ever called concurrently.
var (
	registryMu sync.Mutex
	registered []Provider
	parsers    = map[Kind]ParseFunc{}
	// legacy maps a provider ID to the Kinds its feed USED to be, in
	// registration order. A capture archived under a previous feed is still
	// that provider's capture, and ParseFeed offers it to each of these
	// adapters in turn; every one of them anchors on its own shape, so the
	// order does not decide which reads it.
	legacy = map[string][]Kind{}
)

// Register adds a provider definition to the built-in set. Intended to be called
// from a provider file's init(). Registering two providers with the same ID is a
// programmer error and panics so it is caught at startup.
func Register(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, existing := range registered {
		if existing.ID == p.ID {
			panic("providers: duplicate provider ID " + p.ID)
		}
	}
	registered = append(registered, p)
}

// RegisterParser associates a feed adapter with a Kind. Intended to be called
// from an adapter file's init(). Re-registering a Kind panics.
func RegisterParser(k Kind, fn ParseFunc) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := parsers[k]; dup {
		panic("providers: duplicate parser for kind " + string(k))
	}
	parsers[k] = fn
}

// parserFor returns the adapter registered for a Kind.
func parserFor(k Kind) (ParseFunc, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	fn, ok := parsers[k]
	return fn, ok
}

// RegisterLegacy records that provider id's feed used to be of Kind k, so an
// archived capture of that feed still parses (see ParseFeed). Intended to be
// called from the legacy adapter's init(), beside its RegisterParser, when a
// provider moves to a new backend and the old one keeps no live provider.
func RegisterLegacy(id string, k Kind) {
	registryMu.Lock()
	defer registryMu.Unlock()
	legacy[id] = append(legacy[id], k)
}

// legacyKindsFor returns the Kinds provider id's feed used to be, in
// registration order.
func legacyKindsFor(id string) []Kind {
	registryMu.Lock()
	defer registryMu.Unlock()
	return append([]Kind(nil), legacy[id]...)
}

// Default returns the registered providers, grouped by Category display order
// and then alphabetically by name for a stable, predictable layout. The returned
// slice is a fresh copy; callers may mutate it freely.
func Default() []Provider {
	registryMu.Lock()
	out := append([]Provider(nil), registered...)
	registryMu.Unlock()

	rank := map[Category]int{}
	for i, c := range CategoryOrder {
		rank[c] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank[out[i].Category], rank[out[j].Category]; ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out
}
