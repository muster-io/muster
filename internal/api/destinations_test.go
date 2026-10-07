// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
)

const (
	destinationsReader = "mstr_pat_destinations_read"
	mattermostID       = "DSAAAAAAAAAAA1"
	telegramID         = "DSAAAAAAAAAAA2"
	webhookID          = "DSAAAAAAAAAAA3"
)

const noMentions = `{"new_alert_group":{"everyone":"none","user_ids":[],"groups":[]},` +
	`"new_alerts":{"everyone":"none","user_ids":[],"groups":[]},"reopen":{"everyone":"none","user_ids":[],"groups":[]},` +
	`"ack_timeout":{"everyone":"none","user_ids":[],"groups":[]},` +
	`"snooze_ended":{"everyone":"none","user_ids":[],"groups":[]},` +
	`"rise_to_urgent":{"everyone":"here","user_ids":[],"groups":["sre"]}}`

// fakeDestinations stands for internal/destinations: the Destinations in id order, filtered as the query filters.
type fakeDestinations struct {
	list    []destinations.Destination
	refs    map[int64][]destinations.Ref
	filters []destinations.ListFilter
	err     error
}

func (f *fakeDestinations) List(_ context.Context, lf destinations.ListFilter) (destinations.Page, error) {
	f.filters = append(f.filters, lf)
	var page destinations.Page
	for _, d := range f.list {
		if lf.After != nil && d.ID <= *lf.After || lf.Type != nil && d.Type != *lf.Type ||
			lf.Health != nil && d.Health.State != *lf.Health {
			continue
		}
		if len(page.Destinations) == lf.Limit {
			last := page.Destinations[lf.Limit-1].ID
			page.Next = &last
			break
		}
		page.Destinations = append(page.Destinations, d)
	}
	return page, f.err
}

func (f *fakeDestinations) Get(_ context.Context, id string) (destinations.Destination, error) {
	for _, d := range f.list {
		if d.PublicID == id {
			return d, f.err
		}
	}
	return destinations.Destination{}, destinations.ErrNotFound
}

func (f *fakeDestinations) RouteRefs(_ context.Context, ids []int64) (map[int64][]destinations.Ref, error) {
	out := map[int64][]destinations.Ref{}
	for _, id := range ids {
		out[id] = f.refs[id]
	}
	return out, f.err
}

type fakeDeliveries struct {
	states map[string][]delivery.State
	err    error
}

func (f *fakeDeliveries) States(_ context.Context, id string) ([]delivery.State, error) {
	s, ok := f.states[id]
	if !ok {
		return nil, delivery.ErrNotFound
	}
	return s, f.err
}

