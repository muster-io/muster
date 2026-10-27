// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"bytes"
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

// setRenderer renders the Root messages of the stub with the buttons of their status as messages.Renderer lays them
// out, on a Route with the Snooze durations 1 h, 4 h and 24 h, each signed by *key.
type setRenderer struct {
	stubRenderer
	key *string
}

// setDurations are the Snooze durations of the Route of setRenderer.
var setDurations = []int64{3600, 14400, 86400}

func (r setRenderer) Roots(ctx context.Context, db messages.DBTX, g delivery.GroupView, markups []messages.Markup) (
	delivery.Roots, error) {
	out, err := r.stubRenderer.Roots(ctx, db, g, markups)
	if err != nil {
		return out, err
	}
	labels := map[string]string{buttons.CommandAcknowledge: "Ack", buttons.CommandUnacknowledge: "Unack",
		buttons.CommandResolve: "Resolve", buttons.CommandUnsnooze: "Unsnooze"}
	var bs []messages.Button
	for _, b := range buttons.ForStatus(string(g.Status), len(setDurations)) {
		label := labels[b.Command]
		switch {
		case b.Argument == buttons.ArgumentTomorrow:
			label = "Until tomorrow 09:00"
		case b.Argument == buttons.ArgumentWeekStart:
			label = "Until Monday 09:00"
		case b.Command == buttons.CommandSnooze:
			label = fmt.Sprintf("Snooze %d h", setDurations[b.Argument]/3600)
		}
		bs = append(bs, messages.Button{Command: b.Command, Argument: b.Argument, Label: label,
			ActionID: fmt.Sprintf("%s.%d@%s", b.Command, b.Argument, *r.key), KeyID: *r.key})
	}
	for mk, rd := range out.Messages {
		rd.Message.Buttons, rd.KeyID = slices.Clone(bs), *r.key
		out.Messages[mk] = rd
	}
	out.SnoozeSeconds = setDurations
	return out, nil
}

// desiredLabels are the labels of the buttons of the Desired state of the Alert Group in the Destination dest.
func (e *env) desiredLabels(t *testing.T, dest int64) []string {
	t.Helper()
	var m messages.Message
	if err := json.Unmarshal(e.deliveryTo(t, dest).payload, &m); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, b := range m.Buttons {
		out = append(out, b.Label)
	}
	return out
}

