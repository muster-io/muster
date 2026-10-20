// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

const (
	adapterConnection = int64(3)
	teamURLB          = "https://fake-workspace-b.slack.com/"
)

// The Destinations of the adapter tests: on the channel alerts with the workspace's address known, the same before
// the Connection's first check, and on the channel other, which the bot is not a member of.
const (
	destAlerts int64 = iota + 1
	destUnchecked
	destOther
)

// slackTargets are the Targets of the adapter tests by Destination id; err answers every lookup when set.
type slackTargets struct {
	byID map[int64]Target
	err  error
}

func (ts *slackTargets) SlackTarget(_ context.Context, id int64) (Target, error) {
	if ts.err != nil {
		return Target{}, ts.err
	}
	t, ok := ts.byID[id]
	if !ok {
		return Target{}, ErrNoTarget
	}
	return t, nil
}

// adapterEnv is the adapter over the fake server's app b, whose requests go to rec, and the renderer of its messages.
type adapterEnv struct {
	fake    *fakeslack.Fake
	adapter *Adapter
	targets *slackTargets
	rec     *records
	render  layoutEnv
}

func newAdapterEnv(t *testing.T) adapterEnv {
	t.Helper()
	f := startFake(t)
	rec := &records{}
	n := network(t, nil)
	n.Recorder = rec
	c := newClient(t, n, Settings{BotToken: fakeslack.SecondBotToken, AppToken: fakeslack.SecondAppToken,
		SigningSecret: fakeslack.SecondSigningSecret, Route: devRoute(f),
		Owner: outbound.Owner{Connection: adapterConnection, PublicID: "CN0000000000S1"}})
	target := func(channel, teamURL string) Target {
		return Target{Client: c, ConnectionID: adapterConnection, ConnectionPublicID: "CN0000000000S1",
			ChannelID: channel, TeamURL: teamURL, BotName: "muster-dev-bot-b"}
	}
	ts := &slackTargets{byID: map[int64]Target{destAlerts: target(fakeslack.ChannelAlerts, teamURLB),
		destUnchecked: target(fakeslack.ChannelAlerts, ""), destOther: target(fakeslack.ChannelOther, teamURLB)}}
	return adapterEnv{fake: f, adapter: &Adapter{Targets: ts, Version: "v1.2.3"}, targets: ts, rec: rec,
		render: newLayoutEnv(t)}
}

func slackCall(dest int64) delivery.Call {
	return delivery.Call{Class: outbound.ClassDelivery, Destination: delivery.Destination{ID: dest,
		PublicID: "DS0000000000D1", Type: delivery.TypeSlack}}
}

func loudCall(dest int64, ts ...mentions.Target) delivery.Call {
	c := slackCall(dest)
	c.Loudness, c.Targets = groups.Loud, ts
	return c
}

// root is the Root message of Alert Group #12 in a status, its buttons signed.
func (e adapterEnv) root(t *testing.T, status string) messages.Message {
	t.Helper()
	return e.render.root(t, layoutSource(status))
}

// messages are the messages of the fake's channel alerts of app b.
func (e adapterEnv) messages() []fakeslack.Message {
	return e.fake.Messages(fakeslack.AppB, fakeslack.ChannelAlerts)
}

// bodies are the JSON bodies of the requests the fake received for method, in order.
func (e adapterEnv) bodies(t *testing.T, method string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range e.fake.Requests() {
		if r.Path != "/api/"+method {
			continue
		}
		var b map[string]any
		if err := json.Unmarshal([]byte(r.Body), &b); err != nil {
			t.Fatalf("%s body %q: %v", method, r.Body, err)
		}
		out = append(out, b)
	}
	return out
}

// paths are the paths of the requests the fake received, in order.
func (e adapterEnv) paths() []string {
	var out []string
	for _, r := range e.fake.Requests() {
		out = append(out, strings.TrimPrefix(r.Path, "/api/"))
	}
	return out
}

// fault scripts one answer of the fake to method.
func (e adapterEnv) fault(t *testing.T, method string, status, retryAfter int, body string) {
	t.Helper()
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/" + method, Status: status,
		RetryAfterSeconds: retryAfter, Body: body, ContentType: "application/json", Times: 1}); err != nil {
		t.Fatal(err)
	}
}

