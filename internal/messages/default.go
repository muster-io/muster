// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/links"
	"github.com/muster-io/muster/internal/templates"
)

// The default Root message (C-29.FR-1): the limits of its Alerts and the labels a new-Alert Thread reply leaves out.

// AlertsListed is message.alerts_listed: the distinct values of each label that differs between the Alerts that the
// Fallback template keeps for shortening, at most AlertsListed per label, and the Alerts a preview of a line template
// reads.
const AlertsListed = 10

// CompactAlertsListed is message.compact_alerts_listed (P-83): the values of the distinguishing label a Root message
// lists before "…and K more" (C-29.FR-3).
const CompactAlertsListed = 5

// excludedLabels are left out of the lines of a new-Alert Thread reply (C-12.FR-1 of L1).
var excludedLabels = []string{"severity", "environment", "prometheus", "alertname"}

// frame is what Muster always shows around the body (C-12.FR-2, C-29.FR-5): the colour of the status, the heading
// linked to the Alert Group page with the status as a word, the fields, the links starting with "Open in Muster", the
// notices that apply, the footer and the buttons of the status, with the id of the key that signed them.
func (r *Renderer) frame(src *Source, lang string, sign bool) (Message, string) {
	url := r.GroupURL(src.PublicID)
	m := Message{Kind: KindRoot, Language: lang, TimeZone: src.TimeZone, Colour: src.Status,
		Heading:    &Heading{Number: src.Number, Title: Value(src.Title), URL: url},
		StatusWord: T(lang, "status."+src.Status, nil), Fields: fields(src, lang),
		Links:   append([]Link{{Text: T(lang, "link.open", nil), URL: url}}, linksOf(src.Links, lang)...),
		Notices: notices(src, lang), Footer: footer(src, lang, Value), Buttons: []Button{}}
	token := func(string) string { return templates.MentionToken(templates.Token{Name: FooterUserToken}) }
	if f := footer(src, lang, token); len(src.FooterUsers) > 0 && f != m.Footer {
		m.Footers = make(map[string]string, len(src.FooterUsers))
		m.FooterUsers = make(map[string]FooterUser, len(src.FooterUsers))
		for space, u := range src.FooterUsers {
			m.Footers[space] = f
			m.FooterUsers[space] = FooterUser{Name: u.Name, Username: u.Username, ExternalID: u.ExternalID,
				DisplayName: u.DisplayName}
		}
	}
	keyID := ""
	for _, b := range buttons.ForStatus(src.Status, len(src.Route.SnoozeDurations)) {
		var label string
		if b.Command == buttons.CommandSnooze && buttons.IsPreset(b.Argument) {
			label = PresetLabel(lang, b.Argument, src.WorkdayStart, src.WeekStart)
		} else {
			label = buttonLabel(lang, b, src.Route.SnoozeDurations)
		}
		btn := Button{Command: b.Command, Argument: b.Argument, Label: label}
		if sign && r.keys != nil {
			id, kid, err := buttons.Sign(r.keys, buttons.Action{Subject: buttons.SubjectRoot, PublicID: src.PublicID,
				Command: b.Command, Argument: b.Argument})
			if err == nil {
				btn.ActionID, btn.KeyID, keyID = id, kid, kid
			}
		}
		m.Buttons = append(m.Buttons, btn)
	}
	return m, keyID
}

// linksOf are the links of an Alert Group as a message shows them (C-12.FR-1 item 9): a Link rule's by its name, the
// annotations and the generatorURL by their built-in names in the message's language.
func linksOf(ls []links.Link, lang string) []Link {
	out := make([]Link, 0, len(ls))
	for _, l := range ls {
		text := Value(l.Name)
		if l.Kind != links.KindRule {
			text = T(lang, "link."+l.Kind, nil)
		}
		out = append(out, Link{Text: text, URL: l.URL})
	}
	return out
}

