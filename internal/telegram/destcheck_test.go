// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/outbound"
)

var probe = Direct(delivery.Call{Class: outbound.ClassDelivery})

func stepNames(c DestinationCheck) string {
	var out []string
	for _, s := range c.Steps {
		mark := "ok"
		if !s.OK {
			mark = "fail"
		}
		out = append(out, s.Name+":"+mark)
	}
	return strings.Join(out, " ")
}

func setMember(t *testing.T, f *faketelegram.Fake, chat int64, m faketelegram.Member) {
	t.Helper()
	if err := f.SetMember(chat, faketelegram.BotID, m); err != nil {
		t.Fatal(err)
	}
}

func fault(t *testing.T, f *faketelegram.Fake, method string, status int, body string, times int) {
	t.Helper()
	if err := f.SetFault(fakeserver.Fault{Path: "/bot" + testToken + "/" + method, Status: status, Body: body,
		Times: times}); err != nil {
		t.Fatal(err)
	}
}

// TestDestinationCheckPasses is C-14.AC-13: the channel alone finds its discussion group through getChat; the bot is
// an admin of both. The ids and titles of both chats come back; getMe runs once per client.
func TestDestinationCheckPasses(t *testing.T) {
	f := startFake(t)
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	res, err := CheckDestination(t.Context(), c, probe, "@muster_alerts")
	if err != nil || !res.OK() || stepNames(res) !=
		"channel_exists:ok discussion_group:ok bot_rights_channel:ok bot_rights_group:ok" {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if res.ChannelID != faketelegram.ChannelID || res.ChannelTitle != faketelegram.ChannelTitle ||
		res.GroupID != faketelegram.GroupID || res.GroupTitle != faketelegram.GroupTitle {
		t.Fatalf("found = %+v", res)
	}
	want := "getMe getChat getChatMember getChat getChatMember"
	var got []string
	for _, p := range paths(f) {
		got = append(got, p[strings.LastIndex(p, "/")+1:])
	}
	if strings.Join(got, " ") != want {
		t.Fatalf("requests = %v", got)
	}
	f.ResetRequests()
	setMember(t, f, faketelegram.ChannelID, faketelegram.Member{Status: faketelegram.StatusCreator})
	if res, err := CheckDestination(t.Context(), c, probe, "-1001000000001"); err != nil || !res.OK() ||
		len(f.Requests()) != 4 {
		t.Fatalf("creator, the bot's id kept = %+v %v %d", res, err, len(f.Requests()))
	}
	if (DestinationCheck{}).OK() {
		t.Fatal("an empty check passed")
	}
}

// TestDestinationCheckFailures: each step names what a person must fix, as a Fatal outcome with that message; the
// texts of the discussion group are those of reference.md.
func TestDestinationCheckFailures(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		setup   func(f *faketelegram.Fake)
		steps   string
		message string
		code    string
	}{
		{"no comments", "@no_comments", nil, "channel_exists:ok discussion_group:fail", MessageNoComments,
			CodeNoComments},
		{"unknown chat", "@nobody", nil, "channel_exists:fail", MessageChannelNotFound, CodeChannelNotFound},
		{"a group", "-1001000000002", nil, "channel_exists:fail", MessageNotChannel, CodeNotChannel},
		{"bot left the channel", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.ChannelID, faketelegram.BotID, faketelegram.Member{Status: faketelegram.StatusLeft})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:fail", MessageChannelNotAdmin, CodeChannelNotAdmin},
		{"may not post", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.ChannelID, faketelegram.BotID, faketelegram.Member{
				Status: faketelegram.StatusAdministrator, CanEditMessages: true})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:fail", MessageMayNotPost, CodeMayNotPost},
		{"may not edit", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.ChannelID, faketelegram.BotID, faketelegram.Member{
				Status: faketelegram.StatusAdministrator, CanPostMessages: true})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:fail", MessageMayNotEdit, CodeMayNotEdit},
		{"channel membership refused", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetFault(fakeserver.Fault{Path: "/bot" + testToken + "/getChatMember", Status: 403, Times: 1,
				Body: `{"ok":false,"error_code":403,"description":"Forbidden: bot is not a member of the channel chat"}`})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:fail", MessageChannelNotAdmin, CodeChannelNotAdmin},
		{"a member of the group", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.GroupID, faketelegram.BotID, faketelegram.Member{Status: faketelegram.StatusMember})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:ok bot_rights_group:fail",
			"The bot is not an admin of the discussion group Muster alerts Chat. Make the bot an admin there, allowed " +
				"to post messages.", CodeGroupNotAdmin},
		{"outside the group", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.GroupID, faketelegram.BotID, faketelegram.Member{Status: faketelegram.StatusLeft})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:ok bot_rights_group:fail",
			MessageGroupNotAdmin(faketelegram.GroupTitle), CodeGroupNotAdmin},
		{"a revoked token", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetConfig(nil, nil, &[]string{testToken})
		}, "channel_exists:fail", MessageTokenInvalid, CodeTokenInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := startFake(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
			res, err := CheckDestination(t.Context(), c, probe, tc.channel)
			if err != nil {
				t.Fatal(err)
			}
			last := res.Steps[len(res.Steps)-1]
			if res.OK() || stepNames(res) != tc.steps || last.Message != tc.message || last.Code != tc.code ||
				res.Outcome.Kind != delivery.OutcomeFatal || string(res.Outcome.Error) != tc.message {
				t.Fatalf("check = %s %+v", stepNames(res), res)
			}
		})
	}
}

