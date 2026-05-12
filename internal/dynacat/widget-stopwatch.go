package dynacat

import "html/template"

var stopwatchWidgetTemplate = mustParseTemplate("stopwatch.html", "widget-base.html")

type stopwatchWidget struct {
	widgetBase   `yaml:",inline"`
	Frameless    bool          `yaml:"frameless"`
	StartOnOpen  bool          `yaml:"start-on-open"`
	cachedHTML   template.HTML `yaml:"-"`
}

func (widget *stopwatchWidget) initialize() error {
	widget.withTitle("Stopwatch").withError(nil)
	widget.UpdateInterval = nil
	widget.cachedHTML = widget.renderTemplate(widget, stopwatchWidgetTemplate)
	return nil
}

func (widget *stopwatchWidget) Render() template.HTML {
	return widget.cachedHTML
}
