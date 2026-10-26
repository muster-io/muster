// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/links"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

var update = flag.Bool("update", false, "rewrite the golden requests under testdata/layout")

const (
	layoutGroup    = "AG0000000000A1"
	layoutGroupURL = "http://localhost:8080/alert-groups/" + layoutGroup
	layoutChannel  = "C0FAKEALERTS"
	// acLabel is the label value of C-24.AC-6, and acEscaped what Slack receives of it.
	acLabel   = "@channel <!here> <b>x</b> *y* <https://evil.example|z> &"
	acEscaped = "@\u200bchannel &lt;!here&gt; &lt;b&gt;x&lt;/b&gt; *\u200by*\u200b &lt;https://evil.example|z&gt; &amp;"
)

var layoutNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

var (
	hereTarget    = mentions.Target{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneHere}
	channelTarget = mentions.Target{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneChannel}
	aliceTarget   = mentions.Target{Kind: mentions.TargetUser,
		User: &mentions.User{PublicID: "US0000000000U1", Name: "Alice *Smith*", Login: "alice"}}
)

// layoutKeys sign action ids with a fixed key, SHA-256 standing in for its HMAC, so that the goldens stay put.
type layoutKeys struct{}

const layoutKeyID = "k-0011223344556677"

func (layoutKeys) Sign(_ keyring.Purpose, msg []byte) ([]byte, string, error) {
	mac := sha256.Sum256(msg)
	return mac[:], layoutKeyID, nil
}

func (layoutKeys) VerifyPrefix(keyring.Purpose, string, []byte, []byte) (bool, error) {
	return true, nil
}

func (layoutKeys) KeyIDs() []string { return []string{layoutKeyID} }

// layoutEnv renders messages as delivery renders them for a Slack Destination, its buttons signed.
type layoutEnv struct {
	r    *messages.Renderer
	keys buttons.Keys
}

func newLayoutEnv(t *testing.T) layoutEnv {
	t.Helper()
	c := clock.NewManual(layoutNow)
	return layoutEnv{keys: layoutKeys{}, r: messages.New(messages.Config{OrgID: 1, PublicURL: "http://localhost:8080",
		Business: c, Real: c, Keys: layoutKeys{}, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo)})}
}

// layoutSource is Alert Group #12 in a status, with the label value of C-24.AC-6, one firing and one resolved Alert, a
// runbook link, a Reopen and a Route with three Snooze durations.
func layoutSource(status string) *messages.Source {
	until := layoutNow.Add(4 * time.Hour)
	alert := func(pod string, firing bool) messages.SourceAlert {
		return messages.SourceAlert{Fingerprint: "f-" + pod, Labels: map[string]string{"alertname": "DiskFull",
			"team": "sl", "cluster": "a", "note": acLabel, "pod": pod}, StartsAt: layoutNow.Add(-10 * time.Minute),
			Firing: firing}
	}
	return &messages.Source{ID: 1, PublicID: layoutGroup, Number: 12, Title: "DiskFull", Summary: "Disk almost full",
		Status: status, SeverityLevel: "critical", TimeZone: "UTC", WorkdayStart: "09:00", WeekStart: "monday",
		StartedAt: layoutNow.Add(-10 * time.Minute), KeyLabels: []string{"alertname"},
		KeyValues:         map[string]string{"alertname": "DiskFull"},
		CommonLabels:      map[string]string{"alertname": "DiskFull", "team": "sl", "cluster": "a", "note": acLabel},
		CommonAnnotations: map[string]string{"summary": "Disk almost full"},
		FiringCount:       1, ReopenCount: 1, TotalAlerts: 2,
		Route: messages.RouteRef{ID: 1, PublicID: "RT0000000000R1", Name: "sl", Language: "en",
			SnoozeDurations: []int64{3600, 14400, 86400}},
		Owner: "Alice Smith", SnoozedBy: "Alice Smith", SnoozeUntil: &until, ResolvedByKind: "user",
		ResolvedBy: "Alice Smith",
		Alerts:     []messages.SourceAlert{alert("i1", true), alert("i2", false)},
		Links:      []links.Link{{Kind: links.KindRunbook, URL: "https://runbooks.example.org/disk?a=1&b=2"}}}
}

// root is the Root message of src in mrkdwn.
func (e layoutEnv) root(t *testing.T, src *messages.Source) messages.Message {
	t.Helper()
	out := e.r.Root(src, messages.MarkupMrkdwn)
	if out.Failure != nil {
		t.Fatalf("render: %+v", out.Failure)
	}
	return out.Message
}

// reminder is a Reminder in lang, with "Still on it" and "Unack" signed as a Thread reply's buttons.
func (e layoutEnv) reminder(t *testing.T, lang string) messages.Message {
	t.Helper()
	m := messages.Message{Kind: messages.KindReply, Language: lang, TimeZone: "UTC",
		Colour: messages.ColourAcknowledged,
		Lines:  []string{messages.T(lang, "reply.reminder", messages.Args{"user": "Alice Smith"})}}
	for _, c := range []string{buttons.CommandStillOnIt, buttons.CommandUnacknowledge} {
		id, kid, err := buttons.Sign(e.keys, buttons.Action{Subject: buttons.SubjectReply, PublicID: layoutGroup,
			Command: c})
		if err != nil {
			t.Fatal(err)
		}
		label := map[string]string{buttons.CommandStillOnIt: "button.stillOnIt",
			buttons.CommandUnacknowledge: "button.unacknowledge"}[c]
		m.Buttons = append(m.Buttons, messages.Button{Command: c, Label: messages.T(lang, label, nil), ActionID: id,
			KeyID: kid})
	}
	return m
}

// newAlertsReply is the Thread reply of a new Alert, whose label carries the value of C-24.AC-6.
func newAlertsReply() messages.Message {
	return messages.Message{Kind: messages.KindReply, Language: "en", TimeZone: "UTC", Colour: messages.ColourFiring,
		Buttons: []messages.Button{}, Lines: []string{"New alerts (1):", "• pod: i3, note: " + messages.Value(acLabel)}}
}

func testLook() look {
	return look{channel: layoutChannel, space: "slack:3", version: "1.2.3"}
}

func plainLook() look {
	l := testLook()
	l.plain = true
	return l
}

