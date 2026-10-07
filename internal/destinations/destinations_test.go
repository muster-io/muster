// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/metrics"
)

var (
	t0      = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	errBoom = errors.New("boom")
)

// fakeStore is the destinations table in memory, filtered as the queries filter.
type fakeStore struct {
	rows   []dbgen.GetDestinationRow
	routes []dbgen.ListDestinationRoutesRow
	refs   []dbgen.ListRouteDestinationRefsRow
	info   []dbgen.ListDestinationInfoRow
	lists  []dbgen.ListDestinationsParams
	fail   map[string]error
}

func (f *fakeStore) ListDestinations(_ context.Context, arg dbgen.ListDestinationsParams) (
	[]dbgen.ListDestinationsRow, error) {
	f.lists = append(f.lists, arg)
	var out []dbgen.ListDestinationsRow
	for _, r := range f.rows {
		if arg.AfterID.Valid && r.ID <= arg.AfterID.Int64 || arg.Type.Valid && r.Type != arg.Type.String ||
			len(out) == int(arg.PageSize) {
			continue
		}
		out = append(out, dbgen.ListDestinationsRow(r))
	}
	return out, f.fail["ListDestinations"]
}

func (f *fakeStore) GetDestination(_ context.Context, arg dbgen.GetDestinationParams) (dbgen.GetDestinationRow,
	error) {
	if err := f.fail["GetDestination"]; err != nil {
		return dbgen.GetDestinationRow{}, err
	}
	for _, r := range f.rows {
		if r.PublicID == arg.PublicID {
			return r, nil
		}
	}
	return dbgen.GetDestinationRow{}, pgx.ErrNoRows
}

func (f *fakeStore) ListDestinationRoutes(_ context.Context, arg dbgen.ListDestinationRoutesParams) (
	[]dbgen.ListDestinationRoutesRow, error) {
	var out []dbgen.ListDestinationRoutesRow
	for _, r := range f.routes {
		if slices.Contains(arg.DestinationIds, r.DestinationID) {
			out = append(out, r)
		}
	}
	return out, f.fail["ListDestinationRoutes"]
}

func (f *fakeStore) ListRouteDestinationRefs(_ context.Context, arg dbgen.ListRouteDestinationRefsParams) (
	[]dbgen.ListRouteDestinationRefsRow, error) {
	var out []dbgen.ListRouteDestinationRefsRow
	for _, r := range f.refs {
		if slices.Contains(arg.RouteIds, r.RouteID) {
			out = append(out, r)
		}
	}
	return out, f.fail["ListRouteDestinationRefs"]
}

func (f *fakeStore) ListDestinationInfo(context.Context, int64) ([]dbgen.ListDestinationInfoRow, error) {
	return f.info, f.fail["ListDestinationInfo"]
}

