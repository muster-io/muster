// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package organization_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/organization/dbgen"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// heartbeatRow is the state of the outgoing heartbeat on the organization row, with the version of the resource.
type heartbeatRow struct {
	version   int64
	updatedAt time.Time
	attemptAt pgtype.Timestamptz
	ok        pgtype.Bool
	err       pgtype.Text
}

// setupOrganization migrates a new database with the Organization and returns its queries and id.
func setupOrganization(t *testing.T, s dbtest.Server) (*db.DB, *dbgen.Queries, int64) {
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
	return d, dbgen.New(d.Pool), org.ID
}

func readHeartbeat(t *testing.T, d *db.DB, orgID int64) heartbeatRow {
	t.Helper()
	var r heartbeatRow
	if err := d.Pool.QueryRow(t.Context(), `SELECT version, updated_at, outgoing_heartbeat_last_attempt_at,
		outgoing_heartbeat_last_ok, outgoing_heartbeat_last_error FROM organizations WHERE id = $1`, orgID).Scan(
		&r.version, &r.updatedAt, &r.attemptAt, &r.ok, &r.err); err != nil {
		t.Fatal(err)
	}
	return r
}

// settings is an update of the organization row that sets the URL of the outgoing heartbeat to ciphertext, nil to
// clear it.
func settings(orgID int64, ciphertext []byte, changed bool, now time.Time) dbgen.UpdateSettingsParams {
	d := organization.Defaults()
	mapping, _ := json.Marshal(d.SeverityMapping)
	styles, _ := json.Marshal(d.SeverityStyles)
	p := dbgen.UpdateSettingsParams{OrgID: orgID, Name: d.Name, TimeZone: d.TimeZone, WorkdayStart: d.WorkdayStart,
		WeekStart: d.WeekStart, SeverityLabel: d.SeverityLabel,
		SeverityMapping: mapping, SeverityStyles: styles, CriticalIsUrgent: d.CriticalIsUrgent,
		InstanceLabels: d.InstanceLabels, RetentionStoredSnapshotsDays: d.Retention.StoredSnapshotsDays,
		RetentionAlertDetailsDays:        d.Retention.AlertDetailsDays,
		RetentionAlertGroupSummariesDays: d.Retention.AlertGroupSummariesDays,
		RetentionAuditLogDays:            d.Retention.AuditLogDays,
		OidcTokenGraceSeconds:            int64(d.OIDCTokenGrace / time.Second), TotpRequired: "nobody",
		Proxy: []byte(`{"enabled": false}`), UrlCiphertext: ciphertext, UrlChanged: changed, Now: now}
	if ciphertext != nil {
		p.UrlKeyID = pgtype.Text{String: "k1", Valid: true}
		p.UrlUpdatedAt = pgtype.Timestamptz{Time: now, Valid: true}
	}
	return p
}

