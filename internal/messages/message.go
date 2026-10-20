// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package messages renders what Muster sends to messengers (C-12): the markup-neutral Root message of C-12.FR-1 with
// its default content, the Thread replies, the Storm summary and the notices, in English or Russian per
// route.language and with absolute times in organization.time_zone; Route templates run in the sandbox of ADR-0012
// with a dry run on save and a preview, and the Fallback template stands in for a template that fails at runtime.
// It never sends: delivery stores what it renders as the Desired state and the adapters lay it out (ADR-0005).
package messages

import (
	"cmp"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/templates"
)

// Markup is the markup an adapter writes, which template output is written in and alert data are escaped for.
type Markup string

// The markups of C-12.FR-7: Mattermost Markdown, Telegram HTML, CommonMark for bridges, Slack's mrkdwn, and plain text
// for outgoing webhooks and the stored text of a Desired state.
const (
	MarkupMarkdown Markup = "markdown"
	MarkupHTML     Markup = "html"
	MarkupPlain    Markup = "plain"
	// MarkupCommonMark is CommonMark 0.31 with strikethrough, a bridge's envelope body (C-22.FR-4).
	MarkupCommonMark Markup = "commonmark"
	// MarkupMrkdwn is Slack's mrkdwn, the text of a Slack message's sections (C-24.FR-4, FR-7).
	MarkupMrkdwn Markup = "mrkdwn"
)

// Kind is what a message is.
type Kind string

// The kinds of messages.
const (
	KindRoot  Kind = "root"
	KindReply Kind = "reply"
	KindStorm Kind = "storm_summary"
)

// The colours of a message: the status of its Alert Group, or a Storm.
const (
	ColourFiring       = "firing"
	ColourAcknowledged = "acknowledged"
	ColourSnoozed      = "snoozed"
	ColourResolved     = "resolved"
	ColourStorm        = "storm"
)

// Message is a rendered message, neutral of any markup, in the sections of C-12.FR-1: the colour of its status, the
// heading with #N and the title linked to the Alert Group page, the environment and start time, the group labels,
// common labels and common annotations, the summary, the Alerts, the links, the notices, the footer and the buttons.
// A Route's root_message template replaces the sections from Environment to Alerts with its Body; Lines are the text
// of a Thread reply, a Storm summary or the Fallback template. Each adapter lays a Message out for its messenger and
// escapes its neutral text (C-13.FR-8, C-14.FR-13); a Body and the template lines of Alerts are already written in
// their Markup, where a trusted Mention token of the `mention` function stands for the adapter to render
// (C-12.FR-8). Footers are the footer per identity space whose username names the user (C-12.FR-12). Language and
// TimeZone are those it was rendered in, for the notes delivery adds at call time. SnoozeSeconds are the Route's
// Snooze durations, which its Snooze buttons name by index, set only in CommonMark and mrkdwn: a bridge writes the
// first in the commands line and drops Snooze buttons from the longest (C-22.FR-5), Slack's Snooze menu names each as
// its option (C-24.FR-5), and the Desired states of the other markups keep their hashes.
type Message struct {
	Kind              Kind              `json:"kind"`
	Language          string            `json:"language"`
	TimeZone          string            `json:"time_zone"`
	Colour            string            `json:"colour"`
	Heading           *Heading          `json:"heading,omitempty"`
	Environment       string            `json:"environment,omitempty"`
	GroupLabels       []Label           `json:"group_labels,omitempty"`
	CommonLabels      []Label           `json:"common_labels,omitempty"`
	CommonAnnotations []Label           `json:"common_annotations,omitempty"`
	Summary           string            `json:"summary,omitempty"`
	Body              *Body             `json:"body,omitempty"`
	Alerts            *AlertList        `json:"alerts,omitempty"`
	Lines             []string          `json:"lines,omitempty"`
	Links             []Link            `json:"links,omitempty"`
	Notices           []string          `json:"notices,omitempty"`
	Footer            string            `json:"footer,omitempty"`
	Footers           map[string]string `json:"footers,omitempty"`
	Buttons           []Button          `json:"buttons"`
	SnoozeSeconds     []int64           `json:"snooze_seconds,omitempty"`
}

