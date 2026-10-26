// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

// The layout of messages (C-24.FR-4 to FR-7, C-12.FR-1). Every request names its `text`, its `blocks` and its
// `attachments`, an empty list where there are none, because an edit without them keeps or replaces what was there
// (F-085), and turns unfurling off (F-083); it uses only `section`, `context` and `actions` blocks, never `rich_text`
// or `markdown` (F-081, F-086). A Root message's `text` and top `section` hold the summary line — the status emoji, #N
// and the title — and, on a Loud message, its Mentions after it: a User with an Account link for the Destination's
// Connection as `<@U…>` (F-097), anyone else by their display name as text; one attachment in the colour of its status
// (F-075) holds a `section` with the heading linked to the Alert Group page, a `section` with items 3 to 11 in mrkdwn —
// the body, the line of links that starts with "Open in Muster", the notices and the footer — within 3,000 code points
// as sent (F-076), a `context` with "Muster v<version>" and no image, and the buttons with the Snooze menu (C-24.FR-5).
// A Thread reply, a Reminder and a Storm summary have their Mentions and text in one `section`, a Reminder's buttons in
// an `actions` block, and no attachment. `text` is only a notification's fallback: its title is cut to 60 code points
// and its Mentions to 1,000 bytes, so that it stays under the 4,000 bytes an edit takes even when Slack stores every
// character as an entity or a shortcode (F-077 to F-079). Alert data reach the layout escaped for mrkdwn by
// internal/messages (C-24.FR-7); template output keeps its markup, with its trusted Mention tokens as text that
// notifies nobody and its literal `<!…>` and `<@…>` neutralized, so that only the Destination's Mention settings notify
// (C-12.FR-8). After Slack rejected the markup, the same message goes as `text` alone (C-11.FR-8).

// LengthLimit is the longest text of a section in code points of the mrkdwn as sent, entities and link markup
// included (F-076): the length limit the adapter declares, which the body of a message is shortened to (C-12.FR-11).
const LengthLimit = 3000

const (
	// textTitleLimit is the longest title in a message's `text`, in code points before escaping, and the longest
	// first line of a Thread reply there (C-24.FR-4).
	textTitleLimit = 60
	// textMentionsBytes is the most bytes of Mentions a `text` carries; the others stay in the top section.
	textMentionsBytes = 1000
	// storedTextLimit is the size of `text` in Slack's stored form at which an edit is refused (F-078).
	storedTextLimit = 4000
	// shortcodeBytes stands for a symbol in Slack's stored form: longer than the shortcode of any emoji (F-079).
	shortcodeBytes = 48
	// labelLimit is the longest text of a button and of an option of the Snooze menu, in code points (F-076).
	labelLimit = 75
	// valueLimit is the longest `value` of an option of the Snooze menu, in bytes (F-076): a button's `value` keeps
	// to it too, so that every signed action id fits both (C-24.FR-5).
	valueLimit = 150
	// maxOptions is the most options of a static_select menu in Block Kit.
	maxOptions = 100
	// plainLimit is the most bytes of a message sent as `text` alone after Slack rejected its markup, which keeps it
	// one message (F-077, C-24.FR-8).
	plainLimit = 3000
)

// The block ids of a message, each unique in it (F-076); a press names the block of its button, muster_actions.
const (
	blockSummary = "muster_summary"
	blockHeading = "muster_heading"
	blockBody    = "muster_body"
	blockContext = "muster_context"
	blockActions = "muster_actions"
	blockText    = "muster_text"
)

// The types of blocks, elements and text objects Muster sends, and the one button style it uses (F-074).
const (
	typeSection      = "section"
	typeContext      = "context"
	typeActions      = "actions"
	typeButton       = "button"
	typeStaticSelect = "static_select"
	typeMrkdwn       = "mrkdwn"
	typePlainText    = "plain_text"
	stylePrimary     = "primary"
)

