// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
)

const (
	bridgeToken = "bridge-token-secret-0123456789"
	pushSecret  = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	// otherPushSecret is a push secret in the Standard Webhooks form that replaces pushSecret.
	otherPushSecret = "whsec_c2Vjb25kIHB1c2ggc2VjcmV0IG9mIHRoZSBicmlkZ2U="
)

// standIn is a bridge that answers getInfo, for the checks: an info with every feature and a field Muster does not
// know, changed by mutate; or status with a problem whose detail and title echo the token and the Authorization
// header; or a connection it closes at once. It records the requests.
type standIn struct {
	srv *httptest.Server

	mu       sync.Mutex
	mutate   func(map[string]any)
	status   int
	hijack   bool
	requests []*http.Request
}

func startStandIn(t *testing.T) *standIn {
	t.Helper()
	b := &standIn{}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *standIn) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.requests = append(b.requests, r.Clone(context.WithoutCancel(r.Context())))
	mutate, status, hijack := b.mutate, b.status, b.hijack
	b.mu.Unlock()
	switch {
	case hijack:
		if c, _, err := http.NewResponseController(w).Hijack(); err == nil {
			_ = c.Close()
		}
		return
	case status != 0:
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		echo := bridgeToken + " " + r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "about:blank", "title": "refused " + echo,
			"detail": "the database is down for " + echo})
		return
	}
	info := map[string]any{
		"protocol_version": "1",
		"bridge":           map[string]any{"name": "Stand-in", "version": "0.1.0"},
		"messenger":        map[string]any{"name": "Fake chat"},
		"capabilities": map[string]any{
			"buttons": map[string]any{"max_per_message": 8, "max_per_row": 4, "data_max_bytes": 256,
				"styles": []any{"primary", "danger"}},
			"thread_messages": "all", "native_mentions": true, "deletion_events": true,
		},
		"limits":         map[string]any{"max_body_length": 4000, "edit_max_age_seconds": 172800},
		"user_lookup":    "email",
		"event_delivery": []any{"poll", "push"},
		"future_field":   map[string]any{"kept": true},
	}
	if mutate != nil {
		mutate(info)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

func (b *standIn) set(f func(b *standIn)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mutate, b.status, b.hijack = nil, 0, false
	f(b)
}

func (b *standIn) seen() []*http.Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.requests)
}

// newBridgeEnv is the env of connections_test.go with a stand-in bridge; at its end, neither the token nor the push
// secrets of the Bridge Connections reached the log.
func newBridgeEnv(t *testing.T) (*env, *standIn) {
	t.Helper()
	e := newEnv(t)
	b := startStandIn(t)
	t.Cleanup(func() {
		for _, s := range []string{bridgeToken, pushSecret, otherPushSecret, strings.TrimPrefix(pushSecret, "whsec_")} {
			if strings.Contains(e.log.String(), s) {
				t.Errorf("a Bridge secret reached the log: %s", e.log.String())
			}
		}
	})
	return e, b
}

func bridgeInput(name, base, mode string) connections.Input {
	in := connections.Input{Type: connections.TypeBridge, Name: name, BridgeBaseURL: base, EventDelivery: mode,
		BotToken: keyring.Replace(bridgeToken), Limiter: connections.Limiter{Limit: connections.BridgeDefaultLimit,
			PerSeconds: connections.BridgeDefaultPerSeconds}}
	if mode == connections.EventDeliveryPush {
		in.PushSecret = keyring.Replace(pushSecret)
	}
	return in
}

