// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/outbound"
)

// infoFixture is an info with every feature, as the fake bridge answers by default.
func infoFixture() map[string]any {
	return map[string]any{
		"protocol_version": "1",
		"bridge":           map[string]any{"name": "Test bridge", "version": "0.1.0"},
		"messenger":        map[string]any{"name": "Test chat"},
		"capabilities": map[string]any{
			"buttons": map[string]any{"max_per_message": 8, "max_per_row": 4, "data_max_bytes": 256,
				"styles": []any{"primary", "danger", "neutral", "success"}},
			"thread_messages":  "all",
			"direct_messages":  map[string]any{"bot_can_start": true},
			"private_answers":  []any{"ephemeral", "popup"},
			"native_mentions":  true,
			"quiet_messages":   true,
			"status_color":     true,
			"delete":           map[string]any{"max_age_seconds": 172800},
			"deletion_events":  true,
			"some_new_feature": map[string]any{"x": 1},
		},
		"limits": map[string]any{"max_body_length": 4000, "edit_max_age_seconds": 172800,
			"send_per_second_per_channel": 1, "send_per_second": 30.5},
		"user_lookup":       "email",
		"event_delivery":    []any{"poll", "push"},
		"link_url_template": "https://chat.example.org/bots/muster?start={token}",
		"future_field":      map[string]any{"kept": true},
	}
}

// at is the object at the dotted path of m.
func at(m map[string]any, path string) map[string]any {
	for p := range strings.SplitSeq(path, ".") {
		if p == "" {
			continue
		}
		m = m[p].(map[string]any)
	}
	return m
}

