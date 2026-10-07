// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package templates

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// The Alertmanager statuses of the template data.
const (
	StatusFiring   = "firing"
	StatusResolved = "resolved"
)

// Data is what a Root message template sees: Alertmanager's template data — Status, Alerts, GroupLabels,
// CommonLabels, CommonAnnotations and ExternalURL — and the Alert Group. Label and annotation values arrive escaped
// for the target markup, with `@` neutralized and cut to message.value_cap, so safeHtml returns its argument
// unchanged.
type Data struct {
	Receiver          string
	Status            string
	Alerts            Alerts
	GroupLabels       KV
	CommonLabels      KV
	CommonAnnotations KV
	ExternalURL       string
	AlertGroup        AlertGroup
}

// Alert is one Alert of the template data.
type Alert struct {
	Status       string
	Labels       KV
	Annotations  KV
	StartsAt     time.Time
	EndsAt       time.Time
	GeneratorURL string
	Fingerprint  string
}

// LineData is what a line template sees: one Alert, whose fields it reads directly (`.Labels.pod`), and the Alert
// Group.
type LineData struct {
	Alert
	AlertGroup AlertGroup
}

// AlertGroup is the Alert Group of the template data.
type AlertGroup struct {
	Number        int64
	Title         string
	Summary       string
	Status        string
	Route         string
	SeverityLevel string
	Urgent        bool
	StartedAt     time.Time
	URL           string
	ReopenCount   int64
	Owner         string
}

// Alerts is a list of Alerts, with Alertmanager's Firing and Resolved.
type Alerts []Alert

// Firing are the firing Alerts.
func (as Alerts) Firing() []Alert {
	return as.with(StatusFiring)
}

// Resolved are the resolved Alerts.
func (as Alerts) Resolved() []Alert {
	return as.with(StatusResolved)
}

func (as Alerts) with(status string) []Alert {
	out := []Alert{}
	for _, a := range as {
		if a.Status == status {
			out = append(out, a)
		}
	}
	return out
}

// KV is a set of labels or annotations, with Alertmanager's SortedPairs, Names, Values and Remove.
type KV map[string]string

// Pair is a label or annotation with its name and value.
type Pair struct {
	Name, Value string
}

// Pairs is a list of Pairs, with Alertmanager's Names and Values.
type Pairs []Pair

// Names are the names of the pairs.
func (ps Pairs) Names() []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// Values are the values of the pairs.
func (ps Pairs) Values() []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Value
	}
	return out
}

// SortedPairs are the pairs sorted by name, alertname first, as Alertmanager sorts them.
func (kv KV) SortedPairs() Pairs {
	names := slices.SortedFunc(maps.Keys(kv), func(a, b string) int {
		switch {
		case a == b:
			return 0
		case a == "alertname":
			return -1
		case b == "alertname":
			return 1
		}
		return strings.Compare(a, b)
	})
	out := make(Pairs, len(names))
	for i, n := range names {
		out[i] = Pair{Name: n, Value: kv[n]}
	}
	return out
}

// Names are the sorted names.
func (kv KV) Names() []string {
	return kv.SortedPairs().Names()
}

// Values are the values in the order of the sorted names.
func (kv KV) Values() []string {
	return kv.SortedPairs().Values()
}

// Remove is a copy without the names given.
func (kv KV) Remove(names []string) KV {
	out := maps.Clone(kv)
	if out == nil {
		out = KV{}
	}
	for _, n := range names {
		delete(out, n)
	}
	return out
}
