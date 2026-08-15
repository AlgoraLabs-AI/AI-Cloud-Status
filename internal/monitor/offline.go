package monitor

import "sync"

// MinProvidersForAllDown is the number of checked providers required before an
// "every feed is unreachable" observation is treated as the local machine being
// offline rather than coincidental per-provider outages. With only one provider
// checked we cannot distinguish "you're offline" from "that one provider's feed
// is down", so connectivity probes are the authoritative signal in that case.
const MinProvidersForAllDown = 2

// OfflineDetector decides whether the local machine appears to be offline, so
// the UI can show ONE "You're offline" banner and SUPPRESS the per-provider
// feed-unreachable noise that would otherwise storm when wifi drops. Being
// offline is distinct from a single provider's status feed being unreachable:
// the discriminator is whether connectivity probes (ICMP/TCP) are down, or
// whether EVERY checked provider feed failed at once. It is edge-triggered and
// safe for concurrent use.
type OfflineDetector struct {
	mu          sync.Mutex
	offline     bool
	initialized bool
}

// NewOfflineDetector returns a ready-to-use detector.
func NewOfflineDetector() *OfflineDetector { return &OfflineDetector{} }

// Evaluate is the pure decision function (no state): the machine is considered
// offline when connectivity probes are down, OR when at least
// MinProvidersForAllDown providers were checked this cycle and all of them had
// an unreachable feed.
//
// Positive evidence outranks both. If any provider's status page was fetched and
// parsed this cycle, something on the internet answered over HTTPS — a
// first-hand observation, against which "offline" is only an inference drawn
// from ICMP and TCP:443 probes to two fixed IPs. The two disagree permanently on
// any network that blocks direct egress but allows HTTPS through a proxy, and
// there the inference used to win: connDown latched true for the life of the
// process, so the offline banner sat on screen, checkTotalLoss stayed gated, and
// every provider outage alert was silently journaled instead of shown — while
// rows turned red and the tray read "Outage". A monitor that has just read a
// status page cannot coherently claim it has no internet.
func Evaluate(connectivityDown bool, providersChecked, providersUnreachable int) bool {
	if providersChecked > 0 && providersUnreachable < providersChecked {
		return false
	}
	if connectivityDown {
		return true
	}
	if providersChecked >= MinProvidersForAllDown && providersUnreachable == providersChecked {
		return true
	}
	return false
}

// Update folds the latest observation into the detector and reports the current
// offline state plus whether it changed since the previous observation. The
// first observation establishes the baseline and reports changed=true only if it
// is already offline (so a launch into an offline state still surfaces once).
func (d *OfflineDetector) Update(connectivityDown bool, providersChecked, providersUnreachable int) (offline, changed bool) {
	now := Evaluate(connectivityDown, providersChecked, providersUnreachable)

	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.initialized {
		d.initialized = true
		d.offline = now
		return now, now // changed only if we start offline
	}
	changed = now != d.offline
	d.offline = now
	return now, changed
}

// Offline reports the last computed offline state.
func (d *OfflineDetector) Offline() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.offline
}
