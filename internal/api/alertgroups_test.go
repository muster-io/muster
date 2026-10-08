// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/routing"
)

const groupID = "AGAAAAAAAAAAAA"

var errBoom = errors.New("boom")

func ptr[T any](v T) *T { return &v }

// fakeAlertGroups stands for internal/groups: one Alert Group, resolved by the system, with two Alerts and a
// Timeline of one entry of each kind.
type fakeAlertGroups struct {
	view      groups.View
	alerts    []groups.AlertFilter
	timelines []groups.TimelineFilter
	moved     []groups.Requester
	lists     []groups.ListRequest
	counts    []groups.Filter
	related   []*groups.ListPosition
	stats     []groups.StatisticsRequest
	open      map[string]int64
	notes     []*groups.NotePosition
	err       error
}

func (f *fakeAlertGroups) Notes(_ context.Context, id string, after *groups.NotePosition, limit int) (groups.NotePage,
	error) {
	f.notes = append(f.notes, after)
	if id != groupID {
		return groups.NotePage{}, groups.ErrNotFound
	}
	page := groups.NotePage{Notes: []groups.NoteView{{PublicID: "NEAAAAAAAAAAAA", Body: "Rolled back.",
		Author: &groups.ActorRef{Kind: "user", PublicID: "SRAAAAAAAAAAAA", Name: "gone", Login: "gone",
			Deactivated: true}, Transport: "ui", CreatedAt: t0}, {PublicID: "NEAAAAAAAAAAAB", Body: "x",
		Author:    &groups.ActorRef{Kind: "service_account", PublicID: "SAAAAAAAAAAAAA", Name: "robot"},
		Transport: "api", CreatedAt: t0}}}
	if after == nil && limit == 2 {
		page.Next = &groups.NotePosition{At: t0, ID: 9}
	}
	return page, f.err
}

func newFakeAlertGroups() *fakeAlertGroups {
	summary, reason, code, label, count := "Disk on i1 is full", "Integration lab deleted", "integration_deleted",
		"pod", int64(2)
	return &fakeAlertGroups{view: groups.View{PublicID: groupID, Number: 412, Title: "DiskFull", Summary: &summary,
		Status: groups.StatusResolved, Severity: "critical", Urgent: true,
		Route:        groups.Ref{PublicID: routeID, Name: "payments"},
		Integrations: []groups.Ref{{PublicID: "NTAAAAAAAAAAAA", Name: "lab"}}, StartedAt: t0,
		LastChangedAt: t0.Add(time.Hour), ResolvedAt: ptr(t0.Add(time.Hour)), ReopenCount: 1, ResolvedCount: 2,
		Resolution:  &groups.Resolution{By: "system", Reason: &reason, ReasonCode: &code},
		GroupLabels: map[string]string{"alertname": "DiskFull"}, CommonLabels: map[string]string{"cluster": "a"},
		CommonAnnotations: map[string]string{"summary": summary},
		Notices: []groups.Notice{{Kind: groups.NoticeReplacement, Label: &label},
			{Kind: groups.NoticeAlertsStillFiring, Count: &count},
			{Kind: groups.NoticeFiringAgainAfterManualResolve, ResolvedNumber: &count}}}}
}

func (f *fakeAlertGroups) Get(_ context.Context, id string) (groups.View, error) {
	if id != groupID {
		return groups.View{}, groups.ErrNotFound
	}
	return f.view, f.err
}

func (f *fakeAlertGroups) Alerts(_ context.Context, id string, fl groups.AlertFilter) (groups.AlertPage, error) {
	f.alerts = append(f.alerts, fl)
	if id != groupID {
		return groups.AlertPage{}, groups.ErrNotFound
	}
	reason, text, url := "resolved", "resolved by Alertmanager", "http://prometheus/graph"
	page := groups.AlertPage{Alerts: []groups.GroupAlert{
		{Fingerprint: "a", Integration: groups.Ref{PublicID: "NTAAAAAAAAAAAA", Name: "lab"},
			Labels: map[string]string{"pod": "i1"}, Annotations: map[string]string{"summary": "s"}, Firing: true,
			StartsAt: t0, LastSeenAt: t0, GroupKeys: []string{`{}:{alertname="DiskFull"}`}, SourceURL: &url},
		{Fingerprint: "b", Integration: groups.Ref{PublicID: "NTAAAAAAAAAAAA", Name: "lab"},
			Labels: map[string]string{"pod": "i2"}, StartsAt: t0, LastSeenAt: t0, ResolvedAt: ptr(t0),
			Reason: &reason, ReasonText: &text, GroupKeys: []string{}},
	}}
	if fl.After == nil {
		page.Next = &groups.AlertPosition{Rank: 1, ID: 9}
	}
	return page, f.err
}

