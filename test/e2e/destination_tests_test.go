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

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// destTestStep is a step of a Destination test as the API answers it.
type destTestStep struct {
	Name           string            `json:"name"`
	ErrorClass     string            `json:"error_class"`
	Error          *string           `json:"error"`
	ResponseStatus *int              `json:"response_status"`
	Extracted      map[string]string `json:"extracted"`
	Request        *struct {
		URL     string `json:"url"`
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
		Body *string `json:"body"`
	} `json:"request"`
}

type destTestResult struct {
	Steps  []destTestStep `json:"steps"`
	Health health         `json:"health"`
}

// destTest is testDestination of dest from source, a TestSource; it fails unless the answer is 200.
func (e *mm) destTest(dest, source string) destTestResult {
	e.t.Helper()
	var res destTestResult
	a := e.api.do(http.MethodPost, "/api/v1/destinations/"+dest+"/tests", `{"source":`+source+`}`)
	if a.status != http.StatusOK {
		e.t.Fatalf("test of %s = %d %s", dest, a.status, a.body)
	}
	decode(e.t, a, &res)
	return res
}

func classes(r destTestResult) string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Name+":"+s.ErrorClass)
	}
	return strings.Join(out, ",")
}

func header(s destTestStep, name string) string {
	if s.Request == nil {
		return ""
	}
	for _, h := range s.Request.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

const example = `{"kind":"example"}`

// TestDestinationTests is the end-to-end check of Destination tests and previews against `muster dev` and its fake
// Mattermost, Telegram and webhook servers (C-16, S-047): the Mattermost test message and the bot's press of it, with
// and without the address allowed (AC-1, AC-7); a person's press of a test message (AC-4); the Telegram test message
// and a press of it; the test event (AC-6); the template mode with extracted values and masked Secrets (AC-3); the
// mode both; an unreachable endpoint within the budget (AC-2); the class limited; a test from a recent Alert Group of
// the Destination's Route; the end of a Broken state (AC-5); previews (FR-4); the Audit log (FR-5); and the probe at once
// after saving a Broken outgoing webhook.
func TestDestinationTests(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	conn := e.connection(50)
	md := e.destination("alerts", conn, fakemattermost.ChannelAlerts, nil, 50)
	e.route("db", "db", []string{md}, nil)

	openGroups := func() int64 { return h.count(t, `SELECT count(*) FROM alert_groups`) }
	timeline := func() int64 { return h.count(t, `SELECT count(*) FROM timeline_entries`) }

	t.Run("mattermost", func(t *testing.T) {
		e := e.on(t)
		groups, entries := openGroups(), timeline()
		res := e.destTest(md, example)
		if classes(res) != "message:none,press:none" || res.Health.State != "healthy" || res.Steps[1].Error != nil {
			t.Fatalf("test %+v", res)
		}
		posts := e.posts()
		last := posts[len(posts)-1]
		if title := last.attachment(t).Title; !strings.HasPrefix(title, "🧪 Test message") {
			t.Errorf("title %q", title)
		}
		for _, x := range e.ephemeral() {
			if strings.Contains(x.Message, "test message") {
				t.Errorf("the bot's press was answered: %+v", x)
			}
		}
		if header(res.Steps[0], "Authorization") != "Bearer [redacted]" ||
			strings.Contains(*res.Steps[0].Request.Body, "mm-dev-token") {
			t.Errorf("request %+v", res.Steps[0].Request)
		}
		if openGroups() != groups || timeline() != entries ||
			h.count(t, `SELECT count(*) FROM deliveries`) != 0 {
			t.Errorf("a test changed Alert Groups or deliveries")
		}
		// The server does not allow Muster's address: the press does not reach Muster.
		e.fake(http.MethodPut, e.fmm+"/config", `{"allowed_untrusted_internal_connections":""}`)
		res = e.destTest(md, example)
		// MUSTER_INGEST_URL of `muster dev` names the ingest listener by localhost.
		ingest := strings.Replace(strings.TrimSuffix(r.Ingest, "/"), "127.0.0.1", "localhost", 1)
		want := "The button press did not reach Muster. Add the host of " + ingest +
			" to ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server."
		if len(res.Steps) != 2 || res.Steps[1].ErrorClass != "unknown" || res.Steps[1].Error == nil ||
			*res.Steps[1].Error != want {
			t.Errorf("not allowed %+v, want %q", res.Steps, want)
		}
		e.fake(http.MethodPut, e.fmm+"/config", `{"allowed_untrusted_internal_connections":"`+
			devmode.AllowedInternalConnections+`"}`)
		// A person presses the test message.
		posts = e.posts()
		pressed := e.press(posts[len(posts)-1].ID, "ack", fakemattermost.AliceUserID)
		eph := e.ephemeral()
		if pressed.PersonStatus != http.StatusOK || len(eph) == 0 ||
			eph[len(eph)-1].Message != "This is a test message; nothing was changed" || openGroups() != groups {
			t.Errorf("person press %+v %+v", pressed, eph)
		}
	})

	t.Run("telegram", func(t *testing.T) {
		tg := &tgd{mm: e.on(t), ftg: h.Fakes.Telegram + "/_fake"}
		for _, item := range tg.api.json(http.MethodGet, "/api/v1/connections?type=telegram", "",
			http.StatusOK)["items"].([]any) {
			if c := item.(map[string]any); c["name"] == devmode.TelegramConnectionName {
				tg.conn = c["id"].(string)
			}
		}
		tg.member(faketelegram.GroupID, faketelegram.Member{Status: faketelegram.StatusAdministrator,
			CanPostMessages: true})
		a := tg.create("alerts-tg", "@muster_alerts", 30)
		if a.status != http.StatusCreated {
			t.Fatalf("create = %d %s", a.status, a.body)
		}
		var created struct {
			Destination struct {
				ID string `json:"id"`
			} `json:"destination"`
		}
		decode(t, a, &created)
		res := tg.destTest(created.Destination.ID, example)
		if classes(res) != "message:none" || strings.Contains(res.Steps[0].Request.URL, "dev-telegram") {
			t.Fatalf("test %+v", res)
		}
		msgs := tg.messages(faketelegram.ChannelID)
		m := msgs[len(msgs)-1]
		if !strings.HasPrefix(m.Text, "🧪 Test message") {
			t.Errorf("text %q", m.Text)
		}
		if ans := tg.press(m.ID, 5001, "Ack", ""); ans.Text != "This is a test message; nothing was changed" {
			t.Errorf("answer %+v", ans)
		}
	})

	t.Run("webhook events", func(t *testing.T) {
		e := e.on(t)
		wd := e.webhook("auto", "auto", `[]`)["id"].(string)
		res := e.destTest(wd, example)
		if classes(res) != "event:none" || res.Steps[0].ResponseStatus == nil || *res.Steps[0].ResponseStatus != 200 {
			t.Fatalf("test %+v", res)
		}
		var got []struct {
			SignaturesValid int `json:"signatures_valid"`
			Body            struct {
				Event    string `json:"event"`
				Test     bool   `json:"test"`
				Version  int    `json:"version"`
				Sequence int    `json:"sequence"`
			} `json:"body"`
		}
		decode(t, call(t, http.MethodGet, webhookBase+"/_fake/received/auto", ""), &got)
		if len(got) != 1 || got[0].Body.Event != "test" || !got[0].Body.Test || got[0].Body.Version != 1 ||
			got[0].Body.Sequence != 0 || got[0].SignaturesValid != 1 {
			t.Errorf("received %+v", got)
		}
		if h.count(t, `SELECT count(*) FROM webhook_events`) != 0 {
			t.Error("the test event was queued")
		}
	})

	cd := e.templateWebhook("chat", "template", chatTemplate("chat", true), "", nil)

	t.Run("webhook template", func(t *testing.T) {
		e := e.on(t)
		res := e.destTest(cd, example)
		if classes(res) != "create:none" || res.Steps[0].Extracted["id"] == "" ||
			header(res.Steps[0], "Authorization") != "Bearer [redacted]" {
			t.Fatalf("test %+v", res)
		}
		b, _ := json.Marshal(res)
		if strings.Contains(string(b), "chat-token-value") {
			t.Errorf("the Secret shows: %s", b)
		}
		both := e.templateWebhook("both", "both", chatTemplate("both", false), "both", nil)
		if res := e.destTest(both, example); classes(res) != "event:none,create:none" {
			t.Errorf("both %+v", res)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		e := e.on(t)
		down := e.api.json(http.MethodPost, "/api/v1/destinations", `{"type":"webhook","name":"down","mode":"events",
			"events":{"url":"http://10.255.255.1/hook","headers":[]},"proxy":{"enabled":false},"mentions":`+
			mentionSettings(nil)+`,"limiter":{"limit":5,"per_seconds":1}}`, http.StatusCreated)["destination"].(map[string]any)["id"].(string)
		start := time.Now()
		res := e.destTest(down, example)
		if took := time.Since(start); classes(res) != "event:transient" || took > 5500*time.Millisecond {
			t.Errorf("unreachable %+v after %v", res, took)
		}
	})

	t.Run("limited", func(t *testing.T) {
		e := e.on(t)
		lim := e.api.json(http.MethodPost, "/api/v1/destinations", `{"type":"webhook","name":"lim","mode":"events",
			"events":{"url":"`+webhookBase+`/hook/lim","headers":[]},"proxy":{"enabled":false},"mentions":`+
			mentionSettings(nil)+`,"limiter":{"limit":1,"per_seconds":60}}`, http.StatusCreated)["destination"].(map[string]any)["id"].(string)
		e.route("lim", "lim", []string{lim}, nil)
		e.group("l1", "Limited")
		e.alert("l1", "a", `{"team":"lim","cluster":"l"}`, "")
		e.notify("l1", "first notification")
		eventually(t, "the created event", func() bool { return len(e.received("lim")) == 1 })
		res := e.destTest(lim, example)
		if classes(res) != "event:limited" || len(e.received("lim")) != 1 {
			t.Errorf("limited %+v", res)
		}
	})

	t.Run("alert group source", func(t *testing.T) {
		e := e.on(t)
		e.group("d1", "DiskFull")
		e.alert("d1", "k0", `{"team":"db","cluster":"k"}`, "")
		e.notify("d1", "first notification")
		g := e.groupOf("k")
		res := e.destTest(md, `{"kind":"alert_group","alert_group_id":"`+g+`"}`)
		posts := e.posts()
		if classes(res) != "message:none,press:none" ||
			!strings.Contains(posts[len(posts)-1].attachment(t).Title, fmt.Sprintf("#%d", e.number(g))) {
			t.Errorf("alert group %+v", res)
		}
		other := e.groupOf("l")
		if a := e.api.do(http.MethodPost, "/api/v1/destinations/"+md+"/tests",
			`{"source":{"kind":"alert_group","alert_group_id":"`+other+`"}}`); a.status != http.StatusUnprocessableEntity ||
			!strings.Contains(string(a.body), "unknown_id") {
			t.Errorf("another route = %d %s", a.status, a.body)
		}
	})

	t.Run("broken", func(t *testing.T) {
		e := e.on(t)
		member := e.fmm + "/channels/" + fakemattermost.ChannelAlerts + "/members/" + fakemattermost.BotUserID
		e.fake(http.MethodDelete, member, "")
		if res := e.destTest(md, example); classes(res) != "message:fatal" || res.Health.State != "healthy" {
			t.Errorf("failed test %+v", res)
		}
		e.alert("d1", "k1", `{"team":"db","cluster":"k1"}`, "")
		e.notify("d1", "new alerts added")
		e.waitHealth(md, "broken")
		if res := e.destTest(md, example); res.Health.State != "broken" {
			t.Errorf("a failed test changed the health %+v", res)
		}
		e.fake(http.MethodPut, member, "")
		if res := e.destTest(md, example); res.Health.State != "healthy" {
			t.Errorf("passing test %+v", res)
		}
		eventually(t, "MusterDestinationBroken resolved", func() bool {
			return !slices.Contains(e.internalAlerts(), "MusterDestinationBroken")
		})
	})

	t.Run("previews", func(t *testing.T) {
		e := e.on(t)
		before := len(e.posts())
		var p struct {
			Items []struct {
				Name   string  `json:"name"`
				Format *string `json:"format"`
			} `json:"items"`
		}
		decode(t, e.api.do(http.MethodPost, "/api/v1/destinations/"+md+"/previews", `{"source":`+example+`}`), &p)
		if len(p.Items) != 1 || p.Items[0].Name != "message" || *p.Items[0].Format != "markdown" {
			t.Errorf("mattermost %+v", p)
		}
		a := e.api.do(http.MethodPost, "/api/v1/destinations/"+cd+"/previews", `{"source":`+example+`}`)
		if strings.Contains(string(a.body), "chat-token-value") || !strings.Contains(string(a.body), "Bearer [redacted]") {
			t.Errorf("the Secret is not masked: %s", a.body)
		}
		decode(t, a, &p)
		var names []string
		for _, it := range p.Items {
			names = append(names, it.Name)
		}
		if !slices.Equal(names, []string{"create", "update", "open_thread", "reply_in_thread"}) {
			t.Errorf("template %v", names)
		}
		if len(e.posts()) != before {
			t.Error("a preview sent something")
		}
	})

	t.Run("audit log", func(t *testing.T) {
		e := e.on(t)
		page := e.api.json(http.MethodGet, "/api/v1/audit-log?action=destination.tested&limit=1", "", http.StatusOK)
		items := page["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["action"] != "destination.tested" {
			t.Errorf("audit %+v", page)
		}
	})

	t.Run("probe after save", func(t *testing.T) {
		e := e.on(t)
		created := e.webhook("fix", "fix", `[]`)
		fix := created["id"].(string)
		e.route("fix", "fix", []string{fix}, nil)
		webhookFault(t, `{"path":"/hook/fix","status":404}`)
		defer resetWebhookFaults(t)
		e.group("f1", "Fix")
		e.alert("f1", "a", `{"team":"fix","cluster":"f"}`, "")
		e.notify("f1", "first notification")
		e.waitHealth(fix, "broken")
		resetWebhookFaults(t)
		a := e.api.do(http.MethodGet, "/api/v1/destinations/"+fix, "")
		body := `{"type":"webhook","name":"fix","mode":"events","events":{"url":"` + webhookBase +
			`/hook/fix","headers":[]},"proxy":{"enabled":false},"mentions":` + mentionSettings(nil) +
			`,"limiter":{"limit":50,"per_seconds":1}}`
		e.api.json(http.MethodPut, "/api/v1/destinations/"+fix, body, http.StatusOK, "If-Match", a.header.Get("ETag"))
		e.waitHealth(fix, "healthy")
	})
}
