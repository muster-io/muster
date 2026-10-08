// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/internalalerts"
)

// alertsFingerprintKey is the unique constraint of an Alert's fingerprint in its Integration.
const alertsFingerprintKey = "alerts_integration_id_fingerprint_key"

// item is a pending Stored Snapshot the worker holds, parsed: its groupKey and the fingerprints it lists decide which
// earlier Snapshots it waits for. A deletion marker is a barrier: it waits for every earlier Snapshot and holds back
// every later one. A body that does not parse waits for nothing but a barrier and fails in its lane.
type item struct {
	row          dbgen.ListPendingSnapshotsRow
	payload      *Payload
	parseErr     error
	groupKey     string
	fingerprints map[string]bool
	barrier      bool
	deletion     internalalerts.Deletion
	started      bool
}

func newItem(row dbgen.ListPendingSnapshotsRow) *item {
	it := &item{row: row}
	payload, err := ParsePayload(row.Body)
	if err != nil {
		it.parseErr = err
		return it
	}
	it.payload, it.groupKey = &payload, payload.GroupKey
	it.fingerprints = make(map[string]bool, len(payload.Alerts))
	for _, a := range payload.Alerts {
		it.fingerprints[a.Fingerprint] = true
	}
	d, marker := internalalerts.DeletionOf(row.Body)
	it.barrier, it.deletion = marker && row.Source == SourceInternal, d
	return it
}

// waitsFor reports whether it, which arrived later, must wait for the earlier Snapshot e to finish: a barrier on
// either side, the same groupKey, or a fingerprint both list.
func (it *item) waitsFor(e *item) bool {
	switch {
	case it.barrier || e.barrier:
		return true
	case it.parseErr != nil || e.parseErr != nil:
		return false
	case it.groupKey == e.groupKey:
		return true
	}
	small, large := it.fingerprints, e.fingerprints
	if len(small) > len(large) {
		small, large = large, small
	}
	for fp := range small {
		if large[fp] {
			return true
		}
	}
	return false
}

// laneResult is how processing a held Snapshot in a lane ended.
type laneResult struct {
	it   *item
	lane int
	err  error
}

// run is the processing of one claimed Integration (C-06.FR-1): it holds a window of the oldest pending Stored
// Snapshots in arrival order, starts each in a free lane once every earlier one it waits for has finished, renews the
// lease, and stops when none is left, the lease is lost, ctx ends or a lane fails in a way that leaves its Snapshot
// pending; then it waits for the lanes in flight.
type run struct {
	p             *Processor
	integrationID int64
	horizon       time.Time
	window        []*item
	lanes         []bool
	inFlight      int
	results       chan laneResult
	processed     int
	lost          bool
	err           error
}

func (r *run) stopping() bool { return r.lost || r.err != nil }

// stop records why the run stops; the first reason wins.
func (r *run) stop(err error) {
	if r.err == nil {
		r.err = err
	}
}

// renew extends the lease; a lost one stops the run without an error.
func (r *run) renew(ctx context.Context) error {
	_, err := r.p.renew(ctx, r.p.store, r.integrationID)
	if errors.Is(err, errLeaseLost) {
		r.lost = true
		return nil
	}
	return err
}

func (r *run) loop(ctx context.Context) (int, error) {
	r.lanes = make([]bool, r.p.lanes)
	tick := time.NewTicker(r.p.renewEvery)
	defer tick.Stop()
	done := ctx.Done()
	for {
		if !r.stopping() {
			if err := r.fill(ctx); err != nil {
				r.stop(err)
			}
		}
		if !r.stopping() {
			r.start(ctx)
		}
		if r.inFlight == 0 && (r.stopping() || len(r.window) == 0) {
			return r.processed, r.err
		}
		select {
		case res := <-r.results:
			r.finished(res)
		case <-tick.C:
			if !r.stopping() {
				if err := r.renew(ctx); err != nil {
					r.stop(err)
				}
			}
		case <-done:
			done = nil
			r.stop(ctx.Err())
		}
	}
}

