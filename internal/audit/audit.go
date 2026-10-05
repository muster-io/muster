// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package audit is the writer of the Audit log (C-03.FR-14): it appends an entry to audit_log with the actor, the
// token used, the Transport, a stable action `<resource>.<verb>`, the resource, a before/after diff and details, and
// copies every entry to stdout as the log event audit_entry. The table is append-only; this package only inserts.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/publicid"
)

// ActorKind is who did what an entry records.
type ActorKind string

const (
	ActorUser           ActorKind = "user"
	ActorServiceAccount ActorKind = "service_account"
	ActorSystem         ActorKind = "system"
	ActorBootstrap      ActorKind = "bootstrap"
	ActorCLI            ActorKind = "cli"
)

// Transport is the way the action reached Muster.
type Transport string

const (
	TransportUI         Transport = "ui"
	TransportAPI        Transport = "api"
	TransportMattermost Transport = "mattermost"
	TransportTelegram   Transport = "telegram"
	TransportCLI        Transport = "cli"
	TransportSystem     Transport = "system"
)

// The actions of C-03; each capability adds its own.
const (
	ActionUserCreated     = "user.created"
	ActionSignedIn        = "session.signed_in"
	ActionSignInFailed    = "session.sign_in_failed"
	ActionSignedOut       = "session.signed_out"
	ActionSessionsEnded   = "session.ended_all"
	ActionProfileUpdated  = "user.profile_updated"
	ActionPasswordChanged = "user.password_changed"
)

// The resource types of C-03.
const (
	ResourceUser    = "user"
	ResourceSession = "session"
)

var actionRe = regexp.MustCompile(`^[a-z_]+(\.[a-z_]+)+$`)

// Actor is who did it: a user or a Service account by id and public_id, Muster itself, the bootstrap step, or a CLI
// command with its --actor name. TokenID and TokenName name the Personal access token or Service account token used.
type Actor struct {
	Kind ActorKind
	// ID and PublicID name the user or the Service account.
	ID        int64
	PublicID  string
	Name      string
	TokenID   int64
	TokenName string
}

// User is the actor of a user.
func User(id int64, publicID string) Actor {
	return Actor{Kind: ActorUser, ID: id, PublicID: publicID}
}

// System is Muster itself.
var System = Actor{Kind: ActorSystem}

// Bootstrap is the start-up step that creates the bootstrap Admin.
var Bootstrap = Actor{Kind: ActorBootstrap}

// Resource is what an entry is about.
type Resource struct {
	Type     string
	PublicID string
	Name     string
}

// Change is one configured value that changed; a Secret shows only that it changed, never its value.
type Change struct {
	Pointer       string `json:"pointer"`
	Before        any    `json:"before,omitempty"`
	After         any    `json:"after,omitempty"`
	SecretChanged bool   `json:"secret_changed,omitempty"`
}

// Changed returns the change at pointer when before and after differ, and nothing otherwise.
func Changed[T comparable](pointer string, before, after T) []Change {
	if before == after {
		return nil
	}
	return []Change{{Pointer: pointer, Before: before, After: after}}
}

// Entry is one Audit log entry. Details never carry a Secret.
type Entry struct {
	OrgID         int64
	Actor         Actor
	Transport     Transport
	Action        string
	Resource      Resource
	Diff          []Change
	Details       map[string]any
	SourceAddress netip.Addr
}

// Store inserts entries; it runs over a pool or inside the transaction of the change it records.
type Store interface {
	InsertAuditEntry(ctx context.Context, arg dbgen.InsertAuditEntryParams) error
}

// NewStore is the Store over a pool, a connection or a transaction.
func NewStore(db dbgen.DBTX) Store {
	return dbgen.New(db)
}

// Writer records entries on the business clock and copies each to the log.
type Writer struct {
	log   *logging.Logger
	clock clock.Clock
}

// NewWriter returns a Writer that dates entries with the business clock.
func NewWriter(log *logging.Logger, business clock.Clock) *Writer {
	return &Writer{log: log, clock: business}
}

