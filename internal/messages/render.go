// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/links"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages/dbgen"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/templates"
)

// Config is what a Renderer needs: the Organization, the main pool that previews and dry runs read, MUSTER_PUBLIC_URL
// for the links to Muster, the business clock of `now` and of the template error state, the real clock of the
// sandbox's execution limit and of the render durations, the Keyring that signs the buttons (nil leaves them
// unsigned), the logger, MUSTER_RUNBOOK_BASE_URL, the links of Alert Groups and the usernames of the users a footer
// names (nil for none).
type Config struct {
	OrgID       int64
	DB          DBTX
	PublicURL   string
	Business    clock.Clock
	Real        clock.Clock
	Keys        buttons.Keys
	Log         *logging.Logger
	RunbookBase string
	Links       Links
	Names       Names
}

// Links are the links of an Alert Group and the preview of a Link rule's URL template (C-12.FR-9), declared by their
// consumer; *links.Service implements them.
type Links interface {
	Evaluate(ctx context.Context, db links.DBTX, in links.Input) ([]links.Link, error)
	RenderURL(ctx context.Context, db links.DBTX, source string, in links.Input) (string, error)
}

// Names are the messenger usernames, by identity space, of the user the footer of an Alert Group names
// (C-12.FR-12), declared by their consumer; *mentions.Service implements them.
type Names interface {
	FooterNames(ctx context.Context, db mentions.DBTX, groupID int64) (map[string]string, error)
}

// Renderer renders the messages of an Organization (C-12): Root messages, with a Route's templates or the default
// content and the Fallback template when a template fails; Thread replies; Storm summaries; previews and dry runs.
type Renderer struct {
	orgID     int64
	db        DBTX
	publicURL string
	business  clock.Clock
	real      clock.Clock
	keys      buttons.Keys
	log       *logging.Logger
	internal  *internalalerts.Raiser
	sandbox   *templates.Sandbox
	links     Links
	names     Names

	mu    sync.Mutex
	cache map[string]*templates.Template
	// queries are the queries of the package over a pool or a transaction; tests replace them.
	queries func(db DBTX) queries
}

// queries are the queries of the package and the Internal alerts it raises and resolves.
type queries interface {
	GetRenderGroup(ctx context.Context, arg dbgen.GetRenderGroupParams) (dbgen.GetRenderGroupRow, error)
	GetGroupIDByPublicID(ctx context.Context, arg dbgen.GetGroupIDByPublicIDParams) (int64, error)
	ListRenderAlerts(ctx context.Context, arg dbgen.ListRenderAlertsParams) ([]dbgen.ListRenderAlertsRow, error)
	ListAlertsByFingerprint(ctx context.Context, arg dbgen.ListAlertsByFingerprintParams) (
		[]dbgen.ListAlertsByFingerprintRow, error)
	GetReplyEntry(ctx context.Context, arg dbgen.GetReplyEntryParams) (dbgen.GetReplyEntryRow, error)
	GetPreviewRoute(ctx context.Context, arg dbgen.GetPreviewRouteParams) (dbgen.GetPreviewRouteRow, error)
	GetRenderSettings(ctx context.Context, orgID int64) (dbgen.GetRenderSettingsRow, error)
	ListRouteSnapshots(ctx context.Context, arg dbgen.ListRouteSnapshotsParams) ([]dbgen.ListRouteSnapshotsRow, error)
	GetSnapshotBody(ctx context.Context, arg dbgen.GetSnapshotBodyParams) ([]byte, error)
	GetSnapshotRetention(ctx context.Context, orgID int64) (int64, error)
	SetTemplateError(ctx context.Context, arg dbgen.SetTemplateErrorParams) ([]string, error)
	ClearTemplateError(ctx context.Context, arg dbgen.ClearTemplateErrorParams) ([]string, error)
	internalalerts.Store
}

// pgQueries are the queries over a pool or a transaction.
type pgQueries struct {
	*dbgen.Queries
	internalalerts.Store
}

// q are the queries over db.
func (r *Renderer) q(db DBTX) queries {
	return r.queries(db)
}

