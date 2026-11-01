package dynacat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/renderer/html"
)

var markdownWidgetTemplate = mustParseTemplate("markdown.html", "widget-base.html")

var (
	markdownRenderer       = goldmark.New()
	markdownUnsafeRenderer = goldmark.New(goldmark.WithRendererOptions(html.WithUnsafe()))
)

type markdownWidget struct {
	widgetBase `yaml:",inline"`
	Frameless  bool          `yaml:"frameless"`
	Source     string        `yaml:"source"`
	File       string        `yaml:"file"`
	AllowHTML  bool          `yaml:"allow-html"`
	Content    template.HTML `yaml:"-"`
	filePath   string        `yaml:"-"`
	modTime    time.Time     `yaml:"-"`
	cachedHTML template.HTML `yaml:"-"`
}

func (widget *markdownWidget) initialize() error {
	widget.withTitle("").withError(nil)

	if (widget.Source == "") == (widget.File == "") {
		return errors.New("exactly one of source or file is required")
	}

	return nil
}

func (widget *markdownWidget) setProviders(providers *widgetProviders) {
	widget.widgetBase.setProviders(providers)

	if widget.File == "" {
		widget.renderMarkdown([]byte(widget.Source))
		return
	}

	// Rooting the path before joining keeps "../" from escaping the assets directory.
	widget.filePath = filepath.Join(providers.app.Config.assetsPath(), filepath.Clean("/"+strings.TrimPrefix(widget.File, "/assets/")))
}

func (widget *markdownWidget) Render() template.HTML {
	if widget.filePath != "" {
		widget.loadFile()
	}

	return widget.cachedHTML
}

func (widget *markdownWidget) loadFile() {
	info, err := os.Stat(widget.filePath)
	if err == nil && info.ModTime().Equal(widget.modTime) {
		return
	}

	var data []byte
	if err == nil {
		data, err = os.ReadFile(widget.filePath)
	}

	if err != nil {
		// Unwrapping drops the absolute server path from the PathError.
		widget.modTime = time.Time{}
		widget.ContentAvailable = false
		widget.withError(fmt.Errorf("reading %s: %w", widget.File, errors.Unwrap(err)))
		widget.cachedHTML = widget.renderTemplate(widget, markdownWidgetTemplate)
		return
	}

	widget.modTime = info.ModTime()
	widget.withError(nil)
	widget.renderMarkdown(data)
}

func (widget *markdownWidget) renderMarkdown(source []byte) {
	renderer := markdownRenderer
	if widget.AllowHTML {
		renderer = markdownUnsafeRenderer
	}

	var buf bytes.Buffer
	if err := renderer.Convert(source, &buf); err != nil {
		widget.withError(err)
	}

	widget.Content = rewriteImgSrcs(context.Background(), template.HTML(buf.String()), widget.Providers)
	widget.cachedHTML = widget.renderTemplate(widget, markdownWidgetTemplate)
}
