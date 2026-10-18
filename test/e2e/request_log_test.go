// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// requestLog lists the Request log that query names — connection=… or destination=…, with any filters — all
// pages followed, newest first.
func (e *mm) requestLog(query string) []map[string]any {
	e.t.Helper()
	var out []map[string]any
	cursor := ""
	for {
		target := "/api/v1/request-log?" + query + "&limit=200"
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		var page struct {
			Items      []map[string]any `json:"items"`
			NextCursor *string          `json:"next_cursor"`
		}
		decode(e.t, e.api.do(http.MethodGet, target, ""), &page)
		out = append(out, page.Items...)
		if page.NextCursor == nil {
			return out
		}
		cursor = *page.NextCursor
	}
}

// requestRecord reads the record id with its headers and bodies, and its JSON as text.
func (e *mm) requestRecord(id string) (map[string]any, string) {
	e.t.Helper()
	a := e.api.do(http.MethodGet, "/api/v1/request-log/"+id, "")
	if a.status != http.StatusOK {
		e.t.Fatalf("getRequestRecord %s = %d %s", id, a.status, a.body)
	}
	var m map[string]any
	decode(e.t, a, &m)
	return m, string(a.body)
}

// connectionNamed is the id of the Connection name.
func (e *mm) connectionNamed(name string) string {
	e.t.Helper()
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
		if c := item.(map[string]any); c["name"] == name {
			return c["id"].(string)
		}
	}
	e.t.Fatalf("no Connection %s", name)
	return ""
}

// ofOperation are the records of operation among records.
func ofOperation(records []map[string]any, operation string) []map[string]any {
	var out []map[string]any
	for _, r := range records {
		if r["operation"] == operation {
			out = append(out, r)
		}
	}
	return out
}

// countRequests counts the requests a fake server recorded whose path ends with suffix.
func countRequests(t *testing.T, fakeURL, suffix string) int {
	t.Helper()
	n := 0
	for _, r := range fakeRequests(t, fakeURL) {
		if strings.HasSuffix(r.Path, suffix) {
			n++
		}
	}
	return n
}

// waitInFlight waits until the newest request of the fake server at fakeURL whose path ends with suffix is still being
// served — a long poll that a fault scripted now leaves alone, so that the fault answers the poll after it.
func waitInFlight(t *testing.T, fakeURL, suffix string) {
	t.Helper()
	eventually(t, "a long poll in flight", func() bool {
		reqs := fakeRequests(t, fakeURL)
		for i := len(reqs) - 1; i >= 0; i-- {
			if strings.HasSuffix(reqs[i].Path, suffix) {
				return reqs[i].Status == 0
			}
		}
		return false
	})
}

// fakeFault scripts the fault f of the fake server at fakeURL.
func fakeFault(t *testing.T, fakeURL string, f fakeserver.Fault) {
	t.Helper()
	b, _ := json.Marshal(f)
	if a := call(t, http.MethodPost, fakeURL+"/_fake/faults", string(b)); a.status/100 != 2 {
		t.Fatalf("fault %s = %d %s", b, a.status, a.body)
	}
}

// startToken is a token of a Telegram deep link or a Bridge account_link event: 43 base64url characters.
const startToken = "rl0123456789abcdefghijklmnopqrstuvwxyzABCDE"

