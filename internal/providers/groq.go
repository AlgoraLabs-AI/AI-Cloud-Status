package providers

func init() {
	// Best-effort: skipped gracefully if it does not expose a public summary.json.
	Register(Provider{
		ID: "groq", Name: "Groq", Category: CategoryAI, Kind: KindStatuspage,
		URL: "https://groqstatus.com/api/v2/summary.json", Optional: true,
	})
}
