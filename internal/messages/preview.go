// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/messages/dbgen"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/templates"
)

// Dry runs and previews (C-12.FR-5): a template is parsed, then rendered against up to template.dry_run_sample of the
// most recent Stored Snapshots whose Alerts the Route took, or a built-in example when there are none. A preview
// renders through the same path as the messages Muster sends, laid out in Mattermost Markdown or Telegram HTML and
// shortened to a length limit when one is given.

// DryRunSample is template.dry_run_sample.
const DryRunSample = 20

// The samples a preview renders against.
const (
	SampleStoredSnapshot = "stored_snapshot"
	SampleAlertGroup     = "alert_group"
	SampleExample        = "example"
)

// The kinds a preview takes until Link rules (S-037) and outgoing webhooks (S-045) bring theirs.
var previewKinds = []string{TemplateRootMessage, TemplateLine, TemplateAckTimeoutNotice}

// ErrUnsupportedKind is a preview of a kind of template this build does not render.
var ErrUnsupportedKind = errors.New("the kind of template has no preview yet")

// CodeInvalidSample is the code of a Stored Snapshot that cannot serve as a sample.
const CodeInvalidSample = "invalid_format"

// PreviewRequest is previewTemplate: the kind and the template, empty for the built-in one; the markup, Markdown by
// default; the Route; the sample — a Stored Snapshot or an Alert Group, by default the Route's recent Stored
// Snapshots or the example; the language, by default the Route's; and a length limit, 0 for none.
type PreviewRequest struct {
	Kind         string
	Template     string
	Format       Markup
	RouteID      string
	SnapshotID   string
	AlertGroupID string
	Language     string
	LengthLimit  int
}

// PreviewResult is what a preview shows: whether the template rendered against every sample, the output against the
// first one in the requested markup, whether it was shortened, the source of the template — the built-in one for an
// empty template — the sample and the errors with their positions.
type PreviewResult struct {
	Valid     bool
	Output    string
	Truncated bool
	Format    Markup
	Source    string
	Sample    string
	Errors    []*templates.Error
}

// settings are what a preview takes from its Route, or from the Default route without one.
type settings struct {
	route RouteRef
	tz    string
}

// Preview renders a template against its sample (previewTemplate), reading the main pool. An unknown Route, Stored
// Snapshot or Alert Group is ErrRouteNotFound, ErrSnapshotNotFound or ErrNotFound; a template that fails is a result
// with Valid false, never an error.
func (r *Renderer) Preview(ctx context.Context, req PreviewRequest) (PreviewResult, error) {
	db := r.db
	if !slices.Contains(previewKinds, req.Kind) {
		return PreviewResult{}, ErrUnsupportedKind
	}
	format := cmp.Or(req.Format, MarkupMarkdown)
	q := r.q(db)
	set, err := r.settings(ctx, q, req.RouteID)
	if err != nil {
		return PreviewResult{}, err
	}
	var group *Source
	if req.AlertGroupID != "" {
		if group, err = r.previewGroup(ctx, db, req.AlertGroupID); err != nil {
			return PreviewResult{}, err
		}
		if req.RouteID == "" {
			set.route = group.Route
		}
	}
	lang := language(cmp.Or(req.Language, set.route.Language))
	res := PreviewResult{Format: format, Source: req.Template}
	builtin := req.Template == ""
	if builtin {
		res.Source = BuiltinSource(req.Kind, lang)
	}
	if _, err := r.compile(req.Kind, res.Source); err != nil {
		res.Errors = []*templates.Error{sandboxError(err)}
		return res, nil
	}
	var samples []*Source
	switch {
	case group != nil:
		samples, res.Sample = []*Source{group}, SampleAlertGroup
	case req.SnapshotID != "":
		s, err := r.snapshotSample(ctx, q, req.SnapshotID, set)
		if errors.Is(err, templates.ErrNotWebhook) {
			res.Errors = []*templates.Error{{Code: CodeInvalidSample,
				Detail: "The Stored Snapshot is not an Alertmanager webhook."}}
			return res, nil
		}
		if err != nil {
			return PreviewResult{}, err
		}
		samples, res.Sample = []*Source{s}, SampleStoredSnapshot
	default:
		if samples, err = r.routeSamples(ctx, q, set); err != nil {
			return PreviewResult{}, err
		}
		res.Sample = SampleStoredSnapshot
		if len(samples) == 0 {
			samples, res.Sample = []*Source{r.example(set)}, SampleExample
		}
	}
	tmpl := &res.Source
	if builtin {
		tmpl = nil
	}
	for i, s := range samples {
		s.Route.Language = lang
		m, err := r.previewMessage(s, req.Kind, tmpl, format)
		if err != nil {
			res.Errors = []*templates.Error{sandboxError(err)}
			res.Output = ""
			return res, nil
		}
		if i == 0 {
			m, res.Truncated = Shorten(m, req.LengthLimit, format)
			res.Output = Layout(m, format)
		}
	}
	res.Valid = true
	return res, nil
}

