// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// brd drives Bridge Destinations: the mm environment — an Admin's Personal access token, the Integration "lab" and
// the fake Alertmanager — with the demo Bridge Connection "Dev Bridge" of the fake bridge, whose configuration is
// restored when the test ends.
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
	t.Cleanup(func() {
		restore(t, http.MethodPut, e.fb+"/config",
			`{"replays":true,"limits":{"max_body_length":4000,"edit_max_age_seconds":null}}`)
		restore(t, http.MethodDelete, e.fb+"/faults", "")
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
// the Connection check, an Alert Group of 40 Alerts posts a body of at most 1,000 code points with the link to Muster,
// shortened by its Alert lines, and keeps its title, link and buttons in the envelope.
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
			"instance":"node-%02d.region-a.example.org:9100"}`, i, i), "")
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
	if !strings.HasPrefix(m.Envelope.Title, "#") || m.Envelope.Link.Label != "Open in Muster" ||
		!strings.Contains(m.Envelope.Link.Url, "/alert-groups/") || len(m.Envelope.Buttons) == 0 {
		t.Errorf("envelope %+v", m.Envelope)
	}
	t.Logf("40 alerts: the body has %d code points", utf8.RuneCountInString(body))
}