// TestDestinationCheckGroupByID: a discussion group the bot cannot read is named by its id.
func TestDestinationCheckGroupByID(t *testing.T) {
	f := startFake(t)
	setMember(t, f, faketelegram.GroupID, faketelegram.Member{Status: faketelegram.StatusMember})
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	calls := 0
	run := func(ctx context.Context, fn func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		calls++
		if calls == 4 { // the getChat of the group, after getMe, getChat and getChatMember of the channel
			fault(t, f, "getChat", 403,
				`{"ok":false,"error_code":403,"description":"Forbidden: bot is not a member of the supergroup chat"}`, 1)
		}
		return fn(ctx, delivery.Call{Class: outbound.ClassDelivery}), nil
	}
	res, err := CheckDestination(t.Context(), c, run, "@muster_alerts")
	if err != nil || res.OK() || res.Steps[len(res.Steps)-1].Message != MessageGroupNotAdmin("-1001000000002") {
		t.Fatalf("check = %+v %v", res, err)
	}
}

// TestDestinationCheckTransient: an answer that says nothing of the chat keeps its own outcome, so that the Broken
// probe does not take a timeout for a missing right.
func TestDestinationCheckTransient(t *testing.T) {
	for i, method := range []string{"getMe", "getChat", "getChatMember"} {
		f := startFake(t)
		fault(t, f, method, 502, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`, 0)
		c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
		res, err := CheckDestination(t.Context(), c, Direct(delivery.Call{Class: outbound.ClassInteractive}),
			"@muster_alerts")
		want := []string{"channel_exists", "channel_exists", "bot_rights_channel"}[i]
		if err != nil || res.OK() || res.Outcome.Kind != delivery.OutcomeTransient ||
			res.Steps[len(res.Steps)-1].Name != want ||
			res.Steps[len(res.Steps)-1].Message != "Telegram answered 502: Bad Gateway" ||
			res.Steps[len(res.Steps)-1].Code != delivery.CodeRequestFailed {
			t.Fatalf("%s: %+v %v", method, res, err)
		}
	}
	f := startFake(t)
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	calls := 0
	run := func(ctx context.Context, fn func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		calls++
		if calls == 5 { // the getChatMember of the group
			fault(t, f, "getChatMember", 502, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`, 1)
		}
		return fn(ctx, delivery.Call{Class: outbound.ClassInteractive}), nil
	}
	res, err := CheckDestination(t.Context(), c, run, "@muster_alerts")
	if err != nil || res.Outcome.Kind != delivery.OutcomeTransient || stepNames(res) !=
		"channel_exists:ok discussion_group:ok bot_rights_channel:ok bot_rights_group:fail" {
		t.Fatalf("group: %+v %v", res, err)
	}
}

