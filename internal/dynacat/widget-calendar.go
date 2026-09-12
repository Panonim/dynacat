package dynacat

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

	"gopkg.in/yaml.v3"
)

type stringListField []string

func (s *stringListField) UnmarshalYAML(node *yaml.Node) error {
	var list []string
	if err := node.Decode(&list); err == nil {
		*s = list
		return nil
	}

	var single string
	if err := node.Decode(&single); err != nil {
		return err
	}

	for _, part := range strings.Split(single, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			*s = append(*s, trimmed)
		}
	}
	return nil
}

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

type calendarReleaseService struct {
	URL           string `yaml:"url"`
	PublicURL     string `yaml:"public-url"`
	Token         string `yaml:"token"`
	AllowInsecure bool   `yaml:"allow-insecure"`

	serverType    string `yaml:"-"`
	baseURL       string `yaml:"-"`
	publicBaseURL string `yaml:"-"`
}

type calendarReleaseItem struct {
	Source      string `json:"source"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail"`
	Link        string `json:"link"`
	Type        string `json:"type"`
	State       string `json:"state"`

	dedupKey string
}

type calendarReleaseCacheEntry struct {
	fetchedAt time.Time
	data      map[string][]calendarReleaseItem
}

type calendarWidget struct {
	widgetBase       `yaml:",inline"`
	FirstDayOfWeek   string                   `yaml:"first-day-of-week"`
	FirstDay         int                      `yaml:"-"`
	Frameless        bool                     `yaml:"frameless"`
	Hosts            []calendarReleaseService `yaml:"hosts"`
	ReleaseTypes     stringListField          `yaml:"release-types"`
	ShowReleaseState bool                     `yaml:"show-release-state"`

	cachedHTML          template.HTML   `yaml:"-"`
	releasesInterval    time.Duration   `yaml:"-"`
	enabledReleaseTypes map[string]bool `yaml:"-"`

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

	widget.enabledReleaseTypes = map[string]bool{}
	if len(widget.ReleaseTypes) == 0 {
		widget.enabledReleaseTypes = map[string]bool{"cinema": true, "physical": true, "digital": true, "episode": true}
	} else {
		for _, t := range widget.ReleaseTypes {
			switch normalized := strings.ToLower(strings.TrimSpace(t)); normalized {
			case "cinema", "physical", "digital", "episode":
				widget.enabledReleaseTypes[normalized] = true
			}
		}
	}

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

func (widget *calendarWidget) HasReleases() bool {
	return len(widget.Hosts) > 0
}

func (widget *calendarWidget) ReleasesIntervalMs() int64 {
	return widget.releasesInterval.Milliseconds()
}

// Always 0 so the page never HTMX-swaps the calendar and resets the viewed month.
func (widget *calendarWidget) UpdateIntervalMs() int64 {
	return 0
}

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

	if !withinCalendarReleaseRange(year, time.Month(month), time.Now()) {
		http.Error(w, "month out of range", http.StatusBadRequest)
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

// Bounds the release cache and the traffic a caller can aim at the configured services.
func withinCalendarReleaseRange(year int, month time.Month, now time.Time) bool {
	requested := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	return !requested.Before(current.AddDate(-2, 0, 0)) && !requested.After(current.AddDate(2, 0, 0))
}
