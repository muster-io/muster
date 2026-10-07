// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package devmode is the development mode of `muster dev`: its published defaults, the rule that lets the environment
// replace them, the fake Alertmanager, Mattermost, Telegram and OIDC servers and the fake HTTP and SOCKS5 proxies on
// fixed loopback addresses, the demo OIDC configuration, and Muster itself in the same process, against the
// development database and migrated on start, with the demo Integration that the fake Alertmanager sends to; and the
// development clock that every replica of the development database shares.
package devmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/devmode/dbgen"
	"github.com/muster-io/muster/internal/fakes/fakealertmanager"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeoidc"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/oidc"
)

// The development defaults, published so that anyone can run Muster locally.
//
//nolint:gosec // G101: published development credentials, never used outside development mode
const (
	// DevelopmentKey is the development master key; the server command refuses it outside development mode.
	DevelopmentKey = keyring.DevelopmentKey
	PublicURL      = "http://localhost:8080"
	DatabaseURL    = "postgres://muster:muster@127.0.0.1:55432/muster?sslmode=disable"
	AdminEmail     = "admin@example.org"
	AdminPassword  = "muster-dev-password"
	// OIDCClientID and OIDCClientSecret are the client of the demo OIDC configuration; the fake IdP accepts any secret.
	OIDCClientID     = "muster-dev"
	OIDCClientSecret = "muster-dev-oidc-secret"
	OIDCDisplayName  = "Dev IdP"
	// IngestURL is MUSTER_INGEST_URL in development mode: the ingest listener of `muster dev`.
	IngestURL = "http://localhost:8081"
	// IntegrationName is the demo Integration, IntegrationToken its published token and IntegrationReceiver the
	// fake Alertmanager's receiver that sends with it.
	IntegrationName     = "dev-alertmanager"
	IntegrationToken    = "mstr_int_devdevdevdevdevdevdevdevdevdevdevdevdevdevdevdevdevd"
	IntegrationReceiver = "muster"
)

const (
	AlertmanagerAddr = "127.0.0.1:19093"
	MattermostAddr   = "127.0.0.1:18065"
	TelegramAddr     = "127.0.0.1:18081"
	OIDCAddr         = "127.0.0.1:18090"
	HTTPProxyAddr    = "127.0.0.1:18091"
	SOCKSProxyAddr   = "127.0.0.1:18092"
	// LoopbackNetwork is added to the allowed networks of the development database, so that Muster may call the
	// fakes; a real installation keeps the standard policy.
	LoopbackNetwork = "127.0.0.0/8"

	// The listen addresses of `muster dev --replica`.
	ReplicaListenApp      = ":9080"
	ReplicaListenIngest   = ":9081"
	ReplicaListenInternal = ":9082"

	ReplicaLine = "muster dev: additional replica, no fake servers"

	shutdownTimeout = 5 * time.Second
)

// Env is the process environment, or a stand-in for it.
type Env interface {
	LookupEnv(name string) (string, bool)
	Setenv(name, value string) error
}

// OSEnv is the process environment.
type OSEnv struct{}

func (OSEnv) LookupEnv(name string) (string, bool) { return os.LookupEnv(name) }
func (OSEnv) Setenv(name, value string) error      { return os.Setenv(name, value) }

type setting struct {
	name, value string
	// alternatives are the other variables that, when set, configure the same thing; the default would then mix
	// with them or, for the database URL, win over them.
	alternatives []string
}

var settings = []setting{
	{name: "MUSTER_PUBLIC_URL", value: PublicURL},
	{name: "MUSTER_INGEST_URL", value: IngestURL},
	{name: "MUSTER_DATABASE_URL", value: DatabaseURL, alternatives: []string{
		"MUSTER_DATABASE_HOST", "MUSTER_DATABASE_PORT", "MUSTER_DATABASE_NAME", "MUSTER_DATABASE_USER",
		"MUSTER_DATABASE_PASSWORD", "MUSTER_DATABASE_PASSWORD_FILE", "MUSTER_DATABASE_SSLMODE",
	}},
	{name: "MUSTER_SECRET_KEYS", value: DevelopmentKey, alternatives: []string{"MUSTER_SECRET_KEYS_FILE"}},
	{name: "MUSTER_BOOTSTRAP_ADMIN_EMAIL", value: AdminEmail},
	{name: "MUSTER_BOOTSTRAP_ADMIN_PASSWORD", value: AdminPassword, alternatives: []string{
		"MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE",
	}},
}

