// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/publicid"
)

// The queue of calls through Notification gateways (C-30.FR-4, ADR-0005): a call is a row of gateway_calls per person
// and gateway, with the Alert Groups it is about in gateway_call_items, sent at least once by the gateway sender of any
// replica (sender.go) under the Idempotency-Key calls/<public_id>, never reconciled: a call has no Root message and no
// Desired state. The first call to a person through a gateway goes at once. A Page through the same gateway to the
// same person that comes due while a call to them waits, is being retried, or within gateway.combine_window after the
// previous call ended — accepted or Not delivered — joins the one call that waits, which goes once that window after
// the previous call has passed; until the previous call ends, the waiting call has no not_before and is never claimed.
// From its first attempt a call keeps its Alert Groups, its contact and its text.
//
// Lock order. A transaction that locks rows of more than one of these locks them in this order: the Alert Groups by id
// (the dispatcher's LockGroups, with whatever its re-render step locks after them), then the lock of the calls of one
// person through one gateway (LockCallPerson, db.GatewayCallLockClass), then that person's gateway_calls rows by id —
// the call that ends before the call that waits behind it, which a later Page created — then gateway_call_items, then
// the limiter buckets of the gateway's Connection. Page runs in its caller's transaction, which may hold Alert Groups
// already, and takes the person's lock and the waiting call after them; so a caller that Pages takes every lock of
// Alert Groups it needs before its first Page, never one after. The sender's claim takes gateway_calls rows alone,
// SKIP LOCKED. Its preparation takes the person's lock, then the call, its items and the buckets, never an Alert Group:
// it reads their status unlocked, and an Alert Group acknowledged a moment later is answered by the gateway's
// Acknowledge (S-173). The end of a call Not delivered records page_not_delivered on its Alert Groups through the
// dispatcher first, which locks them, and only then takes the person's lock, the call and the call waiting behind
// it, in the same transaction, so that page_not_delivered is recorded once with the end it belongs to; every other
// end takes no Alert Group. Page and the end of a call rely on READ COMMITTED: each statement after the person's lock
// sees what the transaction that held it before committed, so Page's PreviousCall and LockWaitingCall, read after the
// lock, see the end of the previous call, and the end's ReleaseWaitingCall sees the waiting call a Page created.
// Explicit locks of gateway_calls are FOR NO KEY UPDATE, never FOR UPDATE, as
// gateway_call_items references it and the foreign-key check of a Page's new item takes FOR KEY SHARE on its call.
// TestNotDeliveredAgainstPageLockOrder (calls_integration_test.go) shows the deadlock the other order would make.

// CombineWindow is gateway.combine_window (P-87, provisional): the first call to a person through a gateway goes at
// once; the Pages that come due after it go together in the next call, no sooner than this after the previous call
// ended, accepted or Not delivered.
const CombineWindow = 60 * time.Second

// CallsChannel is the LISTEN/NOTIFY channel that wakes the gateway senders of every replica when a Page is queued.
const CallsChannel = "muster_gateway_calls"

// The states of a call.
const (
	CallWaiting      = "waiting"
	CallSending      = "sending"
	CallAccepted     = "accepted"
	CallNotDelivered = "not_delivered"
	CallDropped      = "dropped"
)

// StopCheck says when the chain or the rule that Paged has stopped on an Alert Group (C-30.FR-4): the sender drops
// such an Alert Group from a call before its first attempt, and ends the retries of a call once every one of its Alert
// Groups stopped. It is a name rather than a function, as any replica sends the call; until S-157 the only one is
// StopNotFiring, which S-157 extends with the stops of chains and rules.
type StopCheck string

// StopNotFiring is the stop of this story: the Alert Group is no longer firing — acknowledged, snoozed or resolved.
const StopNotFiring StopCheck = "not_firing"

// stopped reports whether an Alert Group in status stopped for c.
func (c StopCheck) stopped(status string) bool {
	return status != "firing"
}

