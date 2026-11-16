// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mentions

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/mentions/dbgen"
)

// TestCan is the table of C-28.FR-2 cell by cell: whom each Destination type can mention on its Connection, and the
// reason of each refusal.
func TestCan(t *testing.T) {
	all := Connection{Type: TypeBridge, Messenger: true, GroupMentions: true,
		Everyone: []string{EveryoneChannel, EveryoneHere, EveryoneAll, EveryoneEveryone}}
	some := Connection{Type: TypeBridge, Messenger: true, Everyone: []string{EveryoneHere}}
	gateway := Connection{Type: TypeBridge, GroupMentions: true, Everyone: []string{EveryoneChannel}}
	scoped := Connection{Type: TypeSlack, UsergroupsRead: true}
	targets := []struct {
		name string
		t    EntryTarget
	}{
		{"channel", EntryTarget{Kind: TargetEveryone, Everyone: EveryoneChannel}},
		{"here", EntryTarget{Kind: TargetEveryone, Everyone: EveryoneHere}},
		{"all", EntryTarget{Kind: TargetEveryone, Everyone: EveryoneAll}},
		{"everyone", EntryTarget{Kind: TargetEveryone, Everyone: EveryoneEveryone}},
		{"group", EntryTarget{Kind: TargetGroup, Group: "ops"}},
		{"team", EntryTarget{Kind: TargetTeam, TeamID: payments}},
		{"teams from the label", EntryTarget{Kind: TargetTeamsFromLabel}},
		{"user", EntryTarget{Kind: TargetUser, UserID: alice}},
		{"on-call", EntryTarget{Kind: TargetOnCallOfTeam, TeamID: payments}},
		{"on-call from the label", EntryTarget{Kind: TargetOnCallOfTeamsFromLabel}},
	}
	const ok = ""
	for _, row := range []struct {
		typ  string
		conn Connection
		want []string // per target: "" when it can, else the reason
	}{
		{TypeMattermost, Connection{Type: TypeMattermost}, []string{ok, ok, ok, ReasonMessengerHasNoToken, ok, ok, ok,
			ok, ok, ok}},
		{TypeSlack, scoped, []string{ok, ok, ReasonMessengerHasNoToken, ReasonMessengerHasNoToken, ok, ok, ok, ok, ok,
			ok}},
		{TypeSlack, Connection{Type: TypeSlack}, []string{ok, ok, ReasonMessengerHasNoToken, ReasonMessengerHasNoToken,
			ReasonUsergroupsReadMissing, ReasonUsergroupsReadMissing, ReasonUsergroupsReadMissing, ok, ok, ok}},
		{TypeTelegram, Connection{Type: TypeTelegram}, []string{ReasonTelegramNoGroups, ReasonTelegramNoGroups,
			ReasonTelegramNoGroups, ReasonTelegramNoGroups, ReasonTelegramNoGroups, ReasonTelegramNoGroups,
			ReasonTelegramNoGroups, ok, ok, ok}},
		{TypeBridge, all, []string{ok, ok, ok, ok, ok, ok, ok, ok, ok, ok}},
		{TypeBridge, some, []string{ReasonBridgeNotDeclared, ok, ReasonBridgeNotDeclared, ReasonBridgeNotDeclared,
			ReasonBridgeNoGroupMentions, ReasonBridgeNoGroupMentions, ReasonBridgeNoGroupMentions, ok, ok, ok}},
		{TypeBridge, gateway, []string{ReasonBridgeNotDeclared, ReasonBridgeNotDeclared, ReasonBridgeNotDeclared,
			ReasonBridgeNotDeclared, ReasonBridgeNoGroupMentions, ReasonBridgeNoGroupMentions,
			ReasonBridgeNoGroupMentions, ok, ok, ok}},
		{TypeWebhook, Connection{}, []string{ok, ok, ok, ok, ok, ok, ok, ok, ok, ok}},
	} {
		for i, tg := range targets {
			can, reason := Can(row.typ, row.conn, tg.t)
			if can != (row.want[i] == ok) || reason != row.want[i] {
				t.Errorf("%s %+v %s: %v %q, want %q", row.typ, row.conn, tg.name, can, reason, row.want[i])
			}
		}
	}
	for _, reason := range []string{ReasonUsergroupsReadMissing, ReasonBridgeNoGroupMentions, ReasonBridgeNotDeclared,
		ReasonMessengerHasNoToken, ReasonTelegramNoGroups} {
		if unsupportedDetail(reason) == "" {
			t.Errorf("no detail for %s", reason)
		}
	}
}

