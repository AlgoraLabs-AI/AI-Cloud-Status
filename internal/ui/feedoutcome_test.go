package ui

import (
	"testing"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// TestFeedOutcomeCoversEveryFailureClass pins the mapping from the classifier's
// vocabulary to the coverage record's: a class added to one and forgotten by
// the other falls through to "transport" with no test noticing. A challenge
// is filed as "http" on purpose: it is an HTTP answer that refused the request
// shape, and the record keeps its coarse vocabulary.
func TestFeedOutcomeCoversEveryFailureClass(t *testing.T) {
	want := map[providers.FeedFailure]string{
		providers.FeedFailNone:        history.OutcomeReadable,
		providers.FeedFailOther:       history.OutcomeTransport,
		providers.FeedFailDNS:         history.OutcomeTransport,
		providers.FeedFailTimeout:     history.OutcomeTransport,
		providers.FeedFailTLS:         history.OutcomeTransport,
		providers.FeedFailHTTP:        history.OutcomeHTTP,
		providers.FeedFailUnparseable: history.OutcomeParse,
		providers.FeedFailChallenged:  history.OutcomeHTTP,
	}
	for k, w := range want {
		if got := feedOutcome(k); got != w {
			t.Errorf("feedOutcome(%v) = %q, want %q", k, got, w)
		}
	}
	// Every class the classifier can return is in the table above; the bound
	// is the classifier's own, so a class added there is a failure here.
	for k := providers.FeedFailNone; k < providers.FeedFailureCount; k++ {
		if _, ok := want[k]; !ok {
			t.Errorf("FeedFailure %d (%v) has no expected outcome in this test", k, k)
		}
	}
}
