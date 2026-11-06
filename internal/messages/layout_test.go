// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/templates"
)

// TestLayoutOf is C-29.FR-8 for the structured content of a Root message: the number alone and the title without it,
// the status by its key and word, the fields, the summary and the description as plain text without CommonMark
// escapes, the key labels with "N values", the Alerts with every listed value, whether it is resolved and the total of
// the Alert Group, the notices with the footer last, and the links with "Open in Muster" first.
func TestLayoutOf(t *testing.T) {
	r := newRenderer(t)
	src := source("firing", 7)
	src.Summary, src.ReopenCount = "Queue *full* on #1", 1
	src.Alerts[0].Labels["namespace"] = "checkout"
	m := r.Root(src, MarkupCommonMark).Message
	l := LayoutOf(m)
	if l.Number != 12 || l.Title != "PodDown" || l.Status != ColourFiring || l.StatusWord != "Firing" ||
		l.Summary != "Queue *full* on #1" || l.Description != "The pods of the shop do not answer." ||
		!slices.Equal(l.Fields, m.Fields) || len(l.Fields) != 4 ||
		!slices.Equal(l.Labels, []Label{{Name: "namespace", Value: "2 values"}, {Name: "cluster", Value: "prod"}}) ||
		!slices.Equal(l.Notices, []string{"🔁 Reopened ×1"}) || l.Owner != nil || len(l.Assignees) != 0 ||
		l.Links[0] != (Link{Text: "Open in Muster", URL: "https://muster.example.org/alert-groups/AGK7M3QX9P2RTA"}) {
		t.Fatalf("layout %+v", l)
	}
	if a := l.Alerts; a == nil || a.Label != "pod" || a.Total != 7 || len(a.Values) != CompactAlertsListed ||
		a.Values[0] != (AlertValue{Value: "p1"}) || slices.ContainsFunc(a.Values, func(v AlertValue) bool {
		return v.Resolved
	}) {
		t.Fatalf("alerts %+v", l.Alerts)
	}
	src = source("firing", 3)
	if a := LayoutOf(r.Root(src, MarkupCommonMark).Message).Alerts; a == nil || len(a.Values) != 3 ||
		a.Values[2] != (AlertValue{Value: "p2", Resolved: true}) || a.Total != 3 {
		t.Fatalf("a resolved Alert %+v", a)
	}
	if one := LayoutOf(r.Root(source("firing", 1), MarkupCommonMark).Message); one.Alerts != nil {
		t.Fatalf("one Alert %+v", one.Alerts)
	}
	ru := source("acknowledged", 3)
	ru.Route.Language, ru.Owner = LanguageRussian, "Алиса"
	if l := LayoutOf(r.Root(ru, MarkupCommonMark).Message); l.StatusWord != T(LanguageRussian, "status.acknowledged", nil) ||
		l.StatusWord == "Acknowledged" || l.StatusWord == "" || l.Owner == nil || l.Owner.Name != "Алиса" {
		t.Fatalf("in Russian %q %+v", l.StatusWord, l.Owner)
	}
}

// TestHasLayout is C-29.FR-8 and C-29.FR-5: only a Root message of Muster's compact default content has a layout; a
// Route's own root_message template, the Fallback template and a Desired state stored before the compact layout have
// content that only the body holds, and so have none, nor does a Thread reply or a Storm summary; the preview of a
// Route's own template carries no layout either.
func TestHasLayout(t *testing.T) {
	r := newRenderer(t)
	src := source("firing", 3)
	compact := r.Root(src, MarkupCommonMark).Message
	templated := src
	templated.Route.RootMessage = ptr("{{ .AlertGroup.Title }} is down")
	withTemplate := r.Root(templated, MarkupCommonMark).Message
	fallback := r.fallback(compact, src, LanguageEnglish)
	earlier := compact
	earlier.StatusWord = ""
	if !HasLayout(compact) || withTemplate.Body == nil || HasLayout(withTemplate) || HasLayout(fallback) ||
		HasLayout(earlier) || HasLayout(Message{Kind: KindReply, Lines: []string{"x"}}) ||
		HasLayout(Message{Kind: KindStorm, Lines: []string{"x"}}) {
		t.Fatalf("compact %v, template %v, fallback %v, earlier %v", HasLayout(compact), HasLayout(withTemplate),
			HasLayout(fallback), HasLayout(earlier))
	}
	f := &fakeQueries{group: source("firing", 3)}
	pr := withFake(t, f)
	res, err := pr.Preview(t.Context(), PreviewRequest{Kind: TemplateRootMessage, AlertGroupID: "AGK7M3QX9P2RTA",
		Template: "{{ .AlertGroup.Title }} is down", Format: MarkupCommonMark})
	if err != nil || !res.Valid || res.Layout != nil || res.Length == 0 {
		t.Fatalf("the preview of a Route's own template %+v %v", res, err)
	}
}

