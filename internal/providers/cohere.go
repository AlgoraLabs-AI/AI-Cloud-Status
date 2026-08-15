package providers

func init() {
	// Best-effort: skipped gracefully if it does not expose a public summary.json.
	Register(Provider{
		ID: "cohere", Name: "Cohere", Category: CategoryAI, Kind: KindStatuspage,
		URL: "https://status.cohere.com/api/v2/summary.json", Optional: true,
	})
}
