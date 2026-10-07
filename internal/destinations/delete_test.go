// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// fakeDestination is a row of destinations as a deletion sees it, with its Routes and its secrets.
type fakeDestination struct {
	id                  int64
	publicID, name      string
	version             int64
	deleted             bool
	routes              []int64
	secrets, named      int
	pendingFinalEdits   int
	internalAlertFiring bool
}

// fakeWriter is the database of a deletion in memory; InTx undoes what a failed transaction changed. bumped are the
// Routes the Routes hook gave a new version, in the order of the calls.
type fakeWriter struct {
	rows   []*fakeDestination
	audit  []auditdb.InsertAuditEntryParams
	hints  []db.Hint
	fail   map[string]error
	calls  map[string]int
	bumped []int64
}

func (f *fakeWriter) call(name string) error {
	f.calls[name]++
	return f.fail[name]
}

func (f *fakeWriter) find(id int64) *fakeDestination {
	for _, d := range f.rows {
		if d.id == id {
			return d
		}
	}
	return nil
}

func (f *fakeWriter) InTx(_ context.Context, fn func(TxQueries) error) error {
	saved := make([]fakeDestination, len(f.rows))
	for i, d := range f.rows {
		saved[i] = *d
	}
	audits, hints := len(f.audit), len(f.hints)
	if err := fn(f); err != nil {
		for i := range saved {
			*f.rows[i] = saved[i]
		}
		f.audit, f.hints = f.audit[:audits], f.hints[:hints]
		return err
	}
	return nil
}

func (f *fakeWriter) LockDestination(_ context.Context, arg dbgen.LockDestinationParams) (dbgen.LockDestinationRow,
	error) {
	if err := f.call("LockDestination"); err != nil {
		return dbgen.LockDestinationRow{}, err
	}
	for _, d := range f.rows {
		if d.publicID == arg.PublicID && !d.deleted && arg.OrgID == 1 {
			return dbgen.LockDestinationRow{ID: d.id, PublicID: d.publicID, Name: d.name, Version: d.version}, nil
		}
	}
	return dbgen.LockDestinationRow{}, pgx.ErrNoRows
}

func (f *fakeWriter) MarkDestinationDeleted(_ context.Context, arg dbgen.MarkDestinationDeletedParams) error {
	if err := f.call("MarkDestinationDeleted"); err != nil {
		return err
	}
	d := f.find(arg.ID)
	d.deleted, d.version = true, d.version+1
	return nil
}

func (f *fakeWriter) DeleteDestinationRoutes(_ context.Context, arg dbgen.DeleteDestinationRoutesParams) error {
	if err := f.call("DeleteDestinationRoutes"); err != nil {
		return err
	}
	f.find(arg.DestinationID).routes = nil
	return nil
}

func (f *fakeWriter) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := f.call("InsertAuditEntry"); err != nil {
		return err
	}
	f.audit = append(f.audit, arg)
	return nil
}

func (f *fakeWriter) Notify(_ context.Context, h db.Hint) error {
	if err := f.call("Notify"); err != nil {
		return err
	}
	f.hints = append(f.hints, h)
	return nil
}

func (f *fakeWriter) DB() dbgen.DBTX { return nil }

// routesHook stands for routing's DestinationDeleted: each Route the Destination still belongs to gets a new version.
func routesHook(f *fakeWriter) Routes {
	return func(_ context.Context, _ dbgen.DBTX, id int64) error {
		if err := f.call("routes"); err != nil {
			return err
		}
		f.bumped = append(f.bumped, f.find(id).routes...)
		return nil
	}
}

// retireHook stands for delivery's RetireDestination: the final edits wait, and the secrets are wiped and
// MusterDestinationBroken resolved at once when none waits.
func retireHook(f *fakeWriter, called *[]int64) Retire {
	return func(_ context.Context, _ dbgen.DBTX, id int64) error {
		*called = append(*called, id)
		if err := f.call("retire"); err != nil {
			return err
		}
		d := f.find(id)
		d.internalAlertFiring = false
		if d.pendingFinalEdits == 0 {
			d.secrets, d.named = 0, 0
		}
		return nil
	}
}