// actionIDs name the Command of each button, and of the Snooze menu, unique within their block (C-24.FR-5); a press
// carries the Command in its signed `value` as well.
var actionIDs = map[string]string{
	buttons.CommandAcknowledge:   "ack",
	buttons.CommandUnacknowledge: "unack",
	buttons.CommandResolve:       "resolve",
	buttons.CommandUnsnooze:      "unsnooze",
	buttons.CommandSnooze:        "snooze",
	buttons.CommandStillOnIt:     "still_on_it",
}

// everyoneMentions are the Mentions of everyone in the chat by their word, a Destination's setting or a template's
// token alike (C-24.FR-6): Slack has no `@all`, and Muster names no groups.
var everyoneMentions = map[string]string{
	mentions.EveryoneChannel: "<!channel>",
	mentions.EveryoneHere:    "<!here>",
}

// zeroWidthSpace follows the `@` of a Mention token written as text, so that it notifies nobody.
const zeroWidthSpace = "\u200b"

// ellipsis ends a text that was cut.
const ellipsis = "…"

// outgoing is the body of chat.postMessage and chat.update: the channel, the `ts` of the message an edit changes, the
// `ts` of the Root message a Thread reply goes under — never with `reply_broadcast` (C-24.FR-6) — the fallback
// `text`, the blocks and the attachments.
type outgoing struct {
	Channel     string
	TS          string
	ThreadTS    string
	Text        string
	Blocks      []block
	Attachments []attachment
}

// args are the arguments of the request: `blocks` and `attachments` always named, an empty list where there are
// none, and unfurling off.
func (o outgoing) args() Args {
	a := Args{"channel": o.Channel, "text": o.Text, "blocks": o.Blocks, "attachments": o.Attachments,
		"unfurl_links": false, "unfurl_media": false}
	if o.Blocks == nil {
		a["blocks"] = []block{}
	}
	if o.Attachments == nil {
		a["attachments"] = []attachment{}
	}
	if o.TS != "" {
		a["ts"] = o.TS
	}
	if o.ThreadTS != "" {
		a["thread_ts"] = o.ThreadTS
	}
	return a
}

// textObject is a text in mrkdwn or plain text.
type textObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func mrkdwnText(s string) *textObject { return &textObject{Type: typeMrkdwn, Text: s} }

func plainText(s string) textObject { return textObject{Type: typePlainText, Text: s} }

// block is a `section` with its text, a `context` or an `actions` block with its elements.
type block struct {
	Type     string      `json:"type"`
	BlockID  string      `json:"block_id"`
	Text     *textObject `json:"text,omitempty"`
	Elements []any       `json:"elements,omitempty"`
}

func section(id, text string) block {
	return block{Type: typeSection, BlockID: id, Text: mrkdwnText(text)}
}

// button is a button whose `value` is its signed action id.
type button struct {
	Type     string     `json:"type"`
	ActionID string     `json:"action_id"`
	Text     textObject `json:"text"`
	Value    string     `json:"value"`
	Style    string     `json:"style,omitempty"`
}

// staticSelect is the Snooze menu, its options the Snooze durations and then the Snooze presets.
type staticSelect struct {
	Type        string     `json:"type"`
	ActionID    string     `json:"action_id"`
	Placeholder textObject `json:"placeholder"`
	Options     []option   `json:"options"`
}

// option is an option of the Snooze menu, whose `value` is the signed action id of its Snooze button.
type option struct {
	Text  textObject `json:"text"`
	Value string     `json:"value"`
}

// attachment holds the blocks of a Root message under the colour bar of its status (F-075).
type attachment struct {
	Color    string  `json:"color,omitempty"`
	Fallback string  `json:"fallback"`
	Blocks   []block `json:"blocks"`
}

// look is how messages are laid out for one Destination: its channel, the identity space whose Account links the
// footer names users by (C-12.FR-12), the version the context names, plain text after Slack rejected the markup, and
// the mark of a test message, empty for any other.
type look struct {
	channel string
	space   string
	version string
	plain   bool
	mark    string
}

