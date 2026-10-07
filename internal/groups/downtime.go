// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// RecordDowntime gives every open Alert Group the system entry "Muster was unavailable from … to …" (C-09.FR-18,
// C-02.FR-12), in the transaction tx in which the Leader records the downtime, so that a downtime is recorded once
// with its entries. The entry is no lifecycle event: it reaches neither delivery nor outgoing webhook events.
func (s *Service) RecordDowntime(ctx context.Context, tx dbgen.DBTX, from, to time.Time) error {
	q := s.queries(tx)
	ids, err := q.ListOpenGroupIDs(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the open alert groups: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	publicIDs := make([]string, len(ids))
	for i := range ids {
		publicIDs[i] = publicid.New(publicid.TimelineEntry)
	}
	if err := q.InsertDowntimeEntries(ctx, dbgen.InsertDowntimeEntriesParams{OrgID: s.orgID, PublicIds: publicIDs,
		AlertGroupIds: ids, At: s.clock.Now().UTC(), PeriodFrom: from.UTC(),
		PeriodTo: to.UTC()}); err != nil {
		return fmt.Errorf("record the downtime on the open alert groups: %w", err)
	}
	return nil
}
