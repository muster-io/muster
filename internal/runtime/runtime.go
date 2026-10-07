// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package runtime wires the process: the server command, `muster migrate` and Muster inside `muster dev`. It owns the
// start-up order — settings, logger, the Keyring, connections and checks, migrations when enabled, the schema version
// check, the key canary and the start-up ensure steps (the partitions among them) under the migration lock, the
// replica key record, the listeners, then the Leader lock keeper, the clock skew check and the live updates — and the
// graceful shutdown on SIGTERM (C-02.FR-16).
package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/api"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	authdb "github.com/muster-io/muster/internal/auth/dbgen"
	"github.com/muster-io/muster/internal/buildinfo"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/heartbeat"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/leader"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/oidc"
	oidcdb "github.com/muster-io/muster/internal/oidc/dbgen"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/partitions"
	"github.com/muster-io/muster/internal/server"
	"github.com/muster-io/muster/internal/tokens"
	"github.com/muster-io/muster/internal/totp"
	"github.com/muster-io/muster/internal/users"
	usersdb "github.com/muster-io/muster/internal/users/dbgen"
	"github.com/muster-io/muster/web"
)

// IntegrationInfoInterval is how often muster_integration_info is read again from the database besides the hints of
// changes, which repairs a refresh that failed.
const IntegrationInfoInterval = time.Minute

// ShutdownGrace is process.shutdown_grace: from SIGTERM to exit, inside the chart's 30 s termination grace period.
const ShutdownGrace = 20 * time.Second

// Options are what the process gives the runtime.
type Options struct {
	// Environ is the environment in the form of os.Environ; only MUSTER_* variables are read.
	Environ []string
	// Stdout receives the log lines.
	Stdout io.Writer
	// Development is `muster dev`: the schema is migrated on start whatever MUSTER_MIGRATE_ON_START says, and the
	// published development key is accepted.
	Development bool

	// The fields below are replaced by tests.
	open             func(context.Context, config.Config) (database, error)
	grace            time.Duration
	serving          func(server.Addresses)
	keyRecordRefresh time.Duration
	// every makes the ticks of the Leader lock keeper, the clock skew check and the live updates.
	every func(time.Duration) (<-chan time.Time, func())
}

// database is what the runtime needs of internal/db; tests give a fake.
type database interface {
	Check(ctx context.Context, log *logging.Logger) error
	Migrate(ctx context.Context, log *logging.Logger) error
	CheckSchema(ctx context.Context, log *logging.Logger) error
	Ping(ctx context.Context) error
	RegisterMetrics()
	Close()
	WithMigrationLock(ctx context.Context, f func(context.Context) error) error
	KeyringStore() keyring.Store
	OrganizationStore() organization.Store
	UsersStore() users.Store
	AdminStore() users.AdminStore
	AuditReader() audit.ListQueries
	AuthStore() auth.Store
	TOTPStore() totp.Store
	SettingsStore() organization.SettingsStore
	OIDCStore() oidc.Store
	TokensStore() tokens.Store
	IntegrationsStore() integrations.Store
	// HeartbeatStore serves the Heartbeat endpoint and the Heartbeat check.
	HeartbeatStore() heartbeat.Store
	IngestStore() ingest.Store
	// ProcessStore serves Snapshot processing and the Alerts view; ClockStore the development clock.
	ProcessStore() ingest.ProcessStore
	// ReplayStore serves muster ingest replay.
	ReplayStore() ingest.ReplayStore
	ClockStore() devmode.ClockStore
	// SessionListenConn opens a session connection for the LISTEN of the live-update hints.
	SessionListenConn(ctx context.Context) (db.ListenConn, error)
	// LeaderSession opens a session connection for the Leader lock; PartitionSession one for partition maintenance.
	LeaderSession(ctx context.Context) (leader.Session, error)
	PartitionSession(ctx context.Context) (partitions.Session, error)
	LeaderStore() leader.Store
	ReplicaPruner() keyring.Pruner
	AuthPruner() auth.PruneQueries
	UsersPruner() users.PruneQueries
	OIDCPruner() oidc.PruneQueries
	// OIDCClaimer claims the due background re-checks of OIDC users with lease.
	OIDCClaimer(lease db.Lease) oidc.Claimer
	// Clock reads the database clock, for the clock skew check.
	Clock() rowQuerier
}

// pgDatabase is internal/db with the stores of the domain packages over its main pool.
type pgDatabase struct {
	*db.DB
}

