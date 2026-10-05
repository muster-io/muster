// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package devmode is the development mode of `muster dev`: its published defaults, the rule that lets the environment
// replace them, the fake Alertmanager, Mattermost and Telegram servers on fixed loopback addresses, and Muster itself
// in the same process, against the development database and migrated on start.
package devmode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakealertmanager"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/keyring"
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
)

const (
	AlertmanagerAddr = "127.0.0.1:19093"
	MattermostAddr   = "127.0.0.1:18065"
	TelegramAddr     = "127.0.0.1:18081"

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

// Addresses are the listen addresses of the fake servers.
type Addresses struct {
	Alertmanager, Mattermost, Telegram string
}

// FakeAddresses are the fixed addresses that later checks and the second replica rely on.
func FakeAddresses() Addresses {
	return Addresses{Alertmanager: AlertmanagerAddr, Mattermost: MattermostAddr, Telegram: TelegramAddr}
}

// Fakes are the running fake servers.
type Fakes struct {
	Alertmanager *fakealertmanager.Fake
	Mattermost   *fakemattermost.Fake
	Telegram     *faketelegram.Fake
}

func (f *Fakes) servers() []*fakeserver.Server {
	return []*fakeserver.Server{f.Alertmanager.Server, f.Mattermost.Server, f.Telegram.Server}
}

// StartFakes starts the three fake servers; when one cannot listen, it closes those already started.
func StartFakes(ctx context.Context, addrs Addresses) (*Fakes, error) {
	f := &Fakes{
		Alertmanager: fakealertmanager.New(),
		Mattermost:   fakemattermost.New(),
		Telegram:     faketelegram.New(),
	}
	listen := []string{addrs.Alertmanager, addrs.Mattermost, addrs.Telegram}
	for i, s := range f.servers() {
		if err := s.Start(ctx, listen[i]); err != nil {
			closeErr := closeAll(context.WithoutCancel(ctx), f.servers()[:i])
			return nil, errors.Join(fmt.Errorf("start the fake %s on %s: %w", s.Name(), listen[i], err), closeErr)
		}
	}
	return f, nil
}

// Close stops the fake servers, waiting at most until ctx ends.
func (f *Fakes) Close(ctx context.Context) error {
	return closeAll(ctx, f.servers())
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

// Run starts the fake servers at addrs, prints their addresses, then runs Muster in the same process with serve until
// ctx ends or serve fails; the fake servers stop after it.
func Run(ctx context.Context, w io.Writer, addrs Addresses, serve func(context.Context) error) error {
	f, err := StartFakes(ctx, addrs)
	if err != nil {
		return err
	}
	for _, s := range f.servers() {
		fmt.Fprintf(w, "muster dev: fake %s %s\n", s.Name(), s.URL())
	}
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
