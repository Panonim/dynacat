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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var homeAssistantWidgetTemplate = mustParseTemplate("home-assistant.html", "widget-base.html")

var validHomeAssistantEntityPattern = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

var homeAssistantNumberPattern = regexp.MustCompile(`^-?[0-9]+(\.([0-9]+))?$`)

// Sensors without a display precision report raw floats such as 25.3888888888889.
const homeAssistantDefaultMaxDecimals = 2

var homeAssistantToggleableDomains = map[string]bool{
	"light":         true,
	"switch":        true,
	"fan":           true,
	"input_boolean": true,
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
	Entity    string          `yaml:"entity"`
	Title     string          `yaml:"title"`
	Icon      customIconField `yaml:"icon"`
	Precision *int            `yaml:"precision"`
}

type homeAssistantEntity struct {
	ID         string
	Name       string
	State      string
	On         bool
	Inactive   bool
	Toggleable bool
	Icon       customIconField
}

type homeAssistantStateJSON struct {
	EntityID   string `json:"entity_id"`
	State      string `json:"state"`
	Attributes struct {
		FriendlyName string `json:"friendly_name"`
		Unit         string `json:"unit_of_measurement"`
		Icon         string `json:"icon"`
		DeviceClass  string `json:"device_class"`
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

		if precision := widget.Entities[i].Precision; precision != nil && (*precision < 0 || *precision > 10) {
			return fmt.Errorf("precision of %q must be between 0 and 10", widget.Entities[i].Entity)
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
	row.State = homeAssistantStateText(state.State, state.Attributes.Unit, cfg.Precision)

	return row
}

func homeAssistantStateText(state, unit string, precision *int) string {
	if state == "" {
		return state
	}

	state = homeAssistantFormatNumber(state, precision)

	if unit != "" && state != "unavailable" && state != "unknown" {
		return state + " " + unit
	}

	state = strings.ReplaceAll(state, "_", " ")
	first, size := utf8.DecodeRuneInString(state)
	return string(unicode.ToUpper(first)) + state[size:]
}

// Numbers are rounded to precision when it is set, otherwise only those with more than
// homeAssistantDefaultMaxDecimals decimals are rounded, so values like 86.0 stay as reported.
func homeAssistantFormatNumber(state string, precision *int) string {
	match := homeAssistantNumberPattern.FindStringSubmatch(state)
	if match == nil {
		return state
	}

	if precision == nil && len(match[2]) <= homeAssistantDefaultMaxDecimals {
		return state
	}

	value, err := strconv.ParseFloat(state, 64)
	if err != nil {
		return state
	}

	if precision != nil {
		return strconv.FormatFloat(value, 'f', *precision, 64)
	}

	rounded := strconv.FormatFloat(value, 'f', homeAssistantDefaultMaxDecimals, 64)
	return strings.TrimSuffix(strings.TrimRight(rounded, "0"), ".")
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
	if r.PathValue("action") != "toggle" {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body struct {
		Entity string `json:"entity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	index := slices.IndexFunc(widget.Rows, func(row homeAssistantEntity) bool { return row.ID == body.Entity && row.Toggleable })
	if index == -1 {
		http.Error(w, "entity cannot be toggled", http.StatusForbidden)
		return
	}

	domain, _, _ := strings.Cut(body.Entity, ".")
	payload, _ := json.Marshal(map[string]string{"entity_id": body.Entity})

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	changed, err := homeAssistantRequest[[]homeAssistantStateJSON](ctx, widget, "POST", "/api/services/"+domain+"/toggle", payload)
	if err != nil {
		slog.Error("Home Assistant toggle failed", "entity", body.Entity, "error", err)
		http.Error(w, "toggle failed", http.StatusBadGateway)
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
