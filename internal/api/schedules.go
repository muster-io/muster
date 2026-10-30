// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"time"

	"github.com/oapi-codegen/nullable"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/schedules"
)

// Schedules is what the API needs of internal/schedules: Schedules with their Rotation layers, their Overrides and
// their On-call.
type Schedules interface {
	List(ctx context.Context, f schedules.ListFilter) (schedules.Page, error)
	Get(ctx context.Context, publicID string) (schedules.Schedule, error)
	Create(ctx context.Context, r schedules.Requester, in schedules.Input) (schedules.Schedule, error)
	Update(ctx context.Context, r schedules.Requester, publicID string, version *int64, in schedules.Input) (
		schedules.Schedule, error)
	Delete(ctx context.Context, r schedules.Requester, publicID string, version *int64) error
	OnCall(ctx context.Context, publicID string, from, to *time.Time) (schedules.Result, error)
	Overrides(ctx context.Context, publicID string, f schedules.OverrideFilter) (schedules.OverridePage, error)
	CreateOverride(ctx context.Context, r schedules.Requester, scheduleID string, in schedules.OverrideInput) (
		schedules.Override, error)
	UpdateOverride(ctx context.Context, r schedules.Requester, scheduleID, overrideID string, version *int64,
		in schedules.OverrideInput) (schedules.Override, error)
	DeleteOverride(ctx context.Context, r schedules.Requester, scheduleID, overrideID string, version *int64) error
}

// The cursors of the Schedule and Override lists.
const (
	schedulesCursor = "schedules"
	overridesCursor = "schedule-overrides"
)

// schedulesRequester is who asks for a change of a Schedule or an Override through the API, with whether their Role
// is Admin.
func schedulesRequester(ctx context.Context) (schedules.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return schedules.Requester{}, err
	}
	role := id.Session.User.Role
	if id.IsServiceAccount() {
		role = id.Token.ServiceAccount.Role
	}
	return schedules.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx),
		Admin: role == auth.RoleAdmin}, nil
}

// ListSchedules is listSchedules: the Schedules by name, of a Team or of "My Teams", matching q.
func (s *Server) ListSchedules(ctx context.Context, req gen.ListSchedulesRequestObject) (
	gen.ListSchedulesResponseObject, error) {
	p := req.Params
	team, member, err := owningTeamQuery(ctx, p.Team)
	if err != nil {
		return nil, err
	}
	f := schedules.ListFilter{Team: team, MemberID: member, Limit: pageSize(p.Limit)}
	if p.Q != nil {
		f.Query = *p.Q
	}
	var after schedules.Position
	if ok, err := decodeCursor(p.Cursor, schedulesCursor, &after); err != nil {
		return nil, err
	} else if ok {
		f.After = &after
	}
	page, err := s.schedules.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.ScheduleList{Items: make([]gen.Schedule, 0, len(page.Schedules))}
	for _, sc := range page.Schedules {
		out.Items = append(out.Items, scheduleOf(sc))
	}
	out.NextCursor.SetNull()
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(schedulesCursor, *page.Next))
	}
	return gen.ListSchedules200JSONResponse(out), nil
}

