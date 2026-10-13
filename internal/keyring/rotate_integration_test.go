// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package keyring_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// syncBuffer is a log written by a replica's goroutines and read by the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) count(event string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), `"event":"`+event+`"`)
}

// migratedForRotation is a migrated database with the Organization, the partitions of the Audit log and the key canary of k.
func migratedForRotation(t *testing.T, s dbtest.Server, k *keyring.Keyring) (*db.DB, int64, string) {
	t.Helper()
	ctx := t.Context()
	u := s.NewDatabase(t)
	conn := config.Database{URL: logging.Secret(u), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	if err := d.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), log, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
		FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(ctx, keyring.NewStore(d.Pool), t0)
	if err != nil || k.Open(ctx, log, st) != nil {
		t.Fatal(err)
	}
	var org int64
	if err := d.Pool.QueryRow(ctx, `SELECT id FROM organizations`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	return d, org, u
}

// secretFixtures write one value of every field of encrypted_values, encrypted by k with the field bound, last
// changed at t0, and return the plaintexts by field.
func secretFixtures(t *testing.T, d *db.DB, org int64, k *keyring.Keyring) map[string]string {
	t.Helper()
	ctx := t.Context()
	plain := map[string]string{}
	enc := func(field string) []byte {
		plain[field] = "value of " + field
		ct, _, err := k.Encrypt(field, []byte(plain[field]))
		if err != nil {
			t.Fatal(err)
		}
		return ct
	}
	key := k.ActiveKeyID()
	for _, f := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE organizations SET outgoing_heartbeat_url_ciphertext = $2, outgoing_heartbeat_url_key_id = $4,
			outgoing_heartbeat_url_updated_at = $5, outgoing_heartbeat_proxy_password_ciphertext = $3,
			outgoing_heartbeat_proxy_password_key_id = $4, outgoing_heartbeat_proxy_password_updated_at = $5
			WHERE id = $1`, []any{org, enc("organizations.outgoing_heartbeat_url"),
			enc("organizations.outgoing_heartbeat_proxy_password"), key, t0}},
		{`INSERT INTO oidc_settings (org_id, enabled, issuer_url, client_id, client_secret_ciphertext,
			client_secret_key_id, client_secret_updated_at, scopes, groups_claim, group_mappings, unmatched_role,
			sync_role, skip_totp_with_idp_mfa, proxy_password_ciphertext, proxy_password_key_id,
			proxy_password_updated_at, updated_at)
			VALUES ($1, true, 'https://idp.example.org', 'muster', $2, $4, $5, '{openid}', 'groups', '[]', 'none',
			false, false, $3, $4, $5, $5)`, []any{org, enc("oidc_settings.client_secret"),
			enc("oidc_settings.proxy_password"), key, t0}},
		{`INSERT INTO oidc_auth_requests (state_hash, org_id, purpose, nonce, code_verifier_ciphertext,
			code_verifier_key_id, created_at, expires_at)
			VALUES (decode(repeat('ab', 32), 'hex'), $1, 'sign_in', 'nonce', $2, $3, $4,
			$4::timestamptz + interval '10 minutes')`, []any{org, enc("oidc_auth_requests.code_verifier"), key, t0}},
		{`WITH u AS (INSERT INTO users (org_id, public_id, login, name, role, source, status, oidc_issuer,
			oidc_subject, oidc_offline_token_ciphertext, oidc_offline_token_key_id, oidc_offline_token_updated_at,
			created_at, updated_at)
			VALUES ($1, 'SRAAAAAAAAAAA1', 'alice', 'alice', 'responder', 'oidc', 'active', 'https://idp.example.org',
			'alice', $2, $4, $5, $5, $5) RETURNING id)
			INSERT INTO user_totp (user_id, org_id, seed_ciphertext, seed_key_id, seed_updated_at, enrolled_at,
			pending_seed_ciphertext, pending_seed_key_id, pending_seed_updated_at)
			SELECT u.id, $1, $3, $4, $5, $5, $6, $4, $5 FROM u`, []any{org, enc("users.oidc_offline_token"),
			enc("user_totp.seed"), key, t0, enc("user_totp.pending_seed")}},
		{`INSERT INTO connections (org_id, public_id, type, name, telegram_bot_api_base_url, telegram_update_mode,
			telegram_webhook_secret_ciphertext, telegram_webhook_secret_key_id, telegram_webhook_secret_updated_at,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, proxy_password_ciphertext,
			proxy_password_key_id, proxy_password_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at)
			VALUES ($1, 'CNAAAAAAAAAAA1', 'telegram', 'tg', 'https://api.telegram.org', 'webhook', $2, $4, $5, $3, $4,
			$5, $6, $4, $5, 30, 1, $5, $5)`, []any{org, enc("connections.telegram_webhook_secret"),
			enc("connections.bot_token"), key, t0, enc("connections.proxy_password")}},
		{`WITH ds AS (INSERT INTO destinations (org_id, public_id, type, name, webhook_mode, webhook_template_config,
			proxy_password_ciphertext, proxy_password_key_id, proxy_password_updated_at, signing_secret_ciphertext,
			signing_secret_key_id, signing_secret_updated_at, previous_signing_secret_ciphertext,
			previous_signing_secret_key_id, previous_signing_secret_since, mentions, limiter_limit,
			limiter_per_seconds, health, created_at, updated_at)
			VALUES ($1, 'DSAAAAAAAAAAA1', 'webhook', 'hook', 'template', '{}', $2, $4, $5, $3, $4, $5, $6, $4, $5,
			'{}', 30, 1, 'healthy', $5, $5) RETURNING id)
			INSERT INTO destination_secrets (destination_id, org_id, name, value_ciphertext, value_key_id,
			value_updated_at) SELECT ds.id, $1, 'API_KEY', $7, $4, $5 FROM ds`,
			[]any{org, enc("destinations.proxy_password"), enc("destinations.signing_secret"), key, t0,
				enc("destinations.previous_signing_secret"), enc("destination_secrets.value")}},
	} {
		if _, err := d.Pool.Exec(ctx, f.sql, f.args...); err != nil {
			t.Fatalf("%s: %v", f.sql, err)
		}
	}
	return plain
}

// listener receives the notifications of the hints and of key activations, in commit order.
type listener struct {
	conn *pgx.Conn
}

func listen(t *testing.T, d *db.DB) *listener {
	t.Helper()
	conn, err := d.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })
	for _, ch := range []string{db.HintChannel, keyring.Channel} {
		if _, err := conn.Exec(t.Context(), "LISTEN "+ch); err != nil {
			t.Fatal(err)
		}
	}
	return &listener{conn: conn}
}

// next is the next notification as channel and hint type, or the payload of another channel.
func (l *listener) next(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	n, err := l.conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("no notification: %v", err)
	}
	if n.Channel != db.HintChannel {
		return n.Channel + ":" + n.Payload
	}
	var h db.Hint
	if err := json.Unmarshal([]byte(n.Payload), &h); err != nil {
		t.Fatal(err)
	}
	return n.Channel + ":" + h.Type
}

// TestIntegrationRotation covers C-20.FR-7, AC-2 and C-02.FR-15 on PostgreSQL with two replicas: the activation is
// refused while a live replica lacks the key, and rolled back by a value that does not decrypt, sending nothing; once
// both hold it, one transaction makes it active, encrypts the canary and every field of encrypted_values with it —
// the three outgoing webhook secret fields among them — keeping the times they last changed, records
// keyring.key_activated and sends the organization and system-status hints and the keyring notification, received
// after it commits; both replicas switch on the notification without a restart, each logging key_switched once; a
// replica without the key stops; the Keyring status then shows the old key removable. The registry of rotation lists
// exactly the fields of the encrypted_values view.
func TestIntegrationRotation(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		cli, err := keyring.New([][]byte{material('a'), material('b')}, false)
		if err != nil {
			t.Fatal(err)
		}
		d, org, _ := migratedForRotation(t, s, cli)
		a, b := keyring.KeyID(material('a')), keyring.KeyID(material('b'))
		plain := secretFixtures(t, d, org, cli)

		var def string
		if err := d.Pool.QueryRow(ctx, `SELECT pg_get_viewdef('encrypted_values'::regclass)`).Scan(&def); err != nil {
			t.Fatal(err)
		}
		var view []string
		for _, m := range regexp.MustCompile(`'([a-z_]+\.[a-z_]+)'`).FindAllStringSubmatch(def, -1) {
			view = append(view, m[1])
		}
		if registry := keyring.EncryptedFields(); !slices.Equal(view, registry) {
			t.Errorf("the view lists %v, rotation %v", view, registry)
		}
		if n := countRows(t, d, `SELECT count(*) FROM encrypted_values WHERE key_id = $1`, a); n != 15 {
			t.Fatalf("%d fixtures", n)
		}

		// Replica r-a holds both keys, replica r-b only the old one, on the real clock.
		replica := func(id string, keys *keyring.Keyring, log *syncBuffer) *keyring.Recorder {
			r := keyring.NewRecorder(keys, keyring.NewStore(d.Pool), clock.Real{}, logging.New(log, logging.LevelInfo),
				id, "", "dev")
			if err := r.Start(ctx); err != nil {
				t.Fatal(err)
			}
			return r
		}
		open := func(cs ...byte) *keyring.Keyring {
			var m [][]byte
			for _, c := range cs {
				m = append(m, material(c))
			}
			k, err := keyring.New(m, false)
			if err != nil {
				t.Fatal(err)
			}
			st, err := keyring.NewStore(d.Pool).GetKeyringState(ctx)
			if err != nil || k.Open(ctx, logging.New(&bytes.Buffer{}, logging.LevelInfo), keyring.State{
				ActiveKeyID: st.ActiveKeyID, CanaryKeyID: st.CanaryKeyID, Canary: st.CanaryCiphertext}) != nil {
				t.Fatal(err)
			}
			return k
		}
		var logA, logB syncBuffer
		keysA, keysB := open('a', 'b'), open('a')
		ra, rb := replica("r-a", keysA, &logA), replica("r-b", keysB, &logB)
		var cliLog bytes.Buffer
		rotation := keyring.Rotation{Store: keyring.NewRotationStore(d.Pool), Keys: cli,
			Audit: audit.NewWriter(logging.New(&cliLog, logging.LevelInfo), clock.NewManual(t0)),
			Log:   logging.New(&cliLog, logging.LevelInfo), Business: clock.NewManual(t0), Real: clock.Real{}}
		activate := func() (keyring.Rotated, error) {
			var out keyring.Rotated
			err := d.WithMigrationLock(ctx, func(ctx context.Context) error {
				var err error
				out, err = rotation.Activate(ctx, b, "ops")
				return err
			})
			return out, err
		}
		notes := listen(t, d)

		_, err = activate()
		if lacking, ok := errors.AsType[*keyring.ReplicasLackKeyError](err); !ok ||
			err.Error() != "Replicas r-b do not hold key "+b+" yet." || !slices.Equal(lacking.Replicas, []string{"r-b"}) {
			t.Fatalf("refusal = %v", err)
		}
		if _, err := (keyring.Rotation{Store: rotation.Store, Keys: keysB, Audit: rotation.Audit, Log: rotation.Log,
			Business: rotation.Business, Real: rotation.Real}).Activate(ctx, b, "ops"); !errors.Is(err,
			keyring.ErrKeyNotHeld) {
			t.Errorf("a key not held = %v", err)
		}
		// r-b restarts with both keys.
		if err := rb.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		var logB2 syncBuffer
		keysB = open('a', 'b')
		rb = replica("r-b2", keysB, &logB2)

		// A value that does not decrypt rolls the activation back, and nothing is sent.
		if _, err := d.Pool.Exec(ctx, `UPDATE connections SET bot_token_ciphertext = '\x0000' || bot_token_ciphertext`); err != nil {
			t.Fatal(err)
		}
		if _, err := activate(); !errors.Is(err, keyring.ErrDecrypt) || !strings.Contains(err.Error(),
			"connections.bot_token") || strings.Contains(err.Error(), plain["connections.bot_token"]) {
			t.Fatalf("an undecryptable value = %v", err)
		}
		if _, err := d.Pool.Exec(ctx, `UPDATE connections SET bot_token_ciphertext = substring(bot_token_ciphertext from 3)`); err != nil {
			t.Fatal(err)
		}
		if n := countRows(t, d, `SELECT count(*) FROM encrypted_values WHERE key_id = $1`, a); n != 15 {
			t.Fatalf("the rollback left %d values with the old key", n)
		}

		out, err := activate()
		if err != nil || out != (keyring.Rotated{KeyID: b, Previous: a, Reencrypted: 15}) {
			t.Fatalf("activation = %+v, %v", out, err)
		}
		// The notifications of the activation, and none of the refusals or the rollback before them.
		if _, err := d.Pool.Exec(ctx, `SELECT pg_notify($1, 'sentinel')`, keyring.Channel); err != nil {
			t.Fatal(err)
		}
		var got []string
		for range 4 {
			got = append(got, notes.next(t))
		}
		if want := []string{db.HintChannel + ":" + live.HintOrganization, db.HintChannel + ":" + live.HintSystemStatus,
			keyring.Channel + ":" + b, keyring.Channel + ":sentinel"}; !slices.Equal(got, want) {
			t.Errorf("notifications %v, want %v", got, want)
		}

		only := open('b')
		if only.ActiveKeyID() != b {
			t.Errorf("the canary does not open with the new key alone")
		}
		if n := countRows(t, d, `SELECT count(*) FROM encrypted_values WHERE key_id <> $1`, b); n != 0 {
			t.Errorf("%d values not on the new key", n)
		}
		values, err := d.Pool.Query(ctx, `SELECT 'organizations.outgoing_heartbeat_url', outgoing_heartbeat_url_ciphertext,
				outgoing_heartbeat_url_key_id, outgoing_heartbeat_url_updated_at FROM organizations
			UNION ALL SELECT 'organizations.outgoing_heartbeat_proxy_password', outgoing_heartbeat_proxy_password_ciphertext,
				outgoing_heartbeat_proxy_password_key_id, outgoing_heartbeat_proxy_password_updated_at FROM organizations
			UNION ALL SELECT 'oidc_settings.client_secret', client_secret_ciphertext, client_secret_key_id,
				client_secret_updated_at FROM oidc_settings
			UNION ALL SELECT 'oidc_settings.proxy_password', proxy_password_ciphertext, proxy_password_key_id,
				proxy_password_updated_at FROM oidc_settings
			UNION ALL SELECT 'oidc_auth_requests.code_verifier', code_verifier_ciphertext, code_verifier_key_id,
				created_at FROM oidc_auth_requests
			UNION ALL SELECT 'users.oidc_offline_token', oidc_offline_token_ciphertext, oidc_offline_token_key_id,
				oidc_offline_token_updated_at FROM users
			UNION ALL SELECT 'user_totp.seed', seed_ciphertext, seed_key_id, seed_updated_at FROM user_totp
			UNION ALL SELECT 'user_totp.pending_seed', pending_seed_ciphertext, pending_seed_key_id,
				pending_seed_updated_at FROM user_totp
			UNION ALL SELECT 'connections.bot_token', bot_token_ciphertext, bot_token_key_id, bot_token_updated_at
				FROM connections
			UNION ALL SELECT 'connections.proxy_password', proxy_password_ciphertext, proxy_password_key_id,
				proxy_password_updated_at FROM connections
			UNION ALL SELECT 'connections.telegram_webhook_secret', telegram_webhook_secret_ciphertext,
				telegram_webhook_secret_key_id, telegram_webhook_secret_updated_at FROM connections
			UNION ALL SELECT 'destinations.proxy_password', proxy_password_ciphertext, proxy_password_key_id,
				proxy_password_updated_at FROM destinations
			UNION ALL SELECT 'destinations.signing_secret', signing_secret_ciphertext, signing_secret_key_id,
				signing_secret_updated_at FROM destinations
			UNION ALL SELECT 'destinations.previous_signing_secret', previous_signing_secret_ciphertext,
				previous_signing_secret_key_id, previous_signing_secret_since FROM destinations
			UNION ALL SELECT 'destination_secrets.value', value_ciphertext, value_key_id, value_updated_at
				FROM destination_secrets`)
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for values.Next() {
			var field, key string
			var ct []byte
			var at time.Time
			if err := values.Scan(&field, &ct, &key, &at); err != nil {
				t.Fatal(err)
			}
			seen++
			value, err := only.Decrypt(field, key, ct)
			if err != nil || string(value) != plain[field] || !at.Equal(t0) {
				t.Errorf("%s: %q, %v, changed at %v", field, value, err, at)
			}
		}
		values.Close()
		if seen != 15 {
			t.Errorf("%d fields read", seen)
		}

		var actorKind, actorName, diff, details string
		if err := d.Pool.QueryRow(ctx, `SELECT actor_kind, actor_name, diff::text, details::text FROM audit_log
			WHERE action = $1`, keyring.ActionKeyActivated).Scan(&actorKind, &actorName, &diff, &details); err != nil {
			t.Fatal(err)
		}
		if actorKind != "cli" || actorName != "ops" ||
			diff != fmt.Sprintf(`[{"after": "%s", "before": "%s", "pointer": "/active_key_id"}]`, b, a) ||
			details != `{"reencrypted": 15, "open_deliveries_to_resign": 0}` {
			t.Errorf("audit %s %s %s %s", actorKind, actorName, diff, details)
		}
		if !strings.Contains(cliLog.String(), `"event":"key_activated"`) {
			t.Errorf("cli log %s", cliLog.String())
		}

		// Both replicas switch when woken by the notification, without a restart; r-old never got the key and stops.
		runCtx, stop := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for _, r := range []*keyring.Recorder{ra, rb} {
			wg.Go(func() { _ = r.Run(runCtx, nil) })
			r.Wake()
		}
		for deadline := time.Now().Add(10 * time.Second); keysA.ActiveKeyID() != b || keysB.ActiveKeyID() != b; {
			if time.Now().After(deadline) {
				t.Fatalf("active keys %s %s", keysA.ActiveKeyID(), keysB.ActiveKeyID())
			}
			time.Sleep(10 * time.Millisecond)
		}
		for _, r := range []*keyring.Recorder{ra, rb} {
			r.Wake()
		}
		stop()
		wg.Wait()
		if err := ra.Switch(ctx); err != nil {
			t.Fatal(err)
		}
		if logA.count("key_switched") != 1 || logB2.count("key_switched") != 1 || logB.count("key_switched") != 0 {
			t.Errorf("key_switched %d %d", logA.count("key_switched"), logB2.count("key_switched"))
		}
		var logOld syncBuffer
		lacking, err := keyring.New([][]byte{material('a')}, false)
		if err != nil {
			t.Fatal(err)
		}
		old := keyring.NewRecorder(lacking, keyring.NewStore(d.Pool), clock.Real{},
			logging.New(&logOld, logging.LevelInfo), "r-old", "", "dev")
		if err := old.Switch(ctx); !errors.Is(err, keyring.ErrKeyMismatch) || logOld.count("active_key_not_held") != 1 {
			t.Errorf("a replica without the key = %v", err)
		}

		// The Keyring status: the new key active, the old one removable, held by both live replicas.
		st, err := keyring.ReadStatus(ctx, keyring.NewStatusQueries(d.Pool), cli.KeyIDs(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if k, ok := st.Key(a); !ok || k.State != keyring.KeyRemovable || !slices.Equal(k.HeldBy, []string{"r-a", "r-b2"}) {
			t.Errorf("old key %+v", k)
		}
		if k, ok := st.Key(b); !ok || k.State != keyring.KeyActive || k.EncryptedValues != 15 {
			t.Errorf("new key %+v", k)
		}

		// Running it again changes nothing and records nothing.
		if out, err := activate(); err != nil || !out.AlreadyActive || out.Reencrypted != 0 ||
			countRows(t, d, `SELECT count(*) FROM audit_log WHERE action = $1`, keyring.ActionKeyActivated) != 1 {
			t.Errorf("again = %+v, %v", out, err)
		}
	})
}

func countRows(t *testing.T, d *db.DB, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}