func (d pgDatabase) KeyringStore() keyring.Store { return keyring.NewStore(d.Pool) }

func (d pgDatabase) OrganizationStore() organization.Store { return organization.NewStore(d.Pool) }

func (d pgDatabase) UsersStore() users.Store { return users.NewStore(d.Pool) }

func (d pgDatabase) AdminStore() users.AdminStore { return users.NewAdminStore(d.Pool) }

func (d pgDatabase) AuditReader() audit.ListQueries { return audit.NewListQueries(d.Pool) }

func (d pgDatabase) AuthStore() auth.Store { return auth.NewStore(d.Pool) }

func (d pgDatabase) TOTPStore() totp.Store { return totp.NewStore(d.Pool) }

func (d pgDatabase) SettingsStore() organization.SettingsStore {
	return organization.NewSettingsStore(d.Pool)
}

func (d pgDatabase) OIDCStore() oidc.Store { return oidc.NewStore(d.Pool) }

func (d pgDatabase) TokensStore() tokens.Store { return tokens.NewStore(d.Pool) }

func (d pgDatabase) IntegrationsStore() integrations.Store { return integrations.NewStore(d.Pool) }

func (d pgDatabase) HeartbeatStore() heartbeat.Store { return heartbeat.NewStore(d.Pool) }

func (d pgDatabase) IngestStore() ingest.Store { return ingest.NewStore(d.Pool) }

func (d pgDatabase) ProcessStore() ingest.ProcessStore { return ingest.NewProcessStore(d.Pool) }

func (d pgDatabase) ReplayStore() ingest.ReplayStore { return ingest.NewReplayStore(d.Pool) }

func (d pgDatabase) ClockStore() devmode.ClockStore { return devmode.NewClockStore(d.Pool) }

func (d pgDatabase) LeaderSession(ctx context.Context) (leader.Session, error) {
	return leader.Dial(d.ConnectSession, leader.ServerBound)(ctx)
}

func (d pgDatabase) PartitionSession(ctx context.Context) (partitions.Session, error) {
	c, err := d.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := leader.BoundSession(ctx, c, leader.ServerBound); err != nil {
		_ = c.Close(ctx)
		return nil, err
	}
	return c, nil
}

func (d pgDatabase) LeaderStore() leader.Store { return leader.NewStore(d.Pool) }

func (d pgDatabase) ReplicaPruner() keyring.Pruner { return kdb.New(d.Pool) }

func (d pgDatabase) AuthPruner() auth.PruneQueries { return authdb.New(d.Pool) }

func (d pgDatabase) UsersPruner() users.PruneQueries { return usersdb.New(d.Pool) }

func (d pgDatabase) OIDCPruner() oidc.PruneQueries { return oidcdb.New(d.Pool) }

func (d pgDatabase) OIDCClaimer(lease db.Lease) oidc.Claimer { return oidc.NewClaimer(d.Pool, lease) }

func (d pgDatabase) Clock() rowQuerier { return d.Pool }

func openDB(ctx context.Context, cfg config.Config) (database, error) {
	d, err := db.Open(ctx, cfg.Database, cfg.Session)
	if err != nil {
		return nil, err
	}
	return pgDatabase{d}, nil
}

// Run is the server: it starts in order and serves until ctx ends, then shuts down within the grace period and
// returns nil. A failure during startup or a listener that stops is returned; the caller exits non-zero.
func Run(ctx context.Context, opts Options) error {
	p, err := begin(ctx, opts)
	if errors.Is(err, errStopped) {
		return nil
	}
	if err != nil {
		return err
	}
	return p.serve(ctx)
}

// Migrate is `muster migrate`: settings, logger, the master keys, connections and checks, then the migrations under
// the migration lock. It refuses keys the server refuses, except the development key, which only the server command
// checks for. It takes no --actor (C-02.FR-15).
func Migrate(ctx context.Context, opts Options) error {
	cfg, log, err := setup(ctx, opts)
	if err != nil {
		return err
	}
	if _, err := loadKeyring(ctx, cfg, true); err != nil {
		return failed(ctx, log, err)
	}
	d, err := connect(ctx, opts, cfg, log)
	if err != nil {
		return err
	}
	defer d.Close()
	return failed(ctx, log, d.Migrate(ctx, log))
}

