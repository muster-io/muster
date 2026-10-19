// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// The kinds of an attempt, the kind label of muster_delivery_attempts_total.
const (
	kindPublication  = "publication"
	kindUpdate       = "update"
	kindReply        = "thread_reply"
	kindStormSummary = "storm_summary"
	kindFinalEdit    = "final_edit"
)

// Lock order. A transaction that locks rows of more than one of these tables locks them in this order, the
// dispatcher's: the Alert Groups by id (groups' LockGroups), then their deliveries, then the Thread replies of a
// delivery. Enqueue takes the Route's membership lock before its deliveries, and a Storm's row comes before the
// deliveries it changes. A delivery is made held by a Storm only under a lock of that Storm that its calm check,
// which locks it FOR NO KEY UPDATE, waits for: Enqueue's JoinStorm updates the Storm, and a change of the Route's
// Destinations takes the membership lock, then the active Storm FOR SHARE (LockActiveStorm), then the deliveries;
// otherwise a calm check could end the Storm before those deliveries commit, and they would stay held. The transactions
// of the worker follow it: the record of a Thread reply's outcome locks its delivery first (LockReplyDelivery), and the
// first Publication is marked through the dispatcher only after the outcome committed (markPublished), never inside it.
// A Publication a bridge replayed is no exception: its outcome and the edit that follows lock the delivery row only, as
// every outcome and every edit does, never its Alert Group before it; nor is a republication past a bridge's edit
// window (editwindow.go), whose preparation updates the delivery row only — its lease and its start, as a Publication's
// does — and whose outcome locks it only.
// The preparation of a Telegram reply that waits for its copy is the one exception: it holds the reply, then takes the
// post's copy lock and the delivery, the order of the learning of a copy and of the record of a Publication
// (copies.go). The re-render after a key rotation (rerender.go) takes the dispatcher's order — the Alert Group, SKIP
// LOCKED, then the Route's membership lock shared, then its deliveries by Destination — and never a Storm. A key's
// advisory lock (LockButtonKey) is taken only by the outcome that replaces the key of a Root message's buttons
// (released), after its delivery; after it, that outcome still updates the delivery's Thread (publishedThread), records
// delivery events (RecordEvent) and may lock its Destination's row to end the Broken state (markHealthy). No
// transaction locks a Destination before a key's lock, so none waits for the key while holding what the outcome
// waits for.
//
// PostgreSQL takes locks of its own that this order does not show: an UPDATE of a row version the same transaction made
// — a second UPDATE of a row in one transaction, or an UPDATE after its INSERT — checks every foreign key of the row
// again, and each check locks the referenced row FOR KEY SHARE: the Alert Group, the Storm and the Destination of a
// delivery, the delivery of a Thread reply. Such a check runs against the order: the worker's prepare locks a delivery,
// updates it twice and so locks its Alert Group after it. So an explicit lock on a row other rows reference —
// alert_groups, deliveries, storms, destinations — is FOR NO KEY UPDATE, never FOR UPDATE, as no transaction here
// deletes such a row or changes its key: FOR KEY SHARE conflicts only with FOR UPDATE, so these checks never wait for
// the dispatcher, the worker, a Storm's calm check or a Destination's recovery, which still exclude each other. The
// claims keep FOR UPDATE SKIP LOCKED: a claim holds its rows in a transaction of its own that waits for nothing. The
// retention of Alert Groups deletes them, FOR UPDATE, and skips every one another transaction holds; only its cascade
// to the deliveries of an Alert Group resolved retention.alert_group_summaries ago could still meet the worker.

