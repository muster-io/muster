// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/teams/dbgen"
)

// fakeConnection is a row of connections as the Team mentions read it: what the Connection can mention.
type fakeConnection struct {
	id             int64
	name, typ      string
	usergroupsRead pgtype.Bool
	groupMentions  bool
	gatewayOnly    bool
	deleted        bool
}

// fakeMention is a row of team_mentions.
type fakeMention struct {
	connection         int64
	group, usergroupID string
}

func (s *fakeStore) connByID(id int64) (string, *fakeConnection) {
	for p, c := range s.connections {
		if c.id == id {
			return p, c
		}
	}
	return "", nil
}

func (s *fakeStore) ListTeamMentions(_ context.Context, arg dbgen.ListTeamMentionsParams) (
	[]dbgen.ListTeamMentionsRow, error) {
	if err := s.fail["ListTeamMentions"]; err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []dbgen.ListTeamMentionsRow
	for _, team := range slices.Sorted(slices.Values(arg.TeamIds)) {
		var rows []dbgen.ListTeamMentionsRow
		for _, m := range s.mentions[team] {
			p, c := s.connByID(m.connection)
			if c == nil || c.deleted {
				continue
			}
			rows = append(rows, dbgen.ListTeamMentionsRow{TeamID: team, ConnectionID: c.id, GroupName: m.group,
				SlackUsergroupID: pgtype.Text{String: m.usergroupID, Valid: m.usergroupID != ""}, PublicID: p,
				Name: c.name, Type: c.typ, SlackUsergroupsRead: c.usergroupsRead, GroupMentions: c.groupMentions,
				Messenger: !c.gatewayOnly})
		}
		slices.SortFunc(rows, func(a, b dbgen.ListTeamMentionsRow) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.PublicID, b.PublicID))
		})
		out = append(out, rows...)
	}
	return out, nil
}

func (s *fakeStore) ListMentionConnections(_ context.Context, org int64) ([]dbgen.ListMentionConnectionsRow, error) {
	if err := s.fail["ListMentionConnections"]; err != nil || org != orgID {
		return nil, err
	}
	var out []dbgen.ListMentionConnectionsRow
	for p, c := range s.connections {
		if c.deleted || c.gatewayOnly {
			continue
		}
		out = append(out, dbgen.ListMentionConnectionsRow{ID: c.id, PublicID: p, Name: c.name, Type: c.typ,
			SlackUsergroupsRead: c.usergroupsRead, GroupMentions: c.groupMentions})
	}
	slices.SortFunc(out, func(a, b dbgen.ListMentionConnectionsRow) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.PublicID, b.PublicID))
	})
	return out, nil
}

func (s *fakeStore) ResolveMentionConnections(_ context.Context, arg dbgen.ResolveMentionConnectionsParams) (
	[]dbgen.ResolveMentionConnectionsRow, error) {
	if err := s.fail["ResolveMentionConnections"]; err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []dbgen.ResolveMentionConnectionsRow
	for _, p := range arg.PublicIds {
		if c := s.connections[p]; c != nil && !c.deleted {
			out = append(out, dbgen.ResolveMentionConnectionsRow{ID: c.id, PublicID: p, Name: c.name, Type: c.typ,
				SlackUsergroupsRead: c.usergroupsRead, GroupMentions: c.groupMentions, Messenger: !c.gatewayOnly})
		}
	}
	return out, nil
}

func (s *fakeStore) DeleteTeamMentions(_ context.Context, arg dbgen.DeleteTeamMentionsParams) error {
	if err := s.fail["DeleteTeamMentions"]; err != nil || arg.OrgID != orgID {
		return err
	}
	delete(s.mentions, arg.TeamID)
	return nil
}

func (s *fakeStore) InsertTeamMentions(_ context.Context, arg dbgen.InsertTeamMentionsParams) error {
	if err := s.fail["InsertTeamMentions"]; err != nil || arg.OrgID != orgID {
		return err
	}
	if s.mentions == nil {
		s.mentions = map[int64][]fakeMention{}
	}
	for i, c := range arg.ConnectionIds {
		s.mentions[arg.TeamID] = append(s.mentions[arg.TeamID], fakeMention{connection: c, group: arg.GroupNames[i],
			usergroupID: arg.UsergroupIds[i]})
	}
	return nil
}

