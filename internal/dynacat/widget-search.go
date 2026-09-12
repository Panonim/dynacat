package dynacat

import (
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"strings"
	"sync"
)

var searchWidgetTemplate = mustParseTemplate("search.html", "widget-base.html")

type SearchBang struct {
	Title    string
	Shortcut string
	URL      string
	Icon     customIconField `yaml:"icon"`
}

type searchTargetMatch struct {
	Kind   string
	Title  string
	URL    string
	Target string
	Icon   customIconField
}

// A suggestion endpoint taken from the config, which may point at a private
// instance the user runs themselves.
type searchAutocompleteSource struct {
	URL          string
	AllowPrivate bool
}

type searchWidget struct {
	widgetBase                `yaml:",inline"`
	cachedHTML                template.HTML       `yaml:"-"`
	Frameless                 bool                `yaml:"frameless"`
	SearchEngine              string              `yaml:"search-engine"`
	DegoogURL                 string              `yaml:"degoog-url"`
	autocompleteAllowPrivate  bool                `yaml:"-"`
	AutocompleteEnabled       *bool               `yaml:"autocomplete"`
	Autocomplete              bool                `yaml:"-"`
	AutocompleteProvider      string              `yaml:"autocomplete-provider"`
	DeprecatedAutocompleteURL string              `yaml:"autocomplete-url"`
	Bangs                     []SearchBang        `yaml:"bangs"`
	NewTab                    bool                `yaml:"new-tab"`
	Target                    string              `yaml:"target"`
	Autofocus                 bool                `yaml:"autofocus"`
	Placeholder               string              `yaml:"placeholder"`
	IncludeBookmarks          bool                `yaml:"include-bookmarks"`
	CrossPageBookmarks        bool                `yaml:"cross-page-bookmarks"`
	IncludeDocker             bool                `yaml:"include-docker"`
	CrossPageDocker           bool                `yaml:"cross-page-docker"`
	IncludeMonitor            bool                `yaml:"include-monitor"`
	CrossPageMonitor          bool                `yaml:"cross-page-monitor"`
	BookmarkMatches           []searchTargetMatch `yaml:"-"`
	LiveMatches               []searchTargetMatch `yaml:"-"`
}

func convertSearchUrl(url string) string {
	return strings.ReplaceAll(url, "{QUERY}", "!QUERY!")
}

var searchEngines = map[string]string{
	"duckduckgo": "https://duckduckgo.com/?q={QUERY}",
	"google":     "https://www.google.com/search?q={QUERY}",
	"bing":       "https://www.bing.com/search?q={QUERY}",
	"perplexity": "https://www.perplexity.ai/search?q={QUERY}",
	"kagi":       "https://kagi.com/search?q={QUERY}",
	"startpage":  "https://www.startpage.com/search?q={QUERY}",
	"qwant":      "https://www.qwant.com/?q={QUERY}&t=web",
	"brave":      "https://search.brave.com/search?q={QUERY}",
}

// Degoog serves search and OpenSearch suggestions on fixed paths, so the
// instance URL is all that's needed to derive both.
func (widget *searchWidget) applyDegoogURLs() error {
	base := strings.TrimRight(strings.TrimSpace(widget.DegoogURL), "/")
	if base == "" {
		return fmt.Errorf("degoog-url is required when search-engine is degoog")
	}

	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("degoog-url must be a full URL, e.g. https://degoog.example.com")
	}

	widget.SearchEngine = base + "/search?q={QUERY}"

	if widget.AutocompleteProvider == "" {
		widget.AutocompleteProvider = base + "/api/suggest/opensearch?q={QUERY}"
		widget.autocompleteAllowPrivate = true
	}

	return nil
}