// lookOf is how the messages of the call c are laid out in channel by a Muster of version.
func lookOf(c delivery.Call, channel, version string) look {
	return look{channel: channel, space: mentions.IdentitySpace(mentions.TypeSlack, c.Destination.Connection),
		version: strings.TrimPrefix(version, "v"), plain: c.Plain}
}

// callMentions are the Mentions of the call c with the message m: of a Loud call, its targets and the everyone tokens
// of the template output of m (C-12.FR-8); a Quiet call, every edit included, mentions nobody (C-24.FR-6).
func callMentions(c delivery.Call, m messages.Message) []string {
	if c.Loudness != groups.Loud {
		return nil
	}
	return mentionText(c.Targets, templateTokens(m))
}

// layout is the request of m with the rendered mentions, empty for a Quiet message: as text alone after Slack
// rejected the markup, a Root message — a final edit and a test message included — by its heading, and anything
// else as a Thread reply. The caller sets the `ts` of an edit and the `thread_ts` of a Thread reply.
func layout(m messages.Message, l look, mentions []string) outgoing {
	switch {
	case l.plain:
		return plainRequest(m, l, mentions)
	case m.Heading == nil:
		return replyRequest(m, l, mentions)
	}
	return rootRequest(m, l, mentions)
}

// testRequest is the test message m (C-24.FR-13): laid out as a Root message, Quiet, with "🧪 Test message" before
// its summary line in `text` and in the top section.
func testRequest(m messages.Message, l look) outgoing {
	l.mark = messages.EscapeMrkdwn(messages.TestMark(m.Language))
	return layout(m, l, nil)
}

// request is an empty request to the channel of l.
func request(l look) outgoing {
	return outgoing{Channel: l.channel, Blocks: []block{}, Attachments: []attachment{}}
}

// rootRequest is a Root message: the summary line in `text` and in the top section, with the Mentions, and one
// attachment in the colour of its status with the heading, the body, the context and the buttons.
func rootRequest(m messages.Message, l look, mentions []string) outgoing {
	o := request(l)
	o.Text = withMentions(head(m, l, " · ", textTitleLimit), mentions)
	o.Blocks = append(o.Blocks, section(blockSummary, topText(m, l, mentions)))
	blocks := []block{section(blockHeading, headingText(m))}
	if body := bodyText(m, l, sectionBound); body != "" {
		blocks = append(blocks, section(blockBody, body))
	}
	blocks = append(blocks, block{Type: typeContext, BlockID: blockContext,
		Elements: []any{plainText("Muster v" + l.version)}})
	if a, ok := actionsBlock(m); ok {
		blocks = append(blocks, a)
	}
	o.Attachments = append(o.Attachments, attachment{Color: messages.StatusColour(m.Colour),
		Fallback: head(m, l, " · ", textTitleLimit), Blocks: blocks})
	return o
}

// replyRequest is a Thread reply, a Reminder or a Storm summary: the Mentions and the text in one section, a
// Reminder's buttons in an `actions` block, no attachment, and `text` its first line with the Mentions.
func replyRequest(m messages.Message, l look, mentions []string) outgoing {
	o := request(l)
	line := strings.Join(fitMentions(mentions, bound{limit: LengthLimit / 2, size: utf8.RuneCountInString}), " ")
	body := bodyText(m, l, sectionBound.after(line))
	if text := joinLines(line, body); text != "" {
		o.Blocks = append(o.Blocks, section(blockText, text))
	}
	if a, ok := actionsBlock(m); ok {
		o.Blocks = append(o.Blocks, a)
	}
	first, _, _ := strings.Cut(body, "\n")
	o.Text = withMentions(cutText(first, bound{limit: textTitleLimit, size: utf8.RuneCountInString}), mentions)
	return o
}

