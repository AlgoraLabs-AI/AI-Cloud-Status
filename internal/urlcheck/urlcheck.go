// Package urlcheck probes a user-defined HTTP endpoint and reports whether it is
// "up" under one of three rules: any 2xx status, any 3xx redirect (not followed),
// or the response body containing an expected string. It performs the network I/O
// only; the UI owns scheduling, state, and alerting.
package urlcheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
)

// timeout bounds a single probe so a hung endpoint can't stall a poll cycle.
const timeout = 10 * time.Second

// maxBody caps how much of the response body is read for a contains-check.
const maxBody = 1 << 20 // 1 MiB

// Outcome is the result of a single probe.
type Outcome struct {
	Up      bool          // did the endpoint satisfy the check's rule?
	Code    int           // HTTP status code (0 if the request never completed)
	Latency time.Duration // wall-clock time of the request
	Detail  string        // short human summary for the UI ("200 OK", "302 → /login", …)
	Err     error         // transport/DNS/timeout error, if any
}

// Probe runs check once and reports the Outcome. It never panics on a bad URL or
// a dead host — those come back as Up=false with Err/Detail set.
//
// The URL is whatever the user typed and is probed as-is (no SSRF allow/deny
// list): this is a single-user desktop app monitoring the user's OWN endpoints,
// so reaching localhost or an internal host is intended, not a vulnerability.
func Probe(ctx context.Context, check config.URLCheck) Outcome {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Both modes follow redirects (Go's default client) and judge the FINAL
	// response, so a 301→200 site reads as up.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, check.URL, nil)
	if err != nil {
		return Outcome{Detail: "invalid URL", Err: err}
	}
	req.Header.Set("User-Agent", "AI-Cloud-Status")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	lat := time.Since(start)
	if err != nil {
		// A redirect loop / too many redirects also lands here.
		return Outcome{Latency: lat, Detail: "request failed", Err: err}
	}
	defer resp.Body.Close()
	code := resp.StatusCode
	ok2xx := code >= 200 && code < 300

	if check.Mode == config.URLModeContains {
		if !ok2xx {
			return Outcome{Up: false, Code: code, Latency: lat,
				Detail: fmt.Sprintf("%d %s (not checked)", code, http.StatusText(code))}
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if rerr != nil {
			return Outcome{Code: code, Latency: lat, Detail: "body read failed", Err: rerr}
		}
		found := check.Expect != "" &&
			strings.Contains(strings.ToLower(string(body)), strings.ToLower(check.Expect))
		word := "found"
		if !found {
			word = "not found"
		}
		return Outcome{Up: found, Code: code, Latency: lat,
			Detail: fmt.Sprintf("%d · %q %s", code, check.Expect, word)}
	}

	// URLModeReachable (and any legacy mode): up if the endpoint responds with a
	// success or a (resolved) redirect — final status 2xx/3xx.
	up := code >= 200 && code < 400
	return Outcome{Up: up, Code: code, Latency: lat,
		Detail: fmt.Sprintf("%d %s", code, http.StatusText(code))}
}