var replicaSettings = []setting{
	{name: "MUSTER_LISTEN_APP", value: ReplicaListenApp},
	{name: "MUSTER_LISTEN_INGEST", value: ReplicaListenIngest},
	{name: "MUSTER_LISTEN_INTERNAL", value: ReplicaListenInternal},
}

// Applied names the variables, never their values: Defaults took their development default, FromEnv were set in the
// environment and kept a default out.
type Applied struct {
	Defaults []string
	FromEnv  []string
}

// Apply sets the development defaults in env. A variable counts as set when it is present, even if empty; a set
// variable replaces its default and never adds to it, so a MUSTER_SECRET_KEYS from the environment is the whole
// Keyring. With replica, the listen addresses of an additional replica are defaults too.
func Apply(env Env, replica bool) (Applied, error) {
	all := settings
	if replica {
		all = append(all[:len(all):len(all)], replicaSettings...)
	}
	var a Applied
	for _, s := range all {
		var set []string
		for _, name := range append([]string{s.name}, s.alternatives...) {
			if _, ok := env.LookupEnv(name); ok {
				set = append(set, name)
			}
		}
		if len(set) > 0 {
			a.FromEnv = append(a.FromEnv, set...)
			continue
		}
		if err := env.Setenv(s.name, s.value); err != nil {
			return a, fmt.Errorf("set %s: %w", s.name, err)
		}
		a.Defaults = append(a.Defaults, s.name)
	}
	return a, nil
}

// Addresses are the listen addresses of the fake servers and the fake proxies.
type Addresses struct {
	Alertmanager, Mattermost, Telegram, OIDC string
	HTTPProxy, SOCKSProxy                    string
}

// FakeAddresses are the fixed addresses that later checks and the second replica rely on.
func FakeAddresses() Addresses {
	return Addresses{Alertmanager: AlertmanagerAddr, Mattermost: MattermostAddr, Telegram: TelegramAddr,
		OIDC: OIDCAddr, HTTPProxy: HTTPProxyAddr, SOCKSProxy: SOCKSProxyAddr}
}

// OIDCDemo is the demo OIDC configuration that `muster dev` stores at its first start on a database: OIDC against the
// fake IdP, with muster-admins mapped to Admin and oncall to Responder.
func OIDCDemo() oidc.Demo {
	return oidc.Demo{
		IssuerURL: "http://" + OIDCAddr, ClientID: OIDCClientID, ClientSecret: OIDCClientSecret,
		DisplayName: OIDCDisplayName, AllowNetwork: LoopbackNetwork,
		Mappings: []oidc.GroupMapping{{Group: "muster-admins", Role: "admin"}, {Group: "oncall", Role: "responder"}},
	}
}

// IntegrationDemo is the demo Integration that `muster dev` ensures at start: dev-alertmanager with the Static label
// cluster=dev, its Heartbeat on, and the published token that the fake Alertmanager's receiver muster and its
// Heartbeat sender send with.
func IntegrationDemo() integrations.Demo {
	return integrations.Demo{Name: IntegrationName, StaticLabels: map[string]string{"cluster": "dev"}, Heartbeat: true,
		Token: IntegrationToken, TokenName: "dev"}
}

// HeartbeatInterval is how often the fake Alertmanager signals the Heartbeat of the demo Integration.
const HeartbeatInterval = 60

