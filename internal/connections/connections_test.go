// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
)

const (
	botToken   = "mm-bot-token-0123456789"
	otherToken = "mm-bot-token-replaced-987"
)

var (
	t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	by = connections.Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportAPI}
)

// memRow is a stored Connection and whether it is deleted.
type memRow struct {
	row     dbgen.GetConnectionRow
	deleted bool
}

// memStore is the connections table in memory, with the Destinations counted per Connection, the Audit log and the
// hints; a transaction is undone when its function fails. fail makes a query fail by its name.
type memStore struct {
	rows      map[int64]*memRow
	next      int64
	dests     map[int64]int64
	audit     []auditdb.InsertAuditEntryParams
	hints     []db.Hint
	updates   []dbgen.UpdateConnectionParams
	bots      []dbgen.SetBotIdentityParams
	fail      map[string]error
	tx        int
	committed int
	// targets are the Mattermost Destinations by id, as GetDestinationTarget reads them, and tgTargets the Telegram
	// ones, as GetTelegramDestinationTarget reads them.
	targets   map[int64]memTarget
	tgTargets map[int64]memTGTarget
}

// memTGTarget is a Telegram Destination: its Connection, its channel as entered and the ids its check learned.
type memTGTarget struct {
	connection         int64
	channel            string
	channelID, groupID int64
}

// memTarget is a Mattermost Destination: its Connection, team and channel.
type memTarget struct {
	connection                  int64
	teamID, teamName, channelID string
}

func newMemStore() *memStore {
	return &memStore{rows: map[int64]*memRow{}, dests: map[int64]int64{}, fail: map[string]error{},
		targets: map[int64]memTarget{}, tgTargets: map[int64]memTGTarget{}}
}

// txHandle stands for the transaction that the Abandon hook writes through.
type txHandle struct{ n int }

func (txHandle) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("no database")
}

func (txHandle) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("no database")
}

func (txHandle) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (s *memStore) InTx(_ context.Context, f func(connections.Queries) error) error {
	s.tx++
	rows := map[int64]*memRow{}
	for id, r := range s.rows {
		c := *r
		rows[id] = &c
	}
	nAudit, nHints, nUpdates, nBots, next := len(s.audit), len(s.hints), len(s.updates), len(s.bots), s.next
	if err := f(s); err != nil {
		s.rows, s.audit, s.hints, s.updates, s.bots, s.next = rows, s.audit[:nAudit], s.hints[:nHints],
			s.updates[:nUpdates], s.bots[:nBots], next
		return err
	}
	s.committed++
	return nil
}

func (s *memStore) DB() dbgen.DBTX { return txHandle{n: s.tx} }

func (s *memStore) live(match func(r dbgen.GetConnectionRow) bool) *memRow {
	for _, id := range slices.Sorted(maps.Keys(s.rows)) {
		if r := s.rows[id]; !r.deleted && match(r.row) {
			return r
		}
	}
	return nil
}

func (s *memStore) withCount(r dbgen.GetConnectionRow) dbgen.GetConnectionRow {
	r.DestinationCount = s.dests[r.ID]
	return r
}

