// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package links

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/links/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/templates"
)

// The kinds of a link: a Link rule's, or the runbook_url and dashboard_url annotations and the generatorURL of the
// Alerts.
const (
	KindRule      = "rule"
	KindRunbook   = "runbook"
	KindDashboard = "dashboard"
	KindSource    = "source"
)

// The names of the links that are not a Link rule's, as the API shows them; messages translate them.
const (
	NameRunbook   = "Runbook"
	NameDashboard = "Dashboard"
	NameSource    = "Source"
)

// templateKind is the label template of the metrics of Link rules (C-12.FR-13).
const templateKind = "link_rule"

// ReadAlerts is the most Alerts of an Alert Group its links are computed from, as many as its messages read.
const ReadAlerts = 1000

// ErrNotFound is an Alert Group that does not exist in the Organization.
var ErrNotFound = errors.New("no such alert group")

// Link is one link of an Alert Group: its kind, its name and its http(s) URL.
type Link struct {
	Kind string
	Name string
	URL  string
}

// Input is an Alert Group as its links are computed from: its public_id and its Route's, which the metric and the log
// name — both empty for a sample, whose failures are neither counted nor logged — and its template data with the
// values as received, the Alerts firing first and newest first.
type Input struct {
	Group string
	Route string
	Data  templates.Data
}

// Data is what a Link rule's URL template sees: the data of a message template, Labels — the common labels for the
// scope alert_group, the labels of the first Alert with the value for label_value — and Value, the label's value.
type Data struct {
	templates.Data
	Labels templates.KV
	Value  string
}

// Evaluate computes the links of an Alert Group, reading the Link rules and the Lookup tables through db, or the
// main pool for a nil db (C-12.FR-9): each rule whose Matchers all match the common labels yields one link, or one per
// distinct value of its label among the Alerts, in the order the rules were created; then the runbook_url and the
// dashboard_url of the first Alert that has one, and the generatorURL of the first firing Alert that has one. Every
// URL is kept only as an absolute http(s) URL, and an empty one is no link. A rule whose template fails is left out:
// for an Alert Group it is counted in muster_template_errors_total and logged once per rule and Alert Group.
func (s *Service) Evaluate(ctx context.Context, db DBTX, in Input) ([]Link, error) {
	q := s.q(db)
	rules, err := s.allRules(ctx, q)
	if err != nil {
		return nil, err
	}
	env := s.newLookups(ctx, q).env()
	common := map[string]string(in.Data.CommonLabels)
	out := []Link{}
	for _, r := range rules {
		if !matchers.All(r.compiled, common) {
			continue
		}
		links, err := s.ruleLinks(r, env, in.Data)
		if err != nil {
			s.failed(ctx, r, in, err)
			continue
		}
		out = append(out, links...)
	}
	return append(out, alertLinks(in.Data)...), nil
}

// ruleLinks are the links of one rule, or the error of its template.
func (s *Service) ruleLinks(r Rule, env templates.Env, d templates.Data) ([]Link, error) {
	t, err := s.parse(r.URLTemplate)
	if err != nil {
		return nil, err
	}
	if r.Scope.Type != ScopeLabelValue {
		u, err := s.render(t, env, Data{Data: d, Labels: d.CommonLabels})
		if err != nil || u == "" {
			return nil, err
		}
		return []Link{{Kind: KindRule, Name: r.Name, URL: u}}, nil
	}
	var out []Link
	seen := map[string]bool{}
	for _, a := range d.Alerts {
		v := a.Labels[r.Scope.Label]
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		u, err := s.render(t, env, Data{Data: d, Labels: a.Labels, Value: v})
		if err != nil {
			return nil, err
		}
		if u != "" {
			out = append(out, Link{Kind: KindRule, Name: r.Name + ": " + v, URL: u})
		}
	}
	return out, nil
}

