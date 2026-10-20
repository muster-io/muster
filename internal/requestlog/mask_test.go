// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package requestlog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/keyring"
)

// testKeys sign action ids with a fixed MAC: the masking reads their structure, never their signature.
type testKeys struct{}

func (testKeys) Sign(keyring.Purpose, []byte) ([]byte, string, error) {
	return bytes.Repeat([]byte{0xa5}, 32), "k-0123456789abcdef", nil
}

func (testKeys) VerifyPrefix(keyring.Purpose, string, []byte, []byte) (bool, error) {
	return false, nil
}

func (testKeys) KeyIDs() []string { return nil }

func signed(t *testing.T, subject buttons.Subject, command string) string {
	t.Helper()
	id, _, err := buttons.Sign(testKeys{}, buttons.Action{Subject: subject, PublicID: "AGK7M3QX9P2RTA",
		Command: command})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// linkToken is an Account link token: 32 bytes in base64url, 43 characters.
func linkToken(b byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// TestMaskShape covers the shape masking of C-23.FR-3 and C-23.AC-3: signed action ids — nested in a Mattermost post,
// in a Telegram keyboard and a Bridge envelope, in text, in a query and behind JSON escapes — the token of `/start`
// in its forms, and the token of an account_link event wherever it stands in the JSON, escaped or not, are
// [redacted]; what only resembles them is kept.
func TestMaskShape(t *testing.T) {
	ack, resolve := signed(t, buttons.SubjectRoot, buttons.CommandAcknowledge), signed(t, buttons.SubjectReply,
		buttons.CommandResolve)
	test := signed(t, buttons.SubjectTest, buttons.CommandSnooze)
	tok, other := linkToken(1), linkToken(2)
	escaped := func(s string) string { // every character as \uXXXX
		var b strings.Builder
		for _, r := range s {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		return b.String()
	}
	tests := []struct {
		name     string
		in       string
		gone     []string
		want     string
		contains []string
	}{
		{name: "a Mattermost post", in: `{"channel_id":"c1","props":{"attachments":[{"actions":[{"id":"ack",` +
			`"integration":{"url":"http://muster/cb","context":{"action":"` + ack + `","key_id":"k-01"}}},` +
			`{"id":"res","integration":{"context":{"action":"` + resolve + `"}}}]}]}}`,
			gone: []string{ack, resolve}, contains: []string{`"action":"[redacted]","key_id":"k-01"`, `"channel_id":"c1"`}},
		{name: "a Telegram keyboard", in: `{"chat_id":-100,"reply_markup":{"inline_keyboard":[[{"text":"Ack",` +
			`"callback_data":"` + ack + `"},{"text":"Snooze","callback_data":"` + test + `"}]]}}`,
			gone: []string{ack, test}, contains: []string{`"callback_data":"[redacted]"`, `"text":"Snooze"`}},
		{name: "a Bridge envelope", in: `{"buttons":[{"label":"Resolve","data":"` + resolve + `","style":"success"}]}`,
			want: `{"buttons":[{"label":"Resolve","data":"[redacted]","style":"success"}]}`},
		{name: "text and a query", in: "pressed " + ack + ", then ?data=" + resolve + "&x=1",
			want: "pressed [redacted], then ?data=[redacted]&x=1"},
		{name: "an escaped action id", in: `{"callback_data":"` + escaped(ack) + `","text":"\u0410ck"}`,
			gone: []string{escaped(ack)}, want: `{"callback_data":"[redacted]","text":"\u0410ck"}`},
		{name: "a 51-character run that is no action id", in: `{"id":"` + strings.Repeat("Q", 51) + `"}`,
			want: `{"id":"` + strings.Repeat("Q", 51) + `"}`},
		{name: "/start in an update", in: `{"result":[{"message":{"text":"/start ` + tok + `"}}]}`,
			want: `{"result":[{"message":{"text":"/start [redacted]"}}]}`},
		{name: "/start with an escaped slash", in: `{"text":"\/start ` + tok + `"}`,
			want: `{"text":"\/start [redacted]"}`},
		{name: "/start to a named bot after an escaped newline", in: `{"text":"/start@muster_bot\n` + other + `"}`,
			want: `{"text":"/start@muster_bot\n[redacted]"}`},
		{name: "/start behind escapes", in: `{"text":"\u002fstart\u0020` + tok + `"}`, gone: []string{tok}},
		{name: "two /start in text", in: "/start " + tok + "/start " + other, want: "/start [redacted]/start [redacted]"},
		{name: "/start with 42 characters", in: "/start " + tok[:42], want: "/start " + tok[:42]},
		{name: "/start with 44 characters", in: "/start " + tok + "A", want: "/start " + tok + "A"},
		{name: "/start without a token", in: "/start", want: "/start"},
		{name: "an account_link event among events", in: `{"events":[{"id":"e1","token":"` + tok +
			`","type":"account_link","user":{"id":"u1"}},{"id":"e2","type":"press","data":"x","token":"` + other +
			`"}],"next_cursor":"c"}`,
			want: `{"events":[{"id":"e1","token":"[redacted]","type":"account_link","user":{"id":"u1"}},` +
				`{"id":"e2","type":"press","data":"x","token":"` + other + `"}],"next_cursor":"c"}`},
		{name: "an escaped account_link token, repeated in text", in: `{"type":"account_link","token":"` +
			escaped(tok) + `","note":"got ` + tok + `"}`, gone: []string{tok, escaped(tok)},
			contains: []string{`"token":"[redacted]"`, `"note":"got [redacted]"`}},
		{name: "a short account_link token", in: `{"type":"account_link","token":"ab","x":"abc"}`,
			want: `{"type":"account_link","token":"[redacted]","x":"abc"}`},
		{name: "account_link that is no JSON", in: `account_link token ` + tok, want: `account_link token ` + tok},
		{name: "nothing to mask", in: `{"message":"ok","n":1e400}`, want: `{"message":"ok","n":1e400}`},
	}
	for _, tt := range tests {
		got := maskShape(tt.in)
		if tt.want != "" && got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
		for _, g := range tt.gone {
			if strings.Contains(got, g) {
				t.Errorf("%s: %q is left in %s", tt.name, g, got)
			}
		}
		for _, c := range tt.contains {
			if !strings.Contains(got, c) {
				t.Errorf("%s: %s lacks %s", tt.name, got, c)
			}
		}
		if json.Valid([]byte(tt.in)) && !json.Valid([]byte(got)) {
			t.Errorf("%s: the masked JSON is no longer JSON: %s", tt.name, got)
		}
	}
}

// TestTextOf covers the bodies of C-23.FR-2 and C-23.AC-4: a 40 KB body is kept as its first 16 KB, cut at a
// character boundary, marked cut with its full size; a body of exactly the limit is not cut; bytes that are not UTF-8
// and NUL are replaced; the masking comes before the cut, so a secret at the edge leaves nothing of itself.
func TestTextOf(t *testing.T) {
	big := []byte(strings.Repeat("é", 20480)) // 2 bytes each: 40,960 bytes
	text, full, cut := textOf(big)
	if len(text) > BodyMax || len(text) < BodyMax-1 || !utf8.ValidString(text) || full != 40960 || !cut ||
		!strings.HasPrefix(string(big), text) {
		t.Errorf("40 KB: %d bytes, valid %v, full %d, cut %v", len(text), utf8.ValidString(text), full, cut)
	}
	odd := append([]byte("a"), big...)
	if text, _, _ := textOf(odd); len(text) != BodyMax-1 || !utf8.ValidString(text) {
		t.Errorf("a cut inside a character: %d bytes", len(text))
	}
	exact := []byte(strings.Repeat("a", BodyMax))
	if text, full, cut := textOf(exact); text != string(exact) || full != BodyMax || cut {
		t.Errorf("exactly the limit: %d bytes, full %d, cut %v", len(text), full, cut)
	}
	if text, full, cut := textOf([]byte("ok\xff\xfe!\x00end")); text != "ok\uFFFD!\uFFFDend" || full != 9 ||
		cut {
		t.Errorf("not UTF-8: %q, %d, %v", text, full, cut)
	}
	if text, full, cut := textOf(nil); text != "" || full != 0 || cut {
		t.Errorf("no body: %q, %d, %v", text, full, cut)
	}
	tok := linkToken(3)
	edge := append(bytes.Repeat([]byte("x"), BodyMax-30), []byte("/start "+tok+" and more")...)
	text, full, cut = textOf(edge)
	if strings.Contains(text, tok[:10]) || !strings.HasSuffix(text, "/start [redacted] and more") || cut ||
		full != int64(len(edge)) {
		t.Errorf("a token at the edge: ...%q, full %d, cut %v", text[len(text)-40:], full, cut)
	}
	if got := sanitize("fine ✓"); got != "fine ✓" {
		t.Errorf("sanitize changed valid text: %q", got)
	}
}

// slackPayload is a block_actions payload as Slack sends it (F-093): the verification token, the response_url, the
// signed action id of the pressed button and those of the message's buttons and Snooze menu.
func slackPayload(t *testing.T, responseURL string, values ...string) string {
	t.Helper()
	elements := []any{}
	for _, v := range values {
		elements = append(elements, map[string]any{"type": "button", "action_id": "ack", "value": v})
	}
	elements = append(elements, map[string]any{"type": "static_select", "action_id": "snooze",
		"options": []any{map[string]any{"text": map[string]any{"type": "plain_text", "text": "4h"}, "value": values[0]}}})
	b, err := json.Marshal(map[string]any{"type": "block_actions", "token": "Vt0kenOfTheApp99",
		"api_app_id": "A0MUSTERDEVB", "user": map[string]any{"id": "U0FAKEALICE", "name": "alice"},
		"container": map[string]any{"type": "message_attachment", "message_ts": "1.000001", "channel_id": "C0FAKEALERTS"},
		"message": map[string]any{"attachments": []any{map[string]any{"blocks": []any{map[string]any{"type": "actions",
			"block_id": "muster_actions", "elements": elements}}}}},
		"response_url": responseURL,
		"actions": []any{map[string]any{"action_id": "snooze", "block_id": "muster_actions", "type": "static_select",
			"selected_option": map[string]any{"value": values[0]}}, map[string]any{"action_id": "ack", "value": values[0]}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMaskSlack covers C-24.FR-14 and C-23.FR-3 for Slack: a value of X-Slack-Signature wherever it stands; the
// response_url — on hooks.slack.com or on the fake server's address — plain, with JSON-escaped slashes and
// percent-encoded, and as the value of the member response_url in JSON and in the form a press arrives in, as it
// arrived and with Slack's escaped slashes; the payload's verification token; and the signed action ids of the form,
// whose `%22` the form joins to them, are [redacted], and the form stays a form with a JSON payload. What only
// resembles them is kept.
func TestMaskSlack(t *testing.T) {
	ack, resolve := signed(t, buttons.SubjectRoot, buttons.CommandAcknowledge), signed(t, buttons.SubjectRoot,
		buttons.CommandResolve)
	sig := strings.Repeat("0a1B", 16)
	hooks := "https://hooks.slack.com/actions/T0123ABCD/4567890123/aBcDeF0123456789xyz"
	fake := "http://127.0.0.1:18097/actions/T0MUSTERDEVB/7/0123456789abcdef01234567"
	slackEscaped := func(s string) string { return strings.ReplaceAll(s, "/", `\/`) }
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a signature in text", "got v0=" + sig + ", refused", "got [redacted], refused"},
		{"a signature percent-encoded", "sig=v0%3D" + sig + "&x=1", "sig=[redacted]&x=1"},
		{"63 hex digits", "v0=" + sig[:63], "v0=" + sig[:63]},
		{"a response_url in text", "posted to " + hooks + " at once", "posted to [redacted] at once"},
		{"a response_url in another case", "HTTPS://Hooks.Slack.com/actions/T0/1/abc", "[redacted]"},
		{"a response_url with escaped slashes", `see ` + slackEscaped(hooks) + ` now`, `see [redacted] now`},
		{"a response_url percent-encoded", "u=" + url.QueryEscape(hooks) + "&v=1", "u=[redacted]&v=1"},
		{"a response_url percent-encoded in lower case", "u=https%3a%2f%2fhooks.slack.com%2factions%2fT0%2f1%2fabc",
			"u=[redacted]"},
		{"a response_url escaped, then percent-encoded", url.QueryEscape(slackEscaped(hooks)), "[redacted]"},
		{"an incoming webhook", "https://hooks.slack.com/services/T0/B0/xyz", "[redacted]"},
		{"the fake's address", "posted to " + fake, "posted to http://127.0.0.1:18097[redacted]"},
		{"the fake's address percent-encoded", url.QueryEscape(fake), "http%3A%2F%2F127.0.0.1%3A18097[redacted]"},
		{"a response_url member in JSON", `{"response_url":"` + slackEscaped(fake) + `","n":1}`,
			`{"response_url":"[redacted]","n":1}`},
		{"a response_url member of another address", `{"response_url" : "ftp:\/\/x\"y", "n":1}`,
			`{"response_url" : "[redacted]", "n":1}`},
		{"a response_url member in a form", "payload=%7B%22response_url%22%3A%22ftp%3A%2F%2Fx%5C%22y%22%2C%22n%22%3A1%7D",
			"payload=%7B%22response_url%22%3A%22[redacted]%22%2C%22n%22%3A1%7D"},
		{"the token of a Slack payload", `{"token":"Vt0ken","api_app_id":"A0"}`, `{"token":"[redacted]","api_app_id":"A0"}`},
		{"a token of something else", `{"token":"keep","type":"other"}`, `{"token":"keep","type":"other"}`},
		{"the outbound URL of an answer", "http://127.0.0.1:18097[redacted]", "http://127.0.0.1:18097[redacted]"},
		{"Slack's Web API", "https://slack.com/api/chat.postEphemeral", "https://slack.com/api/chat.postEphemeral"},
		{"actions without an address", "/actions/x and hooks.slack.com", "/actions/x and hooks.slack.com"},
		{"a GitHub Actions link is kept", "see https://github.com/o/r/actions/runs/12345/job/678 for the run",
			"see https://github.com/o/r/actions/runs/12345/job/678 for the run"},
		{"a GitHub Actions log link is kept", "https://github.com/o/r/actions/runs/99/logs",
			"https://github.com/o/r/actions/runs/99/logs"},
		{"a GitHub Actions link percent-encoded is kept",
			url.QueryEscape("https://github.com/o/r/actions/runs/12345/job/678"),
			url.QueryEscape("https://github.com/o/r/actions/runs/12345/job/678")},
		{"a GitHub Actions link with escaped slashes is kept",
			`{"u":"https:\/\/github.com\/o\/r\/actions\/runs\/7\/job\/8"}`,
			`{"u":"https:\/\/github.com\/o\/r\/actions\/runs\/7\/job\/8"}`},
		{"actions in capitals are no response_url", "http://127.0.0.1:18097/ACTIONS/T0MUSTERDEVB/7/0123456789abcdef",
			"http://127.0.0.1:18097/ACTIONS/T0MUSTERDEVB/7/0123456789abcdef"},
		{"the fake's address of the app a", "http://127.0.0.1:18097/actions/T0MUSTERDEVA/3/0123456789abcdef01234567",
			"http://127.0.0.1:18097[redacted]"},
		{"an Enterprise Grid team", "http://127.0.0.1:18097/actions/E0ENTERPRISE/3/abc",
			"http://127.0.0.1:18097[redacted]"},
		{"a signed action id after a percent-encoded character", "%41" + ack + "%22", "%41[redacted]%22"},
		{"a run after a percent sign that is no escape", "%zz" + ack, "%zz" + ack},
	}
	for _, tt := range tests {
		if got := maskShape(tt.in); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}

	for _, address := range []string{hooks, fake} {
		payload := slackPayload(t, address, ack, resolve)
		for _, form := range []string{"payload=" + url.QueryEscape(payload),
			"payload=" + url.QueryEscape(slackEscaped(payload)), payload} {
			got := maskShape(form)
			for _, secret := range []string{ack, resolve, "Vt0kenOfTheApp99", address, url.QueryEscape(address),
				slackEscaped(address), url.QueryEscape(slackEscaped(address)), "/actions/", "actions%2F", `actions\/`} {
				if strings.Contains(got, secret) {
					t.Errorf("%q is left in %s", secret, got)
				}
			}
			p := got
			if rest, ok := strings.CutPrefix(got, "payload="); ok {
				v, err := url.ParseQuery(got)
				if err != nil {
					t.Fatalf("the masked form is no form: %v", err)
				}
				p = v.Get("payload")
				if rest == "" || p == "" {
					t.Fatalf("no payload in %s", got)
				}
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(p), &m); err != nil {
				t.Fatalf("the masked payload is no JSON: %v: %s", err, p)
			}
			if m["type"] != "block_actions" || m["token"] != redacted || m["response_url"] != redacted ||
				m["user"].(map[string]any)["id"] != "U0FAKEALICE" {
				t.Errorf("masked payload %v", m)
			}
			if n := strings.Count(p, redacted); n != 7 {
				t.Errorf("%d values masked in %s, want 7", n, p)
			}
		}
	}
}

// TestMaskSocketTicket covers C-24.FR-14 and C-23.FR-3 for the ticket of a Socket Mode address (F-100): in the answer
// of apps.connections.open as Slack and Go encode it, with escaped slashes, percent-encoded, in a form, in capitals and
// wherever the ticket stands in the query, its value is [redacted] up to the end of the value; a ticket= that is not
// in a ws or wss address is kept.
func TestMaskSocketTicket(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"Slack's answer", `{"ok":true,"url":"wss:\/\/wss-primary.slack.com\/link\/?ticket=1a2b3c4d-5e6f-7a8b-9c0d` +
			`-e1f2a3b4c5d6&app_id=A0MUSTERDEVB"}`,
			`{"ok":true,"url":"wss:\/\/wss-primary.slack.com\/link\/?ticket=[redacted]&app_id=A0MUSTERDEVB"}`},
		{"plain JSON", `"url":"wss://wss-primary.slack.com/link/?ticket=abc-123&app_id=A0X"`,
			`"url":"wss://wss-primary.slack.com/link/?ticket=[redacted]&app_id=A0X"`},
		{"Go's JSON, & escaped", `{"ok":true,"url":"ws://127.0.0.1:18097/link/?ticket=0a1b2c3d4e5f60718293a4b5c6d7e8f9` +
			`\u0026app_id=A0MUSTERDEVB"}`, `{"ok":true,"url":"ws://127.0.0.1:18097/link/?ticket=[redacted]` +
			`\u0026app_id=A0MUSTERDEVB"}`},
		{"percent-encoded", "u=wss%3A%2F%2Fwss-primary.slack.com%2Flink%2F%3Fticket%3Dabc%2B1%2F2%3D%26app_id%3DA0X&v=1",
			"u=wss%3A%2F%2Fwss-primary.slack.com%2Flink%2F%3Fticket%3D[redacted]%26app_id%3DA0X&v=1"},
		{"escaped, then percent-encoded", "wss%3A%5C%2F%5C%2Fh.slack.com%5C%2Flink%5C%2F%3Fticket%3Dabc%22",
			"wss%3A%5C%2F%5C%2Fh.slack.com%5C%2Flink%5C%2F%3Fticket%3D[redacted]%22"},
		{"the ticket after another parameter", "wss://h.slack.com/link/?app_id=A0X&ticket=abc",
			"wss://h.slack.com/link/?app_id=A0X&ticket=[redacted]"},
		{"after an escaped &", `"wss://h.slack.com/link/?app_id=A0X\u0026ticket=abc"`,
			`"wss://h.slack.com/link/?app_id=A0X\u0026ticket=[redacted]"`},
		{"after a percent-encoded &", "wss%3A%2F%2Fh%2Flink%2F%3Fapp_id%3DA%26ticket%3Dabc",
			"wss%3A%2F%2Fh%2Flink%2F%3Fapp_id%3DA%26ticket%3D[redacted]"},
		{"in capitals", "WSS://H.SLACK.COM/LINK/?TICKET=ABC&APP_ID=A", "WSS://H.SLACK.COM/LINK/?TICKET=[redacted]&APP_ID=A"},
		{"base64 characters", "wss://h/link/?ticket=ab+c/d=e#f", "wss://h/link/?ticket=[redacted]#f"},
		{"at the end of a text", "dial wss://h/link/?ticket=abc", "dial wss://h/link/?ticket=[redacted]"},
		{"in an error", `websocket "wss://h/link/?ticket=abc&app_id=A": 403`,
			`websocket "wss://h/link/?ticket=[redacted]&app_id=A": 403`},
		{"two addresses", "wss://a/?ticket=x and ws://b/?ticket=y", "wss://a/?ticket=[redacted] and ws://b/?ticket=[redacted]"},
		{"a label", "ticket=OPS-1 is open", "ticket=OPS-1 is open"},
		{"an https address", "https://example.org/?ticket=x", "https://example.org/?ticket=x"},
		{"another scheme ending in ws", "news://h/?ticket=x", "news://h/?ticket=x"},
		{"a parameter that only ends in ticket", "wss://h/?xticket=x", "wss://h/?xticket=x"},
		{"no ticket", "wss://h/link/?app_id=A", "wss://h/link/?app_id=A"},
		{"an address in another JSON string", `{"a":"wss://h/x","b":"https://y/?ticket=k"}`,
			`{"a":"wss://h/x","b":"https://y/?ticket=k"}`},
		{"an address in another JSON string, percent-encoded",
			"%7B%22a%22%3A%22wss%3A%2F%2Fh%2Fx%22%2C%22b%22%3A%22https%3A%2F%2Fy%2F%3Fticket%3Dk%22%7D",
			"%7B%22a%22%3A%22wss%3A%2F%2Fh%2Fx%22%2C%22b%22%3A%22https%3A%2F%2Fy%2F%3Fticket%3Dk%22%7D"},
		{"across a blank", "wss://h/x then ?ticket=k", "wss://h/x then ?ticket=k"},
	}
	for _, tt := range tests {
		if got := maskShape(tt.in); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}

	ticket := "0a1b2c3d4e5f60718293a4b5c6d7e8f9"
	answer, err := json.Marshal(map[string]any{"ok": true, "url": "wss://wss-primary.slack.com/link/?ticket=" + ticket +
		"&app_id=A0MUSTERDEVB"})
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{string(answer), strings.ReplaceAll(string(answer), "/", `\/`),
		"payload=" + url.QueryEscape(string(answer))} {
		got := maskShape(form)
		if strings.Contains(got, ticket) || !strings.Contains(got, "A0MUSTERDEVB") {
			t.Errorf("masked %s", got)
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(maskShape(string(answer))), &m); err != nil ||
		m["url"] != "wss://wss-primary.slack.com/link/?ticket=[redacted]&app_id=A0MUSTERDEVB" {
		t.Errorf("the masked answer is %v, %v", m, err)
	}
}