// requestJSON is the body of the request as Slack receives it, indented.
func requestJSON(t *testing.T, o outgoing) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(o.args()); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// golden compares a request with its file under testdata/layout, which -update rewrites.
func golden(t *testing.T, name string, o outgoing) {
	t.Helper()
	got := requestJSON(t, o)
	path := filepath.Join("testdata", "layout", name+".json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s:\n%s", name, path, got)
	}
}

// allBlocks are the blocks of a request, the top ones first.
func allBlocks(o outgoing) []block {
	out := slices.Clone(o.Blocks)
	for _, a := range o.Attachments {
		out = append(out, a.Blocks...)
	}
	return out
}

// sections are the texts of the sections of a request, by block id.
func sections(o outgoing) map[string]string {
	out := map[string]string{}
	for _, b := range allBlocks(o) {
		if b.Type == typeSection && b.Text != nil {
			out[b.BlockID] = b.Text.Text
		}
	}
	return out
}

// unbroken reports whether every `&` of the mrkdwn s starts an entity and every `<` is closed by a `>`.
func unbroken(s string) bool {
	for i := range len(s) {
		switch s[i] {
		case '&':
			if entityLen(s[i:]) == 0 {
				return false
			}
		case '<':
			if !strings.Contains(s[i:], ">") {
				return false
			}
		}
	}
	return true
}

// slackRules checks a request against the rules of Slack that Muster relies on: blocks and attachments named and
// unfurling off (F-083, F-085); only section, context and actions blocks (F-081, F-086), at most 50, with unique block
// ids; a section's text in mrkdwn, not empty, within 3,000 code points and never cut inside an entity or a link; a
// context of at most 10 texts and no image; an actions block of at most 25 elements with unique action ids, buttons of
// at most 75 code points whose value is a signed action id of at most 150 bytes, at most one `primary` in the message
// and no other style (F-074, F-076); and a `text` under 4,000 bytes in the stored form (F-078).
func slackRules(t *testing.T, name string, o outgoing) {
	t.Helper()
	a := o.args()
	if a["unfurl_links"] != false || a["unfurl_media"] != false {
		t.Errorf("%s: unfurling is not off: %v", name, a)
	}
	if _, ok := a["blocks"].([]block); !ok {
		t.Errorf("%s: blocks %T", name, a["blocks"])
	}
	if _, ok := a["attachments"].([]attachment); !ok {
		t.Errorf("%s: attachments %T", name, a["attachments"])
	}
	if o.Channel != layoutChannel || o.Text == "" || storedBytes(o.Text) >= storedTextLimit {
		t.Errorf("%s: channel %q, text %q (%d stored bytes)", name, o.Channel, o.Text, storedBytes(o.Text))
	}
	all := allBlocks(o)
	if len(all) > 50 {
		t.Errorf("%s: %d blocks", name, len(all))
	}
	ids, primaries := map[string]bool{}, 0
	signed := func(v string) bool { return buttons.LooksSigned(v) && len(v) <= valueLimit }
	label := func(s string) bool { return s != "" && utf8.RuneCountInString(s) <= labelLimit }
	for _, b := range all {
		if ids[b.BlockID] || b.BlockID == "" {
			t.Errorf("%s: block id %q again", name, b.BlockID)
		}
		ids[b.BlockID] = true
		switch b.Type {
		case typeSection:
			if b.Text == nil || b.Text.Type != typeMrkdwn || b.Text.Text == "" ||
				utf8.RuneCountInString(b.Text.Text) > LengthLimit || !unbroken(b.Text.Text) || len(b.Elements) > 0 {
				t.Errorf("%s: section %+v", name, b)
			}
		case typeContext:
			if len(b.Elements) == 0 || len(b.Elements) > 10 {
				t.Errorf("%s: context %+v", name, b)
			}
			for _, e := range b.Elements {
				if tx, ok := e.(textObject); !ok || tx.Type != typePlainText {
					t.Errorf("%s: a context element %#v", name, e)
				}
			}
		case typeActions:
			if len(b.Elements) == 0 || len(b.Elements) > 25 || b.BlockID != blockActions {
				t.Errorf("%s: actions %+v", name, b)
			}
			actions := map[string]bool{}
			for _, e := range b.Elements {
				var id string
				switch e := e.(type) {
				case button:
					id = e.ActionID
					if e.Type != typeButton || !label(e.Text.Text) || e.Text.Type != typePlainText || !signed(e.Value) ||
						(e.Style != "" && e.Style != stylePrimary) {
						t.Errorf("%s: button %+v", name, e)
					}
					if e.Style == stylePrimary {
						primaries++
					}
				case *staticSelect:
					id = e.ActionID
					if e.Type != typeStaticSelect || !label(e.Placeholder.Text) || len(e.Options) == 0 ||
						len(e.Options) > maxOptions {
						t.Errorf("%s: menu %+v", name, e)
					}
					for _, op := range e.Options {
						if !label(op.Text.Text) || !signed(op.Value) {
							t.Errorf("%s: option %+v", name, op)
						}
					}
				default:
					t.Errorf("%s: an element %#v", name, e)
				}
				if actions[id] || id == "" {
					t.Errorf("%s: action id %q again", name, id)
				}
				actions[id] = true
			}
		default:
			t.Errorf("%s: a block of type %q", name, b.Type)
		}
	}
	if primaries > 1 {
		t.Errorf("%s: %d primary buttons", name, primaries)
	}
}

// TestLayoutGolden is the request of every kind of message, as C-24.FR-4 to FR-6 lay them out, against its golden
// file and the rules of Slack: a Root message per status, Loud and Quiet, an edit and a final edit, a Reminder, Thread
// replies, a Storm summary, a test message, and the plain text after Slack rejected the markup.
func TestLayoutGolden(t *testing.T) {
	for _, c := range layoutCases(t) {
		t.Run(c.name, func(t *testing.T) {
			slackRules(t, c.name, c.o)
			golden(t, c.name, c.o)
		})
	}
}

// layoutCase is a request of a golden file: an edit names the `ts` 1.000001 and a Thread reply its `thread_ts`.
type layoutCase struct {
	name string
	o    outgoing
}

