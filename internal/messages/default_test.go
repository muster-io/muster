// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
)

var t0 = time.Date(2026, 10, 4, 14, 5, 0, 0, time.UTC)

// fakeKeys sign with one fixed key, as an opened Keyring does.
type fakeKeys struct{}

func (fakeKeys) Sign(keyring.Purpose, []byte) ([]byte, string, error) {
	return bytes.Repeat([]byte{7}, 32), "k-0123456789abcdef", nil
}
func (fakeKeys) VerifyPrefix(keyring.Purpose, string, []byte, []byte) (bool, error) { return true, nil }
func (fakeKeys) KeyIDs() []string                                                   { return []string{"k-0123456789abcdef"} }

func newRenderer(t *testing.T) *Renderer {
	t.Helper()
	return New(Config{OrgID: 1, PublicURL: "https://muster.example.org/", Business: clock.NewManual(t0),
		Real: clock.NewManual(t0), Keys: fakeKeys{}, Log: logging.New(io.Discard, logging.LevelInfo)})
}

// source is an Alert Group of Route db with n Alerts differing in pod, all firing but the second, in a status, of the
// Team payments, whose Route shows the labels namespace, team and cluster.
func source(status string, n int) *Source {
	s := &Source{ID: 9, PublicID: "AGK7M3QX9P2RTA", Number: 12, Title: "PodDown", Summary: "Pods are down",
		Status: status, SeverityLevel: "critical", KeyLabels: []string{"alertname", "namespace"},
		KeyValues: map[string]string{"alertname": "PodDown", "namespace": "shop"},
		CommonLabels: map[string]string{"alertname": "PodDown", "namespace": "shop", "cluster": "prod",
			"environment": "production", "severity": "critical", "prometheus": "mon/k8s", "owner": "@channel",
			"team": "web"},
		CommonAnnotations: map[string]string{"summary": "Pods are down", "description": "long text",
			"runbook_url": "https://runbooks/x", "dashboard": "https://grafana/d"},
		Description: "The pods of the shop do not answer.",
		StartedAt:   t0, TimeZone: "UTC", WorkdayStart: "09:00", WeekStart: "monday",
		InstanceLabels: []string{"pod", "instance", "container", "endpoint"}, Teams: []string{"payments"},
		Route: RouteRef{ID: 3, PublicID: "RTAAAAAAAAAAA1", Name: "db", Language: "en",
			SnoozeDurations: []int64{3600, 14400, 86400}, MessageLabels: []string{"namespace", "team", "cluster"}},
		TotalAlerts: int64(n)}
	for i := range n {
		s.Alerts = append(s.Alerts, SourceAlert{Fingerprint: fmt.Sprintf("fp%d", i+1),
			Labels: map[string]string{"alertname": "PodDown", "namespace": "shop", "cluster": "prod",
				"pod": fmt.Sprintf("p%d", i+1), "owner": "@channel", "severity": "critical"},
			Annotations: map[string]string{}, StartsAt: t0.Add(-time.Duration(i) * time.Minute), Firing: i != 1})
	}
	return s
}

