// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
)

// The reasons of gateway_acknowledge_refused: action data that is not Muster's signature of a call, data signed with a
// key that left the Keyring, a call that is not one of the Connection that reported it, and a person the call was for
// who is disabled or deleted.
const (
	AckNotVerified = "not_verified"
	AckExpired     = "expired"
	AckUnknownCall = "unknown_call"
	AckDisabled    = "disabled"
	AckDeleted     = "deleted"
)

// statusDisabled is the status of a disabled User.
const statusDisabled = "disabled"

// AckQueries are the reads of the Acknowledge through a Notification gateway; *dbgen.Queries implements them.
type AckQueries interface {
	GetAcknowledgedCall(ctx context.Context, arg dbgen.GetAcknowledgedCallParams) (dbgen.GetAcknowledgedCallRow, error)
	ListCallGroups(ctx context.Context, arg dbgen.ListCallGroupsParams) ([]dbgen.ListCallGroupsRow, error)
}

// Acknowledger is the Command a call acknowledges with; *groups.Service implements it.
type Acknowledger interface {
	Acknowledge(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
}

// Acknowledgements handle the notification_acknowledged events of the Notification gateways of an Organization
// (C-30.FR-6, ADR-0013): they implement bridge.Acknowledgements, which internal/runtime wires into bridge.Events, and
// run inside the event's claim, so that an event is handled once per id (C-22.FR-11).
type Acknowledgements struct {
	OrgID   int64
	Queries AckQueries
	// Commands run Acknowledge through the dispatcher, one Alert Group at a time, each in its own transaction.
	Commands Acknowledger
	// Roles are the Permissions of each Role, which the person the call was for acts with.
	Roles auth.Roles
	// Keys verify the action data: the Keyring.
	Keys buttons.Keys
	Log  *logging.Logger
}

var _ bridge.Acknowledgements = (*Acknowledgements)(nil)

// NewAckQueries are the AckQueries over the main pool.
func NewAckQueries(d DBTX) AckQueries { return dbgen.New(d) }

// Acknowledged runs the Acknowledge of the event ev of the Connection c and returns its answer, in the profile language
// of the person the call was for (C-12.FR-3), which bridge.Events makes with answerEvent on the interactive path. The
// action data is verified with the key it names and must name a call of this Connection and this Organization
// (buttons.SubjectCall); the actor is the User the call was for, never the contact (ADR-0013). For each Alert Group
// of the call — those it carried from its first attempt, also one whose chain stopped meanwhile — the dispatcher runs
// Acknowledge with the Transport gateway, whose precondition refuses an Alert Group that someone owns or that is
// resolved (groups.acknowledge); a person without the Permission is refused by the dispatcher, as for a press
// (C-18.FR-8). Data that does not verify, a call of another Connection, and a disabled or deleted person run nothing,
// are logged as gateway_acknowledge_refused and answered so. An error — a read that failed, or a Command that failed
// otherwise than by its refusal — fails the event, so that it comes again.
func (a *Acknowledgements) Acknowledged(ctx context.Context, c bridge.Conn, ev bridge.Event) (string, error) {
	act, reason := a.verify(ev.Data)
	if reason == AckExpired {
		return a.refuse(ctx, c, act.PublicID, reason, messages.LanguageEnglish, "gatewayAck.expired"), nil
	}
	if reason != "" {
		return a.refuse(ctx, c, act.PublicID, reason, messages.LanguageEnglish, "gatewayAck.notVerified"), nil
	}
	call, err := a.Queries.GetAcknowledgedCall(ctx, dbgen.GetAcknowledgedCallParams{OrgID: a.OrgID,
		ConnectionID: c.ID, PublicID: act.PublicID})
	if errors.Is(err, pgx.ErrNoRows) {
		return a.refuse(ctx, c, act.PublicID, AckUnknownCall, messages.LanguageEnglish, "gatewayAck.notVerified"), nil
	}
	if err != nil {
		return "", fmt.Errorf("read the call %s: %w", act.PublicID, err)
	}
	lang := language(call.UserLanguage)
	switch call.UserStatus {
	case statusDisabled:
		return a.refuse(ctx, c, call.PublicID, AckDisabled, lang, "gatewayAck.disabled"), nil
	case statusDeleted:
		return a.refuse(ctx, c, call.PublicID, AckDeleted, lang, "gatewayAck.disabled"), nil
	}
	gs, err := a.Queries.ListCallGroups(ctx, dbgen.ListCallGroupsParams{OrgID: a.OrgID, CallID: call.ID})
	if err != nil {
		return "", fmt.Errorf("read the alert groups of the call %s: %w", call.PublicID, err)
	}
	caller := groups.Caller{Actor: audit.User(call.UserID, call.UserPublicID), Transport: audit.TransportGateway,
		Permissions: a.Roles.Permissions(call.UserRole), Gateway: c.ID}
	var out answer
	// Each Alert Group is acknowledged in a transaction of its own. When one fails and the event comes again, the
	// Alert Groups already acknowledged are refused as owned — by the same person — and answered "Already taken by"
	// them, which changes nothing and is what C-30.FR-6 says of an Alert Group the person owns.
	for _, g := range gs {
		_, err := a.Commands.Acknowledge(ctx, caller, g.PublicID)
		if _, ok := errors.AsType[*groups.ForbiddenError](err); ok {
			// Without the Permission before anything ran, nothing changed; a person disabled once some Alert Groups
			// were acknowledged is told which, and that the others were not.
			if out.empty() {
				return messages.T(lang, "gatewayAck.forbidden", nil), nil
			}
			out.other = append(out.other, g.Number)
			continue
		}
		if err := out.add(g.Number, err); err != nil {
			return "", fmt.Errorf("acknowledge #%d of the call %s: %w", g.Number, call.PublicID, err)
		}
	}
	return out.text(lang, len(gs)), nil
}

// verify reads and verifies the action data of a call: the Action it names, or the reason it is refused with.
func (a *Acknowledgements) verify(data string) (buttons.Action, string) {
	if len(data) > buttons.MaxLen {
		return buttons.Action{}, AckNotVerified
	}
	act, err := buttons.Verify(a.Keys, data, "")
	switch {
	case errors.Is(err, buttons.ErrExpired) && act.Subject == buttons.SubjectCall:
		return act, AckExpired
	case err != nil || act.Subject != buttons.SubjectCall || act.Command != buttons.CommandAcknowledge:
		return buttons.Action{}, AckNotVerified
	}
	return act, ""
}

// refuse logs gateway_acknowledge_refused of the call publicID with reason and returns the answer key in lang.
func (a *Acknowledgements) refuse(ctx context.Context, c bridge.Conn, publicID, reason, lang, key string) string {
	a.Log.Log(ctx, logging.GatewayAcknowledgeRefused, logging.F("call", publicID),
		logging.F("connection", c.PublicID), logging.F("reason", reason))
	return messages.T(lang, key, nil)
}

// answer collects what the Acknowledge of each Alert Group of a call did, by #N.
type answer struct {
	acknowledged []int64
	resolved     []int64
	other        []int64
	// owners are the Owners of the Alert Groups already taken, in the order met, with their #N.
	owners []string
	taken  map[string][]int64
}

// add records the outcome err of the Acknowledge of #number; an error that is neither a refusal nor an Alert Group
// gone meanwhile is returned.
func (o *answer) add(number int64, err error) error {
	r, refused := errors.AsType[*groups.RefusedError](err)
	switch {
	case err == nil:
		o.acknowledged = append(o.acknowledged, number)
	case errors.Is(err, groups.ErrNotFound):
	case !refused:
		return err
	case r.Code == groups.CodeOwnedByOther:
		if o.taken == nil {
			o.taken = map[string][]int64{}
		}
		if _, ok := o.taken[r.OwnerName]; !ok {
			o.owners = append(o.owners, r.OwnerName)
		}
		o.taken[r.OwnerName] = append(o.taken[r.OwnerName], number)
	case r.Code == groups.CodeAlreadyResolved:
		o.resolved = append(o.resolved, number)
	default:
		o.other = append(o.other, number)
	}
	return nil
}

// empty reports whether nothing was recorded yet.
func (o *answer) empty() bool {
	return len(o.acknowledged)+len(o.resolved)+len(o.other)+len(o.owners) == 0
}

// text is the answer in lang to a call about n Alert Groups: what was acknowledged, by #N, then who has the Alert
// Groups already taken, what was resolved and what else was refused, each with its #N when the call is about more than
// one, "Already taken by Ivan" alone when it is about one; "Nothing to acknowledge" when none of them is left.
func (o *answer) text(lang string, n int) string {
	if o.empty() {
		return messages.T(lang, "gatewayAck.nothing", nil)
	}
	var parts []string
	if len(o.acknowledged) > 0 {
		parts = append(parts, messages.N(lang, "gatewayAck.acknowledged", int64(len(o.acknowledged)),
			messages.Args{"numbers": numbers(o.acknowledged)}))
	}
	for _, name := range o.owners {
		// An Owner without a row has no name: "another user", in lang.
		key := "gatewayAck.takenBy"
		if name == "" {
			key = "gatewayAck.takenByOther"
		}
		if n == 1 {
			parts = append(parts, messages.T(lang, key, messages.Args{"name": name}))
			continue
		}
		ns := o.taken[name]
		parts = append(parts, messages.N(lang, key+"Groups", int64(len(ns)),
			messages.Args{"name": name, "numbers": numbers(ns)}))
	}
	if len(o.resolved) > 0 {
		if n == 1 {
			parts = append(parts, messages.T(lang, "gatewayAck.resolved", nil))
		} else {
			parts = append(parts, messages.N(lang, "gatewayAck.resolvedGroups", int64(len(o.resolved)),
				messages.Args{"numbers": numbers(o.resolved)}))
		}
	}
	if len(o.other) > 0 {
		parts = append(parts, messages.N(lang, "gatewayAck.notAcknowledged", int64(len(o.other)),
			messages.Args{"numbers": numbers(o.other)}))
	}
	return strings.Join(parts, ". ")
}

// numbers are the #N of Alert Groups, in order: "#12, #13".
func numbers(ns []int64) string {
	out := make([]string, len(ns))
	for i, n := range slices.Sorted(slices.Values(ns)) {
		out[i] = "#" + strconv.FormatInt(n, 10)
	}
	return strings.Join(out, ", ")
}
