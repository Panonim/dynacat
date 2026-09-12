package dynacat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const dynawidgetsDefaultRepo = "main"
const dynawidgetsAssetsDir = "/app/assets/dynawidgets"

var dynawidgetsSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var dynawidgetsRepoPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var dynawidgetsTemplateHost = "raw.githubusercontent.com"

type dynawidgetsWidget struct {
	widgetBase        `yaml:",inline"`
	Widget            string `yaml:"widget"`
	Repo              string `yaml:"repo"`
	*CustomAPIRequest `yaml:",inline"`
	Subrequests       map[string]*CustomAPIRequest `yaml:"subrequests"`
	Options           customAPIOptions             `yaml:"options"`
	Frameless         bool                         `yaml:"frameless"`
	slug              string                       `yaml:"-"`
	repo              string                       `yaml:"-"`
	templateContent   string                       `yaml:"-"`
	compiledTemplate  *template.Template           `yaml:"-"`
	templateModTime   time.Time                    `yaml:"-"`
	templateCheckedAt time.Time                    `yaml:"-"`
	templateUpdatedAt time.Time                    `yaml:"-"`
	CompiledHTML      template.HTML                `yaml:"-"`
	APIResponse       json.RawMessage              `yaml:"-"`
}

type dynawidgetsListEntry struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Slug        string `json:"slug"`
	Template    string `json:"template"`
}

type dynawidgetsRequired struct {
	URL         string                       `yaml:"url"`
	Subrequests map[string]*CustomAPIRequest `yaml:"subrequests"`
}

func (widget *dynawidgetsWidget) initialize() error {
	widget.withTitle("Dynawidgets").withCacheDuration(1 * time.Minute)
	widget.widgetBase.WIP = true

	if widget.Widget == "" {
		return errors.New("widget (slug) is required")
	}

	slug := strings.ToLower(widget.Widget)
	if !dynawidgetsSlugPattern.MatchString(slug) {
		return fmt.Errorf("widget slug %q is invalid; must match %s", slug, dynawidgetsSlugPattern)
	}
	repo := widget.Repo
	if repo == "" {
		repo = dynawidgetsDefaultRepo
	}
	if !dynawidgetsRepoPattern.MatchString(repo) {
		return fmt.Errorf("repo %q is invalid", repo)
	}
	templateContent, title, required, err := dynawidgetsResolveTemplate(slug, repo)
	if err != nil {
		return fmt.Errorf("resolving dynawidget template: %w", err)
	}
	widget.slug = slug
	widget.repo = repo
	widget.templateContent = templateContent
	widget.templateModTime = dynawidgetsTemplateModTime(slug, repo)

	if widget.Title == "" && title != "" {
		widget.Title = title
	}

	if required != nil {
		if required.URL != "" {
			if widget.CustomAPIRequest == nil {
				widget.CustomAPIRequest = &CustomAPIRequest{}
			}
			if widget.CustomAPIRequest.URL == "" {
				widget.CustomAPIRequest.URL = required.URL
			}
		}

		for key, req := range required.Subrequests {
			if req == nil {
				continue
			}
			if widget.Subrequests == nil {
				widget.Subrequests = make(map[string]*CustomAPIRequest)
			}
			existing, ok := widget.Subrequests[key]
			if !ok || existing == nil {
				widget.Subrequests[key] = req
			} else if existing.URL == "" {
				existing.URL = req.URL
			}
		}
	}

	compiledTemplate, err := template.New("").Funcs(customAPITemplateFuncs(nil)).Parse(templateContent)
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}
	widget.compiledTemplate = compiledTemplate

	if widget.CustomAPIRequest != nil {
		if err := widget.CustomAPIRequest.initialize(); err != nil {
			return fmt.Errorf("initializing primary request: %v", err)
		}
	}

	for key := range widget.Subrequests {
		if err := widget.Subrequests[key].initialize(); err != nil {
			return fmt.Errorf("initializing subrequest %q: %v", key, err)
		}
	}

	if widget.UpdateInterval == nil {
		interval := updateIntervalField(10 * time.Second)
		widget.UpdateInterval = &interval
	}

	if *widget.UpdateInterval <= 0 {
		return errors.New("update-interval must be greater than 0")
	}

	return nil
}

