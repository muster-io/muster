// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"

	"github.com/muster-io/muster/internal/ingest/dbgen"
)

// ChangeKind is the kind of an Alert change (C-06.FR-3).
type ChangeKind string

// The Alert changes processing emits.
const (
	// ChangeFired is a new firing: a fingerprint not seen before, or one that fires again after it resolved.
	ChangeFired ChangeKind = "fired"
	// ChangeResolved is a resolution, with its reason and text.
	ChangeResolved ChangeKind = "resolved"
	// ChangeContinued is a Continuation: a new startsAt without a resolve in between (C-06.FR-11).
	ChangeContinued ChangeKind = "continued"
	// ChangeAnnotations is a change in the annotations of a firing Alert (C-06.FR-12).
	ChangeAnnotations ChangeKind = "annotations_changed"
)

// AlertChange is one change of an Alert, made by the Stored Snapshot StoredSnapshotID.
type AlertChange struct {
	Kind             ChangeKind
	AlertID          int64
	Fingerprint      string
	Episode          int64
	StoredSnapshotID int64
	// Reason and ReasonText are set on a ChangeResolved.
	Reason     string
	ReasonText string
}

// Routed names the Routes that took Alerts of the changes: their ids, which the Stored Snapshot keeps in route_ids,
// and their public_ids, which its log line carries, in evaluation order.
type Routed struct {
	IDs       []int64
	PublicIDs []string
}

// Sink takes the Alert changes of a Snapshot inside the Snapshot's transaction, tx, which it may write through; an
// error rolls the Snapshot back. Routing (C-08) attaches here and says which Routes took the newly firing Alerts;
// grouping (C-09) follows it.
type Sink interface {
	AlertChanges(ctx context.Context, tx dbgen.DBTX, changes []AlertChange) (Routed, error)
}
