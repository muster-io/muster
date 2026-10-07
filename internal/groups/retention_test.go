// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
)

// TestReadsAfterDetailsRemoved is C-09.AC-18 on the reads: once retention.alert_details has passed since the
// resolution, getAlertGroup carries details_removed with the notice and the period, the Alerts are gone and the
// Timeline keeps only the Notes — whether or not the Leader deleted the rows yet.
func TestReadsAfterDetailsRemoved(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	a := h.alert(2, "warning", map[string]string{"alertname": "DiskFull", "cluster": "a"})
	h.changes(t, ingest.ChangeFired, a)
	h.resolve(t, a)
	g := h.groupOf(t, a)
	h.db.notes = []dbgen.ListTimelineNotesRow{{ID: 1, PublicID: "NEAAAAAAAAAAAA", CreatedAt: t0, Body: "checked",
		ActorKind: "user", ActorUserID: pgtype.Int8{Int64: 9, Valid: true}, Transport: "ui"}}
	h.db.users[9] = dbgen.ListUserRefsRow{ID: 9, PublicID: "SRAAAAAAAAAAAA", Name: "Ann", Login: "ann",
		Status: "active"}
	v, err := h.svc.Get(ctx, g.PublicID)
	if err != nil || v.DetailsRemoved || len(v.Notices) != 0 {
		t.Fatalf("before = %+v, %v", v, err)
	}
	if page, _ := h.svc.Timeline(ctx, g.PublicID, TimelineFilter{Limit: 50}); len(page.Entries) < 3 {
		t.Fatalf("timeline before = %d entries", len(page.Entries))
	}
	h.clock.Advance(90*24*time.Hour + time.Second)
	v, err = h.svc.Get(ctx, g.PublicID)
	if err != nil || !v.DetailsRemoved || len(v.Notices) != 1 || v.Notices[0].Kind != NoticeDetailsRemoved ||
		*v.Notices[0].RetentionDays != 90 || v.Title != "DiskFull" {
		t.Errorf("after = %+v, %v", v, err)
	}
	alerts, err := h.svc.Alerts(ctx, g.PublicID, AlertFilter{Limit: 50})
	if err != nil || len(alerts.Alerts) != 0 || alerts.Alerts == nil || alerts.Next != nil {
		t.Errorf("alerts = %+v, %v", alerts, err)
	}
	page, err := h.svc.Timeline(ctx, g.PublicID, TimelineFilter{Limit: 50})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Kind != KindNotes ||
		page.Entries[0].Note.Author.Name != "Ann" {
		t.Errorf("timeline = %+v, %v", page, err)
	}
	if page, _ := h.svc.Timeline(ctx, g.PublicID, TimelineFilter{Kinds: []Kind{KindStatus}, Limit: 50}); len(page.Entries) != 0 || page.Entries == nil {
		t.Errorf("status entries = %+v", page)
	}
	if page, _ := h.svc.Timeline(ctx, g.PublicID, TimelineFilter{Kinds: []Kind{KindStatus, KindNotes},
		Limit: 50}); len(page.Entries) != 1 {
		t.Errorf("status and notes = %+v", page)
	}
	if h.db.calls["ListTimelineDesc"] != 1 || h.db.calls["ListTimelineDeliveryEvents"] != 1 {
		t.Errorf("the details were read: %v", h.db.calls)
	}
}

// member adds an ended membership of an Alert Group.
func (h *harness) member(group int64, state string, ended time.Time, movedTo int64) {
	h.db.members = append(h.db.members, &fakeMember{id: h.db.id(), group: group, alert: h.db.id(), episode: 1,
		state: state, ended: &ended, movedTo: movedTo})
}

// TestPurge is the retention of C-09.FR-16 (C-09.AC-18): Alerts inside Alert Groups that ended
// retention.alert_details ago go in batches, then summary rows resolved retention.alert_group_summaries ago, the
// summary of a person-resolved Alert Group that a later one fires again after included; a summary that an Alert of
// another Alert Group still names as where it moved waits; running it twice deletes nothing more.
func TestPurge(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	now := t0
	old := now.Add(-91 * 24 * time.Hour)
	for range RetentionBatch + 10 {
		h.member(1, "resolved", old, 0)
	}
	h.member(1, "resolved", now.Add(-89*24*time.Hour), 0)
	h.member(1, "firing", old, 0)
	ancient := now.Add(-731 * 24 * time.Hour)
	manual := h.summary(1, 2, ancient.Add(-time.Hour), ancient, "Manual", nil)
	manual.ResolvedByKind, manual.ResolveReason = pgtype.Text{String: ResolvedByUser, Valid: true}, pgtype.Text{}
	later := h.summary(2, 2, ancient, ancient.Add(time.Minute), "Later", nil)
	later.FiringAgainAfterID = pgtype.Int8{Int64: manual.ID, Valid: true}
	target := h.summary(3, 2, ancient, ancient, "Target", nil)
	kept := h.summary(4, 2, now.Add(-24*time.Hour), now.Add(-time.Hour), "Kept", nil)
	open := h.summary(5, 2, ancient, time.Time{}, "Open", nil)
	holder := h.summary(6, 2, now, time.Time{}, "Holder", nil)
	h.member(holder.ID, "moved", now.Add(-time.Hour), target.ID)

	got, err := Purge(ctx, h.db, orgID, now)
	if err != nil || got != (Purged{Details: RetentionBatch + 10, Summaries: 2}) {
		t.Fatalf("purged %+v, %v", got, err)
	}
	if h.db.groups[manual.ID] != nil || h.db.groups[later.ID] != nil || h.db.groups[target.ID] == nil ||
		h.db.groups[open.ID] == nil || h.db.groups[kept.ID] == nil || h.db.groups[holder.ID] == nil {
		t.Errorf("groups left %v", h.db.groups)
	}
	if len(h.db.members) != 3 {
		t.Errorf("members left %d", len(h.db.members))
	}
	if got, err := Purge(ctx, h.db, orgID, now); err != nil || got != (Purged{}) {
		t.Errorf("a second run = %+v, %v", got, err)
	}
	// The later Alert Group keeps its row without the reference when only the earlier one goes.
	h.summary(7, 2, ancient.Add(-time.Hour), ancient, "Manual2", nil)
	ref := h.summary(8, 2, now.Add(-time.Hour), time.Time{}, "Again", nil)
	ref.FiringAgainAfterID = pgtype.Int8{Int64: 1007, Valid: true}
	if got, err := Purge(ctx, h.db, orgID, now); err != nil || got.Summaries != 1 || ref.FiringAgainAfterID.Valid {
		t.Errorf("purged %+v, %v, reference %v", got, err, ref.FiringAgainAfterID)
	}
	h.summary(9, 2, ancient.Add(-time.Hour), ancient, "Task", nil)
	if d, n, err := RetentionTask(h.db)(ctx, orgID, now); err != nil || d != 0 || n != 1 {
		t.Errorf("the task = %d, %d, %v", d, n, err)
	}
	for _, q := range []string{"GetRetentionPeriods", "DeleteExpiredMemberships", "DeleteExpiredGroups"} {
		h.db.fail[q] = errBoom
		if _, err := Purge(ctx, h.db, orgID, now); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
		delete(h.db.fail, q)
	}
}
