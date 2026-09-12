package dynacat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const (
	editorMaxBodyBytes      = 1 << 20
	editorNotAllowedMessage = "you are not allowed to use the web UI editor"
)

func (a *application) handleEditorSchema(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}
	writeJSON(w, http.StatusOK, allWidgetSchemas())
}

func (a *application) handleEditorStatus(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"generation": a.CreatedAt.UnixNano()})
}

func (a *application) handleEditorConfigLoad(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}
	user := a.getAuthenticatedUser(w, r)
	if !a.userCanEditAnything(user) {
		writeEditorForbidden(w)
		return
	}

	view, err := a.buildEditorConfigView(user)
	if err != nil {
		slog.Error("Editor config load failed", "error", err)
		writeEditorError(w, http.StatusInternalServerError, "editor_config_unreadable",
			"the config could not be read into the editor",
			"The config file is missing a pages list or failed to parse. The server log holds the parser error.")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *application) handleEditorConfigSave(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}
	user := a.getAuthenticatedUser(w, r)
	if !a.userCanEditAnything(user) {
		writeEditorForbidden(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, editorMaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeEditorError(w, http.StatusBadRequest, "editor_body_unreadable", "invalid body",
			"The request body could not be read, most likely because it exceeded the 1 MiB editor limit.")
		return
	}

	var mutation editorMutation
	if err := json.Unmarshal(body, &mutation); err != nil {
		writeEditorError(w, http.StatusBadRequest, "editor_body_not_json", "invalid JSON",
			"The editor sent a body the server could not decode into a mutation.")
		return
	}

	if err := a.applyEditorMutation(user, mutation); err != nil {
		out := editorErrorBody{Error: err.Error(), Context: mutationContext(mutation)}
		status := http.StatusBadRequest

		switch e := err.(type) {
		case *editorPermissionError:
			status, out.Code = http.StatusForbidden, "editor_write_blocked"
			out.Hint = "The config directory is read only or owned by another user. Check the volume mount and file ownership."
		case *editorDisabledError:
			status, out.Code = http.StatusForbidden, "editor_page_locked"
			out.Hint = "Editing is off for this page. Check server.allow-editing and the user's restrict-editing list."
		case *editorValidationError:
			status, out.Code, out.Field = http.StatusUnprocessableEntity, "editor_config_invalid", e.field
			out.Hint = "The change was rolled back because the resulting config did not validate. The message is the config validation error."
		default:
			out.Code = "editor_mutation_failed"
			out.Hint = "The mutation could not be applied to the config document. The server log holds the full error."
			slog.Error("Editor mutation failed", "op", mutation.Op, "error", err)
		}

		writeJSON(w, status, out)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Reports which variables a dynawidget template needs and which of them the container is missing.
func (a *application) handleEditorDynawidgetVariables(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}
	if !a.userCanEditAnything(a.getAuthenticatedUser(w, r)) {
		writeEditorForbidden(w)
		return
	}

	slug := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("widget")))
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		repo = dynawidgetsDefaultRepo
	}
	if !dynawidgetsSlugPattern.MatchString(slug) || !dynawidgetsRepoPattern.MatchString(repo) {
		writeJSON(w, http.StatusBadRequest, editorErrorBody{
			Error:   "invalid widget or repo",
			Code:    "dynawidgets_bad_reference",
			Hint:    "A slug is lowercase letters, digits and dashes, a repo is owner/name.",
			Context: map[string]any{"widget": slug, "repo": repo},
		})
		return
	}

	variables, err := dynawidgetsRequiredVariables(slug, repo)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, editorErrorBody{
			Error:   err.Error(),
			Code:    "dynawidgets_upstream_failed",
			Hint:    "The template could not be fetched from the repo. Check the slug, network access and the GitHub rate limit.",
			Context: map[string]any{"widget": slug, "repo": repo},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"variables": variables})
}

type editorConvertRequest struct {
	To    string          `json:"to"`
	Value json.RawMessage `json:"value"`
	Text  string          `json:"text"`
}

