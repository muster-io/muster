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
// dispatcher's: the Alert Groups by id (groups' LockGroups), then the Route's membership lock, then the Teams'
// membership locks by Team id, then the Route's active Storm, then their deliveries, then the Thread replies of a
// delivery. Enqueue takes the Route's membership lock before its deliveries and, when the Route delivers to the Teams,
// the membership lock of each Team of the Alert Group shared after it, and a Storm's row comes before the deliveries it
// changes. The dispatcher itself takes the Route's membership lock shared after its lock of the Alert Group and before
// it reads the reach of a transition that Alerts made, from which it records destination_joined (C-26.FR-5), then the
// Teams' locks shared when the Route delivers to the Teams, so that no change of the Route's or a Team's Destinations
// commits between that reach and the one Enqueue applies under the same locks, which it takes again. A re-render of
// several open Alert Groups (groups' rerenderIDs) and a batch of the evaluation of the Teams (evaluateTeams) take
// every Route's lock and then every Team's lock up front, each in id order, the Teams the evaluation brings included.
// A Team save takes the Team's lock exclusively after the Team's row and then writes only its entries and its
// open_group_updates rows, and the deletion of a Team takes it last, after the rows of the Destinations and the Routes
// it owns, which grouping holds FOR SHARE or locks before the Team's lock: neither waits for anything after it, so
// neither closes a cycle. A Route save, which holds its row and the Route's lock exclusively, takes the Teams' locks
// shared after them, as grouping does. The deletion of a Destination takes the Routes' membership locks and then the
// Teams', exclusively and in Team id order, before the deliveries it retires. Grouping takes the Teams' locks shared
// per Alert Group, so a Snapshot that changes Alert Groups of different Teams takes them out of id order across them,
// and can close a cycle with such a deletion: a deadlock that PostgreSQL detects. The deletion then runs again, up to
// 3 times (destinations' Delete), and a Snapshot chosen as the victim is retried once its lease runs out, as for the
// Routes' locks. Every Enqueue takes the Route's Storm right after the membership locks, before the first delivery it
// locks: the creation of an Alert
// Group joins or starts it (JoinStorm, StartStorm), any other rendering takes the active one FOR SHARE
// (LockActiveStorm), so that a calm check, which locks the Storm FOR NO KEY UPDATE and then its held deliveries, never
// holds the Storm while waiting for a delivery such a transaction locked. Only the creation of an Alert Group updates a
// Storm, and only grouping creates one. Grouping's order is: the Routes FOR SHARE, the counter row when it creates or
// reopens, the Alert Groups by id, then, before its first dispatch and in Route id order, the active Storm of every
// Route whose Alert Groups it changes or creates — FOR NO KEY UPDATE where it creates one (groups' LockRouteStorm), FOR
// SHARE elsewhere (ShareRouteStorm); then, per Alert Group, the Route's membership lock shared, the Storm, which it
// holds already in the mode Enqueue needs, and the deliveries. Taken lazily, per Alert Group, the Storms would close
// cycles. The counter row alone does not keep the update of a Storm out of one: a transaction that creates nothing
// takes no counter row, shares the Storm and may wait for a Storm summary that an earlier rendering of the creating
// transaction locked (EnsureStormSummary, for a held Alert Group that grew into a new Destination), while the creation
// would wait for that share to update the Storm. And Alert Groups in id order are not Routes in id order: a transaction
// that shares the Storm of one Route for its first Alert Group and the Storm of another for its second would meet a
// creating one that took them the other way round. Taken up front in one order, for update where they are updated, the
// Storms make every other transaction wait before it locks a delivery, and none upgrades a lock. A Route without an
// active Storm has no row to lock, and none to share: a Storm a creation starts is seen only once it commits, and only
// a creation, under the counter row, starts one. A Storm that another creation starts and commits after a transaction
// that creates nothing took its Storms is taken by that transaction's Enqueue FOR SHARE, late and out of Route id
// order: rare, and a cycle it closes is a deadlock PostgreSQL detects, after which the lane retries the Snapshot.
// Taking the Storms after the Routes closes no cycle with a change of a Route's Destinations, which takes the
// membership lock exclusively and then the Storm FOR SHARE. Where grouping updates the Storm, it holds the Route's row
// FOR SHARE, and each such change takes the row in a mode that conflicts with it before the membership lock — a Route
// update writes the row before its Destinations, the deletion of a Destination locks its Routes FOR NO KEY UPDATE
// (BumpRoutesOfDestination) before their membership locks — so it waits for grouping before it holds anything grouping
// waits for; where grouping only shares the Storm, the change's share does not wait for it, and grouping holds no
// delivery the change locks. A calm check takes the Storm, then deliveries only. A delivery is made held by a Storm
// only under a lock of that Storm that its calm check waits for: Enqueue's, or a change of the Route's Destinations,
// which takes the membership lock, then the active Storm FOR SHARE (LockActiveStorm), then the deliveries; otherwise a
// calm check could end the Storm before those deliveries commit, and they would stay held. The transactions of the
// worker follow it: the record of a Thread reply's outcome locks its delivery first (LockReplyDelivery), and the first
// Publication is marked through the dispatcher only after the outcome committed (markPublished), never inside it. A
// Publication a bridge replayed is no exception: its outcome and the edit that follows lock the delivery row only, as
// every outcome and every edit does, never its Alert Group before it; nor is a republication past a bridge's edit
// window (editwindow.go), whose preparation updates the delivery row only — its lease and its start, as a Publication's
// does — and whose outcome locks it only. The preparation of a Telegram reply that waits for its copy is the one
// exception: it holds the reply, then takes the post's copy lock and the delivery, the order of the learning of a copy
// and of the record of a Publication (copies.go). The re-render after a key rotation (rerender.go) takes the
// dispatcher's order — the Alert Group, SKIP LOCKED, then the Route's membership lock shared, then its deliveries by
// Destination — and never a Storm. A key's advisory lock (LockButtonKey) is taken only by the outcome that replaces the
// key of a Root message's buttons (released), after its delivery; after it, that outcome still updates the delivery's
// Thread (publishedThread), records delivery events (RecordEvent) and may lock its Destination's row to end the Broken
// state (markHealthy). No transaction locks a Destination before a key's lock, so none waits for the key while holding
// what the outcome waits for.
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
// short transaction, up to Concurrency attempts at once with a backlog (Round). It is woken by NOTIFY, by the earliest
// due time and, in development mode, by a move of the development clock.
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
	// TeamMentions are the Team mentions the replies of page_team steps mention; Pages settles the outcome of the
	// replies of Pages (pages.go). Without Pages no Page settles.
	TeamMentions mentions.TeamMentions
	Pages        PageSettler
	Log          *logging.Logger
	// MaxWait bounds the wait between rounds, Batch the rows one claim takes, and Concurrency the attempts a round makes
	// at once; zero is the default, and a round with the default Concurrency makes one attempt at a time until a
	// second of its claims comes back full (Round), while one set makes that many at once from the start.
	MaxWait     time.Duration
	Batch       int32
	Concurrency int
	// Wait waits for d or until a wake arrives; nil waits on the real clock.
	Wait func(ctx context.Context, d time.Duration, wake <-chan struct{})
	// Random is uniform in [0, 1) and jitters delivery.transient_backoff; nil is math/rand/v2.
	Random func() float64
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL, the base of the runbook_url of MusterDestinationBroken.
	RunbookBase string
	// PublicURL is MUSTER_PUBLIC_URL, the base of the link to an Alert Group in a final edit.
	PublicURL string
	// Published marks the first Publication of an Alert Group at a time, with whether it starts the Escalation chains
	// — false only for one that a change of configuration released — (*groups.Service).MarkPublished, once the
	// outcome that recorded it committed; nil marks nothing.
	Published func(ctx context.Context, groupID int64, at time.Time, startsChains bool) error

	once sync.Once
	wake chan struct{}
	// memos keeps the memo of each Organization's reconcile between rounds (refused).
	memoMu sync.Mutex
	memos  map[int64]*refused
	// pagesAfter is where the next round goes on settling the Pages of each Organization (settlePages): the id of the
	// last Page a round listed, none to start again from the oldest. It is this replica's own, and only Round, on the
	// worker's goroutine, reads and writes it.
	pagesAfter map[int64]int64
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