// PasswordReset is what `muster admin reset-password` asks for: the --actor name, the login and the new password.
type PasswordReset struct {
	Actor    string
	Login    string
	Password logging.Secret
}

// ResetPassword is `muster admin reset-password` (C-03.FR-11): settings, logger, connections and checks and the schema
// version check, then the reset of the account's password in the Organization, recorded in the Audit log with the
// --actor name. It returns the public_id of the account. It needs no master key: the Keyring is not opened.
func ResetPassword(ctx context.Context, opts Options, r PasswordReset) (string, error) {
	cfg, log, err := setup(ctx, opts)
	if err != nil {
		return "", err
	}
	d, err := connect(ctx, opts, cfg, log)
	if err != nil {
		return "", err
	}
	defer d.Close()
	if err := d.CheckSchema(ctx, log); err != nil {
		return "", failed(ctx, log, err)
	}
	org, err := d.OrganizationStore().GetOrganization(ctx)
	if err != nil {
		return "", failed(ctx, log, fmt.Errorf("read the organization: %w", err))
	}
	clocks, err := commandClocks(ctx, opts, d, log)
	if err != nil {
		return "", failed(ctx, log, err)
	}
	admin := users.NewAdmin(org.ID, d.AdminStore(), audit.NewWriter(log, clocks.Business), clocks.Business,
		cfg.PublicURL)
	return admin.ResetPassword(ctx, r.Actor, r.Login, string(r.Password))
}

// TOTPReset is what `muster admin reset-totp` asks for: the --actor name and the login.
type TOTPReset struct {
	Actor string
	Login string
}

// ResetTOTP is `muster admin reset-totp` (C-03.FR-11): settings, logger, connections and checks and the schema version
// check, then the removal of the account's TOTP and recovery codes in the Organization, which ends its sessions and
// is recorded in the Audit log with the --actor name. It returns the public_id of the account and whether it had
// TOTP. It needs no master key: the Keyring is not opened.
func ResetTOTP(ctx context.Context, opts Options, r TOTPReset) (string, bool, error) {
	cfg, log, err := setup(ctx, opts)
	if err != nil {
		return "", false, err
	}
	d, err := connect(ctx, opts, cfg, log)
	if err != nil {
		return "", false, err
	}
	defer d.Close()
	if err := d.CheckSchema(ctx, log); err != nil {
		return "", false, failed(ctx, log, err)
	}
	org, err := d.OrganizationStore().GetOrganization(ctx)
	if err != nil {
		return "", false, failed(ctx, log, fmt.Errorf("read the organization: %w", err))
	}
	clocks, err := commandClocks(ctx, opts, d, log)
	if err != nil {
		return "", false, failed(ctx, log, err)
	}
	factors := totp.New(org.ID, d.TOTPStore(), nil, audit.NewWriter(log, clocks.Business), clocks, nil)
	return factors.ResetByLogin(ctx, r.Actor, r.Login)
}

// IngestReplay is what `muster ingest replay` asks for: the period, as a duration and as given, the name of one
// Integration or empty for all, and the --actor name.
type IngestReplay struct {
	Since       time.Duration
	SinceText   string
	Integration string
	Actor       string
}

// ReplayIngest is `muster ingest replay` (C-06.FR-17, C-02.FR-15): settings, logger, connections and checks and the
// schema version check, then the Stored Snapshots of the period set back to pending in one transaction, recorded in
// the Audit log with the --actor name; the processing workers of the running replicas process them again. It needs no
// master key: the Keyring is not opened.
func ReplayIngest(ctx context.Context, opts Options, r IngestReplay) (ingest.Replayed, error) {
	cfg, log, err := setup(ctx, opts)
	if err != nil {
		return ingest.Replayed{}, err
	}
	d, err := connect(ctx, opts, cfg, log)
	if err != nil {
		return ingest.Replayed{}, err
	}
	defer d.Close()
	if err := d.CheckSchema(ctx, log); err != nil {
		return ingest.Replayed{}, failed(ctx, log, err)
	}
	org, err := d.OrganizationStore().GetOrganization(ctx)
	if err != nil {
		return ingest.Replayed{}, failed(ctx, log, fmt.Errorf("read the organization: %w", err))
	}
	clocks, err := commandClocks(ctx, opts, d, log)
	if err != nil {
		return ingest.Replayed{}, failed(ctx, log, err)
	}
	replayer := ingest.Replayer{OrgID: org.ID, Store: d.ReplayStore(), Audit: audit.NewWriter(log, clocks.Business),
		Log: log, Business: clocks.Business}
	return replayer.Replay(ctx, ingest.Replay{Since: r.Since, SinceText: r.SinceText, Integration: r.Integration,
		Actor: r.Actor})
}