func (s *memStore) ListConnections(_ context.Context, arg dbgen.ListConnectionsParams) ([]dbgen.ListConnectionsRow,
	error) {
	if err := s.fail["ListConnections"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListConnectionsRow
	for _, id := range slices.Sorted(maps.Keys(s.rows)) {
		r := s.rows[id]
		if r.deleted || arg.Type.Valid && r.row.Type != arg.Type.String || arg.AfterID.Valid && id <= arg.AfterID.Int64 {
			continue
		}
		if len(out) == int(arg.PageSize) {
			break
		}
		out = append(out, dbgen.ListConnectionsRow(s.withCount(r.row)))
	}
	return out, nil
}

func (s *memStore) GetConnection(_ context.Context, arg dbgen.GetConnectionParams) (dbgen.GetConnectionRow, error) {
	if err := s.fail["GetConnection"]; err != nil {
		return dbgen.GetConnectionRow{}, err
	}
	r := s.live(func(r dbgen.GetConnectionRow) bool {
		return arg.PublicID.Valid && r.PublicID == arg.PublicID.String || !arg.PublicID.Valid && r.ID == arg.ID.Int64
	})
	if r == nil {
		return dbgen.GetConnectionRow{}, pgx.ErrNoRows
	}
	return s.withCount(r.row), nil
}

func (s *memStore) LockConnection(_ context.Context, arg dbgen.LockConnectionParams) (dbgen.LockConnectionRow,
	error) {
	if err := s.fail["LockConnection"]; err != nil {
		return dbgen.LockConnectionRow{}, err
	}
	r := s.live(func(r dbgen.GetConnectionRow) bool { return r.PublicID == arg.PublicID })
	if r == nil {
		return dbgen.LockConnectionRow{}, pgx.ErrNoRows
	}
	return dbgen.LockConnectionRow{ID: r.row.ID, PublicID: r.row.PublicID, Type: r.row.Type, Version: r.row.Version},
		nil
}

func (s *memStore) FindConnectionByName(_ context.Context, arg dbgen.FindConnectionByNameParams) (int64, error) {
	if err := s.fail["FindConnectionByName"]; err != nil {
		return 0, err
	}
	r := s.live(func(r dbgen.GetConnectionRow) bool { return r.Name == arg.Name })
	if r == nil {
		return 0, pgx.ErrNoRows
	}
	return r.row.ID, nil
}

// taken is the refusal of the unique index of the names, or another error when fail names one.
func (s *memStore) taken(name string, id int64) error {
	if r := s.live(func(r dbgen.GetConnectionRow) bool { return r.Name == name && r.ID != id }); r != nil {
		return &pgconn.PgError{Code: "23505", ConstraintName: "connections_name_key"}
	}
	return nil
}

func (s *memStore) InsertConnection(_ context.Context, a dbgen.InsertConnectionParams) (int64, error) {
	if err := s.fail["InsertConnection"]; err != nil {
		return 0, err
	}
	if err := s.taken(a.Name, 0); err != nil {
		return 0, err
	}
	s.next++
	s.rows[s.next] = &memRow{row: dbgen.GetConnectionRow{ID: s.next, PublicID: a.PublicID,
		Type: a.Type, Name: a.Name, MattermostServerUrl: a.ServerUrl, TelegramBotApiBaseUrl: a.BotApiBaseUrl,
		TelegramUpdateMode: a.UpdateMode, TelegramWebhookSecretCiphertext: a.WebhookSecretCiphertext,
		TelegramWebhookSecretKeyID: a.WebhookSecretKeyID, TelegramWebhookSecretUpdatedAt: a.WebhookSecretUpdatedAt,
		BotTokenCiphertext: a.BotTokenCiphertext, BotTokenKeyID: a.BotTokenKeyID, BotTokenUpdatedAt: a.BotTokenUpdatedAt,
		Proxy: a.Proxy, ProxyPasswordCiphertext: a.ProxyPasswordCiphertext, ProxyPasswordKeyID: a.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: a.ProxyPasswordUpdatedAt, LimiterLimit: a.LimiterLimit,
		LimiterPerSeconds: a.LimiterPerSeconds, CreatedAt: a.Now, Version: 1}}
	return s.next, nil
}

func (s *memStore) UpdateConnection(_ context.Context, a dbgen.UpdateConnectionParams) error {
	if err := s.fail["UpdateConnection"]; err != nil {
		return err
	}
	if err := s.taken(a.Name, a.ID); err != nil {
		return err
	}
	s.updates = append(s.updates, a)
	r := &s.rows[a.ID].row
	r.Name, r.MattermostServerUrl, r.Proxy = a.Name, a.ServerUrl, a.Proxy
	r.TelegramBotApiBaseUrl, r.TelegramUpdateMode = a.BotApiBaseUrl, a.UpdateMode
	r.TelegramWebhookSecretCiphertext, r.TelegramWebhookSecretKeyID, r.TelegramWebhookSecretUpdatedAt =
		a.WebhookSecretCiphertext, a.WebhookSecretKeyID, a.WebhookSecretUpdatedAt
	r.BotTokenCiphertext, r.BotTokenKeyID, r.BotTokenUpdatedAt = a.BotTokenCiphertext, a.BotTokenKeyID,
		a.BotTokenUpdatedAt
	r.ProxyPasswordCiphertext, r.ProxyPasswordKeyID, r.ProxyPasswordUpdatedAt = a.ProxyPasswordCiphertext,
		a.ProxyPasswordKeyID, a.ProxyPasswordUpdatedAt
	r.LimiterLimit, r.LimiterPerSeconds = a.LimiterLimit, a.LimiterPerSeconds
	if a.ForgetBot {
		r.BotUserID, r.BotUsername = pgtype.Text{}, pgtype.Text{}
	}
	if a.ForgetUpdates {
		r.TelegramUpdateOffset = pgtype.Int8{}
	}
	r.Version++
	return nil
}

func (s *memStore) SetBotIdentity(_ context.Context, a dbgen.SetBotIdentityParams) error {
	if err := s.fail["SetBotIdentity"]; err != nil {
		return err
	}
	s.bots = append(s.bots, a)
	r := &s.rows[a.ID].row
	r.BotUserID, r.BotUsername = a.BotUserID, a.BotUsername
	return nil
}

func (s *memStore) CountConnectionDestinations(_ context.Context, a dbgen.CountConnectionDestinationsParams) (int64,
	error) {
	if err := s.fail["CountConnectionDestinations"]; err != nil {
		return 0, err
	}
	return s.dests[a.ConnectionID.Int64], nil
}

func (s *memStore) MarkConnectionDeleted(_ context.Context, a dbgen.MarkConnectionDeletedParams) error {
	if err := s.fail["MarkConnectionDeleted"]; err != nil {
		return err
	}
	r := s.rows[a.ID]
	r.deleted = true
	r.row.BotTokenCiphertext, r.row.BotTokenKeyID = nil, pgtype.Text{}
	r.row.ProxyPasswordCiphertext, r.row.ProxyPasswordKeyID = nil, pgtype.Text{}
	r.row.TelegramWebhookSecretCiphertext, r.row.TelegramWebhookSecretKeyID = nil, pgtype.Text{}
	r.row.Version++
	return nil
}

func (s *memStore) ListPollingConnections(_ context.Context, org int64) ([]dbgen.ListPollingConnectionsRow, error) {
	if err := s.fail["ListPollingConnections"]; err != nil || org != 1 {
		return nil, err
	}
	var out []dbgen.ListPollingConnectionsRow
	for _, id := range slices.Sorted(maps.Keys(s.rows)) {
		r := s.rows[id]
		if r.deleted || r.row.Type != connections.TypeTelegram ||
			r.row.TelegramUpdateMode.String != connections.ModeLongPolling {
			continue
		}
		c := r.row
		out = append(out, dbgen.ListPollingConnectionsRow{ID: c.ID, PublicID: c.PublicID,
			TelegramBotApiBaseUrl: c.TelegramBotApiBaseUrl, TelegramUpdateOffset: c.TelegramUpdateOffset,
			BotTokenCiphertext: c.BotTokenCiphertext, BotTokenKeyID: c.BotTokenKeyID,
			BotTokenUpdatedAt: c.BotTokenUpdatedAt, Proxy: c.Proxy, ProxyPasswordCiphertext: c.ProxyPasswordCiphertext,
			ProxyPasswordKeyID: c.ProxyPasswordKeyID, ProxyPasswordUpdatedAt: c.ProxyPasswordUpdatedAt,
			Version: c.Version})
	}
	return out, nil
}

func (s *memStore) LockUpdateOffset(_ context.Context, a dbgen.LockUpdateOffsetParams) (pgtype.Int8, error) {
	if err := s.fail["LockUpdateOffset"]; err != nil {
		return pgtype.Int8{}, err
	}
	r := s.rows[a.ID]
	if r == nil || r.deleted || r.row.Type != connections.TypeTelegram || a.OrgID != 1 {
		return pgtype.Int8{}, pgx.ErrNoRows
	}
	return r.row.TelegramUpdateOffset, nil
}

func (s *memStore) StoreUpdateOffset(_ context.Context, a dbgen.StoreUpdateOffsetParams) error {
	if err := s.fail["StoreUpdateOffset"]; err != nil {
		return err
	}
	r := &s.rows[a.ID].row
	if !r.TelegramUpdateOffset.Valid || r.TelegramUpdateOffset.Int64 < a.Next {
		r.TelegramUpdateOffset = pgtype.Int8{Int64: a.Next, Valid: true}
	}
	return nil
}

func (s *memStore) LockDemo(context.Context, int64) error { return s.fail["LockDemo"] }

func (s *memStore) GetDestinationTarget(_ context.Context, a dbgen.GetDestinationTargetParams) (
	dbgen.GetDestinationTargetRow, error) {
	if err := s.fail["GetDestinationTarget"]; err != nil {
		return dbgen.GetDestinationTargetRow{}, err
	}
	d, ok := s.targets[a.DestinationID]
	r := s.rows[d.connection]
	if !ok || r == nil || r.deleted || a.OrgID != 1 {
		return dbgen.GetDestinationTargetRow{}, pgx.ErrNoRows
	}
	c := r.row
	return dbgen.GetDestinationTargetRow{MattermostTeamID: pgtype.Text{String: d.teamID, Valid: true},
		MattermostTeamName:  pgtype.Text{String: d.teamName, Valid: d.teamName != ""},
		MattermostChannelID: pgtype.Text{String: d.channelID, Valid: true}, ID: c.ID, PublicID: c.PublicID,
		Type: c.Type, Name: c.Name, MattermostServerUrl: c.MattermostServerUrl, BotTokenCiphertext: c.BotTokenCiphertext,
		BotTokenKeyID: c.BotTokenKeyID, BotTokenUpdatedAt: c.BotTokenUpdatedAt, Proxy: c.Proxy,
		ProxyPasswordCiphertext: c.ProxyPasswordCiphertext, ProxyPasswordKeyID: c.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: c.ProxyPasswordUpdatedAt, Version: c.Version}, nil
}

func (s *memStore) GetTelegramDestinationTarget(_ context.Context, a dbgen.GetTelegramDestinationTargetParams) (
	dbgen.GetTelegramDestinationTargetRow, error) {
	if err := s.fail["GetTelegramDestinationTarget"]; err != nil {
		return dbgen.GetTelegramDestinationTargetRow{}, err
	}
	d, ok := s.tgTargets[a.DestinationID]
	r := s.rows[d.connection]
	if !ok || r == nil || r.deleted || a.OrgID != 1 {
		return dbgen.GetTelegramDestinationTargetRow{}, pgx.ErrNoRows
	}
	c := r.row
	return dbgen.GetTelegramDestinationTargetRow{TelegramChannelID: pgtype.Text{String: d.channel, Valid: true},
		TelegramChannelChatID:    pgtype.Int8{Int64: d.channelID, Valid: d.channelID != 0},
		TelegramDiscussionChatID: pgtype.Int8{Int64: d.groupID, Valid: d.groupID != 0}, ID: c.ID,
		PublicID: c.PublicID, Type: c.Type, Name: c.Name, TelegramBotApiBaseUrl: c.TelegramBotApiBaseUrl,
		BotTokenCiphertext: c.BotTokenCiphertext, BotTokenKeyID: c.BotTokenKeyID, BotTokenUpdatedAt: c.BotTokenUpdatedAt,
		Proxy: c.Proxy, ProxyPasswordCiphertext: c.ProxyPasswordCiphertext, ProxyPasswordKeyID: c.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: c.ProxyPasswordUpdatedAt, Version: c.Version}, nil
}

func (s *memStore) InsertAuditEntry(_ context.Context, a auditdb.InsertAuditEntryParams) error {
	if err := s.fail["InsertAuditEntry"]; err != nil {
		return err
	}
	s.audit = append(s.audit, a)
	return nil
}

func (s *memStore) Notify(_ context.Context, h db.Hint) error {
	if err := s.fail["Notify"]; err != nil {
		return err
	}
	s.hints = append(s.hints, h)
	return nil
}

// telegramID is the Telegram Connection that addTelegram stores.
const telegramID = "CNAAAAAAAAAAT1"

// addTelegram stores a Telegram Connection directly, without a bot token, to a Bot API base URL where nothing listens.
func (s *memStore) addTelegram() {
	publicID := telegramID
	s.next++
	s.rows[s.next] = &memRow{row: dbgen.GetConnectionRow{ID: s.next, PublicID: publicID, Type: connections.TypeTelegram,
		Name: "tg-" + publicID, TelegramBotApiBaseUrl: pgtype.Text{String: "http://127.0.0.1:1", Valid: true},
		TelegramUpdateMode: pgtype.Text{String: connections.ModeWebhook, Valid: true},
		Proxy:              []byte(`{"enabled":false}`), LimiterLimit: 30, LimiterPerSeconds: 1,
		CreatedAt: t0, BotTokenUpdatedAt: t0, Version: 1}}
}

// keyStore keeps the keyring state that Establish writes.
type keyStore struct {
	state *kdb.GetKeyringStateRow
}

func (s *keyStore) GetKeyringState(context.Context) (kdb.GetKeyringStateRow, error) {
	if s.state == nil {
		return kdb.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.state, nil
}

func (s *keyStore) CreateKeyringState(_ context.Context, a kdb.CreateKeyringStateParams) (int64, error) {
	s.state = &kdb.GetKeyringStateRow{ActiveKeyID: a.ActiveKeyID, ActivatedAt: a.ActivatedAt,
		CanaryCiphertext: a.CanaryCiphertext, CanaryKeyID: a.ActiveKeyID}
	return 1, nil
}

func (s *keyStore) GetActiveKeyID(context.Context) (string, error)               { return "", nil }
func (s *keyStore) RecordReplica(context.Context, kdb.RecordReplicaParams) error { return nil }
func (s *keyStore) DeleteReplica(context.Context, string) error                  { return nil }
func (s *keyStore) ListLiveReplicas(context.Context, time.Time) ([]kdb.Replica, error) {
	return nil, nil
}

func openKeyring(t *testing.T, log *logging.Logger) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{'k'}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(t.Context(), &keyStore{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(t.Context(), log, st); err != nil {
		t.Fatal(err)
	}
	return k
}

// ticking is a real clock that moves by step on every read, so that a check measures a latency of one step.
type ticking struct {
	mu   sync.Mutex
	at   time.Time
	step time.Duration
}

func (c *ticking) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(c.step)
	return c.at
}

// recordingPath is an interactive path that records the subject of every call before it hands it on, or refuses it as
// limited when limited is set.
type recordingPath struct {
	next     connections.Interactive
	subjects []delivery.Subject
	limited  bool
}

func (p *recordingPath) Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error) {
	p.subjects = append(p.subjects, s)
	if p.limited {
		return delivery.Outcome{}, &delivery.LimitedError{RetryAfter: 3 * time.Second}
	}
	return p.next.Do(ctx, s, op)
}

type env struct {
	svc      *connections.Service
	store    *memStore
	fake     *fakemattermost.Fake
	path     *recordingPath
	log      *bytes.Buffer
	abandon  []int64
	txs      []dbgen.DBTX
	done     []bool
	abandonE error
	keys     *keyring.Keyring
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f := fakemattermost.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	var log bytes.Buffer
	logger := logging.New(&log, logging.LevelInfo)
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	clocks := clock.Clocks{Business: clock.NewManual(t0), Real: &ticking{at: t0, step: 7 * time.Millisecond}}
	e := &env{store: newMemStore(), fake: f, log: &log, keys: openKeyring(t, logger),
		path: &recordingPath{next: deliverytest.Unlimited(1, clocks)}}
	e.svc = connections.New(connections.Config{OrgID: 1, Store: e.store, Keyring: e.keys,
		Audit: audit.NewWriter(logger, clocks.Business), Clocks: clocks,
		Network:     mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}},
		Interactive: e.path, IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081", Path: "/"}, Log: logger,
		Abandon: func(_ context.Context, tx dbgen.DBTX, id int64) (func(context.Context), error) {
			e.abandon, e.txs = append(e.abandon, id), append(e.txs, tx)
			if e.abandonE != nil {
				return nil, e.abandonE
			}
			committed := e.store.committed
			return func(context.Context) { e.done = append(e.done, e.store.committed == committed+1) }, nil
		}})
	t.Cleanup(func() {
		for _, s := range []string{botToken, otherToken} {
			if strings.Contains(log.String(), s) {
				t.Errorf("a bot token reached the log: %s", log.String())
			}
		}
	})
	return e
}