// Converts a list field between its structured form and its YAML text so the
// editor can offer both views without a YAML library in the browser.
func (a *application) handleEditorConvert(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}
	if !a.userCanEditAnything(a.getAuthenticatedUser(w, r)) {
		writeEditorForbidden(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, editorMaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeEditorError(w, http.StatusBadRequest, "editor_body_unreadable", "invalid body",
			"The request body could not be read, most likely because it exceeded the 1 MiB editor limit.")
		return
	}

	var req editorConvertRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeEditorError(w, http.StatusBadRequest, "editor_body_not_json", "invalid JSON",
			"The editor sent a body the server could not decode into a conversion request.")
		return
	}

	switch req.To {
	case "yaml":
		var value any = []any{}
		if len(req.Value) > 0 {
			if err := json.Unmarshal(req.Value, &value); err != nil {
				writeEditorError(w, http.StatusBadRequest, "convert_value_not_json", "invalid value",
					"The value being converted to YAML was not valid JSON.")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": nodeToText(valueNode(value))})
	case "value":
		node, err := parseYAMLValue(req.Text)
		if err != nil {
			writeEditorError(w, http.StatusUnprocessableEntity, "convert_yaml_invalid", err.Error(),
				"The pasted text is not valid YAML. The message carries the line the parser stopped on.")
			return
		}
		var value any
		if err := node.Decode(&value); err != nil {
			writeEditorError(w, http.StatusUnprocessableEntity, "convert_yaml_undecodable", err.Error(),
				"The YAML parsed but could not be decoded into a plain value. Anchors and custom tags are not supported here.")
			return
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			writeEditorError(w, http.StatusUnprocessableEntity, "convert_yaml_unsupported", "unsupported YAML structure",
				"The YAML decoded into something JSON cannot represent, such as a map with non-string keys.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]json.RawMessage{"value": encoded})
	default:
		writeEditorError(w, http.StatusBadRequest, "convert_unknown_target", "unknown conversion",
			`The "to" field has to be either "yaml" or "value".`)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

type editorErrorBody struct {
	Error   string         `json:"error"`
	Code    string         `json:"code"`
	Field   string         `json:"field,omitempty"`
	Hint    string         `json:"hint,omitempty"`
	Context map[string]any `json:"context,omitempty"`
}

func writeEditorError(w http.ResponseWriter, status int, code, message, hint string) {
	writeJSON(w, status, editorErrorBody{Error: message, Code: code, Hint: hint})
}

// Which change was rejected, so the console can name the widget instead of only the config error.
func mutationContext(m editorMutation) map[string]any {
	ctx := map[string]any{"op": m.Op, "page": m.Page, "column": m.Column, "index": m.Index}
	if m.WidgetType != "" {
		ctx["widgetType"] = m.WidgetType
	}
	if len(m.Path) > 0 {
		ctx["path"] = m.Path
	}
	if m.PresetKey != "" {
		ctx["presetKey"] = m.PresetKey
	}
	return ctx
}

func writeEditorForbidden(w http.ResponseWriter) {
	writeEditorError(w, http.StatusForbidden, "editor_forbidden", editorNotAllowedMessage,
		"Your user is not listed in server.editing-users or server.editing-groups, or server.allow-editing is off.")
}

type editorPreviewRequest struct {
	URL           string                       `json:"url"`
	AllowInsecure bool                         `json:"allow-insecure"`
	Headers       map[string]string            `json:"headers"`
	Subrequests   map[string]*CustomAPIRequest `json:"subrequests"`
	Template      string                       `json:"template"`
}

type editorPreviewResponse struct {
	JSON            json.RawMessage            `json:"json,omitempty"`
	SubrequestsJSON map[string]json.RawMessage `json:"subrequestsJson,omitempty"`
	HTML            string                     `json:"html,omitempty"`
	Error           string                     `json:"error,omitempty"`
	// Which step failed, since all of them come back as a 200 with an error string.
	Stage      string `json:"stage,omitempty"`
	Subrequest string `json:"subrequest,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

var editorPreviewHints = map[string]string{
	"request":         "The primary request never produced a usable response. Check the url, the headers and whether the host needs allow-insecure.",
	"subrequest":      "A subrequest failed, so no data reached the template. The subrequest key is in this payload.",
	"template-parse":  "The template text is not valid Go template syntax. The message carries the offending action.",
	"template-render": "The template parsed but blew up while rendering, usually a field that is missing or of another type than expected.",
}

// Fetches a custom-api request and renders its template so the editor can show errors before the widget is saved.
func (a *application) handleEditorCustomAPIPreview(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}
	if !a.userCanEditAnything(a.getAuthenticatedUser(w, r)) {
		writeEditorForbidden(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, editorMaxBodyBytes)
	var req editorPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeEditorError(w, http.StatusBadRequest, "editor_body_not_json", "invalid JSON",
			"The widget builder sent a body the server could not decode into a preview request.")
		return
	}

	// Fetch and template problems are the point of the preview, so they travel in the body instead of a status code.
	fail := func(stage, subrequest string, err error) {
		writeJSON(w, http.StatusOK, editorPreviewResponse{
			Error:      err.Error(),
			Stage:      stage,
			Subrequest: subrequest,
			Hint:       editorPreviewHints[stage],
		})
	}

	primary := &CustomAPIRequest{URL: req.URL, AllowInsecure: req.AllowInsecure, Headers: req.Headers}
	primaryData, err := fetchEditorPreviewRequest(primary)
	if err != nil {
		fail("request", "", err)
		return
	}

	response := editorPreviewResponse{
		JSON:            json.RawMessage(primaryData.JSON.Raw),
		SubrequestsJSON: make(map[string]json.RawMessage, len(req.Subrequests)),
	}
	subData := make(map[string]*customAPIResponseData, len(req.Subrequests))

	for key, sub := range req.Subrequests {
		data, err := fetchEditorPreviewRequest(sub)
		if err != nil {
			fail("subrequest", key, fmt.Errorf("subrequest %q: %w", key, err))
			return
		}
		subData[key] = data
		response.SubrequestsJSON[key] = json.RawMessage(data.JSON.Raw)
	}

	if req.Template != "" {
		providers := &widgetProviders{
			assetResolver: a.StaticAssetPath,
			imageCache:    a.imageCache,
			baseURL:       a.Config.Server.BaseURL,
			app:           a,
		}

		tmpl, err := template.New("").Funcs(customAPITemplateFuncs(providers)).Parse(req.Template)
		if err != nil {
			fail("template-parse", "", err)
			return
		}

		html, _, err := renderCustomAPIData(primaryData, subData, customAPIOptions{}, tmpl)
		if err != nil {
			fail("template-render", "", err)
			return
		}
		response.HTML = string(html)
	}

	writeJSON(w, http.StatusOK, response)
}

// ${VAR} references are deliberately left unexpanded: the preview body is caller-controlled, so
// expanding them would hand any editor user the process environment.
func fetchEditorPreviewRequest(req *CustomAPIRequest) (*customAPIResponseData, error) {
	if req == nil {
		return nil, errors.New("missing request")
	}

	if err := req.initialize(); err != nil {
		return nil, err
	}

	return fetchCustomAPIResponse(context.Background(), req)
}