// commandClocks are the clocks of a subcommand: under `muster dev` the business clock runs on the development clock
// that the replicas of the database share, so that what the command writes agrees with them.
func commandClocks(ctx context.Context, opts Options, d database, log *logging.Logger) (clock.Clocks, error) {
	clocks, business := clock.System()
	if !opts.Development {
		return clocks, nil
	}
	dev := devmode.NewClock(d.ClockStore(), business, func(context.Context, time.Time) error { return nil }, nil)
	if err := dev.Load(ctx); err != nil {
		return clocks, err
	}
	log.Log(ctx, logging.DevClockLoaded, logging.F("offset_seconds", dev.OffsetSeconds()))
	return clocks, nil
}

type process struct {
	opts       Options
	cfg        config.Config
	log        *logging.Logger
	db         database
	clocks     clock.Clocks
	keyring    *keyring.Keyring
	replica    *keyring.Recorder
	partitions *partitions.Maintainer
	keeper     *leader.Keeper
	api        http.Handler
	// hub, notices and listener carry the live-update hints to the streams of this replica.
	hub      *live.Hub
	notices  *live.Notices
	listener *db.Listener
	// orgID and signIn are the Organization and its OIDC service, for the background re-checks.
	orgID  int64
	signIn *oidc.Service
	// integrations and snapshots serve ingestion on the ingest listener besides the API, and signals the Heartbeat
	// endpoint; worker processes the Stored Snapshots, and scanner runs the Leader's Stale scan.
	integrations *integrations.Service
	snapshots    *ingest.Service
	signals      *heartbeat.Service
	worker       *ingest.Worker
	scanner      *ingest.Processor
	// devClock is the development clock of `muster dev`, nil outside development mode; clockMoved wakes the
	// Leader's Heartbeat check and Stale scan when it moves.
	devClock   *devmode.Clock
	clockMoved *leader.Wakes
}

func setup(ctx context.Context, opts Options) (config.Config, *logging.Logger, error) {
	cfg, err := config.Load(opts.Environ)
	if err != nil {
		return config.Config{}, nil, err
	}
	log := logging.New(opts.Stdout, cfg.LogLevel)
	log.CaptureStdlib(ctx)
	log.Log(ctx, logging.ProcessStarted, logging.F("version", buildinfo.Version), logging.F("commit", buildinfo.Commit))
	for _, c := range cfg.Conflicts {
		log.Log(ctx, logging.DatabaseSettingsConflict, logging.F("used", c.Used), logging.F("ignored", c.Ignored))
	}
	return cfg, log, nil
}

func loadKeyring(ctx context.Context, cfg config.Config, allowDevelopmentKey bool) (*keyring.Keyring, error) {
	return keyring.Load(ctx, keyring.Env{Keys: cfg.SecretKeys, Source: cfg.SecretKeysSource}, allowDevelopmentKey)
}

func connect(ctx context.Context, opts Options, cfg config.Config, log *logging.Logger) (database, error) {
	open := opts.open
	if open == nil {
		open = openDB
	}
	d, err := open(ctx, cfg)
	if err != nil {
		return nil, failed(ctx, log, err)
	}
	if err := d.Check(ctx, log); err != nil {
		d.Close()
		return nil, failed(ctx, log, err)
	}
	return d, nil
}

