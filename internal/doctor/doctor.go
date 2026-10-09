// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package doctor is `muster doctor` (C-02.FR-14, FR-15): it checks the database, its connections, the Keyring and the
// clock, prints one line per check as `OK|WARN|FAIL <check>: <detail>` and reports whether any check failed. It only
// reads: both connections it opens run every statement in a read-only transaction, and it takes no --actor. It checks
// every Mattermost Connection and Destination as their checks do, in the background client class (C-13.FR-10), and
// every Telegram Connection with the steps of its check — the dry probe, getMe, getWebhookInfo — printing the failing
// step with its message (C-14.FR-11).
package doctor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/connections"
	cdb "github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/runtime"
)

// Status is the outcome of one check.
type Status string

const (
	OK   Status = "OK"
	Warn Status = "WARN"
	Fail Status = "FAIL"
)

// The checks, in the order they are printed.
const (
	CheckDatabase          = "database"
	CheckPostgreSQLVersion = "postgresql_version"
	CheckSessionConnection = "session_connection"
	CheckTLSMain           = "tls_main"
	CheckTLSSession        = "tls_session"
	CheckKeyCanary         = "key_canary"
	CheckKeyring           = "keyring"
	CheckClockSkew         = "clock_skew"
	// CheckConnections fails when the Connections and Destinations cannot be read; each one checked prints its own
	// line, "connection <name>" or "destination <name>".
	CheckConnections = "connections"
)

const (
	// checkTimeout bounds the whole run, so that an unreachable database cannot hang it.
	checkTimeout = 30 * time.Second
	closeTimeout = 5 * time.Second
	// messengerTimeout bounds the retries of the background class in each Connection and Destination check.
	messengerTimeout = 10 * time.Second
)

// Result is one printed line.
type Result struct {
	Status Status
	Check  string
	Detail string
}

func (r Result) String() string {
	return fmt.Sprintf("%-4s %s: %s", r.Status, r.Check, r.Detail)
}

// Conn is what the checks need of a connection; *pgx.Conn and *pgxpool.Conn implement it.
type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SessionConn is the session connection, which also receives notifications.
type SessionConn interface {
	Conn
	WaitForNotification(ctx context.Context) (*pgconn.Notification, error)
}

// Connections are the main and the session connection; Close closes both.
type Connections struct {
	Main    Conn
	Session SessionConn
	Close   func()
}

// Opener opens the two connections of cfg. The error of an unreachable connection names which one it is.
type Opener func(ctx context.Context, cfg config.Config) (Connections, error)

// Options are what the CLI gives the doctor.
type Options struct {
	// Environ is the environment in the form of os.Environ; only MUSTER_* variables are read.
	Environ []string
	// Out receives the lines.
	Out io.Writer
	// Development is `muster dev doctor`, which accepts the published development key like the server does in
	// development mode.
	Development bool

	// open and real are replaced by tests.
	open Opener
	real clock.Clock
	each time.Duration
}

// Run runs every check and prints its line. It returns false when a check failed, and an error when the settings
// cannot be read.
func Run(ctx context.Context, opts Options) (bool, error) {
	cfg, err := config.Load(opts.Environ)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	c := &checks{cfg: cfg, out: opts.Out, real: cmp.Or[clock.Clock](opts.real, clock.Real{}), ok: true,
		each: cmp.Or(opts.each, messengerTimeout)}
	c.keys, c.keysErr = keyring.Load(ctx, keyring.Env{Keys: cfg.SecretKeys, Source: cfg.SecretKeysSource},
		opts.Development)
	open := opts.open
	if open == nil {
		open = Open
	}
	conns, err := open(ctx, cfg)
	if err != nil {
		// Nothing else can be checked without the database.
		c := &checks{out: opts.Out}
		c.print(Fail, CheckDatabase, err.Error())
		return false, nil
	}
	defer conns.Close()
	c.conns = conns
	c.print(OK, CheckDatabase, "main and session connections reachable")
	c.version(ctx)
	c.session(ctx)
	c.tls(ctx, CheckTLSMain, conns.Main, cfg.Database.SSLMode)
	c.tls(ctx, CheckTLSSession, conns.Session, cfg.Session.SSLMode)
	state, read := c.canary(ctx)
	c.keyring(ctx, state, read)
	c.clockSkew(ctx)
	c.messengers(ctx)
	return c.ok, nil
}

