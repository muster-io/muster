// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/logging"
)

const orgID = 7

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

var errBoom = errors.New("boom")

// fakeRoute is a routes row.
type fakeRoute struct {
	dbgen.LockRoutesRow
	name    string
	deleted bool
}

// fakeMember is an alert_group_alerts row.
type fakeMember struct {
	id, group, alert, episode int64
	state                     string
	joined, startsAt          time.Time
	annotations               []byte
	ended                     *time.Time
	reason, text              string
	movedTo                   int64
}

// fakeEntry is a timeline_entries row.
type fakeEntry struct {
	id int64
	dbgen.InsertTimelineEntryParams
}

// fakeDB is the database of the package in memory, with the semantics of its queries: the routes, Alerts, Alert
// Groups, memberships, Timeline, timers and counter of one Organization.
type fakeDB struct {
	mu       sync.Mutex
	settings dbgen.GetGroupingSettingsRow
	routes   map[int64]*fakeRoute
	alerts   map[int64]*dbgen.ListChangedAlertsRow
	groups   map[int64]*dbgen.LockGroupsRow
	members  []*fakeMember
	entries  []*fakeEntry
	timers   map[string]time.Time
	counter  *int64
	notified int
	users    map[int64]dbgen.ListUserRefsRow
	accounts map[int64]dbgen.ListServiceAccountRefsRow
	ints     map[int64]dbgen.ListIntegrationRefsRow
	keys     map[int64][]string
	notes    []dbgen.ListTimelineNotesRow
	// noteRows are the notes rows InsertNote wrote, whose Alert Group ListTimelineNotes filters by.
	noteRows map[int64]dbgen.InsertNoteParams
	delivery []dbgen.ListTimelineDeliveryEventsRow
	audit    []auditdb.InsertAuditEntryParams
	nextID   int64
	fail     map[string]error
	calls    map[string]int
	locked   [][]int64
	// before runs before a query of that name, for changes that race the transaction.
	before map[string]func()
	// details and summaries are the retention periods in days, timeZone organization.time_zone.
	details, summaries int64
	timeZone           string
	// hints are the live-update hints sent, stats the rows the statistics queries answer.
	hints []db.Hint
	stats []dbgen.RouteStatisticsRow
	// received are the receipt times of the Stored Snapshots, by id.
	received map[int64]time.Time
	// problems are the Alert Groups with a Delivery problem, by id.
	problems map[int64]bool
}

func newDB() *fakeDB {
	return &fakeDB{settings: dbgen.GetGroupingSettingsRow{CriticalIsUrgent: true,
		InstanceLabels: []string{"pod", "instance", "container", "endpoint"}}, routes: map[int64]*fakeRoute{},
		alerts: map[int64]*dbgen.ListChangedAlertsRow{}, groups: map[int64]*dbgen.LockGroupsRow{},
		timers: map[string]time.Time{}, users: map[int64]dbgen.ListUserRefsRow{},
		accounts: map[int64]dbgen.ListServiceAccountRefsRow{}, ints: map[int64]dbgen.ListIntegrationRefsRow{},
		keys: map[int64][]string{}, fail: map[string]error{}, calls: map[string]int{}, before: map[string]func(){},
		nextID: 100, details: 90, summaries: 730, timeZone: "UTC"}
}

func (f *fakeDB) call(name string) error {
	f.calls[name]++
	if b := f.before[name]; b != nil {
		delete(f.before, name)
		f.mu.Unlock()
		b()
		f.mu.Lock()
	}
	return f.fail[name]
}

func (f *fakeDB) id() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeDB) InTx(_ context.Context, fn func(Queries) error) error { return fn(f) }

// DB is no connection: the tests' re-render step writes nothing.
func (f *fakeDB) DB() DBTX { return nil }

func (f *fakeDB) GetSnapshotReceivedAt(_ context.Context, arg dbgen.GetSnapshotReceivedAtParams) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetSnapshotReceivedAt"); err != nil {
		return time.Time{}, err
	}
	at, ok := f.received[arg.ID]
	if !ok || arg.OrgID != orgID {
		return time.Time{}, pgx.ErrNoRows
	}
	return at, nil
}

func (f *fakeDB) GetGroupingSettings(context.Context, int64) (dbgen.GetGroupingSettingsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings, f.call("GetGroupingSettings")
}

func (f *fakeDB) ListChangedAlerts(_ context.Context, arg dbgen.ListChangedAlertsParams) (
	[]dbgen.ListChangedAlertsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListChangedAlerts"); err != nil {
		return nil, err
	}
	var out []dbgen.ListChangedAlertsRow
	for _, id := range arg.Ids {
		if a := f.alerts[id]; a != nil && !slices.ContainsFunc(out, func(r dbgen.ListChangedAlertsRow) bool {
			return r.ID == id
		}) {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeDB) ListFiringMemberships(_ context.Context, arg dbgen.ListFiringMembershipsParams) (
	[]dbgen.ListFiringMembershipsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListFiringMemberships"); err != nil {
		return nil, err
	}
	var out []dbgen.ListFiringMembershipsRow
	for _, m := range f.members {
		if m.state == "firing" && slices.Contains(arg.AlertIds, m.alert) {
			out = append(out, dbgen.ListFiringMembershipsRow{ID: m.id, AlertGroupID: m.group, AlertID: m.alert,
				Episode: m.episode, StartsAt: m.startsAt, Annotations: m.annotations})
		}
	}
	return out, nil
}

func (f *fakeDB) LockRoutes(_ context.Context, arg dbgen.LockRoutesParams) ([]dbgen.LockRoutesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockRoutes"); err != nil {
		return nil, err
	}
	var out []dbgen.LockRoutesRow
	for _, id := range arg.Ids {
		if r := f.routes[id]; r != nil && !r.deleted {
			out = append(out, r.LockRoutesRow)
		}
	}
	return out, nil
}

func (f *fakeDB) LockDefaultRoute(context.Context, int64) (dbgen.LockDefaultRouteRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockDefaultRoute"); err != nil {
		return dbgen.LockDefaultRouteRow{}, err
	}
	for _, r := range f.routes {
		if r.IsDefault {
			return dbgen.LockDefaultRouteRow(r.LockRoutesRow), nil
		}
	}
	return dbgen.LockDefaultRouteRow{}, pgx.ErrNoRows
}

func (f *fakeDB) GetRoutePolicy(_ context.Context, arg dbgen.GetRoutePolicyParams) (dbgen.GetRoutePolicyRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetRoutePolicy"); err != nil {
		return dbgen.GetRoutePolicyRow{}, err
	}
	r := f.routes[arg.ID]
	if r == nil {
		return dbgen.GetRoutePolicyRow{}, pgx.ErrNoRows
	}
	return dbgen.GetRoutePolicyRow(r.LockRoutesRow), nil
}

func (f *fakeDB) FindOpenGroup(_ context.Context, arg dbgen.FindOpenGroupParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("FindOpenGroup"); err != nil {
		return 0, err
	}
	for _, g := range f.sortedGroups() {
		if g.RouteID == arg.RouteID && bytes.Equal(g.GroupKeySha256, arg.GroupKeySha256) && g.Status != "resolved" &&
			!g.MovedFromRouteID.Valid {
			return g.ID, nil
		}
	}
	return 0, pgx.ErrNoRows
}

func (f *fakeDB) FindReopenableGroup(_ context.Context, arg dbgen.FindReopenableGroupParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("FindReopenableGroup"); err != nil {
		return 0, err
	}
	var best *dbgen.LockGroupsRow
	for _, g := range f.sortedGroups() {
		if g.RouteID == arg.RouteID && bytes.Equal(g.GroupKeySha256, arg.GroupKeySha256) && g.ReopenDeadline.Valid &&
			g.ReopenDeadline.Time.After(arg.Now) && (best == nil || !g.ResolvedAt.Time.Before(best.ResolvedAt.Time)) {
			best = g
		}
	}
	if best == nil {
		return 0, pgx.ErrNoRows
	}
	return best.ID, nil
}