// layoutCases are the requests of the golden files: the Root message in each status, Loud and Quiet, its edits and
// final edit, a Reminder, Thread replies, a Storm summary, a test message and messages as text alone.
func layoutCases(t *testing.T) []layoutCase {
	t.Helper()
	e := newLayoutEnv(t)
	l := testLook()
	loud := []string{"<!here>"}
	edit := func(o outgoing) outgoing { o.TS = "1.000001"; return o }
	reply := func(o outgoing) outgoing { o.ThreadTS = "1.000001"; return o }
	firing := e.root(t, layoutSource(messages.ColourFiring))
	acked := e.root(t, layoutSource(messages.ColourAcknowledged))
	test := e.r.TestRoot(layoutSource(messages.ColourFiring), messages.MarkupMrkdwn, "DS0000000000D1").Message
	storm := e.r.Storm("RT0000000000R1", "sl", "en", 12, 0)
	return []layoutCase{
		{"root_firing_loud", layout(firing, l, loud)},
		{"root_firing_quiet", layout(messages.DeletedNote(firing, layoutNow.Add(-time.Minute)), l, nil)},
		{"root_acknowledged_edit", edit(layout(acked, l, nil))},
		{"root_snoozed", edit(layout(e.root(t, layoutSource(messages.ColourSnoozed)), l, nil))},
		{"root_resolved", edit(layout(e.root(t, layoutSource(messages.ColourResolved)), l, nil))},
		{"final_edit", edit(layout(messages.FinalEdit(acked, layoutGroupURL), l, nil))},
		{"reminder", reply(layout(e.reminder(t, "en"), l, mentionText([]mentions.Target{aliceTarget}, nil)))},
		{"reply_quiet", reply(layout(newAlertsReply(), l, nil))},
		{"reply_loud", reply(layout(newAlertsReply(), l, mentionText([]mentions.Target{hereTarget, aliceTarget},
			nil)))},
		{"storm_summary", layout(storm, l, loud)},
		{"test_message", testRequest(test, l)},
		{"plain_root", layout(firing, plainLook(), loud)},
		{"plain_reply", reply(layout(newAlertsReply(), plainLook(), loud))},
	}
}

// TestRootStructure is C-24.FR-4 and C-24.AC-5: `text` and the top section are the summary line with the Mentions of
// a Loud message; one attachment in the colour of the status holds the heading linked to the Alert Group page, the
// body whose line of links starts with "Open in Muster" and ends it with the notices and the footer, the context
// "Muster v<version>" with no image, and the actions; unfurling is off, and blocks and attachments are always named.
func TestRootStructure(t *testing.T) {
	e := newLayoutEnv(t)
	o := layout(e.root(t, layoutSource(messages.ColourFiring)), testLook(), []string{"<!here>"})
	if o.Text != "🔴 #12 DiskFull <!here>" || len(o.Blocks) != 1 || o.Blocks[0].Type != typeSection ||
		o.Blocks[0].Text.Text != "🔴 #12 DiskFull\n<!here>" || len(o.Attachments) != 1 {
		t.Fatalf("request = %s", requestJSON(t, o))
	}
	a := o.Attachments[0]
	types := make([]string, len(a.Blocks))
	for i, b := range a.Blocks {
		types[i] = b.Type
	}
	if a.Color != "#d32f2f" || a.Fallback != "🔴 #12 DiskFull" ||
		!slices.Equal(types, []string{typeSection, typeSection, typeContext, typeActions}) {
		t.Errorf("attachment = %+v", a)
	}
	if h := a.Blocks[0].Text.Text; h != "*<"+layoutGroupURL+"|#12 DiskFull>*" {
		t.Errorf("heading = %q", h)
	}
	body := strings.Split(a.Blocks[1].Text.Text, "\n")
	n := len(body)
	if n < 3 || body[n-2] != "<"+layoutGroupURL+"|Open in Muster> · "+
		"<https://runbooks.example.org/disk?a=1&amp;b=2|Runbook>" || body[n-1] != "🔁 Reopened ×1" {
		t.Errorf("body = %q", body)
	}
	if ctx := a.Blocks[2].Elements; len(ctx) != 1 || ctx[0] != plainText("Muster v1.2.3") {
		t.Errorf("context = %#v", ctx)
	}
	args := o.args()
	if args["unfurl_links"] != false || args["unfurl_media"] != false || args["channel"] != layoutChannel {
		t.Errorf("args = %v", args)
	}
	raw := requestJSON(t, o)
	for _, banned := range []string{`"rich_text"`, `"markdown"`, `"image"`, `"danger"`, `"reply_broadcast"`} {
		if strings.Contains(raw, banned) {
			t.Errorf("the request holds %s", banned)
		}
	}

	// Each status has its colour and emoji; an unknown colour has neither.
	for status, want := range map[string][2]string{messages.ColourAcknowledged: {"#f57c00", "🟠"},
		messages.ColourSnoozed: {"#9e9e9e", "⚪"}, messages.ColourResolved: {"#388e3c", "🟢"}} {
		o := layout(e.root(t, layoutSource(status)), testLook(), nil)
		if o.Attachments[0].Color != want[0] || o.Text != want[1]+" #12 DiskFull" {
			t.Errorf("%s: %q %q", status, o.Attachments[0].Color, o.Text)
		}
	}
	m := e.root(t, layoutSource(messages.ColourFiring))
	m.Colour = "unknown"
	if o := layout(m, testLook(), nil); o.Attachments[0].Color != "" || o.Text != "#12 DiskFull" {
		t.Errorf("unknown colour: %q %q", o.Attachments[0].Color, o.Text)
	}

	// A Thread reply and a Storm summary: their text in a section, a short `text`, no attachment.
	for _, m := range []messages.Message{newAlertsReply(), e.r.Storm("RT0000000000R1", "sl", "en", 12, 0)} {
		o := layout(m, testLook(), nil)
		if len(o.Attachments) != 0 || len(o.Blocks) != 1 || o.Blocks[0].BlockID != blockText ||
			utf8.RuneCountInString(o.Text) > textTitleLimit || strings.Contains(o.Text, "\n") {
			t.Errorf("reply = %s", requestJSON(t, o))
		}
	}
	if o := layout(newAlertsReply(), testLook(), nil); o.Text != "New alerts (1):" {
		t.Errorf("reply text = %q", o.Text)
	}

	// The footer names a user without a user id of Slack's form by their display name, escaped (C-12.FR-12).
	src := layoutSource(messages.ColourAcknowledged)
	src.Owner, src.FooterUsers = "alice_s", map[string]mentions.User{"slack:3": {Name: "alice_s", ExternalID: "u-1"}}
	body = strings.Split(sections(layout(e.root(t, src), testLook(), nil))[blockBody], "\n")
	if got := body[len(body)-1]; got != "Acknowledged by alice_\u200bs" {
		t.Errorf("footer = %q", got)
	}
}

