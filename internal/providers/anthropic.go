package providers

func init() {
	Register(Provider{
		ID: "anthropic", Name: "Anthropic", Category: CategoryAI, Kind: KindStatuspage,
		// status.anthropic.com only reaches this host through a 301, so a poll
		// there costs an extra request and depends on the redirect surviving.
		URL: "https://status.claude.com/api/v2/summary.json",
	})
}