func (w *Worker) concurrency() int {
	if w.Concurrency <= 0 {
		return Concurrency
	}
	return w.Concurrency
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

// Round probes every Broken Destination whose probe is due, then attempts the due work of every Organization, claiming
// Batch at a time, in six ranks: 1, the Urgent deliveries that are not configuration only; 2, the Urgent configuration
// edits (ClaimDueDeliveries); 3, the other deliveries that are not configuration only; 4, the Thread replies; 5, the
// head events of outgoing webhooks; 6, last, the other configuration edits. Then it drops the Page replies of the
// Broken Destinations and settles their Pages (dropBrokenPages), and settles the Pages whose replies all ended
// (settlePages). It returns how long until the next one can be claimed, at most MaxWait, or at most MinWait when it
// settled as many batches of ended Pages as a round settles and more may wait (settlePages), so that a backlog of them
// drains a round after another. A row that fails is logged and attempted again once its lease runs out; the round goes
// on.
//
// They share the buckets of each Destination and Connection, and the order decides who gets a token (C-11.FR-3): a row
// denied one is due again at the token's time plus TokenMargin, as every row denied before that token is, and the first
// round after it claims them in this order. Configuration edits take every token the rest leaves, and none is dropped;
// Thread replies and webhook events come after the Root-message work of both urgencies due before them, and the Thread
// batching window keeps the replies of new Alerts to one per window and delivery.
//
// The memo. Once the limiter of a Destination or of its Connection refused a take, the later claims of the work that is
// not Urgent skip every Destination behind it, untouched — not leased, not rescheduled — for the rest of the round, and
// so do the rounds that start before the refused token's time plus TokenMargin, when the refused rows are due again:
// the memo outlives its round until then, the latest due time of the tokens each limiter refused, and a round reads the
// buckets of the limiters it carries again, by the current limits, so that a limit raised meanwhile ends the wait of
// the Urgent claims for its next token, while the later ranks still wait until the refused rows are due again; a failed
// read carries them as they were (refused, per Organization; another replica keeps its own, and the buckets in
// PostgreSQL stay the authority). The bounded claims of a visit of the Urgent ranks skip the limiters refused during
// the visit for the rest of it, and the others until their token is due; those of rank 2 skip a limiter that refused a
// row of rank 1 until its token's time plus TokenMargin, judged at the start of the step of the visit, as its drain is,
// so that the drain of a later step claims the refused rows first. Its batches of rank 1 due now skip a refused limiter
// only until its token is due, so that Urgent work that comes due meanwhile still gets the next token within one
// claimed batch once that token is due. So a spent limiter costs a round at most one claimed batch of refused takes of
// the work that is not Urgent, and of the Urgent work one per visit and one per batch due now that finds its token due,
// whatever its backlog in any rank, and none to a round that starts before its token is due; within one replica no row
// takes a token that a refused row of an earlier rank waits for, on a Destination or on its Connection, which C-11.FR-3
// asks. Within a rank the order is by due time: a refused row is due at its token's time plus TokenMargin, and work of
// its rank due before then can take that token. Across replicas the order is likely but not strict: one replica can
// take a token ahead of a row that another claimed, and each such race costs the row one token interval. When the only
// work due waits for refused limiters, the worker sleeps until the earliest refused token's time, when the batches of
// rank 1 due now can take it, then until that time plus TokenMargin (next), and a NOTIFY still wakes it at once.
//
// The round starts with a visit of the Urgent ranks: rank 1 is drained, then one batch of rank 1 due now is claimed,
// then one batch of rank 2; while that batch comes back full the visit repeats these three steps, and at its end, after
// a short one that claimed a row or after a full batch due now, it claims one more batch of rank 1 due now. Each drain
// of rank 1 claims only the rows due when it starts, and rank 2 only those due when the visit started, the round's
// start for the first; a batch due now is never repeated while full. Ranks 3, 4 and 5 then go in this order, each
// claimed one batch at a time until a claim comes back short, and only the rows due when the round started; after every
// batch of ranks 3 to 6 that claimed a row, a short one included, the round visits the Urgent ranks again, due by then.
// So Urgent work that comes due during the round goes within one claimed batch, behind the rows of its rank due before
// it: in the next batch of rank 1 due now of a visit, or in the next visit after a batch of a later rank, and behind a
// limiter that refused during the visit once its token is due; a visit leaves for the next visit, after
// the next batch of a later rank, or else for the next round, which the NOTIFY of its Enqueue starts at once, only the
// Urgent configuration edits that come due during it, the Urgent work that comes due during its last batch due now, and
// what comes due during its last drain beyond two batches. Rank 6 comes last, also only the rows due at the start, one
// batch at a time; between two of its batches the round claims one batch each of ranks 3, 4 and 5 due now, in this
// order and never repeated while full, so that a Publication, an edit that Alerts or a Command made, a Thread reply or
// a webhook event that comes due during a long pass of configuration edits goes within one batch of configuration edits
// per batch of its rank, behind the rows of its rank due before it.
//
// So a Thread reply or a webhook event due when a round starts waits for the Root-message work due before it on the
// Destinations whose limiters have tokens, each one batch at a time, plus, per limiter that is spent, at most one
// claimed batch of refused takes of the bounded claims and one per batch due now of the Urgent ranks that finds its
// token due; one that comes due during ranks 3 to 5 goes in the next round. Measured on the fake environment
// at 2 ms a take, with 1000-row backlogs of ranks 3, 4, 5 and 6 on one outgoing webhook at 5 a second and a Publication
// on another Destination injected at take 25 to 3500: a round makes 10 limiter takes in 20 ms and the Publication goes
// about 120 ms after it came due, against rounds of 8000 takes in 16 s and waits of 8 to 13 s without the memo, and
// against 388,892 takes in about 13 minutes for a round that drains ranks 3 to 5 on what is due now. With 1000 Urgent
// edits on one Mattermost Destination at 100 a second, whose limiter has a token again after almost every batch, the
// round makes about 170 takes in a third of a second, and the edits due in another Destination and the next
// Organization go after 130 to 165, against 4082 takes in 8.2 s, the edits after 1001 and 4070 takes and the next
// Organization after 4078, when the visits of the Urgent ranks claimed what is due now and skipped a refused
// Destination only until its token was due; at 5 a second both make about 23 takes. An Urgent Publication injected
// during such a round goes after 3 to 13 takes, against 852.
//
// Termination. Every claim of the round but the batches due now is bounded: those of ranks 3 to 6 by the start, those
// of rank 2 by the start of their visit, those of a drain of rank 1 by the start of the drain. A row that a bounded
// claim took leaves the claims of that bound: it is delivered or ended; or rescheduled — to its token's time plus
// TokenMargin, a backoff or a Retry-After — after the bound; or it keeps a lease past now; or its Destination became
// Broken or is skipped by the memo, its Root message is gone or a Storm holds it, which the claims skip. A row that
// comes due later, a Root message published again included, is due after the bound. So each row due at a bound is
// claimed at most once by the claims of that bound, unless its lease runs out meanwhile: ranks 3 to 6 end after at most
// the rows due at the start, divided by Batch and rounded up, plus those lease expiries, full batches, a visit after at
// most the rows of rank 2 due at its start, so divided, and a drain after at most the rows of rank 1 due at its start,
// so divided. The first visit's bounded claims claim each Urgent row due at the start at most once, unless it is moved
// to a time within the visit without a refusal — by TelegramPressEditDelay or a short Transient backoff — or its lease
// runs out: a later drain of a long visit then claims it again. The batches due now come one after each drain, so one
// per batch of rank 2 of a visit, and one more at its end, after a last batch of rank 2 that claimed a row or a full
// batch due now, and one of each of ranks 3, 4 and 5 between two batches of rank 6: those add at most 3 times Batch
// limiter takes, three claims and three visits per full batch of rank 6. A visit comes only at the start and after a
// batch of ranks 3 to 6 that claimed a row. The memo bounds the refused takes of a round by one claimed batch per
// limiter for the bounded claims of the work that is not Urgent, one per limiter and visit for the bounded Urgent
// claims, and one per limiter and batch of rank 1 due now that finds its token due: with a limiter that has a token
// again after about every batch, one per batch of rank 2 of the visit, a cost bounded by that work, which also gives
// the Destination its tokens. With 300 Urgent edits in a Destination at 100 a second and 200 Urgent configuration edits
// in another, that is about 140 refused takes in a round. Under a lasting backlog the worker sleeps until the earliest
// refused token's time, and a round woken earlier leaves the refused limiters alone; the round at the token takes it
// for work of rank 1 that is due, the round TokenMargin later makes about one claimed batch of takes there, one of
// which gets the token, about a dozen claims per Organization, and each claim reads past the skipped rows due before
// the others, about a millisecond per 3,000. Measured on the fake environment with 200 or 1000 edits waiting in a
// Destination at 10 calls a minute, as a Telegram chat allows: 9 refused takes per message sent, and one round per
// token for Urgent edits, two for the others, when nothing else wakes the worker, against 169 and 611 refused takes and
// a round every MinWait when the memo lasted one round and the skipped rows kept the worker awake.
//
// Attempts at once. A round makes up to Concurrency attempts at once (batchOnce). Its own goroutine claims and
// prepares the claimed rows one after another in the order of the claim, and only the preparation takes tokens and
// notes a refusal in the memo, so the ranks, the memo and the order of the tokens above are those of one attempt at a
// time, and so are the takes counted above. The call, the record of its outcome and what runs once that committed —
// the mark of a first Publication, the settling of a Page — go on in a goroutine of their own beside the preparation of
// the next rows; the next claim follows once every row of a batch was prepared, without waiting for their calls. Every
// claim and attempt holds one of Concurrency slots until it ends, and each holds at most one connection of the main
// pool at a time. A round of the default Concurrency starts narrow, with one slot: it makes one attempt at a time, and
// each claim waits for the attempt before it, until a second of its claims comes back full — a backlog, more due than
// two batches — and from then on, to its end, up to Concurrency at once (flight.narrow, backlogClaims). Without a
// backlog, attempts made at once only end each round sooner, and the next round, which a NOTIFY starts at once, claims
// fewer rows with the claims of every rank: measured on the L1 load test (one Mattermost Destination, 50 Snapshots a
// second), four at once from the start made 4.7 times the rounds and 4 times the claims of one at a time for the same
// attempts, read 38 % more buffers and made the processing of a Snapshot, which shares PostgreSQL with them, 6 %
// slower. A row waits, before its preparation, until no attempt of its Root message — the delivery's own or one of its
// Thread replies — or, for a webhook event, of its Alert Group in that outgoing webhook is in flight: the replies of
// one delivery go one after another, each with the `thread_ref` the one before it stored, an edit never overtakes
// another of its Root message, and a row whose lease ran out during a slow call is never attempted twice at once.
// Attempts of one round meet in PostgreSQL as those of several replicas do, in the lock order above; so the marks
// of the first Publications of one Alert Group in two Destinations may now run at once in one replica too, where the
// dispatcher's lock of the Alert Group lets the first to commit date it and the second change nothing (MarkPublished).
// The calls through one Connection, or to one Destination without one, take turns in the order of their preparations,
// and a RetryAfter answer — the pointer reply of a republication's included — holds the limiter it names from the moment
// it arrives until the end of its wait (gate): an attempt prepared before its record held the bucket makes no call when
// its turn comes, and the RetryAfter of the hold is its outcome, recorded as that answer, so that within one replica the
// next call to any Destination the answer covers waits exactly as asked (C-11.FR-8), as with one attempt at a time;
// across replicas it is likely but not strict, as the order of the tokens is. The end of a hold is measured once, when
// the answer arrives: the row that got it, unless it was a pointer reply, which is never retried, and the rows it held
// are due at that end plus TokenMargin, and their records hold the bucket until it, however late they came, so that
// they keep the order of their ranks (C-11.FR-3). A call sends the state its preparation read, as old as the wait for
// its turn: at most the calls of Concurrency − 1 attempts before it on its line. A turn that comes with less left of
// the lease than its call may take — callRequests requests within outbound.CallTimeout and one more, 40 s of the Lease
// — renews it first, guarded by it, and an attempt whose lease ran out makes no call and records nothing, as another
// replica may hold the row (renewLate). The events of outgoing webhooks take no turn: their own RetryAfter holds only
// the event, and they only look whether a request of their Destination's Root messages holds its limiter; so an event
// of a Destination in the mode both may be on the wire beside a Root request. A failure that makes a Destination Broken
// stops its claims, and the rows claimed before it are still prepared and called, as the rest of a claimed batch was
// with one attempt at a time. The reconcile of an Organization returns once every attempt it started ended, also when
// ctx ends, so that the probes, the Pages the round settles and its next due time never overlap them.
func (w *Worker) Round(ctx context.Context) (time.Duration, error) {
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list the organizations for delivery: %w", err)
	}
	next := w.maxWait()
	var errs []error
	for _, org := range orgs {
		errs = append(errs, w.probe(ctx, org), w.reconcile(ctx, org), w.dropBrokenPages(ctx, org))
		more, err := w.settlePages(ctx, org)
		errs = append(errs, err)
		if more {
			next = min(next, MinWait)
		}
		n, err := w.next(ctx, org, next)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		next = min(next, n)
	}
	return next, errors.Join(errs...)
}

