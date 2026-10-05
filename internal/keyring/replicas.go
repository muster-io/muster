// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

const (
	// KeyRecordRefresh is replica.key_record_refresh: how often a replica refreshes its record.
	KeyRecordRefresh = 30 * time.Second
	// LiveExpiry is replica.live_expiry: a replica is live while its record is younger.
	LiveExpiry = 2 * time.Minute
	// PruneAfter is replica.prune_after: the Leader removes a record not refreshed for this long.
	PruneAfter = time.Hour
	// ReregisterAfter is leader.absence_notice: a replica that could not refresh its record for longer, because it
	// lost the database, re-registers with a new start time, so that it does not count as having run across the
	// outage and the next Leader records the downtime. internal/leader checks that the two agree.
	ReregisterAfter = 2 * time.Minute

	replicaSuffixLength = 8
)

// Replica is the record of a running replica: the ids of the keys it holds.
type Replica struct {
	ID          string
	Hostname    string
	Version     string
	KeyIDs      []string
	StartedAt   time.Time
	RefreshedAt time.Time
}

// LiveReplicas are the replicas whose record is younger than LiveExpiry at now, on the real clock.
func LiveReplicas(ctx context.Context, s Store, now time.Time) ([]Replica, error) {
	rows, err := s.ListLiveReplicas(ctx, now.Add(-LiveExpiry))
	if err != nil {
		return nil, fmt.Errorf("list the live replicas: %w", err)
	}
	out := make([]Replica, len(rows))
	for i, r := range rows {
		out[i] = Replica{ID: r.ReplicaID, Hostname: r.Hostname.String, Version: r.Version, KeyIDs: r.KeyIds,
			StartedAt: r.StartedAt, RefreshedAt: r.RefreshedAt}
	}
	return out, nil
}

// NewReplicaID is the id of a replica, chosen at process start: the host name and a random suffix.
func NewReplicaID(hostname string) string {
	suffix := strings.ToLower(rand.Text()[:replicaSuffixLength])
	if hostname == "" {
		return "replica-" + suffix
	}
	return hostname + "-" + suffix
}

// Recorder keeps the record of this replica in replicas, on the real clock, so that the development clock never
// makes a replica look gone, and stops the replica when the active key is one it does not hold.
type Recorder struct {
	keyring  *Keyring
	store    Store
	real     clock.Clock
	log      *logging.Logger
	id       string
	hostname string
	version  string
	started  time.Time
	// refreshed is the real time of the last record written, kept in memory.
	refreshed time.Time
}

// NewRecorder prepares the record of the replica id; Start writes it.
func NewRecorder(k *Keyring, s Store, realClock clock.Clock, log *logging.Logger, id, hostname, version string,
) *Recorder {
	return &Recorder{keyring: k, store: s, real: realClock, log: log, id: id, hostname: hostname, version: version}
}

// ID is the replica id.
func (r *Recorder) ID() string { return r.id }

// Start writes the record with the start time.
func (r *Recorder) Start(ctx context.Context) error {
	r.started = r.real.Now()
	return r.record(ctx, r.started)
}

// record writes the record at now. After a gap of more than ReregisterAfter since the last record written, the
// replica re-registers: its start time becomes now, as if it had just started.
func (r *Recorder) record(ctx context.Context, now time.Time) error {
	started := r.started
	if !r.refreshed.IsZero() && now.Sub(r.refreshed) > ReregisterAfter {
		started = now
	}
	err := r.store.RecordReplica(ctx, dbgen.RecordReplicaParams{
		ReplicaID:   r.id,
		Hostname:    pgtype.Text{String: r.hostname, Valid: r.hostname != ""},
		Version:     r.version,
		KeyIds:      r.keyring.KeyIDs(),
		StartedAt:   started,
		RefreshedAt: now,
	})
	if err != nil {
		return fmt.Errorf("record the keys of replica %s: %w", r.id, err)
	}
	r.started, r.refreshed = started, now
	return nil
}

// Refresh refreshes the record, then compares the active key with the keys held: a key that is not held logs
// active_key_not_held and returns ErrKeyMismatch; another held key becomes the Keyring's active key.
func (r *Recorder) Refresh(ctx context.Context) error {
	if err := r.record(ctx, r.real.Now()); err != nil {
		return err
	}
	active, err := r.store.GetActiveKeyID(ctx)
	if err != nil {
		return fmt.Errorf("read the active key: %w", err)
	}
	if !r.keyring.Holds(active) {
		r.log.Log(ctx, logging.ActiveKeyNotHeld, logging.F("active_key_id", active),
			logging.F("key_ids", r.keyring.KeyIDs()), logging.F("error", ErrKeyMismatch.Error()))
		return ErrKeyMismatch
	}
	return r.keyring.setActive(active)
}

// Run refreshes the record at every tick until ctx ends, when it returns nil, or until the active key is one this
// replica does not hold, when it returns ErrKeyMismatch. A failed refresh is logged and retried at the next tick.
func (r *Recorder) Run(ctx context.Context, ticks <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
		}
		// A hung connection must not stall the watch beyond one refresh period.
		refreshCtx, cancel := context.WithTimeout(ctx, KeyRecordRefresh)
		err := r.Refresh(refreshCtx)
		cancel()
		switch {
		case errors.Is(err, ErrKeyMismatch):
			return err
		case err != nil && ctx.Err() == nil:
			r.log.Log(ctx, logging.ReplicaRecordFailed, logging.F("replica", r.id), logging.F("error", err.Error()))
		}
	}
}

// Stop deletes the record, at a graceful shutdown.
func (r *Recorder) Stop(ctx context.Context) error {
	if err := r.store.DeleteReplica(ctx, r.id); err != nil {
		return fmt.Errorf("delete the record of replica %s: %w", r.id, err)
	}
	return nil
}

// Pruner deletes old replica records; *dbgen.Queries implements it.
type Pruner interface {
	PruneReplicas(ctx context.Context, refreshedBefore time.Time) (int64, error)
}

// PruneReplicas is the Leader task that removes the records not refreshed for PruneAfter at now, on the real clock,
// and logs replicas_pruned when it removed any. Running it twice removes nothing more.
func PruneReplicas(ctx context.Context, p Pruner, log *logging.Logger, now time.Time) error {
	n, err := p.PruneReplicas(ctx, now.Add(-PruneAfter))
	if err != nil {
		return fmt.Errorf("prune the replica records: %w", err)
	}
	if n > 0 {
		log.Log(ctx, logging.ReplicasPruned, logging.F("replicas", n))
	}
	return nil
}
