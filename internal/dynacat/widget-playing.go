package dynacat

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var playingWidgetTemplate = mustParseTemplate("playing.html", "widget-base.html")

type plexSessionsResponse struct {
	MediaContainer struct {
		Metadata []struct {
			User struct {
				Title string `json:"title"`
			} `json:"User"`
			Player struct {
				State string `json:"state"`
			} `json:"Player"`
			Type             string `json:"type"`
			Title            string `json:"title"`
			GrandparentTitle string `json:"grandparentTitle"`
			ParentTitle      string `json:"parentTitle"`
			ParentIndex      int    `json:"parentIndex"`
			Index            int    `json:"index"`
			Duration         int64  `json:"duration"`
			ViewOffset       int64  `json:"viewOffset"`
			Thumb            string `json:"thumb"`
			GrandparentThumb string `json:"grandparentThumb"`
			ParentThumb      string `json:"parentThumb"`
			Key              string `json:"key"`
		} `json:"Metadata"`
	} `json:"MediaContainer"`
}

type jellyfinEmbySessionsResponse []struct {
	UserName       string `json:"UserName"`
	NowPlayingItem *struct {
		Type              string `json:"Type"`
		Name              string `json:"Name"`
		SeriesName        string `json:"SeriesName"`
		AlbumArtist       string `json:"AlbumArtist"`
		Album             string `json:"Album"`
		ParentIndexNumber int    `json:"ParentIndexNumber"`
		IndexNumber       int    `json:"IndexNumber"`
		RunTimeTicks      int64  `json:"RunTimeTicks"`
		Id                string `json:"Id"`
	} `json:"NowPlayingItem"`
	PlayState *struct {
		IsPaused      bool  `json:"IsPaused"`
		CanSeek       bool  `json:"CanSeek"`
		PositionTicks int64 `json:"PositionTicks"`
	} `json:"PlayState"`
}