func (s *fakeStore) GetTeamMention(_ context.Context, arg dbgen.GetTeamMentionParams) (dbgen.GetTeamMentionRow,
	error) {
	if err := s.fail["GetTeamMention"]; err != nil || arg.OrgID != orgID {
		return dbgen.GetTeamMentionRow{}, err
	}
	if t := s.byID(arg.TeamID); t == nil || t.deleted {
		return dbgen.GetTeamMentionRow{}, pgx.ErrNoRows
	}
	for _, m := range s.mentions[arg.TeamID] {
		if _, c := s.connByID(m.connection); c != nil && !c.deleted && c.id == arg.ConnectionID {
			return dbgen.GetTeamMentionRow{GroupName: m.group, Type: c.typ,
				SlackUsergroupID:    pgtype.Text{String: m.usergroupID, Valid: m.usergroupID != ""},
				SlackUsergroupsRead: c.usergroupsRead, GroupMentions: c.groupMentions, Messenger: !c.gatewayOnly}, nil
		}
	}
	return dbgen.GetTeamMentionRow{}, pgx.ErrNoRows
}

// The Connections of the tests of the Team mentions.
const (
	connMM       = "CN0000000000MM"
	connTG       = "CN0000000000TG"
	connSlack    = "CN0000000000SK"
	connBridge   = "CN0000000000BR"
	connGateway  = "CN0000000000GW"
	connDeleted  = "CN0000000000DX"
	slackPayTeam = "S0FAKEPAYTEAM"
)

// mentionConnections gives the store a Mattermost, a Telegram, a Slack Connection without usergroups:read, a Bridge
// without group mentions, a Notification gateway only and a deleted Connection.
func mentionConnections(s *fakeStore) {
	s.connections = map[string]*fakeConnection{
		connMM:      {id: 61, name: "Mattermost", typ: connectionMattermost},
		connTG:      {id: 62, name: "Telegram", typ: connectionTelegram},
		connSlack:   {id: 63, name: "Slack", typ: connectionSlack, usergroupsRead: pgtype.Bool{Bool: false, Valid: true}},
		connBridge:  {id: 64, name: "Bridge", typ: connectionBridge},
		connGateway: {id: 65, name: "Gateway", typ: connectionBridge, groupMentions: true, gatewayOnly: true},
		connDeleted: {id: 66, name: "Old", typ: connectionMattermost, deleted: true},
	}
}

