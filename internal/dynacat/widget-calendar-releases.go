package dynacat

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

func calendarReleaseState(dateISO string, hasFile bool, todayISO string) string {
	if hasFile {
		return "available"
	}
	if dateISO <= todayISO {
		return "released"
	}
	return "upcoming"
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
	HasFile       bool   `json:"hasFile"`
	Series        struct {
		Title     string     `json:"title"`
		TitleSlug string     `json:"titleSlug"`
		Images    []arrImage `json:"images"`
	} `json:"series"`
}

type radarrCalendarMovie struct {
	Title           string     `json:"title"`
	Overview        string     `json:"overview"`
	Status          string     `json:"status"`
	TmdbID          int        `json:"tmdbId"`
	HasFile         bool       `json:"hasFile"`
	TitleSlug       string     `json:"titleSlug"`
	InCinemas       string     `json:"inCinemas"`
	DigitalRelease  string     `json:"digitalRelease"`
	PhysicalRelease string     `json:"physicalRelease"`
	ReleaseDate     string     `json:"releaseDate"`
	Images          []arrImage `json:"images"`
}

func (widget *calendarWidget) getReleasesForMonth(ctx context.Context, year int, month time.Month) map[string][]calendarReleaseItem {
	key := fmt.Sprintf("%04d-%02d", year, month)

	widget.releaseCacheMu.Lock()
	if entry, ok := widget.releaseCache[key]; ok && time.Since(entry.fetchedAt) < widget.releasesInterval {
		widget.releaseCacheMu.Unlock()
		return entry.data
	}
	widget.releaseCacheMu.Unlock()

	monthStart := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, -1)
	start := monthStart.AddDate(0, 0, -7)
	end := monthEnd.AddDate(0, 0, 7)

	data := make(map[string][]calendarReleaseItem)
	seen := make(map[string]struct{})

	for i := range widget.Hosts {
		service := &widget.Hosts[i]

		items, err := fetchCalendarReleases(ctx, service, start, end, widget.enabledReleaseTypes)
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

				if widget.Providers != nil && widget.Providers.app != nil && item.Thumbnail != "" {
					hash := hashString(item.Thumbnail)
					widget.Providers.app.registerImageProxy(hash, item.Thumbnail, service.AllowInsecure)
					item.Thumbnail = widget.GetBaseURL() + "/api/image-proxy/" + hash
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

func fetchCalendarReleases(ctx context.Context, service *calendarReleaseService, start, end time.Time, enabledTypes map[string]bool) (map[string][]calendarReleaseItem, error) {
	switch service.serverType {
	case "sonarr":
		if !enabledTypes["episode"] {
			return nil, nil
		}
		return fetchSonarrReleases(ctx, service, start, end)
	case "radarr":
		return fetchRadarrReleases(ctx, service, start, end, enabledTypes)
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

	type sonarrGroupKey struct {
		date    string
		groupID string
		season  int
	}

	groups := make(map[sonarrGroupKey][]sonarrCalendarEpisode)
	order := make(map[string][]sonarrGroupKey)

	for _, episode := range episodes {
		date := arrParseDate(episode.AirDateUtc)
		if date == "" {
			continue
		}

		groupID := episode.Series.TitleSlug
		if groupID == "" {
			groupID = episode.Series.Title
		}

		key := sonarrGroupKey{date: date, groupID: groupID, season: episode.SeasonNumber}
		if _, ok := groups[key]; !ok {
			order[date] = append(order[date], key)
		}
		groups[key] = append(groups[key], episode)
	}

	result := make(map[string][]calendarReleaseItem)
	todayISO := time.Now().UTC().Format(calendarReleaseDateLayout)

	for date, keys := range order {
		for _, key := range keys {
			eps := groups[key]
			first := eps[0]

			hasFile := true
			for _, ep := range eps {
				if !ep.HasFile {
					hasFile = false
					break
				}
			}

			link := ""
			if first.Series.TitleSlug != "" {
				link = service.publicBaseURL + "/series/" + first.Series.TitleSlug
			}

			var title, description, dedupKey string
			if len(eps) == 1 {
				title = first.Series.Title + fmt.Sprintf(" S%02dE%02d", first.SeasonNumber, first.EpisodeNumber)
				if first.Title != "" {
					title += " - " + first.Title
				}
				description = first.Overview
				dedupKey = fmt.Sprintf("sonarr:%s:s%de%d", key.groupID, first.SeasonNumber, first.EpisodeNumber)
			} else {
				minEp, maxEp := first.EpisodeNumber, first.EpisodeNumber
				for _, ep := range eps {
					if ep.EpisodeNumber < minEp {
						minEp = ep.EpisodeNumber
					}
					if ep.EpisodeNumber > maxEp {
						maxEp = ep.EpisodeNumber
					}
				}
				title = first.Series.Title + fmt.Sprintf(" S%02d E%02d-E%02d", first.SeasonNumber, minEp, maxEp)
				description = fmt.Sprintf("%d episodes", len(eps))
				dedupKey = fmt.Sprintf("sonarr:%s:s%d:group", key.groupID, first.SeasonNumber)
			}

			result[date] = append(result[date], calendarReleaseItem{
				Source:      "Sonarr",
				Title:       title,
				Description: description,
				Thumbnail:   arrPosterURL(first.Series.Images),
				Link:        link,
				Type:        "episode",
				State:       calendarReleaseState(date, hasFile, todayISO),
				dedupKey:    dedupKey,
			})
		}
	}

	return result, nil
}

func fetchRadarrReleases(ctx context.Context, service *calendarReleaseService, start, end time.Time, enabledTypes map[string]bool) (map[string][]calendarReleaseItem, error) {
	client := ternary[requestDoer](service.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)

	request, err := newArrCalendarRequest(ctx, service, start, end, nil)
	if err != nil {
		return nil, err
	}

	movies, err := decodeJsonFromRequest[[]radarrCalendarMovie](client, request)
	if err != nil {
		return nil, err
	}

	startISO := start.Format(calendarReleaseDateLayout)
	endISO := end.Format(calendarReleaseDateLayout)
	todayISO := time.Now().UTC().Format(calendarReleaseDateLayout)

	result := make(map[string][]calendarReleaseItem)

	for _, movie := range movies {
		link := ""
		dedupBase := "radarr:slug:" + movie.TitleSlug
		if movie.TmdbID != 0 {
			link = fmt.Sprintf("%s/movie/%d", service.publicBaseURL, movie.TmdbID)
			dedupBase = fmt.Sprintf("radarr:tmdb:%d", movie.TmdbID)
		}

		poster := arrPosterURL(movie.Images)

		for _, candidate := range []struct{ releaseType, raw string }{
			{"cinema", movie.InCinemas},
			{"physical", movie.PhysicalRelease},
			{"digital", movie.DigitalRelease},
		} {
			if !enabledTypes[candidate.releaseType] {
				continue
			}

			date := arrParseDate(candidate.raw)
			if date == "" || date < startISO || date > endISO {
				continue
			}

			result[date] = append(result[date], calendarReleaseItem{
				Source:      "Radarr",
				Title:       movie.Title,
				Description: movie.Overview,
				Thumbnail:   poster,
				Link:        link,
				Type:        candidate.releaseType,
				State:       calendarReleaseState(date, movie.HasFile, todayISO),
				dedupKey:    dedupBase + ":" + candidate.releaseType,
			})
		}
	}

	return result, nil
}

func arrParseDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.Format(calendarReleaseDateLayout)
	}

	if len(value) >= len(calendarReleaseDateLayout) {
		if _, err := time.Parse(calendarReleaseDateLayout, value[:len(calendarReleaseDateLayout)]); err == nil {
			return value[:len(calendarReleaseDateLayout)]
		}
	}

	return ""
}
