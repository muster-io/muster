// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// The gateway sender (C-30.FR-4, FR-5, NFR-4): a worker on every replica, not a Leader task, that claims the due
// calls with FOR NO KEY UPDATE SKIP LOCKED and a lease (real clock), critical first and then by when they are due
// (business clock), prepares each in a short transaction, calls delivery.Gateway.Notify outside any transaction in
// the delivery client class through the limiter of the gateway's Connection, and records the outcome in another short
// transaction, guarded by its lease. Lint 3 allows Notify here and in delivery's interactive path only.
//
// A call is sent at least once: its Idempotency-Key calls/<public_id>, its contact, its text and its action data,
// signed with the active button-signature key whose id the call keeps (button_key_id), so that the Keyring keeps that
// key while the gateway may still report the data, are fixed at its first attempt and identical on every retry, on any
// replica and after a restart. When a replica dies after the gateway accepted the call but before the outcome
// committed, or its lease ran out during a slow call, the replica that claims the call again resends it with the same
// key; a gateway that keeps idempotency keys answers replayed, which counts as accepted, and makes no second call. Two
// replicas never hold the lease of one call at once, and the outcome of a replica whose lease went to another changes
// nothing. The lock order is in calls.go.

// The defaults of the sender, as the delivery worker's (internal/delivery): the lease of a claimed call, renewed
// before its attempt; the calls one claim takes; the margin after the time a limiter token is due; the longest and
// the shortest wait between rounds; and the first wait after a failed round.
const (
	SenderLease          = delivery.Lease
	SenderBatch          = delivery.Batch
	SenderTokenMargin    = delivery.TokenMargin
	SenderMaxWait        = delivery.MaxWait
	SenderMinWait        = delivery.MinWait
	SenderFailureBackoff = delivery.FailureBackoff
)

// Limiter takes the tokens of the limiter of a gateway's Connection, through the transaction db of the call's
// preparation, and holds it after a RetryAfter (C-11.FR-3, connection.bridge.limiter); delivery's functions implement
// it.
type Limiter interface {
	ConnectionToken(ctx context.Context, db DBTX, orgID, connection int64, now time.Time) (bool, time.Time, error)
	HoldConnection(ctx context.Context, db DBTX, orgID, connection int64, until time.Time) error
}

// ConnectionLimiter is the limiter of delivery: the token buckets of rate_limit_buckets, shared by every replica.
type ConnectionLimiter struct{}

// ConnectionToken takes a token of the Connection connection (delivery.ConnectionToken).
func (ConnectionLimiter) ConnectionToken(ctx context.Context, d DBTX, orgID, connection int64, now time.Time) (bool,
	time.Time, error) {
	return delivery.ConnectionToken(ctx, d, orgID, connection, now)
}

// HoldConnection holds the bucket of the Connection connection until until (delivery.HoldConnection).
func (ConnectionLimiter) HoldConnection(ctx context.Context, d DBTX, orgID, connection int64, until time.Time) error {
	return delivery.HoldConnection(ctx, d, orgID, connection, until)
}

// PageRecorder records page_not_delivered on Alert Groups through the dispatcher, in the transaction tx of the end of
// a call; *groups.Service implements it.
type PageRecorder interface {
	PageNotDelivered(ctx context.Context, tx groups.DBTX, ids []int64, p groups.Page) (func(context.Context), error)
}

// Sender is the gateway sender of one replica for the Organization OrgID.
type Sender struct {
	OrgID int64
	Store CallStore
	// Lease names this replica in lease_owner and carries the clocks: due times, the combine window and the limiter
	// on the business clock, leases on the real clock.
	Lease db.Lease
	// Gateway sends the notifications: *bridge.Adapter.
	Gateway delivery.Gateway
	// Limiter is the limiter of the gateways' Connections; nil is ConnectionLimiter.
	Limiter Limiter
	// Keys sign the action data of calls with the button-signature key: the Keyring.
	Keys  buttons.Keys
	Pages PageRecorder
	Log   *logging.Logger
	// Random is uniform in [0, 1) and jitters delivery.transient_backoff; nil is math/rand/v2.
	Random func() float64
	// MaxWait bounds the wait between rounds, and Batch the calls one claim takes; zero is the default.
	MaxWait time.Duration
	Batch   int32
	// Wait waits for d or until a wake arrives; nil waits on the real clock.
	Wait func(ctx context.Context, d time.Duration, wake <-chan struct{})

	// lockCallFirst reverses the order of the end of a call Not delivered — the person's lock and the call before the
	// Alert Groups — which only the test that shows the deadlock of the reversed order sets.
	lockCallFirst bool

	once sync.Once
	wake chan struct{}
}

