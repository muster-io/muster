// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package connections_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/connections"
	connectionsdb "github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/routing"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

// liveEnv is the Connections, the Destination write path and delivery against PostgreSQL, with the fake Mattermost
// server and the real interactive path.
type liveEnv struct {
	d     *db.DB
	orgID int64
	log   *bytes.Buffer
	fake  *fakemattermost.Fake
	conns *connections.Service
	dests *destinations.Service
}

func setupLiveEnv(t *testing.T, s dbtest.Server) *liveEnv {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	var log bytes.Buffer
	logger := logging.New(&log, logging.LevelInfo)
	if err := d.Migrate(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"audit_log", "delivery_events"} {
		if _, err := d.Pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s_p202610 PARTITION OF %s
			FOR VALUES FROM ('2026-10-01T00:00:00Z') TO ('2026-11-01T00:00:00Z')`, table, table)); err != nil {
			t.Fatal(err)
		}
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := routing.EnsureDefault(ctx, routing.NewStore(d.Pool), org.ID, t0); err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.New([][]byte{bytes.Repeat([]byte{'k'}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := keys.Establish(ctx, keyring.NewStore(d.Pool), t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Open(ctx, logger, state); err != nil {
		t.Fatal(err)
	}
	f := fakemattermost.New()
	if err := f.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(ctx)) })
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	business := clock.NewManual(t0)
	clocks := clock.Clocks{Business: business, Real: clock.Real{}}
	w := audit.NewWriter(logger, business)
	svc := delivery.New(delivery.Config{OrgID: org.ID, Store: delivery.NewStore(d.Pool, d.Pool), Business: business,
		Log: logger})
	e := &liveEnv{d: d, orgID: org.ID, log: &log, fake: f}
	e.conns = connections.New(connections.Config{OrgID: org.ID, Store: connections.NewStore(d.Pool), Keyring: keys,
		Audit: w, Clocks: clocks, Log: logger,
		Network:     mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}},
		Interactive: &delivery.Interactive{OrgID: org.ID, Store: delivery.NewStore(d.Pool, d.Pool), Clocks: clocks},
		Abandon: func(ctx context.Context, tx connectionsdb.DBTX, id int64) (func(context.Context), error) {
			return svc.AbandonConnection(ctx, tx, id)
		}})
	e.dests = destinations.New(org.ID, destinations.NewStore(d.Pool))
	e.dests.SetWriter(destinations.WriterConfig{Writer: destinations.NewWriter(d.Pool), Audit: w, Business: business,
		Mentions: mentions.New(org.ID, d.Pool), Mattermost: e.conns})
	return e
}

func (e *liveEnv) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func quiet() mentions.Settings {
	set := mentions.Settings{}
	for _, k := range mentions.Kinds {
		set[k] = mentions.Setting{Everyone: "none", UserIDs: []string{}, Groups: []string{}}
	}
	return set
}

func destinationInput(name, conn, channel string) destinations.Input {
	return destinations.Input{Type: destinations.TypeMattermost, Name: name, Mentions: quiet(),
		Limiter:    destinations.Limiter{Limit: 1000, PerSeconds: 1},
		Mattermost: &destinations.MattermostInput{Connection: conn, TeamID: fakemattermost.TeamID, ChannelID: channel}}
}

// TestLive runs the Connections against PostgreSQL 14 and 17.
func TestLive(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupLiveEnv(t, s)
		t.Run("crud", e.crud)
		t.Run("deleteConnection", e.deleteConnection)
		t.Run("demo", e.demo)
	})
}

// crud is C-13.FR-1 and FR-2 on the real store: a Connection created, checked, read, listed and updated; a name that
// another Connection that is not deleted has is refused by the unique index.
func (e *liveEnv) crud(t *testing.T) {
	ctx := t.Context()
	in := input("crud", e.fake.URL())
	in.Limiter = connections.Limiter{Limit: 1000, PerSeconds: 1}
	c, err := e.conns.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.conns.Check(ctx, c.PublicID)
	if err != nil || !res.OK || res.BotName != fakemattermost.BotUsername {
		t.Fatalf("check = %+v, %v", res, err)
	}
	got, err := e.conns.Get(ctx, c.PublicID)
	if err != nil || got.BotUsername == nil || *got.BotUsername != fakemattermost.BotUsername || got.Version != 1 ||
		!got.BotToken.Set {
		t.Errorf("get = %+v, %v", got, err)
	}
	if _, err := e.conns.Create(ctx, by, in); !errors.Is(err, connections.ErrNameTaken) {
		t.Errorf("a taken name = %v", err)
	}
	in.Name, in.BotToken = "crud-renamed", keyring.Keep
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("socks5"), Address: new("127.0.0.1:1080")}
	u, err := e.conns.Update(ctx, by, c.PublicID, &got.Version, in)
	if err != nil || u.Name != "crud-renamed" || u.Version != 2 || u.BotUsername == nil || !u.Proxy.Enabled {
		t.Errorf("update = %+v, %v", u, err)
	}
	mm := connections.TypeMattermost
	page, err := e.conns.List(ctx, connections.ListFilter{Type: &mm, Limit: 10})
	if err != nil || len(page.Connections) == 0 {
		t.Errorf("list = %+v, %v", page, err)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log WHERE resource_public_id = $1`, c.PublicID); n != 2 {
		t.Errorf("%d Audit log entries", n)
	}
	if err := e.conns.Delete(ctx, by, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
}

// deleteConnection is C-13.FR-6 and C-11.FR-14: a Connection that a Mattermost Destination uses is in use; once the
// Destination is deleted, deleting the Connection soft-deletes it and wipes its secrets, and its final edit still
// pending ends as Not delivered without a request to the server.
func (e *liveEnv) deleteConnection(t *testing.T) {
	ctx := t.Context()
	in := input("mm-old", e.fake.URL())
	in.Limiter = connections.Limiter{Limit: 1000, PerSeconds: 1}
	c, err := e.conns.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	dby := destinations.Requester{Actor: by.Actor, Transport: by.Transport}
	_, err = e.dests.Create(ctx, dby, destinationInput("nobot", c.PublicID, fakemattermost.ChannelNoBot))
	if cf, ok := errors.AsType[*destinations.CheckFailedError](err); !ok || len(cf.Items) != 1 ||
		cf.Items[0].Pointer != "/channel_id" || cf.Items[0].Message != mattermost.MessageNotMember {
		t.Fatalf("a channel without the bot = %v", err)
	}
	d, err := e.dests.Create(ctx, dby, destinationInput("old", c.PublicID, fakemattermost.ChannelAlertsProd))
	if err != nil || *d.MattermostTeamName != "dev" || *d.MattermostChannelName != "alerts-prod" {
		t.Fatalf("create the destination = %+v, %v", d, err)
	}
	if n := e.count(t, `SELECT count(*) FROM destinations WHERE connection_id = $1`, c.ID); n != 1 {
		t.Fatalf("%d destinations saved", n)
	}
	target, err := e.conns.Target(ctx, d.ID)
	if err != nil || target.ConnectionID != c.ID || target.ConnectionPublicID != c.PublicID ||
		target.TeamID != fakemattermost.TeamID || target.TeamName != "dev" ||
		target.ChannelID != fakemattermost.ChannelAlertsProd {
		t.Fatalf("target = %+v, %v", target, err)
	}
	if out := (&mattermost.Adapter{Targets: e.conns}).Check(ctx, delivery.Call{Class: outbound.ClassDelivery,
		Destination: delivery.Destination{ID: d.ID}}); out.Kind != delivery.OutcomeOK {
		t.Errorf("the adapter's check = %+v", out)
	}
	if err := e.conns.Delete(ctx, by, c.PublicID, nil); !errors.Is(err, connections.ErrInUse) {
		t.Fatalf("delete a used connection = %v", err)
	}
	if got, err := e.conns.Get(ctx, c.PublicID); err != nil || got.DestinationCount != 1 {
		t.Errorf("destination_count = %+v, %v", got, err)
	}
	var storm, deliveryID int64
	if err := e.d.Pool.QueryRow(ctx, `INSERT INTO storms (org_id, route_id, started_at)
		SELECT $1, id, $2 FROM routes WHERE org_id = $1 AND is_default RETURNING id`, e.orgID, t0).Scan(
		&storm); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Pool.QueryRow(ctx, `INSERT INTO deliveries (org_id, destination_id, storm_id, state, desired_version,
		desired_text, desired_retire, actual_version, message_id, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, $3, 'pending', 2, 'final', true, 1, 'post-1', $4, $4, $4) RETURNING id`, e.orgID, d.ID, storm,
		t0).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	if err := e.dests.Delete(ctx, dby, d.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	// A deleted Destination still posts its final edit through its Connection.
	if again, err := e.conns.Target(ctx, d.ID); err != nil || again.Client != target.Client {
		t.Errorf("the target of a deleted destination = %+v, %v", again, err)
	}
	e.fake.ResetRequests()
	e.log.Reset()
	if err := e.conns.Delete(ctx, by, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	var state, lastError string
	var retire bool
	if err := e.d.Pool.QueryRow(ctx, `SELECT state, last_error, desired_retire FROM deliveries WHERE id = $1`,
		deliveryID).Scan(&state, &lastError, &retire); err != nil {
		t.Fatal(err)
	}
	if state != "not_delivered" || lastError != "the Connection was deleted" || retire {
		t.Errorf("delivery %s %q retire=%v", state, lastError, retire)
	}
	if n := e.count(t, `SELECT count(*) FROM delivery_events WHERE destination_id = $1 AND storm_id = $2
		AND kind = 'not_delivered'`, d.ID, storm); n != 1 {
		t.Errorf("%d not_delivered events", n)
	}
	if n := e.count(t, `SELECT count(*) FROM connections WHERE id = $1 AND deleted_at IS NOT NULL
		AND bot_token_ciphertext IS NULL AND bot_token_key_id IS NULL`, c.ID); n != 1 {
		t.Error("the connection is not deleted with its secrets wiped")
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log WHERE resource_public_id = $1 AND action = $2`, c.PublicID,
		connections.ActionDeleted); n != 1 {
		t.Errorf("%d connection.deleted entries", n)
	}
	if reqs := e.fake.Requests(); len(reqs) != 0 {
		t.Errorf("the deletion called the server: %+v", reqs)
	}
	if !strings.Contains(e.log.String(), `"event":"delivery_not_delivered"`) {
		t.Errorf("log %s", e.log)
	}
	if _, err := e.conns.Get(ctx, c.PublicID); !errors.Is(err, connections.ErrNotFound) {
		t.Errorf("read a deleted connection = %v", err)
	}
	if _, err := e.conns.Target(ctx, d.ID); !errors.Is(err, mattermost.ErrNoTarget) {
		t.Errorf("the target of a deleted connection = %v", err)
	}
	t.Logf("deleted connection %s: delivery %d not_delivered (%q), no request to the server", c.PublicID, deliveryID,
		lastError)
}

// demo is C-01.FR-13: the demo Connection is created once, however often `muster dev` starts.
func (e *liveEnv) demo(t *testing.T) {
	d := connections.Demo{Name: "Dev Mattermost", ServerURL: e.fake.URL(), BotToken: botToken}
	for range 2 {
		if err := e.conns.EnsureDemo(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM connections WHERE name = 'Dev Mattermost' AND deleted_at IS NULL`); n != 1 {
		t.Errorf("%d demo connections", n)
	}
}