func newDeleter(t *testing.T) (*Service, *fakeWriter, *[]int64) {
	t.Helper()
	f := &fakeWriter{fail: map[string]error{}, calls: map[string]int{}, rows: []*fakeDestination{
		{id: 1, publicID: "DSAAAAAAAAAAA1", name: "ops", version: 2, routes: []int64{7, 8}, pendingFinalEdits: 2},
		{id: 3, publicID: "DSAAAAAAAAAAA3", name: "hook", version: 4, routes: []int64{7}, secrets: 3, named: 2,
			internalAlertFiring: true},
	}}
	var called []int64
	s := New(1, newStore())
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	s.SetWriter(WriterConfig{Writer: f, Audit: audit.NewWriter(logger, clock.NewManual(t0)),
		Business: clock.NewManual(t0), Routes: routesHook(f), Retire: retireHook(f, &called)})
	return s, f, &called
}

var deleter = Requester{Actor: audit.User(1, "USAAAAAAAAAAA1"), Transport: audit.TransportAPI}

// TestDelete is deleteDestination (C-11.FR-14): in one transaction the Destination is marked deleted with a new
// version, leaves every Route, delivery's hook gives its open Root messages the final edit — wiping its secrets at
// once when none waits — and destination.deleted is recorded with the hint; each of its Routes gets a new version
// through routing's hook before it leaves them; a stale If-Match, an unknown or a deleted Destination is refused and
// changes nothing.
func TestDelete(t *testing.T) {
	s, f, called := newDeleter(t)
	metrics.DestinationInfo.With("DSAAAAAAAAAAA3", "hook").Set(1)
	s.info["DSAAAAAAAAAAA3"] = "hook"
	if err := s.Delete(t.Context(), deleter, "DSAAAAAAAAAAA3", new(int64(3))); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	if err := s.Delete(t.Context(), deleter, "dsaaaaaaaaaaa3", new(int64(4))); err != nil {
		t.Fatal(err)
	}
	hook := f.find(3)
	if !hook.deleted || hook.version != 5 || hook.routes != nil || hook.secrets != 0 || hook.named != 0 ||
		hook.internalAlertFiring || !slices.Equal(*called, []int64{3}) || !slices.Equal(f.bumped, []int64{7}) {
		t.Errorf("deleted %+v, hook %v, routes %v", hook, *called, f.bumped)
	}
	e := f.audit[0]
	var diff []audit.Change
	_ = json.Unmarshal(e.Diff, &diff)
	if len(f.audit) != 1 || e.Action != ActionDeleted || e.ResourceType.String != ResourceDestination ||
		e.ResourcePublicID.String != "DSAAAAAAAAAAA3" || e.ResourceName.String != "hook" || len(diff) != 1 ||
		diff[0].Pointer != "/deleted_at" {
		t.Errorf("audit %+v %+v", e, diff)
	}
	if len(f.hints) != 1 || f.hints[0] != (db.Hint{OrgID: 1, Type: Hint, ID: "DSAAAAAAAAAAA3"}) {
		t.Errorf("hints %+v", f.hints)
	}
	if _, ok := s.info["DSAAAAAAAAAAA3"]; ok || strings.Contains(scrape(), `destination="DSAAAAAAAAAAA3"`) {
		t.Error("the info series of the deleted destination stays")
	}
	for _, id := range []string{"DSAAAAAAAAAAA3", "DSZZZZZZZZZZZZ", "nope"} {
		if err := s.Delete(t.Context(), deleter, id, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s = %v", id, err)
		}
	}

	// Final edits that wait keep the secrets until delivery wipes them after the last one.
	f.find(1).secrets = 1
	if err := s.Delete(t.Context(), deleter, "DSAAAAAAAAAAA1", nil); err != nil {
		t.Fatal(err)
	}
	if ops := f.find(1); !ops.deleted || ops.secrets != 1 || ops.routes != nil {
		t.Errorf("deleted with final edits waiting %+v", ops)
	}
}

// TestDeleteFailures: a failed step rolls the deletion back.
func TestDeleteFailures(t *testing.T) {
	for _, q := range []string{"LockDestination", "MarkDestinationDeleted", "routes", "DeleteDestinationRoutes",
		"retire", "InsertAuditEntry", "Notify"} {
		s, f, _ := newDeleter(t)
		f.fail[q] = errBoom
		if err := s.Delete(t.Context(), deleter, "DSAAAAAAAAAAA3", nil); !errors.Is(err, errBoom) {
			t.Errorf("%s = %v", q, err)
		}
		if d := f.find(3); d.deleted || d.routes == nil || d.secrets == 0 || len(f.audit) != 0 {
			t.Errorf("%s left %+v", q, d)
		}
	}
	s, f, _ := newDeleter(t)
	s.writer.Retire, s.writer.Routes = nil, nil
	if err := s.Delete(t.Context(), deleter, "DSAAAAAAAAAAA3", nil); err != nil || !f.find(3).deleted {
		t.Errorf("without a hook = %v", err)
	}
}