// plainRequest is m as `text` alone (C-24.FR-8): the summary line of a Root message, the Mentions and the text of its
// sections, still escaped for mrkdwn — Slack reads `text` as mrkdwn, and it rejected only the blocks — within
// plainLimit bytes and under storedTextLimit in Slack's stored form; no blocks and no attachment.
func plainRequest(m messages.Message, l look, mentions []string) outgoing {
	o := request(l)
	var top string
	if m.Heading != nil {
		top = cutText(head(m, l, "\n", 0), bound{limit: plainLimit / 2, size: plainSize})
	}
	line := strings.Join(fitMentions(mentions, bound{limit: plainLimit / 2, size: plainSize}), " ")
	top = joinLines(top, line)
	o.Text = joinLines(top, bodyText(m, l, plainBound.after(top)))
	return o
}

// bound is a limit on a text and how a text is measured against it.
type bound struct {
	limit int
	size  func(string) int
}

// The bounds of a section (F-076) and of a message sent as text alone.
var (
	sectionBound = bound{limit: LengthLimit, size: utf8.RuneCountInString}
	plainBound   = bound{limit: plainLimit, size: plainSize}
)

// after is b for a text that follows prefix on a line of its own, in the same section.
func (b bound) after(prefix string) bound {
	if prefix == "" {
		return b
	}
	size := b.size
	return bound{limit: b.limit, size: func(s string) int { return size(joinLines(prefix, s)) }}
}

func (b bound) fits(s string) bool { return b.size(s) <= b.limit }

// summaryLine is the status emoji, #N and the title of m, escaped as alert data, the title cut to titleLimit code
// points first, or whole for 0.
func summaryLine(m messages.Message, titleLimit int) string {
	h := m.Heading
	title := templates.StripTokens(h.Title)
	if titleLimit > 0 {
		title = cutRunes(title, titleLimit)
	}
	return strings.TrimSpace(messages.StatusEmoji(m.Colour) + " #" + strconv.FormatInt(h.Number, 10) + " " +
		messages.EscapeMrkdwn(title))
}

// head is the summary line of m, after the mark of a test message and sep.
func head(m messages.Message, l look, sep string, titleLimit int) string {
	s := summaryLine(m, titleLimit)
	if l.mark != "" {
		s = l.mark + sep + s
	}
	return s
}

// topText is the top section of a Root message: the whole summary line and, on a Loud message, every Mention that
// fits beside the summary line with its title cut as in `text`; the title gives way first.
func topText(m messages.Message, l look, mentions []string) string {
	least := utf8.RuneCountInString(head(m, l, "\n", textTitleLimit))
	line := strings.Join(fitMentions(mentions, bound{limit: LengthLimit - least - 1,
		size: utf8.RuneCountInString}), " ")
	room := sectionBound
	if line != "" {
		room.limit -= utf8.RuneCountInString(line) + 1
	}
	return joinLines(cutText(head(m, l, "\n", 0), room), line)
}

// withMentions is a `text`: start and the Mentions that fit in textMentionsBytes after it, fewer while the whole stays
// at or above storedTextLimit in Slack's stored form.
func withMentions(start string, mentions []string) string {
	kept := fitMentions(mentions, bound{limit: textMentionsBytes, size: func(s string) int { return len(s) }})
	text := func() string {
		if len(kept) == 0 {
			return start
		}
		return start + " " + strings.Join(kept, " ")
	}
	stored := bound{limit: storedTextLimit - 1, size: storedBytes}
	for len(kept) > 0 && !stored.fits(text()) {
		kept = kept[:len(kept)-1]
	}
	return cutText(text(), stored)
}

// fitMentions are the first mentions, whole, whose line — joined by spaces — fits b.
func fitMentions(mentions []string, b bound) []string {
	var out []string
	for _, s := range mentions {
		if !b.fits(strings.Join(append(slices.Clone(out), s), " ")) {
			break
		}
		out = append(out, s)
	}
	return out
}

