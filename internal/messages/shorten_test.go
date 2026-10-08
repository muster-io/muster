// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"strings"
	"testing"
)

// mustKeep are the parts shortening never removes: the title and status, the footer, the buttons and the link to
// Muster.
func mustKeep(t *testing.T, out string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(out, p) {
			t.Errorf("lost %q:\n%s", p, out)
		}
	}
}

// TestShortenManyAlerts is C-12.AC-4 and C-12.FR-11: 25 Alerts with long common annotations, laid out within 4,096
// characters, lose the annotations first and keep the distinct values, the title, the status, the footer, the
// buttons and the link to Muster; ten Alert lines give way to the distinct values first, then the annotations.
func TestShortenManyAlerts(t *testing.T) {
	r := newRenderer(t)
	src := source("acknowledged", 25)
	src.Owner = "Alice"
	src.CommonAnnotations["details"] = strings.Repeat("d", 4000)
	m := r.Root(src, MarkupMarkdown).Message
	if Length(m, MarkupMarkdown) <= 4096 {
		t.Fatalf("the message is only %d characters", Length(m, MarkupMarkdown))
	}
	short, truncated := Shorten(m, 4096, MarkupMarkdown)
	out := Layout(short, MarkupMarkdown)
	if !truncated || Length(short, MarkupMarkdown) > 4096 || strings.Contains(out, "details:") {
		t.Errorf("shortened %v to %d:\n%s", truncated, Length(short, MarkupMarkdown), out)
	}
	mustKeep(t, out, "🟠 [#12 PodDown]", "pod: p1, p2, p3, p4, p5, p6, p7, p8, p9, p10 +15 more", "[Full list in Muster]",
		"[Open in Muster]", "Acknowledged by Alice", "[Unack] [Resolve] [Snooze 1 h]", "owner: @\u200bchannel")
	if Layout(m, MarkupMarkdown) == out || len(m.CommonAnnotations) == 0 {
		t.Error("Shorten changed its argument")
	}
	if same, changed := Shorten(m, 0, MarkupMarkdown); changed || Length(same, MarkupMarkdown) != Length(m,
		MarkupMarkdown) {
		t.Error("no limit shortened")
	}
	// Alert lines go first, down to the distinct values.
	ten := source("firing", 10)
	ten.CommonAnnotations["details"] = strings.Repeat("d", 300)
	m = r.Root(ten, MarkupHTML).Message
	limit := Length(m, MarkupHTML) - 1
	short, _ = Shorten(m, limit, MarkupHTML)
	out = Layout(short, MarkupHTML)
	// The link to the full list costs more than ten short lines, so the annotations go too; the labels stay.
	if len(short.Alerts.Lines) != 0 || !strings.Contains(out, "pod: p1, p2, p3") || !strings.Contains(out,
		"Full list in Muster") || strings.Contains(out, "details:") || !strings.Contains(out, "namespace: shop") {
		t.Errorf("lines first:\n%s", out)
	}
	// Then the label sections, the distinct values, the environment and the summary.
	short, _ = Shorten(m, 400, MarkupHTML)
	out = Layout(short, MarkupHTML)
	if strings.Contains(out, "namespace:") || strings.Contains(out, "owner:") || strings.Contains(out, "pod: p1") {
		t.Errorf("labels:\n%s", out)
	}
	mustKeep(t, out, "#12 PodDown", "Open in Muster", "[Ack] [Resolve]")
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