// TestValidateEntries is C-28.FR-1 and FR-2, AC-3: the entries in canonical form, every event for one that names
// none; each target with exactly its fields, once; each event once; a target the Connection cannot mention refused with
// mention_unsupported at /mention_entries/{i}/target — Telegram's everyone, groups and Teams, Slack's everyone and all
// and its groups without usergroups:read, a bridge's undeclared ones — and a person and the On-call taken on every
// type; an unknown or deleted User or Team refused with unknown_id; an entry whose target equals a stored one kept
// whatever its Connection can mention now and whether its User or Team was deleted.
func TestValidateEntries(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s := newService(f)
	check := func(typ, conn string, entries []EntryInput, stored ...EntryInput) ([]EntryInput, error) {
		return s.ValidateEntries(ctx, Check{DestinationType: typ, Connection: conn, Entries: entries, Stored: stored})
	}
	got, err := check(TypeMattermost, "CNAAAAAAAAAAM3", []EntryInput{
		{Target: EntryTarget{Kind: TargetUser, UserID: strings.ToLower(alice)}},
		{Target: EntryTarget{Kind: TargetTeam, TeamID: strings.ToLower(payments)},
			Events: []string{EventStormStarted, KindReopen}},
		{Target: EntryTarget{Kind: TargetEveryone, Everyone: EveryoneHere}, Events: []string{KindNewAlertGroup}},
		{Target: EntryTarget{Kind: TargetOnCallOfTeamsFromLabel}},
		{Target: EntryTarget{Kind: TargetGroup, Group: "payments_team"}},
	})
	want := []EntryInput{person(alice), teamEntry(TargetTeam, payments, KindReopen, EventStormStarted),
		everyone(EveryoneHere, KindNewAlertGroup), ent(TargetOnCallOfTeamsFromLabel), group("payments_team", "")}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("mattermost = %+v, %v", got, err)
	}
	refused := func(name string, err error, pointer, code string) {
		t.Helper()
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != pointer || fe.Code != code {
			t.Errorf("%s: %v, want %s %s", name, err, pointer, code)
		}
	}
	tgt := func(tg EntryTarget) []EntryInput { return []EntryInput{{Target: tg}} }
	for _, tc := range []struct {
		name    string
		typ     string
		conn    string
		entries []EntryInput
		pointer string
		code    string
	}{
		{"an unknown kind", TypeMattermost, "", tgt(EntryTarget{Kind: "boss"}), "/mention_entries/0/target/kind",
			CodeInvalidFormat},
		{"a field of another kind", TypeMattermost, "", tgt(EntryTarget{Kind: TargetUser, UserID: alice,
			Group: "x"}), "/mention_entries/0/target/group", CodeInvalidFormat},
		{"everyone without a word", TypeMattermost, "", tgt(EntryTarget{Kind: TargetEveryone}),
			"/mention_entries/0/target/everyone", CodeRequired},
		{"an unknown word", TypeMattermost, "", tgt(EntryTarget{Kind: TargetEveryone, Everyone: "none"}),
			"/mention_entries/0/target/everyone", CodeInvalidFormat},
		{"a user without an id", TypeMattermost, "", tgt(EntryTarget{Kind: TargetUser}),
			"/mention_entries/0/target/user_id", CodeRequired},
		{"a malformed user", TypeMattermost, "", tgt(EntryTarget{Kind: TargetUser, UserID: "x"}),
			"/mention_entries/0/target/user_id", CodeUnknownID},
		{"an unknown user", TypeMattermost, "", tgt(EntryTarget{Kind: TargetUser, UserID: "SRAAAAAAAAAAA9"}),
			"/mention_entries/0/target/user_id", CodeUnknownID},
		{"a deleted user", TypeMattermost, "", tgt(EntryTarget{Kind: TargetUser, UserID: gone}),
			"/mention_entries/0/target/user_id", CodeUnknownID},
		{"a team without an id", TypeMattermost, "", tgt(EntryTarget{Kind: TargetOnCallOfTeam}),
			"/mention_entries/0/target/team_id", CodeRequired},
		{"a malformed team", TypeMattermost, "", tgt(EntryTarget{Kind: TargetTeam, TeamID: "x"}),
			"/mention_entries/0/target/team_id", CodeUnknownID},
		{"a deleted team", TypeMattermost, "", tgt(EntryTarget{Kind: TargetTeam, TeamID: oldTeam}),
			"/mention_entries/0/target/team_id", CodeUnknownID},
		{"an unknown team", TypeMattermost, "", tgt(EntryTarget{Kind: TargetOnCallOfTeam, TeamID: "TMAAAAAAAAAAZ9"}),
			"/mention_entries/0/target/team_id", CodeUnknownID},
		{"a group without a name", TypeMattermost, "", tgt(EntryTarget{Kind: TargetGroup}),
			"/mention_entries/0/target/group", CodeRequired},
		{"a malformed group", TypeMattermost, "", tgt(EntryTarget{Kind: TargetGroup, Group: "@ops"}),
			"/mention_entries/0/target/group", CodeInvalidFormat},
		{"a slack group without its id", TypeSlack, "CNAAAAAAAAAAS4", tgt(EntryTarget{Kind: TargetGroup,
			Group: "ops"}), "/mention_entries/0/target/group_id", CodeRequired},
		{"a malformed slack id", TypeSlack, "CNAAAAAAAAAAS4", tgt(EntryTarget{Kind: TargetGroup, Group: "ops",
			GroupID: "s1"}), "/mention_entries/0/target/group_id", CodeInvalidFormat},
		{"a slack id elsewhere", TypeMattermost, "", tgt(EntryTarget{Kind: TargetGroup, Group: "ops",
			GroupID: "S1"}), "/mention_entries/0/target/group_id", CodeUnsupported},
		{"a target twice", TypeMattermost, "", []EntryInput{person(alice), person(strings.ToLower(alice))},
			"/mention_entries/1/target", CodeDuplicate},
		{"a slack user group twice, by other handles", TypeSlack, "CNAAAAAAAAAAS4", []EntryInput{
			group("payments-team", "S0FAKEPAYTEAM"), group("payments", "S0FAKEPAYTEAM")}, "/mention_entries/1/target",
			CodeDuplicate},
		{"no events", TypeMattermost, "", []EntryInput{{Target: EntryTarget{Kind: TargetTeamsFromLabel},
			Events: []string{}}}, "/mention_entries/0/events", CodeRequired},
		{"an unknown event", TypeMattermost, "", []EntryInput{ent(TargetTeamsFromLabel, KindReopen, "reminder")},
			"/mention_entries/0/events/1", CodeInvalidFormat},
		{"an event twice", TypeMattermost, "", []EntryInput{ent(TargetTeamsFromLabel, KindReopen, KindReopen)},
			"/mention_entries/0/events/1", CodeDuplicate},
		{"telegram: channel", TypeTelegram, "", tgt(EntryTarget{Kind: TargetEveryone, Everyone: EveryoneChannel}),
			"/mention_entries/0/target", CodeMentionUnsupported},
		{"telegram: a group", TypeTelegram, "", tgt(EntryTarget{Kind: TargetGroup, Group: "ops"}),
			"/mention_entries/0/target", CodeMentionUnsupported},
		{"telegram: a team", TypeTelegram, "", []EntryInput{person(alice), teamEntry(TargetTeam, payments)},
			"/mention_entries/1/target", CodeMentionUnsupported},
		{"telegram: the teams from the label", TypeTelegram, "", tgt(EntryTarget{Kind: TargetTeamsFromLabel}),
			"/mention_entries/0/target", CodeMentionUnsupported},
		{"mattermost: everyone", TypeMattermost, "CNAAAAAAAAAAM3", tgt(EntryTarget{Kind: TargetEveryone,
			Everyone: EveryoneEveryone}), "/mention_entries/0/target", CodeMentionUnsupported},
		{"slack: everyone", TypeSlack, "CNAAAAAAAAAAS4", tgt(EntryTarget{Kind: TargetEveryone,
			Everyone: EveryoneEveryone}), "/mention_entries/0/target", CodeMentionUnsupported},
		{"slack: all", TypeSlack, "CNAAAAAAAAAAS4", tgt(EntryTarget{Kind: TargetEveryone, Everyone: EveryoneAll}),
			"/mention_entries/0/target", CodeMentionUnsupported},
		{"slack: a group without usergroups:read", TypeSlack, "CNAAAAAAAAAAS6", tgt(EntryTarget{Kind: TargetGroup,
			Group: "ops", GroupID: "S0OPS"}), "/mention_entries/0/target", CodeMentionUnsupported},
		{"slack: a team without usergroups:read", TypeSlack, "CNAAAAAAAAAAS6", []EntryInput{teamEntry(TargetTeam,
			payments)}, "/mention_entries/0/target", CodeMentionUnsupported},
		{"a bridge: an undeclared channel", TypeBridge, "CNAAAAAAAAAAB7", tgt(EntryTarget{Kind: TargetEveryone,
			Everyone: EveryoneChannel}), "/mention_entries/0/target", CodeMentionUnsupported},
		{"a bridge: a group without group_mentions", TypeBridge, "CNAAAAAAAAAAB7", tgt(EntryTarget{
			Kind: TargetGroup, Group: "ops"}), "/mention_entries/0/target", CodeMentionUnsupported},
		{"a gateway only", TypeBridge, "CNAAAAAAAAAAB8", tgt(EntryTarget{Kind: TargetEveryone,
			Everyone: EveryoneChannel}), "/mention_entries/0/target", CodeMentionUnsupported},
	} {
		_, err := check(tc.typ, tc.conn, tc.entries)
		refused(tc.name, err, tc.pointer, tc.code)
	}

	for _, tc := range []struct {
		typ, conn string
		entries   []EntryInput
	}{
		{TypeTelegram, "", []EntryInput{person(alice), teamEntry(TargetOnCallOfTeam, payments),
			ent(TargetOnCallOfTeamsFromLabel)}},
		{TypeSlack, "CNAAAAAAAAAAS4", []EntryInput{everyone(EveryoneChannel), everyone(EveryoneHere),
			group("payments-team", "S0FAKEPAYTEAM"), teamEntry(TargetTeam, payments)}},
		{TypeBridge, "CNAAAAAAAAAAB5", []EntryInput{everyone(EveryoneChannel), everyone(EveryoneEveryone),
			group("ops", ""), person(alice)}},
		{TypeBridge, "CNAAAAAAAAAAB7", []EntryInput{person(alice)}},
		{TypeWebhook, "", []EntryInput{everyone(EveryoneEveryone), everyone(EveryoneAll), group("any name here", ""),
			teamEntry(TargetTeam, payments), ent(TargetTeamsFromLabel), person(bob)}},
	} {
		if _, err := check(tc.typ, tc.conn, tc.entries); err != nil {
			t.Errorf("%s %s: %v", tc.typ, tc.conn, err)
		}
	}
	// A stored entry stays whatever its Connection can mention now or whether its User or Team was deleted.
	kept := []EntryInput{group("payments-team", "S0FAKEPAYTEAM"), person(gone), teamEntry(TargetTeam, oldTeam)}
	if got, err := check(TypeSlack, "CNAAAAAAAAAAS6", kept, kept...); err != nil || !reflect.DeepEqual(got, kept) {
		t.Errorf("kept = %+v, %v", got, err)
	}
	for _, name := range []string{"GetMentionConnection", "ResolveEntryUsers", "ResolveEntryTeams"} {
		f.fail[name] = errBoom
		if _, err := check(TypeMattermost, "CNAAAAAAAAAAM3", []EntryInput{person(alice),
			teamEntry(TargetTeam, payments)}); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(f.fail, name)
	}
}