// reconcile works the due deliveries, Thread replies and webhook events of the Organization in the order of Round, up
// to Concurrency attempts at once; it returns once every attempt it started ended.
func (w *Worker) reconcile(ctx context.Context, org int64) error {
	start := w.Lease.Clocks.Business.Now()
	memo, memoErr := w.memoOf(ctx, org, start)
	defer w.keepMemo(org, memo)
	ctx = context.WithValue(ctx, refusedKey{}, memo)
	f := newFlight(w.concurrency())
	if w.Concurrency <= 0 {
		f.narrow()
	}
	defer f.wait()
	ctx = withGate(ctx, f.gate)
	type pass struct {
		claim claimFunc
		work  workFunc
	}
	// Ranks 3 to 6, due by the start.
	passes := []pass{{w.claimDeliveries(false, false, start, start), w.startDelivery},
		{w.claimReplies(start), w.startReply}, {w.claimEvents(start), w.startEvent},
		{w.claimDeliveries(false, true, start, start), w.startDelivery}}
	// Ranks 3, 4 and 5 again, due now: one batch of each between two batches of rank 6.
	live := []pass{{w.claimDeliveries(false, false, time.Time{}, time.Time{}), w.startDelivery},
		{w.claimReplies(time.Time{}), w.startReply}, {w.claimEvents(time.Time{}), w.startEvent}}
	// The first error of each rank, rank 1's at 0, and the memo's: the loops may claim a rank many times.
	errs := make([]error, 2+len(passes), 3+len(passes))
	errs = append(errs, memoErr)
	keep := func(i int, err error) {
		if errs[i] == nil {
			errs[i] = err
		}
	}
	// now1 claims one batch of rank 1 due now, which skips a refused limiter only until its token is due, and says
	// whether it was full.
	now1 := func() bool {
		n, err := w.batchOnce(ctx, f, org, w.claimDeliveries(true, false, time.Time{}, time.Time{}), w.startDelivery)
		keep(0, err)
		return n == int(w.batch())
	}
	// urgent visits the Urgent ranks: rank 1 drained, due by the start of each drain, then one batch of rank 1 due
	// now, then one batch of rank 2 due by until, again while that comes back full; after a short batch of rank 2 that
	// claimed a row, or after a full batch due now, one more batch of rank 1 due now, never repeated while full: with a
	// limiter that has a token again after every batch it would stay full. The bounded claims skip the limiters that
	// refused a take during the visit, and those refused earlier whose token is not due yet at the start of the step,
	// at, rank 2 one that refused a row of rank 1 until its token's time plus TokenMargin; the batches due now skip a
	// refused limiter only until its token is due (refusedUrgent).
	urgent := func(until time.Time) {
		visitRefused(ctx)
		for {
			at := w.Lease.Clocks.Business.Now()
			keep(0, w.drain(ctx, f, org, w.claimDeliveries(true, false, at, at), w.startDelivery))
			full := now1()
			n, err := w.batchOnce(ctx, f, org, w.claimDeliveries(true, true, until, at), w.startDelivery)
			keep(1, err)
			if n < int(w.batch()) {
				if n > 0 || full {
					now1()
				}
				return
			}
		}
	}
	// once claims one batch of p, rank i+1 of ranks 3 to 6, and visits the Urgent ranks again, due by now, when it
	// claimed a row; it says whether the batch was full.
	once := func(i int, p pass) bool {
		n, err := w.batchOnce(ctx, f, org, p.claim, p.work)
		keep(i, err)
		if n > 0 {
			urgent(w.Lease.Clocks.Business.Now())
		}
		return n == int(w.batch())
	}
	// untilShort claims rank i+1 of ranks 3 to 5, one batch at a time, until a claim comes back short.
	untilShort := func(i int) {
		for full := true; full; {
			full = once(i, passes[i-2])
		}
	}
	urgent(start)
	for i := 2; i < 5; i++ {
		untilShort(i)
	}
	for between := false; ; between = true {
		if between {
			for i, p := range live {
				once(2+i, p)
			}
		}
		if !once(5, passes[3]) {
			return errors.Join(errs...)
		}
	}
}

