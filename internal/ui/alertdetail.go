package ui

import (
	"log/slog"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// An alert card names one check, and the card's body is a one-line summary of
// it. "Show" used to stop at the main window, leaving the user to find the row
// the card was already about — and on a recovery card the row is green again by
// then, so the incident that just ended is only reachable through the row's
// last-24h history. Resolving the id to its drill-down closes that gap for both
// edges: the outage card opens on the live incident, the recovery card on the
// same provider's panel where the resolved one is listed.

// alertTargetKind names which drill-down panel an alert's check id belongs to.
// The three kinds keep their state in three different places on the Controller,
// so the id has to be resolved against each in turn.
type alertTargetKind int

const (
	// alertTargetNone — the id belongs to no panel that can be opened. The
	// app-wide connectivity alerts (total loss / restored) carry no id at all,
	// and a check disabled or deleted between the alert and the click leaves an
	// id nothing answers to.
	alertTargetNone alertTargetKind = iota
	alertTargetProvider
	alertTargetURL
	alertTargetConn
)

// alertTarget is a resolved alert id together with everything its panel needs,
// snapshotted under one lock. It exists so the resolution can be tested without
// a Fyne canvas: showDetailForAlert is then a thin dispatch.
type alertTarget struct {
	kind alertTargetKind

	prov     providers.Provider
	provSt   provState
	provSeen bool // whether the provider has a polled state yet (the panel's ok)

	url     config.URLCheck
	urlSt   urlState
	urlSeen bool

	conn monitor.TargetStatus
}

// resolveAlertTarget maps an alert's check id to the panel that explains it.
//
// Resolution order is provider → custom URL → connectivity target, matching the
// order the table itself groups them; ids come from distinct namespaces
// (registry ids, "url-" prefixed ids, ping- target ids) so no id can match two.
//
// The panels each take c.mu themselves, so this returns a snapshot rather than
// calling them: holding the lock across the dispatch would deadlock.
func (c *Controller) resolveAlertTarget(id string) alertTarget {
	if id == "" {
		return alertTarget{}
	}

	c.mu.Lock()
	for _, p := range c.providers {
		if p.ID != id {
			continue
		}
		st, ok := c.provStates[id]
		c.mu.Unlock()
		return alertTarget{kind: alertTargetProvider, prov: p, provSt: st, provSeen: ok}
	}
	for _, u := range c.cfg.CustomURLChecks {
		if u.ID != id {
			continue
		}
		st, ok := c.urlStates[id]
		c.mu.Unlock()
		return alertTarget{kind: alertTargetURL, url: u, urlSt: st, urlSeen: ok}
	}
	eng := c.engine
	c.mu.Unlock()

	if eng != nil {
		for _, s := range eng.Snapshot() {
			if s.ID == id {
				return alertTarget{kind: alertTargetConn, conn: s}
			}
		}
	}
	return alertTarget{}
}

// showDetailForAlert opens the drill-down panel for the check an alert card is
// about, and reports whether one was opened. An id that resolves to nothing is
// not an error — the main window is already up, which is the rest of what Show
// promised. Must run on the Fyne thread.
func (c *Controller) showDetailForAlert(id string) bool {
	tgt := c.resolveAlertTarget(id)
	switch tgt.kind {
	case alertTargetProvider:
		c.showProviderDetail(tgt.prov, tgt.provSt, tgt.provSeen, c.regionsOfInterest())
	case alertTargetURL:
		c.showURLDetail(tgt.url, tgt.urlSt, tgt.urlSeen)
	case alertTargetConn:
		c.showConnDetail(tgt.conn)
	default:
		slog.Info("alert Show: no drill-down for this alert", "id", id)
		return false
	}
	return true
}
