package dynacat

import (
	"reflect"
	"slices"
	"strings"
)

type widgetFieldSchema struct {
	Name     string              `json:"name"`
	Label    string              `json:"label"`
	Kind     string              `json:"kind"` // text|number|checkbox|select|duration|icon|yaml|text-block|list
	Hint     string              `json:"hint,omitempty"`
	Required bool                `json:"required,omitempty"`
	Advanced bool                `json:"advanced,omitempty"`
	Hidden   bool                `json:"hidden,omitempty"`
	Options  []string            `json:"options,omitempty"`
	Item     []widgetFieldSchema `json:"item,omitempty"`
	ItemKind string              `json:"itemKind,omitempty"`
}

type widgetTypeSchema struct {
	Type   string              `json:"type"`
	Label  string              `json:"label"`
	Icon   string              `json:"icon"`
	Docs   string              `json:"docs,omitempty"`
	Hidden bool                `json:"hidden,omitempty"`
	Fields []widgetFieldSchema `json:"fields"`
}

var hiddenWidgetTypes = map[string]bool{
	"html": true,
}

const docsBaseURL = "https://dynacat.artur.zone/#"

type widgetTypeMeta struct {
	Type  string
	Label string
	Icon  string
	Docs  string
}

// Keep in sync with the newWidget switch in widget.go.
var widgetTypeCatalog = []widgetTypeMeta{
	{"calendar", "Calendar", "mdi:calendar", "configuration/calendar"},
	{"clock", "Clock", "mdi:clock-outline", "configuration/clock"},
	{"weather", "Weather", "mdi:weather-partly-cloudy", "configuration/weather"},
	{"bookmarks", "Bookmarks", "mdi:bookmark-outline", "configuration/bookmarks"},
	{"iframe", "IFrame", "mdi:application-brackets-outline", "configuration/iframe"},
	{"html", "HTML", "mdi:language-html5", "configuration/html"},
	{"hacker-news", "Hacker News", "mdi:newspaper-variant-outline", "configuration/hacker-news"},
	{"releases", "Releases", "mdi:tag-outline", "configuration/releases"},
	{"videos", "Videos", "mdi:youtube", "configuration/videos"},
	{"markets", "Markets", "mdi:chart-line", "configuration/markets"},
	{"reddit", "Reddit", "mdi:reddit", "configuration/reddit"},
	{"rss", "RSS", "mdi:rss", "configuration/rss"},
	{"monitor", "Monitor", "mdi:heart-pulse", "configuration/monitor"},
	{"twitch-top-games", "Twitch Top Games", "mdi:twitch", "configuration/twitch-top-games"},
	{"twitch-channels", "Twitch Channels", "mdi:twitch", "configuration/twitch-channels"},
	{"lobsters", "Lobsters", "mdi:message-text-outline", "configuration/lobsters"},
	{"change-detection", "Change Detection", "mdi:eye-outline", "configuration/changedetectionio"},
	{"repository", "Repository", "mdi:source-repository", "configuration/repository"},
	{"search", "Search", "mdi:magnify", "configuration/search-widget"},
	{"stopwatch", "Stopwatch", "mdi:timer-outline", "configuration/stopwatch"},
	{"extension", "Extension", "mdi:puzzle-outline", "configuration/extension"},
	{"group", "Group", "mdi:tab", "configuration/group"},
	{"dns-stats", "DNS Stats", "mdi:dns-outline", "configuration/dns-stats"},
	{"split-column", "Split Column", "mdi:view-column-outline", "configuration/split-column"},
	{"custom-api", "Custom API", "mdi:api", "custom-api"},
	{"dynawidgets", "Dynawidgets", "mdi:widgets-outline", "configuration/dynawidgets"},
	{"docker-containers", "Docker Containers", "mdi:docker", "configuration/docker-containers"},
	{"docker-controller", "Docker Controller", "mdi:docker", "configuration/docker-controller"},
	{"server-stats", "Server Stats", "mdi:server", "configuration/server-stats"},
	{"speedtest", "Speedtest", "mdi:speedometer", "configuration/speedtest"},
	{"to-do", "To-do", "mdi:checkbox-marked-outline", "configuration/todo"},
	{"playing", "Now Playing", "mdi:play-circle-outline", "configuration/currently-playing"},
	{"latest-media", "Latest Media", "mdi:multimedia", "configuration/latest-media"},
	{"torrenting", "Torrenting", "mdi:download-network-outline", "configuration/torrenting"},
}

