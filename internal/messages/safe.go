// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"html"
	"strings"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/templates"
)

// Safe output (C-12.FR-7, ADR-0012): alert data never mention anyone, never carry a link other than http(s), never
// exceed message.value_cap per value, and reach a template already escaped for the markup it writes. The escapers
// live here and the adapters apply them to the neutral text of a Message (C-13.FR-8, C-14.FR-13, C-24.FR-7).

// ValueCap is message.value_cap: 4 KB per label or annotation value.
const ValueCap = 4096

// zeroWidthSpace follows every `@` of alert data, so that no messenger reads a Mention in it, and in mrkdwn every
// character of emphasis, strikethrough and code.
const zeroWidthSpace = "\u200b"

// ellipsis ends a value cut at ValueCap.
const ellipsis = "…"

// Neutralize puts a zero-width space after every `@` of s that has none yet, so that neutralizing twice changes
// nothing.
func Neutralize(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := range len(s) {
		b.WriteByte(s[i])
		if s[i] == '@' && !strings.HasPrefix(s[i+1:], zeroWidthSpace) {
			b.WriteString(zeroWidthSpace)
		}
	}
	return b.String()
}

// Cap cuts s to at most ValueCap bytes at a character boundary, ending it with "…".
func Cap(s string) string {
	if len(s) <= ValueCap {
		return s
	}
	cut := ValueCap - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// Value is an alert value made safe and neutral: without the characters of Mention tokens, cut to ValueCap, with its
// `@` neutralized.
func Value(s string) string {
	return Neutralize(Cap(templates.StripTokens(s)))
}

// SafeURL is u when it is an absolute http or https link, and empty otherwise.
func SafeURL(u string) string {
	return templates.SafeURL(u)
}

// markdownSpecial are the characters Mattermost Markdown gives a meaning to inside a line.
var markdownSpecial = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "~", `\~`, "[", `\[`,
	"]", `\]`, "<", `\<`, ">", `\>`, "|", `\|`)

// EscapeMarkdown escapes s for Mattermost Markdown.
func EscapeMarkdown(s string) string {
	return markdownSpecial.Replace(s)
}

// EscapeHTML escapes s for Telegram HTML.
func EscapeHTML(s string) string {
	return html.EscapeString(s)
}