func (s *Sender) init() {
	s.once.Do(func() { s.wake = make(chan struct{}, 1) })
}

// Wake makes the sender run a round at once: a Page was queued, the development clock moved, or the LISTEN is back.
func (s *Sender) Wake() {
	s.init()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run sends calls until ctx ends: a round, then a wait until the next call can be claimed, between SenderMinWait and
// MaxWait, or until woken; after a failed round, such as one without the database, a backoff, as the delivery worker
// does.
func (s *Sender) Run(ctx context.Context) {
	s.init()
	wait := s.Wait
	if wait == nil {
		wait = waitReal
	}
	failures := 0
	for ctx.Err() == nil {
		next, err := s.Round(ctx)
		d := max(next, SenderMinWait)
		if err != nil {
			failures++
			d = db.Backoff(failures, SenderFailureBackoff, s.maxWait(), rand.Float64)
		} else {
			failures = 0
		}
		wait(ctx, d, s.wake)
	}
}

func waitReal(ctx context.Context, d time.Duration, wake <-chan struct{}) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-t.C:
	}
}

func (s *Sender) maxWait() time.Duration {
	if s.MaxWait <= 0 {
		return SenderMaxWait
	}
	return s.MaxWait
}

func (s *Sender) batch() int32 {
	if s.Batch <= 0 {
		return SenderBatch
	}
	return s.Batch
}

func (s *Sender) limiter() Limiter {
	if s.Limiter == nil {
		return ConnectionLimiter{}
	}
	return s.Limiter
}

// Round attempts every due call, Batch at a time, and returns how long until the next one can be claimed, at most
// MaxWait. A call that fails is logged and attempted again once its lease runs out; the round goes on.
func (s *Sender) Round(ctx context.Context) (time.Duration, error) {
	for {
		// One statement, which commits alone: it selects the due calls SKIP LOCKED and leases them.
		p := s.Lease.Params(s.batch())
		rows, err := s.Store.ClaimDueCalls(ctx, dbgen.ClaimDueCallsParams{OrgID: s.OrgID, Owner: p.Owner,
			LeaseUntil: p.LeaseUntil, Due: p.Due, Now: p.Now, Lim: p.Limit})
		if err != nil {
			return 0, fmt.Errorf("claim the due calls: %w", err)
		}
		slices.SortFunc(rows, func(a, b dbgen.ClaimDueCallsRow) int {
			switch {
			case a.Critical != b.Critical && a.Critical:
				return -1
			case a.Critical != b.Critical:
				return 1
			}
			if c := a.NextAttemptAt.Compare(b.NextAttemptAt); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})
		for _, r := range rows {
			s.attempt(ctx, r)
		}
		if len(rows) < int(s.batch()) {
			break
		}
	}
	realNow := s.Lease.Clocks.Real.Now().UTC()
	r, err := s.Store.NextCallWork(ctx, dbgen.NextCallWorkParams{OrgID: s.OrgID, Now: realNow})
	if err != nil {
		return 0, fmt.Errorf("read when the next call is due: %w", err)
	}
	d := s.maxWait()
	if r.FreeAt.Year() > 1 {
		d = min(d, r.FreeAt.Sub(s.Lease.Clocks.Business.Now()))
	}
	if r.LeaseEnd.Year() > 1 {
		d = min(d, r.LeaseEnd.Sub(realNow))
	}
	return d, nil
}