// fill reads more pending Stored Snapshots when the window has room for a lane's worth of them, or is empty.
func (r *run) fill(ctx context.Context) error {
	size := windowPerLane * r.p.lanes
	room := size - len(r.window)
	if room < r.p.lanes && len(r.window) > 0 {
		return nil
	}
	held := make([]int64, len(r.window))
	for i, it := range r.window {
		held[i] = it.row.ID
	}
	page := int32(min(room, maxClaim)) //nolint:gosec // G115: room is between 1 and windowPerLane × lanes
	rows, err := r.p.store.ListPendingSnapshots(ctx, dbgen.ListPendingSnapshotsParams{OrgID: r.p.orgID,
		IntegrationID: r.integrationID, Horizon: r.horizon, HeldIds: held, PageSize: page})
	if err != nil {
		return fmt.Errorf("read the pending snapshots: %w", err)
	}
	for _, row := range rows {
		r.add(newItem(row))
	}
	return nil
}

// add places a newly read Snapshot in arrival order among the held ones that have not started: one that became
// visible late, with an earlier receipt than Snapshots already held, goes before the later ones that wait. Those that
// started stay where they are, and it waits for them where waitsFor says so.
func (r *run) add(it *item) {
	at := len(r.window)
	for i := len(r.window) - 1; i >= 0; i-- {
		e := r.window[i]
		if e.started || !before(it.row, e.row) {
			break
		}
		at = i
	}
	r.window = slices.Insert(r.window, at, it)
}

// before reports whether a was received before b, in the order of the pending query: receipt, then id.
func before(a, b dbgen.ListPendingSnapshotsRow) bool {
	if !a.ReceivedAt.Equal(b.ReceivedAt) {
		return a.ReceivedAt.Before(b.ReceivedAt)
	}
	return a.ID < b.ID
}

// start starts, in arrival order, every held Snapshot that waits for no earlier unfinished one, while lanes are free.
func (r *run) start(ctx context.Context) {
	for i, it := range r.window {
		if r.inFlight == len(r.lanes) {
			return
		}
		if it.started || r.blocked(i) {
			continue
		}
		lane := r.freeLane()
		it.started, r.lanes[lane] = true, true
		r.inFlight++
		go func() {
			r.results <- laneResult{it: it, lane: lane, err: r.p.gated(ctx, func() error {
				return r.p.processInLane(ctx, r.integrationID, it, lane)
			})}
		}()
	}
}

// blocked reports whether the i-th held Snapshot waits for an earlier one that has not finished.
func (r *run) blocked(i int) bool {
	for _, e := range r.window[:i] {
		if r.window[i].waitsFor(e) {
			return true
		}
	}
	return false
}

func (r *run) freeLane() int {
	for i, busy := range r.lanes {
		if !busy {
			return i
		}
	}
	panic("ingest: no free lane") // start checks inFlight first
}

// finished takes the result of a lane: a Snapshot that left pending leaves the window; anything else stops the run.
func (r *run) finished(res laneResult) {
	r.lanes[res.lane] = false
	r.inFlight--
	switch {
	case res.err == nil:
		r.processed++
		for i, it := range r.window {
			if it == res.it {
				r.window = append(r.window[:i], r.window[i+1:]...)
				break
			}
		}
	case errors.Is(res.err, errLeaseLost):
		r.lost = true
	default:
		r.stop(res.err)
	}
}

// gated runs f once the Processor's gate has room, so that the lanes of all its Integrations together keep to half
// the main pool.
func (p *Processor) gated(ctx context.Context, f func() error) error {
	if p.gate == nil {
		return f()
	}
	select {
	case p.gate <- struct{}{}:
		defer func() { <-p.gate }()
		return f()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processInLane processes a held Snapshot, retrying it in its lane after a deadlock, a serialization failure or a
// fingerprint that another transaction inserted, up to laneRetries times with a short pause; only the Snapshots that
// wait for it wait meanwhile.
func (p *Processor) processInLane(ctx context.Context, integrationID int64, it *item, lane int) error {
	for attempt := 0; ; attempt++ {
		err := p.processOne(ctx, integrationID, it, lane)
		if err == nil || !retryable(err) || attempt >= laneRetries || ctx.Err() != nil {
			return err
		}
		t := time.NewTimer(p.pause(attempt))
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// retryable reports an error a lane retries at once: a deadlock or serialization failure (class 40), or the unique
// violation of a fingerprint that another transaction inserted first.
func retryable(err error) bool {
	pe, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		return false
	}
	return pe.Code[:2] == "40" || (pe.Code == "23505" && pe.ConstraintName == alertsFingerprintKey)
}
