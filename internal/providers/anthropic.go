package providers

func init() {
	Register(Provider{
		ID: "anthropic", Name: "Anthropic", Category: CategoryAI, Kind: KindStatuspage,
		// status.anthropic.com 301-redirects here; the app only reached the feed
		// because net/http follows redirects. Point at the real host so a poll is
		// one request and a later redirect change cannot silently break it.
		// NOTE: this is hygiene, NOT the fix for the 2026-09-05/06 405 window —
		// github and bitbucket failed identically with URLs that never changed.
		URL: "https://status.claude.com/api/v2/summary.json",
	})
}
