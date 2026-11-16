// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mentions

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/mentions/dbgen"
)

const orgID = 7

var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

type user struct {
	id                    int64
	publicID, name, login string
	deleted               bool
	// links are the usernames by identity space, externals the messenger's user ids and displays the display names
	// of the links.
	links     map[string]string
	externals map[string]string
	displays  map[string]string
}

type team struct {
	id                   int64
	publicID, name, slug string
	deleted              bool
}

// fakeQueries is the database of the package in memory.
type fakeQueries struct {
	users  []user
	teams  []team
	owners map[int64]dbgen.GetEventOwnersRow
	footer []dbgen.ListFooterUsersRow
	// entries are the Mention entries of each Destination, conns the Connections by id, groupTeams the Teams of each
	// Alert Group and stormTeams those of each Storm, dests the Destinations a Team's entry locks, by id.
	entries    map[int64][]EntryInput
	conns      map[int64]dbgen.GetMentionConnectionRow
	groupTeams map[int64][]int64
	stormTeams map[int64][]int64
	dests      map[int64]dbgen.LockEntryDestinationRow
	bumped     []int64
	written    []dbgen.InsertMentionEntriesParams
	// eventAt are the times of the lifecycle events by seq, publishedAt those of the Loud Publications by Alert Group
	// and Destination, and stormAt the starts of the Storms.
	eventAt     map[int64]time.Time
	publishedAt map[[2]int64]time.Time
	stormAt     map[int64]time.Time
	fail        map[string]error
	reads       []string
}

func (f *fakeQueries) err(name string) error { return f.fail[name] }

func (f *fakeQueries) userByID(id int64) *user {
	for i := range f.users {
		if f.users[i].id == id {
			return &f.users[i]
		}
	}
	return nil
}

func (f *fakeQueries) userByPublicID(id string) *user {
	for i := range f.users {
		if f.users[i].publicID == id {
			return &f.users[i]
		}
	}
	return nil
}

func (f *fakeQueries) teamByID(id int64) *team {
	for i := range f.teams {
		if f.teams[i].id == id {
			return &f.teams[i]
		}
	}
	return nil
}

func (f *fakeQueries) teamByPublicID(id string) *team {
	for i := range f.teams {
		if f.teams[i].publicID == id {
			return &f.teams[i]
		}
	}
	return nil
}

