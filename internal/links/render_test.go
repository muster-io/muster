// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package links

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/links/dbgen"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/templates"
)

// highLatency is the Alert Group of the story's verification: alert a with a runbook and a generatorURL, alert b with
// a pod and a javascript: runbook, both in cluster prod and namespace api.
func highLatency() Input {
	a := templates.Alert{Status: templates.StatusFiring,
		Labels:       templates.KV{"alertname": "HighLatency", "cluster": "prod", "namespace": "api"},
		Annotations:  templates.KV{"runbook_url": "https://wiki.example.org/latency"},
		GeneratorURL: "http://prometheus:9090/graph?g0.expr=up%3D%3D0", Fingerprint: "a"}
	b := templates.Alert{Status: templates.StatusFiring,
		Labels:      templates.KV{"alertname": "HighLatency", "cluster": "prod", "namespace": "api", "pod": "b"},
		Annotations: templates.KV{"runbook_url": "javascript:alert(1)"}, Fingerprint: "b"}
	return Input{Group: "AGAAAAAAAAAAAA", Route: "RTAAAAAAAAAAAA", Data: templates.Data{Status: templates.StatusFiring,
		Alerts:       templates.Alerts{b, a},
		CommonLabels: templates.KV{"alertname": "HighLatency", "cluster": "prod", "namespace": "api"}}}
}

// tx stands for the transaction of a render; the Service reads the fake through it.
type tx struct{ DBTX }

func exploreURL(address, uid, expr string) string {
	panes := `{"muster":{"datasource":"` + uid + `","queries":[{"refId":"A","expr":"` + expr +
		`","datasource":{"type":"prometheus","uid":"` + uid + `"}}],"range":{"from":"now-1h","to":"now"}}}`
	return address + "/explore?schemaVersion=1&orgId=1&panes=" + url.QueryEscape(panes)
}

func names(ls []Link) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Name + " " + l.URL
	}
	return out
}

