// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"cmp"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/keyring"
)

// keyringKey is an item of getKeyring.
type keyringKey struct {
	ID        string   `json:"id"`
	Active    bool     `json:"active"`
	HeldBy    []string `json:"held_by"`
	MissingOn []string `json:"missing_on"`
	Needed    bool     `json:"needed"`
	Removable bool     `json:"removable"`
	State     string   `json:"state"`
}

// keyringEnv is the reminderEnv of S-068 with a third Destination: the Mattermost Destination "alerts-2" of the
// Connection "mm-2" to the second fake Mattermost server, on the Route "t" beside "alerts" and the Telegram
// Destination; and the Route "st" to "alerts" with the Storm threshold 2.
type keyringEnv struct {
	*timersEnv
	mm2    *mm
	mm2Dst string
}

// on is e reporting to t, for a subtest.
func (e *keyringEnv) on(t *testing.T) *keyringEnv {
	m2 := *e.mm2.on(t)
	return &keyringEnv{timersEnv: e.timersEnv.on(t), mm2: &m2, mm2Dst: e.mm2Dst}
}

// quiet is settle while a Storm holds deliveries: every due timer has fired and every message not held by a Storm has
// reached the healthy Destinations.
func (e *keyringEnv) quiet() {
	e.t.Helper()
	now := readClock(e.t, e.r).Now.UTC().Format(time.RFC3339Nano)
	eventually(e.t, "the due timers and their messages", func() bool {
		return e.h.count(e.t, `SELECT (SELECT count(*) FROM timers WHERE deadline <= '`+now+`')
			+ (SELECT count(*) FROM thread_replies r JOIN destinations d ON d.id = r.destination_id
				WHERE r.state = 'pending' AND d.health = 'healthy')
			+ (SELECT count(*) FROM deliveries x JOIN destinations d ON d.id = x.destination_id
				WHERE x.state = 'pending' AND x.held_by_storm_id IS NULL AND d.health = 'healthy')`) == 0
	})
}

func newKeyringEnv(t *testing.T, h *Harness, r *Replica) *keyringEnv {
	t.Helper()
	e := &keyringEnv{timersEnv: &timersEnv{tgd: newTGD(t, h, r)}}
	_, e.bob = e.responder("bob")
	e.mmConn = e.connection(50)
	e.mmDest = e.destination("alerts", e.mmConn, fakemattermost.ChannelAlerts, nil, 50)
	conn2 := e.api.json(http.MethodPost, "/api/v1/connections", fmt.Sprintf(`{"type":"mattermost","name":"mm-2",
		"server_url":%q,"bot_token":%q,"proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":1}}`,
		h.Fakes.MattermostSecond, devmode.ConnectionBotToken), http.StatusCreated)["id"].(string)
	e.mm2Dst = e.destination("alerts-2", conn2, fakemattermost.ChannelAlerts, nil, 50)
	m2 := *e.mm
	m2.fmm = h.Fakes.MattermostSecond + "/_fake"
	e.mm2 = &m2
	e.member(faketelegram.GroupID, faketelegram.Member{Status: faketelegram.StatusAdministrator, CanPostMessages: true})
	a := e.create("tg", "@"+faketelegram.ChannelUsername, 60)
	if a.status != http.StatusCreated {
		t.Fatalf("telegram destination = %d %s", a.status, a.body)
	}
	var tg struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	decode(t, a, &tg)
	e.tgDest = tg.Destination.ID
	e.route("t", "t", []string{e.mmDest, e.mm2Dst, e.tgDest}, nil)
	e.route("st", "st", []string{e.mmDest}, func(p map[string]any) { p["storm_threshold"] = 2 })
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		created_at) SELECT 1, 'AK0000000000B1', u.id, 'mattermost', c.id, 'u-bob', 'bob', now() FROM users u,
		connections c WHERE u.login = 'bob' AND c.name = 'mm'`)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		created_at) SELECT 1, 'AK0000000000T1', u.id, 'telegram', c.id, '5001', 'bob_tg', now() FROM users u,
		connections c WHERE u.login = 'bob' AND c.name = '`+devmode.TelegramConnectionName+`'`)
	return e
}

