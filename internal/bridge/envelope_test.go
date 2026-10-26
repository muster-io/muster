// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

const (
	groupPage = "https://muster.example.org/alert-groups/AGK7M3QX9P2RTA"
	routePage = "https://muster.example.org/alert-groups?route=RTK7M3QX9P2RTA"
	runbook   = "https://runbooks.example.org/disk"
)

// snoozeSeconds are the Snooze durations of the Route of the fixtures: 1 h, 4 h and 24 h.
var snoozeSeconds = []int64{3600, 14400, 86400}

// testKeys sign action ids with a fixed MAC and key id, as the Keyring does with its active key.
type testKeys struct{}

func (testKeys) Sign(keyring.Purpose, []byte) ([]byte, string, error) {
	return bytes.Repeat([]byte{0xab}, 32), "k-0123456789abcdef0123456789abcdef", nil
}

func (testKeys) VerifyPrefix(keyring.Purpose, string, []byte, []byte) (bool, error) { return true, nil }

func (testKeys) KeyIDs() []string { return []string{"k-0123456789abcdef0123456789abcdef"} }

// signedButtons are the buttons of a Root message in status, in lang, with the Snooze durations seconds and the Snooze
// presets of a working day that starts at 09:00 and a week that starts on Monday, each with a signed action id as the
// renderer gives it.
func signedButtons(t *testing.T, status, lang string, seconds []int64) []messages.Button {
	t.Helper()
	out := []messages.Button{}
	for _, b := range buttons.ForStatus(status, len(seconds)) {
		id, kid, err := buttons.Sign(testKeys{}, buttons.Action{Subject: buttons.SubjectRoot,
			PublicID: "AGK7M3QX9P2RTA", Command: b.Command, Argument: b.Argument})
		if err != nil {
			t.Fatal(err)
		}
		label := messages.T(lang, "button."+map[string]string{buttons.CommandAcknowledge: "acknowledge",
			buttons.CommandUnacknowledge: "unacknowledge", buttons.CommandResolve: "resolve",
			buttons.CommandUnsnooze: "unsnooze"}[b.Command], nil)
		switch {
		case b.Command == buttons.CommandSnooze && buttons.IsPreset(b.Argument):
			label = messages.PresetLabel(lang, b.Argument, "09:00", "monday")
		case b.Command == buttons.CommandSnooze:
			label = messages.T(lang, "button.snooze", messages.Args{"duration": messages.Duration(lang,
				seconds[b.Argument])})
		}
		out = append(out, messages.Button{Command: b.Command, Argument: b.Argument, Label: label, ActionID: id,
			KeyID: kid})
	}
	return out
}

// rootMessage is a Root message as the renderer gives it in CommonMark, in a status: two Alerts, one of them
// resolved, a Runbook link, a notice, a footer when someone acted, and the buttons of the status.
func rootMessage(t *testing.T, status string) messages.Message {
	t.Helper()
	m := messages.Message{Kind: messages.KindRoot, Language: "en", TimeZone: "UTC", Colour: status,
		Heading:      &messages.Heading{Number: 42, Title: "DiskFull", URL: groupPage},
		Environment:  "prod · started 2026-10-09 12:00 UTC",
		GroupLabels:  []messages.Label{{Name: "alertname", Value: "DiskFull"}},
		CommonLabels: []messages.Label{{Name: "team", Value: "br"}},
		Summary:      "The disk is almost full",
		Alerts: &messages.AlertList{Lines: []messages.AlertLine{{Text: "pod: i1"},
			{Text: "pod: i2", Resolved: true}}},
		Links:         []messages.Link{{Text: "Open in Muster", URL: groupPage}, {Text: "Runbook", URL: runbook}},
		Notices:       []string{"🔁 Reopened ×2"},
		Buttons:       signedButtons(t, status, "en", snoozeSeconds),
		SnoozeSeconds: snoozeSeconds}
	if status != messages.ColourFiring {
		m.Footer = "Acknowledged by Bob Smith"
		m.Footers = map[string]string{"mattermost:3": "Acknowledged by bob"}
	}
	return m
}