// begin runs the start-up order up to the listeners.
func begin(ctx context.Context, opts Options) (*process, error) {
	cfg, log, err := setup(ctx, opts)
	if err != nil {
		return nil, err
	}
	// Development mode accepts the published development key: muster dev, also with --replica.
	k, err := loadKeyring(ctx, cfg, opts.Development)
	if err != nil {
		return nil, failed(ctx, log, err)
	}
	d, err := connect(ctx, opts, cfg, log)
	if err != nil {
		return nil, err
	}
	if cfg.MigrateOnStart || opts.Development {
		if err := d.Migrate(ctx, log); err != nil {
			d.Close()
			return nil, failed(ctx, log, err)
		}
	}
	if err := d.CheckSchema(ctx, log); err != nil {
		d.Close()
		return nil, failed(ctx, log, err)
	}
	clocks, business := clock.System()
	p := &process{opts: opts, cfg: cfg, log: log, db: d, clocks: clocks, keyring: k}
	p.partitions = partitions.New(d.PartitionSession, clocks.Business, log)
	p.worker = &ingest.Worker{Log: log, Real: clocks.Real}
	if opts.Development {
		// The development clock comes before anything reads the business clock, the partitions among them.
		p.devClock = devmode.NewClock(d.ClockStore(), business, func(ctx context.Context, at time.Time) error {
			return partitions.New(d.PartitionSession, clock.NewManual(at), log).Maintain(ctx)
		}, p.worker.Wake)
		if err := p.devClock.Load(ctx); err != nil {
			d.Close()
			return nil, failed(ctx, log, err)
		}
		p.clockMoved = &leader.Wakes{}
		log.Log(ctx, logging.DevClockLoaded, logging.F("offset_seconds", p.devClock.OffsetSeconds()))
	}
	if err := p.bootstrap(ctx); err != nil {
		d.Close()
		return nil, failed(ctx, log, err)
	}
	if p.api, err = p.newAPI(ctx); err != nil {
		d.Close()
		return nil, failed(ctx, log, err)
	}
	if err := p.startReplica(ctx); err != nil {
		d.Close()
		return nil, failed(ctx, log, err)
	}
	p.configureWorker()
	return p, nil
}

// serve starts the listeners and serves until ctx ends or a listener stops.
func (p *process) serve(ctx context.Context) error {
	defer p.db.Close()
	p.db.RegisterMetrics()
	p.keeper = p.newKeeper()
	leader.Register(p.keeper)
	health := server.NewHealth(p.db.Ping)
	// Requests keep their context through the shutdown, so that the drain lets them finish.
	srv, err := server.Start(context.WithoutCancel(ctx), server.Addresses{
		App: p.cfg.ListenApp, Ingest: p.cfg.ListenIngest, Internal: p.cfg.ListenInternal,
	}, server.Handlers{
		App: server.App(p.api, web.Dist(), p.cfg.PublicURL.Scheme == "https"),
		Ingest: server.Ingest(ingest.NewHandler(ingest.HandlerConfig{
			Auth: p.integrations, Snapshots: p.snapshots, Log: p.log, Real: p.clocks.Real,
			TrustedProxies: p.cfg.TrustedProxies,
		}), heartbeat.NewHandler(heartbeat.HandlerConfig{
			Auth: p.integrations, Signals: p.signals, Log: p.log, TrustedProxies: p.cfg.TrustedProxies,
		})),
		Internal: p.internalHandler(health),
	})
	if err != nil {
		p.stopReplica(ctx)
		return failed(ctx, p.log, err)
	}
	addrs := srv.Addrs()
	p.log.Log(ctx, logging.ListenersStarted, logging.F("app", addrs.App), logging.F("ingest", addrs.Ingest),
		logging.F("internal", addrs.Internal))
	if p.opts.serving != nil {
		p.opts.serving(addrs)
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	keys := make(chan error, 1)
	go func() { keys <- p.watchKeys(watchCtx) }()
	stopWork := p.startWork(ctx)

	grace := cmp.Or(p.opts.grace, ShutdownGrace)
	var stopped error
	watching := true
	select {
	case <-ctx.Done():
		p.log.Log(ctx, logging.ShutdownRequested, logging.F("grace_seconds", grace.Seconds()))
	case stopped = <-srv.Errors():
		p.log.Log(ctx, logging.ListenerFailed, logging.F("listener", listenerOf(stopped)),
			logging.F("error", stopped.Error()))
	case stopped = <-keys:
		// The watch logged active_key_not_held; it ends without an error only when ctx ended.
		watching = false
		if stopped == nil {
			p.log.Log(ctx, logging.ShutdownRequested, logging.F("grace_seconds", grace.Seconds()))
		}
	}
	stopWatch()
	// The grace period counts from here: stopping the Leader work, the replica record and the drain all fit in it.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if watching {
		// A key watch that stopped the replica just as ctx ended still decides the exit.
		if err := <-keys; errors.Is(err, keyring.ErrKeyMismatch) {
			stopped = err
		}
	}
	// The Leader tasks stop and the lock goes at once, so that another replica takes over without waiting for a bound.
	stopWork(shutdownCtx)
	// The record goes before the drain, so that a refresh never writes it back.
	p.stopReplica(ctx)
	health.ShuttingDown()
	// Workers that later stories register stop claiming here and finish or release their rows within the grace.
	if err := srv.Shutdown(shutdownCtx); err != nil {
		p.log.Log(ctx, logging.ShutdownGraceExceeded, logging.F("grace_seconds", grace.Seconds()))
	}
	if stopped != nil {
		return stopped
	}
	p.log.Log(ctx, logging.ProcessStopped)
	return nil
}