func (e *env) createBridge(t *testing.T, name, base, mode string) connections.Connection {
	t.Helper()
	c, err := e.svc.Create(t.Context(), by, bridgeInput(name, base, mode))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestBridgeCreate is C-22.FR-1: a Bridge Connection with its name, base URL without its trailing slash, event
// delivery mode, token, push secret and limiter; the token and the push secret are encrypted with the fields
// connections.bot_token and connections.bridge_push_secret as associated data, read only as their status and shown in
// the Audit log only as secret_changed; the callback address is MUSTER_INGEST_URL/api/v1/callbacks/bridge/<public_id>
// and the info is null before the first check.
func TestBridgeCreate(t *testing.T) {
	e, b := newBridgeEnv(t)
	c := e.createBridge(t, "br", b.srv.URL+"/", connections.EventDeliveryPush)
	if c.Type != connections.TypeBridge || c.BridgeBaseURL != b.srv.URL || c.EventDelivery != "push" ||
		!c.BotToken.Set || !c.PushSecret.Set || c.BridgeInfo != nil || c.BridgeInfoReadAt != nil ||
		c.Limiter != (connections.Limiter{Limit: 10, PerSeconds: 1}) || c.ServerURL != "" || c.BotAPIBaseURL != "" ||
		c.UpdateMode != "" || c.Version != 1 {
		t.Fatalf("created %+v", c)
	}
	if got := e.svc.BridgeCallbackURL(c.PublicID); got != "http://localhost:8081/api/v1/callbacks/bridge/"+c.PublicID {
		t.Errorf("callback %s", got)
	}
	row := e.store.rows[c.ID].row
	token := keyring.StoredSecret{Ciphertext: row.BotTokenCiphertext, KeyID: row.BotTokenKeyID.String}
	push := keyring.StoredSecret{Ciphertext: row.BridgePushSecretCiphertext, KeyID: row.BridgePushSecretKeyID.String}
	if v, err := e.keys.OpenSecret("connections.bot_token", token); err != nil || v != bridgeToken {
		t.Errorf("token = %v", err)
	}
	if v, err := e.keys.OpenSecret("connections.bridge_push_secret", push); err != nil || v != pushSecret {
		t.Errorf("push secret = %v", err)
	}
	if _, err := e.keys.OpenSecret("connections.bot_token", push); err == nil {
		t.Error("the push secret opens as a token")
	}
	if bytes.Contains(row.BridgePushSecretCiphertext, []byte(pushSecret)) || !row.BridgePushSecretUpdatedAt.Valid ||
		row.BridgeBaseUrl.String != b.srv.URL || row.BridgeEventDelivery.String != "push" {
		t.Errorf("row %+v", row)
	}
	a := e.store.audit[0]
	d := diffOf(t, a)
	after := func(pointer string, v any) bool {
		return slices.ContainsFunc(d, func(c audit.Change) bool { return c.Pointer == pointer && c.After == v })
	}
	if a.Action != connections.ActionCreated || !secretChanged(d, "/bot_token") || !secretChanged(d, "/push_secret") ||
		bytes.Contains(a.Diff, []byte(bridgeToken)) || bytes.Contains(a.Diff, []byte(pushSecret[len("whsec_"):])) ||
		!after("/base_url", b.srv.URL) || !after("/event_delivery", "push") {
		t.Errorf("audit %s %s", a.Action, a.Diff)
	}
	got, err := e.svc.Get(t.Context(), c.PublicID)
	if err != nil || got.BridgeBaseURL != b.srv.URL || !got.PushSecret.Set || got.BridgeInfo != nil {
		t.Errorf("get = %+v, %v", got, err)
	}
	poll := e.createBridge(t, "br-poll", b.srv.URL, connections.EventDeliveryPoll)
	if poll.PushSecret.Set || e.store.rows[poll.ID].row.BridgePushSecretCiphertext != nil ||
		secretChanged(diffOf(t, e.store.audit[1]), "/push_secret") {
		t.Errorf("poll %+v", poll)
	}
	page, err := e.svc.List(t.Context(), connections.ListFilter{Type: new(connections.TypeBridge), Limit: 10})
	if err != nil || len(page.Connections) != 2 || page.Connections[1].EventDelivery != "poll" {
		t.Errorf("list = %+v, %v", page, err)
	}
}

// TestBridgeValidation is C-22.FR-1: a base URL with a query, a fragment or user information, or one that is not an
// absolute http or https URL, is refused at /base_url, and a path prefix is kept without its trailing slash; push
// event delivery needs a push secret in the Standard Webhooks form, which poll refuses; no refusal echoes the secret.
func TestBridgeValidation(t *testing.T) {
	e, _ := newBridgeEnv(t)
	for _, base := range []string{"http://bridge.example.org/?a=1", "http://bridge.example.org/?",
		"http://bridge.example.org/#top", "http://user:pass@bridge.example.org", "ftp://bridge.example.org",
		"bridge.example.org", "/v1", "", "http://"} {
		_, err := e.svc.Create(t.Context(), by, bridgeInput("br", base, connections.EventDeliveryPoll))
		if fe := fieldError(err); fe == nil || fe.Pointer != "/base_url" || fe.Code != connections.CodeInvalidFormat ||
			strings.Contains(fe.Detail, "pass") {
			t.Errorf("%q = %v", base, err)
		}
	}
	c, err := e.svc.Create(t.Context(), by, bridgeInput("br", " https://bridge.example.org/muster/bridge// ",
		connections.EventDeliveryPoll))
	if err != nil || c.BridgeBaseURL != "https://bridge.example.org/muster/bridge" {
		t.Errorf("a path prefix = %+v, %v", c, err)
	}
	for _, tc := range []struct {
		name    string
		mode    string
		secret  keyring.SecretInput
		pointer string
		code    string
	}{
		{"an unknown mode", "smoke", keyring.Keep, "/event_delivery", connections.CodeInvalidFormat},
		{"push without a secret", connections.EventDeliveryPush, keyring.Keep, "/push_secret",
			connections.CodeRequired},
		{"push with a cleared secret", connections.EventDeliveryPush, keyring.Clear, "/push_secret",
			connections.CodeRequired},
		{"poll with a secret", connections.EventDeliveryPoll, keyring.Replace(pushSecret), "/push_secret",
			connections.CodeUnsupported},
		{"no prefix", connections.EventDeliveryPush, keyring.Replace("MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"),
			"/push_secret", connections.CodeInvalidFormat},
		{"no key", connections.EventDeliveryPush, keyring.Replace("whsec_"), "/push_secret",
			connections.CodeInvalidFormat},
		{"not base64", connections.EventDeliveryPush, keyring.Replace("whsec_not base64 at all!"), "/push_secret",
			connections.CodeInvalidFormat},
		{"empty", connections.EventDeliveryPush, keyring.Replace(""), "/push_secret", connections.CodeInvalidFormat},
	} {
		in := bridgeInput("br-"+tc.name, "http://bridge.example.org", tc.mode)
		in.PushSecret = tc.secret
		_, err := e.svc.Create(t.Context(), by, in)
		fe := fieldError(err)
		if fe == nil || fe.Pointer != tc.pointer || fe.Code != tc.code ||
			len(tc.secret.Value) > len("whsec_") && strings.Contains(fe.Error(), string(tc.secret.Value)) {
			t.Errorf("%s = %v", tc.name, err)
		}
	}
	in := bridgeInput("br-no-token", "http://bridge.example.org", connections.EventDeliveryPoll)
	in.BotToken = keyring.Keep
	if fe := fieldError(func() error { _, err := e.svc.Create(t.Context(), by, in); return err }()); fe == nil ||
		fe.Pointer != "/bot_token" || fe.Code != connections.CodeRequired {
		t.Errorf("without a token = %v", fe)
	}
	in = bridgeInput("br-type", "http://bridge.example.org", connections.EventDeliveryPoll)
	in.Type = "smoke"
	if fe := fieldError(func() error { _, err := e.svc.Create(t.Context(), by, in); return err }()); fe == nil ||
		fe.Detail != "The type is not mattermost, telegram or bridge." {
		t.Errorf("an unknown type = %v", fe)
	}
	if len(e.store.rows) != 1 {
		t.Errorf("%d rows", len(e.store.rows))
	}
}

// TestBridgeUpdate is C-22.FR-1: a new base URL needs the token again, so that the stored one never reaches another
// address; a new base URL or token forgets the stored info; push needs a push secret, a stored one is kept, a save in
// poll clears it; each change of a secret is secret_changed in the Audit log; the type cannot change.
func TestBridgeUpdate(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	c := e.createBridge(t, "br", b.srv.URL, connections.EventDeliveryPoll)
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	keep := bridgeInput("br-2", b.srv.URL+"/", connections.EventDeliveryPoll)
	keep.BotToken = keyring.Keep
	got, err := e.svc.Update(ctx, by, c.PublicID, &c.Version, keep)
	if err != nil || got.Name != "br-2" || got.BridgeInfo == nil || got.Version != 2 ||
		e.store.updates[0].ForgetBot {
		t.Fatalf("a new name = %+v, %v", got, err)
	}
	moved := keep
	moved.BridgeBaseURL = "http://127.0.0.1:9"
	if fe := fieldError(e.update(t, c.PublicID, moved)); fe == nil || fe.Pointer != "/bot_token" ||
		fe.Code != connections.CodeRequired || fe.Detail != "A new base URL needs the bot token again." {
		t.Errorf("a new base URL without the token = %v", fe)
	}
	moved.BotToken = keyring.Replace(bridgeToken)
	if err := e.update(t, c.PublicID, moved); err != nil {
		t.Fatal(err)
	}
	got, _ = e.svc.Get(ctx, c.PublicID)
	if got.BridgeBaseURL != "http://127.0.0.1:9" || got.BridgeInfo != nil || got.BridgeInfoReadAt != nil ||
		!e.store.updates[1].ForgetBot {
		t.Errorf("a new base URL kept the info: %+v", got)
	}

	back := bridgeInput("br-2", b.srv.URL, connections.EventDeliveryPoll)
	if err := e.update(t, c.PublicID, back); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	newToken := keep
	newToken.BridgeBaseURL, newToken.BotToken = b.srv.URL, keyring.Replace(bridgeToken)
	if err := e.update(t, c.PublicID, newToken); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, c.PublicID); got.BridgeInfo != nil {
		t.Errorf("a new token kept the info: %+v", got)
	}

	push := keep
	push.BridgeBaseURL, push.EventDelivery = b.srv.URL, connections.EventDeliveryPush
	if fe := fieldError(e.update(t, c.PublicID, push)); fe == nil || fe.Pointer != "/push_secret" ||
		fe.Code != connections.CodeRequired {
		t.Errorf("push without a push secret = %v", fe)
	}
	push.PushSecret = keyring.Replace(pushSecret)
	n := len(e.store.audit)
	if err := e.update(t, c.PublicID, push); err != nil {
		t.Fatal(err)
	}
	d := diffOf(t, e.store.audit[n])
	if got, _ := e.svc.Get(ctx, c.PublicID); !got.PushSecret.Set || got.EventDelivery != "push" ||
		!secretChanged(d, "/push_secret") || secretChanged(d, "/bot_token") {
		t.Errorf("to push = %+v, %s", got, e.store.audit[n].Diff)
	}
	stored := e.store.rows[c.ID].row.BridgePushSecretCiphertext
	push.PushSecret = keyring.Keep
	push.Name = "br-3"
	if err := e.update(t, c.PublicID, push); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(e.store.rows[c.ID].row.BridgePushSecretCiphertext, stored) ||
		secretChanged(diffOf(t, e.store.audit[n+1]), "/push_secret") {
		t.Errorf("an omitted push secret changed: %s", e.store.audit[n+1].Diff)
	}
	push.PushSecret = keyring.Replace(otherPushSecret)
	if err := e.update(t, c.PublicID, push); err != nil {
		t.Fatal(err)
	}
	row := e.store.rows[c.ID].row
	if v, err := e.keys.OpenSecret("connections.bridge_push_secret", keyring.StoredSecret{
		Ciphertext: row.BridgePushSecretCiphertext, KeyID: row.BridgePushSecretKeyID.String}); err != nil ||
		v != otherPushSecret || !secretChanged(diffOf(t, e.store.audit[n+2]), "/push_secret") {
		t.Errorf("a new push secret = %v", err)
	}

	poll := push
	poll.EventDelivery, poll.PushSecret = connections.EventDeliveryPoll, keyring.Keep
	if err := e.update(t, c.PublicID, poll); err != nil {
		t.Fatal(err)
	}
	a := e.store.audit[n+3]
	d = diffOf(t, a)
	if got, _ := e.svc.Get(ctx, c.PublicID); got.PushSecret.Set || e.store.rows[c.ID].row.BridgePushSecretCiphertext !=
		nil || !secretChanged(d, "/push_secret") || !bytes.Contains(a.Diff, []byte(`"push"`)) {
		t.Errorf("to poll = %+v, %s", got, a.Diff)
	}
	n = len(e.store.audit)
	if got, err := e.svc.Update(ctx, by, c.PublicID, nil, poll); err != nil || len(e.store.audit) != n ||
		got.PushSecret.Set {
		t.Errorf("an update that changes nothing = %+v, %v", got, err)
	}

	mm := input("br-3", "http://mm.example.org")
	if fe := fieldError(e.update(t, c.PublicID, mm)); fe == nil || fe.Pointer != "/type" {
		t.Errorf("a changed type = %v", fe)
	}
}

