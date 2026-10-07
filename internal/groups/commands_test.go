// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
)

func (f *fakeDB) GetGroupRef(_ context.Context, arg dbgen.GetGroupRefParams) (dbgen.GetGroupRefRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetGroupRef"); err != nil {
		return dbgen.GetGroupRefRow{}, err
	}
	g := f.groups[arg.ID]
	if g == nil {
		return dbgen.GetGroupRefRow{}, pgx.ErrNoRows
	}
	return dbgen.GetGroupRefRow{PublicID: g.PublicID, Number: g.Number}, nil
}

// newer is the open Alert Group of the Route and key of a person-resolved g that takes part in grouping, as the reads
// select it.
func (f *fakeDB) newer(g *dbgen.LockGroupsRow) (string, int64) {
	if g.ResolvedByKind.String != ResolvedByUser || g.MovedFromRouteID.Valid {
		return "", 0
	}
	for _, n := range f.sortedGroups() {
		if n.RouteID == g.RouteID && bytes.Equal(n.GroupKeySha256, g.GroupKeySha256) && n.Status != "resolved" &&
			!n.MovedFromRouteID.Valid {
			return n.PublicID, n.Number
		}
	}
	return "", 0
}

// The people of the Command tests: the Responders Alice (the User owner of events_test.go) and Bob, Bob through a
// Personal access token, the Service account robot with the Role responder, and a Viewer.
const (
	bobID   = 10
	robotID = 20
	carolID = 11
)

var (
	responder = []auth.Permission{"alert-groups:read", PermissionAcknowledge, PermissionResolve, PermissionSnooze}
	alice     = Caller{Actor: audit.User(owner, "SRAAAAAAAAAAA9"), Transport: audit.TransportUI, Permissions: responder}
	bob       = Caller{Actor: audit.User(bobID, "SRAAAAAAAAAAB0"), Transport: audit.TransportUI, Permissions: responder}
	bobToken  = Caller{Actor: audit.User(bobID, "SRAAAAAAAAAAB0").Via(31, "script"), Transport: audit.TransportAPI,
		Permissions: responder}
	robot = Caller{Actor: audit.ServiceAccount(robotID, "SAAAAAAAAAAA20").Via(32, "ci"),
		Transport: audit.TransportAPI, Permissions: responder}
	carol = Caller{Actor: audit.User(carolID, "SRAAAAAAAAAAB1"), Transport: audit.TransportUI,
		Permissions: []auth.Permission{"alert-groups:read"}}
)

// people adds the Users and the Service account of the Command tests to the fake.
func (h *harness) people() {
	for id, name := range map[int64]string{owner: "Alice", bobID: "Bob", carolID: "Carol"} {
		pid := map[int64]string{owner: "SRAAAAAAAAAAA9", bobID: "SRAAAAAAAAAAB0", carolID: "SRAAAAAAAAAAB1"}[id]
		h.db.users[id] = dbgen.ListUserRefsRow{ID: id, PublicID: pid, Name: name, Login: strings.ToLower(name),
			Status: "active"}
	}
	h.db.accounts[robotID] = dbgen.ListServiceAccountRefsRow{ID: robotID, PublicID: "SAAAAAAAAAAA20", Name: "robot",
		Status: "active"}
}

// firing starts a firing Alert Group of one warning Alert on the Route db with the key cluster=x and returns it.
func (h *harness) firing(t *testing.T, cluster string) *dbgen.LockGroupsRow {
	t.Helper()
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": cluster})
	h.changes(t, ingest.ChangeFired, a)
	return h.groupOf(t, a)
}