// Worker is the delivery worker of one replica (C-11.FR-1, FR-15; schema.md §5): it probes the Broken Destinations
// that are due, claims the due deliveries of healthy Destinations, Urgent first, and the due Thread replies with FOR
// UPDATE SKIP LOCKED and a lease, re-reads each in a short transaction, takes the limiter tokens, calls the adapter
// outside any transaction in the delivery client class, and records the outcome by its rule (outcomes.go) in another
// short transaction. It is woken by NOTIFY, by the earliest due time and, in development mode, by a move of the
// development clock.
type Worker struct {
	Store *Store
	// Lease names this replica in lease_owner and carries the clocks: due times and limiters on the business clock,
	// leases and the duration of calls on the real clock.
	Lease db.Lease
	// Organizations lists the Organizations whose deliveries it makes: one in L1.
	Organizations func(ctx context.Context) ([]int64, error)
	Adapters      Adapters
	// Events sends the events-mode requests of outgoing webhooks.
	Events   EventSender
	Renderer Renderer
	// Mentions resolves the Mentions of Loud calls; nil mentions nobody.
	Mentions Mentioner
	Log      *logging.Logger
	// MaxWait bounds the wait between rounds, and Batch the rows one claim takes; zero is the default.
	MaxWait time.Duration
	Batch   int32
	// Wait waits for d or until a wake arrives; nil waits on the real clock.
	Wait func(ctx context.Context, d time.Duration, wake <-chan struct{})
	// Random is uniform in [0, 1) and jitters delivery.transient_backoff; nil is math/rand/v2.
	Random func() float64
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL, the base of the runbook_url of MusterDestinationBroken.
	RunbookBase string
	// PublicURL is MUSTER_PUBLIC_URL, the base of the link to an Alert Group in a final edit.
	PublicURL string
	// Published marks the first Publication of an Alert Group at a time, (*groups.Service).MarkPublished, once the
	// outcome that recorded it committed; nil marks nothing.
	Published func(ctx context.Context, groupID int64, at time.Time) error

	once sync.Once
	wake chan struct{}
}

func (w *Worker) init() {
	w.once.Do(func() { w.wake = make(chan struct{}, 1) })
}

