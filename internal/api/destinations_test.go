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

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/webhooks"
)

const (
	destinationsWriter = "mstr_pat_destinations_write"
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

// fakeDestinations stands for internal/destinations: the Destinations in id order, filtered as the query filters,
// and the deletions with who asked and the version they named.
type fakeDestinations struct {
	list     []destinations.Destination
	refs     map[int64][]destinations.Ref
	filters  []destinations.ListFilter
	deleted  []string
	versions []*int64
	by       []destinations.Requester
	err      error
	// The saves and checks: their inputs and versions, and what they answer.
	saved    []destinations.Input
	saveVers []*int64
	saveErr  error
	check    destinations.CheckResult
	checkErr error
}

func (f *fakeDestinations) Create(_ context.Context, r destinations.Requester, in destinations.Input) (
	destinations.Destination, error) {
	f.saved, f.by = append(f.saved, in), append(f.by, r)
	if f.saveErr != nil {
		return destinations.Destination{}, f.saveErr
	}
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return destinations.Destination{}, err
	}
	if in.Webhook != nil {
		events, _ := json.Marshal(in.Webhook.Events)
		d := destinations.Destination{ID: int64(len(f.list) + 10), PublicID: "DSAAAAAAAAAAA8", Type: in.Type,
			Name: in.Name, WebhookMode: &in.Webhook.Mode, EventsConfig: events,
			Proxy:         destinations.Proxy{Enabled: in.Webhook.Proxy.Enabled, PasswordSet: in.Webhook.Proxy.Password.Given},
			SigningSecret: destinations.SigningSecret{Set: true, UpdatedAt: &t0}, SigningSecretOnce: "whsec_once",
			Mentions: set, LimiterLimit: in.Limiter.Limit, LimiterPerSeconds: in.Limiter.PerSeconds,
			Health: destinations.Health{State: "healthy"}, Routes: []destinations.RouteRef{}, CreatedAt: t0, Version: 1}
		f.list = append(f.list, d)
		return d, nil
	}
	if in.Telegram != nil {
		d := destinations.Destination{ID: int64(len(f.list) + 10), PublicID: "DSAAAAAAAAAAA7", Type: in.Type,
			Name: in.Name, Connection: &in.Telegram.Connection, TelegramChannelID: &in.Telegram.ChannelID,
			TelegramChannelTitle: ptr("Muster alerts"), TelegramDiscussionGroupID: ptr("-1001000000002"),
			TelegramDiscussionGroupTitle: ptr("Muster alerts Chat"), Mentions: set, LimiterLimit: in.Limiter.Limit,
			LimiterPerSeconds: in.Limiter.PerSeconds, Health: destinations.Health{State: "healthy"},
			Routes: []destinations.RouteRef{}, CreatedAt: t0, Version: 1}
		f.list = append(f.list, d)
		return d, nil
	}
	d := destinations.Destination{ID: int64(len(f.list) + 10), PublicID: "DSAAAAAAAAAAA9", Type: in.Type,
		Name: in.Name, Connection: &in.Mattermost.Connection, MattermostTeamID: &in.Mattermost.TeamID,
		MattermostChannelID: &in.Mattermost.ChannelID, MattermostTeamName: ptr("dev"),
		MattermostChannelName: ptr("alerts"), Mentions: set, LimiterLimit: in.Limiter.Limit,
		LimiterPerSeconds: in.Limiter.PerSeconds, Health: destinations.Health{State: "healthy"},
		Routes: []destinations.RouteRef{}, CreatedAt: t0, Version: 1}
	f.list = append(f.list, d)
	return d, nil
}

func (f *fakeDestinations) Update(_ context.Context, r destinations.Requester, id string, version *int64,
	in destinations.Input) (destinations.Destination, error) {
	f.saved, f.saveVers, f.by = append(f.saved, in), append(f.saveVers, version), append(f.by, r)
	i := slices.IndexFunc(f.list, func(d destinations.Destination) bool { return d.PublicID == id })
	if i < 0 {
		return destinations.Destination{}, destinations.ErrNotFound
	}
	if version != nil && *version != f.list[i].Version {
		return destinations.Destination{}, destinations.ErrVersionMismatch
	}
	if f.saveErr != nil {
		return destinations.Destination{}, f.saveErr
	}
	d := &f.list[i]
	d.Name, d.LimiterLimit = in.Name, in.Limiter.Limit
	switch {
	case in.Mattermost != nil:
		d.MattermostChannelID = &in.Mattermost.ChannelID
	case in.Telegram != nil:
		d.TelegramChannelID = &in.Telegram.ChannelID
	}
	d.Version++
	return *d, nil
}

