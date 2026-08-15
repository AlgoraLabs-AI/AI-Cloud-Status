package ui

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/url"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/urlcheck"
)

// urlState is the latest probe result for a custom URL check.
type urlState struct {
	checked bool
	up      bool
	code    int
	latency time.Duration
	detail  string
	when    time.Time
	err     error
}

// urlModeLabel returns a localized description of a check's mode for its subtitle:
// reachability is always done, and Contains additionally verifies the page text.
func urlModeLabel(m config.URLMode) string {
	if m == config.URLModeContains {
		return i18n.T().URLModeContains
	}
	return i18n.T().URLModeReachable
}

// urlCheckID derives a stable ID from the URL ALONE (not the mode), so changing
// a check's mode keeps its enabled/mute/history state — one URL is one check.
func urlCheckID(rawURL string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(rawURL))
	return fmt.Sprintf("url-%08x", h.Sum32())
}

// defaultURLName returns a friendly default name for a URL (its host).
func defaultURLName(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// normalizeURL ensures the URL has exactly one scheme so http.Get accepts it: it
// adds https:// when no scheme is present and collapses an accidental doubled
// scheme (e.g. a paste like "https://https://x") so the host still parses.
func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	for _, s := range []string{"https://", "http://"} {
		for strings.HasPrefix(raw, s+s) {
			raw = strings.TrimPrefix(raw, s)
		}
	}
	return raw
}

// enabledURLChecks returns the URL checks that are enabled and not
// session-disabled, in config order.
func (c *Controller) enabledURLChecks() []config.URLCheck {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []config.URLCheck
	for _, u := range c.cfg.CustomURLChecks {
		if c.cfg.IsEnabled(u.ID, true) {
			out = append(out, u)
		}
	}
	return out
}

// addURLCheck validates the form, persists a new URL check, and refreshes. It
// returns an error message for the form (empty on success).
func (c *Controller) addURLCheck(rawURL, name string, mode config.URLMode, expect string) string {
	rawURL = normalizeURL(rawURL)
	t := i18n.T()
	if rawURL == "" {
		return t.URLEnterURL
	}
	if u, err := url.Parse(rawURL); err != nil || u.Host == "" {
		return t.URLInvalid
	}
	if mode == config.URLModeContains && strings.TrimSpace(expect) == "" {
		return t.URLEnterText
	}
	if strings.TrimSpace(name) == "" {
		name = defaultURLName(rawURL)
	}
	check := config.URLCheck{
		ID: urlCheckID(rawURL), Name: strings.TrimSpace(name),
		URL: rawURL, Mode: mode, Expect: strings.TrimSpace(expect),
	}
	c.mu.Lock()
	added := c.cfg.AddURLCheck(check)
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	if !added {
		return t.URLExists
	}
	c.saveCfg(cfg)
	c.populateURLChecks()
	c.forceRefreshNow()
	c.refresh()
	return ""
}

// removeURLCheck deletes a URL check and its state.
func (c *Controller) removeURLCheck(id string) {
	c.mu.Lock()
	c.cfg.RemoveURLCheck(id)
	delete(c.urlStates, id)
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	c.outages.Reset(id)
	c.saveOutageState()
	c.forgetAlertOutcome(id)
	c.saveCfg(cfg)
	c.populateURLChecks()
	c.refresh()
}

