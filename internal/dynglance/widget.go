package dynglance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

var widgetIDCounter atomic.Uint64

func newWidget(widgetType string) (widget, error) {
	if widgetType == "" {
		return nil, errors.New("widget 'type' property is empty or not specified")
	}

	var w widget

	switch widgetType {
	case "calendar":
		w = &calendarWidget{}
	case "calendar-legacy":
		return nil, errors.New("legacy calendar has been removed")
	case "clock":
		w = &clockWidget{}
	case "weather":
		w = &weatherWidget{}
	case "bookmarks":
		w = &bookmarksWidget{}
	case "iframe":
		w = &iframeWidget{}
	case "html":
		w = &htmlWidget{}
	case "hacker-news":
		w = &hackerNewsWidget{}
	case "releases":
		w = &releasesWidget{}
	case "videos":
		w = &videosWidget{}
	case "markets", "stocks":
		w = &marketsWidget{}
	case "reddit":
		w = &redditWidget{}
	case "rss":
		w = &rssWidget{}
	case "monitor":
		w = &monitorWidget{}
	case "twitch-top-games":
		w = &twitchGamesWidget{}
	case "twitch-channels":
		w = &twitchChannelsWidget{}
	case "lobsters":
		w = &lobstersWidget{}
	case "change-detection":
		w = &changeDetectionWidget{}
	case "repository":
		w = &repositoryWidget{}
	case "search":
		w = &searchWidget{}
	case "stopwatch":
		w = &stopwatchWidget{}
	case "extension":
		w = &extensionWidget{}
	case "group":
		w = &groupWidget{}
	case "dns-stats":
		w = &dnsStatsWidget{}
	case "split-column":
		w = &splitColumnWidget{}
	case "custom-api":
		w = &customAPIWidget{}
	case "dynawidgets":
		w = &dynawidgetsWidget{}
	case "docker-containers":
		w = &dockerContainersWidget{}
	case "docker-controller":
		w = &dockerControllerWidget{}
	case "server-stats":
		w = &serverStatsWidget{}
	case "speedtest":
		w = &speedtestWidget{}
	case "to-do":
		w = &todoWidget{}
	case "playing":
		w = &playingWidget{}
	case "latest-media":
		w = &latestMediaWidget{}
	case "torrenting":
		w = &torrentingWidget{}
	default:
		return nil, fmt.Errorf("unknown widget type: %s", widgetType)
	}

	w.setID(widgetIDCounter.Add(1))

	return w, nil
}

type widgets []widget

func (w *widgets) UnmarshalYAML(node *yaml.Node) error {
	var nodes []yaml.Node

	if err := node.Decode(&nodes); err != nil {
		return err
	}

	for _, node := range nodes {
		meta := struct {
			Type string `yaml:"type"`
		}{}

		if err := node.Decode(&meta); err != nil {
			return err
		}

		widget, err := newWidget(meta.Type)
		if err != nil {
			return fmt.Errorf("line %d: %w", node.Line, err)
		}

		if err = node.Decode(widget); err != nil {
			return err
		}

		*w = append(*w, widget)
	}

	return nil
}

type widget interface {
	// These need to be exported because they get called in templates
	Render() template.HTML
	GetType() string
	GetID() uint64

	initialize() error
	requiresUpdate(*time.Time) bool
	getCacheDuration() time.Duration
	setProviders(*widgetProviders)
	update(context.Context)
	setID(uint64)
	handleRequest(w http.ResponseWriter, r *http.Request)
	setHideHeader(bool)
}

type cacheType int

const (
	cacheTypeInfinite cacheType = iota
	cacheTypeDuration
	cacheTypeOnTheHour
)