// TestDefaultRootMessage is C-29.FR-1 and C-29.AC-1: the heading — #N and the title linked to the Alert Group page —
// with the status as a word and its emoji, the fields Severity, Route, Teams and Started, the summary and the
// description as plain text, the key labels, the Alerts by their distinguishing label with the resolved one struck
// through, the notices, the footer, the links with "Open in Muster" first and the buttons of the status, in that order;
// no label section of L1.
func TestDefaultRootMessage(t *testing.T) {
	r := newRenderer(t)
	src := source("acknowledged", 3)
	src.Owner, src.ReopenCount = "Alice", 2
	out := r.Root(src, MarkupMarkdown)
	if out.Failure != nil || out.KeyID != "k-0123456789abcdef" {
		t.Fatalf("rendered %+v", out)
	}
	got := Layout(out.Message, MarkupMarkdown)
	want := strings.Join([]string{
		"[#12 PodDown](https://muster.example.org/alert-groups/AGK7M3QX9P2RTA) — 🟠 Acknowledged",
		"Severity: critical",
		"Route: db",
		"Teams: payments",
		"Started: 2026-10-04 14:05 UTC",
		"Pods are down",
		"The pods of the shop do not answer.",
		"namespace=shop · cluster=prod",
		"pod: p1, p3, ~~p2~~",
		"🔁 Reopened ×2",
		"Acknowledged by Alice",
		"[Open in Muster](https://muster.example.org/alert-groups/AGK7M3QX9P2RTA)",
		"[Unack] [Resolve] [Snooze 1 h] [Snooze 4 h] [Snooze 24 h] [Until tomorrow 09:00] [Until Monday 09:00]",
	}, "\n")
	if got != want {
		t.Errorf("layout\n%s\nwant\n%s", got, want)
	}
	m := out.Message
	for i, b := range m.Buttons {
		a, err := buttons.Verify(fakeKeys{}, b.ActionID, "")
		if err != nil || a.PublicID != src.PublicID || a.Command != b.Command || b.KeyID != out.KeyID {
			t.Errorf("button %d %+v: %+v %v", i, b, a, err)
		}
	}
	if m.Environment != "" || m.GroupLabels != nil || m.CommonLabels != nil || m.CommonAnnotations != nil ||
		m.Alerts != nil {
		t.Errorf("sections of L1 %+v", m)
	}
	if a := m.AlertsLine; a == nil || a.Label != "pod" || a.Total != 3 || a.More() != 0 ||
		!slices.Equal(a.Values, []AlertValue{{Value: "p1"}, {Value: "p3"}, {Value: "p2", Resolved: true}}) {
		t.Errorf("alerts line %+v", m.AlertsLine)
	}
	// HTML lays out the same structure with alert data escaped for it.
	src.Route.MessageLabels = []string{"team"}
	for i := range src.Alerts {
		src.Alerts[i].Labels["team"] = "<web&ops>"
	}
	html := Layout(r.Root(src, MarkupHTML).Message, MarkupHTML)
	if !strings.Contains(html, `<a href="https://muster.example.org/alert-groups/AGK7M3QX9P2RTA">#12 PodDown</a> — `+
		`🟠 Acknowledged`) || !strings.Contains(html, "\nteam=&lt;web&amp;ops&gt;\n") ||
		!strings.Contains(html, "\nPods are down\n") || !strings.Contains(html, "pod: p1, p3, <s>p2</s>") {
		t.Errorf("html\n%s", html)
	}
	if plain := r.Root(src, MarkupPlain).Message.Text(); !strings.HasPrefix(plain,
		"#12 PodDown https://muster.example.org/alert-groups/AGK7M3QX9P2RTA — 🟠 Acknowledged\nSeverity: critical\n") ||
		!strings.Contains(plain, "\npod: p1, p3, p2\n") {
		t.Errorf("plain\n%s", plain)
	}
	// In Russian, the words and the fields of the Route's language; an Alert Group without Teams has no field Teams.
	src.Route.Language, src.Teams, src.Status = "ru", nil, "firing"
	ru := r.Root(src, MarkupMarkdown).Message
	if ru.StatusWord != "Горит" || !slices.Equal(ru.Fields, []Label{{Name: "Важность", Value: "critical"},
		{Name: "Маршрут", Value: "db"}, {Name: "Начало", Value: "2026-10-04 14:05 UTC"}}) {
		t.Errorf("ru %q %+v", ru.StatusWord, ru.Fields)
	}
	for status, word := range map[string]string{"firing": "Firing", "snoozed": "Snoozed", "resolved": "Resolved"} {
		src.Route.Language, src.Status = "en", status
		if m := r.Root(src, MarkupMarkdown).Message; m.StatusWord != word {
			t.Errorf("%s: %q", status, m.StatusWord)
		}
	}
	// A Desired state of an earlier version, without the word, keeps the heading of L1.
	old := Message{Colour: ColourFiring, Heading: &Heading{Number: 3, Title: "Old", URL: "https://m/ag/3"},
		Environment: "prod", GroupLabels: []Label{{Name: "namespace", Value: "shop"}}}
	if got := Layout(old, MarkupMarkdown); got != "🔴 [#3 Old](https://m/ag/3)\nprod\nnamespace: shop" {
		t.Errorf("an old state\n%s", got)
	}
}

// TestKeyLabels is C-29.FR-2 and C-29.AC-2: a label of "Labels in the message" is `name=value` when every firing Alert
// has the same value, "name: N values" when they differ, and absent when no Alert has it; the line follows the order
// of the list; an Alert Group without a firing Alert reads its resolved ones, and one without Alerts its Group key and
// common labels.
func TestKeyLabels(t *testing.T) {
	r := newRenderer(t)
	src := source("firing", 3)
	src.Route.MessageLabels = []string{"pod", "cluster", "zone", "namespace"}
	src.Alerts[0].Labels["namespace"] = "checkout"
	m := r.Root(src, MarkupMarkdown).Message
	// The resolved second Alert does not count: p1 and p3, checkout and shop.
	if got := KeyLabelsLine(m); got != "pod: 2 values · cluster=prod · namespace: 2 values" {
		t.Errorf("line %q", got)
	}
	src.Route.Language = "ru"
	if got := KeyLabelsLine(r.Root(src, MarkupMarkdown).Message); got != "pod: 2 значения · cluster=prod · "+
		"namespace: 2 значения" {
		t.Errorf("ru %q", got)
	}
	for n, want := range map[int64]string{1: "pod: 1 значение", 5: "pod: 5 значений", 22: "pod: 22 значения"} {
		if got := (KeyLabel{Name: "pod", Count: int(n)}).Text("ru"); n > 1 && got != want {
			t.Errorf("ru %d: %q", n, got)
		}
	}
	if got := (KeyLabel{Name: "pod", Count: 5}).Text("en"); got != "pod: 5 values" {
		t.Errorf("en %q", got)
	}
	resolved := source("resolved", 2)
	for i := range resolved.Alerts {
		resolved.Alerts[i].Firing = false
	}
	resolved.Route.MessageLabels = []string{"pod", "cluster"}
	if got := KeyLabelsLine(r.Root(resolved, MarkupMarkdown).Message); got != "pod: 2 values · cluster=prod" {
		t.Errorf("resolved %q", got)
	}
	none := source("firing", 0)
	none.Route.MessageLabels = []string{"namespace", "cluster", "zone"}
	if got := KeyLabelsLine(r.Root(none, MarkupMarkdown).Message); got != "namespace=shop · cluster=prod" {
		t.Errorf("without alerts %q", got)
	}
	none.Route.MessageLabels = nil
	if m := r.Root(none, MarkupMarkdown).Message; m.KeyLabels != nil || m.AlertsLine != nil {
		t.Errorf("no labels %+v", m)
	}
}