type fieldAnnotation struct {
	Advanced bool
	Hidden   bool
	Kind     string
	Options  []string
	// Hint is a short note shown under the field's label in the editor, keyed by field
	// name ("<list-field>.<entry-field>" for entries inside a list).
	Hint string
}

var alwaysAdvancedFields = map[string]bool{
	"title": true, "title-icon": true, "title-url": true, "hide-header": true,
	"css-class": true, "cache": true, "update-interval": true, "lazy-load": true,
	"frameless": true, "api-id": true,
}

// Fields the user must fill in for the widget to work. Fields of a list entry are addressed
// as "<list-field>.<entry-field>". Mirrors the "required" column in docs/docs/configuration.md.
var requiredFields = map[string][]string{
	"bookmarks":       {"groups", "groups.title", "groups.links", "groups.links.title", "groups.links.url"},
	"calendar":        {"hosts.url", "hosts.token"},
	"clock":           {"timezones.timezone"},
	"custom-api":      {"template"},
	"dns-stats":       {"url"},
	"dynawidgets":     {"widget"},
	"extension":       {"url"},
	"html":            {"source"},
	"iframe":          {"source"},
	"latest-media":    {"hosts", "hosts.url", "hosts.token"},
	"markets":         {"markets", "markets.symbol", "stocks.symbol"},
	"monitor":         {"sites", "sites.title", "sites.url"},
	"playing":         {"hosts", "hosts.url", "hosts.token"},
	"reddit":          {"subreddit"},
	"releases":        {"repositories", "repositories.repository"},
	"repository":      {"repository"},
	"rss":             {"feeds", "feeds.url"},
	"search":          {"bangs.shortcut", "bangs.url"},
	"server-stats":    {"servers.url"},
	"torrenting":      {"hosts", "hosts.url"},
	"twitch-channels": {"channels"},
	"videos":          {"channels"},
	"weather":         {"location"},
}

// Per-entry fields tucked behind the "Advanced" toggle of a list card.
var itemAdvancedFields = map[string]bool{
	"check-url": true, "error-url": true, "method": true, "timeout": true,
	"allow-insecure": true, "basic-auth": true, "token": true, "headers": true,
	"alt-status-codes": true, "same-tab": true, "target": true, "hide-arrow": true,
	"disabled": true, "item-link-prefix": true, "public-url": true, "invert-colors": true,
}

const maxListDepth = 2

