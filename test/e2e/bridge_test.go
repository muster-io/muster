// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// brd drives Bridge Destinations: the mm environment — an Admin's Personal access token, the Integration "lab" and
// the fake Alertmanager — with the demo Bridge Connection "Example Bridge" of the fake bridge, whose configuration is
// restored when the test ends: replays on, the default capabilities, no edit window and no push target.
type brd struct {
	*mm
	fb   string
	conn string
}

func newBRD(t *testing.T, h *Harness, r *Replica) *brd {
	t.Helper()
	e := &brd{mm: newMM(t, h, r), fb: h.Fakes.Bridge + "/_fake"}
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
		if c := item.(map[string]any); c["name"] == devmode.BridgeConnectionName {
			e.conn = c["id"].(string)
		}
	}
	if e.conn == "" {
		t.Fatal("no demo Bridge Connection")
	}
	capabilities, _ := json.Marshal(fakebridge.DefaultConfig().Capabilities)
	t.Cleanup(func() {
		restore(t, http.MethodPut, e.fb+"/config", `{"replays":true,"push":null,"capabilities":`+string(capabilities)+
			`,"limits":{"max_body_length":4000,"edit_max_age_seconds":null}}`)
		restore(t, http.MethodDelete, e.fb+"/faults", "")
		// The second request may find the first's connection still busy and dial another, which then stays unused;
		// the fake's server would wait 5 s for such a connection when muster dev stops.
		client.CloseIdleConnections()
	})
	return e
}

// restore sends a request of a cleanup, after the test's context ended, while the fake still runs.
func restore(t *testing.T, method, url, body string) {
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	if err != nil {
		t.Error(err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("%s %s: %v", method, url, err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Errorf("%s %s = %d", method, url, resp.StatusCode)
	}
}

// destination creates a Bridge Destination on channel, which mentions nobody.
func (e *brd) destination(name, channel string) string {
	e.t.Helper()
	created := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":%q,
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":30,"per_seconds":60}}`, name, e.conn,
		channel, mentionSettings(nil)), http.StatusCreated)
	return created["destination"].(map[string]any)["id"].(string)
}

// configure changes the fake bridge's configuration by patch.
func (e *brd) configure(patch string) {
	e.t.Helper()
	e.fake(http.MethodPut, e.fb+"/config", patch)
}

func (e *brd) bridgeFault(f fakeserver.Fault) {
	e.t.Helper()
	b, _ := json.Marshal(f)
	if a := call(e.t, http.MethodPost, e.fb+"/faults", string(b)); a.status != http.StatusNoContent {
		e.t.Fatalf("fault %s = %d %s", b, a.status, a.body)
	}
}

// problemFault answers the requests to path with status and a problem of class, once.
func problemFault(path string, status int, class, detail string) fakeserver.Fault {
	b, _ := json.Marshal(map[string]any{"status": status, "class": class, "detail": detail})
	return fakeserver.Fault{Path: path, Status: status, ContentType: "application/problem+json", Body: string(b),
		Times: 1}
}

// posted are the messages the fake bridge holds whose title ends with " "+title, in the order they were posted,
// deleted ones included.
func (e *brd) posted(title string) []fakebridge.Message {
	e.t.Helper()
	var all []fakebridge.Message
	decode(e.t, call(e.t, http.MethodGet, e.fb+"/messages", ""), &all)
	return slices.DeleteFunc(all, func(m fakebridge.Message) bool {
		return !strings.HasSuffix(m.Envelope.Title, " "+title)
	})
}

// all are the messages the fake bridge holds, in the order they were posted, deleted ones included.
func (e *brd) all() []fakebridge.Message {
	e.t.Helper()
	var all []fakebridge.Message
	decode(e.t, call(e.t, http.MethodGet, e.fb+"/messages", ""), &all)
	return all
}

// testMessages are the test messages the fake bridge holds.
func (e *brd) testMessages() []fakebridge.Message {
	e.t.Helper()
	return slices.DeleteFunc(e.all(), func(m fakebridge.Message) bool { return m.Envelope.Kind != "test" })
}

// waitPosted waits for n messages of title and returns them.
func (e *brd) waitPosted(title string, n int) []fakebridge.Message {
	e.t.Helper()
	var got []fakebridge.Message
	eventually(e.t, fmt.Sprintf("%d messages %q", n, title), func() bool {
		got = e.posted(title)
		return len(got) >= n
	})
	if len(got) != n {
		e.t.Fatalf("%d messages %q, want %d", len(got), title, n)
	}
	return got
}

// bridgeCalls are the requests the fake bridge recorded by method and path.
func (e *brd) bridgeCalls(method, path string) []fakeserver.Request {
	e.t.Helper()
	var all []fakeserver.Request
	decode(e.t, call(e.t, http.MethodGet, e.fb+"/requests", ""), &all)
	return slices.DeleteFunc(all, func(r fakeserver.Request) bool { return r.Method != method || r.Path != path })
}