func (f *fakeAlertGroups) Timeline(_ context.Context, id string, fl groups.TimelineFilter) (groups.TimelinePage,
	error) {
	f.timelines = append(f.timelines, fl)
	if id != groupID {
		return groups.TimelinePage{}, groups.ErrNotFound
	}
	reason, label, period := "resolved", "pod", t0.Add(-10*time.Minute)
	alice := &groups.ActorRef{Kind: "user", PublicID: "SRAAAAAAAAAAAA", Name: "Alice", Login: "alice"}
	system := groups.TimelineActor{Kind: "system", Transport: "system"}
	page := groups.TimelinePage{Entries: []groups.TimelineEntry{
		{PublicID: "TEAAAAAAAAAAA1", At: t0, Kind: groups.KindStatus, Event: "resolved", Loudness: groups.Quiet,
			Mentions: []string{}, Actor: system, Reason: &reason, From: groups.StatusFiring,
			To: groups.StatusResolved, Fingerprints: []string{"a"}, Owner: alice, PreviousOwner: alice},
		{PublicID: "TEAAAAAAAAAAA2", At: t0, Kind: groups.KindStatus, Event: "created", Loudness: groups.Loud,
			Mentions: []string{"new_alert_group"}, Actor: system, To: groups.StatusFiring,
			LabelConflicts: []string{"team"}},
		{PublicID: "TEAAAAAAAAAAA3", At: t0, Kind: groups.KindAlerts, Event: "alert_replaced", Loudness: groups.Quiet,
			Mentions: []string{}, Actor: system, ReplacedLabel: &label, Fingerprints: []string{"b"},
			LabelConflicts: []string{"team"}},
		{PublicID: "TEAAAAAAAAAAA4", At: t0, Kind: groups.KindTimers, Event: "reminder", Loudness: groups.Loud,
			Mentions: []string{"owner"}, Actor: system, NoticeNumber: ptr(int64(1)), MissedCount: ptr(int64(2))},
		{PublicID: "NEAAAAAAAAAAAA", At: t0, Kind: groups.KindNotes, Event: "note_added", Loudness: groups.Quiet,
			Mentions: []string{}, Actor: groups.TimelineActor{Kind: "user", Ref: alice, Transport: "ui"},
			Note: &groups.NoteView{PublicID: "NEAAAAAAAAAAAA", Body: "looking", Author: alice, Transport: "ui",
				CreatedAt: t0}},
		{PublicID: "DEAAAAAAAAAAAA", At: t0, Kind: groups.KindDelivery, Loudness: groups.Loud, Mentions: []string{},
			Actor: system, Delivery: &groups.DeliveryView{Event: "publication",
				Destination: groups.Ref{PublicID: "DSAAAAAAAAAAAA", Name: "ops"}}},
		{PublicID: "TEAAAAAAAAAAA5", At: t0, Kind: groups.KindSystem, System: "muster_unavailable", Actor: system,
			PeriodFrom: &period, PeriodTo: ptr(t0)},
		{PublicID: "TEAAAAAAAAAAA6", At: t0, Kind: groups.KindSystem, System: "moved_to_default_route",
			Event: "moved_to_default_route", Loudness: groups.Quiet, Mentions: []string{}, Actor: system},
	}}
	if fl.After == nil {
		page.Next = &groups.TimelinePosition{At: t0, Source: 1, ID: 3}
	}
	return page, f.err
}

func (f *fakeAlertGroups) List(_ context.Context, r groups.ListRequest) (groups.ListPage, error) {
	f.lists = append(f.lists, r)
	item := f.view
	item.Notices, item.GroupLabels, item.CommonLabels, item.CommonAnnotations = nil, nil, nil, nil
	item.LabelValues = map[string]string{"namespace": "payments"}
	page := groups.ListPage{Groups: []groups.View{item}}
	if r.After == nil {
		page.Next = &groups.ListPosition{At: t0, ID: 7}
	}
	return page, f.err
}

func (f *fakeAlertGroups) Counts(_ context.Context, fl groups.Filter) (groups.Counts, error) {
	f.counts = append(f.counts, fl)
	return groups.Counts{Firing: 1, Acknowledged: 2, Snoozed: 3, Resolved: 4, All: 10}, f.err
}

func (f *fakeAlertGroups) Related(_ context.Context, id string, after *groups.ListPosition, limit int) (
	groups.RelatedPage, error) {
	f.related = append(f.related, after)
	if id != groupID {
		return groups.RelatedPage{}, groups.ErrNotFound
	}
	hour := time.Hour
	page := groups.RelatedPage{Groups: []groups.Related{
		{PublicID: "AGBBBBBBBBBBBB", Number: 411, Status: groups.StatusResolved, StartedAt: t0.Add(-2 * time.Hour),
			Duration: &hour, Resolution: &groups.Resolution{By: "system", ReasonCode: ptr("resolved")}},
		{PublicID: "AGCCCCCCCCCCCC", Number: 413, Status: groups.StatusFiring, StartedAt: t0},
	}}
	if after == nil && limit == 2 {
		page.Next = &groups.ListPosition{At: t0, ID: 3}
	}
	return page, f.err
}

