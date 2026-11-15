// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/slack"
)

func (s *memStore) SetGroupsRead(_ context.Context, a dbgen.SetGroupsReadParams) (int64, error) {
	if err := s.fail["SetGroupsRead"]; err != nil {
		return 0, err
	}
	s.groupsRead = append(s.groupsRead, a)
	r := s.rows[a.ID]
	if r == nil || r.deleted || r.row.Type != connections.TypeMattermost || r.row.Version != a.Version {
		return 0, nil
	}
	r.row.MattermostGroupsRead = pgtype.Bool{Bool: a.GroupsRead, Valid: true}
	return 1, nil
}

func (s *memStore) LockConnectionTeams(_ context.Context, a dbgen.LockConnectionTeamsParams) ([]int64, error) {
	if err := s.fail["LockConnectionTeams"]; err != nil {
		return nil, err
	}
	var out []int64
	for _, m := range s.mentions {
		if m.ConnectionPublicID == a.PublicID && !slices.Contains(out, m.TeamID) {
			out = append(out, m.TeamID)
		}
	}
	slices.Sort(out)
	s.lockedTeams = append(s.lockedTeams, out)
	return out, nil
}

// ListConnectionTeams lists the Teams with a Team mention for the Connection, with the next of newTeams, a Team that
// gave it its first Team mention while the deletion waited, until there are none.
func (s *memStore) ListConnectionTeams(_ context.Context, a dbgen.ListConnectionTeamsParams) ([]int64, error) {
	if err := s.fail["ListConnectionTeams"]; err != nil {
		return nil, err
	}
	var out []int64
	for _, m := range s.mentions {
		if m.ConnectionID == a.ConnectionID && !slices.Contains(out, m.TeamID) {
			out = append(out, m.TeamID)
		}
	}
	if len(s.newTeams) > 0 {
		out, s.newTeams = append(out, s.newTeams[0]), s.newTeams[1:]
	}
	return out, nil
}

func (s *memStore) ListConnectionTeamMentions(_ context.Context, a dbgen.ListConnectionTeamMentionsParams) (
	[]dbgen.ListConnectionTeamMentionsRow, error) {
	if err := s.fail["ListConnectionTeamMentions"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListConnectionTeamMentionsRow
	for _, m := range s.mentions {
		if slices.Contains(a.TeamIds, m.TeamID) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(x, y dbgen.ListConnectionTeamMentionsRow) int {
		if x.TeamID != y.TeamID {
			return int(x.TeamID - y.TeamID)
		}
		return strings.Compare(x.ConnectionPublicID, y.ConnectionPublicID)
	})
	return out, nil
}

func (s *memStore) DeleteConnectionTeamMentions(_ context.Context, a dbgen.DeleteConnectionTeamMentionsParams) error {
	if err := s.fail["DeleteConnectionTeamMentions"]; err != nil {
		return err
	}
	s.mentions = slices.DeleteFunc(s.mentions, func(m dbgen.ListConnectionTeamMentionsRow) bool {
		return m.ConnectionID == a.ConnectionID && slices.Contains(a.TeamIds, m.TeamID)
	})
	return nil
}

func (s *memStore) BumpTeamVersions(_ context.Context, a dbgen.BumpTeamVersionsParams) error {
	if err := s.fail["BumpTeamVersions"]; err != nil {
		return err
	}
	s.bumps = append(s.bumps, a)
	return nil
}

// TestMattermostCheckGroups covers C-25.FR-8: the check of a Mattermost Connection gains the step groups, after token,
// which reads the groups as a bot with the role Member may (F-122) and passes, recording groups_read true; a server
// that refuses the read leaves the step passing with a message that says so, no warning, and groups_read false; a
// token that fails skips it and records nothing; a check of an earlier version records nothing.
func TestMattermostCheckGroups(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	if c.MattermostGroupsRead != nil {
		t.Fatalf("groups_read before a check = %v", *c.MattermostGroupsRead)
	}
	res, err := e.svc.Check(t.Context(), c.PublicID, nil)
	if err != nil || !res.OK || len(res.Steps) != 2 || res.Steps[1].Name != mattermost.StepGroups ||
		!res.Steps[1].OK || res.Steps[1].Message != "" || res.Steps[1].Latency != 7*time.Millisecond ||
		res.Steps[1].Via != res.Steps[0].Via || !res.Steps[1].Called() {
		t.Fatalf("check = %+v, %v", res, err)
	}
	got, err := e.svc.Get(t.Context(), c.PublicID)
	if err != nil || got.MattermostGroupsRead == nil || !*got.MattermostGroupsRead {
		t.Fatalf("groups_read after a check = %+v, %v", got.MattermostGroupsRead, err)
	}
	if n := len(e.store.groupsRead); n != 1 || e.store.groupsRead[0].Version != c.Version {
		t.Fatalf("recorded %+v", e.store.groupsRead)
	}

	e.configure(t, `{"groups_forbidden":true}`)
	res, err = e.svc.Check(t.Context(), c.PublicID, nil)
	if err != nil || !res.OK || len(res.Steps) != 2 || !res.Steps[1].OK || res.Steps[1].Code != "" ||
		!strings.Contains(res.Steps[1].Message, "api.context.permissions.app_error") ||
		slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "group") }) {
		t.Fatalf("groups refused = %+v, %v", res, err)
	}
	if got, _ := e.svc.Get(t.Context(), c.PublicID); got.MattermostGroupsRead == nil || *got.MattermostGroupsRead {
		t.Fatalf("groups_read after a refused read = %v", got.MattermostGroupsRead)
	}

	e.configure(t, `{"groups_forbidden":false,"revoked_tokens":["`+botToken+`"]}`)
	before := len(e.store.groupsRead)
	if res, err := e.svc.Check(t.Context(), c.PublicID, nil); err != nil || res.OK || len(res.Steps) != 1 ||
		len(e.store.groupsRead) != before {
		t.Fatalf("a refused token = %+v, %v, %d records", res, err, len(e.store.groupsRead))
	}
	e.configure(t, `{"revoked_tokens":[]}`)
	if _, err := e.svc.Check(t.Context(), c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.update(t, c.PublicID, input("mm", e.fake.URL())); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(t.Context(), c.PublicID); got.MattermostGroupsRead != nil {
		t.Fatalf("a new bot token keeps groups_read %v", *got.MattermostGroupsRead)
	}

	e.store.fail["SetGroupsRead"] = errors.New("boom")
	if _, err := e.svc.Check(t.Context(), c.PublicID, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a failed record = %v", err)
	}
	delete(e.store.fail, "SetGroupsRead")
	e.path.limited = true
	if _, err := e.svc.Check(t.Context(), c.PublicID, nil); !errors.As(err, new(*delivery.LimitedError)) {
		t.Fatalf("limited = %v", err)
	}
}