// shownElement is an element of an actions block as a person sees it: its action id, style and text, and the
// options of a menu.
type shownElement struct {
	id, style, text string
	options         []string
}

// actionsShown are the elements of the actions block of a request with their values, none without one.
func actionsShown(t *testing.T, o outgoing) ([]shownElement, []string) {
	t.Helper()
	var out []shownElement
	var values []string
	for _, b := range allBlocks(o) {
		if b.Type != typeActions {
			continue
		}
		for _, e := range b.Elements {
			switch e := e.(type) {
			case button:
				out = append(out, shownElement{id: e.ActionID, style: e.Style, text: e.Text.Text})
				values = append(values, e.Value)
			case *staticSelect:
				s := shownElement{id: e.ActionID, text: e.Placeholder.Text}
				for _, op := range e.Options {
					s.options = append(s.options, op.Text.Text)
					values = append(values, op.Value)
				}
				out = append(out, s)
			default:
				t.Fatalf("element %#v", e)
			}
		}
	}
	return out, values
}

// TestButtonsByStatus is C-24.FR-5 and C-24.AC-5: the buttons of each status, one `primary` — Ack on a firing and a
// snoozed Root message, Resolve on an acknowledged one, "Still on it" on a Reminder — and the Snooze menu whose
// options are the Route's durations as a Text command writes them and then the Snooze presets, in the Route's
// language; every value is the signed action id of its button, in the order of the buttons.
func TestButtonsByStatus(t *testing.T) {
	e := newLayoutEnv(t)
	options := []string{"1h", "4h", "24h", "Until tomorrow 09:00", "Until Monday 09:00"}
	ru := layoutSource(messages.ColourFiring)
	ru.Route.Language = "ru"
	cases := []struct {
		name string
		m    messages.Message
		want []shownElement
	}{
		{"firing", e.root(t, layoutSource(messages.ColourFiring)), []shownElement{{"ack", "primary", "Ack", nil},
			{"resolve", "", "Resolve", nil}, {"snooze", "", "Snooze…", options}}},
		{"acknowledged", e.root(t, layoutSource(messages.ColourAcknowledged)), []shownElement{
			{"unack", "", "Unack", nil}, {"resolve", "primary", "Resolve", nil}, {"snooze", "", "Snooze…", options}}},
		{"snoozed", e.root(t, layoutSource(messages.ColourSnoozed)), []shownElement{{"ack", "primary", "Ack", nil},
			{"unsnooze", "", "Unsnooze", nil}, {"resolve", "", "Resolve", nil}}},
		{"resolved", e.root(t, layoutSource(messages.ColourResolved)), nil},
		{"reminder", e.reminder(t, "en"), []shownElement{{"still_on_it", "primary", "Still on it", nil},
			{"unack", "", "Unack", nil}}},
		{"firing_ru", e.root(t, ru), []shownElement{{"ack", "primary", "Подтвердить", nil},
			{"resolve", "", "Закрыть", nil}, {"snooze", "", "Отложить…", []string{"1ч", "4ч", "24ч",
				"До завтра 09:00", "До понедельника 09:00"}}}},
		{"reminder_ru", e.reminder(t, "ru"), []shownElement{{"still_on_it", "primary", "Ещё работаю", nil},
			{"unack", "", "Снять подтверждение", nil}}},
	}
	for _, c := range cases {
		o := layout(c.m, testLook(), nil)
		slackRules(t, c.name, o)
		got, values := actionsShown(t, o)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: elements %v, want %v", c.name, got, c.want)
		}
		ids := make([]string, 0, len(c.m.Buttons))
		for _, b := range c.m.Buttons {
			ids = append(ids, b.ActionID)
		}
		if !slices.Equal(values, ids) {
			t.Errorf("%s: values %v, want the action ids %v", c.name, values, ids)
		}
	}
	// A Reminder carries its buttons in a top-level actions block, a Root message in its attachment.
	if o := layout(e.reminder(t, "en"), testLook(), nil); len(o.Blocks) != 2 || o.Blocks[1].BlockID != blockActions {
		t.Errorf("reminder = %s", requestJSON(t, o))
	}
}

// TestNoMenuWithoutDurations: a Route without Snooze durations offers neither the durations nor the presets.
func TestNoMenuWithoutDurations(t *testing.T) {
	e := newLayoutEnv(t)
	for status, want := range map[string]string{messages.ColourFiring: "[{ack primary Ack []} {resolve  Resolve []}]",
		messages.ColourAcknowledged: "[{unack  Unack []} {resolve primary Resolve []}]"} {
		src := layoutSource(status)
		src.Route.SnoozeDurations = nil
		got, _ := actionsShown(t, layout(e.root(t, src), testLook(), nil))
		if fmt.Sprint(got) != want {
			t.Errorf("%s: %v", status, got)
		}
	}
}

