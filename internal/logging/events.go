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

// ProcessStopped is the last line of a run of the server that ended without an error.
var ProcessStopped = newEvent("process_stopped", LevelInfo, "C-02",
	"The server process stopped after a graceful shutdown.")

// ShutdownRequested is logged when the server receives SIGTERM or an interrupt and starts its graceful shutdown.
var ShutdownRequested = newEvent("shutdown_requested", LevelWarn, "C-02",
	"The server received SIGTERM or an interrupt: readiness answers 503, work in progress finishes and the listeners "+
		"drain within the grace period, in seconds.",
	"grace_seconds")

// ShutdownGraceExceeded is logged when the listeners did not drain within the grace period and were closed.
var ShutdownGraceExceeded = newEvent("shutdown_grace_exceeded", LevelWarn, "C-02",
	"Requests were still running when the shutdown grace period ended; their connections were closed.",
	"grace_seconds")

// StartupFailed is logged when the server or a subcommand stops during startup, with the reason.
var StartupFailed = newEvent("startup_failed", LevelError, "C-02",
	"Startup stopped: the database cannot be reached or fails a check, a migration failed, or a listener cannot "+
		"listen. The error says what to fix.",
	"error")

// ListenersStarted is logged once the server serves, with the address of each listener.
var ListenersStarted = newEvent("listeners_started", LevelInfo, "C-02",
	"The listeners serve and the process is ready; app and ingest are the same address when one port serves both.",
	"app", "ingest", "internal")

// ListenerFailed is logged when a listener stops serving while the process runs; the process then stops.
var ListenerFailed = newEvent("listener_failed", LevelError, "C-02",
	"A listener stopped serving with an error; the process stops so that it is restarted.",
	"listener", "error")

// DatabaseSettingsConflict is logged when a connection is configured both as a URL and as fields.
var DatabaseSettingsConflict = newEvent("database_settings_conflict", LevelWarn, "C-02",
	"A database connection is set both as a URL and as fields: the URL is used and the fields are ignored.",
	"used", "ignored")

// DatabaseConnectionSecurity reports whether a database connection is encrypted, at WARN when it is not.
var DatabaseConnectionSecurity = newEventAt([]Level{LevelInfo, LevelWarn}, "database_connection_security", "C-02",
	"Whether a database connection (main or session) is encrypted, with the sslmode in effect; WARN when it is not "+
		"encrypted, which prefer allows without an error.",
	"connection", "sslmode", "encrypted")

// MigrationsApplied is logged when muster migrate or the start-up migration applied migrations.
var MigrationsApplied = newEvent("migrations_applied", LevelInfo, "C-02",
	"Migrations were applied under the migration lock, from one schema version to another.",
	"from", "to")

// MigrationsCurrent is logged when the schema already has the newest version the binary knows.
var MigrationsCurrent = newEvent("migrations_current", LevelInfo, "C-02",
	"No migration was applied: the schema already has the newest version this binary knows.",
	"version")

// SchemaTooNew is logged when the database schema is newer than the binary knows; the process refuses to run.
var SchemaTooNew = newEvent("schema_too_new", LevelError, "C-02",
	"The database schema is newer than this binary knows, after an upgrade was rolled back: run a release that "+
		"knows the schema version.",
	"database_version", "known_version")

// SchemaTooOld is logged when the server starts on a schema older than the binary needs, without migrating.
var SchemaTooOld = newEvent("schema_too_old", LevelError, "C-02",
	"The database schema is older than this binary needs: run muster migrate or set MUSTER_MIGRATE_ON_START.",
	"database_version", "known_version")

// SchemaDirty is logged when a migration failed halfway and left the schema dirty; the process refuses to run.
var SchemaDirty = newEvent("schema_dirty", LevelError, "C-02",
	"A migration failed halfway and the schema is marked dirty at its version: repair the schema from a backup or "+
		"by hand, then clear the dirty flag in schema_migrations.",
	"database_version")

// Event is a registered log event. Its zero value is not registered, and the logger refuses it.
type Event struct {
	def *eventDef
}

type eventDef struct {
	name  string
	level Level
	// also are the other levels of an event that LogAt writes at a level chosen by the caller.
	also        []Level
	capability  string
	description string
	fields      []string
}

func (d *eventDef) levels() []Level {
	return append([]Level{d.level}, d.also...)
}

// EventInfo describes a registered event for the reference page.
type EventInfo struct {
	Name  string
	Level Level
	// Levels are all the levels of the event, Level first; an event with more than one is logged with LogAt.
	Levels      []Level
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

// newEventAt declares an event whose level the caller chooses among levels, the first being its usual one.
func newEventAt(levels []Level, name, capability, description string, fields ...string) Event {
	e := newEvent(name, levels[0], capability, description, fields...)
	e.def.also = levels[1:]
	return e
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
			Levels:      d.levels(),
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
		levels := map[Level]bool{}
		for _, l := range append([]Level{e.Level}, e.Levels...) {
			if !l.valid() {
				bad("level %d is not INFO, WARN or ERROR", int(l))
			}
		}
		for _, l := range e.Levels {
			if levels[l] {
				bad("level %s is declared twice", l)
			}
			levels[l] = true
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
