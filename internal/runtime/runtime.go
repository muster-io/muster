// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package runtime wires the process: the server command, `muster migrate` and Muster inside `muster dev`. It owns the
// start-up order — settings, logger, the Keyring, connections and checks, migrations when enabled, the schema version
// check, the key canary and the start-up ensure steps under the migration lock, the replica key record, the
// listeners — and the graceful shutdown on SIGTERM (C-02.FR-16).
package runtime

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/muster-io/muster/internal/buildinfo"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/server"
	"github.com/muster-io/muster/web"
)

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
}

// pgDatabase is internal/db with the stores of the domain packages over its main pool.
type pgDatabase struct {
	*db.DB
}

func (d pgDatabase) KeyringStore() keyring.Store { return keyring.NewStore(d.Pool) }

func (d pgDatabase) OrganizationStore() organization.Store { return organization.NewStore(d.Pool) }

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

type process struct {
	opts    Options
	cfg     config.Config
	log     *logging.Logger
	db      database
	clocks  clock.Clocks
	keyring *keyring.Keyring
	replica *keyring.Recorder
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
	clocks, _ := clock.System()
	p := &process{opts: opts, cfg: cfg, log: log, db: d, clocks: clocks, keyring: k}
	if err := p.bootstrap(ctx); err != nil {
		d.Close()
		return nil, failed(ctx, log, err)
	}
	if err := p.startReplica(ctx); err != nil {
		d.Close()
		return nil, failed(ctx, log, err)
	}
	return p, nil
}

// serve starts the listeners and serves until ctx ends or a listener stops.
func (p *process) serve(ctx context.Context) error {
	defer p.db.Close()
	p.db.RegisterMetrics()
	health := server.NewHealth(p.db.Ping)
	// Requests keep their context through the shutdown, so that the drain lets them finish.
	srv, err := server.Start(context.WithoutCancel(ctx), server.Addresses{
		App: p.cfg.ListenApp, Ingest: p.cfg.ListenIngest, Internal: p.cfg.ListenInternal,
	}, server.Handlers{
		App:      server.SPA(web.Dist()),
		Ingest:   http.NotFoundHandler(), // the ingestion routes arrive with C-05, C-07, C-13 and C-14
		Internal: server.Internal(health, metrics.Handler(nil)),
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
	if watching {
		// A key watch that stopped the replica just as ctx ended still decides the exit.
		if err := <-keys; errors.Is(err, keyring.ErrKeyMismatch) {
			stopped = err
		}
	}
	// The record goes before the drain, so that a refresh never writes it back.
	p.stopReplica(ctx)
	health.ShuttingDown()
	// Workers that later stories register stop claiming here and finish or release their rows within the grace.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		p.log.Log(ctx, logging.ShutdownGraceExceeded, logging.F("grace_seconds", grace.Seconds()))
	}
	if stopped != nil {
		return stopped
	}
	p.log.Log(ctx, logging.ProcessStopped)
	return nil
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