func txt(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func newStore() *fakeStore {
	since := pgtype.Timestamptz{Time: t0.Add(-time.Hour), Valid: true}
	return &fakeStore{fail: map[string]error{}, rows: []dbgen.GetDestinationRow{
		{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: "mattermost", Name: "ops", ConnectionPublicID: txt("CNAAAAAAAAAAA1"),
			MattermostTeamID: txt("team"), MattermostChannelID: txt("chan"), Proxy: []byte(`{"enabled":false}`),
			Mentions: []byte(`{}`), LimiterLimit: 30, LimiterPerSeconds: 60, Health: "healthy", CreatedAt: t0,
			Version: 2},
		{ID: 2, PublicID: "DSAAAAAAAAAAA2", Type: "telegram", Name: "alerts", ConnectionPublicID: txt("CNAAAAAAAAAAA2"),
			TelegramChannelID: txt("@alerts"), TelegramDiscussionChatID: pgtype.Int8{Int64: -100200, Valid: true},
			Proxy: []byte(`{"enabled":false}`), Mentions: []byte(`{}`), LimiterLimit: 10, LimiterPerSeconds: 60,
			Health: "broken", BrokenSince: since, BrokenReason: txt("chat not found"), CreatedAt: t0, Version: 1},
		{ID: 3, PublicID: "DSAAAAAAAAAAA3", Type: "webhook", Name: "hook", WebhookMode: txt("events"),
			WebhookEventsConfig: []byte(`{"url":"https://example.org","headers":[]}`),
			Proxy:               []byte(`{"enabled":true,"type":"http","address":"proxy:3128","username":"u"}`),
			ProxyPasswordSet:    true, ProxyPasswordUpdatedAt: since, SigningSecretSet: true,
			SigningSecretUpdatedAt: since, Mentions: []byte(`{}`), LimiterLimit: 60, LimiterPerSeconds: 60,
			Health: "healthy", TemplateErrorSince: since, TemplateError: txt("no value"), CreatedAt: t0, Version: 4},
	}, routes: []dbgen.ListDestinationRoutesRow{{DestinationID: 1, PublicID: "RTAAAAAAAAAAA1", Name: "payments"}},
		refs: []dbgen.ListRouteDestinationRefsRow{{RouteID: 7, PublicID: "DSAAAAAAAAAAA2", Name: "alerts",
			Type: "telegram", Health: "broken", BrokenSince: since, BrokenReason: txt("chat not found")}}}
}

// TestList is listDestinations (C-11.FR-18): every type with its health and Routes, its own fields and Secrets as
// their status only, in pages after a cursor, filtered as asked.
func TestList(t *testing.T) {
	f := newStore()
	s := New(1, f)
	page, err := s.List(t.Context(), ListFilter{Limit: 2})
	if err != nil || len(page.Destinations) != 2 || page.Next == nil || *page.Next != 2 {
		t.Fatalf("first page %+v, %v", page, err)
	}
	mm, tg := page.Destinations[0], page.Destinations[1]
	if *mm.Connection != "CNAAAAAAAAAAA1" || *mm.MattermostTeamID != "team" || len(mm.Routes) != 1 ||
		mm.Routes[0].Name != "payments" || len(tg.Routes) != 0 || tg.Routes == nil {
		t.Errorf("mattermost %+v telegram routes %v", mm, tg.Routes)
	}
	if tg.Health.State != "broken" || tg.Health.Since == nil || *tg.Health.Reason != "chat not found" ||
		*tg.TelegramDiscussionGroupID != "-100200" {
		t.Errorf("telegram %+v", tg)
	}
	page, err = s.List(t.Context(), ListFilter{Limit: 2, After: page.Next})
	if err != nil || len(page.Destinations) != 1 || page.Next != nil {
		t.Fatalf("second page %+v, %v", page, err)
	}
	hook := page.Destinations[0]
	if !hook.Proxy.Enabled || *hook.Proxy.Address != "proxy:3128" || !hook.Proxy.PasswordSet ||
		hook.Proxy.PasswordUpdatedAt == nil || !hook.SigningSecret.Set || hook.TemplateError == nil ||
		hook.TemplateError.Error != "no value" || !strings.Contains(string(hook.EventsConfig), "example.org") {
		t.Errorf("webhook %+v", hook)
	}
	typ, health, route := "telegram", "broken", "RTAAAAAAAAAAA1"
	if _, err := s.List(t.Context(), ListFilter{Type: &typ, Health: &health, Route: &route, Limit: 5000}); err != nil {
		t.Fatal(err)
	}
	last := f.lists[len(f.lists)-1]
	if last.Type.String != "telegram" || last.Health.String != "broken" || last.Route.String != route ||
		last.PageSize != 1001 {
		t.Errorf("params %+v", last)
	}
	bad := "not-a-route"
	if page, err := s.List(t.Context(), ListFilter{Route: &bad}); err != nil || len(page.Destinations) != 0 ||
		page.Destinations == nil || len(f.lists) != 3 {
		t.Errorf("an unknown route = %+v, %v", page, err)
	}
	f.fail["ListDestinationRoutes"] = errBoom
	if _, err := s.List(t.Context(), ListFilter{}); !errors.Is(err, errBoom) {
		t.Errorf("routes failed = %v", err)
	}
	f.fail["ListDestinations"] = errBoom
	if _, err := s.List(t.Context(), ListFilter{}); !errors.Is(err, errBoom) {
		t.Errorf("list failed = %v", err)
	}
	f.rows = nil
	f.fail = map[string]error{}
	if page, err := s.List(t.Context(), ListFilter{}); err != nil || len(page.Destinations) != 0 {
		t.Errorf("empty = %+v, %v", page, err)
	}
}

// TestGet is getDestination: a Destination with its Routes; an unknown, malformed or deleted one is ErrNotFound.
func TestGet(t *testing.T) {
	f := newStore()
	s := New(1, f)
	d, err := s.Get(t.Context(), "dsaaaaaaaaaaa1")
	if err != nil || d.PublicID != "DSAAAAAAAAAAA1" || len(d.Routes) != 1 {
		t.Errorf("get = %+v, %v", d, err)
	}
	for _, id := range []string{"DS000000000000", "RTAAAAAAAAAAA1", ""} {
		if _, err := s.Get(t.Context(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q = %v", id, err)
		}
	}
	f.fail["ListDestinationRoutes"] = errBoom
	if _, err := s.Get(t.Context(), "DSAAAAAAAAAAA1"); !errors.Is(err, errBoom) {
		t.Errorf("routes failed = %v", err)
	}
	f.fail["GetDestination"] = errBoom
	if _, err := s.Get(t.Context(), "DSAAAAAAAAAAA1"); !errors.Is(err, errBoom) {
		t.Errorf("get failed = %v", err)
	}
	f.rows[0].Proxy = []byte(`not json`)
	if d := destinationOf(f.rows[0]); d.Proxy.Enabled {
		t.Errorf("an unreadable proxy reads as enabled")
	}
}

// TestRouteRefs is C-08.FR-1: the Destinations of Routes with their health.
func TestRouteRefs(t *testing.T) {
	f := newStore()
	s := New(1, f)
	refs, err := s.RouteRefs(t.Context(), []int64{7, 8})
	if err != nil || len(refs[7]) != 1 || refs[7][0].Health.State != "broken" || len(refs[8]) != 0 {
		t.Errorf("refs = %+v, %v", refs, err)
	}
	if refs, err := s.RouteRefs(t.Context(), nil); err != nil || len(refs) != 0 {
		t.Errorf("no routes = %v, %v", refs, err)
	}
	f.fail["ListRouteDestinationRefs"] = errBoom
	if _, err := s.RouteRefs(t.Context(), []int64{7}); !errors.Is(err, errBoom) {
		t.Errorf("failed = %v", err)
	}
}

// TestInfo is muster_destination_info: a series per Destination with its name, renamed and removed with it, kept
// current by RunInfo until its context ends.
func TestInfo(t *testing.T) {
	f := newStore()
	f.info = []dbgen.ListDestinationInfoRow{{PublicID: "DSAAAAAAAAAAA1", Name: "ops"},
		{PublicID: "DSAAAAAAAAAAA2", Name: "alerts"}}
	s := New(1, f)
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		s.RunInfo(ctx, ticks)
		close(done)
	}()
	ticks <- t0
	cancel()
	<-done
	if !strings.Contains(scrape(), `muster_destination_info{destination="DSAAAAAAAAAAA1",name="ops"} 1`) {
		t.Errorf("info %s", scrape())
	}
	f.info = []dbgen.ListDestinationInfoRow{{PublicID: "DSAAAAAAAAAAA1", Name: "ops-2"}}
	if err := s.RefreshInfo(t.Context()); err != nil {
		t.Fatal(err)
	}
	out := scrape()
	if strings.Contains(out, `name="ops"}`) || strings.Contains(out, "DSAAAAAAAAAAA2") || !strings.Contains(out,
		`name="ops-2"`) {
		t.Errorf("after rename %s", out)
	}
	f.fail["ListDestinationInfo"] = errBoom
	if err := s.RefreshInfo(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("failed = %v", err)
	}
}

func scrape() string {
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}