// call is a claimed call that its preparation found due: the row as it was read under its lease, the Alert Groups it
// is about, and what its attempt sends — its contact, and its text and language — or, when it cannot be sent, the
// reason it ends Not delivered with: the gateway was deleted or is no Notification gateway any more, or the person
// has no confirmed contact at it.
type call struct {
	row      dbgen.GetLeasedCallRow
	groups   []dbgen.ListCallGroupsRow
	contact  string
	text     string
	language string
	// data is the action data signed at the first attempt, empty for a call that a replica of an earlier version
	// started, whose attempts are signed with the active key each.
	data   string
	unsent string
}

// key is the Idempotency-Key of the call, the same for each of its attempts.
func (c call) key() string { return "calls/" + c.row.PublicID }

// attempt prepares the claimed call r and, when it is due, sends it and records the outcome; a failure is logged and
// the call is attempted again once its lease runs out.
func (s *Sender) attempt(ctx context.Context, r dbgen.ClaimDueCallsRow) {
	c, logs, err := s.prepare(ctx, r)
	switch {
	case err != nil:
	case c == nil:
		logs.run(ctx)
	case c.unsent != "":
		logs.run(ctx)
		err = s.notDelivered(ctx, c, c.unsent, 0, delivery.Outcome{})
	default:
		logs.run(ctx)
		err = s.send(ctx, c)
	}
	if err != nil && ctx.Err() == nil {
		s.Log.Log(ctx, logging.GatewayCallFailed, logging.F("call", r.PublicID), logging.F("error", err.Error()))
	}
}

// logs are the lines to log once the transaction that recorded them committed.
type logs []func(ctx context.Context)

func (l logs) run(ctx context.Context) {
	for _, f := range l {
		f(ctx)
	}
}

