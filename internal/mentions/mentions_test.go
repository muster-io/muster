// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mentions

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/mentions/dbgen"
)

const orgID = 7

type user struct {
	id                    int64
	publicID, name, login string
	deleted               bool
	// links are the usernames by identity space, and externals the messenger's user ids.
	links     map[string]string
	externals map[string]string
}

// fakeQueries is the database of the package in memory.
type fakeQueries struct {
	users    []user
	settings map[int64][]byte
	owners   map[int64]dbgen.GetEventOwnersRow
	footer   []dbgen.ListFooterUsernamesRow
	fail     map[string]error
	reads    []string
}

func (f *fakeQueries) GetDestinationMentions(_ context.Context, arg dbgen.GetDestinationMentionsParams) ([]byte,
	error) {
	f.reads = append(f.reads, "settings")
	if err := f.fail["GetDestinationMentions"]; err != nil {
		return nil, err
	}
	b, ok := f.settings[arg.ID]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return b, nil
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
		out = append(out, dbgen.ListMentionUsersRow{ID: u.id, PublicID: u.publicID, Name: u.name, Login: u.login,
			Username: u.links[arg.IdentitySpace], ExternalID: u.externals[arg.IdentitySpace]})
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

func (f *fakeQueries) ListFooterUsernames(context.Context, dbgen.ListFooterUsernamesParams) (
	[]dbgen.ListFooterUsernamesRow, error) {
	return f.footer, f.fail["ListFooterUsernames"]
}

const (
	alice = "SRAAAAAAAAAAA1"
	bob   = "SRAAAAAAAAAAA2"
	carol = "SRAAAAAAAAAAA3"
	gone  = "SRAAAAAAAAAAA4"
)

func newFake() *fakeQueries {
	return &fakeQueries{users: []user{
		{id: 1, publicID: alice, name: "Alice Smith", login: "alice",
			links:     map[string]string{"telegram": "alice_tg", "mattermost:3": "alice.mm"},
			externals: map[string]string{"telegram": "4242", "mattermost:3": "u-alice"}},
		{id: 2, publicID: bob, name: "Bob Jones", login: "bob"},
		{id: 3, publicID: carol, name: "Carol", login: "carol", links: map[string]string{"mattermost:9": "carol"}},
		{id: 4, publicID: gone, name: "deleted-user-4", login: "gone", deleted: true},
	}, settings: map[int64][]byte{}, owners: map[int64]dbgen.GetEventOwnersRow{}, fail: map[string]error{}}
}

func newService(f *fakeQueries) *Service {
	s := New(orgID, nil)
	s.queries = func(DBTX) Queries { return f }
	return s
}

func nobody() Settings {
	out := Settings{}
	for _, k := range Kinds {
		out[k] = Setting{Everyone: EveryoneNone, UserIDs: []string{}, Groups: []string{}}
	}
	return out
}

func with(kind string, s Setting) Settings {
	out := nobody()
	out[kind] = s
	return out
}

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
			if err := s.Validate(ctx, typ, set); err != nil {
				t.Errorf("%s %s: %v", typ, word, err)
			}
		}
	}
	if err := s.Validate(ctx, TypeTelegram, with(KindNewAlerts, Setting{Everyone: EveryoneNone,
		UserIDs: []string{alice, bob}})); err != nil {
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
		err := s.Validate(ctx, tc.typ, tc.set)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != tc.pointer || fe.Code != tc.code || fe.Error() == "" {
			t.Errorf("%s %s: %v, want %s", tc.typ, tc.pointer, err, tc.code)
		}
	}
	f := newFake()
	f.fail["ListLiveUserPublicIDs"] = errors.New("boom")
	if err := newService(f).Validate(ctx, TypeMattermost, with(KindReopen, full)); err == nil {
		t.Error("a failing read of the users")
	}
}

