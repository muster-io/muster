// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
)

// markSaves are the helpers of TestManagedByMarkOnlySaves: the Admin's Personal access token, as a tool such as the
// Terraform provider holds one.
type markSaves struct {
	t   *testing.T
	api patClient
}

// read reads the object at target and its ETag.
func (m markSaves) read(target string) (map[string]any, string) {
	m.t.Helper()
	a := m.api.do(http.MethodGet, target, "")
	if a.status != http.StatusOK {
		m.t.Fatalf("GET %s = %d %s", target, a.status, a.body)
	}
	var got map[string]any
	decode(m.t, a, &got)
	return got, a.header.Get("ETag")
}

// put saves body at target with If-Match etag.
func (m markSaves) put(target string, body map[string]any, etag string) answer {
	m.t.Helper()
	b, _ := json.Marshal(body)
	return m.api.do(http.MethodPut, target, string(b), "If-Match", etag)
}

// saved is the object a save answered, the Destination of a Destination's answer, and its ETag; a save that did not
// answer 200 fails the test.
func (m markSaves) saved(what string, a answer) (map[string]any, string) {
	m.t.Helper()
	if a.status != http.StatusOK {
		m.t.Fatalf("%s = %d %s", what, a.status, a.body)
	}
	var got map[string]any
	decode(m.t, a, &got)
	if d, ok := got["destination"].(map[string]any); ok {
		got = d
	}
	return got, a.header.Get("ETag")
}

// onlyMark fails unless the newest Audit log entry of the object id changes /managed_by alone, to mark.
func (m markSaves) onlyMark(what, id, mark string) {
	m.t.Helper()
	page := m.api.json(http.MethodGet, "/api/v1/audit-log?limit=1&resource_id="+id, "", http.StatusOK)
	items, _ := page["items"].([]any)
	if len(items) == 0 {
		m.t.Fatalf("%s: no Audit log entry", what)
	}
	diff, _ := items[0].(map[string]any)["diff"].([]any)
	if len(diff) != 1 || diff[0].(map[string]any)["pointer"] != "/managed_by" ||
		diff[0].(map[string]any)["after"] != mark {
		m.t.Errorf("%s: the Audit log of the mark alone: %v", what, diff)
	}
}

// stale fails unless a save of body and a deletion at target with the ETag etag are refused with 412.
func (m markSaves) stale(what, target string, body map[string]any, etag string) {
	m.t.Helper()
	if a := m.put(target, body, etag); a.status != http.StatusPreconditionFailed {
		m.t.Errorf("%s: a save with the ETag before the mark = %d %s", what, a.status, a.body)
	}
	if a := m.api.do(http.MethodDelete, target, "", "If-Match", etag); a.status != http.StatusPreconditionFailed {
		m.t.Errorf("%s: a deletion with the ETag before the mark = %d %s", what, a.status, a.body)
	}
}

// bodyOf is the body of a save of the Connection or Destination got of the type typ as it reads, without its secrets,
// so that a save of it keeps them, with the fields given.
func bodyOf(typ string, got map[string]any, keys []string, fields map[string]any) map[string]any {
	out := map[string]any{"type": typ, "name": got["name"], "limiter": got["limiter"]}
	for _, k := range keys {
		out[k] = got[k]
	}
	for k, v := range fields {
		out[k] = v
	}
	return out
}

// connectionBody is bodyOf of a Connection without a proxy, as the demo Connections have none: a read shows the
// status of the proxy's password, which a save does not take.
func connectionBody(typ string, got map[string]any, keys []string, fields map[string]any) map[string]any {
	out := bodyOf(typ, got, keys, fields)
	out["proxy"] = map[string]any{"enabled": false}
	return out
}

// polls are the long polls of pollEvents carrying token that the fake bridge recorded, in the order they came, and
// how many of them were given up: unanswered with a later one after them, as the poll that a poller starting again
// leaves. A poll the fake answered at its wait has a status.
func polls(t *testing.T, h *Harness, token string) (all []fakeserver.Request, givenUp int) {
	t.Helper()
	for _, r := range fakeRequests(t, h.Fakes.Bridge) {
		if r.Path == "/v1/events" && carries(r, token) {
			all = append(all, r)
		}
	}
	for i, r := range all {
		if r.Status == 0 && i < len(all)-1 {
			givenUp++
		}
	}
	return all, givenUp
}

// inFlight reports whether the newest long poll carrying token is still served.
func inFlight(t *testing.T, h *Harness, token string) bool {
	t.Helper()
	all, _ := polls(t, h, token)
	return len(all) > 0 && all[len(all)-1].Status == 0
}

// slackApp is the Socket Mode state of the fake Slack server's app key.
func slackApp(t *testing.T, h *Harness, key string) fakeslack.AppSockets {
	t.Helper()
	var st fakeslack.SocketState
	decode(t, call(t, http.MethodGet, h.Fakes.Slack+"/_fake/socket", ""), &st)
	return st.Apps[key]
}