// Open opens the main pool and takes one connection of it, then the session connection, and makes every
// transaction of both read-only.
func Open(ctx context.Context, cfg config.Config) (Connections, error) {
	d, err := db.Open(ctx, cfg.Database, cfg.Session)
	if err != nil {
		return Connections{}, err
	}
	main, err := d.Pool.Acquire(ctx)
	if err != nil {
		d.Close()
		return Connections{}, fmt.Errorf("the main connection is unreachable: %w", err)
	}
	session, err := d.ConnectSession(ctx)
	if err != nil {
		main.Release()
		d.Close()
		return Connections{}, fmt.Errorf("the session connection is unreachable: %w", err)
	}
	conns := Connections{Main: main, Session: session, Close: func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		_ = session.Close(closeCtx)
		main.Release()
		d.Close()
	}}
	if err := readOnly(ctx, conns); err != nil {
		conns.Close()
		return Connections{}, err
	}
	return conns, nil
}

func readOnly(ctx context.Context, conns Connections) error {
	for name, c := range map[string]Conn{db.ConnectionMain: conns.Main, db.ConnectionSession: conns.Session} {
		if _, err := c.Exec(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY"); err != nil {
			return fmt.Errorf("make the %s connection read-only: %w", name, err)
		}
	}
	return nil
}

type checks struct {
	cfg     config.Config
	out     io.Writer
	real    clock.Clock
	each    time.Duration
	conns   Connections
	keys    *keyring.Keyring
	keysErr error
	ok      bool
}

func (c *checks) print(s Status, check, detail string) {
	if s == Fail {
		c.ok = false
	}
	fmt.Fprintln(c.out, Result{Status: s, Check: check, Detail: oneLine(detail)})
}

// oneLine joins the lines of a multi-line error, such as pgx's list of the addresses it tried.
func oneLine(s string) string {
	var parts []string
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " ")
}

func (c *checks) version(ctx context.Context) {
	for _, q := range []Conn{c.conns.Main, c.conns.Session} {
		if err := db.CheckVersion(ctx, q); err != nil {
			c.print(Fail, CheckPostgreSQLVersion, err.Error())
			return
		}
	}
	var v string
	if err := c.conns.Main.QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
		c.print(Fail, CheckPostgreSQLVersion, fmt.Sprintf("read the PostgreSQL version: %v", err))
		return
	}
	c.print(OK, CheckPostgreSQLVersion, strings.Fields(v + " ")[0])
}

func (c *checks) session(ctx context.Context) {
	if err := db.CheckSession(ctx, c.conns.Session, c.conns.Main); err != nil {
		c.print(Fail, CheckSessionConnection, err.Error())
		return
	}
	c.print(OK, CheckSessionConnection, "advisory locks and LISTEN work")
}

func (c *checks) tls(ctx context.Context, check string, q Conn, mode string) {
	encrypted, err := db.Encrypted(ctx, q)
	switch {
	case err != nil:
		c.print(Fail, check, fmt.Sprintf("read the TLS state: %v", err))
	case encrypted:
		c.print(OK, check, "sslmode="+mode+", the connection is encrypted")
	default:
		c.print(Warn, check, "sslmode="+mode+", the connection is not encrypted")
	}
}

// canary decrypts the key canary with the environment's Keyring and returns the keyring state, with whether it could
// be read.
func (c *checks) canary(ctx context.Context) (keyring.State, bool) {
	row, err := keyring.NewStore(c.conns.Main).GetKeyringState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errors.New("the database has no key canary yet: Muster writes it at its first start")
	}
	if err != nil {
		c.print(Fail, CheckKeyCanary, fmt.Sprintf("read the key canary: %v", err))
		return keyring.State{}, false
	}
	st := keyring.State{ActiveKeyID: row.ActiveKeyID, CanaryKeyID: row.CanaryKeyID, Canary: row.CanaryCiphertext}
	switch {
	case c.keysErr != nil:
		c.print(Fail, CheckKeyCanary, c.keysErr.Error())
	// The doctor prints its findings; the canary's log lines are for the server.
	case c.keys.Open(ctx, logging.New(io.Discard, logging.LevelError), st) != nil:
		c.print(Fail, CheckKeyCanary, keyring.ErrKeyMismatch.Error())
	default:
		c.print(OK, CheckKeyCanary, "decrypts with key "+st.ActiveKeyID)
	}
	return st, true
}

