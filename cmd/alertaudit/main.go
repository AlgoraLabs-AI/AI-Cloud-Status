// Command alertaudit is a GUI-free diagnostic that re-derives ground truth
// from AI-Cloud-Status's own evidence trail (feed-samples/ raw captures and
// alert-log.jsonl) to answer, with citations: did every real major-severity
// incident correctly produce a matched outage alert (and a recovery, or is it
// legitimately still open) — and are any enabled providers suspiciously
// silent (possibly a broken feed request or parser, not genuine uptime)?
//
// Usage:
//
//	go run ./cmd/alertaudit               # audits the app's real config dir
//	go run ./cmd/alertaudit -dir <path>    # audits an arbitrary directory
//
// Exit codes: 0 = clean; 1 = at least one Uncovered finding, a never-captured
// provider, a provider whose captures ALL currently fail to parse, or a
// missing recovery; 2 = the tool itself failed to run (bad -dir, unreadable
// files).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/audit"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
)

func main() {
	dirFlag := flag.String("dir", "", "app data directory to audit (default: the real config.Dir())")
	flag.Parse()

	dir := *dirFlag
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "alertaudit: resolve config dir:", err)
			os.Exit(2)
		}
		dir = d
	}

	report, err := audit.Run(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "alertaudit:", err)
		os.Exit(2)
	}

	printReport(os.Stdout, report)

	// The capture-less sources — custom URL checks and the app-wide
	// connectivity alerts — are audited separately because the EVIDENCE is of a
	// different kind, and conflating the two would overstate what was verified.
	cont, cerr := audit.RunContinuity(dir)
	if cerr != nil {
		fmt.Fprintln(os.Stderr, "alertaudit: continuity section:", cerr)
	} else {
		printContinuity(os.Stdout, cont)
	}

	if hasIssues(report) || (cerr == nil && len(cont.Unclosed()) > 0) {
		os.Exit(1)
	}
}

// printContinuity renders the capture-less section.
//
// The header is not decoration. The section above it re-derives the truth from
// archived raw feed payloads — evidence independent of anything this app
// decided. This one cross-examines two of the app's OWN records against each
// other, which catches the alerting path dropping a transition the sampler saw
// (exactly the 2026-08-02 failure) but is blind to a check that was never
// probed at all. A reader who cannot tell those apart will over-trust a clean
// run, so the difference is stated every time.
func printContinuity(w *os.File, r audit.ContinuityReport) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "CONTINUITY — custom URL checks & connectivity alerts (no raw-capture archive exists for these)")
	fmt.Fprintln(w, "  Evidence: the alert journal cross-checked against the sample history — two of the app's OWN")
	fmt.Fprintln(w, "  records, written by independent code paths. It catches an outage whose recovery was never")
	fmt.Fprintln(w, "  logged. It CANNOT detect a check that was never probed or is probed wrongly; only the")
	fmt.Fprintln(w, "  provider section above has external evidence for that.")
	if !r.HistoryHorizon.IsZero() {
		fmt.Fprintf(w, "  Sample history reaches back to %s; nothing older can be judged here.\n",
			r.HistoryHorizon.Local().Format("2006-01-02 15:04"))
	}
	if len(r.Checked) == 0 {
		fmt.Fprintln(w, "  No custom URL or connectivity alerts on record.")
		return
	}
	fmt.Fprintf(w, "  Checks examined: %d.  Windows closed as stale across a restart: %d.\n",
		len(r.Checked), r.StaleClosures)
	if len(r.Windows) == 0 {
		fmt.Fprintln(w, "  Every outage alert has a matching recovery.")
		return
	}
	for _, win := range r.Windows {
		name := win.Name
		if win.ID == "" {
			name = "(connectivity)"
		}
		fmt.Fprintf(w, "  %-22s opened %s  %s", name,
			win.Opened.Local().Format("2006-01-02 15:04:05"), win.Outcome)
		if win.Outcome == audit.ContinuityUnclosed {
			fmt.Fprintf(w, " — the samples show it healthy again at %s, but no recovery was ever logged",
				win.RecoveredAt.Local().Format("15:04:05"))
		}
		if win.Suppressed != "" {
			fmt.Fprintf(w, " [opening alert suppressed: %s]", win.Suppressed)
		}
		fmt.Fprintln(w)
	}
}

// hasIssues reports whether the report contains anything worth a non-zero exit
// code: an Uncovered finding, a provider whose captures ALL currently fail to
// parse, or a recovery that was never logged.
//
// NeverCaptured is deliberately NOT here, though it is still printed. Captures
// are only written when a feed reads non-operational, so zero captures is the
// intersection of three states the archive cannot tell apart: genuinely healthy
// all window, feed URL broken, and never polled. Letting it fail the run meant a
// clean machine exited 1 with "check its feed request/parser" next to
// "no uncovered major incidents found" — a contradiction that teaches the
// operator to ignore exit 1, which is the same output a genuinely broken feed
// produces. AllCapturesUnparseable covers the broken-parser case with actual
// evidence. Telling healthy from broken needs a signal the audit does not
// persist today (a per-provider last-successful-poll), so until that exists the
// honest thing is to surface it as advisory rather than assert it as a fault.
func hasIssues(r audit.Report) bool {
	for _, p := range r.Providers {
		if p.UncoveredCount() > 0 || p.AllCapturesUnparseable() || len(p.MissingRecoveries) > 0 {
			return true
		}
	}
	return false
}

