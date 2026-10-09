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
)

// The default Root message (C-12.FR-1): its exclusions and the limits of its Alerts section.

// AlertsListed is message.alerts_listed: one line per Alert up to it; above it, the distinct values of each differing
// label, at most AlertsListed per label.
const AlertsListed = 10

// excludedLabels are left out of the common labels and the Alert lines (C-12.FR-1 items 5 and 8), with the group
// labels.
var excludedLabels = []string{"severity", "environment", "prometheus", "alertname"}

// excludedAnnotation reports whether a common annotation is left out (item 6): the summary, which item 7 shows, the
// description and the runbook links.
func excludedAnnotation(name string) bool {
	return name == "summary" || name == "description" || strings.HasPrefix(name, "runbook")
}

// frame is what Muster always shows around the body (C-12.FR-2): the colour of the status, the heading linked to the
// Alert Group page, the link to Muster, the notices that apply, the footer and the buttons of the status, with the id
// of the key that signed them.
func (r *Renderer) frame(src *Source, lang string, sign bool) (Message, string) {
	url := r.GroupURL(src.PublicID)
	m := Message{Kind: KindRoot, Language: lang, TimeZone: src.TimeZone, Colour: src.Status,
		Heading: &Heading{Number: src.Number, Title: Value(src.Title), URL: url},
		Links:   append([]Link{{Text: T(lang, "link.open", nil), URL: url}}, linksOf(src.Links, lang)...),
		Notices: notices(src, lang), Footer: footer(src, lang, ""), Buttons: []Button{}}
	for space, name := range src.FooterNames {
		if f := footer(src, lang, name); f != "" && f != m.Footer {
			if m.Footers == nil {
				m.Footers = map[string]string{}
			}
			m.Footers[space] = f
		}
	}
	keyID := ""
	for _, b := range buttons.ForStatus(src.Status, len(src.Route.SnoozeDurations)) {
		btn := Button{Command: b.Command, Argument: b.Argument, Label: buttonLabel(lang, b, src.Route.SnoozeDurations)}
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

// notices are the notices of reference.md that apply to src (item 10): still firing after a manual resolve,
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

// footer is item 11: who acknowledged, resolved or snoozed the Alert Group, or why the system resolved it. The user
// is named by username when it is set — the messenger username of their Account link — and by the Muster display
// name otherwise, which mentions nobody (C-12.FR-12).
func footer(src *Source, lang, username string) string {
	user := func(name string) string { return Value(cmp.Or(username, name)) }
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

// defaultBody fills items 3 to 7 of the default Root message: the environment and start time, the group labels, the
// common labels and annotations without the excluded ones, and the summary. Values are cut and neutralized; the
// adapters escape them.
func (r *Renderer) defaultBody(m *Message, src *Source, lang string) {
	m.Environment = environment(src, lang)
	for _, name := range src.KeyLabels {
		if v := src.KeyValues[name]; name != "alertname" && v != "" {
			m.GroupLabels = append(m.GroupLabels, Label{Name: Value(name), Value: Value(v)})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(src.CommonLabels)) {
		if !slices.Contains(excludedLabels, name) && !slices.Contains(src.KeyLabels, name) {
			m.CommonLabels = append(m.CommonLabels, Label{Name: Value(name), Value: Value(src.CommonLabels[name])})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(src.CommonAnnotations)) {
		if !excludedAnnotation(name) {
			m.CommonAnnotations = append(m.CommonAnnotations, Label{Name: Value(name),
				Value: Value(src.CommonAnnotations[name])})
		}
	}
	m.Summary = Value(src.Summary)
}

// environment is item 3: the cluster and environment labels when present and the start time, absolute in the
// Organization's time zone.
func environment(src *Source, lang string) string {
	var parts []string
	for _, l := range []string{"cluster", "environment"} {
		if v := src.CommonLabels[l]; v != "" {
			parts = append(parts, Value(v))
		}
	}
	parts = append(parts, T(lang, "environment.started", Args{"time": FullTime(src.StartedAt, src.TimeZone)}))
	return strings.Join(parts, " · ")
}

// alertList is item 8: up to AlertsListed Alerts one line each — the Route's line template or the labels that differ
// between the Alerts — firing and newest first, resolved ones struck through; above it the distinct values of each
// differing label and "Full list in Muster". The distinct values are kept in either case, for shortening.
func (r *Renderer) alertList(src *Source, lang string, markup Markup) (*AlertList, *Failure) {
	if len(src.Alerts) == 0 {
		return nil, nil
	}
	differing := differingLabels(src)
	list := &AlertList{Distinct: distinct(src.Alerts, differing)}
	if max(src.TotalAlerts, int64(len(src.Alerts))) > AlertsListed {
		list.FullList = &Link{Text: T(lang, "alerts.fullList", nil), URL: r.GroupURL(src.PublicID)}
		return list, nil
	}
	group, loc := r.groupData(src), location(src.TimeZone)
	for _, a := range src.Alerts {
		line := AlertLine{Resolved: !a.Firing}
		if t := src.Route.Line; t != nil {
			start := r.real.Now()
			out, err := r.execute(TemplateLine, *t, SafeLine(alertData(a, loc), group, markup))
			observe(TemplateLine, r.real.Now().Sub(start))
			if err != nil {
				return nil, &Failure{Template: TemplateLine, Error: sandboxError(err)}
			}
			line.Text, line.Markup = Neutralize(out), markup
		} else {
			line.Text = defaultLine(a, differing)
		}
		list.Lines = append(list.Lines, line)
	}
	return list, nil
}

// defaultLine is the labels of an Alert that differ between the Alerts of its Alert Group; an Alert alone is named by
// its alertname, or its fingerprint.
func defaultLine(a SourceAlert, differing []string) string {
	var parts []string
	for _, name := range differing {
		if v, ok := a.Labels[name]; ok {
			parts = append(parts, Value(name)+": "+Value(v))
		}
	}
	if len(parts) == 0 {
		return Value(cmp.Or(a.Labels["alertname"], a.Fingerprint))
	}
	return strings.Join(parts, ", ")
}

// differingLabels are the labels whose values are not the same in every Alert, a missing one counting as empty,
// except the excluded ones and the group labels, sorted.
func differingLabels(src *Source) []string {
	names := map[string]bool{}
	for _, a := range src.Alerts {
		for n := range a.Labels {
			names[n] = true
		}
	}
	var out []string
	for _, n := range slices.Sorted(maps.Keys(names)) {
		if slices.Contains(excludedLabels, n) || slices.Contains(src.KeyLabels, n) {
			continue
		}
		first, same := src.Alerts[0].Labels[n], true
		for _, a := range src.Alerts[1:] {
			if a.Labels[n] != first {
				same = false
				break
			}
		}
		if !same {
			out = append(out, n)
		}
	}
	return out
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
// previewTemplate returns for an empty template. They show what the default message shows; the default message
// itself is laid out section by section, so that shortening can trim the Alerts and the label sections in turn.
var builtinSources = map[string]map[string]string{
	TemplateRootMessage: {
		LanguageEnglish: rootSource("Started", "resolved"),
		LanguageRussian: rootSource("Начало:", "закрыт"),
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

// rootSource is the built-in Root message template in a language: the words for the start and for a resolved Alert.
func rootSource(started, resolved string) string {
	return `{{ with .CommonLabels.cluster }}{{ . }} · {{ end }}{{ with .CommonLabels.environment }}{{ . }} · {{ end -}}
` + started + ` {{ .AlertGroup.StartedAt | date "2006-01-02 15:04 MST" }}
{{ range (.GroupLabels.Remove (stringSlice "alertname")).SortedPairs }}{{ .Name }}: {{ .Value }}
{{ end -}}
{{ range ((.CommonLabels.Remove (stringSlice "severity" "environment" "prometheus" "alertname")).Remove .GroupLabels.Names).SortedPairs -}}
{{ .Name }}: {{ .Value }}
{{ end -}}
{{ range (.CommonAnnotations.Remove (stringSlice "summary" "description" "runbook_url")).SortedPairs -}}
{{ .Name }}: {{ .Value }}
{{ end -}}
{{ with .AlertGroup.Summary }}{{ . }}
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
// its Route's templates: with destination, the public_id of the Destination under test, its buttons carry action ids of
// the subject test that name that Destination, whose presses change nothing; with an empty one they are unsigned.
func (r *Renderer) TestRoot(src *Source, markup Markup, destination string) Rendered {
	out := r.root(src, markup, false)
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