// Wake makes the worker run a round at once: a Desired state changed, a Thread reply was queued, the development
// clock moved, or the LISTEN is back.
func (w *Worker) Wake() {
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run delivers until ctx ends: a round, then a wait until the next row can be claimed — its due time on the business
// clock or the end of its lease on the real clock — between MinWait and MaxWait, or until woken; after a failed
// round, a backoff.
func (w *Worker) Run(ctx context.Context) {
	w.init()
	wait := w.Wait
	if wait == nil {
		wait = waitReal
	}
	failures := 0
	for ctx.Err() == nil {
		next, err := w.Round(ctx)
		d := max(next, MinWait)
		if err != nil {
			failures++
			d = db.Backoff(failures, FailureBackoff, w.maxWait(), rand.Float64)
		} else {
			failures = 0
		}
		wait(ctx, d, w.wake)
	}
}

func (w *Worker) maxWait() time.Duration {
	if w.MaxWait <= 0 {
		return MaxWait
	}
	return w.MaxWait
}

func (w *Worker) batch() int32 {
	if w.Batch <= 0 {
		return Batch
	}
	return w.Batch
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

// Round probes every Broken Destination whose probe is due, then attempts every due delivery, then every due Thread
// reply, then every due head event of an outgoing webhook, of every Organization, claiming Batch at a time, and returns how long until the next one can be claimed, at
// most MaxWait. A row that fails is logged and attempted again once its lease runs out; the round goes on.
func (w *Worker) Round(ctx context.Context) (time.Duration, error) {
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list the organizations for delivery: %w", err)
	}
	next := w.maxWait()
	var errs []error
	for _, org := range orgs {
		errs = append(errs, w.probe(ctx, org), w.drain(ctx, org, w.claimDeliveries, w.deliver),
			w.drain(ctx, org, w.claimReplies, w.reply), w.drain(ctx, org, w.claimEvents, w.sendEvent))
		n, err := w.next(ctx, org, next)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		next = min(next, n)
	}
	return next, errors.Join(errs...)
}

// drain claims Batch rows at a time with claim and works each with work until a claim comes back short.
func (w *Worker) drain(ctx context.Context, org int64,
	claim func(ctx context.Context, org int64) ([]int64, error), work func(ctx context.Context, org, id int64)) error {
	for {
		ids, err := claim(ctx, org)
		if err != nil {
			return err
		}
		for _, id := range ids {
			work(ctx, org, id)
		}
		if len(ids) < int(w.batch()) {
			return nil
		}
	}
}

// claimDeliveries leases the due deliveries of the Organization, Urgent first.
func (w *Worker) claimDeliveries(ctx context.Context, org int64) ([]int64, error) {
	rows, err := db.Claim(ctx, w.Store.begin, w.Lease, w.batch(),
		func(ctx context.Context, tx pgx.Tx, p db.ClaimParams) ([]dbgen.ClaimDueDeliveriesRow, error) {
			return w.Store.queries(tx).ClaimDueDeliveries(ctx, dbgen.ClaimDueDeliveriesParams{OrgID: org,
				Owner: p.Owner, LeaseUntil: p.LeaseUntil, Due: p.Due, Now: p.Now, Lim: p.Limit})
		})
	if err != nil {
		return nil, fmt.Errorf("claim the due deliveries: %w", err)
	}
	slices.SortFunc(rows, func(a, b dbgen.ClaimDueDeliveriesRow) int {
		if a.Urgent != b.Urgent {
			if a.Urgent {
				return -1
			}
			return 1
		}
		if c := a.NextAttemptAt.Compare(b.NextAttemptAt); c != 0 {
			return c
		}
		if a.LastDeliveredAt.Valid != b.LastDeliveredAt.Valid {
			if !a.LastDeliveredAt.Valid {
				return -1
			}
			return 1
		}
		return cmp.Or(a.LastDeliveredAt.Time.Compare(b.LastDeliveredAt.Time), cmp.Compare(a.ID, b.ID))
	})
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// next is how long until a delivery or a Thread reply of the Organization can be claimed, at most limit.
func (w *Worker) next(ctx context.Context, org int64, limit time.Duration) (time.Duration, error) {
	realNow := w.Lease.Clocks.Real.Now().UTC()
	r, err := w.Store.q().NextDeliveryWork(ctx, dbgen.NextDeliveryWorkParams{OrgID: org, Now: realNow})
	if err != nil {
		return 0, fmt.Errorf("read when the next delivery is due: %w", err)
	}
	d := limit
	if r.FreeAt.Year() > 1 {
		d = min(d, r.FreeAt.Sub(w.Lease.Clocks.Business.Now()))
	}
	if r.LeaseEnd.Year() > 1 {
		d = min(d, r.LeaseEnd.Sub(realNow))
	}
	return d, nil
}

// attempt is a delivery that has its tokens and is about to be called; late says that its Publication carries the
// late note. idempotent says that its adapter is Idempotent, and started that its Publication — or its republication —
// had started before this attempt. For a Root message past its bridge's edit window (C-22.FR-9), republish is a new
// Root message for a new status, with pointer the outcome of the reply under the old one once it was sent, and
// finalReply a final edit sent as a Thread reply; needsWindow is a delivery whose rule needs the window read first,
// outside any transaction.
type attempt struct {
	row         dbgen.GetLeasedDeliveryRow
	destination Destination
	publication bool
	late        bool
	message     Message
	at          time.Time
	// targets are the Mentions of a Loud Publication.
	targets     []mentions.Target
	idempotent  bool
	started     bool
	republish   bool
	finalReply  bool
	needsWindow bool
	pointer     *Outcome
}

// replayed reports whether the outcome out is a Publication the messenger replayed: the first result of its
// idempotency key, posted by an earlier attempt (C-22.FR-6).
func (a attempt) replayed(out Outcome) bool { return (a.publication || a.republish) && out.Replayed }

// duplicate reports whether the ok outcome out of the attempt records a possible duplicate (C-22.FR-6): a retry of a
// started Publication or republication to an Idempotent adapter that was not replayed, so the messenger did not know
// its key.
func (a attempt) duplicate(out Outcome) bool {
	return a.idempotent && (a.publication || a.republish) && a.started && !out.Replayed
}

// keepStarted reports whether the outcome out keeps the start of the attempt's Publication or republication for its
// retry (C-22.FR-6): for an Idempotent adapter, one that may have reached the messenger — started before this attempt,
// or a request without an answer — so that the retry is replayed, or records a possible duplicate when it is not. A
// failure the messenger answered stored nothing, and a refused connection sent nothing.
func (a attempt) keepStarted(out Outcome) bool {
	return a.idempotent && (a.publication || a.republish) && (a.started || out.Unanswered)
}

// idempotent is the adapter of the Destination type typ when it keeps idempotency keys (C-22.FR-6).
func (w *Worker) idempotent(typ string) (Idempotent, bool) {
	i, ok := w.Adapters[typ].(Idempotent)
	return i, ok
}

// kind is the kind of the attempt's call: the final edit, a call of a Storm summary, a Publication or an edit.
func (a attempt) kind() string {
	switch {
	case a.row.DesiredRetire:
		return kindFinalEdit
	case a.row.StormID.Valid:
		return kindStormSummary
	case a.publication, a.republish:
		return kindPublication
	}
	return kindUpdate
}

// group is the Alert Group a delivery event of the attempt concerns, nil for a Storm summary.
func (a attempt) group() *int64 { return groupOf(a.row) }

// storm is the Storm of a Storm summary, nil for an Alert Group.
func (a attempt) storm() *int64 { return int8Of(a.row.StormID) }

// linksOf are the links of the requests of a delivery to d about the Alert Group group, nil for a Storm summary: the
// Request log shows them under d and the Alert Group (C-23.FR-2).
func linksOf(d Destination, group *int64) outbound.Links {
	l := outbound.Links{Destination: d.ID}
	if group != nil {
		l.AlertGroup = *group
	}
	return l
}

// groupOf is the Alert Group of a leased delivery, nil for a Storm summary.
func groupOf(row dbgen.GetLeasedDeliveryRow) *int64 {
	if row.StormID.Valid {
		return nil
	}
	id := row.AlertGroupID
	return &id
}

// deliver attempts one claimed delivery; a failure is logged and the row is attempted again once its lease runs out. A
// delivery whose rule needs its bridge's edit window is prepared again once the window was read.
func (w *Worker) deliver(ctx context.Context, org, id int64) {
	a, ok, logs, err := w.prepare(ctx, org, id, nil)
	if err == nil && a.needsWindow {
		a, ok, logs, err = w.prepare(ctx, org, id, w.editWindow(ctx, a.destination))
	}
	if err == nil {
		logs.run(ctx, w.Log)
	}
	if err == nil && ok {
		err = w.call(ctx, org, a)
	}
	if err != nil && ctx.Err() == nil {
		w.Log.Log(ctx, logging.DeliveryWorkFailed, logging.F("work", "delivery"), logging.F("error", err.Error()))
	}
}

// prepare re-reads a claimed delivery in a short transaction, while its lease is still held: a delivery whose actual
// message already shows the Desired state is delivered with no call; an edit of a Telegram Root message while a press
// of its Connection is being handled waits TelegramPressEditDelay, taking no token; one without tokens waits until they
// are due plus TokenMargin; otherwise the tokens are taken, the lease is renewed for the call and, before a
// Publication, a possible duplicate and the start of the Publication are recorded and its notes added. For an
// Idempotent adapter the start of an earlier attempt is kept and no possible duplicate is recorded here: the outcome
// decides it (C-22.FR-6). A final edit due before its Root message was published withholds the delivery, except after
// a started Publication of an Idempotent adapter, which may have reached the messenger: the Publication is made again
// with its key, Quiet and without notes, carrying the final edit, so that a replay names the Root message the final
// edit is then made to. A published Root message of an EditWindowed adapter follows the rules of its edit window
// (editwindow.go), which win, read outside any transaction, says, before any token is taken: without win, and unless
// the row is flagged past its window already or its republication started, nothing is written and the attempt
// needsWindow. A started republication is made again even when the actual message shows the Desired state, as the
// bridge may have posted a new Root message; its start is recorded before the call, as a Publication's is. ok says
// whether to call; logs are the lines to log now that it committed.
func (w *Worker) prepare(ctx context.Context, org, id int64, win *editWindow) (attempt, bool, after, error) {
	var a attempt
	var logs after
	ok := false
	err := w.Store.inTxWith(ctx, func(tx dbgen.DBTX, q queries) error {
		ok, logs = false, nil
		realNow := w.Lease.Clocks.Real.Now().UTC()
		row, err := q.GetLeasedDelivery(ctx, dbgen.GetLeasedDeliveryParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			Now: realNow})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the lease ran out: any replica claims it again
		}
		if err != nil {
			return fmt.Errorf("read the delivery: %w", err)
		}
		now := w.Lease.Clocks.Business.Now().UTC()
		_, idempotent := w.idempotent(row.DestinationType)
		mayExist := idempotent && row.PublicationStartedAt.Valid
		if row.DesiredRetire && unpublished(row) && !mayExist {
			// Its Destination left before its Root message was ever published there.
			if err := q.WithholdLeased(ctx, dbgen.WithholdLeasedParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				Now: now}); err != nil {
				return fmt.Errorf("withhold the delivery: %w", err)
			}
			if err := hintGroup(ctx, q, org, row.AlertGroupPublicID); err != nil {
				return err
			}
			return wipeSecrets(ctx, q, org, row.DestinationID, realNow)
		}
		if !row.DesiredRetire && !republishing(row) && bytes.Equal(row.ActualHash, row.DesiredHash) {
			if err := q.MarkDelivered(ctx, dbgen.MarkDeliveredParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				Version: row.DesiredVersion, Now: now}); err != nil {
				return fmt.Errorf("mark the delivery delivered: %w", err)
			}
			return hintGroup(ctx, q, org, row.AlertGroupPublicID)
		}
		if busy, err := pressPending(ctx, q, row); err != nil || busy {
			if err != nil {
				return err
			}
			if err := q.RescheduleDelivery(ctx, dbgen.RescheduleDeliveryParams{OrgID: org, ID: id,
				Owner: w.Lease.Owner, At: now.Add(TelegramPressEditDelay), Now: now}); err != nil {
				return fmt.Errorf("reschedule the delivery: %w", err)
			}
			return nil
		}
		d := Destination{ID: row.DestinationID, PublicID: row.DestinationPublicID, Name: row.DestinationName,
			Type: row.DestinationType, Connection: int8Of(row.ConnectionID)}
		msg, err := decode(row.DesiredPayload)
		if err != nil {
			return err
		}
		rule := ruleEdit
		if _, windowed := w.editWindowed(row.DestinationType); windowed && !unpublished(row) {
			if win == nil && !row.PastEditWindow && !republishing(row) {
				a = attempt{row: row, destination: d, needsWindow: true}
				return nil
			}
			var known editWindow
			if win != nil {
				known = *win
			}
			if rule = ruleOf(row, msg.Colour, known, now); rule == ruleNoCall {
				if err := q.MarkDelivered(ctx, dbgen.MarkDeliveredParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
					Version: row.DesiredVersion, PastEditWindow: true, Now: now}); err != nil {
					return fmt.Errorf("mark the delivery delivered past its edit window: %w", err)
				}
				return hintGroup(ctx, q, org, row.AlertGroupPublicID)
			}
		}
		t, err := takeTokens(ctx, q, org, subjectOf(d), now)
		if err != nil {
			return err
		}
		if !t.ok {
			if err := q.RescheduleDelivery(ctx, dbgen.RescheduleDeliveryParams{OrgID: org, ID: id,
				Owner: w.Lease.Owner, At: t.due.Add(TokenMargin), Now: now}); err != nil {
				return fmt.Errorf("reschedule the delivery: %w", err)
			}
			return nil
		}
		if row.DesiredRetire {
			msg = messages.FinalEdit(msg, w.link(row.AlertGroupPublicID))
		}
		a = attempt{row: row, destination: d, publication: unpublished(row), message: msg, at: now,
			idempotent: idempotent, started: row.PublicationStartedAt.Valid, republish: rule == ruleRepublish,
			finalReply: rule == ruleFinalReply}
		if a.republish {
			a.started = row.RepublicationStartedAt.Valid
		}
		if err := q.RenewDeliveryLease(ctx, dbgen.RenewDeliveryLeaseParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			LeaseUntil: realNow.Add(w.Lease.Duration)}); err != nil {
			return fmt.Errorf("renew the lease of the delivery: %w", err)
		}
		if a.republish && a.idempotent {
			if err := q.StartRepublication(ctx, dbgen.StartRepublicationParams{OrgID: org, ID: id,
				Owner: w.Lease.Owner, Now: now}); err != nil {
				return fmt.Errorf("record the start of the republication: %w", err)
			}
		}
		if a.publication {
			if a.started && !a.idempotent {
				if err := w.possibleDuplicate(ctx, q, org, row, now, &logs); err != nil {
					return err
				}
			}
			if err := q.StartPublication(ctx, dbgen.StartPublicationParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				Keep: a.idempotent, Now: now}); err != nil {
				return fmt.Errorf("record the start of the publication: %w", err)
			}
			if row.DesiredRetire {
				ok = true
				return nil
			}
			if a.message, a.late, err = publicationNotes(ctx, q, org, row, a.message); err != nil {
				return err
			}
			loud, ms := publicationLoudness(row.PublicationLoud, row.StormID.Valid)
			if a.targets, err = w.targets(ctx, tx, org, d, row.AlertGroupID, 0, loud, ms); err != nil {
				return err
			}
		}
		ok = true
		return nil
	})
	return a, ok, logs, err
}