func (f *fakeDB) sortedGroups() []*dbgen.LockGroupsRow {
	var out []*dbgen.LockGroupsRow
	for _, g := range f.groups {
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b *dbgen.LockGroupsRow) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (f *fakeDB) PeekGroup(_ context.Context, arg dbgen.PeekGroupParams) (dbgen.PeekGroupRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("PeekGroup"); err != nil {
		return dbgen.PeekGroupRow{}, err
	}
	g := f.groups[arg.ID]
	if g == nil {
		return dbgen.PeekGroupRow{}, pgx.ErrNoRows
	}
	return dbgen.PeekGroupRow{RouteID: g.RouteID, GraceDeadline: g.GraceDeadline}, nil
}

func (f *fakeDB) LockGroups(_ context.Context, arg dbgen.LockGroupsParams) ([]dbgen.LockGroupsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockGroups"); err != nil {
		return nil, err
	}
	f.locked = append(f.locked, slices.Clone(arg.Ids))
	var out []dbgen.LockGroupsRow
	for _, id := range arg.Ids {
		if g := f.groups[id]; g != nil {
			out = append(out, *g)
		}
	}
	return out, nil
}

func (f *fakeDB) EnsureCounter(context.Context, int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("EnsureCounter"); err != nil {
		return err
	}
	if f.counter == nil {
		f.counter = new(int64)
	}
	return nil
}

func (f *fakeDB) LockCounter(context.Context, int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockCounter"); err != nil {
		return 0, err
	}
	if f.counter == nil {
		return 0, pgx.ErrNoRows
	}
	return *f.counter, nil
}

func (f *fakeDB) NextNumber(context.Context, int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("NextNumber"); err != nil {
		return 0, err
	}
	*f.counter++
	return *f.counter, nil
}

func (f *fakeDB) InsertGroup(_ context.Context, arg dbgen.InsertGroupParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertGroup"); err != nil {
		return 0, err
	}
	id := f.id()
	f.groups[id] = &dbgen.LockGroupsRow{ID: id, PublicID: arg.PublicID, Number: arg.Number, RouteID: arg.RouteID,
		GroupKeyLabels: arg.GroupKeyLabels, GroupKeyValues: arg.GroupKeyValues, GroupKeySha256: arg.GroupKeySha256,
		Title: arg.Title, TitleFromGroupKey: arg.TitleFromGroupKey, Summary: arg.Summary, CommonLabels: arg.CommonLabels,
		CommonAnnotations: arg.CommonAnnotations, IntegrationIds: arg.IntegrationIds, Status: "firing",
		SeverityLevel: arg.SeverityLevel, Urgent: arg.Urgent, FiringAgainAfterID: arg.FiringAgainAfterID,
		CreatedAt: arg.CreatedAt, LastChangedAt: arg.CreatedAt}
	return id, nil
}

func (f *fakeDB) SaveGroup(_ context.Context, a dbgen.SaveGroupParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SaveGroup"); err != nil {
		return err
	}
	g := f.groups[a.ID]
	g.RouteID, g.MovedFromRouteID, g.Title, g.TitleFromGroupKey, g.Summary = a.RouteID, a.MovedFromRouteID, a.Title,
		a.TitleFromGroupKey, a.Summary
	g.CommonLabels, g.CommonAnnotations, g.IntegrationIds, g.Status = a.CommonLabels, a.CommonAnnotations,
		a.IntegrationIds, a.Status
	g.SeverityLevel, g.Urgent, g.OwnerUserID, g.AcknowledgedAt = a.SeverityLevel, a.Urgent, a.OwnerUserID,
		a.AcknowledgedAt
	g.SnoozeUntil, g.SnoozeNoEnd, g.SnoozedWhileUrgent = a.SnoozeUntil, a.SnoozeNoEnd, a.SnoozedWhileUrgent
	g.SnoozedByUserID, g.SnoozedByServiceAccountID = a.SnoozedByUserID, a.SnoozedByServiceAccountID
	g.FiringAlertCount, g.ResolvedAlertCount, g.ReopenCount = a.FiringAlertCount, a.ResolvedAlertCount, a.ReopenCount
	g.ResolvedAt, g.ResolvedByKind, g.ResolvedByUserID = a.ResolvedAt, a.ResolvedByKind, a.ResolvedByUserID
	g.ResolvedByServiceAccountID, g.ResolveReason, g.ResolveReasonText = a.ResolvedByServiceAccountID,
		a.ResolveReason, a.ResolveReasonText
	g.ReopenDeadline, g.PriorStatus, g.PriorOwnerUserID = a.ReopenDeadline, a.PriorStatus, a.PriorOwnerUserID
	g.PriorSnoozeUntil, g.PriorSnoozeNoEnd, g.PriorSnoozedWhileUrgent = a.PriorSnoozeUntil, a.PriorSnoozeNoEnd,
		a.PriorSnoozedWhileUrgent
	g.PriorSnoozedByUserID, g.PriorSnoozedByServiceAccountID = a.PriorSnoozedByUserID,
		a.PriorSnoozedByServiceAccountID
	g.GraceDeadline, g.EventSeq, g.LastChangedAt = a.GraceDeadline, a.EventSeq, a.LastChangedAt
	g.FirstAcknowledgedAt = a.FirstAcknowledgedAt
	return nil
}

func (f *fakeDB) IsActiveUser(_ context.Context, arg dbgen.IsActiveUserParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("IsActiveUser"); err != nil {
		return false, err
	}
	return f.users[arg.ID].Status == "active", nil
}

func (f *fakeDB) InsertMemberships(_ context.Context, arg dbgen.InsertMembershipsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertMemberships"); err != nil {
		return err
	}
	for i := range arg.AlertIds {
		for _, m := range f.members {
			if m.alert == arg.AlertIds[i] && m.state == "firing" {
				return errors.New("alert_group_alerts_firing_key")
			}
		}
		f.members = append(f.members, &fakeMember{id: f.id(), group: arg.AlertGroupIds[i], alert: arg.AlertIds[i],
			episode: arg.Episodes[i], state: "firing", joined: arg.JoinedAt, startsAt: arg.StartsAts[i],
			annotations: arg.Annotations[i]})
	}
	return nil
}

func (f *fakeDB) member(id int64) *fakeMember {
	for _, m := range f.members {
		if m.id == id {
			return m
		}
	}
	return nil
}

func (f *fakeDB) ResolveMemberships(_ context.Context, arg dbgen.ResolveMembershipsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ResolveMemberships"); err != nil {
		return err
	}
	for i, id := range arg.Ids {
		if m := f.member(id); m != nil && m.state == "firing" {
			m.state, m.ended, m.reason, m.text = "resolved", &arg.EndedAt, arg.Reasons[i], arg.ReasonTexts[i]
		}
	}
	return nil
}

func (f *fakeDB) MoveMemberships(_ context.Context, arg dbgen.MoveMembershipsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("MoveMemberships"); err != nil {
		return err
	}
	for i, id := range arg.Ids {
		if m := f.member(id); m != nil && m.state == "firing" {
			m.state, m.ended, m.movedTo = "moved", &arg.EndedAt, arg.MovedTos[i]
		}
	}
	return nil
}

func (f *fakeDB) UpdateMemberships(_ context.Context, arg dbgen.UpdateMembershipsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("UpdateMemberships"); err != nil {
		return err
	}
	for i, id := range arg.Ids {
		if m := f.member(id); m != nil {
			m.startsAt, m.annotations = arg.StartsAts[i], arg.Annotations[i]
		}
	}
	return nil
}

func (f *fakeDB) ListGroupFiringAlerts(_ context.Context, arg dbgen.ListGroupFiringAlertsParams) (
	[]dbgen.ListGroupFiringAlertsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupFiringAlerts"); err != nil {
		return nil, err
	}
	var out []dbgen.ListGroupFiringAlertsRow
	for _, m := range f.members {
		if m.state != "firing" || !slices.Contains(arg.AlertGroupIds, m.group) {
			continue
		}
		a := f.alerts[m.alert]
		out = append(out, dbgen.ListGroupFiringAlertsRow{ID: m.id, AlertGroupID: m.group, AlertID: m.alert,
			Episode: m.episode, StartsAt: m.startsAt, Annotations: m.annotations, IntegrationID: a.IntegrationID,
			Fingerprint: a.Fingerprint, Labels: a.Labels, StaticLabelConflicts: a.StaticLabelConflicts,
			SeverityLevel: a.SeverityLevel, Status: a.Status})
	}
	return out, nil
}

func (f *fakeDB) InsertTimelineEntry(_ context.Context, arg dbgen.InsertTimelineEntryParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertTimelineEntry"); err != nil {
		return err
	}
	f.entries = append(f.entries, &fakeEntry{id: f.id(), InsertTimelineEntryParams: arg})
	return nil
}