type navidromeNowPlayingResponse struct {
	SubsonicResponse struct {
		Status     string `json:"status"`
		NowPlaying struct {
			Entry []struct {
				ID         string `json:"id"`
				Username   string `json:"username"`
				Title      string `json:"title"`
				Artist     string `json:"artist"`
				Album      string `json:"album"`
				Duration   int64  `json:"duration"`
				MinutesAgo int    `json:"minutesAgo"`
				CoverArt   string `json:"coverArt"`
			} `json:"entry"`
		} `json:"nowPlaying"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"subsonic-response"`
}

type playingWidget struct {
	widgetBase  `yaml:",inline"`
	Frameless   bool                `yaml:"frameless"`
	Hosts       []PlayingHostConfig `yaml:"hosts"`
	SmallColumn bool                `yaml:"small-column"`
	// `compact` option removed — layouts use the default (non-compact) sizing
	PlayState               string `yaml:"play-state"`
	ShowThumbnail           *bool  `yaml:"show-thumbnail"`
	ShowPaused              bool   `yaml:"show-paused"`
	ShowProgressBar         *bool  `yaml:"show-progress-bar"`
	ShowProgressInfo        *bool  `yaml:"show-progress-info"`
	GroupByHost             bool   `yaml:"group-by-host"`
	EpisodeTitleFormat      string `yaml:"episode-title-format"`
	Debug                   bool   `yaml:"debug"`
	ShowThumbnailEnabled    bool   `yaml:"-"`
	ShowProgressBarEnabled  bool   `yaml:"-"`
	ShowProgressInfoEnabled bool   `yaml:"-"`

	mu             sync.RWMutex              `yaml:"-"`
	Sessions       []mediaSession            `yaml:"-"`
	SessionsByHost map[string][]mediaSession `yaml:"-"`
}

type PlayingHostConfig struct {
	URL           string `yaml:"url"`
	Username      string `yaml:"username"`
	Token         string `yaml:"token"`
	AllowInsecure bool   `yaml:"allow-insecure"`
	ServerType    string `yaml:"-"`
	BaseURL       string `yaml:"-"`
}

type mediaSession struct {
	ServerType         string
	ServerURL          string
	UserName           string
	IsPlaying          bool
	State              string
	MediaType          string
	Title              string
	ShowTitle          string
	Season             string
	Episode            string
	Artist             string
	AlbumTitle         string
	ThumbnailURL       string
	Duration           int64
	Offset             int64
	Progress           int
	RemainingSeconds   int
	FormattedDuration  string
	FormattedPosition  string
	FormattedRemaining string
	DisplayTitle       string
	DisplaySubtitle    string
	EpisodeInfo        string
}

func (widget *playingWidget) initialize() error {
	if widget.Debug {
		slog.Info("Playing widget initialize called", "debugEnabled", widget.Debug)
	}

	widget.withTitle("Currently Playing")

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(30 * time.Second)
		widget.UpdateInterval = &interval
	}

	// Set cache duration to match update interval
	widget.withCacheDuration(time.Duration(*widget.UpdateInterval))

	// Set defaults
	if widget.PlayState == "" {
		widget.PlayState = "indicator"
	}
	if widget.EpisodeTitleFormat == "" {
		widget.EpisodeTitleFormat = "series"
	}

	// Boolean defaults — only applied when not explicitly set by the user
	t := true
	if widget.ShowThumbnail == nil {
		widget.ShowThumbnail = &t
	}
	if widget.ShowProgressBar == nil {
		widget.ShowProgressBar = &t
	}
	if widget.ShowProgressInfo == nil {
		widget.ShowProgressInfo = &t
	}

	// Explicit default for grouping
	widget.GroupByHost = false

	// Ensure progress info is disabled if there's no progress bar
	if !*widget.ShowProgressBar {
		f := false
		widget.ShowProgressInfo = &f
	}

	widget.ShowThumbnailEnabled = widget.ShowThumbnail != nil && *widget.ShowThumbnail
	widget.ShowProgressBarEnabled = widget.ShowProgressBar != nil && *widget.ShowProgressBar
	widget.ShowProgressInfoEnabled = widget.ShowProgressInfo != nil && *widget.ShowProgressInfo

	// Validate and parse host URLs
	if len(widget.Hosts) == 0 {
		return fmt.Errorf("at least one host must be specified")
	}

	if widget.Debug {
		slog.Info("Playing widget hosts", "count", len(widget.Hosts))
	}

	for i := range widget.Hosts {
		host := &widget.Hosts[i]
		if host.URL == "" {
			return fmt.Errorf("host URL is required")
		}
		if host.Token == "" {
			return fmt.Errorf("host token is required")
		}

		serverType, baseURL, err := parseHostURL(host.URL)
		if err != nil {
			return fmt.Errorf("invalid host URL %s: %w", host.URL, err)
		}

		host.ServerType = serverType
		host.BaseURL = baseURL

		if host.ServerType == "navidrome" && host.Username == "" {
			return fmt.Errorf("host username is required for navidrome")
		}

		if widget.Debug {
			slog.Info("Playing widget host configured", "type", serverType, "url", baseURL)
		}
	}

	// Validate play-state
	if widget.PlayState != "indicator" && widget.PlayState != "text" {
		return fmt.Errorf("play-state must be 'indicator' or 'text'")
	}

	// Validate episode-title-format
	if widget.EpisodeTitleFormat != "series" && widget.EpisodeTitleFormat != "episode" {
		return fmt.Errorf("episode-title-format must be 'series' or 'episode'")
	}

	// Initialize session maps
	if widget.GroupByHost {
		widget.SessionsByHost = make(map[string][]mediaSession)
	}

	return nil
}

