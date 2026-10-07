// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/publicid"
)

// Command is a Command on an Alert Group (C-10.FR-1, CommandName).
type Command string

// The Commands of C-10.FR-1 but Add Note, which S-063 adds.
const (
	CommandAcknowledge   Command = "acknowledge"
	CommandUnacknowledge Command = "unacknowledge"
	CommandResolve       Command = "resolve"
	CommandUnresolve     Command = "unresolve"
	CommandSnooze        Command = "snooze"
	CommandUnsnooze      Command = "unsnooze"
)

// The Permissions of the Commands (reference.md, Permissions).
const (
	PermissionAcknowledge auth.Permission = "alert-groups:acknowledge"
	PermissionResolve     auth.Permission = "alert-groups:resolve"
	PermissionSnooze      auth.Permission = "alert-groups:snooze"
)

// Permission is the Permission the Command needs.
func (c Command) Permission() auth.Permission {
	switch c {
	case CommandAcknowledge, CommandUnacknowledge:
		return PermissionAcknowledge
	case CommandResolve, CommandUnresolve:
		return PermissionResolve
	default:
		return PermissionSnooze
	}
}

// ResourceAlertGroup is the resource type of the Audit log entries of Commands.
const ResourceAlertGroup = "alert_group"

// The Audit log actions of the Commands (C-10.FR-12); alert_group.note_added is S-063's.
const (
	ActionAcknowledged   = "alert_group.acknowledged"
	ActionTakenOver      = "alert_group.taken_over"
	ActionUnacknowledged = "alert_group.unacknowledged"
	ActionResolved       = "alert_group.resolved"
	ActionUnresolved     = "alert_group.unresolved"
	ActionSnoozed        = "alert_group.snoozed"
	ActionUnsnoozed      = "alert_group.unsnoozed"
)

// The refusal codes of command-refused (C-10.FR-2), forbidden for a missing Permission, and the codes of the other
// outcomes of a bulk command.
const (
	CodeAlreadyResolved       = "already_resolved"
	CodeNotAcknowledged       = "not_acknowledged"
	CodeNotSnoozed            = "not_snoozed"
	CodeNotResolved           = "not_resolved"
	CodeAllAlertsResolved     = "all_alerts_resolved"
	CodeResolvedAutomatically = "resolved_automatically"
	CodeNewerGroupExists      = "newer_alert_group_exists"
	CodeOwnerMustBeUser       = "owner_must_be_user"
	CodeRouteDeleted          = "route_deleted"
	CodeForbidden             = "forbidden"
	CodeOwnedByOther          = "owned_by_other"
	CodeNotFound              = "not_found"
)

// The field codes of the requests of Commands.
const (
	CodeRequired      = "required"
	CodeOneOfRequired = "one_of_required"
)

// messages are the messages of the refusals (C-10.FR-2); newer_alert_group_exists names the Alert Group.
var messages = map[string]string{
	CodeAlreadyResolved:       "already resolved",
	CodeNotAcknowledged:       "not acknowledged",
	CodeNotSnoozed:            "not snoozed",
	CodeNotResolved:           "not resolved",
	CodeAllAlertsResolved:     "all alerts have already resolved",
	CodeResolvedAutomatically: "resolved automatically — it reopens by itself when an alert returns",
	CodeOwnerMustBeUser:       "a Service account cannot be an Owner",
	CodeRouteDeleted:          "its Route was deleted",
}

// Caller is who runs a Command and how: a User — through the web session, or a Personal access token, which acts as
// its User — or a Service account through its token; Permissions are those of the request, a Personal access token's
// narrowed ones (C-04.FR-7), Transport is how it reached Muster and Address the client address.
type Caller struct {
	Actor       audit.Actor
	Transport   audit.Transport
	Permissions []auth.Permission
	Address     netip.Addr
}

func (c Caller) can(p auth.Permission) bool { return slices.Contains(c.Permissions, p) }

// user is the User the caller acts as; 0 for a Service account, which cannot become an Owner (C-04.FR-2).
func (c Caller) user() int64 {
	if c.Actor.Kind != audit.ActorUser {
		return 0
	}
	return c.Actor.ID
}