// code is the code of a refusal, forbidden for a missing Permission, not_found for an unknown Alert Group.
func code(err error) string {
	if r, ok := errors.AsType[*RefusedError](err); ok {
		return r.Code
	}
	if _, ok := errors.AsType[*ForbiddenError](err); ok {
		return CodeForbidden
	}
	if errors.Is(err, ErrNotFound) {
		return CodeNotFound
	}
	if f, ok := errors.AsType[*FieldError](err); ok {
		return f.Pointer + " " + f.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// do runs the Command cmd as c on g, with a Snooze of an hour.
func (h *harness) do(c Caller, cmd Command, g *dbgen.LockGroupsRow) (Result, error) {
	ctx := context.Background()
	switch cmd {
	case CommandAcknowledge:
		return h.svc.Acknowledge(ctx, c, g.PublicID)
	case CommandUnacknowledge:
		return h.svc.Unacknowledge(ctx, c, g.PublicID)
	case CommandResolve:
		return h.svc.Resolve(ctx, c, g.PublicID, nil)
	case CommandUnresolve:
		return h.svc.Unresolve(ctx, c, g.PublicID)
	case CommandSnooze:
		return h.svc.Snooze(ctx, c, g.PublicID, SnoozeEnd{Until: ptr(h.clock.Now().Add(time.Hour))})
	default:
		return h.svc.Unsnooze(ctx, c, g.PublicID)
	}
}

// TestCommandMatrix is C-10.FR-1 and C-10.FR-2: every Command from every status, as Alice. A refusal answers its code
// and writes nothing; a Command that runs changes the status as the table of C-10.FR-1 says, with one Audit log entry
// and one Timeline entry.
func TestCommandMatrix(t *testing.T) {
	setups := map[string]func(h *harness, g *dbgen.LockGroupsRow){
		"firing":       func(*harness, *dbgen.LockGroupsRow) {},
		"acknowledged": func(h *harness, g *dbgen.LockGroupsRow) { h.acknowledge(g) },
		"acknowledged by bob": func(h *harness, g *dbgen.LockGroupsRow) {
			h.acknowledge(g)
			g.OwnerUserID = i8(bobID)
		},
		"snoozed": func(h *harness, g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), false) },
		"resolved by the system": func(_ *harness, g *dbgen.LockGroupsRow) {
			g.Status, g.ResolvedAt, g.ResolvedByKind = "resolved", ts(t0), txt(ResolvedBySystem)
			g.ResolveReason = txt("resolved")
		},
		"resolved by a person": func(h *harness, g *dbgen.LockGroupsRow) { h.personResolve(g, 15*time.Minute) },
	}
	cases := []struct {
		status string
		cmd    Command
		code   string
		to     Status
		event  Event
		action string
	}{
		{"firing", CommandAcknowledge, "", StatusAcknowledged, EventAcknowledged, ActionAcknowledged},
		{"firing", CommandUnacknowledge, CodeNotAcknowledged, "", "", ""},
		{"firing", CommandResolve, "", StatusResolved, EventResolved, ActionResolved},
		{"firing", CommandUnresolve, CodeNotResolved, "", "", ""},
		{"firing", CommandSnooze, "", StatusSnoozed, EventSnoozed, ActionSnoozed},
		{"firing", CommandUnsnooze, CodeNotSnoozed, "", "", ""},
		{"acknowledged", CommandAcknowledge, "", StatusAcknowledged, "", ""},
		{"acknowledged by bob", CommandAcknowledge, "", StatusAcknowledged, EventTakeover, ActionTakenOver},
		{"acknowledged", CommandUnacknowledge, "", StatusFiring, EventUnacknowledged, ActionUnacknowledged},
		{"acknowledged", CommandResolve, "", StatusResolved, EventResolved, ActionResolved},
		{"acknowledged", CommandUnresolve, CodeNotResolved, "", "", ""},
		{"acknowledged", CommandSnooze, "", StatusSnoozed, EventSnoozed, ActionSnoozed},
		{"acknowledged", CommandUnsnooze, CodeNotSnoozed, "", "", ""},
		{"snoozed", CommandAcknowledge, "", StatusAcknowledged, EventAcknowledged, ActionAcknowledged},
		{"snoozed", CommandUnacknowledge, CodeNotAcknowledged, "", "", ""},
		{"snoozed", CommandResolve, "", StatusResolved, EventResolved, ActionResolved},
		{"snoozed", CommandUnresolve, CodeNotResolved, "", "", ""},
		{"snoozed", CommandSnooze, "", StatusSnoozed, EventSnoozed, ActionSnoozed},
		{"snoozed", CommandUnsnooze, "", StatusFiring, EventUnsnoozed, ActionUnsnoozed},
		{"resolved by the system", CommandAcknowledge, CodeAlreadyResolved, "", "", ""},
		{"resolved by the system", CommandUnacknowledge, CodeNotAcknowledged, "", "", ""},
		{"resolved by the system", CommandResolve, CodeAlreadyResolved, "", "", ""},
		{"resolved by the system", CommandUnresolve, CodeResolvedAutomatically, "", "", ""},
		{"resolved by the system", CommandSnooze, CodeAlreadyResolved, "", "", ""},
		{"resolved by the system", CommandUnsnooze, CodeNotSnoozed, "", "", ""},
		{"resolved by a person", CommandAcknowledge, CodeAlreadyResolved, "", "", ""},
		{"resolved by a person", CommandResolve, CodeAlreadyResolved, "", "", ""},
		{"resolved by a person", CommandSnooze, CodeAlreadyResolved, "", "", ""},
		{"resolved by a person", CommandUnresolve, "", StatusFiring, EventUnresolved, ActionUnresolved},
	}
	for _, c := range cases {
		t.Run(c.status+" "+string(c.cmd), func(t *testing.T) {
			h := newHarness(t)
			h.people()
			g := h.firing(t, "x")
			setups[c.status](h, g)
			h.clock.Advance(time.Minute)
			before, saves := len(h.entriesOf(g)), h.db.calls["SaveGroup"]
			res, err := h.do(alice, c.cmd, g)
			if got := code(err); got != c.code {
				t.Fatalf("%s = %q, want %q", c.cmd, got, c.code)
			}
			entries := h.entriesOf(g)[before:]
			if c.code != "" || c.event == "" {
				if len(entries) != 0 || len(h.db.audit) != 0 || h.db.calls["SaveGroup"] != saves {
					t.Errorf("wrote %d entries, %d audit entries", len(entries), len(h.db.audit))
				}
				if c.code != "" && !strings.Contains(h.log.String(), `"event":"command_refused"`) {
					t.Errorf("no command_refused in %s", h.log)
				}
				if c.code == "" && res.Outcome != OutcomeUnchanged {
					t.Errorf("outcome %s", res.Outcome)
				}
				return
			}
			if res.Outcome != OutcomeDone || res.Group.Status != c.to || Status(g.Status) != c.to {
				t.Errorf("result %s %s, row %s, want %s", res.Outcome, res.Group.Status, g.Status, c.to)
			}
			if len(entries) != 1 || entries[0].Event.String != string(c.event) || entries[0].ActorKind != "user" ||
				entries[0].Transport != "ui" || entries[0].ActorUserID != i8(owner) || entries[0].ToStatus.String != string(c.to) {
				t.Errorf("entries %+v", entries)
			}
			if len(h.db.audit) != 1 || h.db.audit[0].Action != c.action || h.db.audit[0].ActorUserID != i8(owner) ||
				h.db.audit[0].ResourcePublicID.String != g.PublicID || h.db.audit[0].Transport != "ui" {
				t.Errorf("audit %+v", h.db.audit)
			}
			if !strings.Contains(h.log.String(), `"event":"command_executed","command":"`+string(c.cmd)) {
				t.Errorf("no command_executed in %s", h.log)
			}
		})
	}
}