// ErrUnknownStop is a Page with a StopCheck that this version does not know.
var ErrUnknownStop = errors.New("unknown stop check")

// ErrNoPage is a Page that names no User, no Notification gateway or no Alert Group of the Organization, or a deleted
// one.
var ErrNoPage = errors.New("no such user, notification gateway or alert group")

// PageCall is a Page through a Notification gateway: the User called, the gateway's Connection and the Alert Group
// it is about, by internal id, and the StopCheck of the chain or the rule that Paged.
type PageCall struct {
	UserID       int64
	GatewayID    int64
	AlertGroupID int64
	Stop         StopCheck
}

// CallQueries are the queries of the calls; *dbgen.Queries implements them.
type CallQueries interface {
	GetPage(ctx context.Context, arg dbgen.GetPageParams) (dbgen.GetPageRow, error)
	GetPageGroupID(ctx context.Context, arg dbgen.GetPageGroupIDParams) (int64, error)
	GetGateway(ctx context.Context, arg dbgen.GetGatewayParams) (dbgen.GetGatewayRow, error)
	GetUserByPublicID(ctx context.Context, arg dbgen.GetUserByPublicIDParams) (dbgen.GetUserByPublicIDRow, error)
	LockCallPerson(ctx context.Context, arg dbgen.LockCallPersonParams) error
	PreviousCall(ctx context.Context, arg dbgen.PreviousCallParams) (dbgen.PreviousCallRow, error)
	LockWaitingCall(ctx context.Context, arg dbgen.LockWaitingCallParams) (dbgen.LockWaitingCallRow, error)
	InsertWaitingCall(ctx context.Context, arg dbgen.InsertWaitingCallParams) (int64, error)
	MarkCallCritical(ctx context.Context, arg dbgen.MarkCallCriticalParams) error
	AddCallItem(ctx context.Context, arg dbgen.AddCallItemParams) error
	NotifyCalls(ctx context.Context, channel string) error
	ClaimDueCalls(ctx context.Context, arg dbgen.ClaimDueCallsParams) ([]dbgen.ClaimDueCallsRow, error)
	GetLeasedCall(ctx context.Context, arg dbgen.GetLeasedCallParams) (dbgen.GetLeasedCallRow, error)
	ListCallGroups(ctx context.Context, arg dbgen.ListCallGroupsParams) ([]dbgen.ListCallGroupsRow, error)
	DropCallItems(ctx context.Context, arg dbgen.DropCallItemsParams) error
	CallContact(ctx context.Context, arg dbgen.CallContactParams) (dbgen.CallContactRow, error)
	StartCall(ctx context.Context, arg dbgen.StartCallParams) (int64, error)
	RenewCallLease(ctx context.Context, arg dbgen.RenewCallLeaseParams) (int64, error)
	RescheduleCall(ctx context.Context, arg dbgen.RescheduleCallParams) (int64, error)
	RecordCallRetry(ctx context.Context, arg dbgen.RecordCallRetryParams) (int64, error)
	EndCall(ctx context.Context, arg dbgen.EndCallParams) (bool, error)
	ReleaseWaitingCall(ctx context.Context, arg dbgen.ReleaseWaitingCallParams) error
	NextCallWork(ctx context.Context, arg dbgen.NextCallWorkParams) (dbgen.NextCallWorkRow, error)
}

// CallStore runs the queries of the calls over the main pool, alone or in short transactions that also hand out the
// transaction itself, which the dispatcher and the limiter write through.
type CallStore interface {
	CallQueries
	InCallTx(ctx context.Context, f func(tx DBTX, q CallQueries) error) error
}

// NewCallStore is the CallStore over the main pool.
func NewCallStore(p Pool) CallStore {
	return pgCallStore{Queries: dbgen.New(p), pool: p}
}

type pgCallStore struct {
	*dbgen.Queries
	pool Pool
}