// buttonLabel is the label of a button: "Ack", "Resolve", "Snooze 1 h" …
func buttonLabel(lang string, b buttons.Button, durations []int64) string {
	if b.Command == buttons.CommandSnooze && b.Argument < len(durations) {
		return T(lang, "button.snooze", Args{"duration": Duration(lang, durations[b.Argument])})
	}
	return T(lang, "button."+map[string]string{buttons.CommandAcknowledge: "acknowledge",
		buttons.CommandUnacknowledge: "unacknowledge", buttons.CommandResolve: "resolve",
		buttons.CommandUnsnooze: "unsnooze", buttons.CommandStillOnIt: "stillOnIt"}[b.Command], nil)
}

// PresetLabel is the label of the Snooze button of a Snooze preset in lang (C-10.FR-18), by the argument of the
// button: "Until tomorrow 09:00" for buttons.ArgumentTomorrow and "Until Monday 09:00" — «До понедельника 09:00» —
// for buttons.ArgumentWeekStart, from workdayStart, organization.workday_start as HH:MM, and weekStart,
// organization.week_start from monday to sunday; empty for any other argument.
func PresetLabel(lang string, argument int, workdayStart, weekStart string) string {
	switch argument {
	case buttons.ArgumentTomorrow:
		return T(lang, "button.snoozeTomorrow", Args{"time": workdayStart})
	case buttons.ArgumentWeekStart:
		return T(lang, "button.snoozeWeekStart", Args{"day": T(lang, "button.snoozeWeekStartDay."+weekStart, nil),
			"time": workdayStart})
	}
	return ""
}

// notices are the notices of reference.md that apply to src (item 6): still firing after a manual resolve,
// Unclaimed and the Reopen count. Delivery adds the late, deleted and final notes at call time.
func notices(src *Source, lang string) []string {
	var out []string
	if src.Status == ColourResolved && src.ResolvedByKind == "user" && src.FiringCount > 0 {
		out = append(out, N(lang, "notice.stillFiring", src.FiringCount, nil))
	}
	if src.Unclaimed && src.Status != ColourResolved {
		out = append(out, T(lang, "notice.unclaimed", nil))
	}
	if src.ReopenCount > 0 {
		out = append(out, T(lang, "notice.reopened", Args{"count": strconv.FormatInt(src.ReopenCount, 10)}))
	}
	return out
}

// footer is item 7, until the Assignees and the Owner's Team come: who acknowledged, resolved or snoozed the Alert Group, or why the system resolved it, with the
// user written by user from their Muster display name: the name made safe, or the trusted token of the user that an
// adapter writes as its messenger's Mention where they have an Account link (C-12.FR-12).
func footer(src *Source, lang string, user func(name string) string) string {
	switch src.Status {
	case ColourAcknowledged:
		return T(lang, "footer.acknowledged", Args{"user": user(src.Owner)})
	case ColourSnoozed:
		if src.SnoozeUntil == nil {
			return T(lang, "footer.snoozedNoEnd", Args{"user": user(src.SnoozedBy)})
		}
		return T(lang, "footer.snoozed", Args{"user": user(src.SnoozedBy),
			"time": FullTime(*src.SnoozeUntil, src.TimeZone)})
	case ColourResolved:
		if src.ResolvedByKind == "user" {
			return T(lang, "footer.resolvedBy", Args{"user": user(src.ResolvedBy)})
		}
		return T(lang, "footer.resolvedAutomatically", Args{"reason": reason(lang, src.ResolveReason)})
	}
	return ""
}

// reason is why the system resolved an Alert Group, in words.
func reason(lang, r string) string {
	if r == "" {
		r = "resolved"
	}
	return T(lang, "reason."+r, nil)
}

// fields are item 2 of the default Root message (C-29.FR-1): the Severity level, the Route, the Teams — left out when
// the Alert Group has none (C-25.FR-6) — and the start, absolute in the Organization's time zone.
func fields(src *Source, lang string) []Label {
	out := []Label{{Name: T(lang, "field.severity", nil), Value: Value(src.SeverityLevel)},
		{Name: T(lang, "field.route", nil), Value: Value(src.Route.Name)}}
	if len(src.Teams) > 0 {
		names := make([]string, len(src.Teams))
		for i, t := range src.Teams {
			names[i] = Value(t)
		}
		out = append(out, Label{Name: T(lang, "field.teams", nil), Value: strings.Join(names, ", ")})
	}
	return append(out, Label{Name: T(lang, "field.started", nil), Value: FullTime(src.StartedAt, src.TimeZone)})
}