func (c Caller) actor() Actor {
	return Actor{Kind: c.Actor.Kind, Transport: c.Transport, Person: c.Actor, Address: c.Address}
}

// person is the User or the Service account of the caller, as the columns of a resolver or a snoozer take it.
func (c Caller) person() (user, account *int64) {
	if c.Actor.Kind == audit.ActorServiceAccount {
		return nil, ptr(c.Actor.ID)
	}
	return ptr(c.Actor.ID), nil
}

// Outcome is what a Command did: done, or unchanged for an idempotent repeat such as Acknowledge by the current Owner.
type Outcome string

// The outcomes of a Command that ran.
const (
	OutcomeDone      Outcome = "done"
	OutcomeUnchanged Outcome = "unchanged"
)

// Result is the outcome of a Command and the Alert Group after it.
type Result struct {
	Outcome Outcome
	Group   View
}

// GroupRef names an Alert Group by public_id and #N.
type GroupRef struct {
	PublicID string
	Number   int64
}

// RefusedError is a Command whose precondition does not hold or whose caller cannot take part (409
// command-refused); it changed nothing.
type RefusedError struct {
	Code    string
	Message string
	// Related is the newer open Alert Group of newer_alert_group_exists.
	Related *GroupRef
}

func (e *RefusedError) Error() string { return "command refused: " + e.Code }

func refusal(code string) *RefusedError {
	return &RefusedError{Code: code, Message: messages[code]}
}

// ForbiddenError is a Command the caller lacks the Permission of (403 forbidden); it changed nothing.
type ForbiddenError struct {
	Permission auth.Permission
}

func (e *ForbiddenError) Error() string {
	return "the command needs the permission " + string(e.Permission)
}

// skippedError is an Alert Group that a bulk Acknowledge leaves to the other user who owns it (C-10.FR-14).
type skippedError struct {
	owner int64
}

func (e *skippedError) Error() string { return "owned by another user" }

// SnoozeEnd is the end of a Snooze, chosen explicitly: a future Until, or NoEnd (C-10.FR-6).
type SnoozeEnd struct {
	Until *time.Time
	NoEnd bool
}

// check refuses a Snooze with neither or both of the choices, or an end that is not in the future at now; pointer is
// where the Snooze is in the request.
func (e SnoozeEnd) check(pointer string, now time.Time) error {
	if (e.Until == nil) != e.NoEnd {
		return &FieldError{Pointer: pointer, Code: CodeOneOfRequired,
			Detail: "Give exactly one of until and no_end: true."}
	}
	if e.Until != nil && !e.Until.After(now) {
		return &FieldError{Pointer: pointer + "/until", Code: CodeOutOfRange, Detail: "The Snooze end is not in the future."}
	}
	return nil
}

// Acknowledge is the Command Acknowledge of the Alert Group publicID (C-10.FR-1, FR-4): the caller becomes its Owner;
// by another user while acknowledged it is a Takeover, by the current Owner it changes nothing.
func (s *Service) Acknowledge(ctx context.Context, c Caller, publicID string) (Result, error) {
	return s.command(ctx, c, CommandAcknowledge, publicID, args{})
}

// Unacknowledge is the Command Unacknowledge: firing without an Owner.
func (s *Service) Unacknowledge(ctx context.Context, c Caller, publicID string) (Result, error) {
	return s.command(ctx, c, CommandUnacknowledge, publicID, args{})
}

// Resolve is the Command Resolve: resolved by the caller, with the Grace period of the Alerts still firing
// (C-09.FR-5). A Note in the same request is refused as unsupported until Notes exist (S-063).
func (s *Service) Resolve(ctx context.Context, c Caller, publicID string, note *string) (Result, error) {
	return s.command(ctx, c, CommandResolve, publicID, args{note: note})
}

// Unresolve is the Command Unresolve of an Alert Group a person resolved (C-10.FR-7): firing again without an Owner.
func (s *Service) Unresolve(ctx context.Context, c Caller, publicID string) (Result, error) {
	return s.command(ctx, c, CommandUnresolve, publicID, args{})
}

