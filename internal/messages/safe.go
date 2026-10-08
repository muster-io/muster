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
// live here and the adapters apply them to the neutral text of a Message (C-13.FR-8, C-14.FR-13).

// ValueCap is message.value_cap: 4 KB per label or annotation value.
const ValueCap = 4096

// zeroWidthSpace follows every `@` of alert data, so that no messenger reads a Mention in it.
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

// Escaper is the escaper of a markup; plain text is left as it is.
func Escaper(m Markup) func(string) string {
	switch m {
	case MarkupMarkdown:
		return EscapeMarkdown
	case MarkupHTML:
		return EscapeHTML
	case MarkupPlain:
	}
	return func(s string) string { return s }
}

// SafeData is the template data d as a template writing markup sees it: every label and annotation value — and the
// title, summary, Route and Owner of the Alert Group — cut to ValueCap, neutralized and escaped for markup, and every
// generator URL kept only when it is http(s).
func SafeData(d templates.Data, markup Markup) templates.Data {
	esc := Escaper(markup)
	safe := func(s string) string { return esc(Value(s)) }
	out := d
	out.GroupLabels = safeKV(d.GroupLabels, safe)
	out.CommonLabels = safeKV(d.CommonLabels, safe)
	out.CommonAnnotations = safeKV(d.CommonAnnotations, safe)
	out.Alerts = make(templates.Alerts, len(d.Alerts))
	for i, a := range d.Alerts {
		out.Alerts[i] = safeAlert(a, safe)
	}
	out.AlertGroup = safeGroup(d.AlertGroup, safe)
	return out
}

// SafeLine is the data of a line template, made safe as SafeData does.
func SafeLine(a templates.Alert, g templates.AlertGroup, markup Markup) templates.LineData {
	esc := Escaper(markup)
	safe := func(s string) string { return esc(Value(s)) }
	return templates.LineData{Alert: safeAlert(a, safe), AlertGroup: safeGroup(g, safe)}
}

func safeAlert(a templates.Alert, safe func(string) string) templates.Alert {
	a.Labels = safeKV(a.Labels, safe)
	a.Annotations = safeKV(a.Annotations, safe)
	a.GeneratorURL = SafeURL(a.GeneratorURL)
	a.Fingerprint, a.Status = templates.StripTokens(a.Fingerprint), templates.StripTokens(a.Status)
	return a
}

func safeGroup(g templates.AlertGroup, safe func(string) string) templates.AlertGroup {
	g.Title, g.Summary, g.Route, g.Owner = safe(g.Title), safe(g.Summary), safe(g.Route), safe(g.Owner)
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