// TestTeamMentions covers C-25.FR-8, FR-11 and AC-7: a Team takes one Team mention per Connection, saved with the
// Team's version and in the diff of team.created and team.updated; one for a Mattermost Connection is saved, one for
// Telegram, for a Slack Connection whose last check found no usergroups:read, for a Bridge that declares no group
// mentions and for a Notification gateway only is refused with group_mentions_unsupported at its connection_id; a Slack
// one needs the user group's id and the others refuse it; an update without them keeps them, and the same ones change
// nothing.
func TestTeamMentions(t *testing.T) {
	svc, s := newService(t)
	mentionConnections(s)
	created, err := svc.Create(t.Context(), by, Input{Name: "Payments", Slug: "payments",
		Mentions: &[]MentionInput{{ConnectionID: strings.ToLower(connMM), Group: "payments_team"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Mentions) != 1 || created.Mentions[0].MentionInput != (MentionInput{ConnectionID: connMM,
		Group: "payments_team"}) || !created.Mentions[0].Available || created.Mentions[0].UnavailableReason != "" ||
		created.Mentions[0].Connection != (MentionConnectionRef{PublicID: connMM, Name: "Mattermost",
			Type: connectionMattermost}) {
		t.Fatalf("created %+v", created.Mentions)
	}
	if last := s.audit[len(s.audit)-1]; last.Action != ActionCreated ||
		!strings.Contains(string(last.Diff), `"pointer":"/team_mentions"`) ||
		!strings.Contains(string(last.Diff), `"group":"payments_team"`) {
		t.Errorf("team.created %s", last.Diff)
	}

	for _, tc := range []struct {
		name     string
		mentions []MentionInput
		pointer  string
		code     string
	}{
		{"telegram", []MentionInput{{ConnectionID: connTG, Group: "x"}}, "/team_mentions/0/connection_id",
			CodeGroupMentionsUnsupported},
		{"slack without usergroups:read", []MentionInput{{ConnectionID: connMM, Group: "payments_team"},
			{ConnectionID: connSlack, Group: "payments-team", GroupID: slackPayTeam}},
			"/team_mentions/1/connection_id", CodeGroupMentionsUnsupported},
		{"bridge without group mentions", []MentionInput{{ConnectionID: connBridge, Group: "payments"}},
			"/team_mentions/0/connection_id", CodeGroupMentionsUnsupported},
		{"a gateway only", []MentionInput{{ConnectionID: connGateway, Group: "payments"}},
			"/team_mentions/0/connection_id", CodeGroupMentionsUnsupported},
		{"unknown", []MentionInput{{ConnectionID: "CN0000000000ZZ", Group: "x"}}, "/team_mentions/0/connection_id",
			CodeUnknownID},
		{"deleted", []MentionInput{{ConnectionID: connDeleted, Group: "x"}}, "/team_mentions/0/connection_id",
			CodeUnknownID},
		{"not a connection id", []MentionInput{{ConnectionID: "TM0000000000AA", Group: "x"}},
			"/team_mentions/0/connection_id", CodeUnknownID},
		{"twice", []MentionInput{{ConnectionID: connMM, Group: "a"}, {ConnectionID: connMM, Group: "b"}},
			"/team_mentions/1/connection_id", CodeDuplicate},
		{"a group with a space", []MentionInput{{ConnectionID: connMM, Group: "payments team"}},
			"/team_mentions/0/group", CodeInvalidFormat},
		{"an empty group", []MentionInput{{ConnectionID: connMM}}, "/team_mentions/0/group", CodeInvalidFormat},
		{"a group of 65", []MentionInput{{ConnectionID: connMM, Group: strings.Repeat("g", 65)}},
			"/team_mentions/0/group", CodeInvalidFormat},
		{"a group id for mattermost", []MentionInput{{ConnectionID: connMM, Group: "g", GroupID: slackPayTeam}},
			"/team_mentions/0/group_id", CodeUnsupported},
	} {
		_, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{Name: "Payments", Slug: "payments",
			Mentions: &tc.mentions})
		if f := fieldError(t, err); f.Pointer != tc.pointer || f.Code != tc.code || f.Detail == "" {
			t.Errorf("%s: %+v", tc.name, f)
		}
	}
	tooMany := make([]MentionInput, MaxTeamMentions+1)
	if _, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{Name: "Payments", Slug: "payments",
		Mentions: &tooMany}); fieldError(t, err).Code != CodeOutOfRange {
		t.Errorf("too many = %v", err)
	}
	if got, _ := svc.Get(t.Context(), created.PublicID); got.Version != created.Version {
		t.Fatalf("a refused update changed the team: %d", got.Version)
	}

	kept, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{Name: "Payments", Slug: "payments"})
	if err != nil || kept.Version != created.Version || len(kept.Mentions) != 1 {
		t.Fatalf("an update without team mentions = %+v, %v", kept.Mentions, err)
	}
	same, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{Name: "Payments", Slug: "payments",
		Mentions: &[]MentionInput{{ConnectionID: connMM, Group: "payments_team"}}})
	if err != nil || same.Version != created.Version {
		t.Fatalf("the same team mentions = %+v, %v", same, err)
	}

	s.connections[connSlack].usergroupsRead = pgtype.Bool{Bool: true, Valid: true}
	s.connections[connBridge].groupMentions = true
	for _, tc := range []struct {
		mentions []MentionInput
		pointer  string
		code     string
	}{
		{[]MentionInput{{ConnectionID: connSlack, Group: "payments-team"}}, "/team_mentions/0/group_id", CodeRequired},
		{[]MentionInput{{ConnectionID: connSlack, Group: "payments-team", GroupID: "U0FAKEALICE"}},
			"/team_mentions/0/group_id", CodeInvalidFormat},
		{[]MentionInput{{ConnectionID: connBridge, Group: "payments", GroupID: slackPayTeam}},
			"/team_mentions/0/group_id", CodeUnsupported},
	} {
		_, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{Name: "Payments", Slug: "payments",
			Mentions: &tc.mentions})
		if f := fieldError(t, err); f.Pointer != tc.pointer || f.Code != tc.code {
			t.Errorf("%+v: %+v", tc.mentions, f)
		}
	}
	version := created.Version
	updated, err := svc.Update(t.Context(), by, created.PublicID, &version, Input{Name: "Payments", Slug: "payments",
		Mentions: &[]MentionInput{{ConnectionID: connSlack, Group: "payments-team", GroupID: slackPayTeam},
			{ConnectionID: connMM, Group: "payments_team"}, {ConnectionID: connBridge, Group: "payments"}}})
	if err != nil || updated.Version != created.Version+1 || len(updated.Mentions) != 3 {
		t.Fatalf("update = %+v, %v", updated.Mentions, err)
	}
	var names []string
	for _, m := range updated.Mentions {
		names = append(names, m.Connection.Name)
		if !m.Available {
			t.Errorf("unavailable %+v", m)
		}
	}
	if !slices.Equal(names, []string{"Bridge", "Mattermost", "Slack"}) || updated.Mentions[2].GroupID != slackPayTeam {
		t.Errorf("by name %v, %+v", names, updated.Mentions)
	}
	last := s.audit[len(s.audit)-1]
	var diff []struct {
		Pointer string         `json:"pointer"`
		Before  []MentionInput `json:"before"`
		After   []MentionInput `json:"after"`
	}
	if err := json.Unmarshal(last.Diff, &diff); err != nil || last.Action != ActionUpdated || len(diff) != 1 ||
		diff[0].Pointer != "/team_mentions" || len(diff[0].Before) != 1 || !slices.Equal(diff[0].After,
		[]MentionInput{{ConnectionID: connBridge, Group: "payments"}, {ConnectionID: connMM, Group: "payments_team"},
			{ConnectionID: connSlack, Group: "payments-team", GroupID: slackPayTeam}}) {
		t.Errorf("team.updated %s, %v", last.Diff, err)
	}
	if _, err := svc.Update(t.Context(), by, created.PublicID, &version, Input{Name: "Payments",
		Slug: "payments", Mentions: &[]MentionInput{}}); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("a stale If-Match = %v", err)
	}
	cleared, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{Name: "Payments", Slug: "payments",
		Mentions: &[]MentionInput{}})
	if err != nil || len(cleared.Mentions) != 0 || len(s.mentions[created.ID]) != 0 {
		t.Fatalf("cleared = %+v, %v", cleared.Mentions, err)
	}
}

