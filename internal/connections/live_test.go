// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package connections_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/connections"
	connectionsdb "github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/slack"
	"github.com/muster-io/muster/internal/telegram"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

// liveEnv is the Connections, the Destination write path and delivery against PostgreSQL, with the fake Mattermost
// server and the real interactive path; slackBase is where Slack Connections' requests go, the fake Slack server once
// a test starts one.
type liveEnv struct {
	d         *db.DB
	orgID     int64
	log       *bytes.Buffer
	fake      *fakemattermost.Fake
	business  *clock.Manual
	conns     *connections.Service
	dests     *destinations.Service
	slackBase string
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
	e := &liveEnv{d: d, orgID: org.ID, log: &log, fake: f, business: business}
	e.conns = connections.New(connections.Config{OrgID: org.ID, Store: connections.NewStore(d.Pool), Keyring: keys,
		Audit: w, Clocks: clocks, Log: logger, IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081"},
		Network:     mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}},
		Interactive: &delivery.Interactive{OrgID: org.ID, Store: delivery.NewStore(d.Pool, d.Pool), Clocks: clocks},
		SlackRoute:  func(logging.Secret) string { return e.slackBase },
		Abandon: func(ctx context.Context, tx connectionsdb.DBTX, id int64) (func(context.Context), error) {
			return svc.AbandonConnection(ctx, tx, id)
		}})
	e.dests = destinations.New(org.ID, destinations.NewStore(d.Pool))
	e.dests.SetWriter(destinations.WriterConfig{Writer: destinations.NewWriter(d.Pool), Audit: w, Business: business,
		Mentions: mentions.New(org.ID, d.Pool), Mattermost: e.conns, Telegram: e.conns, Bridge: e.conns,
		Slack: e.conns})
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
		t.Run("demoRename", e.demoRename)
		t.Run("telegram", e.telegram)
		t.Run("telegramDestinations", e.telegramDestinations)
		t.Run("telegramGroupPeople", e.telegramGroupPeople)
		t.Run("telegramCheckLimiter", e.telegramCheckLimiter)
		t.Run("mattermostCheckLimiter", e.mattermostCheckLimiter)
		t.Run("bridge_event_claim", e.bridgeEventClaim)
		t.Run("bridgeCheckLimiter", e.bridgeCheckLimiter)
		t.Run("slack", e.slack)
		t.Run("slackCheckLimiter", e.slackCheckLimiter)
	})
}