// TestBridgeDelete follows S-039 for a Bridge Connection: it is marked deleted, its token and push secret are wiped,
// and connection.deleted is recorded.
func TestBridgeDelete(t *testing.T) {
	e, b := newBridgeEnv(t)
	c := e.createBridge(t, "br", b.srv.URL, connections.EventDeliveryPush)
	if err := e.svc.Delete(t.Context(), by, c.PublicID, &c.Version); err != nil {
		t.Fatal(err)
	}
	r := e.store.rows[c.ID]
	if !r.deleted || r.row.BotTokenCiphertext != nil || r.row.BridgePushSecretCiphertext != nil ||
		r.row.BridgePushSecretKeyID.Valid || e.store.audit[len(e.store.audit)-1].Action != connections.ActionDeleted ||
		!slices.Equal(e.abandon, []int64{c.ID}) {
		t.Errorf("deleted %+v, abandoned %v", r, e.abandon)
	}
	if _, err := e.svc.Get(t.Context(), c.PublicID); !errors.Is(err, connections.ErrNotFound) {
		t.Errorf("get = %v", err)
	}
}

// infoLine is the log line bridge_info_read.
type infoLine struct {
	Event      string   `json:"event"`
	Connection string   `json:"connection"`
	Version    string   `json:"protocol_version"`
	Features   []string `json:"features"`
}

func infoLines(t *testing.T, log string) []infoLine {
	t.Helper()
	var out []infoLine
	for line := range strings.Lines(log) {
		var l infoLine
		if err := json.Unmarshal([]byte(line), &l); err == nil && l.Event == "bridge_info_read" {
			out = append(out, l)
		}
	}
	return out
}

// TestBridgeCheck is C-22.FR-2, AC-1 and FR-24: checkConnection calls getInfo through the interactive path, limited by
// the Connection, with the token as a bearer token, and returns the steps info and event_delivery with their latency
// and path and the info, which it stores as the bridge sent it, on the business clock, with the live hint, and logs
// with bridge_info_read; protocol_version 2, a refused token and an info larger than 64 KiB fail info with their codes
// and texts and store nothing; a bridge that does not offer the mode fails event_delivery and still stores the info.
func TestBridgeCheck(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	c := e.createBridge(t, "br", b.srv.URL, connections.EventDeliveryPush)
	hints := len(e.store.hints)
	res, err := e.svc.Check(ctx, c.PublicID, nil)
	if err != nil || !res.OK || !res.Bridge || len(res.Steps) != 2 || res.BotName != "" {
		t.Fatalf("check = %+v, %v", res, err)
	}
	info, ev := res.Steps[0], res.Steps[1]
	if info.Name != connections.StepInfo || !info.OK || info.Latency != 7*time.Millisecond || info.Via != "direct" ||
		info.Code != "" || info.Message != "" || ev.Name != connections.StepEventDelivery || !ev.OK || ev.Skipped ||
		ev.Via != "direct" || ev.Code != "" {
		t.Errorf("steps %+v", res.Steps)
	}
	var read map[string]any
	if err := json.Unmarshal(res.Info, &read); err != nil || read["protocol_version"] != "1" ||
		read["messenger"].(map[string]any)["name"] != "Fake chat" || read["future_field"] == nil {
		t.Errorf("info %s, %v", res.Info, err)
	}
	reqs := b.seen()
	if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].URL.Path != "/v1/info" ||
		reqs[0].Header.Get("Authorization") != "Bearer "+bridgeToken {
		t.Errorf("requests %v", reqs)
	}
	if s := e.path.subjects; len(s) != 1 || s[0].Connection == nil || *s[0].Connection != c.ID ||
		s[0].Destination != nil {
		t.Errorf("subjects %+v", s)
	}
	got, _ := e.svc.Get(ctx, c.PublicID)
	if !bytes.Contains(got.BridgeInfo, []byte(`"future_field"`)) || got.BridgeInfoReadAt == nil ||
		!got.BridgeInfoReadAt.Equal(t0) || got.Version != c.Version || len(e.store.infos) != 1 ||
		e.store.infos[0].Version != c.Version {
		t.Errorf("stored %+v", got)
	}
	if len(e.store.hints) != hints+1 || e.store.hints[hints].ID != c.PublicID ||
		e.store.hints[hints].Type != connections.Hint {
		t.Errorf("hints %+v", e.store.hints)
	}
	lines := infoLines(t, e.log.String())
	if len(lines) != 1 || lines[0].Connection != c.PublicID || lines[0].Version != "1" ||
		!slices.Equal(lines[0].Features, []string{"buttons", "deletion_events", "native_mentions", "thread_messages"}) {
		t.Errorf("logged %+v", lines)
	}

	for _, tc := range []struct {
		name    string
		set     func(*standIn)
		code    string
		message string
	}{
		{"protocol version 2", func(b *standIn) { b.mutate = func(m map[string]any) { m["protocol_version"] = "2" } },
			bridge.CodeProtocolVersionUnknown, "The bridge speaks protocol version 2; this Muster speaks 1."},
		{"a refused token", func(b *standIn) { b.status = http.StatusUnauthorized }, bridge.CodeTokenInvalid,
			"The bridge refused the token."},
		{"info larger than 64 KiB", func(b *standIn) {
			b.mutate = func(m map[string]any) { m["padding"] = strings.Repeat("x", 70000) }
		}, bridge.CodeProtocolMismatch, "The bridge's answer does not match the protocol: info is larger than 64 KiB"},
		{"info without its messenger", func(b *standIn) { b.mutate = func(m map[string]any) { delete(m, "messenger") } },
			bridge.CodeProtocolMismatch, "The bridge's answer does not match the protocol: messenger is missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b.set(tc.set)
			stored, logged := len(e.store.infos), len(infoLines(t, e.log.String()))
			res, err := e.svc.Check(ctx, c.PublicID, nil)
			if err != nil || res.OK || !res.Bridge || res.Info != nil || len(res.Steps) != 2 ||
				res.Steps[0].OK || res.Steps[0].Code != tc.code || res.Steps[0].Message != tc.message ||
				res.Steps[0].Via != "direct" || !res.Steps[1].Skipped || res.Steps[1].OK || res.Steps[1].Code != "" {
				t.Fatalf("check = %+v, %v", res, err)
			}
			if len(e.store.infos) != stored || len(infoLines(t, e.log.String())) != logged {
				t.Errorf("a failed info was stored or logged")
			}
		})
	}

	b.set(func(b *standIn) { b.mutate = func(m map[string]any) { m["event_delivery"] = []any{"poll"} } })
	stored := len(e.store.infos)
	res, err = e.svc.Check(ctx, c.PublicID, nil)
	if err != nil || res.OK || !res.Steps[0].OK || res.Steps[1].OK || res.Steps[1].Skipped ||
		res.Steps[1].Code != bridge.CodeEventDeliveryNotOffered ||
		res.Steps[1].Message != "The bridge does not offer push event delivery." || res.Info == nil ||
		len(e.store.infos) != stored+1 {
		t.Errorf("push not offered = %+v, %v", res, err)
	}
	if got, _ := e.svc.Get(ctx, c.PublicID); !bytes.Contains(got.BridgeInfo, []byte(`["poll"]`)) {
		t.Errorf("the info of a failed event_delivery is not stored: %s", got.BridgeInfo)
	}
}