var fieldAnnotations = map[string]map[string]fieldAnnotation{
	"calendar": {
		"first-day-of-week": {Options: []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}},
		"hosts.url":         {Hint: "Prefix with the service, e.g. sonarr:https://... or radarr:https://..."},
		"hosts.token":       {Hint: "API key from the Sonarr/Radarr instance's Settings → General."},
	},
	"clock": {
		"hour-format":        {Options: []string{"24h", "12h"}},
		"timezones.timezone": {Hint: "IANA identifier, e.g. Europe/London."},
	},
	"weather": {
		"hour-format": {Options: []string{"12h", "24h"}},
		"units":       {Options: []string{"metric", "imperial"}},
		"location":    {Hint: `City and country, e.g. "Berlin, Germany" — the first match from Open-Meteo is used.`},
	},
	"hacker-news": {
		"sort-by":               {Options: []string{"top", "new", "best"}},
		"extra-sort-by":         {Options: []string{"engagement"}},
		"comments-url-template": {Advanced: true},
	},
	"releases": {
		"token":                   {Advanced: true, Hint: "Personal access token, only needed to avoid GitHub's 60 requests/hour limit."},
		"gitlab-token":            {Advanced: true, Hint: "Same as token above but for GitLab, only needed to avoid rate limiting."},
		"repositories.repository": {Hint: "owner/repo for GitHub, or prefix with gitlab:, codeberg: or dockerhub: for other sources."},
	},
	"repository": {
		"token":      {Advanced: true, Hint: "Personal access token, only needed to avoid GitHub's 60 requests/hour limit."},
		"repository": {Hint: "owner/repo, e.g. Panonim/dynacat."},
	},
	"videos": {
		"style":              {Options: []string{"grid-cards", "vertical-list"}},
		"video-url-template": {Advanced: true},
		"channels":           {Hint: "Channel IDs, not usernames — found in the channel page source or a lookup tool."},
		"playlists":          {Hint: "Playlist IDs, found in the playlist's URL."},
	},
	"markets": {
		"sort-by":              {Options: []string{"absolute-change", "change"}},
		"chart-link-template":  {Advanced: true},
		"symbol-link-template": {Advanced: true},
		"proxy":                {Advanced: true},
	},
	"reddit": {
		"sort-by":               {Options: []string{"hot", "new", "top", "rising"}},
		"top-period":            {Options: []string{"hour", "day", "week", "month", "year", "all"}},
		"style":                 {Options: []string{"horizontal-cards", "vertical-cards"}},
		"extra-sort-by":         {Options: []string{"engagement"}},
		"proxy":                 {Advanced: true},
		"comments-url-template": {Advanced: true},
		"request-url-template":  {Advanced: true},
		"app-auth":              {Advanced: true},
	},
	"rss": {
		"style": {Options: []string{"detailed-list", "horizontal-cards", "horizontal-cards-2"}},
	},
	"monitor": {
		"style":     {Options: []string{"compact"}},
		"sites.url": {Hint: "Also used for the status check unless check-url is set."},
	},
	"twitch-channels": {
		"sort-by": {Options: []string{"viewers", "live"}},
	},
	"twitch-top-games": {
		"exclude": {Hint: "Category slugs, found in the category page URL on Twitch."},
	},
	"lobsters": {
		"sort-by": {Options: []string{"hot", "new"}},
	},
	"change-detection": {
		"allow-insecure": {Advanced: true},
		"token":          {Advanced: true, Hint: "Found in changedetection.io under Settings → API."},
		"instance-url":   {Hint: "Base URL of your changedetection.io instance."},
	},
	"search": {
		"search-engine":         {Options: []string{"duckduckgo", "google", "bing", "perplexity", "kagi", "startpage", "qwant", "brave", "degoog", "custom"}},
		"degoog-url":            {Hint: "Base URL of your Degoog instance."},
		"autocomplete-provider": {Options: []string{"duckduckgo", "brave", "custom"}},
		"bangs.shortcut":        {Hint: `The prefix typed before a search, e.g. "yt" for !yt.`},
	},
	"extension": {
		"fallback-content-type":            {Options: []string{"html"}},
		"headers":                          {Advanced: true},
		"allow-potentially-dangerous-html": {Advanced: true},
		"url":                              {Hint: "Query parameters are stripped and replaced with those from parameters below."},
	},
	"dns-stats": {
		"service":        {Options: []string{"adguard", "pihole", "pihole-v6", "technitium", "blocky"}},
		"hour-format":    {Options: []string{"12h", "24h"}},
		"token":          {Advanced: true, Hint: "Only used with Pi-hole v5 or earlier, found under Settings → API."},
		"username":       {Advanced: true, Hint: "Only used with AdGuard Home."},
		"password":       {Advanced: true, Hint: "Only used with AdGuard Home."},
		"allow-insecure": {Advanced: true},
		"url":            {Hint: "Base URL of the AdGuard Home / Pi-hole / Technitium / Blocky instance."},
	},
	"custom-api": {
		"template":       {Kind: "text-block", Hint: "Go html/template syntax with gjson selectors for parsing the response — see the Custom API docs."},
		"builder":        {Hidden: true},
		"subrequests":    {Advanced: true},
		"method":         {Advanced: true},
		"body":           {Advanced: true},
		"body-type":      {Advanced: true, Options: []string{"json", "string"}},
		"headers":        {Advanced: true},
		"allow-insecure": {Advanced: true},
	},
	"dynawidgets": {
		"repo":           {Advanced: true, Options: []string{"main", "testing"}, Hint: "Branch of the dynawidgets repository to fetch the widget from, defaults to main."},
		"subrequests":    {Advanced: true},
		"method":         {Advanced: true},
		"body":           {Advanced: true},
		"body-type":      {Advanced: true, Options: []string{"json", "string"}},
		"headers":        {Advanced: true},
		"allow-insecure": {Advanced: true},
		"widget":         {Hint: "Widget from the dynawidgets repository."},
	},
	"docker-containers": {
		"sock-path": {Advanced: true, Hint: "Defaults to /var/run/docker.sock; can also be a tcp://host:port or http://host:port address."},
	},
	"docker-controller": {
		"show":      {Options: []string{"both", "containers", "images"}},
		"sock-path": {Advanced: true, Hint: "Defaults to /var/run/docker.sock; can also be a tcp://host:port or http://host:port address."},
	},
	"to-do": {
		"storage": {Options: []string{"local", "server"}},
		"id":      {Advanced: true},
	},
	"playing": {
		"play-state":           {Options: []string{"indicator", "text"}},
		"episode-title-format": {Options: []string{"series", "episode"}},
		"hosts.url":            {Hint: "Prefix with the service, e.g. plex:https://..., jellyfin:https://..., emby:https://... or navidrome:https://..."},
		"hosts.token":          {Hint: "Plex token, or the Jellyfin/Emby/Navidrome API key."},
	},
	"latest-media": {
		"hosts.url":   {Hint: "Prefix with the service, e.g. plex:https://..., jellyfin:https://... or emby:https://..."},
		"hosts.token": {Hint: "Plex token, or the Jellyfin/Emby API key."},
	},
	"torrenting": {
		"hosts.client": {Hint: "qbittorrent (default), deluge or transmission."},
	},
	"speedtest": {
		"server": {Hint: "Leave empty to auto-select a public LibreSpeed server; or set the base URL of your own LibreSpeed instance."},
	},
	"server-stats": {
		"servers":     {Hint: "Leave empty to show stats for the server Dynacat itself is running on."},
		"servers.url": {Hint: "Address and port of the remote server's Dynacat Agent."},
	},
}