func validInfo(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	m := infoFixture()
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// set sets key in the object at path to v.
func set(path, key string, v any) func(map[string]any) {
	return func(m map[string]any) { at(m, path)[key] = v }
}

// del removes key from the object at path.
func del(path, key string) func(map[string]any) {
	return func(m map[string]any) { delete(at(m, path), key) }
}

func minimal(m map[string]any) {
	m["capabilities"] = map[string]any{}
	m["limits"] = map[string]any{"max_body_length": 500, "edit_max_age_seconds": nil}
	m["event_delivery"] = []any{"poll"}
	m["user_lookup"] = "none"
	delete(m, "link_url_template")
	delete(m, "future_field")
}

// bridgeInfoSchema is the schema Info of api/bridge.yaml, which checks the fixtures of these tests.
func bridgeInfoSchema(t *testing.T) *openapi3.Schema {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/bridge.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ref := doc.Components.Schemas["Info"]
	if ref == nil || ref.Value == nil {
		t.Fatal("no Info in api/bridge.yaml")
	}
	return ref.Value
}

func visit(t *testing.T, s *openapi3.Schema, body []byte) error {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return s.VisitJSON(v)
}

// TestParseValidInfo covers C-22.FR-2: valid infos parse, keep unknown fields in Raw, name their features, and pass
// the schema of api/bridge.yaml.
func TestParseValidInfo(t *testing.T) {
	schema := bridgeInfoSchema(t)
	for _, tc := range []struct {
		name     string
		mutate   func(map[string]any)
		features []string
	}{
		{"every feature", nil, []string{"buttons", "delete", "deletion_events", "direct_messages", "native_mentions",
			"private_answers", "quiet_messages", "status_color", "thread_messages"}},
		{"minimal", minimal, []string{}},
		{"switched off", func(m map[string]any) {
			c := at(m, "capabilities")
			c["native_mentions"], c["quiet_messages"], c["status_color"], c["deletion_events"] = false, false, false, false
			c["private_answers"] = []any{}
			delete(c, "buttons")
			at(m, "capabilities.delete")["max_age_seconds"] = nil
			m["link_url_template"] = nil
		}, []string{"delete", "direct_messages", "thread_messages"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := validInfo(t, tc.mutate)
			if err := visit(t, schema, body); err != nil {
				t.Fatalf("the fixture does not match api/bridge.yaml: %v", err)
			}
			i, err := ParseInfo(body)
			if err != nil {
				t.Fatal(err)
			}
			if string(i.ProtocolVersion) != ProtocolVersion || i.Messenger.Name != "Test chat" ||
				!bytes.Equal(i.Raw, body) || !slices.Equal(i.Features(), tc.features) {
				t.Fatalf("info %+v, features %v", i.Info, i.Features())
			}
			stored, err := DecodeInfo(i.Raw)
			if err != nil || !slices.Equal(stored.Features(), tc.features) || !bytes.Equal(stored.Raw, body) {
				t.Fatalf("decoded %+v, %v", stored, err)
			}
		})
	}
	i, err := ParseInfo(validInfo(t, nil))
	if err != nil || !strings.Contains(string(i.Raw), `"future_field":{"kept":true}`) ||
		!strings.Contains(string(i.Raw), "some_new_feature") {
		t.Fatalf("unknown fields are not kept: %s, %v", i.Raw, err)
	}
	if _, err := DecodeInfo([]byte(`{"bridge": 5}`)); err == nil {
		t.Fatal("a broken stored copy decoded")
	}
}

// padded is a valid info of exactly n bytes.
func padded(t *testing.T, n int) []byte {
	t.Helper()
	base := validInfo(t, set("", "padding", ""))
	return validInfo(t, set("", "padding", strings.Repeat("x", n-len(base))))
}

// TestInfoSize covers C-22.FR-2: an info larger than 64 KiB is refused before it is parsed.
func TestInfoSize(t *testing.T) {
	const tooLarge = "The bridge's answer does not match the protocol: info is larger than 64 KiB"
	if b := padded(t, InfoMaxBytes); len(b) != 65536 {
		t.Fatalf("fixture of %d bytes", len(b))
	} else if _, err := ParseInfo(b); err != nil {
		t.Fatalf("65,536 bytes refused: %v", err)
	}
	for _, body := range [][]byte{padded(t, InfoMaxBytes+1), bytes.Repeat([]byte("x"), InfoMaxBytes+1)} {
		_, err := ParseInfo(body)
		var ie *InfoError
		if !errors.As(err, &ie) || ie.Code != CodeProtocolMismatch || ie.Message != tooLarge {
			t.Fatalf("%d bytes: %v", len(body), err)
		}
	}
	if _, err := ParseInfo([]byte("xxxx")); err == nil || err.Error() != "The bridge's answer does not match the "+
		"protocol: info is not a JSON object" {
		t.Fatalf("a small body that is not JSON: %v", err)
	}
}

// TestUnstorableInfo: an info that jsonb would refuse — not UTF-8, the escape \u0000 or an unpaired surrogate, in a
// value or a key, in a known or an unknown member — does not match the protocol, so it never fails the store.
func TestUnstorableInfo(t *testing.T) {
	const placeholder = "PLACEHOLDER"
	in := func(mutate func(map[string]any), raw string) []byte {
		return bytes.Replace(validInfo(t, mutate), []byte(placeholder), []byte(raw), 1)
	}
	name := set("bridge", "name", placeholder)
	const (
		notUTF8 = "info is not valid UTF-8"
		nul     = `info contains the escape \u0000`
		lone    = "info contains an unpaired UTF-16 surrogate escape"
	)
	for _, tc := range []struct {
		name string
		body []byte
		want string
	}{
		{"not UTF-8", in(name, "a\xffb"), notUTF8},
		{"NUL in a value", in(name, `a\u0000b`), nul},
		{"NUL in a key", in(set("", placeholder, 1), `k\u0000`), nul},
		{"NUL in an unknown member", in(set("future_field", "kept", placeholder), `\u0000`), nul},
		{"lone high surrogate", in(name, `a\ud800`), lone},
		{"lone low surrogate", in(name, `\uDC00a`), lone},
		{"high surrogate before another escape", in(name, `\uD800\u0041`), lone},
		{"high surrogate before an escaped quote", in(name, `\ud800\"`), lone},
		{"high surrogate at the end", in(set("", placeholder, 1), `\ud800`), lone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseInfo(tc.body)
			var ie *InfoError
			if !errors.As(err, &ie) || ie.Code != CodeProtocolMismatch ||
				ie.Message != "The bridge's answer does not match the protocol: "+tc.want {
				t.Fatalf("err %v", err)
			}
		})
	}
	for _, raw := range []string{`\\u0000`, `\ud83d\ude00 \u0001 \"\u00e9`, `\\\\\ud83d\uDE00`} {
		if _, err := ParseInfo(in(name, raw)); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// TestInfoVersion covers C-22.FR-2 and AC-1: protocol_version is read before the rest is validated.
func TestInfoVersion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		code   string
		msg    string
	}{
		{"2", set("", "protocol_version", "2"), CodeProtocolVersionUnknown,
			"The bridge speaks protocol version 2; this Muster speaks 1."},
		{"2 with nothing else", func(m map[string]any) {
			for k := range m {
				delete(m, k)
			}
			m["protocol_version"] = "2"
		}, CodeProtocolVersionUnknown, "The bridge speaks protocol version 2; this Muster speaks 1."},
		{"long", set("", "protocol_version", strings.Repeat("9", 40)), CodeProtocolVersionUnknown,
			"The bridge speaks protocol version " + strings.Repeat("9", 32) + "…; this Muster speaks 1."},
		{"missing", del("", "protocol_version"), CodeProtocolMismatch,
			"The bridge's answer does not match the protocol: protocol_version is missing"},
		{"number", set("", "protocol_version", 1), CodeProtocolMismatch,
			"The bridge's answer does not match the protocol: protocol_version is not a string"},
		{"null", set("", "protocol_version", nil), CodeProtocolMismatch,
			"The bridge's answer does not match the protocol: protocol_version is not a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseInfo(validInfo(t, tc.mutate))
			var ie *InfoError
			if !errors.As(err, &ie) || ie.Code != tc.code || ie.Message != tc.msg {
				t.Fatalf("err %#v", err)
			}
		})
	}
}