// TestMentionConnections covers C-25.FR-8 and C-28.FR-2: a read lists under MentionConnections every Connection that
// is not deleted and may have a Destination — not a Notification gateway only — by name, with the Team's Team mention
// or none, whether it can mention a group and why not; a Team mention whose Connection loses the ability stays, reads
// unavailable with the reason and mentions nobody (ForConnection); one of a deleted Connection is not read.
func TestMentionConnections(t *testing.T) {
	svc, s := newService(t)
	mentionConnections(s)
	s.connections[connSlack].usergroupsRead = pgtype.Bool{Bool: true, Valid: true}
	s.connections[connBridge].groupMentions = true
	created, err := svc.Create(t.Context(), by, Input{Name: "Payments", Slug: "payments",
		Mentions: &[]MentionInput{{ConnectionID: connMM, Group: "payments_team"},
			{ConnectionID: connSlack, Group: "payments-team", GroupID: slackPayTeam},
			{ConnectionID: connBridge, Group: "payments"}}})
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		name, reason string
		available    bool
		group        string
	}
	read := func() []row {
		t.Helper()
		got, err := svc.Get(t.Context(), created.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		var out []row
		for _, c := range got.MentionConnections {
			r := row{name: c.Connection.Name, reason: c.UnavailableReason, available: c.Available}
			if c.Mention != nil {
				r.group = c.Mention.Group
				if c.Mention.Available != c.Available || c.Mention.UnavailableReason != c.UnavailableReason {
					t.Errorf("the mention of %s disagrees: %+v", r.name, c.Mention)
				}
			}
			out = append(out, r)
		}
		return out
	}
	if got := read(); !slices.Equal(got, []row{{"Bridge", "", true, "payments"}, {"Mattermost", "", true,
		"payments_team"}, {"Slack", "", true, "payments-team"}, {"Telegram", ReasonTelegramNoGroups, false, ""}}) {
		t.Fatalf("mention connections %+v", got)
	}
	tm, ok, err := svc.ForConnection(t.Context(), created.ID, 63)
	if err != nil || !ok || tm != (TeamMention{Group: "payments-team", GroupID: slackPayTeam}) {
		t.Fatalf("ForConnection slack = %+v, %v, %v", tm, ok, err)
	}

	s.connections[connSlack].usergroupsRead = pgtype.Bool{Bool: false, Valid: true}
	s.connections[connBridge].groupMentions = false
	s.connections[connMM].deleted = true
	if got := read(); !slices.Equal(got, []row{{"Bridge", ReasonBridgeNoGroupMentions, false, "payments"},
		{"Slack", ReasonUsergroupsReadMissing, false, "payments-team"},
		{"Telegram", ReasonTelegramNoGroups, false, ""}}) {
		t.Fatalf("after the abilities were lost %+v", got)
	}
	got, _ := svc.Get(t.Context(), created.PublicID)
	if len(got.Mentions) != 2 || got.Mentions[1].Available ||
		got.Mentions[1].UnavailableReason != ReasonUsergroupsReadMissing {
		t.Fatalf("team mentions %+v", got.Mentions)
	}
	for _, id := range []int64{63, 64, 61, 62, 99} {
		if tm, ok, err := svc.ForConnection(t.Context(), created.ID, id); err != nil || ok || tm != (TeamMention{}) {
			t.Errorf("ForConnection %d = %+v, %v, %v", id, tm, ok, err)
		}
	}
	// An unchanged stored Team mention of a Connection that lost the ability is kept; a changed or new one is refused.
	all := []MentionInput{{ConnectionID: connSlack, Group: "payments-team", GroupID: slackPayTeam},
		{ConnectionID: connBridge, Group: "payments"}}
	resaved, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{Name: "Payments", Slug: "payments",
		Mentions: &all})
	if err != nil || resaved.Version != got.Version || len(resaved.Mentions) != 2 || resaved.Mentions[0].Available ||
		resaved.Mentions[1].Available {
		t.Fatalf("the unchanged set = %+v, %v", resaved.Mentions, err)
	}
	for i, changed := range [][]MentionInput{
		{{ConnectionID: connSlack, Group: "payments-team", GroupID: "S0OTHER"}, all[1]},
		{all[0], {ConnectionID: connBridge, Group: "payments-2"}},
		{all[0], all[1], {ConnectionID: connTG, Group: "x"}},
	} {
		_, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{Name: "Payments", Slug: "payments",
			Mentions: &changed})
		if f := fieldError(t, err); f.Code != CodeGroupMentionsUnsupported {
			t.Errorf("changed set %d: %+v", i, f)
		}
	}
	if renamed, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{Name: "Payments 2",
		Slug: "payments", Mentions: &all}); err != nil || renamed.Name != "Payments 2" || len(renamed.Mentions) != 2 {
		t.Fatalf("another change with the unchanged set = %+v, %v", renamed, err)
	}

	s.connections[connSlack].usergroupsRead = pgtype.Bool{}
	if got := read(); got[1].reason != ReasonUsergroupsReadMissing {
		t.Errorf("a Slack Connection never checked = %+v", got)
	}
	s.fail["GetTeamMention"] = errors.New("boom")
	if _, _, err := svc.ForConnection(t.Context(), created.ID, 63); err == nil {
		t.Error("a failed read")
	}
	delete(s.fail, "GetTeamMention")

	page, err := svc.List(t.Context(), ListFilter{Limit: 10})
	if err != nil || len(page.Teams) != 1 || len(page.Teams[0].MentionConnections) != 3 ||
		len(page.Teams[0].Mentions) != 2 {
		t.Fatalf("listed %+v, %v", page, err)
	}
	for _, name := range []string{"ListTeamMentions", "ListMentionConnections"} {
		s.fail[name] = errors.New("boom")
		if _, err := svc.Get(t.Context(), created.PublicID); err == nil {
			t.Errorf("%s failed, the read did not", name)
		}
		delete(s.fail, name)
	}
}

