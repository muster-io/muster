// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mentions

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
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
	// links are the usernames by identity space, externals the messenger's user ids and displays the display names
	// of the links.
	links     map[string]string
	externals map[string]string
	displays  map[string]string
}

// fakeQueries is the database of the package in memory.
type fakeQueries struct {
	users    []user
	settings map[int64][]byte
	owners   map[int64]dbgen.GetEventOwnersRow
	footer   []dbgen.ListFooterUsersRow
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
			Username: u.links[arg.IdentitySpace], ExternalID: u.externals[arg.IdentitySpace],
			DisplayName: u.displays[arg.IdentitySpace]})
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

// TestValidateBridge covers C-22.FR-3 and AC-20: a Bridge Destination takes nobody and chosen Users for every kind,
// and refuses everyone in the chat and messenger groups with unsupported at their pointer, in a text that names
// Bridges; Telegram's texts are its own.
func TestValidateBridge(t *testing.T) {
	ctx := t.Context()
	s := newService(newFake())
	if err := s.Validate(ctx, TypeBridge, nobody()); err != nil {
		t.Errorf("nobody: %v", err)
	}
	for _, kind := range Kinds {
		users := with(kind, Setting{Everyone: EveryoneNone, UserIDs: []string{alice, bob}, Groups: []string{}})
		if err := s.Validate(ctx, TypeBridge, users); err != nil {
			t.Errorf("%s with chosen users: %v", kind, err)
		}
		for _, word := range []string{EveryoneChannel, EveryoneAll, EveryoneHere} {
			err := s.Validate(ctx, TypeBridge, with(kind, Setting{Everyone: word}))
			fe, ok := errors.AsType[*FieldError](err)
			if !ok || fe.Pointer != "/mentions/"+kind+"/everyone" || fe.Code != CodeUnsupported ||
				fe.Detail != "A Bridge has no Mention of everyone in the chat." {
				t.Errorf("%s everyone %s: %v", kind, word, err)
			}
		}
		err := s.Validate(ctx, TypeBridge, with(kind, Setting{Everyone: EveryoneNone, Groups: []string{"ops"}}))
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != "/mentions/"+kind+"/groups" || fe.Code != CodeUnsupported ||
			fe.Detail != "A Bridge has no messenger groups." {
			t.Errorf("%s groups: %v", kind, err)
		}
		if err := s.Validate(ctx, TypeBridge, with(kind, Setting{Everyone: EveryoneNone,
			UserIDs: []string{gone}})); err == nil {
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
		if fe, ok := errors.AsType[*FieldError](s.Validate(ctx, TypeTelegram, tc.set)); !ok || fe.Detail != tc.detail {
			t.Errorf("telegram: %v, want %q", fe, tc.detail)
		}
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
	br := Request{DestinationID: 20, DestinationType: TypeBridge, ConnectionID: &conn, AlertGroupID: 5}
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
		{br, 0, []string{KindNewAlertGroup}, ""},
		{br, 0, []string{KindNewAlerts}, "user:Alice Smith,user:Bob Jones"},
		{br, 0, []string{KindReopen}, "user:Alice Smith"},
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

// TestResolveBridge covers C-22.FR-16: a Mention through a Bridge Destination names a User with an Account link for its
// Connection by the link's messenger user id and display name, a User linked through another Connection only — or not
// at all — by the Muster display name, without a user id.
func TestResolveBridge(t *testing.T) {
	f := newFake()
	f.users[0].links["bridge:5"] = "alice@example.org"
	f.users[0].externals["bridge:5"] = "u-alice"
	f.users[0].displays = map[string]string{"bridge:5": "Alice S."}
	f.users[1].links = map[string]string{"bridge:6": "bob"}
	f.users[1].externals = map[string]string{"bridge:6": "u-bob"}
	f.users[1].displays = map[string]string{"bridge:6": "Bob J."}
	s := newService(f)
	set := nobody()
	set[KindNewAlertGroup] = Setting{Everyone: EveryoneNone, UserIDs: []string{alice, bob}}
	f.settings[50] = settingsJSON(t, set)
	conn := int64(5)
	got, err := s.Resolve(t.Context(), nil, orgID, Request{DestinationID: 50, DestinationType: TypeBridge,
		ConnectionID: &conn, AlertGroupID: 5, Mentions: []string{KindNewAlertGroup}})
	if err != nil || len(got) != 2 {
		t.Fatalf("targets = %+v, %v", got, err)
	}
	if a := got[0].User; a.ExternalID != "u-alice" || a.DisplayName != "Alice S." || a.Name != "Alice Smith" ||
		a.Username != "alice@example.org" {
		t.Errorf("a linked user %+v", a)
	}
	if b := got[1].User; b.ExternalID != "" || b.DisplayName != "" || b.Username != "" || b.Name != "Bob Jones" {
		t.Errorf("a user linked through another connection %+v", b)
	}
}

// TestValidateSlack covers C-24.FR-6, AC-15 and C-12.FR-8, a case per kind: a Slack Destination takes nobody, channel,
// here and chosen Users, and refuses all and messenger groups with unsupported at their pointer, in texts that name
// Slack.
func TestValidateSlack(t *testing.T) {
	ctx := t.Context()
	s := newService(newFake())
	if err := s.Validate(ctx, TypeSlack, nobody()); err != nil {
		t.Errorf("nobody: %v", err)
	}
	for _, kind := range Kinds {
		for _, word := range []string{EveryoneChannel, EveryoneHere} {
			if err := s.Validate(ctx, TypeSlack, with(kind, Setting{Everyone: word, UserIDs: []string{alice},
				Groups: []string{}})); err != nil {
				t.Errorf("%s %s: %v", kind, word, err)
			}
		}
		users := with(kind, Setting{Everyone: EveryoneNone, UserIDs: []string{alice, bob}, Groups: []string{}})
		if err := s.Validate(ctx, TypeSlack, users); err != nil {
			t.Errorf("%s with chosen users: %v", kind, err)
		}
		err := s.Validate(ctx, TypeSlack, with(kind, Setting{Everyone: EveryoneAll}))
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != "/mentions/"+kind+"/everyone" || fe.Code != CodeUnsupported ||
			fe.Detail != "Slack has no Mention of all; choose channel or here." {
			t.Errorf("%s all: %v", kind, err)
		}
		err = s.Validate(ctx, TypeSlack, with(kind, Setting{Everyone: EveryoneHere, Groups: []string{"oncall"}}))
		fe, ok = errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != "/mentions/"+kind+"/groups" || fe.Code != CodeUnsupported ||
			fe.Detail != "Slack has no messenger groups that Muster can name." {
			t.Errorf("%s groups: %v", kind, err)
		}
		if err := s.Validate(ctx, TypeSlack, with(kind, Setting{Everyone: EveryoneNone,
			UserIDs: []string{gone}})); err == nil {
			t.Errorf("%s with a deleted user", kind)
		}
	}
	for _, typ := range []string{TypeMattermost, TypeWebhook} {
		if err := s.Validate(ctx, typ, with(KindNewAlerts, Setting{Everyone: EveryoneAll,
			Groups: []string{"oncall"}})); err != nil {
			t.Errorf("%s takes all and groups: %v", typ, err)
		}
	}
}

// TestResolveSlack covers C-24.FR-6: a Slack Destination's Mentions resolve into channel and here and its Users, and
// leave out all and groups that settings stored before the rule would carry; its identity space is the Slack links of
// its Connection, in which a User with a link has its user id, which the adapter mentions as <@id>, and a User without
// one, or linked through another Slack Connection only, has no username and is named by their display name.
func TestResolveSlack(t *testing.T) {
	f := newFake()
	f.users[1].links = map[string]string{"slack:4": "bob"}
	f.users[1].externals = map[string]string{"slack:4": "U0FAKEBOB"}
	s := newService(f)
	set := nobody()
	set[KindNewAlertGroup] = Setting{Everyone: EveryoneHere, Groups: []string{"oncall"}, UserIDs: []string{alice}}
	set[KindNewAlerts] = Setting{Everyone: EveryoneAll, Groups: []string{"ops"}}
	set[KindReopen] = Setting{Everyone: EveryoneChannel}
	f.settings[60] = settingsJSON(t, set)
	conn := int64(3)
	r := Request{DestinationID: 60, DestinationType: TypeSlack, ConnectionID: &conn, AlertGroupID: 5,
		Mentions: []string{KindNewAlertGroup, KindNewAlerts, KindReopen}}
	got, err := s.Resolve(t.Context(), nil, orgID, r)
	if err != nil || describe(got) != "everyone:here,user:Alice Smith,everyone:channel" {
		t.Errorf("slack = %s, %v", describe(got), err)
	}
	if u := got[1].User; u == nil || u.Username != "" || u.Display() != "Alice Smith" {
		t.Errorf("a User without a Slack link = %+v", u)
	}
	if IdentitySpace(TypeSlack, &conn) != "slack:3" || IdentitySpace(TypeSlack, nil) != "" {
		t.Errorf("identity space %q", IdentitySpace(TypeSlack, &conn))
	}
	f.users[0].links["slack:3"] = "alice"
	f.users[0].externals["slack:3"] = "U0FAKEALICE"
	f.users[0].displays = map[string]string{"slack:3": "Alice S."}
	set[KindNewAlertGroup] = Setting{Everyone: EveryoneNone, UserIDs: []string{alice, bob}}
	f.settings[60] = settingsJSON(t, set)
	r.Mentions = []string{KindNewAlertGroup}
	got, err = s.Resolve(t.Context(), nil, orgID, r)
	if err != nil || len(got) != 2 {
		t.Fatalf("linked = %s, %v", describe(got), err)
	}
	if a := got[0].User; a.ExternalID != "U0FAKEALICE" || a.Username != "alice" || a.DisplayName != "Alice S." {
		t.Errorf("a User with a Slack link %+v", a)
	}
	if b := got[1].User; b.ExternalID != "" || b.Username != "" || b.Display() != "Bob Jones" {
		t.Errorf("a User linked through another Slack Connection %+v", b)
	}
}