// settled waits until no delivery is pending.
func (e *brd) settled() {
	e.t.Helper()
	eventually(e.t, "the deliveries to settle", func() bool {
		return e.h.count(e.t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0
	})
}

// start starts the Alert Group of the Alert name of cluster under the Alertmanager group of alertname, through the
// Route of team, and returns its id.
func (e *brd) start(group, alertname, team, cluster string) string {
	e.t.Helper()
	e.group(group, alertname)
	e.alert(group, cluster, fmt.Sprintf(`{"team":%q,"cluster":%q}`, team, cluster), "")
	e.notify(group, "first notification")
	return e.groupOf(cluster)
}

// TestBridgeReplaysOff is the rest of C-22.AC-6 against `muster dev` and the fake bridge: with replays turned off in
// the fake, a Publication whose answer is lost — the fake posts and holds the answer past the client's timeout — is
// retried with the same Idempotency-Key, which posts a second message, and the outcome records a possible duplicate as
// a delivery event.
func TestBridgeReplaysOff(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	dest := e.destination("alerts", "ch-alerts")
	e.route("replays", "replays", []string{dest}, nil)
	e.configure(`{"replays":false}`)
	e.bridgeFault(fakeserver.Fault{Path: "/v1/messages", DelayMs: 12_000, Times: 1})

	g := e.start("p1", "Replays", "replays", "replays")
	e.waitPosted("Replays", 1)
	// The client gives up after 10 s of real time; the outcome keeps the start of the Publication.
	eventually(t, "the unanswered attempt", func() bool {
		return h.count(t, `SELECT count(*) FROM deliveries WHERE attempts = 1 AND last_error_class = 'transient'
			AND publication_started_at IS NOT NULL AND message_id IS NULL`) == 1
	})
	if ev := deliveryEvents(e.timeline(g, "")); slices.Contains(ev, "possible_duplicate") {
		t.Fatalf("a possible duplicate before the retry: %v", ev)
	}
	advance(t, r, 3)
	posted := e.waitPosted("Replays", 2)
	posts := e.bridgeCalls(http.MethodPost, "/v1/messages")
	if len(posts) != 2 || posts[0].Headers["Idempotency-Key"][0] != posts[1].Headers["Idempotency-Key"][0] ||
		!strings.HasPrefix(posts[0].Headers["Idempotency-Key"][0], "pub-"+dest+"-") {
		t.Fatalf("posts %+v", posts)
	}
	eventually(t, "the possible duplicate", func() bool {
		return slices.Contains(deliveryEvents(e.timeline(g, "")), "possible_duplicate")
	})
	e.settled()
	if n := h.count(t, fmt.Sprintf(`SELECT count(*) FROM deliveries WHERE possible_duplicate AND message_id = '%s'`,
		posted[1].ID)); n != 1 {
		t.Errorf("%d deliveries record the possible duplicate on %s", n, posted[1].ID)
	}
	if ev := deliveryEvents(e.timeline(g, "")); countOf(ev, "possible_duplicate") != 1 || countOf(ev,
		"publication") != 1 {
		t.Errorf("delivery events %v", ev)
	}
}

// countOf is how often s is in list.
func countOf(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

// TestBridgeTransientInvalidGone is the rest of C-22.AC-7 against `muster dev` and the fake bridge: a transient
// problem on an edit is retried after the Transient backoff, an invalid one ends the delivery as Not delivered and
// both leave the Destination healthy; gone on an edit and gone on a Thread reply each start the deleted Root message
// flow once.
func TestBridgeTransientInvalidGone(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	// Without deletion events, so that only the gone answers find the deleted messages.
	e.deletionEvents(false)
	dest := e.destination("alerts", "ch-alerts")
	e.route("classes", "classes", []string{dest}, nil)

	g := e.start("c1", "Classes", "classes", "classes")
	root := e.waitPosted("Classes", 1)[0]
	e.settled()
	patch := "/v1/messages/" + root.ID

	// transient: retried after the backoff; the Destination stays healthy.
	e.bridgeFault(problemFault("/v1/messages/*", http.StatusInternalServerError, "transient", "Try again later."))
	e.command(g, "acknowledge")
	eventually(t, "the transient edit", func() bool {
		return h.count(t, `SELECT count(*) FROM deliveries WHERE attempts = 1 AND last_error_class = 'transient'`) == 1
	})
	advance(t, r, 3)
	eventually(t, "the edit after the backoff", func() bool {
		edits := e.posted("Classes")[0].Edits
		return len(edits) == 1 && edits[0].Status == "acknowledged"
	})
	e.settled()
	if p := e.bridgeCalls(http.MethodPatch, patch); len(p) != 2 || p[0].Status != http.StatusInternalServerError ||
		p[1].Status != http.StatusOK {
		t.Fatalf("edits %+v", p)
	}
	if hl := e.health(dest); hl.State != "healthy" {
		t.Errorf("health after transient %+v", hl)
	}

	// invalid: Not delivered, the Destination healthy.
	e.bridgeFault(problemFault("/v1/messages/*", http.StatusBadRequest, "invalid", "The envelope is not valid."))
	e.command(g, "unacknowledge")
	eventually(t, "Not delivered", func() bool {
		return h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'not_delivered'
			AND last_error = 'The envelope is not valid.'`) == 1
	})
	if ev := deliveryEvents(e.timeline(g, "")); !slices.Contains(ev, "not_delivered") {
		t.Errorf("delivery events %v", ev)
	}
	if hl := e.health(dest); hl.State != "healthy" {
		t.Errorf("health after invalid %+v", hl)
	}

	// gone on an edit: the message deleted in the messenger is published again once, Quietly, with the note.
	gd := e.start("c2", "Deleted", "classes", "deleted")
	old := e.waitPosted("Deleted", 1)[0]
	e.settled()
	e.fake(http.MethodDelete, e.fb+"/messages/"+old.ID, "")
	e.command(gd, "acknowledge")
	e.waitPosted("Deleted", 2)
	e.settled()
	if p := e.bridgeCalls(http.MethodPatch, "/v1/messages/"+old.ID); len(p) != 1 || p[0].Status != http.StatusNotFound {
		t.Errorf("the edit of the deleted message %+v", p)
	}
	if again := e.posted("Deleted"); len(again) != 2 || !again[0].Deleted || again[1].Envelope.Notify ||
		!deletedNote.MatchString(again[1].Envelope.Body) {
		t.Errorf("after gone on the edit: %+v", again)
	}
	if ev := deliveryEvents(e.timeline(gd, "")); countOf(ev, "republished") != 1 {
		t.Errorf("delivery events after gone on the edit %v", ev)
	}

	// gone on a Thread reply: the edit for the new Alert is answered as made, so the reply meets the deleted message.
	gr := e.start("c3", "Gone", "classes", "gone")
	old = e.waitPosted("Gone", 1)[0]
	e.settled()
	e.fake(http.MethodDelete, e.fb+"/messages/"+old.ID, "")
	e.bridgeFault(fakeserver.Fault{Path: "/v1/messages/" + old.ID, Status: http.StatusOK,
		ContentType: "application/json", Body: `{"truncated":false}`, Times: 1})
	e.alert("c3", "gone2", `{"team":"classes","cluster":"gone","disk":"g2"}`, "")
	e.notify("c3", "new alerts added")
	e.waitPosted("Gone", 2)
	e.settled()
	replies := e.bridgeCalls(http.MethodPost, "/v1/messages/"+old.ID+"/thread-replies")
	if len(replies) != 1 || replies[0].Status != http.StatusNotFound {
		t.Errorf("replies to the deleted message %+v", replies)
	}
	if again := e.posted("Gone"); len(again) != 2 || again[1].Envelope.Notify ||
		!deletedNote.MatchString(again[1].Envelope.Body) {
		t.Errorf("after gone on the reply: %+v", again)
	}
	if ev := deliveryEvents(e.timeline(gr, "")); countOf(ev, "republished") != 1 {
		t.Errorf("delivery events after gone on the reply %v", ev)
	}
	if hl := e.health(dest); hl.State != "healthy" {
		t.Errorf("health after gone %+v", hl)
	}
}

// TestBridgeBodyLimit is C-22.AC-10 against `muster dev` and the fake bridge: with max_body_length 1,000 read again by
// the Connection check, an Alert Group of 40 Alerts with four long labels that differ per Alert, about 1,450 code
// points laid out in full, posts a body of at most 1,000 code points with the link to Muster, shortened down to its
// distinct values, and keeps its title, link and buttons in the envelope.
func TestBridgeBodyLimit(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	dest := e.destination("alerts", "ch-alerts")
	e.route("long", "long", []string{dest}, nil)
	e.configure(`{"limits":{"max_body_length":1000,"edit_max_age_seconds":null}}`)
	if check := e.api.json(http.MethodPost, "/api/v1/connections/"+e.conn+"/checks", "", http.StatusOK); check["ok"] !=
		true {
		t.Fatalf("check = %v", check)
	}
	e.group("l1", "Many")
	for i := 1; i <= 40; i++ {
		e.alert("l1", fmt.Sprintf("m%d", i), fmt.Sprintf(`{"team":"long","cluster":"many","pod":"m%d",
			"instance":"node-%d.region-a.example.org:9100","host":"host-%d.rack-b.datacenter-c.example.org",
			"disk":"/dev/disk/by-id/nvme-sample-serial-0000%d"}`, i, i, i, i), "")
	}
	e.notify("l1", "first notification")
	m := e.waitPosted("Many", 1)[0]
	body := m.Envelope.Body
	if n := utf8.RuneCountInString(body); n > 1000 || m.Truncated {
		t.Fatalf("the body has %d code points, truncated %v", n, m.Truncated)
	}
	if !strings.Contains(body, "Open in Muster") || strings.Count(body, "\n- ") >= 40 {
		t.Errorf("body %q", body)
	}
	// Laid out in full, each of the four labels lists ten values and "+30 more"; only shortening removes them.
	if strings.Contains(body, "+30 more") {
		t.Errorf("the body was not shortened: %q", body)
	}
	if !strings.HasPrefix(m.Envelope.Title, "#") || m.Envelope.Link.Label != "Open in Muster" ||
		!strings.Contains(m.Envelope.Link.Url, "/alert-groups/") || len(m.Envelope.Buttons) == 0 {
		t.Errorf("envelope %+v", m.Envelope)
	}
	t.Logf("40 alerts: the body has %d code points", utf8.RuneCountInString(body))
}

// windowed creates the Bridge Destination "edits" on the channel alerts-prod and the Route of team to it, without ack
// timeout and Reminders so that only the test changes its Alert Groups, and makes the fake bridge declare an edit
// window of 600 s, which the Connection check reads again; it returns the Destination and the Route.
func (e *brd) windowed(team string) (string, string) {
	e.t.Helper()
	dest := e.destination("edits", fakebridge.ChannelAlertsProd)
	route := e.route(team, team, []string{dest}, func(p map[string]any) {
		p["ack_timeout"].(map[string]any)["enabled"] = false
		p["reminders"].(map[string]any)["enabled"] = false
	})
	e.configure(`{"limits":{"max_body_length":4000,"edit_max_age_seconds":600}}`)
	if check := e.api.json(http.MethodPost, "/api/v1/connections/"+e.conn+"/checks", "", http.StatusOK); check["ok"] !=
		true {
		e.t.Fatalf("check = %v", check)
	}
	return dest, route
}

// offRoute takes every Destination off the Route.
func (e *brd) offRoute(route string) {
	e.t.Helper()
	a := e.api.do(http.MethodGet, "/api/v1/routes/"+route, "")
	var rt map[string]any
	decode(e.t, a, &rt)
	body, _ := json.Marshal(map[string]any{"name": rt["name"], "matchers": rt["matchers"], "urgent": rt["urgent"],
		"group_key": rt["group_key"], "destination_ids": []string{}, "policy": rt["policy"]})
	e.api.json(http.MethodPut, "/api/v1/routes/"+route, string(body), http.StatusOK, "If-Match", a.header.Get("ETag"))
}

// deliveryID is the id of the delivery of the Alert Group g to the Destination dest.
func (e *brd) deliveryID(g, dest string) int64 {
	e.t.Helper()
	return e.h.count(e.t, fmt.Sprintf(`SELECT d.id FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id
		JOIN alert_groups ag ON ag.id = d.alert_group_id WHERE ds.public_id = '%s' AND ag.public_id = '%s'`, dest, g))
}

// keyOf is the Idempotency-Key of a recorded request.
func keyOf(r fakeserver.Request) string {
	if k := r.Headers["Idempotency-Key"]; len(k) == 1 {
		return k[0]
	}
	return ""
}

// TestBridgeFinalEditPastWindow is the final edit of C-22.FR-9 against `muster dev` and the fake bridge: with an edit
// window of 600 s, once the development clock is 11 minutes past the Publication, the Destination leaving the Route
// makes no edit of the Root message but replies under it, Quietly, with the final text and the key
// final-<Destination>-<delivery id>; the delivery is retired, and the Timeline shows the final edit.
func TestBridgeFinalEditPastWindow(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	dest, route := e.windowed("final")

	g := e.start("f1", "Final", "final", "final")
	root := e.waitPosted("Final", 1)[0]
	e.settled()
	advance(t, r, 660)
	e.offRoute(route)
	eventually(t, "the final edit as a Thread reply", func() bool { return len(e.posted("Final")[0].Replies) == 1 })
	e.settled()

	got := e.posted("Final")
	if len(got) != 1 || len(got[0].Edits) != 0 {
		t.Fatalf("after the Destination left: %+v", got)
	}
	if reply := got[0].Replies[0].Input; !strings.Contains(reply.Body, "No longer updated here") || reply.Notify {
		t.Errorf("the final reply %+v", reply)
	}
	if p := e.bridgeCalls(http.MethodPatch, "/v1/messages/"+root.ID); len(p) != 0 {
		t.Errorf("edits past the window %+v", p)
	}
	key := fmt.Sprintf("final-%s-%d", dest, e.deliveryID(g, dest))
	if replies := e.bridgeCalls(http.MethodPost, "/v1/messages/"+root.ID+"/thread-replies"); len(replies) != 1 ||
		keyOf(replies[0]) != key {
		t.Errorf("replies %+v, want one with the key %s", replies, key)
	}
	if n := h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'retired'`); n != 1 {
		t.Errorf("%d retired deliveries", n)
	}
	if ev := deliveryEvents(e.timeline(g, "")); countOf(ev, "final_edit") != 1 {
		t.Errorf("delivery events %v", ev)
	}
}

// TestBridgePointerFails is the pointer reply of C-22.FR-9 against `muster dev` and the fake bridge: with an edit
// window of 600 s, Acknowledge 11 minutes past the Publication posts a new Root message Quietly with the key of the
// second Publication, and the pointer reply under the old one, refused as invalid, is sent once and never retried:
// the delivery event republished_past_edit_window carries its error, the old id is kept in superseded_message_ids,
// and the next Thread reply goes under the new Root message.
func TestBridgePointerFails(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	dest, _ := e.windowed("pointer")

	g := e.start("w1", "Pointer", "pointer", "pointer")
	old := e.waitPosted("Pointer", 1)[0]
	e.settled()
	advance(t, r, 660)
	replies := "/v1/messages/" + old.ID + "/thread-replies"
	e.bridgeFault(problemFault(replies, http.StatusBadRequest, "invalid", "The reply is not valid."))
	e.command(g, "acknowledge")
	got := e.waitPosted("Pointer", 2)
	e.settled()
	advance(t, r, 120)
	e.settled()

	id := e.deliveryID(g, dest)
	if cur := got[1]; cur.Envelope.Notify || cur.Envelope.Status != "acknowledged" || got[0].Deleted {
		t.Errorf("the new Root message %+v", cur.Envelope)
	}
	if posts := e.bridgeCalls(http.MethodPost, "/v1/messages"); len(posts) != 2 ||
		keyOf(posts[1]) != fmt.Sprintf("pub-%s-%d-2", dest, id) {
		t.Errorf("posts %+v", posts)
	}
	pointer := e.bridgeCalls(http.MethodPost, replies)
	if len(pointer) != 1 || pointer[0].Status != http.StatusBadRequest ||
		keyOf(pointer[0]) != fmt.Sprintf("pointer-%s-%d-1", dest, id) {
		t.Errorf("pointer replies %+v", pointer)
	}
	if n := h.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'republished_past_edit_window'
		AND loudness = 'quiet' AND error_class = 'unknown' AND error = 'The reply is not valid.'
		AND detail->>'pointer' = 'failed' AND detail->>'old_message_id' = '`+old.ID+`'
		AND detail->>'new_message_id' = '`+got[1].ID+`'`); n != 1 {
		t.Errorf("%d republished_past_edit_window events with the pointer's error", n)
	}
	if n := h.count(t, fmt.Sprintf(`SELECT count(*) FROM deliveries WHERE id = %d AND state = 'delivered'
		AND NOT past_edit_window AND publications = 2 AND message_id = '%s' AND actual_status = 'acknowledged'
		AND superseded_message_ids = ARRAY['%s']`, id, got[1].ID, old.ID)); n != 1 {
		t.Errorf("the delivery after the republication: %s", h.text(t, fmt.Sprintf(`SELECT row_to_json(d)::text
			FROM deliveries d WHERE id = %d`, id)))
	}
	if ev := deliveryEvents(e.timeline(g, "")); countOf(ev, "republished_past_edit_window") != 1 {
		t.Errorf("delivery events %v", ev)
	}
	eventually(t, "the bridge_republished_past_edit_window line", func() bool {
		for _, rep := range h.Replicas {
			if line, _, ok := rep.LogLine("bridge_republished_past_edit_window"); ok &&
				line["old_message_id"] == old.ID && line["new_message_id"] == got[1].ID && line["destination"] == dest &&
				line["group"] == g {
				return true
			}
		}
		return false
	})

	e.alert("w1", "pointer2", `{"team":"pointer","cluster":"pointer","pod":"p2"}`, "")
	e.notify("w1", "new alerts added")
	advance(t, r, 61)
	eventually(t, "the Thread reply under the new Root message", func() bool {
		return len(e.posted("Pointer")[1].Replies) == 1
	})
	e.settled()
	if now := e.posted("Pointer"); len(now) != 2 || len(now[0].Replies) != 0 {
		t.Errorf("after the next Thread reply: %+v", now)
	}
	if n := len(e.bridgeCalls(http.MethodPost, replies)); n != 1 {
		t.Errorf("%d requests to the old Thread", n)
	}
}

// deletionEvents switches the fake bridge's deletion events on or off, keeping its other capabilities.
func (e *brd) deletionEvents(on bool) {
	e.t.Helper()
	c := fakebridge.DefaultConfig().Capabilities
	c.DeletionEvents = &on
	b, _ := json.Marshal(c)
	e.configure(`{"capabilities":` + string(b) + `}`)
}

// The answers of Muster to the people behind Bridge events, as reference.md and the texts of internal/messages give
// them.
const (
	textBridgeNotLinked = "Your account is not linked to Muster. Link it in your profile: " + devmode.PublicURL +
		"/profile"
	textTestPress = "This is a test message; nothing was changed"
)

// people creates the Responder bob and the Viewer vera and links their accounts u-bob and u-vera of the fake bridge
// to them through Example Bridge, directly in the database as the Verification of S-075 does; alice stays unlinked.
func (e *brd) people() {
	e.t.Helper()
	for _, p := range []struct{ login, role, external, link string }{{"bob", "responder", fakebridge.BobID,
		"AK0000000000B7"}, {"vera", "viewer", fakebridge.VeraID, "AK0000000000V7"}} {
		e.api.json(http.MethodPost, "/api/v1/users", `{"name":"`+p.login+`","login":"`+p.login+`","role":"`+p.role+`"}`,
			http.StatusCreated)
		e.h.exec(e.t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
			display_name, created_at) SELECT 1, '`+p.link+`', u.id, 'bridge', c.id, '`+p.external+`', u.name, now()
			FROM users u, connections c WHERE u.login = '`+p.login+`' AND c.public_id = '`+e.conn+`'`)
	}
	if n := e.h.count(e.t, `SELECT count(DISTINCT identity_space) FROM account_links
		WHERE messenger = 'bridge' AND identity_space = 'bridge:' || connection_id`); n != 1 {
		e.t.Fatalf("%d identity spaces of the Bridge links", n)
	}
}

// calm is a policy without ack timeout and Reminders, so that only the test changes its Alert Groups.
func calm(p map[string]any) {
	p["ack_timeout"].(map[string]any)["enabled"] = false
	p["reminders"].(map[string]any)["enabled"] = false
}

// event sends a control request of the fake bridge that queues an event and returns the event's id.
func (e *brd) event(path string, body map[string]any) string {
	e.t.Helper()
	b, _ := json.Marshal(body)
	a := call(e.t, http.MethodPost, e.fb+path, string(b))
	if a.status != http.StatusOK {
		e.t.Fatalf("POST %s %s = %d %s", path, b, a.status, a.body)
	}
	var ev struct {
		Event struct {
			ID string `json:"id"`
		} `json:"event"`
	}
	decode(e.t, a, &ev)
	return ev.Event.ID
}

// press presses the button label of the message as the person user in the fake bridge and returns the event's id.
func (e *brd) press(message, user, label string) string {
	e.t.Helper()
	return e.event("/press", map[string]any{"message_id": message, "user_id": user, "button": label})
}

// say writes text in the Thread of the message as the person user in the fake bridge and returns the event's id.
func (e *brd) say(message, user, text string) string {
	e.t.Helper()
	return e.event("/thread-message", map[string]any{"message_id": message, "user_id": user, "text": text})
}

// events are the events of the fake bridge with their bots.
func (e *brd) events() []fakebridge.Event {
	e.t.Helper()
	var all []fakebridge.Event
	decode(e.t, call(e.t, http.MethodGet, e.fb+"/events", ""), &all)
	return all
}

// eventOf is the event id of the fake bridge as the bridge sends it, and its index among the events of its bot.
func (e *brd) eventOf(id string) (json.RawMessage, int) {
	e.t.Helper()
	index := map[int]int{}
	for _, ev := range e.events() {
		var head struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(ev.Event, &head)
		if head.ID == id {
			return ev.Event, index[ev.Bot]
		}
		index[ev.Bot]++
	}
	e.t.Fatalf("no event %s in the fake bridge", id)
	return nil, 0
}

// lastEvent is the id of the newest event of the fake bridge.
func (e *brd) lastEvent() string {
	e.t.Helper()
	all := e.events()
	if len(all) == 0 {
		e.t.Fatal("no event in the fake bridge")
	}
	var head struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(all[len(all)-1].Event, &head)
	return head.ID
}

// answers are the answers Muster made to the event id through the fake bridge.
func (e *brd) answers(id string) []fakebridge.Answer {
	e.t.Helper()
	var all []fakebridge.Answer
	decode(e.t, call(e.t, http.MethodGet, e.fb+"/answers", ""), &all)
	return slices.DeleteFunc(all, func(a fakebridge.Answer) bool { return a.EventID != id })
}

// answer waits for the answer to the event id and returns it; there must be exactly one.
func (e *brd) answer(id string) fakebridge.Answer {
	e.t.Helper()
	var got []fakebridge.Answer
	eventually(e.t, "the answer to "+id, func() bool {
		got = e.answers(id)
		return len(got) > 0
	})
	if len(got) != 1 {
		e.t.Fatalf("answers to %s: %+v", id, got)
	}
	return got[0]
}

// handled waits until Muster has handled the event id that Example Bridge polls: its id is recorded and the stored
// cursor is past it, which the poller stores only once it answered the event; an event without an answer has none then.
func (e *brd) handled(id string) {
	e.t.Helper()
	_, index := e.eventOf(id)
	eventually(e.t, "the handling of "+id, func() bool {
		return e.h.count(e.t, `SELECT count(*) FROM bridge_events_seen WHERE event_id = '`+id+`'`) == 1 &&
			e.h.count(e.t, `SELECT coalesce(bridge_events_cursor, '0')::bigint FROM connections
				WHERE public_id = '`+e.conn+`'`) > int64(index)
	})
}

// unanswered handles the event id and checks that it is not answered.
func (e *brd) unanswered(id string) {
	e.t.Helper()
	e.handled(id)
	if got := e.answers(id); len(got) != 0 {
		e.t.Errorf("answers to %s: %+v", id, got)
	}
}

// lines are the log lines of the event on every replica whose fields hold want.
func (e *brd) lines(event string, want map[string]any) []map[string]any {
	var out []map[string]any
	for _, r := range e.h.Replicas {
		for _, line := range logLines(r, event) {
			if func() bool {
				for k, v := range want {
					if fmt.Sprint(line[k]) != fmt.Sprint(v) {
						return false
					}
				}
				return true
			}() {
				out = append(out, line)
			}
		}
	}
	return out
}

// waitLine waits for a log line of the event whose fields hold want.
func (e *brd) waitLine(event string, want map[string]any) {
	e.t.Helper()
	eventually(e.t, fmt.Sprintf("the line %s %v", event, want), func() bool { return len(e.lines(event, want)) > 0 })
}

// status is the status of the Alert Group g.
func (e *brd) status(g string) string {
	e.t.Helper()
	return fmt.Sprint(e.alertGroup(g)["status"])
}

// byBob waits until the Alert Group g has the status and its last change, the event, was made by bob through the
// bridge.
func (e *brd) byBob(g, status, event string) {
	e.t.Helper()
	eventually(e.t, g+" "+status, func() bool { return e.status(g) == status })
	tl := e.timeline(g, "&kind=status")
	if len(tl) == 0 || tl[0].Event != event || tl[0].Actor.Transport != "bridge" || tl[0].Actor.Name != "bob" {
		e.t.Errorf("the last status change of %s: %+v", g, tl)
	}
}

// latest are the buttons of the latest content of the message: its last edit, or what was posted.
func latest(m fakebridge.Message) []string {
	buttons := m.Envelope.Buttons
	if len(m.Edits) > 0 {
		buttons = m.Edits[len(m.Edits)-1].Buttons
	}
	var labels []string
	for _, b := range buttons {
		labels = append(labels, b.Label)
	}
	return labels
}

// dataOf is the data of the button label in what message posted.
func dataOf(t *testing.T, m fakebridge.Message, label string) string {
	t.Helper()
	for _, b := range m.Envelope.Buttons {
		if b.Label == label {
			return b.Data
		}
	}
	t.Fatalf("no button %q on %s: %+v", label, m.ID, m.Envelope.Buttons)
	return ""
}

// bridgeCommands is the sum of muster_commands_total with the Transport bridge over the replicas.
func (e *brd) bridgeCommands() float64 {
	e.t.Helper()
	sum := 0.0
	for _, r := range e.h.Replicas {
		for line := range strings.Lines(r.metrics(e.t)) {
			if strings.HasPrefix(line, "muster_commands_total{") && strings.Contains(line, `transport="bridge"`) {
				var v float64
				fields := strings.Fields(line)
				_, _ = fmt.Sscan(fields[len(fields)-1], &v)
				sum += v
			}
		}
	}
	return sum
}

// TestBridgePresses is C-22.AC-13, AC-15, AC-12 and FR-24 against `muster dev` and the fake bridge, polled by the
// Leader: bob's press acknowledges with the Transport bridge in the Timeline and the Audit log, is not answered and
// moves the stored cursor; an unlinked account is answered with the profile link; tampered data and data of another
// message are answered "not verified"; a press older than bridge.event_max_age and a press on a message Muster did not
// post are dropped unanswered; a press on a test message is answered and changes nothing; muster_commands_total counts
// transport="bridge"; and past the edit window, a press on the older Root message of a republished delivery runs its
// Command while only the current Root message is edited.
func TestBridgePresses(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	e.people()
	dest := e.destination("alerts", fakebridge.ChannelAlerts)
	e.route("presses", "presses", []string{dest}, calm)
	g := e.start("p1", "Presses", "presses", "presses")
	root := e.waitPosted("Presses", 1)[0]
	other := e.start("p2", "Other", "presses", "other")
	e.waitPosted("Other", 1)
	e.settled()

	ack := e.press(root.ID, fakebridge.BobID, "Ack")
	e.byBob(g, "acknowledged", "acknowledged")
	e.unanswered(ack)
	if n := h.count(t, `SELECT count(*) FROM audit_log WHERE transport = 'bridge' AND actor_kind = 'user'
		AND action = 'alert_group.acknowledged' AND resource_public_id = '`+g+`'`); n != 1 {
		t.Errorf("%d Audit log entries of the acknowledgement through the bridge", n)
	}
	if n := h.count(t, `SELECT count(*) FROM connections WHERE public_id = '`+e.conn+`'
		AND bridge_events_cursor IS NOT NULL`); n != 1 {
		t.Error("no cursor stored")
	}
	e.waitLine("bridge_press", map[string]any{"connection": e.conn, "group": g, "command": "acknowledge",
		"external_id": fakebridge.BobID})
	e.settled()
	if labels := latest(e.posted("Presses")[0]); !slices.Contains(labels, "Resolve") {
		t.Fatalf("buttons after the acknowledgement %v", labels)
	}

	if a := e.answer(e.press(root.ID, fakebridge.AliceID, "Resolve")); a.Text != textBridgeNotLinked ||
		a.MessageID != root.ID || a.UserID != fakebridge.AliceID || a.ShownAs != "ephemeral" {
		t.Errorf("the answer to alice %+v", a)
	}
	e.waitLine("bridge_press", map[string]any{"connection": e.conn, "outcome": "not_linked",
		"external_id": fakebridge.AliceID})
	tampered := e.event("/press", map[string]any{"message_id": root.ID, "user_id": fakebridge.BobID,
		"data": "a9.k1.AAAAAAAAAAAA"})
	if a := e.answer(tampered); a.Text != textNotVerified {
		t.Errorf("the answer to tampered data %+v", a)
	}
	foreign := e.event("/press", map[string]any{"message_id": e.posted("Other")[0].ID, "user_id": fakebridge.BobID,
		"data": dataOf(t, root, "Resolve")})
	if a := e.answer(foreign); a.Text != textNotVerified {
		t.Errorf("the answer to data of another message %+v", a)
	}
	old := e.event("/press", map[string]any{"message_id": root.ID, "user_id": fakebridge.BobID, "button": "Resolve",
		"occurred_at": time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)})
	e.unanswered(old)
	e.waitLine("bridge_press_dropped", map[string]any{"connection": e.conn, "external_id": fakebridge.BobID})
	unknown := e.event("/press", map[string]any{"message_id": "nope", "user_id": fakebridge.BobID,
		"data": dataOf(t, root, "Resolve")})
	e.unanswered(unknown)
	e.waitLine("bridge_press", map[string]any{"connection": e.conn, "outcome": "unknown_message"})
	if s, so := e.status(g), e.status(other); s != "acknowledged" || so != "firing" {
		t.Errorf("after the refused presses: %s, %s", s, so)
	}

	e.destTest(dest, example)
	tests := e.testMessages()
	if len(tests) != 1 || len(tests[0].Envelope.Buttons) == 0 {
		t.Fatalf("test messages %+v", tests)
	}
	if a := e.answer(e.press(tests[0].ID, fakebridge.BobID, tests[0].Envelope.Buttons[0].Label)); a.Text !=
		textTestPress {
		t.Errorf("the answer to the press on the test message %+v", a)
	}
	e.waitLine("bridge_press", map[string]any{"connection": e.conn, "outcome": "test"})
	if s := e.status(g); s != "acknowledged" {
		t.Errorf("after the test press: %s", s)
	}
	if n := e.bridgeCommands(); n < 1 {
		t.Errorf("muster_commands_total with transport bridge = %v", n)
	}

	e.windowed("older")
	g3 := e.start("o1", "Older", "older", "older")
	older := e.waitPosted("Older", 1)[0]
	e.settled()
	advance(t, r, 660)
	e.command(g3, "acknowledge")
	e.waitPosted("Older", 2)
	e.settled()
	if n := h.count(t, `SELECT count(*) FROM deliveries WHERE superseded_message_ids = ARRAY['`+older.ID+`']`); n != 1 {
		t.Fatalf("%d deliveries supersede %s", n, older.ID)
	}
	e.press(older.ID, fakebridge.BobID, "Resolve")
	e.byBob(g3, "resolved", "resolved")
	e.settled()
	now := e.posted("Older")
	if p := e.bridgeCalls(http.MethodPatch, "/v1/messages/"+older.ID); len(p) != 0 || len(now[0].Edits) != 0 {
		t.Errorf("edits of the older Root message %+v", p)
	}
	if edits := now[1].Edits; len(edits) == 0 || edits[len(edits)-1].Status != "resolved" {
		t.Errorf("the current Root message %+v", now[1])
	}
}