// FooterIn is the footer in an identity space — "telegram" or "mattermost:<connection>" — naming the user by the
// username of their Account link there, otherwise by the Muster display name (C-12.FR-12).
func (m Message) FooterIn(space string) string {
	if f, ok := m.Footers[space]; ok {
		return f
	}
	return m.Footer
}

// Heading is #N and the title of the Alert Group, linked to its page.
type Heading struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// Label is a label or an annotation.
type Label struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Body is the output of a Route's root_message template, in the markup it was rendered for.
type Body struct {
	Text   string `json:"text"`
	Markup Markup `json:"markup"`
}

// AlertList is the Alerts section: one line per Alert up to message.alerts_listed, or above it the distinct values of
// each label that differs between the Alerts and a link to the full list. With lines, the distinct values are kept
// for shortening and not shown.
type AlertList struct {
	Lines    []AlertLine `json:"lines,omitempty"`
	Distinct []Distinct  `json:"distinct,omitempty"`
	FullList *Link       `json:"full_list,omitempty"`
}

// AlertLine is the line of one Alert; Markup is set for the output of a Route's line template.
type AlertLine struct {
	Text     string `json:"text"`
	Resolved bool   `json:"resolved,omitempty"`
	Markup   Markup `json:"markup,omitempty"`
}

// Distinct is the distinct values of a label that differs between the Alerts, at most the per-label limit, and how
// many more there are.
type Distinct struct {
	Label  string   `json:"label"`
	Values []string `json:"values"`
	More   int      `json:"more,omitempty"`
}

// Link is a link with its text.
type Link struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// Button is a Command button: the Command, its argument (the index of a Snooze duration), its label and the signed
// action id with the full id of the key that signed it (C-12.FR-1, internal/buttons).
type Button struct {
	Command  string `json:"command"`
	Argument int    `json:"argument,omitempty"`
	Label    string `json:"label"`
	ActionID string `json:"action_id,omitempty"`
	KeyID    string `json:"key_id,omitempty"`
}

// Style is the style hint of the button, decided by its Command. It is not a field: the JSON of a Message is the
// Desired state's payload and the input of its hash, which the style must not change.
func (b Button) Style() buttons.Style {
	return buttons.StyleOf(b.Command)
}

// Text is the message laid out as plain text: the stored text of a Desired state.
func (m Message) Text() string {
	return Layout(m, MarkupPlain)
}

// statusEmoji is the colour of item 1 as an emoji (C-12.FR-1).
var statusEmoji = map[string]string{
	ColourFiring: "🔴", ColourAcknowledged: "🟠", ColourResolved: "🟢", ColourSnoozed: "⚪", ColourStorm: "⛈",
}

// statusColours are the colour of item 1 as RGB (C-12.FR-1), which a Mattermost attachment and a bridge's envelope
// show.
var statusColours = map[string]string{
	ColourFiring: "#d32f2f", ColourAcknowledged: "#f57c00", ColourResolved: "#388e3c", ColourSnoozed: "#9e9e9e",
	ColourStorm: "#6a1b9a",
}

// StatusColour is the colour of a message as RGB, "#d32f2f" for firing; empty for an unknown one.
func StatusColour(colour string) string {
	return statusColours[colour]
}

// StatusEmoji is the colour of a message as an emoji, 🔴 for firing; empty for an unknown one.
func StatusEmoji(colour string) string {
	return statusEmoji[colour]
}