// headingText is the heading of a Root message (C-12.FR-1 item 2): #N and the title, in bold and linked to the Alert
// Group page, the title cut before it is escaped to the most code points with which the section fits.
func headingText(m messages.Message) string {
	title := templates.StripTokens(m.Heading.Title)
	text := func(n int) string {
		h := *m.Heading
		h.Title = cutRunes(title, n)
		return "*" + messages.Layout(messages.Message{Heading: &h}, messages.MarkupMrkdwn) + "*"
	}
	n := utf8.RuneCountInString(title)
	if sectionBound.fits(text(n)) {
		return text(n)
	}
	// The longest title that fits: its section never shrinks as the title grows.
	fit := 0
	for lo, hi := 1, n-1; lo <= hi; {
		mid := (lo + hi) / 2
		if sectionBound.fits(text(mid)) {
			fit, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	return text(fit)
}

// bodyText is items 3 to 11 of m in mrkdwn within b (C-12.FR-11): the body with its Alerts, its line of links, its
// notices and its footer, laid out by internal/messages without the heading and the buttons. The line of links, the
// notices and the footer stay whole; the rest is shortened by its Alert list first, losing whole sections and lines,
// so that no entity and no link is ever cut.
func bodyText(m messages.Message, l look, b bound) string {
	cp := laidOut(m, l)
	tail := messages.Layout(messages.Message{Language: cp.Language, Links: cp.Links, Notices: cp.Notices,
		Footer: cp.Footer, FooterMention: cp.FooterMention}, messages.MarkupMrkdwn)
	if !b.fits(tail) {
		return cutText(tail, b)
	}
	cp.Links, cp.Notices, cp.Footer, cp.FooterMention = nil, nil, "", ""
	// The heading is not in the body: shortening lays it out only so that the full list of Alerts links to the Alert
	// Group page, without the title, and its line does not count.
	extra := 0
	if cp.Heading != nil {
		extra = messages.Length(messages.Message{Heading: cp.Heading, Colour: cp.Colour}, messages.MarkupMrkdwn) + 1
	}
	budget := b.limit - b.size(tail) - 1
	for budget > 0 {
		short, _ := messages.Shorten(cp, budget+extra, messages.MarkupMrkdwn)
		short.Heading = nil
		text := joinLines(messages.Layout(short, messages.MarkupMrkdwn), tail)
		n := b.size(text)
		if n <= b.limit {
			return text
		}
		budget = min(budget*b.limit/n, budget-1)
	}
	return tail
}

// laidOut is m as its body lays it out for the Destination: without buttons, with the footer of its identity space,
// whose user with an Account link there is <@U…> (C-12.FR-12), the heading without its title, and its template output
// with its trusted Mention tokens as text that notifies nobody and its literal Mentions neutralized.
func laidOut(m messages.Message, l look) messages.Message {
	cp := m.WithFooterIn(l.space, userMention)
	cp.Buttons = nil
	if h := m.Heading; h != nil {
		cp.Heading = &messages.Heading{Number: h.Number, URL: h.URL}
	}
	if m.Body != nil {
		cp.Body = &messages.Body{Text: templateText(m.Body.Text), Markup: m.Body.Markup}
	}
	if m.Alerts != nil {
		a := *m.Alerts
		a.Lines = slices.Clone(a.Lines)
		for i, line := range a.Lines {
			if line.Markup != "" {
				a.Lines[i].Text = templateText(line.Text)
			}
		}
		cp.Alerts = &a
	}
	return cp
}

// templateText is template output as a Slack message carries it: its trusted Mention tokens as text, and its literal
// Mentions neutralized as the preview neutralizes them (messages.NeutralizeMrkdwnMentions).
func templateText(s string) string {
	return messages.NeutralizeMrkdwnMentions(templates.ReplaceTokens(s, tokenText))
}

// tokenText is a trusted Mention token as text that notifies nobody: `@`, a zero-width space and its word or group.
// A Loud message mentions everyone in the chat for the channel and here tokens in its Mentions instead.
func tokenText(tk templates.Token) string {
	if tk.Name == templates.MentionGroup {
		return "@" + zeroWidthSpace + messages.EscapeMrkdwn(tk.Group)
	}
	return "@" + zeroWidthSpace + tk.Name
}

// templateTokens are the trusted Mention tokens of the template output of m: its body and its Alert lines written by a
// line template.
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

// mentionText are the Mentions of a Loud message in mrkdwn, in order and without duplicates (C-24.FR-6): of the
// targets, everyone in the chat as <!channel> or <!here>, a User with an Account link for the Destination's Connection
// as <@U…> of the link's user id (F-097), and anyone else by their display name as text, escaped, which notifies
// nobody; then everyone of the template tokens. `all` and messenger groups, which a Slack Destination refuses,
// mention nobody.
func mentionText(ts []mentions.Target, tokens []templates.Token) []string {
	var out []string
	add := func(s string) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, t := range ts {
		switch t.Kind {
		case mentions.TargetEveryone:
			add(everyoneMentions[t.Everyone])
		case mentions.TargetUser:
			if t.User != nil {
				add(userMention(*t.User))
			}
		}
	}
	for _, tk := range tokens {
		add(everyoneMentions[tk.Name])
	}
	return out
}

// userMention is a User in mrkdwn: <@U…> of the user id of their Account link for the Destination's Connection
// (F-097), otherwise their display name as text, escaped, which notifies nobody (C-24.FR-6, C-12.FR-12).
func userMention(u mentions.User) string {
	if slackUserID.MatchString(u.ExternalID) {
		return "<@" + u.ExternalID + ">"
	}
	return messages.EscapeMrkdwn(messages.Value(strings.Join(strings.Fields(u.Name), " ")))
}

// actionsBlock is the `actions` block of the buttons of m (C-24.FR-5), false when it has none: each button with the
// action id of its Command and its signed action id as `value`, the Snooze buttons folded into one static_select menu
// where the first of them stood, its options the Snooze durations as a Text command writes them and then the Snooze
// presets by their labels. One button is `primary` — Ack, "Still on it", or Resolve when neither is there — and no
// button is `danger` (F-074). A button without a signed action id that fits valueLimit is left out.
func actionsBlock(m messages.Message) (block, bool) {
	usable := slices.DeleteFunc(slices.Clone(m.Buttons), func(b messages.Button) bool {
		return b.ActionID == "" || len(b.ActionID) > valueLimit || actionIDs[b.Command] == ""
	})
	primary := primaryIndex(usable)
	var elements []any
	var menu *staticSelect
	var durations, presets []option
	for i, b := range usable {
		if b.Command == buttons.CommandSnooze {
			if menu == nil {
				menu = &staticSelect{Type: typeStaticSelect, ActionID: actionIDs[b.Command],
					Placeholder: plainText(cutRunes(messages.T(m.Language, "button.snoozeMenu", nil), labelLimit))}
				elements = append(elements, menu)
			}
			o := option{Text: plainText(cutRunes(optionLabel(m, b), labelLimit)), Value: b.ActionID}
			if buttons.IsPreset(b.Argument) {
				presets = append(presets, o)
			} else {
				durations = append(durations, o)
			}
			continue
		}
		bt := button{Type: typeButton, ActionID: actionIDs[b.Command], Text: plainText(cutRunes(b.Label, labelLimit)),
			Value: b.ActionID}
		if i == primary {
			bt.Style = stylePrimary
		}
		elements = append(elements, bt)
	}
	if menu != nil {
		presets = presets[:min(len(presets), maxOptions)]
		menu.Options = slices.Concat(durations[:min(len(durations), maxOptions-len(presets))], presets)
	}
	if len(elements) == 0 {
		return block{}, false
	}
	return block{Type: typeActions, BlockID: blockActions, Elements: elements}, true
}

// primaryIndex is the index of the one `primary` button of bs: the first of the primary hint, otherwise the first of
// the success hint, as Resolve on an acknowledged Root message; -1 without either.
func primaryIndex(bs []messages.Button) int {
	for _, style := range []buttons.Style{buttons.StylePrimary, buttons.StyleSuccess} {
		if i := slices.IndexFunc(bs, func(b messages.Button) bool {
			return b.Command != buttons.CommandSnooze && b.Style() == style
		}); i >= 0 {
			return i
		}
	}
	return -1
}

// optionLabel is the text of the Snooze menu option of the Snooze button b: a duration as a Text command writes it —
// "1h", «1ч» — and a Snooze preset by its label.
func optionLabel(m messages.Message, b messages.Button) string {
	if !buttons.IsPreset(b.Argument) && b.Argument >= 0 && b.Argument < len(m.SnoozeSeconds) {
		return messages.CommandDuration(m.Language, m.SnoozeSeconds[b.Argument])
	}
	return b.Label
}

// joinLines joins the parts that are not empty with new lines.
func joinLines(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), "\n")
}

