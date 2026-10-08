// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package messages renders what Muster sends to messengers (C-12): the markup-neutral Root message of C-12.FR-1 with
// its default content, the Thread replies, the Storm summary and the notices, in English or Russian per
// route.language and with absolute times in organization.time_zone; Route templates run in the sandbox of ADR-0012
// with a dry run on save and a preview, and the Fallback template stands in for a template that fails at runtime.
// It never sends: delivery stores what it renders as the Desired state and the adapters lay it out (ADR-0005).
package messages

import (
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/templates"
)

// Markup is the markup an adapter writes, which template output is written in and alert data are escaped for.
type Markup string

// The markups of C-12.FR-7: Mattermost Markdown, Telegram HTML, and plain text for outgoing webhooks and the stored
// text of a Desired state.
const (
	MarkupMarkdown Markup = "markdown"
	MarkupHTML     Markup = "html"
	MarkupPlain    Markup = "plain"
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
// their Markup. Language and TimeZone are those it was rendered in, for the notes delivery adds at call time.
type Message struct {
	Kind              Kind       `json:"kind"`
	Language          string     `json:"language"`
	TimeZone          string     `json:"time_zone"`
	Colour            string     `json:"colour"`
	Heading           *Heading   `json:"heading,omitempty"`
	Environment       string     `json:"environment,omitempty"`
	GroupLabels       []Label    `json:"group_labels,omitempty"`
	CommonLabels      []Label    `json:"common_labels,omitempty"`
	CommonAnnotations []Label    `json:"common_annotations,omitempty"`
	Summary           string     `json:"summary,omitempty"`
	Body              *Body      `json:"body,omitempty"`
	Alerts            *AlertList `json:"alerts,omitempty"`
	Lines             []string   `json:"lines,omitempty"`
	Links             []Link     `json:"links,omitempty"`
	Notices           []string   `json:"notices,omitempty"`
	Footer            string     `json:"footer,omitempty"`
	Buttons           []Button   `json:"buttons"`
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

// Text is the message laid out as plain text: the stored text of a Desired state.
func (m Message) Text() string {
	return Layout(m, MarkupPlain)
}

// statusEmoji is the colour of item 1 as an emoji (C-12.FR-1).
var statusEmoji = map[string]string{
	ColourFiring: "🔴", ColourAcknowledged: "🟠", ColourResolved: "🟢", ColourSnoozed: "⚪", ColourStorm: "⛈",
}

// Layout lays m out as text in a markup, as the preview shows it: one section per line, neutral text escaped for the
// markup, template output as written, and the buttons as a last line of bracketed labels. The adapters of S-061 and
// S-042 lay messages out for their messengers.
func Layout(m Message, markup Markup) string {
	esc := Escaper(markup)
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
		add(strings.TrimRight(m.Body.Text, "\n"))
	}
	if a := m.Alerts; a != nil {
		for _, l := range a.Lines {
			text := l.Text
			if l.Markup == "" {
				text = esc(text)
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

func link(markup Markup, text, url string) string {
	esc := Escaper(markup)
	if url == "" {
		return esc(text)
	}
	switch markup {
	case MarkupMarkdown:
		return "[" + esc(text) + "](" + url + ")"
	case MarkupHTML:
		return `<a href="` + esc(url) + `">` + esc(text) + "</a>"
	case MarkupPlain:
	}
	return text + " " + url
}

func italic(markup Markup, s string) string {
	switch markup {
	case MarkupMarkdown:
		return "_" + s + "_"
	case MarkupHTML:
		return "<i>" + s + "</i>"
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
	case MarkupPlain:
	}
	return s
}

func bullet(markup Markup) string {
	if markup == MarkupMarkdown {
		return "- "
	}
	return "• "
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