// prepare re-reads the claimed call r in a short transaction, under the person's lock and while its lease is still
// held, and returns it when it is due to be sent now. Before the first attempt it drops the Alert Groups that stopped
// and ends a call left without any dropped, unsent; at the first attempt it keeps the person's confirmed contact, the
// text in the language of their profile and the Alert Groups, and takes no more. A call being retried whose Alert
// Groups all stopped ends dropped. Without a limiter token of the gateway's Connection the call waits until one is due
// plus SenderTokenMargin, which is no attempt. A call through a gateway deleted meanwhile, or that is no Notification
// gateway any more, is returned to end Not delivered with gateway_deleted (C-30.FR-9), and one to a person without a
// confirmed contact with no_confirmed_contact. Nothing is returned when the lease ran out or the call ended.
func (s *Sender) prepare(ctx context.Context, r dbgen.ClaimDueCallsRow) (*call, logs, error) {
	var c *call
	var after logs
	err := s.Store.InCallTx(ctx, func(tx DBTX, q CallQueries) error {
		c, after = nil, nil
		if err := s.lockPerson(ctx, q, r.ConnectionID, r.UserID); err != nil {
			return err
		}
		realNow := s.Lease.Clocks.Real.Now().UTC()
		row, err := q.GetLeasedCall(ctx, dbgen.GetLeasedCallParams{OrgID: s.OrgID, ID: r.ID, Owner: s.Lease.Owner,
			Now: realNow})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the call: %w", err)
		}
		gs, err := q.ListCallGroups(ctx, dbgen.ListCallGroupsParams{OrgID: s.OrgID, CallID: row.ID})
		if err != nil {
			return fmt.Errorf("read the alert groups of the call %s: %w", row.PublicID, err)
		}
		now := s.Lease.Clocks.Business.Now().UTC()
		live := slices.DeleteFunc(slices.Clone(gs), func(g dbgen.ListCallGroupsRow) bool {
			return StopNotFiring.stopped(g.Status)
		})
		if row.State == CallWaiting && len(live) < len(gs) {
			var ids []int64
			for _, g := range gs {
				if !slices.ContainsFunc(live, func(l dbgen.ListCallGroupsRow) bool { return l.ID == g.ID }) {
					ids = append(ids, g.ID)
				}
			}
			if err := q.DropCallItems(ctx, dbgen.DropCallItemsParams{OrgID: s.OrgID, CallID: row.ID,
				AlertGroupIds: ids, Now: now}); err != nil {
				return fmt.Errorf("drop the stopped alert groups of the call %s: %w", row.PublicID, err)
			}
		}
		if len(live) == 0 {
			// Every Alert Group stopped: before the first attempt the call is never sent; a call being retried
			// ends its retries.
			ok, err := s.end(ctx, q, row, dbgen.EndCallParams{State: CallDropped}, now)
			if err != nil || !ok {
				return err
			}
			after = append(after, func(ctx context.Context) {
				s.Log.Log(ctx, logging.GatewayCallDropped, logging.F("call", row.PublicID),
					logging.F("connection", row.ConnectionPublicID), logging.F("attempts", row.Attempts))
			})
			return nil
		}
		// From its first attempt a call keeps its Alert Groups: page_not_delivered names them all.
		kept := gs
		if row.State == CallWaiting {
			kept = live
		}
		next := &call{row: row, groups: kept, contact: row.Contact.String, text: row.Text.String,
			language: row.Language.String, data: row.ActionData.String}
		if !row.Contact.Valid {
			next.unsent = groups.PageNoConfirmedContact
		}
		var contactID pgtype.Int8
		if row.State == CallWaiting {
			k, err := q.CallContact(ctx, dbgen.CallContactParams{OrgID: s.OrgID, UserID: row.UserID,
				ConnectionID: row.ConnectionID})
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				next.unsent = groups.PageNoConfirmedContact
			case err != nil:
				return fmt.Errorf("read the contact of the call %s: %w", row.PublicID, err)
			default:
				next.unsent, next.contact, contactID = "", k.Contact, pgtype.Int8{Int64: k.ID, Valid: true}
			}
			next.language = language(row.UserLanguage)
			next.text = callText(next.language, live)
		}
		if !row.Gateway {
			next.unsent = groups.PageGatewayDeleted
		}
		if next.unsent == "" {
			ok, due, err := s.limiter().ConnectionToken(ctx, tx, s.OrgID, row.ConnectionID, now)
			if err != nil {
				return err
			}
			if !ok {
				// No token: the call waits, and so far it made no attempt — a waiting call still takes Pages.
				_, err := q.RescheduleCall(ctx, dbgen.RescheduleCallParams{OrgID: s.OrgID, ID: row.ID,
					Owner: s.Lease.Owner, At: later(due, now).Add(SenderTokenMargin)})
				if err != nil {
					return fmt.Errorf("reschedule the call %s: %w", row.PublicID, err)
				}
				return nil
			}
		}
		leaseUntil := realNow.Add(s.Lease.Duration)
		if row.State == CallWaiting {
			var keyID pgtype.Text
			if next.unsent == "" {
				var kid string
				if next.data, kid, err = s.sign(row.PublicID); err != nil {
					return err
				}
				keyID = nonEmpty(kid)
			}
			if _, err := q.StartCall(ctx, dbgen.StartCallParams{OrgID: s.OrgID, ID: row.ID, Owner: s.Lease.Owner,
				ContactID: contactID, Language: next.language, Text: next.text, ActionData: nonEmpty(next.data),
				ButtonKeyID: keyID, Now: now, LeaseUntil: leaseUntil}); err != nil {
				return fmt.Errorf("start the call %s: %w", row.PublicID, err)
			}
		} else if _, err := q.RenewCallLease(ctx, dbgen.RenewCallLeaseParams{OrgID: s.OrgID, ID: row.ID,
			Owner: s.Lease.Owner, LeaseUntil: leaseUntil}); err != nil {
			return fmt.Errorf("renew the lease of the call %s: %w", row.PublicID, err)
		}
		c = next
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return c, after, nil
}

// lockPerson takes the lock of the calls of the User userID through the gateway connection.
func (s *Sender) lockPerson(ctx context.Context, q CallQueries, connection, userID int64) error {
	if err := q.LockCallPerson(ctx, dbgen.LockCallPersonParams{LockClass: db.GatewayCallLockClass, OrgID: s.OrgID,
		ConnectionID: connection, UserID: userID}); err != nil {
		return fmt.Errorf("lock the calls of the user: %w", err)
	}
	return nil
}