func input(name, serverURL string) connections.Input {
	return connections.Input{Type: connections.TypeMattermost, Name: name, ServerURL: serverURL,
		BotToken: keyring.Replace(botToken), Limiter: connections.Limiter{Limit: 5, PerSeconds: 1}}
}

func (e *env) create(t *testing.T, name string) connections.Connection {
	t.Helper()
	c, err := e.svc.Create(t.Context(), by, input(name, e.fake.URL()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// configure changes the configuration of the fake Mattermost server, such as its revoked tokens.
func (e *env) configure(t *testing.T, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, e.fake.URL()+"/_fake/config",
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("PUT /_fake/config = %d", resp.StatusCode)
	}
}

func diffOf(t *testing.T, a auditdb.InsertAuditEntryParams) []audit.Change {
	t.Helper()
	var d []audit.Change
	if err := json.Unmarshal(a.Diff, &d); err != nil {
		t.Fatalf("diff %s: %v", a.Diff, err)
	}
	return d
}

func secretChanged(d []audit.Change, pointer string) bool {
	return slices.ContainsFunc(d, func(c audit.Change) bool { return c.Pointer == pointer && c.SecretChanged })
}

func fieldError(err error) *connections.FieldError {
	fe, _ := errors.AsType[*connections.FieldError](err)
	return fe
}

// TestCreate is C-13.FR-1: a Mattermost Connection with its name, server URL, write-only bot token, proxy and limiter;
// the token is stored encrypted, shown only as its status, and the Audit log shows it only as secret_changed.
func TestCreate(t *testing.T) {
	e := newEnv(t)
	in := input("mm", e.fake.URL())
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new("127.0.0.1:3128"), UsernameSet: true,
		Username: new("muster"), Password: keyring.Replace("proxy-pass")}
	c, err := e.svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.PublicID, "CN") || c.Type != connections.TypeMattermost || c.Name != "mm" ||
		c.ServerURL != e.fake.URL() || !c.BotToken.Set || c.BotUsername != nil || !c.Proxy.Enabled ||
		c.Proxy.Address != "127.0.0.1:3128" || !c.ProxyPassword.Set || c.Limiter.Limit != 5 || c.Version != 1 ||
		!c.CreatedAt.Equal(t0) || c.DestinationCount != 0 {
		t.Errorf("created %+v", c)
	}
	row := e.store.rows[c.ID].row
	if bytes.Contains(row.BotTokenCiphertext, []byte(botToken)) || len(row.BotTokenCiphertext) == 0 ||
		!row.BotTokenKeyID.Valid {
		t.Errorf("the token is not encrypted: %+v", row)
	}
	if len(e.store.audit) != 1 || len(e.store.hints) != 1 || e.store.hints[0].Type != connections.Hint ||
		e.store.hints[0].ID != c.PublicID {
		t.Fatalf("audit %d, hints %+v", len(e.store.audit), e.store.hints)
	}
	a := e.store.audit[0]
	d := diffOf(t, a)
	if a.Action != connections.ActionCreated || a.ResourceType.String != connections.ResourceConnection ||
		!secretChanged(d, "/bot_token") || !secretChanged(d, "/proxy/password") ||
		bytes.Contains(a.Diff, []byte(botToken)) || bytes.Contains(a.Diff, []byte("proxy-pass")) {
		t.Errorf("audit %s %s", a.Action, a.Diff)
	}
	if _, err := e.svc.Create(t.Context(), by, input("mm", e.fake.URL())); !errors.Is(err, connections.ErrNameTaken) {
		t.Errorf("a taken name = %v", err)
	}
	e.store.fail["InsertConnection"] = &pgconn.PgError{Code: "23505", ConstraintName: "other_key"}
	if _, err := e.svc.Create(t.Context(), by, input("mm2", e.fake.URL())); err == nil ||
		errors.Is(err, connections.ErrNameTaken) {
		t.Errorf("another unique index = %v", err)
	}
	delete(e.store.fail, "InsertConnection")
	e.store.fail["GetConnection"] = errors.New("down")
	if _, err := e.svc.Create(t.Context(), by, input("mm3", e.fake.URL())); err == nil {
		t.Error("a failed read after the insert")
	}
	delete(e.store.fail, "GetConnection")
	e.store.fail["InsertAuditEntry"] = errors.New("down")
	if _, err := e.svc.Create(t.Context(), by, input("mm4", e.fake.URL())); err == nil || len(e.store.rows) != 1 {
		t.Errorf("a failed Audit log entry = %v, %d rows", err, len(e.store.rows))
	}
}

