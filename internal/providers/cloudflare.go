package providers

func init() {
	Register(Provider{
		ID: "cloudflare", Name: "Cloudflare", Category: CategoryCloud, Kind: KindStatuspage,
		URL: "https://www.cloudflarestatus.com/api/v2/summary.json",
	})
}