// TestAcknowledge is C-10.FR-4, FR-5 and C-10.AC-3: the first Acknowledge sets the Owner and the first
// acknowledgement once, Quietly; another user's Acknowledge is a Loud Takeover naming the previous Owner, with an
// Audit log entry of its own; the Owner's own Acknowledge changes nothing. Two Acknowledges one after the other, as
// the row lock serializes them, leave the later one as Owner and a single Takeover.
func TestAcknowledge(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	h.snooze(g, t0.Add(time.Hour), false)
	h.clock.Advance(5 * time.Minute)
	res, err := h.svc.Acknowledge(t.Context(), alice, g.PublicID)
	if err != nil || res.Outcome != OutcomeDone || res.Group.Owner == nil || res.Group.Owner.Name != "Alice" ||
		res.Group.SnoozeUntil != nil || g.SnoozedByUserID.Valid || g.FirstAcknowledgedAt != ts(t0.Add(5*time.Minute)) {
		t.Fatalf("acknowledge = %v %+v, row %+v", err, res, g)
	}
	if e := h.last(t, g); e.Event.String != "acknowledged" || e.Loudness.String != "quiet" || len(e.Mentions) != 0 ||
		e.FromStatus.String != "snoozed" || e.OwnerUserID != i8(owner) {
		t.Errorf("acknowledged = %+v", e)
	}
	h.clock.Advance(time.Minute)
	n := len(h.entriesOf(g))
	if res, err := h.svc.Acknowledge(t.Context(), alice, g.PublicID); err != nil || res.Outcome != OutcomeUnchanged ||
		len(h.entriesOf(g)) != n || len(h.db.audit) != 1 {
		t.Errorf("acknowledge by the owner = %v %+v", err, res)
	}
	if _, err := h.svc.Acknowledge(t.Context(), bobToken, g.PublicID); err != nil {
		t.Fatal(err)
	}
	e := h.last(t, g)
	if e.Event.String != "takeover" || e.Loudness.String != "loud" || !slices.Equal(e.Mentions, []string{"previous_owner"}) ||
		e.PreviousOwnerUserID != i8(owner) || e.OwnerUserID != i8(bobID) || e.ApiTokenID != i8(31) ||
		e.TokenName.String != "script" || e.Transport != "api" {
		t.Errorf("takeover = %+v", e)
	}
	if a := h.db.audit[len(h.db.audit)-1]; a.Action != ActionTakenOver || a.TokenName.String != "script" ||
		!strings.Contains(string(a.Details), "SRAAAAAAAAAAA9") || a.ApiTokenID != i8(31) {
		t.Errorf("audit = %+v %s", a, a.Details)
	}
	if g.OwnerUserID != i8(bobID) || g.FirstAcknowledgedAt != ts(t0.Add(5*time.Minute)) {
		t.Errorf("row %+v", g)
	}
	if _, err := h.svc.Acknowledge(t.Context(), alice, g.PublicID); err != nil {
		t.Fatal(err)
	}
	takeovers := 0
	for _, e := range h.entriesOf(g) {
		if e.Event.String == "takeover" {
			takeovers++
		}
	}
	if takeovers != 2 || g.OwnerUserID != i8(owner) {
		t.Errorf("%d takeovers, owner %v", takeovers, g.OwnerUserID)
	}
	// A Service account cannot become an Owner (C-04.FR-2); a Personal access token acts as its User (C-04.AC-6).
	other := h.firing(t, "y")
	if _, err := h.svc.Acknowledge(t.Context(), robot, other.PublicID); code(err) != CodeOwnerMustBeUser ||
		other.Status != "firing" {
		t.Errorf("robot = %v", err)
	}
	if res, err := h.svc.Acknowledge(t.Context(), bobToken, other.PublicID); err != nil ||
		res.Group.Owner.Name != "Bob" {
		t.Errorf("bob's token = %v %+v", err, res.Group.Owner)
	}
}

