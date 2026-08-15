package providers

func init() {
	Register(Provider{
		ID: "anthropic", Name: "Anthropic", Category: CategoryAI, Kind: KindStatuspage,
		URL: "https://status.anthropic.com/api/v2/summary.json",
	})
}
