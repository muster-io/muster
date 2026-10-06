// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package internalalerts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// SnapshotChannel is the LISTEN/NOTIFY channel that wakes the processing workers when a Stored Snapshot was stored;
// the payload is a Notification.
const SnapshotChannel = "muster_snapshots"

// Notification is the payload of a notification on SnapshotChannel: the Integration whose pending Stored Snapshots
// grew.
type Notification struct {
	OrgID         int64 `json:"org_id"`
	IntegrationID int64 `json:"integration_id"`
}

// DefaultRunbookBase is MUSTER_RUNBOOK_BASE_URL when it is not set: the published documentation site.
const DefaultRunbookBase = "https://muster-io.github.io/muster"

// The groupKeys of synthetic Stored Snapshots: one per Internal alert, and the marker of a deleted Integration.
const (
	groupKeyPrefix   = `{}/{muster="internal"}`
	DeletionGroupKey = groupKeyPrefix + `:{event="integration_deleted"}`
)

// receiver is the receiver of synthetic Stored Snapshots.
const receiver = "muster"

// GroupKey is the groupKey of the synthetic Stored Snapshots of the Internal alert named name.
func GroupKey(name string) string {
	return groupKeyPrefix + `:{alertname="` + name + `"}`
}

// Store writes synthetic Stored Snapshots and reads the open Internal alerts; it runs inside the caller's
// transaction.
type Store interface {
	FindBuiltinIntegration(ctx context.Context, orgID int64) (int64, error)
	InsertInternalBody(ctx context.Context, arg dbgen.InsertInternalBodyParams) error
	InsertInternalSnapshot(ctx context.Context, arg dbgen.InsertInternalSnapshotParams) error
	NotifyInternalSnapshot(ctx context.Context, arg dbgen.NotifyInternalSnapshotParams) error
	ListOpenInternalAlerts(ctx context.Context, arg dbgen.ListOpenInternalAlertsParams) (
		[]dbgen.ListOpenInternalAlertsRow, error)
	ListPendingInternalRaises(ctx context.Context, orgID int64) ([][]byte, error)
}

// NewStore is the Store over a pool, a connection or a transaction.
func NewStore(d dbgen.DBTX) Store {
	return dbgen.New(d)
}

// ErrNoBuiltin is an Organization without the built-in Integration, which the start-up ensure step creates.
var ErrNoBuiltin = errors.New("the built-in Muster integration is missing")

// Entity is the configuration entity an Internal alert is about: its immutable public_id and its current name.
type Entity struct {
	ID   string
	Name string
}

// Raiser raises and resolves the Internal alerts of an Organization. Callers raise and resolve on transitions of the
// condition, not on every check.
type Raiser struct {
	orgID int64
	base  string
}

// NewRaiser returns the Raiser of the Organization orgID; runbookBase is MUSTER_RUNBOOK_BASE_URL, the base of each
// runbook_url, DefaultRunbookBase when empty.
func NewRaiser(orgID int64, runbookBase string) *Raiser {
	if runbookBase == "" {
		runbookBase = DefaultRunbookBase
	}
	return &Raiser{orgID: orgID, base: strings.TrimSuffix(runbookBase, "/")}
}

// Raise raises the Internal alert d about the entity e at now, with its extra labels.
func (r *Raiser) Raise(ctx context.Context, q Store, now time.Time, d *Definition, e Entity,
	extra map[string]string) error {
	labels := map[string]string{"alertname": d.Name, "severity": d.Severity}
	if d.Entity != "" {
		labels[d.Entity], labels[d.NameLabel] = e.ID, e.Name
	}
	for _, l := range d.Extra {
		labels[l] = extra[l]
	}
	return r.raise(ctx, q, now, d, labels, now)
}

// raise writes the synthetic Stored Snapshot of a firing Internal alert with the labels, firing since startsAt.
func (r *Raiser) raise(ctx context.Context, q Store, now time.Time, d *Definition, labels map[string]string,
	startsAt time.Time) error {
	name := ""
	if d.NameLabel != "" {
		name = labels[d.NameLabel]
	}
	annotations := map[string]string{
		"summary":     strings.ReplaceAll(d.Summary, "{name}", name),
		"description": strings.ReplaceAll(d.Description, "{name}", name),
		"runbook_url": r.base + "/" + d.Runbook(),
	}
	builtin, err := r.builtin(ctx, q)
	if err != nil {
		return err
	}
	return r.insert(ctx, q, builtin, now, webhookOf(d, StatusFiring, labels, annotations, startsAt))
}