// TestUnusableButtons: a button without a signed action id that fits an option's value, or of a Command Slack has no
// action id for, is left out, and a message left without buttons has no actions block; the menu keeps the presets
// within Block Kit's 100 options, and an option whose duration is unknown takes its button's label.
func TestUnusableButtons(t *testing.T) {
	e := newLayoutEnv(t)
	m := e.root(t, layoutSource(messages.ColourFiring))
	m.Buttons[0].ActionID = ""
	m.Buttons[1].ActionID = strings.Repeat("a", valueLimit+1)
	m.Buttons = append(m.Buttons, messages.Button{Command: "unresolve", Label: "Unresolve", ActionID: "x"})
	got, _ := actionsShown(t, layout(m, testLook(), nil))
	if len(got) != 1 || got[0].id != "snooze" {
		t.Errorf("elements = %v", got)
	}
	if _, ok := actionsBlock(messages.Message{Buttons: m.Buttons[:2]}); ok {
		t.Error("a message without usable buttons has an actions block")
	}

	src := layoutSource(messages.ColourFiring)
	src.Route.SnoozeDurations = make([]int64, 120)
	for i := range src.Route.SnoozeDurations {
		src.Route.SnoozeDurations[i] = int64(i+1) * 60
	}
	m = e.root(t, src)
	m.SnoozeSeconds = m.SnoozeSeconds[:1]
	got, _ = actionsShown(t, layout(m, testLook(), nil))
	opts := got[2].options
	if len(opts) != maxOptions || opts[0] != "1m" || opts[1] != "Snooze 2 min" ||
		!slices.Equal(opts[maxOptions-2:], []string{"Until tomorrow 09:00", "Until Monday 09:00"}) {
		t.Errorf("options = %d: %v … %v", len(opts), opts[:3], opts[len(opts)-2:])
	}

	// Labels are cut to 75 code points.
	m = e.root(t, layoutSource(messages.ColourFiring))
	m.Buttons[0].Label = strings.Repeat("я", 100)
	if got, _ := actionsShown(t, layout(m, testLook(), nil)); utf8.RuneCountInString(got[0].text) != labelLimit ||
		!strings.HasSuffix(got[0].text, ellipsis) {
		t.Errorf("label = %q", got[0].text)
	}
}

// TestLimits is C-24.FR-4 and C-24.AC-7: the body of an Alert Group with 40 Alerts keeps to 3,000 code points and ends
// with the line of links, the notices and the footer, its Alerts as the distinct values with "+30 more" and "Full list
// in Muster"; the title of 200 emoji is cut to 60 code points in `text`, which stays under 4,000 bytes with every emoji
// counted as its shortcode, while the top section keeps it whole; the Mentions beyond 1,000 bytes of `text` stay in
// the top section; and a title of 4 KB of `&` keeps every section within its limit, never cut inside an entity.
func TestLimits(t *testing.T) {
	e := newLayoutEnv(t)
	src := layoutSource(messages.ColourAcknowledged)
	src.ReopenCount, src.TotalAlerts = 2, 40
	src.CommonAnnotations = map[string]string{"summary": "Disk almost full", "details": strings.Repeat("d", 1200),
		"impact": strings.Repeat("i", 1200), "notes": strings.Repeat("n", 1200)}
	src.Alerts = nil
	for i := range 40 {
		src.Alerts = append(src.Alerts, messages.SourceAlert{Fingerprint: fmt.Sprint("f", i),
			Labels: map[string]string{"alertname": "DiskFull", "team": "sl", "pod": fmt.Sprint("p", i+1)},
			Firing: true})
	}
	m := e.root(t, src)
	if messages.Length(m, messages.MarkupMrkdwn) <= LengthLimit {
		t.Fatal("the message is not longer than a section")
	}
	o := layout(m, testLook(), nil)
	slackRules(t, "40 alerts", o)
	body := sections(o)[blockBody]
	lines := strings.Split(body, "\n")
	n := len(lines)
	if utf8.RuneCountInString(body) > LengthLimit || n < 4 ||
		!strings.HasPrefix(lines[n-3], "<"+layoutGroupURL+"|Open in Muster> · ") ||
		lines[n-2] != "🔁 Reopened ×2" || lines[n-1] != "Acknowledged by Alice Smith" {
		t.Errorf("body (%d code points) =\n%s", utf8.RuneCountInString(body), body)
	}
	if !slices.Contains(lines, "pod: p1, p2, p3, p4, p5, p6, p7, p8, p9, p10 +30 more") ||
		!slices.Contains(lines, "<"+layoutGroupURL+"|Full list in Muster>") || strings.Contains(body, "ddd") {
		t.Errorf("alerts in the body =\n%s", body)
	}

	// The title of 200 emoji.
	src = layoutSource(messages.ColourFiring)
	src.Title = strings.Repeat("🔥", 200)
	o = layout(e.root(t, src), testLook(), nil)
	slackRules(t, "emoji", o)
	title := strings.TrimPrefix(o.Text, "🔴 #12 ")
	stored := strings.NewReplacer("🔥", ":fire:", "🔴", ":red_circle:").Replace(o.Text)
	if utf8.RuneCountInString(title) != textTitleLimit || !strings.HasSuffix(title, ellipsis) || len(stored) >= 4000 ||
		storedBytes(o.Text) >= storedTextLimit {
		t.Errorf("text = %q (%d bytes as shortcodes)", o.Text, len(stored))
	}
	if top := o.Blocks[0].Text.Text; top != "🔴 #12 "+src.Title {
		t.Errorf("top = %q", top)
	}

	// Mentions of 30 Users with names of 60 bytes.
	var names []string
	for i := range 30 {
		names = append(names, fmt.Sprintf("U%02d%s", i, strings.Repeat("x", 57)))
	}
	o = layout(e.root(t, layoutSource(messages.ColourFiring)), testLook(), names)
	slackRules(t, "mentions", o)
	if want := "🔴 #12 DiskFull " + strings.Join(names[:16], " "); o.Text != want {
		t.Errorf("text = %q", o.Text)
	}
	if top := o.Blocks[0].Text.Text; top != "🔴 #12 DiskFull\n"+strings.Join(names, " ") {
		t.Errorf("top = %q", top)
	}
	if strings.Contains(requestJSON(t, outgoing{Attachments: o.Attachments}), "U00") {
		t.Error("a Mention in the attachment")
	}

	// A title of 4 KB of `&`, a Mention that the top section has no room for, and a test mark.
	src = layoutSource(messages.ColourFiring)
	src.Title = strings.Repeat("&", 5000)
	l := testLook()
	l.mark = "🧪 Test message"
	// The least head, the title cut as in `text`, is 317 code points: 2,682 are left for the Mentions.
	o = layout(e.root(t, src), l, []string{strings.Repeat("m", 2680), "<!here>"})
	slackRules(t, "ampersands", o)
	top := o.Blocks[0].Text.Text
	if !strings.HasPrefix(top, "🧪 Test message\n🔴 #12 &amp;") || !strings.HasSuffix(top, "&amp;…\n"+
		strings.Repeat("m", 2680)) || strings.Contains(top, "<!here>") || utf8.RuneCountInString(top) > LengthLimit {
		t.Errorf("top = %.80q … %.40q", top, top[len(top)-40:])
	}
	if h := sections(o)[blockHeading]; !strings.HasSuffix(h, "&amp;…>*") {
		t.Errorf("heading ends %q", h[len(h)-20:])
	}

	// A line of links, notices and footer that leaves no room keeps them alone, and one longer than a section is cut
	// at its end.
	m = messages.Message{Kind: messages.KindRoot, Language: "en", Colour: messages.ColourFiring,
		Heading: &messages.Heading{Number: 12, Title: "DiskFull", URL: layoutGroupURL},
		Alerts:  &messages.AlertList{FullList: &messages.Link{Text: "Full list in Muster", URL: layoutGroupURL}},
		Notices: []string{strings.Repeat("n", 2990)}}
	if body := sections(layout(m, testLook(), nil))[blockBody]; body != m.Notices[0] {
		t.Errorf("no room: %.40q", body)
	}
	m.Notices = []string{strings.Repeat("n", 3500)}
	if body := sections(layout(m, testLook(), nil))[blockBody]; body != strings.Repeat("n", 2999)+ellipsis {
		t.Errorf("a long tail: %.40q (%d)", body, utf8.RuneCountInString(body))
	}
}

