// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// The names the Audit log shows for the actors that are not a user or a Service account.
const (
	SystemName    = "Muster"
	BootstrapName = "bootstrap"
)

// Cursor is the sort key of the last entry of a page: the next page starts after it.
type Cursor struct {
	At time.Time
	ID int64
}

// Filter selects entries (C-03.FR-15). From is inclusive and To exclusive; Actor is the public_id of a user or a
// Service account; empty fields do not filter. The list has no default time range: without From and To it starts
// with the newest entry.
type Filter struct {
	From         *time.Time
	To           *time.Time
	Actor        string
	Action       string
	ResourceType string
	ResourceID   string
	After        *Cursor
	Limit        int
}

// ListedActor is the actor of a listed entry with its current name: a deleted user shows as deleted-user-<id>.
type ListedActor struct {
	Kind     ActorKind
	PublicID string
	Name     string
}

// Listed is one entry as the Audit log lists it.
type Listed struct {
	PublicID      string
	At            time.Time
	Actor         ListedActor
	TokenPublicID string
	TokenName     string
	Transport     Transport
	Action        string
	ResourceType  string
	ResourceID    string
	ResourceName  string
	Diff          []Change
	Details       map[string]any
}

// Page is a page of entries, newest first; Next is nil on the last page.
type Page struct {
	Entries []Listed
	Next    *Cursor
}

// ListQueries are the reads of the list.
type ListQueries interface {
	FindUserActor(ctx context.Context, arg dbgen.FindUserActorParams) (int64, error)
	FindServiceAccountActor(ctx context.Context, arg dbgen.FindServiceAccountActorParams) (int64, error)
	ListAuditEntries(ctx context.Context, arg dbgen.ListAuditEntriesParams) ([]dbgen.ListAuditEntriesRow, error)
}

// NewListQueries are the reads of the list over a pool.
func NewListQueries(db dbgen.DBTX) ListQueries {
	return dbgen.New(db)
}

// Reader lists the Audit log of the Organization.
type Reader struct {
	orgID int64
	q     ListQueries
}

// NewReader returns the Reader of the Organization orgID.
func NewReader(orgID int64, q ListQueries) *Reader {
	return &Reader{orgID: orgID, q: q}
}

// List returns a page of at most f.Limit entries, newest first by time and then id, that match f. An actor that is
// neither a user nor a Service account of the Organization matches nothing.
func (r *Reader) List(ctx context.Context, f Filter) (Page, error) {
	p := dbgen.ListAuditEntriesParams{
		OrgID: r.orgID, From: timestamp(f.From), To: timestamp(f.To), Action: text(f.Action),
		ResourceType: text(f.ResourceType), ResourceID: text(normalizeID(f.ResourceID)), PageSize: int32(f.Limit + 1), //nolint:gosec // G115: the limit is at most 500
	}
	if f.Actor != "" {
		found, err := r.actor(ctx, f.Actor, &p)
		if err != nil || !found {
			return Page{Entries: []Listed{}}, err
		}
	}
	if f.After != nil {
		p.BeforeAt = pgtype.Timestamptz{Time: f.After.At, Valid: true}
		p.BeforeID = pgtype.Int8{Int64: f.After.ID, Valid: true}
	}
	rows, err := r.q.ListAuditEntries(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the audit log: %w", err)
	}
	page := Page{Entries: make([]Listed, 0, min(len(rows), f.Limit))}
	for i, row := range rows {
		if i == f.Limit {
			last := rows[i-1]
			page.Next = &Cursor{At: last.At, ID: last.ID}
			break
		}
		e, err := listed(row)
		if err != nil {
			return Page{}, err
		}
		page.Entries = append(page.Entries, e)
	}
	return page, nil
}