// Resolve resolves the Internal alert d about the entity whose id is entityID, at now. A resolve of an Internal alert
// that does not fire changes nothing.
func (r *Raiser) Resolve(ctx context.Context, q Store, now time.Time, d *Definition, entityID string) error {
	labels := map[string]string{"alertname": d.Name, "severity": d.Severity}
	if d.Entity != "" {
		labels[d.Entity] = entityID
	}
	builtin, err := r.builtin(ctx, q)
	if err != nil {
		return err
	}
	return r.insert(ctx, q, builtin, now, webhookOf(d, StatusResolved, labels, map[string]string{}, now))
}

// Renamed raises again every Internal alert about the entity entity=id with the entity's new name, as the same
// firing: its startsAt is kept, so processing updates the name label in place (C-06.AC-11). It covers the Internal
// alerts that fire and those whose raise still waits for processing in the built-in Integration's queue, which would
// otherwise fire with the old name.
func (r *Raiser) Renamed(ctx context.Context, q Store, now time.Time, entity, id, name string) error {
	open, err := r.firingAbout(ctx, q, entity, id)
	if err != nil {
		return err
	}
	for _, fp := range slices.Sorted(maps.Keys(open)) {
		f := open[fp]
		d := Lookup(f.labels["alertname"])
		if d == nil || d.Entity != entity || f.labels[d.NameLabel] == name {
			continue
		}
		labels := maps.Clone(f.labels)
		labels[d.NameLabel] = name
		if err := r.raise(ctx, q, now, d, labels, f.startsAt); err != nil {
			return err
		}
	}
	return nil
}

// ResolveAbout resolves, at now, every Internal alert about the entity entity=id that fires or whose raise waits for
// processing, as the deletion of the entity does (C-06.FR-16). An entity with none writes nothing.
func (r *Raiser) ResolveAbout(ctx context.Context, q Store, now time.Time, entity, id string) error {
	open, err := r.firingAbout(ctx, q, entity, id)
	if err != nil {
		return err
	}
	for _, fp := range slices.Sorted(maps.Keys(open)) {
		d := Lookup(open[fp].labels["alertname"])
		if d == nil || d.Entity != entity {
			continue
		}
		if err := r.Resolve(ctx, q, now, d, id); err != nil {
			return err
		}
	}
	return nil
}

// firing is an Internal alert that fires, or will once its pending raise is processed.
type firing struct {
	labels   map[string]string
	startsAt time.Time
}

// firingAbout finds, by fingerprint, the Internal alerts about the entity entity=id that fire, as their pending
// synthetic Snapshots of the built-in Integration will leave them: a pending raise fires one, a pending resolve ends
// it.
func (r *Raiser) firingAbout(ctx context.Context, q Store, entity, id string) (map[string]firing, error) {
	contains, err := json.Marshal(map[string]string{entity: id})
	if err != nil {
		return nil, fmt.Errorf("encode the entity label: %w", err)
	}
	rows, err := q.ListOpenInternalAlerts(ctx, dbgen.ListOpenInternalAlertsParams{OrgID: r.orgID,
		Contains: contains})
	if err != nil {
		return nil, fmt.Errorf("list the open internal alerts of %s: %w", id, err)
	}
	open := map[string]firing{}
	for _, row := range rows {
		labels := map[string]string{}
		if err := json.Unmarshal(row.Labels, &labels); err != nil {
			return nil, fmt.Errorf("read the labels of internal alert %s: %w", row.Fingerprint, err)
		}
		open[row.Fingerprint] = firing{labels: labels, startsAt: row.StartsAt}
	}
	bodies, err := q.ListPendingInternalRaises(ctx, r.orgID)
	if err != nil {
		return nil, fmt.Errorf("list the pending internal snapshots: %w", err)
	}
	for _, body := range bodies {
		var w webhook
		if err := json.Unmarshal(body, &w); err != nil {
			continue // a body that processing will fail raises nothing
		}
		for _, a := range w.Alerts {
			if a.Labels[entity] != id {
				continue
			}
			startsAt, err := time.Parse(time.RFC3339Nano, a.StartsAt)
			switch {
			case err != nil:
			case a.Status == StatusResolved:
				delete(open, a.Fingerprint)
			default:
				open[a.Fingerprint] = firing{labels: a.Labels, startsAt: startsAt}
			}
		}
	}
	return open, nil
}

// MarkDeleted writes the marker of the deletion of the Integration integrationID (public_id publicID, named name)
// into its own queue: processing reaches it after the Stored Snapshots the Integration accepted before, and then
// resolves its Alerts and its Internal alerts (C-06.FR-16).
func (r *Raiser) MarkDeleted(ctx context.Context, q Store, now time.Time, integrationID int64, publicID,
	name string) error {
	w := webhook{Version: "4", GroupKey: DeletionGroupKey, Status: StatusResolved, Receiver: receiver,
		GroupLabels:       map[string]string{"event": "integration_deleted"},
		CommonLabels:      map[string]string{EntityIntegration: publicID, "integration_name": name},
		CommonAnnotations: map[string]string{"summary": "Integration " + name + " deleted"},
		Alerts:            []webhookAlert{}}
	return r.insert(ctx, q, integrationID, now, w)
}

