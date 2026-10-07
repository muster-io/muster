// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/routing/dbgen"
	"github.com/muster-io/muster/internal/templates"
)

const orgID = 7

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

var by = Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportUI}

var errUnique = &pgconn.PgError{Code: "23505", ConstraintName: "routes_name_key"}

var errBoom = errors.New("boom")

type routeRow struct {
	dbgen.ListRoutesRow
	deleted bool
}

// fakeStore is the database of the package in memory: the Routes, their Matchers, the version of the list and the
// Alerts that routing reads and writes. InTx rolls back what a failed transaction changed.
type fakeStore struct {
	mu       sync.Mutex
	rows     []*routeRow
	matchers map[int64][]dbgen.ListRouteMatchersRow
	order    int64
	audit    []auditdb.InsertAuditEntryParams
	hints    []db.Hint
	fail     map[string]error
	nextID   int64
	calls    map[string]int

	// The Organization's Severity level settings, the labels of the Alerts by id and what routing recorded.
	severityLabel string
	mapping       []byte
	alerts        map[int64][]byte
	routed        map[int64]dbgen.SetAlertRoutesParams

	// The Integrations with a Heartbeat, and the Route suggestions each User dismissed.
	heartbeats []dbgen.ListHeartbeatIntegrationsRow
	dismissals map[int64][]string

	// open are the open Alert Groups of each Route by id.
	open map[int64]int64

	// dests are the Destinations by public_id, deleted the deleted ones, and links the Destinations of each Route with
	// when they were added.
	dests   map[string]int64
	deleted map[string]bool
	links   map[int64][]link

	// membershipLocks are the Routes whose membership lock was taken, in order.
	membershipLocks []int64
}

// link is a row of route_destinations.
type link struct {
	dest  int64
	added time.Time
}

func newStore() *fakeStore {
	return &fakeStore{matchers: map[int64][]dbgen.ListRouteMatchersRow{}, order: 1, fail: map[string]error{},
		calls: map[string]int{}, severityLabel: "severity",
		mapping: []byte(`[{"value":"critical","level":"critical"},{"value":"warning","level":"warning"},` +
			`{"value":"info","level":"info"},{"value":"none","level":"info"}]`),
		alerts: map[int64][]byte{}, routed: map[int64]dbgen.SetAlertRoutesParams{}, dismissals: map[int64][]string{},
		open: map[int64]int64{}, dests: map[string]int64{}, deleted: map[string]bool{}, links: map[int64][]link{}}
}

func (s *fakeStore) call(name string) error {
	s.calls[name]++
	return s.fail[name]
}

func (s *fakeStore) find(publicID string) *routeRow {
	for _, r := range s.rows {
		if r.PublicID == publicID && !r.deleted {
			return r
		}
	}
	return nil
}