// TestEvaluate (C-12.FR-9, C-12.AC-1, C-12.AC-6): the matching rules yield their links in order — Explore through the
// Lookup table, Dashboard through lookup — then Runbook, Dashboard and Source; a javascript: runbook yields nothing,
// the failing rule is left out, counted at every failure and logged once.
func TestEvaluate(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, log := newService(t, f)
	if err := EnsureExplore(ctx, f, orgID, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTable(ctx, by, grafana()); err != nil {
		t.Fatal(err)
	}
	dash, err := s.CreateRule(ctx, by, dashboard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRule(ctx, by, RuleInput{Name: "Broken", Scope: Scope{Type: ScopeAlertGroup},
		URLTemplate: `https://x.example.org/{{ if eq .Labels.namespace "api" }}{{ (index .Alerts 5).Labels.pod }}{{ end }}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRule(ctx, by, RuleInput{Name: "Elsewhere", Scope: Scope{Type: ScopeAlertGroup},
		Matchers: []Matcher{{Label: "pod", Op: "=", Value: "b"}}, URLTemplate: "https://never.example.org"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRule(ctx, by, RuleInput{Name: "Not http", Scope: Scope{Type: ScopeAlertGroup},
		URLTemplate: "javascript:alert({{ .Labels.cluster }})"}); err != nil {
		t.Fatal(err)
	}
	in := highLatency()
	before := metrics.TemplateErrors.With(in.Route, "", "link_rule").Get()
	got, err := s.Evaluate(ctx, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Explore " + exploreURL("https://grafana.example.org", "PROM1", "up==0"),
		"Dashboard https://grafana.example.org/d/latency?var-ns=api",
		"Runbook https://wiki.example.org/latency",
		"Source http://prometheus:9090/graph?g0.expr=up%3D%3D0",
	}
	if strings.Join(names(got), "\n") != strings.Join(want, "\n") {
		t.Fatalf("links\n%s\nwant\n%s", strings.Join(names(got), "\n"), strings.Join(want, "\n"))
	}
	if got[0].Kind != KindRule || got[0].Rule != f.rules[0].PublicID || got[1].Kind != KindRule ||
		got[1].Rule != dash.PublicID || got[2].Kind != KindRunbook || got[2].Rule != "" || got[3].Kind != KindSource ||
		got[3].Rule != "" {
		t.Errorf("kinds and rules %+v", got)
	}
	if _, err := s.Evaluate(ctx, nil, in); err != nil {
		t.Fatal(err)
	}
	if d := metrics.TemplateErrors.With(in.Route, "", "link_rule").Get() - before; d != 2 {
		t.Errorf("muster_template_errors_total grew by %d, want 2", d)
	}
	if n := strings.Count(log.String(), `"event":"link_rule_failed"`); n != 1 ||
		!strings.Contains(log.String(), `"group":"AGAAAAAAAAAAAA"`) {
		t.Errorf("link_rule_failed logged %d times: %s", n, log.String())
	}
	sample := in
	sample.Group, sample.Route = "", ""
	if _, err := s.Evaluate(ctx, nil, sample); err != nil ||
		metrics.TemplateErrors.With(in.Route, "", "link_rule").Get()-before != 2 {
		t.Errorf("a sample is not counted: %v", err)
	}
}

// TestEvaluateWithoutTable: without the Lookup table, its row or the expression, Explore and the Dashboard rule yield
// no link and no error; a dashboard_url annotation and the environment label are used.
func TestEvaluateWithoutTable(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, log := newService(t, f)
	if err := EnsureExplore(ctx, f, orgID, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRule(ctx, by, dashboard()); err != nil {
		t.Fatal(err)
	}
	in := highLatency()
	in.Data.Alerts[0].Annotations["dashboard_url"] = "https://dash.example.org/x"
	before := metrics.TemplateErrors.With(in.Route, "", "link_rule").Get()
	got, err := s.Evaluate(ctx, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names(got), "|") != "Runbook https://wiki.example.org/latency|Dashboard https://dash.example.org/x|"+
		"Source http://prometheus:9090/graph?g0.expr=up%3D%3D0" {
		t.Errorf("links %v", names(got))
	}
	if metrics.TemplateErrors.With(in.Route, "", "link_rule").Get() != before || strings.Contains(log.String(),
		"link_rule_failed") {
		t.Error("a missing table is no error")
	}
	// The environment label wins over the cluster; a row without datasource_uid yields no Explore link.
	if _, err := s.CreateTable(ctx, by, TableInput{Name: ExploreTable, Columns: []string{"address", "datasource_uid"},
		Entries: []Entry{
			{Key: "staging", Values: map[string]string{"address": "https://stage.example.org/", "datasource_uid": "S1"}},
			{Key: "prod", Values: map[string]string{"address": "https://grafana.example.org", "datasource_uid": ""}},
		}}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Evaluate(ctx, nil, in)
	if strings.Contains(strings.Join(names(got), "|"), "Explore") {
		t.Errorf("no data source, no Explore: %v", names(got))
	}
	in.Data.CommonLabels["environment"] = "staging"
	got, _ = s.Evaluate(ctx, nil, in)
	if names(got)[0] != "Explore "+exploreURL("https://stage.example.org", "S1", "up==0") {
		t.Errorf("by environment: %v", names(got))
	}
	// Without a firing Alert with an expression there is no Explore link and no Source.
	for i := range in.Data.Alerts {
		in.Data.Alerts[i].GeneratorURL = "http://prometheus:9090/graph?g1.expr=x"
	}
	got, _ = s.Evaluate(ctx, nil, in)
	if strings.Contains(strings.Join(names(got), "|"), "Explore") {
		t.Errorf("no expression, no Explore: %v", names(got))
	}
	in.Data.Alerts[1].Status = templates.StatusResolved
	in.Data.Alerts[0].Status = templates.StatusResolved
	got, _ = s.Evaluate(ctx, nil, in)
	if strings.Contains(strings.Join(names(got), "|"), "Source") {
		t.Errorf("no firing Alert, no Source: %v", names(got))
	}
}

// TestEvaluateLabelValue: a rule of the scope label_value yields one link per distinct value of its label among the
// Alerts, with the labels of the first Alert carrying it.
func TestEvaluateLabelValue(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	logs, err := s.CreateRule(ctx, by, RuleInput{Name: "Logs", Scope: Scope{Type: ScopeLabelValue, Label: "pod"},
		URLTemplate: "https://logs.example.org/{{ .Labels.namespace }}/{{ .Value }}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRule(ctx, by, RuleInput{Name: "Empty", Scope: Scope{Type: ScopeLabelValue, Label: "pod"},
		URLTemplate: "{{ if eq .Value \"p1\" }}https://e.example.org{{ end }}"}); err != nil {
		t.Fatal(err)
	}
	alert := func(pod, ns string) templates.Alert {
		return templates.Alert{Status: templates.StatusFiring, Labels: templates.KV{"pod": pod, "namespace": ns}}
	}
	in := Input{Data: templates.Data{Alerts: templates.Alerts{alert("p1", "a"), alert("p2", "b"), alert("p1", "c"),
		alert("", "d")}}}
	got, err := s.Evaluate(ctx, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names(got), "|") != "Logs: p1 https://logs.example.org/a/p1|Logs: p2 https://logs.example.org/b/p2|"+
		"Empty: p1 https://e.example.org" {
		t.Errorf("links %v", names(got))
	}
	if got[0].Kind != KindRule || got[0].Rule != logs.PublicID || got[1].Rule != logs.PublicID {
		t.Errorf("a label_value link names its rule: %+v", got)
	}
}

// TestForGroup: getAlertGroup's links are computed from the Alert Group and its Alerts as read.
func TestForGroup(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	if _, err := s.ForGroup(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("no alert group: %v", err)
	}
	if _, err := s.CreateRule(ctx, by, RuleInput{Name: "Group", Scope: Scope{Type: ScopeAlertGroup},
		URLTemplate: "{{ .AlertGroup.URL }}?n={{ .AlertGroup.Number }}&s={{ .Status }}&t={{ (index .Alerts 1).StartsAt.Format \"15:04\" }}"}); err != nil {
		t.Fatal(err)
	}
	f.group = &dbgen.GetLinkGroupRow{ID: 1, PublicID: "AGAAAAAAAAAAAA", Number: 7, Title: "HighLatency",
		Status: "firing", GroupKeyValues: []byte(`{"alertname":"HighLatency"}`), CommonLabels: []byte(`{"cluster":"prod"}`),
		CommonAnnotations: []byte(`{}`), CreatedAt: t0, RoutePublicID: "RTAAAAAAAAAAAA", TimeZone: "Europe/Moscow"}
	f.alerts = []dbgen.ListLinkAlertsRow{
		{Fingerprint: "a", Labels: []byte(`{"pod":"a"}`), Annotations: []byte(`{"runbook_url":"https://r.example.org"}`),
			StartsAt: t0, State: "firing", GeneratorUrl: pgtype.Text{String: "https://p.example.org/g", Valid: true}},
		{Fingerprint: "b", Labels: []byte(`{"pod":"b"}`), Annotations: []byte(`{}`), StartsAt: t0.Add(-time.Hour),
			State: "resolved", EndedAt: pgtype.Timestamptz{Time: t0, Valid: true}},
	}
	got, err := s.ForGroup(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names(got), "|") != "Group https://muster.example.org/alert-groups/AGAAAAAAAAAAAA?n=7&s=firing&t=14:00|"+
		"Runbook https://r.example.org|Source https://p.example.org/g" {
		t.Errorf("links %v", names(got))
	}
	f.fail["ListLinkAlerts"] = errors.New("boom")
	if _, err := s.ForGroup(ctx, 1); err == nil {
		t.Error("a failing read of the alerts")
	}
	f.fail["GetLinkGroup"] = errors.New("boom")
	if _, err := s.ForGroup(ctx, 1); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("a failing read of the alert group: %v", err)
	}
	f.fail["ListLinkRules"] = errors.New("boom")
	if _, err := s.Evaluate(ctx, nil, highLatency()); err == nil {
		t.Error("a failing read of the rules")
	}
}

// TestRenderURL (C-12.AC-6): the preview of a URL template reads the Lookup tables and returns the output as written,
// or the template's error.
func TestRenderURL(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	if _, err := s.CreateTable(ctx, by, grafana()); err != nil {
		t.Fatal(err)
	}
	src := dashboard().URLTemplate
	got, err := s.RenderURL(ctx, tx{}, src, highLatency())
	if err != nil || got != "https://grafana.example.org/d/latency?var-ns=api" {
		t.Errorf("render %q %v", got, err)
	}
	for _, src := range []string{" not a url ", "javascript:alert({{ .Labels.cluster }})"} {
		if got, err := s.RenderURL(ctx, tx{}, src, highLatency()); err != nil || got != "" {
			t.Errorf("%s: %q %v, want no link", src, got, err)
		}
	}
	if _, err := s.RenderURL(ctx, tx{}, "{{ env \"HOME\" }}", highLatency()); err == nil {
		t.Error("an unknown function")
	}
	if _, err := s.RenderURL(ctx, tx{}, "{{ index .Alerts 9 }}", highLatency()); err == nil {
		t.Error("a failing template")
	}
}

// TestSample: a webhook without common labels takes the labels its Alerts share, and is titled by its alertname.
func TestSample(t *testing.T) {
	s, _ := newService(t, newFake())
	d, err := templates.FromWebhook(webhook(`{"alertname":"HighLatency","cluster":"prod"}`))
	if err != nil {
		t.Fatal(err)
	}
	in := s.Sample(d)
	if in.Data.CommonLabels["cluster"] != "prod" || in.Data.AlertGroup.Title != "HighLatency" ||
		in.Data.Status != templates.StatusFiring || in.Data.ExternalURL != "https://muster.example.org" {
		t.Errorf("sample %+v", in.Data)
	}
	if location("Nowhere/City") != time.UTC || location("") != time.UTC {
		t.Error("unknown time zones are UTC")
	}
}