// TestAlertsLine is C-29.FR-3, C-29.AC-2 and C-29.AC-3: the Alerts are named by the first Instance label whose values
// differ, else by the first differing label by name; up to message.compact_alerts_listed values, firing ones first in
// natural order, the resolved ones struck through, then "…and K more"; one Alert shows no Alerts line, and neither do
// Alerts that no label tells apart.
func TestAlertsLine(t *testing.T) {
	r := newRenderer(t)
	twelve := source("firing", 12)
	out := Layout(r.Root(twelve, MarkupMarkdown).Message, MarkupMarkdown)
	if !strings.Contains(out, "\npod: p1, p3, p4, p5, p6 …and 7 more\n") {
		t.Errorf("twelve\n%s", out)
	}
	twelve.Route.Language = "ru"
	if out := Layout(r.Root(twelve, MarkupMarkdown).Message, MarkupMarkdown); !strings.Contains(out,
		"\npod: p1, p3, p4, p5, p6 …и ещё 7\n") {
		t.Errorf("ru\n%s", out)
	}
	// The resolved Alert comes after the firing ones, struck through.
	five := source("firing", 5)
	if got := AlertsLines(r.Root(five, MarkupHTML).Message, EscapeHTML, nil,
		func(s string) string { return "<s>" + s + "</s>" }, "• "); !slices.Equal(got,
		[]string{"pod: p1, p3, p4, p5, <s>p2</s>"}) {
		t.Errorf("five %q", got)
	}
	// Instance labels first, in their order: instance before pod when both differ.
	inst := source("firing", 3)
	for i := range inst.Alerts {
		inst.Alerts[i].Labels["instance"] = fmt.Sprintf("10.0.0.%d:9100", 3-i)
		inst.Alerts[i].Labels["a"] = fmt.Sprintf("a%d", i)
	}
	inst.InstanceLabels = []string{"instance", "pod"}
	if a := r.Root(inst, MarkupMarkdown).Message.AlertsLine; a.Label != "instance" || a.Values[0].Value !=
		"10.0.0.1:9100" {
		t.Errorf("instance %+v", a)
	}
	// Without Instance labels that differ, the first differing label by name.
	inst.InstanceLabels = []string{"container"}
	if a := r.Root(inst, MarkupMarkdown).Message.AlertsLine; a.Label != "a" {
		t.Errorf("by name %+v", a)
	}
	if got := distinguishingLabel(inst.Alerts[:1], nil); got != "" {
		t.Errorf("one alert %q", got)
	}
	// An Alert without the label counts in K, after the others.
	missing := source("firing", 3)
	delete(missing.Alerts[0].Labels, "pod")
	if a := r.Root(missing, MarkupMarkdown).Message.AlertsLine; a.Label != "pod" || len(a.Values) != 2 ||
		a.More() != 1 || a.Values[0].Value != "p3" {
		t.Errorf("missing %+v", a)
	}
	// One Alert, and Alerts that nothing tells apart, have no Alerts line; nor has an Alert Group without Alerts.
	if m := r.Root(source("firing", 1), MarkupMarkdown).Message; m.AlertsLine != nil ||
		strings.Contains(Layout(m, MarkupMarkdown), "pod:") {
		t.Errorf("one alert %+v", m.AlertsLine)
	}
	same := source("firing", 2)
	same.Alerts[1].Labels = same.Alerts[0].Labels
	if m := r.Root(same, MarkupMarkdown).Message; m.AlertsLine != nil {
		t.Errorf("alike %+v", m.AlertsLine)
	}
	// An Alert that flapped is one Alert: its episodes, firing and resolved, share a fingerprint, so an Alert Group of
	// that one Alert has no Alerts line; with a second Alert the total counts two.
	flap := source("firing", 1)
	ep := flap.Alerts[0]
	ep.Firing = false
	flap.Alerts = append(flap.Alerts, ep, ep)
	flap.TotalAlerts = 3
	if m := r.Root(flap, MarkupMarkdown).Message; m.AlertsLine != nil {
		t.Errorf("a flapping alert %+v", m.AlertsLine)
	}
	other := source("firing", 3).Alerts[2]
	flap.Alerts, flap.TotalAlerts = append(flap.Alerts, other), 4
	if a := r.Root(flap, MarkupMarkdown).Message.AlertsLine; a == nil || a.Total != 2 || len(a.Values) != 2 {
		t.Errorf("a flapping alert and another %+v", a)
	}
	// No listed Alert has the label: no "pod: …and K more" line.
	bare := source("firing", 7)
	for i := range bare.Alerts {
		delete(bare.Alerts[i].Labels, "pod")
		bare.Alerts[i].Firing = true
	}
	bare.Alerts[6].Labels["pod"], bare.Alerts[6].Firing = "late", false
	if m := r.Root(bare, MarkupMarkdown).Message; m.AlertsLine != nil ||
		strings.Contains(Layout(m, MarkupMarkdown), "more") {
		t.Errorf("no listed value %+v", m.AlertsLine)
	}
	// More Alerts than were read: the total counts them all.
	many := source("firing", 3)
	many.TotalAlerts = 1500
	if a := r.Root(many, MarkupMarkdown).Message.AlertsLine; a.Total != 1500 || a.More() != 1497 {
		t.Errorf("total %+v", a)
	}
	// The summary and description: only a description shows it; neither shows no text.
	only := source("firing", 1)
	only.Summary = ""
	if m := r.Root(only, MarkupMarkdown).Message; m.Summary != "" || m.Description !=
		"The pods of the shop do not answer." {
		t.Errorf("description only %+v", m)
	}
	only.Description = ""
	if out := Layout(r.Root(only, MarkupMarkdown).Message, MarkupMarkdown); strings.Contains(out, "shop do not") ||
		strings.Contains(out, "Pods are down") {
		t.Errorf("no text\n%s", out)
	}
}