// slack is C-24.FR-3 on the real store against the fake Slack server: a checked Slack Connection's Destination is
// refused for a channel without the bot, with the bot's stored name, and saved for one with it, with the channel id as
// entered and the channel's name, which the reads return; an update stores a new name; checkDestination, the channel
// list and muster doctor find the Destination; the Destination keeps its Connection in use until it is deleted.
func (e *liveEnv) slack(t *testing.T) {
	ctx := t.Context()
	f := fakeslack.New()
	if err := f.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(ctx)) })
	e.slackBase = f.URL() + "/api/"
	t.Cleanup(func() { e.slackBase = "" })
	c, err := e.conns.Create(ctx, by, connections.Input{Type: connections.TypeSlack, Name: "live-sl",
		EventMode: connections.EventModeSocket, BotToken: keyring.Replace(fakeslack.SecondBotToken),
		AppToken: keyring.Replace(fakeslack.SecondAppToken), Limiter: connections.Limiter{Limit: 1000, PerSeconds: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := e.conns.Check(ctx, c.PublicID, nil); err != nil || !res.OK {
		t.Fatalf("check = %+v, %v", res, err)
	}
	dby := destinations.Requester{Actor: by.Actor, Transport: by.Transport}
	in := destinations.Input{Type: destinations.TypeSlack, Name: "live-sl", Mentions: quiet(),
		Limiter: destinations.Limiter{Limit: 30, PerSeconds: 60},
		Slack:   &destinations.SlackInput{Connection: c.PublicID, ChannelID: fakeslack.ChannelOther}}
	_, err = e.dests.Create(ctx, dby, in)
	if failed, ok := errors.AsType[*destinations.CheckFailedError](err); !ok ||
		failed.Items[0].Code != slack.CodeBotNotInChannel ||
		failed.Items[0].Message != "Invite the bot to the channel: /invite @muster-dev-bot-b" {
		t.Fatalf("a channel without the bot = %v", err)
	}
	in.Slack.ChannelID = fakeslack.ChannelAlerts
	d, err := e.dests.Create(ctx, dby, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.dests.Get(ctx, d.PublicID)
	if err != nil || got.Type != destinations.TypeSlack || *got.SlackChannelID != fakeslack.ChannelAlerts ||
		*got.SlackChannelName != "alerts" || *got.Connection != c.PublicID || got.MattermostChannelID != nil ||
		e.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND type = 'slack' AND slack_channel_id = $2
			AND slack_channel_name = 'alerts' AND connection_id = $3`, d.ID, fakeslack.ChannelAlerts, c.ID) != 1 {
		t.Fatalf("stored %+v, %v", got, err)
	}
	typ := destinations.TypeSlack
	page, err := e.dests.List(ctx, destinations.ListFilter{Type: &typ, Limit: 10})
	if err != nil || len(page.Destinations) != 1 || *page.Destinations[0].SlackChannelName != "alerts" {
		t.Fatalf("list = %+v, %v", page, err)
	}
	cfg := fakeslack.DefaultConfig()
	cfg.Channels[fakeslack.AppB][fakeslack.ChannelAlerts] = fakeslack.Channel{Name: "alerts-renamed", Member: true}
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	in.Name = "live-sl-renamed"
	if d, err := e.dests.Update(ctx, dby, d.PublicID, nil, in); err != nil || *d.SlackChannelName != "alerts-renamed" ||
		e.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND name = 'live-sl-renamed'
			AND slack_channel_name = 'alerts-renamed'`, d.ID) != 1 {
		t.Fatalf("update = %+v, %v", d, err)
	}
	if res, err := e.dests.Check(ctx, d.PublicID); err != nil || !res.OK || len(res.Items) != 1 ||
		res.Items[0].Name != slack.ItemChannel {
		t.Fatalf("checkDestination = %+v, %v", res, err)
	}
	if list, err := e.conns.SlackChannels(ctx, c.PublicID, ""); err != nil || len(list.Channels) != 1 ||
		list.Channels[0].Name != "alerts-renamed" {
		t.Fatalf("channels = %+v, %v", list, err)
	}
	found, err := e.conns.Doctor(ctx, connectionsdb.New(e.d.Pool), 2*time.Second)
	if err != nil || !slices.ContainsFunc(found, func(f connections.Finding) bool {
		return f.Kind == connections.FindingDestination && f.Name == "live-sl-renamed" && f.OK()
	}) || !slices.ContainsFunc(found, func(f connections.Finding) bool {
		return f.Kind == connections.FindingConnection && f.Name == "live-sl" && f.OK()
	}) {
		t.Fatalf("doctor = %+v, %v", found, err)
	}
	if err := e.conns.Delete(ctx, by, c.PublicID, nil); !errors.Is(err, connections.ErrInUse) {
		t.Fatalf("delete in use = %v", err)
	}
	if err := e.dests.Delete(ctx, dby, d.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.conns.Delete(ctx, by, c.PublicID, nil); err != nil {
		t.Fatalf("delete = %v", err)
	}
}

// bridgeEventClaim is the claim of Bridge events on the real store (C-22.FR-11): two copies of one event handled at
// the same time run one after the other under the transaction advisory lock of the Connection and the event id, and
// only the first runs f; an error of f records nothing; the records are pruned once bridge.event_dedup_window passed on
// the business clock; the poller's list carries the cursor, which a poller of an earlier version cannot store; the push
// callback reads the Connection with its mode.
func (e *liveEnv) bridgeEventClaim(t *testing.T) {
	ctx := t.Context()
	in := connections.Input{Type: connections.TypeBridge, Name: "live-br", BridgeBaseURL: "http://127.0.0.1:1",
		EventDelivery: connections.EventDeliveryPoll, BotToken: keyring.Replace("live-bridge-token"),
		Limiter: connections.Limiter{Limit: 10, PerSeconds: 1}}
	c, err := e.conns.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	locked := func() bool {
		t.Helper()
		var free bool
		if err := e.d.Pool.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1::int,
			hashtext(format('%s/%s', $2::bigint, $3::text)))`, 0x6d75_0007, c.ID, "ev-1").Scan(&free); err != nil {
			t.Fatal(err)
		}
		return !free
	}
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := e.conns.HandleEventOnce(ctx, c.ID, "ev-1", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
		first <- err
	}()
	<-entered
	second := make(chan bool, 1)
	go func() {
		ran, _ := e.conns.HandleEventOnce(ctx, c.ID, "ev-1", func(context.Context) error { return nil })
		second <- ran
	}()
	waitUntil(t, "the second copy waiting for the claim's lock", func() bool {
		return e.count(t, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
			AND classid = $1::int::oid AND objsubid = 2`, 0x6d75_0007) == 1
	})
	select {
	case <-second:
		t.Fatal("the second copy did not wait for the first")
	default:
	}
	if !locked() {
		t.Fatal("the claim lock is free while the event is handled")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if <-second {
		t.Fatal("the same event was handled twice")
	}
	if locked() {
		t.Fatal("the claim lock is held while no event is handled")
	}
	failed := errors.New("the dispatcher is down")
	if ran, err := e.conns.HandleEventOnce(ctx, c.ID, "ev-2", func(context.Context) error { return failed }); ran ||
		!errors.Is(err, failed) {
		t.Fatalf("a failing event = %v, %v", ran, err)
	}
	if n := e.count(t, `SELECT count(*) FROM bridge_events_seen WHERE connection_id = $1 AND event_id = 'ev-2'`,
		c.ID); n != 0 {
		t.Fatal("a failed event was recorded")
	}
	if ran, err := e.conns.HandleEventOnce(ctx, c.ID, "ev-2", func(context.Context) error { return nil }); !ran ||
		err != nil {
		t.Fatalf("the event again = %v, %v", ran, err)
	}
	if n := e.count(t, `SELECT count(*) FROM bridge_events_seen WHERE org_id = $1 AND connection_id = $2
		AND seen_at = $3`, e.orgID, c.ID, e.business.Now()); n != 2 {
		t.Fatalf("%d events recorded at the business clock's now", n)
	}
	if n, err := e.conns.PruneEventsSeen(ctx, e.business.Now().Add(bridge.EventDedupWindow), 1000); n != 0 ||
		err != nil {
		t.Errorf("prune within the window = %d, %v", n, err)
	}
	if n, err := e.conns.PruneEventsSeen(ctx, e.business.Now().Add(bridge.EventDedupWindow+time.Second), 1); n != 1 ||
		err != nil {
		t.Errorf("prune past the window, limit 1 = %d, %v", n, err)
	}
	if n, err := e.conns.PruneEventsSeen(ctx, e.business.Now().Add(bridge.EventDedupWindow+time.Second), 1000); n != 1 ||
		err != nil {
		t.Errorf("prune the rest = %d, %v", n, err)
	}

	polled := func(t *testing.T) (bridge.Polled, bool) {
		t.Helper()
		list, err := e.conns.PollingConnections(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			if p.Conn.ID == c.ID {
				return p, true
			}
		}
		return bridge.Polled{}, false
	}
	p, ok := polled(t)
	if !ok || p.Cursor != nil || p.Conn.Version != c.Version {
		t.Fatalf("polling = %+v, %v", p, ok)
	}
	next := "cursor-1"
	if err := e.conns.StoreEventsCursor(ctx, p.Conn, &next); err != nil {
		t.Fatal(err)
	}
	in.Name, in.BotToken = "live-br-renamed", keyring.Keep
	if _, err := e.conns.Update(ctx, by, c.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	stale := "cursor-stale"
	if err := e.conns.StoreEventsCursor(ctx, p.Conn, &stale); err != nil {
		t.Fatal(err)
	}
	if p, ok = polled(t); !ok || p.Cursor == nil || *p.Cursor != "cursor-1" || p.Conn.Version != c.Version+1 {
		t.Fatalf("polling after a save = %+v, %v", p, ok)
	}
	if err := e.conns.StoreEventsCursor(ctx, p.Conn, nil); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM connections WHERE id = $1 AND bridge_events_cursor IS NULL`,
		c.ID); n != 1 {
		t.Error("the cursor was not dropped")
	}
	if conn, mode, err := e.conns.PushConnection(ctx, c.PublicID); err != nil || conn.ID != c.ID ||
		mode != connections.EventDeliveryPoll {
		t.Errorf("push connection = %+v, %q, %v", conn, mode, err)
	}
	if err := e.conns.Delete(ctx, by, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.conns.PushConnection(ctx, c.PublicID); !errors.Is(err, bridge.ErrNoConnection) {
		t.Errorf("a deleted Connection = %v", err)
	}
	if _, ok := polled(t); ok {
		t.Error("a deleted Connection is polled")
	}
}

// telegramDestinations is C-14.FR-2 and FR-14 on the real store: a Telegram Destination saved from its channel alone
// stores what its check found, the adapter's Target and muster doctor read it, a post goes to the channel, and its
// Connection cannot be deleted while it exists.
func (e *liveEnv) telegramDestinations(t *testing.T) {
	ctx := t.Context()
	f := faketelegram.New()
	if err := f.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(ctx)) })
	c, err := e.conns.Create(ctx, by, connections.Input{Type: connections.TypeTelegram, Name: "live-tg-dest",
		BotAPIBaseURL: f.URL(), UpdateMode: connections.ModeLongPolling, BotToken: keyring.Replace("777001:live-dest"),
		Limiter: connections.Limiter{Limit: 1000, PerSeconds: 1}})
	if err != nil {
		t.Fatal(err)
	}
	dby := destinations.Requester{Actor: by.Actor, Transport: by.Transport}
	in := destinations.Input{Type: destinations.TypeTelegram, Name: "live-tg", Mentions: quiet(),
		Limiter:  destinations.Limiter{Limit: 10, PerSeconds: 60},
		Telegram: &destinations.TelegramInput{Connection: c.PublicID, ChannelID: "@no_comments"}}
	if _, err := e.dests.Create(ctx, dby, in); !errors.As(err, new(*destinations.CheckFailedError)) {
		t.Fatalf("no comments = %v", err)
	}
	in.Telegram.ChannelID = "@muster_alerts"
	d, err := e.dests.Create(ctx, dby, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.dests.Get(ctx, d.PublicID)
	if err != nil || *got.TelegramDiscussionGroupID != "-1001000000002" ||
		*got.TelegramDiscussionGroupTitle != faketelegram.GroupTitle || *got.TelegramChannelID != "@muster_alerts" ||
		e.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND telegram_channel_chat_id = $2`, d.ID,
			faketelegram.ChannelID) != 1 {
		t.Fatalf("stored %+v, %v", got, err)
	}
	in.Name = "live-tg-renamed"
	if _, err := e.dests.Update(ctx, dby, d.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	target, err := e.conns.TelegramTarget(ctx, d.ID)
	if err != nil || target.ChannelID != faketelegram.ChannelID || target.GroupID != faketelegram.GroupID {
		t.Fatalf("target %+v, %v", target, err)
	}
	a := &telegram.Adapter{Targets: e.conns}
	call := delivery.Call{Class: outbound.ClassDelivery, Destination: delivery.Destination{ID: d.ID,
		Type: delivery.TypeTelegram}}
	if o := a.Publish(ctx, call, messages.Message{Language: "en", Colour: messages.ColourFiring,
		Heading: &messages.Heading{Number: 1, Title: "live"}, Buttons: []messages.Button{}}); o.Kind !=
		delivery.OutcomeOK || len(f.Messages(faketelegram.ChannelID)) != 1 {
		t.Fatalf("publish = %+v", o)
	}
	found, err := e.conns.Doctor(ctx, connectionsdb.New(e.d.Pool), 2*time.Second)
	if err != nil || !slices.ContainsFunc(found, func(f connections.Finding) bool {
		return f.Kind == connections.FindingDestination && f.Name == "live-tg-renamed" && f.OK()
	}) {
		t.Fatalf("doctor = %+v, %v", found, err)
	}
	if err := e.conns.Delete(ctx, by, c.PublicID, nil); !errors.Is(err, connections.ErrInUse) {
		t.Fatalf("delete in use = %v", err)
	}
}

// telegramGroupPeople is the step discussion_group_members of C-27.FR-8 on the real store and the real limiters: a
// Telegram Destination with the default limiter, 10 requests a minute, of a Team with 10 active people linked to
// Telegram and a disabled one; "Check" asks getChatMember about each active person — not the disabled one — although
// the Destination's bucket holds 10 tokens, since the check takes its tokens from the Connection's bucket alone; the
// people the fake answers 400 PARTICIPANT_ID_INVALID are absent, and the
// Destination stores them, but not once a save moved it to another group than the one the check read.
func (e *liveEnv) telegramGroupPeople(t *testing.T) {
	ctx := t.Context()
	f := faketelegram.New()
	if err := f.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(ctx)) })
	c, err := e.conns.Create(ctx, by, connections.Input{Type: connections.TypeTelegram, Name: "live-tg-members",
		BotAPIBaseURL: f.URL(), UpdateMode: connections.ModeLongPolling, BotToken: keyring.Replace("777001:live-mem"),
		Limiter: connections.Limiter{Limit: 100, PerSeconds: 60}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.dests.Create(ctx, destinations.Requester{Actor: by.Actor, Transport: by.Transport},
		destinations.Input{Type: destinations.TypeTelegram, Name: "live-tg-members", Mentions: quiet(),
			Limiter:  destinations.Limiter{Limit: 10, PerSeconds: 60},
			Telegram: &destinations.TelegramInput{Connection: c.PublicID, ChannelID: "@muster_alerts"}})
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.d.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO teams (org_id, public_id, name, slug, own_working_hours, created_at, updated_at)
		VALUES ($1, 'TM00000000MEMB', 'members', 'members', false, $2, $2)`, e.orgID, t0)
	exec(`INSERT INTO team_destinations (org_id, team_id, destination_id, severity_condition, added_at)
		SELECT $1, id, $2, 'any', $3 FROM teams WHERE public_id = 'TM00000000MEMB'`, e.orgID, d.ID, t0)
	var want []string
	for i := range 11 {
		status, tg := "active", int64(9001+i)
		if i == 10 {
			status = "disabled"
		}
		id := fmt.Sprintf("%012d", i+1)
		exec(`WITH u AS (INSERT INTO users (org_id, public_id, login, name, role, source, status, created_at, updated_at)
				VALUES ($1, 'SR' || $2, 'm' || $2, 'm' || $2, 'responder', 'local', $3, $4, $4) RETURNING id),
			tu AS (INSERT INTO team_users (org_id, team_id, user_id, by_hand, created_at)
				SELECT $1, t.id, u.id, true, $4 FROM teams t, u WHERE t.public_id = 'TM00000000MEMB')
			INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
				created_at)
			SELECT $1, 'AK' || $2, u.id, 'telegram', $5, $6, 'm' || $2, $4 FROM u`, e.orgID, id, status, t0, c.ID,
			strconv.FormatInt(tg, 10))
		if i%2 == 0 && i < 10 {
			if err := f.SetMember(faketelegram.GroupID, tg, faketelegram.Member{Status: faketelegram.StatusMember}); err != nil {
				t.Fatal(err)
			}
		} else if i < 10 {
			want = append(want, "m"+id)
		}
	}
	f.ResetRequests()
	res, err := e.dests.Check(ctx, d.PublicID)
	if err != nil || !res.OK {
		t.Fatalf("check = %+v, %v", res, err)
	}
	last := res.Items[len(res.Items)-1]
	var absent []string
	for _, u := range last.AbsentUsers {
		absent = append(absent, u.Login)
	}
	if last.Name != telegram.StepDiscussionGroupMembers || !slices.Equal(absent, want) {
		t.Errorf("absent %v, want %v", absent, want)
	}
	var asked []string
	for _, r := range f.Requests() {
		if strings.HasSuffix(r.Path, "/getChatMember") && strings.Contains(r.Body, `"chat_id":-1001000000002`) &&
			!strings.Contains(r.Body, fmt.Sprintf(`"user_id":%d`, faketelegram.BotID)) {
			asked = append(asked, r.Body)
		}
	}
	if len(asked) != 10 || slices.ContainsFunc(asked, func(b string) bool { return strings.Contains(b, "9011") }) {
		t.Errorf("asked about %d people: %v", len(asked), asked)
	}
	// A bucket is created full on its first use, which a check is not.
	if n := e.count(t, `SELECT count(*) FROM rate_limit_buckets WHERE subject_kind = 'destination' AND subject_id = $1
		AND tokens < 10`, d.ID); n != 0 {
		t.Errorf("the check took tokens of the Destination's bucket")
	}
	if n := e.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND cardinality(telegram_absent_user_ids) = 5
		AND telegram_members_checked_at IS NOT NULL`, d.ID); n != 1 {
		t.Errorf("stored %d", n)
	}
	// The checks below would wait for the tokens that the default limiters refill slowly; each starts with full buckets.
	refill := func() {
		t.Helper()
		exec(`UPDATE rate_limit_buckets SET tokens = 10 WHERE subject_kind = 'destination' AND subject_id = $1`, d.ID)
		exec(`UPDATE rate_limit_buckets SET tokens = 100 WHERE subject_kind = 'connection' AND subject_id = $1`, c.ID)
	}
	refill()
	// A check that cannot tell an absent person — Telegram answers another error about m000000000002 — keeps their
	// absence while the item names only who it saw outside; a definitive "in the group" ends it.
	if err := f.SetMember(faketelegram.GroupID, 9002, faketelegram.Member{ErrorCode: 400,
		Description: "Bad Request: user not found"}); err != nil {
		t.Fatal(err)
	}
	stored := func() []string {
		t.Helper()
		var logins []string
		if err := e.d.Pool.QueryRow(ctx, `SELECT array(SELECT u.login FROM users u
			WHERE u.id = ANY(d.telegram_absent_user_ids) ORDER BY u.login) FROM destinations d WHERE d.id = $1`,
			d.ID).Scan(&logins); err != nil {
			t.Fatal(err)
		}
		return logins
	}
	if res, err := e.dests.Check(ctx, d.PublicID); err != nil || !res.OK ||
		len(res.Items[len(res.Items)-1].AbsentUsers) != 4 || !slices.Equal(stored(), want) {
		t.Errorf("an unknown answer: %+v, %v, stored %v", res.Items, err, stored())
	}
	if err := f.SetMember(faketelegram.GroupID, 9002, faketelegram.Member{Status: faketelegram.StatusMember}); err != nil {
		t.Fatal(err)
	}
	refill()
	if res, err := e.dests.Check(ctx, d.PublicID); err != nil || !res.OK ||
		!slices.Equal(stored(), want[1:]) {
		t.Errorf("a member again: %v, stored %v", err, stored())
	}
	// A save moved the Destination to another group while a check read the old one: the check fails and its write,
	// which names the group it read, leaves what the save stored.
	exec(`UPDATE destinations SET telegram_discussion_chat_id = -100999, telegram_absent_user_ids = '{42}'
		WHERE id = $1`, d.ID)
	refill()
	if res, err := e.dests.Check(ctx, d.PublicID); err != nil || res.OK {
		t.Fatalf("a check of a moved group = %+v, %v", res, err)
	}
	if n := e.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND telegram_absent_user_ids = '{42}'`,
		d.ID); n != 1 {
		t.Error("a check of another group than the stored one overwrote the members")
	}
}

// telegramCheckLimiter is C-14.FR-14 on the real limiters: the requests of a Telegram Destination's check post nothing,
// so they take no token of the Destination's bucket, which counts its sends and edits (F-016), only of the
// Connection's. Three "Check"s within a minute of a Destination with the default limiter, 10 a minute, all pass, and
// neither they nor a save leave the Destination's bucket lower; a Destination whose deliveries took every token
// still checks, and its deliveries keep the token that refills next.
func (e *liveEnv) telegramCheckLimiter(t *testing.T) {
	ctx := t.Context()
	f := faketelegram.New()
	if err := f.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(ctx)) })
	c, err := e.conns.Create(ctx, by, connections.Input{Type: connections.TypeTelegram, Name: "live-tg-check-limiter",
		BotAPIBaseURL: f.URL(), UpdateMode: connections.ModeLongPolling, BotToken: keyring.Replace("777001:live-lim"),
		Limiter: connections.Limiter{Limit: 1000, PerSeconds: 1}})
	if err != nil {
		t.Fatal(err)
	}
	dby := destinations.Requester{Actor: by.Actor, Transport: by.Transport}
	in := destinations.Input{Type: destinations.TypeTelegram, Name: "live-tg-check-limiter", Mentions: quiet(),
		Limiter:  destinations.Limiter{Limit: 10, PerSeconds: 60},
		Telegram: &destinations.TelegramInput{Connection: c.PublicID, ChannelID: "@muster_alerts"}}
	d, err := e.dests.Create(ctx, dby, in)
	if err != nil {
		t.Fatal(err)
	}
	// The business clock stands still, so the Destination's bucket refills nothing while the checks run.
	set := func(kind string, id int64, tokens float64) {
		t.Helper()
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO rate_limit_buckets (subject_kind, subject_id, org_id, tokens,
				refilled_at) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (subject_kind, subject_id) DO UPDATE SET tokens = excluded.tokens,
				refilled_at = excluded.refilled_at`, kind, id, e.orgID, tokens, e.business.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	bucket := func(tokens float64) { t.Helper(); set("destination", d.ID, tokens) }
	read := func(kind string, id int64) float64 {
		t.Helper()
		var n float64
		if err := e.d.Pool.QueryRow(ctx, `SELECT tokens FROM rate_limit_buckets WHERE subject_kind = $1
			AND subject_id = $2`, kind, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tokens := func() float64 { t.Helper(); return read("destination", d.ID) }
	bucket(10)
	set("connection", c.ID, 1000)
	for i := range 3 {
		if res, err := e.dests.Check(ctx, d.PublicID); err != nil || !res.OK {
			t.Fatalf("check %d = %+v, %v", i+1, res, err)
		}
	}
	if n := tokens(); n != 10 {
		t.Errorf("the Destination's bucket after three checks: %v tokens, want 10", n)
	}
	// The checks are limited all the same: by the Connection, whose bucket they took from.
	if n := read("connection", c.ID); n >= 1000 {
		t.Errorf("the Connection's bucket after three checks: %v tokens, want fewer than 1000", n)
	}
	if _, err := e.dests.Update(ctx, dby, d.PublicID, nil, in); err != nil {
		t.Fatalf("save = %v", err)
	}
	if n := tokens(); n != 10 {
		t.Errorf("the Destination's bucket after a save: %v tokens, want 10", n)
	}
	bucket(0)
	if res, err := e.dests.Check(ctx, d.PublicID); err != nil || !res.OK {
		t.Fatalf("a check of a Destination without tokens = %+v, %v", res, err)
	}
	if n := tokens(); n != 0 {
		t.Errorf("the Destination's bucket after a check without tokens: %v tokens, want 0", n)
	}
}

// mattermostCheckLimiter is checkLimiter for a Mattermost Destination (C-13.FR-10).
func (e *liveEnv) mattermostCheckLimiter(t *testing.T) {
	in := input("live-mm-check-limiter", e.fake.URL())
	in.Limiter = connections.Limiter{Limit: 1000, PerSeconds: 1}
	c, err := e.conns.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	e.checkLimiter(t, c.ID, destinationInput("live-mm-check-limiter", c.PublicID, fakemattermost.ChannelAlerts))
}

// slackCheckLimiter is checkLimiter for a Slack Destination (C-24.FR-3).
func (e *liveEnv) slackCheckLimiter(t *testing.T) {
	ctx := t.Context()
	f := fakeslack.New()
	if err := f.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(ctx)) })
	e.slackBase = f.URL() + "/api/"
	t.Cleanup(func() { e.slackBase = "" })
	c, err := e.conns.Create(ctx, by, connections.Input{Type: connections.TypeSlack, Name: "live-sl-check-limiter",
		EventMode: connections.EventModeSocket, BotToken: keyring.Replace(fakeslack.BotToken),
		AppToken: keyring.Replace(fakeslack.AppToken), Limiter: connections.Limiter{Limit: 1000, PerSeconds: 1}})
	if err != nil {
		t.Fatal(err)
	}
	e.checkLimiter(t, c.ID, destinations.Input{Type: destinations.TypeSlack, Name: "live-sl-check-limiter",
		Mentions: quiet(), Slack: &destinations.SlackInput{Connection: c.PublicID, ChannelID: fakeslack.ChannelAlerts}})
}

