// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

// stubRenderer stands in for messages.Renderer without a database: a Root message is "#N title" and its status with
// the buttons of the status; a Thread reply is "#N event" with its fingerprints, at most its limit and then "…and K
// more — open in Muster"; the Storm summary says its counts. It records the markups it was asked for, and fails a
// Route template, or fails outright, when told to.
type stubRenderer struct {
	markups  *[][]messages.Markup
	failure  *messages.Failure
	err      error
	afterRan *int
}

var stubStatus = map[groups.Status]string{groups.StatusFiring: "Firing", groups.StatusAcknowledged: "Acknowledged",
	groups.StatusSnoozed: "Snoozed", groups.StatusResolved: "Resolved"}

var stubButtons = map[groups.Status][]string{
	groups.StatusFiring:       {"acknowledge", "resolve", "snooze"},
	groups.StatusAcknowledged: {"unacknowledge", "resolve", "snooze"},
	groups.StatusSnoozed:      {"acknowledge", "unsnooze", "resolve"},
}

func (s stubRenderer) Roots(_ context.Context, _ messages.DBTX, g delivery.GroupView, markups []messages.Markup) (
	delivery.Roots, error) {
	if s.err != nil {
		return delivery.Roots{}, s.err
	}
	if s.markups != nil {
		*s.markups = append(*s.markups, markups)
	}
	status := stubStatus[g.Status]
	if g.Urgent {
		status += " · Urgent"
	}
	m := messages.Message{Kind: messages.KindRoot, Language: "en", TimeZone: "UTC", Colour: string(g.Status),
		Lines: []string{fmt.Sprintf("#%d %s", g.Number, g.Title), status}, Buttons: []messages.Button{}}
	for _, c := range stubButtons[g.Status] {
		m.Buttons = append(m.Buttons, messages.Button{Command: c, Label: c})
	}
	out := delivery.Roots{Messages: map[messages.Markup]messages.Rendered{}, Language: "en",
		Data: &templates.Data{Status: string(g.Status), AlertGroup: templates.AlertGroup{Number: g.Number,
			Title: g.Title, Status: status, Urgent: g.Urgent}}}
	for _, mk := range markups {
		out.Messages[mk] = messages.Rendered{Message: m, KeyID: "k-stub"}
	}
	if s.failure != nil {
		out.Failures = []messages.Failure{*s.failure}
	}
	if s.afterRan != nil {
		out.After = func(context.Context) { *s.afterRan++ }
	}
	return out, nil
}

func (s stubRenderer) Reply(_ context.Context, _ messages.DBTX, r delivery.ReplyView, g delivery.GroupView) (
	delivery.Message, error) {
	if s.err != nil {
		return delivery.Message{}, s.err
	}
	m := messages.Message{Kind: messages.KindReply, Language: "en", Colour: string(g.Status),
		Lines: []string{fmt.Sprintf("#%d %s", g.Number, r.Event)}, Buttons: []messages.Button{}}
	listed := r.Fingerprints
	if r.Listed >= 0 && len(listed) > r.Listed {
		listed = listed[:r.Listed]
	}
	m.Lines = append(m.Lines, listed...)
	if more := len(r.Fingerprints) - len(listed); more > 0 {
		m.Lines = append(m.Lines, fmt.Sprintf("…and %d more — open in Muster", more))
	}
	if r.Event == messages.EventReminder {
		m.Buttons = []messages.Button{{Command: buttons.CommandStillOnIt, Label: "Still on it", ActionID: "s",
			KeyID: "k-stub"}, {Command: buttons.CommandUnacknowledge, Label: "Unack", ActionID: "u", KeyID: "k-stub"}}
	}
	return m, nil
}

func (stubRenderer) Storm(_, routeName, _ string, count, urgent int64) delivery.Message {
	return messages.Message{Kind: messages.KindStorm, Colour: messages.ColourStorm, Buttons: []messages.Button{},
		Lines: []string{fmt.Sprintf("Storm on Route %s: %d new Alert Groups, %d Urgent", routeName, count, urgent)}}
}