// channel sets the channel alerts of app b in the fake.
func (e adapterEnv) channel(t *testing.T, member, archived bool) {
	t.Helper()
	cfg := e.fake.Config()
	cfg.Channels[fakeslack.AppB][fakeslack.ChannelAlerts] = fakeslack.Channel{Name: "alerts", Member: member,
		Archived: archived}
	if err := e.fake.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

// publish posts the firing Root message to the channel alerts and returns its `ts`.
func (e adapterEnv) publish(t *testing.T) string {
	t.Helper()
	o := e.adapter.Publish(t.Context(), slackCall(destAlerts), e.root(t, messages.ColourFiring))
	if o.Kind != delivery.OutcomeOK || o.MessageID == "" {
		t.Fatalf("publish = %+v", o)
	}
	return o.MessageID
}

// outcomeIs reports whether o is of kind, with an error that contains text.
func outcomeIs(o delivery.Outcome, kind delivery.OutcomeKind, text string) bool {
	return o.Kind == kind && strings.Contains(string(o.Error), text)
}

// TestPublishUpdateReply is C-24.FR-4, FR-6 and AC-5 against the fake server: a Publication is one chat.postMessage
// that names its blocks and attachments and turns unfurling off, whose `ts` is the message id and whose link is made
// from the workspace's address, none before the Connection's check; an edit is chat.update of that `ts` with the whole
// message, Quiet even on a Loud call; a Thread reply is chat.postMessage under the root's `ts`, never broadcast.
func TestPublishUpdateReply(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := t.Context()
	pub := e.adapter.Publish(ctx, slackCall(destAlerts), e.root(t, messages.ColourFiring))
	if pub.Kind != delivery.OutcomeOK || pub.MessageID != "1.000001" || pub.Status != http.StatusOK ||
		pub.MessageURL != "https://fake-workspace-b.slack.com/archives/C0FAKEALERTS/p1000001" {
		t.Fatalf("publish = %+v", pub)
	}
	posts := e.bodies(t, "chat.postMessage")
	if len(posts) != 1 || posts[0]["unfurl_links"] != false || posts[0]["unfurl_media"] != false ||
		posts[0]["channel"] != fakeslack.ChannelAlerts || posts[0]["thread_ts"] != nil {
		t.Fatalf("post %v", posts)
	}
	ms := e.messages()
	var atts []map[string]any
	if len(ms) != 1 || json.Unmarshal(ms[0].Attachments, &atts) != nil || len(atts) != 1 ||
		atts[0]["color"] != messages.StatusColour(messages.ColourFiring) || !strings.Contains(ms[0].Text, "#12") {
		t.Fatalf("messages %+v", ms)
	}

	up := e.adapter.Update(ctx, loudCall(destAlerts, hereTarget), pub.MessageID, e.root(t, messages.ColourAcknowledged))
	if up.Kind != delivery.OutcomeOK || up.MessageID != pub.MessageID || up.MessageURL != pub.MessageURL {
		t.Fatalf("update = %+v", up)
	}
	edits := e.bodies(t, "chat.update")
	_, blocks := edits[0]["blocks"]
	_, attachments := edits[0]["attachments"]
	if len(edits) != 1 || edits[0]["ts"] != pub.MessageID || !blocks || !attachments ||
		strings.Contains(edits[0]["text"].(string), "<!") {
		t.Fatalf("edit %v", edits)
	}
	if ms := e.messages(); ms[0].Edited == nil || json.Unmarshal(ms[0].Attachments, &atts) != nil ||
		atts[0]["color"] != messages.StatusColour(messages.ColourAcknowledged) {
		t.Fatalf("after the edit %+v", ms)
	}

	rep := e.adapter.Reply(ctx, slackCall(destAlerts), delivery.Root{MessageID: pub.MessageID}, newAlertsReply())
	if rep.Kind != delivery.OutcomeOK || rep.MessageID == "" || rep.MessageURL != "" {
		t.Fatalf("reply = %+v", rep)
	}
	posts = e.bodies(t, "chat.postMessage")
	_, broadcast := posts[1]["reply_broadcast"]
	if len(posts) != 2 || posts[1]["thread_ts"] != pub.MessageID || broadcast {
		t.Fatalf("reply %v", posts[1])
	}
	if ms := e.messages(); len(ms) != 1 || len(ms[0].Replies) != 1 || ms[0].Replies[0].TS != rep.MessageID {
		t.Fatalf("thread %+v", ms)
	}

	// Before the Connection's first check, the workspace's address is unknown: no link.
	if o := e.adapter.Publish(ctx, slackCall(destUnchecked), e.root(t, messages.ColourFiring)); o.Kind !=
		delivery.OutcomeOK || o.MessageURL != "" {
		t.Fatalf("unchecked publish = %+v", o)
	}
	if e.adapter.LengthLimit() != LengthLimit || e.adapter.Markup() != messages.MarkupMrkdwn {
		t.Error("limit and markup")
	}
}

// TestMessageURL covers the delivery's link (C-24.FR-6): the workspace's address, archives, the channel and the `ts`
// without its dot; none without an address or a `ts`, or for an address that is not a safe link.
func TestMessageURL(t *testing.T) {
	for _, tc := range []struct{ team, channel, ts, want string }{
		{teamURLB, "C1", "1.000001", "https://fake-workspace-b.slack.com/archives/C1/p1000001"},
		{"https://x.slack.com", "C1", "1712345678.123456", "https://x.slack.com/archives/C1/p1712345678123456"},
		{"", "C1", "1.000001", ""},
		{teamURLB, "C1", "", ""},
		{teamURLB, "", "1.000001", ""},
		{"javascript:alert(1)//", "C1", "1.000001", ""},
	} {
		if got := messageURL(tc.team, tc.channel, tc.ts); got != tc.want {
			t.Errorf("messageURL(%q, %q, %q) = %q", tc.team, tc.channel, tc.ts, got)
		}
	}
}

// TestLoudPublish is C-24.FR-6 and AC-15: a Loud Root message mentions here in its `text` and its top section, and
// nowhere in the attachment; a User without a Slack link is named by their display name, escaped; a Quiet Thread
// reply carries no Mention, and a Loud one carries it in its section.
func TestLoudPublish(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := t.Context()
	o := e.adapter.Publish(ctx, loudCall(destAlerts, hereTarget, aliceTarget), e.root(t, messages.ColourFiring))
	if o.Kind != delivery.OutcomeOK {
		t.Fatalf("publish = %+v", o)
	}
	m := e.messages()[0]
	if top := texts(t, m.Blocks, nil); !strings.Contains(top, "<!here>") || !strings.Contains(m.Text, "<!here>") ||
		!strings.Contains(m.Text, "Alice *\u200bSmith*\u200b") || strings.Contains(texts(t, nil, m.Attachments), "<!") {
		t.Fatalf("loud root %+v", m)
	}
	quiet := e.adapter.Reply(ctx, slackCall(destAlerts), delivery.Root{MessageID: o.MessageID}, newAlertsReply())
	loud := e.adapter.Reply(ctx, loudCall(destAlerts, hereTarget), delivery.Root{MessageID: o.MessageID},
		newAlertsReply())
	replies := e.messages()[0].Replies
	if quiet.Kind != delivery.OutcomeOK || loud.Kind != delivery.OutcomeOK || len(replies) != 2 ||
		strings.Contains(replies[0].Text+texts(t, replies[0].Blocks, nil), "<!") ||
		!strings.Contains(texts(t, replies[1].Blocks, nil), "<!here>") {
		t.Fatalf("replies %+v", replies)
	}
}

// texts are the texts of the sections of blocks and of the attachments' blocks, as the fake keeps them, joined.
func texts(t *testing.T, blocks, attachments json.RawMessage) string {
	t.Helper()
	var bs []block
	var as []attachment
	if blocks != nil && json.Unmarshal(blocks, &bs) != nil || attachments != nil && json.Unmarshal(attachments,
		&as) != nil {
		t.Fatalf("blocks %s, attachments %s", blocks, attachments)
	}
	for _, a := range as {
		bs = append(bs, a.Blocks...)
	}
	var out []string
	for _, b := range bs {
		if b.Text != nil {
			out = append(out, b.Text.Text)
		}
	}
	return strings.Join(out, "\n")
}

// TestErrorClasses is C-24.FR-8 and AC-8 against the fake server: 429 with Retry-After holds the whole Connection for
// its seconds; 5xx and internal_error are Transient; a refused token, a missing scope — naming the scope needed — and a
// channel the bot is not in, answered by a fault or by the fake itself, are Fatal with the reason named;
// edit_window_closed, cant_update_message and any other error are unknown, as is message_not_found on a Publication.
func TestErrorClasses(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := t.Context()
	ts := e.publish(t)
	acked := e.root(t, messages.ColourAcknowledged)
	slackErr := func(code string) string { return `{"ok":false,"error":"` + code + `"}` }
	for _, tc := range []struct {
		name, method string
		status       int
		retryAfter   int
		body         string
		kind         delivery.OutcomeKind
		text         string
	}{
		{"429", "chat.postMessage", http.StatusTooManyRequests, 1, slackErr("ratelimited"),
			delivery.OutcomeRetryAfter, ""},
		{"429_edit", "chat.update", http.StatusTooManyRequests, 1, slackErr("ratelimited"),
			delivery.OutcomeRetryAfter, ""},
		{"429_without_retry_after", "chat.postMessage", http.StatusTooManyRequests, 0, slackErr("ratelimited"),
			delivery.OutcomeTransient, ""},
		{"503", "chat.postMessage", http.StatusServiceUnavailable, 0, "", delivery.OutcomeTransient, ""},
		{"internal_error", "chat.update", http.StatusOK, 0, slackErr("internal_error"), delivery.OutcomeTransient,
			"internal_error"},
		{"not_in_channel", "chat.postMessage", http.StatusOK, 0, slackErr("not_in_channel"),
			delivery.OutcomeFatal, "not_in_channel"},
		{"invalid_auth", "chat.update", http.StatusOK, 0, slackErr("invalid_auth"), delivery.OutcomeFatal,
			"invalid_auth"},
		{"missing_scope", "chat.postMessage", http.StatusOK, 0,
			`{"ok":false,"error":"missing_scope","needed":"chat:write","provided":"users:read"}`,
			delivery.OutcomeFatal, "missing_scope (needed: chat:write)"},
		{"edit_window_closed", "chat.update", http.StatusOK, 0, slackErr("edit_window_closed"),
			delivery.OutcomeUnknown, "edit_window_closed"},
		{"cant_update_message", "chat.update", http.StatusOK, 0, slackErr("cant_update_message"),
			delivery.OutcomeUnknown, "cant_update_message"},
		{"other", "chat.update", http.StatusOK, 0, slackErr("foo_bar"), delivery.OutcomeUnknown, "foo_bar"},
		{"message_not_found_on_publish", "chat.postMessage", http.StatusOK, 0, slackErr("message_not_found"),
			delivery.OutcomeUnknown, "message_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.fault(t, tc.method, tc.status, tc.retryAfter, tc.body)
			var o delivery.Outcome
			if tc.method == "chat.update" {
				o = e.adapter.Update(ctx, slackCall(destAlerts), ts, acked)
			} else {
				o = e.adapter.Publish(ctx, slackCall(destAlerts), e.root(t, messages.ColourFiring))
			}
			if o.Kind != tc.kind || !strings.Contains(string(o.Error), tc.text) || o.Status != tc.status {
				t.Fatalf("outcome %+v", o)
			}
			if tc.kind == delivery.OutcomeRetryAfter && (o.RetryAfter != time.Second ||
				o.Scope != delivery.ScopeConnection) {
				t.Fatalf("retry after %+v", o)
			}
		})
	}
	// The fake's own answer for a channel the bot is not a member of: Fatal, naming it.
	if o := e.adapter.Publish(ctx, slackCall(destOther), e.root(t, messages.ColourFiring)); o.Kind !=
		delivery.OutcomeFatal || o.Error != "not_in_channel" {
		t.Fatalf("not a member = %+v", o)
	}
}