func (s *fakeStore) live() []*routeRow {
	var out []*routeRow
	for _, r := range s.rows {
		if !r.deleted {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b *routeRow) int {
		if a.IsDefault != b.IsDefault {
			if a.IsDefault {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})
	return out
}

func (s *fakeStore) nameTaken(name string, except int64) bool {
	return slices.ContainsFunc(s.rows, func(r *routeRow) bool { return !r.deleted && r.Name == name && r.ID != except })
}

func (s *fakeStore) ListRoutes(_ context.Context, org int64) ([]dbgen.ListRoutesRow, error) {
	if err := s.call("ListRoutes"); err != nil || org != orgID {
		return nil, err
	}
	var out []dbgen.ListRoutesRow
	for _, r := range s.live() {
		out = append(out, r.ListRoutesRow)
	}
	return out, nil
}

func (s *fakeStore) GetRoute(_ context.Context, arg dbgen.GetRouteParams) (dbgen.GetRouteRow, error) {
	if err := s.call("GetRoute"); err != nil {
		return dbgen.GetRouteRow{}, err
	}
	for i, r := range s.live() {
		if r.PublicID == arg.PublicID && arg.OrgID == orgID {
			return getRowOf(r.ListRoutesRow, int64(i)), nil
		}
	}
	return dbgen.GetRouteRow{}, pgx.ErrNoRows
}

func (s *fakeStore) LockRoute(_ context.Context, arg dbgen.LockRouteParams) (int64, error) {
	if err := s.call("LockRoute"); err != nil {
		return 0, err
	}
	if r := s.find(arg.PublicID); r != nil && arg.OrgID == orgID {
		return r.ID, nil
	}
	return 0, pgx.ErrNoRows
}

func (s *fakeStore) ListRouteMatchers(_ context.Context, arg dbgen.ListRouteMatchersParams) (
	[]dbgen.ListRouteMatchersRow, error) {
	if err := s.call("ListRouteMatchers"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRouteMatchersRow
	for _, id := range slices.Sorted(slices.Values(arg.RouteIds)) {
		out = append(out, s.matchers[id]...)
	}
	return out, nil
}

func (s *fakeStore) BumpRouteOrder(_ context.Context, arg dbgen.BumpRouteOrderParams) (int64, error) {
	if err := s.call("BumpRouteOrder"); err != nil {
		return 0, err
	}
	if arg.Expected.Valid && arg.Expected.Int64 != s.order {
		return 0, pgx.ErrNoRows
	}
	s.order++
	return s.order, nil
}

func (s *fakeStore) GetRouteOrderVersion(context.Context, int64) (int64, error) {
	if err := s.call("GetRouteOrderVersion"); err != nil {
		return 0, err
	}
	return s.order, nil
}

func (s *fakeStore) InsertRoute(_ context.Context, arg dbgen.InsertRouteParams) (int64, error) {
	if err := s.call("InsertRoute"); err != nil {
		return 0, err
	}
	if s.nameTaken(arg.Name, 0) {
		return 0, errUnique
	}
	var positions []int64
	for _, r := range s.live() {
		if !r.IsDefault {
			positions = append(positions, r.Position)
		}
	}
	position := int64(0)
	switch {
	case len(positions) == 0:
	case arg.First:
		position = slices.Min(positions) - 1
	default:
		position = slices.Max(positions) + 1
	}
	s.nextID++
	s.rows = append(s.rows, &routeRow{ListRoutesRow: dbgen.ListRoutesRow{
		ID: s.nextID, PublicID: arg.PublicID, Name: arg.Name, Description: arg.Description, Position: position,
		Urgent: arg.Urgent, GroupKey: arg.GroupKey, ReopenWindowSeconds: arg.ReopenWindowSeconds,
		GracePeriodSeconds: arg.GracePeriodSeconds, UrgentRiseRemovesAck: arg.UrgentRiseRemovesAck,
		SnoozeDurationsSeconds: arg.SnoozeDurationsSeconds, ThreadBatchingWindowSeconds: arg.ThreadBatchingWindowSeconds,
		StormThreshold: arg.StormThreshold, Language: arg.Language, TemplateRootMessage: arg.TemplateRootMessage,
		TemplateLine: arg.TemplateLine, TemplateAckTimeoutNotice: arg.TemplateAckTimeoutNotice,
		AckTimeoutEnabled: arg.AckTimeoutEnabled, AckTimeoutFirstIntervalSeconds: arg.AckTimeoutFirstIntervalSeconds,
		RemindersEnabled: arg.RemindersEnabled, RemindersFirstIntervalSeconds: arg.RemindersFirstIntervalSeconds,
		RemindersCapSeconds: arg.RemindersCapSeconds, AutoUnacknowledge: arg.AutoUnacknowledge, CreatedAt: arg.Now,
		Version: 1,
	}})
	return s.nextID, nil
}

func (s *fakeStore) EnsureDefaultRoute(_ context.Context, arg dbgen.EnsureDefaultRouteParams) (string, error) {
	if err := s.call("EnsureDefaultRoute"); err != nil {
		return "", err
	}
	if slices.ContainsFunc(s.rows, func(r *routeRow) bool { return r.IsDefault }) {
		return "", pgx.ErrNoRows
	}
	if s.nameTaken(arg.Name, 0) {
		return "", errUnique
	}
	s.nextID++
	s.rows = append(s.rows, &routeRow{ListRoutesRow: dbgen.ListRoutesRow{
		ID: s.nextID, PublicID: arg.PublicID, Name: arg.Name, Description: arg.Description, IsDefault: true,
		Urgent: arg.Urgent, GroupKey: arg.GroupKey, ReopenWindowSeconds: arg.ReopenWindowSeconds,
		GracePeriodSeconds: arg.GracePeriodSeconds, UrgentRiseRemovesAck: arg.UrgentRiseRemovesAck,
		SnoozeDurationsSeconds: arg.SnoozeDurationsSeconds, ThreadBatchingWindowSeconds: arg.ThreadBatchingWindowSeconds,
		StormThreshold: arg.StormThreshold, Language: arg.Language, AckTimeoutEnabled: arg.AckTimeoutEnabled,
		AckTimeoutFirstIntervalSeconds: arg.AckTimeoutFirstIntervalSeconds, RemindersEnabled: arg.RemindersEnabled,
		RemindersFirstIntervalSeconds: arg.RemindersFirstIntervalSeconds, RemindersCapSeconds: arg.RemindersCapSeconds,
		AutoUnacknowledge: arg.AutoUnacknowledge, CreatedAt: arg.Now, Version: 1,
	}})
	return arg.PublicID, nil
}

func (s *fakeStore) UpdateRoute(_ context.Context, arg dbgen.UpdateRouteParams) error {
	if err := s.call("UpdateRoute"); err != nil {
		return err
	}
	if s.nameTaken(arg.Name, arg.ID) {
		return errUnique
	}
	for _, r := range s.rows {
		if r.ID == arg.ID && arg.OrgID == orgID {
			r.Name, r.Description, r.Urgent, r.GroupKey = arg.Name, arg.Description, arg.Urgent, arg.GroupKey
			r.ReopenWindowSeconds, r.GracePeriodSeconds = arg.ReopenWindowSeconds, arg.GracePeriodSeconds
			r.UrgentRiseRemovesAck, r.SnoozeDurationsSeconds = arg.UrgentRiseRemovesAck, arg.SnoozeDurationsSeconds
			r.ThreadBatchingWindowSeconds, r.StormThreshold = arg.ThreadBatchingWindowSeconds, arg.StormThreshold
			r.Language, r.TemplateRootMessage, r.TemplateLine = arg.Language, arg.TemplateRootMessage, arg.TemplateLine
			r.TemplateAckTimeoutNotice, r.AckTimeoutEnabled = arg.TemplateAckTimeoutNotice, arg.AckTimeoutEnabled
			r.AckTimeoutFirstIntervalSeconds, r.RemindersEnabled = arg.AckTimeoutFirstIntervalSeconds, arg.RemindersEnabled
			r.RemindersFirstIntervalSeconds, r.RemindersCapSeconds = arg.RemindersFirstIntervalSeconds,
				arg.RemindersCapSeconds
			r.AutoUnacknowledge = arg.AutoUnacknowledge
			r.Version++
		}
	}
	return nil
}

func (s *fakeStore) DeleteRouteMatchers(_ context.Context, arg dbgen.DeleteRouteMatchersParams) error {
	if err := s.call("DeleteRouteMatchers"); err != nil {
		return err
	}
	delete(s.matchers, arg.RouteID)
	return nil
}

func (s *fakeStore) InsertRouteMatcher(_ context.Context, arg dbgen.InsertRouteMatcherParams) error {
	if err := s.call("InsertRouteMatcher"); err != nil {
		return err
	}
	s.matchers[arg.RouteID] = append(s.matchers[arg.RouteID], dbgen.ListRouteMatchersRow{RouteID: arg.RouteID,
		Label: arg.Label, Op: arg.Op, Value: arg.Value})
	return nil
}

func (s *fakeStore) DeleteRoute(_ context.Context, arg dbgen.DeleteRouteParams) error {
	if err := s.call("DeleteRoute"); err != nil {
		return err
	}
	for _, r := range s.rows {
		if r.ID == arg.ID && !r.IsDefault {
			r.deleted = true
			r.Version++
		}
	}
	return nil
}

func (s *fakeStore) CountOpenAlertGroups(_ context.Context, arg dbgen.CountOpenAlertGroupsParams) (int64, error) {
	if err := s.call("CountOpenAlertGroups"); err != nil {
		return 0, err
	}
	return s.open[arg.RouteID], nil
}

func (s *fakeStore) ListOpenAlertGroupCounts(context.Context, int64) ([]dbgen.ListOpenAlertGroupCountsRow, error) {
	if err := s.call("ListOpenAlertGroupCounts"); err != nil {
		return nil, err
	}
	var out []dbgen.ListOpenAlertGroupCountsRow
	for id, n := range s.open {
		out = append(out, dbgen.ListOpenAlertGroupCountsRow{RouteID: id, Count: n})
	}
	return out, nil
}

func (s *fakeStore) SetRoutePositions(_ context.Context, arg dbgen.SetRoutePositionsParams) error {
	if err := s.call("SetRoutePositions"); err != nil {
		return err
	}
	for i, id := range arg.Ids {
		for _, r := range s.rows {
			if r.ID == id {
				r.Position = arg.Positions[i]
			}
		}
	}
	return nil
}

func (s *fakeStore) ListRouteInfo(context.Context, int64) ([]dbgen.ListRouteInfoRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call("ListRouteInfo"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRouteInfoRow
	for _, r := range s.live() {
		out = append(out, dbgen.ListRouteInfoRow{PublicID: r.PublicID, Name: r.Name})
	}
	return out, nil
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.call("InsertAuditEntry"); err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

func (s *fakeStore) Notify(_ context.Context, h db.Hint) error {
	if err := s.call("Notify"); err != nil {
		return err
	}
	s.hints = append(s.hints, h)
	return nil
}

// InTx runs f and undoes what it changed when it fails, as a rolled back transaction would.
func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	rows := make([]*routeRow, len(s.rows))
	for i, r := range s.rows {
		c := *r
		rows[i] = &c
	}
	ms, order, audits, hints, next := maps.Clone(s.matchers), s.order, len(s.audit), len(s.hints), s.nextID
	links := maps.Clone(s.links)
	if err := f(s); err != nil {
		s.rows, s.matchers, s.order, s.audit, s.hints, s.nextID = rows, ms, order, s.audit[:audits], s.hints[:hints], next
		s.links = links
		return err
	}
	return nil
}

func (s *fakeStore) ResolveDestinations(_ context.Context, arg dbgen.ResolveDestinationsParams) (
	[]dbgen.ResolveDestinationsRow, error) {
	if err := s.call("ResolveDestinations"); err != nil {
		return nil, err
	}
	var out []dbgen.ResolveDestinationsRow
	for _, id := range arg.PublicIds {
		if internal, ok := s.dests[id]; ok && !s.deleted[id] && arg.OrgID == orgID {
			out = append(out, dbgen.ResolveDestinationsRow{ID: internal, PublicID: id})
		}
	}
	return out, nil
}

func (s *fakeStore) publicOf(id int64) string {
	for p, internal := range s.dests {
		if internal == id {
			return p
		}
	}
	return ""
}

func (s *fakeStore) ListRouteDestinationIDs(_ context.Context, arg dbgen.ListRouteDestinationIDsParams) (
	[]dbgen.ListRouteDestinationIDsRow, error) {
	if err := s.call("ListRouteDestinationIDs"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRouteDestinationIDsRow
	for _, route := range slices.Sorted(slices.Values(arg.RouteIds)) {
		var rows []dbgen.ListRouteDestinationIDsRow
		for _, l := range s.links[route] {
			if p := s.publicOf(l.dest); !s.deleted[p] {
				rows = append(rows, dbgen.ListRouteDestinationIDsRow{RouteID: route, DestinationID: l.dest, PublicID: p})
			}
		}
		slices.SortFunc(rows, func(a, b dbgen.ListRouteDestinationIDsRow) int { return cmp.Compare(a.PublicID, b.PublicID) })
		out = append(out, rows...)
	}
	return out, nil
}

func (s *fakeStore) InsertRouteDestinations(_ context.Context, arg dbgen.InsertRouteDestinationsParams) error {
	if err := s.call("InsertRouteDestinations"); err != nil {
		return err
	}
	links := slices.Clone(s.links[arg.RouteID])
	for _, d := range arg.DestinationIds {
		links = append(links, link{dest: d, added: arg.Now})
	}
	s.links[arg.RouteID] = links
	return nil
}

func (s *fakeStore) DeleteRouteDestinations(_ context.Context, arg dbgen.DeleteRouteDestinationsParams) error {
	if err := s.call("DeleteRouteDestinations"); err != nil {
		return err
	}
	s.links[arg.RouteID] = slices.DeleteFunc(slices.Clone(s.links[arg.RouteID]), func(l link) bool {
		return slices.Contains(arg.DestinationIds, l.dest)
	})
	return nil
}

func (s *fakeStore) DB() dbgen.DBTX { return nil }

func (s *fakeStore) On(dbgen.DBTX) Queries { return s }

func (s *fakeStore) LockRouteMembership(_ context.Context, arg dbgen.LockRouteMembershipParams) error {
	if err := s.call("LockRouteMembership"); err != nil {
		return err
	}
	if arg.LockClass != db.RouteMembershipLockClass {
		return errors.New("not the membership lock")
	}
	s.membershipLocks = append(s.membershipLocks, arg.RouteID)
	return nil
}

func (s *fakeStore) BumpRoutesOfDestination(_ context.Context, arg dbgen.BumpRoutesOfDestinationParams) (
	[]dbgen.BumpRoutesOfDestinationRow, error) {
	if err := s.call("BumpRoutesOfDestination"); err != nil {
		return nil, err
	}
	var out []dbgen.BumpRoutesOfDestinationRow
	for _, r := range s.rows {
		if !r.deleted && slices.ContainsFunc(s.links[r.ID], func(l link) bool { return l.dest == arg.DestinationID }) {
			r.Version++
			out = append(out, dbgen.BumpRoutesOfDestinationRow{ID: r.ID, PublicID: r.PublicID})
		}
	}
	// The order of RETURNING is not guaranteed; the caller sorts.
	slices.Reverse(out)
	return out, nil
}

// getRowOf is a row of ListRoutes with its place in evaluation order, as GetRoute reads it.
func getRowOf(r dbgen.ListRoutesRow, place int64) dbgen.GetRouteRow {
	return dbgen.GetRouteRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Description: r.Description,
		Position: r.Position, IsDefault: r.IsDefault, Urgent: r.Urgent, GroupKey: r.GroupKey,
		ReopenWindowSeconds: r.ReopenWindowSeconds, GracePeriodSeconds: r.GracePeriodSeconds,
		UrgentRiseRemovesAck: r.UrgentRiseRemovesAck, SnoozeDurationsSeconds: r.SnoozeDurationsSeconds,
		ThreadBatchingWindowSeconds: r.ThreadBatchingWindowSeconds, StormThreshold: r.StormThreshold,
		Language: r.Language, TemplateRootMessage: r.TemplateRootMessage, TemplateLine: r.TemplateLine,
		TemplateAckTimeoutNotice: r.TemplateAckTimeoutNotice, AckTimeoutEnabled: r.AckTimeoutEnabled,
		AckTimeoutFirstIntervalSeconds: r.AckTimeoutFirstIntervalSeconds, RemindersEnabled: r.RemindersEnabled,
		RemindersFirstIntervalSeconds: r.RemindersFirstIntervalSeconds, RemindersCapSeconds: r.RemindersCapSeconds,
		AutoUnacknowledge: r.AutoUnacknowledge, CreatedAt: r.CreatedAt, Version: r.Version, Place: place,
		TemplateErrorSince: r.TemplateErrorSince, TemplateError: r.TemplateError,
		StormSince: r.StormSince, StormAlertGroupCount: r.StormAlertGroupCount}
}

func newService(t *testing.T) (*Service, *fakeStore, *clock.Manual) {
	t.Helper()
	store := newStore()
	c := clock.NewManual(t0)
	if err := EnsureDefault(t.Context(), store, orgID, t0); err != nil {
		t.Fatal(err)
	}
	logger := logging.New(nilWriter{}, logging.LevelInfo)
	return New(Config{OrgID: orgID, Store: store, Audit: audit.NewWriter(logger, c), Business: c, Real: c,
		Log: logger}), store, c
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }

// input is a Route named name with the Matchers, the On-call profile's Group key and policy.
func input(name string, ms ...Matcher) Input {
	p := onCall()
	return Input{Name: name, Matchers: ms, GroupKey: p.GroupKey, Policy: p.Policy}
}

func ptr[T any](v T) *T { return &v }

// infoSeries is the muster_route_info series of the Route id, as /metrics shows it.
func infoSeries(t *testing.T, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	var out []string
	for line := range strings.Lines(rec.Body.String()) {
		if strings.HasPrefix(line, `muster_route_info{route="`+id+`"`) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, "\n")
}

func fieldError(t *testing.T, err error) *FieldError {
	t.Helper()
	f, ok := errors.AsType[*FieldError](err)
	if !ok {
		t.Fatalf("err = %v, want a FieldError", err)
	}
	return f
}

func names(list List) string {
	var out []string
	for _, r := range list.Routes {
		out = append(out, r.Name)
	}
	return strings.Join(out, ",")
}

// TestEnsureDefault is C-08.FR-3: the start-up step creates "Default" once, without Matchers and with the On-call
// profile; it changes nothing when the Default route exists, and a Route that has its name stops the start.
func TestEnsureDefault(t *testing.T) {
	svc, store, _ := newService(t)
	if err := EnsureDefault(t.Context(), store, orgID, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Routes) != 1 {
		t.Fatalf("routes %+v", list.Routes)
	}
	d := list.Routes[0]
	on := onCall()
	if d.Name != DefaultName || !d.IsDefault || len(d.Matchers) != 0 || d.Position != 0 || d.Urgent ||
		!slices.Equal(d.GroupKey, on.GroupKey) || !d.CreatedAt.Equal(t0) || d.Version != 1 ||
		d.Policy.Language != LanguageEnglish || !slices.Equal(d.Policy.SnoozeDurationsSeconds, on.Policy.SnoozeDurationsSeconds) ||
		d.Policy.AckTimeout != on.Policy.AckTimeout || d.Policy.Reminders != on.Policy.Reminders ||
		d.Policy.Templates != (Templates{}) {
		t.Errorf("default %+v", d)
	}

	taken := newStore()
	taken.rows = append(taken.rows, &routeRow{ListRoutesRow: dbgen.ListRoutesRow{ID: 1, Name: DefaultName}})
	if err := EnsureDefault(t.Context(), taken, orgID, t0); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	broken := newStore()
	broken.fail["EnsureDefaultRoute"] = errBoom
	if err := EnsureDefault(t.Context(), broken, orgID, t0); !errors.Is(err, errBoom) {
		t.Errorf("failed = %v", err)
	}
}

// TestCreate is C-08.FR-1 and FR-4: a Route is stored with its name, description, Matchers, urgent mark, Group key
// and every policy field, before the Default route; the list ETag moves, the Audit log has the creation with its
// values, the live hint names it and muster_route_info exports it.
func TestCreate(t *testing.T) {
	svc, store, _ := newService(t)
	in := input("payments", Matcher{Label: "team", Op: "=", Value: "payments"},
		Matcher{Label: "pod", Op: "=~", Value: "api-.*"})
	in.Description, in.Urgent = ptr("Payments on call"), true
	in.Policy = informational().Policy
	created, err := svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "payments" || created.Description != "Payments on call" || !created.Urgent ||
		created.IsDefault || created.Position != 0 || created.Version != 1 || len(created.Matchers) != 2 ||
		created.Matchers[1] != (Matcher{Label: "pod", Op: "=~", Value: "api-.*"}) ||
		!slices.Equal(created.Policy.SnoozeDurationsSeconds, []int64{day, 3 * day, 7 * day}) ||
		created.Policy.AckTimeout.Enabled || created.Policy.Reminders.Enabled || !strings.HasPrefix(created.PublicID, "RT") {
		t.Errorf("created %+v", created)
	}
	list, err := svc.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if names(list) != "payments,Default" || list.Version != 2 || list.Routes[1].Position != 1 {
		t.Errorf("list %s version %d", names(list), list.Version)
	}
	e := store.audit[0]
	var diff []audit.Change
	if err := json.Unmarshal(e.Diff, &diff); err != nil {
		t.Fatal(err)
	}
	pointers := make([]string, len(diff))
	for i, c := range diff {
		pointers[i] = c.Pointer
	}
	if e.Action != ActionCreated || e.ResourceType.String != ResourceRoute || e.ResourcePublicID.String != created.PublicID ||
		e.ResourceName.String != "payments" || !slices.Contains(pointers, "/matchers") ||
		!slices.Contains(pointers, "/policy/snooze_durations_seconds") || slices.Contains(pointers, "/policy/templates/line") {
		t.Errorf("audit %+v %v", e, pointers)
	}
	if len(store.hints) != 1 || store.hints[0] != (db.Hint{OrgID: orgID, Type: Hint, ID: created.PublicID}) {
		t.Errorf("hints %+v", store.hints)
	}
	if got := infoSeries(t, created.PublicID); got != `muster_route_info{route="`+created.PublicID+`",name="payments"} 1` {
		t.Errorf("info %q", got)
	}

	second, err := svc.Create(t.Context(), by, input(" second "))
	if err != nil {
		t.Fatal(err)
	}
	if second.Name != "second" || second.Position != 1 || second.Description != "" || len(second.Matchers) != 0 {
		t.Errorf("second %+v", second)
	}
	if _, err := svc.Create(t.Context(), by, input("payments")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("taken = %v", err)
	}
	if list, _ := svc.List(t.Context()); names(list) != "payments,second,Default" || list.Version != 3 {
		t.Errorf("after a refused creation %s %d", names(list), list.Version)
	}
}

// TestCheck is C-08.FR-1, FR-2 and FR-4: what a Route is refused with, at the pointer of the field.
func TestCheck(t *testing.T) {
	svc, _, _ := newService(t)
	for name, c := range map[string]struct {
		change  func(*Input)
		pointer string
		code    string
	}{
		"empty name":   {func(in *Input) { in.Name = "  " }, "/name", CodeInvalidFormat},
		"long name":    {func(in *Input) { in.Name = strings.Repeat("é", 201) }, "/name", CodeTooLong},
		"empty label":  {func(in *Input) { in.Matchers = []Matcher{{Op: "=", Value: "x"}} }, "/matchers/0/label", CodeInvalidFormat},
		"bad operator": {func(in *Input) { in.Matchers = []Matcher{{Label: "a", Op: "==", Value: "x"}} }, "/matchers/0/op", CodeInvalidFormat},
		"invalid regex": {func(in *Input) {
			in.Matchers = []Matcher{{Label: "a", Op: "=", Value: "("}, {Label: "pod", Op: "!~", Value: "api-("}}
		}, "/matchers/1/value", CodeInvalidRegex},
		"empty key":     {func(in *Input) { in.GroupKey = []string{"alertname", ""} }, "/group_key/1", CodeInvalidFormat},
		"duplicate key": {func(in *Input) { in.GroupKey = []string{"alertname", "cluster", "alertname"} }, "/group_key/2", CodeDuplicate},
		"destination":   {func(in *Input) { in.DestinationIDs = []string{"DSAAAAAAAAAAAA"} }, "/destination_ids/0", CodeUnknownID},
		"reopen":        {func(in *Input) { in.Policy.ReopenWindowSeconds = -1 }, "/policy/reopen_window_seconds", CodeOutOfRange},
		"grace":         {func(in *Input) { in.Policy.GracePeriodSeconds = -1 }, "/policy/grace_period_seconds", CodeOutOfRange},
		"batching":      {func(in *Input) { in.Policy.ThreadBatchingWindowSeconds = -1 }, "/policy/thread_batching_window_seconds", CodeOutOfRange},
		"storm":         {func(in *Input) { in.Policy.StormThreshold = 0 }, "/policy/storm_threshold", CodeOutOfRange},
		"ack timeout":   {func(in *Input) { in.Policy.AckTimeout.FirstIntervalSeconds = 0 }, "/policy/ack_timeout/first_interval_seconds", CodeOutOfRange},
		"reminders":     {func(in *Input) { in.Policy.Reminders.FirstIntervalSeconds = 0 }, "/policy/reminders/first_interval_seconds", CodeOutOfRange},
		"snooze":        {func(in *Input) { in.Policy.SnoozeDurationsSeconds = []int64{60, 0} }, "/policy/snooze_durations_seconds/1", CodeOutOfRange},
		"cap":           {func(in *Input) { in.Policy.Reminders.CapSeconds = 60 }, "/policy/reminders/cap_seconds", CodeOutOfRange},
		"language":      {func(in *Input) { in.Policy.Language = "de" }, "/policy/language", CodeInvalidFormat},
	} {
		in := input("r")
		c.change(&in)
		_, err := svc.Create(t.Context(), by, in)
		if f := fieldError(t, err); f.Pointer != c.pointer || f.Code != c.code || f.Error() == "" {
			t.Errorf("%s: %+v", name, f)
		}
	}
	in := input("r", Matcher{Label: "pod", Op: "=~", Value: "api-("})
	f := fieldError(t, func() error { _, err := svc.Create(t.Context(), by, in); return err }())
	if f.Detail != "error parsing regexp: missing closing ): `api-(`" {
		t.Errorf("the regexp error names the anchored expression: %q", f.Detail)
	}
	valid := input("r", Matcher{Label: "a", Op: "!=", Value: ""}, Matcher{Label: "b", Op: "=~", Value: ".*"})
	valid.Policy.ReopenWindowSeconds, valid.Policy.Language, valid.Policy.SnoozeDurationsSeconds = 0, LanguageRussian, nil
	valid.Policy.Reminders.CapSeconds = valid.Policy.Reminders.FirstIntervalSeconds
	if _, err := svc.Create(t.Context(), by, valid); err != nil {
		t.Errorf("valid = %v", err)
	}
}

// TestTemplates is C-12.FR-2, FR-5 and C-08.FR-1: a template is dry-run on save and one that fails is refused at its
// pointer with the code and position of the sandbox; an update keeps a template it leaves out, resets one given as
// null and dry-runs only a template that changed, against the Route's own Stored Snapshots; saving a changed template
// clears the Route's template error; a Route reads its template error state. Without the hooks nothing is dry-run.
func TestTemplates(t *testing.T) {
	svc, store, _ := newService(t)
	in := input("r")
	in.Policy.Templates = Templates{RootMessage: ptr("{{ .AlertGroup.Title }}"), Line: ptr("{{ .Labels.pod }}")}
	if _, err := svc.Create(t.Context(), by, in); err != nil {
		t.Fatalf("without hooks = %v", err)
	}
	type check struct {
		route        int64
		kind, source string
		lang         string
	}
	var checks []check
	var saved [][]string
	svc.SetTemplates(TemplateHooks{
		Check: func(_ context.Context, routeID int64, kind, source, lang string) error {
			checks = append(checks, check{routeID, kind, source, lang})
			switch source {
			case "{{ env }}":
				return &templates.Error{Code: templates.CodeUnknownFunction, Line: 1, Column: 4, Detail: "env"}
			case "boom":
				return errBoom
			}
			return nil
		},
		Saved: func(_ context.Context, _ dbgen.DBTX, routeID int64, publicID string, kinds []string) error {
			saved = append(saved, kinds)
			if routeID == 0 || publicID == "" {
				t.Errorf("saved %d %q", routeID, publicID)
			}
			if slices.Contains(kinds, "ack_timeout_notice") {
				return errBoom
			}
			return nil
		}})
	in = input("t")
	in.Policy.Templates.RootMessage = ptr("{{ env }}")
	f := fieldError(t, func() error { _, err := svc.Create(t.Context(), by, in); return err }())
	if f.Pointer != "/policy/templates/root_message" || f.Code != "unknown_function" || f.Line != 1 || f.Column != 4 ||
		len(checks) != 1 || checks[0] != (check{0, "root_message", "{{ env }}", "en"}) {
		t.Errorf("refused %+v, checks %+v", f, checks)
	}
	in.Policy.Templates.RootMessage = ptr("boom")
	if _, err := svc.Create(t.Context(), by, in); !errors.Is(err, errBoom) {
		t.Errorf("a failed check = %v", err)
	}
	in.Policy.Templates = Templates{Line: ptr("{{ .Labels.pod }}"), AckTimeoutNotice: ptr("late")}
	rt, err := svc.Create(t.Context(), by, in)
	if err != nil || *rt.Policy.Templates.Line != "{{ .Labels.pod }}" || rt.Policy.Templates.RootMessage != nil {
		t.Fatalf("created %+v, %v", rt.Policy.Templates, err)
	}
	// Absent keeps, null resets, a changed template is dry-run with the Route's id; an unchanged one is not.
	checks, saved = nil, nil
	up := input("t")
	up.Policy.Templates = Templates{RootMessage: ptr("{{ .Status }}")}
	up.Keep = KeepTemplates{Line: true}
	if _, err := svc.Update(t.Context(), by, rt.PublicID, nil, up); !errors.Is(err, errBoom) {
		t.Fatalf("resetting the ack timeout notice = %v; Saved fails for it", err)
	}
	up.Keep.AckTimeoutNotice = true
	rt, err = svc.Update(t.Context(), by, rt.PublicID, nil, up)
	if err != nil || *rt.Policy.Templates.Line != "{{ .Labels.pod }}" || *rt.Policy.Templates.RootMessage !=
		"{{ .Status }}" || *rt.Policy.Templates.AckTimeoutNotice != "late" {
		t.Fatalf("updated %+v, %v", rt.Policy.Templates, err)
	}
	if len(checks) != 2 || checks[1] != (check{rt.ID, "root_message", "{{ .Status }}", "en"}) ||
		!slices.Equal(saved[len(saved)-1], []string{"root_message"}) {
		t.Errorf("checks %+v, saved %v", checks, saved)
	}
	up = input("t")
	up.Keep = KeepTemplates{RootMessage: true, AckTimeoutNotice: true}
	rt, err = svc.Update(t.Context(), by, rt.PublicID, nil, up)
	if err != nil || rt.Policy.Templates.Line != nil || *rt.Policy.Templates.RootMessage != "{{ .Status }}" ||
		len(checks) != 2 || !slices.Equal(saved[len(saved)-1], []string{"line"}) {
		t.Errorf("reset %+v, %v, checks %d, saved %v", rt.Policy.Templates, err, len(checks), saved)
	}
	up.Policy.Templates.Line = ptr("{{ env }}")
	up.Keep.Line = false
	f = fieldError(t, func() error { _, err := svc.Update(t.Context(), by, rt.PublicID, nil, up); return err }())
	if f.Pointer != "/policy/templates/line" || f.Line != 1 {
		t.Errorf("update refused %+v", f)
	}
	if _, err := svc.Update(t.Context(), by, "RTZZZZZZZZZZZZ", nil, up); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown route = %v", err)
	}
	// The template error state of the row.
	row := store.find(rt.PublicID)
	row.TemplateErrorSince = pgtype.Timestamptz{Time: t0, Valid: true}
	row.TemplateError = pgtype.Text{String: "root_message template failed: line 1: x", Valid: true}
	got, err := svc.Get(t.Context(), rt.PublicID)
	if err != nil || got.TemplateError == nil || got.TemplateError.Error != row.TemplateError.String ||
		!got.TemplateError.Since.Equal(t0) {
		t.Errorf("template error %+v, %v", got.TemplateError, err)
	}
	list, _ := svc.List(t.Context())
	if list.Routes[1].TemplateError == nil {
		t.Errorf("list %+v", list.Routes[1])
	}
	if changedTemplates(Templates{Line: ptr("a")}, Templates{Line: ptr("a")}) != nil ||
		!slices.Equal(changedTemplates(Templates{Line: ptr("a")}, Templates{Line: ptr("b")}), []string{"line"}) {
		t.Error("changed templates")
	}
}

// TestUpdate is C-08.FR-1, FR-8 and FR-10: an edit replaces the fields and the Matchers as a whole and needs the
// current version; an edit that changes nothing writes nothing; the Default route can be edited except for its
// Matchers.
func TestUpdate(t *testing.T) {
	svc, store, c := newService(t)
	created, err := svc.Create(t.Context(), by, input("a", Matcher{Label: "severity", Op: "=", Value: "critical"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(t.Context(), by, created.PublicID, ptr(int64(7)), input("a")); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	same := input("a", Matcher{Label: "severity", Op: "=", Value: "critical"})
	got, err := svc.Update(t.Context(), by, created.PublicID, ptr(int64(1)), same)
	if err != nil || got.Version != 1 || len(store.audit) != 1 {
		t.Errorf("no change = %+v %v, %d entries", got, err, len(store.audit))
	}
	c.Advance(time.Minute)
	in := input("a2", Matcher{Label: "team", Op: "!=", Value: "x"})
	in.Urgent, in.GroupKey, in.Description = true, []string{"alertname"}, ptr("now urgent")
	got, err = svc.Update(t.Context(), by, strings.ToLower(created.PublicID), ptr(int64(1)), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "a2" || !got.Urgent || got.Version != 2 || got.Description != "now urgent" ||
		len(got.Matchers) != 1 || got.Matchers[0].Label != "team" || !slices.Equal(got.GroupKey, []string{"alertname"}) {
		t.Errorf("updated %+v", got)
	}
	e := store.audit[len(store.audit)-1]
	var diff []audit.Change
	_ = json.Unmarshal(e.Diff, &diff)
	pointers := make([]string, len(diff))
	for i, ch := range diff {
		pointers[i] = ch.Pointer
	}
	if e.Action != ActionUpdated || !slices.Equal(pointers, []string{"/name", "/description", "/matchers", "/urgent",
		"/group_key"}) || store.hints[len(store.hints)-1].ID != created.PublicID {
		t.Errorf("audit %s %v", e.Action, pointers)
	}
	if infoSeries(t, created.PublicID) != `muster_route_info{route="`+created.PublicID+`",name="a2"} 1` {
		t.Errorf("info %q", infoSeries(t, created.PublicID))
	}
	in.Description = nil
	in.Matchers = []Matcher{{Label: "team", Op: "!=", Value: "x"}}
	in.Policy.StormThreshold = 50
	if got, err = svc.Update(t.Context(), by, created.PublicID, nil, in); err != nil || got.Description != "now urgent" ||
		got.Policy.StormThreshold != 50 || store.calls["DeleteRouteMatchers"] != 1 {
		t.Errorf("kept description = %+v %v, matcher rewrites %d", got, err, store.calls["DeleteRouteMatchers"])
	}

	list, _ := svc.List(t.Context())
	def := list.Routes[len(list.Routes)-1]
	dIn := input("Everything else", Matcher{Label: "a", Op: "=", Value: "b"})
	if f := fieldError(t, func() error {
		_, err := svc.Update(t.Context(), by, def.PublicID, nil, dIn)
		return err
	}()); f.Pointer != "/matchers" || f.Code != CodeUnsupported {
		t.Errorf("default matchers %+v", f)
	}
	dIn.Matchers = nil
	if got, err := svc.Update(t.Context(), by, def.PublicID, ptr(def.Version), dIn); err != nil || got.Name != "Everything else" ||
		!got.IsDefault {
		t.Errorf("default edit %+v %v", got, err)
	}
	if _, err := svc.Create(t.Context(), by, input("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(t.Context(), by, created.PublicID, nil, input("b")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("taken = %v", err)
	}
	for _, id := range []string{"RTAAAAAAAAAAAA", "not-an-id", "NTAAAAAAAAAAAA"} {
		if _, err := svc.Update(t.Context(), by, id, nil, input("x")); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s = %v", id, err)
		}
		if _, err := svc.Get(t.Context(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("get %s = %v", id, err)
		}
	}
	if _, err := svc.Update(t.Context(), by, created.PublicID, nil, Input{}); fieldError(t, err).Pointer != "/name" {
		t.Errorf("invalid = %v", err)
	}
}

// TestDelete is C-08.FR-9: a deleted Route leaves the list and the evaluation order at once and muster_route_info;
// the Default route cannot be deleted.
func TestDelete(t *testing.T) {
	svc, store, _ := newService(t)
	a, _ := svc.Create(t.Context(), by, input("a"))
	b, _ := svc.Create(t.Context(), by, input("b"))
	list, _ := svc.List(t.Context())
	def := list.Routes[2]
	if err := svc.Delete(t.Context(), by, def.PublicID, nil); !errors.Is(err, ErrDefaultImmutable) {
		t.Errorf("default = %v", err)
	}
	if err := svc.Delete(t.Context(), by, a.PublicID, ptr(int64(9))); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	if err := svc.Delete(t.Context(), by, "RTAAAAAAAAAAAA", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	// C-09.FR-19: a Route with open Alert Groups is refused with their count, counted after the lock, and shows it.
	store.open[a.ID] = 2
	var open *OpenAlertGroupsError
	if err := svc.Delete(t.Context(), by, a.PublicID, nil); !errors.As(err, &open) || open.Count != 2 ||
		err.Error() != "the route has 2 open alert groups" {
		t.Errorf("open alert groups = %v", err)
	}
	if store.calls["LockRoute"] == 0 {
		t.Error("counted without the lock")
	}
	if got, _ := svc.Get(t.Context(), a.PublicID); got.OpenAlertGroupCount != 2 {
		t.Errorf("get: %d open alert groups", got.OpenAlertGroupCount)
	}
	if got, _ := svc.List(t.Context()); got.Routes[0].OpenAlertGroupCount != 2 || got.Routes[1].OpenAlertGroupCount != 0 {
		t.Errorf("list: %+v", got.Routes)
	}
	delete(store.open, a.ID)
	if after, _ := svc.List(t.Context()); after.Version != list.Version {
		t.Errorf("refused deletions moved the version to %d", after.Version)
	}
	if err := svc.Delete(t.Context(), by, a.PublicID, ptr(int64(1))); err != nil {
		t.Fatal(err)
	}
	after, _ := svc.List(t.Context())
	if names(after) != "b,Default" || after.Version != list.Version+1 || after.Routes[0].Position != 0 {
		t.Errorf("after %s %d", names(after), after.Version)
	}
	e := store.audit[len(store.audit)-1]
	if e.Action != ActionDeleted || e.ResourcePublicID.String != a.PublicID ||
		!strings.Contains(string(e.Diff), `"pointer":"/deleted_at"`) {
		t.Errorf("audit %+v", e)
	}
	if infoSeries(t, a.PublicID) != "" || infoSeries(t, b.PublicID) == "" {
		t.Errorf("info %q %q", infoSeries(t, a.PublicID), infoSeries(t, b.PublicID))
	}
	if _, err := svc.Get(t.Context(), a.PublicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted = %v", err)
	}
	if _, err := svc.Create(t.Context(), by, input("a")); err != nil {
		t.Errorf("the name of a deleted route = %v", err)
	}
}

// TestReorder is C-08.FR-3 and FR-10: a reorder replaces the positions of every Route except the Default route under
// the list ETag; a stale ETag, a list naming the Default route and a different set change nothing.
func TestReorder(t *testing.T) {
	svc, store, _ := newService(t)
	a, _ := svc.Create(t.Context(), by, input("a"))
	b, _ := svc.Create(t.Context(), by, input("b"))
	c, _ := svc.Create(t.Context(), by, input("c"))
	list, _ := svc.List(t.Context())
	def := list.Routes[3]
	v := list.Version
	if _, err := svc.Reorder(t.Context(), by, ptr(v-1), []string{c.PublicID, b.PublicID, a.PublicID}); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	if _, err := svc.Reorder(t.Context(), by, ptr(v), []string{c.PublicID, b.PublicID, a.PublicID,
		def.PublicID}); !errors.Is(err, ErrDefaultImmutable) {
		t.Errorf("default = %v", err)
	}
	for name, ids := range map[string][]string{
		"missing":   {c.PublicID, b.PublicID},
		"duplicate": {c.PublicID, b.PublicID, b.PublicID},
		"unknown":   {c.PublicID, b.PublicID, "RTAAAAAAAAAAAA"},
		"malformed": {c.PublicID, b.PublicID, "x"},
		"extra":     {c.PublicID, b.PublicID, a.PublicID, "RTAAAAAAAAAAAA"},
	} {
		_, err := svc.Reorder(t.Context(), by, ptr(v), ids)
		if f := fieldError(t, err); f.Pointer != "/route_ids" || f.Code != CodeRouteSetMismatch {
			t.Errorf("%s: %+v", name, f)
		}
	}
	if now, _ := svc.List(t.Context()); now.Version != v || names(now) != "a,b,c,Default" {
		t.Errorf("refused reorders changed the list: %s %d", names(now), now.Version)
	}
	audits := len(store.audit)
	got, err := svc.Reorder(t.Context(), by, ptr(v), []string{c.PublicID, strings.ToLower(a.PublicID), b.PublicID})
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "c,a,b,Default" || got.Version != v+1 || got.Routes[1].Position != 1 || got.Routes[3].Position != 3 {
		t.Errorf("reordered %s %d", names(got), got.Version)
	}
	e := store.audit[audits]
	if e.Action != ActionReordered || e.ResourceType.String != ResourceRoute || e.ResourcePublicID.Valid ||
		string(e.Diff) != `[{"pointer":"/route_ids","before":["`+a.PublicID+`","`+b.PublicID+`","`+c.PublicID+
			`"],"after":["`+c.PublicID+`","`+a.PublicID+`","`+b.PublicID+`"]}]` {
		t.Errorf("audit %s %s", e.Action, e.Diff)
	}
	if h := store.hints[len(store.hints)-1]; h != (db.Hint{OrgID: orgID, Type: Hint}) {
		t.Errorf("hint %+v", h)
	}
	if got, err = svc.Reorder(t.Context(), by, nil, []string{c.PublicID, a.PublicID, b.PublicID}); err != nil ||
		got.Version != v+2 || store.audit[len(store.audit)-1].Diff == nil ||
		string(store.audit[len(store.audit)-1].Diff) != "[]" {
		t.Errorf("same order with If-Match * = %v %d %s", err, got.Version, store.audit[len(store.audit)-1].Diff)
	}
	ab, err := svc.Get(t.Context(), a.PublicID)
	if err != nil || ab.Position != 1 {
		t.Errorf("get after reorder %+v %v", ab, err)
	}
}

// TestFailures: a failing query fails the operation, which changes nothing.
func TestFailures(t *testing.T) {
	ops := map[string]func(*Service, *testing.T, Route) error{
		"list": func(svc *Service, t *testing.T, _ Route) error { _, err := svc.List(t.Context()); return err },
		"get": func(svc *Service, t *testing.T, r Route) error {
			_, err := svc.Get(t.Context(), r.PublicID)
			return err
		},
		"create": func(svc *Service, t *testing.T, _ Route) error {
			_, err := svc.Create(t.Context(), by, input("new", Matcher{Label: "a", Op: "=", Value: "b"}))
			return err
		},
		"update": func(svc *Service, t *testing.T, r Route) error {
			_, err := svc.Update(t.Context(), by, r.PublicID, nil, input("renamed", Matcher{Label: "c", Op: "=", Value: "d"}))
			return err
		},
		"delete": func(svc *Service, t *testing.T, r Route) error { return svc.Delete(t.Context(), by, r.PublicID, nil) },
		"reorder": func(svc *Service, t *testing.T, r Route) error {
			_, err := svc.Reorder(t.Context(), by, nil, []string{r.PublicID})
			return err
		},
	}
	queries := []string{"GetRouteOrderVersion", "ListRoutes", "ListRouteMatchers", "GetRoute", "LockRoute",
		"BumpRouteOrder", "InsertRoute", "InsertRouteMatcher", "UpdateRoute", "DeleteRouteMatchers", "DeleteRoute",
		"SetRoutePositions", "InsertAuditEntry", "Notify", "CountOpenAlertGroups", "ListOpenAlertGroupCounts"}
	for name, op := range ops {
		for _, query := range queries {
			svc, store, _ := newService(t)
			r, err := svc.Create(t.Context(), by, input("r", Matcher{Label: "x", Op: "=", Value: "y"}))
			if err != nil {
				t.Fatal(err)
			}
			before := store.order
			store.calls = map[string]int{}
			store.fail[query] = errBoom
			err = op(svc, t, r)
			if store.calls[query] > 0 && !errors.Is(err, errBoom) {
				t.Errorf("%s with %s failing = %v", name, query, err)
			}
			if err != nil && store.order != before {
				t.Errorf("%s with %s failing moved the version", name, query)
			}
		}
	}
}

// The Destinations of the fake: two that exist and one deleted.
const (
	destA    = "DSAAAAAAAAAAA1"
	destB    = "DSAAAAAAAAAAA2"
	destGone = "DSAAAAAAAAAAA3"
)

// membershipCall is a call of the membership hook.
type membershipCall struct {
	route          int64
	added, removed []int64
}

func withDestinations(t *testing.T) (*Service, *fakeStore, *clock.Manual, *[]membershipCall) {
	t.Helper()
	svc, store, c := newService(t)
	store.dests[destA], store.dests[destB], store.dests[destGone] = 51, 52, 53
	store.deleted[destGone] = true
	var calls []membershipCall
	svc.SetMembership(func(_ context.Context, tx dbgen.DBTX, routeID int64, added, removed []int64) error {
		if tx != nil {
			t.Error("the hook runs outside the transaction")
		}
		calls = append(calls, membershipCall{route: routeID, added: added, removed: removed})
		return store.fail["membership"]
	})
	return svc, store, c, &calls
}

// TestRouteDestinations is C-08.FR-1 and C-11.FR-14: createRoute and updateRoute write destination_ids in the Route's
// transaction — added, kept with their added_at, or removed — and run delivery's membership hook with the changes; an
// edit that changes only destination_ids is saved with a new version and records route.updated with /destination_ids
// in its diff; an id that names no Destination, a deleted one or a malformed one is a 422 unknown_id at its pointer.
func TestRouteDestinations(t *testing.T) {
	svc, store, c, calls := withDestinations(t)
	in := input("r")
	in.DestinationIDs = []string{destB, strings.ToLower(destA), destB}
	created, err := svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(created.DestinationIDs, []string{destA, destB}) || len(*calls) != 1 ||
		!slices.Equal((*calls)[0].added, []int64{51, 52}) || (*calls)[0].removed != nil ||
		(*calls)[0].route != created.ID {
		t.Fatalf("created %+v, hook %+v", created.DestinationIDs, *calls)
	}
	var diff []audit.Change
	_ = json.Unmarshal(store.audit[len(store.audit)-1].Diff, &diff)
	if !slices.ContainsFunc(diff, func(ch audit.Change) bool { return ch.Pointer == "/destination_ids" }) {
		t.Errorf("creation diff %+v", diff)
	}
	firstAdded := store.links[created.ID][0].added

	c.Advance(time.Minute)
	in.DestinationIDs = []string{destA}
	got, err := svc.Update(t.Context(), by, created.PublicID, ptr(created.Version), in)
	if err != nil {
		t.Fatal(err)
	}
	e := store.audit[len(store.audit)-1]
	diff = nil
	_ = json.Unmarshal(e.Diff, &diff)
	if got.Version != created.Version+1 || !slices.Equal(got.DestinationIDs, []string{destA}) ||
		e.Action != ActionUpdated || len(diff) != 1 || diff[0].Pointer != "/destination_ids" ||
		store.hints[len(store.hints)-1].ID != created.PublicID {
		t.Errorf("updated %+v, audit %s %+v", got, e.Action, diff)
	}
	if len(*calls) != 2 || (*calls)[1].added != nil || !slices.Equal((*calls)[1].removed, []int64{52}) {
		t.Errorf("hook %+v", *calls)
	}
	// Each change of the Destinations takes the Route's membership lock, which an Enqueue on the Route takes shared.
	if !slices.Equal(store.membershipLocks, []int64{created.ID, created.ID}) {
		t.Errorf("membership locks %v", store.membershipLocks)
	}
	if l := store.links[created.ID]; len(l) != 1 || l[0].dest != 51 || !l[0].added.Equal(firstAdded) {
		t.Errorf("kept destination %+v", l)
	}
	in.DestinationIDs = []string{destB, destA}
	if got, err = svc.Update(t.Context(), by, created.PublicID, nil, in); err != nil ||
		!slices.Equal((*calls)[2].added, []int64{52}) {
		t.Errorf("added again %+v %v %+v", got, err, *calls)
	}
	if got, err = svc.Update(t.Context(), by, created.PublicID, nil, in); err != nil || len(*calls) != 3 {
		t.Errorf("no change = %+v %v, %d calls", got, err, len(*calls))
	}

	for name, ids := range map[string][]string{"unknown": {destA, "DSZZZZZZZZZZZZ"}, "deleted": {destA, destGone},
		"malformed": {destA, "nope"}} {
		in.DestinationIDs = ids
		if f := fieldError(t, func() error {
			_, err := svc.Update(t.Context(), by, created.PublicID, nil, in)
			return err
		}()); f.Pointer != "/destination_ids/1" || f.Code != CodeUnknownID {
			t.Errorf("%s update: %+v", name, f)
		}
		cIn := input("other-" + name)
		cIn.DestinationIDs = ids
		if f := fieldError(t, func() error { _, err := svc.Create(t.Context(), by, cIn); return err }()); f.Pointer !=
			"/destination_ids/1" || f.Code != CodeUnknownID {
			t.Errorf("%s create: %+v", name, f)
		}
	}

	// A failing hook rolls the change back; so does a failing write.
	in.DestinationIDs = nil
	store.fail["membership"] = errBoom
	if _, err := svc.Update(t.Context(), by, created.PublicID, nil, in); !errors.Is(err, errBoom) ||
		len(store.links[created.ID]) != 2 {
		t.Errorf("failed hook = %v, links %+v", err, store.links[created.ID])
	}
	delete(store.fail, "membership")
	for _, q := range []string{"ResolveDestinations", "LockRouteMembership", "ListRouteDestinationIDs",
		"DeleteRouteDestinations", "InsertRouteDestinations"} {
		store.fail[q] = errBoom
		in.DestinationIDs = []string{destA}
		if q == "InsertRouteDestinations" {
			in.DestinationIDs = []string{destA, destB}
			store.links[created.ID] = nil
		}
		if _, err := svc.Update(t.Context(), by, created.PublicID, nil, in); !errors.Is(err, errBoom) {
			t.Errorf("%s = %v", q, err)
		}
		delete(store.fail, q)
	}
	svc.SetMembership(nil)
	in.DestinationIDs = []string{destB}
	if got, err := svc.Update(t.Context(), by, created.PublicID, nil, in); err != nil ||
		!slices.Equal(got.DestinationIDs, []string{destB}) {
		t.Errorf("without a hook = %+v %v", got, err)
	}
}

// TestDestinationDeleted is C-11.FR-14 on the Routes: a Destination being deleted leaves each Route it belongs to, so
// each Route that is not deleted gets a new version and its hint, and its membership lock is taken in id order; a
// Route without it is untouched.
func TestDestinationDeleted(t *testing.T) {
	svc, store, _, _ := withDestinations(t)
	var ids []int64
	for _, name := range []string{"one", "two", "three"} {
		in := input(name)
		in.DestinationIDs = []string{destA}
		if name == "three" {
			in.DestinationIDs = []string{destB}
		}
		r, err := svc.Create(t.Context(), by, in)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	versions := map[int64]int64{}
	for _, r := range store.rows {
		versions[r.ID] = r.Version
	}
	store.membershipLocks, store.hints = nil, nil
	if err := svc.DestinationDeleted(t.Context(), nil, 51); err != nil {
		t.Fatal(err)
	}
	for _, r := range store.rows {
		want := versions[r.ID]
		if r.ID == ids[0] || r.ID == ids[1] {
			want++
		}
		if r.Version != want {
			t.Errorf("route %s version %d, want %d", r.Name, r.Version, want)
		}
	}
	if !slices.Equal(store.membershipLocks, ids[:2]) || len(store.hints) != 2 ||
		store.hints[0] != (db.Hint{OrgID: orgID, Type: Hint, ID: store.rows[1].PublicID}) {
		t.Errorf("locks %v, hints %+v", store.membershipLocks, store.hints)
	}
	for _, q := range []string{"BumpRoutesOfDestination", "LockRouteMembership", "Notify"} {
		store.fail[q] = errBoom
		if err := svc.DestinationDeleted(t.Context(), nil, 51); !errors.Is(err, errBoom) {
			t.Errorf("%s = %v", q, err)
		}
		delete(store.fail, q)
	}
}

// TestRouteStorm is C-11.FR-6 on the Route: its active Storm, since when and how many new Alert Groups it counted,
// read with the Route and the list; none when no Storm is active.
func TestRouteStorm(t *testing.T) {
	svc, store, _ := newService(t)
	created, err := svc.Create(t.Context(), by, input("r"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Storm != nil {
		t.Errorf("storm %+v", created.Storm)
	}
	r := store.find(created.PublicID)
	r.StormSince = pgtype.Timestamptz{Time: t0, Valid: true}
	r.StormAlertGroupCount = pgtype.Int8{Int64: 12, Valid: true}
	got, err := svc.Get(t.Context(), created.PublicID)
	if err != nil || got.Storm == nil || !got.Storm.Since.Equal(t0) || got.Storm.AlertGroupCount != 12 {
		t.Errorf("get %+v %v", got.Storm, err)
	}
	list, err := svc.List(t.Context())
	if err != nil || list.Routes[0].Storm == nil || list.Routes[1].Storm != nil {
		t.Errorf("list %+v %v", list.Routes, err)
	}
}

// TestProfiles is C-08.FR-7: On-call and Informational with the values of defaults.md.
func TestProfiles(t *testing.T) {
	ps := Profiles()
	if len(ps) != 2 || ps[0].ID != ProfileOnCall || ps[0].Name != "On-call" || ps[1].ID != ProfileInformational ||
		ps[1].Name != "Informational" {
		t.Fatalf("profiles %+v", ps)
	}
	for _, p := range ps {
		pol := p.Policy
		if p.Urgent || !slices.Equal(p.GroupKey, []string{"alertname", "severity", "cluster"}) ||
			pol.ReopenWindowSeconds != 900 || pol.GracePeriodSeconds != 900 || !pol.UrgentRiseRemovesAck ||
			pol.ThreadBatchingWindowSeconds != 60 || pol.StormThreshold != 20 || pol.Language != "en" ||
			pol.Templates != (Templates{}) || pol.AckTimeout.FirstIntervalSeconds != 900 ||
			pol.Reminders.FirstIntervalSeconds != 4*3600 || pol.Reminders.CapSeconds != 24*3600 || pol.AutoUnacknowledge {
			t.Errorf("%s %+v", p.ID, p)
		}
		if err := checkPolicy(pol); err != nil {
			t.Errorf("%s does not pass its own checks: %v", p.ID, err)
		}
	}
	if !slices.Equal(ps[0].Policy.SnoozeDurationsSeconds, []int64{3600, 14400, 86400}) || !ps[0].Policy.AckTimeout.Enabled ||
		!ps[0].Policy.Reminders.Enabled {
		t.Errorf("on-call %+v", ps[0].Policy)
	}
	if !slices.Equal(ps[1].Policy.SnoozeDurationsSeconds, []int64{86400, 259200, 604800}) ||
		ps[1].Policy.AckTimeout.Enabled || ps[1].Policy.Reminders.Enabled {
		t.Errorf("informational %+v", ps[1].Policy)
	}
	ps[0].Policy.SnoozeDurationsSeconds[0] = 1
	if Profiles()[0].Policy.SnoozeDurationsSeconds[0] != 3600 {
		t.Error("a caller changed the built-in profile")
	}
}

// TestRefreshInfo is C-08.FR-12: muster_route_info lists exactly the Routes that are not deleted in the database, the
// Default route included, also those another replica changed; RunInfo refreshes at once, on InfoChanged and per tick.
func TestRefreshInfo(t *testing.T) {
	svc, store, _ := newService(t)
	a, _ := svc.Create(t.Context(), by, input("a"))
	list, _ := svc.List(t.Context())
	def := list.Routes[1]
	// Another replica deletes a and creates b.
	store.mu.Lock()
	store.find(a.PublicID).deleted = true
	b := Route{PublicID: "RTBBBBBBBBBBBB"}
	if _, err := store.InsertRoute(t.Context(), dbgen.InsertRouteParams{PublicID: b.PublicID, Name: "b2"}); err != nil {
		t.Fatal(err)
	}
	store.mu.Unlock()
	if err := svc.RefreshInfo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if infoSeries(t, a.PublicID) != "" || infoSeries(t, def.PublicID) == "" ||
		infoSeries(t, b.PublicID) != `muster_route_info{route="`+b.PublicID+`",name="b2"} 1` {
		t.Errorf("info a %q default %q b %q", infoSeries(t, a.PublicID), infoSeries(t, def.PublicID),
			infoSeries(t, b.PublicID))
	}
	store.mu.Lock()
	store.fail["ListRouteInfo"] = errBoom
	store.mu.Unlock()
	if err := svc.RefreshInfo(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("failed = %v", err)
	}
	store.mu.Lock()
	delete(store.fail, "ListRouteInfo")
	store.find(b.PublicID).Name = "b3"
	store.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		svc.RunInfo(ctx, ticks)
		close(done)
	}()
	svc.InfoChanged()
	svc.InfoChanged()
	ticks <- t0
	deadline := time.Now().Add(5 * time.Second)
	for infoSeries(t, b.PublicID) != `muster_route_info{route="`+b.PublicID+`",name="b3"} 1` {
		if time.Now().After(deadline) {
			t.Fatalf("info %q", infoSeries(t, b.PublicID))
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

// execRecorder is a connection that records the statements it runs; it answers nothing else.
type execRecorder struct {
	dbgen.DBTX
	args [][]any
	err  error
}

func (r *execRecorder) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	r.args = append(r.args, args)
	return pgconn.CommandTag{}, r.err
}

// TestRestampAlerts: grouping records the Default route on Alerts whose Route was deleted while their Snapshot waited
// for it, in the Snapshot's transaction.
func TestRestampAlerts(t *testing.T) {
	tx := &execRecorder{}
	if err := RestampAlerts(t.Context(), tx, orgID, []int64{3, 4}, 9); err != nil {
		t.Fatal(err)
	}
	if len(tx.args) != 1 || tx.args[0][1] != int64(orgID) || !slices.Equal(tx.args[0][2].([]int64), []int64{3, 4}) {
		t.Errorf("restamped %v", tx.args)
	}
	tx.err = errBoom
	if err := RestampAlerts(t.Context(), tx, orgID, []int64{3}, 9); !errors.Is(err, errBoom) {
		t.Errorf("a failed restamp = %v", err)
	}
}
