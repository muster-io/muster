// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"fmt"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

// TestTemplateOutput: a Route's template body and line templates are written in HTML as rendered, their trusted
// Mention tokens as text that notifies nobody; above the listed Alerts the distinct values show with the link to the
// full list.
func TestTemplateOutput(t *testing.T) {
	owner := templates.MentionToken(templates.Token{Name: templates.MentionOwner})
	group := templates.MentionToken(templates.Token{Name: templates.MentionGroup, Group: "db<a>"})
	m := messages.Message{Language: "en", Colour: messages.ColourAcknowledged,
		Heading: &messages.Heading{Number: 3, Title: "Disk " + owner},
		Body:    &messages.Body{Text: "<b>Disk</b> on " + owner + " " + group + "\n", Markup: messages.MarkupHTML},
		Alerts: &messages.AlertList{Lines: []messages.AlertLine{{Text: "<i>" + owner + "</i>",
			Markup: messages.MarkupHTML}}},
		Buttons: []messages.Button{}}
	got := writer{}.text(m, "")
	want := "🟠 <b>#3 Disk owner</b>\n<b>Disk</b> on @\u200bowner @\u200bdb&lt;a&gt;\n• <i>@\u200bowner</i>"
	if got != want {
		t.Fatalf("text = %q", got)
	}
	m = messages.Message{Language: "ru", Colour: messages.ColourFiring, Alerts: &messages.AlertList{
		Distinct: []messages.Distinct{{Label: "pod", Values: []string{"a", "b<"}, More: 3}},
		FullList: &messages.Link{Text: "All alerts", URL: "http://x/?a=1&b=2"}}}
	got = writer{}.text(m, "")
	if !strings.HasPrefix(got, "pod: a, b&lt;") || !strings.HasSuffix(got, `<a href="http://x/?a=1&amp;b=2">All alerts</a>`) {
		t.Fatalf("distinct = %q", got)
	}
	if got := (writer{plain: true}).text(m, ""); !strings.HasSuffix(got, "All alerts http://x/?a=1&b=2") {
		t.Fatalf("plain = %q", got)
	}
	m = messages.Message{Links: []messages.Link{{Text: "Runbook <1>"}}}
	if got := (writer{}).text(m, ""); got != "Runbook &lt;1&gt;" {
		t.Fatalf("link without a URL = %q", got)
	}
}

// TestFitKeepsWhatCannotGo: a message whose links alone exceed the limit loses its tags and is cut, so that Telegram
// still takes it.
func TestFitKeepsWhatCannotGo(t *testing.T) {
	m := messages.Message{Language: "en", Colour: messages.ColourFiring,
		Heading: &messages.Heading{Number: 1, Title: "x", URL: groupPage}, Footer: "footer"}
	for i := range 400 {
		m.Links = append(m.Links, messages.Link{Text: fmt.Sprint("link ", i), URL: "http://example.org/🔥"})
	}
	got := fit(m, writer{}, "", LengthLimit)
	if length(got) > LengthLimit || strings.Contains(got, "<a ") || !strings.HasSuffix(got, "…") ||
		!strings.HasPrefix(got, "🔴 #1 ") {
		t.Fatalf("fit = %d %q", length(got), got[:80])
	}
	if got := fit(m, writer{plain: true}, "", 100); length(got) > 100 || !strings.HasSuffix(got, "…") {
		t.Fatalf("plain fit = %q", got)
	}
	if got := cutEnd("short", 10, false); got != "short" {
		t.Fatal(got)
	}
	if got := cutEnd("🔥🔥🔥", 4, true); got != "🔥…" {
		t.Fatalf("cut between surrogates = %q", got)
	}
	if length("a🔥") != 3 {
		t.Fatal("length counts UTF-16 code units")
	}
}

// TestKeyboard: buttons without a usable action id are left out; a message without buttons has an empty keyboard.
func TestKeyboard(t *testing.T) {
	k := keyboard([]messages.Button{
		{Command: buttons.CommandAcknowledge, Label: "Ack", ActionID: "a"},
		{Command: buttons.CommandResolve, Label: "Resolve"},
		{Command: buttons.CommandSnooze, Label: "Snooze", ActionID: strings.Repeat("x", buttons.MaxLen+1)},
		{Command: buttons.CommandSnooze, Label: "\xff", ActionID: "s"},
	})
	if len(k.InlineKeyboard) != 1 || len(k.InlineKeyboard[0]) != 1 || k.InlineKeyboard[0][0].CallbackData != "a" {
		t.Fatalf("keyboard = %+v", k)
	}
	if k := keyboard(nil); k == nil || k.InlineKeyboard == nil || len(k.InlineKeyboard) != 0 {
		t.Fatalf("empty = %+v", k)
	}
}

func TestPermalink(t *testing.T) {
	for chat, want := range map[Chat]string{
		{ID: -1001000000001, Username: "muster_alerts"}: "https://t.me/muster_alerts/5",
		{ID: -1001000000002}:                            "https://t.me/c/1000000002/5",
		{ID: 42}:                                        "",
	} {
		if got := permalink(chat, 5); got != want {
			t.Errorf("%+v = %q", chat, got)
		}
	}
}

// TestPlainTemplateOutput: after the messenger rejected the markup, template output written in HTML is sent as its
// text, without tags and with its entities decoded; neutral text and Mention tokens are not escaped.
func TestPlainTemplateOutput(t *testing.T) {
	group := templates.MentionToken(templates.Token{Name: templates.MentionGroup, Group: "db-oncall"})
	m := messages.Message{Language: "en", Colour: messages.ColourFiring,
		Body:    &messages.Body{Text: "<b>Disk</b> &lt;full&gt; on <p>" + group + "</p>", Markup: messages.MarkupHTML},
		Alerts:  &messages.AlertList{Lines: []messages.AlertLine{{Text: "<i>x &amp; y</i>", Markup: messages.MarkupHTML}}},
		Summary: "a < b", Buttons: []messages.Button{}}
	want := "a < b\nDisk <full> on @\u200bdb-oncall\n• x & y"
	if got := (writer{plain: true}).text(m, ""); got != want {
		t.Fatalf("plain = %q", got)
	}
}

// TestCutEndKeepsEntities: a cut HTML text never ends inside an entity, and a cut message keeps its Mentions.
func TestCutEndKeepsEntities(t *testing.T) {
	if got := cutEnd("<b>a &amp;amp; b</b>", 6, false); got != "a …" {
		t.Fatalf("cut = %q", got)
	}
	if got := cutEnd("a &amp; b c d", 10, false); got != "a &amp; b…" {
		t.Fatalf("cut after an entity = %q", got)
	}
	m := messages.Message{Language: "en", Colour: messages.ColourFiring,
		Heading: &messages.Heading{Number: 1, Title: "x", URL: groupPage}}
	for range 400 {
		m.Links = append(m.Links, messages.Link{Text: "link", URL: "http://example.org/"})
	}
	mention := `<a href="tg://user?id=42">Ann</a>`
	if got := fit(m, writer{}, mention, LengthLimit); !strings.Contains(got, "Ann") || length(got) > LengthLimit {
		t.Fatalf("fit lost the mentions: %q", got[:60])
	}
}