// TestButtonSets is C-28.FR-8, FR-9 and AC-7 on delivery: a Mattermost Destination whose own set is Acknowledge,
// "Snooze 24 h" and "Until tomorrow" stores and publishes on a firing Root message exactly Ack, "Snooze 24 h" and
// "Until tomorrow 09:00" in the order of reference.md, on an acknowledged one Unack, "Snooze 24 h" and "Until tomorrow
// 09:00", and on a snoozed one Ack and Unsnooze, which is not part of the set; a second Destination of the Route that
// follows the Organization's set of every button shows every button, with the hash of a Desired state without any
// set. A Reminder in the Thread keeps "Still on it" and "Unack". A change of the Organization's set re-renders, as an
// edit, only the Destinations that follow it, and a re-render that changes no set changes no hash; the re-render after
// a key rotation keeps each Destination's set; a set that shows no button stores no key.
func TestButtonSets(t *testing.T) {
	e := newEnv(t)
	key := "k-1"
	conn := int64(connID)
	e.db.dests[destMM2] = &fakeDest{id: destMM2, publicID: "DSAAAAAAAAAA13", name: "ops-prod",
		typ: delivery.TypeMattermost, connection: &conn, health: "healthy", limit: 600, per: 60}
	e.db.dests[destMM].limit = 600
	e.db.dests[destMM].buttons = []byte(`[{"button": "acknowledge"}, {"button": "snooze", "snooze_seconds": 86400},
		{"button": "snooze_preset", "preset": "tomorrow"}]`)
	e.db.routeDests[routeID] = []int64{destMM, destWH, destMM2}
	r := setRenderer{key: &key}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Log: e.w.Log,
		Renderer: r})
	e.w.Renderer = r

	every := []string{"Ack", "Resolve", "Snooze 1 h", "Snooze 4 h", "Snooze 24 h", "Until tomorrow 09:00",
		"Until Monday 09:00"}
	e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System, created())
	if got := e.desiredLabels(t, destMM); !slices.Equal(got, []string{"Ack", "Snooze 24 h", "Until tomorrow 09:00"}) {
		t.Errorf("firing, its own set: %v", got)
	}
	if got := e.desiredLabels(t, destMM2); !slices.Equal(got, every) {
		t.Errorf("firing, the organization's set: %v", got)
	}
	if e.deliveryTo(t, destMM).buttonKeyID != "k-1" {
		t.Errorf("the key of the shown buttons: %q", e.deliveryTo(t, destMM).buttonKeyID)
	}
	e.round(t)
	for _, c := range e.callsTo(destMM) {
		if c.Method != deliverytest.MethodPublish || len(c.Message.Buttons) != 3 {
			t.Errorf("published to its own set: %+v", c)
		}
	}
	allHash := e.deliveryTo(t, destMM2).hash
	e.db.orgButtons = []byte(`{"all": true}`)
	e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System)
	if !bytes.Equal(e.deliveryTo(t, destMM2).hash, allHash) {
		t.Error("the organization's set of every button changed the hash")
	}

	e.enqueue(t, e.group(groups.StatusAcknowledged, "disk full"), groups.System, groups.Recorded{Seq: 2,
		Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	if got := e.desiredLabels(t, destMM); !slices.Equal(got, []string{"Unack", "Snooze 24 h",
		"Until tomorrow 09:00"}) {
		t.Errorf("acknowledged, its own set: %v", got)
	}
	e.round(t)
	e.rec.Reset()
	e.enqueue(t, e.group(groups.StatusAcknowledged, "disk full"), groups.System, reminder(3))
	e.round(t)
	var reminders int
	for _, c := range e.callsTo(destMM) {
		if c.Method == deliverytest.MethodReply {
			reminders++
			if len(c.Message.Buttons) != 2 || c.Message.Buttons[0].Label != "Still on it" {
				t.Errorf("the reminder in its thread: %+v", c.Message.Buttons)
			}
		}
	}
	if reminders != 1 {
		t.Errorf("%d reminders in its thread", reminders)
	}
	e.enqueue(t, e.group(groups.StatusSnoozed, "disk full"), groups.System)
	if got := e.desiredLabels(t, destMM); !slices.Equal(got, []string{"Ack", "Unsnooze"}) {
		t.Errorf("snoozed, its own set: %v", got)
	}
	e.enqueue(t, e.group(groups.StatusAcknowledged, "disk full"), groups.System)
	e.round(t)

	// The Organization's set changes: only the Destination that follows it is edited.
	e.rec.Reset()
	own := e.deliveryTo(t, destMM).hash
	e.db.orgButtons = []byte(`{"all": false, "items": [{"button": "acknowledge"}, {"button": "snooze",
		"snooze_seconds": 86400}]}`)
	changed := 0
	rerender := func() {
		t.Helper()
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusAcknowledged,
			"disk full"), Actor: groups.System, Changed: func() { changed++ }}); err != nil {
			t.Fatal(err)
		}
	}
	rerender()
	if changed != 1 {
		t.Errorf("the re-render changed %d desired states, want 1", changed)
	}
	if got := e.desiredLabels(t, destMM2); !slices.Equal(got, []string{"Unack", "Snooze 24 h"}) ||
		!bytes.Equal(e.deliveryTo(t, destMM).hash, own) {
		t.Errorf("the organization's set: %v", got)
	}
	e.round(t)
	if calls := e.callsTo(destMM2); len(calls) != 1 || calls[0].Method != deliverytest.MethodUpdate ||
		calls[0].Loudness != groups.Quiet || len(e.callsTo(destMM)) != 0 {
		t.Errorf("the re-render of the organization's set: %+v, %+v", calls, e.callsTo(destMM))
	}
	e.rec.Reset()
	changed = 0
	rerender()
	e.round(t)
	if len(e.rec.Calls()) != 0 || changed != 0 {
		t.Errorf("a second re-render changed %d desired states and called %+v", changed, e.rec.Calls())
	}

	// A key rotation re-renders with each Destination's set.
	key = "k-2"
	if _, err := e.svc.RerenderOpen(t.Context(), "k-2"); err != nil {
		t.Fatal(err)
	}
	if got := e.desiredLabels(t, destMM); !slices.Equal(got, []string{"Unack", "Snooze 24 h", "Until tomorrow 09:00"}) ||
		e.deliveryTo(t, destMM).buttonKeyID != "k-2" {
		t.Errorf("after the rotation, its own set: %v", got)
	}
	if got := e.desiredLabels(t, destMM2); !slices.Equal(got, []string{"Unack", "Snooze 24 h"}) {
		t.Errorf("after the rotation, the organization's set: %v", got)
	}

	// A set that shows no button of the status stores no key.
	e.db.dests[destMM].buttons = []byte(`[{"button": "resolve"}]`)
	e.db.orgButtons = nil
	e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System)
	if got := e.desiredLabels(t, destMM); !slices.Equal(got, []string{"Resolve"}) {
		t.Errorf("resolve only: %v", got)
	}
	e.db.dests[destMM].buttons = []byte(`[]`)
	e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System)
	if got := e.desiredLabels(t, destMM); len(got) != 0 || e.deliveryTo(t, destMM).buttonKeyID != "" ||
		!bytes.Contains(e.deliveryTo(t, destMM).payload, []byte(`"buttons":[]`)) {
		t.Errorf("no button: %v, key %q", got, e.deliveryTo(t, destMM).buttonKeyID)
	}
	e.db.dests[destMM].buttons = []byte(`not json`)
	e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System)
	if got := e.desiredLabels(t, destMM); !slices.Equal(got, every) {
		t.Errorf("a set that cannot be read shows every button: %v", got)
	}
	e.db.dests[destMM].buttons, e.db.orgButtons = nil, []byte(`[]`)
	e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System)
	if got := e.desiredLabels(t, destMM); !slices.Equal(got, every) {
		t.Errorf("an organization's set that cannot be read shows every button: %v", got)
	}
}