// New is the Renderer of cfg.
func New(cfg Config) *Renderer {
	return &Renderer{orgID: cfg.OrgID, db: cfg.DB, publicURL: strings.TrimSuffix(cfg.PublicURL, "/"), business: cfg.Business,
		real: cfg.Real, keys: cfg.Keys, log: cfg.Log, internal: internalalerts.NewRaiser(cfg.OrgID, cfg.RunbookBase),
		sandbox: templates.New(cfg.Business, cfg.Real), cache: map[string]*templates.Template{},
		links: cfg.Links, names: cfg.Names,
		queries: func(db DBTX) queries {
			return pgQueries{Queries: dbgen.New(db), Store: internalalerts.NewStore(db)}
		}}
}

// DBTX is the pool, connection or transaction the Renderer reads and writes through.
type DBTX = dbgen.DBTX

// The samples and Routes a preview names that do not exist in the Organization, or a Stored Snapshot past
// retention.stored_snapshots.
var (
	ErrNotFound         = errors.New("no such alert group")
	ErrRouteNotFound    = errors.New("no such route")
	ErrSnapshotNotFound = errors.New("no such stored snapshot")
)

// ReadAlerts is the most Alerts of an Alert Group a message reads; the counts of the Alerts section cover them.
const ReadAlerts = 1000

// Source is an Alert Group as its messages show it, read through the transaction of the change.
type Source struct {
	ID                int64
	PublicID          string
	Number            int64
	Title             string
	Summary           string
	Status            string
	SeverityLevel     string
	Urgent            bool
	KeyLabels         []string
	KeyValues         map[string]string
	CommonLabels      map[string]string
	CommonAnnotations map[string]string
	FiringCount       int64
	ReopenCount       int64
	ResolvedByKind    string
	ResolveReason     string
	SnoozeUntil       *time.Time
	SnoozeNoEnd       bool
	Unclaimed         bool
	StartedAt         time.Time
	Route             RouteRef
	TimeZone          string
	Owner             string
	SnoozedBy         string
	ResolvedBy        string
	Alerts            []SourceAlert
	TotalAlerts       int64
	// Links are its links after "Open in Muster"; FooterNames the messenger usernames, by identity space, of the
	// user its footer names.
	Links       []links.Link
	FooterNames map[string]string
}

// RouteRef is the Route of an Alert Group as its messages use it: its templates — nil for the built-in one — and the
// template its error state is about, empty when there is none.
type RouteRef struct {
	ID              int64
	PublicID        string
	Name            string
	Language        string
	SnoozeDurations []int64
	RootMessage     *string
	Line            *string
	ErrorTemplate   string
}

// SourceAlert is one Alert of an Alert Group.
type SourceAlert struct {
	Fingerprint  string
	Labels       map[string]string
	Annotations  map[string]string
	StartsAt     time.Time
	EndsAt       *time.Time
	GeneratorURL string
	Firing       bool
}

// Load reads the Alert Group groupID and its Alerts through db.
func (r *Renderer) Load(ctx context.Context, db DBTX, groupID int64) (*Source, error) {
	src, err := r.loadGroup(ctx, db, groupID)
	if err != nil {
		return nil, err
	}
	rows, err := r.q(db).ListRenderAlerts(ctx, dbgen.ListRenderAlertsParams{OrgID: r.orgID,
		AlertGroupID: groupID, Lim: ReadAlerts})
	if err != nil {
		return nil, fmt.Errorf("read the alerts of alert group #%d: %w", src.Number, err)
	}
	for _, a := range rows {
		src.TotalAlerts = a.Total
		sa := SourceAlert{Fingerprint: a.Fingerprint, Labels: jsonMap(a.Labels), Annotations: jsonMap(a.Annotations),
			StartsAt: a.StartsAt.UTC(), GeneratorURL: a.GeneratorUrl.String, Firing: a.State == "firing"}
		if a.EndedAt.Valid {
			t := a.EndedAt.Time.UTC()
			sa.EndsAt = &t
		}
		src.Alerts = append(src.Alerts, sa)
	}
	if err := r.withLinks(ctx, db, src); err != nil {
		return nil, err
	}
	if r.names != nil {
		if src.FooterNames, err = r.names.FooterNames(ctx, db, groupID); err != nil {
			return nil, err
		}
	}
	return src, nil
}