// TestResolveAndUnresolve is C-09.FR-5, C-10.FR-7 and C-10.AC-2: a person's Resolve ends the acknowledgement and
// starts the Grace period of the Alerts still firing; a Service account resolves as itself; Unresolve ends the Grace
// period and makes it firing without an Owner, and is refused, in order, when the system resolved it, when a newer
// open Alert Group of its key takes part in grouping — also one that opens in a race — and when no Alert still fires.
func TestResolveAndUnresolve(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	h.acknowledge(g)
	h.clock.Advance(time.Minute)
	res, err := h.svc.Resolve(t.Context(), robot, g.PublicID, nil)
	if err != nil || res.Group.Resolution == nil || res.Group.Resolution.Actor == nil ||
		res.Group.Resolution.Actor.Name != "robot" || g.OwnerUserID.Valid || g.ResolvedByServiceAccountID != i8(robotID) {
		t.Fatalf("resolve = %v %+v", err, res.Group.Resolution)
	}
	if d := h.db.timers[timerKey(g.ID, TimerGracePeriodEnd)]; !d.Equal(t0.Add(16*time.Minute)) ||
		g.GraceDeadline != ts(t0.Add(16*time.Minute)) {
		t.Errorf("grace deadline %v", d)
	}
	if !slices.Contains(res.Group.Allowed(alice), CommandUnresolve) {
		t.Errorf("allowed %v", res.Group.Allowed(alice))
	}
	res, err = h.svc.Unresolve(t.Context(), bobToken, g.PublicID)
	if err != nil || res.Group.Status != StatusFiring || res.Group.Owner != nil || g.GraceDeadline.Valid ||
		g.ResolvedByKind.Valid {
		t.Fatalf("unresolve = %v %+v", err, res.Group)
	}
	if _, ok := h.db.timers[timerKey(g.ID, TimerGracePeriodEnd)]; ok || h.db.calls["LockCounter"] == 0 {
		t.Errorf("timers %v, counter locks %d", h.db.timers, h.db.calls["LockCounter"])
	}
	// A newer open Alert Group of the same key, and one that opens between the read and the Command.
	if _, err := h.svc.Resolve(t.Context(), alice, g.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, b)
	n := h.groupOf(t, b)
	v, err := h.svc.Get(t.Context(), g.PublicID)
	if err != nil || v.Newer == nil || v.Newer.PublicID != n.PublicID || slices.Contains(v.Allowed(alice), CommandUnresolve) ||
		!slices.ContainsFunc(v.Notices, func(x Notice) bool {
			return x.Kind == NoticeNewerAlertGroupExists && x.Related != nil && x.Related.Number == n.Number
		}) {
		t.Fatalf("get = %v %+v", err, v)
	}
	_, err = h.svc.Unresolve(t.Context(), alice, g.PublicID)
	if r, ok := errors.AsType[*RefusedError](err); !ok || r.Code != CodeNewerGroupExists || r.Related == nil ||
		r.Related.PublicID != n.PublicID || r.Message != "a newer open Alert Group #2 exists" ||
		r.Detail() != "A newer open Alert Group #2 exists." {
		t.Errorf("unresolve with a newer one = %v", err)
	}
	// A moved Alert Group stays out of grouping: the newer one does not count, but no Alert fires in it.
	g.MovedFromRouteID = i8(3)
	g.FiringAlertCount = 0
	if _, err := h.svc.Unresolve(t.Context(), alice, g.PublicID); code(err) != CodeAllAlertsResolved {
		t.Errorf("moved without alerts = %v", err)
	}
	if v, _ := h.svc.Get(t.Context(), g.PublicID); v.Newer != nil {
		t.Errorf("a moved alert group names %+v", v.Newer)
	}
	// Resolve without Alerts firing starts no Grace period.
	other := h.firing(t, "z")
	h.resolve(t, h.db.members[len(h.db.members)-1].alert)
	other.Status, other.ResolvedByKind, other.ResolveReason = "firing", pgtype.Text{}, pgtype.Text{}
	other.ResolvedAt = pgtype.Timestamptz{}
	if _, err := h.svc.Resolve(t.Context(), alice, other.PublicID, nil); err != nil || other.GraceDeadline.Valid {
		t.Errorf("resolve without firing alerts = %v %v", err, other.GraceDeadline)
	}
}

