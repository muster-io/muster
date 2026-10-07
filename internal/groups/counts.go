// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"fmt"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

// Counts are the Alert Groups matching the filters per status tab (AlertGroupCounts); All is their sum, the Open tab
// the sum of the firing, acknowledged and snoozed ones.
type Counts struct {
	Firing       int64
	Acknowledged int64
	Snoozed      int64
	Resolved     int64
	All          int64
}

// Counts counts the Alert Groups matching f per status (C-09.FR-13): the filters of the list, its status aside. With
// label Matchers that Go applies, the database counts per status and common labels, and Go adds up the matching ones.
func (s *Service) Counts(ctx context.Context, f Filter) (Counts, error) {
	f.Statuses = nil
	q, err := s.resolve(ctx, f, s.clock.Now().UTC())
	if err != nil {
		return Counts{}, err
	}
	rows, err := s.store.CustomPlans().CountGroups(ctx, dbgen.CountGroupsParams{OrgID: s.orgID,
		WithLabels: len(q.rowwise) > 0, Number: q.number, RangeTo: q.to, RangeFrom: q.from, RouteIds: q.routes,
		IntegrationIds: q.integrations, Severities: q.severities, Urgent: q.urgent, ResolvedBy: q.resolvedBy,
		ResolveReason: q.reason, Reopened: q.reopened, Contains: q.contains, Pattern: q.pattern, OwnerSet: q.ownerSet,
		OwnerID: q.owner, SnoozedNoEnd: q.snoozedNoEnd})
	if err != nil {
		return Counts{}, fmt.Errorf("count the alert groups: %w", err)
	}
	var out Counts
	for _, r := range rows {
		if ok, err := matchesLabels(q.rowwise, r.CommonLabels); err != nil {
			return Counts{}, err
		} else if !ok {
			continue
		}
		switch Status(r.Status) {
		case StatusFiring:
			out.Firing += r.Count
		case StatusAcknowledged:
			out.Acknowledged += r.Count
		case StatusSnoozed:
			out.Snoozed += r.Count
		case StatusResolved:
			out.Resolved += r.Count
		}
		out.All += r.Count
	}
	return out, nil
}

// OpenCounts counts, for each Integration of the public_ids, the open Alert Groups with an Alert from it
// (C-09.FR-21): those that deleting it would resolve. An Integration without any is left out.
func (s *Service) OpenCounts(ctx context.Context, integrations []string) (map[string]int64, error) {
	out := make(map[string]int64, len(integrations))
	if len(integrations) == 0 {
		return out, nil
	}
	rows, err := s.store.CountOpenGroupsByIntegration(ctx, dbgen.CountOpenGroupsByIntegrationParams{OrgID: s.orgID,
		PublicIds: integrations})
	if err != nil {
		return nil, fmt.Errorf("count the open alert groups of the integrations: %w", err)
	}
	for _, r := range rows {
		out[r.PublicID] = r.Count
	}
	return out, nil
}