// TestSlackCheckUsergroupsRead covers C-24.FR-1 and FR-2 revised: the step scopes reports usergroups:read as present or
// missing without failing, the Connection keeps usergroups_read from its last passing check, a check that no longer
// finds the scope sets it false, and a new bot token forgets it.
func TestSlackCheckUsergroupsRead(t *testing.T) {
	e, f := newSlackEnv(t)
	c := e.createSlack(t, slackInput("sl", connections.EventModeHTTP))
	if c.SlackUsergroupsRead != nil {
		t.Fatalf("usergroups_read before a check = %v", *c.SlackUsergroupsRead)
	}
	check := func(scopes string, want bool) {
		t.Helper()
		cfg := fakeslack.DefaultConfig()
		cfg.Scopes[fakeslack.AppA] = scopes
		if err := f.SetConfig(cfg); err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Check(t.Context(), c.PublicID, nil)
		if err != nil || !res.OK || len(res.Steps) != 2 || !slices.Equal(res.Steps[1].OptionalScopes,
			[]slack.OptionalScope{{Scope: "usergroups:read", Present: want}}) {
			t.Fatalf("%q: check = %+v, %v", scopes, res, err)
		}
		got, err := e.svc.Get(t.Context(), c.PublicID)
		if err != nil || got.SlackUsergroupsRead == nil || *got.SlackUsergroupsRead != want {
			t.Fatalf("%q: usergroups_read = %v, %v", scopes, got.SlackUsergroupsRead, err)
		}
	}
	check(fakeslack.DefaultScopes, false)
	check(fakeslack.DefaultScopes+",usergroups:read", true)
	check(fakeslack.DefaultScopes, false)
	check(fakeslack.DefaultScopes+",usergroups:read", true)
	in := slackInput("sl", connections.EventModeHTTP)
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(t.Context(), c.PublicID); got.SlackUsergroupsRead != nil {
		t.Fatalf("a new bot token keeps usergroups_read %v", *got.SlackUsergroupsRead)
	}
}

