// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/groups"
)

// webhookBase is the fake receiver of outgoing webhooks that `muster dev` starts; the development database allows the
// loopback network, so Muster may call it.
const webhookBase = "http://" + devmode.WebhookAddr

// webhookRecord is a request the fake receiver recorded, with the version 1 body of an events-mode request.
type webhookRecord struct {
	Status          int               `json:"status"`
	WebhookID       string            `json:"webhook_id"`
	SignaturesValid int               `json:"signatures_valid"`
	Headers         map[string]string `json:"headers"`
	Body            struct {
		Version    int    `json:"version"`
		Event      string `json:"event"`
		Notify     bool   `json:"notify"`
		Sequence   int64  `json:"sequence"`
		AlertGroup struct {
			ID string `json:"id"`
		} `json:"alert_group"`
	} `json:"body"`
}

// webhook creates an outgoing webhook Destination in the mode events that posts to /hook/{hook} of the fake receiver
// with the headers, a JSON array of HeaderTemplate, and registers its Signing secret, shown once, with the fake. It
// returns the Destination as created.
func (e *mm) webhook(name, hook, headers string) map[string]any {
	e.t.Helper()
	created := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"webhook","name":%q,
		"mode":"events","events":{"url":%q,"headers":%s},"proxy":{"enabled":false},"mentions":%s,
		"limiter":{"limit":50,"per_seconds":1}}`, name, webhookBase+"/hook/"+hook, headers, mentionSettings(nil)),
		http.StatusCreated)
	secret, _ := created["signing_secret"].(string)
	if !strings.HasPrefix(secret, "whsec_") {
		e.t.Fatalf("signing secret %q of %s", secret, name)
	}
	b, _ := json.Marshal([]string{secret})
	e.fake(http.MethodPut, webhookBase+"/_fake/secrets/"+hook, string(b))
	return created["destination"].(map[string]any)
}

// received are the requests the fake receiver recorded for hook, in arrival order.
func (e *mm) received(hook string) []webhookRecord {
	e.t.Helper()
	var out []webhookRecord
	decode(e.t, call(e.t, http.MethodGet, webhookBase+"/_fake/received/"+hook, ""), &out)
	return out
}

// lifecycleEntry is a Timeline entry of a lifecycle event.
type lifecycleEntry struct {
	Kind     string `json:"kind"`
	Event    string `json:"event"`
	Loudness string `json:"loudness"`
}

// lifecycle are the lifecycle event entries of the Alert Group's Timeline, oldest first.
func (e *mm) lifecycle(id string) []lifecycleEntry {
	e.t.Helper()
	var page struct {
		Items []lifecycleEntry `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+id+"/timeline?limit=200", ""), &page)
	var out []lifecycleEntry
	for _, x := range slices.Backward(page.Items) {
		if x.Kind != "delivery" && x.Event != "" {
			out = append(out, x)
		}
	}
	return out
}

func lifecycleEvents() map[string]bool {
	out := map[string]bool{}
	for _, row := range groups.Table {
		out[string(row.Event)] = true
	}
	return out
}