// TestFootersAndNotices: every status has its footer and buttons; the notices apply when their condition holds; times
// are absolute in organization.time_zone; Russian per route.language.
func TestFootersAndNotices(t *testing.T) {
	r := newRenderer(t)
	until := t0.Add(2 * time.Hour)
	cases := []struct {
		name    string
		set     func(s *Source)
		footer  string
		buttons []string
		notices []string
	}{
		{"firing", func(s *Source) { s.Unclaimed = true }, "", []string{"Ack", "Resolve", "Snooze 1 h", "Snooze 4 h",
			"Snooze 24 h", "Until tomorrow 09:00", "Until Monday 09:00"}, []string{"⚠ Nobody has taken this"}},
		{"snoozed", func(s *Source) { s.SnoozedBy, s.SnoozeUntil = "Bob", &until },
			"Snoozed by Bob until 2026-10-04 16:05 UTC", []string{"Ack", "Unsnooze", "Resolve"}, nil},
		{"snoozed", func(s *Source) { s.SnoozedBy, s.SnoozeNoEnd = "Bob", true }, "Snoozed by Bob with no end",
			[]string{"Ack", "Unsnooze", "Resolve"}, nil},
		{"resolved", func(s *Source) { s.ResolvedByKind, s.ResolvedBy, s.FiringCount = "user", "Carol", 3 },
			"Resolved by Carol", nil, []string{"⚠ 3 alerts still firing in Alertmanager"}},
		{"resolved", func(s *Source) { s.ResolvedByKind, s.ResolveReason, s.FiringCount = "system", "stale", 0 },
			"Resolved automatically: the alerts are Stale", nil, nil},
		{"resolved", func(s *Source) { s.ResolvedByKind, s.FiringCount, s.Unclaimed = "user", 1, true },
			"Resolved by ", nil, []string{"⚠ 1 alert still firing in Alertmanager"}},
	}
	for _, tc := range cases {
		src := source(tc.name, 1)
		tc.set(src)
		m := r.Root(src, MarkupMarkdown).Message
		var labels []string
		for _, b := range m.Buttons {
			labels = append(labels, b.Label)
		}
		if m.Footer != tc.footer || !slices.Equal(labels, tc.buttons) || !slices.Equal(m.Notices, tc.notices) {
			t.Errorf("%s: footer %q, buttons %v, notices %v", tc.name, m.Footer, labels, m.Notices)
		}
	}
	src := source("resolved", 1)
	src.Route.Language, src.ResolvedByKind, src.ResolveReason, src.TimeZone = "ru", "system", "resolved",
		"Europe/Moscow"
	m := r.Root(src, MarkupMarkdown).Message
	if m.Footer != "Закрыта автоматически: все алерты закрыты" || m.Links[0].Text != "Открыть в Muster" ||
		m.Fields[len(m.Fields)-1] != (Label{Name: "Начало", Value: "2026-10-04 17:05 MSK"}) || len(m.Buttons) != 0 {
		t.Errorf("ru %+v", m)
	}
	src = source("firing", 1)
	src.Route.Language, src.Route.SnoozeDurations = "ru", []int64{1800, 259200, 45}
	var ru []string
	for _, b := range r.Root(src, MarkupMarkdown).Message.Buttons {
		ru = append(ru, b.Label)
	}
	if !slices.Equal(ru, []string{"Подтвердить", "Закрыть", "Отложить на 30 мин", "Отложить на 3 д", "Отложить на 45 с",
		"До завтра 09:00", "До понедельника 09:00"}) {
		t.Errorf("ru buttons %v", ru)
	}
	// An Alert Group without Alerts has no Alerts line.
	one := source("firing", 1)
	one.Alerts, one.TotalAlerts = nil, 0
	if m := r.Root(one, MarkupMarkdown).Message; m.Alerts != nil || m.AlertsLine != nil {
		t.Errorf("no alerts %+v", m.AlertsLine)
	}
	// Buttons are left unsigned without a Keyring, and when signing fails.
	r.keys = nil
	if b := r.Root(source("firing", 1), MarkupMarkdown).Message.Buttons; b[0].ActionID != "" {
		t.Errorf("unsigned %+v", b)
	}
	if reason("en", "") != "all alerts resolved" {
		t.Error("an empty reason")
	}
}

