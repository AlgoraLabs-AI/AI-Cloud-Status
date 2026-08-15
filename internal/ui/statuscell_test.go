package ui

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// certRejected is the error shape a TLS proxy re-signing a status page produces.
func certRejected() error {
	return &tls.CertificateVerificationError{
		Err: x509.HostnameError{Certificate: &x509.Certificate{}, Host: "status.anthropic.com"},
	}
}

// TestStatusBadgeStaysInsideItsColumn is the regression for the overlap in the
// 2026-08-13 screenshot: "Status feed unavailable" was printed straight through
// the Last-checked timestamp of the same row ("Status feed unavaila23:40:38"),
// because Fyne reports a plain label's MinSize as its FULL text width and never
// clips. Every state, in every language, must fit the Status column — the ones
// too long for one line wrap inside it instead of spilling into the next column.
func TestStatusBadgeStaysInsideItsColumn(t *testing.T) {
	reducedMotionPref = true // the dot animation needs a running driver
	defer func() { reducedMotionPref = false }()
	defer i18n.Set("en")

	states := []statusState{stateOK, stateDegraded, stateOutage, stateUnreachable, stateNoFeed, statePending, stateOfflineUnknown}
	limit := tableColWidths[colStatus]
	for _, lang := range i18n.Languages() {
		i18n.Set(lang.Code)
		for _, state := range states {
			c := newRowTestController()
			rw := c.newRowWidget(rowSpec{id: "p", name: "P", state: state}, testCtx(time.Now().Add(30*time.Second)))
			if got := rw.grid.Objects[colStatus].MinSize().Width; got > limit {
				t.Errorf("language %q, state %v (%q): status cell needs %.1fpx, column is %.0fpx — it will paint over Last checked",
					lang.Code, state, visualFor(state).Label, got, limit)
			}
		}
	}
}

// TestBadgeHeightFollowsTheCurrentLabel guards the in-place update: the badge
// label is SetText, never rebuilt, so its layout must measure the text the label
// holds NOW. Measuring a string captured at construction left a two-line state's
// height behind on a row that had gone back to a one-line one.
func TestBadgeHeightFollowsTheCurrentLabel(t *testing.T) {
	reducedMotionPref = true
	defer func() { reducedMotionPref = false }()

	c := newRowTestController()
	ctx := testCtx(time.Now().Add(30 * time.Second))
	rw, _ := c.ensureRow(rowSpec{id: "p", name: "P", state: stateNoFeed}, ctx)
	tall := rw.grid.Objects[colStatus].MinSize().Height

	// Same id: ensureRow must hand back the SAME row, updated in place.
	if again, _ := c.ensureRow(rowSpec{id: "p", name: "P", state: stateOK}, ctx); again != rw {
		t.Fatal("ensureRow returned a different row for the same id")
	}
	if rw.badgeLabel.Text != visualFor(stateOK).Label {
		t.Fatalf("badge label = %q, want the operational label", rw.badgeLabel.Text)
	}
	if short := rw.grid.Objects[colStatus].MinSize().Height; short >= tall {
		t.Errorf("status cell height stayed %.1fpx after the two-line state became a one-line one (was %.1fpx)", short, tall)
	}
}

// TestFeedFailureMessageNamesTheCause pins the mapping from "why did the check
// fail" to the sentence the row shows. The old single sentence said only that
// the feed was unavailable, which for a certificate rejection pointed the reader
// at the provider when the fault was on their own network.
func TestFeedFailureMessageNamesTheCause(t *testing.T) {
	defer i18n.Set("en")
	i18n.Set("en")
	t.Run("certificate", func(t *testing.T) {
		got := feedFailureMessage(&url.Error{Op: "Get", Err: certRejected()})
		if got != i18n.T().FeedErrCertificate {
			t.Errorf("certificate failure rendered %q", got)
		}
	})
	t.Run("dns", func(t *testing.T) {
		got := feedFailureMessage(&url.Error{Op: "Get", Err: &net.DNSError{Err: "no such host", IsNotFound: true}})
		if got != i18n.T().FeedErrDNS {
			t.Errorf("dns failure rendered %q", got)
		}
	})
	t.Run("http status carries the code", func(t *testing.T) {
		got := feedFailureMessage(&providers.StatusError{Provider: "Anthropic", Code: 503})
		if !strings.Contains(got, "503") {
			t.Errorf("http failure rendered %q, want the status code in it", got)
		}
	})
	t.Run("unparseable", func(t *testing.T) {
		got := feedFailureMessage(fmt.Errorf("%w: %v", providers.ErrUnparseable, errors.New("invalid character '<'")))
		if got != i18n.T().FeedErrUnparseable {
			t.Errorf("parse failure rendered %q", got)
		}
	})
	t.Run("unknown cause falls back", func(t *testing.T) {
		got := feedFailureMessage(errors.New("connection reset by peer"))
		if got != i18n.T().StatusFeedUnavailable {
			t.Errorf("unclassified failure rendered %q, want the generic sentence", got)
		}
	})
}