// TestFromSettings is C-28.FR-5 and AC-5: the L1 settings "@channel for new_alert_group, Alice for ack_timeout" become
// two entries — channel for new_alert_group and storm_started, Alice for ack_timeout — settings that mention nobody
// none; entries are everyone, then the Users, then the groups, in the order L1 listed them; what L1 refused on the
// type is left out; and every L1 value reads back unchanged from the entries' L1 form on every type.
func TestFromSettings(t *testing.T) {
	set := nobody()
	set[KindNewAlertGroup] = Setting{Everyone: EveryoneChannel, UserIDs: []string{}, Groups: []string{}}
	set[KindAckTimeout] = Setting{Everyone: EveryoneNone, UserIDs: []string{alice}, Groups: []string{}}
	want := []EntryInput{everyone(EveryoneChannel, KindNewAlertGroup, EventStormStarted),
		person(alice, KindAckTimeout)}
	if got := FromSettings(TypeMattermost, set); !reflect.DeepEqual(got, want) {
		t.Errorf("AC-5 = %+v", got)
	}
	if got := FromSettings(TypeMattermost, nobody()); len(got) != 0 {
		t.Errorf("nobody = %+v", got)
	}
	order := nobody()
	order[KindNewAlertGroup] = Setting{Everyone: EveryoneHere, UserIDs: []string{bob}, Groups: []string{"sre", "ops"}}
	order[KindNewAlerts] = Setting{Everyone: EveryoneChannel, UserIDs: []string{strings.ToLower(alice), bob, "x"},
		Groups: []string{"ops", ""}}
	order[KindReopen] = Setting{Everyone: EveryoneHere}
	want = []EntryInput{everyone(EveryoneHere, KindNewAlertGroup, KindReopen, EventStormStarted),
		everyone(EveryoneChannel, KindNewAlerts), person(bob, KindNewAlertGroup, KindNewAlerts, EventStormStarted),
		person(alice, KindNewAlerts), group("sre", "", KindNewAlertGroup, EventStormStarted),
		group("ops", "", KindNewAlertGroup, KindNewAlerts, EventStormStarted)}
	if got := FromSettings(TypeMattermost, order); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %+v", got)
	}
	// What L1 refused on a type, which an earlier rule may have stored, is left out.
	if got := FromSettings(TypeTelegram, order); !reflect.DeepEqual(got, []EntryInput{
		person(bob, KindNewAlertGroup, KindNewAlerts, EventStormStarted), person(alice, KindNewAlerts)}) {
		t.Errorf("telegram = %+v", got)
	}
	slack := nobody()
	slack[KindNewAlerts] = Setting{Everyone: EveryoneAll, Groups: []string{"ops"}}
	slack[KindReopen] = Setting{Everyone: EveryoneHere}
	if got := FromSettings(TypeSlack, slack); !reflect.DeepEqual(got, []EntryInput{everyone(EveryoneHere,
		KindReopen)}) {
		t.Errorf("slack = %+v", got)
	}

	// The round trip of every L1 value per kind and type.
	words := map[string][]string{TypeMattermost: {EveryoneChannel, EveryoneAll, EveryoneHere},
		TypeWebhook: {EveryoneChannel, EveryoneAll, EveryoneHere}, TypeSlack: {EveryoneChannel, EveryoneHere}}
	for _, typ := range []string{TypeMattermost, TypeSlack, TypeTelegram, TypeBridge, TypeWebhook} {
		for _, kind := range Kinds {
			var values []Setting
			for _, w := range words[typ] {
				values = append(values, Setting{Everyone: w, UserIDs: []string{}, Groups: []string{}})
			}
			values = append(values, Setting{Everyone: EveryoneNone, UserIDs: []string{alice, bob}, Groups: []string{}})
			if groupsOffered(typ) {
				values = append(values, Setting{Everyone: EveryoneNone, UserIDs: []string{}, Groups: []string{"sre",
					"ops"}})
			}
			for _, v := range values {
				in := with(kind, v)
				if got := L1Form(typ, FromSettings(typ, in)); !reflect.DeepEqual(got, in) {
					t.Errorf("%s %s %+v reads back %+v", typ, kind, v, got[kind])
				}
			}
		}
	}
	// The L1 form shows only what L1 can express: no mention of everyone, no Slack user group, no Team, no entry for
	// storm_started alone, and the first everyone choice of a kind.
	form := L1Form(TypeSlack, []EntryInput{everyone(EveryoneHere, KindNewAlerts), everyone(EveryoneChannel,
		KindNewAlerts), everyone(EveryoneEveryone), group("ops", "S0OPS"), teamEntry(TargetTeam, payments),
		everyone(EveryoneChannel, EventStormStarted)})
	if !reflect.DeepEqual(form, with(KindNewAlerts, Setting{Everyone: EveryoneHere, UserIDs: []string{},
		Groups: []string{}})) {
		t.Errorf("the slack form %+v", form)
	}
}