// pollURLChecksOnce probes every enabled URL check concurrently and records the
// results, raising a transition alert when one goes down or recovers.
func (c *Controller) pollURLChecksOnce(ctx context.Context) {
	checks := c.enabledURLChecks()
	var wg sync.WaitGroup
	for _, u := range checks {
		wg.Add(1)
		go func(u config.URLCheck) {
			defer wg.Done()
			out := urlcheck.Probe(ctx, u)
			now := time.Now()
			c.mu.Lock()
			c.urlStates[u.ID] = urlState{
				checked: true, up: out.Up, code: out.Code,
				latency: out.Latency, detail: out.Detail, when: now, err: out.Err,
			}
			c.mu.Unlock()
			// A transport failure while the machine itself has no internet says
			// NOTHING about the endpoint, so it is recorded as unknown rather than
			// as an outage — the same rule the provider path already applies to an
			// unreadable feed. Without it the user's own dead wifi painted a red
			// band on the strip, docked the uptime percentage, and materialised a
			// fabricated "Outage 10:29 → 10:31" in the drill-down for a service
			// that was never asked anything. A COMPLETED response is different: a
			// 500, or a 200 whose body lacked the expected text, was answered by
			// the server and stays a real failure.
			blind := out.Err != nil && c.offline.Offline()
			// Responded records that the round-trip COMPLETED, which out.Latency
			// alone cannot say: Probe times a transport failure too, so a DNS
			// error and a slow 500 both arrive here with a positive latency. The
			// detail panel's average response time needs to include the 500 (a
			// server answering slowly is the thing worth seeing) and exclude the
			// timeout (it measures the client's patience, not the server).
			c.history.Add(u.ID, history.Sample{
				Time: now, Up: out.Up, Unknown: blind,
				Responded: out.Err == nil, Latency: out.Latency,
			})
			if blind {
				// OutageTracker's contract: a check we could not judge must not be
				// fed to it at all, or it latches "down" on the user's own outage
				// and then owes a recovery alert for something that never broke.
				return
			}

			// Journaled when suppressed, like provider/target alerts, so the
			// audit trail covers custom URL checks too.
			summary := ""
			if !out.Up {
				summary = out.Detail
			}
			if n := c.outages.Update(u.ID, u.Name, summary, !out.Up); n != nil {
				// out.Err == nil means the HTTP round-trip COMPLETED — a 500 from
				// a server we reached. That is proof the machine is online, and
				// it is the single most valuable alert this feature produces, so
				// it must not be swallowed by the offline gate. Only a transport
				// error leaves "offline" as a plausible explanation.
				reason := ""
				if out.Err != nil {
					reason = c.notifySuppressedReason(u.ID)
				} else {
					reason = c.notifyGatesReason(u.ID)
				}
				c.deliver(u.ID, n, httpLinkOnly(u.URL), false, reason)
				// AFTER deliver, so the persisted state records whether the
				// opening alert was actually SHOWN. Omitting this call left the
				// whole persistence feature inert for exactly the check class
				// that motivated it: a custom URL latch reached disk only if some
				// unrelated provider happened to transition afterwards.
				c.saveOutageState()
			}
		}(u)
	}
	wg.Wait()
}

// urlStatusState maps a probe result onto the table's status states.
//
// offline is whether the MACHINE has no internet. A transport error then proves
// nothing about the endpoint, so the row reads "unknown" instead of accusing it
// of being unreachable — and, because that state ranks below OK, a local outage
// can no longer surface in the tray as a service outage. A COMPLETED response
// (a 500, or a 200 missing the expected text) is judged on its merits either
// way: the server answered.
func urlStatusState(st urlState, ok, offline bool) statusState {
	switch {
	case !ok || !st.checked:
		return statePending
	case st.err != nil && offline:
		return stateOfflineUnknown
	case st.err != nil:
		return stateUnreachable
	case st.up:
		return stateOK
	default:
		return stateOutage
	}
}

// httpLinkOnly returns raw only when it is an http(s) URL, else "". The row
// name becomes a one-click hyperlink handed to the OS protocol handler, so a
// config-supplied URL with any other scheme (file://, ms-msdt://, …) must not
// become clickable — the probe would fail on it anyway.
func httpLinkOnly(raw string) string {
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return raw
	}
	return ""
}

// urlEntries builds the status-table rows for the custom URL checks.
func (c *Controller) urlEntries(window time.Duration, isMuted func(region string) bool) []rowEntry {
	checks := c.enabledURLChecks()
	if len(checks) == 0 {
		return nil
	}
	c.mu.Lock()
	states := make(map[string]urlState, len(c.urlStates))
	for k, v := range c.urlStates {
		states[k] = v
	}
	c.mu.Unlock()

	group := sectionCustomURLs()
	offline := c.offline.Offline()
	var out []rowEntry
	for _, u := range checks {
		st, ok := states[u.ID]
		state := urlStatusState(st, ok, offline)
		latency := "—"
		if ok && st.checked {
			if st.err == nil {
				latency = formatLatency(st.latency)
			} else {
				latency = i18n.T().LatencyUnreachable
			}
		}
		up, n := c.regionUptime(u.ID, window, true, isMuted, time.Now())
		// Captured per row so the drill-down opens on THIS check's latest probe;
		// rowWidget.update reassigns activate every refresh, keeping it current.
		check, probe, probed := u, st, ok
		out = append(out, rowEntry{
			spec: rowSpec{
				id: u.ID, name: u.Name,
				activate: func() { c.showURLDetail(check, probe, probed) },
				linkURL:  httpLinkOnly(u.URL), // name → the monitored page, like provider rows
				// Redacted: the row subtitle is on screen during screenshots and
				// screen shares, and a monitored URL can legitimately carry a
				// token in its userinfo.
				subtitle:  urlModeLabel(u.Mode) + " · " + redactURL(u.URL),
				state:     state,
				when:      formatTime(st.when),
				latency:   latency,
				incidents: st.detail,
			},
			sev: downRank(state), lat: st.latency, when: st.when,
			uptime: uptimeOrUnknown(up, n), group: group,
		})
	}
	return out
}

