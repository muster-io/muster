// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	err       error
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