// refused is the memo of the reconcile of one Organization: the limiters — of a Destination or of a Connection — that
// refused a take, in the order they were added, with the latest due time of the tokens it refused (until), when its
// next token is due by the current limits (token, until itself unless a limit raised since brought it forward), the
// number of its latest refusal and whether one refused a row of rank 1. The rows it refused are due again
// at until plus TokenMargin. The claims of the work that is not Urgent skip their Destinations, untouched, for the rest
// of the round and, carried, until then. The bounded Urgent claims of a visit of the Urgent ranks skip those refused
// during the visit and, until their token is due, the others; those of rank 2 skip a limiter that refused a row of
// rank 1 until until plus TokenMargin, judged at the start of the step of the visit, as the drain before them is. The
// batches of rank 1 due now skip them only until their token is due, so that Urgent work still gets the next token
// within one claimed batch of it being due. A limiter whose until plus TokenMargin is still to come when a round starts
// stays in the memo for that round, as refused before its first visit, its token read again first (memoOf). It lives
// in the context of the reconcile and on its Worker between rounds; the buckets in PostgreSQL stay the authority, and
// another replica keeps its own memo.
type refused struct {
	until map[limiterKey]time.Time
	token map[limiterKey]time.Time
	keys  []limiterKey
	// seq numbers the refusals, last is each limiter's latest, and visit the last one before the current visit of the
	// Urgent ranks.
	seq   int
	last  map[limiterKey]int
	visit int
	rank1 map[limiterKey]bool
}

