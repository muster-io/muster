// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/templates"
)

const (
	connectionPublicID = "CN0000000000C1"
	groupPage          = "http://localhost:8080/alert-groups/AG0000000000A1"
)

// targets are the Targets of the tests by Destination id; err answers every lookup when set.
type targets struct {
	byID map[int64]mattermost.Target
	err  error
}

func (ts *targets) Target(_ context.Context, id int64) (mattermost.Target, error) {
	if ts.err != nil {
		return mattermost.Target{}, ts.err
	}
	t, ok := ts.byID[id]
	if !ok {
		return mattermost.Target{}, mattermost.ErrNoTarget
	}
	return t, nil
}

type adapterEnv struct {
	fake    *fakemattermost.Fake
	adapter *mattermost.Adapter
	targets *targets
	client  *mattermost.Client
}

// newAdapter is the adapter over the fake server, with Destination 1 on ch-alerts and Destination 2 on a channel that
// does not exist.
func newAdapter(t *testing.T) *adapterEnv {
	t.Helper()
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	ts := &targets{byID: map[int64]mattermost.Target{
		1: {Client: c, ConnectionID: 3, ConnectionPublicID: connectionPublicID, TeamID: fakemattermost.TeamID,
			TeamName: fakemattermost.TeamName, ChannelID: fakemattermost.ChannelAlerts},
		2: {Client: c, ConnectionID: 3, ConnectionPublicID: connectionPublicID, TeamID: fakemattermost.TeamID,
			ChannelID: "ch-missing"},
	}}
	return &adapterEnv{fake: f, targets: ts, client: c, adapter: &mattermost.Adapter{Targets: ts,
		PublicURL: "http://localhost:8080/", IngestURL: "http://localhost:8081/", Version: "v1.2.3"}}
}

func call(destination int64) delivery.Call {
	return delivery.Call{Class: outbound.ClassDelivery, Destination: delivery.Destination{ID: destination,
		PublicID: "DS0000000000D1", Type: delivery.TypeMattermost}}
}

func loud(ts ...mentions.Target) delivery.Call {
	c := call(1)
	c.Loudness, c.Targets = groups.Loud, ts
	return c
}

var channel = mentions.Target{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneChannel}

// root is a Root message in a status, with a label that tries to mention the channel and write HTML.
func root(status string) messages.Message {
	m := messages.Message{Kind: messages.KindRoot, Language: "en", TimeZone: "UTC", Colour: status,
		Heading:      &messages.Heading{Number: 1, Title: "DiskFull", URL: groupPage},
		CommonLabels: []messages.Label{{Name: "note", Value: messages.Value("@channel <b>x</b>")}},
		Links:        []messages.Link{{Text: "Open in Muster", URL: groupPage}},
		Footer:       "Acknowledged by bob"}
	for _, b := range buttons.ForStatus(status, 3) {
		m.Buttons = append(m.Buttons, messages.Button{Command: b.Command, Argument: b.Argument, Label: b.Command,
			ActionID: "action", KeyID: "k-00112233"})
	}
	return m
}

func reply(lines ...string) messages.Message {
	return messages.Message{Kind: messages.KindReply, Language: "en", Colour: messages.ColourFiring, Lines: lines,
		Buttons: []messages.Button{}}
}

// attachmentOf is the one attachment of a recorded post.
func attachmentOf(t *testing.T, p fakemattermost.RecordedPost) map[string]any {
	t.Helper()
	var props struct {
		Attachments []map[string]any `json:"attachments"`
	}
	if err := json.Unmarshal(p.Props, &props); err != nil || len(props.Attachments) != 1 {
		t.Fatalf("props %s: %v", p.Props, err)
	}
	return props.Attachments[0]
}

func postByID(t *testing.T, f *fakemattermost.Fake, id string) fakemattermost.RecordedPost {
	t.Helper()
	for _, p := range f.Posts() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no post %s", id)
	return fakemattermost.RecordedPost{}
}

