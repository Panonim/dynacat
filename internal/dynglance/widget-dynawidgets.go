package dynglance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const dynawidgetsDefaultRepo = "main"
const dynawidgetsAssetsDir = "/app/assets/dynawidgets"

var dynawidgetsSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var dynawidgetsRepoPattern = regexp.MustCompile(`^[a-zA-Z0-9._/-]{1,64}$`)
var dynawidgetsTemplateHost = "raw.githubusercontent.com"

type dynawidgetsWidget struct {
	widgetBase        `yaml:",inline"`
	Widget            string `yaml:"widget"`
	Repo              string `yaml:"repo"`
	*CustomAPIRequest `yaml:",inline"`
	Subrequests       map[string]*CustomAPIRequest `yaml:"subrequests"`
	Options           customAPIOptions             `yaml:"options"`
	Frameless         bool                         `yaml:"frameless"`
	templateContent   string                       `yaml:"-"`
	compiledTemplate  *template.Template           `yaml:"-"`
	CompiledHTML      template.HTML                `yaml:"-"`
}

type dynawidgetsListEntry struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Slug        string `json:"slug"`
	Template    string `json:"template"`
}

type dynawidgetsRequired struct {
	URL string `yaml:"url"`
}

func (widget *dynawidgetsWidget) initialize() error {
	widget.withTitle("Dynawidgets").withCacheDuration(1 * time.Minute)

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
	widget.templateContent = templateContent

	if widget.Title == "" && title != "" {
		widget.Title = title
	}

	// Apply required defaults if user hasn't specified them
	if required != nil && required.URL != "" {
		if widget.CustomAPIRequest == nil {
			widget.CustomAPIRequest = &CustomAPIRequest{}
		}
		if widget.CustomAPIRequest.URL == "" {
			widget.CustomAPIRequest.URL = required.URL
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
	compiledHTML, hidden, err := fetchAndRenderCustomAPIRequest(
		widget.CustomAPIRequest, widget.Subrequests, widget.Options, widget.compiledTemplate,
	)
	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	widget.Hidden = hidden
	widget.CompiledHTML = rewriteImgSrcs(ctx, compiledHTML, widget.Providers)
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

// dynawidgetsParseTemplate splits a template file into the template content
// and the required section. The required section starts with "required: |"
// on its own line at the bottom of the file.
func dynawidgetsParseTemplate(raw string) (templateContent string, required *dynawidgetsRequired) {
	const separator = "required: |"

	idx := strings.LastIndex(raw, separator)
	if idx == -1 {
		return raw, nil
	}

	templateContent = strings.TrimRight(raw[:idx], "\n\r ")
	requiredRaw := strings.TrimSpace(raw[idx+len(separator):])

	if requiredRaw == "" {
		return templateContent, nil
	}

	required = &dynawidgetsRequired{}
	if err := yaml.Unmarshal([]byte(requiredRaw), required); err != nil {
		slog.Error("Failed to parse dynawidget required section", "error", err)
		return templateContent, nil
	}

	return templateContent, required
}

// dynawidgetsResolveTemplate checks for a cached template on disk, or fetches
// it from the dynawidgets repository. Returns the template content, the
// widget title (empty if loaded from cache), and parsed required config.
func dynawidgetsResolveTemplate(slug string, repo string) (templateContent string, title string, required *dynawidgetsRequired, err error) {
	if !dynawidgetsSlugPattern.MatchString(slug) {
		return "", "", nil, fmt.Errorf("invalid slug %q", slug)
	}
	templatePath := filepath.Join(dynawidgetsAssetsDir, slug+".txt")
	if absAssets, aerr := filepath.Abs(dynawidgetsAssetsDir); aerr == nil {
		if absTemplate, terr := filepath.Abs(templatePath); terr != nil || !strings.HasPrefix(absTemplate, absAssets+string(filepath.Separator)) {
			return "", "", nil, fmt.Errorf("refusing to resolve template outside assets dir")
		}
	}

	// Check if template already exists on disk
	if data, readErr := os.ReadFile(templatePath); readErr == nil {
		slog.Info("Using cached dynawidget template", "slug", slug, "path", templatePath)
		content, req := dynawidgetsParseTemplate(string(data))
		return content, "", req, nil
	}

	// Fetch the list index for the first letter of the slug
	firstLetter := string(slug[0])
	baseURL := fmt.Sprintf("https://raw.githubusercontent.com/Panonim/dynawidgets/refs/heads/%s", repo)
	listURL := fmt.Sprintf("%s/database/list-%s.json", baseURL, firstLetter)

	slog.Info("Fetching dynawidgets list", "url", listURL)

	resp, err := defaultHTTPClient.Get(listURL)
	if err != nil {
		return "", "", nil, fmt.Errorf("fetching widget list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", nil, fmt.Errorf("fetching widget list: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var entries []dynawidgetsListEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return "", "", nil, fmt.Errorf("decoding widget list: %w", err)
	}

	// Find the matching slug
	var entry *dynawidgetsListEntry
	for i := range entries {
		if entries[i].Slug == slug {
			entry = &entries[i]
			break
		}
	}

	if entry == nil {
		return "", "", nil, fmt.Errorf("widget %q not found in dynawidgets list", slug)
	}

	// Fetch the template content
	templateURL := entry.Template
	if repo != dynawidgetsDefaultRepo {
		// entry.Template points to the default branch; rewrite to the requested repo
		templateURL = strings.Replace(
			entry.Template,
			"/refs/heads/"+dynawidgetsDefaultRepo+"/",
			"/refs/heads/"+repo+"/",
			1,
		)
	}

	parsedTemplateURL, err := url.Parse(templateURL)
	if err != nil {
		return "", "", nil, fmt.Errorf("invalid template URL %q: %w", templateURL, err)
	}
	if parsedTemplateURL.Scheme != "https" || parsedTemplateURL.Host != dynawidgetsTemplateHost {
		return "", "", nil, fmt.Errorf(
			"refusing to fetch template from unexpected host %q (must be https://%s)",
			parsedTemplateURL.Host, dynawidgetsTemplateHost,
		)
	}

	slog.Info("Fetching dynawidget template", "slug", slug, "url", templateURL)

	templateResp, err := defaultHTTPClient.Get(templateURL)
	if err != nil {
		return "", "", nil, fmt.Errorf("fetching template: %w", err)
	}
	defer templateResp.Body.Close()

	if templateResp.StatusCode != http.StatusOK {
		return "", "", nil, fmt.Errorf("fetching template: %d %s", templateResp.StatusCode, http.StatusText(templateResp.StatusCode))
	}

	bodyBytes, err := io.ReadAll(templateResp.Body)
	if err != nil {
		return "", "", nil, fmt.Errorf("reading template body: %w", err)
	}

	rawContent := string(bodyBytes)

	// Save to disk for future use
	if err := os.MkdirAll(dynawidgetsAssetsDir, 0755); err != nil {
		slog.Error("Failed to create dynawidgets assets directory", "error", err)
	} else if err := os.WriteFile(templatePath, bodyBytes, 0600); err != nil {
		slog.Error("Failed to cache dynawidget template", "error", err, "path", templatePath)
	} else {
		slog.Info("Cached dynawidget template", "slug", slug, "path", templatePath)
	}

	templateContent, required = dynawidgetsParseTemplate(rawContent)
	return templateContent, entry.Title, required, nil
}
