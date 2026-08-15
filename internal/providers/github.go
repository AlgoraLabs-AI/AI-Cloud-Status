package providers

func init() {
	Register(Provider{
		ID: "github", Name: "GitHub", Category: CategoryDev, Kind: KindStatuspage,
		URL: "https://www.githubstatus.com/api/v2/summary.json",
	})
}
