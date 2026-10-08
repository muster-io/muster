// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/internalalerts"
)

// The sources of a Stored Snapshot: a webhook received on the ingest listener, or a synthetic one that raises or
// resolves an Internal alert or marks the deletion of an Integration.
const (
	SourceWebhook  = "webhook"
	SourceInternal = "internal"
)

// The reasons an Alert resolves (NullableResolveReason).
const (
	ResolveResolved           = "resolved"
	ResolveGone               = "gone"
	ResolveStale              = "stale"
	ResolveIntegrationDeleted = "integration_deleted"
)

// ResolveReasons are every reason, as muster_alerts_resolved_total declares them.
var ResolveReasons = []string{ResolveResolved, ResolveGone, ResolveStale, ResolveIntegrationDeleted}

// alert is an alerts row as processing reads and writes it; ID is 0 for a fingerprint this Snapshot fires first.
type alert struct {
	ID           int64
	Fingerprint  string
	Labels       map[string]string
	Annotations  map[string]string
	GeneratorURL string
	Conflicts    []string
	Status       string
	StartsAt     time.Time
	EndsAt       *time.Time
	Episode      int64
	FiredAt      time.Time
	LastSeenAt   time.Time
	ResolvedAt   *time.Time
	Reason       string
	ReasonText   string
	changed      bool
}

// snapshotIn is a parsed Snapshot with what processing needs of its Integration.
type snapshotIn struct {
	StoredSnapshotID int64
	ReceivedAt       time.Time
	Payload          Payload
	StaticLabels     map[string]string
	DuplicateWindow  time.Duration
	// ClockMs is the Integration's liveness clock at receipt (S-023).
	ClockMs int64
	// Internal is a synthetic Snapshot of an Internal alert (C-06.FR-14); Integration is the Integration it was
	// received for, with its current name.
	Internal    bool
	Integration internalalerts.Entity
	// Deleted is a Snapshot of a deleted Integration, which raises and resolves no Internal alert about it: its
	// deletion resolved them. AlertsSince bounds the startsAt of a raise that fires an Internal alert anew.
	Deleted     bool
	AlertsSince time.Time
	// Replayed is a Snapshot that a replay set back to pending.
	Replayed bool
}

// Stats count what one Snapshot did, for its log line and the metrics; Deleted are the Alerts the deletion of their
// Integration resolved.
type Stats struct {
	Alerts, Fired, Resolved, Gone, Continued, Dropped, Deleted int
	Truncated                                                  int64
}

// engine applies one Snapshot to the state of its groupKey in memory (ADR-0002, C-06): the rows it touches are read
// first and written back afterwards, so that the rules stay free of the database.
type engine struct {
	in    snapshotIn
	group *group
	route *route
	// alerts are the rows the Snapshot touches by fingerprint, byID those that exist by id.
	alerts map[string]*alert
	byID   map[int64]*alert
	// presences are the active presences of the groupKey by Alert id.
	presences map[int64]*presence
	// late and early place a Snapshot received before the current window started (timing).
	late, early bool
	// wasTruncated is the truncation of the groupKey before the Snapshot, so that a change raises or resolves
	// MusterSnapshotTruncated.
	wasTruncated bool

	// listedSet and listedAlerts are the firing Alerts the Snapshot lists, whose presences become listed;
	// listedResolved the fingerprints it lists as resolved.
	listedSet      map[string]bool
	listedAlerts   []*alert
	listedResolved map[string]bool
	// missed and gone are the Alert ids whose presence here became missed or Gone; ended the Alerts that resolved,
	// whose active presences end everywhere.
	missed, gone []int64
	ended        []*alert
	changes      []pendingChange
	stats        Stats
}

type pendingChange struct {
	kind       ChangeKind
	alert      *alert
	reason     string
	reasonText string
}

func newEngine(in snapshotIn, g *group, r *route, rows []*alert, presences []*presence) *engine {
	e := &engine{in: in, group: g, route: r, alerts: map[string]*alert{}, byID: map[int64]*alert{},
		presences: map[int64]*presence{}, listedSet: map[string]bool{}, listedResolved: map[string]bool{}}
	for _, a := range rows {
		e.alerts[a.Fingerprint], e.byID[a.ID] = a, a
	}
	for _, p := range presences {
		e.presences[p.AlertID] = p
	}
	e.stats.Alerts, e.stats.Truncated = len(in.Payload.Alerts), in.Payload.TruncatedAlerts
	return e
}

