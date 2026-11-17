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

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// undelivered is a "Mention not delivered" of a Timeline, or a warning of a Destination, as the Verification prints
// them: the person's login, the entry's target kind, the Loud event or the messenger, and the reason.
type undelivered struct {
	User, Kind, Event, Reason string
}

func (u undelivered) String() string { return u.User + "/" + u.Kind + "/" + u.Event + "/" + u.Reason }

// TestMentionWarnings is S-165 against `muster dev`, as its Verification: payments-notice mentions "On-call of the
// Team from the label" and alice, linked; bob, On-call of payments without a Mattermost Account link, is left out of
// the new Alert Group's Root message, nothing is posted in its Thread, the Timeline has one mention_not_delivered for
// him with the entry, the event new_alert_group and the reason no_account_link, alice none, and the Destination warns
// about bob (C-28.FR-3, AC-4, C-11.FR-21, C-12.FR-12). After bob links his account, the next Loud message mentions him,
// records mention_delivered, which the Timeline leaves out, and the warning is gone. Beyond it: a Bridge Destination on
// the fake bridge without native_mentions sends linked bob by the plain name and records bridge_cannot_mention_people
// (C-28.AC-3, C-22.FR-16); a Root message that ends Not delivered and a Quiet message record no event.
func TestMentionWarnings(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := &linksEnv{timersEnv: &timersEnv{tgd: newTGD(t, h, r)}}
	alice := e.person("Alice Smith", "alice", "responder")
	bob := e.person("Bob", "bob", "responder")
	carol := e.person("Carol", "carol", "responder")
	e.mmConn = e.connection(50)
	pn := e.destination("payments-notice", e.mmConn, fakemattermost.ChannelPaymentsNotice, nil, 50)
	rid, code := e.codeFor(alice, "alice", fakemattermost.AliceUserID)
	if a := e.confirm(alice, rid, code); a.status != http.StatusCreated {
		t.Fatalf("alice's link = %d %s", a.status, a.body)
	}

	tp := e.api.json(http.MethodPost, "/api/v1/teams", `{"name":"payments","slug":"payments","own_working_hours":false}`,
		http.StatusCreated)["id"].(string)
	e.api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+bob.id+`"}`, http.StatusCreated)
	now := readClock(t, r).Now.UTC()
	monday := now.AddDate(0, 0, -7-(int(now.Weekday())+6)%7).Format(time.DateOnly)
	e.api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Main","time_zone":"UTC",`+
		`"rotation_layers":[{"name":"Bob","user_ids":["`+bob.id+`"],"shift_length":"week","handover_time":"00:00",`+
		`"handover_day":"monday","start_date":"`+monday+`","limits":null}]}`, http.StatusCreated)
	quiet := func(p map[string]any) {
		p["ack_timeout"] = map[string]any{"enabled": false, "first_interval_seconds": 300}
		p["reminders"] = map[string]any{"enabled": false, "first_interval_seconds": 600, "cap_seconds": 3600}
	}
	e.route("Payments", "payments", []string{pn}, quiet)

	// entries replaces the Mention entries of the Destination id, as the Verification's PUT does.
	entries := func(id, list string) {
		t.Helper()
		read := e.api.do(http.MethodGet, "/api/v1/destinations/"+id, "")
		var body map[string]any
		decode(t, read, &body)
		for _, k := range []string{"id", "etag", "mentions", "health", "routes", "teams", "created_at", "owning_team",
			"team_name", "channel_name", "mention_warnings"} {
			delete(body, k)
		}
		body["mention_entries"] = json.RawMessage(list)
		b, _ := json.Marshal(body)
		if a := e.api.do(http.MethodPut, "/api/v1/destinations/"+id, string(b), "If-Match",
			read.header.Get("ETag")); a.status != http.StatusOK {
			t.Fatalf("save the entries of %s = %d %s", id, a.status, a.body)
		}
	}
	timeline := func(g string) []undelivered {
		t.Helper()
		var page struct {
			Items []struct {
				DeliveryEvent string `json:"delivery_event"`
				Mention       *struct {
					User  struct{ Login string }
					Entry struct {
						Target struct{ Kind string }
					}
					Event, Reason string
				}
			}
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?limit=200&kind=delivery", ""), &page)
		out := []undelivered{}
		for _, it := range page.Items {
			switch it.DeliveryEvent {
			case "mention_delivered":
				t.Errorf("the timeline of %s lists mention_delivered", g)
			case "mention_not_delivered":
				if it.Mention == nil {
					t.Fatalf("mention_not_delivered without mention in %s", g)
				}
				out = append(out, undelivered{it.Mention.User.Login, it.Mention.Entry.Target.Kind, it.Mention.Event,
					it.Mention.Reason})
			}
		}
		return out
	}
	warnings := func(id string) []undelivered {
		t.Helper()
		var d struct {
			Warnings []struct {
				User  struct{ Login string }
				Entry struct {
					Target struct{ Kind string }
				}
				Messenger, Reason, Since string
			} `json:"mention_warnings"`
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/destinations/"+id, ""), &d)
		out := []undelivered{}
		for _, w := range d.Warnings {
			if _, err := time.Parse(time.RFC3339, w.Since); err != nil {
				t.Errorf("since %q", w.Since)
			}
			out = append(out, undelivered{w.User.Login, w.Entry.Target.Kind, w.Messenger, w.Reason})
		}
		return out
	}
	want := func(what string, got []undelivered, want ...string) {
		t.Helper()
		s := []string{}
		for _, u := range got {
			s = append(s, u.String())
		}
		if !slices.Equal(s, append([]string{}, want...)) {
			t.Errorf("%s = %v, want %v", what, s, want)
		}
	}
	mentionEvents := func(kind string) int64 {
		return e.h.count(t, `SELECT count(*) FROM delivery_events WHERE kind = '`+kind+`'`)
	}

	entries(pn, `[{"target":{"kind":"on_call_of_teams_from_label"}},{"target":{"kind":"user","user_id":"`+alice.id+
		`"}}]`)
	labels := func(cluster, team string) string {
		return `{"team":"` + team + `","cluster":"` + cluster + `","severity":"warning"}`
	}

	// C-28.AC-4: bob is not mentioned, nothing is in the Thread, the Timeline and the warning say so.
	e.group("w1", "QueueBacklog")
	e.alert("w1", "a", labels("w1", "payments"), "")
	e.notify("w1", "first notification")
	g := e.groupOf("w1")
	e.waitPublished(g)
	eventually(t, "the undelivered mention", func() bool { return mentionEvents("mention_not_delivered") == 1 })
	e.settle()
	root := e.root(e.number(g))
	text := root.Message
	if text == "" && len(root.Attachments) > 0 {
		text = root.Attachments[0].Text
	}
	if line, _, _ := strings.Cut(text, "\n"); line != "@"+fakemattermost.AliceUsername {
		t.Errorf("the root message mentions %q", line)
	}
	if replies := e.replies(root.ID); len(replies) != 0 {
		t.Errorf("%d thread replies, want 0: %+v", len(replies), replies)
	}
	want("the timeline", timeline(g), "bob/on_call_of_teams_from_label/new_alert_group/no_account_link")
	want("the warnings", warnings(pn), "bob/on_call_of_teams_from_label/mattermost/no_account_link")

	// bob links his account as in S-069; the next Loud message mentions him and the warning is gone.
	rid, code = e.codeFor(bob, "bob", fakemattermost.BobUserID)
	if a := e.confirm(bob, rid, code); a.status != http.StatusCreated {
		t.Fatalf("bob's link = %d %s", a.status, a.body)
	}
	e.alert("w1", "b", `{"team":"payments","cluster":"w1","severity":"warning","queue":"b"}`, "")
	e.notify("w1", "new alerts")
	e.adv(61)
	if !holdsSoon(func() bool { return mentionEvents("mention_delivered") == 1 }) {
		t.Fatalf("no delivered mention: events %s, replies %+v", h.dump(t, `SELECT kind || ' ' || coalesce(detail::text, '')
			FROM delivery_events ORDER BY id`), e.replies(root.ID))
	}
	if replies := e.replies(root.ID); len(replies) != 1 || !strings.Contains(replies[0].Message,
		"@"+fakemattermost.BobUsername) {
		t.Errorf("the new alerts reply %+v", replies)
	}
	want("the warnings after the link", warnings(pn))
	want("the timeline after the link", timeline(g), "bob/on_call_of_teams_from_label/new_alert_group/no_account_link")
	if n := mentionEvents("mention_not_delivered"); n != 1 {
		t.Errorf("%d mention_not_delivered, want 1: alice and the linked bob record nothing", n)
	}

	// A Quiet message records nothing: an acknowledgement edits the Root message and replies Quietly.
	e.command(g, "acknowledge")
	e.settle()
	if n := mentionEvents("mention_not_delivered") + mentionEvents("mention_delivered"); n != 2 {
		t.Errorf("after a quiet message %d mention events, want 2", n)
	}

	// C-28.AC-3, C-22.FR-16: a bridge without native_mentions, read by a check of its Connection on every replica,
	// gets linked bob by the plain name.
	var bridgeConn string
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections?type=bridge", "", http.StatusOK)["items"].([]any) {
		if c := item.(map[string]any); c["name"] == devmode.BridgeConnectionName {
			bridgeConn = c["id"].(string)
		}
	}
	capabilities, _ := json.Marshal(fakebridge.DefaultConfig().Capabilities)
	t.Cleanup(func() {
		restore(t, http.MethodPut, h.Fakes.Bridge+"/_fake/config", `{"capabilities":`+string(capabilities)+`}`)
	})
	e.fake(http.MethodPut, h.Fakes.Bridge+"/_fake/config", `{"capabilities":{"native_mentions":false}}`)
	for _, rep := range h.Replicas {
		api := patClient{t: t, base: rep.App, token: e.api.token}
		if c := api.json(http.MethodPost, "/api/v1/connections/"+bridgeConn+"/checks", "", http.StatusOK); c["ok"] !=
			true {
			t.Fatalf("the check of the bridge: %v", c)
		}
	}
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
		display_name, created_at) SELECT 1, 'AK0000000000B8', u.id, 'bridge', c.id, '`+fakebridge.BobID+`',
		'Bob Jones', now() FROM users u, connections c WHERE u.public_id = '`+bob.id+`' AND c.public_id = '`+
		bridgeConn+`'`)
	bd := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":"ops-bridge",
		"connection_id":%q,"channel_id":%q,"limiter":{"limit":50,"per_seconds":1},"mention_entries":[
		{"target":{"kind":"user","user_id":%q}}]}`, bridgeConn, fakebridge.ChannelAlerts, bob.id),
		http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	e.route("Ops", "ops", []string{bd}, quiet)
	e.group("w2", "DiskFull")
	e.alert("w2", "a", labels("w2", "ops"), "")
	e.notify("w2", "first notification")
	g2 := e.groupOf("w2")
	e.waitPublished(g2)
	e.settle()
	var all []fakebridge.Message
	decode(t, call(t, http.MethodGet, h.Fakes.Bridge+"/_fake/messages", ""), &all)
	sent := false
	for _, m := range all {
		if !strings.HasSuffix(m.Envelope.Title, " DiskFull") {
			continue
		}
		for _, men := range m.Envelope.Mentions {
			sent = sent || men.ExternalId == fakebridge.BobID
		}
		if !strings.Contains(m.Envelope.Body, "Bob Jones") {
			t.Errorf("the bridge message names bob by %q", m.Envelope.Body)
		}
	}
	if !sent {
		t.Errorf("bob was not sent to the bridge: %+v", all)
	}
	want("the timeline of the bridge", timeline(g2), "bob/user/new_alert_group/bridge_cannot_mention_people")
	want("the warnings of the bridge", warnings(bd), "bob/user/bridge/bridge_cannot_mention_people")

	// A Root message that ends Not delivered records no event, though carol, now named and unlinked, would.
	entries(pn, `[{"target":{"kind":"on_call_of_teams_from_label"}},{"target":{"kind":"user","user_id":"`+carol.id+
		`"}}]`)
	before := mentionEvents("mention_not_delivered") + mentionEvents("mention_delivered")
	e.fault(fakeserver.Fault{Path: "/api/v4/posts", Status: http.StatusBadRequest, Times: 1})
	e.group("w3", "QueueStuck")
	e.alert("w3", "a", labels("w3", "payments"), "")
	e.notify("w3", "first notification")
	g3 := e.groupOf("w3")
	eventually(t, "the root message not delivered", func() bool {
		return slices.Contains(deliveryEvents(e.timeline(g3, "&kind=delivery")), "not_delivered")
	})
	e.settle()
	if n := mentionEvents("mention_not_delivered") + mentionEvents("mention_delivered"); n != before {
		t.Errorf("after a root message not delivered: %d mention events, want %d", n, before)
	}
	want("the timeline of the root message not delivered", timeline(g3))
	want("the warnings at the end", warnings(pn))
}

// holdsSoon reports whether cond holds within the time eventually waits.
func holdsSoon(cond func() bool) bool {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cond()
}

// dump is the rows of sql, one text column, one per line.
func (h *Harness) dump(t *testing.T, sql string) string {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	rows, err := conn.Query(t.Context(), sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
