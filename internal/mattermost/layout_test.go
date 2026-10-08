// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

// mentionPattern is a Mention that notifies: `@` and a name, as Mattermost finds them in a message and an attachment.
var mentionPattern = regexp.MustCompile(`(?i)(?:^|[^a-z0-9._-])@[a-z0-9._-]+`)

const (
	groupURL = "http://localhost:8080/alert-groups/AG0000000000A1"
	callback = "http://localhost:8081/api/v1/callbacks/mattermost/CN0000000000C1"
)

func testLook() look {
	return look{markup: messages.MarkupMarkdown, space: "mattermost:3", callback: callback,
		footer: "Muster v1.2.3", icon: "http://localhost:8080/muster-mark-256.png", limit: LengthLimit}
}

// rootMessage is a Root message as the renderer gives it, in a status, with an alert label that tries to mention the
// channel and write HTML.
func rootMessage(status string) messages.Message {
	m := messages.Message{Kind: messages.KindRoot, Language: "en", TimeZone: "UTC", Colour: status,
		Heading:      &messages.Heading{Number: 1, Title: "DiskFull", URL: groupURL},
		Environment:  "a · Started 2026-10-08 12:00 UTC",
		GroupLabels:  []messages.Label{{Name: "cluster", Value: "a"}},
		CommonLabels: []messages.Label{{Name: "note", Value: messages.Value("@channel <b>x</b>")}},
		Summary:      "Disk *almost* full",
		Alerts: &messages.AlertList{Lines: []messages.AlertLine{{Text: "pod: i1"},
			{Text: "pod: i2", Resolved: true}}},
		Links: []messages.Link{{Text: "Open in Muster", URL: groupURL},
			{Text: "Runbook", URL: "https://runbooks.example.org/disk"}},
		Notices: []string{"Reopened 2 times"},
		Footer:  "Acknowledged by Bob Smith", Footers: map[string]string{"mattermost:3": "Acknowledged by bob"}}
	for _, b := range buttons.ForStatus(status, 3) {
		label := map[string]string{buttons.CommandAcknowledge: "Ack", buttons.CommandUnacknowledge: "Unack",
			buttons.CommandResolve: "Resolve", buttons.CommandUnsnooze: "Unsnooze"}[b.Command]
		if b.Command == buttons.CommandSnooze {
			label = fmt.Sprintf("Snooze %d h", []int{1, 4, 24}[b.Argument])
		}
		m.Buttons = append(m.Buttons, messages.Button{Command: b.Command, Argument: b.Argument, Label: label,
			ActionID: "act-" + b.Command + fmt.Sprint(b.Argument), KeyID: "k-0011223344556677"})
	}
	return m
}

// TestRootPost is C-13.FR-3 and C-13.AC-13: the summary line as the message; one attachment coloured by status,
// titled #N and the title linked to the Alert Group page, whose text holds the sections, the links under the Alerts
// starting with "Open in Muster", the notices and the footer of the Destination's identity space as its last line;
// the footer "Muster v<version>" with the mark; and the buttons with stable ids calling the Connection's callback.
func TestRootPost(t *testing.T) {
	p := rootPost(rootMessage(messages.ColourFiring), testLook(), "")
	if p.Message != "🔴 #1 DiskFull" || p.Props == nil || len(p.Props.Attachments) != 1 {
		t.Fatalf("post = %+v", p)
	}
	a := p.Props.Attachments[0]
	if a.Color != "#d32f2f" || a.Title != "#1 DiskFull" || a.TitleLink != groupURL || a.Footer != "Muster v1.2.3" ||
		a.FooterIcon != "http://localhost:8080/muster-mark-256.png" {
		t.Errorf("attachment = %+v", a)
	}
	lines := strings.Split(a.Text, "\n")
	want := []string{
		"a · Started 2026-10-08 12:00 UTC",
		"cluster: a",
		"note: @\u200bchannel \\<b\\>x\\</b\\>",
		"_Disk \\*almost\\* full_",
		"- pod: i1",
		"- ~~pod: i2~~",
		"[Open in Muster](" + groupURL + ") · [Runbook](https://runbooks.example.org/disk)",
		"Reopened 2 times",
		"Acknowledged by bob",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("text =\n%s\nwant\n%s", a.Text, strings.Join(want, "\n"))
	}
	ids := make([]string, len(a.Actions))
	for i, ac := range a.Actions {
		ids[i] = ac.ID
		if ac.Type != "button" || ac.Integration.URL != callback || ac.Integration.Context.KeyID != "k-0011223344556677" ||
			!strings.HasPrefix(ac.Integration.Context.Action, "act-") {
			t.Errorf("action %d = %+v", i, ac)
		}
	}
	if !slices.Equal(ids, []string{"ack", "resolve", "snooze0", "snooze1", "snooze2"}) || a.Actions[2].Name != "Snooze 1 h" {
		t.Errorf("actions = %+v", a.Actions)
	}
}