func settingsJSON(t *testing.T, s Settings) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func describe(ts []Target) string {
	out := make([]string, len(ts))
	for i, tg := range ts {
		switch tg.Kind {
		case TargetEveryone:
			out[i] = "everyone:" + tg.Everyone
		case TargetGroup:
			out[i] = "group:" + tg.Group
		default:
			out[i] = "user:" + tg.User.Display()
		}
	}
	return strings.Join(out, ",")
}

// TestResolve (C-12.FR-8, C-12.FR-12): each kind resolves into the Destination's setting, the Owner into the Owner the
// event recorded — or the Owner it released — the previous Owner into the one a Takeover replaced; users are named
// by their username in the Destination's identity space, else by their display name; Telegram gets no everyone and no
// groups; duplicates and deleted users are left out.
func TestResolve(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s := newService(f)
	set := nobody()
	set[KindNewAlertGroup] = Setting{Everyone: EveryoneChannel, Groups: []string{"oncall"}}
	set[KindNewAlerts] = Setting{Everyone: EveryoneNone, UserIDs: []string{strings.ToLower(alice), bob, gone, "x"}}
	set[KindReopen] = Setting{Everyone: EveryoneAll, UserIDs: []string{alice}}
	f.settings[10] = settingsJSON(t, set)
	f.settings[20] = settingsJSON(t, set)
	f.settings[30] = []byte(`{}`)
	f.settings[40] = []byte(`[`)
	conn := int64(3)
	mm := Request{DestinationID: 10, DestinationType: TypeMattermost, ConnectionID: &conn, AlertGroupID: 5}
	tg := Request{DestinationID: 20, DestinationType: TypeTelegram, AlertGroupID: 5}
	f.owners[11] = dbgen.GetEventOwnersRow{OwnerUserID: pgtype.Int8{Int64: 3, Valid: true}}
	f.owners[12] = dbgen.GetEventOwnersRow{OwnerUserID: pgtype.Int8{Int64: 2, Valid: true},
		PreviousOwnerUserID: pgtype.Int8{Int64: 1, Valid: true}}
	f.owners[13] = dbgen.GetEventOwnersRow{PreviousOwnerUserID: pgtype.Int8{Int64: 1, Valid: true}}
	for _, tc := range []struct {
		r        Request
		seq      int64
		mentions []string
		want     string
	}{
		{mm, 0, []string{KindNewAlertGroup}, "everyone:channel,group:oncall"},
		{tg, 0, []string{KindNewAlertGroup}, ""},
		{mm, 0, []string{KindNewAlerts}, "user:alice.mm,user:Bob Jones"},
		{tg, 0, []string{KindNewAlerts}, "user:alice_tg,user:Bob Jones"},
		{mm, 0, []string{KindReopen, KindNewAlerts}, "everyone:all,user:alice.mm,user:Bob Jones"},
		{mm, 0, []string{KindSnoozeEnded}, ""},
		{mm, 11, []string{Owner}, "user:Carol"},
		{mm, 12, []string{PreviousOwner}, "user:alice.mm"},
		{mm, 13, []string{Owner, KindRiseToUrgent}, "user:alice.mm"},
		{mm, 99, []string{Owner}, ""},
		{mm, 0, []string{Owner}, ""},
		{mm, 0, nil, ""},
		{Request{DestinationID: 30, DestinationType: TypeWebhook, AlertGroupID: 5}, 0, []string{KindNewAlertGroup}, ""},
		{Request{DestinationID: 77, DestinationType: TypeMattermost}, 0, []string{KindNewAlertGroup}, ""},
	} {
		r := tc.r
		r.Seq, r.Mentions = tc.seq, tc.mentions
		got, err := s.Resolve(ctx, nil, orgID, r)
		if err != nil || describe(got) != tc.want {
			t.Errorf("%s %v seq %d: %q %v, want %q", r.DestinationType, tc.mentions, tc.seq, describe(got), err,
				tc.want)
		}
	}
	got, _ := s.Resolve(ctx, nil, orgID, Request{DestinationID: 10, DestinationType: TypeMattermost,
		ConnectionID: &conn, Mentions: []string{KindNewAlerts}})
	if u := got[0].User; u.PublicID != alice || u.Name != "Alice Smith" || u.Login != "alice" || u.Username != "alice.mm" {
		t.Errorf("user target %+v", u)
	}
	f.reads = nil
	if _, err := s.Resolve(ctx, nil, orgID, Request{DestinationID: 10, Mentions: []string{Owner}}); err != nil ||
		len(f.reads) != 0 {
		t.Errorf("an Owner without an event reads nothing: %v %v", f.reads, err)
	}
	for _, tc := range []struct {
		fail string
		r    Request
	}{
		{"GetDestinationMentions", Request{DestinationID: 10, Mentions: []string{KindNewAlerts}}},
		{"", Request{DestinationID: 40, Mentions: []string{KindNewAlerts}}},
		{"GetEventOwners", Request{DestinationID: 10, AlertGroupID: 5, Seq: 11, Mentions: []string{Owner}}},
		{"ListMentionUsers", Request{DestinationID: 10, Mentions: []string{KindNewAlerts}}},
	} {
		g := newFake()
		g.settings = f.settings
		g.owners = f.owners
		if tc.fail != "" {
			g.fail[tc.fail] = errors.New("boom")
		}
		if _, err := newService(g).Resolve(ctx, nil, orgID, tc.r); err == nil {
			t.Errorf("%s: no error", tc.fail)
		}
	}
}