// pressPending reports whether the leased delivery row is an edit of a Telegram Root message while an update of its
// Connection is being handled, such as a press not answered yet (C-14.FR-5, AC-18). It tries the Connection's update
// lock shared, without waiting, after the Desired state was read: a press's Command commits that state while its
// handler holds the lock, until the press is answered, so an edit that finds the lock free carries either a state from
// before the press or one whose press was answered. The shared lock is held until the transaction ends.
func pressPending(ctx context.Context, q queries, row dbgen.GetLeasedDeliveryRow) (bool, error) {
	if !row.MessageID.Valid || row.DestinationType != TypeTelegram || !row.ConnectionID.Valid {
		return false, nil
	}
	free, err := q.TryShareUpdateLock(ctx, dbgen.TryShareUpdateLockParams{LockClass: db.TelegramUpdateLockClass,
		ConnectionID: row.ConnectionID.Int64})
	if err != nil {
		return false, fmt.Errorf("try the update lock of the connection: %w", err)
	}
	return !free, nil
}

// unpublished reports whether the Root message of a leased delivery does not exist yet: published_at is not set
// (D277), or — for a messenger, whose Publication always names its message — the message id is not set, as a replica
// of an earlier version leaves a Root message it forgets for a republication while it upgrades.
func unpublished(row dbgen.GetLeasedDeliveryRow) bool {
	return !row.PublishedAt.Valid || (!sendsRequests(row.DestinationType) && !row.MessageID.Valid)
}

