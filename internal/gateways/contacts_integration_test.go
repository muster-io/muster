// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package gateways_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/gateways"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

// sender is a Notification gateway that keeps the codes it was sent.
type sender struct {
	mu    sync.Mutex
	codes []string
}

func (s *sender) Notify(_ context.Context, _ delivery.Call, n delivery.Notification) delivery.Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes = append(s.codes, regexp.MustCompile(`[0-9]{6}`).FindString(n.Text))
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n1"}
}

func (s *sender) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.codes[len(s.codes)-1]
}

type env struct {
	d      *db.DB
	orgID  int64
	clock  *clock.Manual
	gw     *sender
	svc    *gateways.Service
	keys   *keyring.Keyring
	people map[string]gateways.Requester
}

// setup is a migrated database with the Organization, the Audit log partition of November 2026, the Notification
// gateway CN0000000000G1, the Bridge Connection CN0000000000B1 for messages only, and the Users petr, olga, ivan, anna
// and lev, each signed in twice.
func setup(t *testing.T, s dbtest.Server) *env {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	if err := d.Migrate(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`CREATE TABLE audit_log_p202611 PARTITION OF audit_log
		FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')`)
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys := openKeys(t, d, 'a', 'b')
	// A real ciphertext, which a key rotation re-encrypts.
	token, keyID, err := keys.Encrypt("connections.bot_token", []byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	for id, roles := range map[string]string{"CN0000000000G1": `["gateway"]`, "CN0000000000B1": `["messenger"]`} {
		exec(`INSERT INTO connections (org_id, public_id, type, name, bridge_base_url, bridge_event_delivery,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, bridge_info, created_at, updated_at)
			VALUES ($1, $2, 'bridge', $2, 'http://gw', 'poll', $5, $6, $3, 1, 1, jsonb_build_object('roles', $4::jsonb), $3,
			$3)`, org.ID, id, t0, roles, token, keyID)
	}
	c := clock.NewManual(t0)
	gw := &sender{}
	e := &env{d: d, orgID: org.ID, clock: c, gw: gw, keys: keys, people: map[string]gateways.Requester{}}
	e.svc = e.service(t, keys)
	for i, login := range []string{"petr", "olga", "ivan", "anna", "lev"} {
		pub := "SR00000000000" + string(rune('A'+i))
		var uid int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
			created_at, updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', 'active', $4, $4) RETURNING id`,
			org.ID, pub, login, t0).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		for j, suffix := range []string{"1", "2"} {
			var sid int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO sessions (org_id, public_id, user_id, token_hash, state, method,
				created_at, last_used_at, idle_expires_at, expires_at) VALUES ($1, $2, $3, sha256($5::bytea), 'active',
				'local', $4, $4, $4, $4) RETURNING id`, org.ID, "SN0000000000"+string(rune('A'+i))+suffix, uid,
				t0, []byte(login+suffix)).Scan(&sid); err != nil {
				t.Fatal(err)
			}
			key := login
			if j == 1 {
				key += "2"
			}
			e.people[key] = gateways.Requester{UserID: uid, UserPublicID: pub, SessionID: sid,
				Actor: audit.User(uid, pub), Transport: audit.TransportUI}
		}
	}
	return e
}

// openKeys is the Keyring of master keys of the bytes fills over the database d: on a new database the first key
// becomes active, otherwise the key that keyring_state names.
func openKeys(t *testing.T, d *db.DB, fills ...byte) *keyring.Keyring {
	t.Helper()
	var keys [][]byte
	for _, f := range fills {
		keys = append(keys, bytes.Repeat([]byte{f}, keyring.KeySize))
	}
	k, err := keyring.New(keys, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(t.Context(), keyring.NewStore(d.Pool), t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	return k
}

// service is the Service of the Organization with the Keyring keys.
func (e *env) service(t *testing.T, keys *keyring.Keyring) *gateways.Service {
	t.Helper()
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	return gateways.New(gateways.Config{OrgID: e.orgID, Store: gateways.NewStore(e.d.Pool), Keys: keys,
		Audit: audit.NewWriter(logger, e.clock), Business: e.clock, Log: logger, Gateway: e.gw,
		Path: deliverytest.Unlimited(e.orgID, clock.Clocks{Business: e.clock, Real: clock.Real{}})})
}

// TestIntegrationSignedCodes: a code is stored as its HMAC with the Keyring, not as the SHA-256 of "public_id:code";
// after `muster secrets rotate-key` made another key active within the code's gateway.contact_code it still confirms,
// and a code stored as that SHA-256 before the upgrade confirms too.
func TestIntegrationSignedCodes(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		petr, olga := e.people["petr"], e.people["olga"]
		a, err := e.svc.Create(ctx, petr, "CN0000000000G1", "+10000000001")
		if err != nil {
			t.Fatal(err)
		}
		code := e.gw.last()
		var cliLog bytes.Buffer
		logger := logging.New(&cliLog, logging.LevelInfo)
		keys := openKeys(t, e.d, 'a', 'b')
		b := keys.KeyIDs()[1]
		if _, err := (keyring.Rotation{Store: keyring.NewRotationStore(e.d.Pool), Keys: keys,
			Audit: audit.NewWriter(logger, e.clock), Log: logger, Business: e.clock,
			Real: clock.Real{}}).Activate(ctx, b, "ops"); err != nil {
			t.Fatal(err)
		}
		rotated := openKeys(t, e.d, 'a', 'b')
		if rotated.ActiveKeyID() != b {
			t.Fatalf("active %s, want %s", rotated.ActiveKeyID(), b)
		}
		svc := e.service(t, rotated)
		e.clock.Advance(gateways.CodeTTL - time.Second)
		if got, err := svc.Confirm(ctx, petr, a.PublicID, code); err != nil || !got.Confirmed() {
			t.Errorf("the code after the rotation: %+v, %v", got, err)
		}
		o, err := svc.Create(ctx, olga, "CN0000000000G1", "+10000000002")
		if err != nil {
			t.Fatal(err)
		}
		code = e.gw.last()
		plain := sha256.Sum256([]byte(o.PublicID + ":" + code))
		if _, err := e.d.Pool.Exec(ctx, `UPDATE gateway_contact_codes SET code_hash = $1 WHERE org_id = $2
			AND user_id = $3`, plain[:], e.orgID, olga.UserID); err != nil {
			t.Fatal(err)
		}
		if got, err := svc.Confirm(ctx, olga, o.PublicID, code); err != nil || !got.Confirmed() {
			t.Errorf("a code stored before the upgrade: %+v, %v", got, err)
		}
	})
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIntegrationReplacement is C-30.FR-3 and the settled open question 1 on PostgreSQL: a new contact for a gateway
// at which the User has a confirmed one replaces it only once confirmed; until then the confirmed contact is the one
// called and both are listed, and the confirmation deletes it in its transaction. A Connection that is no Notification
// gateway takes no contact. The code is stored as its hash only.
func TestIntegrationReplacement(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		petr := e.people["petr"]
		if _, err := e.svc.Create(ctx, petr, "CN0000000000B1", "+10000000001"); err == nil {
			t.Error("a contact at a Connection that is no gateway")
		}
		gws, err := e.svc.Gateways(ctx)
		if err != nil || len(gws) != 1 || gws[0].PublicID != "CN0000000000G1" {
			t.Fatalf("Gateways = %+v, %v", gws, err)
		}
		a, err := e.svc.Create(ctx, petr, "CN0000000000G1", "+10000000001")
		if err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_contact_codes WHERE code_hash <> sha256($1::bytea)
			AND octet_length(code_hash) = 32 AND position($2::bytea in code_hash) = 0`,
			[]byte(a.PublicID+":"+e.gw.last()), []byte(e.gw.last())); n != 1 {
			t.Errorf("%d codes stored as an HMAC of the contact and the code", n)
		}
		if _, err := e.svc.Confirm(ctx, petr, a.PublicID, e.gw.last()); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
		b, err := e.svc.Create(ctx, petr, "CN0000000000G1", "+10000000002")
		if err != nil {
			t.Fatal(err)
		}
		if b.Replaces == nil || *b.Replaces != a.PublicID {
			t.Errorf("replaces %v", b.Replaces)
		}
		list, err := e.svc.List(ctx, petr.UserID)
		if err != nil || len(list) != 2 || list[0].PublicID != a.PublicID || !list[0].Confirmed() ||
			list[1].PublicID != b.PublicID || list[1].Confirmed() || *list[1].Replaces != a.PublicID {
			t.Fatalf("List = %+v, %v", list, err)
		}
		if got, ok, err := e.svc.ConfirmedContact(ctx, petr.UserID, gws[0].ID); err != nil || !ok ||
			got != "+10000000001" {
			t.Errorf("called before the confirmation: %q, %v, %v", got, ok, err)
		}
		if _, err := e.svc.Confirm(ctx, petr, b.PublicID, e.gw.last()); err != nil {
			t.Fatal(err)
		}
		list, err = e.svc.List(ctx, petr.UserID)
		if err != nil || len(list) != 1 || list[0].PublicID != b.PublicID || !list[0].Confirmed() ||
			list[0].Replaces != nil {
			t.Errorf("after the confirmation: %+v, %v", list, err)
		}
		if got, _, _ := e.svc.ConfirmedContact(ctx, petr.UserID, gws[0].ID); got != "+10000000002" {
			t.Errorf("called %q", got)
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'gateway_contact.deleted'
			AND details->>'reason' = 'replaced' AND resource_public_id = $1`, a.PublicID); n != 1 {
			t.Errorf("%d replacements recorded", n)
		}
		if err := e.svc.Delete(ctx, petr, b.PublicID); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_contacts`); n != 0 {
			t.Errorf("%d contacts after the removal", n)
		}
		// The codes of the removed contacts stay, counted, without them.
		if n := e.count(t, `SELECT count(*) FROM gateway_contact_codes WHERE contact_id IS NULL`); n != 2 {
			t.Errorf("%d codes without their contact", n)
		}
	})
}

