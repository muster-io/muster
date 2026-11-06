// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
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
// The Mentions of a Loud message are its first line, with a Mention marker for each User linked through the
// Connection, listed in `mentions` (C-22.FR-16). The footer names a user linked through the Connection by a marker of
// their own in a new message for a bridge with `native_mentions`; an edit carries no Mentions, and any other bridge
// would show the marker's `@`, so there it names them by the link's display name (C-12.FR-12).
// Template output keeps its trusted Mention tokens as text that notifies nobody. A Root message and a test message of
// Muster's compact default content also carry `layout` (C-29.FR-8): the same content as plain-text data, shortened together with the body (C-29.FR-6),
// with the Owner of an acknowledged Alert Group, who has the `ref` of their entry in `mentions` only where the message
// carries their marker — a new message to a bridge with `native_mentions` — and null otherwise.

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

// markerScheme is the link scheme of a Mention marker, `[@Display Name](muster-mention:<ref>)`.
const markerScheme = "muster-mention:"

// content is the envelope of m for a bridge whose info is i, with head — the mention line of a Loud call, the mark of a
// test message — before its body, and for a Root message of the compact default content its layout, whose people have
// no reference; a Root message of a Route's own template or of the Fallback template has none (messages.HasLayout).
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
	var short messages.Message
	c.Body, short = fitted(rest, i, head)
	if messages.HasLayout(m) {
		l := layoutOf(m, short)
		c.Layout = &l
	}
	return c
}

// layoutOf is the layout of the Root message m (C-29.FR-8) whose body was laid out from short, m shortened for the
// body's limit: the description, the Alerts, the summary, the key labels, the notices and the footer of short, so that
// the layout and the body never disagree, and the rest of m — the status, the fields, the people and the links are
// never cut (C-29.FR-6) — with the title cut as the envelope's is. The people have no reference; values are plain text,
// and each link is its URL as a URI, with what RFC 3986 does not allow in it percent-encoded.
func layoutOf(m, short messages.Message) gen.Layout {
	cp := m
	cp.Description, cp.AlertsLine, cp.Summary, cp.KeyLabels = short.Description, short.AlertsLine, short.Summary,
		short.KeyLabels
	cp.Notices, cp.Footer, cp.FooterMention, cp.FooterName = short.Notices, short.Footer, short.FooterMention,
		short.FooterName
	l := messages.LayoutOf(cp)
	out := gen.Layout{Number: int(l.Number), Title: cutEnd(strings.TrimSpace(l.Title), maxTitle),
		Status:  gen.LayoutStatus{Key: gen.LayoutStatusKey(l.Status), Word: l.StatusWord},
		Summary: l.Summary, Description: l.Description, Fields: make([]gen.LayoutField, 0, len(l.Fields)),
		Labels: make([]gen.LayoutLabel, 0, len(l.Labels)), Alerts: nullable.NewNullNullable[gen.LayoutAlerts](),
		Notices: append([]string{}, l.Notices...), Assignees: []gen.LayoutPerson{},
		Owner: nullable.NewNullNullable[gen.LayoutPerson](), Links: make([]gen.LayoutLink, 0, len(l.Links))}
	for _, f := range l.Fields {
		out.Fields = append(out.Fields, gen.LayoutField{Name: f.Name, Value: f.Value})
	}
	for _, k := range l.Labels {
		out.Labels = append(out.Labels, gen.LayoutLabel{Key: k.Name, Value: k.Value})
	}
	if a := l.Alerts; a != nil {
		values := make([]gen.LayoutAlertValue, 0, len(a.Values))
		for _, v := range a.Values {
			values = append(values, gen.LayoutAlertValue{Value: v.Value, Resolved: v.Resolved})
		}
		out.Alerts = nullable.NewNullableWithValue(gen.LayoutAlerts{Label: a.Label, Values: values, Total: int(a.Total)})
	}
	if o := l.Owner; o != nil {
		out.Owner = nullable.NewNullableWithValue(gen.LayoutPerson{Name: o.Name, Ref: nullable.NewNullNullable[string]()})
	}
	for _, k := range l.Links {
		out.Links = append(out.Links, gen.LayoutLink{Label: k.Text, Url: uriOf(k.URL)})
	}
	return out
}

