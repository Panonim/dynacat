//go:build !windows

package dynacat

import "log/slog"

func enableConsoleColors() bool {
	return true
}

func newBackgroundLogHandler(slog.Level) slog.Handler {
	return nil
}
