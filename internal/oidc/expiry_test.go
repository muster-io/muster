// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	iadb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc/dbgen"
)

// expiryFake is the OIDC settings row and the synthetic Snapshots of the built-in Integration in memory; every
// Snapshot written stays pending, so the check reads its own raises and resolves back.
type expiryFake struct {
	settings *dbgen.OidcSetting
	bodies   [][]byte
	locks    int
	fail     map[string]error
}

func (f *expiryFake) InTx(_ context.Context, fn func(ExpiryQueries) error) error {
	if err := f.fail["InTx"]; err != nil {
		return err
	}
	return fn(f)
}

func (f *expiryFake) LockSettings(context.Context, int64) (int64, error) {
	f.locks++
	if err := f.fail["LockSettings"]; err != nil {
		return 0, err
	}
	if f.settings == nil {
		return 0, pgx.ErrNoRows
	}
	return 1, nil
}

func (f *expiryFake) GetSettings(context.Context, int64) (dbgen.OidcSetting, error) {
	if err := f.fail["GetSettings"]; err != nil {
		return dbgen.OidcSetting{}, err
	}
	return *f.settings, nil
}

func (f *expiryFake) FindBuiltinIntegration(context.Context, int64) (int64, error) { return 9, nil }

func (f *expiryFake) InsertInternalBody(_ context.Context, arg iadb.InsertInternalBodyParams) error {
	if err := f.fail["InsertInternalBody"]; err != nil {
		return err
	}
	f.bodies = append(f.bodies, arg.Body)
	return nil
}

func (f *expiryFake) InsertInternalSnapshot(context.Context, iadb.InsertInternalSnapshotParams) error {
	return nil
}

func (f *expiryFake) NotifyInternalSnapshot(context.Context, iadb.NotifyInternalSnapshotParams) error {
	return nil
}

func (f *expiryFake) ListOpenInternalAlerts(context.Context, iadb.ListOpenInternalAlertsParams) (
	[]iadb.ListOpenInternalAlertsRow, error) {
	return nil, f.fail["ListOpenInternalAlerts"]
}

func (f *expiryFake) ListPendingInternalRaises(context.Context, int64) ([][]byte, error) {
	return f.bodies, nil
}

func (f *expiryFake) expires(d *time.Time) {
	f.settings = &dbgen.OidcSetting{OrgID: 1}
	if d != nil {
		f.settings.ClientSecretExpiresOn = pgtype.Date{Time: *d, Valid: true}
	}
}