func widgetSchema(meta widgetTypeMeta) (widgetTypeSchema, error) {
	w, err := newWidget(meta.Type)
	if err != nil {
		return widgetTypeSchema{}, err
	}

	fields := reflectWidgetFields(reflect.TypeOf(w).Elem(), 0)
	ann := fieldAnnotations[meta.Type]

	for i := range fields {
		f := &fields[i]
		f.Advanced = alwaysAdvancedFields[f.Name]
		f.Label = humanizeFieldName(f.Name)

		a, ok := ann[f.Name]
		if !ok {
			continue
		}
		if a.Advanced {
			f.Advanced = true
		}
		f.Hidden = a.Hidden
		if a.Kind != "" {
			f.Kind = a.Kind
		}
		if len(a.Options) > 0 {
			f.Kind = "select"
			f.Options = a.Options
		}
	}

	applyFieldHints(fields, "", ann)
	markRequiredFields(fields, "", requiredFields[meta.Type])

	docs := ""
	if meta.Docs != "" {
		docs = docsBaseURL + meta.Docs
	}

	return widgetTypeSchema{
		Type:   meta.Type,
		Label:  meta.Label,
		Icon:   string(newCustomIconField(meta.Icon).URL),
		Docs:   docs,
		Hidden: hiddenWidgetTypes[meta.Type],
		Fields: fields,
	}, nil
}

// applyFieldHints sets each field's Hint from ann, keyed the same way as requiredFields:
// the plain name at the top level, "<list-field>.<entry-field>" for entries inside a list.
func applyFieldHints(fields []widgetFieldSchema, prefix string, ann map[string]fieldAnnotation) {
	for i := range fields {
		path := prefix + fields[i].Name
		fields[i].Hint = ann[path].Hint
		applyFieldHints(fields[i].Item, path+".", ann)
	}
}

