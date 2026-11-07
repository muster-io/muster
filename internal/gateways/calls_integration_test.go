// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package gateways

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// keyedGateway is a Notification gateway that keeps idempotency keys, as the fake bridge does: a repeated key gets
// the first answer, replayed, and calls nobody again. fail answers the next calls with these outcomes first.
type keyedGateway struct {
	mu    sync.Mutex
	calls map[string]int
	texts map[string]string
	fail  []delivery.Outcome
	sent  []string
}

func (g *keyedGateway) Notify(_ context.Context, c delivery.Call, n delivery.Notification) delivery.Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.calls == nil {
		g.calls, g.texts = map[string]int{}, map[string]string{}
	}
	if len(g.fail) > 0 {
		o := g.fail[0]
		g.fail = g.fail[1:]
		return o
	}
	g.calls[c.Key]++
	if g.calls[c.Key] > 1 {
		return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n-" + c.Key, Replayed: true}
	}
	g.texts[c.Key] = n.Text
	g.sent = append(g.sent, n.Contact+" "+n.Text)
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n-" + c.Key}
}

func (g *keyedGateway) sentTexts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.sent)
}

// callEnv is a migrated database with the Organization, the Timeline partitions of November and December 2026, the
// Notification gateway CN0000000000G1, a Route, and n Users p1…pn, each with a confirmed contact at the gateway; the
// queue and a sender over it, with the dispatcher of groups recording page_not_delivered.
type callEnv struct {
	d        *db.DB
	orgID    int64
	gw       int64
	route    int64
	users    []int64
	business *clock.Manual
	real     *clock.Manual
	calls    *Calls
	groups   *groups.Service
	gateway  *keyedGateway
	log      *bytes.Buffer
	number   int64
}

func setupCalls(t *testing.T, s dbtest.Server, n int) *callEnv {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	buf := &bytes.Buffer{}
	logger := logging.New(&syncWriter{w: buf}, logging.LevelInfo)
	if err := d.Migrate(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, c0); err != nil {
		t.Fatal(err)
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := &callEnv{d: d, orgID: org.ID, business: clock.NewManual(c0), real: clock.NewManual(time.Now()),
		gateway: &keyedGateway{}, log: buf}
	e.exec(t, `CREATE TABLE timeline_entries_p202611 PARTITION OF timeline_entries
		FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')`)
	e.exec(t, `CREATE TABLE timeline_entries_p202612 PARTITION OF timeline_entries
		FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00')`)
	e.gw = e.id(t, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url, bridge_event_delivery,
		bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, bridge_info,
		created_at, updated_at)
		VALUES ($1, 'CN0000000000G1', 'bridge', 'Example Gateway', 'http://gw', 'poll', '\x00', 'k1', $2, 1000, 1,
			'{"roles": ["gateway"]}', $2, $2) RETURNING id`, e.orgID, c0)
	e.id(t, `INSERT INTO integrations (org_id, public_id, name, connection_mode, static_labels,
		duplicate_window_seconds, heartbeat_enabled, heartbeat_timeout_seconds, heartbeat_state, created_at, updated_at)
		VALUES ($1, 'NT0000000000C1', 'am', 'webhook_only', '{}', 45, false, 300, 'not_configured', $2, $2)
		RETURNING id`, e.orgID, c0)
	e.route = e.id(t, `INSERT INTO routes (org_id, public_id, name, position, urgent, group_key, reopen_window_seconds,
		grace_period_seconds, urgent_rise_removes_ack, snooze_durations_seconds, thread_batching_window_seconds,
		storm_threshold, language, ack_timeout_enabled, ack_timeout_first_interval_seconds, reminders_enabled,
		reminders_first_interval_seconds, reminders_cap_seconds, auto_unacknowledge, created_at, updated_at)
		VALUES ($1, 'RT0000000000C1', 'r', 1, false, '{alertname}', 0, 0, true, '{}', 0, 10, 'en', false, 60, false,
			60, 60, false, $2, $2) RETURNING id`, e.orgID, c0)
	for i := range n {
		login := fmt.Sprintf("p%d", i+1)
		uid := e.id(t, `INSERT INTO users (org_id, public_id, login, name, role, source, status, created_at,
			updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', 'active', $4, $4) RETURNING id`, e.orgID,
			fmt.Sprintf("SR00000000%04d", i+1), login, c0)
		e.id(t, `INSERT INTO gateway_contacts (org_id, public_id, user_id, connection_id, contact, confirmed_at,
			created_at) VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id`, e.orgID, fmt.Sprintf("GC00000000%04d", i+1),
			uid, e.gw, fmt.Sprintf("+1000000%04d", i+1), c0)
		e.users = append(e.users, uid)
	}
	e.groups = groups.New(groups.Config{OrgID: e.orgID, Store: groups.NewStore(d.Pool),
		Audit: audit.NewWriter(logger, e.business), Business: e.business, Log: logger})
	e.calls = NewCalls(e.orgID, e.business, logger)
	return e
}