func (f *fakeDB) ListOpenGroupIDs(context.Context, int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListOpenGroupIDs"); err != nil {
		return nil, err
	}
	var out []int64
	for _, g := range f.sortedGroups() {
		if g.Status != "resolved" {
			out = append(out, g.ID)
		}
	}
	return out, nil
}

func (f *fakeDB) InsertDowntimeEntries(_ context.Context, arg dbgen.InsertDowntimeEntriesParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertDowntimeEntries"); err != nil {
		return err
	}
	for i, id := range arg.AlertGroupIds {
		f.entries = append(f.entries, &fakeEntry{id: f.id(), InsertTimelineEntryParams: dbgen.InsertTimelineEntryParams{
			OrgID: arg.OrgID, PublicID: arg.PublicIds[i], AlertGroupID: id, At: arg.At, Kind: "system",
			SystemEvent: pgtype.Text{String: "muster_unavailable", Valid: true}, Mentions: []string{},
			ActorKind: "system", Transport: "system", PeriodFrom: pgtype.Timestamptz{Time: arg.PeriodFrom, Valid: true},
			PeriodTo: pgtype.Timestamptz{Time: arg.PeriodTo, Valid: true}}})
	}
	return nil
}

func timerKey(group int64, kind string) string { return fmt.Sprintf("%s/%d", kind, group) }

func (f *fakeDB) UpsertTimer(_ context.Context, arg dbgen.UpsertTimerParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("UpsertTimer"); err != nil {
		return err
	}
	f.timers[timerKey(arg.AlertGroupID, arg.Kind)] = arg.Deadline
	return nil
}

func (f *fakeDB) DeleteTimer(_ context.Context, arg dbgen.DeleteTimerParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DeleteTimer"); err != nil {
		return err
	}
	delete(f.timers, timerKey(arg.AlertGroupID, arg.Kind))
	return nil
}

func (f *fakeDB) NotifyTimers(_ context.Context, channel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("NotifyTimers"); err != nil {
		return err
	}
	if channel == TimersChannel {
		f.notified++
	}
	return nil
}

func (f *fakeDB) LockRouteForMove(_ context.Context, arg dbgen.LockRouteForMoveParams) (dbgen.LockRouteForMoveRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockRouteForMove"); err != nil {
		return dbgen.LockRouteForMoveRow{}, err
	}
	for _, r := range f.routes {
		if r.PublicID == arg.PublicID && !r.deleted {
			return dbgen.LockRouteForMoveRow{ID: r.ID, PublicID: r.PublicID, Name: r.name, IsDefault: r.IsDefault}, nil
		}
	}
	return dbgen.LockRouteForMoveRow{}, pgx.ErrNoRows
}

func (f *fakeDB) GetDefaultRouteID(context.Context, int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetDefaultRouteID"); err != nil {
		return 0, err
	}
	for _, r := range f.routes {
		if r.IsDefault {
			return r.ID, nil
		}
	}
	return 0, pgx.ErrNoRows
}

func (f *fakeDB) ListOpenGroupsOfRoute(_ context.Context, arg dbgen.ListOpenGroupsOfRouteParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListOpenGroupsOfRoute"); err != nil {
		return nil, err
	}
	var out []int64
	for _, g := range f.sortedGroups() {
		if g.RouteID == arg.RouteID && g.Status != "resolved" && !g.MovedFromRouteID.Valid {
			out = append(out, g.ID)
		}
	}
	return out, nil
}

func (f *fakeDB) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertAuditEntry"); err != nil {
		return err
	}
	f.audit = append(f.audit, arg)
	return nil
}

func (f *fakeDB) byPublicID(publicID string) *dbgen.LockGroupsRow {
	for _, g := range f.groups {
		if g.PublicID == publicID {
			return g
		}
	}
	return nil
}

func (f *fakeDB) GetGroup(_ context.Context, arg dbgen.GetGroupParams) (dbgen.GetGroupRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetGroup"); err != nil {
		return dbgen.GetGroupRow{}, err
	}
	g := f.byPublicID(arg.PublicID)
	if g == nil {
		return dbgen.GetGroupRow{}, pgx.ErrNoRows
	}
	r := f.routes[g.RouteID]
	out := dbgen.GetGroupRow{ID: g.ID, PublicID: g.PublicID, Number: g.Number, Title: g.Title, Summary: g.Summary,
		Status: g.Status, SeverityLevel: g.SeverityLevel, Urgent: f.urgent(g), GroupKeyValues: g.GroupKeyValues,
		CommonLabels: g.CommonLabels, CommonAnnotations: g.CommonAnnotations, IntegrationIds: g.IntegrationIds,
		ReopenCount: g.ReopenCount, FiringAlertCount: g.FiringAlertCount, ResolvedAlertCount: g.ResolvedAlertCount,
		ResolvedAt: g.ResolvedAt, ResolvedByKind: g.ResolvedByKind, ResolvedByUserID: g.ResolvedByUserID,
		ResolvedByServiceAccountID: g.ResolvedByServiceAccountID, ResolveReason: g.ResolveReason,
		ResolveReasonText: g.ResolveReasonText, CreatedAt: g.CreatedAt, LastChangedAt: g.LastChangedAt,
		RoutePublicID: r.PublicID, RouteName: r.name, RetentionAlertDetailsDays: f.details,
		OwnerUserID: g.OwnerUserID, SnoozeUntil: g.SnoozeUntil, SnoozedByUserID: g.SnoozedByUserID,
		SnoozedByServiceAccountID: g.SnoozedByServiceAccountID, RouteDeleted: r.deleted,
		DeliveryProblem: f.problems[g.ID]}
	out.NewerPublicID, out.NewerNumber = f.newer(g)
	if g.FiringAgainAfterID.Valid {
		out.FiringAgainAfterNumber = f.groups[g.FiringAgainAfterID.Int64].Number
	}
	for _, e := range f.entries {
		if e.AlertGroupID == g.ID && e.Event.String == "alert_replaced" {
			out.ReplacedLabel = e.ReplacedLabel.String
		}
	}
	for _, m := range f.members {
		if m.group == g.ID && m.state == "firing" {
			out.StillFiring++
		}
	}
	return out, nil
}

func (f *fakeDB) GetGroupID(_ context.Context, arg dbgen.GetGroupIDParams) (dbgen.GetGroupIDRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetGroupID"); err != nil {
		return dbgen.GetGroupIDRow{}, err
	}
	if g := f.byPublicID(arg.PublicID); g != nil {
		return dbgen.GetGroupIDRow{ID: g.ID, ResolvedAt: g.ResolvedAt, RetentionAlertDetailsDays: f.details}, nil
	}
	return dbgen.GetGroupIDRow{}, pgx.ErrNoRows
}

// urgent is the urgency of an Alert Group as the reads derive it from its Route and critical is Urgent.
func (f *fakeDB) urgent(g *dbgen.LockGroupsRow) bool {
	return f.routes[g.RouteID].Urgent || (g.SeverityLevel == "critical" && f.settings.CriticalIsUrgent)
}