func (stubRenderer) StormOver(_, _ string, open int64) delivery.Message {
	return messages.Message{Kind: messages.KindStorm, Colour: messages.ColourStorm, Buttons: []messages.Button{},
		Lines: []string{fmt.Sprintf("Storm over: %d Alert Groups still open", open)}}
}

// textOf is what a recorded call shows: the text of its message, or for a request of an outgoing webhook in the
// template mode its Desired state as the stub renders a message — "#N title" and the status of the Alert Group, or the
// Storm summary — and the text of the final edit.
func textOf(c deliverytest.Call) string {
	if c.Webhook == nil {
		return c.Message.Text()
	}
	var st delivery.RequestState
	_ = json.Unmarshal(c.Webhook.State, &st)
	var lines []string
	switch {
	case st.Group != nil:
		lines = append(lines, fmt.Sprintf("#%d %s", st.Group.AlertGroup.Number, st.Group.AlertGroup.Title),
			st.Group.AlertGroup.Status)
	case st.Storm != nil && st.Storm.Final:
		lines = append(lines, "Storm over on Route "+st.Storm.Route)
	case st.Storm != nil:
		lines = append(lines, fmt.Sprintf("Storm on Route %s: %d new Alert Groups, %d Urgent", st.Storm.Route,
			st.Storm.AlertGroupCount, st.Storm.UrgentCount))
	}
	if c.Webhook.Final != "" {
		lines = append(lines, c.Webhook.Final)
	}
	return strings.Join(lines, "\n")
}

// TestRenderOncePerMarkup: an Enqueue renders the Root message once for the markups of the Route's Destinations —
// Markdown for Mattermost, plain text for an outgoing webhook — stores the key that signed its buttons, reports a
// Route template that failed to the dispatcher and queues what the render runs once committed; a render that fails
// fails the Enqueue.
func TestRenderOncePerMarkup(t *testing.T) {
	e := newEnv(t)
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	var markups [][]messages.Markup
	ran := 0
	fail := &messages.Failure{Template: messages.TemplateRootMessage,
		Error: &templates.Error{Code: templates.CodeUnknownFunction, Line: 1, Column: 4, Detail: `function "env"`}}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business,
		Renderer: stubRenderer{markups: &markups, failure: fail, afterRan: &ran}})
	var reported []groups.TemplateFailure
	var queued []func(context.Context)
	err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring, "a"),
		Actor: groups.System, Events: []groups.Recorded{{Seq: 1, Event: groups.EventCreated, Loudness: groups.Loud}},
		After:    func(f func(context.Context)) { queued = append(queued, f) },
		Fallback: func(f groups.TemplateFailure) { reported = append(reported, f) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(markups) != 1 || !slices.Equal(markups[0], []messages.Markup{messages.MarkupMarkdown,
		messages.MarkupPlain}) {
		t.Errorf("markups %v", markups)
	}
	if len(reported) != 1 || reported[0].Template != "root_message" ||
		!strings.HasPrefix(reported[0].Detail, "root_message template failed: line 1, column 4") {
		t.Errorf("reported %+v", reported)
	}
	for _, f := range queued {
		f(t.Context())
	}
	if ran != 1 {
		t.Errorf("the render's after-commit work ran %d times", ran)
	}
	if d := e.deliveryOf(groupID, destMM); d == nil || d.buttonKeyID != "k-stub" {
		t.Errorf("delivery to Mattermost: %+v", d)
	}
	// An outgoing webhook in the template mode stores the data its request templates read, without buttons.
	var st delivery.RequestState
	if d := e.deliveryOf(groupID, destWH); d == nil || d.buttonKeyID != "" || json.Unmarshal(d.payload, &st) != nil ||
		st.Group == nil || st.Group.AlertGroup.Number != 7 || st.Language != "en" {
		t.Errorf("delivery to the webhook: %+v", d)
	}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business,
		Renderer: stubRenderer{err: errBoom}})
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring, "b"),
		Actor: groups.System}); !errors.Is(err, errBoom) {
		t.Errorf("a failed render = %v", err)
	}
	// A Telegram Destination is laid out in HTML.
	e.db.dests[destMM].typ = delivery.TypeTelegram
	markups = nil
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business,
		Renderer: stubRenderer{markups: &markups}})
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring, "c"),
		Actor: groups.System}); err != nil || len(markups) != 1 || markups[0][0] != messages.MarkupHTML {
		t.Errorf("telegram = %v, %v", err, markups)
	}
}