// StartHeartbeat starts the fake Alertmanager's Heartbeat sender muster: a signal of the demo Integration to the
// Heartbeat endpoint under ingestURL every HeartbeatInterval seconds, until the fake servers stop.
func StartHeartbeat(ctx context.Context, f *fakealertmanager.Fake, ingestURL string) error {
	return f.StartHeartbeat(ctx, fakealertmanager.HeartbeatSender{Name: IntegrationReceiver,
		URL:   strings.TrimSuffix(ingestURL, "/") + integrations.HeartbeatPath,
		Token: IntegrationToken, IntervalSeconds: HeartbeatInterval})
}

// RegisterReceiver registers the receiver muster with the fake Alertmanager: the ingestion endpoint under ingestURL
// with the demo Integration's token.
func RegisterReceiver(f *fakealertmanager.Fake, ingestURL string) error {
	return f.Register(fakealertmanager.Receiver{Name: IntegrationReceiver,
		URL: strings.TrimSuffix(ingestURL, "/") + integrations.IngestPath, Token: IntegrationToken})
}

// Fakes are the running fake servers and fake proxies.
type Fakes struct {
	Alertmanager *fakealertmanager.Fake
	Mattermost   *fakemattermost.Fake
	Telegram     *faketelegram.Fake
	OIDC         *fakeoidc.Fake
	HTTPProxy    *fakeproxy.Server
	SOCKSProxy   *fakeproxy.Server
}

func (f *Fakes) servers() []*fakeserver.Server {
	return []*fakeserver.Server{f.Alertmanager.Server, f.Mattermost.Server, f.Telegram.Server, f.OIDC.Server}
}

// StartFakes starts the fake servers and the fake proxies; when one cannot listen, it closes those already started.
func StartFakes(ctx context.Context, addrs Addresses) (*Fakes, error) {
	f := &Fakes{
		Alertmanager: fakealertmanager.New(),
		Mattermost:   fakemattermost.New(),
		Telegram:     faketelegram.New(),
		OIDC:         fakeoidc.New(),
	}
	listen := []string{addrs.Alertmanager, addrs.Mattermost, addrs.Telegram, addrs.OIDC}
	for i, s := range f.servers() {
		if err := s.Start(ctx, listen[i]); err != nil {
			closeErr := closeAll(context.WithoutCancel(ctx), f.servers()[:i])
			return nil, errors.Join(fmt.Errorf("start the fake %s on %s: %w", s.Name(), listen[i], err), closeErr)
		}
	}
	// The proxies live as long as the process: their own context is not the one of the start.
	proxyCtx := context.WithoutCancel(ctx)
	var err error
	if f.HTTPProxy, err = fakeproxy.Start(proxyCtx, fakeproxy.HTTP, addrs.HTTPProxy, fakeproxy.Options{}); err != nil {
		closeErr := closeAll(context.WithoutCancel(ctx), f.servers())
		return nil, errors.Join(fmt.Errorf("start the fake HTTP proxy on %s: %w", addrs.HTTPProxy, err), closeErr)
	}
	if f.SOCKSProxy, err = fakeproxy.Start(proxyCtx, fakeproxy.SOCKS5, addrs.SOCKSProxy, fakeproxy.Options{}); err != nil {
		closeErr := errors.Join(f.HTTPProxy.Close(), closeAll(context.WithoutCancel(ctx), f.servers()))
		return nil, errors.Join(fmt.Errorf("start the fake SOCKS5 proxy on %s: %w", addrs.SOCKSProxy, err), closeErr)
	}
	return f, nil
}

// Close stops the fake Alertmanager's Heartbeat senders, the fake servers and the fake proxies, waiting at most until
// ctx ends.
func (f *Fakes) Close(ctx context.Context) error {
	f.Alertmanager.StopHeartbeats()
	return errors.Join(f.HTTPProxy.Close(), f.SOCKSProxy.Close(), closeAll(ctx, f.servers()))
}

func closeAll(ctx context.Context, servers []*fakeserver.Server) error {
	ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	var errs []error
	for _, s := range servers {
		errs = append(errs, s.Close(ctx))
	}
	return errors.Join(errs...)
}