// TestManagedByMarkOnlySaves is the save that changes only the mark managed_by (C-33.FR-11) against `muster dev`: it
// changes no behaviour and never refuses a write. A Mattermost Destination whose channel the fake Mattermost server
// refuses takes the mark alone with 200, a new ETag and /managed_by alone in the Audit log, and the fake records no
// request of its Destination check, while the mark with a new name runs the check and is refused with 422
// destination_check_failed. The demo Slack Connection in socket mode takes the mark alone without a new Socket Mode
// connection on the fake Slack server and keeps what its last hello counted, and the demo Bridge Connection in poll
// mode takes it without its long poll starting again; each answers a new ETag and records /managed_by alone, and a new
// name reconnects the socket and starts the poll again. After a save of the mark alone the ETag read before it is
// stale (412) on a save and a deletion, and the one it answered works.
func TestManagedByMarkOnlySaves(t *testing.T) {
	h := Start(t, DevProcess)
	e := newMM(t, h, h.Replicas[0])

	t.Run("mattermost destination", func(t *testing.T) {
		e := e.on(t)
		m := markSaves{t: t, api: e.api}
		conn := e.connection(100)
		dest := e.destination("tf-broken", conn, fakemattermost.ChannelAlerts, nil, 100)
		target := "/api/v1/destinations/" + dest
		got, etag := m.read(target)
		keys := []string{"connection_id", "team_id", "channel_id"}
		fakeFault(t, h.Fakes.Mattermost, fakeserver.Fault{
			Path: "/api/v4/channels/" + fakemattermost.ChannelAlerts + "/members/me", Status: http.StatusNotFound})
		defer call(t, http.MethodDelete, h.Fakes.Mattermost+"/_fake/faults", "")
		checks := func() int {
			n := 0
			for _, r := range fakeRequests(t, h.Fakes.Mattermost) {
				if carries(r, "mm-dev-token") {
					n++
				}
			}
			return n
		}
		before := checks()
		renamed := bodyOf("mattermost", got, keys, map[string]any{"name": "tf-broken-2", "managed_by": "terraform"})
		if a := m.put(target, renamed, etag); fieldError(t, a) != "destination_check_failed /channel_id" {
			t.Errorf("the mark and a new name while the channel is refused = %d %s", a.status, a.body)
		}
		if checks() == before {
			t.Fatal("the save with a new name made no request of the Destination check")
		}

		before = checks()
		body := bodyOf("mattermost", got, keys, map[string]any{"managed_by": "terraform"})
		saved, now := m.saved("the mark alone while the channel is refused", m.put(target, body, etag))
		if saved["managed_by"] != "terraform" || now == "" || now == etag || saved["etag"] != now {
			t.Errorf("the mark alone: mark %v, ETag %s, before %s, etag %v", saved["managed_by"], now, etag,
				saved["etag"])
		}
		if n := checks() - before; n != 0 {
			t.Errorf("the mark alone made %d requests of the Destination check", n)
		}
		m.onlyMark("the Mattermost Destination", dest, "terraform")
		m.stale("the Mattermost Destination", target, body, etag)
		if a := m.api.do(http.MethodDelete, target, "", "If-Match", now); a.status != http.StatusNoContent {
			t.Errorf("a deletion with the ETag after the mark = %d %s", a.status, a.body)
		}
	})

	t.Run("slack socket", func(t *testing.T) {
		e := e.on(t)
		m := markSaves{t: t, api: e.api}
		conn := e.demoConnection("slack", devmode.SlackConnectionName)
		target := "/api/v1/connections/" + conn
		var got map[string]any
		var etag string
		within(t, "the hello of the socket of "+devmode.SlackConnectionName, socketWait, func() bool {
			got, etag = m.read(target)
			return got["socket"] != nil && slackApp(t, h, fakeslack.AppA).Open == 1
		})
		hello := got["socket"].(map[string]any)["hello_at"]
		app := slackApp(t, h, fakeslack.AppA)
		keys := []string{"event_mode"}
		body := connectionBody("slack", got, keys, map[string]any{"managed_by": "terraform"})
		saved, now := m.saved("the mark alone of the Slack Connection", m.put(target, body, etag))
		if saved["managed_by"] != "terraform" || now == etag || saved["etag"] != now {
			t.Errorf("the mark alone: mark %v, ETag %s, before %s", saved["managed_by"], now, etag)
		}
		if sock, _ := saved["socket"].(map[string]any); sock == nil || sock["hello_at"] != hello {
			t.Errorf("the mark alone forgot the hello: %v, before %v", saved["socket"], got["socket"])
		}
		m.onlyMark("the Slack Connection", conn, "terraform")

		// A Slack Connection of the second app in socket mode, created after the save: once its socket said hello, the
		// Leader read the Connections after the save, and the socket of the first app did not reconnect.
		other := e.api.json(http.MethodPost, "/api/v1/connections", `{"type":"slack","name":"tf-socket-b",
			"event_mode":"socket","bot_token":"`+devmode.SlackSecondBotToken+`","app_token":"`+
			devmode.SlackSecondAppToken+`","proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":60}}`,
			http.StatusCreated)["id"].(string)
		within(t, "the hello of the socket of the second app", socketWait, func() bool {
			return slackApp(t, h, fakeslack.AppB).Hellos > 0
		})
		if a := slackApp(t, h, fakeslack.AppA); a.Opened != app.Opened || a.Hellos != app.Hellos || a.Open != 1 {
			t.Errorf("the mark alone reconnected the socket: %+v, before %+v", a, app)
		}
		if after, _ := m.read(target); after["socket"] == nil || after["socket"].(map[string]any)["hello_at"] != hello {
			t.Errorf("after the mark alone the socket reads %v, before %v", after["socket"], got["socket"])
		}
		m.stale("the Slack Connection", target, body, etag)

		// A new name reconnects it.
		m.saved("a new name of the Slack Connection",
			m.put(target, connectionBody("slack", got, keys, map[string]any{"name": "tf-socket-a"}), now))
		within(t, "the socket of the renamed Connection", socketWait, func() bool {
			a := slackApp(t, h, fakeslack.AppA)
			return a.Opened == app.Opened+1 && a.Hellos == app.Hellos+1 && a.Open == 1
		})

		// The second one is deleted with the ETag a save of its mark alone answered.
		otherAt := "/api/v1/connections/" + other
		got, etag = m.read(otherAt)
		body = connectionBody("slack", got, keys, map[string]any{"managed_by": "terraform"})
		_, now = m.saved("the mark alone of the second Slack Connection", m.put(otherAt, body, etag))
		m.stale("the second Slack Connection", otherAt, body, etag)
		if a := m.api.do(http.MethodDelete, otherAt, "", "If-Match", now); a.status != http.StatusNoContent {
			t.Errorf("a deletion with the ETag after the mark = %d %s", a.status, a.body)
		}
	})

	t.Run("bridge poll", func(t *testing.T) {
		e := e.on(t)
		m := markSaves{t: t, api: e.api}
		conn := e.demoConnection("bridge", devmode.BridgeConnectionName)
		target := "/api/v1/connections/" + conn
		got, etag := m.read(target)
		keys := []string{"base_url", "event_delivery"}
		eventually(t, "a long poll of "+devmode.BridgeConnectionName+" in flight", func() bool {
			return inFlight(t, h, fakebridge.Token)
		})
		_, givenUp := polls(t, h, fakebridge.Token)
		body := connectionBody("bridge", got, keys, map[string]any{"managed_by": "terraform"})
		saved, now := m.saved("the mark alone of the Bridge Connection", m.put(target, body, etag))
		if saved["managed_by"] != "terraform" || now == etag || saved["etag"] != now {
			t.Errorf("the mark alone: mark %v, ETag %s, before %s", saved["managed_by"], now, etag)
		}
		m.onlyMark("the Bridge Connection", conn, "terraform")

		// A Bridge Connection of the second bot in poll mode, created after the save: once it polls, the Leader read the
		// Connections after the save, and the poll of the first did not start again.
		other := e.api.json(http.MethodPost, "/api/v1/connections", `{"type":"bridge","name":"tf-poll-b",
			"base_url":"`+got["base_url"].(string)+`","event_delivery":"poll","bot_token":"`+fakebridge.SecondToken+
			`","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
		eventually(t, "a long poll of the second bot", func() bool { return inFlight(t, h, fakebridge.SecondToken) })
		if _, n := polls(t, h, fakebridge.Token); n != givenUp {
			t.Errorf("the mark alone started the poll again: %d polls given up, %d before", n, givenUp)
		}
		m.stale("the Bridge Connection", target, body, etag)

		// A new name starts it again: the poll in flight is given up for a new one.
		eventually(t, "a long poll in flight before the rename", func() bool { return inFlight(t, h, fakebridge.Token) })
		before, _ := polls(t, h, fakebridge.Token)
		last := before[len(before)-1]
		m.saved("a new name of the Bridge Connection",
			m.put(target, connectionBody("bridge", got, keys, map[string]any{"name": "tf-poll-a"}), now))
		eventually(t, "the poll of the renamed Connection", func() bool {
			all, _ := polls(t, h, fakebridge.Token)
			return len(all) > len(before)
		})
		all, _ := polls(t, h, fakebridge.Token)
		i := slices.IndexFunc(all, func(r fakeserver.Request) bool { return r.AtMs == last.AtMs && r.Query == last.Query })
		if i < 0 {
			t.Error("the poll in flight before the rename is gone from the requests")
		} else if all[i].Status != 0 {
			// It ended at its wait just as the rename came: the poll after it is the new one all the same.
			t.Logf("the poll in flight before the rename was answered %d", all[i].Status)
		}

		otherAt := "/api/v1/connections/" + other
		if _, etag := m.read(otherAt); m.api.do(http.MethodDelete, otherAt, "", "If-Match", etag).status !=
			http.StatusNoContent {
			t.Error("the deletion of the second Bridge Connection failed")
		}
	})
}