// internalHandler serves health and metrics, and in development mode the development clock.
func (p *process) internalHandler(health *server.Health) http.Handler {
	internal := server.Internal(health, metrics.Handler(p.keeper.Leading))
	if p.devClock == nil {
		return internal
	}
	mux := http.NewServeMux()
	mux.Handle(devmode.ClockPath, p.devClock.Handler())
	mux.Handle("/", internal)
	return mux
}

func listenerOf(err error) string {
	if le, ok := errors.AsType[*server.ListenerError](err); ok {
		return le.Listener
	}
	return "unknown"
}

// errStopped is a startup that SIGTERM or an interrupt ended; the server then exits 0.
var errStopped = errors.New("stopped by a signal during startup")

// failed logs startup_failed for err and returns it; when ctx ended first, a signal stopped the startup and failed
// logs the shutdown instead and returns errStopped.
func failed(ctx context.Context, log *logging.Logger, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		log.Log(ctx, logging.ShutdownRequested, logging.F("grace_seconds", ShutdownGrace.Seconds()))
		log.Log(ctx, logging.ProcessStopped)
		return errStopped
	}
	log.Log(ctx, logging.StartupFailed, logging.F("error", err.Error()))
	return err
}

// newAPI builds the API of the app listener for the Organization, with the Roles loaded from role_permissions.
func (p *process) newAPI(ctx context.Context) (http.Handler, error) {
	orgID, err := p.organizationID(ctx)
	if err != nil {
		return nil, err
	}
	authStore := p.db.AuthStore()
	roles, err := auth.LoadRoles(ctx, authStore)
	if err != nil {
		return nil, err
	}
	w := audit.NewWriter(p.log, p.clocks.Business)
	sessions := auth.NewService(orgID, authStore, p.keyring, w, p.clocks.Business, roles)
	factors := totp.New(orgID, p.db.TOTPStore(), p.keyring, w, p.clocks, sessions)
	sessions.UseSecondFactor(factors)
	p.hub = live.NewHub(orgID, sessions.LiveSessions)
	leaderStore := p.db.LeaderStore()
	p.notices = live.NewNotices(func(ctx context.Context, now time.Time) ([]organization.Notice, error) {
		return leader.Notices(ctx, leaderStore, now)
	}, p.clocks.Business, p.hub, p.log)
	p.listener = db.NewListener(p.db.SessionListenConn, p.log)
	signIn := oidc.NewService(oidc.Config{
		OrgID: orgID, Store: p.db.OIDCStore(), Keyring: p.keyring, Audit: w, Clocks: p.clocks, Log: p.log,
		Sessions: sessions, PublicURL: p.cfg.PublicURL,
		Network: oidc.Network{
			Policy: organization.NewOutboundPolicies(p.db.OrganizationStore(), orgID, p.clocks.Real),
			Log:    p.log, Real: p.clocks.Real,
		},
	})
	p.orgID, p.signIn = orgID, signIn
	p.integrations = integrations.New(integrations.Config{
		OrgID: orgID, Store: p.db.IntegrationsStore(), Audit: w, Business: p.clocks.Business, IngestURL: p.cfg.IngestURL,
		RunbookBase: p.cfg.RunbookBaseURL.String(),
	})
	p.snapshots = ingest.New(orgID, p.db.IngestStore(), p.clocks.Business)
	p.signals = heartbeat.New(heartbeat.Config{OrgID: orgID, Store: p.db.HeartbeatStore(), Business: p.clocks.Business,
		Log: p.log, RunbookBase: p.cfg.RunbookBaseURL.String()})
	alerts := ingest.NewAlertsView(orgID, p.db.ProcessStore(), p.clocks.Business)
	if p.opts.Development {
		if err := signIn.EnsureDemo(ctx, devmode.OIDCDemo()); err != nil {
			return nil, fmt.Errorf("the demo OIDC configuration: %w", err)
		}
		if err := p.integrations.EnsureDemo(ctx, devmode.IntegrationDemo()); err != nil {
			return nil, fmt.Errorf("the demo integration: %w", err)
		}
	}
	return api.New(api.Config{
		Sessions:     sessions,
		Users:        users.NewService(orgID, p.db.UsersStore(), w, p.clocks.Business),
		Admin:        users.NewAdmin(orgID, p.db.AdminStore(), w, p.clocks.Business, p.cfg.PublicURL),
		AuditLog:     audit.NewReader(orgID, p.db.AuditReader()),
		TOTP:         factors,
		Organization: organization.NewService(orgID, p.db.SettingsStore(), w, p.clocks.Business),
		Notices:      p.notices,
		Live:         p.hub,
		OIDC:         signIn,
		Tokens: tokens.New(orgID, p.db.TokensStore(), w, p.clocks.Business, roles,
			tokens.NewLimiter(p.clocks.Real)),
		Integrations:   p.integrations,
		Snapshots:      p.snapshots,
		Alerts:         alerts,
		TrustedProxies: p.cfg.TrustedProxies,
		Log:            p.log,
		Real:           p.clocks.Real,
	})
}

