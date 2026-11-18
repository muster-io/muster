// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package keyring_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	mentionsdb "github.com/muster-io/muster/internal/mentions/dbgen"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/teams"
)

// channelChecker passes the Destination check of every Mattermost channel, through the Connections it knows by
// public_id.
type channelChecker map[string]int64

func (c channelChecker) CheckChannel(_ context.Context, cc destinations.ChannelCheck) (destinations.ChannelChecked,
	error) {
	return destinations.ChannelChecked{ConnectionID: c[cc.Connection], Check: mattermost.Check{TeamName: "Team",
		ChannelName: cc.ChannelID, Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}}}, nil
}

// lockEnv is a database whose active key is k-a, with the Keyring cli holding k-a and k-b and onB holding k-b alone,
// to write values that are on the new key already.
type lockEnv struct {
	d        *db.DB
	org      int64
	cli, onB *keyring.Keyring
	b        string
	logs     *syncBuffer
	audit    *audit.Writer
	checker  channelChecker
	dests    *destinations.Service
}

// lockConn is a Connection by id and public_id.
type lockConn struct {
	id       int64
	publicID string
}

var destBy = destinations.Requester{Actor: audit.System, Transport: audit.TransportSystem}

func newLockEnv(t *testing.T, s dbtest.Server) *lockEnv {
	t.Helper()
	ctx := t.Context()
	cli, err := keyring.New([][]byte{material('a'), material('b')}, false)
	if err != nil {
		t.Fatal(err)
	}
	d, org := migratedForRotation(t, s, cli)
	onB, err := keyring.New([][]byte{material('b')}, false)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := onB.Establish(ctx, &memStore{}, t0); err != nil ||
		onB.Open(ctx, logging.New(&syncBuffer{}, logging.LevelInfo), st) != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	e := &lockEnv{d: d, org: org, cli: cli, onB: onB, b: keyring.KeyID(material('b')), logs: logs,
		audit: audit.NewWriter(logging.New(logs, logging.LevelInfo), clock.NewManual(t0)), checker: channelChecker{}}
	e.dests = destinations.New(org, destinations.NewStore(d.Pool))
	e.dests.SetWriter(destinations.WriterConfig{Writer: destinations.NewWriter(d.Pool), Audit: e.audit,
		Business: clock.NewManual(t0), Mattermost: e.checker})
	return e
}

// seal encrypts a value of field with the active key of k.
func (e *lockEnv) seal(t *testing.T, k *keyring.Keyring, field string) ([]byte, string) {
	t.Helper()
	ct, id, err := k.Encrypt(field, []byte("value of "+field))
	if err != nil {
		t.Fatal(err)
	}
	return ct, id
}

// optional is a value of field sealed by k, or none for a nil k.
func (e *lockEnv) optional(t *testing.T, k *keyring.Keyring, field string) ([]byte, *string, *time.Time) {
	t.Helper()
	if k == nil {
		return nil, nil, nil
	}
	ct, key := e.seal(t, k, field)
	return ct, &key, &t0
}

// connection inserts a Mattermost Connection with its bot token sealed by token and a proxy password sealed by proxy,
// none for a nil proxy.
func (e *lockEnv) connection(t *testing.T, name string, token, proxy *keyring.Keyring) lockConn {
	t.Helper()
	c := lockConn{publicID: publicid.New(publicid.Connection)}
	tokenCT, tokenKey := e.seal(t, token, "connections.bot_token")
	proxyCT, proxyKey, proxyAt := e.optional(t, proxy, "connections.proxy_password")
	if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO connections (org_id, public_id, type, name,
		mattermost_server_url, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, proxy_password_ciphertext,
		proxy_password_key_id, proxy_password_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at)
		VALUES ($1, $2, 'mattermost', $3, 'https://mattermost.example.org', $4, $5, $6, $7, $8, $9, 10, 1, $6, $6)
		RETURNING id`, e.org, c.publicID, name, tokenCT, tokenKey, t0, proxyCT, proxyKey, proxyAt).Scan(&c.id); err != nil {
		t.Fatal(err)
	}
	e.checker[c.publicID] = c.id
	return c
}

// webhook inserts an outgoing webhook with its Signing secret sealed by signing and a proxy password sealed by proxy,
// none for a nil proxy, and returns its id.
func (e *lockEnv) webhook(t *testing.T, name string, signing, proxy *keyring.Keyring) int64 {
	t.Helper()
	signingCT, signingKey := e.seal(t, signing, "destinations.signing_secret")
	proxyCT, proxyKey, proxyAt := e.optional(t, proxy, "destinations.proxy_password")
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode,
		webhook_template_config, signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at,
		proxy_password_ciphertext, proxy_password_key_id, proxy_password_updated_at, mentions, limiter_limit,
		limiter_per_seconds, health, created_at, updated_at)
		VALUES ($1, $2, 'webhook', $3, 'template', '{}', $4, $5, $6, $7, $8, $9, '{}', 30, 1, 'healthy', $6, $6)
		RETURNING id`, e.org, publicid.New(publicid.Destination), name, signingCT, signingKey, t0, proxyCT, proxyKey,
		proxyAt).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// mattermostInput is a Mattermost Destination through c, to the channel.