// stormMessage is a Storm summary as the renderer gives it.
func stormMessage() messages.Message {
	return messages.Message{Kind: messages.KindStorm, Language: "en", Colour: messages.ColourStorm,
		Buttons: []messages.Button{}, Lines: []string{"⛈ Storm on br: 12 Alert Groups, 1 urgent — open in Muster"},
		Links: []messages.Link{{Text: "Open in Muster", URL: routePage}}}
}

// replyMessage is a Thread reply with its lines.
func replyMessage(lines ...string) messages.Message {
	return messages.Message{Kind: messages.KindReply, Language: "en", Colour: messages.ColourFiring, Lines: lines,
		Buttons: []messages.Button{}}
}

// infoOf is the info of a bridge: the fixture of every feature, changed by mutate.
func infoOf(t *testing.T, mutate ...func(map[string]any)) Info {
	t.Helper()
	i, err := ParseInfo(validInfo(t, func(m map[string]any) {
		for _, f := range mutate {
			f(m)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// threadOnly is a bridge without buttons that forwards Thread messages as said.
func threadOnly(mode string) func(map[string]any) {
	return func(m map[string]any) {
		delete(at(m, "capabilities"), "buttons")
		at(m, "capabilities")["thread_messages"] = mode
	}
}

// bare is a bridge with neither buttons nor Thread messages.
func bare(m map[string]any) {
	delete(at(m, "capabilities"), "buttons")
	delete(at(m, "capabilities"), "thread_messages")
}

// bridgeSchema is the schema name of api/bridge.yaml.
func bridgeSchema(t *testing.T, name string) *openapi3.Schema {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/bridge.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ref := doc.Components.Schemas[name]
	if ref == nil || ref.Value == nil {
		t.Fatalf("no %s in api/bridge.yaml", name)
	}
	return ref.Value
}

// TestEnvelopeMatchesTheProtocol covers C-22.FR-4 and FR-16: the envelopes of a Root message with Mentions, a Storm
// summary, a test message, an edit and Thread replies with and without a thread_ref pass the schemas of
// api/bridge.yaml.
func TestEnvelopeMatchesTheProtocol(t *testing.T) {
	i := infoOf(t)
	head, list := mentionsOf(delivery.Call{Loudness: groups.Loud, Targets: []mentions.Target{
		linked("Alice Smith", "u-alice", "Alice Smith"), linked("Bob Brown", "", "")}})
	firing := content(rootMessage(t, messages.ColourFiring), i, head)
	root := postInput(gen.Root, "ch-alerts", firing, true)
	root.Mentions = list
	reminder := replyMessage("Still unacknowledged")
	reminder.Buttons = signedButtons(t, messages.ColourAcknowledged, "en", nil)
	for _, tc := range []struct {
		name, schema string
		v            any
	}{
		{"root", "MessageInput", root},
		{"storm summary", "MessageInput", postInput(gen.StormSummary, "ch-alerts", content(stormMessage(), i, nil),
			false)},
		{"test", "MessageInput", postInput(gen.Test, "ch-alerts", content(rootMessage(t, messages.ColourResolved),
			infoOf(t, bare), []string{"🧪 Test message"}), false)},
		{"edit", "MessageContent", content(rootMessage(t, messages.ColourAcknowledged), i, nil)},
		{"first reply", "ThreadReplyInput", replyInput("", replyMessage("New Alert: pod i3"), i, nil, nil, false)},
		{"reply", "ThreadReplyInput", replyInput("t-m1", reminder, i, head, list, true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatal(err)
			}
			if err := visit(t, bridgeSchema(t, tc.schema), b); err != nil {
				t.Fatalf("%s does not match %s: %v", b, tc.schema, err)
			}
		})
	}
	if b, _ := json.Marshal(replyInput("", replyMessage("x"), i, nil, nil, false)); !strings.Contains(string(b),
		`"thread_ref":null`) || !strings.Contains(string(b), `"mentions":[]`) {
		t.Fatalf("a first reply = %s", b)
	}
	if !strings.HasPrefix(firing.Body, "[@Alice Smith](muster-mention:m1) Bob Brown\n\n") {
		t.Fatalf("the body of a Root message with Mentions = %q", firing.Body)
	}
}

// TestRootEnvelope covers C-22.FR-4 and FR-5 for the parts of the envelope: the status, colour and emoji of each
// status, the heading as title and link, and the body of items 3 to 11 without the heading or the buttons.
func TestRootEnvelope(t *testing.T) {
	i := infoOf(t)
	for status, want := range map[string][2]string{
		messages.ColourFiring: {"#d32f2f", "🔴"}, messages.ColourAcknowledged: {"#f57c00", "🟠"},
		messages.ColourSnoozed: {"#9e9e9e", "⚪"}, messages.ColourResolved: {"#388e3c", "🟢"},
	} {
		c := content(rootMessage(t, status), i, nil)
		if string(c.Status) != status || c.Color != want[0] || c.Emoji != want[1] || c.Title != "#42 DiskFull" ||
			c.Link != (gen.Link{Url: groupPage, Label: "Open in Muster"}) {
			t.Errorf("%s: %+v", status, c)
		}
	}
	c := content(rootMessage(t, messages.ColourAcknowledged), i, nil)
	want := "prod · started 2026-10-09 12:00 UTC\n\n> **alertname** DiskFull\n\n> **team** br\n\n" +
		"*The disk is almost full*\n\n- pod: i1\n- ~~pod: i2~~\n\n[Open in Muster](" + groupPage + ") · [Runbook](" +
		runbook + ")\n\n🔁 Reopened ×2\n\nAcknowledged by Bob Smith"
	if c.Body != want {
		t.Fatalf("body\n%q\nwant\n%q", c.Body, want)
	}
	storm := content(stormMessage(), i, nil)
	if storm.Status != gen.Storm || storm.Color != "#6a1b9a" || storm.Emoji != "⛈" ||
		storm.Title != "⛈ Storm on br: 12 Alert Groups, 1 urgent — open in Muster" ||
		storm.Link != (gen.Link{Url: routePage, Label: "Open in Muster"}) ||
		storm.Body != "[Open in Muster]("+routePage+")" || len(storm.Buttons) != 0 {
		t.Fatalf("storm summary %+v", storm)
	}
	long := rootMessage(t, messages.ColourFiring)
	long.Heading.Title = strings.Repeat("é", 600)
	if c := content(long, i, nil); utf8.RuneCountInString(c.Title) != maxTitle || !strings.HasSuffix(c.Title, "…") {
		t.Fatalf("a long title has %d characters", utf8.RuneCountInString(c.Title))
	}
	if c := content(messages.Message{Kind: messages.KindStorm, Colour: messages.ColourStorm}, i, nil); c.Title != "" ||
		c.Body != "" || c.Link != (gen.Link{}) {
		t.Fatalf("an empty Storm summary %+v", c)
	}
}

// TestHostileLabelIsLiteral covers C-22.FR-4 and AC-5: a hostile label value reaches the body escaped for CommonMark
// with its `@` neutralized — no link, no HTML, no emphasis, no Mention — and a hostile title stays neutralized plain
// text; the trusted Mention tokens of a template notify nobody.
func TestHostileLabelIsLiteral(t *testing.T) {
	hostile := "@all *x* <b>y</b> [z](http://evil)"
	m := rootMessage(t, messages.ColourFiring)
	m.Heading.Title = messages.Value("@here <i>t</i>")
	m.CommonLabels = append(m.CommonLabels, messages.Label{Name: "note", Value: messages.Value(hostile)})
	m.Alerts.Lines = append(m.Alerts.Lines, messages.AlertLine{Text: messages.Value("# 1. " + hostile)})
	c := content(m, infoOf(t), nil)
	literal := "@\u200ball \\*x\\* \\<b\\>y\\</b\\> \\[z\\](http://evil)"
	if !strings.Contains(c.Body, "> **note** "+literal) || !strings.Contains(c.Body, "- \\# 1. "+literal) {
		t.Fatalf("body %q", c.Body)
	}
	for _, bad := range []string{"<b>", "[z](", "@all", "*x*"} {
		if strings.Contains(c.Body, bad) {
			t.Fatalf("the body has %q: %q", bad, c.Body)
		}
	}
	if c.Title != "#42 @\u200bhere <i>t</i>" {
		t.Fatalf("title %q", c.Title)
	}
	tm := rootMessage(t, messages.ColourFiring)
	tm.Body = &messages.Body{Text: "Owner: " + templates.MentionToken(templates.Token{Name: templates.MentionOwner}) +
		" " + templates.MentionToken(templates.Token{Name: templates.MentionGroup, Group: "db_*team*"}),
		Markup: messages.MarkupCommonMark}
	tm.Alerts.Lines = []messages.AlertLine{{Text: "**i1** " + templates.MentionToken(templates.Token{
		Name: templates.MentionAll}), Markup: messages.MarkupCommonMark}}
	body := content(tm, infoOf(t), nil).Body
	if !strings.Contains(body, "Owner: @\u200bowner @\u200bdb\\_\\*team\\*") || !strings.Contains(body,
		"- **i1** @\u200ball") {
		t.Fatalf("template output %q", body)
	}
}

// TestButtonsAndCommandsLine covers C-22.FR-5 and AC-11: buttons with their style hints and data of at most 64 bytes
// only for a bridge with `buttons`; otherwise, with `thread_messages`, the commands line of the status at the end of a
// Root message, in the Route's language, and none for a resolved one, a Storm summary or a Thread reply.
func TestButtonsAndCommandsLine(t *testing.T) {
	const line = "Reply in the Thread"
	firing := rootMessage(t, messages.ColourFiring)
	c := content(firing, infoOf(t), nil)
	var got []string
	for n, b := range c.Buttons {
		got = append(got, b.Label+":"+string(b.Style))
		if b.Data != firing.Buttons[n].ActionID || len(b.Data) > 64 {
			t.Errorf("data of %s: %q", b.Label, b.Data)
		}
	}
	if fmt.Sprint(got) != "[Ack:primary Resolve:success Snooze 1 h:neutral Snooze 4 h:neutral Snooze 24 h:neutral "+
		"Until tomorrow 09:00:neutral Until Monday 09:00:neutral]" || strings.Contains(c.Body, line) {
		t.Fatalf("buttons %v, body %q", got, c.Body)
	}
	acked := content(rootMessage(t, messages.ColourAcknowledged), infoOf(t), nil)
	if acked.Buttons[0].Label != "Unack" || acked.Buttons[0].Style != gen.Neutral ||
		acked.Buttons[1].Style != gen.Success {
		t.Fatalf("acknowledged buttons %+v", acked.Buttons)
	}
	ru := rootMessage(t, messages.ColourFiring)
	ru.Language, ru.Buttons = messages.LanguageRussian, signedButtons(t, messages.ColourFiring, "ru", snoozeSeconds)
	for _, tc := range []struct {
		name string
		m    messages.Message
		info Info
		want string // the end of the body; "" for no commands line
	}{
		{"neither", firing, infoOf(t, bare), ""},
		{"firing", firing, infoOf(t, threadOnly("all")), "\n\nReply in the Thread: ack · resolve · snooze 1h"},
		{"acknowledged", rootMessage(t, messages.ColourAcknowledged), infoOf(t, threadOnly("all")),
			"\n\nReply in the Thread: unack · resolve · snooze 1h"},
		{"snoozed", rootMessage(t, messages.ColourSnoozed), infoOf(t, threadOnly("all")),
			"\n\nReply in the Thread: ack · unsnooze · resolve"},
		{"resolved", rootMessage(t, messages.ColourResolved), infoOf(t, threadOnly("all")), ""},
		{"mentions only", firing, infoOf(t, threadOnly("mentions_only")),
			"\n\nReply in the Thread, mentioning the bot: ack · resolve · snooze 1h"},
		{"russian", ru, infoOf(t, threadOnly("all")), "\n\nОтветьте в треде: взял · решено · отложить 1ч"},
		{"storm summary", stormMessage(), infoOf(t, threadOnly("all")), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := content(tc.m, tc.info, nil)
			if len(c.Buttons) != 0 {
				t.Fatalf("buttons without the capability: %+v", c.Buttons)
			}
			if tc.want == "" && (strings.Contains(c.Body, line) || strings.Contains(c.Body, "Ответьте")) ||
				tc.want != "" && !strings.HasSuffix(c.Body, tc.want) {
				t.Fatalf("body %q", c.Body)
			}
		})
	}
	reminder := replyMessage("Still unacknowledged")
	reminder.Buttons = signedButtons(t, messages.ColourAcknowledged, "en", nil)
	if in := replyInput("t-1", reminder, infoOf(t, threadOnly("all")), nil, nil, false); len(in.Buttons) != 0 ||
		strings.Contains(in.Body, line) {
		t.Fatalf("a reply %+v", in)
	}
	if in := replyInput("t-1", reminder, infoOf(t), nil, nil, false); len(in.Buttons) != 2 ||
		in.Buttons[0].Label != "Unack" {
		t.Fatalf("a Reminder's buttons %+v", in.Buttons)
	}
}

// TestButtonsBeyondMaxPerMessage covers C-22.FR-5: beyond buttons.max_per_message the Snooze buttons go first — the
// week_start preset, then the tomorrow preset, then the durations from the longest, whatever their order — then
// buttons from the end; a button without signed data and a long label are never sent as they are.
func TestButtonsBeyondMaxPerMessage(t *testing.T) {
	labels := func(bs []gen.Button) string {
		var out []string
		for _, b := range bs {
			out = append(out, b.Label)
		}
		return strings.Join(out, ",")
	}
	most := func(n int) Info { return infoOf(t, set("capabilities.buttons", "max_per_message", n)) }
	firing := rootMessage(t, messages.ColourFiring)
	if got := labels(buttonsOf(firing, most(3))); got != "Ack,Resolve,Snooze 1 h" {
		t.Fatalf("3 = %s", got)
	}
	unsorted := []int64{86400, 3600, 14400}
	shuffled := rootMessage(t, messages.ColourFiring)
	shuffled.Buttons, shuffled.SnoozeSeconds = signedButtons(t, messages.ColourFiring, "en", unsorted), unsorted
	if got := labels(buttonsOf(shuffled, most(4))); got != "Ack,Resolve,Snooze 1 h,Snooze 4 h" {
		t.Fatalf("unsorted durations = %s", got)
	}
	unknown := rootMessage(t, messages.ColourFiring)
	unknown.SnoozeSeconds = nil
	if got := labels(buttonsOf(unknown, most(3))); got != "Ack,Resolve,Snooze 1 h" {
		t.Fatalf("durations unknown = %s", got)
	}
	if got := labels(buttonsOf(rootMessage(t, messages.ColourSnoozed), most(2))); got != "Ack,Unsnooze" {
		t.Fatalf("snoozed = %s", got)
	}
	odd := rootMessage(t, messages.ColourFiring)
	odd.Buttons[0].ActionID = ""
	odd.Buttons[1].Label = strings.Repeat("ж", 80)
	odd.Buttons[2].ActionID = strings.Repeat("a", 65)
	got := buttonsOf(odd, infoOf(t))
	if len(got) != 5 || utf8.RuneCountInString(got[0].Label) != maxLabel || got[1].Label != "Snooze 4 h" {
		t.Fatalf("odd buttons %+v", got)
	}
	if got := labels(buttonsOf(shuffled, most(5))); got != "Ack,Resolve,Snooze 24 h,Snooze 1 h,Snooze 4 h" {
		t.Fatalf("unsorted durations, 5 = %s", got)
	}
}

// TestPresetButtonsBeyondMaxPerMessage covers C-22.FR-5 and AC-27: the Snooze presets follow the durations with the
// style hint neutral, and beyond buttons.max_per_message the week_start preset goes first, then the tomorrow preset,
// then the durations from the longest — whatever the length of the labels, the Russian ones and a long one included —
// on a firing and an acknowledged Root message of a Route with the durations 1 h, 4 h and 24 h, at every limit from 2
// to 7.
func TestPresetButtonsBeyondMaxPerMessage(t *testing.T) {
	all := map[string][]string{
		messages.ColourFiring: {"Ack:primary", "Resolve:success", "Snooze 1 h:neutral", "Snooze 4 h:neutral",
			"Snooze 24 h:neutral", "Until tomorrow 09:00:neutral", "Until Monday 09:00:neutral"},
		messages.ColourAcknowledged: {"Unack:neutral", "Resolve:success", "Snooze 1 h:neutral", "Snooze 4 h:neutral",
			"Snooze 24 h:neutral", "Until tomorrow 09:00:neutral", "Until Monday 09:00:neutral"},
	}
	ru := []string{"Подтвердить:primary", "Закрыть:success", "Отложить на 1 ч:neutral", "Отложить на 4 ч:neutral",
		"Отложить на 24 ч:neutral", "До завтра 09:00:neutral", "До понедельника 09:00:neutral"}
	for limit := 2; limit <= 7; limit++ {
		i := infoOf(t, set("capabilities.buttons", "max_per_message", limit))
		for status, want := range all {
			if got := styled(buttonsOf(rootMessage(t, status), i)); !slices.Equal(got, want[:limit]) {
				t.Errorf("%s, %d: %q, want %q", status, limit, got, want[:limit])
			}
		}
		m := rootMessage(t, messages.ColourFiring)
		m.Language, m.Buttons = messages.LanguageRussian, signedButtons(t, messages.ColourFiring, "ru", snoozeSeconds)
		if got := styled(buttonsOf(m, i)); !slices.Equal(got, ru[:limit]) {
			t.Errorf("russian, %d: %q", limit, got)
		}
		short := rootMessage(t, messages.ColourFiring)
		short.Buttons[5].Label, short.Buttons[6].Label = "T", "W"
		short.Buttons[4].Label = strings.Repeat("x", maxLabel)
		if got := styled(buttonsOf(short, i)); len(got) != limit || limit < 6 && slices.ContainsFunc(got,
			func(s string) bool { return s == "T:neutral" || s == "W:neutral" }) || limit == 6 && got[5] != "T:neutral" {
			t.Errorf("short presets, %d: %q", limit, got)
		}
	}
	if got := styled(buttonsOf(rootMessage(t, messages.ColourFiring),
		infoOf(t, set("capabilities.buttons", "max_per_message", 10)))); !slices.Equal(got,
		all[messages.ColourFiring]) {
		t.Errorf("10: %q", got)
	}
}

// styled are the labels and style hints of bs as label:style.
func styled(bs []gen.Button) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, b.Label+":"+string(b.Style))
	}
	return out
}