// TestWebhookEvents is the end-to-end check of the outgoing webhook events mode against `muster dev` and its fake
// receiver: 30 new Alert Groups on a Route with the Storm threshold 20 send 30 signed created events while the
// Mattermost Destination of the Route gets one Storm summary (C-15.AC-10, C-11.FR-6); one Alert Group driven through
// the lifecycle events the API and the fake Alertmanager reach sends exactly one request per lifecycle entry of its
// Timeline and no delivery event (C-15.AC-11, C-15.FR-2); and a literal Authorization header gives the warning
// literal_credential (C-15.FR-10, C-15.AC-8).
func TestWebhookEvents(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)

	t.Run("storm", func(t *testing.T) {
		e := e.on(t)
		conn := e.connection(50)
		mmDest := e.destination("storm-mm", conn, fakemattermost.ChannelAlertsProd, nil, 50)
		hook := e.webhook("storm-events", "storm", `[]`)
		e.route("wh-storm", "wh-storm", []string{mmDest, hook["id"].(string)},
			func(p map[string]any) { p["storm_threshold"] = 20 })
		e.group("ws", "WebhookSurge")
		for i := 1; i <= 30; i++ {
			e.alert("ws", fmt.Sprintf("s%02d", i), fmt.Sprintf(`{"team":"wh-storm","cluster":"ws%02d"}`, i), "")
		}
		e.notify("ws", "first notification")
		ids := map[string]bool{}
		for i := 1; i <= 30; i++ {
			ids[e.groupOf(fmt.Sprintf("ws%02d", i))] = true
		}
		if len(ids) != 30 {
			t.Fatalf("%d alert groups, want 30", len(ids))
		}

		eventually(t, "30 created events", func() bool { return len(e.received("storm")) >= 30 })
		eventually(t, "the webhook events to settle", func() bool {
			return h.count(t, `SELECT count(*) FROM webhook_events WHERE state = 'pending'`) == 0
		})
		got := e.received("storm")
		seen := map[string]bool{}
		for _, x := range got {
			if x.Body.Event != "created" || x.Body.Version != 1 || !x.Body.Notify || x.Status != http.StatusOK ||
				x.SignaturesValid != 1 || !ids[x.Body.AlertGroup.ID] {
				t.Errorf("event %s of %s: version %d, notify %v, status %d, %d valid signatures", x.Body.Event,
					x.Body.AlertGroup.ID, x.Body.Version, x.Body.Notify, x.Status, x.SignaturesValid)
			}
			seen[x.Body.AlertGroup.ID] = true
		}
		if len(got) != 30 || len(seen) != 30 {
			t.Errorf("%d requests for %d alert groups, want 30 created events of 30 alert groups", len(got), len(seen))
		}

		roots := func() (published, summaries int) {
			for _, p := range e.posts() {
				if p.ChannelID != fakemattermost.ChannelAlertsProd || p.RootID != "" {
					continue
				}
				if strings.HasPrefix(p.Message, "⛈ Storm on") || strings.HasPrefix(p.Message, "Storm over") {
					summaries++
				} else {
					published++
				}
			}
			return published, summaries
		}
		eventually(t, "the storm summary and the root messages before the threshold", func() bool {
			published, summaries := roots()
			return summaries == 1 && published >= 20
		})
		published, summaries := roots()
		if published != 20 || summaries != 1 {
			t.Errorf("%d root messages and %d storm summaries in the channel, want 20 and 1", published, summaries)
		}
		t.Logf("storm: %d created events, %d root messages and %d storm summary in Mattermost", len(got), published,
			summaries)
	})

	t.Run("one request per lifecycle event", func(t *testing.T) {
		e := e.on(t)
		hook := e.webhook("life-events", "life", `[]`)
		e.route("wh-life", "wh-life", []string{hook["id"].(string)}, nil)
		bob := webhookResponder(t, e, r)
		labels := func(pod, severity string) string {
			return `{"team":"wh-life","cluster":"life","pod":"` + pod + `","severity":"` + severity + `"}`
		}

		// created; acknowledged, takeover and unacknowledged through Commands; a Note.
		e.group("wl", "WebhookLifecycle")
		e.alert("wl", "i1", labels("i1", "info"), `,"annotations":{"summary":"first"}`)
		e.notify("wl", "first notification")
		g := e.groupOf("life")
		e.command(g, "acknowledge")
		bob.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/acknowledge", "", http.StatusOK)
		bob.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/unacknowledge", "", http.StatusOK)
		e.api.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/notes", `{"body":"looking into it"}`,
			http.StatusCreated)

		// alerts_added and severity_raised; annotations_changed; alert_continued; alert_replaced; alert_resolved.
		e.alert("wl", "i2", labels("i2", "info"), "")
		e.alert("wl", "i3", labels("i3", "warning"), "")
		e.notify("wl", "new alerts added")
		e.alert("wl", "i1", labels("i1", "info"), `,"annotations":{"summary":"second"}`)
		e.notify("wl", "repeat interval elapsed")
		e.alert("wl", "i1", labels("i1", "info"), `,"annotations":{"summary":"second"},"starts_at":"now"`)
		e.notify("wl", "repeat interval elapsed")
		e.fake(http.MethodDelete, e.fam+"/groups/wl/alerts/i2", "")
		e.alert("wl", "i2b", labels("i2b", "info"), "")
		e.notify("wl", "new alerts added")
		e.alert("wl", "i3", labels("i3", "warning"), `,"status":"resolved"`)
		e.notify("wl", "some alerts resolved")

		// snoozed and unsnoozed; snoozed again until a time the development clock passes, for snooze_ended.
		e.api.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/snooze", `{"no_end":true}`, http.StatusOK)
		e.command(g, "unsnooze")
		until := readClock(t, r).Now.Add(10 * time.Minute).UTC().Format(time.RFC3339)
		e.api.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/snooze", `{"until":"`+until+`"}`, http.StatusOK)
		advance(t, r, 660)
		eventually(t, "the snooze to end", func() bool { return e.alertGroup(g)["status"] != "snoozed" })

		// resolved and unresolved through Commands; urgency_raised; resolved by the Alerts, then reopened.
		e.command(g, "resolve")
		e.command(g, "unresolve")
		e.alert("wl", "i4", labels("i4", "critical"), "")
		e.notify("wl", "new alerts added")
		for pod, severity := range map[string]string{"i1": "info", "i2b": "info", "i4": "critical"} {
			e.alert("wl", pod, labels(pod, severity), `,"status":"resolved"`)
		}
		e.notify("wl", "some alerts resolved")
		if s := e.alertGroup(g)["status"]; s != "resolved" {
			t.Fatalf("status after the alerts resolved = %v", s)
		}
		e.alert("wl", "i1", labels("i1", "info"), `,"status":"firing","starts_at":"now"`)
		e.notify("wl", "new alerts added")
		if s := e.alertGroup(g)["status"]; s != "firing" {
			t.Fatalf("status after the reopen = %v", s)
		}

		entries := e.lifecycle(g)
		var timeline []string
		for _, x := range entries {
			timeline = append(timeline, x.Event)
		}
		for _, want := range []string{"created", "acknowledged", "takeover", "unacknowledged", "note_added",
			"alerts_added", "severity_raised", "annotations_changed", "alert_continued", "alert_replaced",
			"alert_resolved", "snoozed", "unsnoozed", "snooze_ended", "resolved", "unresolved", "urgency_raised",
			"reopened"} {
			if !slices.Contains(timeline, want) {
				t.Errorf("the timeline has no %s: %v", want, timeline)
			}
		}
		eventually(t, "one request per lifecycle event", func() bool { return len(e.received("life")) >= len(entries) })
		eventually(t, "the webhook events to settle", func() bool {
			return h.count(t, `SELECT count(*) FROM webhook_events WHERE state = 'pending'`) == 0
		})
		got := e.received("life")
		var sent []string
		for _, x := range got {
			sent = append(sent, x.Body.Event)
		}
		if !slices.Equal(sent, timeline) {
			t.Fatalf("sent %v\nfor the timeline %v", sent, timeline)
		}
		known := lifecycleEvents()
		ids := map[string]bool{}
		for i, x := range got {
			want := entries[i]
			if x.Body.Notify != (want.Loudness == "loud") || x.Body.Version != 1 || x.Body.AlertGroup.ID != g ||
				x.Status != http.StatusOK || x.SignaturesValid != 1 || !known[x.Body.Event] {
				t.Errorf("request %d (%s, %s): notify %v, version %d, alert group %s, status %d, %d valid signatures", i,
					want.Event, want.Loudness, x.Body.Notify, x.Body.Version, x.Body.AlertGroup.ID, x.Status,
					x.SignaturesValid)
			}
			if i > 0 && x.Body.Sequence <= got[i-1].Body.Sequence {
				t.Errorf("request %d (%s) has the sequence %d after %d", i, x.Body.Event, x.Body.Sequence,
					got[i-1].Body.Sequence)
			}
			if x.WebhookID == "" || ids[x.WebhookID] || x.Headers["webhook-id"] != x.WebhookID {
				t.Errorf("request %d (%s) has the webhook id %q", i, x.Body.Event, x.WebhookID)
			}
			ids[x.WebhookID] = true
		}
		var delivery []string
		var page struct {
			Items []struct {
				DeliveryEvent string `json:"delivery_event"`
			} `json:"items"`
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?kind=delivery&limit=200", ""), &page)
		for _, x := range page.Items {
			delivery = append(delivery, x.DeliveryEvent)
		}
		for _, x := range got {
			if slices.Contains(delivery, x.Body.Event) {
				t.Errorf("a delivery event was sent: %s", x.Body.Event)
			}
		}
		t.Logf("lifecycle: %d requests for %v", len(got), sent)
	})

	t.Run("literal credential", func(t *testing.T) {
		e := e.on(t)
		literal := e.webhook("literal", "literal", `[{"name":"Authorization","value":"Bearer abc"}]`)
		warnings, _ := literal["warnings"].([]any)
		if len(warnings) != 1 {
			t.Fatalf("warnings %v, want one literal_credential", literal["warnings"])
		}
		if w := warnings[0].(map[string]any); w["kind"] != "literal_credential" || w["field"] != "/events/headers/0/value" {
			t.Errorf("warning %v", w)
		}
		referenced := e.webhook("referenced", "referenced",
			`[{"name":"Authorization","value":"Bearer {{ .Secrets.token }}"}]`)
		if warnings, _ := referenced["warnings"].([]any); len(warnings) != 0 {
			t.Errorf("a header with a Secret reference has the warnings %v", warnings)
		}
		read := e.api.json(http.MethodGet, "/api/v1/destinations/"+literal["id"].(string), "", http.StatusOK)
		if warnings, _ := read["warnings"].([]any); len(warnings) != 1 {
			t.Errorf("the destination as read has the warnings %v", read["warnings"])
		}
	})
}

// webhookResponder creates the Responder bob, sets his password and signs him in.
func webhookResponder(t *testing.T, e *mm, r *Replica) *agent {
	t.Helper()
	created := e.api.json(http.MethodPost, "/api/v1/users", `{"name":"bob","login":"bob","role":"responder"}`,
		http.StatusCreated)
	link, err := url.Parse(created["password_setup_link"].(map[string]any)["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	setup, _ := strings.CutPrefix(link.Fragment, "token=")
	if a := call(t, http.MethodPost, r.App+"/api/v1/password-setups", `{"token":"`+setup+`","password":"bob-password-1"}`,
		"Content-Type", "application/json"); a.status != http.StatusNoContent {
		t.Fatalf("password setup = %d %s", a.status, a.body)
	}
	bob := newAgent(t, r.App)
	if a := bob.signIn("bob", "bob-password-1"); a.status != http.StatusCreated {
		t.Fatalf("bob = %d %s", a.status, a.body)
	}
	return bob
}