func mattermostInput(c lockConn, channel string) destinations.Input {
	return destinations.Input{Type: destinations.TypeMattermost, Name: "Payments alerts",
		Limiter:    destinations.Limiter{Limit: 10, PerSeconds: 1},
		Mattermost: &destinations.MattermostInput{Connection: c.publicID, TeamID: "team", ChannelID: channel}}
}

// hold begins a transaction that runs sql, which locks rows; it is rolled back at the end of the test unless
// committed.
func (e *lockEnv) hold(t *testing.T, sql string, args ...any) pgx.Tx {
	t.Helper()
	tx, err := e.d.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) })
	if _, err := tx.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
	return tx
}

// activate activates k-b in the background and waits until it waits for a lock in a statement like pattern.
func (e *lockEnv) activate(t *testing.T, pattern string) <-chan error {
	t.Helper()
	rotation := keyring.Rotation{Store: keyring.NewRotationStore(e.d.Pool), Keys: e.cli, Audit: e.audit,
		Log: logging.New(e.logs, logging.LevelInfo), Business: clock.NewManual(t0), Real: clock.Real{}}
	done := make(chan error, 1)
	go func() {
		_, err := rotation.Activate(t.Context(), e.b, "ops")
		done <- err
	}()
	eventually(t, "the activation waiting for a lock", func() bool { return lockWaits(t, e.d, pattern) == 1 })
	return done
}

// onNewKey checks that no value of the Organization is left on another key than k-b.
func (e *lockEnv) onNewKey(t *testing.T) {
	t.Helper()
	if n := countRows(t, e.d, `SELECT count(*) FROM encrypted_values WHERE org_id = $1 AND key_id <> $2`, e.org,
		e.b); n != 0 {
		t.Errorf("%d values not on the new key", n)
	}
}