// Layout lays m out as text in a markup, as the preview shows it: one section per line, neutral text escaped for the
// markup and without Mention tokens, template output as written with its tokens shown as `@` names, and the buttons
// as a last line of bracketed labels. The adapters lay messages out for their messengers, a Slack message's body
// section with this layout in mrkdwn. CommonMark has a layout of its own, which a bridge's envelope body takes.
func Layout(m Message, markup Markup) string {
	if markup == MarkupCommonMark {
		return layoutCommonMark(m)
	}
	escape := Escaper(markup)
	// Only template output may carry a Mention token; neutral text never shows one.
	esc := func(s string) string { return escape(templates.StripTokens(s)) }
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	if h := m.Heading; h != nil {
		title := fmt.Sprintf("#%d %s", h.Number, h.Title)
		add(strings.TrimSpace(statusEmoji[m.Colour] + " " + link(markup, title, h.URL)))
	}
	add(esc(m.Environment))
	for _, section := range [][]Label{m.GroupLabels, m.CommonLabels, m.CommonAnnotations} {
		for _, l := range section {
			add(esc(l.Name + ": " + l.Value))
		}
	}
	if m.Summary != "" {
		add(italic(markup, esc(m.Summary)))
	}
	if m.Body != nil {
		add(templates.ReplaceTokens(strings.TrimRight(m.Body.Text, "\n"), tokenText))
	}
	if a := m.Alerts; a != nil {
		for _, l := range a.Lines {
			text := templates.ReplaceTokens(l.Text, tokenText)
			if l.Markup == "" {
				text = esc(l.Text)
			}
			if l.Resolved {
				text = struck(markup, text)
			}
			add(bullet(markup) + text)
		}
		for _, d := range a.Distinct {
			if len(a.Lines) > 0 {
				break // kept for shortening only
			}
			line := d.Label + ": " + strings.Join(d.Values, ", ")
			if d.More > 0 {
				line += " " + T(m.Language, "alerts.more", Args{"count": strconv.Itoa(d.More)})
			}
			add(esc(line))
		}
		if a.FullList != nil {
			add(link(markup, a.FullList.Text, a.FullList.URL))
		}
	}
	for _, l := range m.Lines {
		add(esc(l))
	}
	if len(m.Links) > 0 {
		links := make([]string, len(m.Links))
		for i, l := range m.Links {
			links[i] = link(markup, l.Text, l.URL)
		}
		add(strings.Join(links, " · "))
	}
	for _, n := range m.Notices {
		add(esc(n))
	}
	add(esc(m.Footer))
	if len(m.Buttons) > 0 {
		labels := make([]string, len(m.Buttons))
		for i, b := range m.Buttons {
			labels[i] = "[" + esc(b.Label) + "]"
		}
		add(strings.Join(labels, " "))
	}
	return strings.Join(out, "\n")
}

// tokenText is a trusted Mention token as the preview and the plain text show it: `@` and its word or group, the
// Owner as @owner; each adapter writes its own syntax instead (S-061, S-042).
func tokenText(tk templates.Token) string {
	if tk.Name == templates.MentionGroup {
		return "@" + tk.Group
	}
	return "@" + tk.Name
}

// markdownURL escapes the characters that end or break the target of a Markdown link.
var markdownURL = strings.NewReplacer("(", "%28", ")", "%29", "<", "%3C", ">", "%3E", "\\", "%5C")

// commonMarkURL escapes the characters that end or break the destination of a CommonMark link, a space included.
var commonMarkURL = strings.NewReplacer("(", "%28", ")", "%29", "<", "%3C", ">", "%3E", "\\", "%5C", " ", "%20")

// mrkdwnURL escapes the characters that end or break the URL of a mrkdwn link, `<url|text>`: the control characters
// of mrkdwn, the bar that starts its text and a space; `&` becomes the entity Slack reads back as `&`.
var mrkdwnURL = strings.NewReplacer("&", "&amp;", "<", "%3C", ">", "%3E", "|", "%7C", " ", "%20")

func link(markup Markup, text, url string) string {
	esc := Escaper(markup)
	if url == "" {
		return esc(text)
	}
	switch markup {
	case MarkupMarkdown:
		return "[" + esc(text) + "](" + markdownURL.Replace(url) + ")"
	case MarkupHTML:
		return `<a href="` + esc(url) + `">` + esc(text) + "</a>"
	case MarkupCommonMark:
		return commonMarkLink(esc(text), url)
	case MarkupMrkdwn:
		return "<" + mrkdwnURL.Replace(url) + "|" + esc(text) + ">"
	case MarkupPlain:
	}
	return text + " " + url
}