// TestInvalidInfo covers C-22.FR-2: an info that does not match the protocol is refused naming the field, and the
// schema of api/bridge.yaml refuses it too.
func TestInvalidInfo(t *testing.T) {
	schema := bridgeInfoSchema(t)
	for _, tc := range []struct {
		mutate func(map[string]any)
		want   string
	}{
		{del("", "bridge"), "bridge is missing"},
		{set("", "bridge", nil), "bridge is null"},
		{set("", "bridge", "x"), "bridge is not an object"},
		{set("bridge", "name", ""), "bridge.name is not 1 to 100 characters long"},
		{set("bridge", "version", strings.Repeat("v", 101)), "bridge.version is not 1 to 100 characters long"},
		{set("bridge", "name", 5), "bridge.name has the wrong type"},
		{del("messenger", "name"), "messenger.name is missing"},
		{set("", "capabilities", nil), "capabilities is null"},
		{set("", "capabilities", "all"), "capabilities is not an object"},
		{del("limits", "edit_max_age_seconds"), "limits.edit_max_age_seconds is missing"},
		{set("limits", "max_body_length", 499), "limits.max_body_length is below 500"},
		{set("limits", "max_body_length", "500"), "limits.max_body_length has the wrong type"},
		{set("limits", "edit_max_age_seconds", 299), "limits.edit_max_age_seconds is below 300"},
		{set("limits", "send_per_second", 0), "limits.send_per_second is not positive"},
		{set("limits", "send_per_second_per_channel", -1), "limits.send_per_second_per_channel is not positive"},
		{set("limits", "send_per_second", nil), "limits.send_per_second is null"},
		{set("", "user_lookup", "phone"), "user_lookup has an unknown value"},
		{set("", "event_delivery", []any{}), "event_delivery is empty"},
		{set("", "event_delivery", []any{"poll", "poll"}), "event_delivery has a repeated value"},
		{set("", "event_delivery", []any{"webhook"}), "event_delivery has an unknown value"},
		{set("", "event_delivery", nil), "event_delivery is null"},
		{set("", "link_url_template", "ftp://chat/{token}"), "link_url_template is not an http or https link"},
		{set("", "link_url_template", "https://chat/start"), "link_url_template is not an http or https link"},
		{set("", "link_url_template", "https://chat/"+strings.Repeat("a", 1020)+"{token}"),
			"link_url_template is not an http or https link"},
		{set("capabilities.buttons", "max_per_message", 1), "capabilities.buttons.max_per_message is below 2"},
		{set("capabilities.buttons", "max_per_row", 0), "capabilities.buttons.max_per_row is below 1"},
		{set("capabilities.buttons", "data_max_bytes", 63), "capabilities.buttons.data_max_bytes is below 64"},
		{del("capabilities.buttons", "styles"), "capabilities.buttons.styles is missing"},
		{set("capabilities.buttons", "styles", nil), "capabilities.buttons.styles is null"},
		{set("capabilities.buttons", "styles", []any{"primary", "primary"}),
			"capabilities.buttons.styles has a repeated value"},
		{set("capabilities.buttons", "styles", []any{"blue"}), "capabilities.buttons.styles has an unknown value"},
		{set("capabilities", "buttons", nil), "capabilities.buttons is null"},
		{set("capabilities", "thread_messages", "some"), "capabilities.thread_messages has an unknown value"},
		{set("capabilities", "private_answers", []any{"toast"}), "capabilities.private_answers has an unknown value"},
		{set("capabilities", "private_answers", []any{"popup", "popup"}),
			"capabilities.private_answers has a repeated value"},
		{set("capabilities", "direct_messages", map[string]any{}),
			"capabilities.direct_messages.bot_can_start is missing"},
		{set("capabilities.delete", "max_age_seconds", 0), "capabilities.delete.max_age_seconds is below 1"},
		{set("capabilities", "native_mentions", nil), "capabilities.native_mentions is null"},
		{set("capabilities", "native_mentions", "yes"), "capabilities.native_mentions has the wrong type"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			body := validInfo(t, tc.mutate)
			if visit(t, schema, body) == nil {
				t.Fatal("api/bridge.yaml accepts the fixture")
			}
			_, err := ParseInfo(body)
			var ie *InfoError
			if !errors.As(err, &ie) || ie.Code != CodeProtocolMismatch ||
				!strings.HasPrefix(ie.Message, "The bridge's answer does not match the protocol: "+tc.want) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

// TestGetInfo covers C-22.FR-2 and AC-1 through the client: a refused token, an unknown version, a size beyond the
// outbound cap and an answer that is not JSON.
func TestGetInfo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer http.HandlerFunc
		code   string
		msg    string
	}{
		{"wrong token", answerJSON(http.StatusUnauthorized, "application/problem+json",
			`{"title":"Unauthorized","status":401,"class":"forbidden"}`), CodeTokenInvalid,
			"The bridge refused the token."},
		{"version 2", answerJSON(http.StatusOK, "application/json", `{"protocol_version":"2"}`),
			CodeProtocolVersionUnknown, "The bridge speaks protocol version 2; this Muster speaks 1."},
		{"version naming the token", answerJSON(http.StatusOK, "application/json",
			`{"protocol_version":"`+testToken+`"}`), CodeProtocolVersionUnknown,
			"The bridge speaks protocol version [redacted]; this Muster speaks 1."},
		{"beyond the outbound cap", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(bytes.Repeat([]byte(" "), maxAnswerBytes+1))
		}, CodeProtocolMismatch, "The bridge's answer does not match the protocol: info is larger than 64 KiB"},
		{"html", answerJSON(http.StatusOK, "text/html", "<html></html>"), CodeProtocolMismatch,
			"The bridge's answer does not match the protocol: info is not a JSON object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := startStandIn(t, tc.answer)
			c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken})
			_, r := c.GetInfo(t.Context(), outbound.ClassInteractive)
			if r.OK() || r.CheckCode() != tc.code || string(r.Error) != tc.msg {
				t.Fatalf("result %+v", r)
			}
		})
	}
}

