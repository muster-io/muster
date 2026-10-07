// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// Related is an Alert Group of the same Route and Group key values as another one (RelatedAlertGroup).
type Related struct {
	PublicID  string
	Number    int64
	Status    Status
	StartedAt time.Time
	// Duration runs to the resolution; nil while it is open.
	Duration   *time.Duration
	Resolution *Resolution
}

// RelatedPage is a page of related Alert Groups; Next is nil on the last page.
type RelatedPage struct {
	Groups []Related
	Next   *ListPosition
}

// Related lists the other Alert Groups of the Route of the Alert Group publicID with the same Group key values,
// newest first, after the position when given (C-09.FR-20): number, status, start, duration and who resolved them.
func (s *Service) Related(ctx context.Context, publicID string, after *ListPosition, limit int) (RelatedPage,
	error) {
	id, err := publicid.Parse(publicid.AlertGroup, publicID)
	if err != nil {
		return RelatedPage{}, ErrNotFound
	}
	key, err := s.store.GetGroupKey(ctx, dbgen.GetGroupKeyParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return RelatedPage{}, ErrNotFound
	}
	if err != nil {
		return RelatedPage{}, fmt.Errorf("read alert group %s: %w", id, err)
	}
	limit = max(limit, 1)
	p := dbgen.ListRelatedGroupsParams{OrgID: s.orgID, RouteID: key.RouteID, GroupKeySha256: key.GroupKeySha256,
		ID: key.ID, Lim: int32(limit + 1)} //nolint:gosec // G115: limit is at most the page size
	if after != nil {
		p.AfterAt = pgtype.Timestamptz{Time: after.At, Valid: true}
		p.AfterID = pgtype.Int8{Int64: after.ID, Valid: true}
	}
	rows, err := s.store.ListRelatedGroups(ctx, p)
	if err != nil {
		return RelatedPage{}, fmt.Errorf("list the alert groups related to %s: %w", id, err)
	}
	var out RelatedPage
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		out.Next = &ListPosition{At: last.CreatedAt.UTC(), ID: last.ID}
	}
	userIDs, accountIDs := make([]int64, len(rows)), make([]int64, len(rows))
	for i, r := range rows {
		userIDs[i], accountIDs[i] = r.ResolvedByUserID.Int64, r.ResolvedByServiceAccountID.Int64
	}
	actors, err := s.refs(ctx, userIDs, accountIDs)
	if err != nil {
		return RelatedPage{}, err
	}
	out.Groups = make([]Related, 0, len(rows))
	for _, r := range rows {
		g := Related{PublicID: r.PublicID, Number: r.Number, Status: Status(r.Status), StartedAt: r.CreatedAt.UTC()}
		if r.ResolvedAt.Valid {
			d := max(r.ResolvedAt.Time.Sub(r.CreatedAt), 0)
			g.Duration = &d
		}
		if r.ResolvedByKind.Valid {
			g.Resolution = &Resolution{By: r.ResolvedByKind.String, Reason: textOf(r.ResolveReasonText),
				ReasonCode: textOf(r.ResolveReason),
				Actor:      actors.actor(r.ResolvedByUserID, r.ResolvedByServiceAccountID)}
		}
		out.Groups = append(out.Groups, g)
	}
	return out, nil
}