// TestFooterMentions is C-12.FR-12: the Owner, who snoozed and who resolved are a token in the footer of every identity
// space where they have an Account link, with the link as FooterUsers; an adapter writes the token as its Mention,
// while the text around it is escaped like any neutral text, and as the escaped display name without one; the footer
// without a link, a footer the system wrote and a Desired state stored before the tokens stay as they were; shortening
// keeps the footer with its Mention and cuts one that cannot fit alone without breaking a token.
func TestFooterMentions(t *testing.T) {
	r := newRenderer(t)
	until := t0.Add(2 * time.Hour)
	alice := mentions.User{PublicID: "SRAAAAAAAAAAA1", Name: "Alice_*Smith*", Username: "alice.mm"}
	users := map[string]mentions.User{"mattermost:3": alice, "telegram": {Name: "Alice_*Smith*", ExternalID: "7001"}}
	mention := func(u mentions.User) string { return "<@" + u.Username + u.ExternalID + ">" }
	cases := []struct {
		status string
		set    func(s *Source)
		want   string
	}{
		{"acknowledged", func(s *Source) { s.Owner = "Alice_*Smith*" }, "Acknowledged by <@alice.mm>"},
		{"snoozed", func(s *Source) { s.SnoozedBy, s.SnoozeUntil = "Alice_*Smith*", &until },
			"Snoozed by <@alice.mm> until 2026-10-04 16:05 UTC"},
		{"snoozed", func(s *Source) { s.SnoozedBy, s.SnoozeNoEnd = "Alice_*Smith*", true },
			"Snoozed by <@alice.mm> with no end"},
		{"resolved", func(s *Source) { s.ResolvedByKind, s.ResolvedBy = "user", "Alice_*Smith*" },
			"Resolved by <@alice.mm>"},
	}
	for _, tc := range cases {
		src := source(tc.status, 1)
		tc.set(src)
		src.FooterUsers = users
		m := r.Root(src, MarkupMarkdown).Message
		f := m.WithFooterIn("mattermost:3", mention)
		// The text around the token is escaped as one line: CommonMark keeps the space after a Mention.
		if f.FooterText(EscapeMarkdown) != tc.want || f.FooterText(EscapeCommonMark) != tc.want || f.Footers != nil ||
			f.FooterUsers != nil || !strings.Contains(Layout(f, MarkupMarkdown), "\n"+tc.want) {
			t.Errorf("%s: %q in\n%s", tc.status, f.FooterText(EscapeMarkdown), Layout(f, MarkupMarkdown))
		}
		if tg := m.WithFooterIn("telegram", mention); !strings.Contains(tg.FooterText(EscapeHTML), "<@7001>") {
			t.Errorf("%s in telegram: %q", tc.status, tg.FooterText(EscapeHTML))
		}
		// Without a link, or with an adapter that writes nothing, the display name, escaped for the markup.
		for _, plain := range []Message{m.WithFooterIn("mattermost:4", mention), m.WithFooterIn("mattermost:3", nil),
			m.WithFooterIn("telegram", func(mentions.User) string { return "" })} {
			if got := plain.FooterText(EscapeMarkdown); !strings.Contains(got, `Alice\_\*Smith\*`) ||
				plain.FooterMention != "" {
				t.Errorf("%s plain: %q", tc.status, got)
			}
		}
		if m.Footer != strings.Replace(tc.want, "<@alice.mm>", "Alice_*Smith*", 1) || m.Text() != Layout(m, MarkupPlain) ||
			strings.ContainsAny(m.Text(), "\ue000\ue002") {
			t.Errorf("%s: the footer without a link %q", tc.status, m.Footer)
		}
		// WithFooterIn twice changes nothing.
		if again := f.WithFooterIn("telegram", nil); again.Footer != f.Footer || again.FooterMention != f.FooterMention {
			t.Errorf("%s: again %+v", tc.status, again)
		}
	}
	// No link, or a footer the system wrote: no footers by identity space, so that the Desired state is unchanged.
	src := source("acknowledged", 1)
	src.Owner = "Alice"
	plain := r.Root(src, MarkupMarkdown).Message
	src.FooterUsers = users
	src.Status, src.ResolvedByKind, src.ResolveReason = "resolved", "system", "stale"
	if system := r.Root(src, MarkupMarkdown).Message; plain.Footers != nil || plain.FooterUsers != nil ||
		system.Footers != nil || system.FooterUsers != nil {
		t.Errorf("footers without a user: %v %v", plain.Footers, system.Footers)
	}
	b, _ := json.Marshal(plain)
	if strings.Contains(string(b), "footer_users") || strings.Contains(string(b), "FooterMention") {
		t.Errorf("stored %s", b)
	}
	// A Desired state stored before the tokens names the user as text, laid out as it was.
	var old Message
	if err := json.Unmarshal([]byte(`{"kind":"root","footer":"Acknowledged by Alice","footers":{"mattermost:3":`+
		`"Acknowledged by alice.mm"},"buttons":[]}`), &old); err != nil {
		t.Fatal(err)
	}
	if f := old.WithFooterIn("mattermost:3", mention); f.FooterText(EscapeMarkdown) != "Acknowledged by alice.mm" {
		t.Errorf("an old state %q", f.FooterText(EscapeMarkdown))
	}
	// The Mention survives shortening; a footer that cannot fit with it names the user by their display name, cut
	// only when that does not fit either, never inside a token.
	long := source("acknowledged", 30)
	long.Owner, long.FooterUsers = "Alice", users
	m := r.Root(long, MarkupMarkdown).Message.WithFooterIn("mattermost:3", mention)
	short, changed := Shorten(m, 400, MarkupMarkdown)
	if out := Layout(short, MarkupMarkdown); !changed || !strings.Contains(out, "\nAcknowledged by <@alice.mm>\n") ||
		utf8.RuneCountInString(out) > 400 {
		t.Errorf("shortened %v\n%s", changed, out)
	}
	alone := Message{Footer: m.Footer, FooterMention: m.FooterMention, FooterName: "Alice"}
	if plain, _ := Shorten(alone, 22, MarkupMarkdown); Layout(plain, MarkupMarkdown) != "Acknowledged by Alice" ||
		plain.FooterMention != "" {
		t.Errorf("a footer without room for the Mention %q", Layout(plain, MarkupMarkdown))
	}
	cut, _ := Shorten(alone, 10, MarkupMarkdown)
	if out := Layout(cut, MarkupMarkdown); out != "Acknowled…" || strings.ContainsAny(cut.Footer, "\ue000\ue002") {
		t.Errorf("cut %q", out)
	}
	// The stored footer user holds only what an adapter writes: the user's public_id and login, which no messenger
	// shows, leave the Desired state and its hash as they were; a username changes them.
	hash := func(users map[string]mentions.User) [32]byte {
		src := source("acknowledged", 1)
		src.Owner, src.FooterUsers = "Alice", users
		b, _ := json.Marshal(r.Root(src, MarkupMarkdown).Message)
		return sha256.Sum256(b)
	}
	base := hash(map[string]mentions.User{"mattermost:3": {Name: "Alice", Username: "alice.mm"}})
	if hash(map[string]mentions.User{"mattermost:3": {PublicID: "SRAAAAAAAAAAA9", Login: "alice2", Name: "Alice",
		Username: "alice.mm"}}) != base {
		t.Error("an unrendered field changed the hash")
	}
	if hash(map[string]mentions.User{"mattermost:3": {Name: "Alice", Username: "alice.m2"}}) == base {
		t.Error("a new username left the hash as it was")
	}
}