func TestEventDeliveryStep(t *testing.T) {
	i, err := ParseInfo(validInfo(t, set("", "event_delivery", []any{"poll"})))
	if err != nil {
		t.Fatal(err)
	}
	if ok, code, msg := EventDeliveryStep(i, "poll"); !ok || code != "" || msg != "" {
		t.Fatalf("poll: %v %q %q", ok, code, msg)
	}
	ok, code, msg := EventDeliveryStep(i, "push")
	if ok || code != CodeEventDeliveryNotOffered || msg != "The bridge does not offer push event delivery." {
		t.Fatalf("push: %v %q %q", ok, code, msg)
	}
}

// memStore is an InfoStore in memory that counts its reads and writes and fails when told to. A copy belongs to the
// version of the Connection it was stored for, versions[id] when set, as a Connection's row does.
type memStore struct {
	mu        sync.Mutex
	copies    map[int64]Stored
	versions  map[int64]int64
	stores    []time.Time
	reads     int
	readErr   error
	storeErrs error
}

func (s *memStore) StoredInfo(_ context.Context, conn Conn) (Stored, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.readErr != nil {
		return Stored{}, false, s.readErr
	}
	if v, ok := s.versions[conn.ID]; ok && v != conn.Version {
		return Stored{}, false, nil
	}
	st, ok := s.copies[conn.ID]
	return st, ok, nil
}

