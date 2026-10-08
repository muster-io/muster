// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

// The layout of posts (C-13.FR-3, C-12.FR-1): a Root message is a post whose message is the summary line — the status
// emoji, #N and the title — followed on a Loud post by its Mentions, with one attachment coloured by status whose
// title links to the Alert Group page and whose text holds the sections 3 to 8 of C-12.FR-1, the line of links that
// starts with "Open in Muster", the notices and the footer, as its last line; its buttons follow. Mentions never go
// into the attachment, where they would notify with an empty notification (F-056). A Thread reply carries its
// Mentions and its text in its message. Alert data are escaped for Mattermost Markdown and their `@` neutralized by
// the escaper of internal/messages. The trusted Mention tokens of template output are written where they stand
// without notifying; a Loud Publish adds the everyone and group ones to its Mentions (C-13.FR-8).

// LengthLimit is the longest post message, and the longest attachment text, in characters: the server's MaxPostSize
// (F-057).
const LengthLimit = 16_383

// colours are the colours of the attachment by the colour of the message.
var colours = map[string]string{
	messages.ColourFiring:       "#d32f2f",
	messages.ColourAcknowledged: "#f57c00",
	messages.ColourResolved:     "#388e3c",
	messages.ColourSnoozed:      "#9e9e9e",
	messages.ColourStorm:        "#6a1b9a",
}

// statusEmoji starts the summary line, by the colour of the message (C-12.FR-1 item 1).
var statusEmoji = map[string]string{
	messages.ColourFiring: "🔴", messages.ColourAcknowledged: "🟠", messages.ColourResolved: "🟢",
	messages.ColourSnoozed: "⚪", messages.ColourStorm: "⛈",
}

// actionIDs are the stable ids of the buttons by Command; Mattermost takes letters and digits only, and a Snooze
// button is "snooze" with the index of its duration.
var actionIDs = map[string]string{
	buttons.CommandAcknowledge:   "ack",
	buttons.CommandUnacknowledge: "unack",
	buttons.CommandResolve:       "resolve",
	buttons.CommandUnsnooze:      "unsnooze",
	buttons.CommandSnooze:        "snooze",
	buttons.CommandStillOnIt:     "stillonit",
}

// name is a Mattermost username or group name, which a Mention writes after `@`.
var name = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// zeroWidthSpace follows the `@` of a Mention that must not notify.
const zeroWidthSpace = "\u200b"

// look is how messages are laid out for one Destination: the markup, the identity space whose username the footer
// names a user by, the callback address of the buttons, the attachment's footer and icon, and the length limit.
type look struct {
	markup   messages.Markup
	space    string
	callback string
	footer   string
	icon     string
	limit    int
}

// rootPost lays out a Root message, or a Storm summary, which has no heading; mentions are the rendered Mentions of a
// Loud post, empty for a Quiet one and for every edit. A message longer than the limit is shortened first
// (C-12.FR-11).
func rootPost(m messages.Message, l look, mentions string) post {
	m, _ = messages.Shorten(m, l.limit, l.markup)
	h := m.Heading
	if h == nil {
		return post{Message: cutEnd(joinLines(text(m, l), mentions), l.limit), Props: buttonsOnly(m, l)}
	}
	att := attachment{Color: colours[m.Colour], Title: "#" + strconv.FormatInt(h.Number, 10) + " " +
		templates.StripTokens(h.Title), TitleLink: h.URL, Text: text(m, l), Footer: l.footer, FooterIcon: l.icon,
		Actions: actions(m.Buttons, l.callback)}
	return post{Message: cutEnd(joinLines(summaryLine(m, l.markup), mentions), l.limit),
		Props: &props{Attachments: []attachment{att}}}
}

// replyPost lays out a Thread reply: its Mentions first, then its text; its buttons, when it has any, in an
// attachment.
func replyPost(m messages.Message, l look, mentions string) post {
	m, _ = messages.Shorten(m, l.limit, l.markup)
	p := post{Message: cutEnd(joinLines(mentions, text(m, l)), l.limit)}
	if len(m.Buttons) > 0 {
		p.Props = buttonsOnly(m, l)
	}
	return p
}

// buttonsOnly are the props of a post without a heading: one attachment with its buttons, or none.
func buttonsOnly(m messages.Message, l look) *props {
	if len(m.Buttons) == 0 {
		return &props{Attachments: []attachment{}}
	}
	return &props{Attachments: []attachment{{Color: colours[m.Colour], Actions: actions(m.Buttons, l.callback)}}}
}

// summaryLine is the status emoji, #N and the title, escaped like alert data.
func summaryLine(m messages.Message, markup messages.Markup) string {
	h := m.Heading
	title := messages.Escaper(markup)(templates.StripTokens(h.Title))
	return strings.TrimSpace(statusEmoji[m.Colour] + " #" + strconv.FormatInt(h.Number, 10) + " " + title)
}