func (f *fakeAlertGroups) Statistics(_ context.Context, r groups.StatisticsRequest) (groups.Statistics, error) {
	f.stats = append(f.stats, r)
	if r.TimeZone == "Mars/Olympus" {
		return groups.Statistics{}, &groups.FieldError{Pointer: "/query/time_zone", Code: groups.CodeInvalid,
			Detail: "x"}
	}
	median := int64(1200)
	return groups.Statistics{GroupBy: r.GroupBy, From: t0.Add(-7 * 24 * time.Hour), To: t0,
		Items: []groups.StatisticsItem{{Subject: groups.Ref{PublicID: routeID, Name: "payments"}, AlertGroupCount: 3,
			TimeToResolve: groups.DurationStats{Count: 3, Median: &median, P95: &median},
			PerDay: []groups.StatisticsDay{{Date: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), AlertGroupCount: 3,
				TimeToResolve: groups.DurationStats{Count: 3, Median: &median, P95: &median}}}}}}, f.err
}

func (f *fakeAlertGroups) OpenCounts(_ context.Context, ids []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, id := range ids {
		if n, ok := f.open[id]; ok {
			out[id] = n
		}
	}
	return out, f.err
}

func (f *fakeAlertGroups) MoveOpenAlertGroups(_ context.Context, r groups.Requester, id string) (int, error) {
	f.moved = append(f.moved, r)
	switch id {
	case defaultRouteID:
		return 0, groups.ErrDefaultRoute
	case routeID:
		return 2, f.err
	}
	return 0, groups.ErrRouteNotFound
}

// The tokens of the Alert Group tests: alert-groups:read with alerts:read, as every Role holds them, and a token
// without either.
const (
	groupsReader = "mstr_pat_groups_read"
	groupsNone   = "mstr_pat_groups_none"
)

func newAlertGroupsAPI(t *testing.T) (*testAPI, *fakeAlertGroups, *fakeRoutes) {
	t.Helper()
	x, fr := newRoutesAPI(t)
	ft := x.srv.tokens.(*fakeTokens)
	owner := ft.idents[fullToken].Session
	ft.idents[groupsReader] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"alert-groups:read",
		"alerts:read"}, Transport: audit.TransportAPI, Token: &auth.Token{ID: 41, Name: "groups"}}
	ft.idents[groupsNone] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"routes:read"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 42, Name: "none"}}
	fg := newFakeAlertGroups()
	x.srv.alertGroups = fg
	return x, fg, fr
}

// TestGetAlertGroupAPI is getAlertGroup (C-09.FR-1, FR-10, FR-14): the Alert Group with its Route, Integrations,
// resolution, labels and notices, and the fields of later stories at their defaults.
func TestGetAlertGroupAPI(t *testing.T) {
	x, _, _ := newAlertGroupsAPI(t)
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID, "")
	var g gen.AlertGroup
	decodeInto(t, a, &g)
	if a.status != http.StatusOK || g.Number != 412 || g.Title != "DiskFull" ||
		g.Summary.MustGet() != "Disk on i1 is full" || g.Status != gen.AlertGroupStatusResolved || !g.Urgent ||
		g.Route.Name != "payments" || len(g.Integrations) != 1 || g.ReopenCount != 1 || g.ResolvedAlertCount != 2 ||
		g.Resolution == nil || g.Resolution.By != gen.ResolverKindSystem ||
		g.Resolution.Reason.MustGet() != "Integration lab deleted" ||
		g.Resolution.ReasonCode.MustGet() != gen.NullableResolveReasonIntegrationDeleted || g.Unclaimed ||
		g.DeliveryProblem || len(g.AllowedCommands) != 0 || (*g.GroupLabels)["alertname"] != "DiskFull" ||
		len(*g.Notices) != 3 || (*g.Notices)[0].Label.MustGet() != "pod" || (*g.Notices)[1].Count.MustGet() != 2 ||
		(*g.Notices)[2].ResolvedNumber.MustGet() != 2 || len(*g.Links) != 0 || g.Owner != nil {
		t.Errorf("alert group = %d %s", a.status, a.body)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/AGZZZZZZZZZZZZ", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d %s", a.status, a.body)
	}
	if a := x.as(t, groupsNone, http.MethodGet, "/api/v1/alert-groups/"+groupID, ""); a.status != http.StatusForbidden {
		t.Errorf("without the permission = %d", a.status)
	}
	// A person's resolution names the actor.
	x.srv.alertGroups.(*fakeAlertGroups).view.Resolution = &groups.Resolution{By: "user",
		Actor: &groups.ActorRef{Kind: "service_account", PublicID: saPublicID, Name: "bot"}}
	decodeInto(t, x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID, ""), &g)
	if g.Resolution.Actor == nil || g.Resolution.Actor.Name != "bot" || g.Resolution.Actor.Login != nil ||
		!g.Resolution.ReasonCode.IsNull() {
		t.Errorf("person's resolution %+v", g.Resolution)
	}
}