// TestEscapingInLayout is C-24.FR-7 and C-24.AC-6: the label value reaches Slack as literal text — `&`, `<` and `>` as
// entities, a zero-width space after `@` and `*` — in a Root message and in a Thread reply, and no section holds a
// Mention, a link or emphasis of it.
func TestEscapingInLayout(t *testing.T) {
	e := newLayoutEnv(t)
	for name, o := range map[string]outgoing{
		"root":  layout(e.root(t, layoutSource(messages.ColourFiring)), testLook(), nil),
		"reply": layout(newAlertsReply(), testLook(), nil),
		"plain": layout(e.root(t, layoutSource(messages.ColourFiring)), plainLook(), nil),
	} {
		all := o.Text
		for _, s := range sections(o) {
			all += "\n" + s
		}
		if !strings.Contains(all, "note: "+acEscaped) {
			t.Errorf("%s: the label value is not escaped:\n%s", name, all)
		}
		for _, banned := range []string{"<!", "<@", "<https://evil", "*y*", "@channel"} {
			if strings.Contains(all, banned) {
				t.Errorf("%s: %q in\n%s", name, banned, all)
			}
		}
	}
}

// TestMentions is C-24.FR-6 and C-24.AC-15: a Loud Root message carries `<!here>` or `<!channel>` and a User without an
// Account link by their display name, escaped, in `text` and in the top section and never in the attachment; a
// Quiet call, every edit, mentions nobody; template tokens of everyone in the chat join the Mentions of a Loud message
// and are text where they stand, and a literal Mention written by a template notifies nobody.
func TestMentions(t *testing.T) {
	e := newLayoutEnv(t)
	targets := []mentions.Target{hereTarget, channelTarget, aliceTarget, aliceTarget,
		{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneAll}, {Kind: mentions.TargetGroup, Group: "oncall"},
		{Kind: mentions.TargetUser}}
	tokens := []templates.Token{{Name: templates.MentionHere}, {Name: templates.MentionAll},
		{Name: templates.MentionOwner}, {Name: templates.MentionGroup, Group: "dba"}}
	got := mentionText(targets, tokens)
	if want := []string{"<!here>", "<!channel>", "Alice *\u200bSmith*\u200b"}; !slices.Equal(got, want) {
		t.Errorf("mentions = %q, want %q", got, want)
	}

	conn := int64(3)
	d := delivery.Destination{ID: 1, PublicID: "DS0000000000D1", Type: delivery.TypeBridge, Connection: &conn}
	loud := delivery.Call{Destination: d, Loudness: groups.Loud, Targets: []mentions.Target{hereTarget, aliceTarget}}
	quiet := loud
	quiet.Loudness = groups.Quiet
	m := e.root(t, layoutSource(messages.ColourFiring))
	o := layout(m, testLook(), callMentions(loud, m))
	if o.Text != "🔴 #12 DiskFull <!here> Alice *\u200bSmith*\u200b" ||
		o.Blocks[0].Text.Text != "🔴 #12 DiskFull\n<!here> Alice *\u200bSmith*\u200b" {
		t.Errorf("loud = %q, %q", o.Text, o.Blocks[0].Text.Text)
	}
	if att := requestJSON(t, outgoing{Attachments: o.Attachments}); strings.Contains(att, "<!") ||
		strings.Contains(att, "Alice") {
		t.Errorf("a Mention in the attachment: %s", att)
	}
	if callMentions(quiet, m) != nil {
		t.Error("a Quiet call mentions someone")
	}
	if o := layout(m, testLook(), callMentions(quiet, m)); strings.Contains(o.Text, "<") ||
		strings.Contains(o.Blocks[0].Text.Text, "<") {
		t.Errorf("quiet = %q", o.Text)
	}
	if o := testRequest(m, testLook()); strings.Contains(requestJSON(t, o), "<!") {
		t.Errorf("a test message mentions: %s", o.Text)
	}

	// A Thread reply carries its Mentions first in its section and after its first line in `text`.
	o = layout(newAlertsReply(), testLook(), callMentions(loud, newAlertsReply()))
	if o.Text != "New alerts (1): <!here> Alice *\u200bSmith*\u200b" ||
		!strings.HasPrefix(o.Blocks[0].Text.Text, "<!here> Alice *\u200bSmith*\u200b\nNew alerts (1):\n") {
		t.Errorf("reply = %q, %q", o.Text, o.Blocks[0].Text.Text)
	}

	// Template output: a body and a line template with tokens, a literal Mention and a link.
	tmpl := e.root(t, layoutSource(messages.ColourFiring))
	tmpl.Body = &messages.Body{Text: "Paging " + templates.MentionToken(templates.Token{Name: templates.MentionChannel}) +
		" <!here> <@U123> <https://wiki.example.org/x|wiki>", Markup: messages.MarkupMrkdwn}
	tmpl.Alerts = &messages.AlertList{Lines: []messages.AlertLine{{Text: "line " + templates.MentionToken(
		templates.Token{Name: templates.MentionGroup, Group: "dba"}) + " <!channel>", Markup: messages.MarkupMrkdwn}}}
	if got := templateTokens(tmpl); len(got) != 2 {
		t.Errorf("tokens = %+v", got)
	}
	o = layout(tmpl, testLook(), callMentions(loud, tmpl))
	if o.Text != "🔴 #12 DiskFull <!here> Alice *\u200bSmith*\u200b <!channel>" {
		t.Errorf("template mentions = %q", o.Text)
	}
	b := sections(o)[blockBody]
	for _, want := range []string{"Paging @\u200bchannel &lt;!here> &lt;@U123> <https://wiki.example.org/x|wiki>",
		"• line @\u200bdba &lt;!channel>"} {
		if !strings.Contains(b, want) {
			t.Errorf("body lacks %q:\n%s", want, b)
		}
	}
}