// actor sets the actor filter of p from a public_id; found is false when it names no user or Service account.
func (r *Reader) actor(ctx context.Context, id string, p *dbgen.ListAuditEntriesParams) (bool, error) {
	if norm, err := publicid.Parse(publicid.User, id); err == nil {
		uid, err := r.q.FindUserActor(ctx, dbgen.FindUserActorParams{OrgID: r.orgID, PublicID: norm})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("find the actor: %w", err)
		}
		p.ActorUserID = pgtype.Int8{Int64: uid, Valid: true}
		return true, nil
	}
	norm, err := publicid.Parse(publicid.ServiceAccount, id)
	if err != nil {
		return false, nil //nolint:nilerr // an id of no user and no Service account matches nothing; it is no error
	}
	sid, err := r.q.FindServiceAccountActor(ctx, dbgen.FindServiceAccountActorParams{OrgID: r.orgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find the actor: %w", err)
	}
	p.ActorServiceAccountID = pgtype.Int8{Int64: sid, Valid: true}
	return true, nil
}

func listed(row dbgen.ListAuditEntriesRow) (Listed, error) {
	e := Listed{
		PublicID: row.PublicID, At: row.At, Transport: Transport(row.Transport), Action: row.Action,
		TokenPublicID: row.TokenPublicID.String, TokenName: row.TokenName.String, ResourceType: row.ResourceType.String,
		ResourceID: row.ResourcePublicID.String, ResourceName: row.ResourceName.String,
		Actor: ListedActor{Kind: ActorKind(row.ActorKind)},
	}
	if row.ResourceUserName.Valid {
		e.ResourceName = row.ResourceUserName.String
	}
	erased := row.ResourceUserStatus.String == deletedStatus
	switch e.Actor.Kind {
	case ActorUser:
		e.Actor.PublicID, e.Actor.Name = row.ActorUserPublicID.String, row.ActorUserName.String
	case ActorServiceAccount:
		e.Actor.PublicID, e.Actor.Name = row.ActorServiceAccountPublicID.String, row.ActorServiceAccountName.String
	case ActorCLI:
		e.Actor.Name = row.ActorName.String
	case ActorSystem:
		e.Actor.Name = SystemName
	case ActorBootstrap:
		e.Actor.Name = BootstrapName
	}
	if err := json.Unmarshal(row.Diff, &e.Diff); err != nil {
		return Listed{}, fmt.Errorf("read the diff of %s: %w", row.PublicID, err)
	}
	if err := json.Unmarshal(row.Details, &e.Details); err != nil {
		return Listed{}, fmt.Errorf("read the details of %s: %w", row.PublicID, err)
	}
	if e.Diff == nil {
		e.Diff = []Change{}
	}
	if erased {
		e.Diff = maskErased(e.Diff, e.ResourceName)
	}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	return e, nil
}

// Erased replaces, when the Audit log is read, a value that deleting a user erased (C-03.FR-13).
const Erased = "[erased]"

// deletedStatus is the status of a deleted user.
const deletedStatus = "deleted"

// erasedPointers are the fields of a user that deleting it erases: the email, and the name and login it replaces
// with its pseudonym.
var erasedPointers = map[string]bool{"/name": true, "/login": true, "/email": true}

// maskErased returns diff with every value of the erased fields replaced by Erased, before and after, except an absent
// value and the pseudonym itself, which deletion wrote. The rows of the table are never changed (append-only).
func maskErased(diff []Change, pseudonym string) []Change {
	out := make([]Change, len(diff))
	for i, c := range diff {
		if erasedPointers[c.Pointer] {
			c.Before, c.After = erase(c.Before, pseudonym), erase(c.After, pseudonym)
		}
		out[i] = c
	}
	return out
}

func erase(v any, pseudonym string) any {
	if v == nil || v == pseudonym {
		return v
	}
	return Erased
}

// normalizeID reads a public_id in any case, with O as 0 and I and L as 1, when it is one; anything else is kept and
// then matches nothing.
func normalizeID(id string) string {
	if len(id) < 2 {
		return id
	}
	if norm, err := publicid.Parse(publicid.Prefix(strings.ToUpper(id[:2])), id); err == nil {
		return norm
	}
	return id
}

func timestamp(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
