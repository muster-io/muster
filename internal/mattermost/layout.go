// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"cmp"
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

// The layout of posts (C-13.FR-3, C-12.FR-1): a Root message is a post whose message is empty, with one attachment
// coloured by status whose title, #N and the title, links to the Alert Group page and whose text holds, on a Loud post,
// its Mentions as its first line, then the sections 3 to 8 of C-12.FR-1, the line of links that starts with "Open in
// Muster", the notices and the footer, as its last line; its buttons follow. A Storm summary, which has no heading, is
// laid out the same way without a title, in the Storm colour. A user's Mention, `@channel`, `@here` and `@all` in the
// attachment notify (F-056, F-113); an email of such a post shows no content (F-056), while push and desktop
// notifications show the attachment's fallback, the summary line without Mentions, which the channel does not draw
// (F-115). An edit is Quiet and has no Mentions line (F-029) and clears the message of a post laid out with a summary
// line (F-114). A Thread reply carries its Mentions and its text in its message; a Reminder, which has buttons, carries
// its Mentions in its message and its text and buttons in one attachment (C-13.FR-4). Alert data are escaped for
// Mattermost Markdown and their `@` neutralized by the escaper of internal/messages. The trusted Mention tokens of
// template output are written where they stand without notifying; a Loud Publish adds the everyone and group ones to
// its Mentions (C-13.FR-8).

// LengthLimit is the longest post message, and the longest attachment text, in characters: the server's MaxPostSize
// (F-057).
const LengthLimit = 16_383

// actionIDs are the stable ids of the buttons by Command; Mattermost takes letters and digits only, and a Snooze
// button is "snooze" with the index of its duration, or with the name of its Snooze preset (presetActionIDs).
var actionIDs = map[string]string{
	buttons.CommandAcknowledge:   "ack",
	buttons.CommandUnacknowledge: "unack",
	buttons.CommandResolve:       "resolve",
	buttons.CommandUnsnooze:      "unsnooze",
	buttons.CommandSnooze:        "snooze",
	buttons.CommandStillOnIt:     "stillonit",
}

