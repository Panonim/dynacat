package dynacat

import (
	"context"
	"crypto/subtle"
	"encoding"
	"encoding/json"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const apiTokenHeader = "X-API-Token"

const apiDefaultRateLimit = 5
const apiRateLimitWindow = time.Minute

// Guards against runaway recursion and self-referencing structures.
const apiMaxWalkDepth = 12

// Field names that never get serialized. The primary protection is that only fields tagged
// yaml:"-" are exposed (see apiStrictData), so user config never leaves the process. This is
// the second line of defence for a widget that stores a credential in a data field.
var apiSecretNameParts = []string{"token", "password", "passwd", "secret", "credential", "bearer", "apikey"}
var apiSecretNames = []string{"key", "auth", "cookie", "authorization"}

type apiWidgetView struct {
	APIID   string          `json:"api-id,omitempty"`
	Type    string          `json:"type"`
	Title   string          `json:"title,omitempty"`
	Error   string          `json:"error,omitempty"`
	Data    map[string]any  `json:"data,omitempty"`
	Widgets []apiWidgetView `json:"widgets,omitempty"`
}

type apiPageView struct {
	Slug    string          `json:"slug"`
	Name    string          `json:"name"`
	Widgets []apiWidgetView `json:"widgets"`
}

type apiRateWindow struct {
	requests int
	first    time.Time
}

func (a *application) apiRateLimit() int {
	if a.Config.API.RateLimit == nil {
		return apiDefaultRateLimit
	}

	return *a.Config.API.RateLimit
}

// Fixed window per IP. Reports whether the caller is over its allowance and how long
// until the current window ends.
func (a *application) apiExceededRateLimit(ip string) (bool, int) {
	limit := a.apiRateLimit()
	if limit <= 0 {
		return false, 0
	}

	a.apiRateMu.Lock()
	defer a.apiRateMu.Unlock()

	now := time.Now()
	window, exists := a.apiRateRequests[ip]

	if !exists || now.Sub(window.first) >= apiRateLimitWindow {
		a.apiRateRequests[ip] = &apiRateWindow{requests: 1, first: now}

		for other := range a.apiRateRequests {
			if now.Sub(a.apiRateRequests[other].first) >= apiRateLimitWindow {
				delete(a.apiRateRequests, other)
			}
		}

		return false, 0
	}

	window.requests++
	if window.requests > limit {
		return true, max(1, int((apiRateLimitWindow - now.Sub(window.first)).Seconds()))
	}

	return false, 0
}

// Runs before authentication so that guessing credentials is throttled too.
func (a *application) handleAPIRateLimit(w http.ResponseWriter, r *http.Request) bool {
	exceeded, retryAfter := a.apiExceededRateLimit(a.addressOfRequest(r))
	if !exceeded {
		return false
	}

	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")

	return true
}

func (a *application) apiEnforcesPagePermissions() bool {
	return a.Config.API.EnforcePagePermissions == nil || *a.Config.API.EnforcePagePermissions
}

// Survives config reloads, which rebuild the application, so a warning is only repeated
// when the setting behind it goes away and comes back.
var apiWarnedAbout struct {
	mu                  sync.Mutex
	missingToken        bool
	permissionsDisabled bool
}

// Prints straight to stderr instead of slog because LOG_LEVEL can be set to ERROR,
// which would hide these.
func (a *application) warnAboutAPIExposure() {
	enabled := a.Config.API.Enabled

	apiWarnedAbout.mu.Lock()
	defer apiWarnedAbout.mu.Unlock()

	warnOnce(
		enabled && a.Config.API.Token == "",
		&apiWarnedAbout.missingToken,
		"The API is enabled without a token set. Anyone who can reach this server can read your widget data.",
	)

	warnOnce(
		enabled && !a.apiEnforcesPagePermissions(),
		&apiWarnedAbout.permissionsDisabled,
		"The API has enforce-page-permissions set to false. Pages restricted with allowed-users or allowed-groups are readable without logging in.",
	)
}

func warnOnce(condition bool, alreadyWarned *bool, message string) {
	if condition && !*alreadyWarned {
		printUnsuppressableWarning(message)
	}

	*alreadyWarned = condition
}

func (a *application) handleAPIPreflight(w http.ResponseWriter, r *http.Request) {
	a.apiCORS(w, r)
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", apiTokenHeader+", Authorization")
	w.WriteHeader(http.StatusNoContent)
}

func (a *application) apiCORS(w http.ResponseWriter, r *http.Request) {
	origins := a.Config.API.AllowedOrigins
	if len(origins) == 0 {
		return
	}

	if slices.Contains(origins, "*") {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		return
	}

	if origin := r.Header.Get("Origin"); origin != "" && slices.Contains(origins, origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
}

// Resolves the caller: dashboard session, then HTTP basic against the configured users,
// then the shared API token. Returns handled=true when it has already written a response.
func (a *application) apiAuthenticate(w http.ResponseWriter, r *http.Request) (*authenticatedUser, bool) {
	if user := a.getAuthenticatedUser(w, r); user != nil {
		return user, false
	}

	if username, password, ok := r.BasicAuth(); ok && a.PasswordEnabled {
		ip := a.addressOfRequest(r)

		if limited, retryAfter := a.checkAuthRateLimit(ip); limited {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeJSONError(w, http.StatusTooManyRequests, "too many attempts")
			return nil, true
		}

		if user := a.verifyUserPassword(username, password); user != nil {
			a.clearAuthRateLimit(ip)
			return user, false
		}

		slog.Warn("Failed API login attempt", "username", strconv.Quote(username), "ip", ip)
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return nil, true
	}

	token := a.Config.API.Token
	if token == "" {
		return nil, false
	}

	provided := r.Header.Get(apiTokenHeader)
	if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1 {
		return nil, false
	}

	writeJSONError(w, http.StatusUnauthorized, "invalid or missing "+apiTokenHeader)
	return nil, true
}

// Whether the page is within the allow-list. A page outside it is treated as non-existent.
func (a *application) apiPageExposed(p *page) bool {
	allowed := a.Config.API.AllowedPages
	return len(allowed) == 0 || slices.Contains(allowed, p.Slug)
}

func (a *application) apiCanReadPage(user *authenticatedUser, p *page) bool {
	if len(p.AllowedUsers) == 0 && len(p.AllowedGroups) == 0 {
		return true
	}

	if !a.apiEnforcesPagePermissions() {
		return true
	}

	return user != nil && a.isUserAllowedOnPage(user, p)
}

func (a *application) handleAPIPages(w http.ResponseWriter, r *http.Request) {
	a.apiCORS(w, r)

	if a.handleAPIRateLimit(w, r) {
		return
	}

	user, handled := a.apiAuthenticate(w, r)
	if handled {
		return
	}

	views := make([]apiPageView, 0, len(a.Config.Pages))

	for i := range a.Config.Pages {
		p := &a.Config.Pages[i]

		if !a.apiPageExposed(p) || !a.apiCanReadPage(user, p) {
			continue
		}

		// Widget titles and errors are written by background updates that hold this lock.
		p.mu.Lock()
		views = append(views, apiPageView{Slug: p.Slug, Name: p.Title, Widgets: apiPageWidgetViews(p, false)})
		p.mu.Unlock()
	}

	writeJSON(w, http.StatusOK, views)
}

func (a *application) handleAPIPage(w http.ResponseWriter, r *http.Request) {
	a.apiCORS(w, r)

	if a.handleAPIRateLimit(w, r) {
		return
	}

	user, handled := a.apiAuthenticate(w, r)
	if handled {
		return
	}

	p, exists := a.slugToPage[r.PathValue("page")]
	if !exists || !a.apiPageExposed(p) {
		writeJSONError(w, http.StatusNotFound, "page not found")
		return
	}

	if !a.apiCanReadPage(user, p) {
		a.writeAPIForbidden(w, user)
		return
	}

	var view apiPageView

	func() {
		p.mu.Lock()
		defer p.mu.Unlock()

		p.updateOutdatedWidgets()
		view = apiPageView{Slug: p.Slug, Name: p.Title, Widgets: apiPageWidgetViews(p, true)}
	}()

	writeJSON(w, http.StatusOK, view)
}

func (a *application) handleAPIWidget(w http.ResponseWriter, r *http.Request) {
	a.apiCORS(w, r)

	if a.handleAPIRateLimit(w, r) {
		return
	}

	user, handled := a.apiAuthenticate(w, r)
	if handled {
		return
	}

	widget, exists := a.widgetByAPIID[r.PathValue("apiID")]
	if !exists {
		writeJSONError(w, http.StatusNotFound, "widget not found")
		return
	}

	p, exists := a.widgetToPage[widget.GetID()]
	if !exists || !a.apiPageExposed(p) {
		writeJSONError(w, http.StatusNotFound, "widget not found")
		return
	}

	if !a.apiCanReadPage(user, p) {
		a.writeAPIForbidden(w, user)
		return
	}

	var view apiWidgetView

	func() {
		p.mu.Lock()
		defer p.mu.Unlock()

		// Deliberately not a forced update: refreshing only when the cache is stale keeps the
		// widget inside the shared fetch window, so widgets pointing at the same upstream
		// still share a single request.
		now := time.Now()
		if widget.requiresUpdate(&now) || widget.IsLazyLoad() {
			widget.update(withSharedFetchMaxAge(context.Background(), widget.getCacheDuration()))
		}

		view = newAPIWidgetView(widget, true)
	}()

	writeJSON(w, http.StatusOK, view)
}

func (a *application) writeAPIForbidden(w http.ResponseWriter, user *authenticatedUser) {
	if user == nil {
		writeJSONError(w, http.StatusUnauthorized, "this page requires an authenticated user")
		return
	}

	writeJSONError(w, http.StatusForbidden, "user is not allowed on this page")
}

func apiPageWidgetViews(p *page, includeData bool) []apiWidgetView {
	views := make([]apiWidgetView, 0, len(p.HeadWidgets))

	for _, w := range p.HeadWidgets {
		views = append(views, newAPIWidgetView(w, includeData))
	}

	for c := range p.Columns {
		for _, w := range p.Columns[c].Widgets {
			views = append(views, newAPIWidgetView(w, includeData))
		}
	}

	return views
}

func newAPIWidgetView(w widget, includeData bool) apiWidgetView {
	view := apiWidgetView{APIID: w.GetAPIID(), Type: w.GetType(), Title: w.GetTitle()}

	if err := w.GetError(); err != nil {
		view.Error = err.Error()
	}

	// Container widgets keep their children in an unexported embedded struct, so they are
	// listed explicitly rather than picked up by the reflection walk.
	switch v := w.(type) {
	case *groupWidget:
		view.Widgets = apiChildWidgetViews(v.Widgets, includeData)
		return view
	case *splitColumnWidget:
		view.Widgets = apiChildWidgetViews(v.Widgets, includeData)
		return view
	}

	if includeData {
		view.Data = apiStrictData(reflect.ValueOf(w), 0)
	}

	return view
}

func apiChildWidgetViews(ws widgets, includeData bool) []apiWidgetView {
	views := make([]apiWidgetView, 0, len(ws))

	for _, w := range ws {
		views = append(views, newAPIWidgetView(w, includeData))
	}

	return views
}

// Walks a widget struct, emitting only fields that carry no yaml key of their own, since that
// is where widgets store what they fetched. Fields with a real yaml key are user config and are
// only descended into, never emitted, so tokens and passwords cannot escape.
func apiStrictData(v reflect.Value, depth int) map[string]any {
	v = apiDeref(v)
	if depth > apiMaxWalkDepth || v.Kind() != reflect.Struct {
		return nil
	}

	out := make(map[string]any)
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() || apiIsSecretName(field.Name) || apiIsInfrastructureType(field.Type) {
			continue
		}

		value := v.Field(i)
		tag := field.Tag.Get("yaml")

		if field.Anonymous && (tag == "" || strings.Contains(tag, ",inline")) {
			for k, nested := range apiStrictData(value, depth+1) {
				out[k] = nested
			}
			continue
		}

		if name := strings.Split(tag, ",")[0]; name == "-" || name == "" {
			if converted := apiOpenValue(value, depth+1); converted != nil {
				out[field.Name] = converted
			}
			continue
		}

		if nested := apiStrictValue(value, depth+1); nested != nil {
			out[field.Name] = nested
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// Descends through config-tagged fields looking for nested data, e.g. the per-server
// results the server-stats widget stores inside its configured `servers` list.
func apiStrictValue(v reflect.Value, depth int) any {
	v = apiDeref(v)
	if depth > apiMaxWalkDepth || !v.IsValid() {
		return nil
	}

	switch v.Kind() {
	case reflect.Struct:
		if data := apiStrictData(v, depth); data != nil {
			return data
		}
	case reflect.Slice, reflect.Array:
		items := make([]any, 0, v.Len())
		found := false

		for i := 0; i < v.Len(); i++ {
			item := apiStrictValue(v.Index(i), depth+1)
			items = append(items, item)
			found = found || item != nil
		}

		if found {
			return items
		}
	}

	return nil
}

// Converts a value that already sits inside a data field, so upstream payloads survive intact.
func apiOpenValue(v reflect.Value, depth int) any {
	v = apiDeref(v)
	if depth > apiMaxWalkDepth || !v.IsValid() || !v.CanInterface() {
		return nil
	}

	if apiIsInfrastructureType(v.Type()) {
		return nil
	}

	// Types with their own JSON or text representation (json.RawMessage, time.Time, ...)
	// must not be taken apart field by field.
	if apiHasOwnEncoding(v) {
		return v.Interface()
	}

	switch v.Kind() {
	case reflect.String:
		return redactSecretQueryParams(v.String())
	case reflect.Struct:
		// Nested structs go back through the same config-versus-data split, so a widget
		// cannot expose config by reaching it through one of its data fields.
		if data := apiStrictData(v, depth); data != nil {
			return data
		}

		return nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return v.Interface()
		}

		items := make([]any, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			items = append(items, apiOpenValue(v.Index(i), depth+1))
		}

		return items
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil
		}

		out := make(map[string]any, v.Len())
		for _, key := range v.MapKeys() {
			if apiIsSecretName(key.String()) {
				continue
			}

			out[key.String()] = apiOpenValue(v.MapIndex(key), depth+1)
		}

		return out
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return nil
	}

	return v.Interface()
}

func apiDeref(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}

	return v
}

func apiHasOwnEncoding(v reflect.Value) bool {
	if !v.CanInterface() {
		return false
	}

	switch v.Interface().(type) {
	case json.Marshaler, encoding.TextMarshaler:
		return true
	}

	if v.CanAddr() {
		switch v.Addr().Interface().(type) {
		case json.Marshaler, encoding.TextMarshaler:
			return true
		}
	}

	return false
}

// Widget internals and HTTP plumbing are never worth serializing and can be cyclic.
func apiIsInfrastructureType(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t == reflect.TypeOf(widgetProviders{}) || t.PkgPath() == "net/http"
}

func apiIsSecretName(name string) bool {
	lowered := strings.ToLower(name)

	if slices.Contains(apiSecretNames, lowered) {
		return true
	}

	for _, part := range apiSecretNameParts {
		if strings.Contains(lowered, part) {
			return true
		}
	}

	return false
}
