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
			TelegramChannelID: txt("@alerts"), TelegramChannelChatID: pgtype.Int8{Int64: -100100, Valid: true},
			TelegramDiscussionChatID: pgtype.Int8{Int64: -100200, Valid: true},
			Proxy:                    []byte(`{"enabled":false}`), Mentions: []byte(`{}`), LimiterLimit: 10, LimiterPerSeconds: 60,
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

// bridgeRow is the Bridge Destination DSAAAAAAAAAAA4 on ch-alerts, whose check read the name "alerts", Broken.
func bridgeRow() dbgen.GetDestinationRow {
	since := pgtype.Timestamptz{Time: t0.Add(-time.Hour), Valid: true}
	return dbgen.GetDestinationRow{ID: 4, PublicID: "DSAAAAAAAAAAA4", Type: "bridge", Name: "alerts-br",
		ConnectionPublicID: txt("CNAAAAAAAAAAA3"), BridgeChannelID: txt("ch-alerts"), BridgeChannelName: txt("alerts"),
		Proxy: []byte(`{"enabled":false}`), Mentions: []byte(`{}`), LimiterLimit: 30, LimiterPerSeconds: 60,
		Health: "broken", BrokenSince: since, BrokenReason: txt("The bot is not a member of this channel."),
		CreatedAt: t0, Version: 1}
}

// TestBridgeReadModel is getDestination and listDestinations of a Bridge Destination (C-22.FR-3, AC-2): its
// Connection, its channel id and the channel's name its check read, and none of the other types' fields; the list is
// filtered by the type bridge.
func TestBridgeReadModel(t *testing.T) {
	f := newStore()
	f.rows = append(f.rows, bridgeRow())
	s := New(1, f)
	d, err := s.Get(t.Context(), "DSAAAAAAAAAAA4")
	if err != nil || d.Type != TypeBridge || *d.Connection != "CNAAAAAAAAAAA3" || *d.BridgeChannelID != "ch-alerts" ||
		*d.BridgeChannelName != "alerts" || d.TelegramChannelID != nil || d.MattermostChannelID != nil ||
		d.WebhookMode != nil || d.Health.State != "broken" || d.LimiterLimit != 30 || d.Routes == nil {
		t.Fatalf("get = %+v, %v", d, err)
	}
	typ := TypeBridge
	page, err := s.List(t.Context(), ListFilter{Type: &typ})
	if err != nil || len(page.Destinations) != 1 || *page.Destinations[0].BridgeChannelName != "alerts" ||
		f.lists[len(f.lists)-1].Type.String != "bridge" {
		t.Fatalf("list = %+v, %v", page, err)
	}
	if d := destinationOf(f.rows[0]); d.BridgeChannelID != nil || d.BridgeChannelName != nil {
		t.Errorf("a Mattermost Destination has Bridge fields: %+v", d)
	}
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
		mm.Routes[0].Name != "payments" || len(tg.Routes) != 0 || tg.Routes == nil || mm.TelegramChannelChatID != nil {
		t.Errorf("mattermost %+v telegram routes %v", mm, tg.Routes)
	}
	if tg.Health.State != "broken" || tg.Health.Since == nil || *tg.Health.Reason != "chat not found" ||
		*tg.TelegramDiscussionGroupID != "-100200" || *tg.TelegramChannelChatID != "-100100" {
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
	if strings.Contains(out, `name="ops"}`) || strings.Contains(out, `muster_destination_info{destination="DSAAAAAAAAAAA2"`) ||
		!strings.Contains(out, `name="ops-2"`) {
		t.Errorf("after rename %s", out)
	}
	f.fail["ListDestinationInfo"] = errBoom
	if err := s.RefreshInfo(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("failed = %v", err)
	}
}

// counterSeries are the series of muster_delivery_attempts_total and muster_template_errors_total of the Destination
// id with their values, as /metrics shows them.
func counterSeries(id string) []string {
	var out []string
	for line := range strings.Lines(scrape()) {
		if strings.HasPrefix(line, `muster_delivery_attempts_total{destination="`+id+`"`) ||
			strings.HasPrefix(line, `muster_template_errors_total{route="",destination="`+id+`"`) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	slices.Sort(out)
	return out
}

// zeroCounterSeries are the counter series a Destination of type typ has before any attempt.
func zeroCounterSeries(id, typ string) []string {
	kinds := []string{"final_edit", "publication", "storm_summary", "thread_reply", "update"}
	if typ == TypeWebhook {
		kinds = append(kinds, "webhook_event")
	}
	var out []string
	for _, kind := range kinds {
		out = append(out, `muster_delivery_attempts_total{destination="`+id+`",kind="`+kind+`",outcome="delivered"} 0`)
	}
	if typ == TypeWebhook {
		out = append(out, `muster_template_errors_total{route="",destination="`+id+`",template="webhook_request"} 0`)
	}
	return out
}

// TestCounterSeries: every Destination that is not deleted has at zero, before its first attempt, its series of
// muster_delivery_attempts_total with the outcome delivered for each kind its type sends and, for an outgoing webhook,
// of muster_template_errors_total for its request template, so that increase() sees the first one: those stored when
// the replica starts and those another replica creates once their hint arrives. A deleted Destination keeps its
// series, which count its final edits.
func TestCounterSeries(t *testing.T) {
	f := newStore()
	f.info = []dbgen.ListDestinationInfoRow{{PublicID: "DSCNTAAAAAAAM1", Name: "mm", Type: TypeMattermost},
		{PublicID: "DSCNTAAAAAAAT1", Name: "tg", Type: TypeTelegram}}
	s := New(1, f)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		s.RunInfo(ctx, nil)
		close(done)
	}()
	waitSeries(t, "DSCNTAAAAAAAM1", TypeMattermost)
	waitSeries(t, "DSCNTAAAAAAAT1", TypeTelegram)
	if got := counterSeries("DSCNTAAAAAAAW1"); len(got) != 0 {
		t.Fatalf("a Destination not created yet: %q", got)
	}
	f.info = append(f.info, dbgen.ListDestinationInfoRow{PublicID: "DSCNTAAAAAAAW1", Name: "hook", Type: TypeWebhook})
	s.InfoChanged()
	s.InfoChanged() // never blocks
	waitSeries(t, "DSCNTAAAAAAAW1", TypeWebhook)
	cancel()
	<-done

	f.info = nil
	if err := s.RefreshInfo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := counterSeries("DSCNTAAAAAAAW1"); !slices.Equal(got, zeroCounterSeries("DSCNTAAAAAAAW1", TypeWebhook)) {
		t.Errorf("after the deletion = %q", got)
	}
}

// waitSeries waits until the Destination id of type typ has its counter series at zero.
func waitSeries(t *testing.T, id, typ string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Equal(counterSeries(id), zeroCounterSeries(id, typ)) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: %q", id, counterSeries(id))
		}
		time.Sleep(time.Millisecond)
	}
}

func scrape() string {
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}