// syncWriter serializes the writes of the senders of several goroutines into one buffer.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (e *callEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.d.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *callEnv) id(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func (e *callEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// group creates a firing Alert Group titled title of severity.
func (e *callEnv) group(t *testing.T, title, severity string) int64 {
	t.Helper()
	e.number++
	return e.id(t, `INSERT INTO alert_groups (org_id, public_id, number, route_id, group_key_labels, group_key_values,
		group_key_sha256, title, common_labels, common_annotations, integration_ids, status, severity_level, urgent,
		created_at, last_changed_at)
		SELECT $1, $2, $3, $4, '{alertname}', '{}', sha256(convert_to($2, 'UTF8')), $5, '{}', '{}',
			ARRAY[(SELECT id FROM integrations)],
			'firing', $6, false, $7, $7
		RETURNING id`, e.orgID, fmt.Sprintf("AG00000000%04d", e.number), e.number, e.route, title, severity, c0)
}

// ack makes the Alert Group acknowledged by the first User directly, as Acknowledge does.
func (e *callEnv) ack(t *testing.T, group int64) {
	t.Helper()
	e.exec(t, `UPDATE alert_groups SET status = 'acknowledged', owner_user_id = $2, acknowledged_at = $3 WHERE id = $1`,
		group, e.users[0], e.business.Now())
}