func (f *fakeDB) ListIntegrationRefs(_ context.Context, arg dbgen.ListIntegrationRefsParams) (
	[]dbgen.ListIntegrationRefsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListIntegrationRefs"); err != nil {
		return nil, err
	}
	var out []dbgen.ListIntegrationRefsRow
	for _, id := range arg.Ids {
		if r, ok := f.ints[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeDB) ListUserRefs(_ context.Context, arg dbgen.ListUserRefsParams) ([]dbgen.ListUserRefsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListUserRefs"); err != nil {
		return nil, err
	}
	var out []dbgen.ListUserRefsRow
	for _, id := range arg.Ids {
		if r, ok := f.users[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeDB) ListServiceAccountRefs(_ context.Context, arg dbgen.ListServiceAccountRefsParams) (
	[]dbgen.ListServiceAccountRefsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListServiceAccountRefs"); err != nil {
		return nil, err
	}
	var out []dbgen.ListServiceAccountRefsRow
	for _, id := range arg.Ids {
		if r, ok := f.accounts[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeDB) ListGroupAlerts(_ context.Context, arg dbgen.ListGroupAlertsParams) ([]dbgen.ListGroupAlertsRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupAlerts"); err != nil {
		return nil, err
	}
	var out []dbgen.ListGroupAlertsRow
	for _, m := range f.members {
		ended := m.state != "firing"
		rank := int32(0)
		if ended {
			rank = 1
		}
		if m.group != arg.AlertGroupID || (!arg.AllStates && arg.Firing == ended) ||
			cmp.Or(cmp.Compare(rank, arg.AfterRank), cmp.Compare(m.id, arg.AfterID)) <= 0 {
			continue
		}
		a := f.alerts[m.alert]
		row := dbgen.ListGroupAlertsRow{ID: m.id, Ended: ended, State: m.state, StartsAt: m.startsAt,
			Annotations: m.annotations, AlertID: a.ID, Fingerprint: a.Fingerprint, Labels: a.Labels,
			LastSeenAt: t0, IntegrationPublicID: "NTAAAAAAAAAAAA", IntegrationName: "lab"}
		if m.ended != nil {
			row.EndedAt = pgtype.Timestamptz{Time: *m.ended, Valid: true}
		}
		if m.reason != "" {
			row.ResolveReason = pgtype.Text{String: m.reason, Valid: true}
			row.ResolveReasonText = pgtype.Text{String: m.text, Valid: true}
		}
		if a.ID%2 == 0 {
			row.GeneratorUrl = pgtype.Text{String: "http://prometheus/graph", Valid: true}
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b dbgen.ListGroupAlertsRow) int {
		return cmp.Or(compareBool(a.Ended, b.Ended), cmp.Compare(a.ID, b.ID))
	})
	return out[:min(len(out), int(arg.Lim))], nil
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

func (f *fakeDB) ListAlertGroupKeys(_ context.Context, arg dbgen.ListAlertGroupKeysParams) (
	[]dbgen.ListAlertGroupKeysRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListAlertGroupKeys"); err != nil {
		return nil, err
	}
	var out []dbgen.ListAlertGroupKeysRow
	for _, id := range arg.AlertIds {
		for _, k := range f.keys[id] {
			out = append(out, dbgen.ListAlertGroupKeysRow{AlertID: id, GroupKey: k})
		}
	}
	return out, nil
}

// timeline lists the entries of a group after or before a position, in either order.
func (f *fakeDB) timeline(group int64, kinds []string, at pgtype.Timestamptz, source int32, id int64, desc bool,
	lim int32) []dbgen.ListTimelineDescRow {
	var out []dbgen.ListTimelineDescRow
	for _, e := range f.entries {
		if e.AlertGroupID != group || (len(kinds) > 0 && !slices.Contains(kinds, e.Kind)) {
			continue
		}
		if at.Valid {
			c := cmp.Or(e.At.Compare(at.Time), cmp.Compare(0, int(source)), cmp.Compare(e.id, id))
			if (desc && c >= 0) || (!desc && c <= 0) {
				continue
			}
		}
		out = append(out, dbgen.ListTimelineDescRow{ID: e.id, PublicID: e.PublicID, At: e.At, Kind: e.Kind,
			Event: e.Event, EventSeq: e.EventSeq, SystemEvent: e.SystemEvent, Loudness: e.Loudness,
			Mentions: e.Mentions, ActorKind: e.ActorKind, ActorUserID: e.ActorUserID,
			ActorServiceAccountID: e.ActorServiceAccountID, TokenName: e.TokenName, Transport: e.Transport,
			Reason: e.Reason, FromStatus: e.FromStatus, ToStatus: e.ToStatus, OwnerUserID: e.OwnerUserID,
			PreviousOwnerUserID: e.PreviousOwnerUserID, SnoozeUntil: e.SnoozeUntil, Fingerprints: e.Fingerprints,
			ReplacedLabel: e.ReplacedLabel, LabelConflicts: e.LabelConflicts, PeriodFrom: e.PeriodFrom,
			PeriodTo: e.PeriodTo, Detail: e.Detail})
	}
	slices.SortFunc(out, func(a, b dbgen.ListTimelineDescRow) int {
		c := cmp.Or(a.At.Compare(b.At), cmp.Compare(a.ID, b.ID))
		if desc {
			return -c
		}
		return c
	})
	return out[:min(len(out), int(lim))]
}

func (f *fakeDB) ListTimelineDesc(_ context.Context, arg dbgen.ListTimelineDescParams) ([]dbgen.ListTimelineDescRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListTimelineDesc"); err != nil {
		return nil, err
	}
	return f.timeline(arg.AlertGroupID, arg.Kinds, arg.At, arg.Source, arg.ID, true, arg.Lim), nil
}

func (f *fakeDB) ListTimelineAsc(_ context.Context, arg dbgen.ListTimelineAscParams) ([]dbgen.ListTimelineAscRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListTimelineAsc"); err != nil {
		return nil, err
	}
	var out []dbgen.ListTimelineAscRow
	for _, r := range f.timeline(arg.AlertGroupID, arg.Kinds, arg.At, arg.Source, arg.ID, false, arg.Lim) {
		out = append(out, dbgen.ListTimelineAscRow(r))
	}
	return out, nil
}

// after reports whether the position (at, source, id) comes after the cursor in the order.
func after(at time.Time, source int, id int64, cur pgtype.Timestamptz, curSource int32, curID int64,
	desc bool) bool {
	if !cur.Valid {
		return true
	}
	c := cmp.Or(at.Compare(cur.Time), cmp.Compare(source, int(curSource)), cmp.Compare(id, curID))
	return (desc && c < 0) || (!desc && c > 0)
}

func (f *fakeDB) ListTimelineNotes(_ context.Context, arg dbgen.ListTimelineNotesParams) ([]dbgen.ListTimelineNotesRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListTimelineNotes"); err != nil {
		return nil, err
	}
	var out []dbgen.ListTimelineNotesRow
	for _, n := range f.notes {
		if r, ok := f.noteRows[n.ID]; ok && r.AlertGroupID != arg.AlertGroupID {
			continue
		}
		if after(n.CreatedAt, 1, n.ID, arg.At, arg.Source, arg.ID, arg.Descending) {
			out = append(out, n)
		}
	}
	return out[:min(len(out), int(arg.Lim))], nil
}

func (f *fakeDB) ListTimelineDeliveryEvents(_ context.Context, arg dbgen.ListTimelineDeliveryEventsParams) (
	[]dbgen.ListTimelineDeliveryEventsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListTimelineDeliveryEvents"); err != nil {
		return nil, err
	}
	var out []dbgen.ListTimelineDeliveryEventsRow
	for _, d := range f.delivery {
		if after(d.OccurredAt, 2, d.ID, arg.At, arg.Source, arg.ID, arg.Descending) {
			out = append(out, d)
		}
	}
	return out[:min(len(out), int(arg.Lim))], nil
}

func (f *fakeDB) CountOpenGroupsByRoute(context.Context, int64) ([]dbgen.CountOpenGroupsByRouteRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("CountOpenGroupsByRoute"); err != nil {
		return nil, err
	}
	counts := map[[2]string]int64{}
	for _, g := range f.groups {
		if g.Status != "resolved" {
			counts[[2]string{f.routes[g.RouteID].PublicID, g.Status}]++
		}
	}
	var out []dbgen.CountOpenGroupsByRouteRow
	for k, n := range counts {
		out = append(out, dbgen.CountOpenGroupsByRouteRow{PublicID: k[0], Status: k[1], Count: n})
	}
	return out, nil
}

func (f *fakeDB) ListRoutePublicIDs(context.Context, int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListRoutePublicIDs"); err != nil {
		return nil, err
	}
	var out []string
	for _, r := range f.routes {
		if !r.deleted {
			out = append(out, r.PublicID)
		}
	}
	return out, nil
}

// harness is a Service over the fake database at a manual business clock, with the log it writes.
type harness struct {
	db       *fakeDB
	svc      *Service
	clock    *clock.Manual
	log      *bytes.Buffer
	restamps [][]int64
	nextFP   int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{db: newDB(), clock: clock.NewManual(t0), log: &bytes.Buffer{}}
	logger := logging.New(h.log, logging.LevelInfo)
	h.svc = New(Config{OrgID: orgID, Store: h.db, Audit: audit.NewWriter(logger, h.clock), Business: h.clock,
		Log: logger, Restamp: func(_ context.Context, _ DBTX, ids []int64, _ int64) error {
			h.restamps = append(h.restamps, ids)
			return h.db.fail["Restamp"]
		}})
	h.svc.queries = func(dbgen.DBTX) Queries { return h.db }
	h.route(1, "RTDEFAAAAAAAAA", "Default", true, []string{"alertname"})
	h.route(2, "RTDBAAAAAAAAAA", "db", false, []string{"alertname", "cluster"})
	h.route(3, "RTNETAAAAAAAAA", "net", false, []string{"cluster"})
	h.db.ints[5] = dbgen.ListIntegrationRefsRow{ID: 5, PublicID: "NTAAAAAAAAAAAA", Name: "lab"}
	return h
}