// TestMarkupRejected is C-24.FR-8 and C-11.FR-8: invalid_blocks is MarkupRejected, returned as it is; the worker's
// call again with Plain sends `text` alone — within 3,000 bytes, still escaped for mrkdwn, with blocks and attachments
// empty — to a Publication, an edit and a Thread reply alike.
func TestMarkupRejected(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := t.Context()
	firing := e.root(t, messages.ColourFiring)
	e.fault(t, "chat.postMessage", http.StatusOK, 0, `{"ok":false,"error":"invalid_blocks"}`)
	if o := e.adapter.Publish(ctx, slackCall(destAlerts), firing); o.Kind != delivery.OutcomeMarkupRejected ||
		o.Error != "invalid_blocks" {
		t.Fatalf("rejected = %+v", o)
	}
	plain := slackCall(destAlerts)
	plain.Plain = true
	o := e.adapter.Publish(ctx, plain, firing)
	m := e.messages()
	if o.Kind != delivery.OutcomeOK || len(m) != 1 || string(m[0].Blocks) != "[]" || string(m[0].Attachments) != "[]" ||
		len(m[0].Text) > plainLimit || !strings.Contains(m[0].Text, acEscaped) {
		t.Fatalf("plain publish = %+v, %+v", o, m)
	}
	e.fault(t, "chat.update", http.StatusOK, 0, `{"ok":false,"error":"msg_too_long"}`)
	acked := e.root(t, messages.ColourAcknowledged)
	if up := e.adapter.Update(ctx, slackCall(destAlerts), o.MessageID, acked); up.Kind !=
		delivery.OutcomeMarkupRejected {
		t.Fatalf("rejected edit = %+v", up)
	}
	if up := e.adapter.Update(ctx, plain, o.MessageID, acked); up.Kind != delivery.OutcomeOK {
		t.Fatalf("plain edit = %+v", up)
	}
	if rep := e.adapter.Reply(ctx, plain, delivery.Root{MessageID: o.MessageID}, newAlertsReply()); rep.Kind !=
		delivery.OutcomeOK {
		t.Fatalf("plain reply = %+v", rep)
	}
	for _, b := range append(e.bodies(t, "chat.update")[1:], e.bodies(t, "chat.postMessage")[2:]...) {
		if bs, _ := b["blocks"].([]any); len(bs) != 0 || b["attachments"] == nil {
			t.Errorf("plain request %v", b)
		}
	}
}