type widgetBase struct {
	ID                  uint64               `yaml:"-"`
	Providers           *widgetProviders     `yaml:"-"`
	Type                string               `yaml:"type"`
	Title               string               `yaml:"title"`
	TitleIcon           customIconField      `yaml:"title-icon"`
	TitleURL            string               `yaml:"title-url"`
	HideHeader          bool                 `yaml:"hide-header"`
	Hidden              bool                 `yaml:"-"`
	CSSClass            string               `yaml:"css-class"`
	CustomCacheDuration durationField        `yaml:"cache"`
	UpdateInterval      *updateIntervalField `yaml:"update-interval"`
	ContentAvailable    bool                 `yaml:"-"`
	LazyLoad            bool                 `yaml:"lazy-load"`
	WIP                 bool                 `yaml:"-"`
	Error               error                `yaml:"-"`
	Notice              error                `yaml:"-"`
	templateBuffer      bytes.Buffer         `yaml:"-"`
	cacheDuration       time.Duration        `yaml:"-"`
	cacheType           cacheType            `yaml:"-"`
	nextUpdate          time.Time            `yaml:"-"`
	updateRetriedTimes  int                  `yaml:"-"`
}

type widgetProviders struct {
	assetResolver        func(string) string
	imageCache           *imageCache
	baseURL              string
	DynamicUpdateEnabled bool
	app                  *application
}

// SecureImageURL processes an image URL through the caching and proxy system.
// Returns either a cached URL, a proxy URL without credentials, or empty string on error.
// If allowInsecure is true, self-signed certificates will be accepted.
func (p *widgetProviders) SecureImageURL(ctx context.Context, imageURL string, allowInsecure bool) string {
	if imageURL == "" {
		return ""
	}

	parsedURL, err := url.Parse(imageURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return imageURL
	}

	hash := hashString(imageURL)

	// Register with the application's image proxy for on-demand serving
	if p.app != nil {
		p.app.registerImageProxy(hash, imageURL, allowInsecure)
	}

	// Try to cache the image
	if p.imageCache != nil {
		cachedURL, err := p.imageCache.CacheURLWithClient(ctx, imageURL, allowInsecure)
		if err == nil && cachedURL != "" {
			// Successfully cached, return the cached URL
			return cachedURL
		}
	}

	// Fall back to proxy URL (doesn't expose credentials)
	return fmt.Sprintf("/api/image-proxy/%s", hash)
}

func (w *widgetBase) requiresUpdate(now *time.Time) bool {
	if w.cacheType == cacheTypeInfinite {
		return false
	}

	if w.nextUpdate.IsZero() {
		// Lazy widgets skip the initial blocking fetch; JS triggers it after page load
		if w.LazyLoad {
			return false
		}
		return true
	}

	return now.After(w.nextUpdate)
}

func (w *widgetBase) IsWIP() bool {
	return w.WIP
}

// UpdateIntervalMs is the polling interval the page uses for this widget, in
// milliseconds. Widgets can override it to poll dynamically (e.g. faster while a
// background job runs, then back to the configured interval once it finishes).
func (w *widgetBase) UpdateIntervalMs() int64 {
	if w.UpdateInterval == nil {
		return 0
	}
	return w.UpdateInterval.Milliseconds()
}

func (w *widgetBase) IsLazyLoad() bool {
	return w.LazyLoad && !w.ContentAvailable
}

func (w *widgetBase) update(ctx context.Context) {

}

// getCacheDuration returns the effective time between updates for this widget,
// used as the reuse window for shared HTTP requests. Returns -1 for infinite
// cache widgets so they reuse shared responses freely.
func (w *widgetBase) getCacheDuration() time.Duration {
	switch w.cacheType {
	case cacheTypeDuration:
		if w.CustomCacheDuration == 0 && w.UpdateInterval != nil && *w.UpdateInterval > 0 {
			return time.Duration(*w.UpdateInterval)
		}
		return w.cacheDuration
	case cacheTypeOnTheHour:
		now := time.Now()
		return time.Duration(((60-now.Minute())*60)-now.Second()) * time.Second
	default:
		return -1
	}
}

func (w *widgetBase) GetID() uint64 {
	return w.ID
}

func (w *widgetBase) setID(id uint64) {
	w.ID = id
}

func (w *widgetBase) setHideHeader(value bool) {
	w.HideHeader = value
}