// end ends the call row as p says, with the person's lock held, and when the call went, sets when the call waiting
// behind it may go: CombineWindow after now. ok is false when the lease ran out or went to another replica, and
// nothing changed.
func (s *Sender) end(ctx context.Context, q CallQueries, row dbgen.GetLeasedCallRow, p dbgen.EndCallParams,
	now time.Time) (bool, error) {
	p.OrgID, p.ID, p.Owner, p.Now = s.OrgID, row.ID, s.Lease.Owner, now
	went, err := q.EndCall(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("end the call %s %s: %w", row.PublicID, p.State, err)
	}
	if went {
		if err := q.ReleaseWaitingCall(ctx, dbgen.ReleaseWaitingCallParams{OrgID: s.OrgID,
			ConnectionID: row.ConnectionID, UserID: row.UserID, At: now.Add(CombineWindow)}); err != nil {
			return false, fmt.Errorf("release the call waiting behind %s: %w", row.PublicID, err)
		}
	}
	return true, nil
}

// sign is the action data of the call publicID, signed with the active button-signature key, and the id of that key.
func (s *Sender) sign(publicID string) (data, keyID string, err error) {
	data, keyID, err = buttons.Sign(s.Keys, buttons.Action{Subject: buttons.SubjectCall, PublicID: publicID,
		Command: buttons.CommandAcknowledge})
	if err != nil {
		return "", "", fmt.Errorf("sign the action data of the call %s: %w", publicID, err)
	}
	return data, keyID, nil
}

// send makes the attempt of the call c — its text and its action data, signed at its first attempt and the same on
// every retry, under its Idempotency-Key — in the delivery client class, counts it, and records its outcome. A call
// that a replica of an earlier version started has no stored data, and its attempt is signed with the active key.
func (s *Sender) send(ctx context.Context, c *call) error {
	data := c.data
	if data == "" {
		var err error
		if data, _, err = s.sign(c.row.PublicID); err != nil {
			return err
		}
	}
	out := s.Gateway.Notify(ctx, delivery.Call{Class: outbound.ClassDelivery,
		Key: c.key()}, delivery.Notification{Connection: c.row.ConnectionID, Contact: c.contact, Text: c.text,
		Language: c.language, Action: &delivery.NotificationAction{Data: data,
			Label: messages.T(c.language, "gatewayCall.action", nil)}})
	outcome := callOutcome(out.Kind)
	metrics.GatewayCalls.With(c.row.ConnectionPublicID, outcome).Inc()
	s.Log.Log(ctx, logging.GatewayCallAttempted, logging.F("call", c.row.PublicID),
		logging.F("connection", c.row.ConnectionPublicID), logging.F("attempt", c.row.Attempts+1),
		logging.F("outcome", outcome), logging.F("replayed", out.Replayed))
	// The call was made: the end of ctx must not lose its outcome.
	return s.record(context.WithoutCancel(ctx), c, out)
}

// callOutcome is the outcome label of muster_gateway_calls_total of an answer: accepted, a replayed answer included,
// or its error class; an answer of a kind a gateway does not give is unknown.
func callOutcome(k delivery.OutcomeKind) string {
	switch k {
	case delivery.OutcomeOK:
		return metrics.OutcomeAccepted
	case delivery.OutcomeRetryAfter, delivery.OutcomeTransient, delivery.OutcomeFatal:
		return string(k)
	case delivery.OutcomeUnknown, delivery.OutcomeMarkupRejected, delivery.OutcomeGone, delivery.OutcomeThreadLost,
		delivery.OutcomeTemplateError, delivery.OutcomeEditWindowClosed:
	}
	return string(delivery.OutcomeUnknown)
}