// publicationLoudness is the loudness and Mentions of a first Publication: Loud with new_alert_group when it comes
// from `created`, a recovery finds its Alert Group firing or it is a Storm summary; Quiet otherwise — in a Destination
// added to the Route, after a Storm, late, published again after a deletion, or recovered while not firing
// (C-11.FR-6, FR-11, FR-13, FR-14, FR-19).
func publicationLoudness(loud pgtype.Bool, summary bool) (groups.Loudness, []groups.Mention) {
	switch {
	case summary && loud.Valid && loud.Bool:
		r := deliveryEvent(DeliveryStormSummary)
		return r.Loudness, r.Mentions
	case loud.Valid && loud.Bool:
		r := deliveryEvent(DeliveryAfterRecovery)
		return r.Loudness, r.Mentions
	}
	return groups.Quiet, nil
}

// call makes the adapter call of a prepared delivery outside any transaction and records its outcome; a markup the
// messenger rejects is sent again without markup in the same attempt. A Publication to an Idempotent adapter carries
// the key of its number, the same for every retry of it; a replayed one observes no latency, as its message shows the
// state of an earlier attempt. A Publication that carries the final edit is Quiet. A republication past the edit
// window is a Publication as its row of the Loud/Quiet table says, followed, once accepted, by the pointer reply under
// the old Root message; a final edit past the window is a Thread reply with its own key.
func (w *Worker) call(ctx context.Context, org int64, a attempt) error {
	ctx = outbound.WithLinks(ctx, linksOf(a.destination, a.group()))
	c := Call{Class: outbound.ClassDelivery, Destination: a.destination, Loudness: groups.Quiet}
	kind := a.kind()
	if a.publication && !a.row.DesiredRetire {
		c.Loudness, c.Mentions = publicationLoudness(a.row.PublicationLoud, a.row.StormID.Valid)
		c.Targets = a.targets
	}
	if a.republish {
		r := deliveryEvent(DeliveryPastEditWindow)
		c.Loudness, c.Mentions = r.Loudness, r.Mentions
	}
	if idem, ok := w.idempotent(a.destination.Type); ok && (a.publication || a.republish) {
		c.Key = idem.PublicationKey(a.destination, a.row.ID, a.row.Publications+1)
	}
	if e, ok := w.editWindowed(a.destination.Type); ok && a.finalReply {
		c.Key = e.FinalKey(a.destination, a.row.ID)
	}
	if sendsRequests(a.destination.Type) {
		c.Webhook = &WebhookCall{State: a.row.DesiredPayload, Response: responseValues(a.row.ResponseValues),
			ThreadOpened: a.row.ThreadOpened}
		if a.row.DesiredRetire {
			c.Webhook.Final = messages.T(requestState(a.row.DesiredPayload).Language, "notice.final",
				messages.Args{"link": w.link(a.row.AlertGroupPublicID)})
		}
	}
	start := w.Lease.Clocks.Real.Now()
	out := w.send(ctx, a, c)
	var rejected *Outcome
	if out.Kind == OutcomeMarkupRejected {
		first := out
		rejected = &first
		w.attempted(ctx, a.destination, a.row.Number, kind, out, a.row.Attempts+1, w.Lease.Clocks.Real.Now().Sub(start))
		c.Plain = true
		out = plain(func() Outcome { return w.send(ctx, a, c) })
	}
	took := w.Lease.Clocks.Real.Now().Sub(start)
	if a.republish && out.Kind == OutcomeOK {
		p := w.pointer(ctx, a, out)
		a.pointer = &p
	}
	recorded := false
	var logs after
	err := w.Store.inTx(ctx, func(q queries) error {
		logs = nil
		var err error
		recorded, err = w.record(ctx, q, org, a, c, out, rejected, &logs)
		return err
	})
	w.attempted(ctx, a.destination, a.row.Number, kind, out, a.row.Attempts+1, took)
	if err == nil {
		logs.run(ctx, w.Log)
	}
	if err == nil && recorded && out.Kind == OutcomeOK && !a.replayed(out) && a.row.DesiredReceivedAt.Valid {
		metrics.DeliveryLatency.With(a.destination.PublicID).Update(
			max(a.at.Sub(a.row.DesiredReceivedAt.Time).Seconds(), 0))
	}
	return err
}

