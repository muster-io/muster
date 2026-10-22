// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"encoding/json"
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
// channel and write HTML, on a Route with three Snooze durations.
func rootMessage(status string) messages.Message {
	return rootMessageOf(status, 3)
}

// rootMessageOf is rootMessage on a Route with the given number of Snooze durations, at most three.
func rootMessageOf(status string, durations int) messages.Message {
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
	for _, b := range buttons.ForStatus(status, durations) {
		label := map[string]string{buttons.CommandAcknowledge: "Ack", buttons.CommandUnacknowledge: "Unack",
			buttons.CommandResolve: "Resolve", buttons.CommandUnsnooze: "Unsnooze"}[b.Command]
		if b.Command == buttons.CommandSnooze {
			label = messages.PresetLabel("en", b.Argument, "09:00", "monday")
		}
		if b.Command == buttons.CommandSnooze && !buttons.IsPreset(b.Argument) {
			label = fmt.Sprintf("Snooze %d h", []int{1, 4, 24}[b.Argument])
		}
		m.Buttons = append(m.Buttons, messages.Button{Command: b.Command, Argument: b.Argument, Label: label,
			ActionID: "act-" + b.Command + fmt.Sprint(b.Argument), KeyID: "k-0011223344556677"})
	}
	return m
}

// TestRootPost is C-13.FR-3 and C-13.AC-13: an empty message; one attachment coloured by status, titled #N and the
// title linked to the Alert Group page, whose text holds the sections, the links under the Alerts starting with "Open
// in Muster", the notices and the footer of the Destination's identity space as its last line, and no Mentions line on
// a Quiet post; the footer "Muster v<version>" with the mark; and the buttons with stable ids calling the Connection's
// callback, the Snooze presets after the durations (C-10.FR-18, C-10.AC-23).
func TestRootPost(t *testing.T) {
	p := rootPost(rootMessage(messages.ColourFiring), testLook(), "")
	if p.Message != "" || p.Props == nil || len(p.Props.Attachments) != 1 {
		t.Fatalf("post = %+v", p)
	}
	if raw, err := json.Marshal(p); err != nil || !strings.Contains(string(raw), `"message":""`) {
		t.Errorf("the empty message is not sent: %s %v", raw, err)
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
	names := make([]string, len(a.Actions))
	for i, ac := range a.Actions {
		names[i] = ac.Name
	}
	if !slices.Equal(ids, []string{"ack", "resolve", "snooze0", "snooze1", "snooze2", "snoozetomorrow", "snoozeweek"}) ||
		!slices.Equal(names, []string{"Ack", "Resolve", "Snooze 1 h", "Snooze 4 h", "Snooze 24 h",
			"Until tomorrow 09:00", "Until Monday 09:00"}) {
		t.Errorf("actions = %+v", a.Actions)
	}
	for _, ac := range a.Actions {
		if !regexp.MustCompile(`^[a-z]+[0-9]*$`).MatchString(ac.ID) {
			t.Errorf("the action id %q is not letters with an index", ac.ID)
		}
	}
}

// TestRootPostStatuses gives each status its colour and buttons, with an empty message, the Snooze presets after the
// durations of an acknowledged one and none on a snoozed one; a resolved Root message has none, and a Route without
// Snooze durations offers neither Snooze nor the presets.
func TestRootPostStatuses(t *testing.T) {
	cases := []struct {
		status, colour string
		ids            []string
	}{
		{messages.ColourAcknowledged, "#f57c00", []string{"unack", "resolve", "snooze0", "snooze1", "snooze2",
			"snoozetomorrow", "snoozeweek"}},
		{messages.ColourSnoozed, "#9e9e9e", []string{"ack", "unsnooze", "resolve"}},
		{messages.ColourResolved, "#388e3c", []string{}},
	}
	for _, c := range cases {
		p := rootPost(rootMessage(c.status), testLook(), "")
		a := p.Props.Attachments[0]
		ids := []string{}
		for _, ac := range a.Actions {
			ids = append(ids, ac.ID)
		}
		if p.Message != "" || a.Title != "#1 DiskFull" || a.Color != c.colour || !slices.Equal(ids, c.ids) {
			t.Errorf("%s: %q %q %s %v", c.status, p.Message, a.Title, a.Color, ids)
		}
	}
	for status, want := range map[string][]string{messages.ColourFiring: {"ack", "resolve"},
		messages.ColourAcknowledged: {"unack", "resolve"}} {
		ids := []string{}
		for _, ac := range rootPost(rootMessageOf(status, 0), testLook(), "").Props.Attachments[0].Actions {
			ids = append(ids, ac.ID)
		}
		if !slices.Equal(ids, want) {
			t.Errorf("%s without durations: %v", status, ids)
		}
	}
	m := rootMessage(messages.ColourFiring)
	m.Colour = "unknown"
	if p := rootPost(m, testLook(), ""); p.Message != "" || p.Props.Attachments[0].Color != "" ||
		p.Props.Attachments[0].Title != "#1 DiskFull" {
		t.Errorf("an unknown colour = %+v", p)
	}
	m.Buttons = []messages.Button{{Command: buttons.CommandStillOnIt, Label: "Still on it"}}
	if p := rootPost(m, testLook(), ""); p.Props.Attachments[0].Actions[0].ID != "stillonit" {
		t.Errorf("still on it = %+v", p.Props.Attachments[0].Actions)
	}
}

// TestRootPostMentions is C-13.FR-3, C-13.FR-8, F-056 and F-113: the Mentions of a Loud post are the first line of
// the attachment's text, under its title, and nowhere else; the message stays empty. An edit, laid out without
// Mentions, has no Mentions line.
func TestRootPostMentions(t *testing.T) {
	p := rootPost(rootMessage(messages.ColourFiring), testLook(), "@channel @bob")
	a := p.Props.Attachments[0]
	line, rest, _ := strings.Cut(a.Text, "\n")
	if p.Message != "" || line != "@channel @bob" || mentionPattern.MatchString(rest) || a.Title != "#1 DiskFull" ||
		rest != rootPost(rootMessage(messages.ColourFiring), testLook(), "").Props.Attachments[0].Text {
		t.Errorf("post = %q, %q", p.Message, a.Text)
	}
	edit := rootPost(rootMessage(messages.ColourAcknowledged), testLook(), "")
	if edit.Message != "" || mentionPattern.MatchString(edit.Props.Attachments[0].Text) ||
		!strings.HasPrefix(edit.Props.Attachments[0].Text, "a · Started") {
		t.Errorf("edit = %q, %q", edit.Message, edit.Props.Attachments[0].Text)
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
// a Loud Root message, under its Mentions line, as of a Quiet one; neutral text shows no token; the tokens of the
// template output are the body's and those of the Alert lines a line template wrote.
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
	line, text, _ := strings.Cut(rootPost(m, testLook(), "@channel").Props.Attachments[0].Text, "\n")
	if line != "@channel" {
		t.Errorf("the Mentions line = %q", line)
	}
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
	if p := rootPost(storm, testLook(), ""); p.Message != "" || p.Props.Attachments[0].Text != "storm @\u200ball" {
		t.Errorf("a Storm summary = %+v", p)
	}
}

// TestLengthLimit is C-12.FR-11 with F-057: a Loud Root message of 600 Alerts keeps its attachment text, its Mentions
// line included, within 16,383 characters, its Alerts cut to the distinct values with "+N more", and the Mentions, the
// link to Muster and the footer stay; its message is empty.
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
	if p.Message != "" || !strings.HasPrefix(text, "@channel\n") {
		t.Errorf("message %q, text %.40q…", p.Message, text)
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

// TestTextCut cuts a text that shortening could not bring within the limit in its middle, keeping the Mentions line,
// the links, the notices and the footer; a Mentions line too long for that loses whole Mentions from its end, never
// part of an `@name`; a tail longer than the limit is cut at its end.
func TestTextCut(t *testing.T) {
	l := testLook()
	l.limit = 160
	m := rootMessage(messages.ColourFiring)
	m.Summary = strings.Repeat("s", 300)
	got := text(m, l, "")
	if n := utf8.RuneCountInString(got); n > l.limit || !strings.Contains(got, "…\n[Open in Muster](") ||
		!strings.HasSuffix(got, "Reopened 2 times\nAcknowledged by bob") {
		t.Errorf("text (%d) = %q", n, got)
	}
	l.limit = 200
	got = text(m, l, "@channel @bob")
	if n := utf8.RuneCountInString(got); n != l.limit || !strings.HasPrefix(got, "@channel @bob\n") ||
		!strings.Contains(got, "…\n[Open in Muster](") || !strings.HasSuffix(got, "Reopened 2 times\nAcknowledged by bob") {
		t.Errorf("text with mentions (%d) = %q", n, got)
	}
	var many []string
	for i := range 30 {
		many = append(many, fmt.Sprintf("@user%02d", i))
	}
	got = text(m, l, strings.Join(many, " "))
	line, _, _ := strings.Cut(got, "\n")
	words := strings.Fields(line)
	if n := utf8.RuneCountInString(got); n > l.limit || len(words) == 0 || len(words) == len(many) ||
		!slices.Equal(words, many[:len(words)]) || !strings.Contains(got, "\n[Open in Muster](") ||
		!strings.HasSuffix(got, "Reopened 2 times\nAcknowledged by bob") {
		t.Errorf("a long Mentions line (%d) = %q", n, got)
	}
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"@channel @alice", 15, "@channel @alice"},
		{"@channel @alice", 14, "@channel"},
		{"@channel @alice", 8, "@channel"},
		{"@channel @alice", 7, ""},
		{"@channel Alice Smith @bob", 20, "@channel Alice Smith"},
		{"@channel", -1, ""},
	} {
		if got := fitMentions(c.in, c.n); got != c.want {
			t.Errorf("fitMentions(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
	l.limit = 40
	if got := text(m, l, ""); utf8.RuneCountInString(got) != 40 || !strings.HasPrefix(got, "[Open in Muster](") ||
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

// TestStormPost lays out a Storm summary, which has no heading, like a Root message: an empty message and one
// attachment of the Storm colour, without a title, footer or buttons, whose text is the Mentions of a Loud post, then
// its lines and the link; its Quiet edits have no Mentions line.
func TestStormPost(t *testing.T) {
	m := messages.Message{Kind: messages.KindStorm, Language: "en", Colour: messages.ColourStorm,
		Lines:   []string{"⛈ Storm on db_*: 12 Alert Groups"},
		Links:   []messages.Link{{Text: "Open in Muster", URL: "http://localhost:8080/alert-groups?route=RT1"}},
		Buttons: []messages.Button{}}
	body := "⛈ Storm on db\\_\\*: 12 Alert Groups\n[Open in Muster](http://localhost:8080/alert-groups?route=RT1)"
	for mentions, want := range map[string]string{"@channel": "@channel\n" + body, "": body} {
		p := rootPost(m, testLook(), mentions)
		if p.Message != "" || p.Props == nil || len(p.Props.Attachments) != 1 {
			t.Fatalf("storm = %+v", p)
		}
		if a := p.Props.Attachments[0]; a.Text != want || a.Color != "#6a1b9a" || a.Title != "" ||
			a.TitleLink != "" || a.Footer != "" || len(a.Actions) != 0 {
			t.Errorf("storm with %q = %+v", mentions, a)
		}
	}
	if raw, err := json.Marshal(rootPost(m, testLook(), "")); err != nil ||
		!strings.HasPrefix(string(raw), `{"message":"","props":{"attachments":[{"color":"#6a1b9a","text":"`) ||
		strings.Contains(string(raw), `"actions"`) {
		t.Errorf("storm JSON = %s %v", raw, err)
	}
}

// TestReplyPost lays out a Thread reply: the Mentions of a Loud one first, then its text. A Reminder, which has
// buttons, keeps the Owner's Mention in its message and has its text and the buttons "Still on it" and "Unack", with
// the same integration URL and context as on Root messages, in one attachment (C-13.FR-4).
func TestReplyPost(t *testing.T) {
	m := messages.Message{Kind: messages.KindReply, Language: "en", Colour: messages.ColourFiring,
		Lines: []string{"New alerts (1):", "• pod: i_2"}, Buttons: []messages.Button{}}
	if p := replyPost(m, testLook(), ""); p.Message != "New alerts (1):\n• pod: i\\_2" || p.Props != nil {
		t.Errorf("quiet reply = %+v", p)
	}
	if p := replyPost(m, testLook(), "@channel"); !strings.HasPrefix(p.Message, "@channel\nNew alerts (1):") {
		t.Errorf("loud reply = %q", p.Message)
	}
	m = messages.Message{Kind: messages.KindReply, Language: "en", Colour: messages.ColourAcknowledged,
		Lines: []string{"Reminder: bob_1, are you still on it?"},
		Buttons: []messages.Button{{Command: buttons.CommandStillOnIt, Label: "Still on it", ActionID: "x", KeyID: "k"},
			{Command: buttons.CommandUnacknowledge, Label: "Unack", ActionID: "y", KeyID: "k"}}}
	l := testLook()
	p := replyPost(m, l, "@bob")
	if p.Message != "@bob" || p.Props == nil || len(p.Props.Attachments) != 1 {
		t.Fatalf("reminder = %+v", p)
	}
	att := p.Props.Attachments[0]
	if att.Text != "Reminder: bob\\_1, are you still on it?" || att.Color != "#f57c00" || len(att.Actions) != 2 {
		t.Errorf("reminder attachment = %+v", att)
	}
	for i, want := range []action{
		{ID: "stillonit", Name: "Still on it", Type: "button", Style: "primary",
			Integration: integration{URL: l.callback, Context: actionContext{Action: "x", KeyID: "k"}}},
		{ID: "unack", Name: "Unack", Type: "button",
			Integration: integration{URL: l.callback, Context: actionContext{Action: "y", KeyID: "k"}}},
	} {
		if att.Actions[i] != want {
			t.Errorf("action %d = %+v", i, att.Actions[i])
		}
	}
	if p := replyPost(m, l, ""); p.Message != "" || len(p.Props.Attachments) != 1 {
		t.Errorf("quiet reminder = %+v", p)
	}
}

// sentStyles are the actions of the one attachment of p as its JSON sends them: each id with its style, "-" for an
// action without a style field.
func sentStyles(t *testing.T, p post) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Props struct {
			Attachments []struct {
				Actions []map[string]any `json:"actions"`
			} `json:"attachments"`
		} `json:"props"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil || len(sent.Props.Attachments) != 1 {
		t.Fatalf("post %s: %v", raw, err)
	}
	var out []string
	for _, a := range sent.Props.Attachments[0].Actions {
		style, ok := a["style"]
		if !ok {
			style = "-"
		}
		out = append(out, fmt.Sprintf("%v:%v", a["id"], style))
	}
	return strings.Join(out, " ")
}

// TestActionStyles is D329 with F-068: "ack" and "stillonit" are sent with the style primary and "resolve" with
// success, while "unack", "unsnooze", every "snooze<N>" and the Snooze presets have no style field, on a firing, an
// acknowledged and a snoozed Root message and on a Reminder; a button of any other Command has no style either.
func TestActionStyles(t *testing.T) {
	reminder := messages.Message{Kind: messages.KindReply, Language: "en", Colour: messages.ColourAcknowledged,
		Lines: []string{"Reminder: bob, are you still on it?"},
		Buttons: []messages.Button{{Command: buttons.CommandStillOnIt, Label: "Still on it", ActionID: "x", KeyID: "k"},
			{Command: buttons.CommandUnacknowledge, Label: "Unack", ActionID: "y", KeyID: "k"}}}
	for _, c := range []struct {
		name string
		p    post
		want string
	}{
		{"firing", rootPost(rootMessage(messages.ColourFiring), testLook(), ""),
			"ack:primary resolve:success snooze0:- snooze1:- snooze2:- snoozetomorrow:- snoozeweek:-"},
		{"acknowledged", rootPost(rootMessage(messages.ColourAcknowledged), testLook(), ""),
			"unack:- resolve:success snooze0:- snooze1:- snooze2:- snoozetomorrow:- snoozeweek:-"},
		{"snoozed", rootPost(rootMessage(messages.ColourSnoozed), testLook(), ""),
			"ack:primary unsnooze:- resolve:success"},
		{"reminder", replyPost(reminder, testLook(), "@bob"), "stillonit:primary unack:-"},
	} {
		if got := sentStyles(t, c.p); got != c.want {
			t.Errorf("%s: actions %s, want %s", c.name, got, c.want)
		}
	}
	if a := actions([]messages.Button{{Command: "unresolve", Label: "x"}}, callback); a[0].Style != "" {
		t.Errorf("an unknown command has the style %q", a[0].Style)
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