// TestLinkedMentions is C-24.FR-6, C-24.AC-15 and C-12.FR-12: a User with an Account link for the Destination's
// Connection is mentioned as <@U…> of the link's user id in `text` and the top section of a Loud Root message and of
// a Reminder, never in the attachment; a User without a link there, and a link whose id has not the shape of a Slack
// user id, are named by their display name as text; the footer names the User by the link's display name.
func TestLinkedMentions(t *testing.T) {
	e := newLayoutEnv(t)
	linked := mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{PublicID: "US0000000000U1",
		Name: "Alice A.", Login: "alice", Username: "alice", ExternalID: "U0FAKEALICE", DisplayName: "Alice Smith"}}
	odd := mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{PublicID: "US0000000000U2", Name: "Bob B.",
		Login: "bob", ExternalID: "U0>X"}}
	dora := mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{PublicID: "US0000000000U3",
		Name: "Dora D.", Login: "dora"}}
	got := mentionText([]mentions.Target{hereTarget, linked, odd, dora, linked}, nil)
	if want := []string{"<!here>", "<@U0FAKEALICE>", "Bob B.", "Dora D."}; !slices.Equal(got, want) {
		t.Errorf("mentions = %q, want %q", got, want)
	}
	for id, ok := range map[string]bool{"U0FAKEALICE": true, "W012ABC": true, "U0": false, "u0fake": false,
		"D0FAKEALICE": false, "U0FAKE ALICE": false, "": false} {
		if slackUserID.MatchString(id) != ok {
			t.Errorf("slackUserID(%q) = %v", id, !ok)
		}
	}
	conn := int64(3)
	loud := delivery.Call{Destination: delivery.Destination{ID: 1, PublicID: "DS0000000000D1", Type: mentions.TypeSlack,
		Connection: &conn}, Loudness: groups.Loud, Targets: []mentions.Target{linked, dora}}
	src := layoutSource(messages.ColourFiring)
	m := e.root(t, src)
	o := layout(m, testLook(), callMentions(loud, m))
	if o.Text != "🔴 #12 DiskFull <@U0FAKEALICE> Dora D." ||
		o.Blocks[0].Text.Text != "🔴 #12 DiskFull\n<@U0FAKEALICE> Dora D." {
		t.Errorf("loud = %q, %q", o.Text, o.Blocks[0].Text.Text)
	}
	if att := requestJSON(t, outgoing{Attachments: o.Attachments}); strings.Contains(att, "<@") ||
		strings.Contains(att, "U0FAKEALICE") {
		t.Errorf("a Mention in the attachment: %s", att)
	}
	r := e.reminder(t, "en")
	o = layout(r, testLook(), mentionText([]mentions.Target{linked}, nil))
	if !strings.Contains(o.Text, "<@U0FAKEALICE>") || !strings.HasPrefix(o.Blocks[0].Text.Text, "<@U0FAKEALICE>\n") ||
		len(o.Attachments) != 0 {
		t.Errorf("reminder = %q, %q", o.Text, o.Blocks[0].Text.Text)
	}
	// The footer mentions the Owner, who snoozed and who resolved by <@U…> where they have an Account link for the
	// Connection, in an edit — a Quiet call without Mentions — as in a new message and in plain text (C-12.FR-12);
	// another Connection names them by their display name.
	users := map[string]mentions.User{"slack:3": {Name: "Alice Smith", ExternalID: "U0FAKEALICE"},
		"slack:4": {Name: "Alice Smith", ExternalID: "U0FAKEOTHER"}}
	until := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status string
		set    func(s *messages.Source)
		want   string
	}{
		{messages.ColourAcknowledged, func(*messages.Source) {}, "Acknowledged by <@U0FAKEALICE>"},
		{messages.ColourSnoozed, func(s *messages.Source) { s.SnoozedBy, s.SnoozeUntil = "Alice Smith", &until },
			"Snoozed by <@U0FAKEALICE> until 2026-10-08 14:00 UTC"},
		{messages.ColourResolved, func(s *messages.Source) { s.ResolvedByKind, s.ResolvedBy = "user", "Alice Smith" },
			"Resolved by <@U0FAKEALICE>"},
	} {
		src := layoutSource(tc.status)
		tc.set(src)
		src.FooterUsers = users
		m := e.root(t, src)
		quiet := delivery.Call{Destination: loud.Destination, Loudness: groups.Quiet}
		body := strings.Split(sections(layout(m, testLook(), callMentions(quiet, m)))[blockBody], "\n")
		if got := body[len(body)-1]; got != tc.want {
			t.Errorf("%s footer = %q", tc.status, got)
		}
		if lines := strings.Split(layout(m, plainLook(), nil).Text, "\n"); lines[len(lines)-1] != tc.want {
			t.Errorf("%s plain footer = %q", tc.status, lines[len(lines)-1])
		}
		other := testLook()
		other.space = "slack:5"
		body = strings.Split(sections(layout(m, other, nil))[blockBody], "\n")
		if got := body[len(body)-1]; got != strings.Replace(tc.want, "<@U0FAKEALICE>", "Alice Smith", 1) {
			t.Errorf("%s footer without a link = %q", tc.status, got)
		}
	}
}