// Snooze is the Command Snooze until end, which names exactly one future until or no_end (C-10.FR-6).
func (s *Service) Snooze(ctx context.Context, c Caller, publicID string, end SnoozeEnd) (Result, error) {
	return s.command(ctx, c, CommandSnooze, publicID, args{end: &end})
}

// Unsnooze is the Command Unsnooze: firing without an Owner.
func (s *Service) Unsnooze(ctx context.Context, c Caller, publicID string) (Result, error) {
	return s.command(ctx, c, CommandUnsnooze, publicID, args{})
}

// args are the arguments of a Command: the end of a Snooze, a Note of Resolve, whether it is an item of a bulk command
// — whose Acknowledge skips an Alert Group another user owns — and whether its Permission and arguments were checked
// already, once for the whole bulk command.
type args struct {
	end     *SnoozeEnd
	note    *string
	bulk    bool
	checked bool
}

// check checks the arguments of a Command after its Permission; prefix is where they are in the request.
func (a args) check(prefix string, now time.Time) error {
	if a.note != nil {
		return &FieldError{Pointer: "/note", Code: CodeUnsupported,
			Detail: "Notes are not available yet; resolve without a note."}
	}
	if a.end != nil {
		return a.end.check(prefix, now)
	}
	return nil
}

// command runs one Command and reads the Alert Group after it.
func (s *Service) command(ctx context.Context, c Caller, cmd Command, publicID string, a args) (Result, error) {
	outcome, _, err := s.run(ctx, c, cmd, publicID, a)
	if err != nil {
		return Result{}, err
	}
	v, err := s.Get(ctx, publicID)
	if err != nil {
		return Result{}, err
	}
	return Result{Outcome: outcome, Group: v}, nil
}

// permit is the first step of the dispatcher for a Command: the caller holds its Permission, or the Command is
// refused before anything is read, with command_refused and the code forbidden; n is the number of Alert Groups it
// was for, counted as refused.
func (s *Service) permit(ctx context.Context, c Caller, cmd Command, group string, n int) error {
	if c.can(cmd.Permission()) {
		return nil
	}
	s.refused(ctx, c, cmd, group, CodeForbidden, n)
	return &ForbiddenError{Permission: cmd.Permission()}
}

// refused counts and logs a refusal.
func (s *Service) refused(ctx context.Context, c Caller, cmd Command, group, code string, n int) {
	metrics.Commands.With(string(cmd), string(c.Transport), "refused").Add(n)
	s.log.Log(ctx, logging.CommandRefused, logging.F("command", string(cmd)), logging.F("group", group),
		logging.F("actor", c.Actor.PublicID), logging.F("transport", string(c.Transport)), logging.F("code", code))
}

// run is the dispatcher's entry for one Command on the Alert Group publicID, which every Transport reaches through
// the Commands above: the Permission and the arguments, unless a bulk command checked them already, then, in a
// transaction of its own on the locked row, precondition, transition, Audit log, Timeline, re-render and hints. A
// refusal rolls everything back and writes only command_refused. It returns the outcome and the #N of the Alert
// Group, 0 when there is none.
func (s *Service) run(ctx context.Context, c Caller, cmd Command, publicID string, a args) (Outcome, int64, error) {
	if !a.checked {
		if err := s.permit(ctx, c, cmd, publicID, 1); err != nil {
			return "", 0, err
		}
		if err := a.check("", s.clock.Now()); err != nil {
			return "", 0, err
		}
	}
	var number int64
	outcome, after, err := s.transact(ctx, c, cmd, publicID, a, &number)
	count := func(o string) { metrics.Commands.With(string(cmd), string(c.Transport), o).Inc() }
	if r, ok := errors.AsType[*RefusedError](err); ok {
		s.refused(ctx, c, cmd, publicID, r.Code, 1)
		return "", number, err
	}
	if _, ok := errors.AsType[*skippedError](err); ok {
		count("skipped")
		return "", number, err
	}
	if errors.Is(err, ErrNotFound) {
		count("failed")
		return "", 0, err
	}
	if err != nil {
		return "", number, err
	}
	after.run(ctx)
	count(string(outcome))
	s.log.Log(ctx, logging.CommandExecuted, logging.F("command", string(cmd)), logging.F("group", publicID),
		logging.F("actor", c.Actor.PublicID), logging.F("transport", string(c.Transport)),
		logging.F("outcome", string(outcome)))
	return outcome, number, nil
}