// run applies the Snapshot: its window, truncation and repeat learning, every Alert it lists, the resolve of its
// whole Alertmanager group, and absence. A late Snapshot applies only its explicit resolves. A synthetic Snapshot of
// an Internal alert applies only its Alert: it is never evidence of absence and feeds no repeat learning.
func (e *engine) run() {
	e.wasTruncated = e.group.Truncated
	if e.in.Internal {
		for _, pa := range e.in.Payload.Alerts {
			e.applyInternal(pa)
		}
		return
	}
	e.late, e.early = e.timing()
	switch {
	case e.early:
		e.group.WindowTruncated = e.group.WindowTruncated || e.in.Payload.TruncatedAlerts > 0
	case !e.late:
		opened, previous := e.window()
		e.truncation()
		e.learn(opened, previous)
	}
	for _, pa := range e.in.Payload.Alerts {
		e.apply(pa)
	}
	if e.late {
		return
	}
	if e.in.Payload.Status == StatusResolved {
		e.resolveGroup()
	}
	if !e.early {
		e.absence()
	}
}

// apply is the table of the per-Alert rules (C-06.FR-2, FR-4, FR-11, FR-12).
func (e *engine) apply(pa PayloadAlert) {
	a := e.alerts[pa.Fingerprint]
	if pa.Status == StatusResolved {
		e.listedResolved[pa.Fingerprint] = true
		switch {
		case a == nil:
			e.stats.Dropped++
		case a.Status == StatusResolved, a.StartsAt.After(pa.StartsAt):
			// A re-sent resolve, or one of an earlier firing: nothing changes and nothing is counted.
		default:
			e.resolve(a, ResolveResolved, "", pa.EndsAt)
		}
		return
	}
	if e.late {
		return
	}
	labels, conflicts := withStaticLabels(pa.Labels, e.in.StaticLabels)
	t := e.in.ReceivedAt
	switch {
	case a == nil:
		a = &alert{Fingerprint: pa.Fingerprint, Status: StatusFiring, StartsAt: pa.StartsAt, Episode: 1, FiredAt: t}
		e.alerts[pa.Fingerprint] = a
		e.refresh(a, pa, labels, conflicts, false)
		e.change(ChangeFired, a)
	case a.StartsAt.After(pa.StartsAt):
		// An old copy of an earlier firing changes the Alert in nothing; while the Alert fires, its fingerprint is still
		// listed, so its presence stays listed (a listed Alert is listed) and Alertmanager's own list never makes it
		// Gone.
	case a.Status == StatusResolved:
		if a.StartsAt.Equal(pa.StartsAt) && a.Reason != ResolveGone && a.Reason != ResolveStale {
			// An old copy of a firing that Alertmanager resolved.
			return
		}
		a.Status, a.StartsAt, a.Episode, a.FiredAt = StatusFiring, pa.StartsAt, a.Episode+1, t
		a.ResolvedAt, a.Reason, a.ReasonText = nil, "", ""
		e.refresh(a, pa, labels, conflicts, false)
		e.change(ChangeFired, a)
	case a.StartsAt.Equal(pa.StartsAt):
		e.refresh(a, pa, labels, conflicts, true)
	default:
		a.StartsAt = pa.StartsAt
		e.change(ChangeContinued, a)
		e.refresh(a, pa, labels, conflicts, true)
	}
	if a.Status == StatusFiring {
		e.markListed(a)
	}
}

