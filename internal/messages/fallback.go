// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"maps"
	"slices"
	"strings"
)

// The Fallback template (C-12.FR-6, ADR-0012): built in and always valid, it stands in for a Route template that
// failed — under Muster's heading, links, notices, footer and buttons, it shows every label of the Alert Group and of
// each of its Alerts. It runs no template, so nothing in the data can make it fail.

// fallback is the Fallback message of src in the frame m: the notice that the Route's template failed, the common
// labels of the Alert Group and one line per Alert with all its labels; the distinct values of the labels that differ
// stand ready for shortening.
func (r *Renderer) fallback(m Message, src *Source, lang string) Message {
	m.Notices = append([]string{T(lang, "fallback.notice", nil)}, m.Notices...)
	m.Environment, m.GroupLabels, m.CommonLabels, m.CommonAnnotations, m.Summary, m.Body = "", nil, nil, nil, "", nil
	labels := map[string]string{}
	maps.Copy(labels, src.KeyValues)
	maps.Copy(labels, src.CommonLabels)
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		if labels[name] != "" {
			m.CommonLabels = append(m.CommonLabels, Label{Name: name, Value: Value(labels[name])})
		}
	}
	if len(src.Alerts) == 0 {
		m.Alerts = nil
		return m
	}
	names := map[string]bool{}
	list := &AlertList{}
	for _, a := range src.Alerts {
		parts := make([]string, 0, len(a.Labels))
		for _, name := range slices.Sorted(maps.Keys(a.Labels)) {
			names[name] = true
			parts = append(parts, name+"="+Value(a.Labels[name]))
		}
		list.Lines = append(list.Lines, AlertLine{Text: strings.Join(parts, ", "), Resolved: !a.Firing})
	}
	var differing []string
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if _, common := labels[name]; !common {
			differing = append(differing, name)
		}
	}
	list.Distinct = distinct(src.Alerts, differing)
	if src.TotalAlerts > int64(len(src.Alerts)) {
		list.FullList = &Link{Text: T(lang, "alerts.fullList", nil), URL: r.GroupURL(src.PublicID)}
	}
	m.Alerts = list
	return m
}
