package dynglance

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const calendarReleaseDateLayout = "2006-01-02"

// parseCalendarReleaseURL splits a "serverType:url" value (e.g.
// "radarr:https://radarr.domain.com") into its service type and normalized base
// URL, mirroring the host URL parsing used by the latest-media/playing widgets.
func parseCalendarReleaseURL(rawURL string) (serverType string, baseURL string, err error) {
	parts := strings.SplitN(rawURL, ":", 2)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("url missing service type prefix (e.g. 'radarr:https://...')")
	}

	serverType = strings.ToLower(strings.TrimSpace(parts[0]))
	if serverType != "sonarr" && serverType != "radarr" {
		return "", "", fmt.Errorf("unsupported service type %q (must be 'sonarr' or 'radarr')", serverType)
	}

	remainingURL := strings.TrimSpace(parts[1])
	switch {
	case strings.HasPrefix(remainingURL, "//"):
		baseURL = "https:" + remainingURL
	case strings.HasPrefix(remainingURL, "http://"), strings.HasPrefix(remainingURL, "https://"):
		baseURL = remainingURL
	default:
		baseURL = "https://" + remainingURL
	}

	return serverType, strings.TrimRight(baseURL, "/"), nil
}

type arrImage struct {
	CoverType string `json:"coverType"`
	RemoteURL string `json:"remoteUrl"`
	URL       string `json:"url"`
}

func arrPosterURL(images []arrImage) string {
	for _, image := range images {
		if image.CoverType == "poster" {
			if image.RemoteURL != "" {
				return image.RemoteURL
			}
			return image.URL
		}
	}
	return ""
}