// buildURLSection builds the side-panel "Custom URLs" block: the per-check list
// plus the add form (URL, name, mode, expected text, Test, Add).
func (c *Controller) buildURLSection() fyne.CanvasObject {
	c.urlChecks = container.NewVBox()
	c.populateURLChecks()

	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder("https://example.com")
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder(i18n.T().NamePlaceholder)
	t := i18n.T()
	expectEntry := widget.NewEntry()
	expectEntry.SetPlaceHolder(t.ExpectPlaceholder)
	expectEntry.Hide()

	// Reachability (HTTP 200 / redirect) is ALWAYS checked; the string check is an
	// independent, optional extra. Ticking it reveals the text field.
	reachNote := newWrappedLabel(t.URLReachAlways)
	stringChk := c.blurOnToggle(widget.NewCheck(t.URLStringCheck, func(on bool) {
		if on {
			expectEntry.Show()
		} else {
			expectEntry.Hide()
		}
	}))

	// mode derives the check mode from the optional string toggle. When the toggle
	// is ON it stays Contains even if the text is empty, so the user's intent isn't
	// silently dropped — addURLCheck's validation then prompts for the missing text.
	mode := func() (config.URLMode, string) {
		if stringChk.Checked {
			return config.URLModeContains, expectEntry.Text
		}
		return config.URLModeReachable, ""
	}

	result := widget.NewLabel("")
	result.Wrapping = fyne.TextWrapWord
	result.Hide()

	showResult := func(msg string) {
		result.SetText(msg)
		result.Show()
	}

	testBtn := widget.NewButtonWithIcon(t.Test, theme.SearchIcon(), func() {
		raw := normalizeURL(urlEntry.Text)
		if raw == "" {
			showResult(t.URLEnterFirst)
			return
		}
		m, expect := mode()
		showResult(t.URLTesting)
		go func() {
			out := urlcheck.Probe(context.Background(), config.URLCheck{URL: raw, Mode: m, Expect: expect})
			mark := "✓"
			if !out.Up {
				mark = "✗"
			}
			line := fmt.Sprintf("%s %s", mark, out.Detail)
			if out.Err != nil {
				line = fmt.Sprintf("%s %s: %v", mark, out.Detail, out.Err)
			} else if out.Latency > 0 {
				line += fmt.Sprintf(" · %s", formatLatency(out.Latency))
			}
			fyne.Do(func() { showResult(line) })
		}()
	})

	addBtn := widget.NewButtonWithIcon(t.AddURL, theme.ContentAddIcon(), func() {
		m, expect := mode()
		if msg := c.addURLCheck(urlEntry.Text, nameEntry.Text, m, expect); msg != "" {
			showResult(msg)
			return
		}
		urlEntry.SetText("")
		nameEntry.SetText("")
		expectEntry.SetText("")
		stringChk.SetChecked(false)
		result.Hide()
	})

	form := container.NewVBox(
		urlEntry, nameEntry, reachNote, stringChk, expectEntry,
		container.NewGridWithColumns(2, testBtn, addBtn),
		result,
	)
	// The "Custom URLs" title is provided by the enclosing accordion section.
	return container.NewVBox(c.urlChecks, form)
}

// populateURLChecks (re)builds the side-panel list of URL checks, each with an
// enable checkbox and a Remove button. Must run on the Fyne thread.
func (c *Controller) populateURLChecks() {
	if c.urlChecks == nil {
		return
	}
	c.mu.Lock()
	checks := append([]config.URLCheck(nil), c.cfg.CustomURLChecks...)
	c.mu.Unlock()

	objs := make([]fyne.CanvasObject, 0, len(checks))
	for _, u := range checks {
		chk := c.newCheck(u.ID, u.Name, true, false)
		remove := widget.NewButtonWithIcon("", theme.DeleteIcon(), func(id string) func() {
			return func() { c.removeURLCheck(id) }
		}(u.ID))
		remove.Importance = widget.LowImportance
		objs = append(objs, container.NewBorder(nil, nil, nil, remove, chk))
	}
	c.urlChecks.Objects = objs
	c.urlChecks.Refresh()
}