// cutRunes cuts s to at most n code points, ending it with "…" when it was cut.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string([]rune(s)[:n-1]) + ellipsis
}

// cutText cuts the mrkdwn s to fit b, ending it with "…" when it was cut: never inside an entity or `<…>`, nor
// between a character and the zero-width space after it, so that what stays shows as it did. A text that cannot fit
// even "…" is empty.
func cutText(s string, b bound) string {
	if b.fits(s) {
		return s
	}
	cuts := safeCuts(s)
	cut := func(i int) string { return strings.TrimRightFunc(s[:cuts[i]], unicode.IsSpace) + ellipsis }
	// The longest prefix that fits: the size of a prefix never shrinks as it grows.
	n := 0
	for lo, hi := 0, len(cuts)-1; lo <= hi; {
		mid := (lo + hi) / 2
		if b.fits(cut(mid)) {
			n, lo = mid+1, mid+1
		} else {
			hi = mid - 1
		}
	}
	if n == 0 {
		return ""
	}
	return cut(n - 1)
}

// safeCuts are the byte offsets where s may be cut, in order: the rune boundaries outside an entity and outside
// `<…>`, and not before a zero-width space.
func safeCuts(s string) []int {
	var out []int
	entity, angle := false, false
	for i, r := range s {
		if !entity && !angle && !strings.HasPrefix(s[i:], zeroWidthSpace) {
			out = append(out, i)
		}
		switch {
		case angle:
			angle = r != '>'
		case entity:
			entity = r != ';' && !unicode.IsSpace(r)
		case r == '<':
			angle = true
		case r == '&':
			entity = true
		}
	}
	return out
}