// TestDeleteRemovesMentions covers C-25.FR-10 and FR-11: deleting a Team removes its Team mentions in its transaction,
// and ForConnection then answers none.
func TestDeleteRemovesMentions(t *testing.T) {
	svc, s := newService(t)
	mentionConnections(s)
	created, err := svc.Create(t.Context(), by, Input{Name: "Payments", Slug: "payments",
		Mentions: &[]MentionInput{{ConnectionID: connMM, Group: "payments_team"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.ForConnection(t.Context(), created.ID, 61); !ok {
		t.Fatal("no team mention before the deletion")
	}
	s.fail["DeleteTeamMentions"] = errors.New("boom")
	if err := svc.Delete(t.Context(), by, created.PublicID, nil); err == nil {
		t.Fatal("a failed removal deleted the team")
	}
	delete(s.fail, "DeleteTeamMentions")
	if err := svc.Delete(t.Context(), by, created.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.mentions[created.ID]) != 0 {
		t.Fatalf("team mentions left %+v", s.mentions)
	}
	if _, ok, err := svc.ForConnection(t.Context(), created.ID, 61); ok || err != nil {
		t.Fatalf("ForConnection after the deletion = %v, %v", ok, err)
	}
}

// TestMentionWriteFailures: a failed read or write of the Team mentions fails the save.
func TestMentionWriteFailures(t *testing.T) {
	svc, s := newService(t)
	mentionConnections(s)
	for _, name := range []string{"ResolveMentionConnections", "InsertTeamMentions", "DeleteTeamMentions"} {
		s.fail[name] = errors.New("boom " + name)
		_, err := svc.Create(t.Context(), by, Input{Name: "Payments " + name, Slug: strings.ToLower(name),
			Mentions: &[]MentionInput{{ConnectionID: connMM, Group: "payments_team"}}})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s: %v", name, err)
		}
		delete(s.fail, name)
	}
}