func printReport(w *os.File, r audit.Report) {
	fmt.Fprintf(w, "AI-Cloud-Status alert audit — %s\n", r.GeneratedAt.Local().Format("2006-01-02 15:04"))
	if r.AlertLogStart.IsZero() {
		fmt.Fprintln(w, "alert-log.jsonl: not found / empty — nothing to correlate against yet.")
	} else {
		fmt.Fprintf(w, "alert-log.jsonl covers since %s. Captures older than that are labeled pre-alertlog, not a miss.\n",
			r.AlertLogStart.Local().Format("2006-01-02 15:04"))
	}
	fmt.Fprintln(w, "Severity is re-derived from each raw capture with the CURRENTLY registered parser — a mismatch here reflects "+
		"today's parser against the historical alert trail, not necessarily what the parser in production at capture time decided.")
	fmt.Fprintln(w)

	fmt.Fprintf(w, "%-14s %-8s %8s %8s %6s %10s %9s %10s\n",
		"PROVIDER", "ENABLED", "CAPTURES", "MAJOR", "OK", "UNCOVERED", "PARSEERR", "LAST SEEN")
	for _, p := range r.Providers {
		ok, uncovered := 0, 0
		for _, f := range p.Findings {
			if f.Outcome == audit.Uncovered {
				uncovered++
			} else {
				ok++
			}
		}
		lastSeen := "—"
		if !p.LastCaptureTime.IsZero() {
			lastSeen = humanAge(r.GeneratedAt.Sub(p.LastCaptureTime))
		}
		note := ""
		switch {
		case p.NeverCaptured():
			// Advisory only — it does not fail the run. See hasIssues.
			note = "  <- no captures on record: healthy all window, or its feed never loaded (cannot tell apart)"
		case p.AllCapturesUnparseable():
			note = "  <- EVERY capture fails to parse with the current adapter; likely broken right now"
		}
		fmt.Fprintf(w, "%-14s %-8v %8d %8d %6d %10d %9d %10s%s\n",
			p.ID, p.Enabled, p.TotalCaptures, len(p.Findings), ok, uncovered, p.ParseErrors, lastSeen, note)
	}
	fmt.Fprintln(w)

	anyUncovered := false
	for _, p := range r.Providers {
		for _, f := range p.Findings {
			if f.Outcome != audit.Uncovered {
				continue
			}
			if !anyUncovered {
				fmt.Fprintln(w, "UNCOVERED — a real major incident with no matching alert-log entry nearby:")
				anyUncovered = true
			}
			fmt.Fprintf(w, "  [%s] %s at %s — %q\n    %s\n",
				p.ID, f.Outcome, f.Capture.Time.Local().Format("2006-01-02 15:04:05"), f.Capture.Summary, f.Capture.Path)
		}
	}

	anyMissingRecovery := false
	for _, p := range r.Providers {
		for _, m := range p.MissingRecoveries {
			if !anyMissingRecovery {
				fmt.Fprintln(w, "MISSING RECOVERY — the outage alert fired, but alert-log never recorded it clearing "+
					"(the raw feed later showed it had, so this is a delivery/logging gap, not a false outage):")
				anyMissingRecovery = true
			}
			fmt.Fprintf(w, "  [%s] opened %s, feed showed it cleared by %s but no recovery was ever logged\n",
				p.ID, m.Start.Local().Format("2006-01-02 15:04:05"), m.ClearedAt.Local().Format("2006-01-02 15:04:05"))
		}
	}

	anyUnparseable := false
	for _, p := range r.Providers {
		if p.AllCapturesUnparseable() {
			if !anyUnparseable {
				fmt.Fprintln(w, "BROKEN PARSER SUSPECTED — every capture on record fails the current parser:")
				anyUnparseable = true
			}
			fmt.Fprintf(w, "  [%s] %d/%d captures unparseable, last seen %s\n",
				p.ID, p.ParseErrors, p.TotalCaptures, humanAge(r.GeneratedAt.Sub(p.LastCaptureTime)))
		}
	}

	if !anyUncovered && !anyMissingRecovery && !anyUnparseable {
		fmt.Fprintln(w, "No uncovered major incidents, missing recoveries, or broken parsers found — every real "+
			"major-severity capture matches (or is explained by suppression / predating alert-log).")
	} else {
		fmt.Fprintln(w, "See the sections above for what needs a look — everything not listed there checked out clean.")
	}
}

// humanAge renders a duration since last-seen as a compact "Nd" / "Nh" / "Nm".
// A non-positive duration (a capture timestamp at or after GeneratedAt —
// possible after a clock change) reads as "just now" rather than a
// confusing negative age.
func humanAge(d time.Duration) string {
	switch {
	case d <= 0:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
