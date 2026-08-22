package dynglance

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiBlue   = "\033[36m"
	ansiGray   = "\033[90m"
)

func configureLogging() {
	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	slog.SetDefault(slog.New(newPrettyHandler(os.Stderr, level)))
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type prettyHandler struct {
	mu    *sync.Mutex
	out   io.Writer
	level slog.Level
	attrs []slog.Attr
	group string
}

func newPrettyHandler(out io.Writer, level slog.Level) *prettyHandler {
	return &prettyHandler{mu: &sync.Mutex{}, out: out, level: level}
}

func (h *prettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &nh
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	nh := *h
	if h.group != "" {
		nh.group = h.group + "." + name
	} else {
		nh.group = name
	}
	return &nh
}

func levelColor(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return ansiRed
	case level >= slog.LevelWarn:
		return ansiYellow
	case level >= slog.LevelInfo:
		return ansiBlue
	default:
		return ansiGray
	}
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	b.WriteString(ansiDim)
	b.WriteString(r.Time.Format("15:04:05"))
	b.WriteString(ansiReset)
	b.WriteByte(' ')

	color := levelColor(r.Level)
	b.WriteString(color)
	b.WriteString(ansiBold)
	fmt.Fprintf(&b, "%-5s", r.Level.String())
	b.WriteString(ansiReset)
	b.WriteByte(' ')

	b.WriteString(r.Message)

	writeAttr := func(a slog.Attr) {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		b.WriteByte(' ')
		b.WriteString(ansiGray)
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(ansiReset)
		b.WriteString(a.Value.String())
	}

	for _, a := range h.attrs {
		writeAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(a)
		return true
	})

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}