// previewMessage renders one sample: the whole Root message for root_message, the Alert lines for line and the notice
// for ack_timeout_notice; tmpl nil is the built-in template. The buttons of a preview are not signed.
func (r *Renderer) previewMessage(src *Source, kind string, tmpl *string, format Markup) (Message, error) {
	src.Route.RootMessage, src.Route.Line = nil, nil
	switch kind {
	case TemplateRootMessage:
		src.Route.RootMessage = tmpl
		out := r.root(src, format, false)
		if out.Failure != nil {
			return Message{}, out.Failure.Error
		}
		return out.Message, nil
	case TemplateLine:
		src.Route.Line = tmpl
		listed := *src
		listed.Alerts = src.Alerts[:min(len(src.Alerts), AlertsListed)]
		listed.TotalAlerts = int64(len(listed.Alerts))
		list, fail := r.alertList(&listed, language(src.Route.Language), format)
		if fail != nil {
			return Message{}, fail.Error
		}
		return Message{Language: src.Route.Language, Alerts: list}, nil
	}
	source := cmp.Or(deref(tmpl), BuiltinSource(TemplateAckTimeoutNotice, src.Route.Language))
	start := r.real.Now()
	out, err := r.execute(TemplateAckTimeoutNotice, source, SafeData(r.data(src), format))
	observe(TemplateAckTimeoutNotice, r.real.Now().Sub(start))
	if err != nil {
		return Message{}, err
	}
	return Message{Language: src.Route.Language, Body: &Body{Text: out, Markup: format}}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// CheckTemplate is the dry run of a Route template on save (C-12.FR-5), reading the main pool: it parses source and
// renders it as kind against the most recent Stored Snapshots the Route routeID took — none for a new Route, 0 — or
// the example; the first failure is the *templates.Error returned.
func (r *Renderer) CheckTemplate(ctx context.Context, routeID int64, kind, source, lang string) error {
	if _, err := r.compile(kind, source); err != nil {
		return sandboxError(err)
	}
	q := r.q(r.db)
	set, err := r.settings(ctx, q, "")
	if err != nil {
		return err
	}
	set.route.ID, set.route.Language = routeID, lang
	var samples []*Source
	if routeID > 0 {
		if samples, err = r.routeSamples(ctx, q, set); err != nil {
			return err
		}
	}
	if len(samples) == 0 {
		samples = []*Source{r.example(set)}
	}
	for _, s := range samples {
		if _, err := r.previewMessage(s, kind, &source, MarkupMarkdown); err != nil {
			return sandboxError(err)
		}
	}
	return nil
}

// settings reads the time zone and the Route of a preview: the Route publicID, or the Default route.
func (r *Renderer) settings(ctx context.Context, q queries, publicID string) (settings, error) {
	s, err := q.GetRenderSettings(ctx, r.orgID)
	if err != nil {
		return settings{}, fmt.Errorf("read the settings of a preview: %w", err)
	}
	set := settings{tz: s.TimeZone, route: RouteRef{Name: s.Name, Language: s.Language,
		SnoozeDurations: s.SnoozeDurationsSeconds}}
	if publicID == "" {
		return set, nil
	}
	id, err := publicid.Parse(publicid.Route, publicID)
	if err != nil {
		return settings{}, ErrRouteNotFound
	}
	rt, err := q.GetPreviewRoute(ctx, dbgen.GetPreviewRouteParams{OrgID: r.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return settings{}, ErrRouteNotFound
	}
	if err != nil {
		return settings{}, fmt.Errorf("read the route %s: %w", id, err)
	}
	set.route = RouteRef{ID: rt.ID, PublicID: rt.PublicID, Name: rt.Name, Language: rt.Language,
		SnoozeDurations: rt.SnoozeDurationsSeconds}
	return set, nil
}

// previewGroup reads the Alert Group publicID with its Alerts.
func (r *Renderer) previewGroup(ctx context.Context, db DBTX, publicID string) (*Source, error) {
	id, err := publicid.Parse(publicid.AlertGroup, publicID)
	if err != nil {
		return nil, ErrNotFound
	}
	gid, err := r.q(db).GetGroupIDByPublicID(ctx, dbgen.GetGroupIDByPublicIDParams{OrgID: r.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read the alert group %s: %w", id, err)
	}
	return r.Load(ctx, db, gid)
}

// notBefore is the oldest receipt time of a Stored Snapshot within retention.stored_snapshots.
func (r *Renderer) notBefore(ctx context.Context, q queries) (time.Time, error) {
	days, err := q.GetSnapshotRetention(ctx, r.orgID)
	if err != nil {
		return time.Time{}, fmt.Errorf("read retention.stored_snapshots: %w", err)
	}
	return r.business.Now().UTC().AddDate(0, 0, -int(days)), nil
}

// snapshotSample is the Stored Snapshot publicID as a sample.
func (r *Renderer) snapshotSample(ctx context.Context, q queries, publicID string, set settings) (*Source,
	error) {
	id, err := publicid.Parse(publicid.StoredSnapshot, publicID)
	if err != nil {
		return nil, ErrSnapshotNotFound
	}
	since, err := r.notBefore(ctx, q)
	if err != nil {
		return nil, err
	}
	body, err := q.GetSnapshotBody(ctx, dbgen.GetSnapshotBodyParams{OrgID: r.orgID, PublicID: id, NotBefore: since})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSnapshotNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read the stored snapshot %s: %w", id, err)
	}
	d, err := templates.FromWebhook(body)
	if err != nil {
		return nil, err
	}
	return r.sampleSource(d, set), nil
}

// routeSamples are the most recent Stored Snapshots of the Route of set that are Alertmanager webhooks, as samples.
func (r *Renderer) routeSamples(ctx context.Context, q queries, set settings) ([]*Source, error) {
	if set.route.ID == 0 {
		return nil, nil
	}
	since, err := r.notBefore(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRouteSnapshots(ctx, dbgen.ListRouteSnapshotsParams{OrgID: r.orgID, RouteID: set.route.ID,
		NotBefore: since, Lim: DryRunSample})
	if err != nil {
		return nil, fmt.Errorf("read the stored snapshots of the route: %w", err)
	}
	var out []*Source
	for _, row := range rows {
		if d, err := templates.FromWebhook(row.Body); err == nil && len(d.Alerts) > 0 {
			out = append(out, r.sampleSource(d, set))
		}
	}
	return out, nil
}

// sampleSource is an Alert Group made from the template data of a Stored Snapshot or of the example: #1, titled by
// its group labels, with its Alerts.
func (r *Renderer) sampleSource(d templates.Data, set settings) *Source {
	src := &Source{Number: 1, Status: ColourResolved, SeverityLevel: "warning", KeyLabels: d.GroupLabels.Names(),
		KeyValues: d.GroupLabels, CommonLabels: d.CommonLabels, CommonAnnotations: d.CommonAnnotations,
		Summary: d.CommonAnnotations["summary"], Route: set.route, TimeZone: set.tz,
		TotalAlerts: int64(len(d.Alerts)), StartedAt: r.business.Now().UTC()}
	if len(src.CommonLabels) == 0 && len(d.Alerts) > 0 {
		src.CommonLabels = commonOf(d.Alerts)
	}
	src.Title = cmp.Or(d.GroupLabels["alertname"], src.CommonLabels["alertname"],
		strings.Join(d.GroupLabels.Values(), " "))
	for _, a := range d.Alerts {
		firing := a.Status == templates.StatusFiring
		sa := SourceAlert{Fingerprint: a.Fingerprint, Labels: a.Labels, Annotations: a.Annotations,
			StartsAt: a.StartsAt, GeneratorURL: a.GeneratorURL, Firing: firing}
		if !a.EndsAt.IsZero() {
			ends := a.EndsAt
			sa.EndsAt = &ends
		}
		if firing {
			src.Status = ColourFiring
		}
		if !a.StartsAt.IsZero() && a.StartsAt.Before(src.StartedAt) {
			src.StartedAt = a.StartsAt
		}
		src.Alerts = append(src.Alerts, sa)
	}
	slices.SortStableFunc(src.Alerts, func(a, b SourceAlert) int {
		if a.Firing != b.Firing {
			if a.Firing {
				return -1
			}
			return 1
		}
		return b.StartsAt.Compare(a.StartsAt)
	})
	return src
}

// commonOf are the labels every Alert has with the same value.
func commonOf(alerts templates.Alerts) templates.KV {
	out := maps.Clone(alerts[0].Labels)
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
func (r *Renderer) example(set settings) *Source {
	now := r.business.Now().UTC()
	alert := func(pod string) templates.Alert {
		return templates.Alert{Status: templates.StatusFiring,
			Labels: templates.KV{"alertname": "HighErrorRate", "cluster": "prod", "namespace": "shop", "pod": pod,
				"severity": "critical"},
			Annotations: templates.KV{"summary": "Error rate above 5%",
				"description": "The checkout service answers 5% of requests with errors.",
				"runbook_url": "https://runbooks.example.org/high-error-rate"},
			StartsAt: now.Add(-10 * time.Minute), GeneratorURL: "https://prometheus.example.org/graph",
			Fingerprint: "example-" + pod}
	}
	alerts := templates.Alerts{alert("checkout-1"), alert("checkout-2")}
	return r.sampleSource(templates.Data{Status: templates.StatusFiring, Alerts: alerts,
		GroupLabels:       templates.KV{"alertname": "HighErrorRate"},
		CommonLabels:      commonOf(alerts),
		CommonAnnotations: alerts[0].Annotations}, set)
}