// applyInternal is the table of the rules for the Alert of a synthetic Snapshot (C-06.FR-14). A resolve resolves
// the Internal alert while it fires and changes nothing otherwise. A raise of an Internal alert that already fires is
// the same firing whatever its startsAt: a raise whose labels or annotations differ, such as a renamed entity's name
// label, updates them in place as an annotation change, and one received before the latest raise that was applied, as
// in a replay, changes nothing. A raise of a resolved one fires it again only with a newer startsAt, so that a
// replayed raise never reopens it.
func (e *engine) applyInternal(pa PayloadAlert) {
	a := e.alerts[pa.Fingerprint]
	if pa.Status == StatusResolved {
		e.listedResolved[pa.Fingerprint] = true
		if a != nil && a.Status == StatusFiring && !a.StartsAt.After(pa.StartsAt) {
			e.resolve(a, ResolveResolved, "", nil)
		}
		return
	}
	t := e.in.ReceivedAt
	switch {
	case a == nil && pa.StartsAt.Before(e.in.AlertsSince):
		// A replayed raise of an Internal alert that retention has removed since.
		return
	case a == nil:
		a = &alert{Fingerprint: pa.Fingerprint, Status: StatusFiring, StartsAt: pa.StartsAt, Episode: 1, FiredAt: t}
		e.alerts[pa.Fingerprint] = a
		e.refresh(a, pa, pa.Labels, []string{}, false)
		e.change(ChangeFired, a)
	case a.Status == StatusFiring:
		if t.Before(a.LastSeenAt) {
			return
		}
		if !maps.Equal(a.Labels, pa.Labels) || !maps.Equal(a.Annotations, pa.Annotations) {
			e.change(ChangeAnnotations, a)
		}
		e.refresh(a, pa, pa.Labels, []string{}, false)
	case !pa.StartsAt.After(a.StartsAt):
		return
	default:
		a.Status, a.StartsAt, a.Episode, a.FiredAt = StatusFiring, pa.StartsAt, a.Episode+1, t
		a.ResolvedAt, a.Reason, a.ReasonText = nil, "", ""
		e.refresh(a, pa, pa.Labels, []string{}, false)
		e.change(ChangeFired, a)
	}
	e.markListed(a)
}

// refresh takes what a listing as firing carries; a changed annotation is an Alert change of its own when report.
func (e *engine) refresh(a *alert, pa PayloadAlert, labels map[string]string, conflicts []string, report bool) {
	if report && !maps.Equal(a.Annotations, pa.Annotations) {
		e.change(ChangeAnnotations, a)
	}
	a.Labels, a.Conflicts, a.Annotations, a.GeneratorURL, a.EndsAt = labels, conflicts, pa.Annotations,
		pa.GeneratorURL, pa.EndsAt
	if t := e.in.ReceivedAt; t.After(a.LastSeenAt) {
		a.LastSeenAt = t
	}
	a.changed = true
}

// resolveGroup resolves every Alert still firing with an active presence in the groupKey of a Snapshot with
// status resolved, listed or not, as if each were listed as resolved (C-06.FR-21); an Alert listed as resolved has
// been decided by its own row.
func (e *engine) resolveGroup() {
	for _, p := range e.sortedPresences() {
		a := e.byID[p.AlertID]
		if a == nil || a.Status != StatusFiring || e.listedResolved[a.Fingerprint] {
			continue
		}
		e.resolve(a, ResolveResolved, "", nil)
	}
}

// resolve resolves a firing Alert at the Snapshot's receipt.
func (e *engine) resolve(a *alert, reason, text string, endsAt *time.Time) {
	t := e.in.ReceivedAt
	a.Status, a.ResolvedAt, a.Reason, a.ReasonText, a.changed = StatusResolved, &t, reason, text, true
	if endsAt != nil {
		a.EndsAt = endsAt
	}
	if t.After(a.LastSeenAt) && reason == ResolveResolved {
		a.LastSeenAt = t
	}
	e.ended = append(e.ended, a)
	if reason == ResolveGone {
		e.stats.Gone++
	} else {
		e.stats.Resolved++
	}
	e.changes = append(e.changes, pendingChange{kind: ChangeResolved, alert: a, reason: reason, reasonText: text})
}

func (e *engine) change(kind ChangeKind, a *alert) {
	switch kind {
	case ChangeFired:
		e.stats.Fired++
	case ChangeContinued:
		e.stats.Continued++
	case ChangeResolved, ChangeAnnotations, ChangeListed:
	}
	e.changes = append(e.changes, pendingChange{kind: kind, alert: a})
}

// withStaticLabels adds the Integration's Static labels to an Alert's labels; a label the Alert already carries keeps
// the Alert's value and is named in the conflicts (C-06.FR-3).
func withStaticLabels(labels, static map[string]string) (map[string]string, []string) {
	out := maps.Clone(labels)
	conflicts := []string{}
	for name, value := range static {
		if _, ok := out[name]; ok {
			conflicts = append(conflicts, name)
			continue
		}
		out[name] = value
	}
	slices.Sort(conflicts)
	return out, conflicts
}