// route adds a Route with the policy of the On-call profile.
func (h *harness) route(id int64, publicID, name string, isDefault bool, key []string) {
	h.db.routes[id] = &fakeRoute{LockRoutesRow: dbgen.LockRoutesRow{ID: id, PublicID: publicID, IsDefault: isDefault, GroupKey: key,
		ReopenWindowSeconds: 900, GracePeriodSeconds: 900, UrgentRiseRemovesAck: true}, name: name}
}

// alert adds a firing Alert on the Route with labels and annotations, at the Severity level, and returns its id.
func (h *harness) alert(route int64, level string, labels map[string]string, annotations ...string) int64 {
	h.nextFP++
	id := int64(h.nextFP)
	ann := map[string]string{}
	for i := 0; i+1 < len(annotations); i += 2 {
		ann[annotations[i]] = annotations[i+1]
	}
	lb, _ := json.Marshal(labels)
	ab, _ := json.Marshal(ann)
	h.db.alerts[id] = &dbgen.ListChangedAlertsRow{ID: id, IntegrationID: 5, Fingerprint: fingerprintOf(id),
		Labels: lb, Annotations: ab, Status: "firing", StartsAt: t0, Episode: 1,
		RouteID: pgtype.Int8{Int64: route, Valid: route != 0}, SeverityLevel: pgtype.Text{String: level, Valid: level != ""}}
	return id
}

func fingerprintOf(id int64) string { return fmt.Sprintf("fp%03d", id) }

// changes hands changes of one kind over as one Snapshot.
func (h *harness) changes(t *testing.T, kind ingest.ChangeKind, ids ...int64) ingest.Routed {
	t.Helper()
	out, err := h.apply(kind, ids...)
	if err != nil {
		t.Fatalf("%s %v: %v", kind, ids, err)
	}
	return out
}

func (h *harness) apply(kind ingest.ChangeKind, ids ...int64) (ingest.Routed, error) {
	var cs []ingest.AlertChange
	for _, id := range ids {
		c := ingest.AlertChange{Kind: kind, AlertID: id}
		if kind == ingest.ChangeResolved {
			c.Reason, c.ReasonText = "resolved", "resolved by Alertmanager"
		}
		cs = append(cs, c)
	}
	out, err := h.svc.AlertChanges(context.Background(), nil, cs)
	if err == nil && out.Committed != nil {
		out.Committed(context.Background())
	}
	return out, err
}

// resolve marks Alerts resolved and hands the resolutions over as one Snapshot.
func (h *harness) resolve(t *testing.T, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		h.db.alerts[id].Status = "resolved"
	}
	h.changes(t, ingest.ChangeResolved, ids...)
}

// refire makes resolved Alerts fire again and hands the new firings over.
func (h *harness) refire(t *testing.T, ids ...int64) ingest.Routed {
	t.Helper()
	for _, id := range ids {
		h.db.alerts[id].Status = "firing"
		h.db.alerts[id].Episode++
	}
	return h.changes(t, ingest.ChangeFired, ids...)
}

// groupOf is the Alert Group an Alert fires in, or last fired in.
func (h *harness) groupOf(t *testing.T, alert int64) *dbgen.LockGroupsRow {
	t.Helper()
	var last *fakeMember
	for _, m := range h.db.members {
		if m.alert == alert {
			last = m
		}
	}
	if last == nil {
		t.Fatalf("alert %d is in no alert group", alert)
	}
	return h.db.groups[last.group]
}

// entriesOf are the Timeline entries of an Alert Group in order.
func (h *harness) entriesOf(g *dbgen.LockGroupsRow) []*fakeEntry {
	var out []*fakeEntry
	for _, e := range h.db.entries {
		if e.AlertGroupID == g.ID {
			out = append(out, e)
		}
	}
	return out
}

// last is the latest Timeline entry of an Alert Group.
func (h *harness) last(t *testing.T, g *dbgen.LockGroupsRow) *fakeEntry {
	t.Helper()
	es := h.entriesOf(g)
	if len(es) == 0 {
		t.Fatalf("alert group #%d has no timeline entries", g.Number)
	}
	return es[len(es)-1]
}

// events are the events, or system events, of the Timeline of an Alert Group.
func (h *harness) events(g *dbgen.LockGroupsRow) []string {
	var out []string
	for _, e := range h.entriesOf(g) {
		out = append(out, cmp.Or(e.Event.String, e.SystemEvent.String))
	}
	return out
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func i8(v int64) pgtype.Int8 { return pgtype.Int8{Int64: v, Valid: true} }

func txt(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// TestGroupRoundTrip: a locked row reads into a Group and writes back unchanged, the status to return to included.
func TestGroupRoundTrip(t *testing.T) {
	r := dbgen.LockGroupsRow{ID: 4, PublicID: "AGAAAAAAAAAAAA", Number: 3, RouteID: 2, MovedFromRouteID: i8(5),
		GroupKeyValues: []byte(`{"a":"1"}`), CommonLabels: []byte(`{"a":"1"}`), CommonAnnotations: []byte(`{}`),
		Title: "t", Summary: txt("s"), IntegrationIds: []int64{5}, Status: "resolved", SeverityLevel: "critical",
		Urgent: true, FiringAlertCount: 1, ResolvedAlertCount: 2, ReopenCount: 3, ResolvedAt: ts(t0),
		ResolvedByKind: txt("system"), ResolveReason: txt("gone"), ResolveReasonText: txt("gone"),
		ReopenDeadline: ts(t0.Add(time.Minute)), PriorStatus: txt("snoozed"), PriorSnoozeUntil: ts(t0),
		PriorSnoozeNoEnd: pgtype.Bool{Bool: false, Valid: true}, PriorSnoozedWhileUrgent: pgtype.Bool{Bool: true,
			Valid: true}, PriorSnoozedByServiceAccountID: i8(8), GraceDeadline: ts(t0), EventSeq: 9, LastChangedAt: t0}
	g, err := groupOf(r)
	if err != nil {
		t.Fatal(err)
	}
	p := g.saveParams(orgID)
	if p.ID != 4 || p.MovedFromRouteID != i8(5) || p.PriorStatus != txt("snoozed") ||
		p.PriorSnoozedByServiceAccountID != i8(8) || !p.PriorSnoozedWhileUrgent.Bool || p.EventSeq != 9 ||
		p.ResolveReason != txt("gone") || string(p.CommonLabels) != `{"a":"1"}` || p.Summary != txt("s") ||
		!p.GraceDeadline.Valid || p.ReopenCount != 3 {
		t.Errorf("params %+v", p)
	}
	if string(jsonOf(nil)) != `{}` {
		t.Error("jsonOf(nil)")
	}
}

// failing is a transition whose precondition refuses.
type failing struct{}

func (failing) precondition(*Group) error { return errBoom }

func (failing) apply(context.Context, Queries, *Group, *change) error { return nil }

// TestDispatch is ADR-0016's order: precondition, transition, Timeline, save, re-render; a refused precondition
// changes nothing; the counters and log lines run only once committed.
func TestDispatch(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	row := h.groupOf(t, a)
	g, _ := groupOf(*row)
	var after committed
	if err := h.svc.d.dispatch(t.Context(), h.db, g, System, failing{}, &after); !errors.Is(err, errBoom) ||
		len(after) != 0 {
		t.Errorf("refused = %v", err)
	}
	var rendered []int64
	h.svc.d.rerender = func(_ context.Context, _ Queries, r Rendering) error {
		rendered = append(rendered, r.Group.ID)
		return nil
	}
	if err := h.svc.d.dispatch(t.Context(), h.db, g, System, moveToDefault{from: 2, to: 1}, &after); err != nil ||
		!slices.Equal(rendered, []int64{row.ID}) || row.RouteID != 1 {
		t.Errorf("moved = %v, rendered %v", err, rendered)
	}
	// A Route template that failed while rendering is recorded as the system entry fallback_template_used, by the
	// system, once the re-render step returned; a failed record fails the change.
	h.svc.d.rerender = func(_ context.Context, _ Queries, r Rendering) error {
		r.Fallback(TemplateFailure{Template: "root_message", Detail: "root_message template failed: line 1: x"})
		return nil
	}
	g.MovedFromRouteID, g.RouteID = nil, 2
	entries := len(h.db.entries)
	if err := h.svc.d.dispatch(t.Context(), h.db, g, Actor{Kind: audit.ActorUser, Transport: audit.TransportUI, Person: alice.Actor}, moveToDefault{from: 2, to: 1}, &after); err != nil {
		t.Fatal(err)
	}
	if e := h.db.entries[len(h.db.entries)-1]; len(h.db.entries) != entries+2 || e.Kind != string(KindSystem) ||
		e.SystemEvent.String != "fallback_template_used" || e.Detail.String != "root_message template failed: line 1: x" ||
		e.ActorKind != "system" || e.Event.Valid {
		t.Errorf("fallback entry %+v", e)
	}
	h.db.fail["InsertTimelineEntry"] = errBoom
	g.MovedFromRouteID, g.RouteID = nil, 2
	if err := h.svc.d.dispatch(t.Context(), h.db, g, System, moveToDefault{from: 2, to: 1}, &after); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed fallback entry = %v", err)
	}
	delete(h.db.fail, "InsertTimelineEntry")
	h.svc.d.rerender = func(context.Context, Queries, Rendering) error { return errBoom }
	g.MovedFromRouteID = nil
	g.RouteID = 2
	if err := h.svc.d.dispatch(t.Context(), h.db, g, System, moveToDefault{from: 2, to: 1}, &after); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed re-render = %v", err)
	}
	h.svc.d.rerender = nil
	h.db.fail["GetRoutePolicy"] = errBoom
	b := h.alert(2, "warning", map[string]string{"alertname": "B", "cluster": "x"})
	if _, err := h.apply(ingest.ChangeFired, b); !errors.Is(err, errBoom) {
		t.Errorf("a status change without its route = %v", err)
	}
}

