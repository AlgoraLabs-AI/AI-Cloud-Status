package providers

func init() {
	Register(Provider{
		ID: "openai", Name: "OpenAI", Category: CategoryAI, Kind: KindStatuspage,
		URL: "https://status.openai.com/api/v2/summary.json",
	})
}