// TestUpdateGone is C-24.FR-8 and AC-8: an edit answered message_not_found reads the channel with conversations.info
// in the call's client class — the bot still a member of a channel that is not archived: the Root message was deleted,
// Gone, whether it left a tombstone or went; the bot no longer a member, or the channel archived: Fatal; a read that
// fails has its own outcome. An edit of a Root message that still exists in a channel the bot left is not_in_channel.
func TestUpdateGone(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := t.Context()
	acked := e.root(t, messages.ColourAcknowledged)
	update := func(ts string) delivery.Outcome { return e.adapter.Update(ctx, slackCall(destAlerts), ts, acked) }

	withReply := e.publish(t)
	if o := e.adapter.Reply(ctx, slackCall(destAlerts), delivery.Root{MessageID: withReply}, newAlertsReply()); o.Kind !=
		delivery.OutcomeOK {
		t.Fatal(o)
	}
	if !e.fake.Delete(fakeslack.AppB, fakeslack.ChannelAlerts, withReply) || e.messages()[0].Subtype != "tombstone" {
		t.Fatalf("tombstone %+v", e.messages())
	}
	if o := update(withReply); !outcomeIs(o, delivery.OutcomeGone, "message_not_found") {
		t.Fatalf("tombstone edit = %+v", o)
	}
	if p := e.paths(); !slices.Equal(p[len(p)-2:], []string{"chat.update", "conversations.info"}) {
		t.Fatalf("requests %v", p)
	}
	alone := e.publish(t)
	e.fake.Delete("", fakeslack.ChannelAlerts, alone)
	if o := update(alone); !outcomeIs(o, delivery.OutcomeGone, "the root message was deleted") {
		t.Fatalf("deleted edit = %+v", o)
	}

	e.fault(t, "conversations.info", http.StatusTooManyRequests, 2, `{"ok":false,"error":"ratelimited"}`)
	if o := update(alone); o.Kind != delivery.OutcomeRetryAfter || o.RetryAfter != 2*time.Second ||
		o.Scope != delivery.ScopeConnection {
		t.Fatalf("read rate-limited = %+v", o)
	}
	e.fault(t, "conversations.info", http.StatusInternalServerError, 0, "")
	if o := update(alone); o.Kind != delivery.OutcomeTransient {
		t.Fatalf("read failed = %+v", o)
	}

	kept := e.publish(t)
	e.channel(t, false, false)
	if o := update(alone); !outcomeIs(o, delivery.OutcomeFatal, "not_in_channel") {
		t.Fatalf("bot removed = %+v", o)
	}
	if o := update(kept); o.Kind != delivery.OutcomeFatal || o.Error != "not_in_channel" {
		t.Fatalf("existing root, bot removed = %+v", o)
	}
	e.channel(t, true, true)
	if o := update(alone); !outcomeIs(o, delivery.OutcomeFatal, "is_archived") {
		t.Fatalf("archived = %+v", o)
	}
}

