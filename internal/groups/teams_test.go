// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
)

// fakeAlertTeam is an alert_teams row: the Team, the rule and the Team matcher of the rule matcher.
type fakeAlertTeam struct {
	team          int64
	rule, matcher string
}

// fakeGroupTeam is an alert_group_teams row.
type fakeGroupTeam struct {
	fakeAlertTeam
	joined time.Time
}

// fakeTeams are the Teams of the fake database: those routing recorded on each Alert with the value of its Team label
// that named no Team, the Teams of each Alert Group with the values it recorded, the Teams by id and the Teams of
// each User.
type fakeTeams struct {
	alerts  map[int64][]fakeAlertTeam
	unknown map[int64]string
	groups  map[int64][]fakeGroupTeam
	values  map[int64][]string
	refs    map[int64]dbgen.ListTeamRefsRow
	users   map[int64][]int64
}

func (f *fakeDB) teamsOf() *fakeTeams {
	if f.teams.refs == nil {
		f.teams = fakeTeams{alerts: map[int64][]fakeAlertTeam{}, unknown: map[int64]string{},
			groups: map[int64][]fakeGroupTeam{}, values: map[int64][]string{}, refs: map[int64]dbgen.ListTeamRefsRow{},
			users: map[int64][]int64{}}
	}
	return &f.teams
}

func (f *fakeDB) ListAlertTeams(_ context.Context, arg dbgen.ListAlertTeamsParams) ([]dbgen.ListAlertTeamsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListAlertTeams"); err != nil {
		return nil, err
	}
	ts := f.teamsOf()
	var out []dbgen.ListAlertTeamsRow
	for _, id := range slices.Sorted(slices.Values(arg.AlertIds)) {
		if _, ok := f.alerts[id]; !ok || arg.OrgID != orgID {
			continue
		}
		n := len(out)
		for _, m := range slices.SortedFunc(slices.Values(ts.alerts[id]), func(a, b fakeAlertTeam) int {
			return cmp.Compare(a.team, b.team)
		}) {
			if ts.refs[m.team].Deleted {
				continue
			}
			out = append(out, dbgen.ListAlertTeamsRow{AlertID: id, UnknownTeamValue: ts.unknown[id], TeamID: m.team,
				Rule: m.rule, Matcher: m.matcher})
		}
		if len(out) == n {
			out = append(out, dbgen.ListAlertTeamsRow{AlertID: id, UnknownTeamValue: ts.unknown[id]})
		}
	}
	return out, nil
}

func (f *fakeDB) AddGroupTeams(_ context.Context, arg dbgen.AddGroupTeamsParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("AddGroupTeams"); err != nil {
		return nil, err
	}
	ts := f.teamsOf()
	var added []int64
	for i, id := range arg.TeamIds {
		if slices.ContainsFunc(ts.groups[arg.AlertGroupID], func(g fakeGroupTeam) bool { return g.team == id }) {
			continue
		}
		ts.groups[arg.AlertGroupID] = append(ts.groups[arg.AlertGroupID], fakeGroupTeam{
			fakeAlertTeam: fakeAlertTeam{team: id, rule: arg.Rules[i], matcher: arg.Matchers[i]}, joined: arg.JoinedAt})
		added = append(added, id)
	}
	// The database returns the inserted rows in no particular order.
	slices.Reverse(added)
	return added, nil
}

func (f *fakeDB) AddUnknownTeamValues(_ context.Context, arg dbgen.AddUnknownTeamValuesParams) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("AddUnknownTeamValues"); err != nil {
		return nil, err
	}
	ts := f.teamsOf()
	added := []string{}
	for _, v := range slices.Sorted(slices.Values(arg.TeamValues)) {
		if !slices.Contains(ts.values[arg.ID], v) && !slices.Contains(added, v) {
			added = append(added, v)
		}
	}
	ts.values[arg.ID] = append(ts.values[arg.ID], added...)
	return added, nil
}

func (f *fakeDB) ListGroupTeams(_ context.Context, arg dbgen.ListGroupTeamsParams) ([]dbgen.ListGroupTeamsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupTeams"); err != nil {
		return nil, err
	}
	ts := f.teamsOf()
	var out []dbgen.ListGroupTeamsRow
	for _, id := range slices.Sorted(slices.Values(arg.AlertGroupIds)) {
		for _, g := range ts.groups[id] {
			r := ts.refs[g.team]
			out = append(out, dbgen.ListGroupTeamsRow{AlertGroupID: id, PublicID: r.PublicID, Name: r.Name,
				Slug: r.Slug, Deleted: r.Deleted, Rule: g.rule, Matcher: optional(g.matcher), JoinedAt: g.joined})
		}
	}
	return out, nil
}