func (f *fakeQueries) ListMentionEntries(_ context.Context, arg dbgen.ListMentionEntriesParams) (
	[]dbgen.ListMentionEntriesRow, error) {
	f.reads = append(f.reads, "entries")
	if err := f.err("ListMentionEntries"); err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []dbgen.ListMentionEntriesRow
	for _, d := range arg.DestinationIds {
		for i, e := range f.entries[d] {
			r := dbgen.ListMentionEntriesRow{DestinationID: d, Position: int32(i), TargetKind: e.Target.Kind,
				Everyone: e.Target.Everyone, GroupName: e.Target.Group, SlackUsergroupID: e.Target.GroupID,
				Events: e.Events}
			if u := f.userByPublicID(e.Target.UserID); u != nil {
				r.UserID, r.UserPublicID, r.UserName, r.UserLogin = pgtype.Int8{Int64: u.id, Valid: true},
					u.publicID, u.name, u.login
				r.UserDeleted = u.deleted
			}
			if tm := f.teamByPublicID(e.Target.TeamID); tm != nil {
				r.TeamID, r.TeamPublicID, r.TeamName, r.TeamSlug = pgtype.Int8{Int64: tm.id, Valid: true},
					tm.publicID, tm.name, tm.slug
				r.TeamDeleted = tm.deleted
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeQueries) DeleteMentionEntries(_ context.Context, arg dbgen.DeleteMentionEntriesParams) error {
	if err := f.err("DeleteMentionEntries"); err != nil {
		return err
	}
	delete(f.entries, arg.DestinationID)
	return nil
}

func (f *fakeQueries) InsertMentionEntries(_ context.Context, arg dbgen.InsertMentionEntriesParams) error {
	if err := f.err("InsertMentionEntries"); err != nil {
		return err
	}
	f.written = append(f.written, arg)
	for i, kind := range arg.Kinds {
		t := EntryTarget{Kind: kind, Everyone: arg.Everyones[i], Group: arg.GroupNames[i], GroupID: arg.UsergroupIds[i]}
		if u := f.userByID(arg.UserIds[i]); u != nil {
			t.UserID = u.publicID
		}
		if tm := f.teamByID(arg.TeamIds[i]); tm != nil {
			t.TeamID = tm.publicID
		}
		if f.entries == nil {
			f.entries = map[int64][]EntryInput{}
		}
		f.entries[arg.DestinationID] = append(f.entries[arg.DestinationID],
			EntryInput{Target: t, Events: strings.Split(arg.Events[i], ",")})
	}
	return nil
}

func (f *fakeQueries) ResolveEntryUsers(_ context.Context, arg dbgen.ResolveEntryUsersParams) (
	[]dbgen.ResolveEntryUsersRow, error) {
	if err := f.err("ResolveEntryUsers"); err != nil {
		return nil, err
	}
	var out []dbgen.ResolveEntryUsersRow
	for _, u := range f.users {
		if slices.Contains(arg.PublicIds, u.publicID) {
			out = append(out, dbgen.ResolveEntryUsersRow{ID: u.id, PublicID: u.publicID, Deleted: u.deleted})
		}
	}
	return out, nil
}

func (f *fakeQueries) ResolveEntryTeams(_ context.Context, arg dbgen.ResolveEntryTeamsParams) (
	[]dbgen.ResolveEntryTeamsRow, error) {
	if err := f.err("ResolveEntryTeams"); err != nil {
		return nil, err
	}
	var out []dbgen.ResolveEntryTeamsRow
	for _, tm := range f.teams {
		if slices.Contains(arg.PublicIds, tm.publicID) {
			out = append(out, dbgen.ResolveEntryTeamsRow{ID: tm.id, PublicID: tm.publicID, Deleted: tm.deleted})
		}
	}
	return out, nil
}

func (f *fakeQueries) ListLiveTeams(_ context.Context, org int64) ([]dbgen.ListLiveTeamsRow, error) {
	if err := f.err("ListLiveTeams"); err != nil || org != orgID {
		return nil, err
	}
	var out []dbgen.ListLiveTeamsRow
	for _, tm := range f.teams {
		if !tm.deleted {
			out = append(out, dbgen.ListLiveTeamsRow{ID: tm.id, PublicID: tm.publicID, Name: tm.name, Slug: tm.slug})
		}
	}
	return out, nil
}

func (f *fakeQueries) GetMentionConnection(_ context.Context, arg dbgen.GetMentionConnectionParams) (
	dbgen.GetMentionConnectionRow, error) {
	f.reads = append(f.reads, "connection")
	if err := f.err("GetMentionConnection"); err != nil {
		return dbgen.GetMentionConnectionRow{}, err
	}
	for id, c := range f.conns {
		if (arg.ID.Valid && arg.ID.Int64 == id) || (arg.PublicID.Valid && arg.PublicID.String == c.PublicID) {
			c.ID = id
			return c, nil
		}
	}
	return dbgen.GetMentionConnectionRow{}, pgx.ErrNoRows
}

func (f *fakeQueries) teamRows(ids []int64) []dbgen.ListAlertGroupTeamsRow {
	var out []dbgen.ListAlertGroupTeamsRow
	for _, id := range ids {
		if tm := f.teamByID(id); tm != nil && !tm.deleted {
			out = append(out, dbgen.ListAlertGroupTeamsRow{ID: tm.id, PublicID: tm.publicID, Name: tm.name,
				Slug: tm.slug})
		}
	}
	return out
}

func (f *fakeQueries) ListAlertGroupTeams(_ context.Context, arg dbgen.ListAlertGroupTeamsParams) (
	[]dbgen.ListAlertGroupTeamsRow, error) {
	f.reads = append(f.reads, "group teams")
	if err := f.err("ListAlertGroupTeams"); err != nil {
		return nil, err
	}
	return f.teamRows(f.groupTeams[arg.AlertGroupID]), nil
}

func (f *fakeQueries) ListStormTeams(_ context.Context, arg dbgen.ListStormTeamsParams) ([]dbgen.ListStormTeamsRow,
	error) {
	f.reads = append(f.reads, "storm teams")
	if err := f.err("ListStormTeams"); err != nil {
		return nil, err
	}
	var out []dbgen.ListStormTeamsRow
	for _, r := range f.teamRows(f.stormTeams[arg.StormID]) {
		out = append(out, dbgen.ListStormTeamsRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Slug: r.Slug})
	}
	return out, nil
}

func (f *fakeQueries) LockEntryDestination(_ context.Context, arg dbgen.LockEntryDestinationParams) (
	dbgen.LockEntryDestinationRow, error) {
	if err := f.err("LockEntryDestination"); err != nil {
		return dbgen.LockEntryDestinationRow{}, err
	}
	d, ok := f.dests[arg.ID]
	if !ok || arg.OrgID != orgID {
		return dbgen.LockEntryDestinationRow{}, pgx.ErrNoRows
	}
	return d, nil
}

func (f *fakeQueries) GetMentionEventAt(_ context.Context, arg dbgen.GetMentionEventAtParams) (time.Time, error) {
	f.reads = append(f.reads, "event at")
	if err := f.err("GetMentionEventAt"); err != nil {
		return time.Time{}, err
	}
	at, ok := f.eventAt[arg.EventSeq]
	if !ok {
		return time.Time{}, pgx.ErrNoRows
	}
	return at, nil
}

func (f *fakeQueries) GetPublicationEventAt(_ context.Context, arg dbgen.GetPublicationEventAtParams) (time.Time,
	error) {
	f.reads = append(f.reads, "publication at")
	if err := f.err("GetPublicationEventAt"); err != nil {
		return time.Time{}, err
	}
	at, ok := f.publishedAt[[2]int64{arg.AlertGroupID, arg.DestinationID}]
	if !ok {
		return time.Time{}, pgx.ErrNoRows
	}
	return at, nil
}

func (f *fakeQueries) GetStormStartedAt(_ context.Context, arg dbgen.GetStormStartedAtParams) (time.Time, error) {
	if err := f.err("GetStormStartedAt"); err != nil {
		return time.Time{}, err
	}
	at, ok := f.stormAt[arg.ID]
	if !ok {
		return time.Time{}, pgx.ErrNoRows
	}
	return at, nil
}

func (f *fakeQueries) BumpEntryDestination(_ context.Context, arg dbgen.BumpEntryDestinationParams) error {
	if err := f.err("BumpEntryDestination"); err != nil {
		return err
	}
	f.bumped = append(f.bumped, arg.ID)
	return nil
}

func (f *fakeQueries) GetEventOwners(_ context.Context, arg dbgen.GetEventOwnersParams) (dbgen.GetEventOwnersRow,
	error) {
	f.reads = append(f.reads, "owners")
	if err := f.fail["GetEventOwners"]; err != nil {
		return dbgen.GetEventOwnersRow{}, err
	}
	r, ok := f.owners[arg.EventSeq]
	if !ok {
		return dbgen.GetEventOwnersRow{}, pgx.ErrNoRows
	}
	return r, nil
}

func (f *fakeQueries) ListMentionUsers(_ context.Context, arg dbgen.ListMentionUsersParams) (
	[]dbgen.ListMentionUsersRow, error) {
	f.reads = append(f.reads, "users")
	if err := f.fail["ListMentionUsers"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListMentionUsersRow
	for _, u := range f.users {
		if u.deleted || (!slices.Contains(arg.Ids, u.id) && !slices.Contains(arg.PublicIds, u.publicID)) {
			continue
		}
		_, linked := u.externals[arg.IdentitySpace]
		out = append(out, dbgen.ListMentionUsersRow{ID: u.id, PublicID: u.publicID, Name: u.name, Login: u.login,
			Username: u.links[arg.IdentitySpace], ExternalID: u.externals[arg.IdentitySpace],
			DisplayName: u.displays[arg.IdentitySpace], Linked: linked && arg.IdentitySpace != ""})
	}
	return out, nil
}

func (f *fakeQueries) ListLiveUserPublicIDs(_ context.Context, arg dbgen.ListLiveUserPublicIDsParams) ([]string,
	error) {
	if err := f.fail["ListLiveUserPublicIDs"]; err != nil {
		return nil, err
	}
	var out []string
	for _, u := range f.users {
		if !u.deleted && slices.Contains(arg.PublicIds, u.publicID) {
			out = append(out, u.publicID)
		}
	}
	return out, nil
}

func (f *fakeQueries) ListFooterUsers(context.Context, dbgen.ListFooterUsersParams) ([]dbgen.ListFooterUsersRow,
	error) {
	return f.footer, f.fail["ListFooterUsers"]
}

// fakeOnCalls is the On-call of each Team's main Schedule: a User by id before until, nobody after it; a Team that is
// missing has no main Schedule.
type fakeOnCalls struct {
	onCall map[int64]int64
	until  time.Time
	calls  []time.Time
	dbs    []DBTX
	fail   error
}

func (o *fakeOnCalls) TeamOnCallAt(_ context.Context, db DBTX, teamID int64, at time.Time) (int64, bool, error) {
	o.calls, o.dbs = append(o.calls, at), append(o.dbs, db)
	if o.fail != nil {
		return 0, false, o.fail
	}
	user, ok := o.onCall[teamID]
	if !ok || !at.Before(o.until) {
		return 0, false, nil
	}
	return user, true, nil
}

// fakeTeamMentions are the Team mentions of the Teams for each Connection, by Team and then Connection id.
type fakeTeamMentions struct {
	mentions map[int64]map[int64]TeamMention
	dbs      []DBTX
	fail     error
}

func (m *fakeTeamMentions) ForConnection(_ context.Context, db DBTX, teamID, connectionID int64) (TeamMention, bool,
	error) {
	m.dbs = append(m.dbs, db)
	if m.fail != nil {
		return TeamMention{}, false, m.fail
	}
	tm, ok := m.mentions[teamID][connectionID]
	return tm, ok, nil
}

const (
	alice = "SRAAAAAAAAAAA1"
	bob   = "SRAAAAAAAAAAA2"
	carol = "SRAAAAAAAAAAA3"
	gone  = "SRAAAAAAAAAAA4"

	payments = "TMAAAAAAAAAAP1"
	search   = "TMAAAAAAAAAAS1"
	devops   = "TMAAAAAAAAAAD1"
	oldTeam  = "TMAAAAAAAAAAX1"
)

// newFake has alice, linked in Telegram and through the Mattermost Connection 3, bob without links, carol linked
// through the Mattermost Connection 9, and a deleted User; the Teams payments, search and devops and a deleted one;
// the Mattermost Connection 3, the Slack Connections 4 (with usergroups:read) and 6 (without), the Bridges 5 (with
// everything) and 7 (with nothing) and the gateway-only Bridge 8.
func newFake() *fakeQueries {
	return &fakeQueries{users: []user{
		{id: 1, publicID: alice, name: "Alice Smith", login: "alice",
			links:     map[string]string{"telegram": "alice_tg", "mattermost:3": "alice.mm"},
			externals: map[string]string{"telegram": "4242", "mattermost:3": "u-alice"}},
		{id: 2, publicID: bob, name: "Bob Jones", login: "bob"},
		{id: 3, publicID: carol, name: "Carol", login: "carol", links: map[string]string{"mattermost:9": "carol"},
			externals: map[string]string{"mattermost:9": "u-carol"}},
		{id: 4, publicID: gone, name: "deleted-user-4", login: "gone", deleted: true},
	}, teams: []team{
		{id: 11, publicID: payments, name: "Payments", slug: "payments"},
		{id: 12, publicID: search, name: "Search", slug: "search"},
		{id: 13, publicID: devops, name: "Devops", slug: "devops"},
		{id: 14, publicID: oldTeam, name: "Old", slug: "old", deleted: true},
	}, conns: map[int64]dbgen.GetMentionConnectionRow{
		3: {PublicID: "CNAAAAAAAAAAM3", Type: TypeMattermost, Messenger: true},
		4: {PublicID: "CNAAAAAAAAAAS4", Type: TypeSlack, UsergroupsRead: true, Messenger: true},
		6: {PublicID: "CNAAAAAAAAAAS6", Type: TypeSlack, Messenger: true},
		5: {PublicID: "CNAAAAAAAAAAB5", Type: TypeBridge, GroupMentions: true, Messenger: true,
			EveryoneMentions: []string{EveryoneChannel, EveryoneHere, EveryoneAll, EveryoneEveryone}},
		7: {PublicID: "CNAAAAAAAAAAB7", Type: TypeBridge, Messenger: true},
		8: {PublicID: "CNAAAAAAAAAAB8", Type: TypeBridge, GroupMentions: true, EveryoneMentions: []string{"channel"}},
	}, entries: map[int64][]EntryInput{}, groupTeams: map[int64][]int64{}, stormTeams: map[int64][]int64{},
		dests: map[int64]dbgen.LockEntryDestinationRow{}, owners: map[int64]dbgen.GetEventOwnersRow{},
		fail: map[string]error{}}
}

func newService(f *fakeQueries) *Service {
	s := New(orgID, nil)
	s.queries = func(DBTX) Queries { return f }
	return s
}

// wired is the Service over f with the On-call and the Team mentions: the On-call of payments is alice and that of
// devops carol until an hour after t0; search has none. payments has the Team mention payments_team for the
// Mattermost Connection 3 and payments-team (S0FAKEPAYTEAM) for the Slack Connection 4; search has none.
func wired(f *fakeQueries) (*Service, *fakeOnCalls, *fakeTeamMentions) {
	s := newService(f)
	o := &fakeOnCalls{onCall: map[int64]int64{11: 1, 13: 3}, until: t0.Add(time.Hour)}
	m := &fakeTeamMentions{mentions: map[int64]map[int64]TeamMention{
		11: {3: {Group: "payments_team"}, 4: {Group: "payments-team", GroupID: "S0FAKEPAYTEAM"}, 5: {Group: "payments"}},
		13: {3: {Group: "devops_team"}},
	}}
	s.Wire(o, m, clock.NewManual(t0))
	return s, o, m
}

func nobody() Settings { return Nobody() }

func with(kind string, s Setting) Settings {
	out := nobody()
	out[kind] = s
	return out
}

func describe(ts []Target) string {
	out := make([]string, len(ts))
	for i, tg := range ts {
		switch tg.Kind {
		case TargetEveryone:
			out[i] = "everyone:" + tg.Everyone
		case TargetGroup:
			out[i] = "group:" + tg.Group
			if tg.GroupID != "" {
				out[i] += "/" + tg.GroupID
			}
		case TargetTeam:
			out[i] = "team:" + tg.Team.Slug
		default:
			out[i] = "user:" + tg.User.Display()
		}
	}
	return strings.Join(out, ",")
}

func ent(kind string, events ...string) EntryInput {
	if len(events) == 0 {
		events = Events
	}
	return EntryInput{Target: EntryTarget{Kind: kind}, Events: events}
}

func everyone(word string, events ...string) EntryInput {
	e := ent(TargetEveryone, events...)
	e.Target.Everyone = word
	return e
}

func person(id string, events ...string) EntryInput {
	e := ent(TargetUser, events...)
	e.Target.UserID = id
	return e
}

func group(name, id string, events ...string) EntryInput {
	e := ent(TargetGroup, events...)
	e.Target.Group, e.Target.GroupID = name, id
	return e
}

func teamEntry(kind, id string, events ...string) EntryInput {
	e := ent(kind, events...)
	e.Target.TeamID = id
	return e
}

var errBoom = errors.New("boom")

// TestValidate (C-12.FR-8): every kind is present; everyone is one of none, channel, all and here, and only none for
// Telegram; user_ids name Users that exist and are not deleted; groups are Mattermost group names and none for
// Telegram; outgoing webhooks take every choice.
func TestValidate(t *testing.T) {
	ctx := t.Context()
	s := newService(newFake())
	full := Setting{Everyone: EveryoneChannel, UserIDs: []string{strings.ToLower(alice)}, Groups: []string{"on-call"}}
	for _, typ := range []string{TypeMattermost, TypeWebhook} {
		for _, word := range []string{EveryoneNone, EveryoneChannel, EveryoneAll, EveryoneHere} {
			set := with(KindReopen, full)
			st := set[KindReopen]
			st.Everyone = word
			set[KindReopen] = st
			if err := s.Validate(ctx, typ, set, nil); err != nil {
				t.Errorf("%s %s: %v", typ, word, err)
			}
		}
	}
	if err := s.Validate(ctx, TypeTelegram, with(KindNewAlerts, Setting{Everyone: EveryoneNone,
		UserIDs: []string{alice, bob}}), nil); err != nil {
		t.Errorf("telegram with users: %v", err)
	}
	missing := nobody()
	delete(missing, KindSnoozeEnded)
	extra := nobody()
	extra["reminder"] = Setting{Everyone: EveryoneNone}
	for _, tc := range []struct {
		typ           string
		set           Settings
		pointer, code string
	}{
		{TypeMattermost, missing, "/mentions/snooze_ended", CodeRequired},
		{TypeMattermost, extra, "/mentions/reminder", CodeInvalidFormat},
		{TypeMattermost, with(KindReopen, Setting{Everyone: "everyone"}), "/mentions/reopen/everyone", CodeInvalidFormat},
		{TypeTelegram, with(KindNewAlertGroup, Setting{Everyone: EveryoneChannel}), "/mentions/new_alert_group/everyone",
			CodeUnsupported},
		{TypeTelegram, with(KindAckTimeout, Setting{Everyone: EveryoneHere}), "/mentions/ack_timeout/everyone",
			CodeUnsupported},
		{TypeTelegram, with(KindRiseToUrgent, Setting{Everyone: EveryoneNone, Groups: []string{"ops"}}),
			"/mentions/rise_to_urgent/groups", CodeUnsupported},
		{TypeMattermost, with(KindNewAlerts, Setting{Everyone: EveryoneNone, Groups: []string{"bad name"}}),
			"/mentions/new_alerts/groups/0", CodeInvalidFormat},
		{TypeWebhook, with(KindNewAlerts, Setting{Everyone: EveryoneNone, Groups: []string{""}}),
			"/mentions/new_alerts/groups/0", CodeInvalidFormat},
		{TypeMattermost, with(KindReopen, Setting{Everyone: EveryoneNone, UserIDs: []string{alice, "nope"}}),
			"/mentions/reopen/user_ids/1", CodeUnknownID},
		{TypeMattermost, with(KindReopen, Setting{Everyone: EveryoneNone, UserIDs: []string{alice, gone}}),
			"/mentions/reopen/user_ids/1", CodeUnknownID},
		{TypeMattermost, with(KindReopen, Setting{Everyone: EveryoneNone, UserIDs: []string{"SRZZZZZZZZZZZZ"}}),
			"/mentions/reopen/user_ids/0", CodeUnknownID},
		{TypeMattermost, with(KindReopen, Setting{Everyone: EveryoneNone, UserIDs: []string{alice,
			strings.ToLower(alice)}}), "/mentions/reopen/user_ids/1", CodeDuplicate},
	} {
		err := s.Validate(ctx, tc.typ, tc.set, nil)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != tc.pointer || fe.Code != tc.code || fe.Error() == "" {
			t.Errorf("%s %s: %v, want %s", tc.typ, tc.pointer, err, tc.code)
		}
	}
	f := newFake()
	f.fail["ListLiveUserPublicIDs"] = errors.New("boom")
	if err := newService(f).Validate(ctx, TypeMattermost, with(KindReopen, full), nil); err == nil {
		t.Error("a failing read of the users")
	}
}

// TestValidateBridge covers C-22.FR-3 and AC-20: a Bridge Destination takes nobody and chosen Users for every kind,
// and refuses everyone in the chat and messenger groups with unsupported at their pointer, in a text that names
// Bridges; Telegram's texts are its own.
func TestValidateBridge(t *testing.T) {
	ctx := t.Context()
	s := newService(newFake())
	if err := s.Validate(ctx, TypeBridge, nobody(), nil); err != nil {
		t.Errorf("nobody: %v", err)
	}
	for _, kind := range Kinds {
		users := with(kind, Setting{Everyone: EveryoneNone, UserIDs: []string{alice, bob}, Groups: []string{}})
		if err := s.Validate(ctx, TypeBridge, users, nil); err != nil {
			t.Errorf("%s with chosen users: %v", kind, err)
		}
		for _, word := range []string{EveryoneChannel, EveryoneAll, EveryoneHere} {
			err := s.Validate(ctx, TypeBridge, with(kind, Setting{Everyone: word}), nil)
			fe, ok := errors.AsType[*FieldError](err)
			if !ok || fe.Pointer != "/mentions/"+kind+"/everyone" || fe.Code != CodeUnsupported ||
				fe.Detail != "A Bridge has no Mention of everyone in the chat." {
				t.Errorf("%s everyone %s: %v", kind, word, err)
			}
		}
		err := s.Validate(ctx, TypeBridge, with(kind, Setting{Everyone: EveryoneNone, Groups: []string{"ops"}}), nil)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != "/mentions/"+kind+"/groups" || fe.Code != CodeUnsupported ||
			fe.Detail != "A Bridge has no messenger groups." {
			t.Errorf("%s groups: %v", kind, err)
		}
		if err := s.Validate(ctx, TypeBridge, with(kind, Setting{Everyone: EveryoneNone,
			UserIDs: []string{gone}}), nil); err == nil {
			t.Errorf("%s with a deleted user", kind)
		}
	}
	for _, tc := range []struct {
		set    Settings
		detail string
	}{
		{with(KindReopen, Setting{Everyone: EveryoneAll}), "Telegram has no Mention of everyone in the chat."},
		{with(KindReopen, Setting{Everyone: EveryoneNone, Groups: []string{"ops"}}), "Telegram has no groups."},
	} {
		if fe, ok := errors.AsType[*FieldError](s.Validate(ctx, TypeTelegram, tc.set, nil)); !ok || fe.Detail != tc.detail {
			t.Errorf("telegram: %v, want %q", fe, tc.detail)
		}
	}
}

// TestNames (C-12.FR-12): a user is shown by the username of their Account link, else by the display name; the
// footer's user comes by identity space with the link's username, user id and display name on one line, wherever
// its messenger can mention them — a Mattermost link only with a username, any other with a user id — and is left out
// elsewhere, so that the footer names the User by their Muster display name (C-22.FR-16, C-24.FR-6); identity spaces
// follow the Destination type and Connection.
func TestNames(t *testing.T) {
	if (User{Name: "Alice", Username: "alice"}).Display() != "alice" || (User{Name: "Alice"}).Display() != "Alice" {
		t.Error("display names")
	}
	conn := int64(3)
	if IdentitySpace(TypeTelegram, &conn) != "telegram" || IdentitySpace(TypeMattermost, &conn) != "mattermost:3" ||
		IdentitySpace(TypeMattermost, nil) != "" || IdentitySpace(TypeWebhook, nil) != "" ||
		IdentitySpace(TypeBridge, &conn) != "bridge:3" || IdentitySpace(TypeBridge, nil) != "" ||
		IdentitySpace(TypeSlack, &conn) != "slack:3" || IdentitySpace(TypeSlack, nil) != "" {
		t.Error("identity spaces")
	}
	f := newFake()
	s := newService(f)
	if got, err := s.FooterUsers(t.Context(), nil, 5); err != nil || got != nil {
		t.Errorf("no links: %v %v", got, err)
	}
	row := func(space, messenger, username, external, display string) dbgen.ListFooterUsersRow {
		return dbgen.ListFooterUsersRow{IdentitySpace: space, Messenger: messenger, Username: username,
			ExternalID: external, DisplayName: display, Name: "Alice Smith"}
	}
	f.footer = []dbgen.ListFooterUsersRow{
		row("bridge:4", TypeBridge, "alice@example.org", "u-alice", "Alice Smith"),
		row("bridge:5", TypeBridge, "", "u-alice", " Alice\n\t Smith\u0085\r\n"),
		row("bridge:7", TypeBridge, "", "", "Alice"),
		row("mattermost:3", TypeMattermost, " alice.mm\n", "mm-alice", "Alice Smith"),
		row("mattermost:8", TypeMattermost, " ", "mm-alice", ""),
		row("slack:9", TypeSlack, "alice", "U0FAKEALICE", " Alice\n Smith "),
		row("telegram", TypeTelegram, "", "7001", "")}
	got, err := s.FooterUsers(t.Context(), nil, 5)
	user := func(username, external, display string) User {
		return User{Name: "Alice Smith", Username: username, ExternalID: external, DisplayName: display}
	}
	// Only what each messenger's Mention uses: a Mattermost username, a Telegram or Slack user id, and a Bridge link's
	// user id, display name and username.
	if want := map[string]User{"bridge:4": user("alice@example.org", "u-alice", "Alice Smith"),
		"bridge:5": user("", "u-alice", "Alice Smith"), "mattermost:3": user("alice.mm", "", ""),
		"slack:9": user("", "U0FAKEALICE", ""), "telegram": user("", "7001", "")}; err != nil ||
		!maps.Equal(got, want) {
		t.Errorf("footer users %+v %v, want %+v", got, err, want)
	}
	f.footer = []dbgen.ListFooterUsersRow{row("mattermost:8", TypeMattermost, "\t", "mm-alice", "")}
	if got, err := s.FooterUsers(t.Context(), nil, 5); err != nil || got != nil {
		t.Errorf("no link to mention by: %v %v", got, err)
	}
	f.fail["ListFooterUsers"] = errors.New("boom")
	if _, err := s.FooterUsers(t.Context(), nil, 5); err == nil {
		t.Error("a failing read")
	}
}

// TestValidateSlack covers C-24.FR-6, AC-15 and C-12.FR-8, a case per kind: a Slack Destination takes nobody, channel,
// here and chosen Users, and refuses all and messenger groups with unsupported at their pointer, in texts that name
// Slack.
func TestValidateSlack(t *testing.T) {
	ctx := t.Context()
	s := newService(newFake())
	if err := s.Validate(ctx, TypeSlack, nobody(), nil); err != nil {
		t.Errorf("nobody: %v", err)
	}
	for _, kind := range Kinds {
		for _, word := range []string{EveryoneChannel, EveryoneHere} {
			if err := s.Validate(ctx, TypeSlack, with(kind, Setting{Everyone: word, UserIDs: []string{alice},
				Groups: []string{}}), nil); err != nil {
				t.Errorf("%s %s: %v", kind, word, err)
			}
		}
		users := with(kind, Setting{Everyone: EveryoneNone, UserIDs: []string{alice, bob}, Groups: []string{}})
		if err := s.Validate(ctx, TypeSlack, users, nil); err != nil {
			t.Errorf("%s with chosen users: %v", kind, err)
		}
		err := s.Validate(ctx, TypeSlack, with(kind, Setting{Everyone: EveryoneAll}), nil)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != "/mentions/"+kind+"/everyone" || fe.Code != CodeUnsupported ||
			fe.Detail != "Slack has no Mention of all; choose channel or here." {
			t.Errorf("%s all: %v", kind, err)
		}
		err = s.Validate(ctx, TypeSlack, with(kind, Setting{Everyone: EveryoneHere, Groups: []string{"oncall"}}), nil)
		fe, ok = errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != "/mentions/"+kind+"/groups" || fe.Code != CodeUnsupported ||
			fe.Detail != "Slack has no messenger groups that Muster can name." {
			t.Errorf("%s groups: %v", kind, err)
		}
		if err := s.Validate(ctx, TypeSlack, with(kind, Setting{Everyone: EveryoneNone,
			UserIDs: []string{gone}}), nil); err == nil {
			t.Errorf("%s with a deleted user", kind)
		}
	}
	for _, typ := range []string{TypeMattermost, TypeWebhook} {
		if err := s.Validate(ctx, typ, with(KindNewAlerts, Setting{Everyone: EveryoneAll,
			Groups: []string{"oncall"}}), nil); err != nil {
			t.Errorf("%s takes all and groups: %v", typ, err)
		}
	}
}

// resolveFake has the Destinations of TestResolve: the Mattermost one 10 on the Connection 3, the Telegram one 20, the
// outgoing webhook 30, the Slack ones 40 on the Connection 4 and 41 on the Connection 6, and the Bridge ones 50, 51
// and 52 on the Connections 5, 7 and 8; the Alert Group 5 has payments, search and a deleted Team, and the Storm 9
// counts Alert Groups of devops.
func resolveFake() *fakeQueries {
	f := newFake()
	f.entries[10] = []EntryInput{ent(TargetOnCallOfTeamsFromLabel), everyone(EveryoneHere, KindNewAlertGroup),
		teamEntry(TargetTeam, payments), teamEntry(TargetTeam, search), person(alice, KindNewAlerts),
		person(bob, KindNewAlerts), group("oncall", "", KindNewAlertGroup), person(gone, KindNewAlerts),
		ent(TargetTeamsFromLabel, EventStormStarted)}
	f.entries[20] = []EntryInput{person(alice, KindNewAlerts), person(bob, KindNewAlerts),
		teamEntry(TargetOnCallOfTeam, payments, KindNewAlerts), everyone(EveryoneChannel, KindNewAlerts)}
	f.entries[30] = []EntryInput{person(bob), teamEntry(TargetTeam, payments), ent(TargetTeamsFromLabel),
		teamEntry(TargetOnCallOfTeam, devops), everyone(EveryoneEveryone), teamEntry(TargetTeam, oldTeam)}
	slackEntries := []EntryInput{group("payments-team", "S0FAKEPAYTEAM"), teamEntry(TargetTeam, payments),
		everyone(EveryoneHere), everyone(EveryoneAll)}
	f.entries[40], f.entries[41] = slackEntries, slackEntries
	bridgeEntries := []EntryInput{everyone(EveryoneChannel), group("ops", ""), person(alice),
		teamEntry(TargetTeam, payments)}
	f.entries[50], f.entries[51], f.entries[52] = bridgeEntries, bridgeEntries, bridgeEntries
	f.groupTeams[5] = []int64{11, 12, 14}
	f.stormTeams[9] = []int64{13}
	return f
}

// TestResolve is C-28.FR-3, FR-4 and FR-7, and AC-1 and AC-2: for a Loud event the entries that apply to it, in their
// order — the On-call of the Teams from the label at the time of the event and nobody in a gap, everyone with its
// word, a Team by its Team mention for the Connection and nobody without one, never its On-call, a group by its name, a
// linked person by their Account link — and nobody that the Connection cannot mention; a person without an Account
// link in the identity space and a deleted User are left out, and each person and group is mentioned once; the first
// publication of a Storm summary takes the Teams of the Alert Groups the Storm counts. The Owner and the previous
// Owner are named as L1 named them, at their places among the symbolic Mentions.
func TestResolve(t *testing.T) {
	ctx := t.Context()
	f := resolveFake()
	s, _, _ := wired(f)
	conn := func(id int64) *int64 { return &id }
	mm := Request{DestinationID: 10, DestinationType: TypeMattermost, ConnectionID: conn(3), AlertGroupID: 5, At: t0}
	f.owners[11] = dbgen.GetEventOwnersRow{OwnerUserID: pgtype.Int8{Int64: 3, Valid: true}}
	f.owners[12] = dbgen.GetEventOwnersRow{OwnerUserID: pgtype.Int8{Int64: 2, Valid: true},
		PreviousOwnerUserID: pgtype.Int8{Int64: 1, Valid: true}}
	f.owners[13] = dbgen.GetEventOwnersRow{PreviousOwnerUserID: pgtype.Int8{Int64: 1, Valid: true}}
	later := mm
	later.At = t0.Add(2 * time.Hour)
	storm := Request{DestinationID: 10, DestinationType: TypeMattermost, ConnectionID: conn(3), StormID: 9, At: t0}
	for _, tc := range []struct {
		name     string
		r        Request
		seq      int64
		mentions []string
		want     string
	}{
		{"a new alert group", mm, 0, []string{KindNewAlertGroup},
			"user:alice.mm,everyone:here,group:payments_team,group:oncall"},
		{"its reopen", mm, 0, []string{KindReopen}, "user:alice.mm,group:payments_team"},
		{"new alerts: each person once, unlinked and deleted ones left out", mm, 0, []string{KindNewAlerts},
			"user:alice.mm,group:payments_team"},
		{"in a gap of the schedule", later, 0, []string{KindNewAlertGroup},
			"everyone:here,group:payments_team,group:oncall"},
		{"the start of a storm", storm, 0, []string{EventStormStarted}, "group:payments_team,group:devops_team"},
		{"the owner without a link", mm, 11, []string{Owner}, "user:Carol"},
		{"the previous owner", mm, 12, []string{PreviousOwner}, "user:alice.mm"},
		{"the released owner, then the entries", mm, 13, []string{Owner, KindRiseToUrgent},
			"user:alice.mm,group:payments_team"},
		{"an owner the event did not record", mm, 99, []string{Owner}, ""},
		{"an owner without an event", mm, 0, []string{Owner}, ""},
		{"nothing", mm, 0, nil, ""},
		{"telegram: linked people and the on-call only", Request{DestinationID: 20, DestinationType: TypeTelegram,
			AlertGroupID: 5, At: t0}, 0, []string{KindNewAlerts}, "user:alice_tg"},
		{"an outgoing webhook takes every target as data", Request{DestinationID: 30, DestinationType: TypeWebhook,
			AlertGroupID: 5, At: t0}, 0, []string{KindAckTimeout},
			"user:Bob Jones,team:payments,team:search,user:Carol,everyone:everyone"},
		{"slack with usergroups:read", Request{DestinationID: 40, DestinationType: TypeSlack, ConnectionID: conn(4),
			AlertGroupID: 5, At: t0}, 0, []string{KindNewAlertGroup},
			"group:payments-team/S0FAKEPAYTEAM,everyone:here"},
		{"slack without it", Request{DestinationID: 41, DestinationType: TypeSlack, ConnectionID: conn(6),
			AlertGroupID: 5, At: t0}, 0, []string{KindNewAlertGroup}, "everyone:here"},
		{"a bridge that declares everything", Request{DestinationID: 50, DestinationType: TypeBridge,
			ConnectionID: conn(5), AlertGroupID: 5, At: t0}, 0, []string{KindNewAlertGroup},
			"everyone:channel,group:ops,group:payments"},
		{"a bridge that declares nothing", Request{DestinationID: 51, DestinationType: TypeBridge,
			ConnectionID: conn(7), AlertGroupID: 5, At: t0}, 0, []string{KindNewAlertGroup}, ""},
		{"a notification gateway only", Request{DestinationID: 52, DestinationType: TypeBridge,
			ConnectionID: conn(8), AlertGroupID: 5, At: t0}, 0, []string{KindNewAlertGroup}, ""},
		{"a destination without entries", Request{DestinationID: 77, DestinationType: TypeMattermost,
			ConnectionID: conn(3)}, 0, []string{KindNewAlertGroup}, ""},
	} {
		r := tc.r
		r.Seq, r.Mentions = tc.seq, tc.mentions
		got, err := s.Resolve(ctx, nil, orgID, r)
		if err != nil || describe(got) != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.name, describe(got), err, tc.want)
		}
	}
	got, _ := s.Resolve(ctx, nil, orgID, Request{DestinationID: 10, DestinationType: TypeMattermost,
		ConnectionID: conn(3), Mentions: []string{KindNewAlerts}})
	if len(got) != 2 || got[1].User == nil {
		t.Fatalf("without an alert group, no teams from the label: %+v", got)
	}
	if u := got[1].User; u.PublicID != alice || u.Name != "Alice Smith" || u.Login != "alice" || u.Username != "alice.mm" {
		t.Errorf("user target %+v", u)
	}
	f.reads = nil
	if _, err := s.Resolve(ctx, nil, orgID, Request{DestinationID: 10, Mentions: []string{Owner}}); err != nil ||
		slices.Contains(f.reads, "entries") || slices.Contains(f.reads, "owners") {
		t.Errorf("an Owner without an event reads %v, %v", f.reads, err)
	}

	// Without the On-call and the Team mentions wired, a Team and an On-call mention nobody.
	plain := newService(f)
	if got, err := plain.Resolve(ctx, nil, orgID, Request{DestinationID: 10, DestinationType: TypeMattermost,
		ConnectionID: conn(3), AlertGroupID: 5, Mentions: []string{KindNewAlertGroup}}); err != nil ||
		describe(got) != "everyone:here,group:oncall" {
		t.Errorf("not wired = %q %v", describe(got), err)
	}

	for _, name := range []string{"ListMentionEntries", "GetMentionConnection", "ListAlertGroupTeams",
		"ListStormTeams", "ListMentionUsers", "GetEventOwners", "on-calls", "team mentions"} {
		g := resolveFake()
		g.owners = f.owners
		s, o, m := wired(g)
		switch name {
		case "on-calls":
			o.fail = errBoom
		case "team mentions":
			m.fail = errBoom
		default:
			g.fail[name] = errBoom
		}
		r := Request{DestinationID: 10, DestinationType: TypeMattermost, ConnectionID: conn(3), AlertGroupID: 5,
			Seq: 11, At: t0, Mentions: []string{Owner, KindNewAlertGroup}}
		if name == "ListStormTeams" {
			r.StormID, r.Mentions = 9, []string{EventStormStarted}
		}
		if _, err := s.Resolve(ctx, nil, orgID, r); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
	}
}

// TestResolveTimeAndUsers: a request without a time takes the On-call at the business clock's now; a User target
// carries the messenger's user id of the Account link in the Destination's identity space, which Telegram mentions a
// user by (C-12.FR-8), and through a Bridge the link's display name (C-22.FR-16); in Slack a linked User has the link's
// user id and name (C-24.FR-6).
func TestResolveTimeAndUsers(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	f.entries[20] = []EntryInput{person(alice), teamEntry(TargetOnCallOfTeam, payments)}
	f.users[0].links["bridge:5"] = "alice@example.org"
	f.users[0].externals["bridge:5"] = "u-alice"
	f.users[0].displays = map[string]string{"bridge:5": "Alice S.", "slack:4": "Alice Slack"}
	f.users[0].links["slack:4"] = "alice"
	f.users[0].externals["slack:4"] = "U0FAKEALICE"
	f.entries[50] = []EntryInput{person(alice)}
	f.entries[40] = []EntryInput{person(alice), person(bob)}
	s, o, _ := wired(f)
	got, err := s.Resolve(ctx, nil, orgID, Request{DestinationID: 20, DestinationType: TypeTelegram,
		AlertGroupID: 5, Mentions: []string{KindNewAlerts}})
	if err != nil || len(got) != 1 || got[0].User.ExternalID != "4242" || len(o.calls) != 1 ||
		!o.calls[0].Equal(t0) {
		t.Fatalf("telegram = %+v, %v, on-call asked at %v", got, err, o.calls)
	}
	conn := int64(5)
	got, err = s.Resolve(ctx, nil, orgID, Request{DestinationID: 50, DestinationType: TypeBridge,
		ConnectionID: &conn, Mentions: []string{KindNewAlertGroup}})
	if err != nil || len(got) != 1 || got[0].User.ExternalID != "u-alice" || got[0].User.DisplayName != "Alice S." ||
		got[0].User.Username != "alice@example.org" {
		t.Errorf("bridge = %+v, %v", got, err)
	}
	conn = 4
	got, err = s.Resolve(ctx, nil, orgID, Request{DestinationID: 40, DestinationType: TypeSlack,
		ConnectionID: &conn, Mentions: []string{KindNewAlertGroup}})
	if err != nil || len(got) != 1 || got[0].User.ExternalID != "U0FAKEALICE" || got[0].User.Username != "alice" {
		t.Errorf("slack = %+v, %v", got, err)
	}
}

// dummyDB is a transaction the fakes ignore, to see that it reaches the On-call and the Team mentions.
type dummyDB struct{ DBTX }

// TestResolveEventTime is C-28.FR-3 "at the moment of the event": a request without a time takes the On-call at the
// time of its event, read in its transaction, not at the attempt — a reply at its lifecycle event's Timeline entry, a
// first Publication at the latest of the creation and destination_joined in the Destination, the first publication of
// a Storm summary at the Storm's start — so that a send the limiter delayed past a change of Shift, or a retry, still
// mentions the On-call of the event; without such a record, the business clock's now. The On-call and the Team
// mentions are read through the transaction of the resolution. A Slack user group is mentioned once whatever handle
// names it.
func TestResolveEventTime(t *testing.T) {
	ctx := t.Context()
	f := resolveFake()
	s, o, m := wired(f)
	s.Wire(o, m, clock.NewManual(t0.Add(2*time.Hour))) // the attempt, after alice's Shift ended
	f.eventAt = map[int64]time.Time{21: t0}
	f.publishedAt = map[[2]int64]time.Time{{5, 10}: t0.Add(time.Minute)}
	f.stormAt = map[int64]time.Time{9: t0}
	conn := int64(3)
	tx := dummyDB{}
	for _, tc := range []struct {
		name string
		r    Request
		want string
	}{
		{"a reply", Request{DestinationID: 10, DestinationType: TypeMattermost, ConnectionID: &conn, AlertGroupID: 5,
			Seq: 21, Mentions: []string{KindReopen}}, "user:alice.mm,group:payments_team"},
		{"a first publication", Request{DestinationID: 10, DestinationType: TypeMattermost, ConnectionID: &conn,
			AlertGroupID: 5, Mentions: []string{KindNewAlertGroup}},
			"user:alice.mm,everyone:here,group:payments_team,group:oncall"},
		{"a storm summary", Request{DestinationID: 30, DestinationType: TypeWebhook, StormID: 9,
			Mentions: []string{EventStormStarted}},
			"user:Bob Jones,team:payments,team:devops,user:Carol,everyone:everyone"},
		{"an event without a record", Request{DestinationID: 10, DestinationType: TypeMattermost, ConnectionID: &conn,
			AlertGroupID: 5, Seq: 99, Mentions: []string{KindReopen}}, "group:payments_team"},
		{"no alert group", Request{DestinationID: 10, DestinationType: TypeMattermost, ConnectionID: &conn,
			Mentions: []string{KindNewAlerts}}, "group:payments_team,user:alice.mm"},
	} {
		got, err := s.Resolve(ctx, tx, orgID, tc.r)
		if err != nil || describe(got) != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.name, describe(got), err, tc.want)
		}
	}
	if len(o.dbs) == 0 || o.dbs[0] != DBTX(tx) || len(m.dbs) == 0 || m.dbs[0] != DBTX(tx) {
		t.Errorf("the transaction of the resolution did not reach the on-call %v and the team mentions %v", o.dbs,
			m.dbs)
	}
	for _, name := range []string{"GetMentionEventAt", "GetPublicationEventAt", "GetStormStartedAt"} {
		f.fail[name] = errBoom
		r := Request{DestinationID: 10, DestinationType: TypeMattermost, ConnectionID: &conn, AlertGroupID: 5,
			Seq: 21, Mentions: []string{KindReopen}}
		switch name {
		case "GetPublicationEventAt":
			r.Seq = 0
		case "GetStormStartedAt":
			r.AlertGroupID, r.Seq, r.StormID = 0, 0, 9
		}
		if _, err := s.Resolve(ctx, nil, orgID, r); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(f.fail, name)
	}

	// A Slack user group entry and the Team mention of the same user group, named by other handles, mention once.
	f.entries[42] = []EntryInput{group("other-handle", "S0FAKEPAYTEAM"), teamEntry(TargetTeam, payments)}
	slack := int64(4)
	got, err := s.Resolve(ctx, nil, orgID, Request{DestinationID: 42, DestinationType: TypeSlack, ConnectionID: &slack,
		Mentions: []string{KindNewAlerts}})
	if err != nil || describe(got) != "group:other-handle/S0FAKEPAYTEAM" {
		t.Errorf("one slack user group = %q, %v", describe(got), err)
	}
	if !same(Target{Kind: TargetGroup, Group: "a"}, Target{Kind: TargetGroup, Group: "a"}) ||
		same(Target{Kind: TargetGroup, Group: "a"}, Target{Kind: TargetGroup, Group: "a", GroupID: "S1"}) {
		t.Error("same groups")
	}
}