// TestNames (C-12.FR-12): a user is shown by the username of their Account link, else by the display name; the
// footer's usernames come by identity space; identity spaces follow the Destination type and Connection.
func TestNames(t *testing.T) {
	if (User{Name: "Alice", Username: "alice"}).Display() != "alice" || (User{Name: "Alice"}).Display() != "Alice" {
		t.Error("display names")
	}
	conn := int64(3)
	if IdentitySpace(TypeTelegram, &conn) != "telegram" || IdentitySpace(TypeMattermost, &conn) != "mattermost:3" ||
		IdentitySpace(TypeMattermost, nil) != "" || IdentitySpace(TypeWebhook, nil) != "" {
		t.Error("identity spaces")
	}
	f := newFake()
	s := newService(f)
	if got, err := s.FooterNames(t.Context(), nil, 5); err != nil || got != nil {
		t.Errorf("no links: %v %v", got, err)
	}
	f.footer = []dbgen.ListFooterUsernamesRow{{IdentitySpace: "mattermost:3", Username: "alice.mm"},
		{IdentitySpace: "telegram", Username: "alice_tg"}}
	got, err := s.FooterNames(t.Context(), nil, 5)
	if err != nil || got["telegram"] != "alice_tg" || got["mattermost:3"] != "alice.mm" {
		t.Errorf("footer names %v %v", got, err)
	}
	f.fail["ListFooterUsernames"] = errors.New("boom")
	if _, err := s.FooterNames(t.Context(), nil, 5); err == nil {
		t.Error("a failing read")
	}
}

// TestResolveExternalID: a User target carries the messenger's user id of the Account link in the Destination's
// identity space, which Telegram mentions a user by (C-12.FR-8); a User without a link has none.
func TestResolveExternalID(t *testing.T) {
	f := newFake()
	s := newService(f)
	set := nobody()
	set[KindNewAlerts] = Setting{Everyone: EveryoneNone, UserIDs: []string{alice, bob}}
	f.settings[20] = settingsJSON(t, set)
	got, err := s.Resolve(t.Context(), nil, orgID, Request{DestinationID: 20, DestinationType: TypeTelegram,
		AlertGroupID: 5, Mentions: []string{KindNewAlerts}})
	if err != nil || len(got) != 2 || got[0].User.ExternalID != "4242" || got[1].User.ExternalID != "" {
		t.Fatalf("targets = %+v, %v", got, err)
	}
}