func (s *memStore) StoreInfo(_ context.Context, conn Conn, i Info, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storeErrs != nil {
		return s.storeErrs
	}
	if v, ok := s.versions[conn.ID]; ok && v != conn.Version {
		return nil
	}
	if s.copies == nil {
		s.copies = map[int64]Stored{}
	}
	s.copies[conn.ID] = Stored{Info: i, ReadAt: at}
	s.stores = append(s.stores, at)
	return nil
}

// infoBridge is a stand-in bridge that answers info, or 500 once failing is set, and counts the reads.
type infoBridge struct {
	*standIn
	mu      sync.Mutex
	failing bool
}

func startInfoBridge(t *testing.T) *infoBridge {
	t.Helper()
	ib := &infoBridge{}
	body := validInfo(t, nil)
	ib.standIn = startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		ib.mu.Lock()
		failing := ib.failing
		ib.mu.Unlock()
		if failing {
			answerJSON(http.StatusInternalServerError, "application/problem+json",
				`{"title":"down","class":"transient"}`)(w, r)
			return
		}
		answerJSON(http.StatusOK, "application/json", string(body))(w, r)
	})
	return ib
}

func (ib *infoBridge) fail() {
	ib.mu.Lock()
	ib.failing = true
	ib.mu.Unlock()
}