// TestMergeSettings is C-28.FR-5 for an update with the L1 settings alone: settings equal to the L1 form of the stored
// entries change nothing; others replace exactly the entries L1 can express, keep every other entry after them — a
// Slack user group, a Team, a mention of everyone, an On-call — keep the stored storm_started of a target present
// before and after, and merge a stored storm_started-only entry of a target into the converted one.
func TestMergeSettings(t *testing.T) {
	stored := []EntryInput{teamEntry(TargetTeam, payments), everyone(EveryoneHere, KindNewAlertGroup),
		group("payments-team", "S0FAKEPAYTEAM"), person(alice, KindAckTimeout, EventStormStarted),
		everyone(EveryoneChannel, EventStormStarted), ent(TargetOnCallOfTeamsFromLabel)}
	form := L1Form(TypeSlack, stored)
	if got := MergeSettings(TypeSlack, stored, form); !reflect.DeepEqual(got, stored) {
		t.Errorf("the form read = %+v", got)
	}
	lower := L1Form(TypeSlack, stored)
	lower[KindAckTimeout] = Setting{Everyone: EveryoneNone, UserIDs: []string{strings.ToLower(alice)}}
	if got := MergeSettings(TypeSlack, stored, lower); !reflect.DeepEqual(got, stored) {
		t.Errorf("the form read in another case = %+v", got)
	}
	set := L1Form(TypeSlack, stored)
	set[KindNewAlerts] = Setting{Everyone: EveryoneChannel, UserIDs: []string{alice}, Groups: []string{}}
	set[KindAckTimeout] = Setting{Everyone: EveryoneNone, UserIDs: []string{}, Groups: []string{}}
	want := []EntryInput{everyone(EveryoneHere, KindNewAlertGroup), everyone(EveryoneChannel, KindNewAlerts,
		EventStormStarted), person(alice, KindNewAlerts, EventStormStarted), teamEntry(TargetTeam, payments),
		group("payments-team", "S0FAKEPAYTEAM"), ent(TargetOnCallOfTeamsFromLabel)}
	if got := MergeSettings(TypeSlack, stored, set); !reflect.DeepEqual(got, want) {
		t.Errorf("merged = %+v", got)
	}
	if got := MergeSettings(TypeSlack, stored, nobody()); !reflect.DeepEqual(got, []EntryInput{
		teamEntry(TargetTeam, payments), group("payments-team", "S0FAKEPAYTEAM"),
		everyone(EveryoneChannel, EventStormStarted), ent(TargetOnCallOfTeamsFromLabel)}) {
		t.Errorf("nobody = %+v", got)
	}
}