// TestListAlertGroupAlertsAPI is listAlertGroupAlerts (C-09.FR-14, C-06.FR-19): firing first, then resolved with
// their reason, by cursor, filtered by state.
func TestListAlertGroupAlertsAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/alerts?limit=2&state=firing", "")
	var list gen.AlertGroupAlertList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 2 || list.Items[0].State != gen.AlertStateFiring ||
		list.Items[0].SourceUrl.MustGet() != "http://prometheus/graph" || list.Items[0].Integration.Name != "lab" ||
		!list.Items[0].ResolveReason.IsNull() || list.Items[1].State != gen.AlertStateResolved ||
		list.Items[1].ResolveReason.MustGet() != gen.NullableResolveReasonResolved ||
		len(list.Items[1].AlertmanagerGroups) != 0 || list.NextCursor.IsNull() {
		t.Fatalf("alerts = %d %s", a.status, a.body)
	}
	if f := fg.alerts[0]; f.State != "firing" || f.Limit != 2 || f.After != nil {
		t.Errorf("filter %+v", f)
	}
	cursor := list.NextCursor.MustGet()
	a = x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/alerts?state=firing&cursor="+cursor, "")
	decodeInto(t, a, &list)
	if f := fg.alerts[1]; a.status != http.StatusOK || f.After == nil || *f.After != (groups.AlertPosition{Rank: 1,
		ID: 9}) || !list.NextCursor.IsNull() {
		t.Errorf("next page %d %+v", a.status, f)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/alerts?cursor="+cursor, ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("a cursor of another filter = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/AGZZZZZZZZZZZZ/alerts", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
}

