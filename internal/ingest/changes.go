// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"slices"

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
// and their public_ids, which its log line carries, in evaluation order. AlertGroups are the #N of the Alert Groups
// that grouping created or changed, and Committed, when set, counts and logs what the changes did once the
// transaction committed.
type Routed struct {
	IDs         []int64
	PublicIDs   []string
	AlertGroups []int64
	Committed   func(ctx context.Context)
}

// committed runs Committed when it is set.
func (r Routed) committed(ctx context.Context) {
	if r.Committed != nil {
		r.Committed(ctx)
	}
}

// Sink takes the Alert changes of a Snapshot inside the Snapshot's transaction, tx, which it may write through; an
// error rolls the Snapshot back. Routing (C-08) attaches here and says which Routes took the newly firing Alerts;
// grouping (C-09) follows it.
type Sink interface {
	AlertChanges(ctx context.Context, tx dbgen.DBTX, changes []AlertChange) (Routed, error)
}

// Chain is the Sink that hands the changes to each of sinks in order, in the same transaction — routing, then
// grouping — and merges what they return: the Routes once each in the order they came, the Alert Groups sorted.
func Chain(sinks ...Sink) Sink {
	return chain(sinks)
}

type chain []Sink

func (c chain) AlertChanges(ctx context.Context, tx dbgen.DBTX, changes []AlertChange) (Routed, error) {
	var out Routed
	var after []func(context.Context)
	for _, s := range c {
		r, err := s.AlertChanges(ctx, tx, changes)
		if err != nil {
			return Routed{}, err
		}
		for i, id := range r.IDs {
			if !slices.Contains(out.IDs, id) {
				out.IDs = append(out.IDs, id)
				out.PublicIDs = append(out.PublicIDs, r.PublicIDs[i])
			}
		}
		out.AlertGroups = append(out.AlertGroups, r.AlertGroups...)
		if r.Committed != nil {
			after = append(after, r.Committed)
		}
	}
	if out.AlertGroups != nil {
		slices.Sort(out.AlertGroups)
		out.AlertGroups = slices.Compact(out.AlertGroups)
	}
	if len(after) > 0 {
		out.Committed = func(ctx context.Context) {
			for _, f := range after {
				f(ctx)
			}
		}
	}
	return out, nil
}