func (widget *playingWidget) update(ctx context.Context) {
	if widget.Debug {
		slog.Info("Playing widget update called")
	}

	type fetchResult struct {
		host     *PlayingHostConfig
		sessions []mediaSession
		err      error
	}

	results := make(chan fetchResult, len(widget.Hosts))
	var wg sync.WaitGroup

	// Fetch sessions from all hosts in parallel
	for i := range widget.Hosts {
		host := &widget.Hosts[i]
		wg.Add(1)
		go func(h *PlayingHostConfig) {
			defer wg.Done()
			sessions, err := widget.fetchSessionsTask(ctx, h)
			results <- fetchResult{host: h, sessions: sessions, err: err}
		}(host)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var allSessions []mediaSession
	successCount := 0
	errorCount := 0

	for result := range results {
		if result.err != nil {
			errorCount++
			slog.Error(
				"failed to fetch sessions from media server",
				"type", result.host.ServerType,
				"url", result.host.BaseURL,
				"error", result.err,
			)
			continue
		}

		successCount++
		allSessions = append(allSessions, result.sessions...)
	}

	widget.mu.Lock()
	widget.Sessions = allSessions
	if widget.GroupByHost {
		// Rebuild map logic if needed, but the original code did it inside the loop
		// Let's defer map rebuilding to here to be safe under lock or do it properly
		widget.SessionsByHost = make(map[string][]mediaSession)
		for _, session := range allSessions {
			hostKey := fmt.Sprintf("%s:%s", session.ServerType, session.ServerURL)
			widget.SessionsByHost[hostKey] = append(widget.SessionsByHost[hostKey], session)
		}
	}
	widget.mu.Unlock()

	if widget.Debug {
		slog.Info("Playing widget update complete",
			"totalSessions", len(allSessions),
			"successCount", successCount,
			"errorCount", errorCount,
		)
		for _, session := range allSessions {
			slog.Info("  Session", "user", session.UserName, "title", session.Title, "playing", session.IsPlaying)
		}
	}

	// Handle errors
	var err error
	if successCount == 0 {
		err = errNoContent
	} else if errorCount > 0 {
		err = errPartialContent
	}

	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}
}

func (widget *playingWidget) fetchSessionsTask(ctx context.Context, host *PlayingHostConfig) ([]mediaSession, error) {
	switch host.ServerType {
	case "plex":
		return widget.fetchPlexSessions(ctx, host)
	case "jellyfin":
		return widget.fetchJellyfinSessions(ctx, host)
	case "emby":
		return widget.fetchEmbySessions(ctx, host)
	case "navidrome":
		return widget.fetchNavidromeSessions(ctx, host)
	default:
		return nil, fmt.Errorf("unknown server type: %s", host.ServerType)
	}
}

func (widget *playingWidget) secureSessionThumbnails(ctx context.Context, host *PlayingHostConfig, sessions []mediaSession) []mediaSession {
	if widget.Providers == nil {
		return sessions
	}

	for i := range sessions {
		if sessions[i].ThumbnailURL == "" {
			continue
		}
		sessions[i].ThumbnailURL = widget.Providers.SecureImageURL(ctx, sessions[i].ThumbnailURL, host.AllowInsecure)
	}

	return sessions
}

func (widget *playingWidget) buildNavidromeAuthQuery(host *PlayingHostConfig) url.Values {
	query := url.Values{}
	query.Set("u", host.Username)
	query.Set("p", host.Token)
	query.Set("v", "1.16.1")
	query.Set("c", "dynacat")
	query.Set("f", "json")
	return query
}

