// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/schedules/dbgen"
)

// ListTeamReach of the fake store has no Destinations; reachStore gives them.
func (s *fakeStore) ListTeamReach(context.Context, dbgen.ListTeamReachParams) ([]dbgen.ListTeamReachRow, error) {
	return nil, s.err("ListTeamReach")
}

// ListAccountLinkSpaces of the fake store has no Account links; reachStore gives them.
func (s *fakeStore) ListAccountLinkSpaces(context.Context, dbgen.ListAccountLinkSpacesParams) (
	[]dbgen.ListAccountLinkSpacesRow, error) {
	return nil, s.err("ListAccountLinkSpaces")
}

// reachStore is the fake store with the Destinations of the Teams and the Account links of the people, as the
// queries read them.
type reachStore struct {
	*fakeStore
	dests []dbgen.ListTeamReachRow
	links []dbgen.ListAccountLinkSpacesRow
	asked []dbgen.ListTeamReachParams
}

func (s *reachStore) ListTeamReach(_ context.Context, arg dbgen.ListTeamReachParams) ([]dbgen.ListTeamReachRow,
	error) {
	if err := s.err("ListTeamReach"); err != nil {
		return nil, err
	}
	s.asked = append(s.asked, arg)
	var out []dbgen.ListTeamReachRow
	for _, d := range s.dests {
		if arg.OrgID == orgID && slices.Contains(arg.TeamIds, d.TeamID) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *reachStore) ListAccountLinkSpaces(_ context.Context, arg dbgen.ListAccountLinkSpacesParams) (
	[]dbgen.ListAccountLinkSpacesRow, error) {
	if err := s.err("ListAccountLinkSpaces"); err != nil {
		return nil, err
	}
	var out []dbgen.ListAccountLinkSpacesRow
	for _, l := range s.links {
		if arg.OrgID == orgID && slices.Contains(arg.UserIds, l.UserID) {
			out = append(out, l)
		}
	}
	return out, nil
}

// The Connections of the tests: a Mattermost, a Slack, a Bridge and a Telegram one.
const (
	connMattermost int64 = 31
	connSlack      int64 = 32
	connBridge     int64 = 33
	connTelegram   int64 = 34
)

func conn(id int64) pgtype.Int8 { return pgtype.Int8{Int64: id, Valid: true} }

// newReachService is the Service over the fake store with the Destinations dests and the Account links links.
func newReachService(t *testing.T, dests []dbgen.ListTeamReachRow, links []dbgen.ListAccountLinkSpacesRow) (
	*Service, *reachStore, *clock.Manual) {
	t.Helper()
	store := &reachStore{fakeStore: newStore(), dests: dests, links: links}
	c := clock.NewManual(t0)
	return New(Config{OrgID: orgID, Store: store, Audit: audit.NewWriter(logging.New(nilWriter{}, logging.LevelInfo),
		c), Business: c}), store, c
}

// TestReachReason is C-27.FR-8 for every Destination type and reason: a Mattermost, Slack, Bridge or Telegram
// Destination without the person's Account link in the Identity space of its Connection is no_account_link, a link in
// another Connection's space included; a Bridge whose info does not declare native_mentions, or was never read, is
// bridge_no_native_mentions, with a link or without, as delivery renders plain names for both; an
// outgoing webhook sending its templates without "reply in thread" is webhook_no_thread_reply, in template mode and
// in both, and one in events mode has no warning; a Telegram Destination whose check saw the linked person outside
// its discussion group is not_in_discussion_group; a messenger Destination without a Connection has none.
func TestReachReason(t *testing.T) {
	mm, sl, br := conn(connMattermost), conn(connSlack), conn(connBridge)
	all := map[string]bool{"mattermost:31": true, "slack:32": true, "bridge:33": true, "telegram": true}
	other := map[string]bool{"mattermost:99": true, "slack:99": true, "bridge:99": true}
	ptrOf := func(c pgtype.Int8) *int64 {
		if !c.Valid {
			return nil
		}
		return &c.Int64
	}
	d := func(typ string, c pgtype.Int8, f func(*teamDestination)) teamDestination {
		out := teamDestination{DestinationRef: DestinationRef{Type: typ}, Connection: ptrOf(c)}
		if f != nil {
			f(&out)
		}
		return out
	}
	native := func(declared bool) func(*teamDestination) {
		return func(d *teamDestination) { d.NativeMentions = declared }
	}
	webhook := func(mode string, reply bool) func(*teamDestination) {
		return func(d *teamDestination) { d.WebhookMode, d.ReplyInThread = mode, reply }
	}
	absent := func(d *teamDestination) { d.Absent = []int64{petr.ID} }
	for _, tt := range []struct {
		name   string
		d      teamDestination
		spaces map[string]bool
		want   string
	}{
		{"mattermost linked", d("mattermost", mm, nil), all, ""},
		{"mattermost without a link", d("mattermost", mm, nil), nil, ReachNoAccountLink},
		{"mattermost linked through another connection", d("mattermost", mm, nil), other, ReachNoAccountLink},
		{"slack linked", d("slack", sl, nil), all, ""},
		{"slack without a link", d("slack", sl, nil), other, ReachNoAccountLink},
		{"bridge with native mentions, linked", d("bridge", br, native(true)), all, ""},
		{"bridge with native mentions, without a link", d("bridge", br, native(true)), other, ReachNoAccountLink},
		{"bridge without native mentions, linked", d("bridge", br, native(false)), all, ReachBridgeNoNativeMentions},
		{"bridge without native mentions, without a link", d("bridge", br, native(false)), nil,
			ReachBridgeNoNativeMentions},
		{"bridge whose info was never read, as delivery renders plain names", d("bridge", br, nil), all,
			ReachBridgeNoNativeMentions},
		{"telegram linked and in the group", d("telegram", conn(connTelegram), nil), all, ""},
		{"telegram without a link", d("telegram", conn(connTelegram), absent), other, ReachNoAccountLink},
		{"telegram linked outside the group", d("telegram", conn(connTelegram), absent), all,
			ReachNotInDiscussionGroup},
		{"webhook in events mode", d("webhook", pgtype.Int8{}, webhook("events", false)), nil, ""},
		{"webhook in template mode with reply in thread", d("webhook", pgtype.Int8{}, webhook("template", true)), nil,
			""},
		{"webhook in template mode without reply in thread", d("webhook", pgtype.Int8{}, webhook("template", false)),
			all, ReachWebhookNoThreadReply},
		{"webhook in both modes without reply in thread", d("webhook", pgtype.Int8{}, webhook("both", false)), all,
			ReachWebhookNoThreadReply},
		{"mattermost whose connection is gone", d("mattermost", pgtype.Int8{}, nil), nil, ""},
		{"telegram without a connection", d("telegram", pgtype.Int8{}, absent), nil, ""},
		{"bridge without a connection", d("bridge", pgtype.Int8{}, nil), nil, ""},
	} {
		if got := reachReason(tt.d, petr.ID, tt.spaces); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

// reachDests are the Destinations of payments (Team 1): payments-notice through the Mattermost Connection,
// payments-hook an outgoing webhook in events mode, and tg-payments a Telegram channel whose check saw petr outside
// its discussion group; ops (Team 2) has none.
func reachDests() []dbgen.ListTeamReachRow {
	return []dbgen.ListTeamReachRow{
		{TeamID: 1, ID: 51, PublicID: "DS00000000HOOK", Name: "payments-hook", Type: "webhook", Health: "healthy",
			WebhookMode: "events"},
		{TeamID: 1, ID: 50, PublicID: "DS000000000PN1", Name: "payments-notice", Type: "mattermost",
			Health: "broken", BrokenSince: pgtype.Timestamptz{Time: t0, Valid: true},
			BrokenReason: pgtype.Text{String: "403", Valid: true}, ConnectionID: conn(connMattermost)},
		{TeamID: 1, ID: 52, PublicID: "DS00000000TGPA", Name: "tg-payments", Type: "telegram", Health: "healthy",
			ConnectionID: conn(connTelegram), TelegramAbsentUserIds: []int64{petr.ID}},
	}
}

// reachInput is a Schedule of payments in UTC with day Shifts of petr and olga from 28 October: petr on the 28th, the
// 30th, …, olga on the 29th, the 31st, ….
func reachInput(team string) Input {
	return Input{TeamID: team, Name: "Main", TimeZone: ptr("UTC"), RotationLayers: []RotationLayerInput{{
		Name: "Days", UserIDs: []string{petr.PublicID, olga.PublicID}, ShiftLength: ShiftDay, HandoverTime: "00:00",
		StartDate: date("2026-10-28")}}}
}

type reachWarning struct {
	login, destination, messenger, reason, layer, override, start, end string
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func unreachableOf(res Result) []reachWarning {
	var out []reachWarning
	for _, w := range res.Warnings {
		if w.Kind != WarningUnreachable {
			continue
		}
		rw := reachWarning{login: w.User.Login, destination: w.Unreachable.Destination.Name,
			messenger: w.Unreachable.Messenger, reason: w.Unreachable.Reason, override: w.Override, start: rfc(w.Start),
			end: rfc(w.End)}
		if w.RotationLayer != nil {
			rw.layer = w.RotationLayer.Name
		}
		out = append(out, rw)
	}
	return out
}

// TestOnCallUnreachable is C-27.FR-8 and C-27.AC-6 on the Result: petr, without a Mattermost Account link and outside
// the discussion group of tg-payments, has for each segment in which he is On-call — the 28th and the 30th — a warning
// unreachable for payments-notice (no_account_link, its health kept) and for tg-payments (no_account_link without his
// Telegram link, then not_in_discussion_group with it); olga, linked in both spaces, has none, nor does the webhook
// in events mode warn. An Override of petr is its own segment, its warnings naming the Override. A Team with no
// Destination has no warning unreachable. The warnings follow the gaps, which they do not change.
func TestOnCallUnreachable(t *testing.T) {
	links := []dbgen.ListAccountLinkSpacesRow{{UserID: olga.ID, IdentitySpace: "mattermost:31"},
		{UserID: olga.ID, IdentitySpace: "telegram"}}
	svc, store, _ := newReachService(t, reachDests(), links)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, reachInput(teamPay))
	if err != nil {
		t.Fatal(err)
	}
	to := utc("2026-10-31T00:00:00Z")
	res, err := svc.OnCall(ctx, sc.PublicID, nil, &to)
	if err != nil {
		t.Fatal(err)
	}
	day := func(s string) string { return s + "T00:00:00Z" }
	want := []reachWarning{
		{"petr", "payments-notice", "mattermost", ReachNoAccountLink, "Days", "", rfc(t0), day("2026-10-29")},
		{"petr", "tg-payments", "telegram", ReachNoAccountLink, "Days", "", rfc(t0), day("2026-10-29")},
		{"petr", "payments-notice", "mattermost", ReachNoAccountLink, "Days", "", day("2026-10-30"), rfc(to)},
		{"petr", "tg-payments", "telegram", ReachNoAccountLink, "Days", "", day("2026-10-30"), rfc(to)},
	}
	if got := unreachableOf(res); !slices.Equal(got, want) {
		t.Errorf("unreachable = %+v\nwant %+v", got, want)
	}
	for _, w := range res.Warnings {
		if w.Kind == WarningUnreachable && w.Unreachable.Destination.Name == "payments-notice" &&
			(w.Unreachable.Destination.Health != "broken" || w.Unreachable.Destination.BrokenReason == nil ||
				*w.Unreachable.Destination.BrokenReason != "403" || w.Unreachable.Destination.PublicID != "DS000000000PN1") {
			t.Errorf("the Destination of a warning = %+v", w.Unreachable.Destination)
		}
	}
	if n := len(store.asked); n != 1 || !slices.Equal(store.asked[0].TeamIds, []int64{1}) {
		t.Errorf("the reach read %+v", store.asked)
	}

	store.links = append(store.links, dbgen.ListAccountLinkSpacesRow{UserID: petr.ID, IdentitySpace: "telegram"})
	o, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: petr.PublicID,
		Start: utc("2026-10-29T06:00:00Z"), End: utc("2026-10-29T08:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	res, err = svc.OnCall(ctx, sc.PublicID, nil, &to)
	if err != nil {
		t.Fatal(err)
	}
	got := unreachableOf(res)
	if len(got) != 6 || got[1].reason != ReachNotInDiscussionGroup || got[2] != (reachWarning{"petr",
		"payments-notice", "mattermost", ReachNoAccountLink, "", o.PublicID, "2026-10-29T06:00:00Z",
		"2026-10-29T08:00:00Z"}) || got[3].reason != ReachNotInDiscussionGroup || got[3].override != o.PublicID {
		t.Errorf("with petr's Telegram link and an Override = %+v", got)
	}
	if i := slices.IndexFunc(res.Warnings, func(w Warning) bool { return w.Kind == WarningUnreachable }); i < 0 ||
		slices.ContainsFunc(res.Warnings[i:], func(w Warning) bool { return w.Kind != WarningUnreachable }) {
		t.Errorf("the warnings unreachable do not come last: %+v", res.Warnings)
	}

	ops, err := svc.Create(ctx, admin, Input{TeamID: teamOps, Name: "Ops", TimeZone: ptr("UTC"),
		RotationLayers: []RotationLayerInput{{Name: "Days", UserIDs: []string{bob.PublicID}, ShiftLength: ShiftDay,
			HandoverTime: "00:00", StartDate: date("2026-10-28")}}})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := svc.OnCall(ctx, ops.PublicID, nil, nil); err != nil || len(unreachableOf(res)) != 0 {
		t.Errorf("a Team without Destinations = %+v, %v", unreachableOf(res), err)
	}

	for _, name := range []string{"ListTeamReach", "ListAccountLinkSpaces"} {
		store.fail[name] = errBoom
		if _, err := svc.OnCall(ctx, sc.PublicID, nil, nil); !errors.Is(err, errBoom) ||
			!strings.Contains(err.Error(), "read the") {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
}

// TestUnreachableWithoutPeople: a Result with only gaps reads no reach, and neither does a Team without a Schedule.
func TestUnreachableWithoutPeople(t *testing.T) {
	svc, store, _ := newReachService(t, reachDests(), nil)
	r, err := svc.readReach(t.Context(), store, map[int64]bool{1: true}, map[int64]bool{})
	if err != nil || len(r.teams) != 0 || len(store.asked) != 0 {
		t.Errorf("reach of nobody = %+v, %v, asked %d", r, err, len(store.asked))
	}
	if got := unreachableWarnings(Result{Segments: []Segment{{Start: t0, End: t0.Add(time.Hour)}}}, 1, r); len(got) != 0 {
		t.Errorf("warnings of a gap = %+v", got)
	}
}