// Run starts the fake servers at addrs, registers the receiver muster with the fake Alertmanager for MUSTER_INGEST_URL
// and starts its Heartbeat sender for the demo Integration, prints their addresses, then runs Muster in the same process with serve until ctx ends or serve fails; the fake
// servers stop after it.
func Run(ctx context.Context, w io.Writer, addrs Addresses, serve func(context.Context) error) error {
	f, err := StartFakes(ctx, addrs)
	if err != nil {
		return err
	}
	ingestURL, ok := OSEnv{}.LookupEnv("MUSTER_INGEST_URL")
	if !ok || ingestURL == "" {
		ingestURL = IngestURL
	}
	if err := RegisterReceiver(f.Alertmanager, ingestURL); err != nil {
		return errors.Join(err, f.Close(context.WithoutCancel(ctx)))
	}
	if err := StartHeartbeat(ctx, f.Alertmanager, ingestURL); err != nil {
		return errors.Join(err, f.Close(context.WithoutCancel(ctx)))
	}
	// Telegram comes last: the end-to-end harness waits for its line, printed once every fake listens.
	fmt.Fprintf(w, "muster dev: fake Alertmanager %s\n", f.Alertmanager.URL())
	fmt.Fprintf(w, "muster dev: fake Mattermost %s\n", f.Mattermost.URL())
	fmt.Fprintf(w, "muster dev: fake OIDC %s\n", f.OIDC.URL())
	fmt.Fprintf(w, "muster dev: fake HTTP proxy %s\n", f.HTTPProxy.Addr())
	fmt.Fprintf(w, "muster dev: fake SOCKS5 proxy %s\n", f.SOCKSProxy.Addr())
	fmt.Fprintf(w, "muster dev: fake Telegram %s\n", f.Telegram.URL())
	serveErr := serve(ctx)
	if err := f.Close(context.WithoutCancel(ctx)); err != nil {
		return errors.Join(serveErr, fmt.Errorf("stop the fake servers: %w", err))
	}
	return serveErr
}

// RunReplica is `muster dev --replica`: it starts no fake server and runs Muster with serve, an additional replica
// against the same database.
func RunReplica(ctx context.Context, w io.Writer, serve func(context.Context) error) error {
	fmt.Fprintln(w, ReplicaLine)
	return serve(ctx)
}

// ClockChannel is the LISTEN/NOTIFY channel on which a change of the development clock reaches every replica.
const ClockChannel = "dev_clock"

// ClockPath is where the internal listener serves the development clock in development mode.
const ClockPath = "/_dev/clock"

// maxAdvance bounds one advance of the development clock: ten years.
const maxAdvance = 10 * 365 * 24 * 60 * 60

// ClockQueries are the queries of the development clock; *dbgen.Queries implements them.
type ClockQueries interface {
	GetDevClockOffset(ctx context.Context) (int64, error)
	AdvanceDevClock(ctx context.Context, arg dbgen.AdvanceDevClockParams) (int64, error)
	NotifyDevClock(ctx context.Context, arg dbgen.NotifyDevClockParams) error
}

// ClockStore runs the queries alone or in one transaction.
type ClockStore interface {
	ClockQueries
	InTx(ctx context.Context, f func(ClockQueries) error) error
}

// NewClockStore is the ClockStore over the main pool.
func NewClockStore(pool *pgxpool.Pool) ClockStore {
	return clockStore{Queries: dbgen.New(pool), pool: pool}
}

type clockStore struct {
	*dbgen.Queries
	pool *pgxpool.Pool
}

func (s clockStore) InTx(ctx context.Context, f func(ClockQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(dbgen.New(tx)) })
}

// Clock is the development clock (C-01.FR-13): the offset of the business clock, stored in
// runtime_state.dev_clock_offset_seconds so that every replica of one database runs on the same clock. The real
// clock is never offset.
type Clock struct {
	store    ClockStore
	business *clock.Business
	// maintain runs partition maintenance as at the given time, so that rows written at the new time have their
	// partitions.
	maintain func(ctx context.Context, at time.Time) error
	// changed are told once this replica runs on a new offset, to wake what waits on the clock: the processing
	// worker and the timer worker, each re-reading its earliest deadline on the business clock.
	changed []func()
	mu      sync.Mutex
}

