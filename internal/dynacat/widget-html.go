package dynacat

import (
	"context"
	"html/template"
)

type htmlWidget struct {
	widgetBase    `yaml:",inline"`
	Frameless     bool          `yaml:"frameless"`
	Source        template.HTML `yaml:"source"`
	processedHTML template.HTML `yaml:"-"`
}

func (widget *htmlWidget) initialize() error {
	widget.withTitle("").withError(nil)
	widget.processedHTML = widget.Source

	return nil
}

func (widget *htmlWidget) setProviders(providers *widgetProviders) {
	widget.widgetBase.setProviders(providers)
	widget.processedHTML = rewriteImgSrcs(context.Background(), widget.Source, providers)
}

func (widget *htmlWidget) Render() template.HTML {
	return widget.processedHTML
}
