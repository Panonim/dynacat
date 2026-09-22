package dynacat

import (
	"context"
	"sync"
	"time"
)

type containerWidgetBase struct {
	Widgets widgets `yaml:"widgets"`
}

func (widget *containerWidgetBase) _initializeWidgets() error {
	for i := range widget.Widgets {
		if err := widget.Widgets[i].initialize(); err != nil {
			return formatWidgetInitError(err, widget.Widgets[i])
		}
	}

	return nil
}

func (widget *containerWidgetBase) _update(ctx context.Context) {
	var wg sync.WaitGroup
	now := time.Now()

	for w := range widget.Widgets {
		widget := widget.Widgets[w]

		if !widget.requiresUpdate(&now) {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			widget.update(withSharedFetchMaxAge(ctx, widget.getCacheDuration()))
		}()
	}

	wg.Wait()
}

func (widget *containerWidgetBase) _serverPrewarm(ctx context.Context, prewarmAll bool) {
	var wg sync.WaitGroup

	for w := range widget.Widgets {
		widget := widget.Widgets[w]

		wg.Go(func() {
			serverPrewarm(ctx, widget, prewarmAll)
		})
	}

	wg.Wait()
}

func (widget *containerWidgetBase) _setProviders(providers *widgetProviders) {
	for i := range widget.Widgets {
		widget.Widgets[i].setProviders(providers)
	}
}

func (widget *containerWidgetBase) _requiresUpdate(now *time.Time) bool {
	for i := range widget.Widgets {
		if widget.Widgets[i].requiresUpdate(now) {
			return true
		}
	}

	return false
}