// TestNaturalOrder sorts runs of digits as numbers.
func TestNaturalOrder(t *testing.T) {
	got := []string{"p10", "p2", "p1", "a", "p02", "b9", "b10", "", "p1x"}
	slices.SortFunc(got, naturalCompare)
	if want := []string{"", "a", "b9", "b10", "p1", "p1x", "p2", "p02", "p10"}; !slices.Equal(got, want) &&
		!slices.Equal(got, []string{"", "a", "b9", "b10", "p1", "p1x", "p02", "p2", "p10"}) {
		t.Errorf("order %v", got)
	}
}

// placeholders are the {name} placeholders of a text.
var placeholders = regexp.MustCompile(`\{[a-z]+\}`)

// TestTextsInBothLanguages is C-12.FR-3: every built-in text exists in English and in Russian with the same
// placeholders, plural keys have every form of their language, and no text is left untranslated.
func TestTextsInBothLanguages(t *testing.T) {
	base := func(k string) string {
		for _, f := range []string{"_one", "_few", "_many", "_other"} {
			k = strings.TrimSuffix(k, f)
		}
		return k
	}
	keys := map[string]map[string]bool{}
	for _, lang := range Languages {
		keys[lang] = map[string]bool{}
		for k := range texts[lang] {
			keys[lang][base(k)] = true
		}
	}
	for _, k := range slices.Sorted(maps.Keys(keys[LanguageEnglish])) {
		if !keys[LanguageRussian][k] {
			t.Errorf("%s exists in English only", k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(keys[LanguageRussian])) {
		if !keys[LanguageEnglish][k] {
			t.Errorf("%s exists in Russian only", k)
		}
	}
	forms := map[string][]string{LanguageEnglish: {"one", "other"}, LanguageRussian: {"one", "few", "many", "other"}}
	for _, lang := range Languages {
		for k, v := range texts[lang] {
			b := base(k)
			if b != k {
				for _, f := range forms[lang] {
					if _, ok := texts[lang][b+"_"+f]; !ok {
						t.Errorf("%s: %s has no %s form", lang, b, f)
					}
				}
			}
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: %s is empty", lang, k)
			}
			if other := LanguageRussian; lang == LanguageEnglish {
				ru := texts[other][k]
				if ru == "" {
					ru = texts[other][b+"_other"]
				}
				if ph, rph := placeholders.FindAllString(v, -1), placeholders.FindAllString(ru, -1); !sameSet(ph,
					rph) {
					t.Errorf("%s: placeholders %v in English, %v in Russian", k, ph, rph)
				}
				if ru == v && !strings.Contains(v, "{count}") {
					t.Errorf("%s is not translated: %q", k, v)
				}
			}
		}
	}
	for n, want := range map[int64]string{1: "one", 2: "few", 4: "few", 5: "many", 11: "many", 12: "many", 21: "one",
		22: "few", 25: "many", 111: "many", 101: "one", -3: "few", 0: "many"} {
		if got := pluralForm(LanguageRussian, n); got != want {
			t.Errorf("ru %d = %s, want %s", n, got, want)
		}
	}
	if pluralForm("en", 1) != "one" || pluralForm("en", 0) != "other" {
		t.Error("English plural forms")
	}
	if N("ru", "storm.groups", 21, nil) != "21 группа алертов" || N("ru", "storm.groups", 3, nil) != "3 группы алертов" ||
		N("ru", "storm.groups", 5, nil) != "5 групп алертов" || N("en", "storm.groups", 1, nil) != "1 Alert Group" {
		t.Error("plural texts")
	}
	if T("xx", "link.open", nil) != "Open in Muster" || T("en", "no.such.key", nil) != "no.such.key" ||
		N("en", "link.open", 2, nil) != "Open in Muster" {
		t.Error("fallbacks of texts")
	}
	if FullTime(t0, "Nowhere/City") != "2026-10-04 14:05 UTC" || NoteTime(t0, "") != "14:05" {
		t.Error("time zones")
	}
}

func sameSet(a, b []string) bool {
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// TestBuiltinSources: the built-in template of every kind but link_rule and webhook_request, which have none, and
// language parses and renders against the example, in every markup.
func TestBuiltinSources(t *testing.T) {
	r := newRenderer(t)
	set := settings{route: RouteRef{Name: "db", Language: "en", SnoozeDurations: []int64{3600}}, tz: "UTC"}
	for _, kind := range previewKinds {
		if kind == TemplateLinkRule || kind == TemplateWebhookRequest {
			continue
		}
		for _, lang := range Languages {
			src := BuiltinSource(kind, lang)
			if src == "" {
				t.Errorf("%s/%s: no source", kind, lang)
				continue
			}
			for _, mk := range []Markup{MarkupMarkdown, MarkupHTML, MarkupPlain} {
				if _, err := r.previewMessage(r.example(set), kind, &src, mk); err != nil {
					t.Errorf("%s/%s/%s: %v", kind, lang, mk, err)
				}
			}
		}
	}
	src := BuiltinSource(TemplateRootMessage, "en")
	m, err := r.previewMessage(r.example(set), TemplateRootMessage, &src, MarkupPlain)
	if err != nil || !strings.HasPrefix(m.Body.Text, "Error rate above 5%\n"+
		"The checkout service answers 5% of requests with errors.\n") ||
		!strings.Contains(m.Body.Text, "\n• pod: checkout-1 \n• pod: checkout-2 \n") {
		t.Errorf("built-in root message %+v %v", m.Body, err)
	}
	if BuiltinSource(TemplateLinkRule, "en") != "" {
		t.Error("a link rule has no built-in source")
	}
}

// labels are the labels of buttons.
func labels(bs []Button) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, b.Label)
	}
	return out
}

