// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// routeTo sets the Destinations of the Route to dests.
func (e *brd) routeTo(route string, dests ...string) {
	e.t.Helper()
	a := e.api.do(http.MethodGet, "/api/v1/routes/"+route, "")
	var rt map[string]any
	decode(e.t, a, &rt)
	body, _ := json.Marshal(map[string]any{"name": rt["name"], "matchers": rt["matchers"], "urgent": rt["urgent"],
		"group_key": rt["group_key"], "destination_ids": dests, "policy": rt["policy"]})
	e.api.json(http.MethodPut, "/api/v1/routes/"+route, string(body), http.StatusOK, "If-Match", a.header.Get("ETag"))
}

// checkEverywhere runs the check of the demo Bridge Connection on every replica. A check reads the bridge's info
// again only through the replica that serves it, and another replica keeps its copy in memory for
// bridge.info_max_age (C-22.FR-2), so a test that changes the fake's info and then waits for a delivery, which any
// replica may claim, checks on each of them.
func (e *brd) checkEverywhere() {
	e.t.Helper()
	for _, r := range e.h.Replicas {
		c := patClient{t: e.t, base: r.App, token: e.api.token}
		if check := c.json(http.MethodPost, "/api/v1/connections/"+e.conn+"/checks", "", http.StatusOK); check["ok"] !=
			true {
			e.t.Fatalf("check on %s = %v", r.App, check)
		}
	}
}

// in are the messages of the fake bridge whose title ends with " "+title in channel.
func (e *brd) in(channel, title string) []fakebridge.Message {
	e.t.Helper()
	return slices.DeleteFunc(e.posted(title), func(m fakebridge.Message) bool { return m.ChannelID != channel })
}

// waitIn waits for the first message of title in channel and returns it.
func (e *brd) waitIn(channel, title string) fakebridge.Message {
	e.t.Helper()
	var got []fakebridge.Message
	eventually(e.t, fmt.Sprintf("a message %q in %s", title, channel), func() bool {
		got = e.in(channel, title)
		return len(got) > 0
	})
	return got[0]
}

