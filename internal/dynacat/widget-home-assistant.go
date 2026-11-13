package dynacat

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var homeAssistantWidgetTemplate = mustParseTemplate("home-assistant.html", "widget-base.html")

var validHomeAssistantEntityPattern = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

var homeAssistantToggleableDomains = map[string]bool{
	"light":         true,
	"switch":        true,
	"fan":           true,
	"input_boolean": true,
}

var homeAssistantSelectDomains = map[string]bool{
	"input_select": true,
	"select":       true,
}

type homeAssistantWidget struct {
	widgetBase    `yaml:",inline"`
	Frameless     bool                        `yaml:"frameless"`
	URL           string                      `yaml:"url"`
	Token         string                      `yaml:"token"`
	AllowInsecure bool                        `yaml:"allow-insecure"`
	Entities      []homeAssistantEntityConfig `yaml:"entities"`
	Rows          []homeAssistantEntity       `yaml:"-"`
}

type homeAssistantEntityConfig struct {
	Entity  string          `yaml:"entity"`
	Title   string          `yaml:"title"`
	Icon    customIconField `yaml:"icon"`
	Options []string        `yaml:"options"`
}

type homeAssistantEntity struct {
	ID         string
	Name       string
	State      string
	On         bool
	Inactive   bool
	Toggleable bool
	Options    []string
	Selected   string
	Icon       customIconField
}

type homeAssistantStateJSON struct {
	EntityID   string `json:"entity_id"`
	State      string `json:"state"`
	Attributes struct {
		FriendlyName string   `json:"friendly_name"`
		Unit         string   `json:"unit_of_measurement"`
		Icon         string   `json:"icon"`
		DeviceClass  string   `json:"device_class"`
		Options      []string `json:"options"`
	} `json:"attributes"`
}

func (widget *homeAssistantWidget) initialize() error {
	widget.withTitle("Home Assistant").withCacheDuration(15 * time.Second)

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(30 * time.Second)
		widget.UpdateInterval = &interval
	}

	if widget.URL == "" {
		return errors.New("url is required")
	}

	if widget.Token == "" {
		return errors.New("token is required")
	}

	if len(widget.Entities) == 0 {
		return errors.New("at least one entity is required")
	}

	for i := range widget.Entities {
		if !validHomeAssistantEntityPattern.MatchString(widget.Entities[i].Entity) {
			return fmt.Errorf("invalid entity id %q", widget.Entities[i].Entity)
		}
	}

	widget.URL = strings.TrimRight(widget.URL, "/")

	return nil
}

func (widget *homeAssistantWidget) update(ctx context.Context) {
	states, err := homeAssistantRequest[[]homeAssistantStateJSON](ctx, widget, "GET", "/api/states", nil)
	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	byID := make(map[string]*homeAssistantStateJSON, len(states))
	for i := range states {
		byID[states[i].EntityID] = &states[i]
	}

	rows := make([]homeAssistantEntity, len(widget.Entities))
	for i := range widget.Entities {
		rows[i] = widget.buildRow(&widget.Entities[i], byID[widget.Entities[i].Entity])
	}

	widget.Rows = rows
}

func (widget *homeAssistantWidget) buildRow(cfg *homeAssistantEntityConfig, state *homeAssistantStateJSON) homeAssistantEntity {
	domain, _, _ := strings.Cut(cfg.Entity, ".")
	found := state != nil
	if !found {
		state = &homeAssistantStateJSON{}
	}

	row := homeAssistantEntity{ID: cfg.Entity, Name: cmp.Or(cfg.Title, state.Attributes.FriendlyName, cfg.Entity), Icon: cfg.Icon}
	if row.Icon.URL == "" {
		row.Icon = newCustomIconField(cmp.Or(state.Attributes.Icon, homeAssistantDefaultIcon(domain, state.Attributes.DeviceClass, state.State)))
	}
	row.Icon.prepare(widget.Providers)

	if !found {
		row.State, row.Inactive = "Not found", true
		return row
	}

	row.On = state.State == "on"
	row.Inactive = state.State == "off" || state.State == "unavailable" || state.State == "unknown"
	row.Toggleable = homeAssistantToggleableDomains[domain] && state.State != "unavailable"
	row.State = homeAssistantStateText(state.State, state.Attributes.Unit)

	if homeAssistantSelectDomains[domain] && state.State != "unavailable" {
		row.Options = homeAssistantSelectOptions(cfg.Options, state.Attributes.Options)
		row.Selected = state.State
	}

	return row
}