// TestDestinationCheckRunnerErrors: an error of the runner — no limiter token — ends the check at any request; on
// the interactive path the check takes the tokens of its subject.
func TestDestinationCheckRunnerErrors(t *testing.T) {
	f := startFake(t)
	for limit := range 5 {
		c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
		if _, err := CheckDestination(t.Context(), c, limited(limit), "@muster_alerts"); !errors.Is(err, errLimited) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	id := int64(7)
	clocks := clock.Clocks{Business: clock.Real{}, Real: clock.Real{}}
	res, err := CheckDestination(t.Context(), c, Interactive(deliverytest.Unlimited(1, clocks),
		delivery.Subject{Connection: &id}), "@muster_alerts")
	if err != nil || !res.OK() {
		t.Fatalf("check = %+v, %v", res, err)
	}
}

// TestDestinationCheckGroupPeople is the step discussion_group_members of C-27.FR-8 after the steps of C-14.FR-14: one
// getChatMember of the discussion group per person, in order; left, kicked, restricted without is_member and 400
// PARTICIPANT_ID_INVALID (F-128) are outside the group, by Muster User id; member, an anonymous creator and an
// anonymous administrator, and restricted with is_member are in it; another failure — a 502, or another 400 — leaves
// the person unknown, which the step names, and the step passes.
func TestDestinationCheckGroupPeople(t *testing.T) {
	f := startFake(t)
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	yes, no := true, false
	people := []struct {
		tg     int64
		member *faketelegram.Member
	}{
		{101, &faketelegram.Member{Status: faketelegram.StatusMember}},
		{102, &faketelegram.Member{Status: faketelegram.StatusLeft}},
		{103, &faketelegram.Member{Status: faketelegram.StatusKicked}},
		{104, nil}, // never in the group: 400 PARTICIPANT_ID_INVALID
		{105, &faketelegram.Member{Status: faketelegram.StatusCreator, IsAnonymous: true}},
		{106, &faketelegram.Member{Status: faketelegram.StatusAdministrator, IsAnonymous: true}},
		{107, &faketelegram.Member{Status: faketelegram.StatusRestricted, IsMember: &yes}},
		{108, &faketelegram.Member{Status: faketelegram.StatusRestricted, IsMember: &no}},
		{109, &faketelegram.Member{ErrorCode: 400, Description: "Bad Request: user not found"}},
		{110, &faketelegram.Member{Status: faketelegram.StatusLeft}}, // answered 502 by a fault
	}
	var group []GroupPerson
	for i, p := range people {
		if p.member != nil {
			if err := f.SetMember(faketelegram.GroupID, p.tg, *p.member); err != nil {
				t.Fatal(err)
			}
		}
		group = append(group, GroupPerson{UserID: int64(i + 1), TelegramID: p.tg})
	}
	res, err := CheckDestination(t.Context(), c, probe, "@muster_alerts")
	if err != nil {
		t.Fatal(err)
	}
	f.ResetRequests()
	calls := 0
	run := func(ctx context.Context, fn func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		calls++
		if calls == len(people) {
			fault(t, f, "getChatMember", 502, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`, 1)
		}
		return fn(ctx, delivery.Call{Class: outbound.ClassInteractive}), nil
	}
	res = CheckGroupPeople(t.Context(), c, run, res, group)
	absent, unknown, ok := res.GroupPeople()
	if !res.OK() || !ok || stepNames(res) != "channel_exists:ok discussion_group:ok bot_rights_channel:ok "+
		"bot_rights_group:ok discussion_group_members:ok" || !slices.Equal(absent, []int64{2, 3, 4, 8}) ||
		!slices.Equal(unknown, []int64{9, 10}) {
		t.Fatalf("people = %s, absent %v, unknown %v", stepNames(res), absent, unknown)
	}
	reqs := f.Requests()
	if len(reqs) != len(people) {
		t.Fatalf("%d requests, want one per person", len(reqs))
	}
	for i, r := range reqs {
		if !strings.HasSuffix(r.Path, "/getChatMember") || !strings.Contains(r.Body,
			fmt.Sprintf(`"user_id":%d`, people[i].tg)) || !strings.Contains(r.Body, `-1001000000002`) {
			t.Errorf("request %d = %s %s", i, r.Path, r.Body)
		}
	}
}

// TestDestinationCheckGroupPeopleEdges: a check that failed runs no step; no people make a step with nobody absent and
// no request; a runner that refuses a request — no limiter token — and a deadline that passes leave the people not
// asked yet unknown, and the step passes.
func TestDestinationCheckGroupPeopleEdges(t *testing.T) {
	f := startFake(t)
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	failed := DestinationCheck{Steps: []DestinationStep{{Name: StepChannelExists}}}
	if got := CheckGroupPeople(t.Context(), c, probe, failed, []GroupPerson{{UserID: 1, TelegramID: 101}}); len(
		got.Steps) != 1 {
		t.Errorf("a failed check = %s", stepNames(got))
	}
	if _, _, ok := failed.GroupPeople(); ok {
		t.Error("a failed check has the step")
	}
	res, err := CheckDestination(t.Context(), c, probe, "@muster_alerts")
	if err != nil {
		t.Fatal(err)
	}
	f.ResetRequests()
	if absent, unknown, ok := CheckGroupPeople(t.Context(), c, probe, res, nil).GroupPeople(); !ok ||
		len(absent) != 0 || absent == nil || unknown == nil || len(unknown) != 0 || len(f.Requests()) != 0 {
		t.Errorf("nobody = %v %v %v, %d requests", absent, unknown, ok, len(f.Requests()))
	}
	people := []GroupPerson{{UserID: 1, TelegramID: 201}, {UserID: 2, TelegramID: 202}, {UserID: 3, TelegramID: 203}}
	if absent, unknown, ok := CheckGroupPeople(t.Context(), c, limited(1), res, people).GroupPeople(); !ok ||
		!slices.Equal(absent, []int64{1}) || !slices.Equal(unknown, []int64{2, 3}) {
		t.Errorf("a refused request = %v %v %v", absent, unknown, ok)
	}
	// The second request outlasts the deadline: its person and the third are unknown, and the third is not asked.
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	f.ResetRequests()
	calls := 0
	slow := func(ctx context.Context, fn func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		if calls++; calls == 2 {
			if err := f.SetFault(fakeserver.Fault{Path: "/bot" + testToken + "/getChatMember", DelayMs: 1000,
				Status: 200, Body: `{"ok":true,"result":{"status":"member"}}`, Times: 1}); err != nil {
				t.Fatal(err)
			}
		}
		return fn(ctx, delivery.Call{Class: outbound.ClassInteractive}), nil
	}
	if absent, unknown, ok := CheckGroupPeople(ctx, c, slow, res, people).GroupPeople(); !ok ||
		!slices.Equal(absent, []int64{1}) || !slices.Equal(unknown, []int64{2, 3}) || len(f.Requests()) != 2 {
		t.Errorf("past the deadline = %v %v %v, %d requests", absent, unknown, ok, len(f.Requests()))
	}
}