// TestBridgeTextCommands is C-22.AC-16 against `muster dev` and the fake bridge: in the Thread of a firing Alert
// Group, bob's "ack" acknowledges it and is answered "Done: Acknowledged #N"; "still on it" answers the open Reminder;
// "unack" unacknowledges; "snooze 2h" is answered with the Route's durations; "/Snooze 4h" snoozes for 4 hours of the
// business clock; vera's "resolve" is refused; "looking into it" changes nothing and is not answered; a Text command
// older than bridge.event_max_age is dropped unanswered; "Решено" resolves; every answer is shown in the Thread; each
// Text command logs bridge_text_command.
func TestBridgeTextCommands(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	e.people()
	dest := e.destination("alerts", fakebridge.ChannelAlerts)
	e.route("text", "text", []string{dest}, nil)
	g := e.start("t1", "Text", "text", "text")
	root := e.waitPosted("Text", 1)[0]
	e.settled()
	n := e.number(g)
	said := func(user, text, want string) {
		t.Helper()
		if a := e.answer(e.say(root.ID, user, text)); a.Text != want || a.ShownAs != "thread" ||
			a.MessageID != root.ID || a.EventType != "thread_message" {
			t.Errorf("the answer to %q: %+v, want %q", text, a, want)
		}
	}
	done := func(command string) string { return fmt.Sprintf("Done: %s #%d", command, n) }

	said(fakebridge.BobID, "ack", done("Acknowledged"))
	e.byBob(g, "acknowledged", "acknowledged")
	advance(t, r, 14400)
	eventually(t, "the Reminder", func() bool {
		return h.count(t, `SELECT count(*) FROM thread_replies WHERE event = 'reminder' AND state = 'sent'`) > 0
	})
	e.settled()
	said(fakebridge.BobID, "still on it", done("Still on it"))
	if x := e.timeline(g, "&kind=timers"); len(x) == 0 || x[0].Event != "reminder_answered" ||
		x[0].Actor.Transport != "bridge" || x[0].Actor.Name != "bob" {
		t.Errorf("the answer of the Reminder %+v", x)
	}
	said(fakebridge.BobID, "unack", done("Unacknowledged"))
	said(fakebridge.BobID, "snooze 2h", "Snooze for one of: 1h, 4h, 24h, tomorrow, monday")
	said(fakebridge.BobID, "/Snooze 4h", done("Snoozed"))
	until, err := time.Parse(time.RFC3339Nano, fmt.Sprint(e.alertGroup(g)["snooze_until"]))
	if want := readClock(t, r).Now.Add(4 * time.Hour); err != nil || e.status(g) != "snoozed" ||
		until.Sub(want).Abs() > time.Minute {
		t.Errorf("after /Snooze 4h: %s until %v, want about %v", e.status(g), until, want)
	}
	said(fakebridge.VeraID, "resolve", textForbidden)
	e.unanswered(e.say(root.ID, fakebridge.BobID, "looking into it"))
	old := e.event("/thread-message", map[string]any{"message_id": root.ID, "user_id": fakebridge.BobID, "text": "ack",
		"occurred_at": time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)})
	e.unanswered(old)
	if s := e.status(g); s != "snoozed" {
		t.Errorf("after the refused Text commands: %s", s)
	}
	said(fakebridge.BobID, "Решено", done("Resolved"))
	e.byBob(g, "resolved", "resolved")

	lines := e.lines("bridge_text_command", map[string]any{"connection": e.conn})
	outcomes := []string{}
	for _, l := range lines {
		outcomes = append(outcomes, fmt.Sprint(l["outcome"]))
	}
	if len(lines) != 8 || countOf(outcomes, "dropped") != 1 || countOf(outcomes, "forbidden") != 1 ||
		countOf(outcomes, "unknown_duration") != 1 {
		t.Errorf("bridge_text_command outcomes %v", outcomes)
	}
}