// commonMarkLink is a CommonMark link to url whose text is already escaped; text alone without a url.
func commonMarkLink(text, url string) string {
	if url == "" {
		return text
	}
	return "[" + text + "](" + commonMarkURL.Replace(url) + ")"
}

// italic and struck trim s in CommonMark and mrkdwn, where a delimiter next to a space opens or closes nothing.
func italic(markup Markup, s string) string {
	switch markup {
	case MarkupMarkdown:
		return "_" + s + "_"
	case MarkupHTML:
		return "<i>" + s + "</i>"
	case MarkupCommonMark:
		return around("*", strings.TrimSpace(s))
	case MarkupMrkdwn:
		return around("_", strings.TrimSpace(s))
	case MarkupPlain:
	}
	return s
}

func struck(markup Markup, s string) string {
	switch markup {
	case MarkupMarkdown:
		return "~~" + s + "~~"
	case MarkupHTML:
		return "<s>" + s + "</s>"
	case MarkupCommonMark:
		return around("~~", strings.TrimSpace(s))
	case MarkupMrkdwn:
		return around("~", strings.TrimSpace(s))
	case MarkupPlain:
	}
	return s
}

// around puts a delimiter around s, which stays empty when it is.
func around(delimiter, s string) string {
	if s == "" {
		return ""
	}
	return delimiter + s + delimiter
}

func bullet(markup Markup) string {
	if markup == MarkupMarkdown || markup == MarkupCommonMark {
		return "- "
	}
	return "• "
}

// hardBreak ends a line inside a CommonMark paragraph or block quote.
const hardBreak = "\\\n"

// layoutCommonMark lays m out in CommonMark with strikethrough, as a bridge's envelope body (C-22.FR-4): one block per
// section, separated by blank lines — the heading, the environment, each label section as a block quote of bold names
// and their values, the summary in italics, the body, the Alerts as a bullet list with resolved ones struck through,
// the distinct values, the link to the full list, the text lines, the links, each notice, the footer and the buttons —
// with the lines of a block joined by hard line breaks. Neutral text is escaped and template output kept as written.
func layoutCommonMark(m Message) string {
	esc := func(s string) string { return EscapeCommonMark(templates.StripTokens(s)) }
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	// lines joins the lines that are not empty with hard line breaks, each after prefix.
	lines := func(prefix string, ls []string) string {
		ls = slices.DeleteFunc(slices.Clone(ls), func(s string) bool { return s == "" })
		if len(ls) == 0 {
			return ""
		}
		return prefix + strings.Join(ls, hardBreak+prefix)
	}
	if h := m.Heading; h != nil {
		title := "#" + strconv.FormatInt(h.Number, 10) + " " + esc(h.Title)
		add(strings.TrimSpace(statusEmoji[m.Colour] + " " + commonMarkLink(title, h.URL)))
	}
	add(esc(m.Environment))
	for _, section := range [][]Label{m.GroupLabels, m.CommonLabels, m.CommonAnnotations} {
		quoted := make([]string, len(section))
		for i, l := range section {
			quoted[i] = strings.TrimSpace(around("**", esc(l.Name)) + " " + esc(l.Value))
		}
		add(lines("> ", quoted))
	}
	add(italic(MarkupCommonMark, esc(m.Summary)))
	if m.Body != nil {
		add(templates.ReplaceTokens(strings.TrimRight(m.Body.Text, "\n"), tokenText))
	}
	if a := m.Alerts; a != nil {
		items := make([]string, 0, len(a.Lines))
		for _, l := range a.Lines {
			text := templates.ReplaceTokens(l.Text, tokenText)
			if l.Markup == "" {
				text = esc(l.Text)
			}
			if l.Resolved {
				text = struck(MarkupCommonMark, text)
			}
			if text != "" {
				items = append(items, bullet(MarkupCommonMark)+text)
			}
		}
		add(strings.Join(items, "\n"))
		if len(a.Lines) == 0 {
			distinct := make([]string, len(a.Distinct))
			for i, d := range a.Distinct {
				line := d.Label + ": " + strings.Join(d.Values, ", ")
				if d.More > 0 {
					line += " " + T(m.Language, "alerts.more", Args{"count": strconv.Itoa(d.More)})
				}
				distinct[i] = esc(line)
			}
			add(lines("", distinct))
		}
		if a.FullList != nil {
			add(link(MarkupCommonMark, a.FullList.Text, a.FullList.URL))
		}
	}
	text := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		text[i] = esc(l)
	}
	add(lines("", text))
	links := make([]string, len(m.Links))
	for i, l := range m.Links {
		links[i] = link(MarkupCommonMark, l.Text, l.URL)
	}
	add(strings.Join(links, " · "))
	for _, n := range m.Notices {
		add(esc(n))
	}
	add(esc(m.Footer))
	labels := make([]string, len(m.Buttons))
	for i, b := range m.Buttons {
		labels[i] = `\[` + esc(b.Label) + `\]`
	}
	add(strings.Join(labels, " "))
	return strings.Join(out, "\n\n")
}