// TestPlain is the request after Slack rejected the markup (C-24.FR-8): `text` alone, escaped as the blocks are,
// within 3,000 bytes and under 4,000 bytes in the stored form, with no blocks and no attachment.
func TestPlain(t *testing.T) {
	e := newLayoutEnv(t)
	src := layoutSource(messages.ColourAcknowledged)
	src.TotalAlerts, src.Alerts = 40, nil
	for i := range 40 {
		src.Alerts = append(src.Alerts, messages.SourceAlert{Fingerprint: fmt.Sprint("f", i),
			Labels: map[string]string{"pod": fmt.Sprint("я-pod-", i+1)}, Firing: true})
	}
	src.CommonAnnotations = map[string]string{"details": strings.Repeat("я", 2000)}
	o := layout(e.root(t, src), plainLook(), []string{"<!here>"})
	slackRules(t, "plain", o)
	lines := strings.Split(o.Text, "\n")
	if len(o.Text) > plainLimit || len(o.Blocks) != 0 || len(o.Attachments) != 0 || lines[0] != "🟠 #12 DiskFull" ||
		lines[1] != "<!here>" || lines[len(lines)-1] != "Acknowledged by Alice Smith" ||
		!strings.Contains(o.Text, "note: "+acEscaped) || strings.Contains(o.Text, "яяя") {
		t.Errorf("plain (%d bytes) =\n%s", len(o.Text), o.Text)
	}
	if a := o.args(); fmt.Sprint(a["blocks"], a["attachments"]) != "[] []" {
		t.Errorf("args = %v", a)
	}

	// Emoji are counted as shortcodes: a plain text stays under the stored limit of an edit.
	src = layoutSource(messages.ColourFiring)
	src.Title = strings.Repeat("🔥", 1000)
	o = layout(e.root(t, src), plainLook(), nil)
	if storedBytes(o.Text) >= storedTextLimit || len(o.Text) > plainLimit || !strings.Contains(o.Text, "Open in Muster") {
		t.Errorf("emoji plain (%d stored bytes) =\n%s", storedBytes(o.Text), o.Text)
	}

	// A test message keeps its mark.
	l := plainLook()
	if o := testRequest(e.root(t, layoutSource(messages.ColourFiring)), l); !strings.HasPrefix(o.Text,
		"🧪 Test message\n🔴 #12 DiskFull\n") || len(o.Blocks) != 0 {
		t.Errorf("plain test = %q", o.Text)
	}
}

// TestCutText cuts mrkdwn only where what stays shows as it did, and measures it by its bound.
func TestCutText(t *testing.T) {
	runes := func(n int) bound { return bound{limit: n, size: utf8.RuneCountInString} }
	cases := []struct {
		s    string
		b    bound
		want string
	}{
		{"abc", runes(3), "abc"},
		{"abc&amp;def", runes(6), "abc…"},
		{"abc&amp;def", runes(9), "abc&amp;…"},
		{"x <https://e.org|t> y", runes(5), "x…"},
		{"a*\u200bb", runes(3), "a…"},
		{"a   b c", runes(4), "a…"},
		{"&amp;&amp;", runes(7), "&amp;…"},
		{"& x", runes(2), "&…"},
		{"abc", runes(0), ""},
		{"яяя", bound{limit: 5, size: func(s string) int { return len(s) }}, "я…"},
	}
	for _, c := range cases {
		if got := cutText(c.s, c.b); got != c.want {
			t.Errorf("cutText(%q, %d) = %q, want %q", c.s, c.b.limit, got, c.want)
		}
	}
	if got := cutRunes("abc", 0); got != "" {
		t.Errorf("cutRunes = %q", got)
	}
}

// TestStoredBytes counts a text from above as Slack stores it (F-078, F-079).
func TestStoredBytes(t *testing.T) {
	for s, want := range map[string]int{"abc": 3, "&amp;": 5, "&lt;&gt;": 8, "& x": 7, "<!here>": 13, "я": 2,
		"🔥": shortcodeBytes, "\u200b": 3, "· …": 6, "×": shortcodeBytes, "\xff": shortcodeBytes} {
		if got := storedBytes(s); got != want {
			t.Errorf("storedBytes(%q) = %d, want %d", s, got, want)
		}
	}
	// A text whose Mentions would pass the stored limit loses them, then is cut.
	if got := withMentions(strings.Repeat("🔥", 83), []string{"<!channel>"}); got != strings.Repeat("🔥", 83) {
		t.Errorf("with mentions = %q", got)
	}
	if got := withMentions(strings.Repeat("🔥", 90), nil); storedBytes(got) >= storedTextLimit ||
		!strings.HasSuffix(got, ellipsis) {
		t.Errorf("cut = %d stored bytes", storedBytes(got))
	}
	if got := plainSize(strings.Repeat("🔥", 100)); got != 100*shortcodeBytes-999 {
		t.Errorf("plainSize = %d", got)
	}
}

// TestLookOf lays out the messages of a call: in the Destination's identity space, plain after rejected markup, with
// the version without its "v".
func TestLookOf(t *testing.T) {
	conn := int64(3)
	c := delivery.Call{Destination: delivery.Destination{ID: 1, Type: mentions.TypeSlack, Connection: &conn},
		Plain: true}
	l := lookOf(c, layoutChannel, "v1.2.3")
	if l.channel != layoutChannel || l.version != "1.2.3" || !l.plain || l.mark != "" ||
		l.space != mentions.IdentitySpace(mentions.TypeSlack, &conn) {
		t.Errorf("look = %+v", l)
	}
}

// TestArgs names the ts of an edit and the thread_ts of a Thread reply, and blocks and attachments even when nil.
func TestArgs(t *testing.T) {
	a := outgoing{Channel: layoutChannel, TS: "1.000001", ThreadTS: "1.000002", Text: "x"}.args()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"attachments":[],"blocks":[],"channel":"C0FAKEALERTS","text":"x","thread_ts":"1.000002",` +
		`"ts":"1.000001","unfurl_links":false,"unfurl_media":false}`
	if string(b) != want {
		t.Errorf("args = %s", b)
	}
	// A reply with nothing to say has no section; a heading with no URL is its text.
	if o := layout(messages.Message{Kind: messages.KindReply}, testLook(), nil); len(o.Blocks) != 0 || o.Text != "" {
		t.Errorf("empty reply = %+v", o)
	}
	if h := headingText(messages.Message{Heading: &messages.Heading{Number: 1, Title: "a*b"}}); h != "*#1 a*\u200bb*" {
		t.Errorf("heading = %q", h)
	}
}
