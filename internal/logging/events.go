// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package logging

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The closed registry of log events (C-02.FR-18, ADR-0014). Every event Muster logs is declared below with newEvent;
// code outside this package cannot create one. Levels are chosen by the reader: ERROR when someone must look within
// the hour, WARN when the line is needed for an investigation, INFO when an investigation is impossible without it.
// Each capability adds its own events; adding one is a reviewed change, and make generate rebuilds the reference page.

// ProcessStarted is the first line of every run of the server.
var ProcessStarted = newEvent("process_started", LevelInfo, "C-02",
	"The server process started, with the version and commit of the binary.",
	"version", "commit")

// LibraryMessage is a line a third-party library wrote to the standard library logger, once CaptureStdlib redirected
// it into the domain logger.
var LibraryMessage = newEvent("library_message", LevelWarn, "C-02",
	"A third-party library wrote a line of its own, such as a failed read of the process metrics.",
	"message")

// Event is a registered log event. Its zero value is not registered, and the logger refuses it.
type Event struct {
	def *eventDef
}

type eventDef struct {
	name        string
	level       Level
	capability  string
	description string
	fields      []string
}

// EventInfo describes a registered event for the reference page.
type EventInfo struct {
	Name        string
	Level       Level
	Fields      []string
	Capability  string
	Description string
}

var (
	registry []*eventDef

	snakeCaseRe  = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	capabilityRe = regexp.MustCompile(`^C-[0-9]{2}$`)

	// reservedKeys are written by the logger itself on every line.
	reservedKeys = []string{"time", "level", "event", "msg"}
)

func newEvent(name string, level Level, capability, description string, fields ...string) Event {
	def := &eventDef{name: name, level: level, capability: capability, description: description, fields: fields}
	registry = append(registry, def)
	return Event{def: def}
}

// Name is the event's name, as written in the event key of its lines.
func (e Event) Name() string {
	if e.def == nil {
		return ""
	}
	return e.def.name
}

func (d *eventDef) declares(field string) bool {
	return slices.Contains(d.fields, field)
}

// Events returns the registered events sorted by name, and an error naming every invalid declaration.
func Events() ([]EventInfo, error) {
	return describe(registry)
}

func describe(defs []*eventDef) ([]EventInfo, error) {
	infos := make([]EventInfo, 0, len(defs))
	for _, d := range defs {
		infos = append(infos, EventInfo{
			Name:        d.name,
			Level:       d.level,
			Fields:      slices.Clone(d.fields),
			Capability:  d.capability,
			Description: d.description,
		})
	}
	slices.SortFunc(infos, func(a, b EventInfo) int { return strings.Compare(a.Name, b.Name) })
	return infos, validate(infos)
}

func validate(infos []EventInfo) error {
	var errs []error
	seen := map[string]bool{}
	for _, e := range infos {
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("log event %q: %s", e.Name, fmt.Sprintf(format, args...)))
		}
		if !snakeCaseRe.MatchString(e.Name) {
			bad("the name is not snake_case")
		}
		if seen[e.Name] {
			bad("declared twice")
		}
		seen[e.Name] = true
		if !e.Level.valid() {
			bad("level %d is not INFO, WARN or ERROR", int(e.Level))
		}
		if !capabilityRe.MatchString(e.Capability) {
			bad("capability %q is not a capability ID such as C-02", e.Capability)
		}
		if strings.TrimSpace(e.Description) == "" {
			bad("the description is empty")
		}
		fields := map[string]bool{}
		for _, f := range e.Fields {
			switch {
			case !snakeCaseRe.MatchString(f):
				bad("field %q is not snake_case", f)
			case slices.Contains(reservedKeys, f):
				bad("field %q is reserved for the logger", f)
			case fields[f]:
				bad("field %q is declared twice", f)
			}
			fields[f] = true
		}
	}
	return errors.Join(errs...)
}
