package providers

func init() {
	// Perplexity runs on Instatus (it migrated off Atlassian Statuspage; the
	// old-style endpoint now serves the Instatus summary shape).
	// Best-effort: skipped gracefully if it does not expose a public summary.json.
	Register(Provider{
		ID: "perplexity", Name: "Perplexity", Category: CategoryAI, Kind: KindInstatus,
		URL: "https://status.perplexity.com/api/v2/summary.json", Optional: true,
	})
}
