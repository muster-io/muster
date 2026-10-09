// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// SystemTemplateValueMissing is the system entry of a value an extraction rule of an outgoing webhook did not find in
// the response of its "create" or "open thread" (C-15.FR-4); delivery records it.
const SystemTemplateValueMissing SystemEvent = "template_value_missing"

// SystemEntry is a system entry another package records about an Alert Group of the Organization OrgID: what
// happened, when, and its detail.
type SystemEntry struct {
	OrgID        int64
	AlertGroupID int64
	At           time.Time
	System       SystemEvent
	Detail       string
}

// RecordSystemEntry writes the system entry e to the Timeline through db, the transaction of the caller, with Muster
// as the actor: the Timeline has one writer, this package, and delivery records what it learns while delivering
// through here (ADR-0016). Only template_value_missing is recorded this way.
func RecordSystemEntry(ctx context.Context, db DBTX, e SystemEntry) error {
	if e.System != SystemTemplateValueMissing {
		return fmt.Errorf("the system entry %s is not recorded from outside the dispatcher", e.System)
	}
	err := dbgen.New(db).InsertTimelineEntry(ctx, dbgen.InsertTimelineEntryParams{OrgID: e.OrgID,
		PublicID: publicid.New(publicid.TimelineEntry), AlertGroupID: e.AlertGroupID, At: e.At.UTC(),
		Kind: string(KindSystem), SystemEvent: pgtype.Text{String: string(e.System), Valid: true}, Mentions: []string{},
		ActorKind: string(audit.ActorSystem), Transport: string(audit.TransportSystem), Detail: nonEmpty(e.Detail)})
	if err != nil {
		return fmt.Errorf("record %s on alert group %d: %w", e.System, e.AlertGroupID, err)
	}
	return nil
}