// ownerOf is the Owner of a layout as JSON, `null` without one.
func ownerOf(t *testing.T, l *gen.Layout) string {
	t.Helper()
	if l == nil {
		t.Fatal("no layout")
	}
	b, err := json.Marshal(l.Owner)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestBridgeLayout is C-29.AC-6, C-29.FR-1, FR-4, FR-6, FR-8, C-12.FR-1, C-22.FR-4 and FR-21 against `muster dev` and
// the fake bridge: a firing Root message carries `body` and `layout`, whose number is the number alone, whose title has
// no `#`, whose summary keeps `*full*` and `#1` as they are and whose Alerts list five of seven values with the total;
// bob's Ack through the bridge edits it to a layout with the status acknowledged and the Owner bob with the `ref`
// null; a new Root message after the acknowledgement, in a Destination added to the Route, names bob with the `ref`
// of his entry in `mentions`, and through a bridge without `native_mentions` with the `ref` null; a Thread reply
// carries no layout; a body cut to `max_body_length` cuts the layout with it; a Route with its own root_message template
// sends no layout; and `muster bridge check` sends a layout that the fake keeps.
func TestBridgeLayout(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	e.people()
	dest := e.destination("alerts", fakebridge.ChannelAlerts)
	route := e.route("br", "br", []string{dest}, calm)
	e.group("b1", "QueueFull")
	for n := 1; n <= 7; n++ {
		e.alert("b1", fmt.Sprintf("api-%d", n), fmt.Sprintf(`{"team":"br","cluster":"c1","pod":"api-%d",
			"severity":"warning"}`, n), `,"annotations":{"summary":"Queue *full* on #1"}`)
	}
	e.notify("b1", "first notification")
	g := e.groupOf("c1")
	root := e.waitIn(fakebridge.ChannelAlerts, "QueueFull")
	l := root.Envelope.Layout
	if l == nil || l.Number != e.number(g) || l.Title != "QueueFull" || l.Status.Key != gen.LayoutStatusKeyFiring ||
		l.Status.Word != "Firing" || l.Summary != "Queue *full* on #1" || ownerOf(t, l) != "null" {
		t.Fatalf("the layout of a firing Root message %+v", l)
	}
	alerts, err := l.Alerts.Get()
	if err != nil || alerts.Label != "pod" || len(alerts.Values) != 5 || alerts.Total != 7 {
		t.Fatalf("alerts %+v %v", alerts, err)
	}
	if !strings.HasPrefix(root.Envelope.Title, "#") || !strings.Contains(root.Envelope.Body, `Queue \*full\* on #1`) ||
		len(l.Links) == 0 || l.Links[0].Label != "Open in Muster" || len(l.Fields) == 0 {
		t.Fatalf("envelope %+v", root.Envelope)
	}
	t.Logf("firing layout: number %d, title %q, summary %q, alerts %d of %d", l.Number, l.Title, l.Summary,
		len(alerts.Values), alerts.Total)
	e.settled()

	e.byBobPress(root.ID, g)
	var edit gen.MessageContent
	eventually(t, "the edit of the acknowledgement", func() bool {
		m := e.in(fakebridge.ChannelAlerts, "QueueFull")[0]
		if len(m.Edits) == 0 {
			return false
		}
		edit = m.Edits[len(m.Edits)-1]
		return edit.Layout != nil && edit.Layout.Status.Key == gen.LayoutStatusKeyAcknowledged
	})
	if o := ownerOf(t, edit.Layout); o != `{"name":"bob","ref":null}` ||
		!strings.Contains(edit.Body, "Acknowledged by bob") {
		t.Fatalf("the edit: owner %s, body %q", o, edit.Body)
	}
	if slices.ContainsFunc(edit.Layout.Notices, func(n string) bool { return strings.Contains(n, "Acknowledged") }) {
		t.Errorf("the footer of the acknowledgement is a notice %q", edit.Layout.Notices)
	}
	e.settled()

	// A new Root message after the acknowledgement: the Route gains a second Destination of the Connection, where the
	// open Alert Group is published Quietly with bob's marker in the footer.
	prod := e.destination("alerts-prod", fakebridge.ChannelAlertsProd)
	e.routeTo(route, dest, prod)
	again := e.waitIn(fakebridge.ChannelAlertsProd, "QueueFull")
	o, err := again.Envelope.Layout.Owner.Get()
	if err != nil {
		t.Fatalf("no Owner in the new Root message %+v", again.Envelope.Layout)
	}
	ref, err := o.Ref.Get()
	i := slices.IndexFunc(again.Envelope.Mentions, func(m gen.Mention) bool { return m.ExternalId == fakebridge.BobID })
	if err != nil || i < 0 || again.Envelope.Mentions[i].Ref != ref || o.Name != "bob" ||
		!strings.Contains(again.Envelope.Body, "](muster-mention:"+ref+")") || again.Envelope.Notify {
		t.Fatalf("the new Root message: owner %+v, mentions %+v, body %q", o, again.Envelope.Mentions,
			again.Envelope.Body)
	}
	t.Logf("new Root message: owner.ref %q = the ref of %s in mentions", ref, fakebridge.BobID)
	e.settled()

	// Without native_mentions the footer names bob as text, and the Owner has no ref.
	e.configure(`{"capabilities":{"native_mentions":false}}`)
	e.checkEverywhere()
	e.configure(`{"channels":{"ch-third":"third"}}`)
	third := e.destination("alerts-third", "ch-third")
	e.routeTo(route, dest, prod, third)
	plain := e.waitIn("ch-third", "QueueFull")
	if o := ownerOf(t, plain.Envelope.Layout); o != `{"name":"bob","ref":null}` ||
		len(plain.Envelope.Mentions) != 0 {
		t.Fatalf("without native_mentions: owner %s, mentions %+v", o, plain.Envelope.Mentions)
	}
	e.configure(`{"capabilities":` + mustJSON(fakebridge.DefaultConfig().Capabilities) + `}`)
	e.checkEverywhere()
	e.settled()

	// A new Alert is a Thread reply, which carries no layout.
	e.alert("b1", "api-8", `{"team":"br","cluster":"c1","pod":"api-8","severity":"warning"}`, "")
	e.notify("b1", "new alert")
	var replies json.RawMessage
	eventually(t, "a Thread reply", func() bool {
		var raw []struct {
			ID      string          `json:"id"`
			Replies json.RawMessage `json:"replies"`
		}
		decode(t, call(t, http.MethodGet, e.fb+"/messages", ""), &raw)
		for _, m := range raw {
			if m.ID == root.ID && string(m.Replies) != "[]" {
				replies = m.Replies
				return true
			}
		}
		return false
	})
	if strings.Contains(string(replies), `"layout"`) {
		t.Errorf("a Thread reply has a layout: %s", replies)
	}

	// A body cut to max_body_length: the description goes from the body and from the layout.
	e.configure(`{"limits":{"max_body_length":1000,"edit_max_age_seconds":null}}`)
	e.checkEverywhere()
	e.group("l1", "LongQueue")
	for n := 1; n <= 3; n++ {
		e.alert("l1", fmt.Sprintf("l%d", n), fmt.Sprintf(`{"team":"br","cluster":"long","pod":"l%d"}`, n),
			`,"annotations":{"summary":"Queue is long","description":"`+strings.Repeat("d", 1500)+`"}`)
	}
	e.notify("l1", "first notification")
	cut := e.waitIn(fakebridge.ChannelAlerts, "LongQueue")
	cl := cut.Envelope.Layout
	if cl == nil || utf8.RuneCountInString(cut.Envelope.Body) > 1000 || strings.Contains(cut.Envelope.Body, "ddd") ||
		cl.Description != "" || cl.Title != "LongQueue" || len(cl.Fields) == 0 || len(cl.Links) == 0 ||
		(cl.Summary != "") != strings.Contains(cut.Envelope.Body, "Queue is long") ||
		cl.Alerts.IsNull() == strings.Contains(cut.Envelope.Body, "pod: l1") || len(cut.Envelope.Buttons) == 0 {
		t.Fatalf("a cut Root message: layout %+v, body %q", cl, cut.Envelope.Body)
	}
	t.Logf("cut: body %d code points, description %q, summary %q", utf8.RuneCountInString(cut.Envelope.Body),
		cl.Description, cl.Summary)

	// A Route with its own root_message template: the content is the template's, which only body holds, so no layout.
	e.route("tpl", "tpl", []string{dest}, func(p map[string]any) {
		calm(p)
		p["templates"] = map[string]any{"root_message": "Rendered by the template of {{ .AlertGroup.Title }}"}
	})
	e.group("t1", "Templated")
	e.alert("t1", "t1", `{"team":"tpl","cluster":"tpl"}`, "")
	e.notify("t1", "first notification")
	tm := e.waitIn(fakebridge.ChannelAlerts, "Templated")
	if tm.Envelope.Layout != nil || !strings.Contains(tm.Envelope.Body, "Rendered by the template of Templated") {
		t.Fatalf("a Route's own template: layout %+v, body %q", tm.Envelope.Layout, tm.Envelope.Body)
	}

	// muster bridge check sends a layout with its messages, which the fake keeps.
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(devmode.BridgeSecondBotToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := h.runCLIEnv(t, nil, "bridge", "check", h.Fakes.Bridge, "--token-file", token, "--channel",
		fakebridge.ChannelAlerts, "--no-interactive")
	if code != 0 || strings.Contains(stdout, "FAIL") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	checked := slices.DeleteFunc(e.all(), func(m fakebridge.Message) bool { return m.Bot != 2 })
	if len(checked) == 0 || slices.ContainsFunc(checked, func(m fakebridge.Message) bool {
		return m.Envelope.Layout == nil
	}) {
		t.Fatalf("the messages of the check %+v", checked)
	}
}

// byBobPress presses Ack on the message as bob through the fake bridge and waits until the Alert Group g is
// acknowledged by him.
func (e *brd) byBobPress(message, g string) {
	e.t.Helper()
	e.press(message, fakebridge.BobID, "Ack")
	e.byBob(g, "acknowledged", "acknowledged")
}
