// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func testEvent(level Level, fields ...string) Event {
	return Event{def: &eventDef{name: "test_event", level: level, capability: "C-02", description: "A test event.", fields: fields}}
}

func decodeLines(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.Lines(out.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %q is not a JSON object: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines
}

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want one containing %q", want)
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, want) {
			t.Fatalf("panic %q, want one containing %q", msg, want)
		}
	}()
	f()
}

func TestLogShape(t *testing.T) {
	var out bytes.Buffer
	before := time.Now().UTC().Truncate(time.Millisecond)
	New(&out, LevelInfo).Log(t.Context(), ProcessStarted, F("version", "1.2.3"), F("commit", "abc123"))
	after := time.Now().UTC()

	if n := strings.Count(out.String(), "\n"); n != 1 {
		t.Fatalf("got %d lines, want 1: %q", n, out.String())
	}
	lines := decodeLines(t, &out)
	got := lines[0]
	want := map[string]any{"level": "INFO", "event": "process_started", "version": "1.2.3", "commit": "abc123"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want)+1 {
		t.Errorf("got keys %v, want time plus %v", got, want)
	}
	stamp, _ := got["time"].(string)
	if !strings.HasSuffix(stamp, "Z") {
		t.Errorf("time %q is not in UTC", stamp)
	}
	ts, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("time %q is not RFC 3339: %v", stamp, err)
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("time %v is not between %v and %v", ts, before, after)
	}
	if !strings.HasPrefix(out.String(), `{"time":`) {
		t.Errorf("the line does not start with time: %q", out.String())
	}
}

func TestLogLevels(t *testing.T) {
	events := map[Level]Event{LevelInfo: testEvent(LevelInfo), LevelWarn: testEvent(LevelWarn), LevelError: testEvent(LevelError)}
	tests := []struct {
		threshold Level
		want      []string
	}{
		{LevelInfo, []string{"INFO", "WARN", "ERROR"}},
		{LevelWarn, []string{"WARN", "ERROR"}},
		{LevelError, []string{"ERROR"}},
	}
	for _, tt := range tests {
		t.Run(tt.threshold.String(), func(t *testing.T) {
			var out bytes.Buffer
			l := New(&out, tt.threshold)
			var enabled []string
			for _, level := range []Level{LevelInfo, LevelWarn, LevelError} {
				l.Log(t.Context(), events[level])
				if l.Enabled(t.Context(), events[level]) {
					enabled = append(enabled, level.String())
				}
			}
			var got []string
			for _, line := range decodeLines(t, &out) {
				got = append(got, line["level"].(string))
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) || fmt.Sprint(enabled) != fmt.Sprint(tt.want) {
				t.Errorf("written %v, enabled %v, want %v", got, enabled, tt.want)
			}
		})
	}
}

type credentials struct {
	User     string `json:"user"`
	Password Secret `json:"password"`
}

func TestSecretRedacted(t *testing.T) {
	const secret = "hunter2-very-secret"
	var out bytes.Buffer
	e := testEvent(LevelInfo, "token", "credentials", "error", "tokens")
	New(&out, LevelInfo).Log(t.Context(), e,
		F("token", Secret(secret)),
		F("credentials", credentials{User: "alice", Password: Secret(secret)}),
		F("error", fmt.Errorf("dial with %v: %w", Secret(secret), errors.ErrUnsupported)),
		F("tokens", []Secret{Secret(secret)}),
	)
	if strings.Contains(out.String(), secret) {
		t.Fatalf("the secret reached the log: %s", out.String())
	}
	line := decodeLines(t, &out)[0]
	if line["token"] != redacted {
		t.Errorf("token = %v, want %s", line["token"], redacted)
	}
	if c, _ := line["credentials"].(map[string]any); c["password"] != redacted || c["user"] != "alice" {
		t.Errorf("credentials = %v", line["credentials"])
	}
	if line["error"] != "dial with [redacted]: unsupported operation" {
		t.Errorf("error = %v", line["error"])
	}
	for _, s := range []string{fmt.Sprint(Secret(secret)), fmt.Sprintf("%#v %x %q %s", Secret(secret), Secret(secret), Secret(secret), Secret(secret))} {
		if strings.Contains(s, secret) {
			t.Errorf("fmt printed the secret: %q", s)
		}
	}
	if text, _ := Secret(secret).MarshalText(); string(text) != redacted {
		t.Errorf("MarshalText = %q", text)
	}
}