// TestRequestLog is S-088 against `muster dev`: a Publication is recorded with its bodies and links and found by its
// Connection, Destination and Alert Group (C-23.AC-1); polls are recorded only with updates or events, a failed poll
// always, and the tokens they carry are masked (C-23.AC-2, AC-3); an outgoing webhook has one record per attempt with
// the answer's body, its Secrets and signature masked, and a request without an answer its error code (C-23.AC-5);
// the API refuses a list without exactly one owner and an unknown or deleted one (C-23.FR-5); and the count and age
// bounds hold for reads and for the Leader's pruning, which a move of the development clock wakes (C-23.AC-4).
func TestRequestLog(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	t.Cleanup(func() {
		for _, u := range []string{h.Fakes.Telegram, h.Fakes.Bridge, webhookBase} {
			restore(t, http.MethodDelete, u+"/_fake/faults", "")
		}
	})

	t.Run("publication", func(t *testing.T) {
		e := e.on(t)
		conn := e.connection(50)
		dest := e.destination("rl-mm", conn, fakemattermost.ChannelAlerts, nil, 50)
		e.route("rl", "rl", []string{dest}, nil)
		e.group("rl1", "RequestLogCheck")
		e.alert("rl1", "i1", `{"team":"rl","cluster":"rl1"}`, "")
		e.notify("rl1", "first notification")
		g := e.groupOf("rl1")
		var first map[string]any
		eventually(t, "the record of the Publication", func() bool {
			items := e.api.json(http.MethodGet, "/api/v1/request-log?connection="+conn+"&operation=createPost&limit=1",
				"", http.StatusOK)["items"].([]any)
			if len(items) == 0 {
				return false
			}
			first = items[0].(map[string]any)
			return true
		})
		d, _ := first["destination"].(map[string]any)
		ag, _ := first["alert_group"].(map[string]any)
		if first["direction"] != "outbound" || first["operation"] != "createPost" || first["method"] != http.MethodPost ||
			first["status"] != 201.0 || first["via"] != "direct" || first["attempt"] != 1.0 || d["id"] != dest ||
			ag["id"] != g || first["connection_id"] != conn || first["error_class"] != nil {
			t.Fatalf("the record of the Publication = %v", first)
		}
		id := first["id"].(string)
		found := false
		for _, x := range e.requestLog("destination=" + dest + "&alert_group=" + g) {
			found = found || x["id"] == id
		}
		if !found {
			t.Errorf("the Destination and Alert Group do not list %s", id)
		}
		rec, text := e.requestRecord(id)
		headers, _ := rec["request_headers"].(map[string]any)
		auth, _ := headers["Authorization"].([]any)
		ua, _ := headers["User-Agent"].([]any)
		var sent, answered map[string]any
		_ = json.Unmarshal([]byte(rec["request_body"].(string)), &sent)
		_ = json.Unmarshal([]byte(rec["response_body"].(string)), &answered)
		if len(auth) != 1 || auth[0] != "[redacted]" || len(ua) != 1 || !strings.HasPrefix(ua[0].(string), "Muster/") ||
			sent["channel_id"] != fakemattermost.ChannelAlerts || answered["id"] == nil || answered["id"] == "" ||
			rec["response_headers"] == nil || rec["request_body_bytes"].(float64) <= 0 {
			t.Errorf("the record %s", text)
		}
		if strings.Contains(text, "mm-dev-token") {
			t.Error("the record holds the bot token")
		}
		actions := 0
		props, _ := sent["props"].(map[string]any)
		attachments, _ := props["attachments"].([]any)
		for _, a := range attachments {
			for _, x := range a.(map[string]any)["actions"].([]any) {
				ctx := x.(map[string]any)["integration"].(map[string]any)["context"].(map[string]any)
				if ctx["action"] != "[redacted]" {
					t.Errorf("an action id is stored as %v", ctx["action"])
				}
				actions++
			}
		}
		if actions == 0 {
			t.Errorf("the post has no buttons: %s", rec["request_body"])
		}
		t.Logf("%s: createPost %v, %d action ids masked", id, first["status"], actions)
	})

	t.Run("polls", func(t *testing.T) {
		e := e.on(t)
		tg := e.connectionNamed(devmode.TelegramConnectionName)
		botPath := "/bot" + devmode.TelegramConnectionBotToken + "/getUpdates"
		private := func(text string) {
			b, _ := json.Marshal(map[string]any{"token": devmode.TelegramConnectionBotToken,
				"from": map[string]any{"id": 7701, "username": "rl"}, "text": text})
			e.fake(http.MethodPost, h.Fakes.Telegram+"/_fake/private", string(b))
		}
		polls := func() []map[string]any { return ofOperation(e.requestLog("connection="+tg), "getUpdates") }
		// A poll that ends without updates is not recorded; the one that carries the update is, with its answer. The
		// fault answers the poll after the one in flight, which the update ends.
		waitInFlight(t, h.Fakes.Telegram, "/getUpdates")
		before, seen := len(polls()), countRequests(t, h.Fakes.Telegram, "/getUpdates")
		fakeFault(t, h.Fakes.Telegram, fakeserver.Fault{Path: botPath, Status: http.StatusOK,
			ContentType: "application/json", Body: `{"ok":true,"result":[]}`, Times: 1})
		private("/start " + startToken)
		eventually(t, "the poll with the update and the empty one", func() bool {
			return len(polls()) > before && countRequests(t, h.Fakes.Telegram, "/getUpdates") >= seen+2
		})
		got := polls()
		if len(got) != before+1 {
			t.Fatalf("%d getUpdates records after an update and an empty poll, want %d", len(got), before+1)
		}
		rec, text := e.requestRecord(got[0]["id"].(string))
		if !strings.Contains(rec["response_body"].(string), `/start [redacted]`) || strings.Contains(text, startToken) ||
			!strings.Contains(rec["url"].(string), "/bot[redacted]/getUpdates") || rec["status"] != 200.0 {
			t.Errorf("the poll with the update: %s", text)
		}
		// A failed poll is recorded, each attempt of the background class.
		waitInFlight(t, h.Fakes.Telegram, "/getUpdates")
		fakeFault(t, h.Fakes.Telegram, fakeserver.Fault{Path: botPath, Status: http.StatusInternalServerError,
			ContentType: "application/json", Body: `{"ok":false,"error_code":500,"description":"rl-down"}`, Times: 1})
		private("/start " + startToken)
		var failed map[string]any
		eventually(t, "the failed poll", func() bool {
			for _, p := range polls() {
				if p["status"] == 500.0 {
					failed = p
					return true
				}
			}
			return false
		})
		if failed["error_class"] != "transient" || failed["error_code"] != nil {
			t.Errorf("the failed poll = %v", failed)
		}
		all := e.requestLog("connection=" + tg)
		for _, x := range all {
			_, text := e.requestRecord(x["id"].(string))
			if strings.Contains(text, devmode.TelegramConnectionBotToken) || strings.Contains(text, "dev-telegram-token") {
				t.Errorf("the record %s holds the bot token", x["id"])
			}
		}

		// Bridge: a pollEvents without events is not recorded; one with the account_link event is, its token masked.
		// The poll in flight returns the event, and the fault answers the next one with the cursor after it.
		br := e.connectionNamed(devmode.BridgeConnectionName)
		waitInFlight(t, h.Fakes.Bridge, "/v1/events")
		var events []fakebridge.Event
		decode(t, call(t, http.MethodGet, h.Fakes.Bridge+"/_fake/events", ""), &events)
		next := 1
		for _, ev := range events {
			if ev.Bot <= 1 {
				next++
			}
		}
		bpolls := func() []map[string]any { return ofOperation(e.requestLog("connection="+br), "pollEvents") }
		bBefore, bSeen := len(bpolls()), countRequests(t, h.Fakes.Bridge, "/v1/events")
		fakeFault(t, h.Fakes.Bridge, fakeserver.Fault{Path: "/v1/events", Status: http.StatusOK,
			ContentType: "application/json", Body: `{"events":[],"next_cursor":"` + strconv.Itoa(next) + `"}`, Times: 1})
		b, _ := json.Marshal(fakebridge.Link{UserID: fakebridge.BobID, Token: startToken, Bot: 1})
		e.fake(http.MethodPost, h.Fakes.Bridge+"/_fake/link", string(b))
		eventually(t, "the poll with the event and the empty one", func() bool {
			return len(bpolls()) > bBefore && countRequests(t, h.Fakes.Bridge, "/v1/events") >= bSeen+2
		})
		got = bpolls()
		if len(got) != bBefore+1 {
			t.Fatalf("%d pollEvents records after an event and an empty poll, want %d", len(got), bBefore+1)
		}
		rec, text = e.requestRecord(got[0]["id"].(string))
		if !strings.Contains(rec["response_body"].(string), `"token":"[redacted]"`) || strings.Contains(text, startToken) ||
			strings.Contains(text, fakebridge.Token) {
			t.Errorf("the poll with the event: %s", text)
		}
		t.Logf("%d Telegram records, %d getUpdates; %d pollEvents", len(all), len(polls()), len(got))
	})

	t.Run("webhook", func(t *testing.T) {
		e := e.on(t)
		const secret = "rl-webhook-secret-0042"
		created := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"webhook","name":"rl-hook",
			"mode":"events","events":{"url":%q,"headers":[{"name":"X-Api-Key","value":"{{ .Secrets.key }}"}]},
			"proxy":{"enabled":false},"mentions":%s,"limiter":{"limit":50,"per_seconds":1}}`,
			webhookBase+"/hook/rl?k={{ .Secrets.key }}", mentionSettings(nil)), http.StatusCreated)
		hook := created["destination"].(map[string]any)["id"].(string)
		e.api.json(http.MethodPut, "/api/v1/destinations/"+hook+"/secrets/key", `{"value":"`+secret+`"}`, http.StatusOK)
		fakeFault(t, webhookBase, fakeserver.Fault{Path: "/hook/rl", Status: http.StatusInternalServerError,
			Body: "boom-rl", Times: 2})
		e.route("rl-hook", "rl-hook", []string{hook}, nil)
		e.group("rlh", "RequestLogHook")
		e.alert("rlh", "i1", `{"team":"rl-hook","cluster":"rlh"}`, "")
		e.notify("rlh", "first notification")
		g := e.groupOf("rlh")
		for n := 1; n <= 3; n++ {
			eventually(t, fmt.Sprintf("attempt %d of the event", n), func() bool {
				return len(e.requestLog("destination="+hook)) >= n
			})
			if n < 3 {
				advance(t, r, 30) // past delivery.transient_backoff
			}
		}
		got := e.requestLog("destination=" + hook)
		if len(got) != 3 || got[0]["status"] != 200.0 || got[0]["error_class"] != nil {
			t.Fatalf("the records of the outgoing webhook = %v", got)
		}
		for _, x := range got[1:] {
			rec, text := e.requestRecord(x["id"].(string))
			headers := rec["request_headers"].(map[string]any)
			ag, _ := rec["alert_group"].(map[string]any)
			if rec["status"] != 500.0 || rec["response_body"] != "boom-rl" || rec["error_class"] != "transient" ||
				rec["operation"] != "event" || rec["attempt"] != 1.0 || ag["id"] != g ||
				!strings.Contains(rec["url"].(string), "k=[redacted]") || strings.Contains(text, secret) ||
				fmt.Sprint(headers["X-Api-Key"]) != "[[redacted]]" ||
				fmt.Sprint(headers["webhook-signature"]) != "[[redacted]]" {
				t.Errorf("a failed attempt: %s", text)
			}
		}

		// A request that got no answer has no status, the code of the failure and the masked error.
		dead := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"webhook","name":"rl-dead",
			"mode":"events","events":{"url":"http://127.0.0.1:1/x","headers":[]},"proxy":{"enabled":false},
			"mentions":%s,"limiter":{"limit":50,"per_seconds":1}}`, mentionSettings(nil)),
			http.StatusCreated)["destination"].(map[string]any)["id"].(string)
		e.route("rl-dead", "rl-dead", []string{dead}, nil)
		e.group("rld", "RequestLogDead")
		e.alert("rld", "i1", `{"team":"rl-dead","cluster":"rld"}`, "")
		e.notify("rld", "first notification")
		var last []map[string]any
		eventually(t, "the attempt without an answer", func() bool {
			last = e.requestLog("destination=" + dead)
			return len(last) > 0
		})
		rec, text := e.requestRecord(last[len(last)-1]["id"].(string))
		if rec["status"] != nil || rec["error_code"] != "connect_failed" || rec["error_class"] != "transient" ||
			rec["error"] == nil || rec["error"] == "" || rec["response_body"] != nil || rec["response_headers"] != nil {
			t.Errorf("the attempt without an answer: %s", text)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		e := e.on(t)
		for _, q := range []string{"", "?connection=CN0000000000AA&destination=DS0000000000AA"} {
			a := e.api.do(http.MethodGet, "/api/v1/request-log"+q, "")
			if a.status != http.StatusUnprocessableEntity || errorCode(t, a) != "one_of_required" {
				t.Errorf("%q = %d %s", q, a.status, a.body)
			}
		}
		if a := e.api.do(http.MethodGet, "/api/v1/request-log?connection=CN0000000000AA", ""); a.status !=
			http.StatusNotFound {
			t.Errorf("an unknown Connection = %d %s", a.status, a.body)
		}
		gone := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"webhook","name":"rl-gone",
			"mode":"events","events":{"url":%q,"headers":[]},"proxy":{"enabled":false},"mentions":%s,
			"limiter":{"limit":50,"per_seconds":1}}`, webhookBase+"/hook/gone", mentionSettings(nil)),
			http.StatusCreated)["destination"].(map[string]any)["id"].(string)
		if a := e.api.do(http.MethodGet, "/api/v1/request-log?destination="+gone, ""); a.status != http.StatusOK {
			t.Errorf("a Destination without records = %d %s", a.status, a.body)
		}
		if a := e.api.do(http.MethodDelete, "/api/v1/destinations/"+gone, ""); a.status != http.StatusNoContent {
			t.Fatalf("delete = %d %s", a.status, a.body)
		}
		if a := e.api.do(http.MethodGet, "/api/v1/request-log?destination="+gone, ""); a.status != http.StatusNotFound {
			t.Errorf("a deleted Destination = %d %s", a.status, a.body)
		}
		if a := e.api.do(http.MethodGet, "/api/v1/request-log/RQ0000000000AA", ""); a.status != http.StatusNotFound {
			t.Errorf("an unknown record = %d %s", a.status, a.body)
		}
	})

	t.Run("bounds", func(t *testing.T) {
		e := e.on(t)
		conn := e.api.json(http.MethodPost, "/api/v1/connections", fmt.Sprintf(`{"type":"mattermost","name":"rl-cap",
			"server_url":%q,"bot_token":"rl-cap-token","proxy":{"enabled":false},"limiter":{"limit":5,"per_seconds":1}}`,
			h.Fakes.Mattermost), http.StatusCreated)["id"].(string)
		now := readClock(t, r).Now.UTC().Format("2006-01-02T15:04:05.000000Z")
		h.exec(t, `INSERT INTO request_log (org_id, public_id, at, direction, operation, method, url, request_headers,
			request_body, request_body_bytes, request_body_truncated, response_body_bytes, response_body_truncated,
			latency_ms, via, attempt, connection_id)
			SELECT c.org_id, 'RQ' || lpad(i::text, 12, '0'), '`+now+`'::timestamptz - make_interval(secs => i), 'outbound',
				'getMe', 'GET', 'http://rl-cap.example.org/api/v4/users/me', '{}'::jsonb, '', 0, false, 0, false, 1,
				'direct', 1, c.id
			FROM connections c, generate_series(1, 510) i WHERE c.public_id = '`+conn+`'`)
		stored := func() int64 {
			return h.count(t, `SELECT count(*) FROM request_log r JOIN connections c ON c.id = r.connection_id
				WHERE c.public_id = '`+conn+`'`)
		}
		if n := stored(); n < 510 {
			t.Fatalf("%d records inserted", n)
		}
		if n := len(e.requestLog("connection=" + conn)); n != 500 {
			t.Errorf("the list over all pages has %d records, want 500", n)
		}
		pruned := len(logLines(r, "request_log_pruned"))
		advance(t, r, 1) // wakes the Leader's request_log_pruning
		eventually(t, "the pruning to the count bound", func() bool { return stored() == 500 })

		// Past request_log.max_age no record is listed, and the pruning, woken by the move, deletes them.
		advance(t, r, 8*24*3600)
		if n := len(e.requestLog("connection=" + conn)); n != 0 {
			t.Errorf("%d records listed after eight days", n)
		}
		eventually(t, "the pruning to the age bound", func() bool { return stored() == 0 })
		eventually(t, "request_log_pruned", func() bool { return len(logLines(r, "request_log_pruned")) > pruned })
		if v := metricValue(t, r, `muster_request_log_records_total{direction="outbound"}`); v == 0 {
			t.Error("no record counted")
		}
		if v := metricValue(t, r, "muster_request_log_pruned_total"); v < 10 {
			t.Errorf("%d records counted as pruned", v)
		}
	})
}