// uriOf is the link u as a URI (RFC 3986): its path and fragment percent-encoded as Go writes a URL, and every byte of
// its query that a URI does not allow percent-encoded too, so that a bridge that checks `format: uri` takes it. Its
// authority — userinfo, host and port — stays exactly as given, since a client resolves an internationalized host as
// written and not percent-encoded; a link without one, or that does not parse, is written as Go writes it, or stays as
// it is.
func uriOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(p.RawQuery) {
		if c := p.RawQuery[i]; inURIQuery(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	p.RawQuery = b.String()
	prefix := p.Scheme + "://"
	if p.Scheme == "" || p.Opaque != "" || !strings.HasPrefix(u, prefix) {
		return p.String()
	}
	rest := u[len(prefix):]
	authority := rest[:strings.IndexFunc(rest+"/", func(r rune) bool { return r == '/' || r == '?' || r == '#' })]
	out := prefix + authority + p.EscapedPath()
	if p.ForceQuery || p.RawQuery != "" {
		out += "?" + p.RawQuery
	}
	if p.Fragment != "" {
		out += "#" + p.EscapedFragment()
	}
	return out
}

// inURIQuery reports whether the byte c may stand in the query of a URI as it is (RFC 3986): an unreserved character,
// a sub-delimiter, `:`, `@`, `/`, `?`, or the `%` of a percent-encoding.
func inURIQuery(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
		strings.IndexByte("-._~!$&'()*+,;=:@/?%", c) >= 0
}

// withOwnerRef gives the Owner of layout the reference ref, the entry of the footer's user in `mentions`, when list —
// the Mentions the message carries — still has it (C-29.AC-6); a layout without an Owner and an empty ref stay as they
// are.
func withOwnerRef(layout *gen.Layout, ref string, list []gen.Mention) {
	if layout == nil || ref == "" || !slices.ContainsFunc(list, func(m gen.Mention) bool { return m.Ref == ref }) {
		return
	}
	o, err := layout.Owner.Get()
	if err != nil {
		return
	}
	o.Ref = nullable.NewNullableWithValue(ref)
	layout.Owner = nullable.NewNullableWithValue(o)
}

// postInput is the envelope c posted with postMessage as a message of kind to channel, without Mentions; notify is a
// Loud message.
func postInput(kind gen.MessageKind, channel string, c gen.MessageContent, notify bool) gen.MessageInput {
	return gen.MessageInput{Kind: kind, ChannelId: channel, Status: c.Status, Color: c.Color, Emoji: c.Emoji,
		Title: c.Title, Link: c.Link, Body: c.Body, Buttons: c.Buttons, Layout: c.Layout, Mentions: []gen.Mention{},
		Notify: notify}
}

// replyInput is the Thread reply m for a bridge whose info is i, under the Thread threadRef, null when it is empty,
// with head — its Mention line — before its body and the Mentions its markers name; notify is a Loud reply.
func replyInput(threadRef string, m messages.Message, i Info, head []string, list []gen.Mention,
	notify bool) gen.ThreadReplyInput {
	ref := nullable.NewNullNullable[string]()
	if threadRef != "" {
		ref = nullable.NewNullableWithValue(threadRef)
	}
	if list == nil {
		list = []gen.Mention{}
	}
	return gen.ThreadReplyInput{ThreadRef: ref, Body: bodyOf(m, i, head), Buttons: buttonsOf(m, i),
		Mentions: list, Notify: notify}
}

// mentionsOf are the Mentions of a Loud call (C-22.FR-16) as the line that starts the body and the envelope's
// `mentions`: a User with an Account link for the Destination's Connection is a marker `[@name](muster-mention:mN)`,
// whose entry carries the link's external id and display name — the Muster display name when the link has none — and
// any other User is their display name as text, which mentions nobody; in the order of the targets, each once. A Quiet
// call has neither, and the list is never nil.
func mentionsOf(c delivery.Call) ([]string, []gen.Mention) {
	list := []gen.Mention{}
	if c.Loudness != groups.Loud {
		return nil, list
	}
	var names []string
	for _, t := range c.Targets {
		if t.Kind != mentions.TargetUser || t.User == nil {
			continue
		}
		u := *t.User
		if !markable(u) {
			if n := messages.EscapeCommonMark(messages.Value(oneLine(u.Name))); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
			continue
		}
		if slices.ContainsFunc(list, func(m gen.Mention) bool { return m.ExternalId == u.ExternalID }) {
			continue
		}
		var n string
		n, list = marker(u, list)
		names = append(names, n)
	}
	if len(names) == 0 {
		return nil, list
	}
	return []string{strings.Join(names, " ")}, list
}

// markerName is the display name of a User's marker and entry: the link's display name, else the Muster display
// name, on one line and cut to maxDisplayName.
func markerName(u mentions.User) string {
	return cutEnd(cmp.Or(oneLine(u.DisplayName), oneLine(u.Name)), maxDisplayName)
}

// markable reports whether a User has a marker: a link's external id within maxID and a display name.
func markable(u mentions.User) bool {
	return u.ExternalID != "" && utf8.RuneCountInString(u.ExternalID) <= maxID && markerName(u) != ""
}

// marker is the marker `[@name](muster-mention:mN)` of a markable User and the Mentions with their entry: the entry of
// the same account when list has one, otherwise a new one at its end.
func marker(u mentions.User, list []gen.Mention) (string, []gen.Mention) {
	name := markerName(u)
	n := slices.IndexFunc(list, func(m gen.Mention) bool { return m.ExternalId == u.ExternalID })
	if n < 0 {
		list = append(list, gen.Mention{Ref: "m" + strconv.Itoa(len(list)+1), ExternalId: u.ExternalID,
			DisplayName: name})
		n = len(list) - 1
	}
	return "[@" + messages.EscapeCommonMark(messages.Value(name)) + "](" + markerScheme + list[n].Ref + ")", list
}

// footerName is the user of a footer as text, which mentions nobody: the display name of their Account link, else its
// username, else their Muster display name, escaped.
func footerName(u mentions.User) string {
	return messages.EscapeCommonMark(messages.Value(cmp.Or(u.DisplayName, u.Username, oneLine(u.Name))))
}

// oneLine is s on one line: every run of white space and control characters a single space, trimmed.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }),
		" ")
}