// TestBodyShortening covers C-22.FR-7 and C-12.FR-11 within the body: the text is shortened by its Alerts first,
// with the link to the full list on the Alert Group page; a text that still does not fit keeps its line of links, its
// notices and its footer whole after its cut start, or the start of those alone; head and the commands line always
// count, and the whole body never exceeds the limit.
func TestBodyShortening(t *testing.T) {
	m := rootMessage(t, messages.ColourAcknowledged)
	for n := range 40 {
		m.Alerts.Lines = append(m.Alerts.Lines, messages.AlertLine{Text: fmt.Sprintf("pod: m%d · instance: "+
			"host-%02d.example.org:9100 · job: node", n, n)})
	}
	m.Alerts.Distinct = []messages.Distinct{{Label: "pod", Values: []string{"m1", "m2"}, More: 38}}
	limit := infoOf(t, set("limits", "max_body_length", 1000), threadOnly("all"))
	body := bodyOf(m, limit, []string{"Alice Smith"})
	if n := utf8.RuneCountInString(body); n > 1000 || !strings.HasPrefix(body, "Alice Smith\n\n") ||
		!strings.Contains(body, "[Full list in Muster]("+groupPage+")") ||
		!strings.Contains(body, "[Open in Muster]("+groupPage+") · [Runbook]("+runbook+")") ||
		!strings.Contains(body, "🔁 Reopened ×2\n\nAcknowledged by Bob Smith\n\nReply in the Thread: unack") ||
		strings.Contains(body, "m39") {
		t.Fatalf("%d characters: %q", n, body)
	}
	if got := fit(messages.Message{Lines: []string{"a"}}, 10); got != "a" {
		t.Fatalf("a short text = %q", got)
	}
	links := rootMessage(t, messages.ColourAcknowledged)
	for n := range 15 {
		links.Links = append(links.Links, messages.Link{Text: fmt.Sprintf("Link %d", n),
			URL: fmt.Sprintf("https://wiki.example.org/%d", n)})
	}
	line := messages.Layout(messages.Message{Links: links.Links}, messages.MarkupCommonMark)
	room := utf8.RuneCountInString(line) + 40
	if got := fit(links, room); utf8.RuneCountInString(got) > room || !strings.HasPrefix(got, "[Full list in Mus") ||
		!strings.Contains(got, "…\n\n"+line) {
		t.Fatalf("%d characters: %q", utf8.RuneCountInString(got), got)
	}
	if got := fit(links, 200); utf8.RuneCountInString(got) != 200 || !strings.HasPrefix(got, "[Open in Muster]") ||
		!strings.HasSuffix(got, "…") {
		t.Fatalf("a tail that does not fit = %q", got)
	}
	huge := []string{strings.Repeat("x", 600)}
	if got := bodyOf(m, infoOf(t), huge); utf8.RuneCountInString(got) > 4000 || !strings.HasPrefix(got, huge[0]) {
		t.Fatalf("a long head = %d", utf8.RuneCountInString(got))
	}
	if got := bodyOf(m, infoOf(t, set("limits", "max_body_length", 500)), huge); utf8.RuneCountInString(got) != 500 ||
		!strings.HasSuffix(got, "…") {
		t.Fatalf("a head over the limit = %q", got)
	}
	if got := bodyOf(replyMessage("a"), Info{}, nil); got != "a" || bodyLimit(Info{}) != MinBodyLength {
		t.Fatalf("no info = %q", got)
	}
	for _, tc := range []struct {
		head, tail string
		limit      int
		want       string
	}{
		{"abcdef", "", 4, "abc…"},
		{"", "tail", 3, "ta…"},
		{"abcdef", "tail", 9, "ab…\n\ntail"},
		{"abcdef", "tail", 7, "tail"},
		{"abcdef", "tail", 3, "ta…"},
		{"ab", "cd", 0, ""},
	} {
		if got := cutMiddle(tc.head, tc.tail, tc.limit); got != tc.want {
			t.Errorf("cutMiddle(%q, %q, %d) = %q, want %q", tc.head, tc.tail, tc.limit, got, tc.want)
		}
	}
}

