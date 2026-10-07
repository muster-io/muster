// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/groups"
)

// AlertGroups is what the API needs of internal/groups: one Alert Group, its Alerts and its Timeline, and the move
// of a Route's open Alert Groups to the Default route.
type AlertGroups interface {
	Get(ctx context.Context, publicID string) (groups.View, error)
	Alerts(ctx context.Context, publicID string, f groups.AlertFilter) (groups.AlertPage, error)
	Timeline(ctx context.Context, publicID string, f groups.TimelineFilter) (groups.TimelinePage, error)
	MoveOpenAlertGroups(ctx context.Context, r groups.Requester, routeID string) (int, error)
}

// The cursors of the Alert Group lists.
const (
	alertGroupAlertsCursor   = "alert-group-alerts"
	alertGroupTimelineCursor = "alert-group-timeline"
)

// GetAlertGroup is getAlertGroup: the Alert Group with its resolution, labels and notices.
func (s *Server) GetAlertGroup(ctx context.Context, req gen.GetAlertGroupRequestObject) (
	gen.GetAlertGroupResponseObject, error) {
	v, err := s.alertGroups.Get(ctx, req.AlertGroupId)
	if err != nil {
		return nil, err
	}
	return gen.GetAlertGroup200JSONResponse(alertGroupOf(v)), nil
}

// alertGroupOf is the API form of an Alert Group. The Owner, the Snooze and the allowed Commands arrive with the
// Commands, the links, Unclaimed and the delivery problem with their capabilities.
func alertGroupOf(v groups.View) gen.AlertGroup {
	out := gen.AlertGroup{
		Id: v.PublicID, Number: int(v.Number), Title: v.Title, Summary: nullableString(v.Summary),
		Status: gen.AlertGroupStatus(v.Status), SeverityLevel: gen.SeverityLevel(v.Severity), Urgent: v.Urgent,
		Route: gen.EntityRef{Id: v.Route.PublicID, Name: v.Route.Name}, Integrations: []gen.EntityRef{},
		StartedAt: v.StartedAt, LastChangedAt: v.LastChangedAt, ResolvedAt: nullableTime(v.ResolvedAt),
		ReopenCount: int(v.ReopenCount), FiringAlertCount: int(v.FiringCount),
		ResolvedAlertCount: int(v.ResolvedCount), AllowedCommands: []gen.CommandName{},
		Links: &[]gen.AlertGroupLink{},
	}
	for _, i := range v.Integrations {
		out.Integrations = append(out.Integrations, gen.EntityRef{Id: i.PublicID, Name: i.Name})
	}
	labels, common, annotations := labelsOf(v.GroupLabels), labelsOf(v.CommonLabels), labelsOf(v.CommonAnnotations)
	out.GroupLabels, out.CommonLabels, out.CommonAnnotations = &labels, &common, &annotations
	if r := v.Resolution; r != nil {
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
		out.Resolution = &res
	}
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
		notices = append(notices, g)
	}
	out.Notices = &notices
	return out
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
		n := e.Note
		v := gen.TimelineNoteEntry{Id: e.PublicID, At: e.At, Kind: gen.TimelineNoteEntryKindNotes, Actor: actor,
			Event: event, Loudness: loudness, Mentions: mentions,
			Note: gen.Note{Id: n.PublicID, Body: n.Body, Transport: gen.Transport(n.Transport),
				CreatedAt: n.CreatedAt}}
		if n.Author != nil {
			v.Note.Author = actorRefOf(*n.Author)
		}
		err = out.FromTimelineNoteEntry(v)
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
