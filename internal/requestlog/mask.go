// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package requestlog

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/buttons"
)

// redacted stands for what the masking removed, as the outbound package writes it.
const redacted = "[redacted]"

// startRe finds the token of a Telegram deep link, `/start <token>` or `/start@<bot> <token>`, the slash escaped as
// some JSON encoders write it, the space possibly an escaped one: the token is the group, and only a run of exactly 43
// base64url characters is one.
var startRe = regexp.MustCompile(`\\?/start(?:@[A-Za-z0-9_]{1,64})?(?:[ \t\r\n]|\\[tnr])+([A-Za-z0-9_-]{43,})`)

// tokenLen is the length of an Account link token: 32 random bytes in base64url.
const tokenLen = 43

// slash is a slash of a URL as it may stand: plain, escaped in JSON, percent-encoded, or both, as a form-encoded JSON
// payload of Slack carries it.
const slash = `(?:/|\\/|%2[Ff]|%5[Cc]%2[Ff])`

var (
	// slackSignatureRe is a value of X-Slack-Signature wherever it stands: `v0=` and the 64 hex digits of the HMAC.
	slackSignatureRe = regexp.MustCompile(`v0(?:=|%3[Dd])[0-9A-Fa-f]{64}`)
	// hooksRe is an address on hooks.slack.com — a press's response_url, a slash command's, an incoming webhook — in
	// any of the forms of slash.
	hooksRe = regexp.MustCompile(`(?i)https?(?::|%3A)` + slash + slash + `hooks\.slack\.com(?:` + slash +
		`[A-Za-z0-9_.~-]+)*`)
	// actionsRe is the path of a press's response_url on any host, /actions/<team>/<n>/<token>, as the fake Slack
	// server of `muster dev` serves it, in any of the forms of slash.
	actionsRe = regexp.MustCompile(`(?i)` + slash + `actions` + slash + `[A-Z0-9]+` + slash + `[0-9]+` + slash +
		`[A-Za-z0-9]+`)
	// responseURLRes and slackTokenRes find the value of the member response_url, whatever its address, and of the
	// member token of a Slack payload: in JSON, and in JSON that a form carries percent-encoded.
	responseURLRes = memberRes("response_url")
	slackTokenRes  = memberRes("token")
)

// memberRes are the expressions of the string value of the member name: in JSON, `"name":"…"`, and percent-encoded in
// a form, `%22name%22%3A%22…%22` with spaces as `+` or `%20`, a JSON escape such as `%5C%22` within it. The value lies
// between the two groups.
func memberRes(name string) []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`("` + name + `"\s*:\s*")(?:[^"\\]|\\.)*(")`),
		regexp.MustCompile(`(?i)(%22` + name + `%22(?:\+|%20)*%3A(?:\+|%20)*%22)` +
			`(?:[^%&]|%5C%[0-9A-F]{2}|%(?:[013-9A-F][0-9A-F]|2[013-9A-F]))*(%22)`),
	}
}

// maskShape replaces with [redacted], in s, what the shape of a value tells is a secret, whichever Secrets its request
// had (C-23.FR-3): every maximal run of base64url characters that buttons.LooksSigned recognizes as a signed action
// id, alone or right after a percent-encoded character; Slack's secrets (maskSlack); every token of a `/start` deep
// link; and, in JSON, every token member of an object whose type is account_link and every string that holds one of
// these behind escapes.
func maskShape(s string) string {
	s = maskActionIDs(s)
	s = maskSlack(s)
	if strings.Contains(s, "/start") {
		s = maskStart(s)
	}
	if strings.Contains(s, "account_link") || strings.Contains(s, `\u`) {
		s = maskJSON(s)
	}
	return s
}

// maskSlack masks what Slack's requests carry (C-24.FR-14): a value of X-Slack-Signature; the value of the member
// response_url, whatever its address, and, in a Slack payload — one that names block_actions or api_app_id — that of
// its legacy verification token, in JSON and in a form; and any address on hooks.slack.com and any path of a
// response_url, plain, with JSON-escaped slashes or percent-encoded.
func maskSlack(s string) string {
	if strings.Contains(s, "v0") {
		s = slackSignatureRe.ReplaceAllLiteralString(s, redacted)
	}
	if containsFold(s, "response_url") {
		s = maskMembers(s, responseURLRes)
	}
	if strings.Contains(s, "token") && (strings.Contains(s, "block_actions") || strings.Contains(s, "api_app_id")) {
		s = maskMembers(s, slackTokenRes)
	}
	if containsFold(s, "hooks.slack.com") {
		s = hooksRe.ReplaceAllLiteralString(s, redacted)
	}
	if containsFold(s, "actions") {
		s = actionsRe.ReplaceAllLiteralString(s, redacted)
	}
	return s
}

