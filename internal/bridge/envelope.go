// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

// The envelope (C-22.FR-4, FR-5, FR-7): the status of the message with its colour and emoji; for a Root message the
// heading as its title, `#N` and the title, and its link "Open in Muster" to the Alert Group page; for a Storm summary
// its first line and its first link; the buttons, only when the bridge declares `buttons`, with their style hints;
// and the body, the rest of the message in CommonMark within `limits.max_body_length` of the Connection's info. The
// body is shortened as C-12.FR-11 says, keeping its line of links, its notices and its footer; without `buttons`, a
// bridge that forwards Thread messages gets the commands line at the end of a Root message (messages.CommandsLine).
// Template output keeps its trusted Mention tokens as text that notifies nobody, until the Mention markers of S-076.

// MinBodyLength is the protocol's floor of limits.max_body_length, in code points: the body limit when info has none.
const MinBodyLength = 500

// The longest title and button label, in characters, and the longest button data, in bytes (api/bridge.yaml).
const (
	maxTitle = 512
	maxLabel = 64
	maxData  = 64
)

// blockBreak separates the blocks of a CommonMark body.
const blockBreak = "\n\n"

// ellipsis ends a text that was cut.
const ellipsis = "…"

// zeroWidthSpace follows the `@` of a Mention token written as text, so that no messenger reads a Mention in it.
const zeroWidthSpace = "\u200b"

// content is the envelope of m for a bridge whose info is i, with head — the mention line of a Loud call, the mark of a
// test message — before its body.
func content(m messages.Message, i Info, head []string) gen.MessageContent {
	c := gen.MessageContent{Status: gen.Status(m.Colour), Color: messages.StatusColour(m.Colour),
		Emoji: messages.StatusEmoji(m.Colour), Buttons: buttonsOf(m, i)}
	rest := m
	if h := m.Heading; h != nil {
		c.Title = "#" + strconv.FormatInt(h.Number, 10) + " " + templates.StripTokens(h.Title)
		c.Link = gen.Link{Url: h.URL, Label: messages.T(m.Language, "link.open", nil)}
	} else {
		if len(m.Lines) > 0 {
			c.Title, rest.Lines = m.Lines[0], m.Lines[1:]
		}
		if len(m.Links) > 0 {
			c.Link = gen.Link{Url: m.Links[0].URL, Label: m.Links[0].Text}
		}
	}
	c.Title = cutEnd(strings.TrimSpace(c.Title), maxTitle)
	c.Body = bodyOf(rest, i, head)
	return c
}

// postInput is the envelope c posted with postMessage as a message of kind to channel; notify is a Loud message. Its
// Mentions stay empty until S-076.
func postInput(kind gen.MessageKind, channel string, c gen.MessageContent, notify bool) gen.MessageInput {
	return gen.MessageInput{Kind: kind, ChannelId: channel, Status: c.Status, Color: c.Color, Emoji: c.Emoji,
		Title: c.Title, Link: c.Link, Body: c.Body, Buttons: c.Buttons, Mentions: []gen.Mention{}, Notify: notify}
}

// replyInput is the Thread reply m for a bridge whose info is i, under the Thread threadRef, null when it is empty,
// with head before its body; notify is a Loud reply.
func replyInput(threadRef string, m messages.Message, i Info, head []string, notify bool) gen.ThreadReplyInput {
	ref := nullable.NewNullNullable[string]()
	if threadRef != "" {
		ref = nullable.NewNullableWithValue(threadRef)
	}
	return gen.ThreadReplyInput{ThreadRef: ref, Body: bodyOf(m, i, head), Buttons: buttonsOf(m, i),
		Mentions: []gen.Mention{}, Notify: notify}
}