func newDestinationsAPI(t *testing.T) (*testAPI, *fakeDestinations, *fakeDeliveries) {
	t.Helper()
	x, _, _ := newAlertGroupsAPI(t)
	ft := x.srv.tokens.(*fakeTokens)
	ft.idents[destinationsReader] = &auth.Identity{Session: ft.idents[fullToken].Session,
		Permissions: []auth.Permission{"destinations:read", "routes:read", "alert-groups:read"},
		Transport:   audit.TransportAPI, Token: &auth.Token{ID: 51, Name: "destinations"}}
	since := t0.Add(-time.Hour)
	fd := &fakeDestinations{list: []destinations.Destination{
		{ID: 1, PublicID: mattermostID, Type: "mattermost", Name: "ops", Connection: ptr("CNAAAAAAAAAAA1"),
			MattermostTeamID: ptr("team"), MattermostChannelID: ptr("chan"), MattermostChannelName: ptr("ops"),
			Mentions: json.RawMessage(noMentions), LimiterLimit: 30, LimiterPerSeconds: 60,
			Health: destinations.Health{State: "healthy"}, Routes: []destinations.RouteRef{{PublicID: routeID,
				Name: "payments"}}, CreatedAt: t0, Version: 2},
		{ID: 2, PublicID: telegramID, Type: "telegram", Name: "alerts", Connection: ptr("CNAAAAAAAAAAA2"),
			TelegramChannelID: ptr("@alerts"), TelegramDiscussionGroupID: ptr("-100200"),
			Mentions: json.RawMessage(noMentions), LimiterLimit: 10, LimiterPerSeconds: 60,
			Health: destinations.Health{State: "broken", Since: &since, Reason: ptr("chat not found")},
			Routes: []destinations.RouteRef{}, CreatedAt: t0, Version: 1},
		{ID: 3, PublicID: webhookID, Type: "webhook", Name: "hook", WebhookMode: ptr("both"),
			EventsConfig:   json.RawMessage(`{"url":"https://example.org/{{ .Secrets.path }}","headers":[]}`),
			TemplateConfig: json.RawMessage(`{"create":{"method":"POST","url":"https://example.org","headers":[],"body":"{}"},"update":{"method":"PUT","url":"https://example.org","headers":[],"body":"{}"}}`),
			Proxy: destinations.Proxy{Enabled: true, Type: ptr("http"), Address: ptr("proxy:3128"), PasswordSet: true,
				PasswordUpdatedAt: &since},
			SigningSecret: destinations.SigningSecret{Set: true, UpdatedAt: &since, PreviousActiveSince: &since},
			Mentions:      json.RawMessage(noMentions), LimiterLimit: 60, LimiterPerSeconds: 60,
			Health: destinations.Health{State: "healthy"}, Routes: []destinations.RouteRef{},
			TemplateError: &destinations.TemplateError{Since: since, Error: "template: no value"}, CreatedAt: t0,
			Version: 5},
	}, refs: map[int64][]destinations.Ref{2: {{PublicID: mattermostID, Name: "ops", Type: "mattermost",
		Health: destinations.Health{State: "healthy"}}}}}
	fdl := &fakeDeliveries{states: map[string][]delivery.State{groupID: {{
		Destination: delivery.Ref{PublicID: mattermostID, Name: "ops", Type: "mattermost", Health: "healthy"},
		State:       "delivered", MessageURL: ptr("https://chat.example.org/m/1"), UpdatedAt: t0}, {
		Destination: delivery.Ref{PublicID: telegramID, Name: "alerts", Type: "telegram", Health: "broken",
			BrokenSince: &since, BrokenReason: ptr("chat not found")},
		State: "pending", UpdatedAt: t0}}}}
	x.srv.destinations, x.srv.deliveries = fd, fdl
	return x, fd, fdl
}

// TestListDestinationsAPI is listDestinations (C-11.FR-18): every type with its health and Routes, filtered by type,
// health and Route, paged by cursor, with Secrets only as their status.
func TestListDestinationsAPI(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations?limit=2", "")
	if a.status != http.StatusOK {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	var page struct {
		Items []map[string]any `json:"items"`
		Next  *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(a.body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0]["type"] != "mattermost" || page.Items[1]["type"] != "telegram" ||
		page.Next == nil {
		t.Fatalf("first page %s", a.body)
	}
	if h := page.Items[1]["health"].(map[string]any); h["state"] != "broken" || h["reason"] != "chat not found" {
		t.Errorf("health %v", h)
	}
	if r := page.Items[0]["routes"].([]any); len(r) != 1 || r[0].(map[string]any)["id"] != routeID {
		t.Errorf("routes %v", r)
	}
	a = x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations?cursor="+*page.Next, "")
	if err := json.Unmarshal(a.body, &page); err != nil || a.status != http.StatusOK || len(page.Items) != 1 ||
		page.Next != nil {
		t.Fatalf("second page %d %s", a.status, a.body)
	}
	hook := page.Items[0]
	if hook["mode"] != "both" || hook["signing_secret_status"].(map[string]any)["set"] != true ||
		hook["proxy"].(map[string]any)["password_status"].(map[string]any)["set"] != true ||
		strings.Contains(string(a.body), "password\"") || hook["template_error"] == nil ||
		len(hook["warnings"].([]any)) != 1 {
		t.Errorf("webhook %s", a.body)
	}
	a = x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations?type=telegram&health=broken&route="+routeID,
		"")
	last := fd.filters[len(fd.filters)-1]
	if a.status != http.StatusOK || *last.Type != "telegram" || *last.Health != "broken" || *last.Route != routeID {
		t.Errorf("filters %d %+v", a.status, last)
	}
	if a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations?cursor=bad", ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("bad cursor = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/destinations", ""); a.status != http.StatusForbidden {
		t.Errorf("without destinations:read = %d", a.status)
	}
	fd.list = append(fd.list, destinations.Destination{ID: 4, PublicID: "DSAAAAAAAAAAA4", Type: "carrier-pigeon",
		Mentions: json.RawMessage(noMentions)})
	if a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("an unknown type = %d", a.status)
	}
	fd.list[3].Type, fd.list[3].Mentions = "mattermost", json.RawMessage(`[]`)
	if a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations/DSAAAAAAAAAAA4", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("unreadable mentions = %d", a.status)
	}
	fd.list = fd.list[:3]
	fd.err = errors.New("down")
	if a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed list = %d", a.status)
	}
}

