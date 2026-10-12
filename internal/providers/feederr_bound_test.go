package providers

import "testing"

// TestEveryFeedFailureHasItsOwnName pins String() against the classifier's own
// bound: a class added after FeedFailChallenged without a String case would be
// logged as "connection", indistinguishable from FeedFailOther, and the
// sentinel itself must never pass for a class.
func TestEveryFeedFailureHasItsOwnName(t *testing.T) {
	seen := map[string]FeedFailure{}
	for k := FeedFailNone; k < FeedFailureCount; k++ {
		name := k.String()
		if k != FeedFailOther && name == "connection" {
			t.Errorf("FeedFailure %d has no String case: logged as %q", k, name)
		}
		if other, dup := seen[name]; dup {
			t.Errorf("FeedFailure %d and %d share the name %q", other, k, name)
		}
		seen[name] = k
	}
	if got := FeedFailureCount.String(); got != "invalid" {
		t.Errorf("the sentinel reads %q, want \"invalid\" so a leaked bound is visible", got)
	}
}
