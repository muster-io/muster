// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package requestlog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