// text is the text of m without its heading and buttons, as messages.Layout lays it out for the markup, with the
// footer of the Destination's identity space as its last line and the trusted Mention tokens as text that notifies
// nobody.
// A text still longer than the limit loses its middle, so that the line of links, the notices and the footer stay.
func text(m messages.Message, l look) string {
	cp := m
	cp.Heading, cp.Buttons, cp.Footer, cp.Footers = nil, nil, m.FooterIn(l.space), nil
	if m.Body != nil {
		cp.Body = &messages.Body{Text: templates.ReplaceTokens(m.Body.Text, tokenText), Markup: m.Body.Markup}
	}
	if m.Alerts != nil {
		a := *m.Alerts
		a.Lines = slices.Clone(a.Lines)
		for i, line := range a.Lines {
			if line.Markup != "" {
				a.Lines[i].Text = templates.ReplaceTokens(line.Text, tokenText)
			}
		}
		cp.Alerts = &a
	}
	full := messages.Layout(cp, l.markup)
	if utf8.RuneCountInString(full) <= l.limit {
		return full
	}
	tail := messages.Layout(messages.Message{Links: cp.Links, Notices: cp.Notices, Footer: cp.Footer}, l.markup)
	cp.Links, cp.Notices, cp.Footer = nil, nil, ""
	return cutMiddle(messages.Layout(cp, l.markup), tail, l.limit)
}

// tokenText is a trusted Mention token as text that notifies nobody: `@`, a zero-width space and its word or group.
// The Owner stays such text everywhere, since a message does not carry the Owner's username.
func tokenText(tk templates.Token) string {
	if tk.Name == templates.MentionGroup {
		if name.MatchString(tk.Group) {
			return "@" + zeroWidthSpace + tk.Group
		}
		return ""
	}
	return "@" + zeroWidthSpace + tk.Name
}

// templateTokens are the trusted Mention tokens of the template output of m: its body and its Alert lines written by
// a line template.
func templateTokens(m messages.Message) []templates.Token {
	var out []templates.Token
	if m.Body != nil {
		out = append(out, templates.Tokens(m.Body.Text)...)
	}
	if m.Alerts != nil {
		for _, line := range m.Alerts.Lines {
			if line.Markup != "" {
				out = append(out, templates.Tokens(line.Text)...)
			}
		}
	}
	return out
}

// mentionText is the Mentions of a Loud post in Mattermost's syntax, separated by spaces, without duplicates: of the
// targets, everyone with its word, a group by its name, and a user by the username of their Account link, otherwise
// by the display name as plain text, which notifies nobody (C-12.FR-8); then of the template tokens, everyone and the
// groups, the Owner's being text where it stands.
func mentionText(ts []mentions.Target, tokens []templates.Token, markup messages.Markup) string {
	var out []string
	add := func(s string) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, t := range ts {
		switch t.Kind {
		case mentions.TargetEveryone:
			switch t.Everyone {
			case mentions.EveryoneChannel, mentions.EveryoneAll, mentions.EveryoneHere:
				add("@" + t.Everyone)
			}
		case mentions.TargetGroup:
			if name.MatchString(t.Group) {
				add("@" + t.Group)
			}
		case mentions.TargetUser:
			switch {
			case t.User == nil:
			case name.MatchString(t.User.Username):
				add("@" + t.User.Username)
			default:
				add(messages.Escaper(markup)(messages.Value(t.User.Name)))
			}
		}
	}
	for _, tk := range tokens {
		switch tk.Name {
		case templates.MentionAll, templates.MentionChannel, templates.MentionHere:
			add("@" + tk.Name)
		case templates.MentionGroup:
			if name.MatchString(tk.Group) {
				add("@" + tk.Group)
			}
		}
	}
	return strings.Join(out, " ")
}

// actions are the buttons as attachment actions, each calling the callback with its signed action id.
func actions(bs []messages.Button, callback string) []action {
	out := make([]action, 0, len(bs))
	for _, b := range bs {
		id := actionIDs[b.Command]
		if b.Command == buttons.CommandSnooze {
			id += strconv.Itoa(b.Argument)
		}
		out = append(out, action{ID: id, Name: b.Label, Type: "button", Integration: integration{URL: callback,
			Context: actionContext{Action: b.ActionID, KeyID: b.KeyID}}})
	}
	return out
}

// joinLines joins the parts that are not empty with new lines.
func joinLines(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), "\n")
}

// ellipsis ends a text that was cut.
const ellipsis = "…"

// cutEnd cuts s to at most limit characters, ending it with "…" when it was cut.
func cutEnd(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:max(limit-1, 0)]) + ellipsis
}

// cutMiddle joins head and tail on a new line within limit characters, cutting the end of head; a tail that does not
// fit alone is cut at its end.
func cutMiddle(head, tail string, limit int) string {
	if tail == "" {
		return cutEnd(head, limit)
	}
	room := limit - utf8.RuneCountInString(tail) - 2
	if room < 1 {
		return cutEnd(tail, limit)
	}
	return string([]rune(head)[:min(room, utf8.RuneCountInString(head))]) + ellipsis + "\n" + tail
}
