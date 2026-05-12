package dynacat

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var latestMediaWidgetTemplate = mustParseTemplate("latest-media.html", "widget-base.html")

type latestMediaWidget struct {
	widgetBase  `yaml:",inline"`
	Frameless   bool                    `yaml:"frameless"`
	Hosts       []latestMediaHostConfig `yaml:"hosts"`
	ItemCount   int                     `yaml:"item-count"`
	Columns     int                     `yaml:"columns"`
	SmallColumn bool                    `yaml:"small-column"`
	ShowOverlay *bool                   `yaml:"show-overlay"`

	ShowOverlayEnabled bool `yaml:"-"`
	EffectiveColumns   int  `yaml:"-"`

	mu    sync.RWMutex
	Items []latestMediaItem
	// Track which hosts allow insecure connections for image caching
	hostAllowInsecure map[string]bool `yaml:"-"`
}

type latestMediaHostConfig struct {
	URL           string   `yaml:"url"`
	PublicURL     string   `yaml:"public-url"`
	Token         string   `yaml:"token"`
	AllowInsecure bool     `yaml:"allow-insecure"`
	Libraries     []string `yaml:"libraries"`
	ServerType    string   `yaml:"-"`
	BaseURL       string   `yaml:"-"`
	PublicBaseURL string   `yaml:"-"`
}

type latestMediaItem struct {
	ServerType   string
	ServerURL    string
	Title        string
	Year         int
	MediaType    string
	SeriesTitle  string
	AddedAt      time.Time
	Duration     int64
	CoverURL     string
	ThumbnailURL string
	LinkURL      string
	TimeAgo      string
	DurationStr  string
}

func (widget *latestMediaWidget) initialize() error {
	widget.withTitle("Latest Media")

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(30 * time.Minute)
		widget.UpdateInterval = &interval
	}

	widget.withCacheDuration(time.Duration(*widget.UpdateInterval))

	if widget.ItemCount <= 0 {
		widget.ItemCount = 12
	}
	if widget.Columns <= 0 {
		widget.Columns = 6
	}

	t := true
	if widget.ShowOverlay == nil {
		widget.ShowOverlay = &t
	}
	widget.ShowOverlayEnabled = *widget.ShowOverlay

	widget.EffectiveColumns = widget.Columns
	if widget.SmallColumn && widget.EffectiveColumns > 1 {
		widget.EffectiveColumns = widget.EffectiveColumns / 2
	}

	if len(widget.Hosts) == 0 {
		return fmt.Errorf("at least one host must be specified")
	}

	widget.hostAllowInsecure = make(map[string]bool)

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
		if serverType != "plex" && serverType != "jellyfin" && serverType != "emby" {
			return fmt.Errorf("unsupported host type for latest-media: %s", serverType)
		}
		if host.PublicURL != "" {
			host.PublicBaseURL = strings.TrimRight(host.PublicURL, "/")
		} else {
			host.PublicBaseURL = baseURL
		}
		widget.hostAllowInsecure[baseURL] = host.AllowInsecure
	}

	return nil
}

