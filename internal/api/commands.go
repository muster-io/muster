// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/groups"
)

// Commands is what the API needs of the command layer of internal/groups (ADR-0016): the Commands of C-10, Add Note
// included, and bulk commands, each checked by the dispatcher, Permission included.
type Commands interface {
	Acknowledge(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
	Unacknowledge(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
	Resolve(ctx context.Context, c groups.Caller, publicID string, note *string) (groups.Result, error)
	Unresolve(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
	Snooze(ctx context.Context, c groups.Caller, publicID string, end groups.SnoozeEnd) (groups.Result, error)
	Unsnooze(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
	AddNote(ctx context.Context, c groups.Caller, publicID, body string) (groups.NoteView, error)
	Bulk(ctx context.Context, c groups.Caller, r groups.BulkRequest) ([]groups.BulkItem, error)
}

// caller is the identity of the request as the command layer takes it: a session's User with the Transport ui, a
// Personal access token's User or a Service account with api, the Permissions of the request and the client address.
func caller(ctx context.Context) (groups.Caller, error) {
	id, err := identity(ctx)
	if err != nil {
		return groups.Caller{}, err
	}
	return groups.Caller{Actor: id.Actor(), Transport: id.Transport, Permissions: id.Permissions,
		Address: clientAddress(ctx)}, nil
}

// command runs one Command for the caller and answers its outcome with the Alert Group.
func (s *Server) command(ctx context.Context, run func(groups.Caller) (groups.Result, error)) (gen.CommandResult,
	error) {
	c, err := caller(ctx)
	if err != nil {
		return gen.CommandResult{}, err
	}
	r, err := run(c)
	if err != nil {
		return gen.CommandResult{}, err
	}
	return gen.CommandResult{Outcome: gen.CommandOutcome(r.Outcome), AlertGroup: alertGroupOf(r.Group, c)}, nil
}

// AcknowledgeAlertGroup is acknowledgeAlertGroup.
func (s *Server) AcknowledgeAlertGroup(ctx context.Context, req gen.AcknowledgeAlertGroupRequestObject) (
	gen.AcknowledgeAlertGroupResponseObject, error) {
	out, err := s.command(ctx, func(c groups.Caller) (groups.Result, error) {
		return s.commands.Acknowledge(ctx, c, req.AlertGroupId)
	})
	if err != nil {
		return nil, err
	}
	return gen.AcknowledgeAlertGroup200JSONResponse(out), nil
}

// UnacknowledgeAlertGroup is unacknowledgeAlertGroup.
func (s *Server) UnacknowledgeAlertGroup(ctx context.Context, req gen.UnacknowledgeAlertGroupRequestObject) (
	gen.UnacknowledgeAlertGroupResponseObject, error) {
	out, err := s.command(ctx, func(c groups.Caller) (groups.Result, error) {
		return s.commands.Unacknowledge(ctx, c, req.AlertGroupId)
	})
	if err != nil {
		return nil, err
	}
	return gen.UnacknowledgeAlertGroup200JSONResponse(out), nil
}

// ResolveAlertGroup is resolveAlertGroup, with an optional Note that the command layer records after the resolve.
func (s *Server) ResolveAlertGroup(ctx context.Context, req gen.ResolveAlertGroupRequestObject) (
	gen.ResolveAlertGroupResponseObject, error) {
	var note *string
	if req.Body != nil {
		note = req.Body.Note
	}
	out, err := s.command(ctx, func(c groups.Caller) (groups.Result, error) {
		return s.commands.Resolve(ctx, c, req.AlertGroupId, note)
	})
	if err != nil {
		return nil, err
	}
	return gen.ResolveAlertGroup200JSONResponse(out), nil
}

// UnresolveAlertGroup is unresolveAlertGroup.
func (s *Server) UnresolveAlertGroup(ctx context.Context, req gen.UnresolveAlertGroupRequestObject) (
	gen.UnresolveAlertGroupResponseObject, error) {
	out, err := s.command(ctx, func(c groups.Caller) (groups.Result, error) {
		return s.commands.Unresolve(ctx, c, req.AlertGroupId)
	})
	if err != nil {
		return nil, err
	}
	return gen.UnresolveAlertGroup200JSONResponse(out), nil
}

// SnoozeAlertGroup is snoozeAlertGroup: the request names until or no_end, which the command layer checks.
func (s *Server) SnoozeAlertGroup(ctx context.Context, req gen.SnoozeAlertGroupRequestObject) (
	gen.SnoozeAlertGroupResponseObject, error) {
	out, err := s.command(ctx, func(c groups.Caller) (groups.Result, error) {
		return s.commands.Snooze(ctx, c, req.AlertGroupId, snoozeEndOf(req.Body))
	})
	if err != nil {
		return nil, err
	}
	return gen.SnoozeAlertGroup200JSONResponse(out), nil
}

// UnsnoozeAlertGroup is unsnoozeAlertGroup.
func (s *Server) UnsnoozeAlertGroup(ctx context.Context, req gen.UnsnoozeAlertGroupRequestObject) (
	gen.UnsnoozeAlertGroupResponseObject, error) {
	out, err := s.command(ctx, func(c groups.Caller) (groups.Result, error) {
		return s.commands.Unsnooze(ctx, c, req.AlertGroupId)
	})
	if err != nil {
		return nil, err
	}
	return gen.UnsnoozeAlertGroup200JSONResponse(out), nil
}

// snoozeEndOf is the end of a Snooze as the request gives it.
func snoozeEndOf(r *gen.SnoozeRequest) groups.SnoozeEnd {
	if r == nil {
		return groups.SnoozeEnd{}
	}
	return groups.SnoozeEnd{Until: r.Until, NoEnd: r.NoEnd != nil && bool(*r.NoEnd)}
}

// RunBulkCommand is runBulkCommand: one outcome per Alert Group, in the order of the request.
func (s *Server) RunBulkCommand(ctx context.Context, req gen.RunBulkCommandRequestObject) (
	gen.RunBulkCommandResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	r := groups.BulkRequest{Command: groups.Command(b.Command), IDs: b.AlertGroupIds, Note: b.Note}
	if b.Snooze != nil {
		end := snoozeEndOf(b.Snooze)
		r.Snooze = &end
	}
	items, err := s.commands.Bulk(ctx, c, r)
	if err != nil {
		return nil, err
	}
	out := gen.BulkCommandResult{Results: make([]gen.BulkCommandItem, 0, len(items))}
	for _, it := range items {
		item := gen.BulkCommandItem{AlertGroupId: it.PublicID, Outcome: gen.BulkOutcome(it.Outcome)}
		if it.Number != nil {
			item.Number.Set(int(*it.Number))
		} else {
			item.Number.SetNull()
		}
		if it.Code != "" {
			item.Code.Set(it.Code)
			item.Message.Set(it.Message)
		} else {
			item.Code.SetNull()
			item.Message.SetNull()
		}
		out.Results = append(out.Results, item)
	}
	return gen.RunBulkCommand200JSONResponse(out), nil
}
