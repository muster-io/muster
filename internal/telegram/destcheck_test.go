// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

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
	}{
		{"no comments", "@no_comments", nil, "channel_exists:ok discussion_group:fail", MessageNoComments},
		{"unknown chat", "@nobody", nil, "channel_exists:fail", MessageChannelNotFound},
		{"a group", "-1001000000002", nil, "channel_exists:fail", MessageNotChannel},
		{"bot left the channel", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.ChannelID, faketelegram.BotID, faketelegram.Member{Status: faketelegram.StatusLeft})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:fail", MessageChannelNotAdmin},
		{"may not post", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.ChannelID, faketelegram.BotID, faketelegram.Member{
				Status: faketelegram.StatusAdministrator, CanEditMessages: true})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:fail", MessageMayNotPost},
		{"may not edit", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.ChannelID, faketelegram.BotID, faketelegram.Member{
				Status: faketelegram.StatusAdministrator, CanPostMessages: true})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:fail", MessageMayNotEdit},
		{"channel membership refused", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetFault(fakeserver.Fault{Path: "/bot" + testToken + "/getChatMember", Status: 403, Times: 1,
				Body: `{"ok":false,"error_code":403,"description":"Forbidden: bot is not a member of the channel chat"}`})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:fail", MessageChannelNotAdmin},
		{"a member of the group", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.GroupID, faketelegram.BotID, faketelegram.Member{Status: faketelegram.StatusMember})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:ok bot_rights_group:fail",
			"The bot is not an admin of the discussion group Muster alerts Chat. Make the bot an admin there, allowed " +
				"to post messages."},
		{"outside the group", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetMember(faketelegram.GroupID, faketelegram.BotID, faketelegram.Member{Status: faketelegram.StatusLeft})
		}, "channel_exists:ok discussion_group:ok bot_rights_channel:ok bot_rights_group:fail",
			MessageGroupNotAdmin(faketelegram.GroupTitle)},
		{"a revoked token", "@muster_alerts", func(f *faketelegram.Fake) {
			_ = f.SetConfig(nil, nil, &[]string{testToken})
		}, "channel_exists:fail", MessageTokenInvalid},
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
			if res.OK() || stepNames(res) != tc.steps || last.Message != tc.message ||
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
			res.Steps[len(res.Steps)-1].Message != "Telegram answered 502: Bad Gateway" {
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