// maskMembers replaces the value of each match of res in s with [redacted].
func maskMembers(s string, res []*regexp.Regexp) string {
	for _, re := range res {
		s = re.ReplaceAllString(s, "${1}"+redacted+"${2}")
	}
	return s
}

// containsFold reports whether s holds sub, an ASCII text, in any case.
func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i]|0x20 == sub[0]|0x20 && strings.EqualFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func base64URL(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func hexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'F' || c >= 'a' && c <= 'f'
}

// maskActionIDs masks each maximal run of base64url characters that is a signed action id, and the rest of a run after
// a percent-encoded character when that is one: a form joins the two hex digits of `%22` to the id after them.
func maskActionIDs(s string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		if !base64URL(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && base64URL(s[j]) {
			j++
		}
		from := -1
		switch {
		case buttons.LooksSigned(s[i:j]):
			from = i
		case i > 0 && s[i-1] == '%' && j-i > 2 && hexDigit(s[i]) && hexDigit(s[i+1]) && buttons.LooksSigned(s[i+2:j]):
			from = i + 2
		}
		if from >= 0 {
			b.WriteString(s[last:from])
			b.WriteString(redacted)
			last = j
		}
		i = j
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func maskStart(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range startRe.FindAllStringSubmatchIndex(s, -1) {
		if m[3]-m[2] != tokenLen {
			continue
		}
		b.WriteString(s[last:m[2]])
		b.WriteString(redacted)
		last = m[3]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// maskJSON masks the JSON s string by string, as decoded, so that no escape hides a secret: a string that is the
// token of an account_link event becomes [redacted], and a string that holds a signed action id or a `/start` token is
// written again with it masked; a token of an account_link event is also replaced wherever else it stands, when it
// has the length of a token. s that is no JSON is returned as it is.
func maskJSON(s string) string {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return s
	}
	tokens := linkTokens(v, nil)
	d = json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var b strings.Builder
	var last, prev int64
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		end := d.InputOffset()
		if str, ok := tok.(string); ok {
			masked := redacted
			if !slices.Contains(tokens, str) {
				masked = maskActionIDs(str)
				if strings.Contains(masked, "/start") {
					masked = maskStart(masked)
				}
			}
			if masked != str {
				start := prev + int64(strings.IndexByte(s[prev:end], '"'))
				b.WriteString(s[last:start])
				b.WriteString(quote(masked))
				last = end
			}
		}
		prev = end
	}
	b.WriteString(s[last:])
	s = b.String()
	for _, t := range tokens {
		if len(t) >= tokenLen {
			s = strings.ReplaceAll(s, t, redacted)
		}
	}
	return s
}

func linkTokens(v any, out []string) []string {
	switch x := v.(type) {
	case map[string]any:
		if t, ok := x["token"].(string); ok && x["type"] == "account_link" && t != "" {
			out = append(out, t)
		}
		for _, e := range x {
			out = linkTokens(e, out)
		}
	case []any:
		for _, e := range x {
			out = linkTokens(e, out)
		}
	}
	return out
}

// quote is s as a JSON string, without the escapes of HTML.
func quote(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// sanitize replaces the bytes of s that are not UTF-8, and NUL, which PostgreSQL stores in neither text nor jsonb,
// with U+FFFD.
func sanitize(s string) string {
	if utf8.ValidString(s) && !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, string(utf8.RuneError)), "\x00", string(utf8.RuneError))
}

// clean is s as a record stores a URL, a header or an error: shape-masked and sanitized.
func clean(s string) string { return sanitize(maskShape(s)) }

// textOf is body as a record stores it (C-23.FR-2, FR-3): shape-masked, sanitized and cut to at most BodyMax bytes at
// a character boundary, the masking first so that no part of a secret survives the cut; full is the size of body and
// cut whether the text was cut.
func textOf(body []byte) (text string, full int64, cut bool) {
	text = clean(string(body))
	full = int64(len(body))
	if len(text) <= BodyMax {
		return text, full, false
	}
	i := BodyMax
	for i > 0 && !utf8.RuneStart(text[i]) {
		i--
	}
	return text[:i], full, true
}
