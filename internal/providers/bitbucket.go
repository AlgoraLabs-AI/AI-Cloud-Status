package providers

// Bitbucket is an Atlassian Statuspage. This whole file is all it took to add a
// new provider — the self-registration pattern means no central list to edit.
func init() {
	Register(Provider{
		ID: "bitbucket", Name: "Bitbucket", Category: CategoryDev, Kind: KindStatuspage,
		URL: "https://bitbucket.status.atlassian.com/api/v2/summary.json",
	})
}
