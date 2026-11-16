// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// telegramAlice is the Telegram user id of alice's Telegram account, which was never in the discussion group.
const telegramAlice = 7001

// reachMark is a Destination that a Mention of an On-call cannot reach, as a warning unreachable or "On call now" names
// it.
type reachMark struct {
	User        string
	Destination string
	Messenger   string
	Reason      string
}

func (m reachMark) String() string {
	return m.User + "/" + m.Destination + "/" + m.Messenger + "/" + m.Reason
}

// TestScheduleReach is S-164 against `muster dev`, as its Verification: with payments-notice the Mattermost
// Destination of payments, bob, On-call today without a Mattermost Account link, is warned about it with the reason
// no_account_link for his segment, and so is "On call now"; alice, On-call tomorrow and linked, is not (C-27.AC-6,
// C-27.FR-8, FR-9). With a Bridge Destination whose bridge declares no native_mentions, an outgoing webhook in template
// mode without "reply in thread" and a Telegram Destination of the Team, alice is warned about the first two at once
// and, after "Check" of the Telegram Destination, which the fake answers 400 PARTICIPANT_ID_INVALID about her (F-128),
// about it with not_in_discussion_group; once she is a member and "Check" runs again it is gone, and once she has left
// it is back; an error of her getChatMember leaves her unknown, the check passes and her absence is kept, until she is
// a member again. The development clock moves past
// auth.session_idle_timeout, so the reads use Personal access tokens.
func TestScheduleReach(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := &linksEnv{timersEnv: &timersEnv{tgd: newTGD(t, h, r)}}
	alice := e.person("Alice Smith", "alice", "responder")
	bob := e.person("Bob", "bob", "responder")
	e.mmConn = e.connection(50)
	pn := e.destination("payments-notice", e.mmConn, fakemattermost.ChannelAlerts, nil, 50)

	// alice links her Mattermost account u-alice and her Telegram account, as in S-069 and S-068; bob links nothing.
	rid, code := e.codeFor(alice, "alice", fakemattermost.AliceUserID)
	alice.session.json(http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation", `{"request_id":"`+rid+
		`","code":"`+code+`"}`, http.StatusCreated)
	tok := e.startTelegram(alice, e.conn)
	if got := e.private(devmode.TelegramConnectionBotToken, telegramAlice, "alice_t", "/start "+tok); !strings.HasPrefix(
		got, "Linked to Muster user Alice Smith") {
		t.Fatalf("the reply to /start: %q", got)
	}

	tp := e.api.json(http.MethodPost, "/api/v1/teams", `{"name":"payments","slug":"payments","own_working_hours":false}`,
		http.StatusCreated)["id"].(string)
	for _, p := range []linker{alice, bob} {
		e.api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+p.id+`"}`, http.StatusCreated)
	}
	teamDestinations := func(ids ...string) {
		t.Helper()
		a := e.api.do(http.MethodGet, "/api/v1/teams/"+tp, "")
		entries := make([]map[string]string, len(ids))
		for i, id := range ids {
			entries[i] = map[string]string{"destination_id": id}
		}
		body, _ := json.Marshal(map[string]any{"name": "payments", "slug": "payments", "own_working_hours": false,
			"destination_entries": entries})
		if b := e.api.do(http.MethodPut, "/api/v1/teams/"+tp, string(body), "If-Match",
			a.header.Get("ETag")); b.status != http.StatusOK {
			t.Fatalf("save the team = %d %s", b.status, b.body)
		}
	}
	teamDestinations(pn)

	today := readClock(t, r).Now.UTC().Truncate(24 * time.Hour)
	sc := e.api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Main","time_zone":"UTC",`+
		`"rotation_layers":[{"name":"Days","user_ids":["`+bob.id+`","`+alice.id+`"],"shift_length":"day",`+
		`"handover_time":"00:00","start_date":"`+today.Format(time.DateOnly)+`","limits":null}]}`,
		http.StatusCreated)["id"].(string)

	onCall := func(query string) []reachMark {
		t.Helper()
		var res struct {
			Warnings []struct {
				Kind        string
				User        *struct{ Login string }
				Destination *struct{ Name string }
				Messenger   *string
				Reason      *string
				Start, End  *string
			}
		}
		a := e.api.do(http.MethodGet, "/api/v1/schedules/"+sc+"/on-call"+query, "")
		decode(t, a, &res)
		var out []reachMark
		for _, w := range res.Warnings {
			if w.Kind != "unreachable" {
				continue
			}
			if w.Start == nil || w.End == nil || w.Destination == nil || w.Messenger == nil || w.Reason == nil {
				t.Fatalf("a warning unreachable without its segment or reach: %s", a.body)
			}
			m := reachMark{w.User.Login, w.Destination.Name, *w.Messenger, *w.Reason}
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
		return out
	}
	now := func() []reachMark {
		t.Helper()
		var res struct {
			Items []struct {
				Team        struct{ Slug string }
				User        *struct{ Login string }
				Unreachable []struct {
					Destination struct{ Name string }
					Messenger   string
					Reason      string
				}
			}
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/on-call-now", ""), &res)
		for _, it := range res.Items {
			if it.Team.Slug != "payments" {
				continue
			}
			out := []reachMark{}
			for _, u := range it.Unreachable {
				out = append(out, reachMark{it.User.Login, u.Destination.Name, u.Messenger, u.Reason})
			}
			return out
		}
		t.Fatal("no payments in on call now")
		return nil
	}
	want := func(what string, got []reachMark, want ...string) {
		t.Helper()
		var s []string
		for _, m := range got {
			s = append(s, m.String())
		}
		if !slices.Equal(s, want) {
			t.Errorf("%s = %v, want %v", what, s, want)
		}
	}

	// C-27.AC-6: today bob, without a Mattermost Account link, is On-call; tomorrow alice, linked.
	want("the warnings of two days", onCall("?to="+today.AddDate(0, 0, 2).Format(time.RFC3339)),
		"bob/payments-notice/mattermost/no_account_link")
	want("on call now, bob", now(), "bob/payments-notice/mattermost/no_account_link")
	advance(t, r, 86400)
	want("on call now, alice", now())

	// C-27.FR-8: a bridge without native_mentions, read by a check of its Connection, warns whoever is On-call.
	var bridgeConn string
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections?type=bridge", "", http.StatusOK)["items"].([]any) {
		if c := item.(map[string]any); c["name"] == devmode.BridgeConnectionName {
			bridgeConn = c["id"].(string)
		}
	}
	e.fake(http.MethodPut, h.Fakes.Bridge+"/_fake/config", `{"capabilities":{"native_mentions":false}}`)
	if c := e.api.json(http.MethodPost, "/api/v1/connections/"+bridgeConn+"/checks", "", http.StatusOK); c["ok"] != true {
		t.Fatalf("the check of the bridge: %v", c)
	}
	bridgeDest := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge",
		"name":"payments-bridge","connection_id":%q,"channel_id":"ch-alerts","mentions":%s,
		"limiter":{"limit":30,"per_seconds":60}}`, bridgeConn, mentionSettings(nil)),
		http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	// An outgoing webhook in template mode without "reply in thread".
	tmpl, _ := json.Marshal(map[string]any{
		"create": map[string]any{"method": "POST", "url": webhookBase + "/chat/reach/messages", "headers": []any{},
			"body": `{"text":"{{ .AlertGroup.Title }}"}`, "extract": []map[string]string{{"name": "id",
				"path": "$.data.id"}}},
		"update": map[string]any{"method": "PUT", "url": webhookBase + "/chat/reach/messages/{{ .Response.id }}",
			"headers": []any{}, "body": `{"final":{{ .Final | toJson }}}`}})
	hook := e.templateWebhook("payments-hook", "template", string(tmpl), "", nil)
	// A Telegram Destination, whose bot is an admin of the discussion group; its save checks nobody yet.
	e.member(faketelegram.GroupID, faketelegram.Member{Status: faketelegram.StatusAdministrator, CanPostMessages: true})
	a := e.create("tg-payments", "@muster_alerts", 60)
	if a.status != http.StatusCreated {
		t.Fatalf("telegram destination = %d %s", a.status, a.body)
	}
	var created struct {
		Destination struct{ ID string } `json:"destination"`
	}
	decode(t, a, &created)
	tg := created.Destination.ID
	teamDestinations(pn, bridgeDest, hook, tg)
	want("on call now, alice, before the check", now(), "alice/payments-bridge/bridge/bridge_no_native_mentions",
		"alice/payments-hook/webhook/webhook_no_thread_reply")

	// check runs "Check" of the Telegram Destination and returns the logins its item discussion_group_members names.
	check := func() []string {
		t.Helper()
		var res struct {
			OK     bool `json:"ok"`
			Checks []struct {
				Name        string
				OK          bool                      `json:"ok"`
				AbsentUsers *[]struct{ Login string } `json:"absent_users"`
			}
		}
		a := e.api.do(http.MethodPost, "/api/v1/destinations/"+tg+"/checks", "")
		decode(t, a, &res)
		last := res.Checks[len(res.Checks)-1]
		if a.status != http.StatusOK || !res.OK || last.Name != "discussion_group_members" || !last.OK ||
			last.AbsentUsers == nil {
			t.Fatalf("check = %d %s", a.status, a.body)
		}
		out := []string{}
		for _, u := range *last.AbsentUsers {
			out = append(out, u.Login)
		}
		return out
	}
	member := func(status string) {
		t.Helper()
		e.fake(http.MethodPut, fmt.Sprintf("%s/chats/%d/members/%d", e.ftg, faketelegram.GroupID, telegramAlice),
			`{"status":"`+status+`"}`)
	}
	tgWarned := []string{"alice/payments-bridge/bridge/bridge_no_native_mentions",
		"alice/payments-hook/webhook/webhook_no_thread_reply", "alice/tg-payments/telegram/not_in_discussion_group"}
	if got := check(); !slices.Equal(got, []string{"alice"}) {
		t.Errorf("never in the group: absent %v", got)
	}
	var reqs []fakeserver.Request
	decode(t, call(t, http.MethodGet, e.ftg+"/requests", ""), &reqs)
	if !slices.ContainsFunc(reqs, func(req fakeserver.Request) bool {
		return strings.HasSuffix(req.Path, "/getChatMember") &&
			strings.Contains(req.Body, fmt.Sprintf(`"user_id":%d`, telegramAlice)) &&
			strings.Contains(req.Body, fmt.Sprint(faketelegram.GroupID))
	}) {
		t.Error("no getChatMember of the discussion group about alice")
	}
	want("on call now, alice outside the group", now(), tgWarned...)
	// The period of the next schedule.gap_view also has bob's days, without any Account link.
	want("the warnings, alice outside the group", onCall(""), append(slices.Clone(tgWarned),
		"bob/payments-bridge/bridge/bridge_no_native_mentions", "bob/payments-hook/webhook/webhook_no_thread_reply",
		"bob/payments-notice/mattermost/no_account_link", "bob/tg-payments/telegram/no_account_link")...)
	if n := e.h.count(t, `SELECT count(*) FROM destinations WHERE public_id = '`+tg+`'
		AND cardinality(telegram_absent_user_ids) = 1 AND telegram_members_checked_at IS NOT NULL`); n != 1 {
		t.Errorf("the stored members: %d rows", n)
	}

	member("member")
	if got := check(); len(got) != 0 {
		t.Errorf("a member: absent %v", got)
	}
	want("on call now, alice a member", now(), tgWarned[:2]...)
	member("left")
	if got := check(); !slices.Equal(got, []string{"alice"}) {
		t.Errorf("after a leave: absent %v", got)
	}
	want("on call now, alice left", now(), tgWarned...)

	// Another error of her getChatMember leaves her unknown: the check passes without naming her, and her absence,
	// which it could not confirm, is kept; a member again, she is no longer warned about.
	e.fake(http.MethodPut, fmt.Sprintf("%s/chats/%d/members/%d", e.ftg, faketelegram.GroupID, telegramAlice),
		`{"error_code":400,"description":"Bad Request: user not found"}`)
	if got := check(); len(got) != 0 {
		t.Errorf("an unknown answer: absent %v", got)
	}
	want("on call now, alice unknown", now(), tgWarned...)
	member("member")
	if got := check(); len(got) != 0 {
		t.Errorf("a member again: absent %v", got)
	}
	want("on call now, alice a member again", now(), tgWarned[:2]...)
}
