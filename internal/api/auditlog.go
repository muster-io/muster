// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
)

// auditCursor names the cursors of listAuditLog.
const auditCursor = "audit-log"

// auditKey is the sort key of a listAuditLog cursor.
type auditKey struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

// ListAuditLog is listAuditLog: entries newest first, by cursor, filtered by time range, actor, action and resource
// (C-03.FR-15). Without from and to there is no time limit.
func (s *Server) ListAuditLog(ctx context.Context, req gen.ListAuditLogRequestObject) (gen.ListAuditLogResponseObject,
	error) {
	p := req.Params
	f := audit.Filter{From: p.From, To: p.To, Limit: pageSize(p.Limit)}
	for _, v := range []struct {
		dst *string
		src *string
	}{{&f.Actor, p.Actor}, {&f.Action, p.Action}, {&f.ResourceType, p.ResourceType}, {&f.ResourceID, p.ResourceId}} {
		if v.src != nil {
			*v.dst = *v.src
		}
	}
	var key auditKey
	if ok, err := decodeCursor(p.Cursor, auditCursor, &key); err != nil {
		return nil, err
	} else if ok {
		f.After = &audit.Cursor{At: key.At, ID: key.ID}
	}
	page, err := s.auditLog.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.AuditEntryList{Items: make([]gen.AuditEntry, 0, len(page.Entries))}
	for _, e := range page.Entries {
		out.Items = append(out.Items, auditEntryOf(e))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(auditCursor, auditKey{At: page.Next.At, ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListAuditLog200JSONResponse(out), nil
}

func auditEntryOf(e audit.Listed) gen.AuditEntry {
	out := gen.AuditEntry{
		Id: e.PublicID, At: e.At.UTC(), Action: e.Action, Transport: gen.Transport(e.Transport),
		Actor: gen.AuditActor{Kind: gen.AuditActorKind(e.Actor.Kind), Name: e.Actor.Name},
	}
	setNullable(&out.Actor.Id, e.Actor.PublicID)
	setNullable(&out.TokenId, e.TokenPublicID)
	setNullable(&out.TokenName, e.TokenName)
	setNullable(&out.ResourceType, e.ResourceType)
	setNullable(&out.ResourceId, e.ResourceID)
	setNullable(&out.ResourceName, e.ResourceName)
	diff := make([]gen.AuditDiffEntry, 0, len(e.Diff))
	for _, c := range e.Diff {
		d := gen.AuditDiffEntry{Pointer: c.Pointer, Before: c.Before, After: c.After}
		if c.SecretChanged {
			changed := true
			d.SecretChanged, d.Before, d.After = &changed, nil, nil
		}
		diff = append(diff, d)
	}
	out.Diff = &diff
	details := e.Details
	out.Details = &details
	return out
}

// setNullable sets n to v, or to null when v is empty.
func setNullable[T ~string](n interface {
	Set(T)
	SetNull()
}, v string) {
	if v == "" {
		n.SetNull()
		return
	}
	n.Set(T(v))
}