// page queues a Page of the Alert Group to the User through the gateway in a transaction of its own.
func (e *callEnv) page(t *testing.T, user, group int64) {
	t.Helper()
	var log func(context.Context)
	if err := pgx.BeginFunc(t.Context(), e.d.Pool, func(tx pgx.Tx) error {
		var err error
		log, err = e.calls.Page(t.Context(), tx, PageCall{UserID: user, GatewayID: e.gw, AlertGroupID: group,
			Stop: StopNotFiring})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	log(t.Context())
}

// sender is a gateway sender of the replica owner over the database.
func (e *callEnv) sender(owner string, g delivery.Gateway) *Sender {
	return &Sender{OrgID: e.orgID, Store: NewCallStore(e.d.Pool), Lease: db.Lease{Owner: owner,
		Duration: SenderLease, Clocks: clock.Clocks{Business: e.business, Real: e.real}}, Gateway: g, Keys: signKeys{},
		Pages: e.groups, Log: logging.New(&syncWriter{w: e.log}, logging.LevelInfo),
		Random: func() float64 { return 0 }}
}

func (e *callEnv) round(t *testing.T, s *Sender) {
	t.Helper()
	if _, err := s.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestIntegrationCalls is C-30.AC-3, AC-7 and FR-4, FR-7 on PostgreSQL, through the queries of query.sql, the delivery
// limiter and the dispatcher: the first call goes at once; two Pages within the window wait as one call, and the one
// acknowledged meanwhile is dropped from it before it goes; a call whose Alert Groups were all acknowledged ends
// dropped unsent; calls about critical Alert Groups go first; a call refused as invalid ends Not delivered, records
// the Quiet page_not_delivered of the kind escalation naming the person and the gateway with call_refused, which the
// Timeline reads, and the next call goes once the window after it has passed. The log carries no contact.
func TestIntegrationCalls(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		e := setupCalls(t, srv, 2)
		s := e.sender("r1", e.gateway)
		p1, p2 := e.users[0], e.users[1]
		g1, g2, g3 := e.group(t, "Disk1", "warning"), e.group(t, "Disk2", "warning"), e.group(t, "Disk3", "warning")
		e.page(t, p1, g1)
		e.round(t, s)
		if got := e.gateway.sentTexts(); len(got) != 1 ||
			got[0] != "+10000000001 Muster: 1 Alert Group needs you. #1 Disk1, warning. Press 1 to acknowledge." {
			t.Fatalf("the first call %q", got)
		}
		e.business.Advance(10 * time.Second)
		e.page(t, p1, g2)
		e.page(t, p1, g3)
		e.round(t, s)
		if n := len(e.gateway.sentTexts()); n != 1 {
			t.Fatalf("%d calls within the window", n)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'waiting' AND not_before = $1`,
			c0.Add(CombineWindow)); n != 1 {
			t.Errorf("%d calls wait for the end of the window", n)
		}
		// An ended call keeps no text, which names its Alert Groups, and keeps its language.
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'accepted' AND text IS NULL
			AND language = 'en' AND first_attempt_at IS NOT NULL`); n != 1 {
			t.Errorf("%d accepted calls without their text", n)
		}
		e.ack(t, g2)
		e.business.Advance(51 * time.Second)
		e.round(t, s)
		if got := e.gateway.sentTexts(); len(got) != 2 ||
			got[1] != "+10000000001 Muster: 1 Alert Group needs you. #3 Disk3, warning. Press 1 to acknowledge." {
			t.Fatalf("the second call %q", got)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_call_items WHERE alert_group_id = $1 AND dropped_at IS NOT NULL`,
			g2); n != 1 {
			t.Errorf("the acknowledged Alert Group was not dropped: %d", n)
		}

		// Dropped while it waited for a limiter token, then Paged again: queued again, and the call is about it.
		e.exec(t, `UPDATE gateway_call_items SET dropped_at = $2 WHERE call_id = (SELECT max(id) FROM gateway_calls)
			AND alert_group_id = $1`, g3, e.business.Now())
		e.exec(t, `UPDATE gateway_calls SET state = 'waiting', first_attempt_at = NULL, text = NULL, language = NULL,
			contact_id = NULL, ended_at = NULL, notification_id = NULL, next_attempt_at = NULL
			WHERE id = (SELECT max(id) FROM gateway_calls)`)
		e.page(t, p1, g3)
		if n := e.count(t, `SELECT count(*) FROM gateway_call_items WHERE alert_group_id = $1 AND dropped_at IS NULL
			AND call_id = (SELECT max(id) FROM gateway_calls)`, g3); n != 1 {
			t.Error("the Alert Group Paged again after its drop is not queued again")
		}
		e.exec(t, `UPDATE gateway_calls SET state = 'accepted', first_attempt_at = $1, language = 'en',
			ended_at = $1 WHERE id = (SELECT max(id) FROM gateway_calls)`, e.business.Now())

		// All acknowledged before the first attempt: dropped, unsent.
		g4 := e.group(t, "Disk4", "warning")
		e.page(t, p2, g4)
		e.ack(t, g4)
		e.round(t, s)
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'dropped'`, p2); n != 1 ||
			len(e.gateway.sentTexts()) != 2 {
			t.Errorf("%d dropped, %d sent", n, len(e.gateway.sentTexts()))
		}

		// Critical first: both are due; the critical one goes first.
		e.business.Advance(2 * CombineWindow)
		g5, g6 := e.group(t, "Disk5", "warning"), e.group(t, "Down", "critical")
		e.page(t, p1, g5)
		e.business.Advance(time.Second)
		e.page(t, p2, g6)
		e.round(t, s)
		if got := e.gateway.sentTexts(); len(got) != 4 || !strings.Contains(got[2], "Down, critical") ||
			!strings.Contains(got[3], "Disk5") {
			t.Errorf("the order %q", got[2:])
		}

		// Refused as invalid: Not delivered, page_not_delivered.
		e.business.Advance(2 * CombineWindow)
		e.gateway.fail = []delivery.Outcome{{Kind: delivery.OutcomeUnknown, Status: 400, Error: "Bad number"}}
		e.page(t, p1, g1)
		e.round(t, s)
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'not_delivered' AND reason = 'call_refused'
			AND last_error_class = 'unknown' AND attempts = 1 AND text IS NULL`); n != 1 {
			t.Errorf("%d calls refused", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1 AND kind = 'escalation'
			AND event = 'page_not_delivered' AND loudness = 'quiet' AND mentions = '{}' AND paged_user_ids = ARRAY[$2]::bigint[]
			AND gateway_connection_id = $3 AND reason = 'call_refused' AND transport = 'system'`, g1, p1, e.gw); n != 1 {
			t.Errorf("%d entries of page_not_delivered", n)
		}
		g1Public := fmt.Sprintf("AG00000000%04d", 1)
		page, err := e.groups.Timeline(t.Context(), g1Public, groups.TimelineFilter{Kinds: []groups.Kind{
			groups.KindEscalation}, Limit: 10})
		if err != nil || len(page.Entries) != 1 {
			t.Fatalf("the escalation timeline %+v, %v", page, err)
		}
		entry := page.Entries[0]
		if entry.Event != string(groups.EventPageNotDelivered) || len(entry.PagedUsers) != 1 ||
			entry.PagedUsers[0].Login != "p1" || entry.Gateway == nil || entry.Gateway.Name != "Example Gateway" ||
			entry.Reason == nil || *entry.Reason != groups.PageCallRefused {
			t.Errorf("the entry %+v", entry)
		}
		e.page(t, p1, g5)
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'waiting' AND not_before = $1`,
			e.business.Now().Add(CombineWindow)); n != 1 {
			t.Errorf("the call after a call Not delivered is not due at the end of its window")
		}
		if strings.Contains(e.log.String(), "+1000000000") {
			t.Errorf("the log carries a contact:\n%s", e.log)
		}
	})
}

// TestIntegrationTwoSenders is C-30.FR-4 and NFR-4 on PostgreSQL: two senders of two replicas round concurrently over
// the calls to many people and send each once; a replica that lost its lease during a slow call — here, whose outcome
// never committed — is followed by the other, which sends again with the same Idempotency-Key and text, gets the
// replayed answer, counts it as accepted and calls nobody twice. Concurrent Pages to one person never make two
// waiting calls.
func TestIntegrationTwoSenders(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		const people = 12
		e := setupCalls(t, srv, people)
		for _, u := range e.users {
			e.page(t, u, e.group(t, "Disk", "warning"))
		}
		a, b := e.sender("r1", e.gateway), e.sender("r2", e.gateway)
		a.Batch, b.Batch = 2, 2
		var wg sync.WaitGroup
		for _, s := range []*Sender{a, b} {
			wg.Go(func() {
				for range 4 {
					if _, err := s.Round(t.Context()); err != nil {
						t.Error(err)
					}
				}
			})
		}
		wg.Wait()
		if got := e.gateway.sentTexts(); len(got) != people {
			t.Errorf("%d calls sent to %d people", len(got), people)
		}
		for key, n := range e.gateway.calls {
			if n != 1 {
				t.Errorf("%s sent %d times", key, n)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'accepted' AND attempts = 1`); n != people {
			t.Errorf("%d accepted", n)
		}

		// A replica whose outcome never committed: the other resends with the same key, which is replayed.
		e.business.Advance(2 * CombineWindow)
		g := e.group(t, "Disk", "warning")
		e.page(t, e.users[0], g)
		rows, err := a.Store.ClaimDueCalls(t.Context(), claimParams(a))
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		c, _, err := a.prepare(t.Context(), rows[0])
		if err != nil || c == nil {
			t.Fatal(c, err)
		}
		first := e.gateway.Notify(t.Context(), delivery.Call{Key: c.key()}, delivery.Notification{Contact: c.contact,
			Text: c.text})
		e.real.Advance(SenderLease + time.Second)
		e.round(t, b)
		if first.Kind != delivery.OutcomeOK || e.gateway.calls[c.key()] != 2 || len(e.gateway.sentTexts()) != people+1 {
			t.Errorf("the key was sent %d times, %d calls", e.gateway.calls[c.key()], len(e.gateway.sentTexts()))
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE public_id = $1 AND state = 'accepted'
			AND lease_owner IS NULL`, c.row.PublicID); n != 1 {
			t.Error("the replayed call is not accepted")
		}
		if err := a.record(t.Context(), c, first); err != nil {
			t.Errorf("the late outcome of the replica that lost its lease: %v", err)
		}

		// Concurrent Pages to one person: one waiting call with both Alert Groups.
		e.business.Advance(2 * CombineWindow)
		g7, g8 := e.group(t, "A", "warning"), e.group(t, "B", "warning")
		var pw sync.WaitGroup
		for _, gid := range []int64{g7, g8} {
			pw.Go(func() {
				if err := pgx.BeginFunc(t.Context(), e.d.Pool, func(tx pgx.Tx) error {
					_, err := e.calls.Page(t.Context(), tx, PageCall{UserID: e.users[1], GatewayID: e.gw,
						AlertGroupID: gid, Stop: StopNotFiring})
					return err
				}); err != nil {
					t.Error(err)
				}
			})
		}
		pw.Wait()
		if n := e.count(t, `SELECT count(*) FROM gateway_call_items i JOIN gateway_calls c ON c.id = i.call_id
			WHERE c.state = 'waiting' AND c.user_id = $1`, e.users[1]); n != 2 ||
			e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'waiting'`) != 1 {
			t.Errorf("%d items in the waiting calls", n)
		}
	})
}

