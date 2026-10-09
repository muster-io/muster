// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
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

// source is an Alert Group of Route db with n firing Alerts differing in pod, in a status.
func source(status string, n int) *Source {
	s := &Source{ID: 9, PublicID: "AGK7M3QX9P2RTA", Number: 12, Title: "PodDown", Summary: "Pods are down",
		Status: status, SeverityLevel: "critical", KeyLabels: []string{"alertname", "namespace"},
		KeyValues: map[string]string{"alertname": "PodDown", "namespace": "shop"},
		CommonLabels: map[string]string{"alertname": "PodDown", "namespace": "shop", "cluster": "prod",
			"environment": "production", "severity": "critical", "prometheus": "mon/k8s", "owner": "@channel",
			"team": "web"},
		CommonAnnotations: map[string]string{"summary": "Pods are down", "description": "long text",
			"runbook_url": "https://runbooks/x", "dashboard": "https://grafana/d"},
		StartedAt: t0, TimeZone: "UTC",
		Route: RouteRef{ID: 3, PublicID: "RTAAAAAAAAAAA1", Name: "db", Language: "en",
			SnoozeDurations: []int64{3600, 14400, 86400}},
		TotalAlerts: int64(n)}
	for i := range n {
		s.Alerts = append(s.Alerts, SourceAlert{Fingerprint: fmt.Sprintf("fp%d", i+1),
			Labels: map[string]string{"alertname": "PodDown", "namespace": "shop", "cluster": "prod",
				"pod": fmt.Sprintf("p%d", i+1), "owner": "@channel", "severity": "critical"},
			Annotations: map[string]string{}, StartsAt: t0.Add(-time.Duration(i) * time.Minute), Firing: i != 1})
	}
	return s
}

// TestDefaultRootMessage is C-12.FR-1: the colour, `#N` and the title linked to the Alert Group page, the environment
// and the absolute start time, the group labels, the common labels and annotations without the excluded ones, the
// summary in italics, the Alerts, the links, the notices, the footer and the buttons of the status, in that order.
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
		"🟠 [#12 PodDown](https://muster.example.org/alert-groups/AGK7M3QX9P2RTA)",
		"prod · production · Started 2026-10-04 14:05 UTC",
		"namespace: shop",
		"cluster: prod",
		"owner: @\u200bchannel",
		"team: web",
		"dashboard: https://grafana/d",
		"_Pods are down_",
		"- pod: p1",
		"- ~~pod: p2~~",
		"- pod: p3",
		"[Open in Muster](https://muster.example.org/alert-groups/AGK7M3QX9P2RTA)",
		"🔁 Reopened ×2",
		"Acknowledged by Alice",
		"[Unack] [Resolve] [Snooze 1 h] [Snooze 4 h] [Snooze 24 h]",
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
	if len(m.Alerts.Distinct) != 1 || m.Alerts.Distinct[0].Label != "pod" {
		t.Errorf("distinct values kept for shortening: %+v", m.Alerts.Distinct)
	}
	// HTML lays out the same structure with alert data escaped for it.
	src.CommonLabels["team"] = "<web&ops>"
	html := Layout(r.Root(src, MarkupHTML).Message, MarkupHTML)
	if !strings.Contains(html, `🟠 <a href="https://muster.example.org/alert-groups/AGK7M3QX9P2RTA">#12 PodDown</a>`) ||
		!strings.Contains(html, "team: &lt;web&amp;ops&gt;") || !strings.Contains(html, "<i>Pods are down</i>") ||
		!strings.Contains(html, "• <s>pod: p2</s>") {
		t.Errorf("html\n%s", html)
	}
	if plain := r.Root(src, MarkupPlain).Message.Text(); !strings.HasPrefix(plain,
		"🟠 #12 PodDown https://muster.example.org/alert-groups/AGK7M3QX9P2RTA\n") || !strings.Contains(plain,
		"• pod: p2\n") {
		t.Errorf("plain\n%s", plain)
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
			"Snooze 24 h"}, []string{"⚠ Nobody has taken this"}},
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
		m.Environment != "prod · production · Начало: 2026-10-04 17:05 MSK" || len(m.Buttons) != 0 {
		t.Errorf("ru %+v", m)
	}
	src = source("firing", 1)
	src.Route.Language, src.Route.SnoozeDurations = "ru", []int64{1800, 259200, 45}
	var ru []string
	for _, b := range r.Root(src, MarkupMarkdown).Message.Buttons {
		ru = append(ru, b.Label)
	}
	if !slices.Equal(ru, []string{"Подтвердить", "Закрыть", "Отложить на 30 мин", "Отложить на 3 д", "Отложить на 45 с"}) {
		t.Errorf("ru buttons %v", ru)
	}
	// A single Alert is named by its alertname; one without labels by its fingerprint.
	one := source("firing", 1)
	if l := r.Root(one, MarkupMarkdown).Message.Alerts.Lines; len(l) != 1 || l[0].Text != "PodDown" {
		t.Errorf("one alert %+v", l)
	}
	one.Alerts[0].Labels = map[string]string{}
	if l := r.Root(one, MarkupMarkdown).Message.Alerts.Lines; l[0].Text != "fp1" {
		t.Errorf("no labels %+v", l)
	}
	one.Alerts, one.TotalAlerts = nil, 0
	if m := r.Root(one, MarkupMarkdown).Message; m.Alerts != nil {
		t.Errorf("no alerts %+v", m.Alerts)
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

// TestManyAlerts is C-12.AC-4: 25 Alerts differing in pod show the distinct values "pod: p1, p2, … +15 more" and
// "Full list in Muster" instead of their lines.
func TestManyAlerts(t *testing.T) {
	r := newRenderer(t)
	m := r.Root(source("firing", 25), MarkupMarkdown).Message
	out := Layout(m, MarkupMarkdown)
	for _, want := range []string{
		"pod: p1, p2, p3, p4, p5, p6, p7, p8, p9, p10 +15 more",
		"[Full list in Muster](https://muster.example.org/alert-groups/AGK7M3QX9P2RTA)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if len(m.Alerts.Lines) != 0 {
		t.Errorf("lines %+v", m.Alerts.Lines)
	}
	src := source("firing", 25)
	src.Route.Language = "ru"
	if out := Layout(r.Root(src, MarkupMarkdown).Message, MarkupMarkdown); !strings.Contains(out,
		"pod: p1, p2, p3, p4, p5, p6, p7, p8, p9, p10 и ещё 15") || !strings.Contains(out, "[Полный список в Muster]") {
		t.Errorf("ru\n%s", out)
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
	if err != nil || !strings.Contains(m.Body.Text, "prod · Started 2026-10-04 13:55 UTC") ||
		!strings.Contains(m.Body.Text, "• pod: checkout-1") || !strings.Contains(m.Body.Text, "namespace: shop") {
		t.Errorf("built-in root message %+v %v", m.Body, err)
	}
	if BuiltinSource(TemplateLinkRule, "en") != "" {
		t.Error("a link rule has no built-in source")
	}
}