// transact is the transaction of run; it sets number once the Alert Group is locked.
func (s *Service) transact(ctx context.Context, c Caller, cmd Command, publicID string, a args, number *int64) (
	Outcome, committed, error) {
	id, err := publicid.Parse(publicid.AlertGroup, publicID)
	if err != nil {
		return "", nil, ErrNotFound
	}
	var after committed
	outcome := OutcomeDone
	err = s.store.InTx(ctx, func(q Queries) error {
		after, outcome = nil, OutcomeDone
		row, err := q.GetGroupID(ctx, dbgen.GetGroupIDParams{OrgID: s.orgID, PublicID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read alert group %s: %w", id, err)
		}
		routeDeleted := false
		if cmd == CommandUnresolve {
			if routeDeleted, err = s.lockForUnresolve(ctx, q, row.ID); err != nil {
				return err
			}
		}
		locked, err := lock(ctx, q, s.orgID, []int64{row.ID})
		if err != nil {
			return err
		}
		g := locked[row.ID]
		if g == nil {
			return ErrNotFound
		}
		*number = g.Number
		t, err := s.transition(ctx, q, c, cmd, g, a, routeDeleted)
		if err != nil {
			return err
		}
		if err := s.d.dispatch(ctx, q, g, c.actor(), t, &after); err != nil {
			return err
		}
		if a, ok := t.(*acknowledge); ok {
			if a.unchanged {
				outcome = OutcomeUnchanged
			}
			if a.first {
				return s.observeTimeToAck(ctx, q, g, &after)
			}
		}
		return nil
	})
	return outcome, after, err
}

// observeTimeToAck observes muster_alert_group_time_to_ack_seconds once the first acknowledgement committed.
func (s *Service) observeTimeToAck(ctx context.Context, q Queries, g *Group, after *committed) error {
	route, err := s.routePublicID(ctx, q, g.RouteID)
	if err != nil {
		return err
	}
	seconds := max(g.FirstAcknowledgedAt.Sub(g.CreatedAt).Seconds(), 0)
	after.add(func(context.Context) { metrics.AlertGroupTimeToAck.With(route).Update(seconds) })
	return nil
}

// transition is the transition of a Command on the locked Alert Group g, with what it needs to know.
func (s *Service) transition(ctx context.Context, q Queries, c Caller, cmd Command, g *Group, a args,
	routeDeleted bool) (transition, error) {
	now := s.clock.Now().UTC()
	switch cmd {
	case CommandAcknowledge:
		return &acknowledge{user: c.user(), bulk: a.bulk, now: now, orgID: s.orgID}, nil
	case CommandUnacknowledge:
		return unacknowledge{orgID: s.orgID}, nil
	case CommandResolve:
		policy, err := q.GetRoutePolicy(ctx, dbgen.GetRoutePolicyParams{OrgID: s.orgID, ID: g.RouteID})
		if err != nil {
			return nil, fmt.Errorf("read route %d: %w", g.RouteID, err)
		}
		user, account := c.person()
		return resolveCommand{user: user, account: account, now: now, grace: seconds(policy.GracePeriodSeconds),
			orgID: s.orgID}, nil
	case CommandUnresolve:
		newer, err := s.newerOpen(ctx, q, g)
		if err != nil {
			return nil, err
		}
		return unresolve{newer: newer, routeDeleted: routeDeleted, orgID: s.orgID}, nil
	case CommandSnooze:
		urgent, err := s.urgentNow(ctx, q, g)
		if err != nil {
			return nil, err
		}
		user, account := c.person()
		return snooze{end: *a.end, user: user, account: account, urgent: urgent}, nil
	case CommandUnsnooze:
		return unsnooze{}, nil
	}
	return nil, fmt.Errorf("unknown command %q", cmd)
}

// lockForUnresolve takes the locks of grouping before Unresolve opens an Alert Group again, in its order: the Route
// FOR SHARE, so that it is not deleted meanwhile, then the counter row, so that no other Alert Group of the key opens
// until the Command commits. It reports whether the Route was deleted already.
func (s *Service) lockForUnresolve(ctx context.Context, q Queries, id int64) (bool, error) {
	peek, err := q.PeekGroup(ctx, dbgen.PeekGroupParams{OrgID: s.orgID, ID: id})
	if err != nil {
		return false, fmt.Errorf("read alert group %d: %w", id, err)
	}
	routes, err := q.LockRoutes(ctx, dbgen.LockRoutesParams{OrgID: s.orgID, Ids: []int64{peek.RouteID}})
	if err != nil {
		return false, fmt.Errorf("lock the route: %w", err)
	}
	if err := q.EnsureCounter(ctx, s.orgID); err != nil {
		return false, fmt.Errorf("create the alert group counter: %w", err)
	}
	if _, err := q.LockCounter(ctx, s.orgID); err != nil {
		return false, fmt.Errorf("lock the alert group counter: %w", err)
	}
	return len(routes) == 0, nil
}

// newerOpen is the open Alert Group of the Route and Group key values of a person-resolved g that takes part in
// grouping, nil when there is none; an Alert Group moved to the Default route stays out of grouping and has none.
func (s *Service) newerOpen(ctx context.Context, q Queries, g *Group) (*GroupRef, error) {
	if g.Status != StatusResolved || g.ResolvedByKind == nil || *g.ResolvedByKind != ResolvedByUser ||
		g.MovedFromRouteID != nil {
		return nil, nil
	}
	id, err := q.FindOpenGroup(ctx, dbgen.FindOpenGroupParams{OrgID: s.orgID, RouteID: g.RouteID,
		GroupKeySha256: g.KeySHA})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find the open alert group of #%d: %w", g.Number, err)
	}
	r, err := q.GetGroupRef(ctx, dbgen.GetGroupRefParams{OrgID: s.orgID, ID: id})
	if err != nil {
		return nil, fmt.Errorf("read alert group %d: %w", id, err)
	}
	return &GroupRef{PublicID: r.PublicID, Number: r.Number}, nil
}