// presetActionIDs are the ends of the action ids of the Snooze preset buttons, by their argument (C-10.FR-18), in
// letters only: "snoozetomorrow" and "snoozeweek".
var presetActionIDs = map[int]string{
	buttons.ArgumentTomorrow:  "tomorrow",
	buttons.ArgumentWeekStart: "week",
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

// rootPost lays out a Root message, or a Storm summary, which has no heading and so no title: an empty message and
// one attachment that holds everything. mentions are the rendered Mentions of a Loud post, the first line of the
// attachment's text, none for a Quiet one and for every edit. A message longer than the limit, its Mentions line
// counted, is shortened first (C-12.FR-11). The attachment's fallback, which push and desktop notifications show and
// the channel does not (F-115), is the summary line, without Mentions.
func rootPost(m messages.Message, l look, mentions []string) post {
	limit := l.limit
	if line := strings.Join(mentions, " "); line != "" {
		limit -= utf8.RuneCountInString(line) + 1
	}
	m, _ = messages.Shorten(m, max(limit, 1), l.markup)
	att := attachment{Fallback: cutEnd(fallback(m), l.limit), Color: messages.StatusColour(m.Colour),
		Text: text(m, l, mentions), Actions: actions(m.Buttons, l.callback)}
	if h := m.Heading; h != nil {
		att.Title = "#" + strconv.FormatInt(h.Number, 10) + " " + templates.StripTokens(h.Title)
		att.TitleLink, att.Footer, att.FooterIcon = h.URL, l.footer, l.icon
	}
	return post{Props: &props{Attachments: []attachment{att}}}
}

// fallback is the plain summary line of m: the status emoji, #N and the title of a Root message, or the first line of
// a Storm summary, with every `@` neutralized.
func fallback(m messages.Message) string {
	if h := m.Heading; h != nil {
		return messages.Neutralize(strings.TrimSpace(messages.StatusEmoji(m.Colour) + " #" +
			strconv.FormatInt(h.Number, 10) + " " + templates.StripTokens(h.Title)))
	}
	cp := m
	cp.Buttons = nil
	line, _, _ := strings.Cut(strings.TrimSpace(messages.Layout(cp, messages.MarkupPlain)), "\n")
	return messages.Neutralize(line)
}

// replyPost lays out a Thread reply: its Mentions first, then its text. A reply with buttons — a Reminder's "Still on
// it" and "Unack" — keeps its Mentions in its message and has its text and buttons in one attachment.
func replyPost(m messages.Message, l look, mentions []string) post {
	m, _ = messages.Shorten(m, l.limit, l.markup)
	line := strings.Join(mentions, " ")
	if len(m.Buttons) == 0 {
		return post{Message: cutEnd(joinLines(line, text(m, l, nil)), l.limit)}
	}
	return post{Message: cutEnd(line, l.limit), Props: &props{Attachments: []attachment{{
		Color: messages.StatusColour(m.Colour), Text: text(m, l, nil), Actions: actions(m.Buttons, l.callback)}}}}
}

// text is the text of m without its heading and buttons, as messages.Layout lays it out for the markup, after the
// line of mentions when there is one, with the footer of the Destination's identity space as its last line and the
// trusted Mention tokens as text that notifies nobody.
// A text still longer than the limit loses its middle, so that the Mentions, the line of links, the notices and the
// footer stay; a Mentions line too long for that loses whole Mentions from its end.
func text(m messages.Message, l look, mentions []string) string {
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
	full := joinLines(strings.Join(mentions, " "), messages.Layout(cp, l.markup))
	if utf8.RuneCountInString(full) <= l.limit {
		return full
	}
	tail := messages.Layout(messages.Message{Links: cp.Links, Notices: cp.Notices, Footer: cp.Footer}, l.markup)
	cp.Links, cp.Notices, cp.Footer = nil, nil, ""
	// The head keeps limit - tail - 2 characters (cutMiddle): the Mentions line and its new line fit in it whole.
	line := fitMentions(mentions, l.limit-utf8.RuneCountInString(tail)-3)
	return cutMiddle(joinLines(line, messages.Layout(cp, l.markup)), tail, l.limit)
}

// fitMentions is the line of the Mentions that fit in n characters, separated by spaces: whole entries from the
// start, so that no `@name`, and no display name with spaces, is ever cut.
func fitMentions(mentions []string, n int) string {
	var out []string
	size := -1
	for _, w := range mentions {
		size += 1 + utf8.RuneCountInString(w)
		if size > n {
			break
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
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

// mentionText is the Mentions of a Loud post in Mattermost's syntax, one entry each, without duplicates: of the
// targets, everyone with its word, a group by its name, and a user by the username of their Account link, otherwise
// by the display name as plain text, which notifies nobody (C-12.FR-8); then of the template tokens, everyone and the
// groups, the Owner's being text where it stands. A post writes them separated by spaces.
func mentionText(ts []mentions.Target, tokens []templates.Token, markup messages.Markup) []string {
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
	return out
}

// actionStyles are the styles of attachment actions by style hint (F-068); a neutral button has no style and stays
// plain.
var actionStyles = map[buttons.Style]string{
	buttons.StylePrimary: "primary",
	buttons.StyleSuccess: "success",
}

// actions are the buttons as attachment actions, each with the style of its hint and calling the callback with its
// signed action id.
func actions(bs []messages.Button, callback string) []action {
	out := make([]action, 0, len(bs))
	for _, b := range bs {
		id := actionIDs[b.Command]
		if b.Command == buttons.CommandSnooze {
			id += cmp.Or(presetActionIDs[b.Argument], strconv.Itoa(b.Argument))
		}
		out = append(out, action{ID: id, Name: b.Label, Type: "button", Style: actionStyles[b.Style()],
			Integration: integration{URL: callback, Context: actionContext{Action: b.ActionID, KeyID: b.KeyID}}})
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