func (f *fakeDestinations) Check(_ context.Context, id string) (destinations.CheckResult, error) {
	if !slices.ContainsFunc(f.list, func(d destinations.Destination) bool { return d.PublicID == id }) {
		return destinations.CheckResult{}, destinations.ErrNotFound
	}
	return f.check, f.checkErr
}

func (f *fakeDestinations) Delete(_ context.Context, r destinations.Requester, id string, version *int64) error {
	i := slices.IndexFunc(f.list, func(d destinations.Destination) bool { return d.PublicID == id })
	if i < 0 {
		return destinations.ErrNotFound
	}
	if version != nil && *version != f.list[i].Version {
		return destinations.ErrVersionMismatch
	}
	if f.err != nil {
		return f.err
	}
	f.deleted, f.versions, f.by = append(f.deleted, id), append(f.versions, version), append(f.by, r)
	f.list = slices.Delete(f.list, i, i+1)
	return nil
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
	ft.idents[destinationsWriter] = &auth.Identity{Session: ft.idents[fullToken].Session,
		Permissions: []auth.Permission{"destinations:read", "destinations:write"},
		Transport:   audit.TransportAPI, Token: &auth.Token{ID: 52, Name: "destinations-write"}}
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

// TestDeleteDestinationAPI is deleteDestination (C-11.FR-14) at the API with destinations:write: 204 with or without
// If-Match, 412 when it is stale, 404 for an unknown or deleted Destination, 403 without the Permission, and the
// deleted Destination no longer read or listed.
func TestDeleteDestinationAPI(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	path := "/api/v1/destinations/"
	if a := x.as(t, destinationsReader, http.MethodDelete, path+webhookID, ""); a.status != http.StatusForbidden {
		t.Errorf("delete by a reader = %d %s", a.status, a.body)
	}
	if a := x.as(t, destinationsWriter, http.MethodDelete, path+webhookID, "", "If-Match", `"4"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("stale = %d %s", a.status, a.body)
	}
	if a := x.as(t, destinationsWriter, http.MethodDelete, path+webhookID, "", "If-Match", `"5"`); a.status !=
		http.StatusNoContent || !slices.Equal(fd.deleted, []string{webhookID}) || *fd.versions[0] != 5 ||
		fd.by[0].Actor.TokenName != "destinations-write" || fd.by[0].Transport != audit.TransportAPI {
		t.Errorf("delete = %d %s, %+v", a.status, a.body, fd.by)
	}
	if a := x.as(t, destinationsWriter, http.MethodDelete, path+telegramID, ""); a.status != http.StatusNoContent ||
		fd.versions[1] != nil {
		t.Errorf("delete without If-Match = %d %s", a.status, a.body)
	}
	for _, id := range []string{webhookID, "DSZZZZZZZZZZZZ"} {
		if a := x.as(t, destinationsWriter, http.MethodDelete, path+id, ""); a.status != http.StatusNotFound {
			t.Errorf("delete %s again = %d %s", id, a.status, a.body)
		}
	}
	if a := x.as(t, destinationsReader, http.MethodGet, path+webhookID, ""); a.status != http.StatusNotFound {
		t.Errorf("read a deleted destination = %d", a.status)
	}
	a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations", "")
	if a.status != http.StatusOK || strings.Contains(string(a.body), webhookID) {
		t.Errorf("list = %d %s", a.status, a.body)
	}
	if a := x.as(t, destinationsWriter, http.MethodDelete, path+mattermostID, "", "If-Match", "garbage"); a.status !=
		http.StatusPreconditionFailed && a.status != http.StatusBadRequest {
		t.Errorf("malformed If-Match = %d", a.status)
	}
	fd.err = errors.New("boom")
	if a := x.as(t, destinationsWriter, http.MethodDelete, path+mattermostID, ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failure = %d", a.status)
	}
}

const (
	destinationsTester = "mstr_pat_destinations_test"
	mattermostBody     = `{"type":"mattermost","name":"alerts","connection_id":"CNAAAAAAAAAAA1","team_id":"team-dev",` +
		`"channel_id":"ch-alerts","mentions":` + noMentions + `,"limiter":{"limit":5,"per_seconds":1}}`
)

// TestCreateDestinationAPI is createDestination for Mattermost (C-13.FR-2, FR-3, C-11.FR-18, C-12.FR-8): 201 with
// the Destination, its team and channel names and a null signing_secret; a failing Destination check is 422
// destination_check_failed at the field it concerns; a Mention of no User is 422 unknown_id; no limiter token in time
// is 503 with Retry-After.
func TestCreateDestinationAPI(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	a := x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", mattermostBody)
	if a.status != http.StatusCreated || a.header.Get("ETag") != `"1"` ||
		a.header.Get("Location") != "/api/v1/destinations/DSAAAAAAAAAAA9" {
		t.Fatalf("create = %d %v %s", a.status, a.header, a.body)
	}
	var created gen.DestinationCreated
	decodeInto(t, a, &created)
	d, err := created.Destination.AsMattermostDestination()
	if err != nil || !created.SigningSecret.IsNull() || d.Id != "DSAAAAAAAAAAA9" || d.TeamName.MustGet() != "dev" ||
		d.ChannelName.MustGet() != "alerts" || d.Limiter.Limit != 5 || d.ConnectionId != "CNAAAAAAAAAAA1" {
		t.Errorf("created %+v, %v: %s", d, err, a.body)
	}
	in := fd.saved[0]
	if in.Type != "mattermost" || in.Name != "alerts" || in.Mattermost.Connection != "CNAAAAAAAAAAA1" ||
		in.Mattermost.TeamID != "team-dev" || in.Mattermost.ChannelID != "ch-alerts" || in.Limiter.PerSeconds != 1 ||
		len(in.Mentions) != 6 || in.Mentions["rise_to_urgent"].Everyone != "here" ||
		!slices.Equal(in.Mentions["rise_to_urgent"].Groups, []string{"sre"}) || in.Mentions["reopen"].UserIDs == nil ||
		fd.by[0].Actor.TokenName != "destinations-write" {
		t.Errorf("input %+v", in)
	}
	fd.saveErr = &destinations.CheckFailedError{Items: []destinations.CheckItem{{Name: mattermost.StepBotInChannel,
		Message: mattermost.MessageNotMember, Pointer: "/channel_id"}}}
	a = x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", mattermostBody)
	if errs := problemErrors(t, a); a.status != http.StatusUnprocessableEntity ||
		a.json(t)["type"] != problemBase+"validation-failed" || len(errs) != 1 || errs[0]["pointer"] != "/channel_id" ||
		errs[0]["code"] != "destination_check_failed" || errs[0]["detail"] != mattermost.MessageNotMember {
		t.Errorf("no bot = %d %s", a.status, a.body)
	}
	fd.saveErr = &destinations.CheckFailedError{Items: []destinations.CheckItem{{Name: mattermost.StepToken,
		Message: mattermost.MessageTokenInvalid, Pointer: "/connection_id"}}}
	if errs := problemErrors(t, x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations",
		mattermostBody)); len(errs) != 1 || errs[0]["pointer"] != "/connection_id" {
		t.Errorf("revoked token = %v", errs)
	}
	fd.saveErr = &mentions.FieldError{Pointer: "/mentions/new_alerts/user_ids/0", Code: mentions.CodeUnknownID,
		Detail: "No such user."}
	if errs := problemErrors(t, x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations",
		mattermostBody)); len(errs) != 1 || errs[0]["code"] != "unknown_id" ||
		errs[0]["pointer"] != "/mentions/new_alerts/user_ids/0" {
		t.Errorf("unknown user = %v", errs)
	}
	fd.saveErr = &destinations.FieldError{Pointer: "/connection_id", Code: destinations.CodeUnknownID,
		Detail: "No such Mattermost Connection."}
	if errs := problemErrors(t, x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations",
		mattermostBody)); len(errs) != 1 || errs[0]["code"] != "unknown_id" || errs[0]["pointer"] != "/connection_id" {
		t.Errorf("unknown connection = %v", errs)
	}
	fd.saveErr = &delivery.LimitedError{RetryAfter: 2 * time.Second}
	if a := x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", mattermostBody); a.status !=
		http.StatusServiceUnavailable || a.header.Get("Retry-After") != "2" {
		t.Errorf("limited = %d %v %s", a.status, a.header, a.body)
	}
	fd.saveErr = destinations.ErrNameTaken
	if a := x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", mattermostBody); a.status !=
		http.StatusConflict || a.code(t) != "name_taken" {
		t.Errorf("taken = %d %s", a.status, a.body)
	}
	if a := x.as(t, destinationsReader, http.MethodPost, "/api/v1/destinations", mattermostBody); a.status !=
		http.StatusForbidden {
		t.Errorf("by a reader = %d", a.status)
	}
}

// TestUpdateDestinationAPI is updateDestination: If-Match is required (428) and must be current (412); the save runs
// as createDestination's.
func TestUpdateDestinationAPI(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	path := "/api/v1/destinations/" + mattermostID
	if a := x.as(t, destinationsWriter, http.MethodPut, path, mattermostBody); a.status !=
		http.StatusPreconditionRequired {
		t.Errorf("without If-Match = %d %s", a.status, a.body)
	}
	if a := x.as(t, destinationsWriter, http.MethodPut, path, mattermostBody, "If-Match", `"1"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("stale = %d %s", a.status, a.body)
	}
	a := x.as(t, destinationsWriter, http.MethodPut, path, mattermostBody, "If-Match", `"2"`)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"3"` || a.json(t)["name"] != "alerts" ||
		a.json(t)["channel_id"] != "ch-alerts" || *fd.saveVers[len(fd.saveVers)-1] != 2 {
		t.Errorf("update = %d %s", a.status, a.body)
	}
	fd.saveErr = &destinations.CheckFailedError{Items: []destinations.CheckItem{{Name: mattermost.StepBotInChannel,
		Message: mattermost.MessageNotMember, Pointer: "/channel_id"}}}
	if a := x.as(t, destinationsWriter, http.MethodPut, path, mattermostBody, "If-Match", `"3"`); a.status !=
		http.StatusUnprocessableEntity || problemErrors(t, a)[0]["code"] != "destination_check_failed" {
		t.Errorf("no bot = %d %s", a.status, a.body)
	}
	if a := x.as(t, destinationsWriter, http.MethodPut, "/api/v1/destinations/DS000000000000", mattermostBody,
		"If-Match", `"1"`); a.status != http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
}

// TestCheckDestinationAPI is checkDestination (C-13.FR-10) with destinations:test: each check with its result and the
// health after it; a type without a check is 422 check_not_supported; no limiter token in time is 503.
func TestCheckDestinationAPI(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	ft := x.srv.tokens.(*fakeTokens)
	ft.idents[destinationsTester] = &auth.Identity{Session: ft.idents[fullToken].Session,
		Permissions: []auth.Permission{"destinations:read", "destinations:test"}, Transport: audit.TransportAPI,
		Token: &auth.Token{ID: 53, Name: "destinations-test"}}
	path := "/api/v1/destinations/" + mattermostID + "/checks"
	fd.check = destinations.CheckResult{Health: destinations.Health{State: "healthy"}, Items: []destinations.CheckItem{
		{Name: mattermost.StepToken, OK: true}, {Name: mattermost.StepBotInChannel, Message: mattermost.MessageNotMember}}}
	a := x.as(t, destinationsTester, http.MethodPost, path, "")
	var res gen.DestinationCheckResult
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || res.Ok || len(res.Checks) != 2 || res.Checks[0].Name != "token" ||
		!res.Checks[0].Ok || !res.Checks[0].Message.IsNull() || res.Checks[1].Name != "bot_in_channel" ||
		res.Checks[1].Message.MustGet() != "The bot is not a member of this channel." ||
		res.Health.State != gen.Healthy {
		t.Errorf("check = %d %s", a.status, a.body)
	}
	fd.check = destinations.CheckResult{OK: true, Health: destinations.Health{State: "healthy"},
		Items: []destinations.CheckItem{{Name: mattermost.StepToken, OK: true},
			{Name: mattermost.StepBotInChannel, OK: true}}}
	if a := x.as(t, destinationsTester, http.MethodPost, path, ""); a.status != http.StatusOK || a.json(t)["ok"] != true {
		t.Errorf("passing = %d %s", a.status, a.body)
	}
	fd.checkErr = &destinations.FieldError{Pointer: "/path/destination_id", Code: destinations.CodeCheckNotSupported,
		Detail: "This type of Destination has no Destination check."}
	a = x.as(t, destinationsTester, http.MethodPost, "/api/v1/destinations/"+webhookID+"/checks", "")
	if a.status != http.StatusUnprocessableEntity || problemErrors(t, a)[0]["code"] != "check_not_supported" {
		t.Errorf("webhook = %d %s", a.status, a.body)
	}
	fd.checkErr = &delivery.LimitedError{RetryAfter: 4 * time.Second}
	if a := x.as(t, destinationsTester, http.MethodPost, path, ""); a.status != http.StatusServiceUnavailable ||
		a.header.Get("Retry-After") != "4" {
		t.Errorf("limited = %d %v", a.status, a.header)
	}
	if a := x.as(t, destinationsTester, http.MethodPost, "/api/v1/destinations/DS000000000000/checks", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
	if a := x.as(t, destinationsWriter, http.MethodPost, path, ""); a.status != http.StatusForbidden {
		t.Errorf("without destinations:test = %d", a.status)
	}
}

// TestMattermostDestinationRead is C-11.FR-18 and C-13.FR-2: a saved Mattermost Destination reads with its
// Connection, team and channel and their names, its Mention settings and its limiter.
func TestMattermostDestinationRead(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	fd.list[0].MattermostTeamName = ptr("dev")
	a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations/"+mattermostID, "")
	m := a.json(t)
	if a.status != http.StatusOK || m["connection_id"] != "CNAAAAAAAAAAA1" || m["team_id"] != "team" ||
		m["team_name"] != "dev" || m["channel_name"] != "ops" ||
		m["mentions"].(map[string]any)["rise_to_urgent"].(map[string]any)["everyone"] != "here" ||
		m["limiter"].(map[string]any)["limit"] != float64(30) {
		t.Errorf("read = %d %s", a.status, a.body)
	}
}

// fakeWebhooks stands for internal/webhooks: the Secrets and Signing secrets of the webhook Destination, what they
// were asked with, and the error they answer.
type fakeWebhooks struct {
	secrets  []webhooks.Secret
	version  int64
	values   map[string]logging.Secret
	versions []*int64
	by       []webhooks.Requester
	status   webhooks.SigningStatus
	err      error
}

func (f *fakeWebhooks) check(id string) error {
	switch {
	case f.err != nil:
		return f.err
	case id == mattermostID:
		return webhooks.ErrNotWebhook
	case id != webhookID:
		return webhooks.ErrNotFound
	}
	return nil
}

func (f *fakeWebhooks) ListSecrets(_ context.Context, id string) (webhooks.Secrets, error) {
	if err := f.check(id); err != nil {
		return webhooks.Secrets{}, err
	}
	return webhooks.Secrets{Items: f.secrets, Version: f.version}, nil
}

func (f *fakeWebhooks) SetSecret(_ context.Context, r webhooks.Requester, id string, version *int64, name string,
	value logging.Secret) (webhooks.Secret, int64, error) {
	f.by, f.versions = append(f.by, r), append(f.versions, version)
	if err := f.check(id); err != nil {
		return webhooks.Secret{}, 0, err
	}
	if version != nil && *version != f.version {
		return webhooks.Secret{}, 0, webhooks.ErrVersionMismatch
	}
	if !webhooks.ValidSecretName(name) {
		return webhooks.Secret{}, 0, &webhooks.FieldError{Pointer: "/path/secret_name", Code: "invalid_format",
			Detail: "bad"}
	}
	f.values[name] = value
	f.version++
	return webhooks.Secret{Name: name, UpdatedAt: t0}, f.version, nil
}

func (f *fakeWebhooks) DeleteSecret(_ context.Context, r webhooks.Requester, id string, version *int64,
	name string) error {
	f.by, f.versions = append(f.by, r), append(f.versions, version)
	if err := f.check(id); err != nil {
		return err
	}
	if _, ok := f.values[name]; !ok {
		return webhooks.ErrNoSecret
	}
	delete(f.values, name)
	return nil
}

func (f *fakeWebhooks) SigningStatus(_ context.Context, id string) (webhooks.SigningStatus, error) {
	return f.status, f.check(id)
}

func (f *fakeWebhooks) GenerateSigningSecret(_ context.Context, r webhooks.Requester, id string) (logging.Secret,
	webhooks.SigningStatus, error) {
	f.by = append(f.by, r)
	if err := f.check(id); err != nil {
		return "", webhooks.SigningStatus{}, err
	}
	f.status = webhooks.SigningStatus{Set: true, UpdatedAt: &t0, PreviousActiveSince: &t0}
	return "whsec_new", f.status, nil
}

func (f *fakeWebhooks) RetirePreviousSigningSecret(_ context.Context, r webhooks.Requester, id string) error {
	f.by = append(f.by, r)
	if err := f.check(id); err != nil {
		return err
	}
	f.status.PreviousActiveSince = nil
	return nil
}

const webhookBody = `{"type":"webhook","name":"auto","mode":"events","events":{"url":"http://127.0.0.1:18093/hook/auto",` +
	`"headers":[{"name":"Authorization","value":"Bearer {{ .Secrets.token }}"}]},` +
	`"proxy":{"enabled":false,"password":"pp"},"mentions":` + noMentions + `,"limiter":{"limit":5,"per_seconds":1}}`

// TestCreateWebhookDestinationAPI is createDestination of an outgoing webhook (C-15.FR-1, FR-5, AC-1): the input
// reaches the service with its mode, request and proxy, and the answer carries the Signing secret once; a request
// template that fails names its line and column.
func TestCreateWebhookDestinationAPI(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	a := x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", webhookBody)
	if a.status != http.StatusCreated {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	var created gen.DestinationCreated
	decodeInto(t, a, &created)
	w, err := created.Destination.AsWebhookDestination()
	if err != nil || created.SigningSecret.MustGet() != "whsec_once" || w.Events == nil || len(w.Events.Headers) != 1 ||
		!w.SigningSecretStatus.Set {
		t.Fatalf("created %+v (%v): %s", w, err, a.body)
	}
	in := fd.saved[0]
	if in.Type != "webhook" || in.Webhook.Mode != "events" || in.Webhook.Events.URL != "http://127.0.0.1:18093/hook/auto" ||
		in.Webhook.Events.Headers[0].Value != "Bearer {{ .Secrets.token }}" || in.Webhook.Proxy.Password.Value != "pp" ||
		in.Limiter.Limit != 5 {
		t.Errorf("input %+v", in.Webhook)
	}
	fd.saveErr = &destinations.FieldError{Pointer: "/events/url", Code: "template_syntax", Detail: "unclosed",
		Line: 2, Column: 7}
	a = x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", webhookBody)
	if errs := problemErrors(t, a); a.status != http.StatusUnprocessableEntity || errs[0]["line"] != 2.0 ||
		errs[0]["column"] != 7.0 || errs[0]["pointer"] != "/events/url" {
		t.Errorf("template error = %d %s", a.status, a.body)
	}
	fd.list[2].WebhookMode = ptr("events")
	fd.saveErr = &destinations.FieldError{Pointer: "/mode", Code: "unsupported", Detail: "later", Line: 1}
	if a := x.as(t, destinationsWriter, http.MethodPut, "/api/v1/destinations/"+webhookID, webhookBody, "If-Match",
		`"5"`); a.status != http.StatusUnprocessableEntity {
		t.Errorf("update = %d %s", a.status, a.body)
	}
	noEvents := strings.Replace(webhookBody, `"events":{"url":"http://127.0.0.1:18093/hook/auto","headers":[{"name":"Authorization","value":"Bearer {{ .Secrets.token }}"}]},`, "", 1)
	fd.saveErr = nil
	x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", noEvents)
	if fd.saved[len(fd.saved)-1].Webhook.Events != nil {
		t.Error("an omitted request reached the service")
	}
}

// TestWebhookDestinationWarnings is C-15.FR-10 and the warnings of an outgoing webhook as read: a literal
// Authorization header gives literal_credential with its JSON Pointer, and a previous Signing secret that still signs
// previous_signing_secret_active with its date.
func TestWebhookDestinationWarnings(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	fd.list[2].EventsConfig = json.RawMessage(`{"url":"https://example.org/hook",` +
		`"headers":[{"name":"Authorization","value":"Bearer abc"}]}`)
	a := x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations/"+webhookID, "")
	var d gen.Destination
	decodeInto(t, a, &d)
	w, err := d.AsWebhookDestination()
	if err != nil || len(w.Warnings) != 2 || w.Warnings[0].Kind != gen.PreviousSigningSecretActive ||
		w.Warnings[1].Kind != "literal_credential" || w.Warnings[1].Field.MustGet() != "/events/headers/0/value" {
		t.Fatalf("warnings %+v (%v): %s", w.Warnings, err, a.body)
	}
	fd.list[2].EventsConfig = json.RawMessage(`{"url":"https://example.org/hook"}`)
	a = x.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations/"+webhookID, "")
	if !strings.Contains(string(a.body), `"headers":[]`) {
		t.Errorf("headers %s", a.body)
	}
}

// TestDestinationSecretsAPI is listDestinationSecrets, setDestinationSecret and deleteDestinationSecret (C-15.FR-10):
// values are write-only; the ETag is the version of the Secrets and If-Match guards the changes; another type is 409
// not_webhook_destination; readers list but do not change them.
func TestDestinationSecretsAPI(t *testing.T) {
	x, _, _ := newDestinationsAPI(t)
	fw := &fakeWebhooks{version: 5, values: map[string]logging.Secret{}}
	x.srv.webhooks = fw
	path := "/api/v1/destinations/" + webhookID + "/secrets"
	a := x.as(t, destinationsWriter, http.MethodPut, path+"/token", `{"value":"s3cr3t-token-value"}`, "If-Match", `"5"`)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"6"` || a.json(t)["name"] != "token" ||
		a.json(t)["set"] != true || strings.Contains(string(a.body), "s3cr3t") || fw.values["token"] != "s3cr3t-token-value" ||
		*fw.versions[0] != 5 || fw.by[0].Actor.TokenName != "destinations-write" {
		t.Fatalf("set = %d %s", a.status, a.body)
	}
	fw.secrets = []webhooks.Secret{{Name: "token", UpdatedAt: t0}}
	a = x.as(t, destinationsReader, http.MethodGet, path, "")
	if a.status != http.StatusOK || a.header.Get("ETag") != `"6"` ||
		!strings.Contains(string(a.body), `{"name":"token","set":true,"updated_at":"2026-`) {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	for name, c := range map[string]struct {
		method, path, body string
		headers            []string
		status             int
		code               string
	}{
		"stale":       {http.MethodPut, path + "/token", `{"value":"x"}`, []string{"If-Match", `"1"`}, 412, ""},
		"bad name":    {http.MethodPut, path + "/9x", `{"value":"x"}`, nil, 400, ""},
		"no value":    {http.MethodPut, path + "/token", `{}`, nil, 400, ""},
		"bad etag":    {http.MethodPut, path + "/token", `{"value":"x"}`, []string{"If-Match", `x`}, 412, ""},
		"not webhook": {http.MethodGet, "/api/v1/destinations/" + mattermostID + "/secrets", "", nil, 409, codeNotWebhook},
		"unknown":     {http.MethodGet, "/api/v1/destinations/DS000000000000/secrets", "", nil, 404, ""},
		"no secret":   {http.MethodDelete, path + "/other", "", nil, 404, ""},
		"del stale":   {http.MethodDelete, path + "/token", "", []string{"If-Match", `x`}, 412, ""},
		"del other":   {http.MethodDelete, "/api/v1/destinations/" + mattermostID + "/secrets/a", "", nil, 409, codeNotWebhook},
	} {
		a := x.as(t, destinationsWriter, c.method, c.path, c.body, c.headers...)
		if a.status != c.status || (c.code != "" && a.code(t) != c.code) {
			t.Errorf("%s = %d %s", name, a.status, a.body)
		}
	}
	if a := x.as(t, destinationsReader, http.MethodPut, path+"/token", `{"value":"x"}`); a.status != http.StatusForbidden {
		t.Errorf("by a reader = %d", a.status)
	}
	if a := x.as(t, destinationsWriter, http.MethodDelete, path+"/token", ""); a.status != http.StatusNoContent ||
		len(fw.values) != 0 {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	fw.err = errors.New("down")
	if a := x.as(t, destinationsReader, http.MethodGet, path, ""); a.status != http.StatusInternalServerError {
		t.Errorf("failure = %d", a.status)
	}
}

// TestSigningSecretAPI is getSigningSecret, generateSigningSecret and retirePreviousSigningSecret (C-15.FR-5, AC-2):
// the status never carries a value; a new secret is shown once, and the previous one signs until it is retired.
func TestSigningSecretAPI(t *testing.T) {
	x, _, _ := newDestinationsAPI(t)
	fw := &fakeWebhooks{values: map[string]logging.Secret{}, status: webhooks.SigningStatus{Set: true, UpdatedAt: &t0}}
	x.srv.webhooks = fw
	path := "/api/v1/destinations/" + webhookID + "/signing-secret"
	a := x.as(t, destinationsReader, http.MethodGet, path, "")
	if a.status != http.StatusOK || a.json(t)["set"] != true || a.json(t)["previous_active_since"] != nil {
		t.Fatalf("status = %d %s", a.status, a.body)
	}
	a = x.as(t, destinationsWriter, http.MethodPost, path, "")
	var gen1 gen.SigningSecretGenerated
	decodeInto(t, a, &gen1)
	if a.status != http.StatusCreated || gen1.Secret != "whsec_new" || gen1.Status.PreviousActiveSince.IsNull() {
		t.Fatalf("generate = %d %s", a.status, a.body)
	}
	if a := x.as(t, destinationsWriter, http.MethodDelete, path+"/previous", ""); a.status != http.StatusNoContent ||
		fw.status.PreviousActiveSince != nil {
		t.Fatalf("retire = %d %s", a.status, a.body)
	}
	for _, c := range []struct{ method, path string }{{http.MethodGet, ""}, {http.MethodPost, ""},
		{http.MethodDelete, "/previous"}} {
		a := x.as(t, destinationsWriter, c.method, "/api/v1/destinations/"+mattermostID+"/signing-secret"+c.path, "")
		if a.status != http.StatusConflict || a.code(t) != codeNotWebhook {
			t.Errorf("%s %s = %d %s", c.method, c.path, a.status, a.body)
		}
	}
	if a := x.as(t, destinationsReader, http.MethodPost, path, ""); a.status != http.StatusForbidden {
		t.Errorf("by a reader = %d", a.status)
	}
}

// TestTelegramDestinationAPI is createDestination and updateDestination of type telegram (C-14.FR-2, C-14.AC-13,
// C-11.FR-18): the body names the Connection and the channel only; the Destination shows the discussion group its
// check found, read-only; a refused check is 422 destination_check_failed with the check's message as the detail.
func TestTelegramDestinationAPI(t *testing.T) {
	x, fd, _ := newDestinationsAPI(t)
	body := `{"type":"telegram","name":"tg","connection_id":"CNAAAAAAAAAAA2","channel_id":"@muster_alerts",` +
		`"mentions":` + noMentions + `,"limiter":{"limit":10,"per_seconds":60}}`
	a := x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", body)
	if a.status != http.StatusCreated {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	created := a.json(t)["destination"].(map[string]any)
	in := fd.saved[len(fd.saved)-1]
	if in.Type != "telegram" || in.Telegram == nil || in.Telegram.Connection != "CNAAAAAAAAAAA2" ||
		in.Telegram.ChannelID != "@muster_alerts" || in.Mattermost != nil || in.Limiter.Limit != 10 {
		t.Errorf("input %+v", in)
	}
	if created["type"] != "telegram" || created["channel_id"] != "@muster_alerts" ||
		created["discussion_group_id"] != "-1001000000002" || created["discussion_group_title"] != "Muster alerts Chat" ||
		created["channel_title"] != "Muster alerts" {
		t.Errorf("created %s", a.body)
	}
	update := strings.Replace(body, `"name":"tg"`, `"name":"tg2"`, 1)
	a = x.as(t, destinationsWriter, http.MethodPut, "/api/v1/destinations/DSAAAAAAAAAAA7", update,
		"If-Match", `"1"`)
	if a.status != http.StatusOK || a.json(t)["name"] != "tg2" || fd.saved[len(fd.saved)-1].Telegram == nil {
		t.Errorf("update = %d %s", a.status, a.body)
	}
	fd.saveErr = &destinations.CheckFailedError{Items: []destinations.CheckItem{{Name: "discussion_group",
		Pointer: "/channel_id", Message: "Comments are not enabled for this channel. Enable comments in the channel " +
			"settings in Telegram; this creates its discussion group."}}}
	a = x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", body)
	if errs := problemErrors(t, a); a.status != http.StatusUnprocessableEntity || errs[0]["code"] != "destination_check_failed" ||
		errs[0]["pointer"] != "/channel_id" || !strings.HasPrefix(errs[0]["detail"].(string), "Comments are not enabled") {
		t.Errorf("refused = %d %s", a.status, a.body)
	}
	fd.saveErr = nil
	if a := x.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations",
		strings.Replace(body, `"channel_id":"@muster_alerts",`, "", 1)); a.status != http.StatusBadRequest {
		t.Errorf("without a channel = %d %s", a.status, a.body)
	}
}