func (widget *latestMediaWidget) update(ctx context.Context) {
	type fetchResult struct {
		host  *latestMediaHostConfig
		items []latestMediaItem
		err   error
	}

	results := make(chan fetchResult, len(widget.Hosts))
	var wg sync.WaitGroup

	for i := range widget.Hosts {
		host := &widget.Hosts[i]
		wg.Add(1)
		go func(h *latestMediaHostConfig) {
			defer wg.Done()
			items, err := widget.fetchLatestItems(ctx, h)
			results <- fetchResult{host: h, items: items, err: err}
		}(host)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var allItems []latestMediaItem
	successCount := 0
	errorCount := 0

	for result := range results {
		if result.err != nil {
			errorCount++
			slog.Error(
				"failed to fetch latest media from server",
				"type", result.host.ServerType,
				"url", result.host.BaseURL,
				"error", result.err,
			)
			continue
		}
		successCount++
		allItems = append(allItems, result.items...)
	}

	// Sort by date added, newest first
	sort.Slice(allItems, func(i, j int) bool {
		return allItems[i].AddedAt.After(allItems[j].AddedAt)
	})

	// Trim to item count
	if len(allItems) > widget.ItemCount {
		allItems = allItems[:widget.ItemCount]
	}

	// Format relative time and duration
	now := time.Now()
	for i := range allItems {
		allItems[i].TimeAgo = formatTimeAgo(allItems[i].AddedAt, now)
		if allItems[i].Duration > 0 {
			allItems[i].DurationStr = formatDuration(allItems[i].Duration)
		}
	}

	widget.mu.Lock()
	widget.Items = allItems
	widget.mu.Unlock()

	// Cache image URLs with the appropriate insecure setting
	widget.cacheImageURLs()

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

func (widget *latestMediaWidget) fetchLatestItems(ctx context.Context, host *latestMediaHostConfig) ([]latestMediaItem, error) {
	switch host.ServerType {
	case "plex":
		return widget.fetchPlexLatest(ctx, host)
	case "jellyfin":
		return widget.fetchJellyfinLatest(ctx, host)
	case "emby":
		return widget.fetchEmbyLatest(ctx, host)
	default:
		return nil, fmt.Errorf("unknown server type: %s", host.ServerType)
	}
}

// --- Plex ---

type plexIdentityResponse struct {
	MediaContainer struct {
		MachineIdentifier string `json:"machineIdentifier"`
	} `json:"MediaContainer"`
}

type plexSectionsResponse struct {
	MediaContainer struct {
		Directory []struct {
			Key   string `json:"key"`
			Title string `json:"title"`
			Type  string `json:"type"`
		} `json:"Directory"`
	} `json:"MediaContainer"`
}

type plexRecentlyAddedResponse struct {
	MediaContainer struct {
		Metadata []struct {
			Title            string `json:"title"`
			GrandparentTitle string `json:"grandparentTitle"`
			Type             string `json:"type"`
			Year             int    `json:"year"`
			Duration         int64  `json:"duration"`
			AddedAt          int64  `json:"addedAt"`
			Thumb            string `json:"thumb"`
			Art              string `json:"art"`
			GrandparentThumb string `json:"grandparentThumb"`
			GrandparentArt   string `json:"grandparentArt"`
			Key              string `json:"key"`
		} `json:"Metadata"`
	} `json:"MediaContainer"`
}

func (widget *latestMediaWidget) fetchPlexLatest(ctx context.Context, host *latestMediaHostConfig) ([]latestMediaItem, error) {
	client := ternary(host.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	baseURL := strings.TrimRight(host.BaseURL, "/")

	// Fetch machine identifier for deep links
	var machineID string
	identityURL := fmt.Sprintf("%s/identity", baseURL)
	identityReq, err := http.NewRequestWithContext(ctx, "GET", identityURL, nil)
	if err == nil {
		identityReq.Header.Set("X-Plex-Token", host.Token)
		identityReq.Header.Set("Accept", "application/json")
		identityResp, err := decodeJsonFromRequest[plexIdentityResponse](client, identityReq)
		if err == nil {
			machineID = identityResp.MediaContainer.MachineIdentifier
		} else {
			slog.Warn("failed to fetch plex machine identifier", "error", err)
		}
	}

	// Fetch sections
	sectionsURL := fmt.Sprintf("%s/library/sections", baseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", sectionsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", host.Token)
	req.Header.Set("Accept", "application/json")

	sectionsResp, err := decodeJsonFromRequest[plexSectionsResponse](client, req)
	if err != nil {
		return nil, fmt.Errorf("fetching plex sections: %w", err)
	}

	var items []latestMediaItem

	for _, section := range sectionsResp.MediaContainer.Directory {
		// Skip non-video Plex libraries so latest-media only shows video additions.
		if strings.EqualFold(section.Type, "artist") || strings.EqualFold(section.Type, "photo") {
			continue
		}

		// Filter by library names if specified
		if len(host.Libraries) > 0 && !containsString(host.Libraries, section.Title) {
			continue
		}

		recentURL := fmt.Sprintf("%s/library/sections/%s/recentlyAdded?X-Plex-Container-Size=%d",
			baseURL, section.Key, widget.ItemCount)

		req, err := http.NewRequestWithContext(ctx, "GET", recentURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("X-Plex-Token", host.Token)
		req.Header.Set("Accept", "application/json")

		resp, err := decodeJsonFromRequest[plexRecentlyAddedResponse](client, req)
		if err != nil {
			slog.Warn("failed to fetch plex recently added", "section", section.Title, "error", err)
			continue
		}

		for _, meta := range resp.MediaContainer.Metadata {
			// Hard filter out Plex photo items, regardless of library filtering config.
			if strings.EqualFold(meta.Type, "photo") || strings.EqualFold(meta.Type, "photoalbum") {
				continue
			}

			item := latestMediaItem{
				ServerType: "plex",
				ServerURL:  host.BaseURL,
				Title:      meta.Title,
				Year:       meta.Year,
				MediaType:  meta.Type,
				Duration:   meta.Duration,
				AddedAt:    time.Unix(meta.AddedAt, 0),
			}

			if meta.Type == "episode" {
				item.SeriesTitle = meta.GrandparentTitle
			}

			thumbPath := meta.Thumb
			artPath := meta.Art
			if meta.Type == "episode" {
				if meta.GrandparentThumb != "" {
					thumbPath = meta.GrandparentThumb
				}
				if meta.GrandparentArt != "" {
					artPath = meta.GrandparentArt
				}
			}
			if thumbPath != "" {
				item.CoverURL = fmt.Sprintf("%s%s?X-Plex-Token=%s", baseURL, thumbPath, host.Token)
			}
			if artPath != "" {
				item.ThumbnailURL = fmt.Sprintf("%s%s?X-Plex-Token=%s", baseURL, artPath, host.Token)
			}

			if machineID != "" && meta.Key != "" {
				item.LinkURL = fmt.Sprintf("%s/web/index.html#!/server/%s/details?key=%s",
					host.PublicBaseURL, machineID, url.QueryEscape(meta.Key))
			} else {
				item.LinkURL = fmt.Sprintf("%s/web/index.html#!/server", host.PublicBaseURL)
			}

			items = append(items, item)
		}
	}

	return items, nil
}

// --- Jellyfin / Emby ---

type jellyfinLatestItem struct {
	Id             string `json:"Id"`
	Name           string `json:"Name"`
	Type           string `json:"Type"`
	ProductionYear int    `json:"ProductionYear"`
	DateCreated    string `json:"DateCreated"`
	RunTimeTicks   int64  `json:"RunTimeTicks"`
	SeriesName     string `json:"SeriesName"`
	AlbumArtist    string `json:"AlbumArtist"`
}

type jellyfinUserViewsResponse struct {
	Items []struct {
		Id   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"Items"`
}

type jellyfinUsersResponse []struct {
	Id   string `json:"Id"`
	Name string `json:"Name"`
}

func (widget *latestMediaWidget) fetchJellyfinLatest(ctx context.Context, host *latestMediaHostConfig) ([]latestMediaItem, error) {
	return widget.fetchJellyfinEmbyLatest(ctx, host, "jellyfin")
}

func (widget *latestMediaWidget) fetchEmbyLatest(ctx context.Context, host *latestMediaHostConfig) ([]latestMediaItem, error) {
	return widget.fetchJellyfinEmbyLatest(ctx, host, "emby")
}

func (widget *latestMediaWidget) fetchJellyfinEmbyLatest(ctx context.Context, host *latestMediaHostConfig, serverType string) ([]latestMediaItem, error) {
	client := ternary(host.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	baseURL := strings.TrimRight(host.BaseURL, "/")

	// Get first user ID (admin/first user)
	usersURL := fmt.Sprintf("%s/Users?api_key=%s", baseURL, host.Token)
	req, err := http.NewRequestWithContext(ctx, "GET", usersURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	users, err := decodeJsonFromRequest[jellyfinUsersResponse](client, req)
	if err != nil {
		return nil, fmt.Errorf("fetching users: %w", err)
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("no users found")
	}
	userID := users[0].Id

	// If no library filter, fetch latest from all
	if len(host.Libraries) == 0 {
		return widget.fetchJellyfinEmbyLatestFromParent(ctx, client, host, serverType, baseURL, userID, "")
	}

	// Fetch user views to find library IDs
	viewsURL := fmt.Sprintf("%s/UserViews?api_key=%s&userId=%s", baseURL, host.Token, userID)
	req, err = http.NewRequestWithContext(ctx, "GET", viewsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	views, err := decodeJsonFromRequest[jellyfinUserViewsResponse](client, req)
	if err != nil {
		return nil, fmt.Errorf("fetching user views: %w", err)
	}

	var allItems []latestMediaItem
	for _, view := range views.Items {
		if !containsString(host.Libraries, view.Name) {
			continue
		}
		items, err := widget.fetchJellyfinEmbyLatestFromParent(ctx, client, host, serverType, baseURL, userID, view.Id)
		if err != nil {
			slog.Warn("failed to fetch latest from library", "library", view.Name, "error", err)
			continue
		}
		allItems = append(allItems, items...)
	}

	return allItems, nil
}

func (widget *latestMediaWidget) fetchJellyfinEmbyLatestFromParent(
	ctx context.Context,
	client requestDoer,
	host *latestMediaHostConfig,
	serverType string,
	baseURL string,
	userID string,
	parentID string,
) ([]latestMediaItem, error) {
	url := fmt.Sprintf("%s/Users/%s/Items/Latest?api_key=%s&Limit=%d&Fields=DateCreated,RunTimeTicks,AlbumArtist",
		baseURL, userID, host.Token, widget.ItemCount)
	if parentID != "" {
		url += "&ParentId=" + parentID
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	rawItems, err := decodeJsonFromRequest[[]jellyfinLatestItem](client, req)
	if err != nil {
		return nil, err
	}

	var items []latestMediaItem
	for _, raw := range rawItems {
		if isJellyfinEmbyMusicType(raw.Type) {
			continue
		}

		addedAt, _ := time.Parse(time.RFC3339, raw.DateCreated)
		if addedAt.IsZero() {
			addedAt, _ = time.Parse("2006-01-02T15:04:05.0000000Z", raw.DateCreated)
		}

		item := latestMediaItem{
			ServerType:  serverType,
			ServerURL:   host.BaseURL,
			Title:       raw.Name,
			Year:        raw.ProductionYear,
			MediaType:   strings.ToLower(raw.Type),
			Duration:    raw.RunTimeTicks / 10000,
			AddedAt:     addedAt,
			SeriesTitle: raw.SeriesName,
		}

		if raw.Id != "" {
			item.CoverURL = fmt.Sprintf("%s/Items/%s/Images/Art?api_key=%s", baseURL, raw.Id, host.Token)
			item.ThumbnailURL = fmt.Sprintf("%s/Items/%s/Images/Primary?api_key=%s", baseURL, raw.Id, host.Token)
			item.LinkURL = fmt.Sprintf("%s/web/index.html#!/details?id=%s", host.PublicBaseURL, raw.Id)
		}

		items = append(items, item)
	}

	return items, nil
}

// --- Helpers ---

// stripAPIKeysFromError removes sensitive API keys from error messages
func stripAPIKeysFromError(err error) string {
	if err == nil {
		return ""
	}
	errStr := err.Error()
	// Strip api_key parameter from URLs
	errStr = regexp.MustCompile(`api_key=[^&\s"']+`).ReplaceAllString(errStr, "api_key=***")
	// Strip X-Plex-Token from URLs (if it appears in the error)
	errStr = regexp.MustCompile(`X-Plex-Token=[^&\s"']+`).ReplaceAllString(errStr, "X-Plex-Token=***")
	return errStr
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func isJellyfinEmbyMusicType(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "audio", "musicalbum", "musicartist":
		return true
	default:
		return false
	}
}

func formatTimeAgo(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	diff := now.Sub(t)
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		m := int(diff.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case diff < 24*time.Hour:
		h := int(diff.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	case diff < 7*24*time.Hour:
		d := int(diff.Hours() / 24)
		if d == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", d)
	case diff < 30*24*time.Hour:
		w := int(diff.Hours() / (24 * 7))
		if w == 1 {
			return "1w ago"
		}
		return fmt.Sprintf("%dw ago", w)
	default:
		mo := int(diff.Hours() / (24 * 30))
		if mo == 1 {
			return "1mo ago"
		}
		return fmt.Sprintf("%dmo ago", mo)
	}
}

func (widget *latestMediaWidget) setProviders(providers *widgetProviders) {
	widget.widgetBase.setProviders(providers)
	widget.cacheImageURLs()
}

func (widget *latestMediaWidget) cacheImageURLs() {
	widget.mu.Lock()
	defer widget.mu.Unlock()

	ctx := context.Background()
	for i := range widget.Items {
		item := &widget.Items[i]
		allowInsecure := widget.hostAllowInsecure[item.ServerURL]

		// Process cover URL
		if item.CoverURL != "" {
			originalURL := item.CoverURL
			hash := hashString(originalURL)

			// Register with the application's image proxy
			if widget.Providers != nil && widget.Providers.app != nil {
				widget.Providers.app.registerImageProxy(hash, originalURL, allowInsecure)
			}

			// Try to cache the image
			if widget.Providers != nil && widget.Providers.imageCache != nil {
				cachedURL, err := widget.Providers.imageCache.CacheURLWithClient(ctx, originalURL, allowInsecure)
				if err == nil && cachedURL != "" {
					// Successfully cached, use the cached URL
					item.CoverURL = cachedURL
				} else {
					// Failed to cache, use a proxy URL that doesn't expose the API key
					item.CoverURL = fmt.Sprintf("/api/image-proxy/%s", hash)
					if err != nil {
						slog.Debug("failed to cache cover image, using proxy", "hash", hash, "error", stripAPIKeysFromError(err))
					}
				}
			} else {
				// No cache available, use proxy URL
				item.CoverURL = fmt.Sprintf("/api/image-proxy/%s", hash)
			}
		}

		// Process thumbnail URL
		if item.ThumbnailURL != "" {
			originalURL := item.ThumbnailURL
			hash := hashString(originalURL)

			// Register with the application's image proxy
			if widget.Providers != nil && widget.Providers.app != nil {
				widget.Providers.app.registerImageProxy(hash, originalURL, allowInsecure)
			}

			// Try to cache the image
			if widget.Providers != nil && widget.Providers.imageCache != nil {
				cachedURL, err := widget.Providers.imageCache.CacheURLWithClient(ctx, originalURL, allowInsecure)
				if err == nil && cachedURL != "" {
					// Successfully cached, use the cached URL
					item.ThumbnailURL = cachedURL
				} else {
					// Failed to cache, use a proxy URL that doesn't expose the API key
					item.ThumbnailURL = fmt.Sprintf("/api/image-proxy/%s", hash)
					if err != nil {
						slog.Debug("failed to cache thumbnail image, using proxy", "hash", hash, "error", stripAPIKeysFromError(err))
					}
				}
			} else {
				// No cache available, use proxy URL
				item.ThumbnailURL = fmt.Sprintf("/api/image-proxy/%s", hash)
			}
		}
	}
}

func (widget *latestMediaWidget) Render() template.HTML {
	widget.mu.RLock()
	defer widget.mu.RUnlock()
	return widget.renderTemplate(widget, latestMediaWidgetTemplate)
}
