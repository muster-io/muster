// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package links

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// realService is a Service over a migrated database of the server, with its Organization, and a counter of the
// open_group_updates rows organization_links that it has requested.
func realService(t *testing.T, s dbtest.Server) (*Service, *db.DB, int64, func() int) {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	if err := d.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), log, t0); err != nil {
		t.Fatal(err)
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
		FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	now := clock.NewManual(t0)
	svc := New(Config{OrgID: org.ID, Store: NewStore(d.Pool), Audit: audit.NewWriter(log, now),
		Business: now, Real: now, PublicURL: "https://muster.example.org/", Log: log})
	rows := func() int {
		t.Helper()
		var n int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM open_group_updates
			WHERE org_id = $1 AND kind = 'organization_links'`, org.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return svc, d, org.ID, rows
}

// TestIntegrationTableRerenderOnlyWhenRead is requestTableRerender on PostgreSQL: a change of a table no rule reads
// writes no open_group_updates row organization_links; one a rule reads by a literal name, on either side of a
// rename, and any table when a rule computes its table name, writes one.
func TestIntegrationTableRerenderOnlyWhenRead(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		svc, d, orgID, rows := realService(t, s)
		reset := func() {
			t.Helper()
			if _, err := d.Pool.Exec(ctx, `DELETE FROM open_group_updates WHERE org_id = $1`, orgID); err != nil {
				t.Fatal(err)
			}
		}
		table := func(name string) TableInput { return TableInput{Name: name, Columns: []string{"a"}} }
		want := func(what string, n int) {
			t.Helper()
			if got := rows(); got != n {
				t.Fatalf("%s: %d rows, want %d", what, got, n)
			}
			reset()
		}

		unused, err := svc.CreateTable(ctx, by, table("unused"))
		if err != nil {
			t.Fatal(err)
		}
		want("a table with no rule at all", 0)
		if _, err := svc.UpdateTable(ctx, by, unused.PublicID, nil, table("renamed")); err != nil {
			t.Fatal(err)
		}
		want("a rename with no rule", 0)

		reader, err := svc.CreateRule(ctx, by, RuleInput{Name: "reader", Scope: Scope{Type: ScopeAlertGroup},
			URLTemplate: `{{ lookup "grafana" .Labels.c "a" }}`})
		if err != nil {
			t.Fatal(err)
		}
		want("a new rule", 1)
		if _, err := svc.UpdateTable(ctx, by, unused.PublicID, nil, table("renamed again")); err != nil {
			t.Fatal(err)
		}
		want("a rename to a name no rule reads", 0)
		other, err := svc.CreateTable(ctx, by, table("grafana"))
		if err != nil {
			t.Fatal(err)
		}
		want("a table the rule reads by its name", 1)
		if _, err := svc.UpdateTable(ctx, by, unused.PublicID, nil, table("grafana 2")); err != nil {
			t.Fatal(err)
		}
		want("an unread table renamed to another unread name", 0)
		if err := svc.DeleteTable(ctx, by, unused.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		want("the deletion of an unread table", 0)
		if err := svc.DeleteRule(ctx, by, reader.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		reset()
		if err := svc.DeleteTable(ctx, by, other.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		want("the deletion of a table after its rule", 0)

		if _, err := svc.CreateRule(ctx, by, RuleInput{Name: "computed", Scope: Scope{Type: ScopeAlertGroup},
			URLTemplate: `{{ lookup .Labels.t .Labels.c "a" }}`}); err != nil {
			t.Fatal(err)
		}
		reset()
		anyTable, err := svc.CreateTable(ctx, by, table("any"))
		if err != nil {
			t.Fatal(err)
		}
		want("a table with a computed-name rule about", 1)
		if _, err := svc.UpdateTable(ctx, by, anyTable.PublicID, nil, table("any 2")); err != nil {
			t.Fatal(err)
		}
		want("a rename with a computed-name rule about", 1)
		if err := svc.DeleteTable(ctx, by, anyTable.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		want("a deletion with a computed-name rule about", 1)
	})
}

// TestIntegrationLinkChangesSerialize: a change of a table and the save of a rule that reads it, at the same time,
// wait for one another on the Organization's lock rather than deadlock, and one of them requests the re-render,
// whichever commits first. A transaction that holds the evaluation lock of the Organization, as a re-render batch
// does, blocks neither.
func TestIntegrationLinkChangesSerialize(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		svc, d, orgID, rows := realService(t, s)

		evaluation, err := d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer evaluation.Rollback(context.Background()) //nolint:errcheck // already committed or rolled back
		if _, err := evaluation.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int, hashint8($2::bigint))`,
			db.TeamEvaluationLockClass, orgID); err != nil {
			t.Fatal(err)
		}
		gate, err := d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Rollback(context.Background()) //nolint:errcheck // already committed or rolled back
		if _, err := gate.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int, hashint8($2::bigint))`,
			db.LinkRulesLockClass, orgID); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := svc.CreateRule(ctx, by, RuleInput{Name: "reader", Scope: Scope{Type: ScopeAlertGroup},
				URLTemplate: `{{ lookup "grafana" .Labels.c "a" }}`})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := svc.CreateTable(ctx, by, TableInput{Name: "grafana", Columns: []string{"a"}})
			errs <- err
		}()
		// Both wait on the gate; the rows say that neither has committed.
		time.Sleep(500 * time.Millisecond)
		if n := rows(); n != 0 {
			t.Fatalf("%d rows before the gate opened", n)
		}
		if err := gate.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if n := rows(); n != 1 {
			t.Errorf("%d rows after both committed, want 1: a rule and the table it reads were saved", n)
		}
	})
}
