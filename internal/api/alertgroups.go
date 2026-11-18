// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/oapi-codegen/nullable"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/escalation"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/organization"
)

// AlertGroups is what the API needs of internal/groups: the list with its counts, one Alert Group, its Alerts, its
// Timeline, its Notes and its related Alert Groups, the statistics with those by people, the open count of
// Integrations, the move of a Route's open Alert Groups to the Default route, and the ends of the Snooze presets.
type AlertGroups interface {
	List(ctx context.Context, r groups.ListRequest) (groups.ListPage, error)
	Counts(ctx context.Context, f groups.Filter) (groups.Counts, error)
	Get(ctx context.Context, publicID string) (groups.View, error)
	Alerts(ctx context.Context, publicID string, f groups.AlertFilter) (groups.AlertPage, error)
	Timeline(ctx context.Context, publicID string, f groups.TimelineFilter) (groups.TimelinePage, error)
	Notes(ctx context.Context, publicID string, after *groups.NotePosition, limit int) (groups.NotePage, error)
	Related(ctx context.Context, publicID string, after *groups.ListPosition, limit int) (groups.RelatedPage, error)
	Statistics(ctx context.Context, r groups.StatisticsRequest) (groups.Statistics, error)
	ActorStatistics(ctx context.Context, f groups.Filter) (groups.ActorStatistics, error)
	OpenCounts(ctx context.Context, integrations []string) (map[string]int64, error)
	MoveOpenAlertGroups(ctx context.Context, r groups.Requester, routeID string) (int, error)
	SnoozePresets(ctx context.Context, c groups.Caller, timeZone string) (groups.PresetList, error)
}

// permissionAlertGroupsRead reads Alert Groups; the live-update hints about them go only to those who hold it.
const permissionAlertGroupsRead auth.Permission = "alert-groups:read"

// The cursors of the Alert Group lists.
const (
	alertGroupsCursor        = "alert-groups"
	alertGroupAlertsCursor   = "alert-group-alerts"
	alertGroupTimelineCursor = "alert-group-timeline"
	alertGroupNotesCursor    = "alert-group-notes"
	relatedAlertGroupsCursor = "related-alert-groups"
)

// alertGroupKey is the position of an Alert Group in its cursor: the time it is sorted by and its id.
type alertGroupKey struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

// filterParams are the filter parameters that listAlertGroups, getAlertGroupCounts and the statistics share.
type filterParams struct {
	route, integration    *[]string
	severity              *[]gen.SeverityLevel
	urgent, reopened      *bool
	resolvedBy            *gen.ResolverKind
	resolveReason         *gen.ResolveReason
	label                 *[]string
	owner                 *string
	assignee              *string
	team                  *string
	firstAcknowledgedBy   *string
	snoozedNoEnd, problem *bool
	unclaimed             *bool
	from, to              *time.Time
	q                     *string
	// me is the caller, whom owner=me, assignee=me, team=mine and first_acknowledged_by=me name.
	me audit.Actor
}

// filterOf is the groups.Filter of the parameters.
func filterOf(p filterParams) (groups.Filter, error) {
	f := groups.Filter{Urgent: p.urgent, Reopened: p.reopened, From: p.from, To: p.to, SnoozedNoEnd: p.snoozedNoEnd,
		DeliveryProblem: p.problem, Unclaimed: p.unclaimed, Me: p.me}
	if p.owner != nil {
		f.Owner = *p.owner
	}
	if p.assignee != nil {
		f.Assignee = *p.assignee
	}
	if p.team != nil {
		f.Team = *p.team
	}
	if p.firstAcknowledgedBy != nil {
		f.FirstAcknowledgedBy = *p.firstAcknowledgedBy
	}
	if p.route != nil {
		f.Routes = *p.route
	}
	if p.integration != nil {
		f.Integrations = *p.integration
	}
	if p.severity != nil {
		for _, l := range *p.severity {
			f.Severities = append(f.Severities, organization.SeverityLevel(l))
		}
	}
	if p.resolvedBy != nil {
		by := string(*p.resolvedBy)
		f.ResolvedBy = &by
	}
	if p.resolveReason != nil {
		reason := string(*p.resolveReason)
		f.ResolveReason = &reason
	}
	if p.label != nil {
		for i, raw := range *p.label {
			m, err := matchers.Parse(raw)
			if err != nil {
				return groups.Filter{}, matcherProblem(i, err)
			}
			f.Matchers = append(f.Matchers, m)
		}
	}
	if p.q != nil {
		f.Query = *p.q
	}
	return f, nil
}