// TestUnresolveRace: an open Alert Group of the same key that appears after the read still refuses Unresolve.
func TestUnresolveRace(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	if _, err := h.svc.Resolve(t.Context(), alice, g.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	v, err := h.svc.Get(t.Context(), g.PublicID)
	if err != nil || !slices.Contains(v.Allowed(alice), CommandUnresolve) {
		t.Fatalf("allowed before the race: %v %v", err, v.Allowed(alice))
	}
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, b)
	if _, err := h.svc.Unresolve(t.Context(), alice, g.PublicID); code(err) != CodeNewerGroupExists {
		t.Errorf("unresolve after the race = %v", err)
	}
}

// TestSnooze is C-10.FR-6 and C-10.AC-17: exactly one of until and no_end, a future until; an acknowledged Alert
// Group loses its Owner, whom the snoozed entry names; a snoozed one changes only its end; a Snooze set while Urgent is
// marked so; Unsnooze makes it firing without an Owner.
func TestSnooze(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	now := h.clock.Now()
	for _, c := range []struct {
		end  SnoozeEnd
		want string
	}{
		{SnoozeEnd{}, " one_of_required"},
		{SnoozeEnd{Until: ptr(now.Add(time.Hour)), NoEnd: true}, " one_of_required"},
		{SnoozeEnd{Until: ptr(now)}, "/until out_of_range"},
		{SnoozeEnd{Until: ptr(now.Add(-time.Hour))}, "/until out_of_range"},
	} {
		if _, err := h.svc.Snooze(t.Context(), alice, g.PublicID, c.end); code(err) != c.want {
			t.Errorf("snooze %+v = %v, want %s", c.end, err, c.want)
		}
	}
	h.acknowledge(g)
	res, err := h.svc.Snooze(t.Context(), robot, g.PublicID, SnoozeEnd{NoEnd: true})
	if err != nil || res.Group.Status != StatusSnoozed || res.Group.SnoozeUntil != nil || res.Group.SnoozedBy == nil ||
		res.Group.SnoozedBy.Name != "robot" || res.Group.Owner != nil || !g.SnoozeNoEnd || g.SnoozedWhileUrgent {
		t.Fatalf("snooze = %v %+v", err, res.Group)
	}
	if e := h.last(t, g); e.Event.String != "snoozed" || e.PreviousOwnerUserID != i8(owner) ||
		e.FromStatus.String != "acknowledged" || e.ActorServiceAccountID != i8(robotID) {
		t.Errorf("snoozed = %+v", e)
	}
	until := now.Add(2 * time.Hour)
	res, err = h.svc.Snooze(t.Context(), alice, g.PublicID, SnoozeEnd{Until: &until})
	if err != nil || res.Group.SnoozeUntil == nil || !res.Group.SnoozeUntil.Equal(until) || g.SnoozeNoEnd ||
		g.SnoozedByServiceAccountID != i8(robotID) || res.Group.SnoozedBy.Name != "robot" {
		t.Fatalf("change of the end = %v %+v", err, res.Group)
	}
	if a := h.db.audit[len(h.db.audit)-1]; a.Action != ActionSnoozed || !strings.Contains(string(a.Details), `"until"`) {
		t.Errorf("audit %+v %s", a, a.Details)
	}
	res, err = h.svc.Unsnooze(t.Context(), alice, g.PublicID)
	if err != nil || res.Group.Status != StatusFiring || res.Group.SnoozedBy != nil || g.SnoozedByServiceAccountID.Valid {
		t.Fatalf("unsnooze = %v %+v", err, res.Group)
	}
	// Urgent now: the Route is urgent.
	h.db.routes[2].Urgent = true
	if _, err := h.svc.Snooze(t.Context(), alice, g.PublicID, SnoozeEnd{NoEnd: true}); err != nil ||
		!g.SnoozedWhileUrgent {
		t.Errorf("snooze while urgent = %v %v", err, g.SnoozedWhileUrgent)
	}
}