// render runs a URL template and keeps its output only as an http(s) URL.
func (s *Service) render(t *templates.Template, env templates.Env, d Data) (string, error) {
	start := s.real.Now()
	out, err := t.ExecuteIn(env, d)
	metrics.TemplateRenderDuration.With(templateKind).Update(s.real.Now().Sub(start).Seconds())
	if err != nil {
		return "", err
	}
	return templates.SafeURL(out), nil
}

// failed counts a rule that failed for an Alert Group and logs it once per rule and Alert Group.
func (s *Service) failed(ctx context.Context, r Rule, in Input, err error) {
	if in.Group == "" {
		return
	}
	metrics.TemplateErrors.With(in.Route, "", templateKind).Inc()
	key := r.PublicID + "/" + in.Group
	s.mu.Lock()
	logged := s.logged[key]
	if !logged {
		if len(s.logged) >= cacheSize {
			clear(s.logged)
		}
		s.logged[key] = true
	}
	s.mu.Unlock()
	if !logged {
		s.log.Log(ctx, logging.LinkRuleFailed, logging.F("link_rule", r.PublicID), logging.F("route", in.Route),
			logging.F("group", in.Group), logging.F("error", err.Error()))
	}
}

// alertLinks are the links of the Alerts' annotations and generator URLs, each only an http(s) URL.
func alertLinks(d templates.Data) []Link {
	var out []Link
	for _, a := range []struct{ annotation, kind, name string }{
		{"runbook_url", KindRunbook, NameRunbook}, {"dashboard_url", KindDashboard, NameDashboard},
	} {
		for _, alert := range d.Alerts {
			if u := templates.SafeURL(alert.Annotations[a.annotation]); u != "" {
				out = append(out, Link{Kind: a.kind, Name: a.name, URL: u})
				break
			}
		}
	}
	for _, alert := range d.Alerts {
		if u := templates.SafeURL(alert.GeneratorURL); alert.Status == templates.StatusFiring && u != "" {
			out = append(out, Link{Kind: KindSource, Name: NameSource, URL: u})
			break
		}
	}
	return out
}

// RenderURL renders a URL template for the preview of a Link rule against the sample in, with the scope alert_group,
// reading the Lookup tables through db or the main pool: the link the rule yields — empty when the output is not an
// absolute http(s) URL, as a message leaves it out — or the *templates.Error of the template.
func (s *Service) RenderURL(ctx context.Context, db DBTX, source string, in Input) (string, error) {
	t, err := s.parse(source)
	if err != nil {
		return "", err
	}
	start := s.real.Now()
	out, err := t.ExecuteIn(s.newLookups(ctx, s.q(db)).env(), Data{Data: in.Data, Labels: in.Data.CommonLabels})
	metrics.TemplateRenderDuration.With(templateKind).Update(s.real.Now().Sub(start).Seconds())
	if err != nil {
		return "", err
	}
	return templates.SafeURL(out), nil
}