// newKeeper is the Leader lock keeper of this replica with the Leader tasks of tasks.go.
func (p *process) newKeeper() *leader.Keeper {
	id := p.replica.ID()
	authPruner := auth.NewPruner(p.db.AuthPruner())
	return leader.NewKeeper(p.db.LeaderSession, p.clocks.Real, p.log, id, leader.Tasks(leader.Work{
		Alive:              leader.NewAlive(p.db.LeaderStore(), p.clocks, p.log, id),
		MaintainPartitions: p.partitions.Maintain,
		PruneReplicas: func(ctx context.Context) error {
			return keyring.PruneReplicas(ctx, p.db.ReplicaPruner(), p.log, p.clocks.Real.Now())
		},
		Organizations: func(ctx context.Context) ([]int64, error) {
			return p.db.LeaderStore().ListOrganizationIDs(ctx)
		},
		Business: p.clocks.Business,
		Log:      p.log,
		IngestBacklog: func(ctx context.Context, orgs []int64) error {
			return ingest.Backlog(ctx, p.db.ProcessStore(), orgs)
		},
		AlertRetention: func(ctx context.Context, orgID int64, now time.Time) (int64, error) {
			return ingest.PruneAlerts(ctx, p.db.ProcessStore(), orgID, now)
		},
		HeartbeatCheck: (&heartbeat.Checker{Store: p.db.HeartbeatStore(), Business: p.clocks.Business, Log: p.log,
			RunbookBase: p.cfg.RunbookBaseURL.String()}).Check,
		StaleScan: func(ctx context.Context, orgID int64) error {
			if orgID != p.orgID {
				return nil
			}
			return p.scanner.StaleScan(ctx)
		},
		ClockMoved: p.clockMoved,
		PruneAuth: []leader.PruneTable{
			{Name: "sessions", Delete: authPruner.Sessions},
			{Name: "sign_in_throttles", Delete: authPruner.SignInThrottles},
		},
		PruneUsers: []leader.PruneTable{
			{Name: "password_setups", Delete: users.NewPruner(p.db.UsersPruner()).PasswordSetups},
		},
		PruneOIDC: []leader.PruneTable{
			{Name: "oidc_auth_requests", Delete: oidc.NewPruner(p.db.OIDCPruner()).AuthRequests},
		},
	}))
}

