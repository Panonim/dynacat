package dynacat

import (
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
)

var cloudStatusWidgetTemplate = mustParseTemplate("cloud-status.html", "widget-base.html")

const (
	cloudStatusAWSFeedURL       = "https://status.aws.amazon.com/rss/all.rss"
	cloudStatusGCPFeedURL       = "https://status.cloud.google.com/feed.atom"
	cloudStatusAzureFeedURL     = "https://azure.status.microsoft/en-us/status/feed/"
	cloudStatusCloudflareAPIURL = "https://www.cloudflarestatus.com/api/v2/summary.json"
)

var cloudStatusProviderOrder = []string{"aws", "gcp", "azure", "cloudflare"}
var cloudStatusHTMLTagPattern = regexp.MustCompile(`<[^>]+>`)

type cloudStatusWidget struct {
	widgetBase      `yaml:",inline"`
	Providers       []string              `yaml:"providers"`
	HideOperational bool                  `yaml:"hide-operational"`
	Entries         []cloudProviderStatus `yaml:"-"`
}

type cloudProviderStatus struct {
	ProviderKey string
	Provider    string
	Status      string
	StatusClass string
	Summary     string
	URL         string
	UpdatedAt   time.Time
}

func (widget *cloudStatusWidget) initialize() error {
	widget.withTitle("Cloud Status").withCacheDuration(5 * time.Minute)

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(5 * time.Minute)
		widget.UpdateInterval = &interval
	}

	if *widget.UpdateInterval <= 0 {
		return errors.New("update-interval must be greater than 0")
	}

	if len(widget.Providers) == 0 {
		widget.Providers = slices.Clone(cloudStatusProviderOrder)
	}

	normalized, err := normalizeCloudStatusProviders(widget.Providers)
	if err != nil {
		return err
	}

	widget.Providers = normalized

	return nil
}

func (widget *cloudStatusWidget) update(ctx context.Context) {
	results := make([]cloudProviderStatus, len(widget.Providers))
	errList := make([]error, len(widget.Providers))

	var wg sync.WaitGroup

	for i := range widget.Providers {
		provider := widget.Providers[i]

		wg.Add(1)
		go func(index int, key string) {
			defer wg.Done()

			status, err := fetchCloudProviderStatus(ctx, key)
			if err != nil {
				errList[index] = err
				results[index] = cloudProviderStatus{
					ProviderKey: key,
					Provider:    cloudStatusProviderDisplayName(key),
					Status:      "Unknown",
					StatusClass: "unknown",
					Summary:     clampText(cleanCloudStatusText(err.Error()), 160),
					URL:         cloudStatusProviderHomepage(key),
				}
				return
			}

			results[index] = status
		}(i, provider)
	}

	wg.Wait()

	failed := 0
	entries := make([]cloudProviderStatus, 0, len(results))

	for i := range results {
		if errList[i] != nil {
			failed++
		}

		if widget.HideOperational && results[i].StatusClass == "ok" {
			continue
		}

		entries = append(entries, results[i])
	}

	widget.Entries = entries

	if failed == len(results) {
		widget.canContinueUpdateAfterHandlingErr(errNoContent)
		return
	}

	if failed > 0 {
		widget.canContinueUpdateAfterHandlingErr(
			fmt.Errorf("%w: failed to fetch %d provider statuses", errPartialContent, failed),
		)
		return
	}

	widget.canContinueUpdateAfterHandlingErr(nil)
}

func (widget *cloudStatusWidget) Render() template.HTML {
	return widget.renderTemplate(widget, cloudStatusWidgetTemplate)
}

func fetchCloudProviderStatus(ctx context.Context, provider string) (cloudProviderStatus, error) {
	switch provider {
	case "aws":
		return fetchAWSCloudStatus(ctx)
	case "gcp":
		return fetchGCPCloudStatus(ctx)
	case "azure":
		return fetchAzureCloudStatus(ctx)
	case "cloudflare":
		return fetchCloudflareCloudStatus(ctx)
	default:
		return cloudProviderStatus{}, fmt.Errorf("unsupported cloud provider: %s", provider)
	}
}

