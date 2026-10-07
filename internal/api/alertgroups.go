// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/organization"
)

// AlertGroups is what the API needs of internal/groups: the list with its counts, one Alert Group, its Alerts, its
// Timeline, its Notes and its related Alert Groups, the statistics, the open count of Integrations, and the move of a Route's
// open Alert Groups to the Default route.
type AlertGroups interface {
	List(ctx context.Context, r groups.ListRequest) (groups.ListPage, error)
	Counts(ctx context.Context, f groups.Filter) (groups.Counts, error)
	Get(ctx context.Context, publicID string) (groups.View, error)
	Alerts(ctx context.Context, publicID string, f groups.AlertFilter) (groups.AlertPage, error)
	Timeline(ctx context.Context, publicID string, f groups.TimelineFilter) (groups.TimelinePage, error)
	Notes(ctx context.Context, publicID string, after *groups.NotePosition, limit int) (groups.NotePage, error)
	Related(ctx context.Context, publicID string, after *groups.ListPosition, limit int) (groups.RelatedPage, error)
	Statistics(ctx context.Context, r groups.StatisticsRequest) (groups.Statistics, error)
	OpenCounts(ctx context.Context, integrations []string) (map[string]int64, error)
	MoveOpenAlertGroups(ctx context.Context, r groups.Requester, routeID string) (int, error)
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

// filterParams are the filter parameters that listAlertGroups and getAlertGroupCounts share.
type filterParams struct {
	route, integration    *[]string
	severity              *[]gen.SeverityLevel
	urgent, reopened      *bool
	resolvedBy            *gen.ResolverKind
	resolveReason         *gen.ResolveReason
	label                 *[]string
	owner                 *string
	snoozedNoEnd, problem *bool
	unclaimed             *bool
	from, to              *time.Time
	q                     *string
	// me is the caller, whom owner=me names.
	me audit.Actor
}

// filterOf is the groups.Filter of the parameters. The filters "Delivery problem" and Unclaimed belong to later
// stories and answer 422 unsupported until then.
func filterOf(p filterParams) (groups.Filter, error) {
	for _, u := range []struct {
		name string
		set  bool
	}{{"delivery_problem", p.problem != nil}, {"unclaimed", p.unclaimed != nil}} {
		if u.set {
			return groups.Filter{}, fieldProblem(http.StatusUnprocessableEntity, "/query/"+u.name, fieldUnsupported,
				"This filter is not available yet.")
		}
	}
	f := groups.Filter{Urgent: p.urgent, Reopened: p.reopened, From: p.from, To: p.to, SnoozedNoEnd: p.snoozedNoEnd,
		Me: p.me}
	if p.owner != nil {
		f.Owner = *p.owner
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
		label: p.Label, owner: p.Owner, snoozedNoEnd: p.SnoozedNoEnd, problem: p.DeliveryProblem,
		unclaimed: p.Unclaimed, from: p.From, to: p.To, q: p.Q, me: c.Actor})
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
	out := gen.AlertGroupList{Items: make([]gen.AlertGroup, 0, len(page.Groups))}
	for _, v := range page.Groups {
		out.Items = append(out.Items, alertGroupOf(v, c))
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
		label: p.Label, owner: p.Owner, snoozedNoEnd: p.SnoozedNoEnd, problem: p.DeliveryProblem,
		unclaimed: p.Unclaimed, from: p.From, to: p.To, q: p.Q, me: me.Actor})
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

// GetAlertGroupStatistics is getAlertGroupStatistics: per Route or per Integration, totals and days.
func (s *Server) GetAlertGroupStatistics(ctx context.Context, req gen.GetAlertGroupStatisticsRequestObject) (
	gen.GetAlertGroupStatisticsResponseObject, error) {
	p := req.Params
	r := groups.StatisticsRequest{GroupBy: string(p.GroupBy), From: p.From, To: p.To}
	if p.Route != nil {
		r.Routes = *p.Route
	}
	if p.Integration != nil {
		r.Integrations = *p.Integration
	}
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
	return gen.GetAlertGroup200JSONResponse(alertGroupOf(v, c)), nil
}

// alertGroupOf is the API form of an Alert Group for the caller c, of its page or of a list item; a list item leaves
// out the labels and the notices. The links, Unclaimed and the delivery problem arrive with their capabilities.
func alertGroupOf(v groups.View, c groups.Caller) gen.AlertGroup {
	out := gen.AlertGroup{
		Id: v.PublicID, Number: int(v.Number), Title: v.Title, Summary: nullableString(v.Summary),
		Status: gen.AlertGroupStatus(v.Status), SeverityLevel: gen.SeverityLevel(v.Severity), Urgent: v.Urgent,
		Route: gen.EntityRef{Id: v.Route.PublicID, Name: v.Route.Name}, Integrations: []gen.EntityRef{},
		StartedAt: v.StartedAt, LastChangedAt: v.LastChangedAt, ResolvedAt: nullableTime(v.ResolvedAt),
		ReopenCount: int(v.ReopenCount), FiringAlertCount: int(v.FiringCount),
		ResolvedAlertCount: int(v.ResolvedCount), AllowedCommands: []gen.CommandName{},
		Links: &[]gen.AlertGroupLink{}, DetailsRemoved: &v.DetailsRemoved, Resolution: resolvedByOf(v.Resolution),
		Owner: userRefOf(v.Owner),
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
			ResolveReasonText: nullableString(a.ReasonText)}
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
		err = out.FromTimelineStatusEntry(v)
	case groups.KindAlerts:
		v := gen.TimelineAlertsEntry{Id: e.PublicID, At: e.At, Kind: gen.TimelineAlertsEntryKindAlerts, Actor: actor,
			Event: event, Loudness: loudness, Mentions: mentions, Reason: nullableString(e.Reason),
			ReplacedLabel: nullableString(e.ReplacedLabel)}
		if e.Fingerprints != nil {
			v.Fingerprints = &e.Fingerprints
		}
		if e.LabelConflicts != nil {
			v.LabelConflicts = &e.LabelConflicts
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
			Error:       nullableString(d.Error), Loudness: loudness, Mentions: mentions})
	default:
		v := gen.TimelineSystemEntry{Id: e.PublicID, At: e.At, Kind: gen.TimelineSystemEntryKindSystem, Actor: actor,
			SystemEvent: gen.TimelineSystemEntrySystemEvent(e.System), PeriodFrom: nullableTime(e.PeriodFrom),
			PeriodTo: nullableTime(e.PeriodTo), Detail: nullableString(e.Detail)}
		if e.Event != "" {
			v.Event, v.Loudness, v.Mentions = &event, &loudness, &mentions
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
