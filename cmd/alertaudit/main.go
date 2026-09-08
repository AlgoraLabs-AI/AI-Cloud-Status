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
// Exit codes: 0 = clean; 2 = the tool itself failed to run (bad -dir,
// unreadable files); 1 = at least one of:
//
//   - an Uncovered finding
//   - a provider whose captures ALL currently fail to parse
//   - an outage whose recovery was never logged, of either kind
//   - a feed whose coverage record shows a SUSTAINED, RECENT run of failed
//     reads. Not "never read": a feed that worked for months and then broke is
//     the case this exists for, so the verdict keys on the current failure run
//     rather than on whether a success has ever been recorded (see hasIssues)
//   - a continuity window left unclosed
//
// A never-captured provider is deliberately NOT in that list — zero captures is
// an absence, and hasIssues explains at length why failing the run on it teaches
// an operator to ignore exit 1.
package main

import (
	"flag"
	"fmt"
	"io"
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
func printContinuity(w io.Writer, r audit.ContinuityReport) {
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
// evidence.
//
// The per-provider attendance record this comment used to say did not exist now
// does — history.CoverageEntry — so FeedBroken IS a fault: it means a sustained,
// recent run of attempts that all failed to read the feed. That is positive
// evidence, unlike the absence NeverCaptured reports.
//
// Note what it does NOT mean: "never read". A feed that worked for months and
// then broke is precisely the case this exists to catch, so the verdict keys on
// the CURRENT failure run, not on whether a success has ever been recorded — an
// earlier version keyed on the latter and could never fire in production, since
// any working launch records a success within a minute.
//
// The two tiers are judged SEPARATELY and neither is inferred from the other: no
// capture-tier verdict rests on coverage data, and the coverage verdict never
// rests on how many captures exist. That separation is the point. Mixing them is
// how an empty capture corpus on a default install would read as "all clear"
// while the coverage record showed a feed unreadable for weeks.
func hasIssues(r audit.Report) bool {
	for _, p := range r.Providers {
		// CAPTURE tier — only meaningful when diagnostics were on.
		if p.UncoveredCount() > 0 || p.AllCapturesUnparseable() || len(p.MissingRecoveries) > 0 {
			return true
		}
		// COVERAGE tier — always available, judged on its own evidence.
		// FeedFault is the SAME predicate printReport uses for its coverage
		// section, evaluated against the SAME clock. Re-deriving it here would let
		// the exit code and the report drift apart, which is how a run exits 1
		// next to "nothing found".
		if p.FeedFault(r.GeneratedAt) {
			return true
		}
	}
	return false
}

// captureTierPhrase describes the capture tier from a measurement, so nothing
// can announce it without one.
//
// diagnosticsOn is the CONFIGURED state, read from config.json — not inferred
// from the absence of captures. An empty corpus has two very different causes
// (never switched on, or switched on and genuinely quiet) and the config says
// which without guessing.
func captureTierPhrase(present, diagnosticsOn bool) string {
	if present {
		return "CAPTURE tier present (feed-samples + alert-log)"
	}
	why := "diagnostics are OFF in config.json, so these were never written"
	if diagnosticsOn {
		why = "diagnostics are ON, so this corpus is genuinely empty rather than disabled"
	}
	return "CAPTURE tier EMPTY — no alert log and no captures, so every capture-tier verdict below is vacuous rather " +
		"than clean (" + why + ")"
}

// coverageTierPhrase describes the coverage tier, and distinguishes the three
// ways it can fail to answer: absent, present-but-unreadable (an evidence LOSS,
// which calls for a different response than a gap), and readable-but-empty.
func coverageTierPhrase(r audit.Report) string {
	switch {
	case r.HistoryPresent:
		return "COVERAGE tier present (history.json, always written)."
	case r.HistoryFound && r.HistoryErr != "":
		return fmt.Sprintf("history.json is present but could NOT be read (%s), so the coverage tier is LOST, not "+
			"missing. No coverage verdict is given below.", r.HistoryErr)
	case r.HistoryFound:
		return "history.json is readable but holds no coverage record yet (it starts filling on the next save), so no " +
			"coverage verdict can be given below."
	default:
		return "history.json is absent, so no coverage verdict can be given and 'no captures' below stays ambiguous."
	}
}

// printReport renders the provider section.
//
// It takes an io.Writer, not the *os.File it used to, so a test can point it at
// a buffer: the defect this whole change set exists to remove — a fabricated
// clearance timestamp — lived in the FORMATTING, where no test could reach it.
func printReport(w io.Writer, r audit.Report) {
	fmt.Fprintf(w, "AI-Cloud-Status alert audit — %s\n", r.GeneratedAt.Local().Format("2006-01-02 15:04"))
	if r.AlertLogStart.IsZero() {
		fmt.Fprintln(w, "alert-log.jsonl: not found / empty — nothing to correlate against yet.")
	} else {
		fmt.Fprintf(w, "alert-log.jsonl covers since %s. Captures older than that are labeled pre-alertlog, not a miss.\n",
			r.AlertLogStart.Local().Format("2006-01-02 15:04"))
	}
	// Name the evidence tiers explicitly. They are NOT interchangeable: captures
	// and the alert log exist only while diagnostics are enabled, so on a default
	// install both are empty and every capture-tier verdict below is vacuous —
	// which reads identically to "all clear" unless the report says otherwise.
	// The capture half has to be CHECKED, not assumed. Announcing "both present"
	// off HistoryPresent alone asserts a tier this line never looked at — and on
	// a default install with diagnostics off that tier is entirely empty, which
	// is the precise misreading this line exists to prevent.
	captureTier := !r.AlertLogStart.IsZero()
	for _, p := range r.Providers {
		if p.TotalCaptures > 0 {
			captureTier = true
			break
		}
	}
	// One sentence per tier, each built from its OWN measurement and joined here.
	// The previous shape was a five-branch switch that restated both facts in
	// every arm, so a fix applied to one arm left the others asserting a tier
	// they had never examined — twice, in two successive changes. Composing the
	// halves makes that shape impossible rather than merely fixed.
	fmt.Fprintf(w, "Evidence tiers: %s. %s\n",
		captureTierPhrase(captureTier, r.DiagnosticsEnabled), coverageTierPhrase(r))
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
			// Zero captures used to be a three-way ambiguity the archive could not
			// resolve. The coverage tier resolves it directly — it records whether
			// the feed was ever READ, rather than inferring health from an absence.
			switch p.FeedVerdict(r.GeneratedAt) {
			case audit.FeedBroken:
				note = fmt.Sprintf("  <- FEED NOT READABLE: %d consecutive failures (last: %s) — see the section below",
					p.Coverage.ConsecutiveFailures, p.Coverage.LastFailureClass)
			case audit.FeedHealthy:
				// States what the record actually holds, and nothing more. The
				// coverage entry is a high-water mark with no history — a success
				// clears the failure run behind it — so "no captures BECAUSE it was
				// healthy" would certify a whole window from one poll. That is the
				// unobserved-claim class this whole change set is about.
				//
				// humanAge already ends in "ago" (or is "just now").
				// "the last poll that learned anything", not "the last poll":
				// FeedHealthy is defined on the last INFORMATIVE observation, and
				// blind or skipped polls after it keep advancing LastAttempt. The
				// shorter sentence would claim a read for polls that read nothing.
				note = fmt.Sprintf("  <- no captures on record; the last poll that learned anything DID read the feed, %s",
					humanAge(r.GeneratedAt.Sub(p.Coverage.LastSuccess)))
			default:
				// Advisory only — it does not fail the run. See hasIssues. This
				// covers both "no coverage record at all" and "failing, but not
				// yet long enough to call it": neither is an assertion.
				note = "  <- no captures and nothing conclusive in the coverage record: healthy, feed never loaded, or never polled"
			}
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

	// The two Kinds get separate headers because they are separate findings. The
	// single merged header is what claimed "the raw feed later showed it had
	// cleared" for windows where nothing of the sort was ever observed — and then
	// formatted the zero ClearedAt as the literal "0000-12-31 21:00:00", printing
	// a fabricated timestamp as if it were a measurement.
	// TWO SEPARATE PASSES, not one loop with lazily-emitted headers. Interleaving
	// them means a provider carrying both kinds prints its cleared rows under
	// whichever header was emitted first — filing a proven-cleared window under
	// "may still be open" is the same misattribution this section exists to fix,
	// merely inverted.
	anyCleared, anyBounded := false, false
	for _, p := range r.Providers {
		for _, m := range p.MissingRecoveries {
			// The zero Kind is KindUnset, which asserts nothing, so a finding
			// built without naming a Kind already falls to the other pass. The
			// ClearedAt check is the remaining belt-and-braces: a finding that
			// CLAIMS ClearedByCapture without carrying a time would still print a
			// fabricated one, and a formatter should not depend on every producer
			// keeping those two fields in step.
			if m.Kind != audit.ClearedByCapture || m.ClearedAt.IsZero() {
				continue
			}
			if !anyCleared {
				fmt.Fprintln(w, "MISSING RECOVERY — the outage alert fired, but alert-log never recorded it clearing "+
					"(the raw feed later showed it had, so this is a delivery/logging gap, not a false outage):")
				anyCleared = true
			}
			fmt.Fprintf(w, "  [%s] opened %s, feed showed it cleared by %s but no recovery was ever logged\n",
				p.ID, m.Start.Local().Format("2006-01-02 15:04:05"), m.ClearedAt.Local().Format("2006-01-02 15:04:05"))
		}
	}
	for _, p := range r.Providers {
		for _, m := range p.MissingRecoveries {
			// The mirror of the guard above: anything without a real clearance
			// time belongs here, whatever its Kind says.
			if m.Kind == audit.ClearedByCapture && !m.ClearedAt.IsZero() {
				continue
			}
			if !anyBounded {
				fmt.Fprintln(w, "OUTAGE WINDOW LEFT OPEN — the outage alert fired, no recovery was ever logged, and NOTHING "+
					"in the archive shows it clearing. The window was closed by the auditor's own horizon so it could not "+
					"certify every later capture. The incident may still be open:")
				anyBounded = true
			}
			fmt.Fprintf(w, "  [%s] opened %s — no corroborating evidence that it ever cleared\n",
				p.ID, m.Start.Local().Format("2006-01-02 15:04:05"))
		}
	}
	anyMissingRecovery := anyCleared || anyBounded

	// The coverage section is driven by FeedFault, the same predicate hasIssues
	// uses, and it is INDEPENDENT of the capture count. Hanging it off the
	// per-row note instead would print nothing for a provider that has captures
	// and a broken feed — exit 1 beside a report that found nothing.
	anyFeedFault := false
	for _, p := range r.Providers {
		if !p.FeedFault(r.GeneratedAt) {
			continue
		}
		if !anyFeedFault {
			fmt.Fprintln(w, "FEED NOT READABLE — polled repeatedly and no attempt has succeeded since the last success. "+
				"This is the COVERAGE tier (history.json), independent of whether any captures exist:")
			anyFeedFault = true
		}
		last := "never"
		if !p.Coverage.LastSuccess.IsZero() {
			last = humanAge(r.GeneratedAt.Sub(p.Coverage.LastSuccess)) // already ends in "ago"
		}
		// The last-ATTEMPT age is printed too: without it a reader cannot tell a
		// live fault from a record nobody has refreshed.
		fmt.Fprintf(w, "  [%s] %d consecutive failures, last success %s, last attempt %s, last failure class %q\n",
			p.ID, p.Coverage.ConsecutiveFailures, last,
			humanAge(r.GeneratedAt.Sub(p.Coverage.LastAttempt)), p.Coverage.LastFailureClass)
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

	if !anyUncovered && !anyMissingRecovery && !anyUnparseable && !anyFeedFault {
		// "unreadable feeds" belongs in this sentence only when the coverage tier
		// could actually answer. Claiming none were found when the tier was
		// absent, corrupt or empty reports a question that was never asked as a
		// question that came back clean.
		if !r.HistoryPresent {
			fmt.Fprintln(w, "No uncovered major incidents, missing recoveries, outage windows left open, or broken "+
				"parsers found. Feed readability was NOT assessed — see the coverage tier above.")
			return
		}
		fmt.Fprintln(w, "No uncovered major incidents, missing recoveries, outage windows left open, unreadable feeds, or "+
			"broken parsers found — every real "+
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