// newRefused is an empty memo.
func newRefused() *refused {
	return &refused{until: map[limiterKey]time.Time{}, token: map[limiterKey]time.Time{}, last: map[limiterKey]int{},
		rank1: map[limiterKey]bool{}}
}

// limiterKey names the limiter of a Destination, or of a Connection.
type limiterKey struct {
	connection bool
	id         int64
}

type refusedKey struct{}

// memoOf is the memo of the Organization org for a round that starts at start: the limiters of its earlier rounds
// whose until plus TokenMargin is after start, as refused before the round's first visit. Their buckets are read again
// first, by the current limits (LimiterDues): a limiter without a bucket is dropped, and one whose token is now due
// sooner, its limit raised for one, has its token at that time, at once when it is due already, for the Urgent
// claims; until
// stays, as the rows it refused are due again only then, and the work that is not Urgent, new work after a raise
// included, waits for them as before the raise. When the read fails, the limiters are carried as they were. A change
// of a Destination or a Connection wakes the worker (internal/runtime), so that a raised limit is read at once. The
// reconcile gives the memo back with keepMemo.
func (w *Worker) memoOf(ctx context.Context, org int64, start time.Time) (*refused, error) {
	w.memoMu.Lock()
	old := w.memos[org]
	delete(w.memos, org)
	w.memoMu.Unlock()
	r := newRefused()
	if old == nil || len(old.keys) == 0 {
		return r, nil
	}
	carry := func(k limiterKey, token time.Time) {
		if old.until[k].Add(TokenMargin).After(start) {
			r.until[k], r.token[k], r.rank1[k] = old.until[k], token, old.rank1[k]
			r.keys = append(r.keys, k)
		}
	}
	var dests, conns []int64
	for _, k := range old.keys {
		dests, conns = r.add(k, dests, conns)
	}
	rows, err := w.Store.q().LimiterDues(ctx, dbgen.LimiterDuesParams{OrgID: org, Now: start.UTC(),
		DestinationIds: dests, ConnectionIds: conns})
	if err != nil {
		for _, k := range old.keys {
			carry(k, old.token[k])
		}
		return r, fmt.Errorf("read the limiters of the memo: %w", err)
	}
	fresh := map[limiterKey]time.Time{}
	for _, row := range rows {
		fresh[limiterKey{connection: row.Kind == string(ScopeConnection), id: row.ID}] = row.Due.UTC()
	}
	for _, k := range old.keys {
		f, ok := fresh[k]
		if !ok {
			continue
		}
		token := old.token[k]
		if f.Before(token) {
			token = f
		}
		carry(k, token)
	}
	return r, nil
}