// TestPublishUpdateReply is C-13.AC-13, AC-1, AC-5 and AC-6 against the fake server: a Quiet Root message with its
// attachment and its permalink, which notifies nobody for an alert label `@channel <b>x</b>`; an edit to the
// acknowledged colour that notifies nobody; a Quiet Thread reply that raises the reply count, and a Loud one that
// mentions the channel in its message.
func TestPublishUpdateReply(t *testing.T) {
	e := newAdapter(t)
	ctx := t.Context()
	if e.adapter.LengthLimit() != 16383 || e.adapter.Markup() != messages.MarkupMarkdown {
		t.Errorf("limit %d, markup %s", e.adapter.LengthLimit(), e.adapter.Markup())
	}
	out := e.adapter.Publish(ctx, call(1), root(messages.ColourFiring))
	if out.Kind != delivery.OutcomeOK || out.MessageID == "" ||
		out.MessageURL != e.fake.URL()+"/dev/pl/"+out.MessageID {
		t.Fatalf("publish = %+v", out)
	}
	p := postByID(t, e.fake, out.MessageID)
	a := attachmentOf(t, p)
	text, _ := a["text"].(string)
	actions, _ := a["actions"].([]any)
	first, _ := actions[0].(map[string]any)
	integration, _ := first["integration"].(map[string]any)
	if p.Message != "🔴 #1 DiskFull" || p.ChannelID != fakemattermost.ChannelAlerts || a["color"] != "#d32f2f" ||
		a["title"] != "#1 DiskFull" || a["title_link"] != groupPage || a["footer"] != "Muster v1.2.3" ||
		a["footer_icon"] != "http://localhost:8080/muster-mark-256.png" || len(actions) != 5 ||
		integration["url"] != "http://localhost:8081/api/v1/callbacks/mattermost/"+connectionPublicID ||
		!strings.Contains(text, "[Open in Muster]("+groupPage+")") || !strings.HasSuffix(text, "Acknowledged by bob") {
		t.Errorf("post = %+v\nattachment = %+v", p.Post, a)
	}
	// C-13.AC-5: the escaper of S-036 escapes < and > for Markdown, which Mattermost shows as the literal text.
	if !strings.Contains(text, "note: @\u200bchannel \\<b\\>x\\</b\\>") || len(e.fake.Notifications()) != 0 {
		t.Errorf("text %q, notifications %+v", text, e.fake.Notifications())
	}

	if up := e.adapter.Update(ctx, call(1), out.MessageID, root(messages.ColourAcknowledged)); up.Kind !=
		delivery.OutcomeOK || up.MessageID != out.MessageID {
		t.Fatalf("update = %+v", up)
	}
	p = postByID(t, e.fake, out.MessageID)
	if a := attachmentOf(t, p); a["color"] != "#f57c00" || p.Message != "🟠 #1 DiskFull" || p.EditAt == 0 {
		t.Errorf("edited = %+v, %+v", p.Post, a)
	}
	if up := e.adapter.Update(ctx, loud(channel), out.MessageID, root(messages.ColourAcknowledged)); up.Kind !=
		delivery.OutcomeOK || strings.Contains(postByID(t, e.fake, out.MessageID).Message, "@") {
		t.Errorf("an edit carries no mention: %+v", up)
	}

	r := delivery.Root{MessageID: out.MessageID}
	quiet := e.adapter.Reply(ctx, call(1), r, reply("New alerts (1):", "• pod: i2"))
	if quiet.Kind != delivery.OutcomeOK || quiet.MessageID == "" {
		t.Fatalf("quiet reply = %+v", quiet)
	}
	if q := postByID(t, e.fake, quiet.MessageID); q.RootID != out.MessageID || strings.Contains(q.Message, "@") ||
		postByID(t, e.fake, out.MessageID).ReplyCount != 1 || len(e.fake.Notifications()) != 0 {
		t.Errorf("quiet reply = %+v", q.Post)
	}
	bob := mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{Name: "Bob", Username: "bob"}}
	lr := e.adapter.Reply(ctx, loud(channel, bob), r, reply("New alerts (1):", "• pod: i3"))
	l := postByID(t, e.fake, lr.MessageID)
	ns := e.fake.Notifications()
	if lr.Kind != delivery.OutcomeOK || !strings.HasPrefix(l.Message, "@channel @bob\nNew alerts (1):") ||
		len(ns) != 2 || ns[0].PostID != lr.MessageID {
		t.Errorf("loud reply = %+v, %+v", l.Post, ns)
	}

	// C-13.AC-6: a Loud Root message carries its Mentions in its message, never in its attachment.
	lp := e.adapter.Publish(ctx, loud(channel), root(messages.ColourFiring))
	l = postByID(t, e.fake, lp.MessageID)
	if l.Message != "🔴 #1 DiskFull\n@channel" || strings.Contains(attachmentOf(t, l)["text"].(string), "@channel") {
		t.Errorf("loud root = %+v", l.Post)
	}
	for _, req := range e.fake.Requests() {
		if req.Path == "/api/v4/posts" && req.Headers["Content-Type"][0] != "application/json" {
			t.Errorf("%s %s: Content-Type %v", req.Method, req.Path, req.Headers["Content-Type"])
		}
	}
}