// replyMarkups renders as stubRenderer does and records the markup each Thread reply was rendered for.
type replyMarkups struct {
	stubRenderer
	replies *[]messages.Markup
}

func (r replyMarkups) Reply(ctx context.Context, db messages.DBTX, rv delivery.ReplyView, g delivery.GroupView) (
	delivery.Message, error) {
	*r.replies = append(*r.replies, rv.Markup)
	return r.stubRenderer.Reply(ctx, db, rv, g)
}

// TestBridgeMarkup is C-22.FR-4 on delivery: the Root messages of a Bridge Destination are rendered in CommonMark,
// beside the markups of the Route's other Destinations, and so are its Thread replies.
func TestBridgeMarkup(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].typ = delivery.TypeBridge
	e.db.dests[destMM].limit = 600
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	var roots [][]messages.Markup
	var replies []messages.Markup
	r := replyMarkups{stubRenderer: stubRenderer{markups: &roots}, replies: &replies}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Renderer: r})
	e.w.Renderer = r
	e.w.Adapters[delivery.TypeBridge] = e.rec
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	if len(roots) != 1 || !slices.Equal(roots[0], []messages.Markup{messages.MarkupCommonMark, messages.MarkupPlain}) {
		t.Fatalf("root markups %v", roots)
	}
	if calls := e.callsTo(destMM); len(calls) != 1 || calls[0].Method != deliverytest.MethodPublish {
		t.Fatalf("calls to the bridge %+v", calls)
	}
	e.rec.Reset()
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 2,
		Event: groups.EventAckTimeout, Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionAckTimeout}})
	e.round(t)
	calls := e.callsTo(destMM)
	if !slices.Equal(replies, []messages.Markup{messages.MarkupCommonMark}) || len(calls) != 1 ||
		calls[0].Method != deliverytest.MethodReply {
		t.Errorf("reply markups %v, calls to the bridge %+v", replies, calls)
	}
}

// TestSlackMarkup is C-24.FR-7 on delivery: the Root messages of a Slack Destination are rendered in mrkdwn, beside
// the markups of the Route's other Destinations, and so are its Thread replies.
func TestSlackMarkup(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].typ = delivery.TypeSlack
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	var roots [][]messages.Markup
	var replies []messages.Markup
	r := replyMarkups{stubRenderer: stubRenderer{markups: &roots}, replies: &replies}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Renderer: r})
	e.w.Renderer = r
	e.w.Adapters[delivery.TypeSlack] = e.rec
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	if len(roots) != 1 || !slices.Equal(roots[0], []messages.Markup{messages.MarkupMrkdwn, messages.MarkupPlain}) {
		t.Fatalf("root markups %v", roots)
	}
	if calls := e.callsTo(destMM); len(calls) != 1 || calls[0].Method != deliverytest.MethodPublish {
		t.Fatalf("calls to slack %+v", calls)
	}
	e.rec.Reset()
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 2,
		Event: groups.EventAckTimeout, Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionAckTimeout}})
	e.round(t)
	calls := e.callsTo(destMM)
	if !slices.Equal(replies, []messages.Markup{messages.MarkupMrkdwn}) || len(calls) != 1 ||
		calls[0].Method != deliverytest.MethodReply {
		t.Errorf("reply markups %v, calls to slack %+v", replies, calls)
	}
}