// urgentNow is the urgency of g as its Route and organization.critical_is_urgent give it now (C-08.FR-6).
func (s *Service) urgentNow(ctx context.Context, q Queries, g *Group) (bool, error) {
	policy, err := q.GetRoutePolicy(ctx, dbgen.GetRoutePolicyParams{OrgID: s.orgID, ID: g.RouteID})
	if err != nil {
		return false, fmt.Errorf("read route %d: %w", g.RouteID, err)
	}
	st, err := q.GetGroupingSettings(ctx, s.orgID)
	if err != nil {
		return false, fmt.Errorf("read the grouping settings: %w", err)
	}
	return urgentOf(&routeInfo{Urgent: policy.Urgent}, g.Severity, st.CriticalIsUrgent), nil
}

// clearSnooze ends the Snooze of g.
func clearSnooze(g *Group) {
	g.SnoozeUntil, g.SnoozeNoEnd, g.SnoozedWhileUrgent, g.SnoozedByUserID, g.SnoozedByServiceAccount =
		nil, false, false, nil, nil
}

// acknowledge is Acknowledge by the User user, 0 for a Service account; first is set at the first acknowledgement of
// the Alert Group and unchanged when the current Owner acknowledges again.
type acknowledge struct {
	user      int64
	bulk      bool
	now       time.Time
	orgID     int64
	first     bool
	unchanged bool
}

func (t *acknowledge) precondition(g *Group) error {
	switch {
	case t.user == 0:
		return refusal(CodeOwnerMustBeUser)
	case g.Status == StatusResolved:
		return refusal(CodeAlreadyResolved)
	case t.bulk && g.Status == StatusAcknowledged && *g.OwnerUserID != t.user:
		return &skippedError{owner: *g.OwnerUserID}
	}
	return nil
}