func (widget *searchWidget) initialize() error {
	widget.withTitle("Search").withError(nil)
	widget.UpdateInterval = nil

	if widget.CrossPageBookmarks {
		widget.IncludeBookmarks = true
	}

	if widget.CrossPageDocker {
		widget.IncludeDocker = true
	}

	if widget.CrossPageMonitor {
		widget.IncludeMonitor = true
	}

	if widget.SearchEngine == "" {
		widget.SearchEngine = "duckduckgo"
	}

	if widget.Placeholder == "" {
		widget.Placeholder = "Type here to search…"
	}

	if widget.AutocompleteEnabled == nil {
		widget.Autocomplete = true
	} else {
		widget.Autocomplete = *widget.AutocompleteEnabled
	}

	if widget.SearchEngine == "degoog" {
		if err := widget.applyDegoogURLs(); err != nil {
			return err
		}
	}

	if widget.AutocompleteProvider == "custom" && widget.DeprecatedAutocompleteURL != "" {
		widget.AutocompleteProvider = widget.DeprecatedAutocompleteURL
	}

	if widget.AutocompleteProvider == "" {
		widget.AutocompleteProvider = "duckduckgo"
	} else if widget.AutocompleteProvider != "duckduckgo" && widget.AutocompleteProvider != "brave" &&
		!strings.Contains(widget.AutocompleteProvider, "{QUERY}") {
		return fmt.Errorf("autocomplete-provider must be \"duckduckgo\", \"brave\", or a custom URL containing {QUERY}")
	}

	if url, ok := searchEngines[widget.SearchEngine]; ok {
		widget.SearchEngine = url
	}

	widget.SearchEngine = convertSearchUrl(widget.SearchEngine)

	for i := range widget.Bangs {
		if widget.Bangs[i].Shortcut == "" {
			return fmt.Errorf("search bang #%d has no shortcut", i+1)
		}

		if widget.Bangs[i].URL == "" {
			return fmt.Errorf("search bang #%d has no URL", i+1)
		}

		widget.Bangs[i].URL = convertSearchUrl(widget.Bangs[i].URL)
	}

	return nil
}

func (widget *searchWidget) AutocompleteProviderKind() string {
	if widget.AutocompleteProvider == "duckduckgo" || widget.AutocompleteProvider == "brave" {
		return widget.AutocompleteProvider
	}
	return "custom"
}

func (widget *searchWidget) setProviders(providers *widgetProviders) {
	widget.widgetBase.setProviders(providers)
	for i := range widget.Bangs {
		widget.Bangs[i].Icon.prepare(providers)
	}
	if widget.AutocompleteProviderKind() == "custom" && providers.app != nil {
		providers.app.searchAutocompleteURLs[widget.GetID()] = searchAutocompleteSource{
			URL:          widget.AutocompleteProvider,
			AllowPrivate: widget.autocompleteAllowPrivate,
		}
	}
	widget.cachedHTML = widget.renderTemplate(widget, searchWidgetTemplate)
}

// Run only after every widget is registered; pageFilter nil matches all pages.
func (widget *searchWidget) collectBookmarks(app *application, pageFilter *page) {
	if !widget.IncludeBookmarks {
		return
	}

	ownPage := app.widgetToPage[widget.GetID()]

	var matches []searchTargetMatch
	seen := make(map[string]bool)
	for id, w := range app.widgetByID {
		bookmarks, ok := w.(*bookmarksWidget)
		if !ok {
			continue
		}

		source := app.widgetToPage[id]
		if pageFilter != nil && source != pageFilter {
			continue
		}

		if searchSourceIsRestricted(source, ownPage) {
			continue
		}

		for _, group := range bookmarks.Groups {
			for _, link := range group.Links {
				key := link.Title + "\x00" + link.URL
				if seen[key] {
					continue
				}
				seen[key] = true

				matches = append(matches, searchTargetMatch{
					Kind:   "bookmark",
					Title:  link.Title,
					URL:    link.URL,
					Target: link.Target,
					Icon:   link.Icon,
				})
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].Title < matches[j].Title })

	widget.BookmarkMatches = matches
	widget.cachedHTML = widget.renderTemplate(widget, searchWidgetTemplate)
}

