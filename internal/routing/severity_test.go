// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"testing"

	"github.com/muster-io/muster/internal/organization"
)

// TestSeverity is C-08.FR-6 and C-08.AC-9 with the default organization.severity_mapping: critical, warning and info
// map to their levels and none to info; a value without a mapping is warning shown as received; an Alert without the
// label, or with an empty value, is info.
func TestSeverity(t *testing.T) {
	d := organization.Defaults()
	s := NewSeverities(d.SeverityLabel, d.SeverityMapping)
	for _, c := range []struct {
		labels map[string]string
		want   Severity
	}{
		{map[string]string{"severity": "critical"}, Severity{Level: organization.SeverityCritical}},
		{map[string]string{"severity": "warning"}, Severity{Level: organization.SeverityWarning}},
		{map[string]string{"severity": "info"}, Severity{Level: organization.SeverityInfo}},
		{map[string]string{"severity": "none"}, Severity{Level: organization.SeverityInfo}},
		{map[string]string{"severity": "P5"}, Severity{Level: organization.SeverityWarning, Raw: "P5"}},
		{map[string]string{"severity": "Critical"}, Severity{Level: organization.SeverityWarning, Raw: "Critical"}},
		{map[string]string{"severity": ""}, Severity{Level: organization.SeverityInfo}},
		{map[string]string{"level": "critical"}, Severity{Level: organization.SeverityInfo}},
		{nil, Severity{Level: organization.SeverityInfo}},
	} {
		if got := s.Of(c.labels); got != c.want {
			t.Errorf("%v = %+v, want %+v", c.labels, got, c.want)
		}
	}
}

// TestSeverityMapping: another severity label and mapping, the first mapping of a value winning, as the
// organizations row stores them.
func TestSeverityMapping(t *testing.T) {
	s, err := severitiesOf("priority", []byte(`[{"value":"P1","level":"critical"},{"value":"P2","level":"warning"},`+
		`{"value":"P1","level":"info"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Of(map[string]string{"priority": "P1", "severity": "info"}); got.Level != organization.SeverityCritical {
		t.Errorf("P1 = %+v", got)
	}
	if got := s.Of(map[string]string{"severity": "critical"}); got.Level != organization.SeverityInfo {
		t.Errorf("without the label = %+v", got)
	}
	if _, err := severitiesOf("severity", []byte(`{}`)); err == nil {
		t.Error("an object read as a mapping")
	}
}
