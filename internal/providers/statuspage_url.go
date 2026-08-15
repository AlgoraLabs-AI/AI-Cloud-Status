package providers

// statusPageURLs maps a provider ID to its REAL human-facing status page (the
// page a person opens in a browser), as opposed to the machine-readable feed the
// app polls. Clicking a provider name in the table opens this.
var statusPageURLs = map[string]string{
	"openai":      "https://status.openai.com",
	"anthropic":   "https://status.anthropic.com",
	"gemini":      "https://status.cloud.google.com", // Gemini health is reported under Google Cloud
	"mistral":     "https://status.mistral.ai",
	"cohere":      "https://status.cohere.com",
	"perplexity":  "https://status.perplexity.com",
	"huggingface": "https://status.huggingface.co",
	"groq":        "https://groqstatus.com",
	"xai":         "https://status.x.ai",
	"cloudflare":  "https://www.cloudflarestatus.com",
	"googlecloud": "https://status.cloud.google.com",
	"aws":         "https://health.aws.amazon.com/health/status",
	"azure":       "https://azure.status.microsoft",
	"github":      "https://www.githubstatus.com",
	"bitbucket":   "https://bitbucket.status.atlassian.com",
}

// StatusPageURL returns the human-facing status page for a provider ID, or "" if
// none is known.
func StatusPageURL(id string) string { return statusPageURLs[id] }

// StatusPage returns the page to open in a browser for this provider: its
// curated human-facing status page, falling back to the machine feed URL only
// when no human page is known. Every user-facing "open the status page" action
// must go through this — opening p.URL directly shows raw JSON/XML.
func (p Provider) StatusPage() string {
	if u := StatusPageURL(p.ID); u != "" {
		return u
	}
	return p.URL
}
