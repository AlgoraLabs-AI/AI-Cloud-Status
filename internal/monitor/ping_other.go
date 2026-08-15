//go:build !windows

package monitor

import (
	"context"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// icmpProbe runs one ICMP echo over an UNPRIVILEGED datagram socket (Linux/macOS
// allow this when net.ipv4.ping_group_range covers the user). The bool reports
// whether ICMP was usable; a permission/setup error returns false so the caller
// falls back to TCP rather than demanding root.
func icmpProbe(ctx context.Context, host string, timeout time.Duration) (ProbeResult, bool) {
	pinger, err := probing.NewPinger(host)
	if err != nil {
		return ProbeResult{}, false
	}
	pinger.Count = 1
	pinger.Timeout = timeout
	pinger.SetPrivileged(false) // unprivileged UDP datagram ICMP

	done := make(chan error, 1)
	go func() { done <- pinger.Run() }()

	select {
	case <-ctx.Done():
		pinger.Stop()
		return ProbeResult{}, false
	case err = <-done:
	}
	if err != nil {
		return ProbeResult{}, false // not usable here → fall back to TCP
	}

	st := pinger.Statistics()
	if st.PacketsRecv > 0 {
		return ProbeResult{Success: true, RTT: st.AvgRtt, Mode: ModeICMP}, true
	}
	return ProbeResult{Success: false, Mode: ModeICMP}, true // ICMP worked, host silent
}