// TestLayoutPeople is C-29.FR-8 and C-29.AC-6 for the people of the layout: in CommonMark, the markup of bridges, an
// acknowledged Alert Group has its Owner by their Muster display name, and the footer of the acknowledgement, which
// names only them, is no notice; every other markup keeps the footer among the notices and has no Owner, and so does
// a snoozed or resolved Alert Group, whose footer names someone else. A footer an adapter chose names its user by the
// Muster display name, never by the adapter's Mention of them.
func TestLayoutPeople(t *testing.T) {
	r := newRenderer(t)
	src := source("acknowledged", 3)
	src.Owner = "Alice <@here>"
	m := r.Root(src, MarkupCommonMark).Message
	if m.Owner != Value("Alice <@here>") {
		t.Fatalf("owner %q", m.Owner)
	}
	l := LayoutOf(m)
	if l.Owner == nil || l.Owner.Name != Value("Alice <@here>") || len(l.Notices) != 0 {
		t.Fatalf("acknowledged %+v %+v", l.Owner, l.Notices)
	}
	for _, markup := range []Markup{MarkupMarkdown, MarkupHTML, MarkupMrkdwn, MarkupPlain} {
		m := r.Root(src, markup).Message
		if l := LayoutOf(m); m.Owner != "" || l.Owner != nil ||
			!slices.Equal(l.Notices, []string{"Acknowledged by " + Value("Alice <@here>")}) {
			t.Errorf("%s: %q %+v %v", markup, m.Owner, l.Owner, l.Notices)
		}
	}
	for _, status := range []string{"firing", "snoozed", "resolved"} {
		src := source(status, 3)
		src.Owner, src.SnoozedBy, src.ResolvedBy, src.ResolvedByKind = "Alice", "Bob", "Bob", "user"
		m := r.Root(src, MarkupCommonMark).Message
		if l := LayoutOf(m); m.Owner != "" || l.Owner != nil || (status != "firing" && len(l.Notices) == 0) {
			t.Errorf("%s: %q %+v %v", status, m.Owner, l.Owner, l.Notices)
		}
	}
	// The colour decides: an Owner left in a message of another status is no person of its layout.
	if l := LayoutOf(Message{Colour: ColourResolved, Owner: "Alice", Footer: "Resolved by Bob"}); l.Owner != nil ||
		!slices.Equal(l.Notices, []string{"Resolved by Bob"}) {
		t.Errorf("resolved with an Owner %+v", l)
	}
	src = source("snoozed", 3)
	src.SnoozedBy = "Bob Brown"
	src.FooterUsers = map[string]mentions.User{"bridge:7": {Name: "Bob Brown", ExternalID: "u-bob",
		DisplayName: "bob"}}
	snoozed := r.Root(src, MarkupCommonMark).Message
	marked := snoozed.WithFooterIn("bridge:7", func(mentions.User) string {
		return "[@bob](muster-mention:m1)"
	})
	if l := LayoutOf(marked); len(l.Notices) != 1 || l.Notices[0] != LayoutOf(snoozed).Notices[0] ||
		l.Notices[0] == "" || templates.StripTokens(l.Notices[0]) != l.Notices[0] {
		t.Errorf("a footer with a Mention %q, without %q", l.Notices, LayoutOf(snoozed).Notices)
	}
	if l := LayoutOf(snoozed.WithFooterIn("bridge:7", nil)); !slices.Equal(l.Notices, LayoutOf(snoozed).Notices) {
		t.Errorf("a footer without a Mention %q", l.Notices)
	}
}

// TestLayoutInTheDesiredState is C-29.FR-8 for the Desired state, whose payload is the JSON of the Message and whose
// hash, in internal/delivery, is the SHA-256 of that payload: the layout of a Message restored from its JSON is the
// layout of the Message, so a layout is never lost between the render and the call; and every change of the layout —
// here the number, which only the envelope's title shows, the total of the Alerts, which the body shows only through
// "…and K more", and the Owner — alters the JSON, and with it the hash, so that it edits the Root message. The hash
// itself is unexported in internal/delivery (encode), which this test cannot reach.
func TestLayoutInTheDesiredState(t *testing.T) {
	r := newRenderer(t)
	src := source("acknowledged", 7)
	src.Owner = "Alice"
	m := r.Root(src, MarkupCommonMark).Message
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(LayoutOf(restored), LayoutOf(m)) {
		t.Fatalf("restored %+v\nrendered %+v", LayoutOf(restored), LayoutOf(m))
	}
	for name, change := range map[string]func(*Message){
		"number": func(m *Message) { h := *m.Heading; h.Number++; m.Heading = &h },
		"total":  func(m *Message) { a := *m.AlertsLine; a.Total++; m.AlertsLine = &a },
		"owner":  func(m *Message) { m.Owner = "Bob" },
	} {
		cp := m
		change(&cp)
		changed, _ := json.Marshal(cp)
		if reflect.DeepEqual(LayoutOf(cp), LayoutOf(m)) || string(changed) == string(payload) {
			t.Errorf("%s: the layout or the payload did not change", name)
		}
	}
}
