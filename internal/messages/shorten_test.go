// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"strings"
	"testing"
)

// mustKeep are the parts shortening never removes: the title and status, the fields, the footer, the buttons and the
// link to Muster.
func mustKeep(t *testing.T, out string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(out, p) {
			t.Errorf("lost %q:\n%s", p, out)
		}
	}
}

// TestShortenOrder is C-29.FR-6, C-29.AC-3 and C-12.FR-11: a Root message of twelve Alerts with a long description
// and summary, laid out within 4,096 characters, loses its description first; tighter limits take the Alerts line, then
// the summary, then the key labels, and always keep the title, the status, the fields, the footer, the buttons and the
// link to Muster.
func TestShortenOrder(t *testing.T) {
	r := newRenderer(t)
	src := source("acknowledged", 12)
	src.Owner = "Alice"
	src.Summary, src.Description = strings.Repeat("s", 1500), strings.Repeat("d", 3000)
	m := r.Root(src, MarkupMarkdown).Message
	if Length(m, MarkupMarkdown) <= 4096 {
		t.Fatalf("the message is only %d characters", Length(m, MarkupMarkdown))
	}
	kept := []string{"[#12 PodDown](https://muster.example.org/alert-groups/AGK7M3QX9P2RTA) — 🟠 Acknowledged",
		"\nSeverity: critical\nRoute: db\nTeams: payments\nStarted: 2026-10-04 14:05 UTC\n", "\nAcknowledged by Alice\n",
		"[Open in Muster](https://muster.example.org/alert-groups/AGK7M3QX9P2RTA)", "[Unack] [Resolve] [Snooze 1 h]"}
	short, truncated := Shorten(m, 4096, MarkupMarkdown)
	out := Layout(short, MarkupMarkdown)
	if !truncated || Length(short, MarkupMarkdown) > 4096 || strings.Contains(out, "ddd") ||
		!strings.Contains(out, "\n"+strings.Repeat("s", 1500)+"\n") ||
		!strings.Contains(out, "\nnamespace=shop · cluster=prod\npod: p1, p3, p4, p5, p6 …and 7 more\n") {
		t.Errorf("4096: shortened %v to %d:\n%s", truncated, Length(short, MarkupMarkdown), out)
	}
	mustKeep(t, out, kept...)
	if Layout(m, MarkupMarkdown) == out || m.Description == "" {
		t.Error("Shorten changed its argument")
	}
	if same, changed := Shorten(m, 0, MarkupMarkdown); changed || Length(same, MarkupMarkdown) != Length(m,
		MarkupMarkdown) {
		t.Error("no limit shortened")
	}
	// The Alerts go next, then the summary, then the key labels; each step goes only as far as it must.
	without := func(m Message, f func(*Message)) Message {
		f(&m)
		return m
	}
	step := without(m, func(m *Message) { m.Description = "" })
	short, _ = Shorten(m, Length(step, MarkupMarkdown)-1, MarkupMarkdown)
	if out := Layout(short, MarkupMarkdown); strings.Contains(out, "pod: p1") || !strings.Contains(out, "sss") ||
		!strings.Contains(out, "namespace=shop") {
		t.Errorf("the alerts:\n%s", out)
	}
	step = without(step, func(m *Message) { m.AlertsLine = nil })
	short, _ = Shorten(m, Length(step, MarkupMarkdown)-1, MarkupMarkdown)
	if out := Layout(short, MarkupMarkdown); strings.Contains(out, "sss") || !strings.Contains(out, "namespace=shop") {
		t.Errorf("the summary:\n%s", out)
	}
	step = without(step, func(m *Message) { m.Summary = "" })
	short, _ = Shorten(m, Length(step, MarkupMarkdown)-1, MarkupMarkdown)
	out = Layout(short, MarkupMarkdown)
	if strings.Contains(out, "namespace=") || short.KeyLabels != nil {
		t.Errorf("the key labels:\n%s", out)
	}
	mustKeep(t, out, kept...)
	// The Fallback template keeps every label: its Alert lines go first, down to the distinct values and the full list.
	ten := source("firing", 10)
	ten.Route.RootMessage = new("{{ fail }}")
	fb := r.Root(ten, MarkupHTML).Message
	short, _ = Shorten(fb, Length(fb, MarkupHTML)-1, MarkupHTML)
	if out := Layout(short, MarkupHTML); len(short.Alerts.Lines) != 0 || !strings.Contains(out, "pod: p1, p2, p3") ||
		!strings.Contains(out, "Full list in Muster") || !strings.Contains(out, "namespace: shop") {
		t.Errorf("fallback lines first:\n%s", out)
	}
}

// TestShortenLastResorts: text lines, a template body, notices, the title and the footer are cut when nothing else is
// left.
func TestShortenLastResorts(t *testing.T) {
	m := Message{Heading: &Heading{Number: 1, Title: strings.Repeat("T", 300), URL: "https://m/ag"},
		Body: &Body{Text: strings.Repeat("b", 500), Markup: MarkupMarkdown}, Lines: []string{"one", "two"},
		Notices: []string{"n1", "n2"}, Footer: strings.Repeat("f", 300), Links: []Link{{Text: "Open in Muster",
			URL: "https://m/ag"}}, Buttons: []Button{{Label: "Ack"}}}
	for _, limit := range []int{900, 700, 400, 120} {
		short, changed := Shorten(m, limit, MarkupMarkdown)
		if !changed || Length(short, MarkupMarkdown) > limit {
			t.Errorf("limit %d: %d characters\n%s", limit, Length(short, MarkupMarkdown), Layout(short,
				MarkupMarkdown))
		}
		mustKeep(t, Layout(short, MarkupMarkdown), "Open in Muster", "[Ack]")
	}
	// A template body loses whole lines, never a cut tag.
	m = Message{Body: &Body{Text: "<b>one</b>\n<b>two</b>\n<b>three</b>\n", Markup: MarkupHTML},
		Buttons: []Button{{Label: "Ack"}}}
	short, _ := Shorten(m, 30, MarkupHTML)
	if short.Body == nil || short.Body.Text != "<b>one</b>\n<b>two</b>" {
		t.Errorf("body %+v", short.Body)
	}
	if short, _ := Shorten(m, 6, MarkupHTML); short.Body != nil || short.Footer != "" {
		t.Errorf("body dropped %+v", short)
	}
	if cutEnd("abc", 10) != "…" || cutEnd("abcdef", 2) != "abc…" {
		t.Errorf("cutEnd %q %q", cutEnd("abc", 10), cutEnd("abcdef", 2))
	}
}