// compactBody fills items 3 to 5 of the default Root message (C-29.FR-1): the summary and the description as plain
// text, the key labels of the Route's "Labels in the message" and the Alerts by their distinguishing label, with the
// Route's line template when it has one, which may fail. Values are cut and neutralized; the adapters escape them.
func (r *Renderer) compactBody(m *Message, src *Source, markup Markup) *Failure {
	m.Summary, m.Description = Value(src.Summary), Value(src.Description)
	m.KeyLabels = keyLabels(src)
	line, fail := r.alertsLine(src, markup, false)
	if fail != nil {
		return fail
	}
	m.AlertsLine = line
	return nil
}

// keyLabels are item 4 (C-29.FR-2): each label of the Route's "Labels in the message", in its order, as its value when
// every firing Alert that has it has the same one, with the number of its values when they differ, and left out when
// no Alert has it. An Alert Group without a firing Alert reads its resolved ones, and one whose Alerts were not read
// its Group key and common labels.
func keyLabels(src *Source) []KeyLabel {
	alerts := slices.DeleteFunc(slices.Clone(src.Alerts), func(a SourceAlert) bool { return !a.Firing })
	if len(alerts) == 0 {
		alerts = src.Alerts
	}
	var out []KeyLabel
	for _, name := range src.Route.MessageLabels {
		values := map[string]bool{}
		for _, a := range alerts {
			if v := a.Labels[name]; v != "" {
				values[v] = true
			}
		}
		if len(alerts) == 0 {
			if v := cmp.Or(src.KeyValues[name], src.CommonLabels[name]); v != "" {
				values[v] = true
			}
		}
		switch len(values) {
		case 0:
		case 1:
			for v := range values {
				out = append(out, KeyLabel{Name: Value(name), Value: Value(v)})
			}
		default:
			out = append(out, KeyLabel{Name: Value(name), Count: len(values)})
		}
	}
	return out
}

// distinctAlerts are the Alerts of an Alert Group once each, by fingerprint: the episodes of an Alert that resolved
// and fired again are one Alert, its firing episode or else its latest one. The Alerts come firing first and newest
// first (ListRenderAlerts), so the first episode of each fingerprint is the one kept.
func distinctAlerts(alerts []SourceAlert) []SourceAlert {
	seen := make(map[string]bool, len(alerts))
	out := make([]SourceAlert, 0, len(alerts))
	for _, a := range alerts {
		if !seen[a.Fingerprint] {
			seen[a.Fingerprint] = true
			out = append(out, a)
		}
	}
	return out
}

// distinguishingLabel is the label that tells the Alerts apart (C-29.FR-3, P-67): the first of the Instance labels
// whose values differ among them, else the first label, by name, whose values differ, a missing one counting as
// empty; none when every Alert has the same labels.
func distinguishingLabel(alerts []SourceAlert, instance []string) string {
	if len(alerts) < 2 {
		return ""
	}
	differs := func(name string) bool {
		first := alerts[0].Labels[name]
		return slices.ContainsFunc(alerts[1:], func(a SourceAlert) bool { return a.Labels[name] != first })
	}
	for _, name := range instance {
		if differs(name) {
			return name
		}
	}
	names := map[string]bool{}
	for _, a := range alerts {
		for n := range a.Labels {
			names[n] = true
		}
	}
	for _, n := range slices.Sorted(maps.Keys(names)) {
		if differs(n) {
			return n
		}
	}
	return ""
}