// record records the outcome out of an attempt of c (C-11.FR-8 applied to calls): ok, replayed or not, ends it
// accepted; a RetryAfter waits exactly as asked, holds the gateway's limiter and counts no attempt, but its time counts
// toward the time budget of delivery.transient_budget, after which it ends Not delivered with call_budget_used; a
// Transient one is an attempt of delivery.transient_budget, retried after delivery.transient_backoff and Not delivered
// with call_budget_used once the budget is used; a Fatal answer — the gateway refused the call or its bridge, until
// S-149 makes the gateway Broken — and an invalid one end it Not delivered with call_refused; any other answer ends it
// Not delivered with call_unknown.
func (s *Sender) record(ctx context.Context, c *call, out delivery.Outcome) error {
	now := s.Lease.Clocks.Business.Now().UTC()
	switch out.Kind {
	case delivery.OutcomeOK:
		return s.Store.InCallTx(ctx, func(_ DBTX, q CallQueries) error {
			if err := s.lockPerson(ctx, q, c.row.ConnectionID, c.row.UserID); err != nil {
				return err
			}
			_, err := s.end(ctx, q, c.row, dbgen.EndCallParams{State: CallAccepted, Attempted: 1,
				NotificationID: nonEmpty(out.MessageID)}, now)
			return err
		})
	case delivery.OutcomeRetryAfter:
		// A wait counts no attempt, but its time counts toward the time budget of delivery.transient_budget: a gateway
		// that keeps asking to wait ends the call Not delivered, and the Pages waiting behind it go.
		if f := c.row.FirstFailedAt; f.Valid && now.Sub(f.Time) > delivery.TransientBudgetTime {
			return s.notDelivered(ctx, c, groups.PageCallBudgetUsed, 0, out)
		}
		until := now.Add(out.RetryAfter)
		return s.Store.InCallTx(ctx, func(tx DBTX, q CallQueries) error {
			n, err := q.RescheduleCall(ctx, dbgen.RescheduleCallParams{OrgID: s.OrgID, ID: c.row.ID,
				Owner: s.Lease.Owner, At: until.Add(SenderTokenMargin), ErrorClass: nonEmpty(string(out.Kind)),
				Error: nonEmpty(string(out.Error)), FailedAt: pgtype.Timestamptz{Time: now, Valid: true}})
			if err != nil || n == 0 {
				return err
			}
			return s.limiter().HoldConnection(ctx, tx, s.OrgID, c.row.ConnectionID, until)
		})
	case delivery.OutcomeTransient:
		attempts := c.row.Attempts + 1
		first := c.row.FirstFailedAt
		if !first.Valid {
			first = pgtype.Timestamptz{Time: now, Valid: true}
		}
		if delivery.TransientExhausted(attempts, first, now) {
			return s.notDelivered(ctx, c, groups.PageCallBudgetUsed, 1, out)
		}
		_, err := s.Store.RecordCallRetry(ctx, dbgen.RecordCallRetryParams{OrgID: s.OrgID, ID: c.row.ID,
			Owner: s.Lease.Owner, At: now.Add(delivery.TransientBackoff(attempts, s.Random)), Now: now,
			ErrorClass: string(out.Kind), Error: nonEmpty(string(out.Error))})
		if err != nil {
			return fmt.Errorf("record the transient error of the call %s: %w", c.row.PublicID, err)
		}
		return nil
	case delivery.OutcomeFatal:
		return s.notDelivered(ctx, c, groups.PageCallRefused, 1, out)
	case delivery.OutcomeUnknown:
		if out.Status >= http.StatusBadRequest && out.Status < http.StatusInternalServerError && !out.Unanswered {
			return s.notDelivered(ctx, c, groups.PageCallRefused, 1, out)
		}
	case delivery.OutcomeMarkupRejected, delivery.OutcomeGone, delivery.OutcomeThreadLost,
		delivery.OutcomeTemplateError, delivery.OutcomeEditWindowClosed:
	}
	return s.notDelivered(ctx, c, groups.PageCallUnknown, 1, out)
}

