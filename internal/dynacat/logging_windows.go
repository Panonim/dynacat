package dynacat

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/eventlog"
)

const eventLogSource = "Dynacat"

// Legacy consoles print raw escape codes unless virtual terminal processing is on.
func enableConsoleColors() bool {
	handle := windows.Handle(os.Stderr.Fd())

	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false
	}

	return windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}

func newBackgroundLogHandler(level slog.Level) slog.Handler {
	// Detaching closes the console window the shortcut opened.
	windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole").Call()

	log, err := eventlog.Open(eventLogSource)
	if err != nil {
		return nil
	}

	return &eventLogHandler{prettyHandler: newPrettyHandler(nil, level, false), log: log}
}

type eventLogHandler struct {
	*prettyHandler
	log *eventlog.Log
}

func (h *eventLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &eventLogHandler{prettyHandler: h.prettyHandler.WithAttrs(attrs).(*prettyHandler), log: h.log}
}

func (h *eventLogHandler) WithGroup(name string) slog.Handler {
	return &eventLogHandler{prettyHandler: h.prettyHandler.WithGroup(name).(*prettyHandler), log: h.log}
}

func (h *eventLogHandler) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	formatter := *h.prettyHandler
	formatter.out = &b
	formatter.Handle(ctx, r)
	message := strings.TrimSuffix(b.String(), "\n")

	switch {
	case r.Level >= slog.LevelError:
		return h.log.Error(1, message)
	case r.Level >= slog.LevelWarn:
		return h.log.Warning(1, message)
	default:
		return h.log.Info(1, message)
	}
}
