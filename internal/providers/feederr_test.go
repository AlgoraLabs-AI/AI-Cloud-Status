package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
)

// TestClassifyFeedFailure pins the one thing this classifier exists for: telling
// the reader WHICH end of the wire failed. Every error here arrives wrapped the
// way net/http actually delivers it — inside a *url.Error — because unwrapping
// is exactly what a hand-rolled string match got wrong.
func TestClassifyFeedFailure(t *testing.T) {
	wrap := func(err error) error {
		return &url.Error{Op: "Get", URL: "https://status.example.com/api/v2/summary.json", Err: err}
	}
	cases := []struct {
		name string
		err  error
		want FeedFailure
		code int
	}{
		{"nil", nil, FeedFailNone, 0},
		{
			// The 2026-08-13 case: a TLS proxy re-signed status.anthropic.com and
			// the certificate is valid for a name that is not it.
			"certificate name mismatch",
			wrap(&tls.CertificateVerificationError{Err: x509.HostnameError{Certificate: &x509.Certificate{}, Host: "status.anthropic.com"}}),
			FeedFailTLS, 0,
		},
		{"unknown authority", wrap(x509.UnknownAuthorityError{}), FeedFailTLS, 0},
		{"plain http on the tls port", wrap(tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}), FeedFailTLS, 0},
		{"dns not found", wrap(&net.DNSError{Err: "no such host", Name: "status.example.com", IsNotFound: true}), FeedFailDNS, 0},
		{"dns timeout is a timeout", wrap(&net.DNSError{Err: "i/o timeout", IsTimeout: true}), FeedFailTimeout, 0},
		{"context deadline", wrap(context.DeadlineExceeded), FeedFailTimeout, 0},
		{"http status", &StatusError{Provider: "Anthropic", Code: 503}, FeedFailHTTP, 503},
		{"unparseable body", fmt.Errorf("%w: %v", ErrUnparseable, errors.New("invalid character '<'")), FeedFailUnparseable, 0},
		{"anything else", wrap(errors.New("connection reset by peer")), FeedFailOther, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, code := ClassifyFeedFailure(tc.err)
			if got != tc.want {
				t.Errorf("ClassifyFeedFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if code != tc.code {
				t.Errorf("status code = %d, want %d", code, tc.code)
			}
		})
	}
}

// TestStatusErrorCarriesTheCode guards the reason StatusError is a type: the code
// has to survive to the UI and the log without anyone parsing it back out of a
// message.
func TestStatusErrorCarriesTheCode(t *testing.T) {
	err := error(&StatusError{Provider: "Anthropic", Code: 429})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 429 {
		t.Fatalf("errors.As did not recover the status code from %v", err)
	}
	if want := "Anthropic: unexpected status 429"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}