func (f *fakeDB) ListGroupAlertTeams(_ context.Context, arg dbgen.ListGroupAlertTeamsParams) (
	[]dbgen.ListGroupAlertTeamsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupAlertTeams"); err != nil {
		return nil, err
	}
	ts := f.teamsOf()
	var out []dbgen.ListGroupAlertTeamsRow
	for _, id := range arg.AlertIds {
		for _, m := range ts.alerts[id] {
			r := ts.refs[m.team]
			out = append(out, dbgen.ListGroupAlertTeamsRow{AlertID: id, PublicID: r.PublicID, Name: r.Name,
				Slug: r.Slug, Deleted: r.Deleted, Rule: m.rule, Matcher: optional(m.matcher)})
		}
	}
	return out, nil
}

func (f *fakeDB) ListTeamRefs(_ context.Context, arg dbgen.ListTeamRefsParams) ([]dbgen.ListTeamRefsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListTeamRefs"); err != nil {
		return nil, err
	}
	var out []dbgen.ListTeamRefsRow
	for _, id := range arg.Ids {
		if r, ok := f.teamsOf().refs[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeDB) ListTeamsByPublicID(_ context.Context, arg dbgen.ListTeamsByPublicIDParams) (
	[]dbgen.ListTeamsByPublicIDRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListTeamsByPublicID"); err != nil {
		return nil, err
	}
	var out []dbgen.ListTeamsByPublicIDRow
	for _, r := range f.teamsOf().refs {
		if slices.Contains(arg.PublicIds, r.PublicID) {
			out = append(out, dbgen.ListTeamsByPublicIDRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name})
		}
	}
	return out, nil
}

func (f *fakeDB) ListUserTeamIDs(_ context.Context, arg dbgen.ListUserTeamIDsParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListUserTeamIDs"); err != nil {
		return nil, err
	}
	return f.teamsOf().users[arg.UserID], nil
}