// alertsLine is item 5 (C-29.FR-3): when the Alert Group has more than one Alert, or always for the preview of a line
// template, up to CompactAlertsListed Alerts — firing ones first, then by the value of the distinguishing label in
// natural order, those without it last — named by that value, or each on a line of its own by the Route's line
// template, with the total for "…and K more". An Alert without the label is listed only by a line template; nil
// without Alerts, and without a line template when no label tells them apart.
func (r *Renderer) alertsLine(src *Source, markup Markup, always bool) (*AlertsLine, *Failure) {
	alerts := distinctAlerts(src.Alerts)
	total := int64(len(alerts)) + max(src.TotalAlerts-int64(len(src.Alerts)), 0)
	if len(alerts) == 0 || (total < 2 && !always) {
		return nil, nil
	}
	label := distinguishingLabel(alerts, src.InstanceLabels)
	if label == "" && src.Route.Line == nil {
		return nil, nil
	}
	slices.SortStableFunc(alerts, func(a, b SourceAlert) int {
		if a.Firing != b.Firing {
			if a.Firing {
				return -1
			}
			return 1
		}
		va, vb := a.Labels[label], b.Labels[label]
		if (va == "") != (vb == "") {
			if va == "" {
				return 1
			}
			return -1
		}
		return naturalCompare(va, vb)
	})
	listed := alerts[:min(len(alerts), CompactAlertsListed)]
	out := &AlertsLine{Label: Value(label), Total: total}
	for _, a := range listed {
		if v := a.Labels[label]; label != "" && v != "" {
			out.Values = append(out.Values, AlertValue{Value: Value(v), Resolved: !a.Firing})
		}
	}
	t := src.Route.Line
	if t == nil {
		if len(out.Values) == 0 {
			return nil, nil // no listed Alert has the label: no "pod: …and K more"
		}
		return out, nil
	}
	group, loc := r.groupData(src), location(src.TimeZone)
	for _, a := range listed {
		start := r.real.Now()
		text, err := r.execute(TemplateLine, *t, SafeLine(alertData(a, loc), group, markup))
		observe(TemplateLine, r.real.Now().Sub(start))
		if err != nil {
			return nil, &Failure{Template: TemplateLine, Error: sandboxError(err)}
		}
		out.Lines = append(out.Lines, AlertLine{Text: Neutralize(text), Resolved: !a.Firing, Markup: markup})
	}
	return out, nil
}

// distinct are the distinct values of each label, in natural order, at most AlertsListed per label.
func distinct(alerts []SourceAlert, labels []string) []Distinct {
	var out []Distinct
	for _, n := range labels {
		seen := map[string]bool{}
		for _, a := range alerts {
			if v := a.Labels[n]; v != "" {
				seen[v] = true
			}
		}
		values := slices.SortedFunc(maps.Keys(seen), naturalCompare)
		d := Distinct{Label: Value(n)}
		if len(values) > AlertsListed {
			d.More, values = len(values)-AlertsListed, values[:AlertsListed]
		}
		for _, v := range values {
			d.Values = append(d.Values, Value(v))
		}
		if len(d.Values) > 0 {
			out = append(out, d)
		}
	}
	return out
}

// naturalCompare orders strings with their runs of digits compared as numbers: p2 before p10.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := digitRun(a), digitRun(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if c := cmp.Compare(len(na), len(nb)); c != 0 {
				return c
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return cmp.Compare(a[0], b[0])
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}

func digitRun(s string) int {
	n := 0
	for n < len(s) && unicode.IsDigit(rune(s[n])) {
		n++
	}
	return n
}

// The built-in templates of each kind and language (C-12.FR-2): an editor starts from their source, which
// previewTemplate returns for an empty template. The built-in Root message template is a starting point in the spirit
// of items 3 to 5 of C-29.FR-1, written with what a template reads — not the default message itself, which is laid
// out item by item from the Route's "Labels in the message" and the distinguishing label, so that shortening can cut
// the items in turn.
var builtinSources = map[string]map[string]string{
	TemplateRootMessage: {
		LanguageEnglish: rootSource("resolved"),
		LanguageRussian: rootSource("закрыт"),
	},
	TemplateLine: {
		LanguageEnglish: lineSource,
		LanguageRussian: lineSource,
	},
	TemplateAckTimeoutNotice: {
		LanguageEnglish: "Nobody has acknowledged #{{ .AlertGroup.Number }} {{ .AlertGroup.Title }} yet.",
		LanguageRussian: "Группу алертов #{{ .AlertGroup.Number }} {{ .AlertGroup.Title }} ещё никто не подтвердил.",
	},
}