// TestRootPostStatuses gives each status its colour, emoji and buttons; a resolved Root message has none.
func TestRootPostStatuses(t *testing.T) {
	cases := []struct {
		status, colour, emoji string
		ids                   []string
	}{
		{messages.ColourAcknowledged, "#f57c00", "🟠", []string{"unack", "resolve", "snooze0", "snooze1", "snooze2"}},
		{messages.ColourSnoozed, "#9e9e9e", "⚪", []string{"ack", "unsnooze", "resolve"}},
		{messages.ColourResolved, "#388e3c", "🟢", []string{}},
	}
	for _, c := range cases {
		p := rootPost(rootMessage(c.status), testLook(), "")
		a := p.Props.Attachments[0]
		ids := []string{}
		for _, ac := range a.Actions {
			ids = append(ids, ac.ID)
		}
		if p.Message != c.emoji+" #1 DiskFull" || a.Color != c.colour || !slices.Equal(ids, c.ids) {
			t.Errorf("%s: %q %s %v", c.status, p.Message, a.Color, ids)
		}
	}
	m := rootMessage(messages.ColourFiring)
	m.Colour = "unknown"
	if p := rootPost(m, testLook(), ""); p.Message != "#1 DiskFull" {
		t.Errorf("an unknown colour = %q", p.Message)
	}
	m.Buttons = []messages.Button{{Command: buttons.CommandStillOnIt, Label: "Still on it"}}
	if p := rootPost(m, testLook(), ""); p.Props.Attachments[0].Actions[0].ID != "stillonit" {
		t.Errorf("still on it = %+v", p.Props.Attachments[0].Actions)
	}
}

// TestRootPostMentions is C-13.FR-8 and F-056: the Mentions of a Loud post follow the summary line in its message,
// never in the attachment.
func TestRootPostMentions(t *testing.T) {
	p := rootPost(rootMessage(messages.ColourFiring), testLook(), "@channel @bob")
	if p.Message != "🔴 #1 DiskFull\n@channel @bob" || strings.Contains(p.Props.Attachments[0].Text, "@channel") {
		t.Errorf("post = %q, %q", p.Message, p.Props.Attachments[0].Text)
	}
}

// TestMentionText renders the targets of a Loud post in Mattermost's syntax: everyone with its word, groups, users by
// the username of their Account link, otherwise by their display name, which mentions nobody; duplicates go.
func TestMentionText(t *testing.T) {
	got := mentionText([]mentions.Target{
		{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneChannel},
		{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneNone},
		{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneHere},
		{Kind: mentions.TargetGroup, Group: "oncall"},
		{Kind: mentions.TargetGroup, Group: "bad group"},
		{Kind: mentions.TargetUser, User: &mentions.User{Username: "bob", Name: "Bob"}},
		{Kind: mentions.TargetUser, User: &mentions.User{Username: "bob", Name: "Bob"}},
		{Kind: mentions.TargetUser, User: &mentions.User{Name: "Alice @all *A*"}},
		{Kind: mentions.TargetUser},
	}, []templates.Token{{Name: templates.MentionChannel}, {Name: templates.MentionAll},
		{Name: templates.MentionGroup, Group: "oncall"}, {Name: templates.MentionGroup, Group: "dba"},
		{Name: templates.MentionGroup, Group: "bad group"}, {Name: templates.MentionOwner}}, messages.MarkupMarkdown)
	if want := "@channel @here @oncall @bob Alice @\u200ball \\*A\\* @all @dba"; got != want {
		t.Errorf("mentions = %q, want %q", got, want)
	}
	if got := mentionText(nil, nil, messages.MarkupMarkdown); got != "" {
		t.Errorf("no targets = %q", got)
	}
}