func (widget *playingWidget) fetchNavidromeSessions(ctx context.Context, host *PlayingHostConfig) ([]mediaSession, error) {
	baseURL := strings.TrimRight(host.BaseURL, "/")
	query := widget.buildNavidromeAuthQuery(host)

	endpoint := fmt.Sprintf("%s/rest/getNowPlaying.view?%s", baseURL, query.Encode())
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := ternary(host.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	response, err := decodeJsonFromRequest[navidromeNowPlayingResponse](client, req)
	if err != nil {
		return nil, err
	}

	if response.SubsonicResponse.Status != "ok" {
		if response.SubsonicResponse.Error != nil {
			return nil, fmt.Errorf("navidrome api error %d: %s", response.SubsonicResponse.Error.Code, response.SubsonicResponse.Error.Message)
		}
		return nil, fmt.Errorf("navidrome returned status %q", response.SubsonicResponse.Status)
	}

	var sessions []mediaSession
	for _, item := range response.SubsonicResponse.NowPlaying.Entry {
		durationMs := item.Duration * 1000
		offsetMs := int64(item.MinutesAgo) * int64(time.Minute/time.Millisecond)

		session := mediaSession{
			ServerType: "navidrome",
			ServerURL:  host.BaseURL,
			UserName:   item.Username,
			IsPlaying:  true,
			State:      "playing",
			MediaType:  "track",
			Title:      item.Title,
			Artist:     item.Artist,
			AlbumTitle: item.Album,
			Duration:   durationMs,
			Offset:     offsetMs,
		}

		widget.setDisplayTitles(&session)

		if *widget.ShowThumbnail {
			coverID := item.CoverArt
			if coverID == "" {
				coverID = item.ID
			}

			if coverID != "" {
				coverQuery := widget.buildNavidromeAuthQuery(host)
				coverQuery.Set("id", coverID)
				session.ThumbnailURL = fmt.Sprintf("%s/rest/getCoverArt.view?%s", baseURL, coverQuery.Encode())
			}
		}

		widget.calculateProgress(&session)
		sessions = append(sessions, session)
	}

	return widget.secureSessionThumbnails(ctx, host, sessions), nil
}

func (widget *playingWidget) fetchPlexSessions(ctx context.Context, host *PlayingHostConfig) ([]mediaSession, error) {
	url := fmt.Sprintf("%s/status/sessions", strings.TrimRight(host.BaseURL, "/"))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Plex-Token", host.Token)
	req.Header.Set("Accept", "application/json")

	client := ternary(host.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	response, err := decodeJsonFromRequest[plexSessionsResponse](client, req)
	if err != nil {
		return nil, err
	}

	var sessions []mediaSession
	for _, item := range response.MediaContainer.Metadata {
		isPlaying := item.Player.State == "playing"
		if !isPlaying && !widget.ShowPaused {
			continue
		}

		session := mediaSession{
			ServerType: "plex",
			ServerURL:  host.BaseURL,
			UserName:   item.User.Title,
			IsPlaying:  isPlaying,
			State:      item.Player.State,
			MediaType:  item.Type,
			Title:      item.Title,
			Duration:   item.Duration,
			Offset:     item.ViewOffset,
		}

		switch item.Type {
		case "episode":
			session.ShowTitle = item.GrandparentTitle
			if item.ParentIndex > 0 {
				session.Season = fmt.Sprintf("%d", item.ParentIndex)
			}
			if item.Index > 0 {
				session.Episode = fmt.Sprintf("%d", item.Index)
			}
		case "track":
			session.Artist = item.GrandparentTitle
			session.AlbumTitle = item.ParentTitle
		}

		// Set display title and subtitle based on format preference
		widget.setDisplayTitles(&session)

		if *widget.ShowThumbnail {
			if item.Type == "episode" && item.GrandparentThumb != "" {
				session.ThumbnailURL = fmt.Sprintf("%s%s?X-Plex-Token=%s",
					strings.TrimRight(host.BaseURL, "/"),
					item.GrandparentThumb,
					host.Token,
				)
			} else if item.Thumb != "" {
				session.ThumbnailURL = fmt.Sprintf("%s%s?X-Plex-Token=%s",
					strings.TrimRight(host.BaseURL, "/"),
					item.Thumb,
					host.Token,
				)
			}
		}

		widget.calculateProgress(&session)
		sessions = append(sessions, session)
	}

	return widget.secureSessionThumbnails(ctx, host, sessions), nil
}

func (widget *playingWidget) fetchJellyfinSessions(ctx context.Context, host *PlayingHostConfig) ([]mediaSession, error) {
	url := fmt.Sprintf("%s/Sessions?api_key=%s&activeWithinSeconds=30",
		strings.TrimRight(host.BaseURL, "/"),
		host.Token,
	)

	if widget.Debug {
		slog.Info("Jellyfin: fetching sessions", "url", url)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")

	client := ternary(host.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	response, err := decodeJsonFromRequest[jellyfinEmbySessionsResponse](client, req)
	if err != nil {
		if widget.Debug {
			slog.Error("Jellyfin: failed to decode response", "error", err)
		}
		return nil, err
	}

	if widget.Debug {
		slog.Info("Jellyfin: received sessions", "count", len(response))
	}

	sessions, err := widget.parseJellyfinEmbySessions(host, "jellyfin", response)
	if err != nil {
		return nil, err
	}

	return widget.secureSessionThumbnails(ctx, host, sessions), nil
}

func (widget *playingWidget) fetchEmbySessions(ctx context.Context, host *PlayingHostConfig) ([]mediaSession, error) {
	url := fmt.Sprintf("%s/Sessions?api_key=%s&activeWithinSeconds=30",
		strings.TrimRight(host.BaseURL, "/"),
		host.Token,
	)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")

	client := ternary(host.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	response, err := decodeJsonFromRequest[jellyfinEmbySessionsResponse](client, req)
	if err != nil {
		return nil, err
	}

	// Filter for active clients (CanSeek == true)
	var filtered jellyfinEmbySessionsResponse
	for _, item := range response {
		if item.PlayState != nil && item.PlayState.CanSeek {
			filtered = append(filtered, item)
		}
	}

	sessions, err := widget.parseJellyfinEmbySessions(host, "emby", filtered)
	if err != nil {
		return nil, err
	}

	return widget.secureSessionThumbnails(ctx, host, sessions), nil
}

func (widget *playingWidget) parseJellyfinEmbySessions(host *PlayingHostConfig, serverType string, response jellyfinEmbySessionsResponse) ([]mediaSession, error) {
	var sessions []mediaSession
	for _, item := range response {
		if widget.Debug {
			slog.Info("Jellyfin: parsing session",
				"user", item.UserName,
				"hasNowPlaying", item.NowPlayingItem != nil,
				"hasPlayState", item.PlayState != nil,
			)
		}

		if item.NowPlayingItem == nil || item.PlayState == nil {
			if widget.Debug {
				slog.Info("Jellyfin: skipping session - missing required fields")
			}
			continue
		}

		isPlaying := !item.PlayState.IsPaused
		if widget.Debug {
			slog.Info("Jellyfin: session play state", "user", item.UserName, "isPaused", item.PlayState.IsPaused, "isPlaying", isPlaying, "showPaused", widget.ShowPaused)
		}
		if !isPlaying && !widget.ShowPaused {
			if widget.Debug {
				slog.Info("Jellyfin: skipping paused session", "user", item.UserName)
			}
			continue
		}

		session := mediaSession{
			ServerType: serverType,
			ServerURL:  host.BaseURL,
			UserName:   item.UserName,
			IsPlaying:  isPlaying,
			State:      "playing",
			Title:      item.NowPlayingItem.Name,
			Duration:   item.NowPlayingItem.RunTimeTicks / 10000,
			Offset:     item.PlayState.PositionTicks / 10000,
		}

		if item.PlayState.IsPaused {
			session.State = "paused"
		}

		// Map media type
		switch strings.ToLower(item.NowPlayingItem.Type) {
		case "movie":
			session.MediaType = "movie"
		case "episode":
			session.MediaType = "episode"
			session.ShowTitle = item.NowPlayingItem.SeriesName
			if item.NowPlayingItem.ParentIndexNumber > 0 {
				session.Season = fmt.Sprintf("%d", item.NowPlayingItem.ParentIndexNumber)
			}
			if item.NowPlayingItem.IndexNumber > 0 {
				session.Episode = fmt.Sprintf("%d", item.NowPlayingItem.IndexNumber)
			}
		case "audio":
			session.MediaType = "track"
			session.Artist = item.NowPlayingItem.AlbumArtist
			session.AlbumTitle = item.NowPlayingItem.Album
		default:
			session.MediaType = strings.ToLower(item.NowPlayingItem.Type)
		}

		// Set display title and subtitle based on format preference
		widget.setDisplayTitles(&session)

		if *widget.ShowThumbnail && item.NowPlayingItem.Id != "" {
			session.ThumbnailURL = fmt.Sprintf("%s/Items/%s/Images/Primary?api_key=%s",
				strings.TrimRight(host.BaseURL, "/"),
				item.NowPlayingItem.Id,
				host.Token,
			)
		}

		widget.calculateProgress(&session)
		sessions = append(sessions, session)
	}

	if widget.Debug {
		slog.Info("Jellyfin: finished parsing", "totalSessions", len(sessions), "serverType", serverType)
	}

	return sessions, nil
}

func (widget *playingWidget) setDisplayTitles(session *mediaSession) {
	// Set default display titles
	session.DisplayTitle = session.Title
	session.DisplaySubtitle = ""
	session.EpisodeInfo = ""

	// Handle episodes based on format preference
	if session.MediaType == "episode" {
		if widget.EpisodeTitleFormat == "series" {
			// New default: Show series name with S2E4 as title
			if session.ShowTitle != "" {
				session.DisplayTitle = session.ShowTitle
				// Build episode info (S1E4 format)
				if session.Season != "" || session.Episode != "" {
					if session.Season != "" {
						session.EpisodeInfo = "S" + session.Season
					}
					if session.Episode != "" {
						session.EpisodeInfo += "E" + session.Episode
					}
				}
			}
			// Episode name becomes subtitle
			session.DisplaySubtitle = session.Title
		} else {
			// Legacy format: episode name as title
			session.DisplayTitle = session.Title
			// Series info as subtitle
			if session.ShowTitle != "" {
				session.DisplaySubtitle = session.ShowTitle
				if session.Season != "" || session.Episode != "" {
					if session.Season != "" {
						session.DisplaySubtitle += " - S" + session.Season
					}
					if session.Episode != "" {
						session.DisplaySubtitle += "E" + session.Episode
					}
				}
			}
		}
	} else if session.MediaType == "track" {
		// Music tracks: title is track name, subtitle is artist/album
		session.DisplayTitle = session.Title
		if session.Artist != "" || session.AlbumTitle != "" {
			if session.Artist != "" {
				session.DisplaySubtitle = session.Artist
			}
			if session.AlbumTitle != "" {
				if session.DisplaySubtitle != "" {
					session.DisplaySubtitle += " - "
				}
				session.DisplaySubtitle += session.AlbumTitle
			}
		}
	}
	// For movies and other types, DisplayTitle and DisplaySubtitle are already set correctly
}

func (widget *playingWidget) calculateProgress(session *mediaSession) {
	if session.Duration <= 0 {
		return
	}

	session.Progress = int(float64(session.Offset) / float64(session.Duration) * 100)
	if session.Progress > 100 {
		session.Progress = 100
	}

	remainingMs := session.Duration - session.Offset
	if remainingMs < 0 {
		remainingMs = 0
	}

	session.RemainingSeconds = int(remainingMs / 1000)

	session.FormattedDuration = formatDuration(session.Duration)
	session.FormattedPosition = formatDuration(session.Offset)
	session.FormattedRemaining = formatDuration(remainingMs)
}

func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d.Hours() >= 1 {
		return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func (widget *playingWidget) Render() template.HTML {
	widget.mu.RLock()
	defer widget.mu.RUnlock()
	return widget.renderTemplate(widget, playingWidgetTemplate)
}

func parseHostURL(rawURL string) (serverType string, baseURL string, err error) {
	// Check for type prefix
	if !strings.Contains(rawURL, ":") {
		slog.Warn(fmt.Sprintf("Host URL missing server type prefix (e.g., 'plex:https://...'). Unable to determine server type for: %s", rawURL))
		return "", "", fmt.Errorf("host URL missing server type prefix")
	}

	parts := strings.SplitN(rawURL, ":", 2)
	if len(parts) < 2 {
		slog.Warn(fmt.Sprintf("Host URL missing server type prefix (e.g., 'plex:https://...'). Unable to determine server type for: %s", rawURL))
		return "", "", fmt.Errorf("invalid host URL format")
	}

	serverType = strings.ToLower(parts[0])

	// Check if it's a valid server type
	if serverType != "plex" && serverType != "jellyfin" && serverType != "emby" && serverType != "navidrome" {
		// This might be part of a URL like "https://..."
		slog.Warn(fmt.Sprintf("Host URL missing server type prefix (e.g., 'plex:https://...'). Unable to determine server type for: %s", rawURL))
		return "", "", fmt.Errorf("unknown server type: %s", serverType)
	}

	// Reconstruct the URL
	remainingURL := parts[1]
	if strings.HasPrefix(remainingURL, "//") {
		// URL is like "plex://example.com" - add https:
		baseURL = "https:" + remainingURL
	} else if strings.HasPrefix(remainingURL, "http://") || strings.HasPrefix(remainingURL, "https://") {
		// URL is like "plex:https://example.com"
		baseURL = remainingURL
	} else {
		// URL is like "plex:example.com" - add https://
		baseURL = "https://" + remainingURL
	}

	return serverType, baseURL, nil
}