// mentionLine is the Mentions of a Loud call as a line: the display names of the Users it targets, escaped and
// separated by spaces, which notify nobody until the Mention markers of S-076; a Quiet call has none.
func mentionLine(c delivery.Call) []string {
	if c.Loudness != groups.Loud {
		return nil
	}
	var names []string
	for _, t := range c.Targets {
		if t.Kind != mentions.TargetUser || t.User == nil {
			continue
		}
		if n := messages.EscapeCommonMark(messages.Value(t.User.Name)); n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return []string{strings.Join(names, " ")}
}

// bodyLimit is limits.max_body_length of i, MinBodyLength without one.
func bodyLimit(i Info) int {
	if n := i.Limits.MaxBodyLength; n > 0 {
		return n
	}
	return MinBodyLength
}

// bodyOf is the CommonMark body of m for a bridge whose info is i: head, the text of m without its heading and
// buttons, and the commands line of a Root message when the bridge has Thread messages and no buttons, separated by
// blank lines and within the body limit of i.
func bodyOf(m messages.Message, i Info, head []string) string {
	limit := bodyLimit(i)
	var line string
	if caps := i.Capabilities; m.Kind == messages.KindRoot && caps.Buttons == nil && caps.ThreadMessages != nil {
		line = messages.CommandsLine(m, *caps.ThreadMessages == gen.MentionsOnly)
	}
	others := slices.DeleteFunc(append(slices.Clone(head), line), func(s string) bool { return s == "" })
	room := limit
	for _, s := range others {
		room -= utf8.RuneCountInString(s) + utf8.RuneCountInString(blockBreak)
	}
	parts := slices.Clone(head)
	parts = append(parts, fit(laidOut(m), max(room, 0)), line)
	parts = slices.DeleteFunc(parts, func(s string) bool { return s == "" })
	return cutEnd(strings.Join(parts, blockBreak), limit)
}

// laidOut is m as the body lays it out: without buttons, with its footer in no identity space until the Account links
// of S-076, and the trusted Mention tokens of its template output as text that notifies nobody. Its heading stays, so
// that shortening links the full list of Alerts to the Alert Group page.
func laidOut(m messages.Message) messages.Message {
	cp := m
	cp.Buttons, cp.Footers = nil, nil
	if m.Body != nil {
		cp.Body = &messages.Body{Text: templates.ReplaceTokens(m.Body.Text, tokenText), Markup: m.Body.Markup}
	}
	if m.Alerts != nil {
		a := *m.Alerts
		a.Lines = slices.Clone(a.Lines)
		for n, l := range a.Lines {
			if l.Markup != "" {
				a.Lines[n].Text = templates.ReplaceTokens(l.Text, tokenText)
			}
		}
		cp.Alerts = &a
	}
	return cp
}

// tokenText is a trusted Mention token as text that notifies nobody: `@`, a zero-width space and its word or group.
func tokenText(tk templates.Token) string {
	if tk.Name == templates.MentionGroup {
		return "@" + zeroWidthSpace + messages.EscapeCommonMark(tk.Group)
	}
	return "@" + zeroWidthSpace + tk.Name
}

// fit lays m out without its heading in CommonMark within room characters: shortened as C-12.FR-11 says, and when
// that is not enough, its line of links, notices and footer kept whole after the start of the rest, cut.
func fit(m messages.Message, room int) string {
	extra := 0
	if h := m.Heading; h != nil {
		// The heading is not in the body: shortening lays it out only so that the full list of Alerts links to the
		// Alert Group page, without the title, which it would cut, and its block does not count.
		m.Heading = &messages.Heading{Number: h.Number, URL: h.URL}
		extra = messages.Length(messages.Message{Heading: m.Heading, Colour: m.Colour}, messages.MarkupCommonMark) +
			utf8.RuneCountInString(blockBreak)
	}
	short, _ := messages.Shorten(m, room+extra, messages.MarkupCommonMark)
	short.Heading = nil
	full := messages.Layout(short, messages.MarkupCommonMark)
	if utf8.RuneCountInString(full) <= room {
		return full
	}
	tail := messages.Layout(messages.Message{Links: short.Links, Notices: short.Notices, Footer: short.Footer},
		messages.MarkupCommonMark)
	short.Links, short.Notices, short.Footer = nil, nil, ""
	return cutMiddle(messages.Layout(short, messages.MarkupCommonMark), tail, room)
}

// cutMiddle joins head and tail as two blocks within limit characters, cutting the end of head; a tail that does not
// fit alone is cut at its end.
func cutMiddle(head, tail string, limit int) string {
	if head == "" || tail == "" {
		return cutEnd(head+tail, limit)
	}
	room := limit - utf8.RuneCountInString(tail) - utf8.RuneCountInString(blockBreak) -
		utf8.RuneCountInString(ellipsis)
	if room < 1 {
		return cutEnd(tail, limit)
	}
	r := []rune(head)
	return string(r[:min(room, len(r))]) + ellipsis + blockBreak + tail
}

// cutEnd cuts s to at most limit characters, ending it with "…" when it was cut.
func cutEnd(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	if limit < 1 {
		return ""
	}
	return string([]rune(s)[:limit-1]) + ellipsis
}

// buttonsOf are the buttons of m for a bridge whose info is i (C-22.FR-5): none without `buttons`; otherwise each
// signed button with its label, its style hint and its action id as data, at most `buttons.max_per_message` of them —
// the Snooze buttons of the longest durations go first, then buttons from the end.
func buttonsOf(m messages.Message, i Info) []gen.Button {
	out := []gen.Button{}
	caps := i.Capabilities.Buttons
	if caps == nil {
		return out
	}
	kept := slices.DeleteFunc(slices.Clone(m.Buttons), func(b messages.Button) bool {
		return b.ActionID == "" || len(b.ActionID) > maxData || b.Label == ""
	})
	for limit := max(caps.MaxPerMessage, 0); len(kept) > limit; {
		n := longestSnooze(kept, m.SnoozeSeconds)
		if n < 0 {
			kept = kept[:limit]
			break
		}
		kept = slices.Delete(kept, n, n+1)
	}
	for _, b := range kept {
		out = append(out, gen.Button{Label: cutEnd(b.Label, maxLabel), Style: gen.ButtonStyle(b.Style()),
			Data: b.ActionID})
	}
	return out
}

// longestSnooze is the index in bs of the Snooze button of the longest duration of seconds, the last of equal ones;
// -1 without a Snooze button.
func longestSnooze(bs []messages.Button, seconds []int64) int {
	best, longest := -1, int64(-1)
	for n, b := range bs {
		if b.Command != buttons.CommandSnooze {
			continue
		}
		var s int64
		if b.Argument >= 0 && b.Argument < len(seconds) {
			s = seconds[b.Argument]
		}
		if s >= longest {
			best, longest = n, s
		}
	}
	return best
}