// ForGroup computes the links of the Alert Group groupID on read, through the main pool (getAlertGroup).
func (s *Service) ForGroup(ctx context.Context, groupID int64) ([]Link, error) {
	g, err := s.store.GetLinkGroup(ctx, dbgen.GetLinkGroupParams{OrgID: s.orgID, ID: groupID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read alert group %d for its links: %w", groupID, err)
	}
	rows, err := s.store.ListLinkAlerts(ctx, dbgen.ListLinkAlertsParams{OrgID: s.orgID, AlertGroupID: groupID,
		Lim: ReadAlerts})
	if err != nil {
		return nil, fmt.Errorf("read the alerts of alert group #%d for its links: %w", g.Number, err)
	}
	loc := location(g.TimeZone)
	d := templates.Data{Status: templates.StatusResolved, GroupLabels: kv(g.GroupKeyValues),
		CommonLabels: kv(g.CommonLabels), CommonAnnotations: kv(g.CommonAnnotations), ExternalURL: s.publicURL,
		Alerts: make(templates.Alerts, 0, len(rows)),
		AlertGroup: templates.AlertGroup{Number: g.Number, Title: g.Title, Summary: g.Summary.String, Status: g.Status,
			Route: g.RouteName, SeverityLevel: g.SeverityLevel, Urgent: g.Urgent, StartedAt: g.CreatedAt.In(loc),
			URL: s.publicURL + "/alert-groups/" + g.PublicID, ReopenCount: g.ReopenCount, Owner: g.OwnerName}}
	for _, r := range rows {
		a := templates.Alert{Status: templates.StatusResolved, Labels: kv(r.Labels), Annotations: kv(r.Annotations),
			StartsAt: r.StartsAt.In(loc), GeneratorURL: r.GeneratorUrl.String, Fingerprint: r.Fingerprint}
		if r.State == templates.StatusFiring {
			a.Status, d.Status = templates.StatusFiring, templates.StatusFiring
		}
		if r.EndedAt.Valid {
			a.EndsAt = r.EndedAt.Time.In(loc)
		}
		d.Alerts = append(d.Alerts, a)
	}
	return s.Evaluate(ctx, nil, Input{Group: g.PublicID, Route: g.RoutePublicID, Data: d})
}

func kv(b []byte) templates.KV {
	m := templates.KV{}
	_ = json.Unmarshal(b, &m) // the columns are JSON objects of strings (schema.md)
	return m
}

// location is the time zone name, UTC when it is empty or unknown.
func location(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil && name != "" {
		return loc
	}
	return time.UTC
}

// Sample is the template data of a Stored Snapshot or of the example as an Alert Group: #1, titled by its alertname,
// firing when one of its Alerts is, with the labels every Alert shares as its common labels when the webhook has none.
func (s *Service) Sample(d templates.Data) Input {
	if len(d.CommonLabels) == 0 && len(d.Alerts) > 0 {
		d.CommonLabels = commonOf(d.Alerts)
	}
	d.ExternalURL = s.publicURL
	status := templates.StatusResolved
	for _, a := range d.Alerts {
		if a.Status == templates.StatusFiring {
			status = templates.StatusFiring
		}
	}
	d.Status = status
	d.AlertGroup = templates.AlertGroup{Number: 1, Title: cmp.Or(d.GroupLabels["alertname"],
		d.CommonLabels["alertname"]), Summary: d.CommonAnnotations["summary"], Status: status,
		SeverityLevel: "warning", StartedAt: s.business.Now().UTC(), URL: s.publicURL + "/alert-groups"}
	return Input{Data: d}
}

// commonOf are the labels every Alert has with the same value.
func commonOf(alerts templates.Alerts) templates.KV {
	out := maps.Clone(alerts[0].Labels)
	if out == nil {
		out = templates.KV{}
	}
	for _, a := range alerts[1:] {
		for k, v := range out {
			if a.Labels[k] != v {
				delete(out, k)
			}
		}
	}
	return out
}

// example is the built-in example of a dry run without Stored Snapshots: two firing Alerts of one rule in a
// production cluster, ten minutes old.
func (s *Service) example() Input {
	now := s.business.Now().UTC()
	alert := func(pod string) templates.Alert {
		return templates.Alert{Status: templates.StatusFiring,
			Labels: templates.KV{"alertname": "HighErrorRate", "cluster": "prod", "namespace": "shop", "pod": pod,
				"severity": "critical"},
			Annotations: templates.KV{"summary": "Error rate above 5%",
				"runbook_url": "https://runbooks.example.org/high-error-rate"},
			StartsAt: now.Add(-10 * time.Minute), GeneratorURL: "https://prometheus.example.org/graph?g0.expr=up",
			Fingerprint: "example-" + pod}
	}
	alerts := templates.Alerts{alert("checkout-1"), alert("checkout-2")}
	return s.Sample(templates.Data{Alerts: alerts, GroupLabels: templates.KV{"alertname": "HighErrorRate"},
		CommonAnnotations: alerts[0].Annotations})
}