// TestIntegrationLimitsAndSessions are gateway.contact_code_requests and gateway.contact_code on PostgreSQL: five
// codes per User across contacts, three per contact text whoever asks, the code checked only in its session, expired
// on the business clock, and pruned a day after it expired.
func TestIntegrationLimitsAndSessions(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		petr, petr2, olga := e.people["petr"], e.people["petr2"], e.people["olga"]
		a, err := e.svc.Create(ctx, petr, "CN0000000000G1", "+10000000001")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Confirm(ctx, petr2, a.PublicID, e.gw.last()); !codeInvalid(err) {
			t.Errorf("the right code from another session: %v", err)
		}
		if n := e.count(t, `SELECT attempts FROM gateway_contact_codes`); n != 1 {
			t.Errorf("attempts %d", n)
		}
		e.clock.Advance(gateways.CodeTTL)
		if _, err := e.svc.Confirm(ctx, petr, a.PublicID, e.gw.last()); !codeInvalid(err) {
			t.Errorf("an expired code: %v", err)
		}
		for range 2 {
			if _, err := e.svc.SendCode(ctx, petr, a.PublicID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := e.svc.SendCode(ctx, petr, a.PublicID); !limited(err) {
			t.Errorf("a fourth code of the User for the contact: %v", err)
		}
		// Another User's codes for the contact text neither refuse nor withhold the code of the one whose contact it is.
		o, err := e.svc.Create(ctx, olga, "CN0000000000G1", "+10000000001")
		if err != nil {
			t.Fatalf("a fourth code for the contact by another User: %v", err)
		}
		if c, err := e.svc.Confirm(ctx, olga, o.PublicID, e.gw.last()); err != nil || !c.Confirmed() {
			t.Errorf("the other User confirms: %+v, %v", c, err)
		}
		for _, c := range []string{"+10000000002", "+10000000003"} {
			if _, err := e.svc.Create(ctx, petr, "CN0000000000G1", c); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := e.svc.Create(ctx, petr, "CN0000000000G1", "+10000000004"); !limited(err) {
			t.Errorf("the sixth code of the User: %v", err)
		}
		if _, err := e.svc.Create(ctx, olga, "CN0000000000G1", "+10000000005"); err != nil {
			t.Errorf("another User's first code: %v", err)
		}
		e.clock.Advance(time.Hour)
		if _, err := e.svc.Create(ctx, petr, "CN0000000000G1", "+10000000004"); err != nil {
			t.Errorf("an hour later: %v", err)
		}
		pruned, err := gateways.NewPruner(e.d.Pool).Codes(ctx, e.orgID,
			e.clock.Now().Add(gateways.CodePruneAfter+gateways.CodeTTL+time.Second), 100)
		if err != nil || pruned != 8 || e.count(t, `SELECT count(*) FROM gateway_contact_codes`) != 0 {
			t.Errorf("pruned %d, %v", pruned, err)
		}
	})
}

// TestIntegrationContactCap is the cap of gateway.contact_code_requests per contact text across Users on PostgreSQL:
// ten codes within an hour reach the contact; a request beyond them is answered as sent, sends nothing and stores a
// code that looks sent but that no one knows, which counts toward the requester's own limits.
func TestIntegrationContactCap(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		const text = "+10000000001"
		var anna gateways.Contact
		for _, login := range []string{"petr", "olga", "ivan", "anna"} {
			c, err := e.svc.Create(ctx, e.people[login], "CN0000000000G1", text)
			if err != nil {
				t.Fatal(err)
			}
			anna = c
			for range 2 {
				if login == "anna" {
					break
				}
				e.clock.Advance(time.Minute)
				if _, err := e.svc.SendCode(ctx, e.people[login], c.PublicID); err != nil {
					t.Fatal(err)
				}
			}
		}
		tenth := e.gw.last()
		e.clock.Advance(time.Minute)
		if _, err := e.svc.SendCode(ctx, e.people["anna"], anna.PublicID); err != nil {
			t.Fatalf("the eleventh code: %v", err)
		}
		if _, err := e.svc.Create(ctx, e.people["lev"], "CN0000000000G1", text); err != nil {
			t.Fatalf("a first request beyond the cap: %v", err)
		}
		if n := len(e.gw.codes); n != 10 {
			t.Errorf("%d codes sent", n)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_contact_codes WHERE sent_at IS NOT NULL
			AND voided_at IS NULL AND expires_at = created_at + interval '10 minutes'`); n != 5 {
			t.Errorf("%d codes that look sent and unused", n)
		}
		if _, err := e.svc.Confirm(ctx, e.people["anna"], anna.PublicID, tenth); !codeInvalid(err) {
			t.Errorf("the code sent before the withheld one: %v", err)
		}
		if _, err := e.svc.SendCode(ctx, e.people["anna"], anna.PublicID); err != nil {
			t.Errorf("the requester's third code: %v", err)
		}
		if _, err := e.svc.SendCode(ctx, e.people["anna"], anna.PublicID); !limited(err) {
			t.Errorf("the requester's fourth code for the contact text: %v", err)
		}
	})
}

// TestIntegrationParallelAttempts: wrong codes typed at once never pass the attempts of gateway.contact_code, and the
// right code is refused after them.
func TestIntegrationParallelAttempts(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		petr := e.people["petr"]
		a, err := e.svc.Create(ctx, petr, "CN0000000000G1", "+10000000001")
		if err != nil {
			t.Fatal(err)
		}
		right, wrong := e.gw.last(), "000000"
		if right == wrong {
			wrong = "111111"
		}
		var wg sync.WaitGroup
		for range 12 {
			wg.Go(func() {
				if _, err := e.svc.Confirm(ctx, petr, a.PublicID, wrong); !codeInvalid(err) {
					t.Errorf("a wrong code: %v", err)
				}
			})
		}
		wg.Wait()
		if n := e.count(t, `SELECT attempts FROM gateway_contact_codes`); n != gateways.MaxAttempts {
			t.Errorf("%d attempts counted", n)
		}
		if _, err := e.svc.Confirm(ctx, petr, a.PublicID, right); !codeInvalid(err) {
			t.Errorf("the right code after the attempts: %v", err)
		}
	})
}

// TestIntegrationRemoveUserContacts erases a User's contacts and codes in the transaction of the caller.
func TestIntegrationRemoveUserContacts(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		petr, olga := e.people["petr"], e.people["olga"]
		for _, r := range []gateways.Requester{petr, olga} {
			if _, err := e.svc.Create(ctx, r, "CN0000000000G1", "+1000000000"+r.UserPublicID[13:]); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := e.d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RemoveUserContacts(ctx, tx, petr.UserID, audit.System, audit.TransportSystem,
			petr.Address); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT (SELECT count(*) FROM gateway_contacts WHERE user_id = $1) +
			(SELECT count(*) FROM gateway_contact_codes WHERE user_id = $1)`, petr.UserID); n != 0 {
			t.Errorf("petr keeps %d rows", n)
		}
		if n := e.count(t, `SELECT (SELECT count(*) FROM gateway_contacts WHERE user_id = $1) +
			(SELECT count(*) FROM gateway_contact_codes WHERE user_id = $1)`, olga.UserID); n != 2 {
			t.Errorf("olga keeps %d rows", n)
		}
	})
}