// processedSnapshot is what applying a Snapshot made, for the metrics and the log lines after the commit.
type processedSnapshot struct {
	Stats   Stats
	Changes []AlertChange
	// Routed are the Routes that took the Snapshot's newly firing Alerts.
	Routed Routed
	// Internal are the Internal alerts a synthetic Snapshot raised or resolved.
	Internal []internalChange
	// route is the Snapshot's Alertmanager route with the gaps it learned, written at the end of the transaction;
	// truncated is the new truncation of its groupKey when the Snapshot changed it, nil otherwise.
	route     *route
	truncated *bool
}

// internalChange is an Internal alert that fired or resolved, for its log line.
type internalChange struct {
	Resolved                       bool
	Alertname, Fingerprint, Entity string
}

// applySnapshot reads the state a Snapshot touches, applies it and writes the result, in the Snapshot's transaction.
func (p *Processor) applySnapshot(ctx context.Context, q ProcessQueries, tx dbgen.DBTX, integrationID int64,
	in snapshotIn) (processedSnapshot, error) {
	g, r, err := p.readGroup(ctx, q, integrationID, in)
	if err != nil {
		return processedSnapshot{}, err
	}
	fingerprints := make([]string, len(in.Payload.Alerts))
	for i, a := range in.Payload.Alerts {
		fingerprints[i] = a.Fingerprint
	}
	rows, err := q.ListSnapshotAlerts(ctx, dbgen.ListSnapshotAlertsParams{OrgID: p.orgID, IntegrationID: integrationID,
		Fingerprints: fingerprints, AlertmanagerGroupID: g.ID})
	if err != nil {
		return processedSnapshot{}, fmt.Errorf("read the alerts: %w", err)
	}
	alerts := make([]*alert, len(rows))
	for i, row := range rows {
		if alerts[i], err = alertOf(row); err != nil {
			return processedSnapshot{}, err
		}
	}
	prows, err := q.ListActivePresences(ctx, dbgen.ListActivePresencesParams{OrgID: p.orgID,
		AlertmanagerGroupID: g.ID})
	if err != nil {
		return processedSnapshot{}, fmt.Errorf("read the presences: %w", err)
	}
	presences := make([]*presence, len(prows))
	for i, row := range prows {
		presences[i] = &presence{AlertID: row.AlertID, State: row.State, LastListedWindow: row.LastListedWindow,
			MissedSince: timeOf(row.MissedSince), ActiveElsewhere: row.ActiveElsewhere}
	}
	e := newEngine(in, g, r, alerts, presences)
	e.run()
	if err := p.write(ctx, q, integrationID, e); err != nil {
		return processedSnapshot{}, err
	}
	out := processedSnapshot{Stats: e.stats, Changes: make([]AlertChange, len(e.changes)), route: r}
	if e.group.Truncated != e.wasTruncated && !in.Deleted {
		truncated := e.group.Truncated
		out.truncated = &truncated
	}
	for i, c := range e.changes {
		out.Changes[i] = AlertChange{Kind: c.kind, AlertID: c.alert.ID, Fingerprint: c.alert.Fingerprint,
			Episode: c.alert.Episode, StoredSnapshotID: in.StoredSnapshotID, Reason: c.reason,
			ReasonText: c.reasonText}
		if in.Internal && (c.kind == ChangeFired || c.kind == ChangeResolved) {
			out.Internal = append(out.Internal, internalChangeOf(c.kind == ChangeResolved, c.alert.Fingerprint,
				c.alert.Labels))
		}
	}
	handed := append(slices.Clip(out.Changes), e.listed(in.StoredSnapshotID)...)
	if p.sink != nil && len(handed) > 0 {
		routed, err := p.sink.AlertChanges(ctx, tx, handed)
		if err != nil {
			return processedSnapshot{}, fmt.Errorf("hand over the alert changes: %w", err)
		}
		out.Routed = routed
	}
	return out, nil
}

// listed are the firing Alerts the Snapshot lists that did not fire in it, as ChangeListed, so that routing and
// grouping find one that has no Route or no Alert Group.
func (e *engine) listed(storedSnapshotID int64) []AlertChange {
	fired := map[int64]bool{}
	for _, c := range e.changes {
		if c.kind == ChangeFired {
			fired[c.alert.ID] = true
		}
	}
	var out []AlertChange
	for _, a := range e.listedAlerts {
		if a.Status == StatusFiring && a.ID != 0 && !fired[a.ID] {
			out = append(out, AlertChange{Kind: ChangeListed, AlertID: a.ID, Fingerprint: a.Fingerprint,
				Episode: a.Episode, StoredSnapshotID: storedSnapshotID})
		}
	}
	return out
}