func TestRefusedInTests(t *testing.T) {
	l := New(&bytes.Buffer{}, LevelError)
	mustPanic(t, `event process_started does not declare the field "user"`, func() {
		l.Log(t.Context(), ProcessStarted, F("version", "1"), F("user", "alice"))
	})
	mustPanic(t, `event process_started got the field "version" twice`, func() {
		l.Log(t.Context(), ProcessStarted, F("version", "1"), F("version", "2"))
	})
	mustPanic(t, "the event is not registered", func() {
		l.Log(t.Context(), Event{})
	})
}

func TestRefusedAtAnyThreshold(t *testing.T) {
	l := New(&bytes.Buffer{}, LevelError)
	mustPanic(t, `does not declare the field "user"`, func() {
		l.Log(t.Context(), ProcessStarted, F("user", "alice"))
	})
}

func TestCaptureStdlib(t *testing.T) {
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	var out bytes.Buffer
	New(&out, LevelInfo).CaptureStdlib(t.Context())
	log.Printf("ERROR: metrics: cannot open %s", "/proc/self/stat")
	slog.Info("from the default slog logger")
	lines := decodeLines(t, &out)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %s", len(lines), out.String())
	}
	if lines[0]["event"] != "library_message" || lines[0]["level"] != "WARN" ||
		lines[0]["message"] != "ERROR: metrics: cannot open /proc/self/stat" {
		t.Errorf("first line = %v", lines[0])
	}
	if msg, _ := lines[1]["message"].(string); !strings.Contains(msg, "from the default slog logger") {
		t.Errorf("second line = %v", lines[1])
	}
}

func TestRefusedOutsideTests(t *testing.T) {
	strict = false
	t.Cleanup(func() { strict = true })
	var out bytes.Buffer
	l := New(&out, LevelInfo)
	l.Log(t.Context(), Event{})
	l.Log(t.Context(), ProcessStarted, F("version", "1"), F("user", "alice"), F("version", "2"))
	New(&out, LevelError).Log(t.Context(), ProcessStarted, F("user", "alice"))
	lines := decodeLines(t, &out)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %s", len(lines), out.String())
	}
	if _, ok := lines[0]["user"]; ok || lines[0]["version"] != "1" {
		t.Errorf("the refused fields were written: %v", lines[0])
	}
	if l.Enabled(t.Context(), Event{}) {
		t.Error("an unregistered event is enabled")
	}
}

func TestRegistry(t *testing.T) {
	events, err := Events()
	if err != nil {
		t.Fatalf("the registry is invalid:\n%v", err)
	}
	found := false
	for _, e := range events {
		if e.Name == ProcessStarted.Name() {
			found = e.Level == LevelInfo && fmt.Sprint(e.Fields) == "[version commit]" && e.Capability == "C-02"
		}
	}
	if !found {
		t.Errorf("process_started is not registered as INFO with version and commit: %+v", events)
	}
	if (Event{}).Name() != "" {
		t.Error("the zero event has a name")
	}
}

func TestRegistryValidation(t *testing.T) {
	good := func() *eventDef {
		return &eventDef{name: "good_event", level: LevelWarn, capability: "C-05", description: "Fine.", fields: []string{"integration"}}
	}
	tests := []struct {
		name   string
		mutate func(d *eventDef)
		want   string
	}{
		{"name", func(d *eventDef) { d.name = "BadName" }, "the name is not snake_case"},
		{"level", func(d *eventDef) { d.level = Level(-4) }, "level -4 is not INFO, WARN or ERROR"},
		{"capability", func(d *eventDef) { d.capability = "groups" }, `capability "groups" is not a capability ID`},
		{"description", func(d *eventDef) { d.description = " " }, "the description is empty"},
		{"field case", func(d *eventDef) { d.fields = []string{"routeID"} }, `field "routeID" is not snake_case`},
		{"reserved field", func(d *eventDef) { d.fields = []string{"level"} }, `field "level" is reserved`},
		{"repeated field", func(d *eventDef) { d.fields = []string{"route", "route"} }, `field "route" is declared twice`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := good()
			tt.mutate(d)
			_, err := describe([]*eventDef{d})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}
	if _, err := describe([]*eventDef{good(), good()}); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("a duplicate event: got %v", err)
	}
	if _, err := describe([]*eventDef{good()}); err != nil {
		t.Errorf("a good event: %v", err)
	}
}

func TestLevelString(t *testing.T) {
	if got := Level(2).String(); got != "Level(2)" {
		t.Errorf("Level(2) = %q", got)
	}
	if NewStdout(LevelInfo) == nil {
		t.Error("NewStdout returned nil")
	}
}