// send is one adapter call of a prepared delivery: a Publication, an edit, or the final edit of a Root message past
// its edit window as a Thread reply; a type no adapter serves is an unknown response (D277).
func (w *Worker) send(ctx context.Context, a attempt, c Call) Outcome {
	switch adapter := w.Adapters[a.destination.Type]; {
	case adapter == nil:
		return Outcome{Kind: OutcomeUnknown, Error: outbound.Untrusted("no adapter for the destination type " +
			a.destination.Type)}
	case a.publication, a.republish:
		return adapter.Publish(ctx, c, a.message)
	case a.finalReply:
		return adapter.Reply(ctx, c, rootOfRow(a.row), a.message)
	default:
		return adapter.Update(ctx, c, a.row.MessageID.String, a.message)
	}
}

// pointer sends, after the republication published as out was accepted, the pointer reply under the Root message it
// replaced (C-22.FR-9): Quiet, with its own key, under the Publication's tokens, linking to the new message's url when
// it is safe and to the Alert Group page otherwise. It is part of the republication, never retried; its outcome is
// recorded with the republication's delivery event.
func (w *Worker) pointer(ctx context.Context, a attempt, out Outcome) Outcome {
	e, _ := w.editWindowed(a.destination.Type)
	link := messages.SafeURL(out.MessageURL)
	if link == "" {
		link = w.link(a.row.AlertGroupPublicID)
	}
	c := Call{Class: outbound.ClassDelivery, Destination: a.destination, Loudness: groups.Quiet,
		Key: e.PointerKey(a.destination, a.row.ID, a.row.Publications)}
	start := w.Lease.Clocks.Real.Now()
	p := w.Adapters[a.destination.Type].Reply(ctx, c, rootOfRow(a.row), pointerMessage(a.message, link))
	w.attempted(ctx, a.destination, a.row.Number, kindReply, p, 1, w.Lease.Clocks.Real.Now().Sub(start))
	return p
}