// NewClock returns the development clock that moves business; maintain runs partition maintenance as at a time, and
// each of changed that is not nil is called after each change of the offset, on every replica.
func NewClock(s ClockStore, business *clock.Business, maintain func(ctx context.Context, at time.Time) error,
	changed ...func()) *Clock {
	return &Clock{store: s, business: business, maintain: maintain, changed: changed}
}

// Load sets the business clock to the stored offset: at start, on each notification on ClockChannel and when the
// LISTEN is back after a loss.
func (c *Clock) Load(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	offset, err := c.store.GetDevClockOffset(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		offset, err = 0, nil
	}
	if err != nil {
		return fmt.Errorf("read the development clock: %w", err)
	}
	c.set(offset)
	return nil
}

func (c *Clock) set(offset int64) {
	if c.business.Offset() == time.Duration(offset)*time.Second {
		return
	}
	c.business.SetOffset(time.Duration(offset) * time.Second)
	for _, f := range c.changed {
		if f != nil {
			f()
		}
	}
}

// Advance moves the clock of every replica forward by seconds: it runs partition maintenance as at the new time,
// adds the seconds to the stored offset and notifies the replicas in one transaction, and returns once this replica
// runs on the new offset.
func (c *Clock) Advance(ctx context.Context, seconds int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The new time is the stored offset plus the advance, which another replica may have moved since this one read it.
	stored, err := c.store.GetDevClockOffset(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		stored, err = 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read the development clock: %w", err)
	}
	system := c.business.Now().Add(-c.business.Offset())
	at := system.Add(time.Duration(stored+seconds) * time.Second)
	if err := c.maintain(ctx, at); err != nil {
		return 0, fmt.Errorf("create the partitions of the new time: %w", err)
	}
	var offset int64
	err = c.store.InTx(ctx, func(q ClockQueries) error {
		var err error
		offset, err = q.AdvanceDevClock(ctx, dbgen.AdvanceDevClockParams{Seconds: seconds, UpdatedAt: at.UTC()})
		if err != nil {
			return fmt.Errorf("advance the development clock: %w", err)
		}
		if err := q.NotifyDevClock(ctx, dbgen.NotifyDevClockParams{Channel: ClockChannel,
			Payload: fmt.Sprint(offset)}); err != nil {
			return fmt.Errorf("notify the replicas: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	c.set(offset)
	return offset, nil
}

// clockState is the answer of the development clock: the business time and the offset.
type clockState struct {
	Now           time.Time `json:"now"`
	OffsetSeconds int64     `json:"offset_seconds"`
}

// Handler serves GET and POST on ClockPath: GET reads the clock, POST {"advance_seconds": N} advances it by N
// seconds, N between 0 and ten years; both answer {"now", "offset_seconds"}.
func (c *Clock) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+ClockPath, func(w http.ResponseWriter, _ *http.Request) {
		c.answer(w)
	})
	mux.HandleFunc("POST "+ClockPath, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			AdvanceSeconds *int64 `json:"advance_seconds"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || in.AdvanceSeconds == nil || *in.AdvanceSeconds < 0 ||
			*in.AdvanceSeconds > maxAdvance {
			fakeserver.WriteError(w, http.StatusBadRequest,
				fmt.Sprintf(`the body is {"advance_seconds": N} with N from 0 to %d`, maxAdvance))
			return
		}
		if _, err := c.Advance(r.Context(), *in.AdvanceSeconds); err != nil {
			fakeserver.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.answer(w)
	})
	return mux
}

func (c *Clock) answer(w http.ResponseWriter) {
	fakeserver.WriteJSON(w, http.StatusOK, clockState{Now: c.business.Now().UTC(), OffsetSeconds: c.OffsetSeconds()})
}

// OffsetSeconds is how far the business clock of this replica runs ahead of the system time.
func (c *Clock) OffsetSeconds() int64 {
	return int64(c.business.Offset() / time.Second)
}