func optional(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// The Teams of the tests: payments, search and checkout, whose matcher is namespace=~"checkout|billing".
const (
	payments int64 = 10
	search   int64 = 11
	checkout int64 = 12
)

const checkoutMatcher = `namespace=~"checkout|billing"`

// withTeams adds the Teams payments, search and checkout.
func (h *harness) withTeams() {
	ts := h.db.teamsOf()
	for _, r := range []dbgen.ListTeamRefsRow{{ID: payments, PublicID: "TMPAYMENTSAAAA", Name: "Payments",
		Slug: "payments"}, {ID: search, PublicID: "TMSEARCHAAAAAA", Name: "Search", Slug: "search"},
		{ID: checkout, PublicID: "TMCHECKOUTAAAA", Name: "Checkout", Slug: "checkout-team"}} {
		ts.refs[r.ID] = r
	}
}

// teamAlert adds a firing Alert of the Route db in the cluster with the Teams routing gave it and the value of its Team
// label that named no Team.
func (h *harness) teamAlert(cluster, unknown string, teams ...fakeAlertTeam) int64 {
	id := h.alert(2, "warning", map[string]string{"alertname": "OOMKilled", "cluster": cluster,
		"n": fmt.Sprint(h.nextFP)})
	ts := h.db.teamsOf()
	ts.alerts[id], ts.unknown[id] = teams, unknown
	return id
}

func byLabel(team int64) fakeAlertTeam { return fakeAlertTeam{team: team, rule: TeamRuleLabel} }

func byMatcher(team int64, m string) fakeAlertTeam {
	return fakeAlertTeam{team: team, rule: TeamRuleMatcher, matcher: m}
}

// teamEntries are the team_joined and unknown_team_value entries of an Alert Group as event/team/rule/matcher/value,
// with their kind and loudness.
func (h *harness) teamEntries(g *dbgen.LockGroupsRow) []string {
	var out []string
	for _, e := range h.entriesOf(g) {
		switch Event(e.Event.String) {
		case EventTeamJoined, EventUnknownTeamValue:
			out = append(out, strings.Join([]string{e.Event.String, e.Kind, e.Loudness.String, e.SystemEvent.String,
				refName(h, e.TeamID), e.TeamRule.String, e.TeamMatcher.String, e.TeamValue.String}, "/"))
		}
	}
	return out
}

func refName(h *harness, id pgtype.Int8) string {
	if !id.Valid {
		return ""
	}
	return h.db.teamsOf().refs[id.Int64].Slug
}

// groupTeamSlugs are the Teams of an Alert Group as Get reads them, slug:rule:deleted.
func (h *harness) groupTeamSlugs(t *testing.T, g *dbgen.LockGroupsRow) []string {
	t.Helper()
	v, err := h.svc.Get(t.Context(), g.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, gt := range v.Teams {
		s := gt.Team.Slug + ":" + gt.Rule
		if gt.Team.Deleted {
			s += ":deleted"
		}
		out = append(out, s)
	}
	return out
}

// TestTeamsJoin is C-25.AC-1, C-25.AC-5, C-25.FR-6, C-25.FR-7, C-09.FR-1 and C-09.FR-22: the Teams of an Alert Group
// are those of every Alert that joined it and only grow; each Team that joins records one Quiet team_joined of kind
// alerts naming the Team and the rule, by the label when any of its Alerts belongs to it by the label; each value of
// the Team label that named no Team records one Quiet unknown_team_value of kind system, once per value, and adds
// nothing; a Team stays after its Alerts resolve.
func TestTeamsJoin(t *testing.T) {
	h := newHarness(t)
	h.withTeams()
	a := h.teamAlert("x", "", byLabel(payments), byMatcher(checkout, checkoutMatcher))
	b := h.teamAlert("x", "Payments", byMatcher(payments, `pod=~"p.*"`))
	h.changes(t, ingest.ChangeFired, a, b)
	g := h.groupOf(t, a)
	if h.groupOf(t, b) != g {
		t.Fatal("the alerts are in two alert groups")
	}
	want := []string{"team_joined/alerts/quiet//payments/label//",
		"team_joined/alerts/quiet//checkout-team/matcher/" + checkoutMatcher + "/",
		"unknown_team_value/system/quiet/unknown_team_value////Payments"}
	if got := h.teamEntries(g); !slices.Equal(got, want) {
		t.Errorf("after creation\n%v\nwant\n%v", got, want)
	}
	if ev := h.events(g); len(ev) != 4 || ev[0] != "created" {
		t.Errorf("events %v", ev)
	}

	h.clock.Advance(time.Minute)
	c := h.teamAlert("x", "", byLabel(search))
	d := h.teamAlert("x", "Payments", byLabel(payments))
	h.changes(t, ingest.ChangeFired, c, d)
	want = append(want, "team_joined/alerts/quiet//search/label//")
	if got := h.teamEntries(g); !slices.Equal(got, want) {
		t.Errorf("after the alerts of search\n%v\nwant\n%v", got, want)
	}
	if ev := h.events(g); ev[len(ev)-2] != "alerts_added" || ev[len(ev)-1] != "team_joined" {
		t.Errorf("events %v", ev)
	}

	h.clock.Advance(time.Minute)
	h.resolve(t, c)
	if got := h.groupTeamSlugs(t, g); !slices.Equal(got, []string{"payments:label", "checkout-team:matcher",
		"search:label"}) {
		t.Errorf("teams after the alert of search resolved: %v", got)
	}
	v, err := h.svc.Get(t.Context(), g.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if m := v.Teams[1].Matcher; m == nil || *m != checkoutMatcher || v.Teams[0].Matcher != nil ||
		!v.Teams[0].JoinedAt.Equal(t0) || !v.Teams[2].JoinedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("teams %+v", v.Teams)
	}
}

// TestTeamsReads is C-06.FR-19, C-09.FR-11 and C-09.FR-13: the list items and the page carry the Teams of an Alert
// Group, each Alert of its Alerts the Teams routing gave it, and its team_joined and unknown_team_value entries the
// Team, the rule, the matcher and the value; a deleted Team is read by its name, marked deleted.
func TestTeamsReads(t *testing.T) {
	h := newHarness(t)
	h.withTeams()
	a := h.teamAlert("x", "Payments", byMatcher(checkout, checkoutMatcher))
	b := h.teamAlert("y", "")
	h.changes(t, ingest.ChangeFired, a, b)
	ga, gb := h.groupOf(t, a), h.groupOf(t, b)
	ref := h.db.teamsOf().refs[checkout]
	ref.Deleted = true
	h.db.teamsOf().refs[checkout] = ref

	page := h.list(t, ListRequest{Limit: 10})
	got := map[string][]GroupTeam{}
	for _, v := range page.Groups {
		got[v.PublicID] = v.Teams
	}
	if ts := got[ga.PublicID]; len(ts) != 1 || ts[0].Team.PublicID != "TMCHECKOUTAAAA" || !ts[0].Team.Deleted ||
		ts[0].Team.Name != "Checkout" || got[gb.PublicID] == nil || len(got[gb.PublicID]) != 0 {
		t.Errorf("list teams %+v", got)
	}

	alerts, err := h.svc.Alerts(t.Context(), ga.PublicID, AlertFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if ts := alerts.Alerts[0].Teams; len(ts) != 1 || ts[0].Rule != TeamRuleMatcher || *ts[0].Matcher != checkoutMatcher ||
		!ts[0].Team.Deleted {
		t.Errorf("alert teams %+v", ts)
	}
	other, err := h.svc.Alerts(t.Context(), gb.PublicID, AlertFilter{Limit: 10})
	if err != nil || other.Alerts[0].Teams == nil || len(other.Alerts[0].Teams) != 0 {
		t.Errorf("teams of an alert without any: %+v, %v", other.Alerts, err)
	}

	tl, err := h.svc.Timeline(t.Context(), ga.PublicID, TimelineFilter{Ascending: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var joined, unknown *TimelineEntry
	for i, e := range tl.Entries {
		switch Event(e.Event) {
		case EventTeamJoined:
			joined = &tl.Entries[i]
		case EventUnknownTeamValue:
			unknown = &tl.Entries[i]
		}
	}
	if joined == nil || joined.Team == nil || joined.Team.Slug != "checkout-team" || !joined.Team.Deleted ||
		*joined.TeamRule != TeamRuleMatcher || *joined.TeamMatcher != checkoutMatcher || joined.TeamValue != nil {
		t.Errorf("team_joined %+v", joined)
	}
	if unknown == nil || unknown.Kind != KindSystem || unknown.System != string(SystemUnknownTeamValue) ||
		*unknown.TeamValue != "Payments" || unknown.Team != nil {
		t.Errorf("unknown_team_value %+v", unknown)
	}
}

// TestTeamsOnEveryJoin is C-25.FR-6 on each way Alerts join an Alert Group: a Replacement and a Reopen add the Teams
// of their Alerts too; an Alert that fires again keeps its Alert Group and its Teams; a Team that is deleted since
// routing gave it to an Alert does not join; Alerts without Teams record nothing.
func TestTeamsOnEveryJoin(t *testing.T) {
	t.Run("replacement", func(t *testing.T) {
		h := newHarness(t)
		h.withTeams()
		a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "p1"})
		h.changes(t, ingest.ChangeFired, a)
		b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "p2"})
		h.db.teamsOf().alerts[b] = []fakeAlertTeam{byLabel(search)}
		h.changes(t, ingest.ChangeFired, b)
		g := h.groupOf(t, a)
		if ev := h.events(g); !slices.Equal(ev, []string{"created", "alert_replaced", "team_joined"}) {
			t.Errorf("events %v", ev)
		}
	})
	t.Run("reopen", func(t *testing.T) {
		h := newHarness(t)
		h.withTeams()
		a := h.teamAlert("x", "", byLabel(payments))
		h.changes(t, ingest.ChangeFired, a)
		g := h.groupOf(t, a)
		h.clock.Advance(time.Minute)
		h.resolve(t, a)
		h.clock.Advance(time.Minute)
		h.db.teamsOf().alerts[a] = []fakeAlertTeam{byLabel(payments), byLabel(search)}
		h.refire(t, a)
		if h.groupOf(t, a) != g {
			t.Fatal("not reopened")
		}
		if ev := h.events(g); !slices.Equal(ev, []string{"created", "team_joined", "resolved", "reopened",
			"team_joined"}) {
			t.Errorf("events %v", ev)
		}
		if got := h.groupTeamSlugs(t, g); !slices.Equal(got, []string{"payments:label", "search:label"}) {
			t.Errorf("teams %v", got)
		}
	})
	t.Run("deleted team and none", func(t *testing.T) {
		h := newHarness(t)
		h.withTeams()
		ref := h.db.teamsOf().refs[search]
		ref.Deleted = true
		h.db.teamsOf().refs[search] = ref
		a := h.teamAlert("x", "", byLabel(search))
		b := h.teamAlert("x", "")
		h.changes(t, ingest.ChangeFired, a, b)
		g := h.groupOf(t, a)
		if got := h.teamEntries(g); got != nil || h.db.calls["AddGroupTeams"] != 0 ||
			h.db.calls["AddUnknownTeamValues"] != 0 {
			t.Errorf("entries %v, calls %v", got, h.db.calls)
		}
		if got := h.groupTeamSlugs(t, g); len(got) != 0 {
			t.Errorf("teams %v", got)
		}
	})
}

// TestTeamFilter is C-09.FR-13, C-25.FR-2 and C-25.AC-11 on the parameters of the list: no Team does not filter, a
// Team's public_id selects it, deleted or not, mine the Teams of the calling User, none when they are in none; mine
// for a Service account and an unknown Team are refused at /query/team.
func TestTeamFilter(t *testing.T) {
	h := newHarness(t)
	h.withTeams()
	h.db.teamsOf().users[bobID] = []int64{payments, checkout}
	resolve := func(team string, me audit.Actor) (query, error) {
		return h.svc.resolve(t.Context(), Filter{Team: team, Me: me}, t0)
	}
	if q, err := resolve("", bob.Actor); err != nil || q.teamSet || q.teamIDs == nil || len(q.teamIDs) != 0 {
		t.Errorf("no team: %+v %v", q, err)
	}
	if q, err := resolve("tmsearchaaaaaa", bob.Actor); err != nil || !q.teamSet || !slices.Equal(q.teamIDs,
		[]int64{search}) {
		t.Errorf("a team: %+v %v", q, err)
	}
	if q, err := resolve(TeamMine, bob.Actor); err != nil || !q.teamSet || !slices.Equal(q.teamIDs,
		[]int64{payments, checkout}) {
		t.Errorf("mine: %+v %v", q, err)
	}
	if q, err := resolve(TeamMine, alice.Actor); err != nil || !q.teamSet || q.teamIDs == nil || len(q.teamIDs) != 0 {
		t.Errorf("mine without teams: %+v %v", q, err)
	}
	for team, code := range map[string]string{"TMUNKNOWNAAAAA": CodeUnknownID, "RTDBAAAAAAAAAA": CodeUnknownID} {
		var fe *FieldError
		if _, err := resolve(team, bob.Actor); !errors.As(err, &fe) || fe.Pointer != "/query/team" || fe.Code != code {
			t.Errorf("%s: %v", team, err)
		}
	}
	var fe *FieldError
	if _, err := resolve(TeamMine, robot.Actor); !errors.As(err, &fe) || fe.Pointer != "/query/team" ||
		fe.Code != CodeUnsupported {
		t.Errorf("mine for a service account: %v", err)
	}
	h.db.fail["ListUserTeamIDs"] = errBoom
	if _, err := resolve(TeamMine, bob.Actor); !errors.Is(err, errBoom) {
		t.Errorf("failed read of the caller's teams: %v", err)
	}
	h.db.fail["ListTeamsByPublicID"] = errBoom
	if _, err := resolve("TMSEARCHAAAAAA", bob.Actor); !errors.Is(err, errBoom) {
		t.Errorf("failed read of the team: %v", err)
	}
	h.db.fail["ListUserTeamIDs"], h.db.fail["ListTeamsByPublicID"] = nil, nil
	if _, err := h.svc.Counts(t.Context(), Filter{Team: "TMSEARCHAAAAAA", Me: bob.Actor}); err != nil {
		t.Errorf("counts with a team: %v", err)
	}
}

// TestTeamsFailures: a failed read or write of the Teams fails the Snapshot's change or the read, and changes nothing.
func TestTeamsFailures(t *testing.T) {
	for _, q := range []string{"ListAlertTeams", "AddGroupTeams", "AddUnknownTeamValues"} {
		t.Run(q, func(t *testing.T) {
			h := newHarness(t)
			h.withTeams()
			a := h.teamAlert("x", "Payments", byLabel(payments))
			h.db.fail[q] = errBoom
			if _, err := h.apply(ingest.ChangeFired, a); !errors.Is(err, errBoom) {
				t.Errorf("got %v", err)
			}
		})
	}
	h := newHarness(t)
	h.withTeams()
	a := h.teamAlert("x", "", byLabel(payments))
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	reads := map[string]func() error{
		"ListGroupTeams": func() error {
			_, err := h.svc.Get(t.Context(), g.PublicID)
			if err == nil {
				_, err = h.svc.List(t.Context(), ListRequest{Limit: 10})
			}
			return err
		},
		"ListGroupAlertTeams": func() error {
			_, err := h.svc.Alerts(t.Context(), g.PublicID, AlertFilter{Limit: 10})
			return err
		},
		"ListTeamRefs": func() error {
			_, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 10})
			return err
		},
	}
	for _, q := range slices.Sorted(maps.Keys(reads)) {
		h.db.fail[q] = errBoom
		if err := reads[q](); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
		h.db.fail[q] = nil
	}
	h.db.fail["ListGroupTeams"] = nil
	h.db.before["ListGroupTeams"] = func() { h.db.fail["ListGroupTeams"] = errBoom }
	if _, err := h.svc.List(t.Context(), ListRequest{Limit: 10}); !errors.Is(err, errBoom) {
		t.Errorf("list: %v", err)
	}
}
