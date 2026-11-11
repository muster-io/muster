// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package ingest_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/integrations"
)

// TestIntegrationSnapshotFailedWarning is C-06.FR-20 on the Integration against PostgreSQL: the warning snapshot_failed
// follows the newest webhook Stored Snapshot that is processed or failed, in the order of (received_at, id); pending
// ones are skipped and internal ones are not looked at.
func TestIntegrationSnapshotFailedWarning(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("warned"))
		if err != nil {
			t.Fatal(err)
		}
		seq := 0
		add := func(source, state string, at time.Time, processingError string) {
			t.Helper()
			seq++
			var processed *time.Time
			if state != "pending" {
				processed = &at
			}
			var msg *string
			if state == "failed" {
				msg = &processingError
			}
			if _, err := e.d.Pool.Exec(ctx, `INSERT INTO stored_snapshots (org_id, public_id, integration_id, source,
				received_at, body_day, body_sha256, size_bytes, state, processed_at, processing_error)
				VALUES ($1, $2, $3, $4, $5, ($5 AT TIME ZONE 'UTC')::date, decode(repeat('ab', 32), 'hex'), 2, $6, $7, $8)`,
				e.orgID, fmt.Sprintf("SS%012d", seq), in.ID, source, at, state, processed, msg); err != nil {
				t.Fatal(err)
			}
		}
		warning := func() *integrations.Warning {
			t.Helper()
			got, err := e.ints.Get(ctx, in.PublicID)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range got.Warnings {
				if w.Kind == integrations.WarningSnapshotFailed {
					return &w
				}
			}
			return nil
		}
		at := func(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

		if warning() != nil {
			t.Fatal("a warning without Snapshots")
		}
		add("webhook", "failed", at(1), "the webhook has no groupKey")
		if w := warning(); w == nil || w.Error != "the webhook has no groupKey" || !w.Since.Equal(at(1)) ||
			w.SnapshotID != "SS000000000001" {
			t.Fatalf("after a failed Snapshot: %+v", w)
		}
		add("webhook", "pending", at(2), "")
		if warning() == nil {
			t.Error("a pending Snapshot cleared the warning")
		}
		add("internal", "processed", at(3), "")
		add("internal", "failed", at(4), "an internal one")
		if w := warning(); w == nil || w.Error != "the webhook has no groupKey" {
			t.Errorf("internal Snapshots changed the warning: %+v", w)
		}
		add("webhook", "failed", at(5), "the webhook has no status")
		if w := warning(); w == nil || w.Error != "the webhook has no status" || w.SnapshotID != "SS000000000005" {
			t.Errorf("the newest failed Snapshot is not shown: %+v", w)
		}
		// The same receipt time: the higher id is the newer one.
		add("webhook", "processed", at(6), "")
		add("webhook", "failed", at(6), "the webhook has no alerts")
		if w := warning(); w == nil || w.Error != "the webhook has no alerts" {
			t.Errorf("the higher id at the same time did not win: %+v", w)
		}
		add("webhook", "processed", at(6), "")
		if warning() != nil {
			t.Error("a processed Snapshot with the highest id did not clear the warning")
		}
		add("webhook", "failed", at(7), "again")
		add("webhook", "processed", at(8), "")
		if warning() != nil {
			t.Error("a later processed Snapshot did not clear the warning")
		}
	})
}