// TestCommandRefusals is C-10.AC-4: a missing Permission is refused by the dispatcher before anything is read, with
// command_refused and the code forbidden; an unknown or malformed id is not found.
func TestCommandRefusals(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	locks := h.db.calls["LockGroups"]
	for _, cmd := range []Command{CommandAcknowledge, CommandUnacknowledge, CommandResolve, CommandUnresolve,
		CommandSnooze, CommandUnsnooze} {
		_, err := h.do(carol, cmd, g)
		if f, ok := errors.AsType[*ForbiddenError](err); !ok || f.Permission != cmd.Permission() ||
			!strings.Contains(f.Error(), string(cmd.Permission())) {
			t.Errorf("%s by a viewer = %v", cmd, err)
		}
	}
	if h.db.calls["LockGroups"] != locks || len(h.db.audit) != 0 || g.Status != "firing" {
		t.Errorf("a refusal read or wrote: %d locks, %d audit entries", h.db.calls["LockGroups"]-locks, len(h.db.audit))
	}
	if !strings.Contains(h.log.String(), `"event":"command_refused","command":"acknowledge","group":"`+g.PublicID+
		`","actor":"SRAAAAAAAAAAB1","transport":"ui","code":"forbidden"`) {
		t.Errorf("log %s", h.log)
	}
	// The Permission comes before the arguments: a Viewer's Snooze without its end and Resolve with a Note.
	if _, err := h.svc.Snooze(t.Context(), carol, g.PublicID, SnoozeEnd{}); code(err) != CodeForbidden {
		t.Errorf("a viewer's snooze {} = %v", err)
	}
	if _, err := h.svc.Resolve(t.Context(), carol, g.PublicID, ptr("x")); code(err) != CodeForbidden {
		t.Errorf("a viewer's resolve with a note = %v", err)
	}
	if _, err := h.svc.Resolve(t.Context(), alice, g.PublicID, ptr("x")); code(err) != "/note unsupported" ||
		g.Status != "firing" {
		t.Errorf("resolve with a note = %v", err)
	}
	for _, id := range []string{"nonsense", "AGZZZZZZZZZZZZ"} {
		if _, err := h.svc.Acknowledge(t.Context(), alice, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("acknowledge %s = %v", id, err)
		}
	}
	if (&RefusedError{Code: CodeNotSnoozed}).Error() != "command refused: not_snoozed" || sentence("") != "" ||
		(&skippedError{}).Error() == "" {
		t.Error("error texts")
	}
}

