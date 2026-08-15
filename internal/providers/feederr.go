package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
)

// FeedFailure names WHY a status feed could not be read. Every one of these ends
// in the same UI state — health UNKNOWN, never an outage — but they do not have
// the same answer: a rejected certificate is a fact about the reader's own
// network, an HTTP 503 is a fact about the provider, and a parse failure is a
// fact about this app. Collapsing all three into "status feed unavailable" sent
// the reader looking at the wrong end of the wire (2026-08-13: a corporate TLS
// proxy re-signed status.anthropic.com and the row said only "unavailable").
type FeedFailure int

const (
	FeedFailNone        FeedFailure = iota // no error
	FeedFailOther                          // the connection failed for some other reason
	FeedFailDNS                            // the host name did not resolve
	FeedFailTimeout                        // no answer within the client timeout
	FeedFailTLS                            // the HTTPS certificate was rejected
	FeedFailHTTP                           // answered, but not 200
	FeedFailUnparseable                    // answered 200 with bytes no adapter understood
)

// String is the short, stable token used in log lines (`reason=certificate`).
// It is deliberately NOT the user-facing text — that lives in the i18n catalogs.
func (f FeedFailure) String() string {
	switch f {
	case FeedFailNone:
		return "none"
	case FeedFailDNS:
		return "dns"
	case FeedFailTimeout:
		return "timeout"
	case FeedFailTLS:
		return "certificate"
	case FeedFailHTTP:
		return "http-status"
	case FeedFailUnparseable:
		return "unparseable"
	default:
		return "connection"
	}
}

// StatusError is the error a feed answering with a non-200 status yields. It is
// a type rather than a formatted string so the status code survives to the UI
// and the log without anyone parsing an error message back apart.
type StatusError struct {
	Provider string
	Code     int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: unexpected status %d", e.Provider, e.Code)
}

// ErrUnparseable marks "we got bytes and could not understand them", wrapping
// the adapter's own error. It separates a broken FEED from a broken CONNECTION,
// which look identical once both are just an error value.
var ErrUnparseable = errors.New("status feed could not be parsed")

// ClassifyFeedFailure reports why a check failed, plus the HTTP status code when
// the failure is FeedFailHTTP (0 otherwise).
func ClassifyFeedFailure(err error) (FeedFailure, int) {
	if err == nil {
		return FeedFailNone, 0
	}
	var se *StatusError
	if errors.As(err, &se) {
		return FeedFailHTTP, se.Code
	}
	if errors.Is(err, ErrUnparseable) {
		return FeedFailUnparseable, 0
	}
	// TLS is tested before the generic transport buckets: a rejected certificate
	// arrives wrapped in the same *net.OpError / *url.Error as any other dial
	// failure, and "the connection failed" is the wrong sentence for "something
	// on this network re-signed the certificate".
	var certVerify *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &certVerify) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &recordHeader) {
		return FeedFailTLS, 0
	}
	// A DNS lookup that timed out is reported as a timeout: "the name does not
	// resolve" and "the resolver never answered" send the reader to different
	// places.
	var dns *net.DNSError
	if errors.As(err, &dns) {
		if dns.IsTimeout {
			return FeedFailTimeout, 0
		}
		return FeedFailDNS, 0
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return FeedFailTimeout, 0
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return FeedFailTimeout, 0
	}
	return FeedFailOther, 0
}