// CommandsLine is the line that ends the body of a Root message for a bridge that forwards Thread messages and has no
// buttons (C-22.FR-5): the Text commands of its buttons in its language, the first Snooze duration only — "Reply in
// the Thread: ack · resolve · snooze 1h" — or with mentionsOnly, for a bridge that forwards only messages that mention
// the bot, "Reply in the Thread, mentioning the bot: …". A message without buttons, a resolved one, has none.
func CommandsLine(m Message, mentionsOnly bool) string {
	var words []string
	snooze := false
	for _, b := range m.Buttons {
		if b.Command != buttons.CommandSnooze {
			key := map[string]string{buttons.CommandStillOnIt: "stillOnIt"}[b.Command]
			words = append(words, T(m.Language, "textCommand."+cmp.Or(key, b.Command), nil))
			continue
		}
		if !snooze && b.Argument < len(m.SnoozeSeconds) {
			words = append(words, T(m.Language, "textCommand.snooze",
				Args{"duration": commandDuration(m.Language, m.SnoozeSeconds[b.Argument])}))
		}
		snooze = true
	}
	if len(words) == 0 {
		return ""
	}
	key := "bridge.commands"
	if mentionsOnly {
		key = "bridge.commandsMentioning"
	}
	return T(m.Language, key, Args{"commands": strings.Join(words, " · ")})
}

// CommandDuration is a Snooze duration in lang as a Text command writes it (C-22.FR-14), as the commands line of a
// Root message shows it: the answer to a Snooze for a duration the Route does not have names the Route's this way.
func CommandDuration(lang string, seconds int64) string { return commandDuration(lang, seconds) }

// commandDuration is a Snooze duration as a Text command writes it (C-22.FR-14): days above a day, then hours, or
// minutes rounded up — "1h", "24h", "2d", "30m"; "1ч" in Russian.
func commandDuration(lang string, seconds int64) string {
	switch {
	case seconds > 86400 && seconds%86400 == 0:
		return T(lang, "textCommand.days", Args{"count": strconv.FormatInt(seconds/86400, 10)})
	case seconds >= 3600 && seconds%3600 == 0:
		return T(lang, "textCommand.hours", Args{"count": strconv.FormatInt(seconds/3600, 10)})
	}
	return T(lang, "textCommand.minutes", Args{"count": strconv.FormatInt((seconds+59)/60, 10)})
}

// The languages of messages (route.language); English serves any other.
const (
	LanguageEnglish = "en"
	LanguageRussian = "ru"
)

// Languages are the languages built-in texts exist in.
var Languages = []string{LanguageEnglish, LanguageRussian}

//go:embed texts/*.json
var textFiles embed.FS

// texts are the built-in texts by language and key.
var texts = loadTexts()

func loadTexts() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, lang := range Languages {
		b, err := textFiles.ReadFile("texts/" + lang + ".json")
		if err != nil {
			panic(fmt.Sprintf("messages: read the %s texts: %v", lang, err))
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			panic(fmt.Sprintf("messages: parse the %s texts: %v", lang, err))
		}
		out[lang] = m
	}
	return out
}

// Args are the values of the placeholders of a text, `{name}`.
type Args map[string]string