// TestCommandFailures: a query that fails fails the Command, which changes nothing that commits.
func TestCommandFailures(t *testing.T) {
	for _, c := range []struct {
		query string
		cmd   Command
		setup func(h *harness, g *dbgen.LockGroupsRow)
	}{
		{"GetGroupID", CommandAcknowledge, nil},
		{"LockGroups", CommandAcknowledge, nil},
		{"InsertAuditEntry", CommandAcknowledge, nil},
		{"GetRoutePolicy", CommandAcknowledge, nil},
		{"GetGroup", CommandAcknowledge, nil},
		{"GetRoutePolicy", CommandResolve, nil},
		{"UpsertTimer", CommandResolve, nil},
		{"GetGroupingSettings", CommandSnooze, nil},
		{"GetRoutePolicy", CommandSnooze, nil},
		{"EnsureCounter", CommandUnresolve, nil},
		{"LockCounter", CommandUnresolve, nil},
		{"FindOpenGroup", CommandUnresolve, func(h *harness, g *dbgen.LockGroupsRow) { h.personResolve(g, time.Minute) }},
		{"DeleteTimer", CommandUnresolve, func(h *harness, g *dbgen.LockGroupsRow) { h.personResolve(g, time.Minute) }},
		{"ListUserRefs", CommandUnacknowledge, func(h *harness, g *dbgen.LockGroupsRow) { h.acknowledge(g) }},
		{"ListUserRefs", CommandAcknowledge, func(h *harness, g *dbgen.LockGroupsRow) {
			h.acknowledge(g)
			g.OwnerUserID = i8(bobID)
		}},
	} {
		t.Run(c.query+" "+string(c.cmd), func(t *testing.T) {
			h := newHarness(t)
			h.people()
			g := h.firing(t, "x")
			if c.setup != nil {
				c.setup(h, g)
			}
			h.db.fail[c.query] = errBoom
			if _, err := h.do(alice, c.cmd, g); !errors.Is(err, errBoom) {
				t.Errorf("%s failing = %v", c.query, err)
			}
		})
	}
	// The newer open Alert Group cannot be named.
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	h.personResolve(g, time.Minute)
	h.firing(t, "x")
	h.db.fail["GetGroupRef"] = errBoom
	if _, err := h.svc.Unresolve(t.Context(), alice, g.PublicID); !errors.Is(err, errBoom) {
		t.Errorf("GetGroupRef failing = %v", err)
	}
	// An unknown command never reaches a transition.
	if _, err := h.svc.transition(t.Context(), h.db, alice, "add_note", &Group{}, args{}, false); err == nil {
		t.Error("an unknown command ran")
	}
	// A row that disappears between the lookup and the lock is not found.
	h.db.before["LockGroups"] = func() { delete(h.db.groups, g.ID) }
	if _, err := h.svc.Unsnooze(t.Context(), alice, g.PublicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a vanished row = %v", err)
	}
}

// TestOwnerDetailsOfDeletedOwner: an Owner whose row is gone leaves the Audit log details empty.
func TestOwnerDetailsOfDeletedOwner(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	h.acknowledge(g)
	delete(h.db.users, owner)
	if _, err := h.svc.Unacknowledge(t.Context(), bob, g.PublicID); err != nil {
		t.Fatal(err)
	}
	if d := string(h.db.audit[0].Details); d != "{}" {
		t.Errorf("details %s", d)
	}
}

// TestUnresolveDeletedRoute: Unresolve of an Alert Group whose Route was deleted is refused with route_deleted, after
// resolved_automatically and before newer_alert_group_exists, and is not offered.
func TestUnresolveDeletedRoute(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	if _, err := h.svc.Resolve(t.Context(), alice, g.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	h.db.routes[2].deleted = true
	if _, err := h.svc.Unresolve(t.Context(), alice, g.PublicID); code(err) != CodeRouteDeleted {
		t.Errorf("unresolve = %v", err)
	}
	if !strings.Contains(h.log.String(), `"code":"route_deleted"`) || g.Status != "resolved" {
		t.Errorf("log %s", h.log)
	}
	v, err := h.svc.Get(t.Context(), g.PublicID)
	if err != nil || slices.Contains(v.Allowed(alice), CommandUnresolve) {
		t.Errorf("allowed %v %v", err, v.Allowed(alice))
	}
	for _, q := range []string{"PeekGroup", "LockRoutes"} {
		h.db.fail[q] = errBoom
		if _, err := h.svc.Unresolve(t.Context(), alice, g.PublicID); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", q, err)
		}
		delete(h.db.fail, q)
	}
}