// TestPresetLabels is C-10.FR-18 and C-10.AC-23: the two Snooze preset buttons follow the durations and name
// organization.workday_start as HH:MM and the day of organization.week_start, capitalised in English and in the
// genitive in Russian, for every day of the week; another argument has no preset label, and a language without texts
// takes English.
func TestPresetLabels(t *testing.T) {
	r := newRenderer(t)
	for _, c := range []struct{ day, en, ru string }{
		{"monday", "Monday", "понедельника"},
		{"tuesday", "Tuesday", "вторника"},
		{"wednesday", "Wednesday", "среды"},
		{"thursday", "Thursday", "четверга"},
		{"friday", "Friday", "пятницы"},
		{"saturday", "Saturday", "субботы"},
		{"sunday", "Sunday", "воскресенья"},
	} {
		for lang, want := range map[string][]string{
			LanguageEnglish: {"Until tomorrow 09:00", "Until " + c.en + " 09:00"},
			LanguageRussian: {"До завтра 09:00", "До " + c.ru + " 09:00"},
		} {
			src := source("firing", 1)
			src.Route.Language, src.WeekStart = lang, c.day
			bs := r.Root(src, MarkupMarkdown).Message.Buttons
			if got := labels(bs[len(bs)-2:]); !slices.Equal(got, want) || bs[len(bs)-2].Argument !=
				buttons.ArgumentTomorrow || bs[len(bs)-1].Argument != buttons.ArgumentWeekStart {
				t.Errorf("%s %s: %v, want %v", lang, c.day, got, want)
			}
			if got := PresetLabel(lang, buttons.ArgumentWeekStart, "09:00", c.day); got != want[1] {
				t.Errorf("PresetLabel %s %s = %q", lang, c.day, got)
			}
		}
	}
	for _, c := range []struct {
		lang     string
		argument int
		want     string
	}{
		{"en", buttons.ArgumentTomorrow, "Until tomorrow 23:59"},
		{"ru", buttons.ArgumentTomorrow, "До завтра 23:59"},
		{"xx", buttons.ArgumentWeekStart, "Until Sunday 23:59"},
		{"en", 0, ""},
		{"en", 253, ""},
	} {
		if got := PresetLabel(c.lang, c.argument, "23:59", "sunday"); got != c.want {
			t.Errorf("PresetLabel(%s, %d) = %q, want %q", c.lang, c.argument, got, c.want)
		}
	}
}

