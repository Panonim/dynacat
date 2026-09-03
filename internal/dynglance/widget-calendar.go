package dynglance

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var calendarWidgetTemplate = mustParseTemplate("calendar.html", "widget-base.html")

var calendarWeekdaysToInt = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

const calendarDefaultReleasesInterval = 15 * time.Minute

// calendarReleaseService mirrors the host config style of the latest-media /
// playing widgets: the service type is a prefix on the URL (e.g.
// "radarr:https://radarr.domain.com").
type calendarReleaseService struct {
	URL           string `yaml:"url"`
	PublicURL     string `yaml:"public-url"`
	Token         string `yaml:"token"`
	AllowInsecure bool   `yaml:"allow-insecure"`

	serverType    string `yaml:"-"`
	baseURL       string `yaml:"-"`
	publicBaseURL string `yaml:"-"`
}

// calendarReleaseItem is a single release shown on a day, serialized to the client.
type calendarReleaseItem struct {
	Source      string `json:"source"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail"`
	Link        string `json:"link"`

	// dedupKey identifies the underlying movie/episode so the same release coming
	// from multiple hosts of the same type is only shown once. Unexported so it is
	// not serialized to the client.
	dedupKey string
}

type calendarReleaseCacheEntry struct {
	fetchedAt time.Time
	data      map[string][]calendarReleaseItem
}

type calendarWidget struct {
	widgetBase             `yaml:",inline"`
	FirstDayOfWeek         string                   `yaml:"first-day-of-week"`
	FirstDay               int                      `yaml:"-"`
	Frameless              bool                     `yaml:"frameless"`
	Hosts                  []calendarReleaseService `yaml:"hosts"`

	cachedHTML       template.HTML `yaml:"-"`
	releasesInterval time.Duration `yaml:"-"`

	releaseCacheMu sync.Mutex                           `yaml:"-"`
	releaseCache   map[string]calendarReleaseCacheEntry `yaml:"-"`
}

func (widget *calendarWidget) initialize() error {
	widget.withTitle("Calendar").withError(nil)

	if widget.FirstDayOfWeek == "" {
		widget.FirstDayOfWeek = "monday"
	} else if _, ok := calendarWeekdaysToInt[widget.FirstDayOfWeek]; !ok {
		return errors.New("invalid first day of week")
	}

	widget.FirstDay = int(calendarWeekdaysToInt[widget.FirstDayOfWeek])

	widget.releasesInterval = calendarDefaultReleasesInterval
	if widget.UpdateInterval != nil {
		if interval := time.Duration(*widget.UpdateInterval); interval > 0 {
			widget.releasesInterval = interval
		}
	}

	for i := range widget.Hosts {
		service := &widget.Hosts[i]

		if service.Token == "" {
			return errors.New("calendar release service token is required")
		}

		serverType, baseURL, err := parseCalendarReleaseURL(service.URL)
		if err != nil {
			return fmt.Errorf("invalid calendar release url %q: %w", service.URL, err)
		}

		service.serverType = serverType
		service.baseURL = baseURL

		if service.PublicURL != "" {
			service.publicBaseURL = strings.TrimRight(service.PublicURL, "/")
		} else {
			service.publicBaseURL = baseURL
		}
	}

	widget.releaseCache = make(map[string]calendarReleaseCacheEntry)
	widget.cachedHTML = widget.renderTemplate(widget, calendarWidgetTemplate)

	return nil
}

func (widget *calendarWidget) Render() template.HTML {
	return widget.cachedHTML
}

// HasReleases reports whether any Sonarr/Radarr services are configured, used by
// the template to enable the client-side release fetching.
func (widget *calendarWidget) HasReleases() bool {
	return len(widget.Hosts) > 0
}

// ReleasesIntervalMs is the live-refresh interval the client uses to poll for
// release updates, in milliseconds.
func (widget *calendarWidget) ReleasesIntervalMs() int64 {
	return widget.releasesInterval.Milliseconds()
}

// UpdateIntervalMs is always 0: HTMX polling would swap the widget and reset the viewed month; ReleasesIntervalMs drives polling instead.
func (widget *calendarWidget) UpdateIntervalMs() int64 {
	return 0
}

// handleRequest serves per-month release data for the action
// "releases/{year}/{month}". Results are cached server-side for the refresh
// interval so repeated/concurrent requests do not refetch.
func (widget *calendarWidget) handleRequest(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.PathValue("action"), "/"), "/")

	if len(parts) != 3 || parts[0] != "releases" {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	year, err := strconv.Atoi(parts[1])
	if err != nil || year < 1970 || year > 9999 {
		http.Error(w, "invalid year", http.StatusBadRequest)
		return
	}

	month, err := strconv.Atoi(parts[2])
	if err != nil || month < 1 || month > 12 {
		http.Error(w, "invalid month", http.StatusBadRequest)
		return
	}

	if len(widget.Hosts) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
		return
	}

	data := widget.getReleasesForMonth(r.Context(), year, time.Month(month))

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(data)
}
