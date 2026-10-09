// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

// The layout of messages (C-12.FR-1, C-14.FR-13, FR-16): a Root message is a regular message in Telegram HTML — the
// status emoji and `<b><a href="{Alert Group page}">#N title</a></b>`, on a Loud message its Mentions, the environment
// and start, the group labels, common labels and common annotations inside one `<blockquote expandable>`, the summary
// in `<i>`, the Alerts as one line each (resolved ones struck through), never a table (F-020), the line of links that
// starts with "Open in Muster", the notices and the footer — with an inline keyboard of its buttons. Alert data are
// escaped for HTML and their `@` neutralized; the trusted Mention tokens of template output stay text that notifies
// nobody. A Thread reply carries its Mentions and its text. Rich Messages are not used (F-019). After the messenger
// rejected the markup, the same sections are sent as plain text.

// LengthLimit is the longest text of a message (C-14.FR-13, C-12.FR-11). Telegram counts UTF-16 code units after it
// parsed the entities; Muster counts the UTF-16 code units of the HTML it sends, which are never fewer.
const LengthLimit = 4096

// space is the identity space of Telegram, whose usernames the footer names users by (C-12.FR-12).
const space = "telegram"

// zeroWidthSpace follows the `@` of a Mention that must not notify.
const zeroWidthSpace = "\u200b"

// statusEmoji starts the heading, by the colour of the message (C-12.FR-1 item 1).
var statusEmoji = map[string]string{
	messages.ColourFiring: "🔴", messages.ColourAcknowledged: "🟠", messages.ColourResolved: "🟢",
	messages.ColourSnoozed: "⚪", messages.ColourStorm: "⛈",
}

// userID is a Telegram user id, which a `tg://user` link names.
var userID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// writer writes the sections of a message in Telegram HTML, or as plain text.
type writer struct {
	plain bool
}

func (w writer) esc(s string) string {
	if w.plain {
		return s
	}
	return messages.EscapeHTML(s)
}

func (w writer) tag(name, s string) string {
	if w.plain || s == "" {
		return s
	}
	return "<" + name + ">" + s + "</" + name + ">"
}

func (w writer) link(text, url string) string {
	switch {
	case url == "":
		return w.esc(text)
	case w.plain:
		return text + " " + url
	}
	return `<a href="` + messages.EscapeHTML(url) + `">` + messages.EscapeHTML(text) + "</a>"
}

// text lays out m, whose sections are already shortened, with mentions — rendered already — after its heading, or
// first in a message without one.
func (w writer) text(m messages.Message, mentions string) string {
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	esc := func(s string) string { return w.esc(templates.StripTokens(s)) }
	if h := m.Heading; h != nil {
		title := "#" + strconv.FormatInt(h.Number, 10) + " " + templates.StripTokens(h.Title)
		add(strings.TrimSpace(statusEmoji[m.Colour] + " " + w.tag("b", w.link(title, h.URL))))
	}
	add(mentions)
	add(esc(m.Environment))
	var labels []string
	for _, section := range [][]messages.Label{m.GroupLabels, m.CommonLabels, m.CommonAnnotations} {
		for _, l := range section {
			labels = append(labels, esc(l.Name+": "+l.Value))
		}
	}
	if len(labels) > 0 {
		quote := strings.Join(labels, "\n")
		if !w.plain {
			quote = "<blockquote expandable>" + quote + "</blockquote>"
		}
		add(quote)
	}
	if m.Summary != "" {
		add(w.tag("i", esc(m.Summary)))
	}
	if m.Body != nil {
		add(w.output(strings.TrimRight(m.Body.Text, "\n"), m.Body.Markup))
	}
	if a := m.Alerts; a != nil {
		for _, l := range a.Lines {
			line := w.output(l.Text, l.Markup)
			if l.Markup == "" {
				line = esc(l.Text)
			}
			if l.Resolved {
				line = w.tag("s", line)
			}
			add("• " + line)
		}
		if len(a.Lines) == 0 {
			for _, d := range a.Distinct {
				line := d.Label + ": " + strings.Join(d.Values, ", ")
				if d.More > 0 {
					line += " " + messages.T(m.Language, "alerts.more", messages.Args{"count": strconv.Itoa(d.More)})
				}
				add(esc(line))
			}
		}
		if a.FullList != nil {
			add(w.link(a.FullList.Text, a.FullList.URL))
		}
	}
	for _, l := range m.Lines {
		add(esc(l))
	}
	if len(m.Links) > 0 {
		links := make([]string, len(m.Links))
		for i, l := range m.Links {
			links[i] = w.link(l.Text, l.URL)
		}
		add(strings.Join(links, " · "))
	}
	for _, n := range m.Notices {
		add(esc(n))
	}
	add(esc(m.FooterIn(space)))
	return strings.Join(out, "\n")
}