func fetchAWSCloudStatus(ctx context.Context) (cloudProviderStatus, error) {
	feed, err := parseCloudStatusFeed(ctx, cloudStatusAWSFeedURL)
	if err != nil {
		return cloudProviderStatus{}, err
	}

	status := cloudProviderStatus{
		ProviderKey: "aws",
		Provider:    "AWS",
		Status:      "Operational",
		StatusClass: "ok",
		Summary:     "No active incident in latest AWS status feed entry.",
		URL:         "https://status.aws.amazon.com/",
	}

	if len(feed.Items) == 0 {
		return status, nil
	}

	item := feed.Items[0]
	title := cleanCloudStatusText(item.Title)
	details := cleanCloudStatusText(item.Description)
	combined := strings.TrimSpace(title + " " + details)

	status.Status, status.StatusClass = cloudStatusFromIncidentText(combined)
	if title != "" {
		status.Summary = title
	}
	if item.Link != "" {
		status.URL = item.Link
	}
	if item.PublishedParsed != nil {
		status.UpdatedAt = *item.PublishedParsed
	}

	return status, nil
}

func fetchGCPCloudStatus(ctx context.Context) (cloudProviderStatus, error) {
	feed, err := parseCloudStatusFeed(ctx, cloudStatusGCPFeedURL)
	if err != nil {
		return cloudProviderStatus{}, err
	}

	status := cloudProviderStatus{
		ProviderKey: "gcp",
		Provider:    "Google Cloud",
		Status:      "Operational",
		StatusClass: "ok",
		Summary:     "No active incident in latest Google Cloud status feed entry.",
		URL:         "https://status.cloud.google.com/",
	}

	if len(feed.Items) == 0 {
		return status, nil
	}

	item := feed.Items[0]
	title := cleanCloudStatusText(item.Title)
	details := cleanCloudStatusText(item.Description)
	combined := strings.TrimSpace(title + " " + details)

	status.Status, status.StatusClass = cloudStatusFromIncidentText(combined)
	if title != "" {
		status.Summary = title
	}
	if item.Link != "" {
		status.URL = item.Link
	}
	if item.PublishedParsed != nil {
		status.UpdatedAt = *item.PublishedParsed
	}

	return status, nil
}

func fetchAzureCloudStatus(ctx context.Context) (cloudProviderStatus, error) {
	feed, err := parseCloudStatusFeed(ctx, cloudStatusAzureFeedURL)
	if err != nil {
		return cloudProviderStatus{}, err
	}

	status := cloudProviderStatus{
		ProviderKey: "azure",
		Provider:    "Azure",
		Status:      "Operational",
		StatusClass: "ok",
		Summary:     "No active advisory in Azure status feed.",
		URL:         "https://azure.status.microsoft/",
	}

	if len(feed.Items) == 0 {
		return status, nil
	}

	item := feed.Items[0]
	title := cleanCloudStatusText(item.Title)
	details := cleanCloudStatusText(item.Description)
	combined := strings.TrimSpace(title + " " + details)

	status.Status, status.StatusClass = cloudStatusFromIncidentText(combined)
	if title != "" {
		status.Summary = title
	}
	if item.Link != "" {
		status.URL = item.Link
	}
	if item.PublishedParsed != nil {
		status.UpdatedAt = *item.PublishedParsed
	}

	return status, nil
}

func fetchCloudflareCloudStatus(ctx context.Context) (cloudProviderStatus, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, cloudStatusCloudflareAPIURL, nil)
	if err != nil {
		return cloudProviderStatus{}, err
	}

	request.Header.Set("User-Agent", dynacatUserAgentString)

	response, err := decodeJsonFromRequest[cloudflareStatusSummary](defaultHTTPClient, request)
	if err != nil {
		return cloudProviderStatus{}, err
	}

	status := cloudProviderStatus{
		ProviderKey: "cloudflare",
		Provider:    "Cloudflare",
		Status:      "Operational",
		StatusClass: "ok",
		Summary:     "All systems operational.",
		URL:         "https://www.cloudflarestatus.com/",
	}

	indicator := strings.ToLower(strings.TrimSpace(response.Status.Indicator))
	if indicator != "" {
		status.Status, status.StatusClass = cloudStatusFromCloudflareIndicator(indicator)
	}

	if response.Status.Description != "" {
		status.Summary = cleanCloudStatusText(response.Status.Description)
	}

	if len(response.Incidents) > 0 {
		incident := response.Incidents[0]
		if incident.Name != "" {
			status.Summary = cleanCloudStatusText(incident.Name)
		}

		if incident.Status != "" {
			status.Summary += " (" + humanizeCloudStatusText(incident.Status) + ")"
		}

		if incident.Shortlink != "" {
			status.URL = incident.Shortlink
		}

		if incident.UpdatedAt != "" {
			if parsedTime, parseErr := time.Parse(time.RFC3339, incident.UpdatedAt); parseErr == nil {
				status.UpdatedAt = parsedTime
			}
		}
	}

	return status, nil
}

type cloudflareStatusSummary struct {
	Status struct {
		Indicator   string `json:"indicator"`
		Description string `json:"description"`
	} `json:"status"`
	Incidents []struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		UpdatedAt string `json:"updated_at"`
		Shortlink string `json:"shortlink"`
	} `json:"incidents"`
}