// TestTemplateMentions is C-13.FR-8 with F-056: the Mention tokens of a Route's templates never notify from the
// attachment. A Loud Root message adds the everyone and group ones to the Mentions of its message, once each beside
// its targets'; a Quiet one and every edit notify nobody; the Owner's token stays text that notifies nobody.
func TestTemplateMentions(t *testing.T) {
	e := newAdapter(t)
	ctx := t.Context()
	tok := func(name, group string) string {
		return templates.MentionToken(templates.Token{Name: name, Group: group})
	}
	m := root(messages.ColourFiring)
	m.Body = &messages.Body{Text: "page " + tok("channel", "") + " " + tok("group", "oncall") + " " +
		tok("owner", "") + " " + tok("channel", ""), Markup: messages.MarkupMarkdown}
	m.Alerts = &messages.AlertList{Lines: []messages.AlertLine{{Text: "line " + tok("here", ""),
		Markup: messages.MarkupMarkdown}}}
	neutral := func(t *testing.T, p fakemattermost.RecordedPost) {
		t.Helper()
		text, _ := attachmentOf(t, p)["text"].(string)
		for _, want := range []string{"page @\u200bchannel @\u200boncall @\u200bowner @\u200bchannel",
			"line @\u200bhere"} {
			if !strings.Contains(text, want) {
				t.Errorf("the attachment has no %q: %q", want, text)
			}
		}
	}

	out := e.adapter.Publish(ctx, loud(channel), m)
	p := postByID(t, e.fake, out.MessageID)
	neutral(t, p)
	ns := e.fake.Notifications()
	if out.Kind != delivery.OutcomeOK || p.Message != "🔴 #1 DiskFull\n@channel @oncall @here" || len(ns) == 0 {
		t.Fatalf("loud = %+v, %+v; notifications %+v", out, p.Post, ns)
	}
	for _, n := range ns {
		if n.Preview != p.Message || n.Kind != fakemattermost.KindChannel {
			t.Errorf("a notification without its preview: %+v", n)
		}
	}

	quiet := e.adapter.Publish(ctx, call(1), m)
	p = postByID(t, e.fake, quiet.MessageID)
	neutral(t, p)
	if quiet.Kind != delivery.OutcomeOK || p.Message != "🔴 #1 DiskFull" || len(e.fake.Notifications()) != len(ns) {
		t.Errorf("quiet = %+v, %+v; notifications %+v", quiet, p.Post, e.fake.Notifications())
	}

	if up := e.adapter.Update(ctx, loud(channel), out.MessageID, m); up.Kind != delivery.OutcomeOK {
		t.Fatalf("update = %+v", up)
	}
	p = postByID(t, e.fake, out.MessageID)
	neutral(t, p)
	if p.Message != "🔴 #1 DiskFull" || p.EditAt == 0 {
		t.Errorf("an edit mentions: %+v", p.Post)
	}
}

// TestPublishWithoutTeamName links a post through the server's redirect when the team's name is unknown, and a Storm
// summary is a post without an attachment.
func TestPublishWithoutTeamName(t *testing.T) {
	e := newAdapter(t)
	tg := e.targets.byID[1]
	tg.TeamName = ""
	e.targets.byID[3] = tg
	storm := messages.Message{Kind: messages.KindStorm, Language: "en", Colour: messages.ColourStorm,
		Lines: []string{"Storm"}, Buttons: []messages.Button{}}
	out := e.adapter.Publish(t.Context(), call(3), storm)
	if out.Kind != delivery.OutcomeOK || out.MessageURL != e.fake.URL()+"/_redirect/pl/"+out.MessageID {
		t.Errorf("publish = %+v", out)
	}
	if p := postByID(t, e.fake, out.MessageID); p.Message != "Storm" || string(p.Props) != `{"attachments":[]}` {
		t.Errorf("storm = %+v", p.Post)
	}
}