// linked is a User target named name in Muster, with an Account link for the Connection when external is not empty,
// whose display name is display.
func linked(name, external, display string) mentions.Target {
	return mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{Name: name, ExternalID: external,
		DisplayName: display}}
}

// TestMentions covers C-22.FR-16 and AC-20: a Loud call names a User linked through the Connection by a Mention marker
// with the link's display name, escaped, and an entry of mentions — ref m1, m2…, external id and display name — and
// a User without a link by their display name as text, escaped, which mentions nobody; each once, in the order of the
// targets, on one line. A link without a display name takes the Muster name, a long one is cut to 200 characters, and
// a Quiet call names nobody.
func TestMentions(t *testing.T) {
	long := strings.Repeat("ж", 250)
	c := delivery.Call{Loudness: groups.Loud, Targets: []mentions.Target{
		linked("Alice Smith", "u-alice", "Alice Smith"), linked("@bob *b*", "", ""),
		linked("Alice S.", "u-alice", "Alice"), linked("Carol", "u-carol", "Carol ]evil[ (x)\n@all"),
		{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneChannel}, {Kind: mentions.TargetGroup, Group: "db"},
		{Kind: mentions.TargetUser}, linked("", "", ""), linked("@bob *b*", "", ""),
		linked("Dave Doe", "u-dave", "  "), linked("Erin", "u-erin", long),
		linked("Frank", strings.Repeat("f", 513), "Frank F."), linked("", "u-nobody", "")}}
	head, list := mentionsOf(c)
	want := "[@Alice Smith](muster-mention:m1) @\u200bbob \\*b\\* " +
		"[@Carol \\]evil\\[ (x) @\u200ball](muster-mention:m2) [@Dave Doe](muster-mention:m3) " +
		"[@" + strings.Repeat("ж", 199) + "…](muster-mention:m4) Frank"
	if len(head) != 1 || head[0] != want {
		t.Fatalf("line\n%q\nwant\n%q", head, want)
	}
	got, _ := json.Marshal(list)
	wantList := `[{"display_name":"Alice Smith","external_id":"u-alice","ref":"m1"},` +
		`{"display_name":"Carol ]evil[ (x) @all","external_id":"u-carol","ref":"m2"},` +
		`{"display_name":"Dave Doe","external_id":"u-dave","ref":"m3"},` +
		`{"display_name":"` + strings.Repeat("ж", 199) + `…","external_id":"u-erin","ref":"m4"}]`
	if string(got) != wantList {
		t.Fatalf("mentions\n%s\nwant\n%s", got, wantList)
	}
	for _, m := range list {
		if b, _ := json.Marshal(m); visit(t, bridgeSchema(t, "Mention"), b) != nil {
			t.Errorf("%s does not match Mention", b)
		}
	}
	c.Loudness = groups.Quiet
	if head, list := mentionsOf(c); head != nil || list == nil || len(list) != 0 {
		t.Fatalf("quiet = %q %v", head, list)
	}
	if head, list := mentionsOf(delivery.Call{Loudness: groups.Loud}); head != nil || list == nil || len(list) != 0 {
		t.Fatalf("nobody = %q %v", head, list)
	}
}