// TestOutgoingHeartbeatQueries covers the SQL of the outgoing heartbeat (C-19.FR-7) on every supported PostgreSQL:
// one claim per interval, a claim again after the interval and after the clock moved backward, the result stored
// only for the current attempt with the result before it returned, the version and updated_at of the resource left
// by both, and a changed URL clearing the result while another change keeps it.
func TestOutgoingHeartbeatQueries(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		d, q, org := setupOrganization(t, s)
		claim := func(now time.Time) (dbgen.ClaimOutgoingHeartbeatRow, error) {
			return q.ClaimOutgoingHeartbeat(ctx, dbgen.ClaimOutgoingHeartbeatParams{OrgID: org, Now: now,
				DueBefore: now.Add(-time.Minute)})
		}
		record := func(at time.Time, failure string) (pgtype.Bool, error) {
			return q.RecordOutgoingHeartbeat(ctx, dbgen.RecordOutgoingHeartbeatParams{OrgID: org, AttemptAt: at,
				Ok: failure == "", LastError: pgtype.Text{String: failure, Valid: failure != ""}})
		}

		// Without a URL nothing is claimed.
		if _, err := claim(t0); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("claim without a URL = %v", err)
		}
		if n, err := q.UpdateSettings(ctx, settings(org, []byte("ct-1"), true, t0)); err != nil || n != 1 {
			t.Fatalf("UpdateSettings = %d, %v", n, err)
		}
		base := readHeartbeat(t, d, org)

		// One claim per interval.
		t1 := t0.Add(10 * time.Second)
		row, err := claim(t1)
		if err != nil || string(row.OutgoingHeartbeatUrlCiphertext) != "ct-1" || row.OutgoingHeartbeatUrlKeyID.String != "k1" ||
			string(row.OutgoingHeartbeatProxy) != `{"enabled": false}` {
			t.Fatalf("claim = %+v, %v", row, err)
		}
		if _, err := claim(t1.Add(30 * time.Second)); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("a second claim in the interval = %v", err)
		}

		// The result of a stale attempt changes nothing; the current one is stored, with no result before it.
		if _, err := record(t1.Add(-time.Second), ""); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("record of a stale attempt = %v", err)
		}
		if r := readHeartbeat(t, d, org); !r.attemptAt.Time.Equal(t1) || r.ok.Valid || r.err.Valid {
			t.Errorf("after the stale record %+v", r)
		}
		if previous, err := record(t1, "answered 503 (transient)"); err != nil || previous.Valid {
			t.Errorf("record = %+v, %v", previous, err)
		}
		if r := readHeartbeat(t, d, org); !r.ok.Valid || r.ok.Bool || r.err.String != "answered 503 (transient)" {
			t.Errorf("after the failure %+v", r)
		}

		// After the interval the next claim goes through, and the result before it is returned.
		t2 := t1.Add(time.Minute)
		if _, err := claim(t2); err != nil {
			t.Fatalf("claim after the interval = %v", err)
		}
		if previous, err := record(t2, ""); err != nil || !previous.Valid || previous.Bool {
			t.Errorf("record after a failure = %+v, %v", previous, err)
		}
		if r := readHeartbeat(t, d, org); !r.ok.Bool || r.err.Valid || !r.attemptAt.Time.Equal(t2) {
			t.Errorf("after the success %+v", r)
		}

		// A clock that moved backward claims at once rather than waiting for the old attempt time.
		back := t1.Add(-5 * time.Minute)
		if _, err := claim(back); err != nil {
			t.Fatalf("claim after the clock moved back = %v", err)
		}
		if _, err := claim(back); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("a second claim at the moved-back time = %v", err)
		}

		// Claims and results are runtime state: the version and updated_at of the resource stay.
		if r := readHeartbeat(t, d, org); r.version != base.version || !r.updatedAt.Equal(base.updatedAt) {
			t.Errorf("version %d at %v, want %d at %v", r.version, r.updatedAt, base.version, base.updatedAt)
		}
		if previous, err := record(back, "answered 500 (transient)"); err != nil || !previous.Bool {
			t.Fatalf("record = %+v, %v", previous, err)
		}

		// A change that keeps the URL keeps the result; a changed URL clears it.
		t3 := t2.Add(time.Hour)
		if _, err := q.UpdateSettings(ctx, settings(org, []byte("ct-1"), false, t3)); err != nil {
			t.Fatal(err)
		}
		if r := readHeartbeat(t, d, org); r.version != base.version+1 || !r.attemptAt.Valid || !r.ok.Valid ||
			!r.err.Valid {
			t.Errorf("after a change that kept the URL %+v", r)
		}
		if _, err := q.UpdateSettings(ctx, settings(org, []byte("ct-2"), true, t3)); err != nil {
			t.Fatal(err)
		}
		if r := readHeartbeat(t, d, org); r.version != base.version+2 || !r.updatedAt.Equal(t3) || r.attemptAt.Valid ||
			r.ok.Valid || r.err.Valid {
			t.Errorf("after a changed URL %+v", r)
		}
		// The new URL is due at once; a cleared one is never claimed.
		if row, err := claim(t3); err != nil || string(row.OutgoingHeartbeatUrlCiphertext) != "ct-2" {
			t.Errorf("claim of the new URL = %+v, %v", row, err)
		}
		if _, err := q.UpdateSettings(ctx, settings(org, nil, true, t3)); err != nil {
			t.Fatal(err)
		}
		if _, err := claim(t3.Add(time.Hour)); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("claim of a cleared URL = %v", err)
		}
	})
}