// TestNotDeliveredAgainstPageLockOrder is the lock order of calls.go on PostgreSQL: a transaction that holds an Alert
// Group, as a chain's step will, and then Pages the person, against the sender ending a call about that Alert Group
// Not delivered. In the order of calls.go the end waits for the Alert Group, the Page goes on and both commit; with the
// order reversed — the person's lock and the call first — the end holds what the Page waits for and the Page holds
// what the end waits for, and PostgreSQL reports a deadlock (40P01).
func TestNotDeliveredAgainstPageLockOrder(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		t.Run(fmt.Sprintf("reversed=%v", reversed), func(t *testing.T) {
			dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
				ctx := t.Context()
				e := setupCalls(t, srv, 1)
				g := e.group(t, "Disk1", "warning")
				s := e.sender("r1", e.gateway)
				s.lockCallFirst = reversed
				e.page(t, e.users[0], g)
				rows, err := s.Store.ClaimDueCalls(ctx, claimParams(s))
				if err != nil || len(rows) != 1 {
					t.Fatal(rows, err)
				}
				c, _, err := s.prepare(ctx, rows[0])
				if err != nil || c == nil {
					t.Fatal(c, err)
				}
				// The Page holds the Alert Group, as the dispatcher's lock does, until the end waits for a lock.
				tx, err := e.d.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
				if _, err := tx.Exec(ctx, `SELECT id FROM alert_groups WHERE id = $1 FOR NO KEY UPDATE`, g); err != nil {
					t.Fatal(err)
				}
				ended := make(chan error, 1)
				go func() {
					ended <- s.notDelivered(ctx, c, groups.PageCallRefused, 1,
						delivery.Outcome{Kind: delivery.OutcomeUnknown, Status: 400})
				}()
				deadline := time.Now().Add(10 * time.Second)
				// Only the backends of this test's database: other packages run their tests on the same server.
				for e.count(t, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
					WHERE NOT l.granted AND a.datname = current_database()`) == 0 {
					if time.Now().After(deadline) {
						t.Fatal("the end never waited for a lock")
					}
					time.Sleep(10 * time.Millisecond)
				}
				_, pageErr := e.calls.Page(ctx, tx, PageCall{UserID: e.users[0], GatewayID: e.gw, AlertGroupID: g,
					Stop: StopNotFiring})
				if pageErr == nil {
					pageErr = tx.Commit(ctx)
				} else {
					_ = tx.Rollback(ctx)
				}
				endErr := <-ended
				deadlocked := false
				for _, err := range []error{pageErr, endErr} {
					if pe, ok := errors.AsType[*pgconn.PgError](err); ok && pe.Code == "40P01" {
						deadlocked = true
					} else if err != nil {
						t.Errorf("not a deadlock: %v", err)
					}
				}
				if deadlocked != reversed {
					t.Fatalf("deadlocked %v with the order reversed %v: page %v, end %v", deadlocked, reversed,
						pageErr, endErr)
				}
				if !reversed && (e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'not_delivered'`) != 1 ||
					e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'waiting'`) != 1 ||
					e.count(t, `SELECT count(*) FROM timeline_entries WHERE event = 'page_not_delivered'`) != 1) {
					t.Error("the end or the Page did not commit")
				}
			})
		})
	}
}

// TestIntegrationCallRetention is the retention of calls on PostgreSQL (C-30.FR-4, C-09.FR-16): once the Alert Group
// retention deletes an Alert Group resolved retention.alert_group_summaries ago, with its items by cascade, the ended
// calls about it alone are deleted by the call retention of the same Leader task; a call about another Alert Group
// too, and a call still waiting, stay; a second run deletes nothing more. A gateway that keeps asking to wait uses the
// time budget of delivery.transient_budget and ends the call Not delivered with call_budget_used.
func TestIntegrationCallRetention(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		e := setupCalls(t, srv, 2)
		s := e.sender("r1", e.gateway)
		old, kept := e.group(t, "Old", "warning"), e.group(t, "Kept", "warning")
		e.page(t, e.users[0], old)
		e.round(t, s)
		e.business.Advance(2 * CombineWindow)
		e.page(t, e.users[0], old)
		e.page(t, e.users[0], kept)
		e.round(t, s)
		e.page(t, e.users[1], kept)
		e.gateway.fail = []delivery.Outcome{{Kind: delivery.OutcomeRetryAfter, RetryAfter: 20 * time.Minute}}
		e.round(t, s)
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'sending'
			AND first_failed_at = $2 AND attempts = 0`, e.users[1], e.business.Now()); n != 1 {
			t.Fatalf("the wait did not start the budget: %d", n)
		}
		e.business.Advance(31 * time.Minute)
		e.gateway.fail = []delivery.Outcome{{Kind: delivery.OutcomeRetryAfter, RetryAfter: time.Minute}}
		e.round(t, s)
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'not_delivered'
			AND reason = 'call_budget_used'`, e.users[1]); n != 1 {
			t.Errorf("the waits did not use the budget: %d", n)
		}
		e.page(t, e.users[0], kept)

		e.exec(t, `UPDATE alert_groups SET status = 'resolved', resolved_at = $2, resolved_by_kind = 'system',
			resolve_reason = 'resolved', firing_alert_count = 0 WHERE id = $1`, old, c0.AddDate(-3, 0, 0))
		now := e.business.Now()
		_, summaries, err := groups.RetentionTask(groups.NewStore(e.d.Pool))(t.Context(), e.orgID, now)
		if err != nil || summaries != 1 {
			t.Fatalf("the alert group retention deleted %d summaries, %v", summaries, err)
		}
		p := NewCallPruner(e.d.Pool)
		if n, err := p.Calls(t.Context(), e.orgID, now); err != nil || n != 1 {
			t.Errorf("Calls = %d, %v", n, err)
		}
		if n, err := p.Calls(t.Context(), e.orgID, now); err != nil || n != 0 {
			t.Errorf("Calls again = %d, %v", n, err)
		}
		if got := e.count(t, `SELECT count(*) FROM gateway_calls`); got != 3 {
			t.Errorf("%d calls left, want the one about both, the one Not delivered and the waiting one", got)
		}
	})
}