// CreateSchedule is createSchedule.
func (s *Server) CreateSchedule(ctx context.Context, req gen.CreateScheduleRequestObject) (
	gen.CreateScheduleResponseObject, error) {
	r, err := schedulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	sc, err := s.schedules.Create(ctx, r, scheduleInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag, location := etag(sc.Version), BasePath+"/schedules/"+sc.PublicID
	return gen.CreateSchedule201JSONResponse{Body: scheduleOf(sc),
		Headers: gen.CreateSchedule201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// GetSchedule is getSchedule.
func (s *Server) GetSchedule(ctx context.Context, req gen.GetScheduleRequestObject) (gen.GetScheduleResponseObject,
	error) {
	sc, err := s.schedules.Get(ctx, req.ScheduleId)
	if err != nil {
		return nil, err
	}
	tag := etag(sc.Version)
	return gen.GetSchedule200JSONResponse{Body: scheduleOf(sc), Headers: gen.GetSchedule200ResponseHeaders{ETag: &tag}},
		nil
}

// UpdateSchedule is updateSchedule, with If-Match.
func (s *Server) UpdateSchedule(ctx context.Context, req gen.UpdateScheduleRequestObject) (
	gen.UpdateScheduleResponseObject, error) {
	r, err := schedulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	sc, err := s.schedules.Update(ctx, r, req.ScheduleId, version, scheduleInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(sc.Version)
	return gen.UpdateSchedule200JSONResponse{Body: scheduleOf(sc),
		Headers: gen.UpdateSchedule200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteSchedule is deleteSchedule, with an optional If-Match.
func (s *Server) DeleteSchedule(ctx context.Context, req gen.DeleteScheduleRequestObject) (
	gen.DeleteScheduleResponseObject, error) {
	r, err := schedulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := optionalVersion(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if err := s.schedules.Delete(ctx, r, req.ScheduleId, version); err != nil {
		return nil, err
	}
	return gen.DeleteSchedule204Response{}, nil
}

// GetScheduleOnCall is getScheduleOnCall: the Result of a period.
func (s *Server) GetScheduleOnCall(ctx context.Context, req gen.GetScheduleOnCallRequestObject) (
	gen.GetScheduleOnCallResponseObject, error) {
	res, err := s.schedules.OnCall(ctx, req.ScheduleId, req.Params.From, req.Params.To)
	if err != nil {
		return nil, err
	}
	out := gen.ScheduleOnCall{From: res.From.UTC(), To: res.To.UTC(),
		Segments:       make([]gen.OnCallSegment, len(res.Segments)),
		RotationLayers: make([]gen.RotationLayerShifts, len(res.Layers)),
		Overrides:      make([]gen.ScheduleOverride, len(res.Overrides)),
		Warnings:       make([]gen.ScheduleWarning, len(res.Warnings))}
	for i, seg := range res.Segments {
		out.Segments[i] = segmentOf(seg)
	}
	for i, l := range res.Layers {
		shifts := make([]gen.RotationShift, len(l.Shifts))
		for j, sh := range l.Shifts {
			shifts[j] = gen.RotationShift{Start: sh.Start.UTC(), End: sh.End.UTC(), User: personRefOf(sh.User)}
			shifts[j].Skipped.SetNull()
			if sh.Skipped != "" {
				shifts[j].Skipped.Set(gen.NotPageableReason(sh.Skipped))
			}
		}
		out.RotationLayers[i] = gen.RotationLayerShifts{
			RotationLayer: gen.EntityRef{Id: l.Layer.PublicID, Name: l.Layer.Name}, Shifts: shifts}
	}
	for i, o := range res.Overrides {
		out.Overrides[i] = overrideOf(o)
	}
	for i, w := range res.Warnings {
		out.Warnings[i] = warningOf(w)
	}
	return gen.GetScheduleOnCall200JSONResponse(out), nil
}

// ListScheduleOverrides is listScheduleOverrides.
func (s *Server) ListScheduleOverrides(ctx context.Context, req gen.ListScheduleOverridesRequestObject) (
	gen.ListScheduleOverridesResponseObject, error) {
	p := req.Params
	f := schedules.OverrideFilter{From: p.From, To: p.To, Limit: pageSize(p.Limit)}
	var after schedules.OverridePosition
	if ok, err := decodeCursor(p.Cursor, overridesCursor, &after); err != nil {
		return nil, err
	} else if ok {
		f.After = &after
	}
	page, err := s.schedules.Overrides(ctx, req.ScheduleId, f)
	if err != nil {
		return nil, err
	}
	out := gen.ScheduleOverrideList{Items: make([]gen.ScheduleOverride, 0, len(page.Overrides))}
	for _, o := range page.Overrides {
		out.Items = append(out.Items, overrideOf(o))
	}
	out.NextCursor.SetNull()
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(overridesCursor, *page.Next))
	}
	return gen.ListScheduleOverrides200JSONResponse(out), nil
}

// CreateScheduleOverride is createScheduleOverride.
func (s *Server) CreateScheduleOverride(ctx context.Context, req gen.CreateScheduleOverrideRequestObject) (
	gen.CreateScheduleOverrideResponseObject, error) {
	r, err := schedulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	o, err := s.schedules.CreateOverride(ctx, r, req.ScheduleId, overrideInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	schedule, _ := publicid.Parse(publicid.Schedule, req.ScheduleId) // the creation found the Schedule by it
	tag, location := etag(o.Version), BasePath+"/schedules/"+schedule+"/overrides/"+o.PublicID
	return gen.CreateScheduleOverride201JSONResponse{Body: overrideOf(o),
		Headers: gen.CreateScheduleOverride201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// UpdateScheduleOverride is updateScheduleOverride, with If-Match.
func (s *Server) UpdateScheduleOverride(ctx context.Context, req gen.UpdateScheduleOverrideRequestObject) (
	gen.UpdateScheduleOverrideResponseObject, error) {
	r, err := schedulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	o, err := s.schedules.UpdateOverride(ctx, r, req.ScheduleId, req.OverrideId, version,
		overrideInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(o.Version)
	return gen.UpdateScheduleOverride200JSONResponse{Body: overrideOf(o),
		Headers: gen.UpdateScheduleOverride200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteScheduleOverride is deleteScheduleOverride, with an optional If-Match.
func (s *Server) DeleteScheduleOverride(ctx context.Context, req gen.DeleteScheduleOverrideRequestObject) (
	gen.DeleteScheduleOverrideResponseObject, error) {
	r, err := schedulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := optionalVersion(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if err := s.schedules.DeleteOverride(ctx, r, req.ScheduleId, req.OverrideId, version); err != nil {
		return nil, err
	}
	return gen.DeleteScheduleOverride204Response{}, nil
}

// scheduleInputOf is a Schedule as internal/schedules takes it.
func scheduleInputOf(b gen.ScheduleInput) schedules.Input {
	in := schedules.Input{TeamID: b.TeamId, Name: b.Name, TimeZone: b.TimeZone, Main: b.Main,
		Layers: make([]schedules.LayerInput, len(b.RotationLayers))}
	for i, l := range b.RotationLayers {
		li := schedules.LayerInput{Name: l.Name, UserIDs: append([]string(nil), l.UserIds...),
			ShiftLength: string(l.ShiftLength), ShiftSeconds: l.ShiftSeconds, HandoverTime: l.HandoverTime,
			StartDate: l.StartDate.Time}
		if l.Id != nil {
			li.ID = *l.Id
		}
		if l.HandoverDay != nil {
			li.HandoverDay = string(*l.HandoverDay)
		}
		if l.Limits.IsSpecified() && !l.Limits.IsNull() {
			li.Limits = limitsIn(l.Limits.MustGet())
		}
		in.Layers[i] = li
	}
	return in
}

// limitsIn are limits as internal/schedules takes them.
func limitsIn(lm gen.RotationLimits) *schedules.Limits {
	out := &schedules.Limits{Days: make([]string, len(lm.Days))}
	for i, d := range lm.Days {
		out.Days[i] = string(d)
	}
	if lm.Start != nil {
		out.Start = *lm.Start
	}
	if lm.End != nil {
		out.End = *lm.End
	}
	return out
}

// scheduleOf is the API form of a Schedule.
func scheduleOf(sc schedules.Schedule) gen.Schedule {
	tag := etag(sc.Version)
	out := gen.Schedule{Id: sc.PublicID, TeamId: sc.Team.PublicID,
		Team: gen.TeamRef{Id: sc.Team.PublicID, Name: sc.Team.Name, Slug: sc.Team.Slug, Deleted: sc.Team.Deleted},
		Name: sc.Name, TimeZone: sc.TimeZone, Main: sc.Main, CreatedAt: sc.CreatedAt.UTC(), Etag: &tag,
		RotationLayers: make([]gen.RotationLayer, len(sc.Layers))}
	for i, l := range sc.Layers {
		out.RotationLayers[i] = layerOf(l)
	}
	return out
}

// layerOf is the API form of a Rotation layer.
func layerOf(l schedules.Layer) gen.RotationLayer {
	out := gen.RotationLayer{Id: l.PublicID, Number: l.Number, Name: l.Name, ShiftLength: gen.ShiftLength(l.ShiftLength),
		HandoverTime: l.HandoverTime, StartDate: openapi_types.Date{Time: l.StartDate},
		UserIds: make([]string, len(l.Users)), Users: make([]gen.UserRef, len(l.Users))}
	for i, u := range l.Users {
		out.UserIds[i], out.Users[i] = u.PublicID, personRefOf(u)
	}
	if l.ShiftLength == schedules.ShiftHours {
		seconds := l.ShiftSeconds
		out.ShiftSeconds = &seconds
	}
	if l.HandoverDay != "" {
		day := gen.Weekday(l.HandoverDay)
		out.HandoverDay = &day
	}
	out.Limits.SetNull()
	if lm := l.Limits; lm != nil {
		days := make([]gen.Weekday, len(lm.Days))
		for i, d := range lm.Days {
			days[i] = gen.Weekday(d)
		}
		limits := gen.RotationLimits{Days: days}
		if lm.Start != "" {
			start, end := lm.Start, lm.End
			limits.Start, limits.End = &start, &end
		}
		out.Limits.Set(limits)
	}
	return out
}

// personRefOf is the API form of a person a Schedule names.
func personRefOf(p schedules.Person) gen.UserRef {
	login := p.Login
	return gen.UserRef{Id: p.PublicID, Name: p.Name, Login: &login, Deactivated: p.Status == "deleted"}
}

// overrideInputOf is an Override as internal/schedules takes it.
func overrideInputOf(b gen.ScheduleOverrideInput) schedules.OverrideInput {
	in := schedules.OverrideInput{UserID: b.UserId, Start: b.Start, End: b.End}
	if b.ReplacesUserId.IsSpecified() && !b.ReplacesUserId.IsNull() {
		in.ReplacesUserID = b.ReplacesUserId.MustGet()
	}
	return in
}

// overrideOf is the API form of an Override.
func overrideOf(o schedules.Override) gen.ScheduleOverride {
	tag := etag(o.Version)
	out := gen.ScheduleOverride{Id: o.PublicID, UserId: o.User.PublicID, User: personRefOf(o.User),
		Start: o.Start.UTC(), End: o.End.UTC(), CreatedAt: o.CreatedAt.UTC(), Etag: &tag,
		CreatedBy: gen.ActorRef{Kind: gen.ActorRefKind(o.CreatedBy.Kind), Id: o.CreatedBy.PublicID,
			Name: o.CreatedBy.Name, Deactivated: o.CreatedBy.Deactivated}}
	if o.CreatedBy.Login != "" {
		login := o.CreatedBy.Login
		out.CreatedBy.Login = &login
	}
	out.ReplacesUserId.SetNull()
	out.ReplacesUser.SetNull()
	if o.Replaces != nil {
		out.ReplacesUserId.Set(o.Replaces.PublicID)
		out.ReplacesUser.Set(personRefOf(*o.Replaces))
	}
	return out
}

// segmentOf is the API form of a segment of the Result.
func segmentOf(seg schedules.Segment) gen.OnCallSegment {
	out := gen.OnCallSegment{Start: seg.Start.UTC(), End: seg.End.UTC()}
	out.User.SetNull()
	out.Source.SetNull()
	if seg.User != nil {
		out.User.Set(personRefOf(*seg.User))
	}
	if src := seg.Source; src != nil {
		s := gen.OnCallSource{Kind: gen.OnCallSourceKind(src.Kind)}
		if src.Kind == schedules.SourceOverride {
			id := src.Override
			s.OverrideId = &id
		} else {
			s.RotationLayer = &gen.EntityRef{Id: src.Layer.PublicID, Name: src.Layer.Name}
		}
		out.Source.Set(s)
	}
	return out
}

// warningOf is the API form of a warning of the Result.
func warningOf(w schedules.Warning) gen.ScheduleWarning {
	out := gen.ScheduleWarning{Kind: gen.ScheduleWarningKind(w.Kind)}
	out.User.Set(personRefOf(w.User))
	out.RotationLayer.SetNull()
	if w.Layer != nil {
		out.RotationLayer.Set(gen.EntityRef{Id: w.Layer.PublicID, Name: w.Layer.Name})
	}
	out.OverrideId = nullable.NewNullNullable[string]()
	if w.Override != "" {
		out.OverrideId.Set(w.Override)
	}
	return out
}