// keyring is getKeyring, by key id.
func (e *keyringEnv) keyring() map[string]keyringKey {
	e.t.Helper()
	var k struct {
		Keys []keyringKey `json:"keys"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/organization/keyring", ""), &k)
	out := map[string]keyringKey{}
	for _, x := range k.Keys {
		out[x.ID] = x
	}
	return out
}

// buttonKeyID is the key id the Mattermost buttons of the post carry; empty when they disagree or there are none.
func buttonKeyID(t *testing.T, p mmPost) string {
	t.Helper()
	id := ""
	for _, a := range p.attachment(t).Actions {
		k := a.Integration.Context["key_id"]
		if id != "" && k != id {
			return ""
		}
		id = k
	}
	return id
}

// tgKeyPrefixes are the key id prefixes, in hex, of the Telegram buttons of the message: bytes 18 to 22 of each
// callback_data, the action id in its compact base64url form (internal/buttons).
func tgKeyPrefixes(t *testing.T, m faketelegram.Message) []string {
	t.Helper()
	var kb struct {
		InlineKeyboard [][]struct {
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal(m.ReplyMarkup, &kb); err != nil {
		t.Fatalf("keyboard of %d: %v", m.ID, err)
	}
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			raw, err := base64.RawURLEncoding.DecodeString(b.CallbackData)
			if err != nil || len(raw) < 22 {
				t.Fatalf("callback_data %q is no action id", b.CallbackData)
			}
			out = append(out, hex.EncodeToString(raw[18:22]))
		}
	}
	return out
}

// keyIDsIn are the key_id values anywhere in a JSON value, such as the context of each Mattermost button.
func keyIDsIn(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			if s, ok := y.(string); ok && k == "key_id" {
				out = append(out, s)
			} else {
				out = append(out, keyIDsIn(y)...)
			}
		}
	case []any:
		for _, y := range x {
			out = append(out, keyIDsIn(y)...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// channelPost is the message of the channel in the fake Telegram server.
func (e *keyringEnv) channelPost(id int64) faketelegram.Message {
	e.t.Helper()
	for _, m := range e.messages(faketelegram.ChannelID) {
		if m.ID == id {
			return m
		}
	}
	e.t.Fatalf("no channel post %d", id)
	return faketelegram.Message{}
}

// TestKeyring is S-071 on two `muster dev --replica` processes with explicit master keys and the fakes in the test
// process: the Keyring status before a rotation (C-20.FR-6); `muster secrets rotate-key` refused while replica B lacks
// the new key, and without --actor (C-20.FR-7, C-20.AC-2, C-02.FR-15); after B restarts with both keys the activation,
// its hints and Audit entry, both replicas switching without a restart, and the Leader handing over during
// rerender_after_rotation, after which the Desired state of every open delivery was recomputed once and every open
// Root message — both fake Mattermost servers and the fake Telegram server — is edited with buttons the new key signed
// only (C-20.AC-5) and the old key is removable; an Alert Group a Storm holds during the rotation published with the
// new key when the Storm ends; and after both replicas restart with the new key only, startup passes the canary, a
// press on a Root message works and a press on a Reminder sent before the activation has expired and changes nothing
// (C-17.FR-6).
func TestKeyring(t *testing.T) {
	k0 := devmode.DevelopmentKey
	material := make([]byte, keyring.KeySize)
	_, _ = rand.Read(material)
	k1 := base64.StdEncoding.EncodeToString(material)
	dev, err := base64.StdEncoding.DecodeString(k0)
	if err != nil {
		t.Fatal(err)
	}
	k0id, k1id := keyring.KeyID(dev), keyring.KeyID(material)
	both := []string{"MUSTER_SECRET_KEYS=" + k0 + "," + k1}

	h := Start(t, FakesInProcess)
	// A comes first on a new database, so K0, the first key of its list, becomes active.
	a := h.StartReplica(t, ReplicaOptions{ListenApp: ":8080", ListenIngest: ":8081", ListenInternal: ":8082",
		Env: map[string]string{"MUSTER_SECRET_KEYS": k0 + "," + k1}})
	b := h.StartReplica(t, ReplicaOptions{Env: map[string]string{"MUSTER_SECRET_KEYS": k0}})
	e := newKeyringEnv(t, h, a)

	// An open Alert Group in all three Destinations, acknowledged by bob, with a Reminder four hours later.
	g := e.fire("rot", "Rot", "t", true)
	n := e.number(g)
	eventually(t, "the root messages of #"+fmt.Sprint(n), func() bool {
		return e.h.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = '`+g+`' AND d.state = 'delivered'`) == 3
	})
	e.ack(e.bob, g)
	e.adv(14400)
	rp, root := e.reminderPost(g)
	root2 := e.mm2.root(n)
	tr := e.tgReminder(g)
	tgPost := e.h.count(t, `SELECT d.message_id::bigint FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id
		JOIN alert_groups g ON g.id = d.alert_group_id WHERE ds.type = 'telegram' AND g.public_id = '`+g+`'`)
	if buttonKeyID(t, root) != k0id || buttonKeyID(t, root2) != k0id || buttonKeyID(t, rp) != k0id {
		t.Fatalf("before the rotation the buttons carry %s, %s, %s; want %s", buttonKeyID(t, root),
			buttonKeyID(t, root2), buttonKeyID(t, rp), k0id)
	}

	// A Storm on the Route "st": two Alert Groups published, the third held.
	e.group("st", "Surge")
	for i := 1; i <= 3; i++ {
		e.alert("st", fmt.Sprint("s", i), fmt.Sprintf(`{"team":"st","cluster":"s%d","severity":"warning"}`, i), "")
	}
	e.notify("st", "first notification")
	var storm []string
	for i := 1; i <= 3; i++ {
		storm = append(storm, e.groupOf(fmt.Sprint("s", i)))
	}
	// stormRoots are the Root messages of the published ones, and acked the first of them.
	var stormRoots []mmPost
	acked := ""
	held := ""
	eventually(t, "the storm summary and two root messages", func() bool {
		published := 0
		for _, id := range storm {
			if e.h.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
				WHERE g.public_id = '`+id+`' AND d.state = 'delivered'`) == 1 {
				published++
			} else if e.h.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
				WHERE g.public_id = '`+id+`' AND d.held_by_storm_id IS NOT NULL`) == 1 {
				held = id
			}
		}
		return published == 2 && held != "" && e.h.count(t, `SELECT count(*) FROM storms WHERE ended_at IS NULL`) == 1
	})
	for _, id := range storm {
		if id != held {
			stormRoots = append(stormRoots, e.root(e.number(id)))
			acked = cmp.Or(acked, id)
		}
	}
	e.quiet()

	t.Run("the keyring before the rotation", func(t *testing.T) {
		e := e.on(t)
		bID := ""
		keys := e.keyring()
		if k := keys[k0id]; !k.Active || len(k.HeldBy) != 2 || !k.Needed || k.Removable || k.State != "active" {
			t.Errorf("K0 %+v", k)
		}
		if k := keys[k1id]; k.Active || len(k.HeldBy) != 1 || len(k.MissingOn) != 1 || k.Needed || k.Removable ||
			k.State != "not_active_yet" {
			t.Errorf("K1 %+v", k)
		} else {
			bID = k.MissingOn[0]
		}
		if len(keys) != 2 {
			t.Errorf("keys %+v", keys)
		}
		_, out, _ := h.runCLIEnv(t, both, "doctor")
		if !strings.Contains(out, "\nOK   keyring: key "+k1id+" is not active yet\n") {
			t.Errorf("doctor:\n%s", out)
		}

		// C-20.FR-7, AC-2: exit 2 without --actor, and a refusal naming B while it lacks K1.
		code, _, stderr := h.runCLIEnv(t, both, "secrets", "rotate-key", "--activate", k1id)
		if code != 2 || !strings.Contains(stderr, "--actor is required") {
			t.Errorf("without --actor: exit %d, %s", code, stderr)
		}
		code, _, stderr = h.runCLIEnv(t, both, "secrets", "rotate-key", "--activate", k1id, "--actor", "ops")
		if want := "Replicas " + bID + " do not hold key " + k1id + " yet."; code != 1 || bID == "" ||
			strings.TrimSpace(stderr) != want {
			t.Errorf("refused activation: exit %d, %q; want %q", code, stderr, want)
		}
		if got := e.h.text(t, `SELECT active_key_id FROM keyring_state`); got != k0id {
			t.Errorf("the refusal changed the active key to %s", got)
		}
	})

	// B restarts with both keys. Then A restarts, so that B leads at the activation and hands over to A during the
	// re-render. The development clock moved by hours since the last alive mark: a takeover before the Leader marks
	// it again would look like an outage that long.
	b.env = append(b.env, both...)
	b.Restart(t)
	waitAliveMark(t, h, readClock(t, a).Now)
	a.Stop(t)
	b.waitFor(t, "replica B to lead", func() bool { return b.Metric(t, "muster_leader") == "1" })
	a.Start(t)
	if got := a.Metric(t, "muster_leader"); got != "0" {
		t.Fatalf("replica A exports muster_leader %q", got)
	}
	keys := e.keyring()
	if k := keys[k1id]; len(k.HeldBy) != 2 || len(k.MissingOn) != 0 || k.State != "not_active_yet" {
		t.Fatalf("K1 after the restarts %+v", k)
	}

	// The edits of each open Root message so far, and the version of each open delivery's Desired state.
	type editLog struct {
		second bool
		post   string
		tg     bool
	}
	logs := map[string]editLog{"alerts": {false, root.ID, false}, "alerts-2": {true, root2.ID, false},
		"tg": {false, "", true}}
	for i, p := range stormRoots {
		logs[fmt.Sprint("storm-", i)] = editLog{false, p.ID, false}
	}
	// editKeys are the key ids of the buttons of each edit of the Root message, oldest first.
	editKeys := func(e *keyringEnv, l editLog) []string {
		t := e.t
		t.Helper()
		var out []string
		if l.tg {
			for _, x := range e.channelPost(tgPost).Edits {
				prefixes := tgKeyPrefixes(t, faketelegram.Message{ID: tgPost, ReplyMarkup: x.ReplyMarkup})
				slices.Sort(prefixes)
				out = append(out, strings.Join(slices.Compact(prefixes), ","))
			}
			return out
		}
		server := e.mm
		if l.second {
			server = e.mm2
		}
		for _, r := range server.calls(http.MethodPut, "/api/v4/posts/"+l.post+"/patch") {
			out = append(out, strings.Join(keyIDsIn(requestBody(t, r)), ","))
		}
		return out
	}
	versions := func() string {
		return e.h.text(t, `SELECT string_agg(d.id || ':' || d.desired_version, ',' ORDER BY d.id) FROM deliveries d
			JOIN alert_groups g ON g.id = d.alert_group_id WHERE g.status <> 'resolved'`)
	}
	before := map[string]int{}
	for name, l := range logs {
		before[name] = len(editKeys(e, l))
	}
	versionsBefore := versions()
	admin := newAgent(t, a.App)
	if got := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); got.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", got.status, got.body)
	}
	sse := openStream(t, admin)
	eventually(t, "the stream to open", func() bool { return sse.first() != "" })

	// C-20.FR-7: the activation. B leads and stops as soon as it has switched; A takes over the re-render.
	code, _, stderr := h.runCLIEnv(t, both, "secrets", "rotate-key", "--activate", k1id, "--actor", "ops")
	if code != 0 || !strings.Contains(stderr, "key "+k1id+" is active") {
		t.Fatalf("activation: exit %d, %s", code, stderr)
	}
	b.waitFor(t, "key_switched on B", func() bool { return len(logLines(b, "key_switched")) > 0 })
	b.Stop(t)
	a.waitFor(t, "replica A to lead", func() bool { return a.Metric(t, "muster_leader") == "1" })

	t.Run("the activation", func(t *testing.T) {
		e := e.on(t)
		if n := e.h.count(t, `SELECT count(*) FROM encrypted_values WHERE key_id <> '`+k1id+`'`); n != 0 {
			t.Errorf("%d encrypted values on another key", n)
		}
		if got := e.h.text(t, `SELECT active_key_id || ' ' || canary_key_id FROM keyring_state`); got !=
			k1id+" "+k1id {
			t.Errorf("keyring_state %s", got)
		}
		for _, r := range []*Replica{a, b} {
			lines := logLines(r, "key_switched")
			if len(lines) != 1 || lines[0]["active"] != k1id {
				t.Errorf("key_switched of %s: %v", r.name(), lines)
			}
		}
		eventually(t, "the organization and system-status hints", func() bool {
			types := map[string]bool{}
			for _, x := range sse.hints() {
				for _, typ := range []string{"organization", "system-status"} {
					if strings.Contains(x, `"type":"`+typ+`"`) {
						types[typ] = true
					}
				}
			}
			return len(types) == 2
		})
		var audit struct {
			Items []struct {
				Actor struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"actor"`
			} `json:"items"`
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/audit-log?action=keyring.key_activated&limit=1", ""), &audit)
		if len(audit.Items) != 1 || audit.Items[0].Actor.Kind != "cli" || audit.Items[0].Actor.Name != "ops" {
			t.Errorf("audit %+v", audit)
		}
		if lines := logLines(b, "key_activated"); len(lines) != 0 {
			t.Errorf("a replica logged key_activated: %v", lines)
		}
	})

	// C-20.AC-5: every open Root message is edited once with buttons signed by K1, whichever Leader ran the task. An
	// edit B claimed but did not send before it stopped waits for the end of its lease on the real clock.
	deadline := time.Now().Add(delivery.Lease + 30*time.Second)
	for e.h.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
		WHERE g.status <> 'resolved' AND d.actual_button_key_id IS NOT NULL AND d.actual_button_key_id <> '`+
		k1id+`'`) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("open root messages still carry another key than %s:\n%s", k1id, e.h.text(t,
				`SELECT coalesce(string_agg(g.public_id || ' ' || ds.name || ' ' || d.state || ' ' ||
				coalesce(d.actual_button_key_id, '-') || ' ' || coalesce(d.desired_button_key_id, '-'), '; '), '')
				FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id
				JOIN alert_groups g ON g.id = d.alert_group_id WHERE g.status <> 'resolved'`))
		}
		time.Sleep(pollInterval)
	}
	e.quiet()

	t.Run("the root messages are re-signed", func(t *testing.T) {
		e := e.on(t)
		if got := e.h.text(t, `SELECT string_agg(ds.name, ',' ORDER BY ds.name) FROM deliveries d
			JOIN destinations ds ON ds.id = d.destination_id JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = '`+g+`' AND d.actual_button_key_id = '`+k1id+`'`); got != "alerts,alerts-2,tg" {
			t.Errorf("deliveries re-signed: %s", got)
		}
		if k := buttonKeyID(t, e.post(root.ID)); k != k1id {
			t.Errorf("the root message in mattermost carries %s", k)
		}
		if k := buttonKeyID(t, e.mm2.post(root2.ID)); k != k1id {
			t.Errorf("the root message on the second mattermost server carries %s", k)
		}
		for _, p := range stormRoots {
			if k := buttonKeyID(t, e.post(p.ID)); k != k1id {
				t.Errorf("the storm root message %s carries %s", p.ID, k)
			}
		}
		prefixes := tgKeyPrefixes(t, e.channelPost(tgPost))
		if len(prefixes) == 0 || slices.ContainsFunc(prefixes, func(p string) bool { return p != k1id[2:10] }) {
			t.Errorf("the telegram buttons carry the key prefixes %v, want %s", prefixes, k1id[2:10])
		}
		// Each delivery's Desired state was recomputed once, whichever Leader ran the task; each Root message was
		// edited after the activation, and only with K1's buttons: an edit sent by B but not yet recorded when it
		// stopped is sent again by A once its lease ends, the same edit.
		var want []string
		for item := range strings.SplitSeq(versionsBefore, ",") {
			id, v, _ := strings.Cut(item, ":")
			n, _ := strconv.Atoi(v)
			want = append(want, id+":"+strconv.Itoa(n+1))
		}
		if got := versions(); got != strings.Join(want, ",") {
			t.Errorf("desired versions %s, want %s", got, strings.Join(want, ","))
		}
		after := map[string][]string{}
		wantKey := map[bool]string{false: k1id, true: k1id[2:10]}
		for name, l := range logs {
			keys := editKeys(e, l)[before[name]:]
			after[name] = keys
			if len(keys) == 0 || slices.ContainsFunc(keys, func(k string) bool { return k != wantKey[l.tg] }) {
				t.Errorf("%s edited with the keys %v after the activation, want %s only", name, keys, wantKey[l.tg])
			}
		}
		updates := func(r *Replica) int {
			n := 0
			for _, l := range logLines(r, "delivery_attempt") {
				if l["kind"] == "update" {
					n++
				}
			}
			return n
		}
		t.Logf("root messages edited after the activation %v; updates sent by B before it stopped %d, by A %d",
			after, updates(b), updates(a))
		// The Reminder was not re-signed: thread replies are never re-rendered.
		if k := buttonKeyID(t, e.post(rp.ID)); k != k0id {
			t.Errorf("the reminder carries %s", k)
		}
		// The held delivery got the new key and stays held until the Storm ends.
		if n := e.h.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = '`+held+`' AND d.held_by_storm_id IS NOT NULL AND d.published_at IS NULL
			AND d.desired_button_key_id = '`+k1id+`'`); n != 1 {
			t.Errorf("the held delivery of %s is not held with the new key", held)
		}
		keys := e.keyring()
		if k := keys[k0id]; k.Active || k.Needed || !k.Removable || k.State != "removable" {
			t.Errorf("K0 after the re-render %+v", k)
		}
		if k := keys[k1id]; !k.Active || k.State != "active" {
			t.Errorf("K1 after the re-render %+v", k)
		}
		_, out, _ := h.runCLIEnv(t, both, "doctor")
		if !strings.Contains(out, "\nOK   keyring: key "+k0id+
			" is no longer used and can be removed from MUSTER_SECRET_KEYS\n") {
			t.Errorf("doctor:\n%s", out)
		}
	})

	t.Run("the storm releases with the new key", func(t *testing.T) {
		e := e.on(t)
		advance(t, a, 360)
		eventually(t, "the release of "+held, func() bool {
			return e.h.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
				WHERE g.public_id = '`+held+`' AND d.state = 'delivered' AND d.held_by_storm_id IS NULL
				AND d.actual_button_key_id = '`+k1id+`'`) == 1
		})
		if k := buttonKeyID(t, e.root(e.number(held))); k != k1id {
			t.Errorf("the released root message carries %s", k)
		}
	})

	// C-17.FR-6: both replicas restart with K1 only; the canary passes, a Root message works, the Reminder has expired.
	e.settle()
	waitAliveMark(t, h, readClock(t, a).Now)
	a.Stop(t)
	only := "MUSTER_SECRET_KEYS=" + k1
	a.env, b.env = append(a.env, only), append(b.env, only)
	a.Start(t)
	b.Start(t)

	t.Run("after the old key is removed", func(t *testing.T) {
		e := e.on(t)
		for _, r := range []*Replica{a, b} {
			if strings.Contains(r.Output(), `"event":"active_key_not_held"`) {
				t.Errorf("%s does not hold the active key", r.name())
			}
		}
		keys := e.keyring()
		if _, ok := keys[k0id]; ok || len(keys) != 1 || keys[k1id].State != "active" || len(keys[k1id].HeldBy) != 2 {
			t.Errorf("keys %+v", keys)
		}
		answered := len(e.timeline(g, "&kind=timers"))
		if res := e.mm.press(rp.ID, "stillonit", fakemattermost.BobUserID); string(res.Answer) !=
			pressAnswer(textExpired) {
			t.Errorf("the reminder in mattermost: %s", res.Answer)
		}
		if got := e.pressIn(faketelegram.GroupID, tr, "Still on it", ""); got.Text != textExpired {
			t.Errorf("the reminder in telegram: %+v", got)
		}
		if s, _ := e.ownerOf(g); s != "acknowledged" || len(e.timeline(g, "&kind=timers")) != answered {
			t.Errorf("an expired press changed %s: %s, %d timers entries", g, s, len(e.timeline(g, "&kind=timers")))
		}
		if res := e.mm.press(stormRoots[0].ID, "ack", fakemattermost.BobUserID); string(res.Answer) != "{}" {
			t.Errorf("ack on a re-signed root message: %s", res.Answer)
		}
		eventually(t, "the acknowledgement by bob", func() bool {
			s, owner := e.ownerOf(acked)
			o, _ := owner.(map[string]any)
			return s == "acknowledged" && o["login"] == "bob"
		})
	})
}