// TestPostResponseMapping is C-13.FR-5 and C-13.AC-15: 429 with a plain-text body holds the whole Connection for the
// seconds of Retry-After, 1 s without it; 5xx and network errors are Transient; 401, 403 and 404 on a new post are
// Fatal; anything else is unknown, each with the masked text of the answer, and so is a success without a post id.
func TestPostResponseMapping(t *testing.T) {
	e := newAdapter(t)
	ctx := t.Context()
	m := root(messages.ColourFiring)
	cases := []struct {
		name  string
		fault fakeserver.Fault
		kind  delivery.OutcomeKind
		text  string
	}{
		{"429", fakeserver.Fault{Status: 429, RetryAfterSeconds: 2, ContentType: "text/plain", Body: "limit exceeded"},
			delivery.OutcomeRetryAfter, ""},
		{"500", fakeserver.Fault{Status: 500}, delivery.OutcomeTransient, ""},
		{"401", fakeserver.Fault{Status: 401,
			Body: `{"id":"api.context.session_expired.app_error","message":"Invalid or expired session"}`},
			delivery.OutcomeFatal, "Mattermost answered 401: Invalid or expired session"},
		{"403", fakeserver.Fault{Status: 403}, delivery.OutcomeFatal, ""},
		{"400", fakeserver.Fault{Status: 400,
			Body: `{"id":"model.post.is_valid.message_length.app_error","message":"too long"}`},
			delivery.OutcomeUnknown, "model.post.is_valid.message_length.app_error"},
		{"201 without an id", fakeserver.Fault{Status: 201, Body: `{"channel_id":"x"}`}, delivery.OutcomeUnknown,
			"the post was answered without its id"},
	}
	for _, c := range cases {
		c.fault.Path, c.fault.Times = "/api/v4/posts", 1
		fault(t, e.fake, c.fault)
		out := e.adapter.Publish(ctx, call(1), m)
		if out.Kind != c.kind || !strings.Contains(string(out.Error), c.text) || out.MessageID != "" {
			t.Errorf("%s = %+v", c.name, out)
		}
		if c.name == "429" && (out.Scope != delivery.ScopeConnection || out.RetryAfter != 2*time.Second) {
			t.Errorf("429 = %+v", out)
		}
	}
	fault(t, e.fake, fakeserver.Fault{Path: "/api/v4/posts", Status: 429, Times: 1, ContentType: "text/plain",
		Body: "limit exceeded"})
	if out := e.adapter.Publish(ctx, call(1), m); out.Kind != delivery.OutcomeRetryAfter ||
		out.RetryAfter != time.Second || out.Scope != delivery.ScopeConnection {
		t.Errorf("429 without Retry-After = %+v", out)
	}
	if out := e.adapter.Publish(ctx, call(2), m); out.Kind != delivery.OutcomeFatal ||
		!strings.Contains(string(out.Error), "app.channel.get.existing.app_error") {
		t.Errorf("an unknown channel = %+v", out)
	}
	control(t, http.MethodDelete, e.fake.URL()+"/_fake/channels/"+fakemattermost.ChannelAlerts+"/members/"+
		fakemattermost.BotUserID, "")
	if out := e.adapter.Publish(ctx, call(1), m); out.Kind != delivery.OutcomeFatal ||
		!strings.Contains(string(out.Error), "403") {
		t.Errorf("the bot removed from the channel = %+v", out)
	}
	if err := e.fake.Close(context.WithoutCancel(ctx)); err != nil {
		t.Fatal(err)
	}
	if out := e.adapter.Publish(ctx, call(1), m); out.Kind != delivery.OutcomeTransient {
		t.Errorf("a server that is down = %+v", out)
	}
}