func (t *acknowledge) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	if g.Status == StatusAcknowledged {
		if *g.OwnerUserID == t.user {
			t.unchanged = true
			return nil
		}
		previous := g.OwnerUserID
		details, err := ownerDetails(ctx, q, t.orgID, *previous)
		if err != nil {
			return err
		}
		g.OwnerUserID, g.AcknowledgedAt = ptr(t.user), ptr(t.now)
		c.action, c.details = ActionTakenOver, details
		c.add(entry{Event: EventTakeover, From: StatusAcknowledged, To: StatusAcknowledged, OwnerUserID: g.OwnerUserID,
			PreviousOwner: previous})
		return nil
	}
	from := g.Status
	clearSnooze(g)
	g.Status, g.OwnerUserID, g.AcknowledgedAt = StatusAcknowledged, ptr(t.user), ptr(t.now)
	if g.FirstAcknowledgedAt == nil {
		g.FirstAcknowledgedAt, t.first = ptr(t.now), true
	}
	c.action = ActionAcknowledged
	c.add(entry{Event: EventAcknowledged, From: from, To: StatusAcknowledged, OwnerUserID: g.OwnerUserID})
	return nil
}

// unacknowledge is Unacknowledge, by any user with the Permission; the entry names the Owner who lost the Alert Group.
type unacknowledge struct{ orgID int64 }

func (unacknowledge) precondition(g *Group) error {
	if g.Status != StatusAcknowledged {
		return refusal(CodeNotAcknowledged)
	}
	return nil
}

func (t unacknowledge) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	previous := g.OwnerUserID
	details, err := ownerDetails(ctx, q, t.orgID, *previous)
	if err != nil {
		return err
	}
	g.Status, g.OwnerUserID, g.AcknowledgedAt = StatusFiring, nil, nil
	c.action, c.details = ActionUnacknowledged, details
	c.add(entry{Event: EventUnacknowledged, Variant: VariantCommand, From: StatusAcknowledged, To: StatusFiring,
		PreviousOwner: previous})
	return nil
}

// resolveCommand is Resolve by a User or a Service account: it ends an acknowledgement or a Snooze and, while Alerts
// still fire in the Alert Group, starts its Grace period (C-09.FR-5); no Reopen window applies.
type resolveCommand struct {
	user, account *int64
	now           time.Time
	grace         time.Duration
	orgID         int64
}

func (resolveCommand) precondition(g *Group) error {
	if g.Status == StatusResolved {
		return refusal(CodeAlreadyResolved)
	}
	return nil
}

func (t resolveCommand) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	from := g.Status
	clearSnooze(g)
	g.Status, g.OwnerUserID, g.AcknowledgedAt = StatusResolved, nil, nil
	g.ResolvedAt, g.ResolvedByKind = ptr(t.now), ptr(ResolvedByUser)
	g.ResolvedByUserID, g.ResolvedByServiceAccount = t.user, t.account
	g.ResolveReason, g.ResolveReasonText, g.ReopenDeadline, g.Prior = nil, nil, nil, nil
	if g.FiringCount > 0 {
		deadline := t.now.Add(t.grace)
		g.GraceDeadline = &deadline
		if err := setTimer(ctx, q, t.orgID, g.ID, TimerGracePeriodEnd, deadline, t.now); err != nil {
			return err
		}
	}
	c.action = ActionResolved
	c.add(entry{Event: EventResolved, From: from, To: StatusResolved})
	return nil
}

// unresolve is Unresolve: refused, in this order, when the Alert Group is not resolved, when the system resolved it,
// when its Route was deleted, when newer is open with the same key, and when none of its Alerts still fires;
// otherwise it is firing again without an Owner, its Alerts still firing active in it, and the Grace period ends.
type unresolve struct {
	newer        *GroupRef
	routeDeleted bool
	orgID        int64
}