type sonarrCalendarEpisode struct {
	Title         string `json:"title"`
	AirDateUtc    string `json:"airDateUtc"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	Overview      string `json:"overview"`
	Series        struct {
		Title     string     `json:"title"`
		TitleSlug string     `json:"titleSlug"`
		Images    []arrImage `json:"images"`
	} `json:"series"`
}

type radarrCalendarMovie struct {
	Title           string     `json:"title"`
	Overview        string     `json:"overview"`
	TmdbID          int        `json:"tmdbId"`
	TitleSlug       string     `json:"titleSlug"`
	DigitalRelease  string     `json:"digitalRelease"`
	PhysicalRelease string     `json:"physicalRelease"`
	Images          []arrImage `json:"images"`
}

// getReleasesForMonth returns release items keyed by ISO date for the given
// month, serving from the per-month server cache when it is still fresh.
func (widget *calendarWidget) getReleasesForMonth(ctx context.Context, year int, month time.Month) map[string][]calendarReleaseItem {
	key := fmt.Sprintf("%04d-%02d", year, month)

	widget.releaseCacheMu.Lock()
	if entry, ok := widget.releaseCache[key]; ok && time.Since(entry.fetchedAt) < widget.releasesInterval {
		widget.releaseCacheMu.Unlock()
		return entry.data
	}
	widget.releaseCacheMu.Unlock()

	// Pad the range so spillover days shown in the grid also get markers.
	monthStart := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, -1)
	start := monthStart.AddDate(0, 0, -7)
	end := monthEnd.AddDate(0, 0, 7)

	data := make(map[string][]calendarReleaseItem)

	// Track which releases have already been added so the same movie/episode
	// served by multiple hosts of the same type is not shown twice.
	seen := make(map[string]struct{})

	for i := range widget.Hosts {
		service := &widget.Hosts[i]

		items, err := fetchCalendarReleases(ctx, service, start, end)
		if err != nil {
			slog.Warn("calendar: failed to fetch releases", "service", service.serverType, "url", service.baseURL, "error", err)
			continue
		}

		for date, dayItems := range items {
			for _, item := range dayItems {
				if item.dedupKey != "" {
					seenKey := date + "|" + item.dedupKey
					if _, exists := seen[seenKey]; exists {
						continue
					}
					seen[seenKey] = struct{}{}
				}

				data[date] = append(data[date], item)
			}
		}
	}

	widget.releaseCacheMu.Lock()
	widget.releaseCache[key] = calendarReleaseCacheEntry{fetchedAt: time.Now(), data: data}
	widget.releaseCacheMu.Unlock()

	return data
}

func fetchCalendarReleases(ctx context.Context, service *calendarReleaseService, start, end time.Time) (map[string][]calendarReleaseItem, error) {
	switch service.serverType {
	case "sonarr":
		return fetchSonarrReleases(ctx, service, start, end)
	case "radarr":
		return fetchRadarrReleases(ctx, service, start, end)
	default:
		return nil, fmt.Errorf("unknown service type %q", service.serverType)
	}
}

func newArrCalendarRequest(ctx context.Context, service *calendarReleaseService, start, end time.Time, extraParams url.Values) (*http.Request, error) {
	params := url.Values{}
	params.Set("start", start.Format(calendarReleaseDateLayout))
	params.Set("end", end.Format(calendarReleaseDateLayout))
	for key, values := range extraParams {
		for _, value := range values {
			params.Add(key, value)
		}
	}

	endpoint := service.baseURL + "/api/v3/calendar?" + params.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("X-Api-Key", service.Token)
	request.Header.Set("Accept", "application/json")

	return request, nil
}

func fetchSonarrReleases(ctx context.Context, service *calendarReleaseService, start, end time.Time) (map[string][]calendarReleaseItem, error) {
	client := ternary[requestDoer](service.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)

	request, err := newArrCalendarRequest(ctx, service, start, end, url.Values{"includeSeries": {"true"}})
	if err != nil {
		return nil, err
	}

	episodes, err := decodeJsonFromRequest[[]sonarrCalendarEpisode](client, request)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]calendarReleaseItem)

	for _, episode := range episodes {
		date := arrParseDate(episode.AirDateUtc)
		if date == "" {
			continue
		}

		title := episode.Series.Title
		title += fmt.Sprintf(" S%02dE%02d", episode.SeasonNumber, episode.EpisodeNumber)
		if episode.Title != "" {
			title += " - " + episode.Title
		}

		link := ""
		if episode.Series.TitleSlug != "" {
			link = service.publicBaseURL + "/series/" + episode.Series.TitleSlug
		}

		result[date] = append(result[date], calendarReleaseItem{
			Source:      "Sonarr",
			Title:       title,
			Description: episode.Overview,
			Thumbnail:   arrPosterURL(episode.Series.Images),
			Link:        link,
			dedupKey:    fmt.Sprintf("sonarr:%s:s%de%d", episode.Series.TitleSlug, episode.SeasonNumber, episode.EpisodeNumber),
		})
	}

	return result, nil
}

func fetchRadarrReleases(ctx context.Context, service *calendarReleaseService, start, end time.Time) (map[string][]calendarReleaseItem, error) {
	client := ternary[requestDoer](service.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)

	request, err := newArrCalendarRequest(ctx, service, start, end, nil)
	if err != nil {
		return nil, err
	}

	movies, err := decodeJsonFromRequest[[]radarrCalendarMovie](client, request)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]calendarReleaseItem)

	for _, movie := range movies {
		// Digital/physical home release date, preferring digital when both exist.
		raw := movie.DigitalRelease
		if raw == "" {
			raw = movie.PhysicalRelease
		}

		date := arrParseDate(raw)
		if date == "" {
			continue
		}

		link := ""
		dedupKey := "radarr:slug:" + movie.TitleSlug
		if movie.TmdbID != 0 {
			link = fmt.Sprintf("%s/movie/%d", service.publicBaseURL, movie.TmdbID)
			dedupKey = fmt.Sprintf("radarr:tmdb:%d", movie.TmdbID)
		}

		result[date] = append(result[date], calendarReleaseItem{
			Source:      "Radarr",
			Title:       movie.Title,
			Description: movie.Overview,
			Thumbnail:   arrPosterURL(movie.Images),
			Link:        link,
			dedupKey:    dedupKey,
		})
	}

	return result, nil
}

// arrParseDate extracts the ISO date (YYYY-MM-DD) from an arr API timestamp,
// returning "" when the value is empty or unparseable.
func arrParseDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.Format(calendarReleaseDateLayout)
	}

	// Fall back to a bare date if the API returned one.
	if len(value) >= len(calendarReleaseDateLayout) {
		if _, err := time.Parse(calendarReleaseDateLayout, value[:len(calendarReleaseDateLayout)]); err == nil {
			return value[:len(calendarReleaseDateLayout)]
		}
	}

	return ""
}