// TestGetDestinationAPI is getDestination: each type with its ETag; an unknown or deleted Destination is 404.
func TestGetDestinationAPI(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	for _, id := range []string{mattermostID, telegramID, webhookID} {
		a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations/"+id, "")
		if a.status != http.StatusOK || a.json(t)["id"] != id || a.header.Get("ETag") == "" {
			t.Errorf("%s = %d %s", id, a.status, a.body)
		}
	}
	a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations/"+telegramID, "")
	if a.json(t)["discussion_group_id"] != "-100200" {
		t.Errorf("telegram %s", a.body)
	}
	a = x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations/DS000000000000", "")
	if a.status != http.StatusNotFound || a.json(t)["detail"] != "No such destination." {
		t.Errorf("unknown = %d %s", a.status, a.body)
	}
	fd.list[2].EventsConfig = json.RawMessage(`[]`)
	if a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations/"+webhookID, ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("unreadable events = %d", a.status)
	}
	fd.list[2].EventsConfig, fd.list[2].TemplateConfig = nil, json.RawMessage(`[]`)
	if a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations/"+webhookID, ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("unreadable templates = %d", a.status)
	}
}

// TestListAlertGroupDeliveriesAPI is listAlertGroupDeliveries (C-11.FR-16): one item per Destination with its state,
// health and message link; an unknown Alert Group is 404.
func TestListAlertGroupDeliveriesAPI(t *testing.T) {
	x, _, fdl := newDestinationsAPI(t)
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/deliveries", "")
	if a.status != http.StatusOK {
		t.Fatalf("deliveries = %d %s", a.status, a.body)
	}
	var list struct {
		Items []struct {
			Destination struct {
				ID     string `json:"id"`
				Health struct {
					State  string  `json:"state"`
					Reason *string `json:"reason"`
				} `json:"health"`
			} `json:"destination"`
			State      string  `json:"state"`
			MessageURL *string `json:"message_url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(a.body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.Items[0].State != "delivered" || list.Items[0].MessageURL == nil ||
		list.Items[1].State != "pending" || list.Items[1].Destination.Health.State != "broken" {
		t.Errorf("items %s", a.body)
	}
	a = x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/AG000000000000/deliveries", "")
	if a.status != http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
	fdl.err = errors.New("down")
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/deliveries", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failed = %d", a.status)
	}
}

// TestRouteDestinationsAPI is C-08.FR-1: a Route carries its Destinations with their health, in the list and read.
func TestRouteDestinationsAPI(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	a := x.as(t, routesReader, http.MethodGet, "/api/v1/routes/"+routeID, "")
	m := a.json(t)
	ds := m["destinations"].([]any)
	if a.status != http.StatusOK || len(ds) != 1 || ds[0].(map[string]any)["id"] != mattermostID ||
		!slices.Equal(m["destination_ids"].([]any), []any{mattermostID}) {
		t.Errorf("route %d %s", a.status, a.body)
	}
	a = x.as(t, routesReader, http.MethodGet, "/api/v1/routes", "")
	if a.status != http.StatusOK || !strings.Contains(string(a.body), mattermostID) {
		t.Errorf("list %d %s", a.status, a.body)
	}
	fd.err = errors.New("down")
	for _, path := range []string{"/api/v1/routes", "/api/v1/routes/" + routeID} {
		if a := x.as(t, routesReader, http.MethodGet, path, ""); a.status != http.StatusInternalServerError {
			t.Errorf("%s with destinations down = %d", path, a.status)
		}
	}
}

// TestLimitedProblem is the Limited response of the interactive path (C-11.FR-2): 503 interactive-budget-exhausted
// with Retry-After and retry_after_seconds, rounded up.
func TestLimitedProblem(t *testing.T) {
	x := newTestAPI(t)
	p := x.srv.problemFor(t.Context(), "testDestination", &delivery.LimitedError{RetryAfter: 6500 * time.Millisecond})
	w := httptest.NewRecorder()
	writeProblem(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/destinations/x/checks", nil),
		p)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "7" ||
		body["type"] != problemBase+"interactive-budget-exhausted" || body["title"] != "Messenger is busy" ||
		body["retry_after_seconds"] != float64(7) {
		t.Errorf("limited = %d %v %s", w.Code, w.Header(), w.Body)
	}
}