// Deletion is what the marker of a deleted Integration says: the Integration's public_id and its name at deletion.
type Deletion struct {
	IntegrationID string
	Name          string
}

// DeletionOf reads the marker of a deleted Integration from the body of a synthetic Stored Snapshot; ok is false for
// any other body.
func DeletionOf(body []byte) (Deletion, bool) {
	var w webhook
	if err := json.Unmarshal(body, &w); err != nil || w.GroupKey != DeletionGroupKey {
		return Deletion{}, false
	}
	return Deletion{IntegrationID: w.CommonLabels[EntityIntegration], Name: w.CommonLabels["integration_name"]}, true
}

// Fingerprint is the fingerprint of an Internal alert: computed from alertname and the entity's id label only, so
// that a rename of the entity keeps it. It is Alertmanager's: FNV-1a over the label names in order, each name and value
// followed by the byte 0xff, as 16 hexadecimal digits.
func Fingerprint(d *Definition, labels map[string]string) string {
	ids := map[string]string{"alertname": d.Name}
	if d.Entity != "" {
		ids[d.Entity] = labels[d.Entity]
	}
	h := fnv.New64a()
	for _, name := range slices.Sorted(maps.Keys(ids)) {
		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte{0xff})
		_, _ = h.Write([]byte(ids[name]))
		_, _ = h.Write([]byte{0xff})
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

func (r *Raiser) builtin(ctx context.Context, q Store) (int64, error) {
	id, err := q.FindBuiltinIntegration(ctx, r.orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNoBuiltin
	}
	if err != nil {
		return 0, fmt.Errorf("find the built-in integration: %w", err)
	}
	return id, nil
}

// insert writes a synthetic Stored Snapshot of the Integration integrationID, received at now, and the notification
// that wakes processing at the commit.
func (r *Raiser) insert(ctx context.Context, q Store, integrationID int64, now time.Time, w webhook) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(w); err != nil {
		return fmt.Errorf("encode the internal snapshot: %w", err)
	}
	body := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	at := now.UTC().Truncate(time.Microsecond)
	day := pgtype.Date{Time: time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
	sum := sha256.Sum256(body)
	if err := q.InsertInternalBody(ctx, dbgen.InsertInternalBodyParams{OrgID: r.orgID, BodySha256: sum[:],
		BodyDay: day, Body: body}); err != nil {
		return fmt.Errorf("store the internal snapshot body: %w", err)
	}
	if err := q.InsertInternalSnapshot(ctx, dbgen.InsertInternalSnapshotParams{OrgID: r.orgID,
		PublicID: publicid.New(publicid.StoredSnapshot), IntegrationID: integrationID, ReceivedAt: at, BodyDay: day,
		BodySha256: sum[:], SizeBytes: int64(len(body))}); err != nil {
		return fmt.Errorf("store the internal snapshot: %w", err)
	}
	payload, err := json.Marshal(Notification{OrgID: r.orgID, IntegrationID: integrationID})
	if err != nil {
		return fmt.Errorf("encode the snapshot notification: %w", err)
	}
	if err := q.NotifyInternalSnapshot(ctx, dbgen.NotifyInternalSnapshotParams{Channel: SnapshotChannel,
		Payload: string(payload)}); err != nil {
		return fmt.Errorf("notify the processing workers: %w", err)
	}
	return nil
}

// The statuses of a synthetic Snapshot and of its Alert, as in an Alertmanager webhook.
const (
	StatusFiring   = "firing"
	StatusResolved = "resolved"
)

// webhook is a synthetic Stored Snapshot: an Alertmanager webhook (version 4) of one Internal alert.
type webhook struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int64             `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []webhookAlert    `json:"alerts"`
}

type webhookAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// zeroTime is how Alertmanager writes an endsAt that is not set.
const zeroTime = "0001-01-01T00:00:00Z"

func webhookOf(d *Definition, status string, labels, annotations map[string]string, startsAt time.Time) webhook {
	a := webhookAlert{Status: status, Labels: labels, Annotations: annotations,
		StartsAt: startsAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), EndsAt: zeroTime,
		Fingerprint: Fingerprint(d, labels)}
	return webhook{Version: "4", GroupKey: GroupKey(d.Name), Status: status, Receiver: receiver,
		GroupLabels: map[string]string{"alertname": d.Name}, CommonLabels: labels, CommonAnnotations: annotations,
		Alerts: []webhookAlert{a}}
}
