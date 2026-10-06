// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParsePayload(t *testing.T) {
	body := `{"version":"4","groupKey":"{}/{team=\"db\"}:{alertname=\"DiskFull\"}","truncatedAlerts":2,
		"status":"firing","receiver":"lab","notification_reason":"repeat interval elapsed","alerts":[
		{"status":"firing","labels":{"alertname":"DiskFull","instance":"db-a"},"annotations":{"summary":"full"},
		 "startsAt":"2026-10-06T12:00:00.5+02:00","endsAt":"0001-01-01T00:00:00Z","generatorURL":"http://p/g",
		 "fingerprint":"0123456789abcdef"},
		{"status":"resolved","labels":{"alertname":"DiskFull","instance":"db-b"},"startsAt":"2026-10-06T10:00:00Z",
		 "endsAt":"2026-10-06T11:00:00Z"}]}`
	p, err := ParsePayload([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if p.GroupKey != `{}/{team="db"}:{alertname="DiskFull"}` || p.Status != StatusFiring || p.TruncatedAlerts != 2 ||
		!p.HasReason || p.Reason != ReasonRepeat || len(p.Alerts) != 2 {
		t.Fatalf("payload = %+v", p)
	}
	a, b := p.Alerts[0], p.Alerts[1]
	if a.Fingerprint != "0123456789abcdef" || a.Status != StatusFiring || a.Labels["instance"] != "db-a" ||
		a.Annotations["summary"] != "full" || !a.StartsAt.Equal(time.Date(2026, 10, 6, 10, 0, 0, 5e8, time.UTC)) ||
		a.StartsAt.Location() != time.UTC || a.EndsAt != nil || a.GeneratorURL != "http://p/g" {
		t.Errorf("first alert = %+v", a)
	}
	if b.Fingerprint != Fingerprint(b.Labels) || b.Status != StatusResolved || len(b.Annotations) != 0 ||
		b.EndsAt == nil || !b.EndsAt.Equal(time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("second alert = %+v", b)
	}
	// Alertmanager stamps an Alert posted without startsAt to the nanosecond; PostgreSQL keeps microseconds.
	p, err = ParsePayload([]byte(`{"groupKey":"g","status":"firing","alerts":[{"status":"resolved","labels":{},` +
		`"startsAt":"2026-10-06T10:00:00.123456789Z","endsAt":"2026-10-06T11:00:00.987654321Z"}]}`))
	if err != nil || !p.Alerts[0].StartsAt.Equal(time.Date(2026, 10, 6, 10, 0, 0, 123456000, time.UTC)) ||
		!p.Alerts[0].EndsAt.Equal(time.Date(2026, 10, 6, 11, 0, 0, 987654000, time.UTC)) {
		t.Errorf("nanoseconds: %+v, %v", p.Alerts, err)
	}
	p, err = ParsePayload([]byte(`{"groupKey":"{}:{}","status":"resolved","alerts":[]}`))
	if err != nil || p.HasReason || p.TruncatedAlerts != 0 || len(p.Alerts) != 0 {
		t.Errorf("minimal payload = %+v, %v", p, err)
	}
}

func TestParsePayloadErrors(t *testing.T) {
	alert := func(fields string) string {
		return `{"groupKey":"g","status":"firing","alerts":[` + fields + `]}`
	}
	for _, tt := range []struct {
		body, want string
	}{
		{`not json`, "the body is not valid JSON: invalid character 'o' in literal null (expecting 'u')"},
		{``, "the body is not valid JSON: unexpected end of JSON input"},
		{`[1]`, "the body is not an Alertmanager webhook: the value has the wrong type (array)"},
		{`{"groupKey":1}`, "the body is not an Alertmanager webhook: groupKey has the wrong type (number)"},
		{`{"status":"firing","alerts":[]}`, "the webhook has no groupKey"},
		{`{"groupKey":"g","alerts":[]}`, "the webhook has no status"},
		{`{"groupKey":"g","status":"ok","alerts":[]}`, `the status of the webhook is "ok", not firing or resolved`},
		{`{"groupKey":"g","status":"firing"}`, "the webhook has no alerts"},
		{`{"groupKey":"g","status":"firing","alerts":null}`, "the webhook has no alerts"},
		{`{"groupKey":"g","status":"firing","truncatedAlerts":-1,"alerts":[]}`, "truncatedAlerts is negative"},
		{alert(`1`), "alert 0: not an alert: the value has the wrong type (number)"},
		{alert(`{"labels":{},"startsAt":"2026-10-06T10:00:00Z"}`), "alert 0: no status"},
		{alert(`{"status":"x","labels":{},"startsAt":"2026-10-06T10:00:00Z"}`),
			`alert 0: the status is "x", not firing or resolved`},
		{alert(`{"status":"firing","startsAt":"2026-10-06T10:00:00Z"}`), "alert 0: labels are missing"},
		{alert(`{"status":"firing","labels":{"a":1},"startsAt":"2026-10-06T10:00:00Z"}`),
			"alert 0: labels are not an object of strings"},
		{alert(`{"status":"firing","labels":{},"annotations":[],"startsAt":"2026-10-06T10:00:00Z"}`),
			"alert 0: annotations are not an object of strings"},
		{alert(`{"status":"firing","labels":{}}`), "alert 0: no startsAt"},
		{alert(`{"status":"firing","labels":{},"startsAt":"yesterday"}`), "alert 0: startsAt is not an RFC 3339 time"},
		{alert(`{"status":"firing","labels":{},"startsAt":"2026-10-06T10:00:00Z","endsAt":"x"}`),
			"alert 0: endsAt is not an RFC 3339 time"},
		{`{"groupKey":"g\u0000","status":"firing","alerts":[]}`,
			"the webhook has a NUL character, which PostgreSQL cannot store"},
		{`{"groupKey":"g","status":"firing","notification_reason":"\u0000","alerts":[]}`,
			"the webhook has a NUL character, which PostgreSQL cannot store"},
		{alert(`{"status":"firing","labels":{"a\u0000":"b"},"startsAt":"2026-10-06T10:00:00Z"}`),
			"alert 0: a NUL character, which PostgreSQL cannot store"},
		{alert(`{"status":"firing","labels":{},"annotations":{"a":"\u0000"},"startsAt":"2026-10-06T10:00:00Z"}`),
			"alert 0: a NUL character, which PostgreSQL cannot store"},
		{alert(`{"status":"firing","labels":{},"startsAt":"2026-10-06T10:00:00Z","generatorURL":"\u0000"}`),
			"alert 0: a NUL character, which PostgreSQL cannot store"},
	} {
		_, err := ParsePayload([]byte(tt.body))
		var pe *PayloadError
		if !errors.As(err, &pe) || err.Error() != tt.want {
			t.Errorf("ParsePayload(%s) = %v, want %s", tt.body, err, tt.want)
		}
	}
}

// TestFingerprint checks the fingerprint against the label signatures of the Prometheus model that Alertmanager uses.
func TestFingerprint(t *testing.T) {
	for _, tt := range []struct {
		labels map[string]string
		want   string
	}{
		{map[string]string{}, "cbf29ce484222325"},
		{map[string]string{"name": "garland, briggs", "fear": "love is not enough"}, "507a62d79ee76c9a"},
	} {
		if got := Fingerprint(tt.labels); got != tt.want || len(got) != 16 || strings.ToLower(got) != got {
			t.Errorf("Fingerprint(%v) = %s, want %s", tt.labels, got, tt.want)
		}
	}
	a := Fingerprint(map[string]string{"a": "b", "c": "d"})
	if b := Fingerprint(map[string]string{"c": "d", "a": "b"}); a != b {
		t.Error("the fingerprint depends on the order of the labels")
	}
	if b := Fingerprint(map[string]string{"a": "bc", "": "d"}); a == b {
		t.Error("the separator does not separate")
	}
}