// lockWaits counts the sessions of the database that wait for a lock in a statement whose text is like pattern.
func lockWaits(t *testing.T, d *db.DB, pattern string) int64 {
	t.Helper()
	return countRows(t, d, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
		AND wait_event_type = 'Lock' AND query LIKE $1`, pattern)
}

// eventually polls cond until it holds, failing the test after ten seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// ended checks that each named result arrives without an error.
func ended(t *testing.T, results map[string]<-chan error) {
	t.Helper()
	for name, done := range results {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s did not end", name)
		}
	}
}

// TestIntegrationRotationLockOrder is #562 against PostgreSQL 14 and 17: a key activation locks the rows it
// re-encrypts in the order of every save that locks several of them, so it runs at the same time as those saves
// without a deadlock. Each case first makes the activation wait for a row that a field-by-field walk would lock after
// a row of higher id.
func TestIntegrationRotationLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		t.Run("connections, with a team save and a destination save", func(t *testing.T) {
			saves(t, newLockEnv(t, s))
		})
		t.Run("outgoing webhooks, as a team save names them", func(t *testing.T) { webhooks(t, newLockEnv(t, s)) })
		t.Run("messenger destinations left unlocked", func(t *testing.T) { messengers(t, newLockEnv(t, s)) })
		t.Run("users, as the active admins are locked", func(t *testing.T) { users(t, newLockEnv(t, s)) })
	})
}

// saves runs an activation with a save of a Destination and a save of a Team. The activation re-encrypts the proxy
// password of the first Connection and the bot token of the third, which a field-by-field walk locks third first; a
// session holding the first Connection FOR SHARE makes the activation wait while the saves start. The Destination
// moves from the second Connection to the third: it holds its row and takes the third FOR SHARE. The Team mentions the
// first two Connections and names the Destination: it takes the two FOR SHARE in id order, then the Destination.
func saves(t *testing.T, e *lockEnv) {
	ctx := t.Context()
	c1, c2, c3 := e.connection(t, "first", e.onB, e.cli), e.connection(t, "second", e.onB, nil),
		e.connection(t, "third", e.cli, nil)
	teamSvc := teams.New(teams.Config{OrgID: e.org, Store: teams.NewStore(e.d.Pool), Audit: e.audit,
		Business: clock.NewManual(t0)})
	teamBy := teams.Requester{Actor: audit.System, Transport: audit.TransportSystem}
	team, err := teamSvc.Create(ctx, teamBy, teams.Input{Name: "Payments", Slug: "payments"})
	if err != nil {
		t.Fatal(err)
	}
	dest, err := e.dests.Create(ctx, destBy, mattermostInput(c2, "alerts"))
	if err != nil {
		t.Fatal(err)
	}

	holder := e.hold(t, `SELECT id FROM connections WHERE org_id = $1 AND id = $2 FOR SHARE`, e.org, c1.id)
	activated := e.activate(t, "%FROM connections%FOR NO KEY UPDATE%")

	destSaved := make(chan error, 1)
	go func() {
		_, err := e.dests.Update(ctx, destBy, dest.PublicID, nil, mattermostInput(c3, "alerts"))
		destSaved <- err
	}()
	eventually(t, "the destination save ending or waiting for its connection", func() bool {
		return len(destSaved) == 1 || lockWaits(t, e.d, "%-- name: LockMattermostConnection %") == 1
	})
	teamSaved := make(chan error, 1)
	go func() {
		_, err := teamSvc.Update(ctx, teamBy, team.PublicID, nil, teams.Input{Name: team.Name, Slug: team.Slug,
			Mentions: &[]teams.MentionInput{{ConnectionID: c1.publicID, Group: "payments_team"},
				{ConnectionID: c2.publicID, Group: "payments_team"}},
			DestinationEntries: &[]teams.DestinationEntry{{DestinationID: dest.PublicID}}})
		teamSaved <- err
	}()
	eventually(t, "the team save ending or waiting", func() bool {
		return len(teamSaved) == 1 || lockWaits(t, e.d, "%-- name: LockEntryDestination %") == 1 ||
			lockWaits(t, e.d, "%-- name: ResolveMentionConnections %") == 1
	})

	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	ended(t, map[string]<-chan error{"the activation": activated, "the destination save": destSaved,
		"the team save": teamSaved})
	if t.Failed() {
		return
	}
	e.onNewKey(t)
	if n := countRows(t, e.d, `SELECT count(*) FROM team_mentions WHERE org_id = $1 AND team_id = $2`, e.org,
		team.ID); n != 2 {
		t.Errorf("%d team mentions", n)
	}
	if n := countRows(t, e.d, `SELECT connection_id FROM destinations WHERE org_id = $1 AND id = $2`, e.org,
		dest.ID); n != c3.id {
		t.Errorf("the destination is on connection %d, want %d", n, c3.id)
	}
	if n := countRows(t, e.d, `SELECT count(*) FROM team_destinations WHERE org_id = $1 AND team_id = $2`, e.org,
		team.ID); n != 1 {
		t.Errorf("%d destinations of the team", n)
	}
}

// webhooks runs an activation with a transaction that locks two outgoing webhooks in id order with the query of a
// Team save that names them (LockEntryDestination). The activation re-encrypts the proxy password of the second and
// the Signing secret of the first, which a field-by-field walk locks second first. The transaction holds the first
// when the activation starts, then takes the second.
func webhooks(t *testing.T, e *lockEnv) {
	ctx := t.Context()
	w1, w2 := e.webhook(t, "first", e.cli, nil), e.webhook(t, "second", e.onB, e.cli)
	tx, err := e.d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	save := mentionsdb.New(tx)
	if _, err := save.LockEntryDestination(ctx, mentionsdb.LockEntryDestinationParams{OrgID: e.org,
		ID: w1}); err != nil {
		t.Fatal(err)
	}
	activated := e.activate(t, "%FROM destinations%FOR NO KEY UPDATE%")
	saved := make(chan error, 1)
	go func() {
		_, err := save.LockEntryDestination(ctx, mentionsdb.LockEntryDestinationParams{OrgID: e.org, ID: w2})
		if err == nil {
			err = tx.Commit(ctx)
		}
		saved <- err
	}()
	ended(t, map[string]<-chan error{"the save": saved, "the activation": activated})
	if !t.Failed() {
		e.onNewKey(t)
	}
}

// messengers runs an activation with a save of a Mattermost Destination whose id is above an outgoing webhook's: the
// save locks its Destination and then its Connection FOR SHARE, which the activation holds while it waits for the
// webhook; the activation, which never locks a messenger Destination, then ends, and so does the save.
func messengers(t *testing.T, e *lockEnv) {
	ctx := t.Context()
	w := e.webhook(t, "hook", e.cli, nil)
	c := e.connection(t, "messenger", e.cli, nil)
	dest, err := e.dests.Create(ctx, destBy, mattermostInput(c, "alerts"))
	if err != nil {
		t.Fatal(err)
	}
	if dest.ID < w {
		t.Fatalf("the destination %d is below the webhook %d", dest.ID, w)
	}
	holder := e.hold(t, `SELECT id FROM destinations WHERE org_id = $1 AND id = $2 FOR SHARE`, e.org, w)
	activated := e.activate(t, "%FROM destinations%FOR NO KEY UPDATE%")
	saved := make(chan error, 1)
	go func() {
		_, err := e.dests.Update(ctx, destBy, dest.PublicID, nil, mattermostInput(c, "other"))
		saved <- err
	}()
	eventually(t, "the destination save waiting for its connection", func() bool {
		return lockWaits(t, e.d, "%-- name: LockMattermostConnection %") == 1
	})
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	ended(t, map[string]<-chan error{"the activation": activated, "the destination save": saved})
	if !t.Failed() {
		e.onNewKey(t)
	}
}

// users runs an activation that re-encrypts the offline tokens of two active OIDC Admins, the second of which every
// scan meets first: lower login, subject and public_id, and the first's row rewritten after it. While the activation
// waits for the first, held FOR SHARE, it holds no other user, as the changes that lock the active Admins in id order
// (LockActiveAdmins) need.
func users(t *testing.T, e *lockEnv) {
	ctx := t.Context()
	insert := func(publicID, login string) int64 {
		ct, key := e.seal(t, e.cli, "users.oidc_offline_token")
		var id int64
		if err := e.d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
			oidc_issuer, oidc_subject, oidc_offline_token_ciphertext, oidc_offline_token_key_id,
			oidc_offline_token_updated_at, created_at, updated_at)
			VALUES ($1, $2, $3, $3, 'admin', 'oidc', 'active', 'https://idp.example.org', $3, $4, $5, $6, $6, $6)
			RETURNING id`, e.org, publicID, login, ct, key, t0).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	u1, u2 := insert("SRZZZZZZZZZZZZ", "zoe"), insert("SRAAAAAAAAAAAA", "ada")
	if _, err := e.d.Pool.Exec(ctx, `UPDATE users SET name = 'Zoe' WHERE org_id = $1 AND id = $2`, e.org,
		u1); err != nil {
		t.Fatal(err)
	}
	holder := e.hold(t, `SELECT id FROM users WHERE org_id = $1 AND id = $2 FOR SHARE`, e.org, u1)
	activated := e.activate(t, "%FROM users%FOR NO KEY UPDATE%")
	probe := e.hold(t, `SELECT 1`)
	if _, err := probe.Exec(ctx, `SELECT id FROM users WHERE org_id = $1 AND id = $2 FOR NO KEY UPDATE NOWAIT`, e.org,
		u2); err != nil {
		t.Errorf("the second admin while the activation waits for the first: %v", err)
	}
	if err := probe.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	ended(t, map[string]<-chan error{"the activation": activated})
	if !t.Failed() {
		e.onNewKey(t)
	}
}