// TestCreateValidation: every field of a Connection is validated at its JSON pointer.
func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	base := input("mm", "http://127.0.0.1:1")
	cases := []struct {
		name    string
		change  func(in *connections.Input)
		pointer string
		code    string
	}{
		{"unknown type", func(in *connections.Input) { in.Type = "slack" }, "/type", connections.CodeInvalidFormat},
		{"empty name", func(in *connections.Input) { in.Name = "  " }, "/name", connections.CodeRequired},
		{"long name", func(in *connections.Input) { in.Name = strings.Repeat("я", 201) }, "/name",
			connections.CodeTooLong},
		{"not http", func(in *connections.Input) { in.ServerURL = "ftp://mm.example.org" }, "/server_url",
			connections.CodeInvalidFormat},
		{"relative", func(in *connections.Input) { in.ServerURL = "mm.example.org" }, "/server_url",
			connections.CodeInvalidFormat},
		{"no host", func(in *connections.Input) { in.ServerURL = "http://" }, "/server_url",
			connections.CodeInvalidFormat},
		{"userinfo", func(in *connections.Input) { in.ServerURL = "https://u:p@mm.example.org" }, "/server_url",
			connections.CodeInvalidFormat},
		{"query", func(in *connections.Input) { in.ServerURL = "https://mm.example.org/?a=1" }, "/server_url",
			connections.CodeInvalidFormat},
		{"empty query", func(in *connections.Input) { in.ServerURL = "https://mm.example.org/?" }, "/server_url",
			connections.CodeInvalidFormat},
		{"fragment", func(in *connections.Input) { in.ServerURL = "https://mm.example.org/#x" }, "/server_url",
			connections.CodeInvalidFormat},
		{"empty fragment", func(in *connections.Input) { in.ServerURL = "https://mm.example.org/#" }, "/server_url",
			connections.CodeInvalidFormat},
		{"unparsable", func(in *connections.Input) { in.ServerURL = "http://[::1" }, "/server_url",
			connections.CodeInvalidFormat},
		{"no limit", func(in *connections.Input) { in.Limiter.Limit = 0 }, "/limiter", connections.CodeInvalidFormat},
		{"no period", func(in *connections.Input) { in.Limiter.PerSeconds = 0 }, "/limiter",
			connections.CodeInvalidFormat},
		{"missing token", func(in *connections.Input) { in.BotToken = keyring.Keep }, "/bot_token",
			connections.CodeRequired},
		{"cleared token", func(in *connections.Input) { in.BotToken = keyring.Clear }, "/bot_token",
			connections.CodeRequired},
		{"empty token", func(in *connections.Input) { in.BotToken = keyring.Replace("") }, "/bot_token",
			connections.CodeTooShort},
		{"proxy without type", func(in *connections.Input) {
			in.Proxy = proxyconf.Input{Enabled: true, Address: new("127.0.0.1:3128")}
		}, "/proxy/type", "required"},
		{"proxy address", func(in *connections.Input) {
			in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new("proxy")}
		}, "/proxy/address", "invalid_format"},
		{"empty proxy password", func(in *connections.Input) {
			in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new("127.0.0.1:3128"),
				Password: keyring.Replace("")}
		}, "/proxy/password", connections.CodeTooShort},
	}
	for _, tc := range cases {
		in := base
		tc.change(&in)
		_, err := e.svc.Create(t.Context(), by, in)
		if fe := fieldError(err); fe == nil || fe.Pointer != tc.pointer || fe.Code != tc.code || fe.Detail == "" ||
			fe.Error() != fe.Pointer+": "+fe.Detail {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if len(e.store.rows) != 0 || len(e.store.audit) != 0 {
		t.Errorf("an invalid Connection was written: %d rows", len(e.store.rows))
	}
}

// TestGetAndList: a Connection by public_id, and the pages of the Connections that are not deleted in the order they
// were created, of a type when asked, with the count of their Destinations.
func TestGetAndList(t *testing.T) {
	e := newEnv(t)
	a, b := e.create(t, "a"), e.create(t, "b")
	e.store.addTelegram()
	e.create(t, "c")
	e.store.dests[a.ID] = 2
	got, err := e.svc.Get(t.Context(), a.PublicID)
	if err != nil || got.Name != "a" || got.DestinationCount != 2 {
		t.Errorf("get = %+v, %v", got, err)
	}
	for _, id := range []string{"CN000000000000", "bad", b.PublicID[:5]} {
		if _, err := e.svc.Get(t.Context(), id); !errors.Is(err, connections.ErrNotFound) {
			t.Errorf("get %s = %v", id, err)
		}
	}
	page, err := e.svc.List(t.Context(), connections.ListFilter{Limit: 2})
	if err != nil || len(page.Connections) != 2 || page.Connections[0].Name != "a" ||
		page.Connections[0].DestinationCount != 2 || page.Next == nil || *page.Next != b.ID {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, err = e.svc.List(t.Context(), connections.ListFilter{Limit: 2, After: page.Next})
	if err != nil || len(page.Connections) != 2 || page.Connections[0].Type != connections.TypeTelegram ||
		page.Connections[1].Name != "c" || page.Next != nil {
		t.Errorf("second page = %+v, %v", page, err)
	}
	mm := connections.TypeMattermost
	page, err = e.svc.List(t.Context(), connections.ListFilter{Limit: 10, Type: &mm})
	if err != nil || len(page.Connections) != 3 {
		t.Errorf("mattermost = %+v, %v", page, err)
	}
	page, err = e.svc.List(t.Context(), connections.ListFilter{})
	if err != nil || len(page.Connections) != 1 || page.Next == nil {
		t.Errorf("limit 0 = %+v, %v", page, err)
	}
	e.store.fail["ListConnections"] = errors.New("down")
	if _, err := e.svc.List(t.Context(), connections.ListFilter{Limit: 2}); err == nil {
		t.Error("a failed list")
	}
	delete(e.store.fail, "ListConnections")
	e.store.fail["GetConnection"] = errors.New("down")
	if _, err := e.svc.Get(t.Context(), a.PublicID); err == nil || errors.Is(err, connections.ErrNotFound) {
		t.Errorf("a failed read = %v", err)
	}
	delete(e.store.fail, "GetConnection")
	e.store.rows[a.ID].row.Proxy = []byte("{")
	if _, err := e.svc.Get(t.Context(), a.PublicID); err == nil {
		t.Error("an unreadable proxy")
	}
	if _, err := e.svc.List(t.Context(), connections.ListFilter{Limit: 2}); err == nil {
		t.Error("a list with an unreadable proxy")
	}
}

// TestUpdate: an omitted bot token keeps the stored one, a new one replaces it and forgets the bot; If-Match, an
// unknown Connection, a taken name and an update that changes nothing.
func TestUpdate(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	other := e.create(t, "other")
	ctx := t.Context()
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	stored := e.store.rows[c.ID].row
	in := input("mm-renamed", e.fake.URL())
	in.BotToken = keyring.Keep
	v := int64(1)
	u, err := e.svc.Update(ctx, by, c.PublicID, &v, in)
	row := e.store.rows[c.ID].row
	if err != nil || u.Name != "mm-renamed" || u.Version != 2 || u.BotUsername == nil ||
		!bytes.Equal(row.BotTokenCiphertext, stored.BotTokenCiphertext) || e.store.updates[0].ForgetBot {
		t.Fatalf("rename = %+v, %v", u, err)
	}
	if d := diffOf(t, e.store.audit[len(e.store.audit)-1]); len(d) != 1 || d[0].Pointer != "/name" ||
		e.store.audit[len(e.store.audit)-1].Action != connections.ActionUpdated {
		t.Errorf("rename diff %+v", d)
	}
	n := len(e.store.updates)
	if u, err := e.svc.Update(ctx, by, c.PublicID, nil, in); err != nil || u.Version != 2 || len(e.store.updates) != n {
		t.Errorf("no change = %+v, %v, %d updates", u, err, len(e.store.updates))
	}
	in.BotToken = keyring.Replace(otherToken)
	u, err = e.svc.Update(ctx, by, c.PublicID, nil, in)
	last := e.store.audit[len(e.store.audit)-1]
	if err != nil || u.BotUsername != nil || !e.store.updates[len(e.store.updates)-1].ForgetBot ||
		bytes.Equal(e.store.rows[c.ID].row.BotTokenCiphertext, stored.BotTokenCiphertext) ||
		!secretChanged(diffOf(t, last), "/bot_token") || bytes.Contains(last.Diff, []byte(otherToken)) {
		t.Errorf("replace = %+v, %v, %s", u, err, last.Diff)
	}
	in.BotToken = keyring.Keep
	in.ServerURL = "http://127.0.0.1:2"
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/bot_token" || fe.Code != "required" {
		t.Errorf("a new server URL kept the stored token: %v", fe)
	}
	in.BotToken = keyring.Replace(otherToken)
	if u, err := e.svc.Update(ctx, by, c.PublicID, nil, in); err != nil ||
		!e.store.updates[len(e.store.updates)-1].ForgetBot || u.ServerURL != "http://127.0.0.1:2" {
		t.Errorf("new server URL = %+v, %v", u, err)
	}
	in.BotToken = keyring.Keep
	if _, err := e.svc.Update(ctx, by, c.PublicID, &v, in); !errors.Is(err, connections.ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	for _, id := range []string{"CN000000000000", "nope"} {
		if _, err := e.svc.Update(ctx, by, id, nil, in); !errors.Is(err, connections.ErrNotFound) {
			t.Errorf("update %s = %v", id, err)
		}
	}
	in.Name = "other"
	if _, err := e.svc.Update(ctx, by, c.PublicID, nil, in); !errors.Is(err, connections.ErrNameTaken) {
		t.Errorf("taken = %v", err)
	}
	in.Name = "mm"
	in.Limiter.Limit = 0
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/limiter" {
		t.Errorf("invalid = %v", fe)
	}
	in.Limiter.Limit = 5
	in.Proxy = proxyconf.Input{Enabled: true}
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/proxy/type" {
		t.Errorf("invalid proxy = %v", fe)
	}
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new("127.0.0.1:3128"),
		Password: keyring.Replace("")}
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/proxy/password" {
		t.Errorf("empty proxy password = %v", fe)
	}
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new("127.0.0.1:3128"),
		Password: keyring.Replace("pw")}
	if _, err := e.svc.Update(ctx, by, c.PublicID, nil, in); err != nil ||
		!secretChanged(diffOf(t, e.store.audit[len(e.store.audit)-1]), "/proxy/password") {
		t.Errorf("proxy password = %v", err)
	}
	in.BotToken = keyring.Replace("")
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/bot_token" ||
		fe.Code != connections.CodeTooShort {
		t.Errorf("empty token = %v", fe)
	}
	e.store.addTelegram()
	if fe := fieldError(e.update(t, telegramID, input("tg", e.fake.URL()))); fe == nil || fe.Pointer != "/type" {
		t.Errorf("a changed type = %v", fe)
	}
	for _, q := range []string{"LockConnection", "GetConnection", "UpdateConnection"} {
		e.store.fail[q] = errors.New("down")
		if err := e.update(t, other.PublicID, input("other-2", e.fake.URL())); err == nil ||
			errors.Is(err, connections.ErrNotFound) {
			t.Errorf("%s failed = %v", q, err)
		}
		delete(e.store.fail, q)
	}
	e.store.rows[other.ID].row.Proxy = []byte("{")
	if err := e.update(t, other.PublicID, input("other-2", e.fake.URL())); err == nil {
		t.Error("an unreadable proxy")
	}
}