// TestEntries is MentionEntry of a read (C-28.FR-1 to FR-3, AC-2, settled 2): each entry with whether its Connection can
// mention it now, the reason when it cannot, and what it mentions now — a linked person by username and an unlinked one
// by login, a Team by its Team mention and nobody without one, the On-call now, the targets from the label one item per
// Team that is not deleted, a deleted User or Team nobody, an outgoing webhook's people linked and its Teams by slug.
func TestEntries(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	f.entries[10] = []EntryInput{person(alice), person(bob), person(gone), teamEntry(TargetTeam, payments),
		teamEntry(TargetTeam, search), teamEntry(TargetTeam, oldTeam), ent(TargetOnCallOfTeamsFromLabel),
		everyone(EveryoneEveryone), group("oncall", "")}
	f.entries[30] = []EntryInput{person(bob), ent(TargetTeamsFromLabel)}
	f.entries[40] = []EntryInput{teamEntry(TargetTeam, payments), teamEntry(TargetOnCallOfTeam, devops)}
	s, _, _ := wired(f)
	got, err := s.Entries(ctx, nil, []EntryDestination{{ID: 10, Type: TypeMattermost, Connection: "CNAAAAAAAAAAM3"},
		{ID: 30, Type: TypeWebhook}, {ID: 40, Type: TypeSlack, Connection: "CNAAAAAAAAAAS6"},
		{ID: 99, Type: TypeTelegram}})
	if err != nil {
		t.Fatal(err)
	}
	show := func(e Entry) string {
		var parts []string
		for _, r := range e.ResolvesTo {
			p := "-"
			if r.Team != nil {
				p = r.Team.Slug
			}
			var names []string
			for _, m := range r.Mentions {
				n := m.Kind + ":" + m.Name
				if !m.Linked {
					n += "(unlinked)"
				}
				names = append(names, n)
			}
			parts = append(parts, p+"="+strings.Join(names, "+"))
		}
		s := strings.Join(parts, " ")
		if !e.Available {
			s += " unavailable:" + e.UnavailableReason
		}
		return s
	}
	var lines []string
	for _, id := range []int64{10, 30, 40} {
		for _, e := range got[id] {
			lines = append(lines, strconv.FormatInt(id, 10)+" "+e.Target.Kind+" "+show(e))
		}
	}
	// The fake lists the Teams in its order; the query lists them by name.
	want := []string{
		"10 user -=user:alice.mm",
		"10 user -=user:bob(unlinked)",
		"10 user -=",
		"10 team payments=group:payments_team",
		"10 team search=",
		"10 team old=",
		"10 on_call_of_teams_from_label payments=user:alice.mm search= devops=user:carol(unlinked)",
		"10 everyone -= unavailable:messenger_has_no_token",
		"10 group -=group:oncall",
		"30 user -=user:bob",
		"30 teams_from_label payments=group:payments search=group:search devops=group:devops",
		"40 team payments= unavailable:usergroups_read_missing",
		"40 on_call_of_team devops=user:carol(unlinked)",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("entries:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if u := got[10][2].User; u == nil || !u.Deleted || got[10][5].Team == nil || !got[10][5].Team.Deleted {
		t.Errorf("deleted refs %+v %+v", got[10][2].User, got[10][5].Team)
	}
	if len(got[99]) != 0 {
		t.Errorf("a destination without entries %+v", got[99])
	}
	if out, err := s.Entries(ctx, nil, nil); err != nil || len(out) != 0 {
		t.Errorf("none = %v, %v", out, err)
	}
	for _, name := range []string{"ListMentionEntries", "GetMentionConnection", "ListLiveTeams", "ListMentionUsers"} {
		f.fail[name] = errBoom
		if _, err := s.Entries(ctx, nil, []EntryDestination{{ID: 10, Type: TypeMattermost,
			Connection: "CNAAAAAAAAAAM3"}}); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(f.fail, name)
	}
	// Without the business clock, nobody is On-call.
	plain := newService(f)
	out, err := plain.Entries(ctx, nil, []EntryDestination{{ID: 40, Type: TypeSlack, Connection: "CNAAAAAAAAAAS4"}})
	if err != nil || len(out[40][1].ResolvesTo[0].Mentions) != 0 {
		t.Errorf("without a clock = %+v, %v", out, err)
	}
}

// TestWriteEntries: a save's entries replace the stored ones, in order, Users and Teams by their ids, deleted ones
// included; a failing query fails the save.
func TestWriteEntries(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s := newService(f)
	f.entries[10] = []EntryInput{everyone(EveryoneChannel)}
	list := []EntryInput{person(gone, KindNewAlerts), teamEntry(TargetTeam, oldTeam), group("ops", "S0OPS"),
		everyone(EveryoneHere, KindNewAlertGroup, EventStormStarted)}
	if err := s.WriteEntries(ctx, nil, 10, list, t0); err != nil || !reflect.DeepEqual(f.entries[10], list) {
		t.Errorf("written %+v, %v", f.entries[10], err)
	}
	if w := f.written[0]; w.OrgID != orgID || w.DestinationID != 10 || !w.Now.Equal(t0) ||
		!slices.Equal(w.Positions, []int32{0, 1, 2, 3}) || w.UserIds[0] != 4 || w.TeamIds[1] != 14 {
		t.Errorf("params %+v", w)
	}
	if err := s.WriteEntries(ctx, nil, 10, nil, t0); err != nil || len(f.entries[10]) != 0 || len(f.written) != 1 {
		t.Errorf("none written %+v, %v", f.entries[10], err)
	}
	for _, name := range []string{"DeleteMentionEntries", "ResolveEntryUsers", "ResolveEntryTeams",
		"InsertMentionEntries"} {
		f.fail[name] = errBoom
		if err := s.WriteEntries(ctx, nil, 10, list, t0); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(f.fail, name)
	}
	if err := s.WriteEntries(ctx, nil, 10, []EntryInput{person("SRAAAAAAAAAAA9")}, t0); err == nil {
		t.Error("a user that does not exist was written")
	}
	if err := s.WriteEntries(ctx, nil, 10, []EntryInput{teamEntry(TargetTeam, "TMAAAAAAAAAAZ9")}, t0); err == nil {
		t.Error("a team that does not exist was written")
	}
}

// TestAddTeamEntry is C-28.FR-6 and AC-8: a Destination of a Team without an entry naming it gets "Team {Team}" for
// every Loud event after its other entries, with a new version and its entries before and after; one that has it,
// a Telegram one and a deleted one get nothing; a failing query fails.
func TestAddTeamEntry(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	f.dests[10] = dbgen.LockEntryDestinationRow{ID: 10, PublicID: "DSAAAAAAAAAAM1", Name: "payments-critical",
		Type: TypeMattermost}
	f.dests[20] = dbgen.LockEntryDestinationRow{ID: 20, PublicID: "DSAAAAAAAAAAT1", Name: "payments-tg",
		Type: TypeTelegram}
	f.entries[10] = []EntryInput{everyone(EveryoneHere, KindNewAlertGroup)}
	now := t0.Add(time.Minute)
	added, ok, err := addTeamEntry(ctx, f, orgID, 10, 11, payments, now)
	want := []EntryInput{everyone(EveryoneHere, KindNewAlertGroup), teamEntry(TargetTeam, payments)}
	if err != nil || !ok || added.PublicID != "DSAAAAAAAAAAM1" || added.Name != "payments-critical" ||
		!reflect.DeepEqual(added.Before, want[:1]) || !reflect.DeepEqual(added.After, want) ||
		!reflect.DeepEqual(f.entries[10], want) || !slices.Equal(f.bumped, []int64{10}) ||
		f.written[0].Positions[0] != 1 || !f.written[0].Now.Equal(now) {
		t.Fatalf("added %+v %v %v; entries %+v", added, ok, err, f.entries[10])
	}
	for _, tc := range []struct {
		name string
		dest int64
	}{{"again", 10}, {"telegram", 20}, {"deleted", 30}} {
		if _, ok, err := addTeamEntry(ctx, f, orgID, tc.dest, 11, payments, now); ok || err != nil {
			t.Errorf("%s: %v %v", tc.name, ok, err)
		}
	}
	for _, name := range []string{"LockEntryDestination", "ListMentionEntries", "InsertMentionEntries",
		"BumpEntryDestination"} {
		f.fail[name] = errBoom
		if _, _, err := addTeamEntry(ctx, f, orgID, 10, 12, search, now); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(f.fail, name)
	}
	if got := Inputs([]Entry{{EntryInput: want[0]}}); !reflect.DeepEqual(got, want[:1]) {
		t.Errorf("inputs %+v", got)
	}
}

// TestValidateKeepsStoredUsers is C-28.FR-5 with settled 2: the L1 settings of a request may name a User deleted since
// whom a stored entry names, as the read form shows them; one that no stored entry names is refused. The read form of
// such entries is the same form.
func TestValidateKeepsStoredUsers(t *testing.T) {
	ctx := t.Context()
	s := newService(newFake())
	stored := []EntryInput{person(gone, KindNewAlerts), everyone(EveryoneHere, KindReopen)}
	form := L1Form(TypeMattermost, stored)
	if !SameForm(TypeMattermost, stored, form) || form[KindNewAlerts].UserIDs[0] != gone {
		t.Fatalf("the read form %+v", form)
	}
	if err := s.Validate(ctx, TypeMattermost, form, stored); err != nil {
		t.Errorf("the read form with a deleted user: %v", err)
	}
	if err := s.Validate(ctx, TypeMattermost, form, nil); err == nil {
		t.Error("a deleted user that no entry names was taken")
	}
	changed := L1Form(TypeMattermost, stored)
	changed[KindAckTimeout] = Setting{Everyone: EveryoneChannel, UserIDs: []string{}, Groups: []string{}}
	if SameForm(TypeMattermost, stored, changed) {
		t.Error("a changed form is the same")
	}
	if got := MergeSettings(TypeMattermost, stored, changed); len(got) != 3 || got[2].Target.UserID != gone || !slices.Equal(got[2].Events, []string{KindNewAlerts}) {
		t.Errorf("merged %+v", got)
	}
}

// TestMergeSettingsKeepsHiddenKinds is C-28.FR-5 for the kinds the L1 form hides: with @here for new_alert_group and
// @channel for every event, the form shows here for new_alert_group; a save from the L1 editor that only adds a User to
// ack_timeout keeps @channel for new_alert_group, and one that changes new_alert_group's choice drops it there. A second
// word hidden in every kind it has is kept as its own entry.
func TestMergeSettingsKeepsHiddenKinds(t *testing.T) {
	stored := []EntryInput{everyone(EveryoneHere, KindNewAlertGroup), everyone(EveryoneChannel)}
	set := L1Form(TypeMattermost, stored)
	if set[KindNewAlertGroup].Everyone != EveryoneHere || set[KindReopen].Everyone != EveryoneChannel {
		t.Fatalf("the form %+v", set)
	}
	set[KindAckTimeout] = Setting{Everyone: EveryoneChannel, UserIDs: []string{alice}, Groups: []string{}}
	want := []EntryInput{everyone(EveryoneHere, KindNewAlertGroup), everyone(EveryoneChannel),
		person(alice, KindAckTimeout)}
	if got := MergeSettings(TypeMattermost, stored, set); !reflect.DeepEqual(got, want) {
		t.Errorf("a user added = %+v", got)
	}
	changed := L1Form(TypeMattermost, stored)
	changed[KindNewAlertGroup] = Setting{Everyone: EveryoneNone, UserIDs: []string{}, Groups: []string{}}
	want = []EntryInput{everyone(EveryoneChannel, KindNewAlerts, KindReopen, KindAckTimeout, KindSnoozeEnded,
		KindRiseToUrgent, EventStormStarted)}
	if got := MergeSettings(TypeMattermost, stored, changed); !reflect.DeepEqual(got, want) {
		t.Errorf("new_alert_group changed to nobody = %+v", got)
	}
	toChannel := L1Form(TypeMattermost, stored)
	toChannel[KindNewAlertGroup] = Setting{Everyone: EveryoneChannel, UserIDs: []string{}, Groups: []string{}}
	if got := MergeSettings(TypeMattermost, stored, toChannel); !reflect.DeepEqual(got,
		[]EntryInput{everyone(EveryoneChannel)}) {
		t.Errorf("new_alert_group changed to channel = %+v", got)
	}
	// @here hidden in every kind it has, behind @channel listed first.
	behind := []EntryInput{everyone(EveryoneChannel, KindReopen), everyone(EveryoneHere, KindReopen,
		EventStormStarted), person(bob, KindReopen)}
	form := L1Form(TypeMattermost, behind)
	form[KindNewAlerts] = Setting{Everyone: EveryoneNone, UserIDs: []string{alice}, Groups: []string{}}
	want = []EntryInput{everyone(EveryoneChannel, KindReopen), everyone(EveryoneHere, KindReopen, EventStormStarted),
		person(alice, KindNewAlerts), person(bob, KindReopen)}
	if got := MergeSettings(TypeMattermost, behind, form); !reflect.DeepEqual(got, want) {
		t.Errorf("a hidden word = %+v", got)
	}
	// An outgoing webhook's mention of everyone with the word everyone, which L1 cannot express, is not hidden in the
	// form but outside it: it stays whole and last.
	hook := []EntryInput{everyone(EveryoneChannel, KindNewAlertGroup), everyone(EveryoneEveryone)}
	asked := L1Form(TypeWebhook, hook)
	asked[KindNewAlerts] = Setting{Everyone: EveryoneHere, UserIDs: []string{}, Groups: []string{}}
	want = []EntryInput{everyone(EveryoneChannel, KindNewAlertGroup), everyone(EveryoneHere, KindNewAlerts),
		everyone(EveryoneEveryone)}
	if got := MergeSettings(TypeWebhook, hook, asked); !reflect.DeepEqual(got, want) {
		t.Errorf("the word everyone of a webhook = %+v", got)
	}
}

// TestSameFormOrder: the order of the Users and groups within a kind does not count, so that a 0.1 client that sends
// its own settings in another order changes nothing.
func TestSameFormOrder(t *testing.T) {
	stored := []EntryInput{person(alice, KindNewAlerts), person(bob, KindNewAlerts), group("sre", "", KindNewAlerts),
		group("ops", "", KindNewAlerts)}
	set := L1Form(TypeMattermost, stored)
	set[KindNewAlerts] = Setting{Everyone: EveryoneNone, UserIDs: []string{bob, alice}, Groups: []string{"ops", "sre"}}
	if !SameForm(TypeMattermost, stored, set) || !reflect.DeepEqual(MergeSettings(TypeMattermost, stored, set),
		stored) {
		t.Error("another order within a kind is a change")
	}
	set[KindNewAlerts] = Setting{Everyone: EveryoneNone, UserIDs: []string{bob}, Groups: []string{"ops", "sre"}}
	if SameForm(TypeMattermost, stored, set) {
		t.Error("a User removed is the same form")
	}
}

// TestValidateEntriesTargetFirst: a target of a kind the Connection cannot mention is refused at the target, whatever
// its fields — a Telegram group with a group_id — and a failing read of the Connection fails it; a supported kind keeps
// the error of its field.
func TestValidateEntriesTargetFirst(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s := newService(f)
	check := func(typ, conn string, e EntryInput) error {
		_, err := s.ValidateEntries(ctx, Check{DestinationType: typ, Connection: conn, Entries: []EntryInput{e}})
		return err
	}
	err := check(TypeTelegram, "", group("ops", "S0OPS"))
	if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Code != CodeMentionUnsupported ||
		fe.Pointer != "/mention_entries/0/target" {
		t.Errorf("a telegram group with an id = %v", err)
	}
	err = check(TypeMattermost, "CNAAAAAAAAAAM3", group("ops", "S0OPS"))
	if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Code != CodeUnsupported ||
		fe.Pointer != "/mention_entries/0/target/group_id" {
		t.Errorf("a mattermost group with an id = %v", err)
	}
	err = check(TypeBridge, "CNAAAAAAAAAAB7", group("ops", "S0OPS"))
	if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Code != CodeMentionUnsupported {
		t.Errorf("a bridge group with an id = %v", err)
	}
	// A missing or malformed field of the kind keeps its refusal, also where the kind is unsupported.
	for _, tc := range []struct {
		typ, conn string
		e         EntryInput
		pointer   string
		code      string
	}{
		{TypeSlack, "CNAAAAAAAAAAS4", ent(TargetEveryone), "/mention_entries/0/target/everyone", CodeRequired},
		{TypeTelegram, "", ent(TargetEveryone), "/mention_entries/0/target/everyone", CodeRequired},
		{TypeBridge, "CNAAAAAAAAAAB7", ent(TargetGroup), "/mention_entries/0/target/group", CodeRequired},
		{TypeTelegram, "", teamEntry(TargetTeam, "x"), "/mention_entries/0/target/team_id", CodeUnknownID},
		{TypeTelegram, "", EntryInput{Target: EntryTarget{Kind: TargetTeamsFromLabel, UserID: alice}},
			"/mention_entries/0/target", CodeMentionUnsupported},
	} {
		err := check(tc.typ, tc.conn, tc.e)
		if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Code != tc.code || fe.Pointer != tc.pointer {
			t.Errorf("%s %+v = %v, want %s at %s", tc.typ, tc.e.Target, err, tc.code, tc.pointer)
		}
	}
	f.fail["GetMentionConnection"] = errBoom
	if err := check(TypeBridge, "CNAAAAAAAAAAB7", group("ops", "S0OPS")); !errors.Is(err, errBoom) {
		t.Errorf("a failing connection = %v", err)
	}
}

// TestAddTeamEntryEdges: an entry "On-call of a Team" of the Team does not stand for "Team {Team}", which is added; a
// Destination with MaxEntries entries gets none, so that a read sent back stays within the API's limit.
func TestAddTeamEntryEdges(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	f.dests[10] = dbgen.LockEntryDestinationRow{ID: 10, PublicID: "DSAAAAAAAAAAM1", Name: "a", Type: TypeMattermost}
	f.dests[11] = dbgen.LockEntryDestinationRow{ID: 11, PublicID: "DSAAAAAAAAAAM2", Name: "b", Type: TypeMattermost}
	f.entries[10] = []EntryInput{teamEntry(TargetOnCallOfTeam, payments)}
	if _, ok, err := addTeamEntry(ctx, f, orgID, 10, 11, payments, t0); err != nil || !ok || len(f.entries[10]) != 2 {
		t.Errorf("beside the on-call of the team: %v %v %+v", ok, err, f.entries[10])
	}
	for i := range MaxEntries {
		f.entries[11] = append(f.entries[11], group("g"+strconv.Itoa(i), ""))
	}
	if _, ok, err := addTeamEntry(ctx, f, orgID, 11, 11, payments, t0); err != nil || ok ||
		len(f.entries[11]) != MaxEntries {
		t.Errorf("a full destination: %v %v %d", ok, err, len(f.entries[11]))
	}
}

// TestEntriesReadOnce: a read of many Destinations on one Connection asks the Team mention of each Team for the
// Connection and the On-call of each Team once, and lists the Teams once.
func TestEntriesReadOnce(t *testing.T) {
	f := newFake()
	var ds []EntryDestination
	for id := int64(100); id < 110; id++ {
		f.entries[id] = []EntryInput{ent(TargetTeamsFromLabel), ent(TargetOnCallOfTeamsFromLabel)}
		ds = append(ds, EntryDestination{ID: id, Type: TypeMattermost, Connection: "CNAAAAAAAAAAM3"})
	}
	s, o, m := wired(f)
	f.reads = nil
	got, err := s.Entries(t.Context(), nil, ds)
	if err != nil || len(got) != 10 {
		t.Fatalf("entries %d, %v", len(got), err)
	}
	live := 3 // payments, search, devops
	connections := 0
	for _, r := range f.reads {
		if r == "connection" {
			connections++
		}
	}
	if len(o.calls) != live || len(m.dbs) != live || connections != 1 {
		t.Errorf("%d on-call lookups, %d team mention lookups, %d connection reads for 10 destinations", len(o.calls),
			len(m.dbs), connections)
	}
}

// TestResolveWithoutTime: without a time and without a business clock, nobody is On-call and the On-call is not asked.
func TestResolveWithoutTime(t *testing.T) {
	f := resolveFake()
	s := newService(f)
	o := &fakeOnCalls{onCall: map[int64]int64{11: 1}, until: t0.Add(time.Hour)}
	s.Wire(o, &fakeTeamMentions{}, nil)
	conn := int64(3)
	// The Alert Group 5 has payments, whose On-call the entry "On-call of the Team from the label" asks for; with no
	// record of the event and no clock the time is zero.
	got, err := s.Resolve(t.Context(), nil, orgID, Request{DestinationID: 10, DestinationType: TypeMattermost,
		ConnectionID: &conn, AlertGroupID: 5, Mentions: []string{KindReopen}})
	if err != nil || len(got) != 0 || len(o.calls) != 0 || !slices.Contains(f.reads, "publication at") {
		t.Errorf("without a time = %q %v, %d on-call lookups", describe(got), err, len(o.calls))
	}
}