func (t unresolve) precondition(g *Group) error {
	switch {
	case g.Status != StatusResolved:
		return refusal(CodeNotResolved)
	case g.ResolvedByKind == nil || *g.ResolvedByKind != ResolvedByUser:
		return refusal(CodeResolvedAutomatically)
	case t.routeDeleted:
		return refusal(CodeRouteDeleted)
	case t.newer != nil:
		return &RefusedError{Code: CodeNewerGroupExists, Related: t.newer,
			Message: fmt.Sprintf("a newer open Alert Group #%d exists", t.newer.Number)}
	case g.FiringCount == 0:
		return refusal(CodeAllAlertsResolved)
	}
	return nil
}

func (t unresolve) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	g.Status = StatusFiring
	g.ResolvedAt, g.ResolvedByKind, g.ResolvedByUserID, g.ResolvedByServiceAccount = nil, nil, nil, nil
	if g.GraceDeadline != nil {
		g.GraceDeadline = nil
		if err := stopTimer(ctx, q, t.orgID, g.ID, TimerGracePeriodEnd); err != nil {
			return err
		}
	}
	c.action = ActionUnresolved
	c.add(entry{Event: EventUnresolved, From: StatusResolved, To: StatusFiring})
	return nil
}

// snooze is Snooze until end by a User or a Service account: an acknowledged Alert Group loses its Owner, whom the
// entry names; on a snoozed one only the end changes. urgent is its urgency now, which a rise to Urgent compares
// against (C-09.FR-9).
type snooze struct {
	end           SnoozeEnd
	user, account *int64
	urgent        bool
}

func (snooze) precondition(g *Group) error {
	if g.Status == StatusResolved {
		return refusal(CodeAlreadyResolved)
	}
	return nil
}

func (t snooze) apply(_ context.Context, _ Queries, g *Group, c *change) error {
	from := g.Status
	var previous *int64
	if from != StatusSnoozed {
		previous = g.OwnerUserID
		g.Status, g.OwnerUserID, g.AcknowledgedAt = StatusSnoozed, nil, nil
		g.SnoozedByUserID, g.SnoozedByServiceAccount, g.SnoozedWhileUrgent = t.user, t.account, t.urgent
	}
	g.SnoozeUntil, g.SnoozeNoEnd = nil, t.end.NoEnd
	details := map[string]any{"no_end": t.end.NoEnd}
	if t.end.Until != nil {
		g.SnoozeUntil = ptr(t.end.Until.UTC().Truncate(time.Microsecond))
		details["until"] = g.SnoozeUntil.Format(time.RFC3339)
	}
	c.action, c.details = ActionSnoozed, details
	c.add(entry{Event: EventSnoozed, From: from, To: StatusSnoozed, PreviousOwner: previous,
		SnoozeUntil: g.SnoozeUntil})
	return nil
}

// unsnooze is Unsnooze: firing without an Owner.
type unsnooze struct{}

func (unsnooze) precondition(g *Group) error {
	if g.Status != StatusSnoozed {
		return refusal(CodeNotSnoozed)
	}
	return nil
}

func (unsnooze) apply(_ context.Context, _ Queries, g *Group, c *change) error {
	clearSnooze(g)
	g.Status = StatusFiring
	c.action = ActionUnsnoozed
	c.add(entry{Event: EventUnsnoozed, From: StatusSnoozed, To: StatusFiring})
	return nil
}

// ownerDetails name the Owner who lost an Alert Group in the details of its Audit log entry.
func ownerDetails(ctx context.Context, q Queries, orgID, owner int64) (map[string]any, error) {
	rows, err := q.ListUserRefs(ctx, dbgen.ListUserRefsParams{OrgID: orgID, Ids: []int64{owner}})
	if err != nil {
		return nil, fmt.Errorf("read the owner: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return map[string]any{"previous_owner": rows[0].PublicID}, nil
}

// sentence is a refusal message as the detail of a problem: a capital first letter and a full stop.
func sentence(message string) string {
	if message == "" {
		return ""
	}
	return strings.ToUpper(message[:1]) + message[1:] + "."
}

// Detail is the refusal as the detail of a problem.
func (e *RefusedError) Detail() string { return sentence(e.Message) }