// TestReplyMisplaced is C-24.FR-8, AC-8 and F-091: a Thread reply to a Root message deleted before any reply lands in
// the channel, answered without `thread_ts`; the adapter deletes it at once with chat.delete, in the call's client
// class, and the Root message is gone. A delete that fails still makes it gone, with the reason.
func TestReplyMisplaced(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := t.Context()
	root := e.publish(t)
	e.fake.Delete(fakeslack.AppB, fakeslack.ChannelAlerts, root)
	o := e.adapter.Reply(ctx, slackCall(destAlerts), delivery.Root{MessageID: root}, newAlertsReply())
	if !outcomeIs(o, delivery.OutcomeGone, "was deleted") || len(e.messages()) != 0 {
		t.Fatalf("misplaced = %+v, messages %+v", o, e.messages())
	}
	deletes := e.bodies(t, "chat.delete")
	if len(deletes) != 1 || deletes[0]["channel"] != fakeslack.ChannelAlerts || deletes[0]["ts"] == root {
		t.Fatalf("deletes %v", deletes)
	}
	e.fault(t, "chat.delete", http.StatusOK, 0, `{"ok":false,"error":"internal_error"}`)
	o = e.adapter.Reply(ctx, slackCall(destAlerts), delivery.Root{MessageID: root}, newAlertsReply())
	if !outcomeIs(o, delivery.OutcomeGone, "could not be deleted: internal_error") || len(e.messages()) != 1 {
		t.Fatalf("not deleted = %+v, messages %+v", o, e.messages())
	}
}

