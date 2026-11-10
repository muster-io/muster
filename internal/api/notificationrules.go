// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/notificationrules"
	"github.com/muster-io/muster/internal/publicid"
)

// NotificationRules is what the API needs of internal/notificationrules (C-31).
type NotificationRules interface {
	Get(ctx context.Context, userID int64) (notificationrules.Rules, error)
	GetOfUser(ctx context.Context, userPublicID string) (notificationrules.Rules, error)
	Update(ctx context.Context, r notificationrules.Requester, userID int64, version *int64,
		in notificationrules.Update) (notificationrules.Rules, error)
	UpdateOfUser(ctx context.Context, r notificationrules.Requester, userPublicID string, version *int64,
		in notificationrules.Update) (notificationrules.Rules, error)
	Reset(ctx context.Context, r notificationrules.Requester, userID int64) (notificationrules.Rules, error)
}

// rulesRequester is who changes Notification rules: the signed-in User, with the actor, the Transport and the client
// address of the Audit log entry.
func rulesRequester(ctx context.Context) (notificationrules.Requester, int64, error) {
	id, err := identity(ctx)
	if err != nil {
		return notificationrules.Requester{}, 0, err
	}
	return notificationrules.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)},
		id.Session.User.ID, nil
}

// GetMyNotificationRules is getMyNotificationRules: the caller's rules in effect (C-31.FR-1, FR-2); the middleware
// lets only the web session through.
func (s *Server) GetMyNotificationRules(ctx context.Context, _ gen.GetMyNotificationRulesRequestObject) (
	gen.GetMyNotificationRulesResponseObject, error) {
	_, userID, err := rulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.notificationRules.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	tag := etag(rules.Version)
	return gen.GetMyNotificationRules200JSONResponse{Body: notificationRulesOf(rules),
		Headers: gen.GetMyNotificationRules200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateMyNotificationRules is updateMyNotificationRules with If-Match: the caller sets the state of their rules or
// custom sequences from the web session (C-31.FR-4, FR-5).
func (s *Server) UpdateMyNotificationRules(ctx context.Context, req gen.UpdateMyNotificationRulesRequestObject) (
	gen.UpdateMyNotificationRulesResponseObject, error) {
	r, userID, err := rulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	rules, err := s.notificationRules.Update(ctx, r, userID, version, rulesUpdateOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(rules.Version)
	return gen.UpdateMyNotificationRules200JSONResponse{Body: notificationRulesOf(rules),
		Headers: gen.UpdateMyNotificationRules200ResponseHeaders{ETag: &tag}}, nil
}

// ResetMyNotificationRules is resetMyNotificationRules: "Reset to the Organization's rules" (C-31.FR-2).
func (s *Server) ResetMyNotificationRules(ctx context.Context, _ gen.ResetMyNotificationRulesRequestObject) (
	gen.ResetMyNotificationRulesResponseObject, error) {
	r, userID, err := rulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.notificationRules.Reset(ctx, r, userID)
	if err != nil {
		return nil, err
	}
	tag := etag(rules.Version)
	return gen.ResetMyNotificationRules200JSONResponse{Body: notificationRulesOf(rules),
		Headers: gen.ResetMyNotificationRules200ResponseHeaders{ETag: &tag}}, nil
}

// GetUserNotificationRules is getUserNotificationRules: the rules of a User, for users:read (C-31.FR-4).
func (s *Server) GetUserNotificationRules(ctx context.Context, req gen.GetUserNotificationRulesRequestObject) (
	gen.GetUserNotificationRulesResponseObject, error) {
	rules, err := s.notificationRules.GetOfUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	tag := etag(rules.Version)
	return gen.GetUserNotificationRules200JSONResponse{Body: notificationRulesOf(rules),
		Headers: gen.GetUserNotificationRules200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateUserNotificationRules is updateUserNotificationRules with If-Match: an Admin sets the state or the sequences
// of a User's rules (C-31.FR-4). An Admin's own rules change from the web session only, as under /me: with a token it
// is 403 session_required.
func (s *Server) UpdateUserNotificationRules(ctx context.Context, req gen.UpdateUserNotificationRulesRequestObject) (
	gen.UpdateUserNotificationRulesResponseObject, error) {
	r, _, err := rulesRequester(ctx)
	if err != nil {
		return nil, err
	}
	if id, _ := identity(ctx); id.Token != nil && id.Session.User.PublicID != "" {
		if norm, err := publicid.Parse(publicid.User, req.UserId); err == nil && norm == id.Session.User.PublicID {
			return nil, errSessionRequired
		}
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	rules, err := s.notificationRules.UpdateOfUser(ctx, r, req.UserId, version, rulesUpdateOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(rules.Version)
	return gen.UpdateUserNotificationRules200JSONResponse{Body: notificationRulesOf(rules),
		Headers: gen.UpdateUserNotificationRules200ResponseHeaders{ETag: &tag}}, nil
}

// rulesUpdateOf is a change of rules as internal/notificationrules takes it.
func rulesUpdateOf(b gen.NotificationRulesUpdate) notificationrules.Update {
	return notificationrules.Update{State: string(b.State), GatewayID: b.GatewayId,
		Rules: notificationrules.SetInput{Normal: actionInputsOf(b.Normal), Important: actionInputsOf(b.Important)}}
}

// actionInputsOf is a rule of a request body; nil when it is missing. The fields a read sets are ignored.
func actionInputsOf(r *[]gen.NotificationRuleAction) *[]notificationrules.ActionInput {
	if r == nil {
		return nil
	}
	out := make([]notificationrules.ActionInput, len(*r))
	for i, a := range *r {
		out[i] = notificationrules.ActionInput{Kind: string(a.Kind), GatewayID: a.GatewayId,
			WaitSeconds: a.WaitSeconds}
	}
	return &out
}

// ruleSetInputOf is the Notification rules of a request body as internal/organization takes them; nil when missing.
func ruleSetInputOf(s *gen.NotificationRuleSet) *notificationrules.SetInput {
	if s == nil {
		return nil
	}
	return &notificationrules.SetInput{Normal: actionInputsOf(&s.Normal), Important: actionInputsOf(&s.Important)}
}

// notificationRulesOf is the API form of a User's rules.
func notificationRulesOf(r notificationrules.Rules) gen.NotificationRules {
	tag := etag(r.Version)
	out := gen.NotificationRules{State: gen.NotificationRulesState(r.State), Normal: ruleActionsOf(r.Rules.Normal),
		Important: ruleActionsOf(r.Rules.Important), Etag: &tag}
	if r.Gateway != nil {
		out.Gateway.Set(gen.EntityRef{Id: r.Gateway.PublicID, Name: r.Gateway.Name})
	} else {
		out.Gateway.SetNull()
	}
	return out
}

// ruleSetOf is the API form of the Organization's rules.
func ruleSetOf(v notificationrules.SetView) gen.NotificationRuleSet {
	return gen.NotificationRuleSet{Normal: ruleActionsOf(v.Normal), Important: ruleActionsOf(v.Important)}
}

func ruleActionsOf(r []notificationrules.ActionView) []gen.NotificationRuleAction {
	out := make([]gen.NotificationRuleAction, len(r))
	for i, a := range r {
		available := a.Available
		g := gen.NotificationRuleAction{Kind: gen.NotificationRuleActionKind(a.Kind), Available: &available}
		switch a.Kind {
		case notificationrules.KindCall:
			id := a.GatewayID
			g.GatewayId = &id
		case notificationrules.KindWait:
			wait := a.WaitSeconds
			g.WaitSeconds = &wait
		}
		if a.Gateway != nil {
			g.Gateway.Set(gen.EntityRef{Id: a.Gateway.PublicID, Name: a.Gateway.Name})
		} else {
			g.Gateway.SetNull()
		}
		if a.Reason != "" {
			g.UnavailableReason.Set(gen.NotificationRuleUnavailableReason(a.Reason))
		} else {
			g.UnavailableReason.SetNull()
		}
		out[i] = g
	}
	return out
}