// rootOfRow is the Root message of a leased delivery, with its bridge's last `thread_ref`.
func rootOfRow(row dbgen.GetLeasedDeliveryRow) Root {
	return Root{MessageID: row.MessageID.String, ThreadAnchorID: row.ThreadAnchorID.String}
}

// attempted counts an attempt and writes its delivery_attempt line, with the status and the masked error of an answer
// that was not delivered; a Storm summary, number 0, names no Alert Group.
func (w *Worker) attempted(ctx context.Context, d Destination, number int64, kind string, out Outcome, n int64,
	took time.Duration) {
	if o := metricOutcome(out.Kind); o != "" {
		metrics.DeliveryAttempts.With(d.PublicID, kind, o).Inc()
	}
	outcome, code, reason := string(out.Kind), out.Status, string(out.Error)
	if out.Kind == OutcomeOK {
		outcome, code, reason = "delivered", 0, ""
	}
	var retry int64
	if out.Kind == OutcomeRetryAfter {
		retry = out.RetryAfter.Milliseconds()
	}
	group := ""
	if number > 0 {
		group = "#" + strconv.FormatInt(number, 10)
	}
	w.Log.Log(ctx, logging.DeliveryAttempt, logging.F("destination", d.PublicID), logging.F("group", group),
		logging.F("kind", kind), logging.F("outcome", outcome), logging.F("code", code), logging.F("reason", reason),
		logging.F("attempt", n), logging.F("duration_ms", took.Milliseconds()), logging.F("retry_after_ms", retry))
}