// TestReplyUnderTombstone is F-090: a Thread reply under a Root message deleted after it had replies still goes into
// its Thread; the next edit finds the deletion.
func TestReplyUnderTombstone(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := t.Context()
	root := e.publish(t)
	first := e.adapter.Reply(ctx, slackCall(destAlerts), delivery.Root{MessageID: root}, newAlertsReply())
	e.fake.Delete(fakeslack.AppB, fakeslack.ChannelAlerts, root)
	second := e.adapter.Reply(ctx, slackCall(destAlerts), delivery.Root{MessageID: root}, newAlertsReply())
	ms := e.messages()
	if first.Kind != delivery.OutcomeOK || second.Kind != delivery.OutcomeOK || len(ms) != 1 ||
		ms[0].Subtype != "tombstone" || len(ms[0].Replies) != 2 || len(e.bodies(t, "chat.delete")) != 0 {
		t.Fatalf("under a tombstone: %+v %+v %+v", first, second, ms)
	}
}

// TestCheckIsTheProbe is C-24.FR-3 and FR-15: Check is the Destination check, one conversations.info that posts
// nothing — ok for a channel the bot is a member of; Fatal with the invitation that names the bot for one it is not in,
// and channel_not_found for an unknown one.
func TestCheckIsTheProbe(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := t.Context()
	if o := e.adapter.Check(ctx, slackCall(destAlerts)); o.Kind != delivery.OutcomeOK {
		t.Fatalf("member = %+v", o)
	}
	if o := e.adapter.Check(ctx, slackCall(destOther)); o.Kind != delivery.OutcomeFatal ||
		o.Error != "Invite the bot to the channel: /invite @muster-dev-bot-b" {
		t.Fatalf("not a member = %+v", o)
	}
	e.targets.byID[destOther] = Target{Client: e.targets.byID[destAlerts].Client, ChannelID: "C0UNKNOWN"}
	if o := e.adapter.Check(ctx, slackCall(destOther)); o.Kind != delivery.OutcomeFatal ||
		o.Error != MessageChannelNotFound {
		t.Fatalf("unknown channel = %+v", o)
	}
	if p := e.paths(); !slices.Equal(p, []string{"conversations.info", "conversations.info", "conversations.info"}) {
		t.Fatalf("requests %v", p)
	}
}

// TestNoTarget: a Destination without a Slack Connection is Fatal for every call, and one whose Connection could not
// be read Transient.
func TestNoTarget(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := t.Context()
	m := e.root(t, messages.ColourFiring)
	calls := func() []delivery.Outcome {
		c := slackCall(99)
		return []delivery.Outcome{e.adapter.Publish(ctx, c, m), e.adapter.Update(ctx, c, "1.000001", m),
			e.adapter.Reply(ctx, c, delivery.Root{MessageID: "1.000001"}, newAlertsReply()), e.adapter.Check(ctx, c)}
	}
	for _, o := range calls() {
		if o.Kind != delivery.OutcomeFatal || o.Error != outbound.Untrusted(ErrNoTarget.Error()) {
			t.Errorf("no target = %+v", o)
		}
	}
	e.targets.err = errors.New("down")
	for _, o := range calls() {
		if o.Kind != delivery.OutcomeTransient || o.Error != errorNoConnection {
			t.Errorf("unreadable = %+v", o)
		}
	}
	if len(e.fake.Requests()) != 0 {
		t.Errorf("requests %v", e.paths())
	}
}