// TestRerender is the re-render step of S-034: delivery gets, in the dispatcher's transaction, the changed Alert Group,
// the actor, the lifecycle events recorded with their numbers, rows and fingerprints, and the receipt time of the
// Stored Snapshot behind them, read once per transaction; a Command or a timer has none.
func TestRerender(t *testing.T) {
	h := newHarness(t)
	received := t0.Add(-3 * time.Second)
	h.db.received = map[int64]time.Time{77: received}
	var got []Rendering
	var afterCommit []bool
	h.svc.SetRerender(func(_ context.Context, tx DBTX, r Rendering) error {
		if tx != nil {
			t.Errorf("tx = %v", tx)
		}
		got = append(got, r)
		// What the re-render queues runs once the change committed, not inside it.
		i := len(afterCommit)
		afterCommit = append(afterCommit, false)
		r.After(func(context.Context) { afterCommit[i] = true })
		if afterCommit[i] {
			t.Error("the queued function ran inside the transaction")
		}
		return nil
	})
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "job": "j2"})
	ctx := withSnapshot(t.Context(), 77)
	for _, id := range []int64{a, b} {
		routed, err := h.svc.AlertChanges(ctx, nil, []ingest.AlertChange{{Kind: ingest.ChangeFired, AlertID: id,
			StoredSnapshotID: 77}})
		if err != nil {
			t.Fatal(err)
		}
		if routed.Committed == nil {
			t.Fatal("nothing to run once the snapshot committed")
		}
		routed.Committed(t.Context())
	}
	if len(got) != 2 || !slices.Equal(afterCommit, []bool{true, true}) {
		t.Fatalf("renderings %+v, after commit %v", got, afterCommit)
	}
	c, j := got[0], got[1]
	if c.ReceivedAt == nil || !c.ReceivedAt.Equal(received) || c.Actor.Kind != audit.ActorSystem ||
		len(c.Events) != 1 || c.Events[0].Event != EventCreated || c.Events[0].Seq != 1 ||
		c.Events[0].Loudness != Loud || !slices.Equal(c.Events[0].Mentions, []Mention{MentionNewAlertGroup}) ||
		len(c.Events[0].Fingerprints) != 1 {
		t.Errorf("created %+v", c)
	}
	if j.Group.ID != c.Group.ID || len(j.Events) != 1 || j.Events[0].Event != EventAlertsAdded ||
		j.Events[0].Variant != VariantFiring || j.Events[0].Seq != 2 || j.ReceivedAt == nil {
		t.Errorf("joined %+v", j)
	}
	if h.db.calls["GetSnapshotReceivedAt"] != 2 {
		t.Errorf("read the receipt %d times", h.db.calls["GetSnapshotReceivedAt"])
	}
	// A note is a lifecycle event too; a Command has no Snapshot.
	got = nil
	g := h.groupOf(t, a)
	if _, err := h.svc.AddNote(t.Context(), alice, g.PublicID, "looking"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ReceivedAt != nil || len(got[0].Events) != 1 ||
		got[0].Events[0].Event != EventNoteAdded || got[0].Events[0].Seq != 3 {
		t.Errorf("note %+v", got)
	}
	// A Snapshot that cannot be read fails the change.
	h.db.fail["GetSnapshotReceivedAt"] = errBoom
	other := h.alert(3, "warning", map[string]string{"alertname": "B", "cluster": "y"})
	if _, err := h.svc.AlertChanges(t.Context(), nil, []ingest.AlertChange{{Kind: ingest.ChangeFired, AlertID: other,
		StoredSnapshotID: 78}}); !errors.Is(err, errBoom) {
		t.Errorf("unread snapshot = %v", err)
	}
	if withSnapshot(t.Context(), 0) != t.Context() {
		t.Error("a change without a snapshot carries one")
	}
}

// TestMove is C-09.FR-19, C-09.AC-9 and C-08.FR-9: the open Alert Groups of a Route move to the Default route with a
// moved_to_default_route entry each and one Audit log entry; a moved Alert Group keeps its key values, takes no new
// Alert and stays open beside an Alert Group of the Default route with the same values.
func TestMove(t *testing.T) {
	h := newHarness(t)
	h.db.routes[2].GroupKey = []string{"alertname"}
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	b := h.alert(2, "warning", map[string]string{"alertname": "B", "cluster": "x"})
	c := h.alert(2, "warning", map[string]string{"alertname": "C", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a, b, c)
	h.resolve(t, c)
	by := Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportUI}
	n, err := h.svc.MoveOpenAlertGroups(t.Context(), by, "rtdbaaaaaaaaaa")
	if err != nil || n != 2 {
		t.Fatalf("moved %d, %v", n, err)
	}
	ga := h.groupOf(t, a)
	e := h.last(t, ga)
	if ga.RouteID != 1 || ga.MovedFromRouteID.Int64 != 2 || ga.Status != "firing" ||
		string(ga.GroupKeyValues) != `{"alertname":"A"}` || e.Event.String != "moved_to_default_route" ||
		e.SystemEvent.String != "moved_to_default_route" || e.Kind != "system" || e.Loudness.String != "quiet" {
		t.Errorf("moved %+v, entry %+v", ga, e.InsertTimelineEntryParams)
	}
	if h.groupOf(t, c).RouteID != 2 {
		t.Error("moved a resolved alert group")
	}
	au := h.db.audit[len(h.db.audit)-1]
	if au.Action != ActionOpenAlertGroupsMoved || au.ResourcePublicID.String != "RTDBAAAAAAAAAA" ||
		au.ResourceName.String != "db" || string(au.Details) != `{"count":2}` {
		t.Errorf("audit %+v", au)
	}
	// An Alert of the Default route with the same key values starts an Alert Group of its own.
	d := h.alert(1, "warning", map[string]string{"alertname": "A"})
	h.changes(t, ingest.ChangeFired, d)
	if gd := h.groupOf(t, d); gd == ga || gd.RouteID != 1 {
		t.Error("a moved alert group took a new alert")
	}
	if n, err := h.svc.MoveOpenAlertGroups(t.Context(), by, "RTDBAAAAAAAAAA"); err != nil || n != 0 {
		t.Errorf("again: %d, %v", n, err)
	}
	for id, want := range map[string]error{"bad": ErrRouteNotFound, "RTZZZZZZZZZZZZ": ErrRouteNotFound,
		"RTDEFAAAAAAAAA": ErrDefaultRoute} {
		if _, err := h.svc.MoveOpenAlertGroups(t.Context(), by, id); !errors.Is(err, want) {
			t.Errorf("%s = %v", id, err)
		}
	}
	for _, q := range []string{"LockRouteForMove", "GetDefaultRouteID", "ListOpenGroupsOfRoute", "LockGroups",
		"InsertTimelineEntry", "InsertAuditEntry"} {
		h := newHarness(t)
		h.changes(t, ingest.ChangeFired, h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"}))
		h.db.fail[q] = errBoom
		if _, err := h.svc.MoveOpenAlertGroups(t.Context(), by, "RTDBAAAAAAAAAA"); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", q, err)
		}
	}
}