func markRequiredFields(fields []widgetFieldSchema, prefix string, required []string) {
	for i := range fields {
		path := prefix + fields[i].Name
		fields[i].Required = slices.Contains(required, path)
		if fields[i].Required {
			fields[i].Advanced = false
		}
		markRequiredFields(fields[i].Item, path+".", required)
	}
}

func allWidgetSchemas() []widgetTypeSchema {
	schemas := make([]widgetTypeSchema, 0, len(widgetTypeCatalog))
	for _, meta := range widgetTypeCatalog {
		if s, err := widgetSchema(meta); err == nil {
			schemas = append(schemas, s)
		}
	}
	return schemas
}

var deprecatedSchemaFields = map[string]bool{
	"autocomplete-url": true,
}

func reflectWidgetFields(t reflect.Type, depth int) []widgetFieldSchema {
	var fields []widgetFieldSchema

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, inline := parseYAMLTag(f.Tag.Get("yaml"))

		ft := f.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}

		if inline && ft.Kind() == reflect.Struct {
			fields = append(fields, reflectWidgetFields(ft, depth)...)
			continue
		}
		// List entries often come from external structs with no yaml tags, which yaml.v3 maps by lowercased name.
		if name == "" && depth > 0 && f.IsExported() {
			name = strings.ToLower(f.Name)
		}
		if name == "" || name == "-" || name == "type" || !f.IsExported() || deprecatedSchemaFields[name] {
			continue
		}

		fields = append(fields, reflectField(name, f.Type, depth))
	}

	return fields
}

func reflectField(name string, t reflect.Type, depth int) widgetFieldSchema {
	field := widgetFieldSchema{Name: name, Kind: fieldKind(t)}
	if field.Kind != "yaml" || depth >= maxListDepth {
		return field
	}

	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Slice {
		return field
	}

	elem := t.Elem()
	for elem.Kind() == reflect.Ptr {
		elem = elem.Elem()
	}

	if elemKind := fieldKind(elem); elemKind != "yaml" {
		field.Kind, field.ItemKind = "list", elemKind
		return field
	}
	if elem.Kind() != reflect.Struct {
		return field
	}

	item := reflectWidgetFields(elem, depth+1)
	if len(item) == 0 {
		return field
	}

	for i := range item {
		item[i].Label = humanizeFieldName(item[i].Name)
		item[i].Advanced = itemAdvancedFields[item[i].Name]
	}
	slices.SortStableFunc(item, func(a, b widgetFieldSchema) int {
		return fieldOrderRank(a.Name) - fieldOrderRank(b.Name)
	})
	field.Kind, field.Item = "list", item

	return field
}

func parseYAMLTag(tag string) (name string, inline bool) {
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, p := range parts[1:] {
		if p == "inline" {
			inline = true
		}
	}
	return name, inline
}

var (
	iconFieldType     = reflect.TypeOf(customIconField{})
	durationFieldType = reflect.TypeOf(durationField(0))
	intervalFieldType = reflect.TypeOf(updateIntervalField(0))
	colorFieldType    = reflect.TypeOf(hslColorField{})
)

func fieldKind(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t {
	case iconFieldType:
		return "icon"
	case durationFieldType, intervalFieldType:
		return "duration"
	case colorFieldType:
		return "text"
	}

	switch t.Kind() {
	case reflect.Bool:
		return "checkbox"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "number"
	case reflect.String:
		return "text"
	default:
		return "yaml"
	}
}

var fieldLabelAcronyms = map[string]string{"url": "URL", "urls": "URLs", "css": "CSS", "api": "API", "dns": "DNS", "id": "ID", "rss": "RSS", "html": "HTML"}

func humanizeFieldName(name string) string {
	words := strings.Split(name, "-")
	for i, w := range words {
		if a, ok := fieldLabelAcronyms[w]; ok {
			words[i] = a
		} else if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

var preferredFieldOrder = []string{"title", "name", "url", "repository", "symbol", "timezone", "shortcut", "label", "icon", "description"}

func fieldOrderRank(name string) int {
	if i := slices.Index(preferredFieldOrder, name); i >= 0 {
		return i
	}
	return len(preferredFieldOrder)
}