func codeInvalid(err error) bool {
	f, ok := errors.AsType[*gateways.FieldError](err)
	return ok && f.Code == gateways.CodeContactCodeInvalid
}

func limited(err error) bool {
	_, ok := errors.AsType[*gateways.RateLimitedError](err)
	return ok
}

// TestIntegrationCreateDuringUserDeletion is C-03.FR-13: a contact added while the deletion of its User holds the
// User's lock waits for it, finds the User deleted and adds nothing, so that no contact outlives the deletion.
func TestIntegrationCreateDuringUserDeletion(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		petr := e.people["petr"]
		tx, err := e.d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, petr.UserID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET status = 'deleted', name = 'deleted-user-' || public_id,
			login = 'deleted-user-' || public_id, deleted_at = $2 WHERE id = $1`, petr.UserID, t0); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := e.svc.Create(ctx, petr, "CN0000000000G1", "+10000000001")
			done <- err
		}()
		// The Create waits for the lock of the User that the deletion holds.
		for waiting := 0; waiting == 0; {
			select {
			case err := <-done:
				t.Fatalf("Create did not wait for the deletion: %v", err)
			default:
			}
			waiting = e.count(t, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'
				AND datname = current_database()`)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, gateways.ErrNotFound) {
			t.Errorf("Create for a deleted User = %v", err)
		}
		if n := e.count(t, `SELECT (SELECT count(*) FROM gateway_contacts) + (SELECT count(*) FROM gateway_contact_codes)`); n != 0 {
			t.Errorf("%d contacts and codes outlive the deletion", n)
		}
		if len(e.gw.codes) != 0 {
			t.Errorf("%d codes were sent", len(e.gw.codes))
		}
	})
}