// TestGetAlertGroupTimelineAPI is getAlertGroupTimeline (C-09.FR-11, FR-14): every kind of entry with its actor,
// Transport and, for a lifecycle event, its event, loudness and Mentions; newest first by default, by cursor, filtered
// by kind.
func TestGetAlertGroupTimelineAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/timeline?kind=status&kind=notes", "")
	var raw struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(a.body, &raw); err != nil || a.status != http.StatusOK || len(raw.Items) != 8 ||
		raw.NextCursor == nil {
		t.Fatalf("timeline = %d %s", a.status, a.body)
	}
	if f := fg.timelines[0]; f.Ascending || !slices.Equal(f.Kinds, []groups.Kind{groups.KindStatus, groups.KindNotes}) {
		t.Errorf("filter %+v", f)
	}
	var list gen.TimelineEntryList
	decodeInto(t, a, &list)
	resolved, err := list.Items[0].AsTimelineStatusEntry()
	if err != nil || resolved.Event != gen.LifecycleEventResolved || resolved.Loudness != gen.Quiet ||
		len(resolved.Mentions) != 0 || resolved.FromStatus.MustGet() != gen.NullableAlertGroupStatusFiring ||
		*resolved.ToStatus != gen.AlertGroupStatusResolved || resolved.Reason.MustGet() != "resolved" ||
		resolved.Owner == nil || resolved.Owner.Name != "Alice" || resolved.PreviousOwner == nil ||
		resolved.Fingerprints == nil || (*resolved.Fingerprints)[0] != "a" || resolved.Actor.Kind != gen.ActorKindSystem ||
		*resolved.Actor.Transport != gen.TransportSystem || !resolved.Actor.Id.IsNull() {
		t.Errorf("resolved %+v, %v", resolved, err)
	}
	created, _ := list.Items[1].AsTimelineStatusEntry()
	if !created.FromStatus.IsNull() || len(*created.LabelConflicts) != 1 || created.Mentions[0] != "new_alert_group" {
		t.Errorf("created %+v", created)
	}
	replaced, _ := list.Items[2].AsTimelineAlertsEntry()
	if replaced.ReplacedLabel.MustGet() != "pod" || (*replaced.Fingerprints)[0] != "b" || len(*replaced.LabelConflicts) != 1 {
		t.Errorf("replaced %+v", replaced)
	}
	timer, _ := list.Items[3].AsTimelineTimersEntry()
	if timer.NoticeNumber.MustGet() != 1 || timer.MissedCount.MustGet() != 2 {
		t.Errorf("timer %+v", timer)
	}
	note, _ := list.Items[4].AsTimelineNoteEntry()
	if note.Note.Body != "looking" || note.Note.Author.Name != "Alice" || note.Actor.Name.MustGet() != "Alice" ||
		note.Actor.Id.MustGet() != "SRAAAAAAAAAAAA" {
		t.Errorf("note %+v", note)
	}
	delivery, _ := list.Items[5].AsTimelineDeliveryEntry()
	if delivery.Destination.Name != "ops" || delivery.DeliveryEvent != gen.DeliveryEventKindPublication || delivery.Loudness != gen.Loud {
		t.Errorf("delivery %+v", delivery)
	}
	down, _ := list.Items[6].AsTimelineSystemEntry()
	if down.SystemEvent != "muster_unavailable" || down.Event != nil || down.Mentions != nil ||
		!down.PeriodFrom.MustGet().Equal(t0.Add(-10*time.Minute)) {
		t.Errorf("downtime %+v", down)
	}
	moved, _ := list.Items[7].AsTimelineSystemEntry()
	if moved.Event == nil || *moved.Event != gen.LifecycleEventMovedToDefaultRoute || moved.Mentions == nil ||
		len(*moved.Mentions) != 0 {
		t.Errorf("moved %+v", moved)
	}
	a = x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+
		"/timeline?kind=status&kind=notes&order=asc", "")
	if a.status != http.StatusOK || !fg.timelines[1].Ascending {
		t.Errorf("ascending = %d", a.status)
	}
	a = x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/timeline?kind=status&kind=notes&cursor="+
		*raw.NextCursor, "")
	if f := fg.timelines[2]; a.status != http.StatusOK || f.After == nil || f.After.Source != 1 || f.After.ID != 3 ||
		!f.After.At.Equal(t0) {
		t.Errorf("next page %d %+v", a.status, f)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/timeline?cursor="+
		*raw.NextCursor, ""); a.status != http.StatusBadRequest {
		t.Errorf("a cursor of another filter = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/AGZZZZZZZZZZZZ/timeline", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
	if _, err := timelineEntryOf(groups.TimelineEntry{Kind: "unknown"}); err != nil {
		t.Errorf("an unknown kind encodes as a system entry: %v", err)
	}
}

// TestMoveAndDeleteRouteAPI is C-09.FR-19 and C-09.AC-9 at the API: deleteRoute refuses a Route with open Alert
// Groups as route-has-open-alert-groups with their count; moveOpenAlertGroups moves them with routes:write, refuses
// the Default route and an unknown Route.
func TestMoveAndDeleteRouteAPI(t *testing.T) {
	x, fg, fr := newAlertGroupsAPI(t)
	fr.err = &routing.OpenAlertGroupsError{Count: 3}
	a := x.as(t, routesWriter, http.MethodDelete, "/api/v1/routes/"+routeID, "")
	if a.status != http.StatusConflict || !strings.HasSuffix(a.json(t)["type"].(string), "/route-has-open-alert-groups") ||
		a.json(t)["open_alert_group_count"] != 3.0 || a.json(t)["title"] != "Route has open Alert Groups" {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	fr.err = nil
	a = x.as(t, routesWriter, http.MethodPost, "/api/v1/routes/"+routeID+"/move-open-alert-groups", "")
	if a.status != http.StatusOK || a.json(t)["moved"] != 2.0 || fg.moved[0].Actor.TokenName != "routes" ||
		fg.moved[0].Transport != audit.TransportAPI {
		t.Errorf("move = %d %s %+v", a.status, a.body, fg.moved)
	}
	if strings.Contains(string(x.as(t, routesWriter, http.MethodDelete, "/api/v1/routes/"+defaultRouteID, "").body),
		"open_alert_group_count") {
		t.Error("another problem carries the count")
	}
	for id, want := range map[string]int{defaultRouteID: http.StatusConflict, "RTZZZZZZZZZZZZ": http.StatusNotFound} {
		if a := x.as(t, routesWriter, http.MethodPost, "/api/v1/routes/"+id+"/move-open-alert-groups", ""); a.status !=
			want {
			t.Errorf("move %s = %d %s", id, a.status, a.body)
		}
	}
	if a := x.as(t, routesReader, http.MethodPost, "/api/v1/routes/"+routeID+"/move-open-alert-groups", ""); a.status !=
		http.StatusForbidden {
		t.Errorf("without routes:write = %d", a.status)
	}
	fg.err = errBoom
	if a := x.as(t, routesWriter, http.MethodPost, "/api/v1/routes/"+routeID+"/move-open-alert-groups", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failure = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID, ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed read = %d", a.status)
	}
	for _, path := range []string{"/alerts", "/timeline"} {
		if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+path, ""); a.status !=
			http.StatusInternalServerError {
			t.Errorf("a failed %s = %d", path, a.status)
		}
	}
}

// TestListAlertGroupsAPI is listAlertGroups (C-09.FR-13): every filter reaches internal/groups, the items carry
// label_values and details_removed without the labels of the page, the cursor keeps the sort, and the filter of a
// later story answers 422 unsupported.
func TestListAlertGroupsAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	q := url.Values{"status": {"firing", "resolved"}, "route": {routeID}, "integration": {"NTAAAAAAAAAAAA"},
		"severity": {"critical"}, "urgent": {"true"}, "resolved_by": {"system"}, "resolve_reason": {"gone"},
		"reopened": {"false"}, "label": {`namespace="payments"`, `pod=~"api-.*"`}, "from": {"2026-10-01T00:00:00Z"},
		"to": {"2026-10-08T00:00:00Z"}, "number": {"412"}, "q": {"postgres"}, "sort": {"last_changed_at"},
		"label_columns": {"namespace"}, "limit": {"5"}}
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups?"+q.Encode(), "")
	var list gen.AlertGroupList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 1 || list.NextCursor.IsNull() ||
		(*list.Items[0].LabelValues)["namespace"] != "payments" || list.Items[0].GroupLabels != nil ||
		list.Items[0].Notices != nil || list.Items[0].DetailsRemoved == nil || *list.Items[0].DetailsRemoved {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	r := fg.lists[0]
	if !slices.Equal(r.Statuses, []groups.Status{groups.StatusFiring, groups.StatusResolved}) ||
		!slices.Equal(r.Routes, []string{routeID}) || !slices.Equal(r.Integrations, []string{"NTAAAAAAAAAAAA"}) ||
		len(r.Severities) != 1 || !*r.Urgent || *r.ResolvedBy != "system" || *r.ResolveReason != "gone" ||
		*r.Reopened || len(r.Matchers) != 2 || r.Matchers[1].String() != `pod=~"api-.*"` || !r.From.Equal(time.Date(2026, 10, 1, 0,
		0, 0, 0, time.UTC)) || *r.Number != 412 || r.Query != "postgres" || r.Sort != groups.SortChanged ||
		!slices.Equal(r.LabelColumns, []string{"namespace"}) || r.Limit != 5 || r.After != nil {
		t.Errorf("request %+v", r)
	}
	cursor := list.NextCursor.MustGet()
	a = x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups?sort=last_changed_at&cursor="+cursor, "")
	if r := fg.lists[1]; a.status != http.StatusOK || r.After == nil || r.After.ID != 7 || !r.After.At.Equal(t0) ||
		r.Sort != groups.SortChanged || r.Statuses != nil {
		t.Errorf("next page %d %+v", a.status, r)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups?cursor="+cursor, ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("a cursor of another sort = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups", ""); a.status != http.StatusOK ||
		fg.lists[2].Sort != groups.SortStartedDesc || fg.lists[2].Limit != 50 {
		t.Errorf("defaults = %d %+v", a.status, fg.lists[2])
	}
	a = x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups?unclaimed=true", "")
	var p gen.Problem
	decodeInto(t, a, &p)
	if a.status != http.StatusUnprocessableEntity || p.Errors == nil || (*p.Errors)[0].Code != "unsupported" ||
		(*p.Errors)[0].Pointer != "/query/unclaimed" {
		t.Errorf("unclaimed = %d %s", a.status, a.body)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-group-counts?unclaimed=true", ""); a.status !=
		http.StatusUnprocessableEntity {
		t.Errorf("counts unclaimed = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups?label=pod%3D~%22%28%22", ""); a.status !=
		http.StatusBadRequest || a.json(t)["errors"].([]any)[0].(map[string]any)["code"] != "invalid_regex" {
		t.Errorf("a bad matcher = %d %s", a.status, a.body)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-group-counts?label=pod", ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("counts with a bad matcher = %d %s", a.status, a.body)
	}
	if a := x.as(t, groupsNone, http.MethodGet, "/api/v1/alert-groups", ""); a.status != http.StatusForbidden {
		t.Errorf("without the permission = %d", a.status)
	}
	fg.err = &groups.FieldError{Pointer: "/query/route", Code: groups.CodeUnknownID, Detail: "No such route."}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups?route=RTZZZZZZZZZZZZ", ""); a.status !=
		http.StatusUnprocessableEntity || a.json(t)["errors"].([]any)[0].(map[string]any)["code"] != "unknown_id" {
		t.Errorf("an unknown route = %d %s", a.status, a.body)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-group-counts", ""); a.status !=
		http.StatusUnprocessableEntity {
		t.Errorf("counts failing = %d %s", a.status, a.body)
	}
}

// TestOwnerFiltersAPI is C-10.FR-13 and C-10.AC-19 on the API: owner and snoozed_no_end reach the list and the counts
// with the caller, whom owner=me names; a Service account's owner=me is the 422 unsupported that groups reports.
func TestOwnerFiltersAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	for _, path := range []string{"/api/v1/alert-groups", "/api/v1/alert-group-counts"} {
		a := x.as(t, groupsReader, http.MethodGet, path+"?owner=me&snoozed_no_end=true", "")
		if a.status != http.StatusOK {
			t.Fatalf("%s = %d %s", path, a.status, a.body)
		}
	}
	for _, f := range []groups.Filter{fg.lists[0].Filter, fg.counts[0]} {
		if f.Owner != groups.OwnerMe || f.Me.Kind != audit.ActorUser || f.Me.ID == 0 || f.SnoozedNoEnd == nil ||
			!*f.SnoozedNoEnd {
			t.Errorf("filter %+v", f)
		}
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups?owner=none", ""); a.status != http.StatusOK ||
		fg.lists[1].Owner != groups.OwnerNone {
		t.Errorf("owner=none = %d %+v", a.status, fg.lists[1].Filter)
	}
	if a := x.as(t, serviceToken, http.MethodGet, "/api/v1/alert-group-counts?owner=SRAAAAAAAAAAAA", ""); a.status !=
		http.StatusOK || fg.counts[1].Owner != "SRAAAAAAAAAAAA" || fg.counts[1].Me.Kind != audit.ActorServiceAccount {
		t.Errorf("owner=<id> = %d %+v", a.status, fg.counts[1])
	}
	fg.err = &groups.FieldError{Pointer: "/query/owner", Code: groups.CodeUnsupported, Detail: "x"}
	a := x.as(t, serviceToken, http.MethodGet, "/api/v1/alert-groups?owner=me", "")
	if a.status != http.StatusUnprocessableEntity || a.json(t)["errors"].([]any)[0].(map[string]any)["code"] !=
		"unsupported" {
		t.Errorf("owner=me by a service account = %d %s", a.status, a.body)
	}
}

// TestDeliveryProblemAPI is C-13.FR-12 and C-13.AC-10 on the API: delivery_problem reaches the list and the counts,
// and the Alert Group and the list items carry delivery_problem.
func TestDeliveryProblemAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	fg.view.DeliveryProblem = true
	for _, path := range []string{"/api/v1/alert-groups", "/api/v1/alert-group-counts"} {
		for _, v := range []string{"true", "false"} {
			if a := x.as(t, groupsReader, http.MethodGet, path+"?delivery_problem="+v, ""); a.status != http.StatusOK {
				t.Fatalf("%s %s = %d %s", path, v, a.status, a.body)
			}
		}
	}
	for i, f := range []groups.Filter{fg.lists[0].Filter, fg.lists[1].Filter, fg.counts[0], fg.counts[1]} {
		if f.DeliveryProblem == nil || *f.DeliveryProblem != (i%2 == 0) {
			t.Errorf("filter %d %+v", i, f)
		}
	}
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups", "")
	var list gen.AlertGroupList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 1 || !list.Items[0].DeliveryProblem ||
		fg.lists[2].DeliveryProblem != nil {
		t.Errorf("list = %d %s", a.status, a.body)
	}
	a = x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID, "")
	var g gen.AlertGroup
	decodeInto(t, a, &g)
	if a.status != http.StatusOK || !g.DeliveryProblem {
		t.Errorf("alert group = %d %s", a.status, a.body)
	}
}

// TestAlertGroupNotesAPI is listAlertGroupNotes and createAlertGroupNote (C-10.FR-8, C-04.AC-6, C-10.AC-18): the
// Notes by cursor with their author, readable with alert-groups:read; a Note created through the dispatcher, which
// checks alert-groups:note, so a reader is refused with 403; a Note longer than alert_group.note_max_length is
// refused by the request validation with too_long.
func TestAlertGroupNotesAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	fc := &fakeCommands{}
	x.srv.commands = fc
	ft := x.srv.tokens.(*fakeTokens)
	ft.idents["mstr_pat_note"] = &auth.Identity{Session: ft.idents[fullToken].Session,
		Permissions: []auth.Permission{"alert-groups:read", "alert-groups:note"}, Transport: audit.TransportAPI,
		Token: &auth.Token{ID: 43, Name: "note"}}
	path := "/api/v1/alert-groups/" + groupID + "/notes"
	a := x.as(t, groupsReader, http.MethodGet, path+"?limit=2", "")
	var list gen.NoteList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 2 || !list.Items[0].Author.Deactivated ||
		list.Items[0].Author.Kind != gen.ActorRefKindUser || list.Items[1].Transport != gen.TransportApi ||
		list.NextCursor.IsNull() {
		t.Fatalf("notes = %d %s", a.status, a.body)
	}
	a = x.as(t, groupsReader, http.MethodGet, path+"?cursor="+list.NextCursor.MustGet(), "")
	if a.status != http.StatusOK || fg.notes[1] == nil || fg.notes[1].ID != 9 || !fg.notes[1].At.Equal(t0) ||
		a.json(t)["next_cursor"] != nil {
		t.Errorf("second page = %d %s", a.status, a.body)
	}
	if a := x.as(t, groupsReader, http.MethodGet, path+"?cursor=e30", ""); a.status != http.StatusBadRequest {
		t.Errorf("a bad cursor = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/AGZZZZZZZZZZZZ/notes", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("an unknown alert group = %d", a.status)
	}
	if a := x.as(t, groupsNone, http.MethodGet, path, ""); a.status != http.StatusForbidden {
		t.Errorf("notes without alert-groups:read = %d", a.status)
	}

	a = x.as(t, "mstr_pat_note", http.MethodPost, path, `{"body":"Restarted the disk daemon."}`)
	var n gen.Note
	decodeInto(t, a, &n)
	if a.status != http.StatusCreated || n.Body != "Restarted the disk daemon." || n.Author.Name != "robot" ||
		len(fc.notes) != 1 || fc.callers[0].Actor.TokenName != "note" || fc.calls[0] != "add_note "+groupID {
		t.Errorf("create = %d %s, %v", a.status, a.body, fc.calls)
	}
	// The dispatcher checks the Permission: the middleware lets a reader through, the command layer refuses.
	if a := x.as(t, groupsReader, http.MethodPost, path, `{"body":"x"}`); a.status != http.StatusForbidden ||
		fc.calls[len(fc.calls)-1] != "add_note "+groupID {
		t.Errorf("a reader's note = %d %s", a.status, a.body)
	}
	long := strings.Repeat("x", 4001)
	if a := x.as(t, "mstr_pat_note", http.MethodPost, path, `{"body":"`+long+`"}`); a.status !=
		http.StatusBadRequest || a.json(t)["errors"].([]any)[0].(map[string]any)["code"] != "too_long" {
		t.Errorf("a note too long = %d %s", a.status, a.body)
	}
	fc.err = errBoom
	if a := x.as(t, "mstr_pat_note", http.MethodPost, path, `{"body":"x"}`); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failing note = %d", a.status)
	}
}

// TestAlertGroupCountsAPI is getAlertGroupCounts: the counts per status tab for the filters of the list.
func TestAlertGroupCountsAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-group-counts?label=namespace%3D%22payments%22&q=%2312", "")
	var c gen.AlertGroupCounts
	decodeInto(t, a, &c)
	if a.status != http.StatusOK || c != (gen.AlertGroupCounts{Firing: 1, Acknowledged: 2, Snoozed: 3, Resolved: 4,
		All: 10}) || len(fg.counts[0].Matchers) != 1 || fg.counts[0].Query != "#12" {
		t.Errorf("counts = %d %s %+v", a.status, a.body, fg.counts)
	}
}

// TestListRelatedAlertGroupsAPI is listRelatedAlertGroups (C-09.FR-20): number, status, start, duration and who
// resolved them, by cursor.
func TestListRelatedAlertGroupsAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/related?limit=2", "")
	var list gen.RelatedAlertGroupList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 2 || list.Items[0].Number != 411 ||
		list.Items[0].DurationSeconds.MustGet() != 3600 || list.Items[0].Resolution.By != gen.ResolverKindSystem ||
		!list.Items[1].DurationSeconds.IsNull() || list.Items[1].Resolution != nil || list.NextCursor.IsNull() {
		t.Fatalf("related = %d %s", a.status, a.body)
	}
	a = x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/related?cursor="+
		list.NextCursor.MustGet(), "")
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || fg.related[1] == nil || fg.related[1].ID != 3 || !list.NextCursor.IsNull() {
		t.Errorf("next page %d %+v", a.status, fg.related)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID+"/related?cursor=x", ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("a bad cursor = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/AGZZZZZZZZZZZZ/related", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
}

// TestAlertGroupStatisticsAPI is getAlertGroupStatistics (C-09.FR-15): totals and days per subject, durations null
// without any, and a field error as 422.
func TestAlertGroupStatisticsAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-group-statistics?group_by=route&route="+routeID+
		"&integration=NTAAAAAAAAAAAA&time_zone=Europe/Berlin&from=2026-10-01T00:00:00Z&to=2026-10-08T00:00:00Z", "")
	var st gen.AlertGroupStatistics
	decodeInto(t, a, &st)
	if a.status != http.StatusOK || st.GroupBy != gen.AlertGroupStatisticsGroupByRoute || len(st.Items) != 1 ||
		st.Items[0].AlertGroupCount != 3 || st.Items[0].TimeToResolve.MedianSeconds.MustGet() != 1200 ||
		!st.Items[0].TimeToAcknowledge.MedianSeconds.IsNull() || st.Items[0].PerDay[0].Date.String() != "2026-10-07" {
		t.Fatalf("statistics = %d %s", a.status, a.body)
	}
	if r := fg.stats[0]; r.GroupBy != "route" || r.TimeZone != "Europe/Berlin" || !slices.Equal(r.Routes,
		[]string{routeID}) || len(r.Integrations) != 1 || r.From == nil || r.To == nil {
		t.Errorf("request %+v", r)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-group-statistics?group_by=route&time_zone=Mars/Olympus",
		""); a.status != http.StatusUnprocessableEntity {
		t.Errorf("a bad zone = %d %s", a.status, a.body)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-group-statistics", ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("without group_by = %d", a.status)
	}
	fg.err = errBoom
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-group-statistics?group_by=integration", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failure = %d", a.status)
	}
}

// TestGetAlertGroupDetailsRemovedAPI: the notice details_removed carries the retention period.
func TestGetAlertGroupDetailsRemovedAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	days := int64(90)
	fg.view.DetailsRemoved = true
	fg.view.Notices = []groups.Notice{{Kind: groups.NoticeDetailsRemoved, RetentionDays: &days}}
	var g gen.AlertGroup
	decodeInto(t, x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID, ""), &g)
	if g.DetailsRemoved == nil || !*g.DetailsRemoved || len(*g.Notices) != 1 ||
		(*g.Notices)[0].Kind != gen.DetailsRemoved || (*g.Notices)[0].RetentionDays.MustGet() != 90 {
		t.Errorf("alert group %+v", g)
	}
}
