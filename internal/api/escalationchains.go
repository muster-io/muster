// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/escalation"
)

// EscalationChains is what the API needs of internal/escalation: the Escalation chains with their steps (C-32).
type EscalationChains interface {
	List(ctx context.Context, f escalation.ListFilter) (escalation.Page, error)
	Get(ctx context.Context, publicID string) (escalation.Chain, error)
	Create(ctx context.Context, r escalation.Requester, in escalation.Input) (escalation.Chain, error)
	Update(ctx context.Context, r escalation.Requester, publicID string, version *int64, in escalation.Input) (
		escalation.Chain, error)
	Delete(ctx context.Context, r escalation.Requester, publicID string, version *int64) error
}

// ChainRuns is what the API needs of the runs of Escalation chains (C-32.FR-18): the runs of the current start of
// the chains of Alert Groups, and the recent runs of a chain.
type ChainRuns interface {
	GroupChains(ctx context.Context, publicIDs []string) (map[string][]escalation.GroupChain, error)
	ChainRuns(ctx context.Context, publicID string, after *escalation.RunPosition, limit int) (escalation.RunPage,
		error)
}

// escalationChainsCursor is the cursor of the list of Escalation chains, and escalationRunsCursor that of the runs of
// a chain.
const (
	escalationChainsCursor = "escalation-chains"
	escalationRunsCursor   = "escalation-runs"
)

// escalationRequester is who asks for a change of an Escalation chain through the API.
func escalationRequester(ctx context.Context) (escalation.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return escalation.Requester{}, err
	}
	return escalation.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// ListEscalationChains is listEscalationChains: the chains by name, owned by a Team or by "My Teams", matching q.
func (s *Server) ListEscalationChains(ctx context.Context, req gen.ListEscalationChainsRequestObject) (
	gen.ListEscalationChainsResponseObject, error) {
	p := req.Params
	team, member, err := owningTeamQuery(ctx, p.Team)
	if err != nil {
		return nil, err
	}
	f := escalation.ListFilter{Team: team, MemberID: member, Limit: pageSize(p.Limit)}
	if p.Q != nil {
		f.Query = *p.Q
	}
	var after escalation.Position
	if ok, err := decodeCursor(p.Cursor, escalationChainsCursor, &after); err != nil {
		return nil, err
	} else if ok {
		f.After = &after
	}
	page, err := s.escalationChains.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.EscalationChainList{Items: make([]gen.EscalationChain, 0, len(page.Chains))}
	for _, c := range page.Chains {
		out.Items = append(out.Items, escalationChainOf(c))
	}
	out.NextCursor.SetNull()
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(escalationChainsCursor, *page.Next))
	}
	return gen.ListEscalationChains200JSONResponse(out), nil
}