// TestPresetButtonsDesiredState is C-10.FR-18 and C-10.AC-23: the settings the preset labels name are read with the
// Alert Group for its message, so the labels are part of the Desired state and its hash: a render with unchanged
// settings hashes the same, and after organization.workday_start becomes 08:30 the next render reads "Until tomorrow
// 08:30" and hashes differently. The presets follow the durations on firing and acknowledged, signed with their
// arguments and with the neutral style; a snoozed Root message has none, and a Route without Snooze durations offers
// neither presets nor Snooze.
func TestPresetButtonsDesiredState(t *testing.T) {
	f := &fakeQueries{group: source("firing", 2)}
	r := withFake(t, f)
	// render is the Root message of the Alert Group read again, and the hash of its JSON as delivery stores it.
	render := func() (Message, [32]byte) {
		t.Helper()
		src, err := r.Load(t.Context(), nil, 9)
		if err != nil {
			t.Fatal(err)
		}
		m := r.Root(src, MarkupMarkdown).Message
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return m, sha256.Sum256(raw)
	}
	before, hash := render()
	if _, again := render(); again != hash {
		t.Error("the same settings hash differently")
	}
	if got := labels(before.Buttons); !slices.Equal(got, []string{"Ack", "Resolve", "Snooze 1 h", "Snooze 4 h",
		"Snooze 24 h", "Until tomorrow 09:00", "Until Monday 09:00"}) {
		t.Errorf("before: %v", got)
	}
	for i, b := range before.Buttons[5:] {
		a, err := buttons.Verify(fakeKeys{}, b.ActionID, "")
		if err != nil || a.Command != buttons.CommandSnooze || a.Argument != buttons.ArgumentTomorrow+i ||
			b.Argument != a.Argument || b.Style() != buttons.StyleNeutral {
			t.Errorf("preset %d %+v: %+v %v", i, b, a, err)
		}
	}
	f.group.WorkdayStart = "08:30"
	after, changed := render()
	if got := labels(after.Buttons[5:]); !slices.Equal(got, []string{"Until tomorrow 08:30", "Until Monday 08:30"}) ||
		changed == hash {
		t.Errorf("after the change: %v, the hash changed %v", got, changed != hash)
	}
	f.group.WeekStart = "sunday"
	if m, h := render(); m.Buttons[6].Label != "Until Sunday 08:30" || h == changed {
		t.Errorf("another first day of the week: %q", m.Buttons[6].Label)
	}
	for status, want := range map[string][]string{
		"acknowledged": {"Unack", "Resolve", "Snooze 1 h", "Snooze 4 h", "Snooze 24 h", "Until tomorrow 08:30",
			"Until Sunday 08:30"},
		"snoozed": {"Ack", "Unsnooze", "Resolve"},
	} {
		f.group.Status = status
		if m, _ := render(); !slices.Equal(labels(m.Buttons), want) {
			t.Errorf("%s: %v", status, labels(m.Buttons))
		}
	}
	f.group.Route.SnoozeDurations = nil
	for status, want := range map[string][]string{"firing": {"Ack", "Resolve"}, "acknowledged": {"Unack", "Resolve"}} {
		f.group.Status = status
		if m, _ := render(); !slices.Equal(labels(m.Buttons), want) {
			t.Errorf("%s without durations: %v", status, labels(m.Buttons))
		}
	}
}

// TestPresetsOfTestsAndPreviews is C-10.FR-18 with C-16.FR-1: a Destination test message carries the preset buttons
// of its status, signed as the subject test naming the Destination; the Destination preview and a preview of a Route
// lay them out with the Organization's settings, in the Route's language, and a Route without durations has none.
func TestPresetsOfTestsAndPreviews(t *testing.T) {
	f := &fakeQueries{group: source("firing", 2)}
	f.group.WorkdayStart, f.group.WeekStart = "07:45", "saturday"
	r := withFake(t, f)
	ex, err := r.TestExample(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	bs := r.TestRoot(ex, MarkupMarkdown, "DSAAAAAAAAAAA1").Message.Buttons
	if got := labels(bs); !slices.Equal(got, []string{"Ack", "Resolve", "Snooze 1 h", "Until tomorrow 07:45",
		"Until Saturday 07:45"}) {
		t.Errorf("test message: %v", got)
	}
	for i, b := range bs[3:] {
		a, err := buttons.Verify(fakeKeys{}, b.ActionID, b.KeyID)
		if err != nil || a.Subject != buttons.SubjectTest || a.PublicID != "DSAAAAAAAAAAA1" ||
			a.Command != buttons.CommandSnooze || a.Argument != buttons.ArgumentTomorrow+i {
			t.Errorf("test preset %d: %+v %v", i, a, err)
		}
	}
	sample, err := r.TestSample(t.Context(), "AGK7M3QX9P2RTA")
	if err != nil {
		t.Fatal(err)
	}
	if got := labels(r.TestRoot(sample, MarkupHTML, "DSAAAAAAAAAAA1").Message.Buttons); len(got) != 7 ||
		got[6] != "Until Saturday 07:45" {
		t.Errorf("test message of a sample: %v", got)
	}
	res, err := r.Preview(t.Context(), PreviewRequest{Kind: TemplateRootMessage})
	if err != nil || !strings.HasSuffix(res.Output, "[Ack] [Resolve] [Snooze 1 h] [Until tomorrow 07:45] "+
		"[Until Saturday 07:45]") {
		t.Errorf("preview: %+v, %v", res, err)
	}
	f.group.Route.Language = LanguageRussian
	res, err = r.Preview(t.Context(), PreviewRequest{Kind: TemplateRootMessage, RouteID: "RTAAAAAAAAAAA1"})
	if err != nil || !strings.HasSuffix(res.Output, "[Отложить на 24 ч] [До завтра 07:45] [До субботы 07:45]") {
		t.Errorf("preview of a route: %+v, %v", res, err)
	}
	f.group.Route.SnoozeDurations = []int64{}
	res, err = r.Preview(t.Context(), PreviewRequest{Kind: TemplateRootMessage, RouteID: "RTAAAAAAAAAAA1"})
	if err != nil || !strings.HasSuffix(res.Output, "[Подтвердить] [Закрыть]") {
		t.Errorf("preview of a route without durations: %+v, %v", res, err)
	}
}
