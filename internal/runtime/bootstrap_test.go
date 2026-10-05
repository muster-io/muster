// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/server"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

func dbEnv(url string, extra ...string) []string {
	return append([]string{
		"MUSTER_DATABASE_URL=" + url,
		"MUSTER_PUBLIC_URL=http://localhost:8080",
		"MUSTER_MIGRATE_ON_START=true",
		"MUSTER_LISTEN_APP=127.0.0.1:0",
		"MUSTER_LISTEN_INGEST=127.0.0.1:0",
		"MUSTER_LISTEN_INTERNAL=127.0.0.1:0",
	}, extra...)
}

func query(t *testing.T, url, sql string) string {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(t.Context()))
	var out string
	if err := conn.QueryRow(t.Context(), sql).Scan(&out); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return out
}

// TestIntegrationStartup starts two replicas together on a new database: they write one canary with the first key and
// one Organization with its defaults and outbound address policy, and each records its keys while it serves. A later
// start changes nothing, and a Keyring without the active key stops with the canary error.
func TestIntegrationStartup(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		url := s.NewDatabase(t)
		// Both replicas start on the test goroutine's behalf; failures are checked here, never in a goroutine.
		type replica struct {
			cancel context.CancelFunc
			served chan struct{}
			done   chan error
		}
		replicas := make([]replica, 2)
		for i := range replicas {
			ctx, cancel := context.WithCancel(t.Context())
			r := replica{cancel: cancel, served: make(chan struct{}), done: make(chan error, 1)}
			replicas[i] = r
			opts := Options{Environ: dbEnv(url, "MUSTER_SECRET_KEYS="+testKey+","+otherKey), Stdout: io.Discard,
				serving: func(server.Addresses) { close(r.served) }}
			go func() { r.done <- Run(ctx, opts) }()
		}
		for i, r := range replicas {
			select {
			case <-r.served:
			case err := <-r.done:
				t.Fatalf("replica %d stopped before serving: %v", i, err)
			case <-time.After(30 * time.Second):
				t.Fatalf("replica %d did not serve within 30s", i)
			}
		}
		if got := query(t, url, "SELECT count(*)::text FROM replicas"); got != "2" {
			t.Errorf("%s replica records while two replicas serve", got)
		}
		for _, r := range replicas {
			r.cancel()
			if err := <-r.done; err != nil {
				t.Fatal(err)
			}
		}
		first := keyring.KeyID([]byte(strings.Repeat("k", 32)))
		state := query(t, url, "SELECT active_key_id || '|' || canary_key_id || '|' || encode(canary_ciphertext, 'hex') "+
			"FROM keyring_state")
		if !strings.HasPrefix(state, first+"|"+first+"|") {
			t.Errorf("keyring state %s, want the first key %s", state, first)
		}
		org := query(t, url, `SELECT concat_ws('|', count(*), min(public_id), min(name), min(time_zone),
			min(totp_required), min(retention_stored_snapshots_days), min(retention_alert_details_days),
			min(retention_alert_group_summaries_days), min(retention_audit_log_days), min(oidc_token_grace_seconds),
			bool_and(critical_is_urgent), min(array_to_string(instance_labels, ',')), min(severity_label),
			min(severity_mapping::text), min(severity_styles::text)) FROM organizations`)
		want := "|Muster|UTC|nobody|14|90|730|365|604800|t|pod,instance,container,endpoint|severity|" +
			`[{"level": "critical", "value": "critical"}, {"level": "warning", "value": "warning"}, ` +
			`{"level": "info", "value": "info"}, {"level": "info", "value": "none"}]|` +
			`[{"color": "#d32f2f", "emoji": "🟥", "level": "critical"}, {"color": "#f57c00", "emoji": "🟧", ` +
			`"level": "warning"}, {"color": "#1976d2", "emoji": "🟦", "level": "info"}]`
		if !strings.HasPrefix(org, "1|RG") || org[len("1|RG")+12:] != want {
			t.Errorf("organizations: %s\nwant 1|RG<12>%s", org, want)
		}
		policy := query(t, url, "SELECT concat_ws('|', count(*), min(policy), max(cardinality(allowed)), "+
			"max(cardinality(denied))) FROM outbound_policies")
		if policy != "1|standard|0|0" {
			t.Errorf("outbound policies: %s", policy)
		}
		if got := query(t, url, "SELECT count(*)::text FROM alert_group_counters"); got != "0" {
			t.Errorf("%s Alert Group counter rows: groups creates it with the first Alert Group", got)
		}
		if got := query(t, url, "SELECT count(*)::text FROM replicas"); got != "0" {
			t.Errorf("%s replica records after the shutdown", got)
		}

		// A second start with the keys in another order changes nothing.
		_, cancel, done := running(t, Options{Environ: dbEnv(url, "MUSTER_SECRET_KEYS="+otherKey+","+testKey),
			Stdout: io.Discard})
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if got := query(t, url, "SELECT active_key_id || '|' || canary_key_id || '|' || encode(canary_ciphertext, "+
			"'hex') FROM keyring_state"); got != state {
			t.Errorf("the keyring state changed: %s", got)
		}
		if got := query(t, url, `SELECT concat_ws('|', count(*), min(public_id)) FROM organizations`); got !=
			"1|"+org[2:16] {
			t.Errorf("the organization changed: %s", got)
		}

		var out syncBuffer
		err := Run(t.Context(), Options{Environ: dbEnv(url, "MUSTER_SECRET_KEYS="+otherKey), Stdout: &out})
		if !errors.Is(err, keyring.ErrKeyMismatch) {
			t.Fatalf("Run with another key = %v", err)
		}
		if got := names(out.events(t)); !strings.Contains(strings.Join(got, " "), "key_canary_failed startup_failed") {
			t.Errorf("events %v", got)
		}
	})
}