// TestTokens writes the trusted Mention tokens of template output as text that notifies nobody, in the attachment of
// a Loud Root message as of a Quiet one (F-056); neutral text shows no token; the tokens of the template output are
// the body's and those of the Alert lines a line template wrote.
func TestTokens(t *testing.T) {
	tok := func(name, group string) string {
		return templates.MentionToken(templates.Token{Name: name, Group: group})
	}
	m := rootMessage(messages.ColourFiring)
	m.Body = &messages.Body{Text: "page " + tok("channel", "") + " " + tok("group", "oncall") + " " +
		tok("group", "bad group") + " " + tok("owner", "") + "\n", Markup: messages.MarkupMarkdown}
	m.Alerts = &messages.AlertList{Lines: []messages.AlertLine{
		{Text: "line " + tok("here", ""), Markup: messages.MarkupMarkdown},
		{Text: "neutral " + tok("all", "")},
	}}
	text := rootPost(m, testLook(), "@channel").Props.Attachments[0].Text
	for _, want := range []string{"page @\u200bchannel @\u200boncall  @\u200bowner", "- line @\u200bhere",
		"- neutral all"} {
		if !strings.Contains(text, want) {
			t.Errorf("text has no %q:\n%s", want, text)
		}
	}
	if strings.ContainsFunc(text, func(r rune) bool { return r >= '\uE000' && r <= '\uE002' }) ||
		mentionPattern.MatchString(text) {
		t.Errorf("a token survived or notifies: %q", text)
	}
	if got := templateTokens(m); !slices.Equal(got, []templates.Token{{Name: "channel"},
		{Name: "group", Group: "oncall"}, {Name: "group", Group: "bad group"}, {Name: "owner"}, {Name: "here"}}) {
		t.Errorf("tokens = %+v", got)
	}
	if got := templateTokens(rootMessage(messages.ColourFiring)); len(got) != 0 {
		t.Errorf("tokens without template output = %+v", got)
	}
	storm := messages.Message{Kind: messages.KindStorm, Colour: messages.ColourStorm,
		Body: &messages.Body{Text: "storm " + tok("all", ""), Markup: messages.MarkupMarkdown}}
	if p := rootPost(storm, testLook(), ""); p.Message != "storm @\u200ball" {
		t.Errorf("a Storm summary = %q", p.Message)
	}
}

// TestLengthLimit is C-12.FR-11 with F-057: a Root message of 600 Alerts keeps its message and its attachment text
// within 16,383 characters, its Alerts cut to the distinct values with "+N more", and the link to Muster and the
// footer stay.
func TestLengthLimit(t *testing.T) {
	m := rootMessage(messages.ColourFiring)
	list := &messages.AlertList{}
	values := []string{}
	for i := range 600 {
		pod := fmt.Sprintf("pod-%03d", i)
		list.Lines = append(list.Lines, messages.AlertLine{Text: "alertname=DiskFull, cluster=a, instance=" +
			pod + ".db.example.org:9100, pod=" + pod})
		if i < 10 {
			values = append(values, pod)
		}
	}
	list.Distinct = []messages.Distinct{{Label: "pod", Values: values, More: 590}}
	m.Alerts = list
	m.Heading.Title = strings.Repeat("T", messages.ValueCap)
	p := rootPost(m, testLook(), "@channel")
	text := p.Props.Attachments[0].Text
	if n := utf8.RuneCountInString(text); n > LengthLimit {
		t.Errorf("the text has %d characters", n)
	}
	if n := utf8.RuneCountInString(p.Message); n > LengthLimit || !strings.HasSuffix(p.Message, "\n@channel") {
		t.Errorf("the message has %d characters: %q…", n, p.Message[:40])
	}
	for _, want := range []string{"pod: pod-000, pod-001", "+590 more", "[Full list in Muster](" + groupURL + ")",
		"[Open in Muster](" + groupURL + ")"} {
		if !strings.Contains(text, want) {
			t.Errorf("the text has no %q", want)
		}
	}
	if strings.Contains(text, "pod-599") || !strings.HasSuffix(text, "\nAcknowledged by bob") {
		t.Errorf("text = %s", text)
	}
}