// withFooterIn is m with the footer of the identity space of the Bridge Connection conn, which names the user by the
// display name of their Account link there (C-22.FR-16), otherwise by the Muster display name.
func withFooterIn(m messages.Message, conn int64) messages.Message {
	return m.WithFooterIn(mentions.IdentitySpace(mentions.TypeBridge, &conn), footerName)
}

// withFooterMention is m, a new message for a bridge whose info is i, with the footer of the identity space of the
// Bridge Connection conn and the Mentions list (C-12.FR-12, C-22.FR-16), and the ref of the footer user's entry: with
// `native_mentions`, a user linked there is their marker, whose entry list gains unless it has one for the account;
// otherwise withFooterIn names them, and the ref is empty.
func withFooterMention(m messages.Message, conn int64, i Info, list []gen.Mention) (messages.Message, []gen.Mention,
	string) {
	if c := i.Capabilities.NativeMentions; c == nil || !*c {
		return withFooterIn(m, conn), list, ""
	}
	ref := ""
	m = m.WithFooterIn(mentions.IdentitySpace(mentions.TypeBridge, &conn), func(u mentions.User) string {
		if !markable(u) {
			return footerName(u)
		}
		var s string
		s, list = marker(u, list)
		ref = list[slices.IndexFunc(list, func(e gen.Mention) bool { return e.ExternalId == u.ExternalID })].Ref
		return s
	})
	return m, list, ref
}

// marked are the Mentions of list whose marker body still carries: shortening may have cut the footer's (C-12.FR-11),
// and an entry names a marker. The list is never nil.
func marked(list []gen.Mention, body string) []gen.Mention {
	return slices.DeleteFunc(slices.Clone(list), func(m gen.Mention) bool {
		return !strings.Contains(body, "]("+markerScheme+m.Ref+")")
	})
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
	body, _ := fitted(m, i, head)
	return body
}

// fitted is the body of bodyOf and m as shortened for it (C-12.FR-11, C-29.FR-6), which the layout follows.
func fitted(m messages.Message, i Info, head []string) (string, messages.Message) {
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
	text, short := fit(laidOut(m), max(room, 0))
	parts := append(slices.Clone(head), text, line)
	parts = slices.DeleteFunc(parts, func(s string) bool { return s == "" })
	return cutEnd(strings.Join(parts, blockBreak), limit), short
}

// laidOut is m as the body lays it out: without buttons, with the footer withFooterIn chose, and the trusted Mention
// tokens of its template output as text that notifies nobody. Its heading stays, so that shortening links the full
// list of Alerts to the Alert Group page.
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
// that is not enough, its line of links, notices and footer kept whole after the start of the rest, cut. It also
// returns m as shortened.
func fit(m messages.Message, room int) (string, messages.Message) {
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
		return full, short
	}
	tail := messages.Layout(messages.Message{Links: short.Links, Notices: short.Notices, Footer: short.Footer,
		FooterMention: short.FooterMention}, messages.MarkupCommonMark)
	rest := short
	rest.Links, rest.Notices, rest.Footer, rest.FooterMention = nil, nil, "", ""
	return cutMiddle(messages.Layout(rest, messages.MarkupCommonMark), tail, room), short
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
// the Snooze buttons go first, in the order of droppedSnooze, then buttons from the end.
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
		n := droppedSnooze(kept, m.SnoozeSeconds)
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

// droppedSnooze is the index in bs of the Snooze button that goes first beyond `buttons.max_per_message`, whatever the
// length of the labels: the week_start preset, then the tomorrow preset, then the longest duration (longestSnooze);
// -1 without a Snooze button.
func droppedSnooze(bs []messages.Button, seconds []int64) int {
	for _, argument := range []int{buttons.ArgumentWeekStart, buttons.ArgumentTomorrow} {
		if n := slices.IndexFunc(bs, func(b messages.Button) bool {
			return b.Command == buttons.CommandSnooze && b.Argument == argument
		}); n >= 0 {
			return n
		}
	}
	return longestSnooze(bs, seconds)
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
