// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

// Package dbtest runs the integration tests (make test-integration, build tag integration) against every supported
// PostgreSQL version, each in a container that testcontainers starts from a pinned image digest. A test package calls
// Main from its TestMain and gets a fresh database per test from NewDatabase.
package dbtest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Images are the supported PostgreSQL versions: the oldest, 14, and the newest; the development database and the
// compose example use the same 17 image.
var Images = []Image{
	{Version: "14", Ref: "postgres:14.24-alpine@sha256:4ea9e5ed06591da7ea23eb65465e8d3187fe79f4d5ec3ae976d29a33b013e77a"},
	{Version: "17", Ref: "postgres:17.11-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24"},
}

// Image is one PostgreSQL version and its pinned image.
type Image struct {
	Version string
	Ref     string
}

// Server is a running PostgreSQL; URL reaches its postgres database as the superuser, without TLS.
type Server struct {
	Version string
	URL     string
}

const startTimeout = 2 * time.Minute

var (
	servers  []Server
	startErr error
)

// Main starts a container per image, runs the tests of m and removes the containers; TestMain passes its result to
// os.Exit. When a container cannot start, every test that asks for a server fails with the reason: integration
// tests are never skipped.
func Main(ctx context.Context, m *testing.M) int {
	var containers []*postgres.PostgresContainer
	defer func() {
		for _, c := range containers {
			_ = testcontainers.TerminateContainer(c)
		}
	}()
	for _, img := range Images {
		startCtx, cancel := context.WithTimeout(ctx, startTimeout)
		c, err := postgres.Run(startCtx, img.Ref,
			postgres.WithDatabase("postgres"), postgres.WithUsername("postgres"), postgres.WithPassword("postgres"),
			postgres.BasicWaitStrategies())
		if c != nil {
			containers = append(containers, c)
		}
		if err != nil {
			cancel()
			startErr = fmt.Errorf("start PostgreSQL %s (%s): %w", img.Version, img.Ref, err)
			break
		}
		u, err := c.ConnectionString(startCtx, "sslmode=disable")
		cancel()
		if err != nil {
			startErr = fmt.Errorf("the address of PostgreSQL %s: %w", img.Version, err)
			break
		}
		servers = append(servers, Server{Version: img.Version, URL: u})
	}
	return m.Run()
}

// Servers are the running servers, in the order of Images.
func Servers() []Server { return servers }

// ForEach runs f as a subtest named after each PostgreSQL version.
func ForEach(t *testing.T, f func(t *testing.T, s Server)) {
	t.Helper()
	if startErr != nil {
		t.Fatal(startErr)
	}
	if len(servers) == 0 {
		t.Fatal("no PostgreSQL is running: call dbtest.Main from TestMain")
	}
	for _, s := range servers {
		t.Run("postgres"+s.Version, func(t *testing.T) { f(t, s) })
	}
}

// NewDatabase creates an empty database for the test and drops it when the test ends; it returns its URL.
func (s Server) NewDatabase(t testing.TB) string {
	t.Helper()
	name := "muster_test_" + strings.ToLower(rand.Text()[:12])
	if err := s.exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create the database %s on PostgreSQL %s: %v", name, s.Version, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if err := s.exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop the database %s: %v", name, err)
		}
	})
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Exec runs one statement on the postgres database of s.
func (s Server) Exec(t testing.TB, sql string) {
	t.Helper()
	if err := s.exec(t.Context(), sql); err != nil {
		t.Fatalf("%s on PostgreSQL %s: %v", sql, s.Version, err)
	}
}

func (s Server) exec(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, s.URL)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, sql)
	return errors.Join(err, conn.Close(context.WithoutCancel(ctx)))
}