// notDelivered ends the call c Not delivered with reason, counting attempted attempts, and records page_not_delivered
// on each of its Alert Groups naming the person and the gateway, in one transaction in the lock order of calls.go:
// the dispatcher locks the Alert Groups, then the person's lock and the call are taken. A call whose lease went to
// another replica meanwhile changes nothing: the transaction rolls back, page_not_delivered with it.
func (s *Sender) notDelivered(ctx context.Context, c *call, reason string, attempted int64,
	out delivery.Outcome) error {
	now := s.Lease.Clocks.Business.Now().UTC()
	ids := make([]int64, len(c.groups))
	for i, g := range c.groups {
		ids[i] = g.ID
	}
	page := groups.Page{Users: []groups.PagedUser{{ID: c.row.UserID, PublicID: c.row.UserPublicID,
		Name: c.row.UserName, Login: c.row.UserLogin}}, Gateway: &groups.PageGateway{ID: c.row.ConnectionID,
		PublicID: c.row.ConnectionPublicID, Name: c.row.ConnectionName}, Reason: reason}
	p := dbgen.EndCallParams{State: CallNotDelivered, Reason: nonEmpty(reason), Attempted: attempted}
	if class := callOutcome(out.Kind); out.Kind != "" && out.Kind != delivery.OutcomeOK {
		p.ErrorClass, p.Error = nonEmpty(class), nonEmpty(string(out.Error))
	}
	var recorded func(context.Context)
	errLost := errors.New("the lease of the call was lost")
	err := s.Store.InCallTx(ctx, func(tx DBTX, q CallQueries) error {
		recorded = nil
		if s.lockCallFirst {
			if err := s.lockPerson(ctx, q, c.row.ConnectionID, c.row.UserID); err != nil {
				return err
			}
			if _, err := q.GetLeasedCall(ctx, dbgen.GetLeasedCallParams{OrgID: s.OrgID, ID: c.row.ID,
				Owner: s.Lease.Owner, Now: time.Time{}}); err != nil {
				return fmt.Errorf("lock the call: %w", err)
			}
		}
		var err error
		if recorded, err = s.Pages.PageNotDelivered(ctx, tx, ids, page); err != nil {
			return fmt.Errorf("record page_not_delivered of the call %s: %w", c.row.PublicID, err)
		}
		if err := s.lockPerson(ctx, q, c.row.ConnectionID, c.row.UserID); err != nil {
			return err
		}
		ok, err := s.end(ctx, q, c.row, p, now)
		if err == nil && !ok {
			return errLost
		}
		return err
	})
	if errors.Is(err, errLost) {
		return nil
	}
	if err != nil {
		return err
	}
	if recorded != nil {
		recorded(ctx)
	}
	s.Log.Log(ctx, logging.GatewayCallNotDelivered, logging.F("call", c.row.PublicID),
		logging.F("connection", c.row.ConnectionPublicID), logging.F("user", c.row.UserPublicID),
		logging.F("reason", reason), logging.F("error", string(out.Error)))
	return nil
}

// callText is the text of a call in lang (C-30.FR-5, C-12.FR-3): how many Alert Groups need the person, each by its
// number, title and Severity level, and how to acknowledge.
//
// The text stays within MaxCallText, so that the bridge client never cuts how to acknowledge: it lists as many Alert
// Groups as fit, in order, then "and N more", and an Alert Group whose title alone is too long is cut.
func callText(lang string, gs []dbgen.ListCallGroupsRow) string {
	header := messages.N(lang, "gatewayCall.header", int64(len(gs)), nil)
	action := messages.T(lang, "gatewayCall.action", nil)
	items := make([]string, len(gs))
	for i, g := range gs {
		items[i] = messages.T(lang, "gatewayCall.item", messages.Args{
			"number": strconv.FormatInt(g.Number, 10), "title": g.Title, "severity": g.SeverityLevel})
	}
	join := func(listed []string, more int) string {
		parts := append([]string{header}, listed...)
		if more > 0 {
			parts = append(parts, messages.N(lang, "gatewayCall.more", int64(more), nil))
		}
		return strings.Join(append(parts, action), " ")
	}
	for n := len(items); n > 0; n-- {
		if t := join(items[:n], len(items)-n); utf8.RuneCountInString(t) <= MaxCallText {
			return t
		}
	}
	// Not even the first Alert Group fits: it is listed cut, with the others counted.
	room := MaxCallText - utf8.RuneCountInString(join(nil, len(items)-1)) - 1
	if len(items) == 0 || room < 2 {
		return join(nil, len(items))
	}
	return join([]string{string([]rune(items[0])[:room-1]) + "…"}, len(items)-1)
}

// MaxCallText is the longest text of a call: the bridge client cuts a notification's text there (api/bridge.yaml).
const MaxCallText = bridge.MaxNotificationText

func nonEmpty(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