// startWork starts the Leader lock keeper, the clock skew check, the live updates — the Hub's session check, the
// notice watcher and the LISTEN of the hints — the worker of the OIDC re-checks, the writer of the use of Integration
// tokens and the refresh of muster_integration_info; the function it returns stops them all and waits for them until
// its context ends.
func (p *process) startWork(ctx context.Context) func(context.Context) {
	every := p.opts.every
	if every == nil {
		every = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	leaderTicks, stopLeaderTicks := every(leader.PingInterval)
	skewTicks, stopSkewTicks := every(SkewInterval)
	sessionTicks, stopSessionTicks := every(live.CheckInterval)
	noticeTicks, stopNoticeTicks := every(live.CheckInterval)
	infoTicks, stopInfoTicks := every(IntegrationInfoInterval)
	wg.Go(func() {
		defer stopLeaderTicks()
		p.keeper.Run(ctx, leaderTicks)
	})
	wg.Go(func() {
		defer stopSkewTicks()
		skewCheck{q: p.db.Clock(), real: p.clocks.Real, log: p.log}.run(ctx, skewTicks)
	})
	// The live-updates streams end when ctx does: the Hub closes them before the listeners drain.
	wg.Go(func() {
		defer stopSessionTicks()
		p.hub.Run(ctx, sessionTicks)
	})
	wg.Go(func() {
		defer stopNoticeTicks()
		p.notices.Run(ctx, noticeTicks)
	})
	// A hint about an Integration, maybe changed on another replica, refreshes muster_integration_info; so does a
	// LISTEN that is back after a loss, which may have missed hints. A stored Snapshot wakes the processing worker,
	// and in development mode a change of the development clock reloads it; each LISTEN in place does both, in case
	// a notification was missed.
	p.listener.Listen(ingest.SnapshotChannel, func(string) { p.worker.Wake() })
	if p.devClock != nil {
		p.listener.Listen(devmode.ClockChannel, func(string) {
			p.loadDevClock(ctx)
			p.clockMoved.Wake()
		})
	}
	wg.Go(func() {
		p.listener.Run(ctx, func(h db.Hint) {
			p.hub.Receive(h)
			if h.Type == integrations.Hint {
				p.integrations.InfoChanged()
			}
		}, func(restored bool) {
			p.hub.Listening(restored)
			if restored {
				p.integrations.InfoChanged()
				p.loadDevClock(ctx)
			}
			p.worker.Wake()
		})
	})
	wg.Go(func() { p.worker.Run(ctx) })
	wg.Go(func() { p.rechecker().Run(ctx) })
	wg.Go(func() { p.integrations.RunTouches(ctx) })
	wg.Go(func() {
		defer stopInfoTicks()
		p.integrations.RunInfo(ctx, infoTicks)
	})
	return func(wait context.Context) {
		cancel()
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-wait.Done():
		}
	}
}

// loadDevClock reads the development clock again after a change on any replica.
func (p *process) loadDevClock(ctx context.Context) {
	if p.devClock == nil {
		return
	}
	if err := p.devClock.Load(ctx); err != nil {
		p.log.Log(ctx, logging.DevClockLoadFailed, logging.F("error", err.Error()))
		return
	}
	p.log.Log(ctx, logging.DevClockLoaded, logging.F("offset_seconds", p.devClock.OffsetSeconds()))
}

// configureWorker completes the processing worker of this replica (C-06.FR-1) over the Organization, with leases held
// by this replica's id, once both are known and before anything serves or runs that could wake it.
func (p *process) configureWorker() {
	processor := ingest.NewProcessor(ingest.ProcessorConfig{OrgID: p.orgID, Store: p.db.ProcessStore(),
		Business: p.clocks.Business, Log: p.log, RunbookBase: p.cfg.RunbookBaseURL.String(),
		Lease: db.Lease{Owner: p.replica.ID(), Duration: ingest.Lease, Clocks: p.clocks}})
	// The Stale scan claims Integrations under a lease of its own, so that it never takes over one that this replica's
	// worker processes.
	p.scanner = ingest.NewProcessor(ingest.ProcessorConfig{OrgID: p.orgID, Store: p.db.ProcessStore(),
		Business: p.clocks.Business, Log: p.log, RunbookBase: p.cfg.RunbookBaseURL.String(),
		Lease: db.Lease{Owner: p.replica.ID() + "/stale-scan", Duration: ingest.Lease, Clocks: p.clocks}})
	p.worker.Organizations = func(context.Context) ([]int64, error) { return []int64{p.orgID}, nil }
	p.worker.Processor = func(orgID int64) (*ingest.Processor, bool) { return processor, orgID == p.orgID }
}

// rechecker is the worker of the background re-checks of OIDC users on this replica (C-03.FR-30): it claims the due
// re-checks of the Organization with a lease held by this replica's id.
func (p *process) rechecker() oidc.Rechecker {
	id := p.replica.ID()
	leaderStore := p.db.LeaderStore()
	return oidc.Rechecker{
		Claim:         p.db.OIDCClaimer(db.Lease{Owner: id, Duration: oidc.RecheckLease, Clocks: p.clocks}),
		Owner:         id,
		Organizations: leaderStore.ListOrganizationIDs,
		Service: func(orgID int64) (*oidc.Service, bool) {
			return p.signIn, p.signIn != nil && orgID == p.orgID
		},
		Log: p.log,
	}
}