// withLinks computes the links of src through db (C-12.FR-9).
func (r *Renderer) withLinks(ctx context.Context, db DBTX, src *Source) error {
	if r.links == nil {
		return nil
	}
	var err error
	src.Links, err = r.links.Evaluate(ctx, db, r.linkInput(src))
	return err
}

// linkInput is src as its links are computed from: the values as received.
func (r *Renderer) linkInput(src *Source) links.Input {
	return links.Input{Group: src.PublicID, Route: src.Route.PublicID, Data: r.data(src)}
}

// loadGroup reads the Alert Group groupID without its Alerts.
func (r *Renderer) loadGroup(ctx context.Context, db DBTX, groupID int64) (*Source, error) {
	g, err := r.q(db).GetRenderGroup(ctx, dbgen.GetRenderGroupParams{OrgID: r.orgID, ID: groupID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read alert group %d for its message: %w", groupID, err)
	}
	src := &Source{ID: g.ID, PublicID: g.PublicID, Number: g.Number, Title: g.Title, Summary: g.Summary.String,
		Status: g.Status, SeverityLevel: g.SeverityLevel, Urgent: g.Urgent, KeyLabels: g.GroupKeyLabels,
		KeyValues: jsonMap(g.GroupKeyValues), CommonLabels: jsonMap(g.CommonLabels),
		CommonAnnotations: jsonMap(g.CommonAnnotations), FiringCount: g.FiringAlertCount, ReopenCount: g.ReopenCount,
		ResolvedByKind: g.ResolvedByKind.String, ResolveReason: g.ResolveReason.String,
		SnoozeUntil: timeOf(g.SnoozeUntil), SnoozeNoEnd: g.SnoozeNoEnd, Unclaimed: g.Unclaimed,
		StartedAt: g.CreatedAt.UTC(), TimeZone: g.TimeZone, Owner: g.OwnerName, SnoozedBy: g.SnoozedByName,
		ResolvedBy: g.ResolvedByName,
		Route: RouteRef{ID: g.RouteID, PublicID: g.RoutePublicID, Name: g.RouteName, Language: g.Language,
			SnoozeDurations: g.SnoozeDurationsSeconds, RootMessage: textOf(g.TemplateRootMessage),
			Line: textOf(g.TemplateLine), ErrorTemplate: g.TemplateErrorTemplate.String}}
	return src, nil
}

func jsonMap(b []byte) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal(b, &m) // the columns are JSON objects of strings (schema.md)
	return m
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func textOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// Failure is a Route template that failed while rendering a message: its kind and the error with its position.
type Failure struct {
	Template string
	Error    *templates.Error
}

// Detail is the failure as the Timeline and the Route show it: "root_message template failed: line 4, column 7: …".
func (f Failure) Detail() string {
	return f.Template + " template failed: " + f.Error.Error()
}

// Rendered is a Root message, the key that signed its buttons, the Route template that failed, if any — the message
// is then the Fallback template — and the Route templates that rendered.
type Rendered struct {
	Message  Message
	KeyID    string
	Failure  *Failure
	Rendered []string
}

// Root renders the Root message of src for a markup (C-12.FR-1, FR-2, FR-6): Muster's colour, heading, links,
// notices, footer and buttons around the body of the Route's root_message template or the default content with the
// Route's line template; when a Route template fails, the Fallback template.
func (r *Renderer) Root(src *Source, markup Markup) Rendered {
	return r.root(src, markup, true)
}

// root renders the Root message of src, with its buttons signed when sign is set.
func (r *Renderer) root(src *Source, markup Markup, sign bool) Rendered {
	start := r.real.Now()
	defer func() { observe(TemplateRootMessage, r.real.Now().Sub(start)) }()
	lang := language(src.Route.Language)
	m, keyID := r.frame(src, lang, sign)
	out := Rendered{KeyID: keyID}
	if t := src.Route.RootMessage; t != nil {
		body, err := r.execute(TemplateRootMessage, *t, SafeData(r.data(src), markup))
		if err != nil {
			out.Failure = &Failure{Template: TemplateRootMessage, Error: sandboxError(err)}
			out.Message = r.fallback(m, src, lang)
			return out
		}
		m.Body = &Body{Text: Neutralize(body), Markup: markup}
		out.Rendered = append(out.Rendered, TemplateRootMessage)
		out.Message = m
		return out
	}
	r.defaultBody(&m, src, lang)
	list, fail := r.alertList(src, lang, markup)
	if fail != nil {
		out.Failure = fail
		out.Message = r.fallback(m, src, lang)
		return out
	}
	m.Alerts = list
	if src.Route.Line != nil && len(list.Lines) > 0 {
		out.Rendered = append(out.Rendered, TemplateLine)
	}
	out.Message = m
	return out
}

// observe records the time a template of a kind took (C-12.FR-13).
func observe(kind string, d time.Duration) {
	metrics.TemplateRenderDuration.With(kind).Update(d.Seconds())
}

// compile parses a template once and keeps it; the cache is emptied when it grows past its size.
func (r *Renderer) compile(kind, source string) (*templates.Template, error) {
	key := kind + "\x00" + source
	r.mu.Lock()
	t, ok := r.cache[key]
	r.mu.Unlock()
	if ok {
		return t, nil
	}
	t, err := r.sandbox.Parse(kind, source)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if len(r.cache) >= cacheSize {
		clear(r.cache)
	}
	r.cache[key] = t
	r.mu.Unlock()
	return t, nil
}

// cacheSize bounds the parsed templates the Renderer keeps.
const cacheSize = 512

// execute parses and runs a template on data.
func (r *Renderer) execute(kind, source string, data any) (string, error) {
	t, err := r.compile(kind, source)
	if err != nil {
		return "", err
	}
	return t.Execute(data)
}

// GroupURL is the page of an Alert Group in Muster, under MUSTER_PUBLIC_URL.
func (r *Renderer) GroupURL(publicID string) string {
	return r.publicURL + "/alert-groups/" + publicID
}

// routeURL is the Alert Group list of a Route in Muster.
func (r *Renderer) routeURL(publicID string) string {
	return r.publicURL + "/alert-groups?route=" + publicID
}

// data is the template data of src, as received: SafeData makes it safe for a markup. Times are in the
// Organization's time zone.
func (r *Renderer) data(src *Source) templates.Data {
	loc := location(src.TimeZone)
	d := templates.Data{Status: templates.StatusResolved, Alerts: make(templates.Alerts, 0, len(src.Alerts)),
		GroupLabels: templates.KV(clone(src.KeyValues)), CommonLabels: templates.KV(clone(src.CommonLabels)),
		CommonAnnotations: templates.KV(clone(src.CommonAnnotations)), ExternalURL: r.publicURL,
		AlertGroup: r.groupData(src)}
	for _, a := range src.Alerts {
		d.Alerts = append(d.Alerts, alertData(a, loc))
		if a.Firing {
			d.Status = templates.StatusFiring
		}
	}
	return d
}

func alertData(a SourceAlert, loc *time.Location) templates.Alert {
	out := templates.Alert{Status: templates.StatusResolved, Labels: templates.KV(clone(a.Labels)),
		Annotations: templates.KV(clone(a.Annotations)), StartsAt: a.StartsAt.In(loc),
		GeneratorURL: a.GeneratorURL, Fingerprint: a.Fingerprint}
	if a.Firing {
		out.Status = templates.StatusFiring
	}
	if a.EndsAt != nil {
		out.EndsAt = a.EndsAt.In(loc)
	}
	return out
}

func (r *Renderer) groupData(src *Source) templates.AlertGroup {
	return templates.AlertGroup{Number: src.Number, Title: src.Title, Summary: src.Summary, Status: src.Status,
		Route: src.Route.Name, SeverityLevel: src.SeverityLevel, Urgent: src.Urgent,
		StartedAt: src.StartedAt.In(location(src.TimeZone)), URL: r.GroupURL(src.PublicID), ReopenCount: src.ReopenCount,
		Owner: src.Owner}
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Settle records, in the transaction db of the render, what the Route templates of src came to (C-12.FR-6): a
// failure sets the Route's template error when none is set and raises MusterTemplateError; a template that rendered
// again clears the error about it and resolves the Internal alert. It returns what to run once the transaction
// committed: the counter muster_template_errors_total and the log lines.
func (r *Renderer) Settle(ctx context.Context, db DBTX, src *Source, failures []Failure, rendered []string) (
	func(context.Context), error) {
	q := r.q(db)
	now := r.business.Now().UTC()
	var after []func(context.Context)
	route, group := src.Route.PublicID, src.PublicID
	for _, f := range failures {
		names, err := q.SetTemplateError(ctx, dbgen.SetTemplateErrorParams{OrgID: r.orgID, ID: src.Route.ID, Now: now,
			Error: f.Detail(), Template: f.Template})
		if err != nil {
			return nil, fmt.Errorf("record the template error of route %s: %w", route, err)
		}
		if len(names) > 0 {
			if err := r.internal.Raise(ctx, q, now, internalalerts.TemplateError,
				internalalerts.Entity{ID: route, Name: names[0]}, map[string]string{"template": f.Template}); err != nil {
				return nil, err
			}
		}
		tmpl, detail := f.Template, f.Error.Error()
		after = append(after, func(ctx context.Context) {
			metrics.TemplateErrors.With(route, "", tmpl).Inc()
			r.log.Log(ctx, logging.FallbackTemplateUsed, logging.F("route", route), logging.F("group", group),
				logging.F("template", tmpl), logging.F("error", detail))
		})
	}
	for _, t := range rendered {
		// A rendered root_message template also ends an error of the line template, which it puts out of use.
		if t != src.Route.ErrorTemplate && (t != TemplateRootMessage || src.Route.ErrorTemplate != TemplateLine) {
			continue
		}
		t := src.Route.ErrorTemplate
		ids, err := q.ClearTemplateError(ctx, dbgen.ClearTemplateErrorParams{OrgID: r.orgID, ID: src.Route.ID,
			Template: t})
		if err != nil {
			return nil, fmt.Errorf("clear the template error of route %s: %w", route, err)
		}
		if len(ids) == 0 {
			continue
		}
		if err := r.internal.Resolve(ctx, q, now, internalalerts.TemplateError,
			route); err != nil {
			return nil, err
		}
		after = append(after, func(ctx context.Context) {
			r.log.Log(ctx, logging.TemplateRecovered, logging.F("route", route), logging.F("template", t))
		})
	}
	return func(ctx context.Context) {
		for _, f := range after {
			f(ctx)
		}
	}, nil
}

// TemplateSaved clears the template error of the Route routeID and resolves MusterTemplateError, in the transaction
// db that saved a new template of the kind the error is about (C-12.FR-6).
func (r *Renderer) TemplateSaved(ctx context.Context, db DBTX, routeID int64, publicID string, kinds []string) error {
	q := r.q(db)
	for _, kind := range kinds {
		ids, err := q.ClearTemplateError(ctx, dbgen.ClearTemplateErrorParams{OrgID: r.orgID, ID: routeID,
			Template: kind})
		if err != nil {
			return fmt.Errorf("clear the template error of route %s: %w", publicID, err)
		}
		if len(ids) > 0 {
			return r.internal.Resolve(ctx, q, r.business.Now().UTC(),
				internalalerts.TemplateError, publicID)
		}
	}
	return nil
}

// LateNote adds the note of a late Publication to m (C-11.FR-11): started and resolved in m's time zone.
func LateNote(m Message, started, resolved time.Time) Message {
	m.Notices = append(m.Notices, T(m.Language, "notice.late", Args{"started": NoteTime(started, m.TimeZone),
		"resolved": NoteTime(resolved, m.TimeZone)}))
	return m
}

// DeletedNote adds the note of a Root message published again after it was deleted in the messenger (C-11.FR-13).
func DeletedNote(m Message, at time.Time) Message {
	m.Notices = append(m.Notices, T(m.Language, "notice.deleted", Args{"time": NoteTime(at, m.TimeZone)}))
	return m
}

// FinalEdit is the last state of a Root message in a Destination it leaves (C-11.FR-14): what it shows with the
// note "No longer updated here" and the link to Muster, and no buttons, since nothing there is answered any more.
func FinalEdit(m Message, link string) Message {
	m.Notices = append(append([]string{}, m.Notices...), T(m.Language, "notice.final", Args{"link": link}))
	m.Buttons = []Button{}
	return m
}
