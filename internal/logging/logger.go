// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package logging is the domain logger (C-02.FR-17, ADR-0014): one JSON object per line, written only as the events
// registered in events.go and only with the fields each event declares. It is the only package that imports log/slog
// or writes to stdout and stderr.
package logging

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// Level is the level of an event and the threshold of a logger.
type Level int

const (
	LevelInfo  = Level(slog.LevelInfo)
	LevelWarn  = Level(slog.LevelWarn)
	LevelError = Level(slog.LevelError)
)

const (
	redacted   = "[redacted]"
	timeFormat = "2006-01-02T15:04:05.000Z07:00"
)

// strict makes the logger panic on a refused line, so that the test doing it fails. Outside tests a refused field or
// event is dropped instead: a wrong log line never stops the alerting path.
var strict = testing.Testing()

func (l Level) String() string {
	switch l {
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	}
	return fmt.Sprintf("Level(%d)", int(l))
}

func (l Level) valid() bool {
	return l == LevelInfo || l == LevelWarn || l == LevelError
}

// Secret is a value that must never reach a log line: it is written as [redacted] by the logger, by fmt and by
// encoding/json.
type Secret string

func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

func (Secret) String() string { return redacted }

func (Secret) GoString() string { return redacted }

func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Field is one field of a log line; its key must be declared by the event.
type Field struct {
	key   string
	value any
}

// F makes a field. Entities are written so that a line is self-contained: group=#412, route=<public_id>.
func F(key string, value any) Field {
	return Field{key: key, value: value}
}

// Logger writes registered events at or above its threshold.
type Logger struct {
	handler slog.Handler
}

// New returns a logger that writes JSON lines to w; events below threshold are skipped.
func New(w io.Writer, threshold Level) *Logger {
	return &Logger{handler: slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       slog.Level(threshold),
		ReplaceAttr: replaceAttr,
	})}
}

// NewStdout returns the process logger, which writes to stdout.
func NewStdout(threshold Level) *Logger {
	return New(os.Stdout, threshold)
}

// CaptureStdlib sends what third-party libraries write to the standard library logger, and to the default slog
// logger, into l as library_message lines logged with ctx.
func (l *Logger) CaptureStdlib(ctx context.Context) {
	log.SetFlags(0)
	log.SetPrefix("")
	log.SetOutput(stdlibWriter{ctx: ctx, logger: l})
}

type stdlibWriter struct {
	ctx    context.Context // the standard library logger has no context of its own
	logger *Logger
}

func (w stdlibWriter) Write(p []byte) (int, error) {
	w.logger.Log(w.ctx, LibraryMessage, F("message", strings.TrimRight(string(p), "\n")))
	return len(p), nil
}

// Enabled reports whether a line of e would be written.
func (l *Logger) Enabled(ctx context.Context, e Event) bool {
	return e.def != nil && l.handler.Enabled(ctx, slog.Level(e.def.level))
}

// Log writes one line of e with fields. An unregistered event, a field e does not declare and a repeated field are
// refused: they panic in tests and are dropped otherwise.
func (l *Logger) Log(ctx context.Context, e Event, fields ...Field) {
	def := e.def
	if def == nil {
		refuse("logging: the event is not registered in internal/logging/events.go")
		return
	}
	// Tests check the fields at every threshold; elsewhere a disabled line costs nothing more.
	enabled := l.handler.Enabled(ctx, slog.Level(def.level))
	if !enabled && !strict {
		return
	}
	attrs := make([]slog.Attr, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		switch {
		case !def.declares(f.key):
			refuse(fmt.Sprintf("logging: event %s does not declare the field %q", def.name, f.key))
			continue
		case seen[f.key]:
			refuse(fmt.Sprintf("logging: event %s got the field %q twice", def.name, f.key))
			continue
		}
		seen[f.key] = true
		attrs = append(attrs, slog.Any(f.key, f.value))
	}
	if !enabled {
		return
	}
	// A line carries the real time: it must agree with the logs of the systems around Muster, not with the business
	// clock.
	r := slog.NewRecord(time.Now(), slog.Level(def.level), def.name, 0)
	r.AddAttrs(attrs...)
	_ = l.handler.Handle(ctx, r)
}

func refuse(msg string) {
	if strict {
		panic(msg)
	}
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case slog.TimeKey:
		if t, ok := a.Value.Any().(time.Time); ok {
			a.Value = slog.StringValue(t.UTC().Format(timeFormat))
		}
	case slog.MessageKey:
		a.Key = "event"
	}
	return a
}
