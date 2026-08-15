//go:build windows

package monitor

import (
	"context"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows IP Helper ICMP API (iphlpapi.dll). IcmpSendEcho performs a real ICMP
// echo WITHOUT requiring administrator privileges — unlike a raw ICMP socket —
// so the app pings with true ICMP RTT for a normal user.
var (
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")
)

// icmpEchoReply mirrors the Win32 ICMP_ECHO_REPLY structure. The field order and
// the trailing IP_OPTION_INFORMATION must match exactly; Go applies the same
// alignment/padding as C, so on amd64 this lays out at the expected 40 bytes.
type icmpEchoReply struct {
	Address       uint32  // replying IPv4 address
	Status        uint32  // IP_STATUS (0 == IP_SUCCESS)
	RoundTripTime uint32  // milliseconds
	DataSize      uint16  // reply data size
	Reserved      uint16  //
	Data          uintptr // PVOID — pointer to reply data
	// IP_OPTION_INFORMATION:
	TTL         uint8
	Tos         uint8
	Flags       uint8
	OptionsSize uint8
	OptionsData uintptr // PUCHAR
}

const ipSuccess = 0 // IP_SUCCESS

// icmpProbe runs one unprivileged ICMP echo via IcmpSendEcho. The bool reports
// whether ICMP was usable at all; when false the caller falls back to TCP.
func icmpProbe(ctx context.Context, host string, timeout time.Duration) (ProbeResult, bool) {
	ip, ok := resolveIPv4(host)
	if !ok {
		return ProbeResult{}, false // no IPv4 → let TCP fallback handle it
	}

	handle, _, _ := procIcmpCreateFile.Call()
	if handle == 0 || handle == uintptr(windows.InvalidHandle) {
		return ProbeResult{}, false
	}
	// The handle is closed by the goroutine AFTER IcmpSendEcho returns — NOT via a
	// defer here — because on ctx cancellation this function returns early, and
	// closing the handle while IcmpSendEcho is still in flight on it is outside the
	// API's contract. Goroutine ownership keeps the handle (and the buffers) valid
	// until the synchronous call completes.

	// IPAddr is the four octets in network order; on little-endian x86 that is
	// o0 | o1<<8 | o2<<16 | o3<<24.
	dest := uint32(ip[0]) | uint32(ip[1])<<8 | uint32(ip[2])<<16 | uint32(ip[3])<<24

	req := []byte("AI-Cloud-Status-ping")
	replySize := uint32(unsafe.Sizeof(icmpEchoReply{})) + uint32(len(req)) + 8
	reply := make([]byte, replySize)

	ms := uint32(timeout / time.Millisecond)
	if ms == 0 {
		ms = 1000
	}

	// IcmpSendEcho is synchronous and ignores ctx, so run it in a goroutine and
	// race it against ctx so app shutdown stays responsive. The req/reply buffers
	// stay referenced by the closure until the call returns (~the ICMP timeout),
	// so the early-return path can't free them out from under the API.
	type echo struct{ n uintptr }
	done := make(chan echo, 1)
	go func() {
		n, _, _ := procIcmpSendEcho.Call(
			handle,
			uintptr(dest),
			uintptr(unsafe.Pointer(&req[0])),
			uintptr(uint16(len(req))),
			0, // RequestOptions: none
			uintptr(unsafe.Pointer(&reply[0])),
			uintptr(replySize),
			uintptr(ms),
		)
		runtime.KeepAlive(req)
		runtime.KeepAlive(reply)
		procIcmpCloseHandle.Call(handle) // close only after the call has returned
		done <- echo{n}
	}()

	select {
	case <-ctx.Done():
		return ProbeResult{}, false
	case e := <-done:
		if e.n == 0 {
			// API ran but no reply (timeout/unreachable): ICMP IS usable, the host
			// just didn't answer — genuine packet loss, not a reason to fall back.
			return ProbeResult{Success: false, Mode: ModeICMP}, true
		}
		er := (*icmpEchoReply)(unsafe.Pointer(&reply[0]))
		if er.Status == ipSuccess {
			return ProbeResult{Success: true, RTT: time.Duration(er.RoundTripTime) * time.Millisecond, Mode: ModeICMP}, true
		}
		return ProbeResult{Success: false, Mode: ModeICMP}, true
	}
}