// TestFooterOfTheConnection covers C-22.FR-16 and C-12.FR-12: the body's footer names the user as the identity space
// of the Destination's Connection does — by the display name of their Bridge link there — and by the Muster display
// name without a link there.
func TestFooterOfTheConnection(t *testing.T) {
	m := rootMessage(t, messages.ColourAcknowledged)
	m.Footers["bridge:7"] = "Acknowledged by Bob B."
	if body := content(withFooterIn(m, 7), infoOf(t), nil).Body; !strings.HasSuffix(body,
		"\n\nAcknowledged by Bob B.") {
		t.Fatalf("linked through the connection: %q", body)
	}
	if body := content(withFooterIn(m, 8), infoOf(t), nil).Body; !strings.HasSuffix(body,
		"\n\nAcknowledged by Bob Smith") {
		t.Fatalf("not linked through the connection: %q", body)
	}
	// A shortened body keeps the footer's marker whole, with its entry (C-12.FR-11, FR-12).
	m.Footers["bridge:7"] = "Acknowledged by " + templates.MentionToken(templates.Token{Name: messages.FooterUserToken})
	m.FooterUsers = map[string]messages.FooterUser{"bridge:7": {Name: "Bob Smith", ExternalID: "u-bob",
		DisplayName: "Bob B."}}
	m.Summary = strings.Repeat("s ", 1000)
	i := infoOf(t, set("limits", "max_body_length", 500))
	marked, list := withFooterMention(m, 7, i, []gen.Mention{})
	body := content(marked, i, nil).Body
	if !strings.HasSuffix(body, "\n\nAcknowledged by [@Bob B.](muster-mention:m1)") || strings.Contains(body, "s s") ||
		utf8.RuneCountInString(body) > 500 || len(list) != 1 || list[0].ExternalId != "u-bob" {
		t.Fatalf("a cut body %q, %+v", body, list)
	}
}