// TestAdapterRequestLog is C-24.FR-14: each Publication, edit, Thread reply, deletion of a misplaced reply and probe is
// recorded with Slack's method as its operation, the Destination and the Alert Group linked and the Connection as its
// owner, and the bot token appears nowhere.
func TestAdapterRequestLog(t *testing.T) {
	e := newAdapterEnv(t)
	ctx := outbound.WithLinks(t.Context(), outbound.Links{Destination: destAlerts, AlertGroup: 5})
	firing := e.root(t, messages.ColourFiring)
	pub := e.adapter.Publish(ctx, slackCall(destAlerts), firing)
	e.adapter.Update(ctx, slackCall(destAlerts), pub.MessageID, e.root(t, messages.ColourAcknowledged))
	e.adapter.Reply(ctx, slackCall(destAlerts), delivery.Root{MessageID: pub.MessageID}, newAlertsReply())
	e.fake.Delete(fakeslack.AppB, fakeslack.ChannelAlerts, pub.MessageID)
	other := e.adapter.Publish(ctx, slackCall(destAlerts), firing)
	e.fake.Delete(fakeslack.AppB, fakeslack.ChannelAlerts, other.MessageID)
	e.adapter.Reply(ctx, slackCall(destAlerts), delivery.Root{MessageID: other.MessageID}, newAlertsReply())
	e.adapter.Check(ctx, slackCall(destAlerts))
	var ops []string
	for _, r := range e.rec.got() {
		ops = append(ops, r.Operation)
		if r.Links.Destination != destAlerts || r.Links.AlertGroup != 5 || r.Owner.Connection != adapterConnection ||
			r.Status != http.StatusOK {
			t.Errorf("record %s: %+v %+v %d", r.Operation, r.Links, r.Owner, r.Status)
		}
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), fakeslack.SecondBotToken) ||
			r.RequestHeader.Get("Authorization") != "[redacted]" {
			t.Errorf("record %s shows the bot token: %s", r.Operation, raw)
		}
	}
	want := []string{"chat.postMessage", "chat.update", "chat.postMessage", "chat.postMessage", "chat.postMessage",
		"chat.delete", "conversations.info"}
	if !slices.Equal(ops, want) {
		t.Fatalf("operations %v", ops)
	}
}

// TestGoldensReachTheFake posts the request of every golden file through the client to the fake server, whose rules
// of Block Kit — limits, styles, ids — accept each: an edit after a Root message, a Thread reply under one.
func TestGoldensReachTheFake(t *testing.T) {
	f := startFake(t)
	c := newClient(t, network(t, nil), Settings{BotToken: logging.Secret(fakeslack.BotToken), Route: devRoute(f)})
	ctx := t.Context()
	for _, tc := range layoutCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			root, r := c.postMessage(ctx, outbound.ClassDelivery, layout(goldenRoot(t), testLook(), nil))
			if !r.OK() {
				t.Fatalf("root: %+v", r)
			}
			o := tc.o
			switch {
			case o.TS != "":
				o.TS = root.TS
				r = c.updateMessage(ctx, outbound.ClassDelivery, o)
			case o.ThreadTS != "":
				o.ThreadTS = root.TS
				var p posted
				p, r = c.postMessage(ctx, outbound.ClassDelivery, o)
				if r.OK() && p.Message.ThreadTS != root.TS {
					t.Errorf("thread_ts %q, want %q", p.Message.ThreadTS, root.TS)
				}
			default:
				_, r = c.postMessage(ctx, outbound.ClassDelivery, o)
			}
			if !r.OK() {
				t.Fatalf("%s: %+v %s", tc.name, r, r.Body)
			}
		})
	}
}

// goldenRoot is a firing Root message to post the goldens' edits and replies under.
func goldenRoot(t *testing.T) messages.Message {
	t.Helper()
	return newLayoutEnv(t).root(t, layoutSource(messages.ColourFiring))
}
