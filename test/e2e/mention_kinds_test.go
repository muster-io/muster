// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
)

// mentionKind is an entry of a Bridge envelope's mentions as the fake bridge recorded it, its kind null when Muster
// sent none.
type mentionKind struct {
	Kind       *string `json:"kind"`
	ExternalID string  `json:"external_id"`
}

func (m mentionKind) String() string {
	k := "-"
	if m.Kind != nil {
		k = *m.Kind
	}
	return k + ":" + m.ExternalID
}

// TestMentionKinds is S-171 against `muster dev`, as its Verification: the Team payments has the Team mentions
// payments-team (S0FAKEPAYTEAM) for the demo Slack Connection, whose check found usergroups:read, and payments_team for
// the demo Bridge Connection, whose fake bridge declares group mentions and here; a Slack Destination and a Bridge
// Destination mention the Team from the label, a group entry and here for a new Alert Group and new Alerts. Slack
// writes the Team mention and the user group as <!subteam^ID> with <!here> in the first line of the Root message and
// of the Thread reply, and never <!everyone> (C-24.FR-6, C-25.FR-8, C-28.FR-2); the bridge receives the group entry,
// the Team mention and here as entries of mentions of the kind group or everyone, each with its marker in the body,
// and the Admin linked through the bridge as an entry without a kind, a user (C-22.FR-16). Then the negative cases: the
// bridge declares no group mentions and no here, and its next envelope carries the person alone, with no kind; the
// Slack app loses usergroups:read, and its next message carries <!here> and no <!subteam^…>.
func TestMentionKinds(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	conns := map[string]string{}
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
		c := item.(map[string]any)
		conns[c["name"].(string)] = c["id"].(string)
	}
	cs, cb := conns[devmode.SlackConnectionName], conns[devmode.BridgeConnectionName]
	fsl := h.Fakes.Slack + "/_fake"
	t.Cleanup(func() { restore(t, http.MethodPut, fsl+"/config", `{"scopes":{"a":null}}`) })
	const withGroups = `{"scopes":{"a":"chat:write,channels:read,groups:read,im:write,users:read,users:read.email,` +
		`usergroups:read"}}`
	e.fake(http.MethodPut, fsl+"/config", withGroups)
	e.api.json(http.MethodPost, "/api/v1/connections/"+cs+"/checks", "", http.StatusOK)
	declare := func(groups bool, everyone []string) {
		t.Helper()
		var caps map[string]any
		def, _ := json.Marshal(fakebridge.DefaultConfig().Capabilities)
		_ = json.Unmarshal(def, &caps)
		caps["group_mentions"], caps["everyone_mentions"] = groups, everyone
		b, _ := json.Marshal(map[string]any{"capabilities": caps})
		e.configure(string(b))
		// A check refreshes the copy of info that the replica running it keeps in memory, which the send-time filter
		// of the Bridge envelope reads; with E2E_REPLICAS=2 each replica checks, so that every copy is current.
		for _, rep := range h.Replicas {
			api := patClient{t: t, base: rep.App, token: e.api.token}
			api.json(http.MethodPost, "/api/v1/connections/"+cb+"/checks", "", http.StatusOK)
		}
	}
	declare(true, []string{"here"})

	// The Admin, linked to u-alice of the fake bridge through the demo Bridge Connection.
	admin := e.api.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["user"].(map[string]any)["id"].(string)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
		display_name, created_at) SELECT 1, 'AK0000000000A9', u.id, 'bridge', c.id, '`+fakebridge.AliceID+`',
		'Alice Smith', now() FROM users u, connections c WHERE u.public_id = '`+admin+`' AND c.public_id = '`+cb+`'`)

	e.api.json(http.MethodPost, "/api/v1/teams", `{"name":"payments","slug":"payments","own_working_hours":false,`+
		`"team_mentions":[{"connection_id":"`+cs+`","group":"payments-team","group_id":"`+
		fakeslack.UserGroupPayments+`"},{"connection_id":"`+cb+`","group":"payments_team"}]}`, http.StatusCreated)
	events := `"events":["new_alert_group","new_alerts"]`
	ds := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"slack","name":"alerts-sl",
		"connection_id":%q,"channel_id":%q,"limiter":{"limit":50,"per_seconds":1},"mention_entries":[
		{"target":{"kind":"teams_from_label"},%s},
		{"target":{"kind":"group","group":"infra-team","group_id":"S0FAKEINFRA"},%s},
		{"target":{"kind":"everyone","everyone":"here"},%s}]}`, cs, fakeslack.ChannelAlerts, events, events, events),
		http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	db := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":"alerts-br",
		"connection_id":%q,"channel_id":%q,"limiter":{"limit":50,"per_seconds":1},"mention_entries":[
		{"target":{"kind":"teams_from_label"},%s},
		{"target":{"kind":"group","group":"infra_team"},%s},
		{"target":{"kind":"everyone","everyone":"here"},%s},
		{"target":{"kind":"user","user_id":%q},%s}]}`, cb, fakebridge.ChannelAlerts, events, events, events, admin,
		events), http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	e.route("Infrastructure", "payments", []string{ds, db}, calm)

	slackRoot := func(title string) (fakeslack.Message, bool) {
		var all []fakeslack.Message
		decode(t, call(t, http.MethodGet, fsl+"/messages?app="+fakeslack.AppA+"&channel="+fakeslack.ChannelAlerts,
			""), &all)
		for _, m := range all {
			if strings.Contains(m.Text, " "+title+" ") || strings.HasSuffix(m.Text, " "+title) {
				return m, true
			}
		}
		return fakeslack.Message{}, false
	}
	topText := func(blocks json.RawMessage) string {
		var bs []struct {
			Type string `json:"type"`
			Text *struct {
				Text string `json:"text"`
			} `json:"text"`
		}
		_ = json.Unmarshal(blocks, &bs)
		if len(bs) == 0 || bs[0].Text == nil {
			return ""
		}
		return bs[0].Text.Text
	}
	kindsOf := func(raw json.RawMessage) string {
		var list []mentionKind
		_ = json.Unmarshal(raw, &list)
		out := make([]string, len(list))
		for i, m := range list {
			out[i] = m.String()
		}
		return strings.Join(out, ",")
	}
	// rawBridge is the last message of the fake bridge titled title as /_fake/messages lists it.
	rawBridge := func(title string) map[string]any {
		var all []map[string]any
		decode(t, call(t, http.MethodGet, e.fb+"/messages", ""), &all)
		var out map[string]any
		for _, m := range all {
			if env, _ := m["envelope"].(map[string]any); env != nil && strings.HasSuffix(env["title"].(string),
				" "+title) {
				out = m
			}
		}
		return out
	}
	mentionsJSON := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}

	const slackLine = "<!subteam^" + fakeslack.UserGroupPayments + "> <!subteam^S0FAKEINFRA> <!here>"
	const bridgeKinds = "group:payments_team,group:infra_team,everyone:here,-:" + fakebridge.AliceID
	const bridgeLine = "[@payments\\_team](muster-mention:m1) [@infra\\_team](muster-mention:m2) " +
		"[@here](muster-mention:m3) [@Alice Smith](muster-mention:m4)"

	t.Run("root message", func(t *testing.T) {
		e.group("k1", "QueueBacklog")
		e.alert("k1", "a", `{"team":"payments","cluster":"c1","severity":"warning"}`, "")
		e.notify("k1", "first notification")
		var root fakeslack.Message
		eventually(t, "the slack root message", func() bool {
			var ok bool
			root, ok = slackRoot("QueueBacklog")
			return ok
		})
		if !strings.HasSuffix(root.Text, " QueueBacklog "+slackLine) ||
			!strings.HasSuffix(topText(root.Blocks), "QueueBacklog\n"+slackLine) {
			t.Errorf("slack root = %q, top %q", root.Text, topText(root.Blocks))
		}
		if strings.Contains(string(root.Attachments), "<!") || strings.Contains(root.Text+string(root.Blocks),
			"<!everyone") {
			t.Errorf("slack root mentions elsewhere: %s %s", root.Blocks, root.Attachments)
		}
		var m map[string]any
		eventually(t, "the bridge root message", func() bool { m = rawBridge("QueueBacklog"); return m != nil })
		env := m["envelope"].(map[string]any)
		if got := kindsOf(mentionsJSON(env["mentions"])); got != bridgeKinds {
			t.Errorf("bridge mentions = %s, want %s", got, bridgeKinds)
		}
		if body := env["body"].(string); !strings.HasPrefix(body, bridgeLine+"\n\n") {
			t.Errorf("bridge body = %q", body)
		}
		if list := env["mentions"].([]any); len(list) == 4 {
			if _, ok := list[3].(map[string]any)["kind"]; ok {
				t.Errorf("the person's entry has a kind: %v", list[3])
			}
		}
	})

	t.Run("thread reply", func(t *testing.T) {
		e.alert("k1", "b", `{"team":"payments","cluster":"c1","severity":"warning","handler":"b"}`, "")
		e.notify("k1", "new alerts")
		var reply fakeslack.Reply
		eventually(t, "the slack thread reply", func() bool {
			root, _ := slackRoot("QueueBacklog")
			if len(root.Replies) == 0 {
				return false
			}
			reply = root.Replies[len(root.Replies)-1]
			return true
		})
		if !strings.HasPrefix(topText(reply.Blocks), slackLine+"\n") || !strings.HasSuffix(reply.Text, slackLine) {
			t.Errorf("slack reply = %q, section %q", reply.Text, topText(reply.Blocks))
		}
		var input map[string]any
		eventually(t, "the bridge thread reply", func() bool {
			m := rawBridge("QueueBacklog")
			replies, _ := m["replies"].([]any)
			if len(replies) == 0 {
				return false
			}
			input = replies[len(replies)-1].(map[string]any)["input"].(map[string]any)
			return true
		})
		if got := kindsOf(mentionsJSON(input["mentions"])); got != bridgeKinds {
			t.Errorf("bridge reply mentions = %s", got)
		}
		if body := input["body"].(string); !strings.HasPrefix(body, bridgeLine+"\n\n") {
			t.Errorf("bridge reply body = %q", body)
		}
	})

	t.Run("a bridge without group mentions and here, slack without usergroups:read", func(t *testing.T) {
		declare(false, nil)
		e.fake(http.MethodPut, fsl+"/config", `{"scopes":{"a":null}}`)
		e.api.json(http.MethodPost, "/api/v1/connections/"+cs+"/checks", "", http.StatusOK)
		var entries struct {
			Entries []struct {
				Available bool `json:"available"`
			} `json:"mention_entries"`
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/destinations/"+db, ""), &entries)
		if len(entries.Entries) != 4 || entries.Entries[1].Available || entries.Entries[2].Available ||
			!entries.Entries[3].Available {
			t.Errorf("the bridge's entries after the declarations were withdrawn %+v", entries.Entries)
		}
		e.group("k2", "QueueStuck")
		e.alert("k2", "a", `{"team":"payments","cluster":"c2","severity":"warning"}`, "")
		e.notify("k2", "first notification")
		var m map[string]any
		eventually(t, "the bridge root message", func() bool { m = rawBridge("QueueStuck"); return m != nil })
		env := m["envelope"].(map[string]any)
		raw := mentionsJSON(env["mentions"])
		if got := kindsOf(raw); got != "-:"+fakebridge.AliceID || strings.Contains(string(raw), `"kind"`) {
			t.Errorf("bridge mentions = %s", raw)
		}
		if body := env["body"].(string); !strings.HasPrefix(body, "[@Alice Smith](muster-mention:m1)\n\n") {
			t.Errorf("bridge body = %q", body)
		}
		var root fakeslack.Message
		eventually(t, "the slack root message", func() bool {
			var ok bool
			root, ok = slackRoot("QueueStuck")
			return ok
		})
		if !strings.HasSuffix(root.Text, " QueueStuck <!here>") ||
			strings.Contains(root.Text+string(root.Blocks), "<!subteam") {
			t.Errorf("slack root without the scope = %q, %s", root.Text, root.Blocks)
		}
	})
}