// commonMarkSpecial are the characters CommonMark with strikethrough gives a meaning to inside a line: escapes, code
// spans, emphasis, strikethrough, links and images, autolinks and raw HTML, and entities.
var commonMarkSpecial = strings.NewReplacer(`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "~", `\~`, "[", `\[`,
	"]", `\]`, "<", `\<`, ">", `\>`, "&", `\&`)

// EscapeCommonMark escapes s for CommonMark with strikethrough, a bridge's envelope body (C-22.FR-4): every
// punctuation that can start markup inside a line, and the marker a line starts with after any spaces and tabs — of a
// heading, a list item or a setext underline — so that alert data stay literal text. Any indentation, not only up to
// three spaces: a line of alert data laid out inside a list item is indented relative to it. It drops the lines of
// only spaces and tabs, the line ending after the last line and the spaces and tabs s starts with: without a blank
// line every later line continues the paragraph s starts, which an indented code block cannot interrupt, inside its
// list item or block quote. It does not neutralize `@`: Value does that first.
func EscapeCommonMark(s string) string {
	s = commonMarkSpecial.Replace(s)
	var b strings.Builder
	b.Grow(len(s) + 8)
	ending := ""
	for s != "" {
		line, end := s, ""
		if i := strings.IndexAny(s, "\r\n"); i >= 0 {
			line, end = s[:i], s[i:i+1]
			if strings.HasPrefix(s[i:], "\r\n") {
				end = "\r\n"
			}
		}
		s = s[len(line)+len(end):]
		if strings.Trim(line, " \t") == "" {
			continue
		}
		if b.Len() == 0 {
			line = strings.TrimLeft(line, " \t")
		}
		b.WriteString(ending)
		b.WriteString(escapeLineStart(line))
		ending = end
	}
	return b.String()
}

// escapeLineStart escapes the block marker that starts a line after any spaces and tabs: `#`, `-`, `+` or `=`, and
// the `.` or `)` after one to nine digits.
func escapeLineStart(line string) string {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i < len(line) && strings.IndexByte("#-+=", line[i]) >= 0 {
		return line[:i] + `\` + line[i:]
	}
	j := i
	for j < len(line) && j-i < 10 && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	if digits := j - i; digits >= 1 && digits <= 9 && j < len(line) && (line[j] == '.' || line[j] == ')') {
		return line[:j] + `\` + line[j:]
	}
	return line
}

// EscapeMrkdwn escapes s for Slack's mrkdwn (C-24.FR-7): `&`, `<` and `>` become `&amp;`, `&lt;` and `&gt;`, so that
// nothing in s becomes a link, a Mention, <!channel> or <!here> (F-097); and since mrkdwn has no escape for `*`, `_`,
// `~` and the backquote, each of them — and `@`, as Neutralize does — is followed by a zero-width space unless one
// follows it already, so that none pairs into emphasis, strikethrough or code. It is applied once, to neutral text:
// an entity in s is escaped again and shows as written.
func EscapeMrkdwn(s string) string {
	if !strings.ContainsAny(s, "&<>@*_~`") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := range len(s) {
		switch c := s[i]; c {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '@', '*', '_', '~', '`':
			b.WriteByte(c)
			if !strings.HasPrefix(s[i+1:], zeroWidthSpace) {
				b.WriteString(zeroWidthSpace)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Escaper is the escaper of a markup; plain text is left as it is.
func Escaper(m Markup) func(string) string {
	switch m {
	case MarkupMarkdown:
		return EscapeMarkdown
	case MarkupHTML:
		return EscapeHTML
	case MarkupCommonMark:
		return EscapeCommonMark
	case MarkupMrkdwn:
		return EscapeMrkdwn
	case MarkupPlain:
	}
	return func(s string) string { return s }
}

// urlEscaper is the escaper of the URLs of template data in a markup. In mrkdwn a template writes a URL into a link,
// `<url|text>`, so `<`, `>` and `|` are percent-encoded and `&` becomes `&amp;`, as in the links Muster lays out: a
// URL can then neither end the link nor open another one (C-24.FR-7). Other markups keep the URL as it is.
func urlEscaper(m Markup) func(string) string {
	if m == MarkupMrkdwn {
		return mrkdwnURL.Replace
	}
	return func(s string) string { return s }
}

// SafeData is the template data d as a template writing markup sees it: every label and annotation value — and the
// title, summary, Route and Owner of the Alert Group — cut to ValueCap, neutralized and escaped for markup, every
// generator URL kept only when it is http(s), and in mrkdwn every URL — the generator URLs, ExternalURL and the
// Alert Group's — escaped for a link.
func SafeData(d templates.Data, markup Markup) templates.Data {
	esc, escURL := Escaper(markup), urlEscaper(markup)
	safe := func(s string) string { return esc(Value(s)) }
	out := d
	out.GroupLabels = safeKV(d.GroupLabels, safe)
	out.CommonLabels = safeKV(d.CommonLabels, safe)
	out.CommonAnnotations = safeKV(d.CommonAnnotations, safe)
	out.ExternalURL = escURL(d.ExternalURL)
	out.Alerts = make(templates.Alerts, len(d.Alerts))
	for i, a := range d.Alerts {
		out.Alerts[i] = safeAlert(a, safe, escURL)
	}
	out.AlertGroup = safeGroup(d.AlertGroup, safe, escURL)
	return out
}

// SafeLine is the data of a line template, made safe as SafeData does.
func SafeLine(a templates.Alert, g templates.AlertGroup, markup Markup) templates.LineData {
	esc, escURL := Escaper(markup), urlEscaper(markup)
	safe := func(s string) string { return esc(Value(s)) }
	return templates.LineData{Alert: safeAlert(a, safe, escURL), AlertGroup: safeGroup(g, safe, escURL)}
}

func safeAlert(a templates.Alert, safe, escURL func(string) string) templates.Alert {
	a.Labels = safeKV(a.Labels, safe)
	a.Annotations = safeKV(a.Annotations, safe)
	a.GeneratorURL = escURL(SafeURL(a.GeneratorURL))
	a.Fingerprint, a.Status = templates.StripTokens(a.Fingerprint), templates.StripTokens(a.Status)
	return a
}

func safeGroup(g templates.AlertGroup, safe, escURL func(string) string) templates.AlertGroup {
	g.Title, g.Summary, g.Route, g.Owner = safe(g.Title), safe(g.Summary), safe(g.Route), safe(g.Owner)
	g.URL = escURL(g.URL)
	return g
}

// safeKV makes the values safe; a name keeps its spelling, so that a template reads `.Labels.pod`, without the
// characters of Mention tokens.
func safeKV(kv templates.KV, safe func(string) string) templates.KV {
	out := make(templates.KV, len(kv))
	for k, v := range kv {
		out[templates.StripTokens(k)] = safe(v)
	}
	return out
}