// keepMemo keeps the memo r of the Organization org for its next round.
func (w *Worker) keepMemo(org int64, r *refused) {
	w.memoMu.Lock()
	defer w.memoMu.Unlock()
	if w.memos == nil {
		w.memos = map[int64]*refused{}
	}
	w.memos[org] = r
}

// waiting is the limiters in the kept memo of the Organization org whose until plus TokenMargin is after now, whose
// work the claims leave alone until then, with the earliest of the times at which one of them can be claimed again
// that are after now: its token's time, when the batches of rank 1 due now take it, and its until plus TokenMargin,
// when the rows it refused are due again. After a raise moved its token into the past, an Urgent row of rank 1 behind
// it, or of rank 2 when no row of rank 1 was refused, that comes due without a NOTIFY, after a Transient backoff or
// TelegramPressEditDelay, waits for its until plus TokenMargin or MaxWait, whichever comes first, or for any earlier
// round.
func (w *Worker) waiting(org int64, now time.Time) (dests, conns []int64, wake time.Time) {
	w.memoMu.Lock()
	defer w.memoMu.Unlock()
	r := w.memos[org]
	if r == nil {
		return nil, nil, time.Time{}
	}
	for _, k := range r.keys {
		if !r.until[k].Add(TokenMargin).After(now) {
			continue
		}
		dests, conns = r.add(k, dests, conns)
		for _, at := range []time.Time{r.token[k], r.until[k].Add(TokenMargin)} {
			if at.After(now) && (wake.IsZero() || at.Before(wake)) {
				wake = at
			}
		}
	}
	return dests, conns, wake
}

// add appends the limiter k to the Destinations or the Connections.
func (r *refused) add(k limiterKey, dests, conns []int64) ([]int64, []int64) {
	if k.connection {
		return dests, append(conns, k.id)
	}
	return append(dests, k.id), conns
}

// noteRefused adds the limiters of d that refused the take t of a row, of rank 1 or not, to the memo of the reconcile
// of ctx — d's own, its Connection's or both, each with the time its token is due; without a memo it does nothing. A
// refusal of the Connection also records d itself, at the time the whole take is due — the later of its two buckets'
// times, when its refused rows are due again — so that they still hold its later ranks when it moves to another
// Connection; the read of its own bucket frees its Urgent claims (memoOf).
func noteRefused(ctx context.Context, d Destination, t taken, rank1 bool) {
	r, ok := ctx.Value(refusedKey{}).(*refused)
	if !ok {
		return
	}
	note := func(k limiterKey, due time.Time) {
		if _, seen := r.until[k]; !seen {
			r.keys = append(r.keys, k)
		}
		if due.After(r.until[k]) {
			r.until[k] = due
		}
		if due.After(r.token[k]) {
			r.token[k] = due
		}
		r.seq++
		r.last[k] = r.seq
		r.rank1[k] = r.rank1[k] || rank1
	}
	if t.connection.IsZero() || d.Connection == nil {
		note(limiterKey{id: d.ID}, cmp.Or(t.destination, t.due))
		return
	}
	note(limiterKey{id: d.ID}, t.due)
	note(limiterKey{connection: true, id: *d.Connection}, t.connection)
}

// visitRefused starts a visit of the Urgent ranks in the memo of the reconcile of ctx.
func visitRefused(ctx context.Context) {
	if r, ok := ctx.Value(refusedKey{}).(*refused); ok {
		r.visit = r.seq
	}
}

// refusedOf is the Destinations and the Connections of the memo of the reconcile of ctx, none without one.
func refusedOf(ctx context.Context) (dests, conns []int64) {
	r, ok := ctx.Value(refusedKey{}).(*refused)
	if !ok {
		return nil, nil
	}
	for _, k := range r.keys {
		dests, conns = r.add(k, dests, conns)
	}
	return dests, conns
}

// refusedUrgent is the Destinations and the Connections of the memo of the reconcile of ctx that an Urgent claim skips
// at at: those whose token is not due yet; for a claim bounded by the start of a drain or a visit, also those refused
// during the current visit of the Urgent ranks, and for one of rank 2 (configurationOnly) those that refused a row of
// rank 1 until their until plus TokenMargin, when those rows are due again.
func refusedUrgent(ctx context.Context, at time.Time, bounded, configurationOnly bool) (dests, conns []int64) {
	r, ok := ctx.Value(refusedKey{}).(*refused)
	if !ok {
		return nil, nil
	}
	for _, k := range r.keys {
		due := r.token[k]
		if configurationOnly && r.rank1[k] {
			due = r.until[k].Add(TokenMargin)
		}
		if bounded && r.last[k] > r.visit || due.After(at) {
			dests, conns = r.add(k, dests, conns)
		}
	}
	return dests, conns
}