// Record appends e through s and logs it as audit_entry; an entry that breaks the rules of the table is an error
// before anything is written.
func (w *Writer) Record(ctx context.Context, s Store, e Entry) error {
	if err := e.check(); err != nil {
		return err
	}
	diff := e.Diff
	if diff == nil {
		diff = []Change{}
	}
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	diffJSON, err := json.Marshal(diff)
	if err != nil {
		return fmt.Errorf("audit %s: encode the diff: %w", e.Action, err)
	}
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("audit %s: encode the details: %w", e.Action, err)
	}
	id := publicid.New(publicid.AuditLogEntry)
	at := w.clock.Now().UTC()
	p := dbgen.InsertAuditEntryParams{
		OrgID: e.OrgID, PublicID: id, At: at, ActorKind: string(e.Actor.Kind), Transport: string(e.Transport),
		Action: e.Action, ResourceType: text(e.Resource.Type), ResourcePublicID: text(e.Resource.PublicID),
		ResourceName: text(e.Resource.Name), Diff: diffJSON, Details: detailsJSON, TokenName: text(e.Actor.TokenName),
		ActorName: text(e.Actor.Name), ApiTokenID: bigint(e.Actor.TokenID),
	}
	switch e.Actor.Kind {
	case ActorUser:
		p.ActorUserID = bigint(e.Actor.ID)
	case ActorServiceAccount:
		p.ActorServiceAccountID = bigint(e.Actor.ID)
	case ActorSystem, ActorBootstrap, ActorCLI:
	}
	if e.SourceAddress.IsValid() {
		addr := e.SourceAddress
		p.SourceAddress = &addr
	}
	if err := s.InsertAuditEntry(ctx, p); err != nil {
		return fmt.Errorf("audit %s: %w", e.Action, err)
	}
	w.log.Log(ctx, logging.AuditEntry, e.fields(id, at, diff, details)...)
	return nil
}

func (e Entry) check() error {
	if !actionRe.MatchString(e.Action) {
		return fmt.Errorf("audit: the action %q is not <resource>.<verb>", e.Action)
	}
	a := e.Actor
	switch a.Kind {
	case ActorUser, ActorServiceAccount:
		if a.ID == 0 {
			return fmt.Errorf("audit %s: a %s actor needs its id", e.Action, a.Kind)
		}
	case ActorCLI:
		if a.Name == "" || e.Transport != TransportCLI {
			return fmt.Errorf("audit %s: a cli actor needs the --actor name and the cli transport", e.Action)
		}
	case ActorSystem, ActorBootstrap:
		if a.ID != 0 {
			return fmt.Errorf("audit %s: a %s actor names no user or service account", e.Action, a.Kind)
		}
	default:
		return fmt.Errorf("audit %s: unknown actor kind %q", e.Action, a.Kind)
	}
	switch e.Transport {
	case TransportUI, TransportAPI, TransportMattermost, TransportTelegram, TransportCLI, TransportSystem:
	default:
		return fmt.Errorf("audit %s: unknown transport %q", e.Action, e.Transport)
	}
	return nil
}

// fields are the fields of the audit_entry line; empty ones are left out.
func (e Entry) fields(id string, at time.Time, diff []Change, details map[string]any) []logging.Field {
	f := []logging.Field{
		logging.F("entry", id), logging.F("at", at.Format(time.RFC3339Nano)),
		logging.F("actor_kind", string(e.Actor.Kind)), logging.F("transport", string(e.Transport)),
		logging.F("action", e.Action),
	}
	opt := func(key, value string) {
		if value != "" {
			f = append(f, logging.F(key, value))
		}
	}
	opt("actor", e.Actor.PublicID)
	opt("actor_name", e.Actor.Name)
	opt("token_name", e.Actor.TokenName)
	opt("resource_type", e.Resource.Type)
	opt("resource", e.Resource.PublicID)
	opt("resource_name", e.Resource.Name)
	if e.SourceAddress.IsValid() {
		f = append(f, logging.F("source_address", e.SourceAddress.String()))
	}
	if len(diff) > 0 {
		f = append(f, logging.F("diff", diff))
	}
	if len(details) > 0 {
		f = append(f, logging.F("details", details))
	}
	return f
}

func text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func bigint(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: v != 0}
}