// pushSecret is the push secret of the Verification of S-075.
const pushSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

// signed posts the event body to the push callback at url, signed with the push secret for the webhook-id id at the
// Unix time at, or with signature when it is not empty, and returns the status.
func signed(t *testing.T, url, id string, at int64, body, signature string) int {
	t.Helper()
	ts := strconv.FormatInt(at, 10)
	if signature == "" {
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(pushSecret, "whsec_"))
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + ts + "." + body))
		signature = "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	return call(t, http.MethodPost, url, body, "webhook-id", id, "webhook-timestamp", ts, "webhook-signature",
		signature, "Content-Type", "application/json").status
}

// TestBridgePush is C-22.AC-14, the deleted Root message of C-22.AC-17 and C-22.AC-3 against `muster dev` and the fake
// bridge, with Example Bridge in push mode: a signed event is answered 204, and again 204 for the same webhook-id; a
// timestamp 6 minutes old, a wrong signature, an unknown Connection and a Connection in poll mode are 401, a body that
// is no event 400, each logged as bridge_event_rejected; the fake pushes a press, answered once even when it comes
// again, bob's press acknowledges, and a deleted Root message is published again Quietly with the deletion notice; the
// push secret and the tokens reach no log line.
func TestBridgePush(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	e.people()
	dest := e.destination("alerts", fakebridge.ChannelAlerts)
	e.route("push", "push", []string{dest}, calm)
	second := e.api.json(http.MethodPost, "/api/v1/connections", `{"type":"bridge","name":"second","base_url":"`+
		h.Fakes.Bridge+`","bot_token":"`+fakebridge.SecondToken+`","event_delivery":"poll","proxy":{"enabled":false},
		"limiter":{"limit":10,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	a := e.api.do(http.MethodGet, "/api/v1/connections/"+e.conn, "")
	var conn map[string]any
	decode(t, a, &conn)
	conn = e.api.json(http.MethodPut, "/api/v1/connections/"+e.conn, fmt.Sprintf(`{"type":"bridge","name":%q,
		"base_url":%q,"event_delivery":"push","push_secret":%q,"proxy":{"enabled":false},"limiter":%s}`, conn["name"],
		conn["base_url"], pushSecret, mustJSON(conn["limiter"])), http.StatusOK, "If-Match", a.header.Get("ETag"))
	callback := fmt.Sprint(conn["callback_url"])
	if callback != devmode.IngestURL+"/api/v1/callbacks/bridge/"+e.conn {
		t.Fatalf("callback_url %s", callback)
	}
	// Any replica takes a push: the last one gets the checks of the test, the first the pushes of the fake.
	url := h.Replicas[len(h.Replicas)-1].Ingest + "/api/v1/callbacks/bridge/"
	ev := `{"id":"ev-1","type":"message_deleted","occurred_at":"2026-10-14T12:00:00Z","message_id":"nope"}`
	now := time.Now().Unix()
	for _, c := range []struct {
		what, conn, id string
		at             int64
		body, sig      string
		want           int
		reason         string
	}{
		{"a signed event", e.conn, "ev-1", now, ev, "", http.StatusNoContent, ""},
		{"the same event again", e.conn, "ev-1", now, ev, "", http.StatusNoContent, ""},
		{"a timestamp 6 minutes old", e.conn, "ev-1", now - 360, ev, "", http.StatusUnauthorized, "timestamp"},
		{"a wrong signature", e.conn, "ev-1", now, ev, "v1,AAAA", http.StatusUnauthorized, "signature"},
		{"no event", e.conn, "ev-2", now, "not json", "", http.StatusBadRequest, "not_an_event"},
		{"an unknown Connection", "CN0000000000Z9", "ev-1", now, ev, "", http.StatusUnauthorized,
			"unknown_connection"},
		{"a Connection in poll mode", second, "ev-1", now, ev, "", http.StatusUnauthorized, "poll_mode"},
	} {
		if got := signed(t, url+c.conn, c.id, c.at, c.body, c.sig); got != c.want {
			t.Errorf("%s = %d, want %d", c.what, got, c.want)
		}
		if c.reason != "" {
			e.waitLine("bridge_event_rejected", map[string]any{"connection": c.conn, "reason": c.reason})
		}
	}
	if n := h.count(t, `SELECT count(*) FROM bridge_events_seen WHERE event_id = 'ev-1'`); n != 1 {
		t.Errorf("ev-1 recorded %d times", n)
	}

	e.configure(`{"push":{"url":"` + callback + `","secret":"` + pushSecret + `"}}`)
	g := e.start("u1", "Pushed", "push", "pushed")
	root := e.waitPosted("Pushed", 1)[0]
	e.settled()
	alice := e.press(root.ID, fakebridge.AliceID, "Ack")
	if a := e.answer(alice); a.Text != textBridgeNotLinked {
		t.Errorf("the answer to alice %+v", a)
	}
	raw, _ := e.eventOf(alice)
	if got := signed(t, url+e.conn, alice, time.Now().Unix(), string(raw), ""); got != http.StatusNoContent {
		t.Errorf("the press pushed again = %d", got)
	}
	if got := e.answers(alice); len(got) != 1 {
		t.Errorf("answers to the press pushed twice %+v", got)
	}
	e.press(root.ID, fakebridge.BobID, "Ack")
	e.byBob(g, "acknowledged", "acknowledged")
	e.settled()

	e.fake(http.MethodDelete, e.fb+"/messages/"+root.ID, "")
	got := e.waitPosted("Pushed", 2)
	e.settled()
	if !got[0].Deleted || !got[0].Envelope.Notify || deletedNote.MatchString(got[0].Envelope.Body) || got[1].Deleted ||
		got[1].Envelope.Notify || !deletedNote.MatchString(got[1].Envelope.Body) ||
		got[1].Envelope.Status != "acknowledged" {
		t.Errorf("after the deletion: %+v", got)
	}
	if ev := deliveryEvents(e.timeline(g, "")); countOf(ev, "republished") != 1 {
		t.Errorf("delivery events %v", ev)
	}

	key := strings.TrimPrefix(pushSecret, "whsec_")
	for _, r := range h.Replicas {
		for _, secret := range []string{pushSecret, key, fakebridge.Token, fakebridge.SecondToken} {
			if strings.Contains(r.Output(), secret) {
				t.Errorf("%s logged %.12s…", r.name(), secret)
			}
		}
	}
}

// TestBridgeMessageDeleted is C-22.AC-17 against `muster dev` and the fake bridge, polled by the Leader: deleting the
// Root message of an open Alert Group publishes it again at once, Quietly, with the deletion notice, before any edit
// meets the deleted message; deleting the republished message marks the pair deleted in the messenger and posts
// nothing more; deleting a test message changes nothing.
func TestBridgeMessageDeleted(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	dest := e.destination("alerts", fakebridge.ChannelAlerts)
	e.route("deleted", "deleted", []string{dest}, calm)
	g := e.start("d1", "Del", "deleted", "deleted")
	first := e.waitPosted("Del", 1)[0]
	e.settled()

	e.fake(http.MethodDelete, e.fb+"/messages/"+first.ID, "")
	e.handled(e.lastEvent())
	got := e.waitPosted("Del", 2)
	e.settled()
	if !got[0].Deleted || !got[0].Envelope.Notify || deletedNote.MatchString(got[0].Envelope.Body) || got[1].Deleted ||
		got[1].Envelope.Notify || !deletedNote.MatchString(got[1].Envelope.Body) {
		t.Errorf("after the deletion: %+v", got)
	}
	if p := e.bridgeCalls(http.MethodPatch, "/v1/messages/"+first.ID); len(p) != 0 {
		t.Errorf("edits of the deleted message %+v", p)
	}
	if ev := deliveryEvents(e.timeline(g, "")); countOf(ev, "republished") != 1 {
		t.Errorf("delivery events %v", ev)
	}

	e.fake(http.MethodDelete, e.fb+"/messages/"+got[1].ID, "")
	e.handled(e.lastEvent())
	eventually(t, "deleted_in_messenger", func() bool {
		return slices.Contains(deliveryEvents(e.timeline(g, "")), "deleted_in_messenger")
	})
	if n := h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'deleted_in_messenger'`); n != 1 {
		t.Errorf("%d deliveries deleted in the messenger", n)
	}

	e.destTest(dest, example)
	tests := e.testMessages()
	if len(tests) != 1 {
		t.Fatalf("test messages %+v", tests)
	}
	e.fake(http.MethodDelete, e.fb+"/messages/"+tests[0].ID, "")
	e.handled(e.lastEvent())
	e.command(g, "acknowledge")
	e.settled()
	if all := e.all(); len(all) != 3 {
		t.Errorf("messages after the deletion of the test message: %d", len(all))
	}
	if ev := deliveryEvents(e.timeline(g, "")); countOf(ev, "republished") != 1 {
		t.Errorf("delivery events %v", ev)
	}
}