func (s pgCallStore) InCallTx(ctx context.Context, f func(tx DBTX, q CallQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(tx, dbgen.New(tx)) })
}

// Calls queues the calls of an Organization: Page, in the transaction of its caller.
type Calls struct {
	orgID    int64
	business clock.Clock
	log      *logging.Logger
	// queries are the queries of the calls over a transaction; tests replace them.
	queries func(DBTX) CallQueries
}

// NewCalls is the queue of calls of the Organization orgID, dated on the business clock.
func NewCalls(orgID int64, business clock.Clock, log *logging.Logger) *Calls {
	return &Calls{orgID: orgID, business: business, log: log,
		queries: func(d DBTX) CallQueries { return dbgen.New(d) }}
}

// Page queues the Page p in the transaction tx of its caller (C-30.FR-4): it joins the call to the person through the
// gateway that waits for its first attempt, or creates one, due at once when the person had no call through the
// gateway or the last one ended CombineWindow ago or longer, due CombineWindow after the last one ended otherwise, and
// not due while the last one is still being sent, until it ends; the call becomes critical once a critical Alert
// Group joins it, and stays critical even when that Alert Group is dropped from it before its first attempt: it is
// claimed early, and its preparation then finds what is left. It takes the person's lock first (calls.go, Lock order) and wakes the senders once tx commits, and
// returns what to log once tx committed.
func (c *Calls) Page(ctx context.Context, tx DBTX, p PageCall) (func(context.Context), error) {
	if p.Stop != StopNotFiring {
		return nil, fmt.Errorf("%w %q", ErrUnknownStop, p.Stop)
	}
	q := c.queries(tx)
	now := c.business.Now().UTC()
	g, err := q.GetPage(ctx, dbgen.GetPageParams{OrgID: c.orgID, AlertGroupID: p.AlertGroupID,
		ConnectionID: p.GatewayID, UserID: p.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("a page of the alert group %d to the user %d through the gateway %d: %w",
			p.AlertGroupID, p.UserID, p.GatewayID, ErrNoPage)
	}
	if err != nil {
		return nil, fmt.Errorf("read the page: %w", err)
	}
	if err := q.LockCallPerson(ctx, dbgen.LockCallPersonParams{LockClass: db.GatewayCallLockClass, OrgID: c.orgID,
		ConnectionID: p.GatewayID, UserID: p.UserID}); err != nil {
		return nil, fmt.Errorf("lock the calls of the user: %w", err)
	}
	w, err := q.LockWaitingCall(ctx, dbgen.LockWaitingCallParams{OrgID: c.orgID, ConnectionID: p.GatewayID,
		UserID: p.UserID})
	joined := err == nil
	var notBefore pgtype.Timestamptz
	switch {
	case joined:
		if g.Critical {
			if err := q.MarkCallCritical(ctx, dbgen.MarkCallCriticalParams{OrgID: c.orgID, ID: w.ID}); err != nil {
				return nil, fmt.Errorf("make the call %s critical: %w", w.PublicID, err)
			}
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("read the waiting call of the user: %w", err)
	default:
		if notBefore, err = c.notBefore(ctx, q, p, now); err != nil {
			return nil, err
		}
		w.PublicID = publicid.New(publicid.GatewayCall)
		if w.ID, err = q.InsertWaitingCall(ctx, dbgen.InsertWaitingCallParams{OrgID: c.orgID, PublicID: w.PublicID,
			ConnectionID: p.GatewayID, UserID: p.UserID, Critical: g.Critical, NotBefore: notBefore,
			Now: now}); err != nil {
			return nil, fmt.Errorf("queue the call: %w", err)
		}
	}
	if err := q.AddCallItem(ctx, dbgen.AddCallItemParams{OrgID: c.orgID, CallID: w.ID, AlertGroupID: p.AlertGroupID,
		Now: now}); err != nil {
		return nil, fmt.Errorf("add the alert group to the call %s: %w", w.PublicID, err)
	}
	if err := q.NotifyCalls(ctx, CallsChannel); err != nil {
		return nil, fmt.Errorf("wake the gateway senders: %w", err)
	}
	return func(ctx context.Context) {
		at := ""
		if notBefore.Valid {
			at = notBefore.Time.UTC().Format(time.RFC3339)
		}
		c.log.Log(ctx, logging.GatewayCallQueued, logging.F("call", w.PublicID),
			logging.F("connection", g.ConnectionPublicID), logging.F("user", g.UserPublicID),
			logging.F("group", "#"+strconv.FormatInt(g.Number, 10)), logging.F("joined", joined),
			logging.F("not_before", at))
	}, nil
}

// notBefore is when a new waiting call of p may go: now when the person had no call through the gateway that went,
// or it ended CombineWindow ago or longer; the end of that call plus CombineWindow while that is later; and none while
// that call is still being sent, which sets it when it ends (ReleaseWaitingCall).
func (c *Calls) notBefore(ctx context.Context, q CallQueries, p PageCall, now time.Time) (pgtype.Timestamptz,
	error) {
	prev, err := q.PreviousCall(ctx, dbgen.PreviousCallParams{OrgID: c.orgID, ConnectionID: p.GatewayID,
		UserID: p.UserID})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return pgtype.Timestamptz{Time: now, Valid: true}, nil
	case err != nil:
		return pgtype.Timestamptz{}, fmt.Errorf("read the previous call of the user: %w", err)
	case !prev.EndedAt.Valid:
		return pgtype.Timestamptz{}, nil
	}
	return pgtype.Timestamptz{Time: later(now, prev.EndedAt.Time.UTC().Add(CombineWindow)), Valid: true}, nil
}

// later is the later of a and b.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// DevPage queues, under muster dev, a call of the Notification gateway gatewayPublicID to the User userPublicID about
// each of the Alert Groups groupPublicIDs, as a Notification rule's call would, with StopNotFiring, in one transaction
// of store (POST /_dev/gateway-calls). An id that names nothing is an error that wraps ErrNoPage, and nothing is
// queued.
func (c *Calls) DevPage(ctx context.Context, store CallStore, userPublicID, gatewayPublicID string,
	groupPublicIDs []string) error {
	var logs []func(context.Context)
	err := store.InCallTx(ctx, func(tx DBTX, q CallQueries) error {
		logs = nil
		u, err := q.GetUserByPublicID(ctx, dbgen.GetUserByPublicIDParams{OrgID: c.orgID, PublicID: userPublicID})
		if err == nil && u.Status == statusDeleted {
			err = pgx.ErrNoRows
		}
		if err != nil {
			return fmt.Errorf("read the user %q: %w", userPublicID, noPage(err))
		}
		gw, err := q.GetGateway(ctx, dbgen.GetGatewayParams{OrgID: c.orgID, PublicID: gatewayPublicID})
		if err != nil {
			return fmt.Errorf("read the notification gateway %q: %w", gatewayPublicID, noPage(err))
		}
		if len(groupPublicIDs) == 0 {
			return fmt.Errorf("no alert group: %w", ErrNoPage)
		}
		for _, id := range slices.Compact(slices.Sorted(slices.Values(groupPublicIDs))) {
			gid, err := q.GetPageGroupID(ctx, dbgen.GetPageGroupIDParams{OrgID: c.orgID, PublicID: id})
			if err != nil {
				return fmt.Errorf("read the alert group %q: %w", id, noPage(err))
			}
			log, err := c.Page(ctx, tx, PageCall{UserID: u.ID, GatewayID: gw.ID, AlertGroupID: gid,
				Stop: StopNotFiring})
			if err != nil {
				return err
			}
			logs = append(logs, log)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, log := range logs {
		log(ctx)
	}
	return nil
}

// noPage is err, ErrNoPage when it says that nothing was found.
func noPage(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoPage
	}
	return err
}