// TestDowntime is C-09.FR-18, C-02.FR-12 and C-09.AC-11: every open Alert Group gets a muster_unavailable entry with
// the period; resolved ones do not.
func TestDowntime(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	b := h.alert(2, "warning", map[string]string{"alertname": "B", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a, b)
	h.resolve(t, b)
	from, to := t0.Add(-10*time.Minute), t0
	if err := h.svc.RecordDowntime(t.Context(), nil, from, to); err != nil {
		t.Fatal(err)
	}
	e := h.last(t, h.groupOf(t, a))
	if e.SystemEvent.String != "muster_unavailable" || e.Kind != "system" || e.Event.Valid ||
		!e.PeriodFrom.Time.Equal(from) || !e.PeriodTo.Time.Equal(to) || !strings.HasPrefix(e.PublicID, "TE") {
		t.Errorf("entry %+v", e.InsertTimelineEntryParams)
	}
	if h.last(t, h.groupOf(t, b)).SystemEvent.Valid {
		t.Error("a resolved alert group got the entry")
	}
	h.resolve(t, a)
	if err := h.svc.RecordDowntime(t.Context(), nil, from, to); err != nil || h.db.calls["InsertDowntimeEntries"] != 1 {
		t.Errorf("no open alert groups: %v", err)
	}
	for _, q := range []string{"ListOpenGroupIDs", "InsertDowntimeEntries"} {
		h := newHarness(t)
		h.changes(t, ingest.ChangeFired, h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"}))
		h.db.fail[q] = errBoom
		if err := h.svc.RecordDowntime(t.Context(), nil, from, to); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", q, err)
		}
	}
}

// TestGet is C-09.FR-1, FR-10, FR-14 and C-09.AC-4: getAlertGroup reads the status, Route, Integrations, title,
// summary, Severity level, counts, the resolution with its reason and the notices.
func TestGet(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "critical", map[string]string{"alertname": "A", "cluster": "x", "pod": "1"}, "summary", "s")
	h.changes(t, ingest.ChangeFired, a)
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "2"})
	h.changes(t, ingest.ChangeFired, b)
	g := h.groupOf(t, a)
	v, err := h.svc.Get(t.Context(), strings.ToLower(g.PublicID))
	if err != nil {
		t.Fatal(err)
	}
	if v.Number != 1 || v.Title != "A" || *v.Summary != "s" || v.Status != StatusFiring || v.Severity != "critical" ||
		!v.Urgent || v.Route != (Ref{PublicID: "RTDBAAAAAAAAAA", Name: "db"}) || len(v.Integrations) != 1 ||
		v.Integrations[0].Name != "lab" || v.FiringCount != 2 || v.Resolution != nil || v.ResolvedAt != nil ||
		v.GroupLabels["cluster"] != "x" || v.CommonLabels["alertname"] != "A" || v.CommonAnnotations == nil {
		t.Errorf("view %+v", v)
	}
	if len(v.Notices) != 1 || v.Notices[0].Kind != NoticeReplacement || *v.Notices[0].Label != "pod" {
		t.Errorf("notices %+v", v.Notices)
	}
	// A system resolution carries its reason; a person's names the person and counts the Alerts still firing.
	h.resolve(t, a, b)
	if v, _ := h.svc.Get(t.Context(), g.PublicID); v.Resolution == nil || v.Resolution.By != "system" ||
		*v.Resolution.ReasonCode != "resolved" || *v.Resolution.Reason != "resolved by Alertmanager" ||
		v.Resolution.Actor != nil || v.ResolvedAt == nil {
		t.Errorf("system resolution %+v", v.Resolution)
	}
	c := h.alert(2, "warning", map[string]string{"alertname": "C", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, c)
	gc := h.groupOf(t, c)
	h.personResolve(gc, time.Minute)
	h.db.users[9] = dbgen.ListUserRefsRow{ID: 9, PublicID: "SRAAAAAAAAAAA9", Name: "Alice", Login: "alice",
		Status: "deleted"}
	if v, _ := h.svc.Get(t.Context(), gc.PublicID); v.Resolution == nil || v.Resolution.Actor == nil ||
		*v.Resolution.Actor != (ActorRef{Kind: "user", PublicID: "SRAAAAAAAAAAA9", Name: "Alice", Login: "alice",
			Deactivated: true}) || len(v.Notices) != 1 || v.Notices[0].Kind != NoticeAlertsStillFiring ||
		*v.Notices[0].Count != 1 {
		t.Errorf("person's resolution %+v %+v", v.Resolution, v.Notices)
	}
	h.db.fail = map[string]error{"ListUserRefs": errBoom}
	if _, err := h.svc.Get(t.Context(), gc.PublicID); !errors.Is(err, errBoom) {
		t.Errorf("ListUserRefs failing = %v", err)
	}
	h.db.fail = map[string]error{}
	gc.ResolvedByUserID, gc.ResolvedByServiceAccountID = pgtype.Int8{}, i8(4)
	h.db.accounts[4] = dbgen.ListServiceAccountRefsRow{ID: 4, PublicID: "SAAAAAAAAAAAAA", Name: "bot", Status: "active"}
	if v, _ := h.svc.Get(t.Context(), gc.PublicID); v.Resolution.Actor == nil ||
		v.Resolution.Actor.Kind != "service_account" || v.Resolution.Actor.Name != "bot" {
		t.Errorf("service account %+v", v.Resolution.Actor)
	}
	for id, want := range map[string]error{"nope": ErrNotFound, "AGZZZZZZZZZZZZ": ErrNotFound} {
		if _, err := h.svc.Get(t.Context(), id); !errors.Is(err, want) {
			t.Errorf("%s = %v", id, err)
		}
	}
	for _, q := range []string{"GetGroup", "ListIntegrationRefs", "ListServiceAccountRefs"} {
		h.db.fail = map[string]error{q: errBoom}
		if _, err := h.svc.Get(t.Context(), gc.PublicID); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", q, err)
		}
	}
	h.db.fail = map[string]error{}
	// The links are computed on read by the Linker (C-09.FR-14, C-12.FR-9).
	if v, _ := h.svc.Get(t.Context(), gc.PublicID); v.Links == nil || len(v.Links) != 0 {
		t.Errorf("no linker, no links %+v", v.Links)
	}
	var asked int64
	h.svc.SetLinks(func(_ context.Context, id int64) ([]Link, error) {
		asked = id
		return []Link{{Name: "Runbook", URL: "https://r.example.org"}}, nil
	})
	if v, err := h.svc.Get(t.Context(), gc.PublicID); err != nil || asked != gc.ID ||
		!slices.Equal(v.Links, []Link{{Name: "Runbook", URL: "https://r.example.org"}}) {
		t.Errorf("links %+v %v", v.Links, err)
	}
	h.svc.SetLinks(func(context.Context, int64) ([]Link, error) { return nil, errBoom })
	if _, err := h.svc.Get(t.Context(), gc.PublicID); !errors.Is(err, errBoom) {
		t.Errorf("failing links = %v", err)
	}
	h.svc.SetLinks(nil)
	gc.CommonLabels = []byte("[")
	if _, err := h.svc.Get(t.Context(), gc.PublicID); err == nil {
		t.Error("a bad row")
	}
}

