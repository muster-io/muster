// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package internalalerts holds the closed registry of Internal alerts (C-06.FR-14, ADR-0014) and raises and resolves
// them. An Internal alert is an Alert of the built-in "Muster" Integration: each raise and each resolve is a synthetic
// Stored Snapshot of that Integration, marked internal, written in the caller's transaction and processed by the
// Snapshot worker in order with the Integration's other Snapshots. A configuration entity appears in the labels by its
// immutable id, with its current name in a separate label, and the fingerprint is computed from alertname and the id
// labels only, so that renaming the entity updates the open Internal alert instead of starting a new one.
package internalalerts

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The severities of Internal alerts.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

// The entity labels of Internal alerts: the immutable id of a configuration entity, with its name in a label of its
// own.
const (
	EntityIntegration = "integration"
	EntityRoute       = "route"
	EntityDestination = "destination"
)

// Definition is one Internal alert of the registry.
type Definition struct {
	// Name is the alertname, shared with the chart rule for the same condition.
	Name     string
	Severity string
	// Entity is the label of the entity's immutable id, and NameLabel the label of its current name; Extra are the
	// other labels it carries.
	Entity    string
	NameLabel string
	Extra     []string
	// StaticLabels adds the Static labels of the Integration it is about; the labels above win over a Static label
	// of the same name.
	StaticLabels bool
	// Condition says while what it fires; Summary and Description are its annotations, where {name} is the entity's
	// current name.
	Condition   string
	Summary     string
	Description string
	// Capability raises it.
	Capability string
}

// Runbook is the page of the documentation site for the Internal alert, under the runbook base URL.
func (d *Definition) Runbook() string {
	return "operations/runbooks/" + d.Name + "/"
}

// Labels are the label names it carries besides alertname and severity, in order.
func (d *Definition) Labels() []string {
	out := []string{}
	if d.Entity != "" {
		out = append(out, d.Entity, d.NameLabel)
	}
	return append(out, d.Extra...)
}

// reserved reports whether a Static label named name gives way to a label of d itself.
func (d *Definition) reserved(name string) bool {
	return name == "alertname" || name == "severity" || slices.Contains(d.Labels(), name)
}

// SnapshotTruncated is raised while any groupKey of an Integration is truncated (C-06.FR-6).
var SnapshotTruncated = register(&Definition{
	Name:      "MusterSnapshotTruncated",
	Severity:  SeverityWarning,
	Entity:    EntityIntegration,
	NameLabel: "integration_name",
	Condition: "An Integration has a truncated groupKey: Alertmanager cut a Snapshot with max_alerts, so Muster keeps " +
		"the unlisted Alerts alive and cannot resolve them by absence. It resolves with the next untruncated " +
		"Snapshot of every truncated groupKey.",
	Summary: "Alertmanager truncates the Snapshots of Integration {name}",
	Description: "Alertmanager sent a Snapshot with truncatedAlerts above 0 for Integration {name}: the receiver " +
		"has max_alerts set. Muster keeps the Alerts it does not list alive and cannot tell when they resolve. Set " +
		"max_alerts: 0 on the Muster receiver.",
	Capability: "C-06",
})

// HeartbeatLost is raised while an Integration is Heartbeat lost (C-07.FR-4).
var HeartbeatLost = register(&Definition{
	Name:         "MusterHeartbeatLost",
	Severity:     SeverityCritical,
	Entity:       EntityIntegration,
	NameLabel:    "integration_name",
	StaticLabels: true,
	Condition: "An Integration is Heartbeat lost: no Heartbeat signal arrived within its Heartbeat timeout, so the " +
		"path from its Alertmanager to Muster may be broken and Stale resolution is paused. It resolves with the next " +
		"signal, when the Heartbeat is turned off or when the Integration is deleted.",
	Summary: "No Heartbeat from the Alertmanager of Integration {name}",
	Description: "Muster has received no Heartbeat signal for Integration {name} within its Heartbeat timeout: " +
		"Alertmanager, its network path to Muster or the Heartbeat route may be broken, and new alerts may not arrive. " +
		"Nothing is resolved as Stale until the signal returns.",
	Capability: "C-07",
})

var (
	registry []*Definition
	nameRe   = regexp.MustCompile(`^Muster[A-Z][A-Za-z0-9]*$`)
	labelRe  = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// register adds an Internal alert to the registry; only this package declares them.
func register(d *Definition) *Definition {
	registry = append(registry, d)
	return d
}

// Lookup is the Internal alert named name, or nil.
func Lookup(name string) *Definition {
	for _, d := range registry {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// Definitions returns the registry sorted by name, after checking it: names are unique and start with Muster, the
// severity is known, an entity label has a name label, label names are valid and unique, and the texts are set.
func Definitions() ([]*Definition, error) {
	var errs []error
	seen := map[string]bool{}
	for _, d := range registry {
		switch {
		case !nameRe.MatchString(d.Name):
			errs = append(errs, fmt.Errorf("internal alert %q: the name is not Muster<Name>", d.Name))
		case seen[d.Name]:
			errs = append(errs, fmt.Errorf("internal alert %q is registered twice", d.Name))
		}
		seen[d.Name] = true
		if d.Severity != SeverityCritical && d.Severity != SeverityWarning {
			errs = append(errs, fmt.Errorf("internal alert %q: unknown severity %q", d.Name, d.Severity))
		}
		switch d.Entity {
		case "":
			if d.NameLabel != "" {
				errs = append(errs, fmt.Errorf("internal alert %q: a name label without an entity", d.Name))
			}
		case EntityIntegration, EntityRoute, EntityDestination:
			if d.NameLabel != d.Entity+"_name" {
				errs = append(errs, fmt.Errorf("internal alert %q: the name label of %s is %s_name", d.Name, d.Entity,
					d.Entity))
			}
		default:
			errs = append(errs, fmt.Errorf("internal alert %q: unknown entity %q", d.Name, d.Entity))
		}
		labels := d.Labels()
		for _, l := range labels {
			if !labelRe.MatchString(l) || l == "alertname" || l == "severity" {
				errs = append(errs, fmt.Errorf("internal alert %q: invalid label %q", d.Name, l))
			}
		}
		if len(slices.Compact(slices.Sorted(slices.Values(labels)))) != len(labels) {
			errs = append(errs, fmt.Errorf("internal alert %q: a label is listed twice", d.Name))
		}
		if d.StaticLabels && d.Entity != EntityIntegration {
			errs = append(errs, fmt.Errorf("internal alert %q: Static labels need the integration entity", d.Name))
		}
		if strings.TrimSpace(d.Condition) == "" || strings.TrimSpace(d.Summary) == "" ||
			strings.TrimSpace(d.Description) == "" || d.Capability == "" {
			errs = append(errs, fmt.Errorf("internal alert %q: condition, summary, description and capability are "+
				"required", d.Name))
		}
	}
	out := slices.Clone(registry)
	slices.SortFunc(out, func(a, b *Definition) int { return strings.Compare(a.Name, b.Name) })
	return out, errors.Join(errs...)
}