func (e *env) update(t *testing.T, id string, in connections.Input) error {
	t.Helper()
	_, err := e.svc.Update(t.Context(), by, id, nil, in)
	return err
}

// TestDelete is C-13.FR-6: a Connection that a Destination that is not deleted uses is in use; otherwise it is
// soft-deleted with its secrets wiped, delivery's Abandon hook runs in the transaction and its function after the
// commit, and an Abandon that fails undoes the deletion.
func TestDelete(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	ctx := t.Context()
	e.store.dests[c.ID] = 1
	if err := e.svc.Delete(ctx, by, c.PublicID, nil); !errors.Is(err, connections.ErrInUse) || len(e.abandon) != 0 ||
		e.store.rows[c.ID].deleted {
		t.Fatalf("in use = %v", err)
	}
	e.store.dests[c.ID] = 0
	stale := int64(7)
	if err := e.svc.Delete(ctx, by, c.PublicID, &stale); !errors.Is(err, connections.ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	e.abandonE = errors.New("abandon failed")
	if err := e.svc.Delete(ctx, by, c.PublicID, nil); !errors.Is(err, e.abandonE) || e.store.rows[c.ID].deleted ||
		len(e.done) != 0 || len(e.store.rows[c.ID].row.BotTokenCiphertext) == 0 {
		t.Fatalf("a failed abandon = %v, %+v", err, e.store.rows[c.ID])
	}
	e.abandonE = nil
	nAudit := len(e.store.audit)
	current := int64(1)
	if err := e.svc.Delete(ctx, by, c.PublicID, &current); err != nil {
		t.Fatal(err)
	}
	r := e.store.rows[c.ID]
	if !r.deleted || r.row.BotTokenCiphertext != nil || r.row.BotTokenKeyID.Valid || len(e.abandon) != 2 ||
		e.abandon[1] != c.ID || e.txs[1] != (txHandle{n: e.store.tx}) || !slices.Equal(e.done, []bool{true}) {
		t.Errorf("deleted %+v, abandon %v %v, done %v", r, e.abandon, e.txs, e.done)
	}
	if len(e.store.audit) != nAudit+1 || e.store.audit[nAudit].Action != connections.ActionDeleted ||
		diffOf(t, e.store.audit[nAudit])[0].Pointer != "/deleted_at" {
		t.Errorf("audit %+v", e.store.audit)
	}
	if _, err := e.svc.Get(ctx, c.PublicID); !errors.Is(err, connections.ErrNotFound) {
		t.Errorf("read after the deletion = %v", err)
	}
	for _, id := range []string{c.PublicID, "nope"} {
		if err := e.svc.Delete(ctx, by, id, nil); !errors.Is(err, connections.ErrNotFound) {
			t.Errorf("delete %s again = %v", id, err)
		}
	}
	d := e.create(t, "mm-2")
	for _, q := range []string{"LockConnection", "CountConnectionDestinations", "GetConnection",
		"MarkConnectionDeleted", "InsertAuditEntry"} {
		e.store.fail[q] = errors.New("down")
		if err := e.svc.Delete(ctx, by, d.PublicID, nil); err == nil || e.store.rows[d.ID].deleted {
			t.Errorf("%s failed = %v", q, err)
		}
		delete(e.store.fail, q)
	}
	e.store.rows[d.ID].row.Proxy = []byte("{")
	if err := e.svc.Delete(ctx, by, d.PublicID, nil); err == nil {
		t.Error("an unreadable proxy")
	}
}

// TestDeleteWithoutAbandon: a Service without the Abandon hook deletes all the same.
func TestDeleteWithoutAbandon(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	svc := connections.New(connections.Config{OrgID: 1, Store: e.store, Keyring: e.keys,
		Audit:  audit.NewWriter(logging.New(e.log, logging.LevelInfo), clock.NewManual(t0)),
		Clocks: clock.Clocks{Business: clock.NewManual(t0), Real: clock.NewManual(t0)}})
	if err := svc.Delete(t.Context(), by, c.PublicID, nil); err != nil || !e.store.rows[c.ID].deleted {
		t.Errorf("delete = %v", err)
	}
}

// TestCheck is C-13.FR-2: the Connection check on the interactive path, limited by the Connection, returns the bot's
// name and records the bot, with the warning press_answers_in_thread while the bot's roles do not grant
// create_post_ephemeral (D284) and without it once they do; a revoked token fails with "The bot token is not
// valid."; no limiter token in time is a *delivery.LimitedError.
func TestCheck(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	ctx := t.Context()
	res, err := e.svc.Check(ctx, c.PublicID, nil)
	if err != nil || !res.OK || res.BotName != fakemattermost.BotUsername || len(res.Steps) != 1 ||
		res.Steps[0].Name != connections.StepToken || !res.Steps[0].OK || res.Steps[0].Via != mattermost.ViaDirect ||
		res.Steps[0].Latency != 7*time.Millisecond || res.Steps[0].Message != "" ||
		!slices.Equal(res.Warnings, []string{connections.WarningPressAnswersInThread}) {
		t.Fatalf("check = %+v, %v", res, err)
	}
	got, _ := e.svc.Get(ctx, c.PublicID)
	if got.BotUsername == nil || *got.BotUsername != fakemattermost.BotUsername || got.BotUserID == nil ||
		*got.BotUserID != fakemattermost.BotUserID || got.Version != 1 {
		t.Errorf("bot = %+v", got)
	}
	if len(e.path.subjects) != 2 || e.path.subjects[1].Connection == nil || *e.path.subjects[1].Connection != c.ID ||
		e.path.subjects[1].Destination != nil {
		t.Errorf("subjects %+v", e.path.subjects)
	}
	reqs := e.fake.Requests()
	if len(reqs) != 2 || reqs[0].Path != "/api/v4/users/me" || reqs[0].Headers["Authorization"][0] != "Bearer "+botToken ||
		reqs[1].Path != "/api/v4/roles/names" || reqs[1].Body != `["system_user"]` {
		t.Errorf("requests %+v", reqs)
	}
	e.configure(t, `{"bot_system_admin":true}`)
	if res, err := e.svc.Check(ctx, c.PublicID, nil); err != nil || !res.OK || len(res.Warnings) != 0 {
		t.Errorf("an admin bot = %+v, %v", res, err)
	}
	e.configure(t, `{"bot_system_admin":false}`)
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/v4/roles/names", Status: 503, Times: 1}); err != nil {
		t.Fatal(err)
	}
	if res, err := e.svc.Check(ctx, c.PublicID, nil); err != nil || !res.OK || len(res.Warnings) != 0 {
		t.Errorf("roles unreadable = %+v, %v", res, err)
	}
	e.configure(t, `{"revoked_tokens":["`+botToken+`"]}`)
	nBots := len(e.store.bots)
	res, err = e.svc.Check(ctx, c.PublicID, nil)
	if err != nil || res.OK || res.BotName != "" || res.Steps[0].OK ||
		res.Steps[0].Message != mattermost.MessageTokenInvalid || len(e.store.bots) != nBots {
		t.Errorf("revoked = %+v, %v", res, err)
	}
	e.configure(t, `{"revoked_tokens":[]}`)
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/v4/users/me", Status: 503, Times: 1}); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.Check(ctx, c.PublicID, nil)
	if err != nil || res.OK || !strings.Contains(res.Steps[0].Message, "answered 503") {
		t.Errorf("unavailable = %+v, %v", res, err)
	}
	e.path.limited = true
	if _, err := e.svc.Check(ctx, c.PublicID, nil); !isLimited(err) {
		t.Errorf("limited = %v", err)
	}
	e.path.limited = false
	e.store.fail["SetBotIdentity"] = errors.New("down")
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err == nil {
		t.Error("a failed record of the bot")
	}
	delete(e.store.fail, "SetBotIdentity")
	base := "http://127.0.0.1:1"
	if fe := fieldError(func() error { _, err := e.svc.Check(ctx, c.PublicID, &base); return err }()); fe == nil ||
		fe.Pointer != "/base_url" || fe.Code != connections.CodeUnsupported {
		t.Errorf("a base URL for a Mattermost Connection = %v", fe)
	}
	if _, err := e.svc.Check(ctx, "CN000000000000", nil); !errors.Is(err, connections.ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	e.store.rows[c.ID].row.BotTokenCiphertext = []byte("garbage")
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err == nil || strings.Contains(err.Error(), botToken) {
		t.Errorf("an unreadable token = %v", err)
	}
}

func isLimited(err error) bool {
	l, ok := errors.AsType[*delivery.LimitedError](err)
	return ok && l.RetryAfter == 3*time.Second
}

// TestCheckThroughProxy is C-13.AC-7: with an HTTP proxy on the Connection, the check reports the path proxy and every
// request of the check, the channel list and the Destination check reaches the fake only through the proxy.
func TestCheckThroughProxy(t *testing.T) {
	e := newEnv(t)
	p, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "proxy-pass"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	in := input("mm", e.fake.URL())
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new(p.Addr()), UsernameSet: true,
		Username: new("muster"), Password: keyring.Replace("proxy-pass")}
	c, err := e.svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Check(t.Context(), c.PublicID, nil)
	if err != nil || !res.OK || res.Steps[0].Via != mattermost.ViaProxy {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if _, err := e.svc.Channels(t.Context(), c.PublicID, "", ""); err != nil {
		t.Fatal(err)
	}
	checked, err := e.svc.CheckChannel(t.Context(), destinations.ChannelCheck{Connection: c.PublicID,
		TeamID: fakemattermost.TeamID, ChannelID: fakemattermost.ChannelAlerts})
	if err != nil || !checked.Check.OK() {
		t.Fatalf("destination check = %+v, %v", checked, err)
	}
	reqs, targets := e.fake.Requests(), p.Targets()
	if len(reqs) != 8 || len(targets) != len(reqs) || p.Refused() != 0 {
		t.Errorf("%d requests reached the fake, %d through the proxy, %d refused", len(reqs), len(targets),
			p.Refused())
	}
	for _, tg := range targets {
		if "http://"+tg != e.fake.URL() {
			t.Errorf("the proxy connected to %s", tg)
		}
	}
}

// TestChannels: the channels the bot belongs to, team by team, filtered by team and by name; a Telegram Connection is
// not a Mattermost one, and a refused call is a *MessengerError.
func TestChannels(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	ctx := t.Context()
	names := func(cs []connections.Channel) []string {
		var out []string
		for _, ch := range cs {
			out = append(out, ch.Name)
		}
		return out
	}
	all, err := e.svc.Channels(ctx, c.PublicID, "", "")
	if err != nil || !slices.Equal(names(all), []string{"alerts", "alerts-prod"}) || all[0].TeamName != "dev" ||
		all[0].TeamID != fakemattermost.TeamID || all[0].DisplayName != "Alerts" {
		t.Fatalf("channels = %+v, %v", all, err)
	}
	for _, tc := range []struct {
		team, q string
		want    []string
	}{
		{fakemattermost.TeamID, "alerts", []string{"alerts", "alerts-prod"}},
		{"", "PROD", []string{"alerts-prod"}},
		{"", " Alerts prod ", []string{"alerts-prod"}},
		{"", "nothing", nil},
		{"team-other", "", nil},
	} {
		got, err := e.svc.Channels(ctx, c.PublicID, tc.team, tc.q)
		if err != nil || got == nil || !slices.Equal(names(got), tc.want) {
			t.Errorf("team %q q %q = %+v, %v", tc.team, tc.q, got, err)
		}
	}
	for _, s := range e.path.subjects {
		if s.Connection == nil || *s.Connection != c.ID {
			t.Errorf("subject %+v", s)
		}
	}
	e.store.addTelegram()
	if _, err := e.svc.Channels(ctx, telegramID, "", ""); !errors.Is(err, connections.ErrNotMattermost) {
		t.Errorf("telegram = %v", err)
	}
	if _, err := e.svc.Channels(ctx, "CN000000000000", "", ""); !errors.Is(err, connections.ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	e.configure(t, `{"revoked_tokens":["`+botToken+`"]}`)
	_, err = e.svc.Channels(ctx, c.PublicID, "", "")
	if me, ok := errors.AsType[*connections.MessengerError](err); !ok || !strings.Contains(me.Error(), "answered 401") ||
		strings.Contains(me.Error(), botToken) {
		t.Errorf("revoked = %v", err)
	}
	e.configure(t, `{"revoked_tokens":[]}`)
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/v4/users/me/teams/team-dev/channels", Status: 500,
		Times: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Channels(ctx, c.PublicID, "", ""); !isMessenger(err) {
		t.Errorf("channels failed = %v", err)
	}
	e.path.limited = true
	if _, err := e.svc.Channels(ctx, c.PublicID, "", ""); !isLimited(err) {
		t.Errorf("limited = %v", err)
	}
	e.path.limited = false
	calls := 0
	e.path.next = interactiveFunc(func(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome,
		error) {
		calls++
		if calls == 2 {
			return delivery.Outcome{}, &delivery.LimitedError{RetryAfter: 3 * time.Second}
		}
		return deliverytest.Unlimited(1, clock.Clocks{Business: clock.NewManual(t0), Real: clock.NewManual(t0)}).Do(
			ctx, s, op)
	})
	if _, err := e.svc.Channels(ctx, c.PublicID, "", ""); !isLimited(err) {
		t.Errorf("limited on the channels = %v", err)
	}
	e.store.rows[c.ID].row.Proxy = []byte(`{"enabled":true,"type":"ftp","address":"127.0.0.1:1"}`)
	if _, err := e.svc.Channels(ctx, c.PublicID, "", ""); err == nil {
		t.Error("a proxy the client refuses")
	}
	e.store.rows[c.ID].row.Proxy = []byte("{")
	if _, err := e.svc.Channels(ctx, c.PublicID, "", ""); err == nil {
		t.Error("an unreadable proxy")
	}
}

type interactiveFunc func(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error)

func (f interactiveFunc) Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error) {
	return f(ctx, s, op)
}

func isMessenger(err error) bool {
	_, ok := errors.AsType[*connections.MessengerError](err)
	return ok
}

// TestCheckChannel is the Destination check for the Destination write path (C-13.FR-2, FR-10): through the Mattermost
// Connection, limited by the Destination once it exists and by the Connection before; a missing, deleted or Telegram
// Connection is destinations.ErrUnknownConnection.
func TestCheckChannel(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	ctx := t.Context()
	res, err := e.svc.CheckChannel(ctx, destinations.ChannelCheck{Connection: c.PublicID, TeamID: fakemattermost.TeamID,
		ChannelID: fakemattermost.ChannelAlerts})
	if err != nil || res.ConnectionID != c.ID || !res.Check.OK() || res.Check.TeamName != "dev" ||
		res.Check.ChannelName != "alerts" || len(e.path.subjects) != 4 || e.path.subjects[0].Destination != nil ||
		*e.path.subjects[0].Connection != c.ID {
		t.Fatalf("check = %+v, %v, %+v", res, err, e.path.subjects)
	}
	e.path.subjects = nil
	dest := int64(42)
	res, err = e.svc.CheckChannel(ctx, destinations.ChannelCheck{Connection: c.PublicID, Destination: &dest,
		TeamID: fakemattermost.TeamID, ChannelID: fakemattermost.ChannelNoBot})
	s := e.path.subjects[0]
	if err != nil || res.Check.OK() || res.Check.Steps[1].Message != mattermost.MessageNotMember ||
		s.Destination == nil || s.Destination.ID != 42 || s.Destination.Type != connections.TypeMattermost ||
		*s.Destination.Connection != c.ID {
		t.Errorf("no bot = %+v, %v, %+v", res, err, s)
	}
	e.store.addTelegram()
	for _, id := range []string{"CN000000000000", telegramID, "bad"} {
		if _, err := e.svc.CheckChannel(ctx, destinations.ChannelCheck{Connection: id, TeamID: "t",
			ChannelID: "c"}); !errors.Is(err, destinations.ErrUnknownConnection) {
			t.Errorf("connection %s = %v", id, err)
		}
	}
	e.path.limited = true
	if _, err := e.svc.CheckChannel(ctx, destinations.ChannelCheck{Connection: c.PublicID,
		TeamID: fakemattermost.TeamID, ChannelID: fakemattermost.ChannelAlerts}); !isLimited(err) {
		t.Errorf("limited = %v", err)
	}
	e.path.limited = false
	e.store.rows[c.ID].row.Proxy = []byte("{")
	if _, err := e.svc.CheckChannel(ctx, destinations.ChannelCheck{Connection: c.PublicID, TeamID: "t",
		ChannelID: "c"}); err == nil || errors.Is(err, destinations.ErrUnknownConnection) {
		t.Errorf("an unreadable proxy = %v", err)
	}
	e.store.fail["GetConnection"] = errors.New("down")
	if _, err := e.svc.CheckChannel(ctx, destinations.ChannelCheck{Connection: c.PublicID, TeamID: "t",
		ChannelID: "c"}); err == nil || errors.Is(err, destinations.ErrUnknownConnection) {
		t.Errorf("a failed read = %v", err)
	}
}

// TestEnsureDemo is C-01.FR-13: `muster dev` creates the demo Connection once, recorded as a development demo; a
// second start finds it by its name.
func TestEnsureDemo(t *testing.T) {
	e := newEnv(t)
	d := connections.Demo{Name: "Dev Mattermost", ServerURL: e.fake.URL(), BotToken: botToken}
	for range 2 {
		if err := e.svc.EnsureDemo(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.store.rows) != 1 || len(e.store.audit) != 1 || len(e.store.hints) != 1 {
		t.Fatalf("%d rows, %d audit entries", len(e.store.rows), len(e.store.audit))
	}
	a := e.store.audit[0]
	c := e.store.rows[1].row
	if c.Name != "Dev Mattermost" || c.LimiterLimit != connections.DefaultLimit ||
		c.LimiterPerSeconds != connections.DefaultPerSeconds || a.ActorKind != "system" ||
		!bytes.Contains(a.Details, []byte(`"development_demo":true`)) || !secretChanged(diffOf(t, a), "/bot_token") {
		t.Errorf("demo %+v, audit %s %s", c, a.Details, a.Diff)
	}
	if res, err := e.svc.Check(t.Context(), c.PublicID, nil); err != nil || res.BotName != fakemattermost.BotUsername {
		t.Errorf("check of the demo = %+v, %v", res, err)
	}
	other := newEnv(t)
	for _, q := range []string{"LockDemo", "FindConnectionByName", "InsertConnection", "InsertAuditEntry", "Notify"} {
		other.store.fail[q] = errors.New("down")
		if err := other.svc.EnsureDemo(t.Context(), d); err == nil || len(other.store.rows) != 0 {
			t.Errorf("%s failed = %v", q, err)
		}
		delete(other.store.fail, q)
	}
	if err := other.svc.EnsureDemo(t.Context(), connections.Demo{Name: "x", ServerURL: "ftp://x",
		BotToken: botToken}); fieldError(err) == nil {
		t.Errorf("an invalid demo = %v", err)
	}
}

// TestCallbackURL is C-13.FR-13: the callback address is MUSTER_INGEST_URL followed by
// /api/v1/callbacks/mattermost/<public_id>, with or without a trailing slash on the base.
func TestCallbackURL(t *testing.T) {
	for base, want := range map[string]string{
		"http://localhost:8081/":         "http://localhost:8081/api/v1/callbacks/mattermost/CNAAAAAAAAAAA1",
		"http://localhost:8081":          "http://localhost:8081/api/v1/callbacks/mattermost/CNAAAAAAAAAAA1",
		"https://muster.example.org/in/": "https://muster.example.org/in/api/v1/callbacks/mattermost/CNAAAAAAAAAAA1",
	} {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		svc := connections.New(connections.Config{IngestURL: u})
		if got := svc.CallbackURL("CNAAAAAAAAAAA1"); got != want {
			t.Errorf("%s: %s", base, got)
		}
	}
	if got := connections.New(connections.Config{}).CallbackURL("CNAAAAAAAAAAA1"); got !=
		"/api/v1/callbacks/mattermost/CNAAAAAAAAAAA1" {
		t.Errorf("without MUSTER_INGEST_URL: %s", got)
	}
}

// TestMessengerError: the text of a refused call is kept for the API.
func TestMessengerError(t *testing.T) {
	err := &connections.MessengerError{Text: "answered 500"}
	if err.Error() != "the messenger refused the call: answered 500" {
		t.Error(err.Error())
	}
}

// TestNotifyFailure: a failed hint of a Connection check fails the check.
func TestNotifyFailure(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	e.store.fail["Notify"] = errors.New("down")
	if _, err := e.svc.Check(t.Context(), c.PublicID, nil); err == nil {
		t.Error("a failed hint")
	}
	if _, err := e.svc.Create(t.Context(), by, input("mm2", e.fake.URL())); err == nil {
		t.Error("a failed hint on create")
	}
}

// TestTarget is where a Mattermost Destination posts for the adapter: its team and channel and the client of its
// Connection, kept until the Connection changes; without a Connection that is not deleted, or for a Telegram one, it
// is mattermost.ErrNoTarget.
func TestTarget(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	ctx := t.Context()
	e.store.targets[7] = memTarget{connection: c.ID, teamID: fakemattermost.TeamID, teamName: fakemattermost.TeamName,
		channelID: fakemattermost.ChannelAlerts}
	first, err := e.svc.Target(ctx, 7)
	if err != nil || first.Client == nil || first.ConnectionID != c.ID || first.ConnectionPublicID != c.PublicID ||
		first.TeamID != fakemattermost.TeamID || first.TeamName != "dev" ||
		first.ChannelID != fakemattermost.ChannelAlerts {
		t.Fatalf("target = %+v, %v", first, err)
	}
	if u, r := first.Client.Me(ctx, outbound.ClassDelivery); !r.OK() || u.ID != fakemattermost.BotUserID {
		t.Errorf("the target's client = %+v, %+v", u, r)
	}
	if again, err := e.svc.Target(ctx, 7); err != nil || again.Client != first.Client {
		t.Errorf("the client is not kept: %+v, %v", again, err)
	}
	in := input("mm", e.fake.URL())
	in.Limiter.Limit = 9
	if _, err := e.svc.Update(ctx, by, c.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	if changed, err := e.svc.Target(ctx, 7); err != nil || changed.Client == first.Client {
		t.Errorf("the client is not built again for a changed connection: %+v, %v", changed, err)
	}
	if _, err := e.svc.Target(ctx, 8); !errors.Is(err, mattermost.ErrNoTarget) {
		t.Errorf("an unknown destination = %v", err)
	}
	e.store.addTelegram()
	e.store.targets[9] = memTarget{connection: e.store.next, teamID: "t", channelID: "c"}
	if _, err := e.svc.Target(ctx, 9); !errors.Is(err, mattermost.ErrNoTarget) {
		t.Errorf("a telegram connection = %v", err)
	}
	e.store.rows[c.ID].row.Proxy = []byte("{")
	e.store.rows[c.ID].row.Version++
	if _, err := e.svc.Target(ctx, 7); err == nil || errors.Is(err, mattermost.ErrNoTarget) {
		t.Errorf("an unreadable proxy = %v", err)
	}
	e.store.fail["GetDestinationTarget"] = errors.New("down")
	if _, err := e.svc.Target(ctx, 7); err == nil || errors.Is(err, mattermost.ErrNoTarget) {
		t.Errorf("a failed read = %v", err)
	}
	delete(e.store.fail, "GetDestinationTarget")
	e.store.rows[c.ID].deleted = true
	if _, err := e.svc.Target(ctx, 7); !errors.Is(err, mattermost.ErrNoTarget) {
		t.Errorf("a deleted connection = %v", err)
	}
}

// TestCallbackConnection: the callback of button presses finds a Mattermost Connection by public_id with the client
// the adapter posts through; an unknown, deleted or Telegram Connection is mattermost.ErrNoConnection.
func TestCallbackConnection(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "mm")
	ctx := t.Context()
	e.store.targets[7] = memTarget{connection: c.ID, teamID: fakemattermost.TeamID, channelID: fakemattermost.ChannelAlerts}
	target, err := e.svc.Target(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := e.svc.Connection(ctx, c.PublicID)
	if err != nil || conn.ID != c.ID || conn.PublicID != c.PublicID || conn.Client != target.Client {
		t.Fatalf("Connection = %+v, %v; the target's client %p", conn, err, target.Client)
	}
	for _, id := range []string{"CN000000000000", "not-an-id"} {
		if _, err := e.svc.Connection(ctx, id); !errors.Is(err, mattermost.ErrNoConnection) {
			t.Errorf("Connection(%q) = %v", id, err)
		}
	}
	e.store.addTelegram()
	if _, err := e.svc.Connection(ctx, telegramID); !errors.Is(err, mattermost.ErrNoConnection) {
		t.Errorf("a telegram connection = %v", err)
	}
	e.store.rows[c.ID].row.Proxy = []byte("{")
	e.store.rows[c.ID].row.Version++
	if _, err := e.svc.Connection(ctx, c.PublicID); err == nil || errors.Is(err, mattermost.ErrNoConnection) {
		t.Errorf("an unreadable proxy = %v", err)
	}
	e.store.fail["GetConnection"] = errors.New("down")
	if _, err := e.svc.Connection(ctx, c.PublicID); err == nil || errors.Is(err, mattermost.ErrNoConnection) {
		t.Errorf("a failed read = %v", err)
	}
	delete(e.store.fail, "GetConnection")
	e.store.rows[c.ID].deleted = true
	if _, err := e.svc.Connection(ctx, c.PublicID); !errors.Is(err, mattermost.ErrNoConnection) {
		t.Errorf("a deleted connection = %v", err)
	}
}