func (widget *widgetBase) handleRequest(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (w *widgetBase) GetType() string {
	return w.Type
}

func (w *widgetBase) setProviders(providers *widgetProviders) {
	w.Providers = providers
	w.TitleIcon.prepare(providers)
}

func (w *widgetBase) IsDynamicUpdateEnabled() bool {
	return w.Providers == nil || w.Providers.DynamicUpdateEnabled
}

func (w *widgetBase) GetBaseURL() string {
	if w.Providers == nil {
		return ""
	}
	return w.Providers.baseURL
}

func (w *widgetBase) renderTemplate(data any, t *template.Template) template.HTML {
	w.templateBuffer.Reset()
	err := t.Execute(&w.templateBuffer, data)
	if err != nil {
		w.ContentAvailable = false
		w.Error = err

		slog.Error("Failed to render template", "error", err)

		// need to immediately re-render with the error,
		// otherwise risk breaking the page since the widget
		// will likely be partially rendered with tags not closed.
		w.templateBuffer.Reset()
		err2 := t.Execute(&w.templateBuffer, data)

		if err2 != nil {
			slog.Error("Failed to render error within widget", "error", err2, "initial_error", err)
			w.templateBuffer.Reset()
		}
	}

	return template.HTML(w.templateBuffer.String())
}

func (w *widgetBase) withTitle(title string) *widgetBase {
	if w.Title == "" {
		w.Title = title
	}

	return w
}

func (w *widgetBase) withTitleURL(titleURL string) *widgetBase {
	if w.TitleURL == "" {
		w.TitleURL = titleURL
	}

	return w
}

func (w *widgetBase) withCacheDuration(duration time.Duration) *widgetBase {
	w.cacheType = cacheTypeDuration

	if duration == -1 || w.CustomCacheDuration == 0 {
		w.cacheDuration = duration
	} else {
		w.cacheDuration = time.Duration(w.CustomCacheDuration)
	}

	return w
}

func (w *widgetBase) withCacheOnTheHour() *widgetBase {
	w.cacheType = cacheTypeOnTheHour

	return w
}

func (w *widgetBase) withNotice(err error) *widgetBase {
	w.Notice = err

	return w
}

func (w *widgetBase) withError(err error) *widgetBase {
	if err == nil && !w.ContentAvailable {
		w.ContentAvailable = true
	}

	w.Error = err

	return w
}

func (w *widgetBase) canContinueUpdateAfterHandlingErr(err error) bool {
	if err != nil {
		w.scheduleEarlyUpdate()

		if !errors.Is(err, errPartialContent) {
			w.withError(err)
			w.withNotice(nil)
			return false
		}

		w.withError(nil)
		w.withNotice(err)
		return true
	}

	w.withNotice(nil)
	w.withError(nil)
	w.scheduleNextUpdate()
	return true
}

func (w *widgetBase) getNextUpdateTime() time.Time {
	now := time.Now()

	if w.cacheType == cacheTypeDuration {
		if w.CustomCacheDuration == 0 && w.UpdateInterval != nil && *w.UpdateInterval > 0 {
			return now.Add(time.Duration(*w.UpdateInterval))
		}
		return now.Add(w.cacheDuration)
	}

	if w.cacheType == cacheTypeOnTheHour {
		return now.Add(time.Duration(
			((60-now.Minute())*60)-now.Second(),
		) * time.Second)
	}

	return time.Time{}
}

func (w *widgetBase) scheduleNextUpdate() *widgetBase {
	w.nextUpdate = w.getNextUpdateTime()
	w.updateRetriedTimes = 0

	return w
}

func (w *widgetBase) scheduleEarlyUpdate() *widgetBase {
	w.updateRetriedTimes++

	if w.updateRetriedTimes > 5 {
		w.updateRetriedTimes = 5
	}

	nextEarlyUpdate := time.Now().Add(time.Duration(math.Pow(float64(w.updateRetriedTimes), 2)) * time.Minute)
	nextUsualUpdate := w.getNextUpdateTime()

	if nextEarlyUpdate.After(nextUsualUpdate) {
		w.nextUpdate = nextUsualUpdate
	} else {
		w.nextUpdate = nextEarlyUpdate
	}

	return w
}