func parseCloudStatusFeed(ctx context.Context, feedURL string) (*gofeed.Feed, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", dynacatUserAgentString)

	response, err := defaultHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf(
			"unexpected status code %d for %s, response: %s",
			response.StatusCode,
			feedURL,
			clampText(cleanCloudStatusText(string(responseBody)), 200),
		)
	}

	parser := gofeed.NewParser()

	feed, err := parser.Parse(response.Body)
	if err != nil {
		return nil, err
	}

	return feed, nil
}

func cloudStatusFromIncidentText(text string) (status string, class string) {
	lowerText := strings.ToLower(text)

	if strings.Contains(lowerText, "maintenance") ||
		strings.Contains(lowerText, "scheduled") {
		return "Maintenance", "maintenance"
	}

	if strings.Contains(lowerText, "resolved") ||
		strings.Contains(lowerText, "recovered") ||
		strings.Contains(lowerText, "available") ||
		strings.Contains(lowerText, "operational") ||
		strings.Contains(lowerText, "completed") {
		return "Operational", "ok"
	}

	if strings.Contains(lowerText, "outage") ||
		strings.Contains(lowerText, "disruption") ||
		strings.Contains(lowerText, "critical") {
		return "Outage", "error"
	}

	if strings.Contains(lowerText, "degraded") ||
		strings.Contains(lowerText, "impact") ||
		strings.Contains(lowerText, "error") {
		return "Degraded", "warn"
	}

	return "Operational", "ok"
}

func cloudStatusFromCloudflareIndicator(indicator string) (status string, class string) {
	switch indicator {
	case "none", "operational":
		return "Operational", "ok"
	case "maintenance":
		return "Maintenance", "maintenance"
	case "minor":
		return "Degraded", "warn"
	case "major", "critical":
		return "Outage", "error"
	default:
		return "Unknown", "unknown"
	}
}

func normalizeCloudStatusProviders(providers []string) ([]string, error) {
	seen := make(map[string]bool, len(providers))
	normalized := make([]string, 0, len(providers))

	for i := range providers {
		provider, err := normalizeCloudStatusProvider(providers[i])
		if err != nil {
			return nil, err
		}

		if seen[provider] {
			continue
		}

		normalized = append(normalized, provider)
		seen[provider] = true
	}

	if len(normalized) == 0 {
		return nil, errors.New("providers must contain at least one provider")
	}

	return normalized, nil
}

func normalizeCloudStatusProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "aws", "amazon", "amazon-web-services":
		return "aws", nil
	case "gcp", "google", "google-cloud":
		return "gcp", nil
	case "azure", "microsoft", "microsoft-azure":
		return "azure", nil
	case "cloudflare", "cf":
		return "cloudflare", nil
	default:
		return "", fmt.Errorf("unsupported provider %q (supported: aws, gcp, azure, cloudflare)", provider)
	}
}

func cloudStatusProviderDisplayName(provider string) string {
	switch provider {
	case "aws":
		return "AWS"
	case "gcp":
		return "Google Cloud"
	case "azure":
		return "Azure"
	case "cloudflare":
		return "Cloudflare"
	default:
		return provider
	}
}

func cloudStatusProviderHomepage(provider string) string {
	switch provider {
	case "aws":
		return "https://status.aws.amazon.com/"
	case "gcp":
		return "https://status.cloud.google.com/"
	case "azure":
		return "https://azure.status.microsoft/"
	case "cloudflare":
		return "https://www.cloudflarestatus.com/"
	default:
		return ""
	}
}

func cleanCloudStatusText(text string) string {
	clean := html.UnescapeString(text)
	clean = cloudStatusHTMLTagPattern.ReplaceAllString(clean, " ")
	clean = strings.Join(strings.Fields(clean), " ")

	return strings.TrimSpace(clean)
}

func humanizeCloudStatusText(text string) string {
	replaced := strings.ReplaceAll(strings.TrimSpace(text), "_", " ")
	replaced = strings.ReplaceAll(replaced, "-", " ")

	words := strings.Fields(strings.ToLower(replaced))
	for i := range words {
		if words[i] == "api" {
			words[i] = "API"
			continue
		}

		if words[i] == "dns" {
			words[i] = "DNS"
			continue
		}

		if len(words[i]) > 0 {
			words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
		}
	}

	return strings.Join(words, " ")
}

func clampText(text string, limit int) string {
	if limit <= 3 || len(text) <= limit {
		return text
	}

	return text[:limit-3] + "..."
}