// Containers and sites are only known once their widget has updated, so each of them
// publishes a snapshot here for search widgets on any page to read.
type searchTargetRegistry struct {
	mu         sync.RWMutex
	byWidgetID map[uint64][]searchTargetMatch
}

func (registry *searchTargetRegistry) publish(widgetID uint64, matches []searchTargetMatch) {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	if registry.byWidgetID == nil {
		registry.byWidgetID = make(map[uint64][]searchTargetMatch)
	}

	registry.byWidgetID[widgetID] = matches
}

// Published slices are replaced rather than modified, so handing them to fn is safe.
func (registry *searchTargetRegistry) each(fn func(widgetID uint64, matches []searchTargetMatch)) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	for id, matches := range registry.byWidgetID {
		fn(id, matches)
	}
}

func appendSearchTarget(matches []searchTargetMatch, kind, title, url string, sameTab bool, icon customIconField) []searchTargetMatch {
	if title == "" || url == "" {
		return matches
	}

	return append(matches, searchTargetMatch{
		Kind:   kind,
		Title:  title,
		URL:    url,
		Target: ternary(sameTab, "", "_blank"),
		Icon:   icon,
	})
}

func publishSearchTargets(widget widget, providers *widgetProviders, matches []searchTargetMatch) {
	if providers == nil || providers.app == nil {
		return
	}

	providers.app.searchTargets.publish(widget.GetID(), matches)
}

// Pages limited to certain users or groups stay hidden unless the search widget sits on them.
func searchSourceIsRestricted(source, ownPage *page) bool {
	return source != ownPage && source != nil && (len(source.AllowedUsers) > 0 || len(source.AllowedGroups) > 0)
}

// Gathered per render rather than once at startup, since the snapshots change while the app runs.
func (widget *searchWidget) collectLiveMatches() {
	if widget.Providers == nil || widget.Providers.app == nil {
		return
	}

	app := widget.Providers.app
	ownPage := app.widgetToPage[widget.GetID()]

	var matches []searchTargetMatch
	seen := make(map[string]bool)

	app.searchTargets.each(func(id uint64, targets []searchTargetMatch) {
		source := app.widgetToPage[id]
		if searchSourceIsRestricted(source, ownPage) {
			return
		}

		for _, target := range targets {
			var include, crossPage bool
			switch target.Kind {
			case "docker":
				include, crossPage = widget.IncludeDocker, widget.CrossPageDocker
			case "monitor":
				include, crossPage = widget.IncludeMonitor, widget.CrossPageMonitor
			}

			if !include || (!crossPage && source != ownPage) {
				continue
			}

			key := target.Kind + "\x00" + target.Title + "\x00" + target.URL
			if seen[key] {
				continue
			}
			seen[key] = true

			matches = append(matches, target)
		}
	})

	sort.Slice(matches, func(i, j int) bool {
		return strings.ToLower(matches[i].Title) < strings.ToLower(matches[j].Title)
	})

	widget.LiveMatches = matches
}

func (widget *searchWidget) TargetsEnabled() bool {
	return widget.IncludeBookmarks || widget.IncludeDocker || widget.IncludeMonitor
}

func (widget *searchWidget) TargetMatches() []searchTargetMatch {
	matches := make([]searchTargetMatch, 0, len(widget.BookmarkMatches)+len(widget.LiveMatches))
	matches = append(matches, widget.BookmarkMatches...)
	return append(matches, widget.LiveMatches...)
}

func (widget *searchWidget) Render() template.HTML {
	if widget.IncludeDocker || widget.IncludeMonitor {
		widget.collectLiveMatches()
		return widget.renderTemplate(widget, searchWidgetTemplate)
	}

	if widget.cachedHTML == "" {
		widget.cachedHTML = widget.renderTemplate(widget, searchWidgetTemplate)
	}
	return widget.cachedHTML
}