// CreateEscalationChain is createEscalationChain.
func (s *Server) CreateEscalationChain(ctx context.Context, req gen.CreateEscalationChainRequestObject) (
	gen.CreateEscalationChainResponseObject, error) {
	r, err := escalationRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	c, err := s.escalationChains.Create(ctx, r, escalationInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag, location := etag(c.Version), BasePath+"/escalation-chains/"+c.PublicID
	return gen.CreateEscalationChain201JSONResponse{Body: escalationChainOf(c),
		Headers: gen.CreateEscalationChain201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// GetEscalationChain is getEscalationChain.
func (s *Server) GetEscalationChain(ctx context.Context, req gen.GetEscalationChainRequestObject) (
	gen.GetEscalationChainResponseObject, error) {
	c, err := s.escalationChains.Get(ctx, req.EscalationChainId)
	if err != nil {
		return nil, err
	}
	tag := etag(c.Version)
	return gen.GetEscalationChain200JSONResponse{Body: escalationChainOf(c),
		Headers: gen.GetEscalationChain200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateEscalationChain is updateEscalationChain, with If-Match.
func (s *Server) UpdateEscalationChain(ctx context.Context, req gen.UpdateEscalationChainRequestObject) (
	gen.UpdateEscalationChainResponseObject, error) {
	r, err := escalationRequester(ctx)
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
	c, err := s.escalationChains.Update(ctx, r, req.EscalationChainId, version, escalationInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(c.Version)
	return gen.UpdateEscalationChain200JSONResponse{Body: escalationChainOf(c),
		Headers: gen.UpdateEscalationChain200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteEscalationChain is deleteEscalationChain, with an optional If-Match.
func (s *Server) DeleteEscalationChain(ctx context.Context, req gen.DeleteEscalationChainRequestObject) (
	gen.DeleteEscalationChainResponseObject, error) {
	r, err := escalationRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := optionalVersion(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if err := s.escalationChains.Delete(ctx, r, req.EscalationChainId, version); err != nil {
		return nil, err
	}
	return gen.DeleteEscalationChain204Response{}, nil
}

// escalationInputOf is a chain as internal/escalation takes it.
func escalationInputOf(b gen.EscalationChainInput) escalation.Input {
	in := escalation.Input{Name: b.Name, Description: b.Description, OwningTeamID: owningTeamOf(b.OwningTeamId),
		Steps: make([]escalation.StepInput, len(b.Steps)), ManagedBy: managedByIn(b.ManagedBy)}
	for i, st := range b.Steps {
		in.Steps[i] = stepInputOf(st)
	}
	return in
}

// stepInputOf is a step as internal/escalation takes it, every field it was given and none other.
func stepInputOf(st gen.EscalationStepInput) escalation.StepInput {
	out := escalation.StepInput{Kind: string(st.Kind), ScheduleID: st.ScheduleId, TeamID: st.TeamId,
		GatewayID: st.GatewayId, WaitSeconds: st.WaitSeconds, DestinationID: st.DestinationId,
		RepeatFrom: st.RepeatFrom, RepeatTimes: st.RepeatTimes, WorkingHoursOnly: st.WorkingHoursOnly}
	if st.Importance != nil {
		out.Importance = new(string(*st.Importance))
	}
	if st.UserIds != nil {
		ids := append([]string{}, *st.UserIds...)
		out.UserIDs = &ids
	}
	if st.Everyone != nil {
		out.Everyone = new(string(*st.Everyone))
	}
	if st.CallTarget != nil {
		out.CallTarget = new(string(*st.CallTarget))
	}
	if st.SeverityCondition != nil {
		out.Condition = new(string(*st.SeverityCondition))
	}
	return out
}

// escalationTeamRefOf is the API form of a Team a chain names.
func escalationTeamRefOf(t escalation.TeamRef) gen.TeamRef {
	return gen.TeamRef{Id: t.PublicID, Name: t.Name, Slug: t.Slug, Deleted: t.Deleted}
}

// escalationChainOf is the API form of a chain.
func escalationChainOf(c escalation.Chain) gen.EscalationChain {
	tag := etag(c.Version)
	out := gen.EscalationChain{Id: c.PublicID, Name: c.Name, Description: c.Description, CreatedAt: c.CreatedAt.UTC(),
		Etag: &tag, Steps: make([]gen.EscalationStep, len(c.Steps)),
		Referrers: gen.EscalationChainReferrers{Routes: make([]gen.EscalationChainRouteReferrer, len(c.Referrers.Routes)),
			Teams: make([]gen.EscalationChainTeamReferrer, len(c.Referrers.Teams))},
		Destinations: make([]gen.DestinationRef, len(c.Destinations)),
		Warnings:     make([]gen.EscalationChainWarning, len(c.Warnings)), ManagedBy: managedByOf(c.ManagedBy)}
	if o := c.OwningTeam; o != nil {
		out.OwningTeam = teamRefOf(o.PublicID, o.Name, o.Slug, o.Deleted, true)
		out.OwningTeamId = owningTeamIDOf(o.PublicID, true)
	} else {
		out.OwningTeam = teamRefOf("", "", "", false, false)
		out.OwningTeamId = owningTeamIDOf("", false)
	}
	for i, st := range c.Steps {
		out.Steps[i] = escalationStepOf(st, c.OwningTeam)
	}
	for i, r := range c.Referrers.Routes {
		out.Referrers.Routes[i] = gen.EscalationChainRouteReferrer{Route: gen.EntityRef{Id: r.PublicID, Name: r.Name},
			SeverityCondition: gen.SeverityCondition(r.Condition)}
	}
	for i, t := range c.Referrers.Teams {
		out.Referrers.Teams[i] = gen.EscalationChainTeamReferrer{Team: escalationTeamRefOf(t.Team),
			SeverityCondition: gen.SeverityCondition(t.Condition)}
	}
	for i, d := range c.Destinations {
		out.Destinations[i] = gen.DestinationRef{Id: d.PublicID, Name: d.Name, Type: gen.DestinationType(d.Type),
			Health: healthOf(destinations.Health{State: d.Health, Since: d.BrokenSince, Reason: d.BrokenReason})}
	}
	for i, w := range c.Warnings {
		out.Warnings[i] = gen.EscalationChainWarning{Kind: gen.EscalationChainWarningKind(w.Kind),
			Step: nullable.NewNullNullable[int](), Destination: nullable.NewNullNullable[gen.EntityRef](),
			Reason: unavailableReasonOf(w.Reason)}
		if w.Step != 0 {
			out.Warnings[i].Step.Set(w.Step)
		}
		if w.Destination != nil {
			out.Warnings[i].Destination.Set(gen.EntityRef{Id: w.Destination.PublicID, Name: w.Destination.Name})
		}
	}
	return out
}

// escalationStepOf is the API form of a step of a chain owned by owner: the fields of its kind and what they name.
func escalationStepOf(st escalation.Step, owner *escalation.TeamRef) gen.EscalationStep {
	out := gen.EscalationStep{Kind: gen.EscalationStepKind(st.Kind), Number: st.Number}
	if st.Importance != "" {
		out.Importance = new(gen.PageImportance(st.Importance))
	}
	if sc := st.Schedule; sc != nil {
		out.ScheduleId = new(sc.PublicID)
		out.Schedule = &gen.ScheduleRef{Id: sc.PublicID, Name: sc.Name, Team: escalationTeamRefOf(sc.Team)}
	}
	if st.Kind == escalation.KindPagePeople || st.Kind == escalation.KindCall && st.CallTarget == escalation.CallPeople {
		ids, users := make([]string, len(st.Users)), make([]gen.UserRef, len(st.Users))
		for i, u := range st.Users {
			login := u.Login
			ids[i], users[i] = u.PublicID, gen.UserRef{Id: u.PublicID, Name: u.Name, Login: &login,
				Deactivated: u.Status == "deleted"}
		}
		out.UserIds, out.Users = &ids, &users
	}
	if st.Kind == escalation.KindPageTeam {
		switch {
		case st.Everyone != "":
			out.Everyone = new(gen.EscalationStepEveryone(st.Everyone))
		case st.Team != nil:
			out.TeamId = new(st.Team.PublicID)
			out.Team = new(escalationTeamRefOf(*st.Team))
		case owner != nil:
			out.Team = new(escalationTeamRefOf(*owner))
		}
	}
	if st.CallTarget != "" {
		out.CallTarget = new(gen.EscalationStepCallTarget(st.CallTarget))
	}
	if g := st.Gateway; g != nil {
		out.GatewayId = new(g.PublicID)
		out.Gateway = &gen.EntityRef{Id: g.PublicID, Name: g.Name}
	}
	if st.Kind == escalation.KindWait {
		out.WaitSeconds = new(st.WaitSeconds)
	}
	if d := st.Destination; d != nil {
		out.DestinationId = new(d.PublicID)
		out.Destination = &gen.EntityRef{Id: d.PublicID, Name: d.Name}
	}
	if st.Kind == escalation.KindRepeat {
		out.RepeatFrom, out.RepeatTimes = new(st.RepeatFrom), new(st.RepeatTimes)
	}
	if st.Pages() {
		out.WorkingHoursOnly = new(st.WorkingHoursOnly)
		out.SeverityCondition = new(gen.SeverityCondition(st.Condition))
	}
	return out
}

// ListEscalationChainRuns is listEscalationChainRuns: the recent runs of the chain, newest first, for "How it went".
func (s *Server) ListEscalationChainRuns(ctx context.Context, req gen.ListEscalationChainRunsRequestObject) (
	gen.ListEscalationChainRunsResponseObject, error) {
	if s.chainRuns == nil {
		return nil, escalation.ErrNotFound
	}
	var after escalation.RunPosition
	var at *escalation.RunPosition
	if ok, err := decodeCursor(req.Params.Cursor, escalationRunsCursor+":"+req.EscalationChainId, &after); err != nil {
		return nil, err
	} else if ok {
		at = &after
	}
	page, err := s.chainRuns.ChainRuns(ctx, req.EscalationChainId, at, pageSize(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	out := gen.EscalationRunList{Items: make([]gen.EscalationRun, len(page.Runs))}
	for i, r := range page.Runs {
		out.Items[i] = escalationRunOf(r)
	}
	out.NextCursor.SetNull()
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(escalationRunsCursor+":"+req.EscalationChainId, *page.Next))
	}
	return gen.ListEscalationChainRuns200JSONResponse(out), nil
}

// escalationRunOf is the API form of a run of a chain.
func escalationRunOf(r escalation.ChainRun) gen.EscalationRun {
	out := gen.EscalationRun{Id: r.PublicID, State: gen.EscalationRunState(r.State), StartedAt: r.StartedAt,
		EndedAt: nullableTime(r.EndedAt), Steps: make([]gen.EscalationRunStep, len(r.Steps)),
		AlertGroup: gen.EscalationRunAlertGroup{Id: r.Group.PublicID, Number: int(r.Group.Number),
			Title: r.Group.Title, SeverityLevel: gen.SeverityLevel(r.Group.Severity)}}
	for i, st := range r.Steps {
		step := gen.EscalationRunStep{Number: st.Number, Kind: gen.EscalationStepKind(st.Kind), At: st.At,
			Outcome: gen.EscalationRunStepOutcome(st.Outcome), Pages: make([]gen.EscalationRunPage, len(st.Pages))}
		step.Reason.SetNull()
		if st.Reason != "" {
			step.Reason.Set(gen.EscalationSkipReason(st.Reason))
		}
		for j, p := range st.Pages {
			step.Pages[j] = escalationRunPageOf(p)
		}
		out.Steps[i] = step
	}
	out.StoppedBy.SetNull()
	if r.StoppedBy != nil {
		stop := gen.EscalationRunStop{Command: gen.EscalationRunStopCommand(r.StoppedBy.Command)}
		stop.Actor.SetNull()
		stop.Destination.SetNull()
		if a := r.StoppedBy.Actor; a != nil {
			ref := gen.ActorRef{Kind: gen.ActorRefKind(a.Kind), Id: a.PublicID, Name: a.Name, Deactivated: a.Deactivated}
			if a.Login != "" {
				ref.Login = &a.Login
			}
			stop.Actor.Set(ref)
		}
		out.StoppedBy.Set(stop)
	}
	return out
}

// escalationRunPageOf is the API form of a Page a step of a run made.
func escalationRunPageOf(p escalation.RunPageView) gen.EscalationRunPage {
	out := gen.EscalationRunPage{Kind: gen.EscalationRunPageKind(p.Kind), Users: make([]gen.UserRef, len(p.Users)),
		Outcome: gen.EscalationRunPageOutcome(p.Outcome)}
	for i, u := range p.Users {
		login := u.Login
		out.Users[i] = gen.UserRef{Id: u.PublicID, Name: u.Name, Login: &login, Deactivated: u.Status == "deleted"}
	}
	out.Team.SetNull()
	if p.Team != nil {
		out.Team.Set(escalationTeamRefOf(*p.Team))
	}
	out.Everyone.SetNull()
	if p.Everyone != "" {
		out.Everyone.Set(gen.PageEveryone(p.Everyone))
	}
	out.Gateway.SetNull()
	if g := p.Gateway; g != nil {
		out.Gateway.Set(gen.EntityRef{Id: g.PublicID, Name: g.Name})
	}
	out.Reason.SetNull()
	if p.Reason != "" {
		out.Reason.Set(gen.PageNotDeliveredReason(p.Reason))
	}
	return out
}

// alertGroupChainsOf is the API form of the runs of the current start of an Alert Group's chains.
func alertGroupChainsOf(list []escalation.GroupChain) []gen.AlertGroupChain {
	out := make([]gen.AlertGroupChain, len(list))
	for i, c := range list {
		a := gen.AlertGroupChain{RunId: c.RunID, ChainName: c.ChainName, State: gen.EscalationRunState(c.State),
			StartedAt: c.StartedAt, NextStepAt: nullableTime(c.NextStepAt),
			Sources: make([]gen.EscalationRunSource, len(c.Sources))}
		a.Chain = escalationChainRefOf(c.Chain)
		a.Step.SetNull()
		if c.Step != nil {
			a.Step.Set(gen.EscalationStepPosition{Number: c.Step.Number, Kind: gen.EscalationStepKind(c.Step.Kind)})
		}
		for j, src := range c.Sources {
			ref := gen.EscalationRunSource{Kind: gen.EscalationRunSourceKind(src.Kind)}
			ref.Team.SetNull()
			if src.Team != nil {
				ref.Team.Set(escalationTeamRefOf(*src.Team))
			}
			a.Sources[j] = ref
		}
		out[i] = a
	}
	return out
}

// escalationChainRefOf is the API form of the chain of a run, null once it was deleted.
func escalationChainRefOf(c *escalation.ChainRef) nullable.Nullable[gen.EscalationChainRef] {
	var out nullable.Nullable[gen.EscalationChainRef]
	if c == nil {
		out.SetNull()
		return out
	}
	ref := gen.EscalationChainRef{Id: c.PublicID, Name: c.Name}
	ref.OwningTeam.SetNull()
	if c.OwningTeam != nil {
		ref.OwningTeam.Set(escalationTeamRefOf(*c.OwningTeam))
	}
	out.Set(ref)
	return out
}

// groupChains reads the runs of the chains of the Alert Groups publicIDs, none without runs.
func (s *Server) groupChains(ctx context.Context, publicIDs ...string) (map[string][]escalation.GroupChain, error) {
	if s.chainRuns == nil {
		return map[string][]escalation.GroupChain{}, nil
	}
	return s.chainRuns.GroupChains(ctx, publicIDs)
}