// TestInfoCache covers C-22.FR-2 and FR-24: info is read again only when Muster's copy is older than
// bridge.info_max_age on the business clock; a fresh read is stored and logs bridge_info_read.
func TestInfoCache(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	b := startInfoBridge(t)
	var log bytes.Buffer
	n := network(t, &log)
	conn := Conn{ID: 7, PublicID: "CNAAAAAAAAAAA7", Client: newClient(t, n, Settings{BaseURL: b.srv.URL,
		Token: testToken})}
	business := clock.NewManual(t0)
	store := &memStore{}
	ic := NewInfoCache(store, business, 0, n.Log)

	i, r := ic.Info(t.Context(), conn, outbound.ClassDelivery)
	if !r.OK() || i.Messenger.Name != "Test chat" || len(b.seen()) != 1 || len(store.stores) != 1 ||
		!store.stores[0].Equal(t0) {
		t.Fatalf("first read: %+v, %d reads, stores %v", r, len(b.seen()), store.stores)
	}
	var line struct {
		Event      string   `json:"event"`
		Connection string   `json:"connection"`
		Version    string   `json:"protocol_version"`
		Features   []string `json:"features"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(log.Bytes()), &line); err != nil {
		t.Fatalf("log %q: %v", log.String(), err)
	}
	if line.Event != "bridge_info_read" || line.Connection != "CNAAAAAAAAAAA7" || line.Version != "1" ||
		!slices.Equal(line.Features, i.Features()) || len(line.Features) != 9 ||
		strings.Contains(log.String(), testToken) {
		t.Fatalf("log line %s", log.String())
	}

	business.Advance(9*time.Minute + 59*time.Second)
	if _, r := ic.Info(t.Context(), conn, outbound.ClassDelivery); !r.OK() || len(b.seen()) != 1 {
		t.Fatalf("within the max age: %+v, %d reads", r, len(b.seen()))
	}
	business.Advance(time.Second)
	if _, r := ic.Info(t.Context(), conn, outbound.ClassDelivery); !r.OK() || len(b.seen()) != 2 ||
		len(store.stores) != 2 || !store.stores[1].Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("past the max age: %+v, %d reads, stores %v", r, len(b.seen()), store.stores)
	}

	b.fail()
	business.Advance(11 * time.Minute)
	stale, r := ic.Info(t.Context(), conn, outbound.ClassDelivery)
	if r.OK() || r.Classification.Class != ClassTransient || stale.Messenger.Name != "Test chat" ||
		len(b.seen()) != 3 || len(store.stores) != 2 {
		t.Fatalf("failed refresh: %+v %+v, %d reads", r, stale.Info, len(b.seen()))
	}
}

// TestInfoCacheStoredCopies: a stored copy younger than the max age is used, an older one is read again, and a
// failure of the store is transient.
func TestInfoCacheStoredCopies(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	b := startInfoBridge(t)
	n := network(t, nil)
	conn := Conn{ID: 9, PublicID: "CNAAAAAAAAAAA9", Client: newClient(t, n, Settings{BaseURL: b.srv.URL,
		Token: testToken})}
	old, err := ParseInfo(validInfo(t, set("messenger", "name", "Old chat")))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		age   time.Duration
		name  string
		reads int
	}{
		{5 * time.Minute, "Old chat", 0},
		{11 * time.Minute, "Test chat", 1},
	} {
		before := len(b.seen())
		store := &memStore{copies: map[int64]Stored{9: {Info: old, ReadAt: t0.Add(-tc.age)}}}
		ic := NewInfoCache(store, clock.NewManual(t0), 10*time.Minute, n.Log)
		i, r := ic.Info(t.Context(), conn, outbound.ClassInteractive)
		if !r.OK() || i.Messenger.Name != tc.name || len(b.seen())-before != tc.reads {
			t.Fatalf("a copy %s old: %+v %q, %d reads", tc.age, r, i.Messenger.Name, len(b.seen())-before)
		}
	}

	broken := &memStore{readErr: errors.New("connection reset")}
	if _, r := NewInfoCache(broken, clock.NewManual(t0), 0, n.Log).Info(t.Context(), conn,
		outbound.ClassInteractive); r.Classification.Class != ClassTransient ||
		!strings.Contains(string(r.Error), "connection reset") {
		t.Fatalf("store read failure: %+v", r)
	}
	unwritable := &memStore{storeErrs: errors.New("read-only")}
	i, r := NewInfoCache(unwritable, clock.NewManual(t0), 0, n.Log).Info(t.Context(), conn, outbound.ClassInteractive)
	if r.Classification.Class != ClassTransient || i.Messenger.Name != "Test chat" {
		t.Fatalf("store write failure: %+v %+v", r, i.Info)
	}
}

// TestInfoCacheVersions: a copy belongs to the version of its Connection — a new version, as a new base URL or token
// makes, never serves the copy of the old one from memory — and Refresh reads again whatever the copy, stores, keeps
// and logs a fresh read once, changes nothing on a failed one and reports a failure of the store as an error.
func TestInfoCacheVersions(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	b := startInfoBridge(t)
	var log bytes.Buffer
	n := network(t, &log)
	client := newClient(t, n, Settings{BaseURL: b.srv.URL, Token: testToken})
	v1 := Conn{ID: 7, PublicID: "CNAAAAAAAAAAA7", Version: 1, Client: client}
	store := &memStore{versions: map[int64]int64{7: 1}}
	ic := NewInfoCache(store, clock.NewManual(t0), 0, n.Log)

	if _, r := ic.Info(t.Context(), v1, outbound.ClassInteractive); !r.OK() || len(b.seen()) != 1 {
		t.Fatalf("first read: %+v, %d reads", r, len(b.seen()))
	}
	if _, r := ic.Info(t.Context(), v1, outbound.ClassInteractive); !r.OK() || len(b.seen()) != 1 || store.reads != 1 {
		t.Fatalf("from memory: %+v, %d reads, %d store reads", r, len(b.seen()), store.reads)
	}
	// The Connection changed: its row is at version 2 and has forgotten its info.
	store.mu.Lock()
	store.versions[7] = 2
	delete(store.copies, 7)
	store.mu.Unlock()
	v2 := v1
	v2.Version = 2
	if _, r := ic.Info(t.Context(), v2, outbound.ClassInteractive); !r.OK() || len(b.seen()) != 2 ||
		len(store.stores) != 2 {
		t.Fatalf("a new version: %+v, %d reads, %d stores", r, len(b.seen()), len(store.stores))
	}
	if _, r := ic.Info(t.Context(), v1, outbound.ClassInteractive); !r.OK() || len(b.seen()) != 3 ||
		len(store.stores) != 2 {
		t.Fatalf("an old version: %+v, %d reads, %d stores", r, len(b.seen()), len(store.stores))
	}

	log.Reset()
	i, r, err := ic.Refresh(t.Context(), v2, outbound.ClassInteractive)
	if err != nil || !r.OK() || i.Messenger.Name != "Test chat" || len(b.seen()) != 4 || len(store.stores) != 3 ||
		strings.Count(log.String(), `"event":"bridge_info_read"`) != 1 {
		t.Fatalf("refresh: %+v %v, %d reads, %d stores, log %s", r, err, len(b.seen()), len(store.stores), log.String())
	}
	store.storeErrs = errors.New("read-only")
	if i, r, err := ic.Refresh(t.Context(), v2, outbound.ClassInteractive); err == nil || !r.OK() ||
		i.Messenger.Name != "Test chat" {
		t.Fatalf("refresh with a store that fails: %+v %v", r, err)
	}
	reads := store.reads
	if _, r := ic.Info(t.Context(), v2, outbound.ClassInteractive); !r.OK() || len(b.seen()) != 5 ||
		store.reads != reads {
		t.Fatalf("the copy kept in memory: %+v, %d reads", r, len(b.seen()))
	}
	b.fail()
	log.Reset()
	if i, r, err := ic.Refresh(t.Context(), v2, outbound.ClassInteractive); err != nil || r.OK() ||
		i.Messenger.Name != "" || strings.Contains(log.String(), "bridge_info_read") {
		t.Fatalf("a failed refresh: %+v %+v %v", r, i.Info, err)
	}
}

// TestInfoCacheForget: Forget drops the copy kept in memory of a deleted Connection, and only that one; the next read
// of the Connection goes to the store again, and forgetting a Connection without a copy changes nothing.
func TestInfoCacheForget(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	b := startInfoBridge(t)
	var log bytes.Buffer
	n := network(t, &log)
	client := newClient(t, n, Settings{BaseURL: b.srv.URL, Token: testToken})
	c7 := Conn{ID: 7, PublicID: "CNAAAAAAAAAAA7", Version: 1, Client: client}
	c8 := Conn{ID: 8, PublicID: "CNAAAAAAAAAAA8", Version: 1, Client: client}
	store := &memStore{}
	ic := NewInfoCache(store, clock.NewManual(t0), 0, n.Log)
	for _, c := range []Conn{c7, c8} {
		if _, r := ic.Info(t.Context(), c, outbound.ClassInteractive); !r.OK() {
			t.Fatalf("read %s: %+v", c.PublicID, r)
		}
	}
	kept := func() []int64 {
		ic.mu.Lock()
		defer ic.mu.Unlock()
		return slices.Sorted(maps.Keys(ic.copies))
	}
	if got := kept(); !slices.Equal(got, []int64{7, 8}) {
		t.Fatalf("kept %v", got)
	}
	ic.Forget(7)
	ic.Forget(9)
	if got := kept(); !slices.Equal(got, []int64{8}) {
		t.Errorf("after Forget(7): kept %v, want [8]", got)
	}
	reads := store.reads
	if _, r := ic.Info(t.Context(), c7, outbound.ClassInteractive); !r.OK() || store.reads != reads+1 ||
		len(b.seen()) != 2 {
		t.Errorf("a read after Forget: %+v, %d store reads, %d bridge reads", r, store.reads-reads, len(b.seen()))
	}
}
