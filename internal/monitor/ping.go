package monitor

import (
	"context"
	"net"
	"time"
)

// ProbeMode identifies which probe path produced a result.
type ProbeMode string

const (
	ModeICMP ProbeMode = "icmp"
	ModeTCP  ProbeMode = "tcp"
)

// ProbeResult is the outcome of a single connectivity probe.
type ProbeResult struct {
	Success bool
	RTT     time.Duration
	Mode    ProbeMode
}

// fallbackPort is the TCP port used by the unprivileged fallback probe.
const fallbackPort = "443"

// Probe attempts a real ICMP echo against host using the platform's UNPRIVILEGED
// ICMP path (Windows: the IP Helper IcmpSendEcho API; Unix: an unprivileged
// datagram socket). Only if ICMP is genuinely unavailable on this host does it
// fall back to a TCP connect on port 443, so the app never requires admin/root.
// The returned Mode indicates which path ran.
//
// A reachable host that simply does not answer ICMP echoes is reported as a
// failed ICMP probe (real packet loss), not as a fallback.
func Probe(ctx context.Context, host string, timeout time.Duration) ProbeResult {
	if r, usable := icmpProbe(ctx, host, timeout); usable {
		return r
	}
	return tcpProbe(ctx, host, timeout)
}

// resolveIPv4 resolves host to a single IPv4 address (IcmpSendEcho is v4-only).
func resolveIPv4(host string) (net.IP, bool) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4, true
		}
		return nil, false
	}
	if ips, err := net.LookupIP(host); err == nil {
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil {
				return v4, true
			}
		}
	}
	return nil, false
}

// tcpProbe connects to host:443 as an unprivileged reachability check.
func tcpProbe(ctx context.Context, host string, timeout time.Duration) ProbeResult {
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, fallbackPort))
	if err != nil {
		return ProbeResult{Success: false, Mode: ModeTCP}
	}
	_ = conn.Close()
	return ProbeResult{Success: true, RTT: time.Since(start), Mode: ModeTCP}
}