// snapshot decodes the i-th synthetic Snapshot: its status, labels and annotations.
func (f *expiryFake) snapshot(t *testing.T, i int) (string, map[string]string, map[string]string) {
	t.Helper()
	var w struct {
		Alerts []struct {
			Status      string            `json:"status"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(f.bodies[i], &w); err != nil {
		t.Fatal(err)
	}
	a := w.Alerts[0]
	return a.Status, a.Labels, a.Annotations
}

var expiryNow = time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

func day(offset int) *time.Time {
	d := time.Date(2026, 10, 12+offset, 0, 0, 0, 0, time.UTC)
	return &d
}

func newExpiry(f *expiryFake) (ExpiryCheck, *clock.Manual, *bytes.Buffer) {
	c := clock.NewManual(expiryNow)
	var log bytes.Buffer
	return ExpiryCheck{Store: f, Business: c, Log: logging.New(&log, logging.LevelInfo)}, c, &log
}

// TestExpiryRaisesTenDaysAhead covers C-19.AC-5 on the check: a date 10 days ahead raises MusterOIDCSecretExpiring
// with severity warning, no labels besides alertname and severity and the runbook of the development version, logged
// once; a second run writes nothing more.
func TestExpiryRaisesTenDaysAhead(t *testing.T) {
	f := &expiryFake{}
	f.expires(day(10))
	ch, _, log := newExpiry(f)
	for range 2 {
		if err := ch.Check(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.bodies) != 1 {
		t.Fatalf("%d snapshots", len(f.bodies))
	}
	status, labels, annotations := f.snapshot(t, 0)
	if status != "firing" || len(labels) != 2 || labels["alertname"] != "MusterOIDCSecretExpiring" ||
		labels["severity"] != "warning" || annotations["runbook_url"] !=
		"https://muster-io.github.io/muster/latest/operations/runbooks/MusterOIDCSecretExpiring/" {
		t.Errorf("raise %s %v %v", status, labels, annotations)
	}
	if n := strings.Count(log.String(), `"event":"oidc_secret_expiring"`); n != 1 ||
		!strings.Contains(log.String(), `"expires_on":"2026-10-22"`) {
		t.Errorf("log:\n%s", log.String())
	}
}

// TestExpiryResolvesWhenMovedOut covers C-19.AC-5: moving the date 30 days ahead resolves the alert, once.
func TestExpiryResolvesWhenMovedOut(t *testing.T) {
	f := &expiryFake{}
	f.expires(day(10))
	ch, _, _ := newExpiry(f)
	if err := ch.Check(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	f.expires(day(30))
	for range 2 {
		if err := ch.Check(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.bodies) != 2 {
		t.Fatalf("%d snapshots", len(f.bodies))
	}
	if status, labels, _ := f.snapshot(t, 1); status != "resolved" || labels["alertname"] != "MusterOIDCSecretExpiring" {
		t.Errorf("resolve %s %v", status, labels)
	}
}

// TestExpiryResolvesWhenRemoved: removing the date, or the settings, resolves the alert.
func TestExpiryResolvesWhenRemoved(t *testing.T) {
	for _, remove := range []func(f *expiryFake){
		func(f *expiryFake) { f.expires(nil) },
		func(f *expiryFake) { f.settings = nil },
	} {
		f := &expiryFake{}
		f.expires(day(3))
		ch, _, _ := newExpiry(f)
		if err := ch.Check(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
		remove(f)
		if err := ch.Check(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
		if status, _, _ := f.snapshot(t, len(f.bodies)-1); len(f.bodies) != 2 || status != "resolved" {
			t.Errorf("%d snapshots, the last %s", len(f.bodies), status)
		}
	}
}

// TestExpiryWithoutSettings: without OIDC settings, or without a date, nothing is raised.
func TestExpiryWithoutSettings(t *testing.T) {
	f := &expiryFake{}
	ch, _, _ := newExpiry(f)
	if err := ch.Check(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	f.expires(nil)
	if err := ch.Check(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) != 0 || f.locks != 2 {
		t.Errorf("%d snapshots, %d locks", len(f.bodies), f.locks)
	}
}

// TestExpiryBoundary: a date exactly oidc.secret_expiry_lead away does not raise; a second later on the business
// clock it does.
func TestExpiryBoundary(t *testing.T) {
	f := &expiryFake{}
	f.expires(day(14))
	ch, c, _ := newExpiry(f)
	c.Set(*day(0))
	if err := ch.Check(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) != 0 {
		t.Fatal("raised exactly at the lead")
	}
	c.Advance(time.Second)
	if err := ch.Check(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) != 1 {
		t.Fatal("not raised past the lead")
	}
}

func TestExpiryErrors(t *testing.T) {
	down := errors.New("down")
	for _, name := range []string{"InTx", "LockSettings", "GetSettings", "ListOpenInternalAlerts",
		"InsertInternalBody"} {
		f := &expiryFake{fail: map[string]error{name: down}}
		f.expires(day(1))
		ch, _, log := newExpiry(f)
		if err := ch.Check(t.Context(), 1); !errors.Is(err, down) {
			t.Errorf("%s: %v", name, err)
		}
		if log.Len() != 0 {
			t.Errorf("%s: logged %s", name, log.String())
		}
	}
	f := &expiryFake{fail: map[string]error{}}
	f.expires(day(1))
	ch, _, _ := newExpiry(f)
	if err := ch.Check(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	f.expires(day(40))
	f.fail["InsertInternalBody"] = down
	if err := ch.Check(t.Context(), 1); !errors.Is(err, down) {
		t.Errorf("a failed resolve: %v", err)
	}
}
