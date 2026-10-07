// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
)

func (f *fakeDB) ListOwnedGroups(_ context.Context, arg dbgen.ListOwnedGroupsParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListOwnedGroups"); err != nil {
		return nil, err
	}
	var out []int64
	for _, g := range f.sortedGroups() {
		if (g.Status == "acknowledged" && g.OwnerUserID == i8(arg.UserID)) || (g.Status == "resolved" &&
			g.PriorStatus.String == "acknowledged" && g.PriorOwnerUserID == i8(arg.UserID)) {
			out = append(out, g.ID)
		}
	}
	return out, nil
}

// TestReleaseOwner is C-03.FR-13, C-03.AC-27 and C-09.FR-22: releasing a disabled User makes each Alert Group they
// acknowledged firing without an Owner, with a Loud unacknowledged by the system — the reason, the user as the
// previous Owner, no Mentions — and no Audit log entry; a system-resolved one they owned reopens into firing; the
// Alert Groups of others are untouched; it locks the counter row, then the Alert Groups in id order, and counts and
// logs the changes once committed. Releasing again changes nothing.
func TestReleaseOwner(t *testing.T) {
	h := newHarness(t)
	h.people()
	g1, g2, g3, g4, g5 := h.firing(t, "a"), h.firing(t, "b"), h.firing(t, "c"), h.firing(t, "d"), h.firing(t, "e")
	for _, g := range []*dbgen.LockGroupsRow{g1, g2, g4} {
		if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.Acknowledge(t.Context(), alice, g3.PublicID); err != nil {
		t.Fatal(err)
	}
	// g4 resolves by the system while Bob owns it, inside its Reopen window.
	d := h.db.members[slices.IndexFunc(h.db.members, func(m *fakeMember) bool { return m.group == g4.ID })].alert
	h.resolve(t, d)
	if g4.Status != "resolved" || g4.PriorOwnerUserID != i8(bobID) {
		t.Fatalf("g4 = %+v", g4)
	}
	h.clock.Advance(time.Minute)
	audits, counters := len(h.db.audit), h.db.calls["LockCounter"]
	after, err := h.svc.ReleaseOwner(t.Context(), nil, bobID, ReasonOwnerDisabled)
	if err != nil || after == nil {
		t.Fatalf("release = %v", err)
	}
	for _, g := range []*dbgen.LockGroupsRow{g1, g2} {
		e := h.last(t, g)
		if g.Status != "firing" || g.OwnerUserID.Valid || g.AcknowledgedAt.Valid {
			t.Errorf("#%d = %s owned by %v", g.Number, g.Status, g.OwnerUserID)
		}
		if e.Event.String != "unacknowledged" || e.Loudness.String != "loud" || len(e.Mentions) != 0 ||
			e.Reason != txt(ReasonOwnerDisabled) || e.PreviousOwnerUserID != i8(bobID) || e.ActorKind != "system" ||
			e.Transport != "system" || e.FromStatus.String != "acknowledged" || e.ToStatus.String != "firing" ||
			!e.At.Equal(h.clock.Now()) {
			t.Errorf("#%d entry %+v", g.Number, e.InsertTimelineEntryParams)
		}
	}
	if g3.Status != "acknowledged" || g3.OwnerUserID != i8(owner) || g5.Status != "firing" {
		t.Errorf("others changed: %s %v, %s", g3.Status, g3.OwnerUserID, g5.Status)
	}
	if g4.Status != "resolved" || g4.PriorStatus.String != "firing" || g4.PriorOwnerUserID.Valid ||
		h.last(t, g4).Event.String != "resolved" {
		t.Errorf("g4 = %s, prior %v %v", g4.Status, g4.PriorStatus, g4.PriorOwnerUserID)
	}
	if len(h.db.audit) != audits || h.db.calls["LockCounter"] != counters+1 {
		t.Errorf("%d audit entries, %d counter locks", len(h.db.audit)-audits, h.db.calls["LockCounter"]-counters)
	}
	if last := h.db.locked[len(h.db.locked)-1]; !slices.Equal(last, []int64{g1.ID, g2.ID, g4.ID}) {
		t.Errorf("locked %v", last)
	}
	if strings.Contains(h.log.String(), `"reason":"owner_disabled"`) {
		t.Error("logged before the commit")
	}
	after(t.Context())
	if strings.Count(h.log.String(), `"from":"acknowledged","to":"firing","reason":"owner_disabled",`+
		`"transport":"system"`) != 2 {
		t.Errorf("log %s", h.log)
	}

	// The released Alert Group that the system resolved reopens into firing, with the Loud reopened of a Reopen into
	// firing.
	h.clock.Advance(time.Minute)
	h.refire(t, d)
	if e := h.last(t, g4); g4.Status != "firing" || g4.OwnerUserID.Valid || e.Event.String != "reopened" ||
		e.Loudness.String != "loud" || !slices.Equal(e.Mentions, []string{"reopen"}) {
		t.Errorf("g4 reopened = %s, %+v", g4.Status, e.InsertTimelineEntryParams)
	}

	// Nothing left to release: no lock is taken.
	counters = h.db.calls["LockCounter"]
	if after, err := h.svc.ReleaseOwner(t.Context(), nil, bobID, ReasonOwnerDisabled); err != nil || after != nil ||
		h.db.calls["LockCounter"] != counters {
		t.Errorf("second release = %v", err)
	}
}

// TestReleaseDeletedOwner: a deleted Owner is released with owner_deleted, and an Alert Group that changed Owner
// between the lookup and the lock is left alone.
func TestReleaseDeletedOwner(t *testing.T) {
	h := newHarness(t)
	h.people()
	g, o := h.firing(t, "a"), h.firing(t, "b")
	for _, x := range []*dbgen.LockGroupsRow{g, o} {
		if _, err := h.svc.Acknowledge(t.Context(), bob, x.PublicID); err != nil {
			t.Fatal(err)
		}
	}
	h.db.before["LockGroups"] = func() { o.OwnerUserID = i8(owner) }
	entries := len(h.entriesOf(o))
	if _, err := h.svc.ReleaseOwner(t.Context(), nil, bobID, ReasonOwnerDeleted); err != nil {
		t.Fatal(err)
	}
	if e := h.last(t, g); g.Status != "firing" || e.Reason != txt(ReasonOwnerDeleted) {
		t.Errorf("released = %s %+v", g.Status, e.InsertTimelineEntryParams)
	}
	if o.Status != "acknowledged" || len(h.entriesOf(o)) != entries {
		t.Errorf("taken over meanwhile = %s, %v", o.Status, h.events(o))
	}
	if _, err := h.svc.ReleaseOwner(t.Context(), nil, bobID, "owner_bored"); err == nil {
		t.Error("an unknown reason was accepted")
	}
}

// TestReleaseFailures: a failing query fails the release, which the user administration then rolls back.
func TestReleaseFailures(t *testing.T) {
	for _, q := range []string{"ListOwnedGroups", "EnsureCounter", "LockCounter", "LockGroups", "InsertTimelineEntry",
		"SaveGroup"} {
		t.Run(q, func(t *testing.T) {
			h := newHarness(t)
			h.people()
			a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
			h.changes(t, ingest.ChangeFired, a)
			if _, err := h.svc.Acknowledge(t.Context(), bob, h.groupOf(t, a).PublicID); err != nil {
				t.Fatal(err)
			}
			h.db.fail[q] = errBoom
			if _, err := h.svc.ReleaseOwner(t.Context(), nil, bobID, ReasonOwnerDisabled); !errors.Is(err, errBoom) {
				t.Errorf("%s failing = %v", q, err)
			}
		})
	}
}

func (f *fakeDB) LockActiveUser(_ context.Context, arg dbgen.LockActiveUserParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockActiveUser"); err != nil {
		return nil, err
	}
	if u, ok := f.users[arg.ID]; ok && u.Status == "active" {
		return []int64{arg.ID}, nil
	}
	return nil, nil
}

// TestAcknowledgeByInactiveUser: an Acknowledge by a User disabled or deleted after it was authenticated is refused
// under the lock of the User's row, so it never leaves an Owner that the release missed; a failing lock fails it.
func TestAcknowledgeByInactiveUser(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	b := h.db.users[bobID]
	b.Status = "disabled"
	h.db.users[bobID] = b
	_, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID)
	if f, ok := errors.AsType[*ForbiddenError](err); !ok || f.Permission != PermissionAcknowledge || g.Status != "firing" {
		t.Errorf("acknowledge by a disabled user = %v, %s", err, g.Status)
	}
	h.db.fail["LockActiveUser"] = errBoom
	if _, err := h.svc.Acknowledge(t.Context(), alice, g.PublicID); !errors.Is(err, errBoom) {
		t.Errorf("a failing lock = %v", err)
	}
}