// TestGroups covers C-25.FR-8: listConnectionGroups lists the groups of a Mattermost Connection that may be mentioned,
// by name with their member counts, and the user groups of a Slack Connection whose last check found
// usergroups:read, by handle, each filtered by q, through the interactive path limited by the Connection; a refused
// read is a *MessengerError, a Slack 429 and no limiter token a *delivery.LimitedError; a Telegram or Bridge
// Connection, and a Slack one without the scope, answer ErrGroupsNotListed.
func TestGroups(t *testing.T) {
	e, f := newSlackEnv(t)
	ctx := t.Context()
	mm := e.create(t, "mm")
	one := int64(1)
	list, err := e.svc.Groups(ctx, mm.PublicID, "")
	if err != nil || list.Truncated || len(list.Groups) != 2 ||
		list.Groups[0].Name != fakemattermost.GroupDevops || list.Groups[1].Name != fakemattermost.GroupPayments ||
		*list.Groups[0].MemberCount != one || list.Groups[1].DisplayName != "Payments team" {
		t.Fatalf("mattermost groups = %+v, %v", list, err)
	}
	if list, err := e.svc.Groups(ctx, mm.PublicID, " PAY"); err != nil || len(list.Groups) != 1 ||
		list.Groups[0].Name != fakemattermost.GroupPayments {
		t.Fatalf("q = %+v, %v", list, err)
	}
	if list, err := e.svc.Groups(ctx, mm.PublicID, "nothing"); err != nil || list.Groups == nil ||
		len(list.Groups) != 0 {
		t.Fatalf("no match = %#v, %v", list, err)
	}
	for _, s := range e.path.subjects {
		if s.Connection == nil || *s.Connection != mm.ID || s.Destination != nil {
			t.Errorf("subject %+v", s)
		}
	}
	e.configure(t, `{"groups_forbidden":true}`)
	if _, err := e.svc.Groups(ctx, mm.PublicID, ""); !isMessengerError(err, "api.context.permissions.app_error") {
		t.Fatalf("refused = %v", err)
	}
	e.configure(t, `{"groups_forbidden":false}`)

	sl := e.createSlack(t, slackInput("sl", connections.EventModeHTTP))
	if _, err := e.svc.Groups(ctx, sl.PublicID, ""); !errors.Is(err, connections.ErrGroupsNotListed) {
		t.Fatalf("slack before a check = %v", err)
	}
	cfg := fakeslack.DefaultConfig()
	cfg.Scopes[fakeslack.AppA] = fakeslack.DefaultScopes + ",usergroups:read"
	cfg.Usergroups[fakeslack.AppA]["S0FAKEDEVOPS"] = fakeslack.UserGroup{Handle: "devops", Name: "Platform"}
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Check(ctx, sl.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	list, err = e.svc.Groups(ctx, sl.PublicID, "")
	if err != nil || list.Truncated || len(list.Groups) != 2 || list.Groups[0] != (connections.Group{
		ID: "S0FAKEDEVOPS", Name: "devops", DisplayName: "Platform", MemberCount: list.Groups[0].MemberCount}) ||
		*list.Groups[0].MemberCount != 0 || list.Groups[1].ID != fakeslack.UserGroupPayments ||
		list.Groups[1].Name != "payments-team" || *list.Groups[1].MemberCount != 1 {
		t.Fatalf("slack groups = %+v, %v", list, err)
	}
	if list, err := e.svc.Groups(ctx, sl.PublicID, "platform"); err != nil || len(list.Groups) != 1 {
		t.Fatalf("slack q = %+v, %v", list, err)
	}
	if err := f.SetFault(fakeserver.Fault{Path: "/api/usergroups.list", Status: http.StatusTooManyRequests,
		RetryAfterSeconds: 30, Body: `{"ok":false,"error":"ratelimited"}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Groups(ctx, sl.PublicID, ""); !isLimitedFor(err, 30*time.Second) {
		t.Fatalf("slack 429 = %v", err)
	}
	if err := f.SetFault(fakeserver.Fault{Path: "/api/usergroups.list", Status: http.StatusOK,
		Body: `{"ok":false,"error":"missing_scope","needed":"usergroups:read"}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Groups(ctx, sl.PublicID, ""); !isMessengerError(err, "missing_scope") {
		t.Fatalf("slack refused = %v", err)
	}

	e.store.addTelegram()
	if _, err := e.svc.Groups(ctx, telegramID, ""); !errors.Is(err, connections.ErrGroupsNotListed) {
		t.Fatalf("telegram = %v", err)
	}
	br := e.createBridge(t, "br", "http://127.0.0.1:1", connections.EventDeliveryPoll)
	if _, err := e.svc.Groups(ctx, br.PublicID, ""); !errors.Is(err, connections.ErrGroupsNotListed) {
		t.Fatalf("bridge = %v", err)
	}
	if _, err := e.svc.Groups(ctx, "CNZZZZZZZZZZZZ", ""); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	e.path.limited = true
	if _, err := e.svc.Groups(ctx, mm.PublicID, ""); !isLimitedFor(err, 3*time.Second) {
		t.Fatalf("no limiter token = %v", err)
	}
	if _, err := e.svc.Groups(ctx, sl.PublicID, ""); !isLimitedFor(err, 3*time.Second) {
		t.Fatalf("no limiter token for slack = %v", err)
	}
}

// TestGroupsTruncated: a Mattermost page of mattermost.GroupsPageSize groups may have left some out.
func TestGroupsTruncated(t *testing.T) {
	e := newEnv(t)
	mm := e.create(t, "mm")
	groups := make([]map[string]any, mattermost.GroupsPageSize)
	for i := range groups {
		groups[i] = map[string]any{"id": "g" + strings.Repeat("x", i%7), "name": "group" + string(rune('a'+i%26)),
			"display_name": "G", "delete_at": 0}
	}
	groups[0]["delete_at"] = 1
	body, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/v4/groups", Status: http.StatusOK, Body: string(body),
		Times: 1}); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.Groups(t.Context(), mm.PublicID, "")
	if err != nil || !list.Truncated || len(list.Groups) != mattermost.GroupsPageSize-1 ||
		list.Groups[0].MemberCount != nil {
		t.Fatalf("a full page = %d groups, truncated %v, %v", len(list.Groups), list.Truncated, err)
	}
}

// TestDeleteDeletesTeamMentions covers C-25.FR-11: deleting a Connection deletes its Team mentions in the transaction
// of the deletion; each Team that had one gets a new version, an Audit log entry team.updated by the person who
// deleted the Connection with the Team mentions before and after, and its live hint; a Team mention of another
// Connection stays, and a failure undoes all of it.
func TestDeleteDeletesTeamMentions(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	other := e.create(t, "mm-2")
	mention := func(team int64, teamID string, conn connections.Connection, group string) dbgen.ListConnectionTeamMentionsRow {
		return dbgen.ListConnectionTeamMentionsRow{TeamID: team, TeamPublicID: teamID, TeamName: "team " + teamID,
			ConnectionID: conn.ID, ConnectionPublicID: conn.PublicID, GroupName: group}
	}
	e.store.mentions = []dbgen.ListConnectionTeamMentionsRow{
		mention(1, "TMAAAAAAAAAAA1", c, "payments_team"), mention(1, "TMAAAAAAAAAAA1", other, "payments"),
		mention(2, "TMAAAAAAAAAAA2", c, "devops_team"), mention(3, "TMAAAAAAAAAAA3", other, "search"),
	}

	e.store.fail["BumpTeamVersions"] = errors.New("boom")
	if err := e.svc.Delete(t.Context(), by, c.PublicID, nil); err == nil {
		t.Fatal("a failed bump deleted the connection")
	}
	if len(e.store.mentions) != 4 {
		t.Fatalf("a failed deletion kept %d team mentions", len(e.store.mentions))
	}
	delete(e.store.fail, "BumpTeamVersions")

	audits := len(e.store.audit)
	if err := e.svc.Delete(t.Context(), by, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.store.mentions) != 2 || slices.ContainsFunc(e.store.mentions,
		func(m dbgen.ListConnectionTeamMentionsRow) bool { return m.ConnectionID == c.ID }) {
		t.Fatalf("team mentions left %+v", e.store.mentions)
	}
	if len(e.store.bumps) != 1 || !slices.Equal(e.store.bumps[0].TeamIds, []int64{1, 2}) {
		t.Fatalf("bumps %+v", e.store.bumps)
	}
	if last := e.store.lockedTeams[len(e.store.lockedTeams)-1]; !slices.Equal(last, []int64{1, 2}) {
		t.Fatalf("the teams locked before the connection %v", last)
	}
	entries := e.store.audit[audits:]
	if len(entries) != 3 {
		t.Fatalf("audit entries %+v", entries)
	}
	both := `{"connection_id":"` + c.PublicID + `","group":"payments_team"},{"connection_id":"` + other.PublicID +
		`","group":"payments"}`
	if other.PublicID < c.PublicID {
		both = `{"connection_id":"` + other.PublicID + `","group":"payments"},{"connection_id":"` + c.PublicID +
			`","group":"payments_team"}`
	}
	want := map[string]string{
		"TMAAAAAAAAAAA1": `[{"pointer":"/team_mentions","before":[` + both + `],"after":[{` +
			`"connection_id":"` + other.PublicID + `","group":"payments"}]}]`,
		"TMAAAAAAAAAAA2": `[{"pointer":"/team_mentions","before":[{"connection_id":"` + c.PublicID +
			`","group":"devops_team"}],"after":[]}]`,
	}
	for _, a := range entries[:2] {
		if a.Action != "team.updated" || a.ResourceType.String != "team" || want[a.ResourcePublicID.String] == "" ||
			a.ActorKind != "user" || a.Transport != "api" || !strings.Contains(string(a.Details), `"reason":"connection_deleted"`) ||
			!strings.Contains(string(a.Details), c.PublicID) {
			t.Errorf("entry %+v %s %s", a, a.Diff, a.Details)
			continue
		}
		if !jsonEqual(t, a.Diff, want[a.ResourcePublicID.String]) {
			t.Errorf("diff of %s = %s", a.ResourcePublicID.String, a.Diff)
		}
	}
	if entries[2].Action != connections.ActionDeleted {
		t.Errorf("last entry %+v", entries[2])
	}
	var teamHints []string
	for _, h := range e.store.hints {
		if h.Type == "team" {
			teamHints = append(teamHints, h.ID)
		}
	}
	if !slices.Equal(teamHints, []string{"TMAAAAAAAAAAA1", "TMAAAAAAAAAAA2"}) {
		t.Errorf("team hints %v", teamHints)
	}

	audits = len(e.store.audit)
	if err := e.svc.Delete(t.Context(), by, e.create(t, "mm-3").PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.store.bumps) != 1 || len(e.store.audit)-audits != 2 {
		t.Fatalf("a Connection without Team mentions touched Teams: %+v", e.store.bumps)
	}
	for _, name := range []string{"LockConnectionTeams", "ListConnectionTeams", "ListConnectionTeamMentions",
		"DeleteConnectionTeamMentions"} {
		e.store.fail[name] = errors.New("boom " + name)
		if err := e.svc.Delete(t.Context(), by, other.PublicID, nil); err == nil ||
			!strings.Contains(err.Error(), name) && !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s failed: %v", name, err)
		}
		delete(e.store.fail, name)
	}
}