// TestEdits is C-13.AC-12 and F-058: a 404 on an edit is a Root message that is gone; a 403 is followed by the plain
// read of the post, whose 404 makes it gone, whose 200 keeps the 403 Fatal, and whose other answers are classified;
// a reply under a deleted Root message is refused with 400 and is gone at once, without a read.
func TestEdits(t *testing.T) {
	e := newAdapter(t)
	ctx := t.Context()
	m := root(messages.ColourAcknowledged)
	if out := e.adapter.Update(ctx, call(1), "nosuchpost", m); out.Kind != delivery.OutcomeGone {
		t.Errorf("an unknown post = %+v", out)
	}
	id := e.adapter.Publish(ctx, call(1), root(messages.ColourFiring)).MessageID
	patchPath, readPath := "/api/v4/posts/"+id+"/patch", "/api/v4/posts/"+id
	cases := []struct {
		name string
		read *fakeserver.Fault
		kind delivery.OutcomeKind
	}{
		{"read 200", nil, delivery.OutcomeFatal},
		{"read 500", &fakeserver.Fault{Status: 500}, delivery.OutcomeTransient},
		{"read 429", &fakeserver.Fault{Status: 429, RetryAfterSeconds: 1, ContentType: "text/plain"},
			delivery.OutcomeRetryAfter},
		{"read 401", &fakeserver.Fault{Status: 401}, delivery.OutcomeFatal},
	}
	for _, c := range cases {
		fault(t, e.fake, fakeserver.Fault{Path: patchPath, Status: 403, Times: 1,
			Body: `{"id":"api.context.permissions.app_error","message":"You do not have the appropriate permissions."}`})
		if c.read != nil {
			c.read.Path, c.read.Times = readPath, 1
			fault(t, e.fake, *c.read)
		}
		out := e.adapter.Update(ctx, call(1), id, m)
		if out.Kind != c.kind {
			t.Errorf("%s = %+v", c.name, out)
		}
		if c.name == "read 200" && !strings.Contains(string(out.Error), "api.context.permissions.app_error") {
			t.Errorf("a real permission error = %+v", out)
		}
	}
	fault(t, e.fake, fakeserver.Fault{Path: patchPath, Status: 502, Times: 1})
	if out := e.adapter.Update(ctx, call(1), id, m); out.Kind != delivery.OutcomeTransient {
		t.Errorf("an edit answered 502 = %+v", out)
	}
	plain := call(1)
	plain.Plain = true
	if out := e.adapter.Update(ctx, plain, id, m); out.Kind != delivery.OutcomeOK ||
		!strings.Contains(attachmentOf(t, postByID(t, e.fake, id))["text"].(string), "Open in Muster "+groupPage) {
		t.Errorf("a plain edit = %+v", out)
	}
	control(t, http.MethodDelete, e.fake.URL()+"/_fake/posts/"+id, "")
	e.fake.ResetRequests()
	if out := e.adapter.Update(ctx, call(1), id, m); out.Kind != delivery.OutcomeGone ||
		!strings.Contains(string(out.Error), "403") {
		t.Errorf("a deleted root message = %+v", out)
	}
	var statuses []string
	for _, req := range e.fake.Requests() {
		statuses = append(statuses, req.Method+" "+req.Path+"?"+req.Query+" "+http.StatusText(req.Status))
	}
	if want := []string{"PUT " + patchPath + "? Forbidden", "GET " + readPath + "? Not Found"}; strings.Join(statuses,
		"|") != strings.Join(want, "|") {
		t.Errorf("requests = %v", statuses)
	}
	e.fake.ResetRequests()
	if out := e.adapter.Reply(ctx, call(1), delivery.Root{MessageID: id}, reply("New alerts (1):")); out.Kind !=
		delivery.OutcomeGone || !strings.Contains(string(out.Error), "api.post.create_post.root_id.app_error") {
		t.Errorf("a reply under a deleted root message = %+v", out)
	}
	if n := len(e.fake.Requests()); n != 1 {
		t.Errorf("a reply under a deleted root message made %d requests", n)
	}
	if out := e.adapter.Reply(ctx, call(2), delivery.Root{MessageID: id}, reply("x")); out.Kind !=
		delivery.OutcomeFatal {
		t.Errorf("a reply into an unknown channel = %+v", out)
	}
}