// claimDeliveries is the claim of the due deliveries of an Organization of one rank — Urgent or not, configuration
// edits or the rest — in the order of ClaimDueDeliveries: the earliest first, then the one delivered longest ago. A
// non-zero until claims only the deliveries due by then; an Urgent claim skips the limiters of the memo as they stand
// at at, the start of its step of the visit, or when it claims for a zero at. A delivery's attempt is keyed by its Root
// message.
func (w *Worker) claimDeliveries(urgent, configurationOnly bool, until, at time.Time) claimFunc {
	return func(ctx context.Context, org int64) ([]claimedRow, error) {
		dests, conns := refusedOf(ctx)
		if urgent {
			dests, conns = refusedUrgent(ctx, cmp.Or(at, w.Lease.Clocks.Business.Now()), !until.IsZero(),
				configurationOnly)
		}
		rows, err := db.Claim(ctx, w.Store.begin, w.Lease, w.batch(),
			func(ctx context.Context, tx pgx.Tx, p db.ClaimParams) ([]dbgen.ClaimDueDeliveriesRow, error) {
				return w.Store.queries(tx).ClaimDueDeliveries(ctx, dbgen.ClaimDueDeliveriesParams{OrgID: org,
					Owner: p.Owner, LeaseUntil: p.LeaseUntil, Due: dueBy(p.Due, until), Now: p.Now, Urgent: urgent,
					ConfigurationOnly: configurationOnly, SkipDestinations: dests, SkipConnections: conns,
					Lim: p.Limit})
			})
		if err != nil {
			return nil, fmt.Errorf("claim the due deliveries: %w", err)
		}
		slices.SortFunc(rows, func(a, b dbgen.ClaimDueDeliveriesRow) int {
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
		out := make([]claimedRow, len(rows))
		for i, r := range rows {
			out[i] = claimedRow{id: r.ID, key: attemptKey{id: r.ID}}
		}
		return out, nil
	}
}

// dueBy is the due bound of a claim: due, the business time now, or until when it is set and earlier.
func dueBy(due, until time.Time) time.Time {
	if !until.IsZero() && until.Before(due) {
		return until
	}
	return due
}

// next is how long until a delivery, a Thread reply, a head event of an outgoing webhook or a Broken probe of the
// Organization can be claimed, at most limit (NextDeliveryWork): the work of the limiters the memo still waits for
// counts only from their token's time plus TokenMargin.
func (w *Worker) next(ctx context.Context, org int64, limit time.Duration) (time.Duration, error) {
	realNow := w.Lease.Clocks.Real.Now().UTC()
	now := w.Lease.Clocks.Business.Now()
	dests, conns, wake := w.waiting(org, now)
	r, err := w.Store.q().NextDeliveryWork(ctx, dbgen.NextDeliveryWorkParams{OrgID: org, Now: realNow,
		SkipDestinations: dests, SkipConnections: conns})
	if err != nil {
		return 0, fmt.Errorf("read when the next delivery is due: %w", err)
	}
	d := limit
	if !wake.IsZero() {
		d = min(d, wake.Sub(now))
	}
	if r.FreeAt.Year() > 1 {
		d = min(d, r.FreeAt.Sub(now))
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
	// resolved are the Mentions of a Loud Publication: its targets, and the people its Mention entries named, which
	// its delivery records (C-28.FR-3).
	resolved    mentions.Result
	idempotent  bool
	started     bool
	republish   bool
	finalReply  bool
	needsWindow bool
	pointer     *Outcome
	// leaseUntil is the end of the lease its preparation renewed (real clock), and turn its place on the line of its
	// Connection in the round (gate).
	leaseUntil time.Time
	turn       ticket
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

// deliver attempts one claimed delivery from start to end, as a probe does (startDelivery).
func (w *Worker) deliver(ctx context.Context, org, id int64) {
	if finish := w.startDelivery(ctx, org, id); finish != nil {
		finish(ctx)
	}
}

// startDelivery prepares one claimed delivery and returns the rest of its attempt, its call and the record of its
// outcome, nil when there is nothing to call; a failure is logged and the row is attempted again once its lease runs
// out. A delivery whose rule needs its bridge's edit window is prepared again once the window was read.
func (w *Worker) startDelivery(ctx context.Context, org, id int64) func(ctx context.Context) {
	a, ok, logs, err := w.prepare(ctx, org, id, nil)
	if err == nil && a.needsWindow {
		a, ok, logs, err = w.prepare(ctx, org, id, w.editWindow(ctx, a.destination))
	}
	if err != nil {
		w.deliveryFailed(ctx, err)
		return nil
	}
	logs.run(ctx, w.Log)
	if !ok {
		return nil
	}
	a.turn = gateOf(ctx).reserve(a.destination)
	return func(ctx context.Context) { w.deliveryFailed(ctx, w.call(ctx, org, a)) }
}

// deliveryFailed logs err, the failure of an attempt of a delivery, unless it is nil or ctx ended.
func (w *Worker) deliveryFailed(ctx context.Context, err error) {
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
		if row.DestinationDeleted && !row.DesiredRetire {
			// Its Destination was deleted before its Root message was ever published there, after the deletion ended
			// the others: it gets the final edit, which withholds it, or edits a Publication that may exist (C-11.FR-14).
			if err := q.RetireLeased(ctx, dbgen.RetireLeasedParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				Now: now}); err != nil {
				return fmt.Errorf("give the delivery of a deleted destination its final edit: %w", err)
			}
			row.DesiredRetire = true
		}
		if row.DesiredRetire && unpublished(row) && !mayExist {
			// Its Destination left before its Root message was ever published there: it is withheld without a call.
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
			noteRefused(ctx, d, t, row.Urgent && !row.ConfigurationOnly)
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
		a.leaseUntil = realNow.Add(w.Lease.Duration)
		if err := q.RenewDeliveryLease(ctx, dbgen.RenewDeliveryLeaseParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			LeaseUntil: a.leaseUntil}); err != nil {
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
			if a.resolved, err = w.targets(ctx, tx, org, d, row.AlertGroupID, row.StormID.Int64, 0, loud,
				ms); err != nil {
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
// from `created` or a recovery finds its Alert Group firing, and with storm_started when it is a Storm summary
// (C-28.FR-7); Quiet otherwise — in a Destination
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
// the old Root message; a final edit past the window is a Thread reply with its own key. In a round the call waits for
// its turn on the line of its Connection, and is not made while a RetryAfter holds its limiter (gate): the RetryAfter
// of the hold is then its outcome, recorded as that answer, neither counted nor logged as an attempt. The call sends the
// state its preparation read, which is as old as the wait for its turn; a change made meanwhile goes in the next call,
// prepared once this attempt ended (C-11.FR-1). A turn that came late renews the lease first (renewLate).
func (w *Worker) call(ctx context.Context, org int64, a attempt) error {
	ctx = outbound.WithLinks(ctx, linksOf(a.destination, a.group()))
	c := Call{Class: outbound.ClassDelivery, Destination: a.destination, Loudness: groups.Quiet}
	kind := a.kind()
	if a.publication && !a.row.DesiredRetire {
		c.Loudness, c.Mentions = publicationLoudness(a.row.PublicationLoud, a.row.StormID.Valid)
		c.Targets = a.resolved.Targets
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
	g := gateOf(ctx)
	held, leave, err := g.enter(ctx, a.turn, a.destination, w.Lease.Clocks.Business.Now)
	if err != nil {
		return err
	}
	if held == nil {
		lost, err := w.renewLate(a.leaseUntil, func(now, until time.Time) (int64, error) {
			return w.Store.q().RenewHeldDeliveryLease(ctx, dbgen.RenewHeldDeliveryLeaseParams{OrgID: org,
				ID: a.row.ID, Owner: w.Lease.Owner, Now: now, LeaseUntil: until})
		})
		if lost || err != nil {
			leave()
			return err
		}
	}
	start := w.Lease.Clocks.Real.Now()
	var out Outcome
	var rejected *Outcome
	if held != nil {
		out = *held
	} else {
		out = w.send(ctx, a, c)
		if out.Kind == OutcomeMarkupRejected {
			first := out
			rejected = &first
			w.attempted(ctx, a.destination, a.row.Number, kind, out, a.row.Attempts+1,
				w.Lease.Clocks.Real.Now().Sub(start))
			c.Plain = true
			out = plain(func() Outcome { return w.send(ctx, a, c) })
		}
		out.heldUntil = g.answered(a.destination, out, w.Lease.Clocks.Business.Now())
	}
	took := w.Lease.Clocks.Real.Now().Sub(start)
	if a.republish && out.Kind == OutcomeOK {
		p := w.pointer(ctx, a, out)
		p.heldUntil = g.answered(a.destination, p, w.Lease.Clocks.Business.Now())
		a.pointer = &p
	}
	leave()
	recorded := false
	var logs after
	err = w.Store.inTx(ctx, func(q queries) error {
		logs = nil
		var err error
		recorded, err = w.record(ctx, q, org, a, c, out, rejected, &logs)
		return err
	})
	if held == nil {
		w.attempted(ctx, a.destination, a.row.Number, kind, out, a.row.Attempts+1, took)
	}
	if err == nil {
		logs.run(ctx, w.Log)
	}
	if err == nil && recorded && out.Kind == OutcomeOK && !a.replayed(out) && a.row.DesiredReceivedAt.Valid {
		metrics.DeliveryLatency.With(a.destination.PublicID).Update(
			max(a.at.Sub(a.row.DesiredReceivedAt.Time).Seconds(), 0))
	}
	return err
}

// The budget of a call at its turn: each request of an adapter's client takes at most outbound.CallTimeout, and an
// attempt makes at most callRequests of them — its call, the same again without markup or without its Thread's reply
// link, and a republication's pointer reply. A turn renews the lease when less than turnBudget is left: those requests
// and one more as a margin, 40 s, two thirds of the 60 s Lease.
const (
	callRequests = 3
	turnBudget   = (callRequests + 1) * outbound.CallTimeout
)

// renewLate renews, with renew, the lease of an attempt whose preparation renewed it until leaseUntil when its turn came
// with less than turnBudget left on the real clock — the wait behind the calls before it on its line was long — so that
// its call, at most callRequests requests, is made under the lease. The statement is guarded by the lease: lost says
// that it ran out meanwhile, so that another replica may hold the row and send a newer state, and the call is then not
// made and nothing is recorded, as when a replica loses a lease during its call. A turn that came early renews nothing.
func (w *Worker) renewLate(leaseUntil time.Time, renew func(now, until time.Time) (int64, error)) (lost bool,
	err error) {
	now := w.Lease.Clocks.Real.Now().UTC()
	if leaseUntil.Sub(now) >= turnBudget {
		return false, nil
	}
	n, err := renew(now, now.Add(w.Lease.Duration))
	if err != nil {
		return false, fmt.Errorf("renew the lease at the turn of the call: %w", err)
	}
	return n == 0, nil
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