// TestBridgePollFailures is the failing poll of C-22.AC-13 against `muster dev` and the fake bridge: pollEvents
// answered 403 twice logs bridge_poll_failed once, at the first failure, makes no Destination Broken, and once the
// bridge answers again the next press is handled.
func TestBridgePollFailures(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	e.people()
	dest := e.destination("alerts", fakebridge.ChannelAlerts)
	e.route("polls", "polls", []string{dest}, calm)
	g := e.start("f1", "Polls", "polls", "polls")
	root := e.waitPosted("Polls", 1)[0]
	e.settled()

	fault := problemFault("/v1/events", http.StatusForbidden, "forbidden", "The bot was removed.")
	fault.Times = 2
	e.bridgeFault(fault)
	// The long poll in flight is not faulted: the press ends it, and the next two polls fail.
	e.handled(e.press(root.ID, fakebridge.BobID, "Ack"))
	e.byBob(g, "acknowledged", "acknowledged")
	e.waitLine("bridge_poll_failed", map[string]any{"connection": e.conn, "failures": 1})
	eventually(t, "the polls after the failures", func() bool {
		polls := e.bridgeCalls(http.MethodGet, "/v1/events")
		return countOf(statuses(polls), "403") == 2 && polls[len(polls)-1].Status != http.StatusForbidden
	})
	e.settled()
	e.unanswered(e.press(root.ID, fakebridge.BobID, "Unack"))
	e.byBob(g, "firing", "unacknowledged")
	if n := len(e.lines("bridge_poll_failed", map[string]any{"connection": e.conn})); n != 1 {
		t.Errorf("%d bridge_poll_failed lines", n)
	}
	if hl := e.health(dest); hl.State != "healthy" {
		t.Errorf("health after the failed polls %+v", hl)
	}
	if n := h.count(t, `SELECT count(*) FROM destinations WHERE health <> 'healthy'`); n != 0 {
		t.Errorf("%d Destinations not healthy", n)
	}
}

// statuses are the statuses of the requests, as text.
func statuses(reqs []fakeserver.Request) []string {
	out := []string{}
	for _, r := range reqs {
		out = append(out, strconv.Itoa(r.Status))
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