// TestBridgeCheckFailures is C-22.FR-2: a step that fails without a Bridge code of its own carries the code of
// delivery.FailureCode — timeout for a request without an answer in time, blocked for an address the outbound address
// policy refuses, connect_failed for a connection the bridge closes, request_failed for another error answer, with
// the bridge's masked detail; an unsaved base URL is refused, a limiter without a token in time is a
// *delivery.LimitedError and a failed store of the info is an error.
func TestBridgeCheckFailures(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	c := e.createBridge(t, "br", b.srv.URL, connections.EventDeliveryPoll)

	b.set(func(b *standIn) { b.status = http.StatusInternalServerError })
	res, err := e.svc.Check(ctx, c.PublicID, nil)
	if err != nil || res.OK || res.Steps[0].Code != delivery.CodeRequestFailed ||
		!strings.Contains(res.Steps[0].Message, "the database is down for") ||
		strings.Contains(res.Steps[0].Message, bridgeToken) || !res.Steps[1].Skipped {
		t.Errorf("an error answer = %+v, %v", res, err)
	}

	b.set(func(*standIn) {})
	expired, cancel := context.WithTimeout(ctx, 0)
	res, err = e.svc.Check(expired, c.PublicID, nil)
	cancel()
	if err != nil || res.OK || res.Steps[0].Code != delivery.CodeTimeout {
		t.Errorf("no answer = %+v, %v", res, err)
	}

	b.set(func(b *standIn) { b.hijack = true })
	res, err = e.svc.Check(ctx, c.PublicID, nil)
	if err != nil || res.OK || res.Steps[0].Code != delivery.CodeConnectFailed {
		t.Errorf("a closed connection = %+v, %v", res, err)
	}
	b.set(func(*standIn) {})

	logger := logging.New(e.log, logging.LevelInfo)
	strict, err := outbound.ParsePolicy("standard", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	clocks := clock.Clocks{Business: clock.NewManual(t0), Real: &ticking{at: t0, step: time.Millisecond}}
	blocked := connections.New(connections.Config{OrgID: 1, Store: e.store, Keyring: e.keys,
		Audit: audit.NewWriter(logger, clocks.Business), Clocks: clocks,
		Network:     mattermost.Network{Policy: outbound.StaticPolicy(strict), Log: logger, Real: clock.Real{}},
		Interactive: e.path, IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081"}, Log: logger})
	res, err = blocked.Check(ctx, c.PublicID, nil)
	if err != nil || res.OK || res.Steps[0].Code != delivery.CodeBlocked || res.Steps[0].Via != "direct" {
		t.Errorf("a blocked address = %+v, %v", res, err)
	}

	base := b.srv.URL
	if fe := fieldError(func() error { _, err := e.svc.Check(ctx, c.PublicID, &base); return err }()); fe == nil ||
		fe.Pointer != "/base_url" || fe.Code != connections.CodeUnsupported {
		t.Errorf("an unsaved base URL = %v", fe)
	}
	e.path.limited = true
	if _, err := e.svc.Check(ctx, c.PublicID, nil); !isLimited(err) {
		t.Errorf("limited = %v", err)
	}
	e.path.limited = false
	e.store.fail["SetBridgeInfo"] = errors.New("down")
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err == nil || !strings.Contains(err.Error(), "down") {
		t.Errorf("a failed store = %v", err)
	}
	delete(e.store.fail, "SetBridgeInfo")
	e.store.rows[c.ID].row.Proxy = []byte("{")
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err == nil {
		t.Error("an unreadable proxy")
	}
	e.store.rows[c.ID].row.Proxy = []byte(`{"enabled":false}`)
	e.store.rows[c.ID].row.ProxyPasswordCiphertext = []byte("garbage")
	e.store.rows[c.ID].row.ProxyPasswordKeyID = pgtype.Text{String: e.keys.ActiveKeyID(), Valid: true}
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err == nil {
		t.Error("an unreadable proxy password")
	}
	e.store.rows[c.ID].row.BotTokenCiphertext = []byte("garbage")
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err == nil || strings.Contains(err.Error(), bridgeToken) {
		t.Errorf("an unreadable token = %v", err)
	}
}

// TestBridgeCheckThroughProxy is C-22.AC-3: with an HTTP proxy on the Connection, the check reports the path proxy and
// its request reaches the bridge only through the proxy; with the bridge closing the connection or echoing the token
// in its error answer, neither the token nor the push secret is in a step, a returned error or the log.
func TestBridgeCheckThroughProxy(t *testing.T) {
	e, b := newBridgeEnv(t)
	p, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	in := bridgeInput("br", b.srv.URL, connections.EventDeliveryPush)
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new(p.Addr())}
	c, err := e.svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Check(t.Context(), c.PublicID, nil)
	if err != nil || !res.OK || res.Steps[0].Via != bridge.ViaProxy || res.Steps[1].Via != bridge.ViaProxy {
		t.Fatalf("check = %+v, %v", res, err)
	}
	reqs, targets := b.seen(), p.Targets()
	if len(reqs) != 1 || len(targets) != len(reqs) || "http://"+targets[0] != b.srv.URL {
		t.Errorf("%d requests reached the bridge, through the proxy to %v", len(reqs), targets)
	}

	var texts []string
	for _, set := range []func(*standIn){
		func(b *standIn) { b.hijack = true },
		func(b *standIn) { b.status = http.StatusBadGateway },
		func(b *standIn) { b.status = http.StatusForbidden },
	} {
		b.set(set)
		res, err := e.svc.Check(t.Context(), c.PublicID, nil)
		if err != nil || res.OK {
			t.Fatalf("check = %+v, %v", res, err)
		}
		for _, st := range res.Steps {
			texts = append(texts, st.Message)
		}
	}
	in.Name, in.PushSecret = "br-2", keyring.Replace("whsec_"+bridgeToken+"!")
	_, err = e.svc.Create(t.Context(), by, in)
	texts = append(texts, err.Error())
	all := strings.Join(texts, "\n")
	if !strings.Contains(all, "[redacted]") {
		t.Errorf("the echoed token is not masked:\n%s", all)
	}
	for _, s := range []string{bridgeToken, pushSecret, strings.TrimPrefix(pushSecret, "whsec_")} {
		if strings.Contains(all, s) || strings.Contains(e.log.String(), s) {
			t.Errorf("a secret reached a step, an error or the log:\n%s", all)
		}
	}
}

// TestBridgeStoredInfo is the bridge.InfoStore of the Service: the info a check stored, for the version of the
// Connection it was read for, and none for another version, a deleted, unknown or other Connection, or a copy that does
// not decode; a store for a Connection that changed since stores nothing and sends no hint.
func TestBridgeStoredInfo(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	c := e.createBridge(t, "br", b.srv.URL, connections.EventDeliveryPoll)
	conn := bridge.Conn{ID: c.ID, PublicID: c.PublicID, Version: c.Version}
	if _, ok, err := e.svc.StoredInfo(ctx, conn); ok || err != nil {
		t.Errorf("before the first check = %v, %v", ok, err)
	}
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	st, ok, err := e.svc.StoredInfo(ctx, conn)
	if err != nil || !ok || st.Info.Messenger.Name != "Fake chat" || !st.ReadAt.Equal(t0) ||
		!bytes.Contains(st.Info.Raw, []byte("future_field")) {
		t.Fatalf("stored = %+v, %v, %v", st, ok, err)
	}
	stale := conn
	stale.Version++
	if _, ok, err := e.svc.StoredInfo(ctx, stale); ok || err != nil {
		t.Errorf("another version = %v, %v", ok, err)
	}
	hints, infos := len(e.store.hints), len(e.store.infos)
	if err := e.svc.StoreInfo(ctx, stale, st.Info, t0.Add(time.Hour)); err != nil || len(e.store.hints) != hints ||
		len(e.store.infos) != infos+1 {
		t.Errorf("a store for a changed Connection = %v, %d hints", err, len(e.store.hints)-hints)
	}
	if st, _, _ := e.svc.StoredInfo(ctx, conn); !st.ReadAt.Equal(t0) {
		t.Errorf("a store for a changed Connection changed the info: %+v", st)
	}
	e.store.fail["Notify"] = errors.New("down")
	if err := e.svc.StoreInfo(ctx, conn, st.Info, t0); err == nil {
		t.Error("a failed hint")
	}
	delete(e.store.fail, "Notify")
	e.store.rows[c.ID].row.BridgeInfo = []byte(`"not an info"`)
	if _, ok, err := e.svc.StoredInfo(ctx, conn); ok || err != nil {
		t.Errorf("a copy that does not decode = %v, %v", ok, err)
	}
	mm := e.create(t, "mm")
	if _, ok, err := e.svc.StoredInfo(ctx, bridge.Conn{ID: mm.ID, Version: mm.Version}); ok || err != nil {
		t.Errorf("a Mattermost Connection = %v, %v", ok, err)
	}
	if _, ok, err := e.svc.StoredInfo(ctx, bridge.Conn{ID: 999, Version: 1}); ok || err != nil {
		t.Errorf("an unknown Connection = %v, %v", ok, err)
	}
	e.store.fail["GetConnection"] = errors.New("down")
	if _, _, err := e.svc.StoredInfo(ctx, conn); err == nil {
		t.Error("a failed read")
	}
}

// TestBridgeIsNotMattermost: the operations of Mattermost and Telegram Connections refuse a Bridge Connection.
func TestBridgeIsNotMattermost(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	c := e.createBridge(t, "br", b.srv.URL, connections.EventDeliveryPoll)
	if _, err := e.svc.Channels(ctx, c.PublicID, "", ""); !errors.Is(err, connections.ErrNotMattermost) {
		t.Errorf("channels = %v", err)
	}
	if _, err := e.svc.CheckChannel(ctx, destinations.ChannelCheck{Connection: c.PublicID, TeamID: "t",
		ChannelID: "c"}); !errors.Is(err, destinations.ErrUnknownConnection) {
		t.Errorf("a Destination check = %v", err)
	}
	if _, err := e.svc.Connection(ctx, c.PublicID); !errors.Is(err, mattermost.ErrNoConnection) {
		t.Errorf("a callback = %v", err)
	}
	if _, err := e.svc.TelegramConn(ctx, c.PublicID); !errors.Is(err, connections.ErrNotFound) {
		t.Errorf("a Telegram read = %v", err)
	}
	if len(b.seen()) != 0 {
		t.Errorf("%d requests reached the bridge", len(b.seen()))
	}
}

// TestEnsureDemoBridge is C-01.FR-13: the demo Bridge Connection of `muster dev` is created once, in poll mode, with
// the default limiter.
func TestEnsureDemoBridge(t *testing.T) {
	e, b := newBridgeEnv(t)
	d := connections.Demo{Type: connections.TypeBridge, Name: "Dev Bridge", BaseURL: b.srv.URL + "/",
		BotToken: bridgeToken}
	for range 2 {
		if err := e.svc.EnsureDemo(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.store.rows) != 1 {
		t.Fatalf("%d rows", len(e.store.rows))
	}
	c := e.store.rows[1].row
	if c.Type != connections.TypeBridge || c.BridgeBaseUrl.String != b.srv.URL ||
		c.BridgeEventDelivery.String != connections.EventDeliveryPoll || c.BridgePushSecretCiphertext != nil ||
		c.LimiterLimit != connections.BridgeDefaultLimit || c.LimiterPerSeconds != connections.BridgeDefaultPerSeconds {
		t.Errorf("demo %+v", c)
	}
	if res, err := e.svc.Check(t.Context(), c.PublicID, nil); err != nil || !res.OK {
		t.Errorf("check of the demo = %+v, %v", res, err)
	}
}

// startFakeBridge starts the fake bridge of `muster dev`, whose channels the Destination checks read.
func startFakeBridge(t *testing.T) *fakebridge.Fake {
	t.Helper()
	f := fakebridge.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	return f
}

// createFakeBridge creates the Bridge Connection name to the fake bridge f with the bot of token, in mode.
func (e *env) createFakeBridge(t *testing.T, f *fakebridge.Fake, name, token, mode string) connections.Connection {
	t.Helper()
	in := bridgeInput(name, f.URL(), mode)
	in.BotToken = keyring.Replace(logging.Secret(token))
	c, err := e.svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCheckBridgeChannel is the Destination check of a Bridge Destination for the Destination write path
// (C-22.FR-3): checkChannel through the Bridge Connection, limited by the Destination once it exists and by the
// Connection before, with the code of a refused channel; a missing, deleted, malformed or Mattermost Connection is
// destinations.ErrUnknownConnection, and no limiter token within the budget sends nothing.
func TestCheckBridgeChannel(t *testing.T) {
	e := newEnv(t)
	f := startFakeBridge(t)
	ctx := t.Context()
	c := e.createFakeBridge(t, f, "br", fakebridge.Token, connections.EventDeliveryPoll)
	res, err := e.svc.CheckBridgeChannel(ctx, destinations.BridgeChannelCheck{Connection: strings.ToLower(c.PublicID),
		ChannelID: fakebridge.ChannelAlerts})
	if err != nil || res.ConnectionID != c.ID || !res.Check.OK() || res.Check.Name != "alerts" {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if s := e.path.subjects[len(e.path.subjects)-1]; s.Destination != nil || s.Connection == nil ||
		*s.Connection != c.ID {
		t.Errorf("subject %+v", s)
	}
	f.ResetRequests()
	res, err = e.svc.CheckBridgeChannel(ctx, destinations.BridgeChannelCheck{Connection: c.PublicID,
		Destination: new(int64(9)), ChannelID: fakebridge.ChannelNoBot})
	if err != nil || res.Check.OK() || res.Check.Code != bridge.CodeChannelForbidden ||
		res.Check.Message != fakebridge.MessageNotMember || res.Check.Outcome.Kind != delivery.OutcomeFatal {
		t.Errorf("ch-nobot = %+v, %v", res, err)
	}
	if reqs := f.Requests(); len(reqs) != 1 || reqs[0].Method != http.MethodPost ||
		!strings.HasSuffix(reqs[0].Path, "/channels/"+fakebridge.ChannelNoBot+"/check") {
		t.Errorf("ch-nobot reached the bridge with %+v, not one channel check", reqs)
	}
	if s := e.path.subjects[len(e.path.subjects)-1]; s.Destination == nil || s.Destination.ID != 9 ||
		s.Destination.Type != connections.TypeBridge || *s.Destination.Connection != c.ID {
		t.Errorf("subject %+v", s)
	}
	mm := e.create(t, "mm")
	gone := e.createFakeBridge(t, f, "gone", fakebridge.Token, connections.EventDeliveryPoll)
	e.store.rows[gone.ID].deleted = true
	f.ResetRequests()
	for _, id := range []string{mm.PublicID, gone.PublicID, "CN0000000000ZZ", "not-an-id"} {
		if _, err := e.svc.CheckBridgeChannel(ctx, destinations.BridgeChannelCheck{Connection: id,
			ChannelID: fakebridge.ChannelAlerts}); !errors.Is(err, destinations.ErrUnknownConnection) {
			t.Errorf("%s = %v", id, err)
		}
	}
	e.path.limited = true
	if _, err := e.svc.CheckBridgeChannel(ctx, destinations.BridgeChannelCheck{Connection: c.PublicID,
		ChannelID: fakebridge.ChannelAlerts}); !errors.As(err, new(*delivery.LimitedError)) {
		t.Errorf("limited = %v", err)
	}
	e.path.limited = false
	if n := len(f.Requests()); n != 0 {
		t.Errorf("%d requests reached the bridge", n)
	}
	e.store.fail["GetConnection"] = errors.New("down")
	if _, err := e.svc.CheckBridgeChannel(ctx, destinations.BridgeChannelCheck{Connection: c.PublicID,
		ChannelID: fakebridge.ChannelAlerts}); err == nil || errors.Is(err, destinations.ErrUnknownConnection) {
		t.Errorf("a failed read = %v", err)
	}
	delete(e.store.fail, "GetConnection")
	e.store.rows[c.ID].row.BotTokenCiphertext = []byte("garbage")
	if _, err := e.svc.CheckBridgeChannel(ctx, destinations.BridgeChannelCheck{Connection: c.PublicID,
		ChannelID: fakebridge.ChannelAlerts}); err == nil || !strings.Contains(err.Error(), "open the token") {
		t.Errorf("a token that cannot be opened = %v", err)
	}
}

// TestCheckBridgeChannelThroughProxy covers C-22.AC-3 for the Destination check: with an HTTP proxy on the Connection
// its request reaches the bridge only through the proxy; with the bridge closing the connection, neither the token
// nor the push secret is in the check or the log.
func TestCheckBridgeChannelThroughProxy(t *testing.T) {
	e, b := newBridgeEnv(t)
	f := startFakeBridge(t)
	p, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	in := bridgeInput("br", f.URL(), connections.EventDeliveryPush)
	in.BotToken = keyring.Replace(fakebridge.Token)
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new(p.Addr())}
	c, err := e.svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.CheckBridgeChannel(t.Context(), destinations.BridgeChannelCheck{Connection: c.PublicID,
		ChannelID: fakebridge.ChannelAlerts})
	if err != nil || !res.Check.OK() {
		t.Fatalf("check = %+v, %v", res, err)
	}
	reqs, targets := f.Requests(), p.Targets()
	if len(reqs) != 1 || len(targets) != len(reqs) || "http://"+targets[0] != f.URL() {
		t.Errorf("%d requests reached the bridge, through the proxy to %v", len(reqs), targets)
	}
	b.set(func(b *standIn) { b.hijack = true })
	in = bridgeInput("cut", b.srv.URL, connections.EventDeliveryPush)
	cut, err := e.svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.CheckBridgeChannel(t.Context(), destinations.BridgeChannelCheck{Connection: cut.PublicID,
		ChannelID: fakebridge.ChannelAlerts})
	if err != nil || res.Check.OK() || res.Check.Code != delivery.CodeRequestFailed && res.Check.Code !=
		delivery.CodeConnectFailed {
		t.Fatalf("a closed connection = %+v, %v", res, err)
	}
	for _, s := range []string{bridgeToken, pushSecret, strings.TrimPrefix(pushSecret, "whsec_")} {
		if strings.Contains(res.Check.Message+string(res.Check.Outcome.Error), s) {
			t.Errorf("a secret reached the check: %+v", res.Check)
		}
	}
}

// TestBridgeTarget is the bridge.Targets of the Service for the Bridge delivery (C-22.FR-4): the Target of a Bridge
// Destination — its channel and its Connection with the protocol client — posts and checks through that Connection;
// the client is kept while the Connection is unchanged and built again after it changes, so that a new token posts as
// the new bot; a Destination that is unknown, of a deleted Connection or of one that is not a Bridge has none; a read
// that fails, or a token that cannot be opened, is an error.
func TestBridgeTarget(t *testing.T) {
	e := newEnv(t)
	f := startFakeBridge(t)
	ctx := t.Context()
	c := e.createFakeBridge(t, f, "br", fakebridge.Token, connections.EventDeliveryPoll)
	e.store.brTargets[21] = memBRTarget{connection: c.ID, channel: fakebridge.ChannelAlerts}
	tg, err := e.svc.BridgeTarget(ctx, 21)
	if err != nil || tg.ChannelID != fakebridge.ChannelAlerts || tg.Conn.ID != c.ID || tg.Conn.PublicID != c.PublicID ||
		tg.Conn.Version != c.Version || tg.Conn.Client == nil {
		t.Fatalf("target = %+v, %v", tg, err)
	}
	if again, err := e.svc.BridgeTarget(ctx, 21); err != nil || again.Conn.Client != tg.Conn.Client {
		t.Fatalf("the client of an unchanged Connection was built again: %v", err)
	}
	a := &bridge.Adapter{Targets: e.svc}
	call := delivery.Call{Class: outbound.ClassDelivery, Key: "pub-DS000000000021-1-1",
		Destination: delivery.Destination{ID: 21, PublicID: "DS000000000021", Type: delivery.TypeBridge}}
	m := messages.Message{Language: "en", Colour: messages.ColourFiring, Heading: &messages.Heading{Number: 1,
		Title: "DiskFull", URL: "http://localhost:8080/alert-groups/AG00000000000A"}, Buttons: []messages.Button{}}
	if o := a.Publish(ctx, call, m); o.Kind != delivery.OutcomeOK || o.MessageID != "m1" {
		t.Fatalf("publish = %+v", o)
	}
	if o := a.Check(ctx, call); o.Kind != delivery.OutcomeOK {
		t.Fatalf("check = %+v", o)
	}

	in := bridgeInput("br", f.URL(), connections.EventDeliveryPoll)
	in.BotToken = keyring.Replace(fakebridge.SecondToken)
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	moved, err := e.svc.BridgeTarget(ctx, 21)
	if err != nil || moved.Conn.Version != c.Version+1 || moved.Conn.Client == tg.Conn.Client {
		t.Fatalf("the target after a new token = %+v, %v", moved, err)
	}
	call.Key = "pub-DS000000000021-2-1"
	if o := a.Publish(ctx, call, m); o.Kind != delivery.OutcomeOK || o.MessageID != "m2" {
		t.Fatalf("publish after a new token = %+v", o)
	}
	if ms := f.Messages(); len(ms) != 2 || ms[0].Bot != 1 || ms[1].Bot != 2 ||
		ms[1].ChannelID != fakebridge.ChannelAlerts {
		t.Errorf("messages %+v", ms)
	}

	mm := e.create(t, "mm")
	gone := e.createFakeBridge(t, f, "gone", fakebridge.Token, connections.EventDeliveryPoll)
	e.store.brTargets[22] = memBRTarget{connection: mm.ID, channel: fakebridge.ChannelAlerts}
	e.store.brTargets[23] = memBRTarget{connection: gone.ID, channel: fakebridge.ChannelAlerts}
	if _, err := e.svc.BridgeTarget(ctx, 23); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, by, gone.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{22, 23, 99} {
		if _, err := e.svc.BridgeTarget(ctx, id); !errors.Is(err, bridge.ErrNoTarget) {
			t.Errorf("target %d = %v", id, err)
		}
	}
	e.store.fail["GetBridgeDestinationTarget"] = errors.New("down")
	if _, err := e.svc.BridgeTarget(ctx, 21); err == nil || errors.Is(err, bridge.ErrNoTarget) {
		t.Errorf("unreadable = %v", err)
	}
	delete(e.store.fail, "GetBridgeDestinationTarget")
	e.store.rows[c.ID].row.BotTokenCiphertext = []byte("garbage")
	e.store.rows[c.ID].row.Version++
	if _, err := e.svc.BridgeTarget(ctx, 21); err == nil || !strings.Contains(err.Error(), "open the token") {
		t.Errorf("a token that cannot be opened = %v", err)
	}
}

// TestBridgeInfoForDelivery is C-22.FR-7's limit as delivery reads it: BridgeInfo reads the info of a Target's
// Connection through the InfoCache that the Connection check fills, so a checked Connection reads nothing, a read
// within bridge.info_max_age on the business clock reads nothing, and one past it reads getInfo again and stores it; a
// read that then fails keeps the stale copy with the failed Result.
func TestBridgeInfoForDelivery(t *testing.T) {
	e := newEnv(t)
	f := startFakeBridge(t)
	ctx := t.Context()
	c := e.createFakeBridge(t, f, "br", fakebridge.Token, connections.EventDeliveryPoll)
	e.store.brTargets[21] = memBRTarget{connection: c.ID, channel: fakebridge.ChannelAlerts}
	infoReads := func() int {
		n := 0
		for _, r := range f.Requests() {
			if r.Path == "/v1/info" {
				n++
			}
		}
		return n
	}
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	tg, err := e.svc.BridgeTarget(ctx, 21)
	if err != nil {
		t.Fatal(err)
	}
	if i, r := e.svc.BridgeInfo(ctx, tg.Conn, outbound.ClassDelivery); !r.OK() || i.Limits.MaxBodyLength != 4000 ||
		infoReads() != 1 {
		t.Fatalf("info after the check = %+v, %+v, %d reads", i.Limits, r, infoReads())
	}
	cfg := f.Config()
	cfg.Limits.MaxBodyLength = 1000
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	e.business.Advance(bridge.DefaultInfoMaxAge - time.Second)
	if i, _ := e.svc.BridgeInfo(ctx, tg.Conn, outbound.ClassDelivery); i.Limits.MaxBodyLength != 4000 ||
		infoReads() != 1 {
		t.Errorf("info within its maximum age = %+v, %d reads", i.Limits, infoReads())
	}
	e.business.Advance(2 * time.Second)
	infos := len(e.store.infos)
	if i, r := e.svc.BridgeInfo(ctx, tg.Conn, outbound.ClassDelivery); !r.OK() || i.Limits.MaxBodyLength != 1000 ||
		infoReads() != 2 || len(e.store.infos) != infos+1 {
		t.Errorf("info past its maximum age = %+v, %+v, %d reads", i.Limits, r, infoReads())
	}
	cfg.Revoked = []string{fakebridge.Token}
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	e.business.Advance(bridge.DefaultInfoMaxAge)
	if i, r := e.svc.BridgeInfo(ctx, tg.Conn, outbound.ClassDelivery); r.OK() || r.Code != bridge.CodeTokenInvalid ||
		i.Limits.MaxBodyLength != 1000 || infoReads() != 3 {
		t.Errorf("a failed read = %+v, %+v, %d reads", i.Limits, r, infoReads())
	}
}

// TestBridgeInfoForgottenOnDelete: deleting a Bridge Connection drops the info that the InfoCache keeps of it in
// memory, so that a read through a Conn taken before the deletion finds no copy, since the store keeps none for a
// deleted Connection, and reads getInfo again instead of serving the deleted Connection's info.
func TestBridgeInfoForgottenOnDelete(t *testing.T) {
	e := newEnv(t)
	f := startFakeBridge(t)
	ctx := t.Context()
	c := e.createFakeBridge(t, f, "br", fakebridge.Token, connections.EventDeliveryPoll)
	e.store.brTargets[21] = memBRTarget{connection: c.ID, channel: fakebridge.ChannelAlerts}
	infoReads := func() int {
		n := 0
		for _, r := range f.Requests() {
			if r.Path == "/v1/info" {
				n++
			}
		}
		return n
	}
	tg, err := e.svc.BridgeTarget(ctx, 21)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, r := e.svc.BridgeInfo(ctx, tg.Conn, outbound.ClassDelivery); !r.OK() || infoReads() != 1 {
			t.Fatalf("info before the deletion = %+v, %d reads", r, infoReads())
		}
	}
	if err := e.svc.Delete(ctx, by, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if _, r := e.svc.BridgeInfo(ctx, tg.Conn, outbound.ClassDelivery); !r.OK() || infoReads() != 2 {
		t.Errorf("info after the deletion = %+v, %d reads; the copy in memory outlived the Connection", r, infoReads())
	}
}

// TestBridgePushConnection is the Connection of a pushed Bridge event (C-22.FR-11): a Bridge Connection in either
// mode, by its public_id in any case, with its mode, and in push mode a client whose settings hold the push secret,
// kept while the Connection is unchanged; a Connection in poll mode has no client, and its secrets are not opened, so
// that one whose token cannot be opened is still only in poll mode; a Connection that is unknown, deleted, malformed
// or not a Bridge is bridge.ErrNoConnection; a failed read, or a push secret that cannot be opened in push mode, is
// another error.
func TestBridgePushConnection(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	push := e.createBridge(t, "br-push", b.srv.URL, connections.EventDeliveryPush)
	poll := e.createBridge(t, "br-poll", b.srv.URL, connections.EventDeliveryPoll)
	c, mode, err := e.svc.PushConnection(ctx, strings.ToLower(push.PublicID))
	if err != nil || mode != bridge.EventDeliveryPush || c.ID != push.ID || c.PublicID != push.PublicID ||
		c.Version != push.Version || c.Client == nil {
		t.Fatalf("push = %+v, %q, %v", c, mode, err)
	}
	if again, _, err := e.svc.PushConnection(ctx, push.PublicID); err != nil || again.Client != c.Client {
		t.Errorf("the client of an unchanged Connection was built again: %v", err)
	}
	if c, mode, err := e.svc.PushConnection(ctx, poll.PublicID); err != nil || mode != bridge.EventDeliveryPoll ||
		c.ID != poll.ID || c.PublicID != poll.PublicID || c.Client != nil {
		t.Errorf("poll = %+v, %q, %v", c, mode, err)
	}
	e.store.rows[poll.ID].row.BotTokenCiphertext = []byte("garbage")
	e.store.rows[poll.ID].row.Version++
	if c, mode, err := e.svc.PushConnection(ctx, poll.PublicID); err != nil || mode != bridge.EventDeliveryPoll ||
		c.Client != nil {
		t.Errorf("poll with a token that cannot be opened = %+v, %q, %v", c, mode, err)
	}
	mm := e.create(t, "mm")
	gone := e.createBridge(t, "gone", b.srv.URL, connections.EventDeliveryPush)
	if err := e.svc.Delete(ctx, by, gone.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{mm.PublicID, gone.PublicID, "CN0000000000ZZ", "not-an-id", ""} {
		if _, _, err := e.svc.PushConnection(ctx, id); !errors.Is(err, bridge.ErrNoConnection) {
			t.Errorf("%q = %v", id, err)
		}
	}
	e.store.fail["GetConnection"] = errors.New("down")
	if _, _, err := e.svc.PushConnection(ctx, push.PublicID); err == nil || errors.Is(err, bridge.ErrNoConnection) {
		t.Errorf("a failed read = %v", err)
	}
	delete(e.store.fail, "GetConnection")
	e.store.rows[push.ID].row.BridgePushSecretCiphertext = []byte("garbage")
	e.store.rows[push.ID].row.Version++
	if _, _, err := e.svc.PushConnection(ctx, push.PublicID); err == nil ||
		!strings.Contains(err.Error(), "open the push secret") {
		t.Errorf("a push secret that cannot be opened = %v", err)
	}
}

// TestBridgeConn is the Connection of an Account link by code (C-22.FR-15): a Bridge Connection in either mode, by its
// public_id in any case, with its client, kept while the Connection is unchanged and built again after it changed; a
// Connection that is unknown, deleted, malformed or not a Bridge is bridge.ErrNoConnection; a failed read, or a token
// that cannot be opened, is another error.
func TestBridgeConn(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	poll := e.createBridge(t, "br-poll", b.srv.URL, connections.EventDeliveryPoll)
	c, err := e.svc.BridgeConn(ctx, strings.ToLower(poll.PublicID))
	if err != nil || c.ID != poll.ID || c.PublicID != poll.PublicID || c.Version != poll.Version || c.Client == nil {
		t.Fatalf("BridgeConn = %+v, %v", c, err)
	}
	if again, err := e.svc.BridgeConn(ctx, poll.PublicID); err != nil || again.Client != c.Client {
		t.Errorf("the client of an unchanged Connection was built again: %v", err)
	}
	e.store.rows[poll.ID].row.Version++
	if moved, err := e.svc.BridgeConn(ctx, poll.PublicID); err != nil || moved.Client == c.Client ||
		moved.Version != poll.Version+1 {
		t.Errorf("a changed Connection = %+v, %v", moved, err)
	}
	mm := e.create(t, "mm")
	gone := e.createBridge(t, "gone", b.srv.URL, connections.EventDeliveryPoll)
	if err := e.svc.Delete(ctx, by, gone.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{mm.PublicID, gone.PublicID, "CN0000000000ZZ", "not-an-id", ""} {
		if _, err := e.svc.BridgeConn(ctx, id); !errors.Is(err, bridge.ErrNoConnection) {
			t.Errorf("%q = %v", id, err)
		}
	}
	e.store.fail["GetConnection"] = errors.New("down")
	if _, err := e.svc.BridgeConn(ctx, poll.PublicID); err == nil || errors.Is(err, bridge.ErrNoConnection) {
		t.Errorf("a failed read = %v", err)
	}
	delete(e.store.fail, "GetConnection")
	e.store.rows[poll.ID].row.BotTokenCiphertext = []byte("garbage")
	e.store.rows[poll.ID].row.Version++
	if _, err := e.svc.BridgeConn(ctx, poll.PublicID); err == nil || errors.Is(err, bridge.ErrNoConnection) {
		t.Errorf("a token that cannot be opened = %v", err)
	}
}

// TestBridgePollingConnections is the list of the Leader's poller (C-22.FR-11): the Bridge Connections in poll mode
// that are not deleted, in id order, each with its client, kept while the Connection is unchanged and built again after
// it changed, and its stored cursor; a Connection whose client cannot be built is listed with the error instead of a
// client, which the poller logs, and a failed read is an error.
func TestBridgePollingConnections(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	first := e.createBridge(t, "br-1", b.srv.URL, connections.EventDeliveryPoll)
	e.createBridge(t, "br-push", b.srv.URL, connections.EventDeliveryPush)
	e.create(t, "mm")
	second := e.createBridge(t, "br-2", b.srv.URL, connections.EventDeliveryPoll)
	gone := e.createBridge(t, "gone", b.srv.URL, connections.EventDeliveryPoll)
	if err := e.svc.Delete(ctx, by, gone.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	cursor := "c-17"
	e.store.rows[second.ID].cursor = &cursor
	list, err := e.svc.PollingConnections(ctx)
	if err != nil || len(list) != 2 || list[0].Conn.ID != first.ID || list[0].Cursor != nil ||
		list[0].Conn.PublicID != first.PublicID || list[0].Conn.Version != first.Version || list[0].Conn.Client == nil ||
		list[1].Conn.ID != second.ID || list[1].Cursor == nil || *list[1].Cursor != "c-17" {
		t.Fatalf("polling = %+v, %v", list, err)
	}
	again, err := e.svc.PollingConnections(ctx)
	if err != nil || again[0].Conn.Client != list[0].Conn.Client {
		t.Errorf("the client of an unchanged Connection was built again: %v", err)
	}
	in := bridgeInput("br-1", b.srv.URL, connections.EventDeliveryPoll)
	in.BotToken = keyring.Replace("bridge-token-secret-replaced-42")
	if err := e.update(t, first.PublicID, in); err != nil {
		t.Fatal(err)
	}
	moved, err := e.svc.PollingConnections(ctx)
	if err != nil || moved[0].Conn.Version != first.Version+1 || moved[0].Conn.Client == list[0].Conn.Client {
		t.Errorf("the client after a new token = %+v, %v", moved, err)
	}
	e.store.rows[second.ID].row.BotTokenCiphertext = []byte("garbage")
	e.store.rows[second.ID].row.Version++
	e.log.Reset()
	left, err := e.svc.PollingConnections(ctx)
	if err != nil || len(left) != 2 || left[0].Conn.ID != first.ID || left[0].Err != nil ||
		left[1].Conn.ID != second.ID || left[1].Conn.PublicID != second.PublicID || left[1].Conn.Client != nil ||
		left[1].Err == nil || !strings.Contains(left[1].Err.Error(), "open the token") {
		t.Errorf("a token that cannot be opened = %+v, %v", left, err)
	}
	if strings.Contains(e.log.String(), "bridge_poll_failed") {
		t.Errorf("the list logged %s", e.log.String())
	}
	e.store.fail["ListBridgePollingConnections"] = errors.New("down")
	if _, err := e.svc.PollingConnections(ctx); err == nil {
		t.Error("a failed read listed Connections")
	}
}

// TestBridgeEventsCursor is the cursor of the poller (C-22.FR-11): stored and dropped for the version of the Connection
// it was read for, ignored once the Connection changed or was deleted, and forgotten by a new token.
func TestBridgeEventsCursor(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	c := e.createBridge(t, "br", b.srv.URL, connections.EventDeliveryPoll)
	cursor := func() *string {
		list, err := e.svc.PollingConnections(ctx)
		if err != nil || len(list) != 1 {
			t.Fatalf("polling = %+v, %v", list, err)
		}
		return list[0].Cursor
	}
	conn := bridge.Conn{ID: c.ID, PublicID: c.PublicID, Version: c.Version}
	next := "c-1"
	if err := e.svc.StoreEventsCursor(ctx, conn, &next); err != nil || cursor() == nil || *cursor() != "c-1" {
		t.Fatalf("stored %v, %v", cursor(), err)
	}
	in := bridgeInput("br-renamed", b.srv.URL, connections.EventDeliveryPoll)
	in.BotToken = keyring.Keep
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	stale := "c-2"
	if err := e.svc.StoreEventsCursor(ctx, conn, &stale); err != nil || *cursor() != "c-1" {
		t.Errorf("a poller of an earlier version stored %v, %v", *cursor(), err)
	}
	conn.Version++
	if err := e.svc.StoreEventsCursor(ctx, conn, nil); err != nil || cursor() != nil {
		t.Errorf("dropped %v, %v", cursor(), err)
	}
	if err := e.svc.StoreEventsCursor(ctx, conn, &stale); err != nil || *cursor() != "c-2" {
		t.Errorf("stored again %v, %v", cursor(), err)
	}
	in.BotToken = keyring.Replace("bridge-token-secret-replaced-42")
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	if got := cursor(); got != nil {
		t.Errorf("a new token kept the cursor %s", *got)
	}
	e.store.fail["StoreBridgeEventsCursor"] = errors.New("down")
	if err := e.svc.StoreEventsCursor(ctx, conn, &next); err == nil ||
		!strings.Contains(err.Error(), "store the events cursor of the connection "+c.PublicID) {
		t.Errorf("a failed store = %v", err)
	}
}

// TestBridgeHandleEventOnce is the claim of a Bridge event (C-22.FR-11): f runs under the claim lock of the
// Connection and the event id, and the id is recorded once f succeeded, at the business clock's now; an id recorded
// before runs nothing; an error of f, of the lock, of the read or of the record records nothing, so that the event runs
// again; the ids of each Connection are apart.
func TestBridgeHandleEventOnce(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	runs := 0
	f := func(context.Context) error {
		runs++
		return nil
	}
	if ran, err := e.svc.HandleEventOnce(ctx, 7, "ev-1", f); !ran || err != nil || runs != 1 {
		t.Fatalf("first = %v, %v, %d runs", ran, err, runs)
	}
	if at, ok := e.store.seen[memEvent{connection: 7, id: "ev-1"}]; !ok || !at.Equal(t0) {
		t.Errorf("recorded %v at %s", ok, at)
	}
	if len(e.store.locks) != 1 || e.store.locks[0].ConnectionID != 7 || e.store.locks[0].EventID != "ev-1" {
		t.Errorf("locks %+v", e.store.locks)
	}
	if ran, err := e.svc.HandleEventOnce(ctx, 7, "ev-1", f); ran || err != nil || runs != 1 {
		t.Errorf("again = %v, %v, %d runs", ran, err, runs)
	}
	if ran, err := e.svc.HandleEventOnce(ctx, 8, "ev-1", f); !ran || err != nil || runs != 2 {
		t.Errorf("another Connection = %v, %v, %d runs", ran, err, runs)
	}
	failed := errors.New("the dispatcher is down")
	if ran, err := e.svc.HandleEventOnce(ctx, 7, "ev-2", func(context.Context) error { return failed }); ran ||
		!errors.Is(err, failed) {
		t.Errorf("a failing f = %v, %v", ran, err)
	}
	if _, ok := e.store.seen[memEvent{connection: 7, id: "ev-2"}]; ok {
		t.Error("a failed event was recorded")
	}
	for _, query := range []string{"LockBridgeEvent", "BridgeEventSeen", "RecordBridgeEvent"} {
		e.store.fail[query] = errors.New("down")
		before := runs
		if ran, err := e.svc.HandleEventOnce(ctx, 7, "ev-2", f); ran || err == nil {
			t.Errorf("%s failing = %v, %v", query, ran, err)
		}
		if _, ok := e.store.seen[memEvent{connection: 7, id: "ev-2"}]; ok {
			t.Errorf("%s failing recorded the event", query)
		}
		if query != "RecordBridgeEvent" && runs != before {
			t.Errorf("%s failing ran f", query)
		}
		delete(e.store.fail, query)
	}
	if ran, err := e.svc.HandleEventOnce(ctx, 7, "ev-2", f); !ran || err != nil {
		t.Errorf("the event again = %v, %v", ran, err)
	}
}

// TestBridgePruneEventsSeen is the short-lived pruning of bridge_events_seen: the ids recorded more than
// bridge.event_dedup_window before now, at most limit of them; a failed delete is an error.
func TestBridgePruneEventsSeen(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	for i, id := range []string{"ev-1", "ev-2", "ev-3"} {
		e.store.seen[memEvent{connection: 7, id: id}] = t0.Add(time.Duration(i) * time.Hour)
	}
	if n, err := e.svc.PruneEventsSeen(ctx, t0.Add(bridge.EventDedupWindow), 10); n != 0 || err != nil {
		t.Errorf("within the window = %d, %v", n, err)
	}
	if n, err := e.svc.PruneEventsSeen(ctx, t0.Add(bridge.EventDedupWindow+90*time.Minute), 1); n != 1 || err != nil {
		t.Errorf("one past the window, limit 1 = %d, %v", n, err)
	}
	if n, err := e.svc.PruneEventsSeen(ctx, t0.Add(bridge.EventDedupWindow+90*time.Minute), 10); n != 1 ||
		err != nil || len(e.store.seen) != 1 {
		t.Errorf("the rest past the window = %d, %v, %d left", n, err, len(e.store.seen))
	}
	e.store.fail["PruneBridgeEventsSeen"] = errors.New("down")
	if _, err := e.svc.PruneEventsSeen(ctx, t0.Add(48*time.Hour), 10); err == nil {
		t.Error("a failed delete pruned")
	}
}