// TestAlerts is C-09.FR-14, C-06.FR-19: the Alerts of an Alert Group, firing first, then resolved with their reason,
// each with its Integration, labels, annotations, Alertmanager groups and source link, by page and by state.
func TestAlerts(t *testing.T) {
	h := newHarness(t)
	var ids []int64
	for _, n := range []string{"1", "2", "3", "4"} {
		ids = append(ids, h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": n}))
	}
	h.changes(t, ingest.ChangeFired, ids...)
	h.resolve(t, ids[0])
	h.db.keys[ids[1]] = []string{"{}:{alertname=\"A\"}"}
	g := h.groupOf(t, ids[1])
	var all []GroupAlert
	f := AlertFilter{Limit: 3}
	for {
		page, err := h.svc.Alerts(t.Context(), g.PublicID, f)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Alerts...)
		if page.Next == nil {
			break
		}
		f.After = page.Next
	}
	if len(all) != 4 || !all[0].Firing || all[3].Firing || all[3].Fingerprint != fingerprintOf(ids[0]) ||
		*all[3].Reason != "resolved" || all[3].ResolvedAt == nil || all[0].Integration.Name != "lab" ||
		all[0].Labels["n"] != "2" || len(all[0].GroupKeys) != 1 || *all[0].SourceURL != "http://prometheus/graph" ||
		all[1].SourceURL != nil || all[1].GroupKeys == nil {
		t.Errorf("alerts %+v", all)
	}
	resolved, err := h.svc.Alerts(t.Context(), g.PublicID, AlertFilter{State: "resolved", Limit: 10})
	if err != nil || len(resolved.Alerts) != 1 {
		t.Errorf("resolved %+v, %v", resolved, err)
	}
	firing, _ := h.svc.Alerts(t.Context(), g.PublicID, AlertFilter{State: "firing", Limit: 10})
	if len(firing.Alerts) != 3 {
		t.Errorf("firing %+v", firing)
	}
	if _, err := h.svc.Alerts(t.Context(), "AGZZZZZZZZZZZZ", AlertFilter{Limit: 1}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	for _, q := range []string{"GetGroupID", "ListGroupAlerts", "ListAlertGroupKeys"} {
		h.db.fail = map[string]error{q: errBoom}
		if _, err := h.svc.Alerts(t.Context(), g.PublicID, AlertFilter{Limit: 10}); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", q, err)
		}
	}
	h.db.fail = map[string]error{}
	h.db.alerts[ids[1]].Labels = []byte("[")
	if _, err := h.svc.Alerts(t.Context(), g.PublicID, AlertFilter{Limit: 10}); err == nil {
		t.Error("bad labels")
	}
	h.db.alerts[ids[1]].Labels = []byte(`{}`)
	for _, m := range h.db.members {
		m.annotations = []byte("[")
	}
	if _, err := h.svc.Alerts(t.Context(), g.PublicID, AlertFilter{Limit: 10}); err == nil {
		t.Error("bad annotations")
	}
}

// TestTimeline is C-09.FR-11 and FR-14: the Timeline newest first or oldest first, by page, filtered by kind, with
// the Notes and delivery events merged by time, each with its actor and Transport.
func TestTimeline(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	h.clock.Advance(time.Minute)
	b := h.alert(2, "critical", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, b)
	h.clock.Advance(time.Minute)
	h.resolve(t, a, b)
	h.db.entries[len(h.db.entries)-1].ActorKind = "user"
	h.db.entries[len(h.db.entries)-1].ActorUserID = i8(9)
	h.db.entries[len(h.db.entries)-1].OwnerUserID = i8(9)
	h.db.entries[len(h.db.entries)-1].PreviousOwnerUserID = i8(10)
	h.db.users[9] = dbgen.ListUserRefsRow{ID: 9, PublicID: "SRAAAAAAAAAAA9", Name: "Alice", Login: "alice",
		Status: "active"}
	h.db.notes = []dbgen.ListTimelineNotesRow{{ID: 1, PublicID: "NEAAAAAAAAAAAA", CreatedAt: t0.Add(30 * time.Second),
		Body: "looking", ActorKind: "service_account", ActorServiceAccountID: i8(4), Transport: "api"}}
	h.db.accounts[4] = dbgen.ListServiceAccountRefsRow{ID: 4, PublicID: "SAAAAAAAAAAAAA", Name: "bot", Status: "active"}
	h.db.delivery = []dbgen.ListTimelineDeliveryEventsRow{{ID: 1, PublicID: "DEAAAAAAAAAAAA",
		OccurredAt: t0.Add(90 * time.Second), Kind: "publication", Loudness: "loud",
		DestinationPublicID: "DSAAAAAAAAAAAA", DestinationName: "ops"}}
	read := func(f TimelineFilter) []TimelineEntry {
		t.Helper()
		var out []TimelineEntry
		for {
			page, err := h.svc.Timeline(t.Context(), g.PublicID, f)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, page.Entries...)
			if page.Next == nil {
				return out
			}
			f.After = page.Next
		}
	}
	names := func(es []TimelineEntry) string {
		var out []string
		for _, e := range es {
			out = append(out, cmp.Or(e.Event, string(e.Kind)))
		}
		return strings.Join(out, ",")
	}
	desc := read(TimelineFilter{Limit: 2})
	if got := names(desc); got != "resolved,delivery,urgency_raised,alerts_added,note_added,created" {
		t.Errorf("newest first: %s", got)
	}
	asc := read(TimelineFilter{Limit: 4, Ascending: true})
	if got := names(asc); got != "created,note_added,alerts_added,urgency_raised,delivery,resolved" {
		t.Errorf("oldest first: %s", got)
	}
	if got := names(read(TimelineFilter{Limit: 10, Kinds: []Kind{KindNotes, KindStatus}})); got !=
		"resolved,urgency_raised,note_added,created" {
		t.Errorf("notes and status: %s", got)
	}
	if got := names(read(TimelineFilter{Limit: 10, Kinds: []Kind{KindDelivery}})); got != "delivery" {
		t.Errorf("delivery: %s", got)
	}
	r := desc[0]
	if r.Actor.Ref == nil || r.Actor.Ref.Name != "Alice" || r.Owner == nil || r.Owner.Login != "alice" ||
		r.PreviousOwner == nil || !r.PreviousOwner.Deactivated || r.Mentions == nil || r.Reason == nil {
		t.Errorf("resolved %+v", r)
	}
	n := desc[4]
	if n.Note == nil || n.Note.Author == nil || n.Note.Author.Name != "bot" || n.Actor.Ref.Name != "bot" ||
		n.Loudness != Quiet || len(n.Mentions) != 0 {
		t.Errorf("note %+v", n)
	}
	if d := desc[1]; d.Delivery == nil || d.Delivery.Destination.Name != "ops" || d.Loudness != Loud ||
		d.Mentions == nil {
		t.Errorf("delivery %+v", d)
	}
	if _, err := h.svc.Timeline(t.Context(), "AGZZZZZZZZZZZZ", TimelineFilter{Limit: 1}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	for _, q := range []string{"GetGroupID", "ListTimelineDesc", "ListTimelineAsc", "ListTimelineNotes",
		"ListTimelineDeliveryEvents", "ListUserRefs"} {
		h.db.fail = map[string]error{q: errBoom}
		_, err1 := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 10})
		_, err2 := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 10, Ascending: true})
		if !errors.Is(err1, errBoom) && !errors.Is(err2, errBoom) {
			t.Errorf("%s failing = %v %v", q, err1, err2)
		}
	}
}

// TestExportGauges is muster_alert_groups: a series per status of every Route that is not deleted, the open Alert
// Groups counted; the series of a deleted Route without open ones go.
func TestExportGauges(t *testing.T) {
	h := newHarness(t)
	h.changes(t, ingest.ChangeFired, h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"}))
	if err := h.svc.ExportGauges(t.Context()); err != nil {
		t.Fatal(err)
	}
	scrape := metricsText(t)
	for _, want := range []string{`muster_alert_groups{route="RTDBAAAAAAAAAA",status="firing"} 1`,
		`muster_alert_groups{route="RTNETAAAAAAAAA",status="snoozed"} 0`} {
		if !strings.Contains(scrape, want) {
			t.Errorf("no %s in %s", want, scrape)
		}
	}
	h.db.routes[3].deleted = true
	if err := h.svc.ExportGauges(t.Context()); err != nil || strings.Contains(metricsText(t), "RTNETAAAAAAAAA") {
		t.Errorf("a deleted route kept its series: %v", err)
	}
	for _, q := range []string{"ListRoutePublicIDs", "CountOpenGroupsByRoute"} {
		h.db.fail = map[string]error{q: errBoom}
		if err := h.svc.ExportGauges(t.Context()); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", q, err)
		}
	}
}
