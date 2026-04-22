package dynacat

import (
	"fmt"
	"html/template"
	"net/http"
	"time"
)

var (
	themeStyleTemplate         = mustParseTemplate("theme-style.gotmpl")
	themePresetPreviewTemplate = mustParseTemplate("theme-preset-preview.html")
)

const (
	themeCookieName       = "theme"
	themeModeCookieName   = "theme-mode"
	themeManualCookieName = "theme-manual"
	themeLightCookieName  = "theme-light"
	themeDarkCookieName   = "theme-dark"

	themeModeManual = "manual"
	themeModeSystem = "system"
)

const themeCookieDuration = 2 * 365 * 24 * time.Hour

type themeSelectionState struct {
	Mode        string
	ActiveKey   string
	ManualKey   string
	LightKey    string
	DarkKey     string
	ActiveTheme *themeProperties
}

func (a *application) handleThemeChangeRequest(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}

	themeKey := r.PathValue("key")
	properties, exists := a.getThemeByKey(themeKey)
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     themeCookieName,
		Value:    themeKey,
		Path:     a.Config.Server.BaseURL + "/",
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(themeCookieDuration),
	})

	w.Header().Set("Content-Type", "text/css")
	w.Header().Set("X-Scheme", ternary(properties.Light, "light", "dark"))
	w.Write([]byte(properties.CSS))
}

type themeProperties struct {
	BackgroundColor          *hslColorField `yaml:"background-color"`
	PrimaryColor             *hslColorField `yaml:"primary-color"`
	PositiveColor            *hslColorField `yaml:"positive-color"`
	NegativeColor            *hslColorField `yaml:"negative-color"`
	Light                    bool           `yaml:"light"`
	ContrastMultiplier       float32        `yaml:"contrast-multiplier"`
	TextSaturationMultiplier float32        `yaml:"text-saturation-multiplier"`

	Key                  string        `yaml:"-"`
	CSS                  template.CSS  `yaml:"-"`
	PreviewHTML          template.HTML `yaml:"-"`
	BackgroundColorAsHex string        `yaml:"-"`
}

func (a *application) getThemeByKey(themeKey string) (*themeProperties, bool) {
	if themeKey == "" {
		return nil, false
	}

	if themeKey == a.Config.Theme.Key {
		return &a.Config.Theme.themeProperties, true
	}

	return a.Config.Theme.Presets.Get(themeKey)
}

func (a *application) getFallbackThemeKey(light bool) string {
	if a.Config.Theme.Light == light {
		return a.Config.Theme.Key
	}

	for key, properties := range a.Config.Theme.Presets.Items() {
		if properties.Light == light {
			return key
		}
	}

	return a.Config.Theme.Key
}

func (a *application) getThemeCookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}

	return cookie.Value
}

func (a *application) getThemeSelectionState(r *http.Request) themeSelectionState {
	state := themeSelectionState{
		Mode:        themeModeManual,
		ActiveKey:   a.Config.Theme.Key,
		ManualKey:   a.Config.Theme.Key,
		LightKey:    a.getFallbackThemeKey(true),
		DarkKey:     a.getFallbackThemeKey(false),
		ActiveTheme: &a.Config.Theme.themeProperties,
	}

	activeThemeWasLoaded := false

	if themeKey := a.getThemeCookieValue(r, themeCookieName); themeKey != "" {
		if properties, ok := a.getThemeByKey(themeKey); ok {
			state.ActiveKey = themeKey
			state.ManualKey = themeKey
			state.ActiveTheme = properties
			activeThemeWasLoaded = true

			if properties.Light {
				state.LightKey = themeKey
			} else {
				state.DarkKey = themeKey
			}
		}
	}

	manualTheme := state.ActiveTheme
	if themeKey := a.getThemeCookieValue(r, themeManualCookieName); themeKey != "" {
		if properties, ok := a.getThemeByKey(themeKey); ok {
			state.ManualKey = themeKey
			manualTheme = properties
		}
	}

	if manualTheme.Light {
		state.LightKey = state.ManualKey
	} else {
		state.DarkKey = state.ManualKey
	}

	if themeKey := a.getThemeCookieValue(r, themeLightCookieName); themeKey != "" {
		if properties, ok := a.getThemeByKey(themeKey); ok && properties.Light {
			state.LightKey = themeKey
		}
	}

	if themeKey := a.getThemeCookieValue(r, themeDarkCookieName); themeKey != "" {
		if properties, ok := a.getThemeByKey(themeKey); ok && !properties.Light {
			state.DarkKey = themeKey
		}
	}

	if a.getThemeCookieValue(r, themeModeCookieName) == themeModeSystem {
		state.Mode = themeModeSystem
	}

	if state.Mode == themeModeManual {
		state.ActiveKey = state.ManualKey
		state.ActiveTheme, _ = a.getThemeByKey(state.ManualKey)
		if state.ActiveTheme == nil {
			state.ActiveKey = a.Config.Theme.Key
			state.ActiveTheme = &a.Config.Theme.themeProperties
		}
		return state
	}

	if !activeThemeWasLoaded {
		state.ActiveKey = state.DarkKey
		state.ActiveTheme, _ = a.getThemeByKey(state.DarkKey)
	}

	if state.ActiveTheme == nil {
		state.ActiveKey = a.Config.Theme.Key
		state.ActiveTheme = &a.Config.Theme.themeProperties
	}

	return state
}

func (t *themeProperties) init() error {
	css, err := executeTemplateToString(themeStyleTemplate, t)
	if err != nil {
		return fmt.Errorf("compiling theme style: %v", err)
	}
	t.CSS = template.CSS(whitespaceAtBeginningOfLinePattern.ReplaceAllString(css, ""))

	if t.BackgroundColor != nil {
		t.BackgroundColorAsHex = t.BackgroundColor.ToHex()
	} else {
		t.BackgroundColorAsHex = "#151519"
	}

	previewHTML, err := executeTemplateToString(themePresetPreviewTemplate, t)
	if err != nil {
		return fmt.Errorf("compiling theme preview: %v", err)
	}
	t.PreviewHTML = template.HTML(previewHTML)

	return nil
}

func (t1 *themeProperties) SameAs(t2 *themeProperties) bool {
	if t1 == nil && t2 == nil {
		return true
	}
	if t1 == nil || t2 == nil {
		return false
	}
	if t1.Light != t2.Light {
		return false
	}
	if t1.ContrastMultiplier != t2.ContrastMultiplier {
		return false
	}
	if t1.TextSaturationMultiplier != t2.TextSaturationMultiplier {
		return false
	}
	if !t1.BackgroundColor.SameAs(t2.BackgroundColor) {
		return false
	}
	if !t1.PrimaryColor.SameAs(t2.PrimaryColor) {
		return false
	}
	if !t1.PositiveColor.SameAs(t2.PositiveColor) {
		return false
	}
	if !t1.NegativeColor.SameAs(t2.NegativeColor) {
		return false
	}
	return true
}