func (widget *dynawidgetsWidget) update(ctx context.Context) {
	widget.Hidden = false
	widget.refreshTemplate()

	compiledHTML, hidden, rawResponse, err := fetchAndRenderCustomAPIRequest(
		widget.CustomAPIRequest, widget.Subrequests, widget.Options, widget.compiledTemplate,
	)
	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	widget.APIResponse = rawResponse
	widget.Hidden = hidden
	widget.CompiledHTML = rewriteImgSrcs(ctx, compiledHTML, widget.Providers)
	widget.noticeTemplateUpdate()
}

// Polls for a newer template and recompiles whenever the cache file on disk changed.
func (widget *dynawidgetsWidget) refreshTemplate() {
	if widget.slug == "" {
		return
	}

	if time.Since(widget.templateCheckedAt) >= dynawidgetsCheckPollInterval {
		widget.templateCheckedAt = time.Now()
		if err := dynawidgetsCheckTemplate(widget.slug, widget.repo); err != nil {
			slog.Warn("Dynawidget template update check failed", "slug", widget.slug, "error", err)
		}
	}

	modTime := dynawidgetsTemplateModTime(widget.slug, widget.repo)
	if modTime.IsZero() || modTime.Equal(widget.templateModTime) {
		return
	}
	widget.templateModTime = modTime

	templateContent, _, _, err := dynawidgetsResolveTemplate(widget.slug, widget.repo)
	if err != nil {
		slog.Warn("Could not reload updated dynawidget template", "slug", widget.slug, "error", err)
		return
	}

	compiledTemplate, err := template.New("").Funcs(customAPITemplateFuncs(widget.Providers)).Parse(templateContent)
	if err != nil {
		slog.Error("Failed to parse updated dynawidget template", "slug", widget.slug, "error", err)
		return
	}

	widget.templateContent = templateContent
	widget.compiledTemplate = compiledTemplate
	widget.templateUpdatedAt = time.Now()
	slog.Info("Reloaded updated dynawidget template", "slug", widget.slug, "repo", widget.repo)
}

// Flags a template that changed under the user for a while after the reload.
func (widget *dynawidgetsWidget) noticeTemplateUpdate() {
	if widget.templateUpdatedAt.IsZero() || time.Since(widget.templateUpdatedAt) > dynawidgetsUpdateNoticeDuration {
		return
	}

	widget.withNotice(fmt.Errorf(
		"template updated on %s - reload the page or check the widget still looks right",
		widget.templateUpdatedAt.Format("2006-01-02 15:04"),
	))
}

func (widget *dynawidgetsWidget) setProviders(providers *widgetProviders) {
	widget.widgetBase.setProviders(providers)
	if widget.templateContent == "" {
		return
	}

	compiledTemplate, err := template.New("").Funcs(customAPITemplateFuncs(providers)).Parse(widget.templateContent)
	if err != nil {
		slog.Error("Failed to recompile dynawidget template", "error", err)
		return
	}

	widget.compiledTemplate = compiledTemplate
}

func (widget *dynawidgetsWidget) Render() template.HTML {
	return widget.renderTemplate(widget, customAPIWidgetTemplate)
}

func dynawidgetsSplitTemplate(raw string) (templateContent string, requiredRaw string) {
	const separator = "required: |"

	idx := strings.LastIndex(raw, separator)
	if idx == -1 {
		return raw, ""
	}

	return strings.TrimRight(raw[:idx], "\n\r "), dedentYAMLBlock(raw[idx+len(separator):])
}

func dynawidgetsParseTemplate(raw string) (templateContent string, required *dynawidgetsRequired, err error) {
	templateContent, requiredRaw := dynawidgetsSplitTemplate(raw)
	if requiredRaw == "" {
		return templateContent, nil, nil
	}

	// Env placeholders only, so ${secret:...} and ${readFileFromEnv:...} stay untouched.
	expanded, err := parseEnvVariablesOnly([]byte(requiredRaw))
	if err != nil {
		return "", nil, fmt.Errorf("required section: %w", err)
	}

	required = &dynawidgetsRequired{}
	if err := yaml.Unmarshal(expanded, required); err != nil {
		slog.Error("Failed to parse dynawidget required section", "error", err)
		return templateContent, nil, nil
	}

	return templateContent, required, nil
}

func dedentYAMLBlock(raw string) string {
	lines := strings.Split(raw, "\n")

	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}

	if minIndent <= 0 {
		return strings.TrimSpace(raw)
	}

	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		} else {
			lines[i] = strings.TrimLeft(line, " \t")
		}
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func dynawidgetsResolveTemplate(slug string, repo string) (templateContent string, title string, required *dynawidgetsRequired, err error) {
	raw, title, err := dynawidgetsRawTemplate(slug, repo)
	if err != nil {
		return "", "", nil, err
	}

	templateContent, required, err = dynawidgetsParseTemplate(raw)
	if err != nil {
		return "", "", nil, err
	}

	return templateContent, title, required, nil
}