// lineSource is the built-in line template: the labels of the Alert without the excluded ones.
const lineSource = `{{ range (.Labels.Remove (stringSlice "alertname" "severity" "environment" "prometheus")).SortedPairs -}}
{{ .Name }}: {{ .Value }} {{ end }}`

// rootSource is the built-in Root message template in a language, the word for a resolved Alert: the summary, the
// common description annotation, the labels of the Group key without alertname and severity as `name=value` — not
// "Labels in the message", which templates do not read — and one line per Alert, every Alert, with its labels that are
// neither excluded, in the Group key nor common.
func rootSource(resolved string) string {
	return `{{ with .AlertGroup.Summary }}{{ . }}
{{ end -}}
{{ with .CommonAnnotations.description }}{{ . }}
{{ end -}}
{{ with (.GroupLabels.Remove (stringSlice "alertname" "severity")).SortedPairs }}{{ range $i, $l := . -}}
{{ if $i }} · {{ end }}{{ $l.Name }}={{ $l.Value }}{{ end }}
{{ end -}}
{{ range .Alerts }}• {{ range (((.Labels.Remove (stringSlice "alertname" "severity" "environment" "prometheus")).Remove $.GroupLabels.Names).Remove $.CommonLabels.Names).SortedPairs -}}
{{ .Name }}: {{ .Value }} {{ end }}{{ if eq .Status "resolved" }}(` + resolved + `){{ end }}
{{ end -}}`
}

// BuiltinSource is the source of the built-in template of a kind in a language; empty for the kinds without one.
func BuiltinSource(kind, lang string) string {
	return builtinSources[kind][language(lang)]
}

// Destination tests and previews (C-16.FR-1, FR-4) render a recent Alert Group of one of the Destination's Routes or
// the built-in example; a test sends its Root message once, marked as a test, with buttons whose action ids name the
// Destination instead of the Alert Group, so that a press changes nothing. Nothing of a test is stored.

// TestSample reads the Alert Group publicID with its Alerts and links, as the source of a Destination test or preview;
// an unknown one is ErrNotFound. Its Route is in Source.Route.
func (r *Renderer) TestSample(ctx context.Context, publicID string) (*Source, error) {
	return r.previewGroup(ctx, r.db, publicID)
}

// TestExample is the built-in example of a Destination test or preview, with the settings of the Default route: three
// firing Alerts of one rule in a production cluster, ten minutes old.
func (r *Renderer) TestExample(ctx context.Context) (*Source, error) {
	set, err := r.settings(ctx, r.q(r.db), "")
	if err != nil {
		return nil, err
	}
	src := r.exampleOf(set, "checkout-1", "checkout-2", "checkout-3")
	if err := r.withLinks(ctx, r.db, src); err != nil {
		return nil, err
	}
	return src, nil
}

// TestRoot renders the Root message of src in markup as a Destination test sends it, without recording anything about
// its Route's templates: with the buttons of the Destination's set, src.Buttons (C-16.FR-4, C-28.FR-9); with
// destination, the public_id of the Destination under test, its buttons carry action ids of the subject test that name
// that Destination, whose presses change nothing; with an empty one they are unsigned.
func (r *Renderer) TestRoot(src *Source, markup Markup, destination string) Rendered {
	out := r.root(src, markup, false)
	out.Message = ShownButtons(out.Message, src.Buttons, src.Route.SnoozeDurations)
	if destination == "" || r.keys == nil {
		return out
	}
	m := out.Message
	m.Buttons = slices.Clone(m.Buttons)
	for i, b := range m.Buttons {
		id, kid, err := buttons.Sign(r.keys, buttons.Action{Subject: buttons.SubjectTest, PublicID: destination,
			Command: b.Command, Argument: b.Argument})
		if err == nil {
			m.Buttons[i].ActionID, m.Buttons[i].KeyID, out.KeyID = id, kid, kid
		}
	}
	out.Message = m
	return out
}

// TestMark is the mark before the heading of a test message in a language: "🧪 Test message".
func TestMark(lang string) string {
	return T(lang, "test.mark", nil)
}