// readGroup finds or creates the Alertmanager route and group of the Snapshot's groupKey; the group's row stays
// locked until the transaction ends, the route's row, which the route's other groupKeys share, is not locked here.
func (p *Processor) readGroup(ctx context.Context, q ProcessQueries, integrationID int64, in snapshotIn) (*group,
	*route, error) {
	t := in.ReceivedAt
	path := AlertmanagerRoutePath(in.Payload.GroupKey)
	pathSum, keySum := sha256.Sum256([]byte(path)), sha256.Sum256([]byte(in.Payload.GroupKey))
	params := dbgen.UpsertAlertmanagerRouteParams{OrgID: p.orgID, IntegrationID: integrationID, RoutePath: path,
		RoutePathSha256: pathSum[:], SeenAt: t}
	routeID, err := q.UpsertAlertmanagerRoute(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another lane created the route at the same time and committed; it is visible now.
		routeID, err = q.UpsertAlertmanagerRoute(ctx, params)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("record the alertmanager route: %w", err)
	}
	r := &route{ID: routeID}
	gr, err := q.UpsertAlertmanagerGroup(ctx, dbgen.UpsertAlertmanagerGroupParams{OrgID: p.orgID,
		IntegrationID: integrationID, AlertmanagerRouteID: routeID, GroupKey: in.Payload.GroupKey,
		GroupKeySha256: keySum[:], SeenAt: t, ClockMs: in.ClockMs})
	if err != nil {
		return nil, nil, fmt.Errorf("record the alertmanager group: %w", err)
	}
	return &group{ID: gr.ID, WindowSeq: gr.WindowSeq, WindowStartedAt: timeOf(gr.WindowStartedAt),
		WindowTruncated: gr.WindowTruncated, Truncated: gr.Truncated, TruncatedSince: timeOf(gr.TruncatedSince),
		LastRepeatAt: timeOf(gr.LastRepeatAt), LastContent: gr.LastContentSha256,
		LastContentAt: timeOf(gr.LastContentAt), LastSnapshotAt: gr.LastSnapshotAt,
		LastClockMs: gr.LastSnapshotClockMs}, r, nil
}

func alertOf(row dbgen.ListSnapshotAlertsRow) (*alert, error) {
	a := &alert{ID: row.ID, Fingerprint: row.Fingerprint, GeneratorURL: row.GeneratorUrl.String,
		Conflicts: row.StaticLabelConflicts, Status: row.Status, StartsAt: row.StartsAt.UTC(),
		EndsAt: timeOf(row.EndsAt), Episode: row.Episode, FiredAt: row.FiredAt.UTC(), LastSeenAt: row.LastSeenAt.UTC(),
		ResolvedAt: timeOf(row.ResolvedAt), Reason: row.ResolveReason.String, ReasonText: row.ResolveReasonText.String}
	if err := json.Unmarshal(row.Labels, &a.Labels); err != nil {
		return nil, processingError(fmt.Errorf("read the labels of alert %d: %w", row.ID, err))
	}
	if err := json.Unmarshal(row.Annotations, &a.Annotations); err != nil {
		return nil, processingError(fmt.Errorf("read the annotations of alert %d: %w", row.ID, err))
	}
	return a, nil
}

