// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"crypto/sha256"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// keyOf is the Group key of a Route evaluated on the labels of an Alert (C-08.FR-4): the value of each label of the
// key, a missing label as the empty value, and the SHA-256 over the labels and their values in the key's order.
func keyOf(key []string, labels map[string]string) (map[string]string, []byte) {
	values := make(map[string]string, len(key))
	h := sha256.New()
	for _, name := range key {
		v := labels[name]
		values[name] = v
		h.Write([]byte(strconv.Quote(name) + "=" + strconv.Quote(v) + ","))
	}
	return values, h.Sum(nil)
}

// keyText is the Group key values as a title: "cluster=prod, namespace=payments", in the key's order.
func keyText(key []string, values map[string]string) string {
	parts := make([]string, len(key))
	for i, name := range key {
		parts[i] = name + "=" + values[name]
	}
	return strings.Join(parts, ", ")
}

// newTitle is the title of a new Alert Group (C-09.FR-23): the alertname of its first Alert; without one, the Group
// key values, which count as the switch already made; without either, the fingerprint of the first Alert.
func newTitle(a *alertRow, key []string, values map[string]string) (string, bool) {
	if name := a.Labels["alertname"]; name != "" {
		return name, false
	}
	if t := keyText(key, values); t != "" {
		return t, true
	}
	return a.Fingerprint, false
}

// joined updates what an Alert joining g changes besides its counts (C-09.FR-23): the one allowed title switch, when
// an Alert with another alertname joins and the Group key gives a title; the summary, from the first Alert with a
// summary annotation; the common labels and annotations; and the Integrations.
func (g *Group) joined(a *alertRow) {
	if !g.TitleFromKey && a.Labels["alertname"] != g.Title {
		if t := keyText(g.KeyLabels, g.KeyValues); t != "" {
			g.Title, g.TitleFromKey = t, true
		}
	}
	if g.Summary == nil {
		if s := a.Annotations["summary"]; s != "" {
			g.Summary = &s
		}
	}
	g.CommonLabels = common(g.CommonLabels, a.Labels)
	g.CommonAnnots = common(g.CommonAnnots, a.Annotations)
	if !slices.Contains(g.IntegrationIDs, a.IntegrationID) {
		g.IntegrationIDs = append(g.IntegrationIDs, a.IntegrationID)
		slices.Sort(g.IntegrationIDs)
	}
}

// common keeps the pairs of have that next has with the same value.
func common(have, next map[string]string) map[string]string {
	out := make(map[string]string, len(have))
	for k, v := range have {
		if w, ok := next[k]; ok && w == v {
			out[k] = v
		}
	}
	return out
}

// replacedLabel is the label a new Alert differs in from a firing one when it differs only in Instance labels
// (C-09.FR-7), the labels joined by ", " when several differ; empty when it is no Replacement of it.
func replacedLabel(next, firing map[string]string, instance []string) string {
	keys := slices.Collect(maps.Keys(next))
	for k := range firing {
		if _, ok := next[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var differ []string
	for _, k := range keys {
		v, inNext := next[k]
		w, inFiring := firing[k]
		if inNext == inFiring && v == w {
			continue
		}
		if !slices.Contains(instance, k) {
			return ""
		}
		differ = append(differ, k)
	}
	return strings.Join(differ, ", ")
}
