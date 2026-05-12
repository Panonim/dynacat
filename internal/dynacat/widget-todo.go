package dynacat

import (
	"fmt"
	"html/template"
)

var todoWidgetTemplate = mustParseTemplate("todo.html", "widget-base.html")

type todoWidget struct {
	widgetBase `yaml:",inline"`
	Frameless  bool          `yaml:"frameless"`
	cachedHTML template.HTML `yaml:"-"`
	TodoID     string        `yaml:"id"`
	Storage    string        `yaml:"storage"`
	CollapseAfter *int       `yaml:"collapse-after"`
}

func (widget *todoWidget) initialize() error {
	widget.withTitle("To-do").withError(nil)

	if widget.Storage != "" && widget.Storage != "local" && widget.Storage != "server" {
		return fmt.Errorf("storage must be either \"local\" or \"server\", got %q", widget.Storage)
	}

	if widget.Storage == "server" && widget.TodoID == "" {
		return fmt.Errorf("storage \"server\" requires an \"id\" to be set")
	}

	if widget.CollapseAfter != nil && *widget.CollapseAfter < -1 {
		return fmt.Errorf("collapse-after must be -1 or greater, got %d", *widget.CollapseAfter)
	}

	widget.cachedHTML = widget.renderTemplate(widget, todoWidgetTemplate)
	return nil
}

func (widget *todoWidget) Render() template.HTML {
	return widget.cachedHTML
}

func (widget *todoWidget) HasCollapseAfter() bool {
	return widget.CollapseAfter != nil
}

func (widget *todoWidget) GetCollapseAfter() int {
	if widget.CollapseAfter == nil {
		return 0
	}

	return *widget.CollapseAfter
}