// TestTextCut cuts a text that shortening could not bring within the limit in its middle, keeping the links, the
// notices and the footer; a tail longer than the limit is cut at its end.
func TestTextCut(t *testing.T) {
	l := testLook()
	l.limit = 160
	m := rootMessage(messages.ColourFiring)
	m.Summary = strings.Repeat("s", 300)
	got := text(m, l)
	if n := utf8.RuneCountInString(got); n > l.limit || !strings.Contains(got, "…\n[Open in Muster](") ||
		!strings.HasSuffix(got, "Reopened 2 times\nAcknowledged by bob") {
		t.Errorf("text (%d) = %q", n, got)
	}
	l.limit = 40
	if got := text(m, l); utf8.RuneCountInString(got) != 40 || !strings.HasPrefix(got, "[Open in Muster](") ||
		!strings.HasSuffix(got, "…") {
		t.Errorf("a long tail = %q", got)
	}
	if got := cutMiddle("abcdef", "", 4); got != "abc…" {
		t.Errorf("no tail = %q", got)
	}
	if got := cutEnd("abc", 3); got != "abc" {
		t.Errorf("a short text = %q", got)
	}
}

// TestStormPost lays out a Storm summary, which has no heading: its lines and the link in the message, Mentions after
// them, and an attachment only for buttons.
func TestStormPost(t *testing.T) {
	m := messages.Message{Kind: messages.KindStorm, Language: "en", Colour: messages.ColourStorm,
		Lines:   []string{"⛈ Storm on db_*: 12 Alert Groups"},
		Links:   []messages.Link{{Text: "Open in Muster", URL: "http://localhost:8080/alert-groups?route=RT1"}},
		Buttons: []messages.Button{}}
	p := rootPost(m, testLook(), "@channel")
	want := "⛈ Storm on db\\_\\*: 12 Alert Groups\n[Open in Muster](http://localhost:8080/alert-groups?route=RT1)\n@channel"
	if p.Message != want || p.Props == nil || len(p.Props.Attachments) != 0 {
		t.Errorf("storm = %q, %+v", p.Message, p.Props)
	}
}

// TestReplyPost lays out a Thread reply: the Mentions of a Loud one first, then its text; its buttons, when it has
// any, in an attachment.
func TestReplyPost(t *testing.T) {
	m := messages.Message{Kind: messages.KindReply, Language: "en", Colour: messages.ColourFiring,
		Lines: []string{"New alerts (1):", "• pod: i_2"}, Buttons: []messages.Button{}}
	if p := replyPost(m, testLook(), ""); p.Message != "New alerts (1):\n• pod: i\\_2" || p.Props != nil {
		t.Errorf("quiet reply = %+v", p)
	}
	if p := replyPost(m, testLook(), "@channel"); !strings.HasPrefix(p.Message, "@channel\nNew alerts (1):") {
		t.Errorf("loud reply = %q", p.Message)
	}
	m.Buttons = []messages.Button{{Command: buttons.CommandStillOnIt, Label: "Still on it", ActionID: "x", KeyID: "k"},
		{Command: buttons.CommandUnacknowledge, Label: "Unack", ActionID: "y", KeyID: "k"}}
	p := replyPost(m, testLook(), "")
	if p.Props == nil || len(p.Props.Attachments) != 1 || len(p.Props.Attachments[0].Actions) != 2 ||
		p.Props.Attachments[0].Actions[0].ID != "stillonit" || p.Props.Attachments[0].Text != "" {
		t.Errorf("reply with buttons = %+v", p.Props)
	}
}

// TestPlain lays out the same message without markup after the messenger rejected it (C-11.FR-8).
func TestPlain(t *testing.T) {
	l := testLook()
	l.markup = messages.MarkupPlain
	p := rootPost(rootMessage(messages.ColourFiring), l, "")
	text := p.Props.Attachments[0].Text
	if !strings.Contains(text, "Open in Muster "+groupURL) || !strings.Contains(text, "note: @\u200bchannel <b>x</b>") {
		t.Errorf("plain text = %s", text)
	}
}