// output is template output written in markup, with its trusted Mention tokens as text: as it is in HTML; as plain
// text, HTML output loses its tags and its entities are decoded, so that nobody reads markup the messenger rejected.
func (w writer) output(s string, markup messages.Markup) string {
	if w.plain && markup == messages.MarkupHTML {
		s = html.UnescapeString(stripTags(s))
	}
	return templates.ReplaceTokens(s, w.tokenText)
}

// tokenText is a trusted Mention token as text that notifies nobody: `@`, a zero-width space and its word or group.
// Telegram has no word for everyone in a chat, and a message does not carry the Owner's Telegram user.
func (w writer) tokenText(tk templates.Token) string {
	if tk.Name == templates.MentionGroup {
		return "@" + zeroWidthSpace + w.esc(tk.Group)
	}
	return "@" + zeroWidthSpace + tk.Name
}

// length is the length of s in UTF-16 code units, as Telegram counts it.
func length(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// fit lays m out within limit (C-12.FR-11): the sections of m are shortened by messages.Shorten for a budget that
// shrinks by what the layout of Telegram adds, until the text fits. A message that still does not fit keeps its
// heading, its Mentions, its line of links and its footer only.
func fit(m messages.Message, w writer, mentions string, limit int) string {
	markup := messages.MarkupHTML
	if w.plain {
		markup = messages.MarkupPlain
	}
	budget := limit - length(mentions)
	for budget > 0 {
		short, _ := messages.Shorten(m, budget, markup)
		text := w.text(short, mentions)
		n := length(text)
		if n <= limit {
			return text
		}
		budget -= max(n-limit, 1)
	}
	short, _ := messages.Shorten(messages.Message{Language: m.Language, Colour: m.Colour, Heading: m.Heading,
		Links: m.Links, Footer: m.FooterIn(space)}, max(limit/2-length(mentions), 1), markup)
	return cutEnd(w.text(short, mentions), limit, w.plain)
}

// cutEnd cuts a text that does not fit at its end; HTML is cut only as plain text would break no tag, so a cut HTML
// text loses its tags first and is never cut inside an entity.
func cutEnd(s string, limit int, plain bool) string {
	if length(s) <= limit {
		return s
	}
	if !plain {
		s = stripTags(s)
	}
	out := make([]rune, 0, limit)
	n := 0
	for _, r := range s {
		size := 1
		if r >= 0x10000 {
			size = 2
		}
		if n+size > limit-1 {
			break
		}
		out = append(out, r)
		n += size
	}
	cut := string(out)
	if amp := strings.LastIndexByte(cut, '&'); !plain && amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut + "…"
}

var tags = regexp.MustCompile(`<[^>]*>`)

// stripTags is HTML without its tags, its entities left escaped.
func stripTags(s string) string { return tags.ReplaceAllString(s, "") }

// mentionText is the Mentions of a Loud message, separated by spaces, without duplicates (C-12.FR-8): a User with a
// Telegram Account link as a `tg://user` link with their display name, otherwise their display name as text, which
// notifies nobody. Telegram offers no Mention of everyone and has no groups (S-037), so those targets are left out.
func mentionText(ts []mentions.Target, w writer) string {
	var out []string
	seen := map[string]bool{}
	for _, t := range ts {
		if t.Kind != mentions.TargetUser || t.User == nil {
			continue
		}
		name := w.esc(messages.Value(t.User.Name))
		s := name
		if !w.plain && userID.MatchString(t.User.ExternalID) {
			s = `<a href="tg://user?id=` + t.User.ExternalID + `">` + name + "</a>"
		}
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}

// keyboard is the inline keyboard of the buttons (C-14.FR-15): Ack, Unack, Unsnooze and Resolve on one row, one
// Snooze button per Snooze duration on the next, each sending its compact action id, at most buttons.MaxLen bytes. A
// button without a usable action id is left out. The keyboard is never nil, so that an edit always carries it and an
// edit to a state without buttons removes them on purpose (F-011).
func keyboard(bs []messages.Button) *inlineKeyboard {
	var first, snooze []inlineButton
	for _, b := range bs {
		if b.ActionID == "" || len(b.ActionID) > buttons.MaxLen || !utf8.ValidString(b.Label) {
			continue
		}
		ib := inlineButton{Text: b.Label, CallbackData: b.ActionID}
		if b.Command == buttons.CommandSnooze {
			snooze = append(snooze, ib)
		} else {
			first = append(first, ib)
		}
	}
	k := &inlineKeyboard{InlineKeyboard: [][]inlineButton{}}
	for _, row := range [][]inlineButton{first, snooze} {
		if len(row) > 0 {
			k.InlineKeyboard = append(k.InlineKeyboard, row)
		}
	}
	return k
}