// insertRow and updateRow are the JSON rows of InsertAlerts and UpdateAlerts.
type insertRow struct {
	Fingerprint  string            `json:"fingerprint"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	GeneratorURL *string           `json:"generator_url"`
	Conflicts    []string          `json:"static_label_conflicts"`
	StartsAt     time.Time         `json:"starts_at"`
}

type updateRow struct {
	ID           int64             `json:"id"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	GeneratorURL *string           `json:"generator_url"`
	Conflicts    []string          `json:"static_label_conflicts"`
	Status       string            `json:"status"`
	StartsAt     time.Time         `json:"starts_at"`
	EndsAt       *time.Time        `json:"ends_at"`
	Episode      int64             `json:"episode"`
	FiredAt      time.Time         `json:"fired_at"`
	LastSeenAt   time.Time         `json:"last_seen_at"`
	ResolvedAt   *time.Time        `json:"resolved_at"`
	Reason       *string           `json:"resolve_reason"`
	ReasonText   *string           `json:"resolve_reason_text"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// write stores what the engine changed: new and changed Alerts, the presences of the groupKey and of resolved
// Alerts, and the bookkeeping of the group and its route.
func (p *Processor) write(ctx context.Context, q ProcessQueries, integrationID int64, e *engine) error {
	if err := p.writeAlerts(ctx, q, integrationID, e); err != nil {
		return err
	}
	if err := p.writePresences(ctx, q, integrationID, e); err != nil {
		return err
	}
	return p.writeGroup(ctx, q, e)
}

// writeAlerts inserts the fingerprints that fire for the first time, then updates every other changed Alert, and a
// new one that the same Snapshot also resolved.
func (p *Processor) writeAlerts(ctx context.Context, q ProcessQueries, integrationID int64, e *engine) error {
	now := p.clock.Now().UTC()
	var inserts []insertRow
	for _, a := range e.alertsInOrder() {
		if a.ID == 0 {
			inserts = append(inserts, insertRow{Fingerprint: a.Fingerprint, Labels: a.Labels, Annotations: a.Annotations,
				GeneratorURL: optional(a.GeneratorURL), Conflicts: a.Conflicts, StartsAt: a.StartsAt})
			a.changed = a.Status != StatusFiring
		}
	}
	if len(inserts) > 0 {
		body, err := json.Marshal(inserts)
		if err != nil {
			return processingError(fmt.Errorf("encode the new alerts: %w", err))
		}
		ids, err := q.InsertAlerts(ctx, dbgen.InsertAlertsParams{OrgID: p.orgID, IntegrationID: integrationID,
			SeenAt: e.in.ReceivedAt, UpdatedAt: now, Rows: body})
		if err != nil {
			return fmt.Errorf("insert the new alerts: %w", err)
		}
		for _, row := range ids {
			e.alerts[row.Fingerprint].ID = row.ID
		}
	}
	var updates []updateRow
	for _, a := range e.alertsInOrder() {
		if a.changed {
			updates = append(updates, updateRow{ID: a.ID, Labels: a.Labels, Annotations: a.Annotations,
				GeneratorURL: optional(a.GeneratorURL), Conflicts: a.Conflicts, Status: a.Status, StartsAt: a.StartsAt,
				EndsAt: a.EndsAt, Episode: a.Episode, FiredAt: a.FiredAt, LastSeenAt: a.LastSeenAt,
				ResolvedAt: a.ResolvedAt, Reason: optional(a.Reason), ReasonText: optional(a.ReasonText)})
		}
	}
	if len(updates) == 0 {
		return nil
	}
	body, err := json.Marshal(updates)
	if err != nil {
		return processingError(fmt.Errorf("encode the changed alerts: %w", err))
	}
	if err := q.UpdateAlerts(ctx, dbgen.UpdateAlertsParams{OrgID: p.orgID, UpdatedAt: now, Rows: body}); err != nil {
		return fmt.Errorf("update the alerts: %w", err)
	}
	return nil
}

// alertsInOrder are the Alerts the engine holds, in the order of their fingerprints.
func (e *engine) alertsInOrder() []*alert {
	out := make([]*alert, 0, len(e.alerts))
	for _, fp := range slices.Sorted(maps.Keys(e.alerts)) {
		out = append(out, e.alerts[fp])
	}
	return out
}

func (p *Processor) writePresences(ctx context.Context, q ProcessQueries, integrationID int64, e *engine) error {
	g := e.group
	if len(e.listedAlerts) > 0 {
		ids := make([]int64, len(e.listedAlerts))
		for i, a := range e.listedAlerts {
			ids[i] = a.ID
		}
		if err := q.UpsertListedPresences(ctx, dbgen.UpsertListedPresencesParams{AlertmanagerGroupID: g.ID,
			OrgID: p.orgID, IntegrationID: integrationID, SeenAt: e.in.ReceivedAt, ClockMs: e.in.ClockMs,
			WindowSeq: g.WindowSeq, AlertIds: ids}); err != nil {
			return fmt.Errorf("record the listed presences: %w", err)
		}
	}
	if len(e.missed) > 0 {
		if err := q.MarkPresencesMissed(ctx, dbgen.MarkPresencesMissedParams{OrgID: p.orgID,
			AlertmanagerGroupID: g.ID, AlertIds: e.missed,
			MissedSince: pgtype.Timestamptz{Time: *g.WindowStartedAt, Valid: true}}); err != nil {
			return fmt.Errorf("record the missed presences: %w", err)
		}
	}
	if len(e.gone) > 0 {
		if err := q.EndPresences(ctx, dbgen.EndPresencesParams{OrgID: p.orgID, AlertIds: e.gone,
			AlertmanagerGroupID: pgtype.Int8{Int64: g.ID, Valid: true}}); err != nil {
			return fmt.Errorf("record the gone presences: %w", err)
		}
	}
	if len(e.ended) > 0 {
		ids := make([]int64, len(e.ended))
		for i, a := range e.ended {
			ids[i] = a.ID
		}
		if err := q.EndPresences(ctx, dbgen.EndPresencesParams{OrgID: p.orgID, AlertIds: ids}); err != nil {
			return fmt.Errorf("end the presences of the resolved alerts: %w", err)
		}
	}
	return nil
}

func (p *Processor) writeGroup(ctx context.Context, q ProcessQueries, e *engine) error {
	if e.late || e.in.Internal {
		return nil
	}
	g := e.group
	if err := q.UpdateAlertmanagerGroup(ctx, dbgen.UpdateAlertmanagerGroupParams{OrgID: p.orgID, ID: g.ID,
		LastSnapshotAt: g.LastSnapshotAt, LastSnapshotClockMs: g.LastClockMs, WindowSeq: g.WindowSeq,
		WindowStartedAt: timestamptz(g.WindowStartedAt), WindowTruncated: g.WindowTruncated, Truncated: g.Truncated,
		TruncatedSince: timestamptz(g.TruncatedSince), LastRepeatAt: timestamptz(g.LastRepeatAt),
		LastContentSha256: g.LastContent, LastContentAt: timestamptz(g.LastContentAt)}); err != nil {
		return fmt.Errorf("update the alertmanager group: %w", err)
	}
	return nil
}

// tail writes what a Snapshot changed on rows the lanes of its Integration share, as the last statements before the
// Snapshot is marked, in the lock order of every lane: the Alertmanager route — when it was seen, and the gaps it
// learned added to the ring as it is now — and then the Integration, when the Snapshot changed the truncation of its
// groupKey. Each row is held only until the commit.
func (p *Processor) tail(ctx context.Context, q ProcessQueries, integrationID int64, in snapshotIn,
	out processedSnapshot) error {
	if r := out.route; r != nil {
		if err := p.writeRoute(ctx, q, r, in.ReceivedAt); err != nil {
			return err
		}
	}
	if out.truncated != nil {
		return p.truncationChanged(ctx, q, integrationID, in, *out.truncated)
	}
	return nil
}

// writeRoute refreshes when the Alertmanager route was seen and, when the Snapshot learned gaps, locks the route and
// adds them to its ring as other transactions left it.
func (p *Processor) writeRoute(ctx context.Context, q ProcessQueries, r *route, seen time.Time) error {
	if len(r.observed) == 0 {
		if err := q.TouchAlertmanagerRoute(ctx, dbgen.TouchAlertmanagerRouteParams{OrgID: p.orgID, ID: r.ID,
			SeenAt: seen}); err != nil {
			return fmt.Errorf("record when the alertmanager route was seen: %w", err)
		}
		return nil
	}
	row, err := q.LockAlertmanagerRoute(ctx, dbgen.LockAlertmanagerRouteParams{OrgID: p.orgID, ID: r.ID})
	if err != nil {
		return fmt.Errorf("lock the alertmanager route: %w", err)
	}
	ring := &route{ID: r.ID, Gaps: row.RecentRepeatGapsMs, Observations: row.RepeatObservations}
	for _, gap := range r.observed {
		ring.add(gap)
	}
	if err := q.UpdateRepeatInterval(ctx, dbgen.UpdateRepeatIntervalParams{OrgID: p.orgID, ID: r.ID,
		LearnedRepeatIntervalMs: pgtype.Int8{Int64: *ring.Learned, Valid: true}, RepeatObservations: ring.Observations,
		RecentRepeatGapsMs: ring.Gaps, SeenAt: seen}); err != nil {
		return fmt.Errorf("update the learned repeat interval: %w", err)
	}
	return nil
}