// TestCheck is the adapter's Check, the Destination check of C-13.FR-10 for the Broken probe: ok while the bot is in
// the channel, Fatal once it was removed, in the client class of the call.
func TestCheck(t *testing.T) {
	e := newAdapter(t)
	ctx := t.Context()
	if out := e.adapter.Check(ctx, call(1)); out.Kind != delivery.OutcomeOK {
		t.Errorf("check = %+v", out)
	}
	control(t, http.MethodDelete, e.fake.URL()+"/_fake/channels/"+fakemattermost.ChannelAlerts+"/members/"+
		fakemattermost.BotUserID, "")
	if out := e.adapter.Check(ctx, call(1)); out.Kind != delivery.OutcomeFatal {
		t.Errorf("check without the bot = %+v", out)
	}
}

// TestTargetErrors: a Destination without a Mattermost Connection is Fatal for every call, and a Connection that could
// not be read is Transient.
func TestTargetErrors(t *testing.T) {
	e := newAdapter(t)
	ctx := t.Context()
	m := root(messages.ColourFiring)
	calls := map[string]func() delivery.Outcome{
		"publish": func() delivery.Outcome { return e.adapter.Publish(ctx, call(9), m) },
		"update":  func() delivery.Outcome { return e.adapter.Update(ctx, call(9), "p", m) },
		"reply":   func() delivery.Outcome { return e.adapter.Reply(ctx, call(9), delivery.Root{MessageID: "p"}, m) },
		"check":   func() delivery.Outcome { return e.adapter.Check(ctx, call(9)) },
	}
	for name, f := range calls {
		if out := f(); out.Kind != delivery.OutcomeFatal || out.Error != outbound.Untrusted(mattermost.ErrNoTarget.Error()) {
			t.Errorf("%s without a target = %+v", name, out)
		}
	}
	e.targets.err = errors.New("the database is down")
	for name, f := range calls {
		if out := f(); out.Kind != delivery.OutcomeTransient || strings.Contains(string(out.Error), "database") {
			t.Errorf("%s with a failed read = %+v", name, out)
		}
	}
	if len(e.fake.Requests()) != 0 {
		t.Errorf("requests = %+v", e.fake.Requests())
	}
}

// TestEphemeralPost is the private answer of a press (C-13.FR-4, F-026): a post shown to one person in the channel
// view without a root_id, and in the Thread with one, for a bot with the system admin role; a bot with the role Member
// is refused with 403 (F-063).
func TestEphemeralPost(t *testing.T) {
	e := newAdapter(t)
	ctx := t.Context()
	if r := e.client.EphemeralPost(ctx, outbound.ClassInteractive, fakemattermost.AliceUserID,
		fakemattermost.ChannelAlerts, "", "Not linked"); r.Status != http.StatusForbidden ||
		r.ErrorID != "api.context.permissions.app_error" || r.Outcome.Kind != delivery.OutcomeFatal {
		t.Fatalf("ephemeral from a Member bot = %+v", r)
	}
	e.fake.SetBotSystemAdmin(true)
	if r := e.client.EphemeralPost(ctx, outbound.ClassInteractive, fakemattermost.AliceUserID,
		fakemattermost.ChannelAlerts, "", "Not linked"); !r.OK() {
		t.Fatalf("ephemeral = %+v", r)
	}
	if r := e.client.EphemeralPost(ctx, outbound.ClassInteractive, fakemattermost.BobUserID,
		fakemattermost.ChannelAlerts, "root1", "In the thread"); !r.OK() {
		t.Fatalf("ephemeral in a thread = %+v", r)
	}
	eps := e.fake.Ephemeral()
	if len(eps) != 2 || eps[0].UserID != fakemattermost.AliceUserID || eps[0].RootID != "" ||
		eps[0].Message != "Not linked" || eps[0].ShownIn != fakemattermost.ShownInChannel ||
		eps[1].RootID != "root1" || eps[1].ShownIn != fakemattermost.ShownInThread {
		t.Errorf("ephemeral = %+v", eps)
	}
	if r := e.client.EphemeralPost(ctx, outbound.ClassInteractive, fakemattermost.AliceUserID, "ch-missing", "",
		"x"); r.Outcome.Kind != delivery.OutcomeFatal || r.Status != http.StatusNotFound {
		t.Errorf("an unknown channel = %+v", r)
	}
}