// Configured options keep their order but are limited to the ones the entity offers,
// so a renamed or removed option disappears instead of failing when clicked.
func homeAssistantSelectOptions(configured, available []string) []string {
	if len(configured) == 0 {
		return available
	}

	options := make([]string, 0, len(configured))
	for _, option := range configured {
		if slices.Contains(available, option) {
			options = append(options, option)
		}
	}

	return options
}

func homeAssistantStateText(state, unit string) string {
	if state == "" {
		return state
	}

	if unit != "" && state != "unavailable" && state != "unknown" {
		return state + " " + unit
	}

	state = strings.ReplaceAll(state, "_", " ")
	first, size := utf8.DecodeRuneInString(state)
	return string(unicode.ToUpper(first)) + state[size:]
}

func homeAssistantDefaultIcon(domain, deviceClass, state string) string {
	icon, ok := homeAssistantIcons[domain+"."+deviceClass]
	if !ok {
		icon, ok = homeAssistantIcons[domain]
	}

	if !ok {
		return "mdi:bookmark"
	}

	if stateIcon, ok := icon.states[state]; ok {
		return stateIcon
	}

	return icon.def
}

func (widget *homeAssistantWidget) Render() template.HTML {
	return widget.renderTemplate(widget, homeAssistantWidgetTemplate)
}

func (widget *homeAssistantWidget) handleRequest(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "toggle" && action != "select" {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body struct {
		Entity string `json:"entity"`
		Option string `json:"option"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	domain, _, _ := strings.Cut(body.Entity, ".")
	service := "toggle"
	payload := map[string]string{"entity_id": body.Entity}

	var index int
	if action == "toggle" {
		index = slices.IndexFunc(widget.Rows, func(row homeAssistantEntity) bool { return row.ID == body.Entity && row.Toggleable })
	} else {
		index = slices.IndexFunc(widget.Rows, func(row homeAssistantEntity) bool {
			return row.ID == body.Entity && slices.Contains(row.Options, body.Option)
		})
		service = "select_option"
		payload["option"] = body.Option
	}

	if index == -1 {
		http.Error(w, "entity cannot be changed", http.StatusForbidden)
		return
	}

	encodedPayload, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	changed, err := homeAssistantRequest[[]homeAssistantStateJSON](ctx, widget, "POST", "/api/services/"+domain+"/"+service, encodedPayload)
	if err != nil {
		slog.Error("Home Assistant action failed", "entity", body.Entity, "action", action, "error", err)
		http.Error(w, action+" failed", http.StatusBadGateway)
		return
	}

	var state *homeAssistantStateJSON
	for i := range changed {
		if changed[i].EntityID == body.Entity {
			state = &changed[i]
			break
		}
	}

	// Services return only the states that changed, so an unchanged entity has to be read back.
	if state == nil {
		fresh, err := homeAssistantRequest[homeAssistantStateJSON](ctx, widget, "GET", "/api/states/"+body.Entity, nil)
		if err != nil {
			http.Error(w, "reading state failed", http.StatusBadGateway)
			return
		}
		state = &fresh
	}

	widget.Rows[index] = widget.buildRow(&widget.Entities[index], state)
	row := &widget.Rows[index]

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"state":      row.State,
		"on":         row.On,
		"inactive":   row.Inactive,
		"toggleable": row.Toggleable,
		"selected":   row.Selected,
		"icon":       row.Icon.URL,
	})
}

// Bypasses the shared fetch cache so a refresh right after a toggle never shows the old state.
func homeAssistantRequest[T any](ctx context.Context, widget *homeAssistantWidget, method, path string, body []byte) (T, error) {
	var result T

	request, err := http.NewRequestWithContext(ctx, method, widget.URL+path, bytes.NewReader(body))
	if err != nil {
		return result, err
	}

	request.Header.Set("Authorization", "Bearer "+widget.Token)
	request.Header.Set("Content-Type", "application/json")

	client := ternary(widget.AllowInsecure, defaultInsecureHTTPClient, defaultHTTPClient)
	status, _, responseBody, err := doRequestReadAll(client, request)
	if err != nil {
		return result, err
	}

	if status == http.StatusUnauthorized {
		return result, errors.New("home assistant rejected the token (401)")
	}

	if status != http.StatusOK {
		truncated, _ := limitStringLength(string(responseBody), 256)
		return result, fmt.Errorf("unexpected status code %d from %s, response: %s", status, request.URL, truncated)
	}

	return result, json.Unmarshal(responseBody, &result)
}
