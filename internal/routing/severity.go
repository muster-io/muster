// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"encoding/json"
	"fmt"

	"github.com/muster-io/muster/internal/organization"
)

// Severity is the Severity level of an Alert and, when its value has no mapping, the value as received.
type Severity struct {
	Level organization.SeverityLevel
	// Raw is the value of the severity label shown as received; empty when the value is mapped or missing.
	Raw string
}

// Severities map the severity label of Alerts to Severity levels by the Organization's settings
// (organization.severity_label and organization.severity_mapping, C-08.FR-6).
type Severities struct {
	label   string
	mapping map[string]organization.SeverityLevel
}

// NewSeverities returns the Severity levels by the label and its mapping; the first mapping of a value wins.
func NewSeverities(label string, mapping []organization.SeverityMapping) Severities {
	s := Severities{label: label, mapping: make(map[string]organization.SeverityLevel, len(mapping))}
	for _, m := range mapping {
		if _, taken := s.mapping[m.Value]; !taken {
			s.mapping[m.Value] = m.Level
		}
	}
	return s
}

// severitiesOf reads the Severity level settings as the organizations row stores them.
func severitiesOf(label string, mappingJSON []byte) (Severities, error) {
	var mapping []organization.SeverityMapping
	if err := json.Unmarshal(mappingJSON, &mapping); err != nil {
		return Severities{}, fmt.Errorf("read the severity mapping: %w", err)
	}
	return NewSeverities(label, mapping), nil
}

// Of is the Severity level of an Alert with the labels: the level its severity value maps to; warning, keeping the
// value as received, for a value without a mapping; info for an Alert without the label or with an empty value.
func (s Severities) Of(labels map[string]string) Severity {
	value := labels[s.label]
	if value == "" {
		return Severity{Level: organization.SeverityInfo}
	}
	if level, ok := s.mapping[value]; ok {
		return Severity{Level: level}
	}
	return Severity{Level: organization.SeverityWarning, Raw: value}
}
