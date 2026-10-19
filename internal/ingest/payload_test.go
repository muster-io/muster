// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakealertmanager"
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

// capturedBody is one line of the fixtures in testdata/grafana: a body as Grafana sent it, with its time of arrival and
// the receiver's path.
type capturedBody struct {
	Recv time.Time       `json:"recv"`
	Path string          `json:"path"`
	Body json.RawMessage `json:"body"`
}

func readCaptured(t *testing.T, name string) []capturedBody {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "grafana", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []capturedBody
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var c capturedBody
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("%s line %d: %v", name, len(out)+1, err)
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestParsePayloadGrafana is C-06.FR-8 and C-06.FR-21: every body Grafana 13.2.4 sent on the development stand parses
// as a Snapshot from Grafana without notification_reason, each alert with the fingerprint of the body, Grafana's other
// fields accepted and not read; a body of the fake Alertmanager, and one whose orgId is null, is not from Grafana.
func TestParsePayloadGrafana(t *testing.T) {
	for name, n := range map[string]int{"ha-main.jsonl": 84, "trunc.jsonl": 6} {
		bodies := readCaptured(t, name)
		if len(bodies) != n {
			t.Fatalf("%s: %d bodies, want %d", name, len(bodies), n)
		}
		for i, c := range bodies {
			p, err := ParsePayload(c.Body)
			if err != nil {
				t.Fatalf("%s line %d: %v", name, i+1, err)
			}
			var raw struct {
				Alerts []struct {
					Fingerprint string `json:"fingerprint"`
				} `json:"alerts"`
			}
			if err := json.Unmarshal(c.Body, &raw); err != nil {
				t.Fatal(err)
			}
			if !p.Grafana || p.HasReason || len(p.Alerts) == 0 || len(p.Alerts) != len(raw.Alerts) {
				t.Fatalf("%s line %d: grafana %v, reason %v, %d alerts", name, i+1, p.Grafana, p.HasReason,
					len(p.Alerts))
			}
			for j, a := range p.Alerts {
				if a.Fingerprint == "" || a.Fingerprint != raw.Alerts[j].Fingerprint {
					t.Errorf("%s line %d alert %d: fingerprint %q, want %q", name, i+1, j, a.Fingerprint,
						raw.Alerts[j].Fingerprint)
				}
			}
		}
	}
	// Grafana's fingerprint covers the hidden __alert_rule_uid__ (F-107): the labels alone hash to another one.
	p, err := ParsePayload(readCaptured(t, "ha-main.jsonl")[2].Body)
	if err != nil || p.Alerts[0].Labels["alertname"] != "SpikeMem" || p.Alerts[0].Fingerprint != "0b7e255d753c35de" ||
		Fingerprint(p.Alerts[0].Labels) == p.Alerts[0].Fingerprint {
		t.Errorf("SpikeMem: %+v, %v", p.Alerts, err)
	}
	am := fakealertmanager.New().Webhook(1, time.Date(2026, 10, 19, 7, 27, 0, 0, time.UTC))
	var head struct {
		Version string          `json:"version"`
		OrgID   json.RawMessage `json:"orgId"`
	}
	if err := json.Unmarshal(am, &head); err != nil || head.Version != "4" || head.OrgID != nil {
		t.Fatalf("the fake Alertmanager's body: version %q, orgId %s, %v", head.Version, head.OrgID, err)
	}
	if p, err := ParsePayload(am); err != nil || p.Grafana || len(p.Alerts) != 1 {
		t.Errorf("the fake Alertmanager's body: grafana %v, %d alerts, %v", p.Grafana, len(p.Alerts), err)
	}
	for _, tt := range []struct {
		name, body string
		grafana    bool
	}{
		{"orgId null", `{"version":"1","groupKey":"g","status":"firing","orgId":null,"alerts":[]}`, false},
		{"orgId a number", `{"version":"1","groupKey":"g","status":"firing","orgId":1,"alerts":[]}`, true},
		{"orgId a string", `{"groupKey":"g","status":"firing","orgId":"1","alerts":[]}`, true},
		{"orgId an object", `{"groupKey":"g","status":"firing","orgId":{"id":1},"alerts":[]}`, true},
	} {
		p, err := ParsePayload([]byte(tt.body))
		if err != nil || p.Grafana != tt.grafana {
			t.Errorf("%s: grafana %v, %v; want %v", tt.name, p.Grafana, err, tt.grafana)
		}
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