// TestQuietConfigurationRerender is ADR-0018 for the re-render without lifecycle events: a change of configuration —
// a changed button set, or a rise that the Route's Minimum severity made — that revives a Not delivered Publication
// which was never published publishes it Quietly, while an Alert that revives it keeps its Loud Publication; and an
// Urgent Alert Group that a Storm holds is released Quietly by a re-render without events.
func TestQuietConfigurationRerender(t *testing.T) {
	notDelivered := func(t *testing.T) *env {
		t.Helper()
		e := newEnv(t)
		e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System, created())
		d := e.only(t)
		if !d.loud.Bool {
			t.Fatalf("the first publication is not loud: %+v", d.loud)
		}
		d.state, d.publishedAt = "not_delivered", nil
		return e
	}
	for _, c := range []struct {
		name   string
		render func(e *env)
		want   groups.Loudness
	}{
		{"a changed button set", func(e *env) {
			e.db.dests[destMM].buttons = []byte(`[{"button": "resolve"}]`)
			e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System)
		}, groups.Quiet},
		{"a raised Minimum severity", func(e *env) {
			g := e.group(groups.StatusFiring, "disk full")
			g.Urgent = true
			e.enqueue(t, g, groups.System, groups.Recorded{Seq: 2, Event: groups.EventUrgencyRaised,
				Variant: groups.VariantRemovesNone, Loudness: groups.Quiet, Reason: groups.ReasonMinimumSeverity})
		}, groups.Quiet},
		{"new Alerts", func(e *env) {
			e.enqueue(t, e.group(groups.StatusFiring, "disk full again"), groups.System, groups.Recorded{Seq: 2,
				Event: groups.EventAlertsAdded, Variant: groups.VariantFiring, Loudness: groups.Loud,
				Mentions: []groups.Mention{groups.MentionNewAlerts}})
		}, groups.Loud},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := notDelivered(t)
			c.render(e)
			if d := e.only(t); d.state != "pending" {
				t.Fatalf("the delivery is %s", d.state)
			}
			e.round(t)
			if p := e.publications(); len(p) != 1 || p[0].Loudness != c.want {
				t.Errorf("published %+v", p)
			}
		})
	}

	e := stormEnv(t)
	r := e.db.routes[routeID]
	r.threshold = 2
	e.db.routes[routeID] = r
	id := int64(5000)
	for range 4 {
		id++
		e.addGroup(t, id, false)
	}
	e.onlyStorm(t)
	e.round(t)
	e.rec.Reset()
	g := e.view(id, groups.StatusFiring)
	g.Urgent = true
	e.db.groups[id].urgent = true
	e.enqueue(t, g, groups.System)
	e.round(t)
	if p := e.publications(); len(p) != 1 || p[0].Loudness != groups.Quiet ||
		!strings.HasPrefix(textOf(p[0]), fmt.Sprintf("#%d ", id)) {
		t.Errorf("released without events %+v", p)
	}
}

// TestJoinedDestinationButtonSet is C-28.FR-8 for a Destination added to a Route: the Route's open Alert Groups are
// published into it with its own button set, and into a second added Destination that follows the Organization with
// every button.
func TestJoinedDestinationButtonSet(t *testing.T) {
	e := newEnv(t)
	key := "k-1"
	r := setRenderer{key: &key}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Log: e.w.Log,
		Renderer: r})
	e.w.Renderer = r
	e.db.routeDests[routeID] = nil
	e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System, created())
	conn := int64(connID)
	e.db.dests[destMM2] = &fakeDest{id: destMM2, publicID: "DSAAAAAAAAAA13", name: "ops-prod",
		typ: delivery.TypeMattermost, connection: &conn, health: "healthy", limit: 600, per: 60}
	e.db.dests[destMM].buttons = []byte(`[{"button": "acknowledge"}, {"button": "snooze", "snooze_seconds": 3600}]`)
	e.db.routeDests[routeID] = []int64{destMM, destMM2}
	e.changed(t, routeID, []int64{destMM, destMM2}, nil)
	if got := e.desiredLabels(t, destMM); !slices.Equal(got, []string{"Ack", "Snooze 1 h"}) {
		t.Errorf("the added destination with its own set: %v", got)
	}
	if got := e.desiredLabels(t, destMM2); len(got) != 7 {
		t.Errorf("the added destination that follows the organization: %v", got)
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