// language is lang when built-in texts exist in it, English otherwise.
func language(lang string) string {
	if _, ok := texts[lang]; ok {
		return lang
	}
	return LanguageEnglish
}

// T is the built-in text key in lang with its placeholders filled; a key missing in lang falls back to English, and
// one missing in English is the key itself.
func T(lang, key string, args Args) string {
	s, ok := texts[language(lang)][key]
	if !ok {
		if s, ok = texts[LanguageEnglish][key]; !ok {
			s = key
		}
	}
	// One pass, so that a value that holds a placeholder is never filled in turn.
	pairs := make([]string, 0, 2*len(args))
	for name, v := range args {
		pairs = append(pairs, "{"+name+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// N is the plural form of the text key for count in lang — key_one, key_few, key_many or key_other by the language's
// rules — with {count} and the other placeholders filled.
func N(lang, key string, count int64, args Args) string {
	lang = language(lang)
	full := Args{"count": strconv.FormatInt(count, 10)}
	for k, v := range args {
		full[k] = v
	}
	for _, form := range []string{key + "_" + pluralForm(lang, count), key + "_other"} {
		if _, ok := texts[lang][form]; ok {
			return T(lang, form, full)
		}
	}
	return T(lang, key, full)
}

// pluralForm is the CLDR plural category of an integer count: one and other in English; one, few and many in
// Russian.
func pluralForm(lang string, n int64) string {
	if n < 0 {
		n = -n
	}
	if lang == LanguageRussian {
		switch mod10, mod100 := n%10, n%100; {
		case mod10 == 1 && mod100 != 11:
			return "one"
		case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
			return "few"
		default:
			return "many"
		}
	}
	if n == 1 {
		return "one"
	}
	return "other"
}

// Times in messages are absolute in organization.time_zone (C-12.FR-3): the full form for the start and the footer,
// HH:MM inside notes.
const (
	fullTime = "2006-01-02 15:04 MST"
	noteTime = "15:04"
)

// location is the time zone name, UTC when it is empty or unknown.
func location(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil && name != "" {
		return loc
	}
	return time.UTC
}

// FullTime is t in the time zone tz as messages show a start time: 2026-10-04 14:05 UTC.
func FullTime(t time.Time, tz string) string {
	return t.In(location(tz)).Format(fullTime)
}

// NoteTime is t in the time zone tz as notes show it: 14:05.
func NoteTime(t time.Time, tz string) string {
	return t.In(location(tz)).Format(noteTime)
}

// Duration is a Snooze duration as a button shows it: hours up to a day, then days; minutes or seconds below an
// hour.
func Duration(lang string, seconds int64) string {
	switch {
	case seconds > 86400 && seconds%86400 == 0:
		return T(lang, "duration.days", Args{"count": strconv.FormatInt(seconds/86400, 10)})
	case seconds >= 3600 && seconds%3600 == 0:
		return T(lang, "duration.hours", Args{"count": strconv.FormatInt(seconds/3600, 10)})
	case seconds >= 60 && seconds%60 == 0:
		return T(lang, "duration.minutes", Args{"count": strconv.FormatInt(seconds/60, 10)})
	}
	return T(lang, "duration.seconds", Args{"count": strconv.FormatInt(seconds, 10)})
}

// The template kinds of the metrics and of the template error state (C-12.FR-13).
const (
	TemplateRootMessage      = "root_message"
	TemplateLine             = "line"
	TemplateAckTimeoutNotice = "ack_timeout_notice"
	TemplateLinkRule         = "link_rule"
	TemplateWebhookRequest   = "webhook_request"
)

// TemplateKinds is the closed set of the label template.
var TemplateKinds = []string{TemplateRootMessage, TemplateLine, TemplateAckTimeoutNotice, TemplateLinkRule,
	TemplateWebhookRequest}

// sandboxError is err as a template error, for the code and position of a failure.
func sandboxError(err error) *templates.Error {
	if e, ok := err.(*templates.Error); ok { //nolint:errorlint // the sandbox returns *Error itself, never wrapped
		return e
	}
	return &templates.Error{Code: templates.CodeSyntax, Detail: err.Error()}
}