// ListAlertGroups is listAlertGroups: a page of the Alert Group list with its filters, range, search and order.
func (s *Server) ListAlertGroups(ctx context.Context, req gen.ListAlertGroupsRequestObject) (
	gen.ListAlertGroupsResponseObject, error) {
	p := req.Params
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	f, err := filterOf(filterParams{route: p.Route, integration: p.Integration, severity: p.Severity,
		urgent: p.Urgent, reopened: p.Reopened, resolvedBy: p.ResolvedBy, resolveReason: p.ResolveReason,
		label: p.Label, owner: p.Owner, assignee: p.Assignee, team: p.Team, snoozedNoEnd: p.SnoozedNoEnd,
		problem: p.DeliveryProblem, unclaimed: p.Unclaimed, from: p.From, to: p.To, q: p.Q, me: c.Actor})
	if err != nil {
		return nil, err
	}
	r := groups.ListRequest{Filter: f, Sort: groups.SortStartedDesc, Limit: pageSize(p.Limit)}
	if p.Status != nil {
		for _, st := range *p.Status {
			r.Statuses = append(r.Statuses, groups.Status(st))
		}
	}
	if p.Number != nil {
		n := int64(*p.Number)
		r.Number = &n
	}
	if p.Sort != nil {
		r.Sort = groups.Sort(*p.Sort)
	}
	if p.LabelColumns != nil {
		r.LabelColumns = *p.LabelColumns
	}
	list := alertGroupsCursor + ":" + string(r.Sort)
	var key alertGroupKey
	if ok, err := decodeCursor(p.Cursor, list, &key); err != nil {
		return nil, err
	} else if ok {
		r.After = &groups.ListPosition{At: key.At, ID: key.ID}
	}
	page, err := s.alertGroups.List(ctx, r)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(page.Groups))
	for i, v := range page.Groups {
		ids[i] = v.PublicID
	}
	chains, err := s.groupChains(ctx, ids...)
	if err != nil {
		return nil, err
	}
	out := gen.AlertGroupList{Items: make([]gen.AlertGroup, 0, len(page.Groups))}
	for _, v := range page.Groups {
		out.Items = append(out.Items, alertGroupOf(v, c, chains[v.PublicID]))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(list, alertGroupKey{At: page.Next.At, ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListAlertGroups200JSONResponse(out), nil
}

// GetAlertGroupCounts is getAlertGroupCounts: the Alert Groups of the list's filters per status tab.
func (s *Server) GetAlertGroupCounts(ctx context.Context, req gen.GetAlertGroupCountsRequestObject) (
	gen.GetAlertGroupCountsResponseObject, error) {
	p := req.Params
	me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	f, err := filterOf(filterParams{route: p.Route, integration: p.Integration, severity: p.Severity,
		urgent: p.Urgent, reopened: p.Reopened, resolvedBy: p.ResolvedBy, resolveReason: p.ResolveReason,
		label: p.Label, owner: p.Owner, assignee: p.Assignee, team: p.Team, snoozedNoEnd: p.SnoozedNoEnd,
		problem: p.DeliveryProblem, unclaimed: p.Unclaimed, from: p.From, to: p.To, q: p.Q, me: me.Actor})
	if err != nil {
		return nil, err
	}
	c, err := s.alertGroups.Counts(ctx, f)
	if err != nil {
		return nil, err
	}
	return gen.GetAlertGroupCounts200JSONResponse{Firing: int(c.Firing), Acknowledged: int(c.Acknowledged),
		Snoozed: int(c.Snoozed), Resolved: int(c.Resolved), All: int(c.All)}, nil
}

// ListRelatedAlertGroups is listRelatedAlertGroups: the other Alert Groups of the same Route and Group key values,
// newest first.
func (s *Server) ListRelatedAlertGroups(ctx context.Context, req gen.ListRelatedAlertGroupsRequestObject) (
	gen.ListRelatedAlertGroupsResponseObject, error) {
	p := req.Params
	var after *groups.ListPosition
	var key alertGroupKey
	if ok, err := decodeCursor(p.Cursor, relatedAlertGroupsCursor, &key); err != nil {
		return nil, err
	} else if ok {
		after = &groups.ListPosition{At: key.At, ID: key.ID}
	}
	page, err := s.alertGroups.Related(ctx, req.AlertGroupId, after, pageSize(p.Limit))
	if err != nil {
		return nil, err
	}
	out := gen.RelatedAlertGroupList{Items: make([]gen.RelatedAlertGroup, 0, len(page.Groups))}
	for _, g := range page.Groups {
		item := gen.RelatedAlertGroup{Id: g.PublicID, Number: int(g.Number), Status: gen.AlertGroupStatus(g.Status),
			StartedAt: g.StartedAt, Resolution: resolvedByOf(g.Resolution)}
		if g.Duration != nil {
			item.DurationSeconds.Set(int(g.Duration.Seconds()))
		} else {
			item.DurationSeconds.SetNull()
		}
		out.Items = append(out.Items, item)
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(relatedAlertGroupsCursor, alertGroupKey{At: page.Next.At, ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListRelatedAlertGroups200JSONResponse(out), nil
}

// GetAlertGroupStatistics is getAlertGroupStatistics: per Route, per Integration or per Team, totals and days, of the
// Alert Groups that started in the period and match the filters.
func (s *Server) GetAlertGroupStatistics(ctx context.Context, req gen.GetAlertGroupStatisticsRequestObject) (
	gen.GetAlertGroupStatisticsResponseObject, error) {
	p := req.Params
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	f, err := filterOf(filterParams{route: p.Route, integration: p.Integration, severity: p.Severity,
		urgent: p.Urgent, reopened: p.Reopened, label: p.Label, team: p.Team,
		firstAcknowledgedBy: p.FirstAcknowledgedBy, from: p.From, to: p.To, me: c.Actor})
	if err != nil {
		return nil, err
	}
	r := groups.StatisticsRequest{GroupBy: string(p.GroupBy), Filter: f}
	if p.TimeZone != nil {
		r.TimeZone = *p.TimeZone
	}
	st, err := s.alertGroups.Statistics(ctx, r)
	if err != nil {
		return nil, err
	}
	out := gen.AlertGroupStatistics{GroupBy: gen.AlertGroupStatisticsGroupBy(st.GroupBy), From: st.From, To: st.To,
		Items: make([]gen.AlertGroupStatisticsItem, 0, len(st.Items))}
	for _, it := range st.Items {
		item := gen.AlertGroupStatisticsItem{Subject: gen.EntityRef{Id: it.Subject.PublicID, Name: it.Subject.Name},
			AlertGroupCount: int(it.AlertGroupCount), TimeToAcknowledge: durationStatsOf(it.TimeToAcknowledge),
			TimeToResolve: durationStatsOf(it.TimeToResolve), PerDay: make([]gen.StatisticsDay, 0, len(it.PerDay))}
		for _, d := range it.PerDay {
			item.PerDay = append(item.PerDay, gen.StatisticsDay{Date: openapi_types.Date{Time: d.Date},
				AlertGroupCount: int(d.AlertGroupCount), TimeToAcknowledge: durationStatsOf(d.TimeToAcknowledge),
				TimeToResolve: durationStatsOf(d.TimeToResolve)})
		}
		out.Items = append(out.Items, item)
	}
	return gen.GetAlertGroupStatistics200JSONResponse(out), nil
}

// GetAlertGroupActorStatistics is getAlertGroupActorStatistics: by people, of the Alert Groups that started in the
// period and match the filters, with the item of those whose first acknowledgement names nobody, actor null.
func (s *Server) GetAlertGroupActorStatistics(ctx context.Context, req gen.GetAlertGroupActorStatisticsRequestObject) (
	gen.GetAlertGroupActorStatisticsResponseObject, error) {
	p := req.Params
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	f, err := filterOf(filterParams{route: p.Route, integration: p.Integration, severity: p.Severity,
		urgent: p.Urgent, reopened: p.Reopened, label: p.Label, team: p.Team,
		firstAcknowledgedBy: p.FirstAcknowledgedBy, from: p.From, to: p.To, me: c.Actor})
	if err != nil {
		return nil, err
	}
	st, err := s.alertGroups.ActorStatistics(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.AlertGroupActorStatistics{From: st.From, To: st.To,
		Items: make([]gen.AlertGroupActorStatisticsItem, 0, len(st.Items))}
	for _, it := range st.Items {
		item := gen.AlertGroupActorStatisticsItem{AcknowledgedFirstCount: int(it.AcknowledgedFirst),
			ResolvedCount: int(it.Resolved), TimeToAcknowledge: durationStatsOf(it.TimeToAcknowledge)}
		if it.Actor != nil {
			item.Actor.Set(actorRefOf(*it.Actor))
		} else {
			item.Actor.SetNull()
		}
		out.Items = append(out.Items, item)
	}
	return gen.GetAlertGroupActorStatistics200JSONResponse(out), nil
}

// ListSnoozePresets is listSnoozePresets (C-10.FR-18): the end of each Snooze preset now, in the time zone of the
// request or the caller's, which the answer names; an unknown time zone is invalid_format at /query/time_zone.
func (s *Server) ListSnoozePresets(ctx context.Context, req gen.ListSnoozePresetsRequestObject) (
	gen.ListSnoozePresetsResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var zone string
	if req.Params.TimeZone != nil {
		zone = *req.Params.TimeZone
	}
	l, err := s.alertGroups.SnoozePresets(ctx, c, zone)
	if err != nil {
		return nil, err
	}
	out := gen.SnoozePresetList{TimeZone: l.TimeZone, WorkdayStart: l.WorkdayStart, WeekStart: gen.Weekday(l.WeekStart),
		Items: make([]gen.SnoozePresetEnd, 0, len(l.Ends))}
	for _, e := range l.Ends {
		out.Items = append(out.Items, gen.SnoozePresetEnd{Preset: gen.SnoozePreset(e.Preset), Until: e.Until})
	}
	return gen.ListSnoozePresets200JSONResponse(out), nil
}

// durationStatsOf is the API form of durations: median and 95th percentile are null without any.
func durationStatsOf(d groups.DurationStats) gen.DurationStats {
	out := gen.DurationStats{Count: int(d.Count)}
	if d.Median != nil {
		out.MedianSeconds.Set(int(*d.Median))
	} else {
		out.MedianSeconds.SetNull()
	}
	if d.P95 != nil {
		out.P95Seconds.Set(int(*d.P95))
	} else {
		out.P95Seconds.SetNull()
	}
	return out
}

// GetAlertGroup is getAlertGroup: the Alert Group with its resolution, Owner, Snooze, labels, notices and the
// Commands the caller may run.
func (s *Server) GetAlertGroup(ctx context.Context, req gen.GetAlertGroupRequestObject) (
	gen.GetAlertGroupResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.alertGroups.Get(ctx, req.AlertGroupId)
	if err != nil {
		return nil, err
	}
	chains, err := s.groupChains(ctx, v.PublicID)
	if err != nil {
		return nil, err
	}
	return gen.GetAlertGroup200JSONResponse(alertGroupOf(v, c, chains[v.PublicID])), nil
}

// alertGroupOf is the API form of an Alert Group for the caller c, with the runs of the current start of its
// Escalation chains, of its page or of a list item; a list item leaves out the labels, the notices and the links.
func alertGroupOf(v groups.View, c groups.Caller, chains []escalation.GroupChain) gen.AlertGroup {
	out := gen.AlertGroup{
		Id: v.PublicID, Number: int(v.Number), Title: v.Title, Summary: nullableString(v.Summary),
		Status: gen.AlertGroupStatus(v.Status), SeverityLevel: gen.SeverityLevel(v.Severity), Urgent: v.Urgent,
		Route: gen.EntityRef{Id: v.Route.PublicID, Name: v.Route.Name}, Integrations: []gen.EntityRef{},
		StartedAt: v.StartedAt, LastChangedAt: v.LastChangedAt, ResolvedAt: nullableTime(v.ResolvedAt),
		ReopenCount: int(v.ReopenCount), FiringAlertCount: int(v.FiringCount),
		ResolvedAlertCount: int(v.ResolvedCount), AllowedCommands: []gen.CommandName{},
		Links: linksOf(v.Links), DetailsRemoved: &v.DetailsRemoved, Resolution: resolvedByOf(v.Resolution),
		Owner: userRefOf(v.Owner), DeliveryProblem: v.DeliveryProblem, Unclaimed: v.Unclaimed,
		Teams: alertGroupTeamsOf(v.Teams), EscalationChains: alertGroupChainsOf(chains),
		Assignees: assigneesOf(v.Assignees),
	}
	if v.NextNotice != nil {
		out.NextNotice = &gen.NextNotice{Kind: gen.NextNoticeKind(v.NextNotice.Kind), At: v.NextNotice.At}
	}
	for _, cmd := range v.Allowed(c) {
		out.AllowedCommands = append(out.AllowedCommands, gen.CommandName(cmd))
	}
	if v.Status == groups.StatusSnoozed {
		out.SnoozeUntil = nullableTime(v.SnoozeUntil)
		if v.SnoozedBy != nil {
			by := actorRefOf(*v.SnoozedBy)
			out.SnoozedBy = &by
		}
	}
	for _, i := range v.Integrations {
		out.Integrations = append(out.Integrations, gen.EntityRef{Id: i.PublicID, Name: i.Name})
	}
	if v.LabelValues != nil {
		values := labelsOf(v.LabelValues)
		out.LabelValues = &values
	}
	if v.Notices == nil {
		return out
	}
	labels, common, annotations := labelsOf(v.GroupLabels), labelsOf(v.CommonLabels), labelsOf(v.CommonAnnotations)
	out.GroupLabels, out.CommonLabels, out.CommonAnnotations = &labels, &common, &annotations
	notices := make([]gen.AlertGroupNotice, 0, len(v.Notices))
	for _, n := range v.Notices {
		g := gen.AlertGroupNotice{Kind: gen.AlertGroupNoticeKind(n.Kind)}
		if n.Count != nil {
			g.Count.Set(int(*n.Count))
		}
		if n.Label != nil {
			g.Label.Set(*n.Label)
		}
		if n.ResolvedNumber != nil {
			g.ResolvedNumber.Set(int(*n.ResolvedNumber))
		}
		if n.RetentionDays != nil {
			g.RetentionDays.Set(int(*n.RetentionDays))
		}
		if n.Related != nil {
			g.RelatedAlertGroup = &gen.AlertGroupRef{Id: n.Related.PublicID, Number: int(n.Related.Number)}
		}
		notices = append(notices, g)
	}
	out.Notices = &notices
	return out
}

// resolvedByOf is the API form of who resolved an Alert Group, nil while it is open.
func resolvedByOf(r *groups.Resolution) *gen.ResolvedBy {
	if r == nil {
		return nil
	}
	res := gen.ResolvedBy{By: gen.ResolverKind(r.By), Reason: nullableString(r.Reason)}
	if r.ReasonCode != nil {
		res.ReasonCode.Set(gen.NullableResolveReason(*r.ReasonCode))
	} else {
		res.ReasonCode.SetNull()
	}
	if r.Actor != nil {
		a := actorRefOf(*r.Actor)
		res.Actor = &a
	}
	return &res
}

// actorRefOf is the API form of a User or a Service account.
func actorRefOf(a groups.ActorRef) gen.ActorRef {
	out := gen.ActorRef{Kind: gen.ActorRefKind(a.Kind), Id: a.PublicID, Name: a.Name, Deactivated: a.Deactivated}
	if a.Login != "" {
		login := a.Login
		out.Login = &login
	}
	return out
}

// noteOf is the API form of a Note.
func noteOf(n groups.NoteView) gen.Note {
	out := gen.Note{Id: n.PublicID, Body: n.Body, Transport: gen.Transport(n.Transport), CreatedAt: n.CreatedAt}
	if n.Author != nil {
		out.Author = actorRefOf(*n.Author)
	}
	return out
}

// noteKey is the position of a Note in its cursor: the time it was added and its id.
type noteKey struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

// ListAlertGroupNotes is listAlertGroupNotes: the Notes in the order they were added, also once the details of the
// Alert Group were removed.
func (s *Server) ListAlertGroupNotes(ctx context.Context, req gen.ListAlertGroupNotesRequestObject) (
	gen.ListAlertGroupNotesResponseObject, error) {
	var after *groups.NotePosition
	var key noteKey
	if ok, err := decodeCursor(req.Params.Cursor, alertGroupNotesCursor, &key); err != nil {
		return nil, err
	} else if ok {
		after = &groups.NotePosition{At: key.At, ID: key.ID}
	}
	page, err := s.alertGroups.Notes(ctx, req.AlertGroupId, after, pageSize(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	out := gen.NoteList{Items: make([]gen.Note, 0, len(page.Notes))}
	for _, n := range page.Notes {
		out.Items = append(out.Items, noteOf(n))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(alertGroupNotesCursor, noteKey{At: page.Next.At, ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListAlertGroupNotes200JSONResponse(out), nil
}

// CreateAlertGroupNote is createAlertGroupNote: the Command Add Note, whose Permission the dispatcher checks.
func (s *Server) CreateAlertGroupNote(ctx context.Context, req gen.CreateAlertGroupNoteRequestObject) (
	gen.CreateAlertGroupNoteResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	n, err := s.commands.AddNote(ctx, c, req.AlertGroupId, req.Body.Body)
	if err != nil {
		return nil, err
	}
	return gen.CreateAlertGroupNote201JSONResponse(noteOf(n)), nil
}

// undeliveredMentionOf is the API form of the undelivered Mention of a mention_not_delivered, nil for another delivery
// event (C-28.FR-3). Its entry's target names a person or a Team, the only targets that name people.
func undeliveredMentionOf(m *groups.MentionView) *gen.UndeliveredMention {
	if m == nil {
		return nil
	}
	t := gen.MentionTarget{Kind: gen.MentionTargetKind(m.Target.Kind), TeamId: nonEmptyString(m.Target.TeamID),
		UserId: nonEmptyString(m.Target.UserID), User: userRefOf(m.Target.User)}
	if tm := m.Target.Team; tm != nil {
		t.Team = &gen.TeamRef{Id: tm.PublicID, Name: tm.Name, Slug: tm.Slug, Deleted: tm.Deleted}
	}
	out := &gen.UndeliveredMention{Entry: gen.MentionEntryRef{Position: m.Position, Target: t},
		Event: gen.MentionEvent(m.Event), Reason: gen.MentionNotDeliveredReason(m.Reason)}
	if u := userRefOf(m.User); u != nil {
		out.User = *u
	}
	return out
}

// userRefOf is the API form of a User.
func userRefOf(a *groups.ActorRef) *gen.UserRef {
	if a == nil {
		return nil
	}
	out := gen.UserRef{Id: a.PublicID, Name: a.Name, Deactivated: a.Deactivated}
	if a.Login != "" {
		login := a.Login
		out.Login = &login
	}
	return &out
}

// alertGroupAlertKey is the sort key of an Alert of an Alert Group in its cursor.
type alertGroupAlertKey struct {
	Rank int   `json:"r"`
	ID   int64 `json:"i"`
}

// ListAlertGroupAlerts is listAlertGroupAlerts: firing Alerts first, then resolved ones with their reason.
func (s *Server) ListAlertGroupAlerts(ctx context.Context, req gen.ListAlertGroupAlertsRequestObject) (
	gen.ListAlertGroupAlertsResponseObject, error) {
	p := req.Params
	f := groups.AlertFilter{Limit: pageSize(p.Limit)}
	list := alertGroupAlertsCursor
	if p.State != nil {
		f.State = string(*p.State)
		list += ":" + f.State
	}
	var key alertGroupAlertKey
	if ok, err := decodeCursor(p.Cursor, list, &key); err != nil {
		return nil, err
	} else if ok {
		f.After = &groups.AlertPosition{Rank: key.Rank, ID: key.ID}
	}
	page, err := s.alertGroups.Alerts(ctx, req.AlertGroupId, f)
	if err != nil {
		return nil, err
	}
	out := gen.AlertGroupAlertList{Items: make([]gen.AlertGroupAlert, 0, len(page.Alerts))}
	for _, a := range page.Alerts {
		annotations := labelsOf(a.Annotations)
		item := gen.AlertGroupAlert{Fingerprint: a.Fingerprint, Labels: labelsOf(a.Labels), Annotations: &annotations,
			Integration: &gen.EntityRef{Id: a.Integration.PublicID, Name: a.Integration.Name},
			State:       gen.AlertStateResolved, StartsAt: a.StartsAt, ResolvedAt: nullableTime(a.ResolvedAt),
			LastSeenAt: a.LastSeenAt, AlertmanagerGroups: a.GroupKeys, SourceUrl: nullableString(a.SourceURL),
			ResolveReasonText: nullableString(a.ReasonText), Teams: teamMembershipsOf(a.Teams)}
		if a.Firing {
			item.State = gen.AlertStateFiring
		}
		if a.Reason != nil {
			item.ResolveReason.Set(gen.NullableResolveReason(*a.Reason))
		} else {
			item.ResolveReason.SetNull()
		}
		out.Items = append(out.Items, item)
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(list, alertGroupAlertKey{Rank: page.Next.Rank, ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListAlertGroupAlerts200JSONResponse(out), nil
}

// timelineKey is the position of a Timeline entry in its cursor.
type timelineKey struct {
	At     time.Time `json:"a"`
	Source int       `json:"s"`
	ID     int64     `json:"i"`
}

// GetAlertGroupTimeline is getAlertGroupTimeline: the entries with the Notes and delivery events merged by time,
// newest first by default, filtered by kind.
func (s *Server) GetAlertGroupTimeline(ctx context.Context, req gen.GetAlertGroupTimelineRequestObject) (
	gen.GetAlertGroupTimelineResponseObject, error) {
	p := req.Params
	f := groups.TimelineFilter{Limit: pageSize(p.Limit)}
	list := alertGroupTimelineCursor
	if p.Order != nil && *p.Order == gen.Asc {
		f.Ascending = true
		list += ":asc"
	}
	if p.Kind != nil {
		for _, k := range *p.Kind {
			f.Kinds = append(f.Kinds, groups.Kind(k))
			list += ":" + string(k)
		}
	}
	var key timelineKey
	if ok, err := decodeCursor(p.Cursor, list, &key); err != nil {
		return nil, err
	} else if ok {
		f.After = &groups.TimelinePosition{At: key.At, Source: key.Source, ID: key.ID}
	}
	page, err := s.alertGroups.Timeline(ctx, req.AlertGroupId, f)
	if err != nil {
		return nil, err
	}
	out := gen.TimelineEntryList{Items: make([]gen.TimelineEntry, 0, len(page.Entries))}
	for _, e := range page.Entries {
		item, err := timelineEntryOf(e)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(list, timelineKey{At: page.Next.At, Source: page.Next.Source,
			ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.GetAlertGroupTimeline200JSONResponse(out), nil
}

// timelineEntryOf is the API form of a Timeline entry: the variant of its kind.
func timelineEntryOf(e groups.TimelineEntry) (gen.TimelineEntry, error) {
	var out gen.TimelineEntry
	actor := timelineActorOf(e.Actor)
	mentions := mentionsOf(e.Mentions)
	event, loudness := gen.LifecycleEvent(e.Event), gen.Loudness(e.Loudness)
	var err error
	switch e.Kind {
	case groups.KindStatus:
		v := gen.TimelineStatusEntry{Id: e.PublicID, At: e.At, Kind: gen.TimelineStatusEntryKindStatus, Actor: actor,
			Event: event, Loudness: loudness, Mentions: mentions, Reason: nullableString(e.Reason),
			Owner: userRefOf(e.Owner), PreviousOwner: userRefOf(e.PreviousOwner),
			SnoozeUntil: nullableTime(e.SnoozeUntil)}
		if e.From != "" {
			v.FromStatus.Set(gen.NullableAlertGroupStatus(e.From))
		} else {
			v.FromStatus.SetNull()
		}
		if e.ReasonCode != "" {
			v.ReasonCode.Set(gen.NullableStatusChangeReason(e.ReasonCode))
		} else {
			v.ReasonCode.SetNull()
		}
		if e.To != "" {
			to := gen.AlertGroupStatus(e.To)
			v.ToStatus = &to
		}
		if e.Fingerprints != nil {
			v.Fingerprints = &e.Fingerprints
		}
		if e.LabelConflicts != nil {
			v.LabelConflicts = &e.LabelConflicts
		}
		if g := e.Gateway; g != nil {
			v.Gateway.Set(gen.EntityRef{Id: g.PublicID, Name: g.Name})
		} else {
			v.Gateway.SetNull()
		}
		err = out.FromTimelineStatusEntry(v)
	case groups.KindAlerts:
		v := gen.TimelineAlertsEntry{Id: e.PublicID, At: e.At, Kind: gen.TimelineAlertsEntryKindAlerts, Actor: actor,
			Event: event, Loudness: loudness, Mentions: mentions, Reason: nullableString(e.Reason),
			ReplacedLabel: nullableString(e.ReplacedLabel)}
		if e.ReasonCode != "" {
			v.ReasonCode.Set(gen.NullableStatusChangeReason(e.ReasonCode))
		} else {
			v.ReasonCode.SetNull()
		}
		if e.Fingerprints != nil {
			v.Fingerprints = &e.Fingerprints
		}
		if e.LabelConflicts != nil {
			v.LabelConflicts = &e.LabelConflicts
		}
		if e.Team != nil {
			v.Team = &gen.TeamRef{Id: e.Team.PublicID, Name: e.Team.Name, Slug: e.Team.Slug, Deleted: e.Team.Deleted}
		}
		if e.TeamRule != nil {
			rule := gen.TeamRule(*e.TeamRule)
			v.TeamRule = &rule
			v.TeamMatcher = nullableString(e.TeamMatcher)
		}
		err = out.FromTimelineAlertsEntry(v)
	case groups.KindTimers:
		v := gen.TimelineTimersEntry{Id: e.PublicID, At: e.At, Kind: gen.TimelineTimersEntryKindTimers,
			Actor: actor, Event: event, Loudness: loudness, Mentions: mentions}
		if e.NoticeNumber != nil {
			v.NoticeNumber.Set(int(*e.NoticeNumber))
		}
		if e.MissedCount != nil {
			v.MissedCount.Set(int(*e.MissedCount))
		}
		err = out.FromTimelineTimersEntry(v)
	case groups.KindNotes:
		err = out.FromTimelineNoteEntry(gen.TimelineNoteEntry{Id: e.PublicID, At: e.At,
			Kind: gen.TimelineNoteEntryKindNotes, Actor: actor, Event: event, Loudness: loudness, Mentions: mentions,
			Note: noteOf(*e.Note)})
	case groups.KindDelivery:
		d := e.Delivery
		err = out.FromTimelineDeliveryEntry(gen.TimelineDeliveryEntry{Id: e.PublicID, At: e.At,
			Kind: gen.TimelineDeliveryEntryKindDelivery, Actor: actor, DeliveryEvent: gen.DeliveryEventKind(d.Event),
			Destination: gen.EntityRef{Id: d.Destination.PublicID, Name: d.Destination.Name},
			Error:       nullableString(d.Error), Loudness: loudness, Mentions: mentions,
			Mention: undeliveredMentionOf(d.Mention)})
	case groups.KindEscalation:
		v := gen.TimelineEscalationEntry{Id: e.PublicID, At: e.At, Kind: gen.TimelineEscalationEntryKindEscalation,
			Actor: actor, Event: event, Loudness: loudness, Mentions: mentions,
			PagedUsers: make([]gen.UserRef, 0, len(e.PagedUsers))}
		for _, u := range e.PagedUsers {
			v.PagedUsers = append(v.PagedUsers, *userRefOf(u))
		}
		if g := e.Gateway; g != nil {
			v.Gateway.Set(gen.EntityRef{Id: g.PublicID, Name: g.Name})
		} else {
			v.Gateway.SetNull()
		}
		v.Team.SetNull()
		if t := e.Team; t != nil {
			v.Team.Set(gen.TeamRef{Id: t.PublicID, Name: t.Name, Slug: t.Slug, Deleted: t.Deleted})
		}
		v.Everyone.SetNull()
		if e.Everyone != nil {
			v.Everyone.Set(gen.PageEveryone(*e.Everyone))
		}
		v.Importance.SetNull()
		if e.Importance != nil {
			v.Importance.Set(gen.NullablePageImportance(*e.Importance))
		}
		v.Reason.SetNull()
		v.SkipReason.SetNull()
		switch {
		case e.Reason == nil:
		case e.Event == string(groups.EventEscalationStepSkipped):
			v.SkipReason.Set(gen.EscalationSkipReason(*e.Reason))
		default:
			v.Reason.Set(gen.PageNotDeliveredReason(*e.Reason))
		}
		v.Chain, v.ChainName, v.Step = timelineChainOf(e)
		if d := e.Destination; d != nil {
			v.Destination = &gen.DestinationRef{Id: d.PublicID, Name: d.Name, Type: gen.DestinationType(d.Type),
				Health: healthOf(destinations.Health{State: d.Health, Since: d.BrokenSince, Reason: d.BrokenReason})}
		}
		if e.Event == string(groups.EventAssigneesChanged) {
			assignees := assigneesOf(e.Assignees)
			v.Assignees = &assignees
		}
		err = out.FromTimelineEscalationEntry(v)
	default:
		v := gen.TimelineSystemEntry{Id: e.PublicID, At: e.At, Kind: gen.TimelineSystemEntryKindSystem, Actor: actor,
			SystemEvent: gen.TimelineSystemEntrySystemEvent(e.System), PeriodFrom: nullableTime(e.PeriodFrom),
			PeriodTo: nullableTime(e.PeriodTo), Detail: nullableString(e.Detail)}
		if e.Event != "" {
			v.Event, v.Loudness, v.Mentions = &event, &loudness, &mentions
		}
		if e.TeamValue != nil {
			v.TeamValue.Set(*e.TeamValue)
		}
		if d := e.Destination; d != nil {
			v.Destination = &gen.DestinationRef{Id: d.PublicID, Name: d.Name, Type: gen.DestinationType(d.Type),
				Health: healthOf(destinations.Health{State: d.Health, Since: d.BrokenSince, Reason: d.BrokenReason})}
		}
		err = out.FromTimelineSystemEntry(v)
	}
	if err != nil {
		return gen.TimelineEntry{}, fmt.Errorf("encode timeline entry %s: %w", e.PublicID, err)
	}
	return out, nil
}

// timelineActorOf is the API form of who caused an entry.
func timelineActorOf(a groups.TimelineActor) gen.TimelineActor {
	transport := gen.Transport(a.Transport)
	out := gen.TimelineActor{Kind: gen.ActorKind(a.Kind), TokenName: nullableString(a.TokenName),
		Reason: nullableString(a.Reason), Transport: &transport}
	if a.Ref != nil {
		out.Id.Set(a.Ref.PublicID)
		out.Name.Set(a.Ref.Name)
	} else {
		out.Id.SetNull()
		out.Name.SetNull()
	}
	return out
}

// mentionsOf is the API form of symbolic Mentions, an empty list for none.
func mentionsOf(ms []string) []gen.MentionName {
	out := make([]gen.MentionName, len(ms))
	for i, m := range ms {
		out[i] = gen.MentionName(m)
	}
	return out
}

// labelsOf is the API form of labels or annotations, an empty object for none.
func labelsOf(m map[string]string) gen.Labels {
	if m == nil {
		return gen.Labels{}
	}
	return m
}

// ListAlertGroupDeliveries is listAlertGroupDeliveries: the delivery state of the Alert Group in each Destination.
func (s *Server) ListAlertGroupDeliveries(ctx context.Context, req gen.ListAlertGroupDeliveriesRequestObject) (
	gen.ListAlertGroupDeliveriesResponseObject, error) {
	states, err := s.deliveries.States(ctx, req.AlertGroupId)
	if err != nil {
		return nil, err
	}
	out := gen.ListAlertGroupDeliveries200JSONResponse{Items: make([]gen.AlertGroupDelivery, 0, len(states))}
	for _, st := range states {
		updated := st.UpdatedAt
		item := gen.AlertGroupDelivery{
			Destination: gen.DestinationRef{Id: st.Destination.PublicID, Name: st.Destination.Name,
				Type: gen.DestinationType(st.Destination.Type),
				Health: healthOf(destinations.Health{State: st.Destination.Health, Since: st.Destination.BrokenSince,
					Reason: st.Destination.BrokenReason})},
			State: gen.DeliveryState(st.State), ThreadNotAttached: st.ThreadNotAttached,
			PossibleDuplicate: st.PossibleDuplicate, PastEditWindow: st.PastEditWindow,
			MessageUrl: nullableString(st.MessageURL),
			Error:      nullableString(st.Error), UpdatedAt: &updated,
			Entries: make([]gen.DeliveryEntry, 0, len(st.Entries)),
		}
		for _, e := range st.Entries {
			entry := gen.DeliveryEntry{Kind: gen.DeliveryEntryKind(e.Kind),
				SeverityCondition: gen.SeverityCondition(e.Condition)}
			if e.Team != nil {
				entry.Team.Set(gen.TeamRef{Id: e.Team.PublicID, Name: e.Team.Name, Slug: e.Team.Slug,
					Deleted: e.Team.Deleted})
			} else {
				entry.Team.SetNull()
			}
			item.Entries = append(item.Entries, entry)
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// linksOf are the links of an Alert Group as the API shows them: the kind of each, and the rule of a Link rule's.
func linksOf(ls []groups.Link) *[]gen.AlertGroupLink {
	out := make([]gen.AlertGroupLink, len(ls))
	for i, l := range ls {
		out[i] = gen.AlertGroupLink{Kind: gen.AlertGroupLinkKind(l.Kind), Name: l.Name, Url: l.URL}
		if l.Rule != "" {
			out[i].LinkRuleId = &l.Rule
		}
	}
	return &out
}

// teamMembershipOf is the API form of a Team an Alert belongs to.
func teamMembershipOf(t groups.TeamMembership) gen.TeamMembership {
	out := gen.TeamMembership{Team: gen.TeamRef{Id: t.Team.PublicID, Name: t.Team.Name, Slug: t.Team.Slug,
		Deleted: t.Team.Deleted}, Rule: gen.TeamRule(t.Rule)}
	if t.Matcher != nil {
		out.Matcher.Set(*t.Matcher)
	} else {
		out.Matcher.SetNull()
	}
	return out
}

// teamMembershipsOf is the API form of the Teams of an Alert.
func teamMembershipsOf(ts []groups.TeamMembership) []gen.TeamMembership {
	out := make([]gen.TeamMembership, len(ts))
	for i, t := range ts {
		out[i] = teamMembershipOf(t)
	}
	return out
}

// alertGroupTeamsOf is the API form of the Teams of an Alert Group.
func alertGroupTeamsOf(ts []groups.GroupTeam) []gen.AlertGroupTeam {
	out := make([]gen.AlertGroupTeam, len(ts))
	for i, t := range ts {
		m := teamMembershipOf(t.TeamMembership)
		out[i] = gen.AlertGroupTeam{Team: m.Team, Rule: m.Rule, Matcher: m.Matcher, JoinedAt: t.JoinedAt}
	}
	return out
}

// assigneesOf is the API form of Assignees, each with the owning Team of their chain and the chain, null for none.
func assigneesOf(list []groups.Assignee) []gen.Assignee {
	out := make([]gen.Assignee, len(list))
	for i, a := range list {
		v := gen.Assignee{User: *userRefOf(&a.User), Since: a.Since}
		v.Team.SetNull()
		if t := a.Team; t != nil {
			v.Team.Set(gen.TeamRef{Id: t.PublicID, Name: t.Name, Slug: t.Slug, Deleted: t.Deleted})
		}
		v.Chain.SetNull()
		if c := a.Chain; c != nil {
			ref := gen.EscalationChainRef{Id: c.PublicID, Name: c.Name}
			ref.OwningTeam.SetNull()
			if t := c.OwningTeam; t != nil {
				ref.OwningTeam.Set(gen.TeamRef{Id: t.PublicID, Name: t.Name, Slug: t.Slug, Deleted: t.Deleted})
			}
			v.Chain.Set(ref)
		}
		out[i] = v
	}
	return out
}

// timelineChainOf is the chain, its name when its run started and the step of an escalation entry, null for an
// event of no chain.
func timelineChainOf(e groups.TimelineEntry) (nullable.Nullable[gen.EscalationChainRef], nullable.Nullable[string],
	nullable.Nullable[int]) {
	var chain nullable.Nullable[gen.EscalationChainRef]
	var name nullable.Nullable[string]
	var step nullable.Nullable[int]
	chain.SetNull()
	name.SetNull()
	step.SetNull()
	if e.Step == nil {
		return chain, name, step
	}
	step.Set(*e.Step)
	name.Set(e.ChainName)
	if c := e.Chain; c != nil {
		ref := gen.EscalationChainRef{Id: c.PublicID, Name: c.Name}
		ref.OwningTeam.SetNull()
		if t := c.OwningTeam; t != nil {
			ref.OwningTeam.Set(gen.TeamRef{Id: t.PublicID, Name: t.Name, Slug: t.Slug, Deleted: t.Deleted})
		}
		chain.Set(ref)
	}
	return chain, name, step
}