// Returns the template exactly as the repository ships it, without any variable expansion.
func dynawidgetsRawTemplate(slug string, repo string) (raw string, title string, err error) {
	if repo == "" {
		repo = dynawidgetsDefaultRepo
	}

	templatePath, err := dynawidgetsAssetPath(slug, repo, ".txt")
	if err != nil {
		return "", "", err
	}

	if data, readErr := os.ReadFile(templatePath); readErr == nil {
		slog.Info("Using cached dynawidget template", "slug", slug, "path", templatePath)
		if meta := dynawidgetsReadMeta(slug, repo); meta != nil {
			title = meta.Title
		}
		return string(data), title, nil
	}

	templateURL, title, err := dynawidgetsTemplateURL(slug, repo)
	if err != nil {
		return "", "", err
	}

	slog.Info("Fetching dynawidget template", "slug", slug, "url", templateURL)

	bodyBytes, _, etag, err := dynawidgetsDownloadTemplate(templateURL, "")
	if err != nil {
		return "", "", err
	}

	if err := os.MkdirAll(dynawidgetsAssetsDir, 0755); err != nil {
		slog.Error("Failed to create dynawidgets assets directory", "error", err)
	} else if err := os.WriteFile(templatePath, bodyBytes, 0600); err != nil {
		slog.Error("Failed to cache dynawidget template", "error", err, "path", templatePath)
	} else {
		slog.Info("Cached dynawidget template", "slug", slug, "path", templatePath)
		dynawidgetsWriteMeta(slug, repo, &dynawidgetsTemplateMeta{
			URL:       templateURL,
			Title:     title,
			ETag:      etag,
			CheckedAt: time.Now(),
		})
	}

	return string(bodyBytes), title, nil
}

// Looks the widget up in the repository's per-letter list file.
func dynawidgetsTemplateURL(slug string, repo string) (templateURL string, title string, err error) {
	if !dynawidgetsSlugPattern.MatchString(slug) {
		return "", "", fmt.Errorf("invalid slug %q", slug)
	}

	listURL := fmt.Sprintf("https://%s/Panonim/dynawidgets/refs/heads/%s/database/list-%s.json", dynawidgetsTemplateHost, repo, slug[:1])

	slog.Info("Fetching dynawidgets list", "url", listURL)

	resp, err := defaultHTTPClient.Get(listURL)
	if err != nil {
		return "", "", fmt.Errorf("fetching widget list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("fetching widget list: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var entries []dynawidgetsListEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return "", "", fmt.Errorf("decoding widget list: %w", err)
	}

	var entry *dynawidgetsListEntry
	for i := range entries {
		if entries[i].Slug == slug {
			entry = &entries[i]
			break
		}
	}

	if entry == nil {
		return "", "", fmt.Errorf("widget %q not found in dynawidgets list", slug)
	}

	templateURL = entry.Template
	if repo != dynawidgetsDefaultRepo {
		templateURL = strings.Replace(templateURL, "/refs/heads/"+dynawidgetsDefaultRepo+"/", "/refs/heads/"+repo+"/", 1)
	}

	return templateURL, entry.Title, nil
}

type dynawidgetsVariable struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Set  bool   `json:"set"`
}

// Lists the variables a widget's required block expects and whether the container has each one.
func dynawidgetsRequiredVariables(slug string, repo string) ([]dynawidgetsVariable, error) {
	raw, _, err := dynawidgetsRawTemplate(slug, repo)
	if err != nil {
		return nil, err
	}

	_, requiredRaw := dynawidgetsSplitTemplate(raw)
	if requiredRaw == "" {
		return nil, nil
	}

	variables := make([]dynawidgetsVariable, 0)
	seen := make(map[string]bool)

	for _, match := range configVariablePattern.FindAllStringSubmatch(requiredRaw, -1) {
		// Typed variables are never expanded in a template, so only env ones are worth reporting.
		if match[1] == `\` || match[2] != "" {
			continue
		}

		name := match[3]
		if seen[name] {
			continue
		}
		seen[name] = true

		_, isSet := os.LookupEnv(name)
		variables = append(variables, dynawidgetsVariable{Name: name, Type: configVarTypeEnv, Set: isSet})
	}

	return variables, nil
}