// bridgeCheckLimiter is checkLimiter for a Bridge Destination (C-22.FR-3).
func (e *liveEnv) bridgeCheckLimiter(t *testing.T) {
	ctx := t.Context()
	f := fakebridge.New()
	if err := f.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(ctx)) })
	c, err := e.conns.Create(ctx, by, connections.Input{Type: connections.TypeBridge, Name: "live-br-check-limiter",
		BridgeBaseURL: f.URL(), EventDelivery: connections.EventDeliveryPoll, BotToken: keyring.Replace(fakebridge.Token),
		Limiter: connections.Limiter{Limit: 1000, PerSeconds: 1}})
	if err != nil {
		t.Fatal(err)
	}
	e.checkLimiter(t, c.ID, destinations.Input{Type: destinations.TypeBridge, Name: "live-br-check-limiter",
		Mentions: quiet(), Bridge: &destinations.BridgeInput{Connection: c.PublicID, ChannelID: fakebridge.ChannelAlerts}})
}

// checkLimiter is telegramCheckLimiter for the Destination of in through the Connection conn, whose limiter allows 1000
// requests a second: the requests of its check post nothing, so they take no token of the Destination's bucket, which
// counts its sends and edits, only of the Connection's. Eleven "Check"s within a minute of a Destination whose limiter
// allows 10 a minute all pass, and neither they nor a save leave the Destination's bucket lower; a Destination whose
// deliveries took every token still checks, and its deliveries keep the token that refills next.
func (e *liveEnv) checkLimiter(t *testing.T, conn int64, in destinations.Input) {
	t.Helper()
	ctx := t.Context()
	in.Limiter = destinations.Limiter{Limit: 10, PerSeconds: 60}
	dby := destinations.Requester{Actor: by.Actor, Transport: by.Transport}
	d, err := e.dests.Create(ctx, dby, in)
	if err != nil {
		t.Fatal(err)
	}
	set := func(kind string, id int64, tokens float64) {
		t.Helper()
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO rate_limit_buckets (subject_kind, subject_id, org_id, tokens,
				refilled_at) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (subject_kind, subject_id) DO UPDATE SET tokens = excluded.tokens,
				refilled_at = excluded.refilled_at`, kind, id, e.orgID, tokens, e.business.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	read := func(kind string, id int64) float64 {
		t.Helper()
		var n float64
		if err := e.d.Pool.QueryRow(ctx, `SELECT tokens FROM rate_limit_buckets WHERE subject_kind = $1
			AND subject_id = $2`, kind, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The business clock stands still, so neither bucket refills while the checks run.
	set("destination", d.ID, 10)
	set("connection", conn, 1000)
	for i := range 11 {
		if res, err := e.dests.Check(ctx, d.PublicID); err != nil || !res.OK {
			t.Fatalf("check %d = %+v, %v", i+1, res, err)
		}
	}
	if n := read("destination", d.ID); n != 10 {
		t.Errorf("the Destination's bucket after eleven checks: %v tokens, want 10", n)
	}
	if n := read("connection", conn); n > 989 {
		t.Errorf("the Connection's bucket after eleven checks: %v tokens, want at most 989", n)
	}
	in.Name += "-saved"
	if _, err := e.dests.Update(ctx, dby, d.PublicID, nil, in); err != nil {
		t.Fatalf("save = %v", err)
	}
	if n := read("destination", d.ID); n != 10 {
		t.Errorf("the Destination's bucket after a save: %v tokens, want 10", n)
	}
	set("destination", d.ID, 0)
	if res, err := e.dests.Check(ctx, d.PublicID); err != nil || !res.OK {
		t.Fatalf("a check of a Destination without tokens = %+v, %v", res, err)
	}
	if n := read("destination", d.ID); n != 0 {
		t.Errorf("the Destination's bucket after a check without tokens: %v tokens, want 0", n)
	}
}

// telegram is C-14.FR-1 on the real store: a Telegram Connection created and switched to the webhook mode and back
// against the fake Bot API, the poller's list with the stored offset, HandleOnce holding the offset row so that a second
// router with the same update waits and skips it, and an offset that never goes back.
func (e *liveEnv) telegram(t *testing.T) {
	ctx := t.Context()
	f := faketelegram.New()
	if err := f.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(ctx)) })
	in := connections.Input{Type: connections.TypeTelegram, Name: "live-tg", BotAPIBaseURL: f.URL() + "/",
		UpdateMode: connections.ModeWebhook, BotToken: keyring.Replace("777001:live-token"),
		Limiter: connections.Limiter{Limit: 1000, PerSeconds: 1}}
	c, err := e.conns.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, secret, err := e.conns.Hooked(ctx, c.PublicID); err != nil || string(secret) != f.Webhooks()["777001:live-token"].SecretToken {
		t.Fatalf("hooked = %v", err)
	}
	in.UpdateMode, in.BotToken = connections.ModeLongPolling, keyring.Keep
	got, err := e.conns.Update(ctx, by, c.PublicID, nil, in)
	if err != nil || got.UpdateMode != connections.ModeLongPolling || len(f.Webhooks()) != 0 ||
		e.count(t, `SELECT count(*) FROM connections WHERE id = $1 AND telegram_webhook_secret_ciphertext IS NULL`,
			c.ID) != 1 {
		t.Fatalf("back to long polling = %+v, %v", got, err)
	}
	list, err := e.conns.Polling(ctx)
	if err != nil || len(list) != 1 || list[0].ID != c.ID || list[0].Offset != nil {
		t.Fatalf("polling = %+v, %v", list, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := e.conns.HandleOnce(ctx, c.ID, 10, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
		first <- err
	}()
	<-entered
	second := make(chan bool, 1)
	go func() {
		ran, _ := e.conns.HandleOnce(ctx, c.ID, 10, func(context.Context) error { return nil })
		second <- ran
	}()
	// The delivery worker's try of the update lock, shared and without waiting, finds it busy while the handler runs
	// and free after it (C-14.FR-5).
	free := func() bool {
		t.Helper()
		var ok bool
		if err := e.d.Pool.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock_shared($1::int, hashint8($2::bigint))`,
			db.TelegramUpdateLockClass, c.ID).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	select {
	case <-second:
		t.Fatal("the second router did not wait for the first")
	case <-time.After(200 * time.Millisecond):
	}
	if free() {
		t.Fatal("the update lock is free while a handler runs")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if <-second {
		t.Fatal("the same update was handled twice")
	}
	if !free() {
		t.Fatal("the update lock is busy while no update is handled")
	}
	var tokenAt time.Time
	if err := e.d.Pool.QueryRow(ctx, `SELECT bot_token_updated_at FROM connections WHERE id = $1`, c.ID).Scan(
		&tokenAt); err != nil {
		t.Fatal(err)
	}
	if err := connectionsdb.New(e.d.Pool).StoreUpdateOffset(ctx, connectionsdb.StoreUpdateOffsetParams{OrgID: e.orgID,
		ID: c.ID, Next: 5, TokenUpdatedAt: tokenAt}); err != nil {
		t.Fatal(err)
	}
	list, _ = e.conns.Polling(ctx)
	if *list[0].Offset != 11 {
		t.Fatalf("offset %d", *list[0].Offset)
	}
	e.saveDuringAnUpdate(t, f, c, in)
	if o, err := e.conns.Outage(ctx); err != nil || !o.DowntimeEnd.IsZero() {
		t.Fatalf("outage = %+v, %v", o, err)
	}
	if err := e.conns.Delete(ctx, by, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.conns.HandleOnce(ctx, c.ID, 20, func(context.Context) error { return nil }); ran || err != nil {
		t.Fatalf("a deleted Connection = %v %v", ran, err)
	}
}

// saveDuringAnUpdate is the update lock of the router (journal D289, D292): while a save of the Connection holds its
// row during a slow setWebhook, a handler runs at once, and only the final offset write waits for the save; a second
// caller with the same update waits for the lock and then skips the update.
func (e *liveEnv) saveDuringAnUpdate(t *testing.T, f *faketelegram.Fake, c connections.Connection,
	in connections.Input) {
	ctx := t.Context()
	if err := f.SetFault(fakeserver.Fault{Path: "/bot*/setWebhook", DelayMs: 1500, Times: 1}); err != nil {
		t.Fatal(err)
	}
	in.UpdateMode, in.BotToken = connections.ModeWebhook, keyring.Keep
	setWebhooks := func() int {
		n := 0
		for _, r := range f.Requests() {
			if strings.HasSuffix(r.Path, "/setWebhook") {
				n++
			}
		}
		return n
	}
	before := setWebhooks()
	saved := make(chan error, 1)
	go func() {
		_, err := e.conns.Update(ctx, by, c.PublicID, nil, in)
		saved <- err
	}()
	waitUntil(t, "the save calling setWebhook", func() bool { return setWebhooks() > before })
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan bool, 1)
	go func() {
		ran, _ := e.conns.HandleOnce(ctx, c.ID, 30, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
		first <- ran
	}()
	select {
	case <-entered:
	case err := <-saved:
		t.Fatalf("the save ended before the handler ran: %v", err)
	case <-time.After(time.Second):
		t.Fatal("the handler waited for the save")
	}
	second := make(chan bool, 1)
	go func() {
		ran, _ := e.conns.HandleOnce(ctx, c.ID, 30, func(context.Context) error { return nil })
		second <- ran
	}()
	close(release)
	select {
	case <-first:
		t.Fatal("the final offset write did not wait for the save")
	case <-second:
		t.Fatal("the second caller did not wait for the first")
	case <-time.After(200 * time.Millisecond):
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if !<-first || <-second {
		t.Fatal("the update was not handled exactly once")
	}
	list, err := e.conns.Polling(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("polling after the save = %+v, %v", list, err)
	}
	if e.count(t, `SELECT count(*) FROM connections WHERE id = $1 AND telegram_update_offset = 31`, c.ID) != 1 {
		t.Fatal("the offset was not raised past the update")
	}
	in.UpdateMode = connections.ModeLongPolling
	if _, err := e.conns.Update(ctx, by, c.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
}

// waitUntil waits for cond, up to 5 seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen in time", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	res, err := e.conns.Check(ctx, c.PublicID, nil)
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

// demoRename: a development database of an earlier version keeps a demo Connection under its earlier name; the start
// renames it in place, with its id and bot token, once, and records the rename; with both names present, nothing
// changes.
func (e *liveEnv) demoRename(t *testing.T) {
	ensure := func(d connections.Demo) {
		t.Helper()
		if err := e.conns.EnsureDemo(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	ensure(connections.Demo{Name: "Dev Rename", ServerURL: e.fake.URL(), BotToken: botToken})
	var id int64
	var publicID string
	var token []byte
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT id, public_id, bot_token_ciphertext FROM connections
		WHERE org_id = $1 AND name = 'Dev Rename' AND deleted_at IS NULL`, e.orgID).Scan(&id, &publicID,
		&token); err != nil {
		t.Fatal(err)
	}
	d := connections.Demo{Name: "Example Rename", Was: "Dev Rename", ServerURL: e.fake.URL(), BotToken: botToken}
	ensure(d)
	ensure(d)
	if n := e.count(t, `SELECT count(*) FROM connections WHERE org_id = $1 AND name IN ('Dev Rename', 'Example Rename')
		AND deleted_at IS NULL`, e.orgID); n != 1 {
		t.Fatalf("%d demo connections", n)
	}
	if n := e.count(t, `SELECT count(*) FROM connections WHERE org_id = $1 AND id = $2 AND public_id = $3
		AND name = 'Example Rename' AND bot_token_ciphertext = $4 AND version = 2`, e.orgID, id, publicID,
		token); n != 1 {
		t.Error("the renamed connection lost its id, its token or its version")
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = 'connection.updated'
		AND resource_public_id = $2 AND actor_kind = 'system' AND details @> '{"development_demo": true}'
		AND diff @> '[{"pointer": "/name", "before": "Dev Rename", "after": "Example Rename"}]'`, e.orgID,
		publicID); n != 1 {
		t.Errorf("%d audit entries of the rename", n)
	}

	ensure(connections.Demo{Name: "Dev Both", ServerURL: e.fake.URL(), BotToken: botToken})
	ensure(connections.Demo{Name: "Example Both", ServerURL: e.fake.URL(), BotToken: botToken})
	ensure(connections.Demo{Name: "Example Both", Was: "Dev Both", ServerURL: e.fake.URL(), BotToken: botToken})
	if n := e.count(t, `SELECT count(*) FROM connections WHERE org_id = $1 AND name IN ('Dev Both', 'Example Both')
		AND deleted_at IS NULL AND version = 1`, e.orgID); n != 2 {
		t.Errorf("with both names, %d connections unchanged", n)
	}
}

// demo is C-01.FR-13: the demo Connection is created once, however often `muster dev` starts.
func (e *liveEnv) demo(t *testing.T) {
	d := connections.Demo{Name: "Example Mattermost", ServerURL: e.fake.URL(), BotToken: botToken}
	for range 2 {
		if err := e.conns.EnsureDemo(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM connections WHERE name = 'Example Mattermost' AND deleted_at IS NULL`); n != 1 {
		t.Errorf("%d demo connections", n)
	}
}