// storedBytes is the size of the mrkdwn s in Slack's stored form, from above (F-078, F-079): an entity as it is, a
// bare `&`, `<` or `>` as its entity, a letter, a digit, a mark, a punctuation mark or a space in UTF-8, and any other
// character — an emoji, a symbol, a format character — as the longest shortcode.
func storedBytes(s string) int {
	n := 0
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '&' && entityLen(s[i:]) > 0:
			w = entityLen(s[i:])
			n += w
		case r == '&':
			n += len("&amp;")
		case r == '<' || r == '>':
			n += len("&lt;")
		case r < utf8.RuneSelf || s[i:i+w] == zeroWidthSpace:
			n += w
		case unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.Z):
			n += w
		default:
			n += shortcodeBytes
		}
		i += w
	}
	return n
}

// entityLen is the length of the entity s starts with, `&amp;`, `&lt;` or `&gt;`; 0 when it starts with none.
func entityLen(s string) int {
	for _, e := range []string{"&amp;", "&lt;", "&gt;"} {
		if strings.HasPrefix(s, e) {
			return len(e)
		}
	}
	return 0
}

// plainSize measures a message sent as text alone against plainLimit: at most plainLimit exactly when it has at most
// plainLimit bytes and stays under storedTextLimit in Slack's stored form, which an edit needs (F-078).
func plainSize(s string) int {
	return max(len(s), storedBytes(s)-(storedTextLimit-plainLimit-1))
}