// TestIntegrationParallelSends: two code requests sent at once on top of a valid code both succeed, serialized under
// the lock of the User, and the newest code is the one that confirms.
func TestIntegrationParallelSends(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		petr := e.people["petr"]
		a, err := e.svc.Create(ctx, petr, "CN0000000000G1", "+10000000001")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Go(func() { _, errs[i] = e.svc.SendCode(ctx, petr, a.PublicID) })
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("send %d: %v", i, err)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_contact_codes WHERE sent_at IS NOT NULL`); n != 3 {
			t.Errorf("%d codes marked sent", n)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_contact_codes WHERE voided_at IS NULL`); n != 1 {
			t.Errorf("%d codes left valid", n)
		}
		var newest string
		var stored []byte
		if err := e.d.Pool.QueryRow(ctx, `SELECT code_hash FROM gateway_contact_codes ORDER BY id DESC LIMIT 1`).
			Scan(&stored); err != nil {
			t.Fatal(err)
		}
		e.gw.mu.Lock()
		for _, c := range e.gw.codes {
			if ok, err := e.keys.Verify(keyring.PurposeGatewayContactCode, e.keys.ActiveKeyID(),
				[]byte(a.PublicID+":"+c), stored); err == nil && ok {
				newest = c
			}
		}
		e.gw.mu.Unlock()
		if _, err := e.svc.Confirm(ctx, petr, a.PublicID, newest); err != nil {
			t.Errorf("the newest code: %v", err)
		}
	})
}