func isMessengerError(err error, text string) bool {
	m, ok := errors.AsType[*connections.MessengerError](err)
	return ok && strings.Contains(m.Text, text)
}

func isLimitedFor(err error, after time.Duration) bool {
	l, ok := errors.AsType[*delivery.LimitedError](err)
	return ok && l.RetryAfter == after
}

func jsonEqual(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// TestDeleteRetriesForANewTeamMention covers C-25.FR-11: a Team that gives the Connection its first Team mention while
// the deletion waits for the Teams it locked makes the deletion run again, which deletes the Connection then; when
// every attempt finds another, the deletion is ErrTeamMentionsChanged and nothing is deleted.
func TestDeleteRetriesForANewTeamMention(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	e.store.newTeams = []int64{7}
	if err := e.svc.Delete(t.Context(), by, c.PublicID, nil); err != nil {
		t.Fatalf("one new team mention = %v", err)
	}
	if len(e.store.lockedTeams) != 2 || e.store.committed == 0 {
		t.Fatalf("attempts %v", e.store.lockedTeams)
	}
	if _, err := e.svc.Get(t.Context(), c.PublicID); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("the connection after the retry = %v", err)
	}

	other := e.create(t, "mm-2")
	e.store.newTeams = []int64{7, 8, 9, 10}
	audits := len(e.store.audit)
	err := e.svc.Delete(t.Context(), by, other.PublicID, nil)
	if !errors.Is(err, connections.ErrTeamMentionsChanged) || len(e.store.newTeams) != 1 ||
		len(e.store.audit) != audits {
		t.Fatalf("a new team mention at every attempt = %v, %d left, %d audits", err, len(e.store.newTeams),
			len(e.store.audit)-audits)
	}
	if _, err := e.svc.Get(t.Context(), other.PublicID); err != nil {
		t.Fatalf("the connection after the refusal = %v", err)
	}
}