// keyring checks that the Keyring holds the active key and reports the older keys that encrypted values still need.
func (c *checks) keyring(ctx context.Context, st keyring.State, read bool) {
	switch {
	case !read:
		c.print(Fail, CheckKeyring, "the active key is unknown: the key canary could not be read")
		return
	case c.keysErr != nil:
		c.print(Fail, CheckKeyring, "the master keys could not be loaded: "+c.keysErr.Error())
		return
	case !c.keys.Holds(st.ActiveKeyID):
		c.print(Fail, CheckKeyring, fmt.Sprintf("the active key %s is not in %s", st.ActiveKeyID, keySource(c.cfg)))
		return
	}
	usage, err := keyUsage(ctx, c.conns.Main)
	if err != nil {
		c.print(Fail, CheckKeyring, err.Error())
		return
	}
	delete(usage, st.ActiveKeyID)
	if len(usage) == 0 {
		c.print(OK, CheckKeyring, fmt.Sprintf("active key %s, no older key in use", st.ActiveKeyID))
		return
	}
	var older []string
	for _, id := range slices.Sorted(maps.Keys(usage)) {
		held := ""
		if !c.keys.Holds(id) {
			held = ", not in " + keySource(c.cfg)
		}
		older = append(older, fmt.Sprintf("%s (%d encrypted values%s)", id, usage[id], held))
	}
	c.print(Warn, CheckKeyring, fmt.Sprintf("active key %s; older keys still in use: %s", st.ActiveKeyID,
		strings.Join(older, ", ")))
}

func keySource(cfg config.Config) string {
	return cmp.Or(cfg.SecretKeysSource, keyring.SecretKeysVar)
}

// keyUsage counts the encrypted values of every Organization by key id.
func keyUsage(ctx context.Context, q Conn) (map[string]int64, error) {
	store := kdb.New(q)
	orgs, err := store.ListOrganizationIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the organizations: %w", err)
	}
	usage := map[string]int64{}
	for _, org := range orgs {
		rows, err := store.CountEncryptedValuesByKey(ctx, org)
		if err != nil {
			return nil, fmt.Errorf("count the encrypted values: %w", err)
		}
		for _, r := range rows {
			if r.KeyID.Valid {
				usage[r.KeyID.String] += r.Encrypted
			}
		}
	}
	return usage, nil
}

func (c *checks) clockSkew(ctx context.Context) {
	skew, err := runtime.MeasureSkew(ctx, c.conns.Main, c.real)
	switch {
	case err != nil:
		c.print(Warn, CheckClockSkew, "not measured: "+err.Error())
	case skew.Abs() > runtime.ClockSkewWarning:
		c.print(Warn, CheckClockSkew, fmt.Sprintf("%.3fs against the database clock, more than %v", skew.Seconds(),
			runtime.ClockSkewWarning))
	default:
		c.print(OK, CheckClockSkew, fmt.Sprintf("%.3fs", skew.Seconds()))
	}
}

// messengers checks the Connections and the Mattermost Destinations of every Organization, one line each, through the
// Organization's outbound address policy; the outbound log lines are for the server, not for the doctor.
func (c *checks) messengers(ctx context.Context) {
	orgs, err := kdb.New(c.conns.Main).ListOrganizationIDs(ctx)
	if err != nil {
		c.print(Fail, CheckConnections, fmt.Sprintf("list the organizations: %v", err))
		return
	}
	keys := c.keys
	if c.keysErr != nil {
		keys = nil
	}
	quiet := logging.New(io.Discard, logging.LevelError)
	for _, org := range orgs {
		svc := connections.New(connections.Config{OrgID: org, Keyring: keys, Log: quiet,
			Clocks: clock.Clocks{Business: c.real, Real: c.real},
			Network: mattermost.Network{Policy: organization.NewOutboundPolicies(organization.NewStore(c.conns.Main),
				org, c.real), Log: quiet, Real: c.real}})
		found, err := svc.Doctor(ctx, cdb.New(c.conns.Main), c.each)
		if err != nil {
			c.print(Fail, CheckConnections, err.Error())
			continue
		}
		for _, f := range found {
			switch {
			case f.OK() && f.Warning != "":
				c.print(Warn, f.Kind+" "+f.Name, f.Warning)
			case f.OK():
				c.print(OK, f.Kind+" "+f.Name, "ok")
			default:
				c.print(Fail, f.Kind+" "+f.Name, f.Message)
			}
		}
	}
}
