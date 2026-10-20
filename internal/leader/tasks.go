// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package leader

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// MaintenanceInterval is how often the hourly Leader tasks run: partition maintenance, replica pruning, short-lived
// pruning, the retention of the Alerts view and the Alert Group retention, and the re-render after a key rotation,
// whose interval is the fallback of a lost notification.
const MaintenanceInterval = time.Hour

// BacklogInterval is how often the Leader counts the pending Stored Snapshots for muster_ingest_backlog.
const BacklogInterval = 15 * time.Second

// AlertGroupGaugeInterval is how often the Leader counts the open Alert Groups for muster_alert_groups.
const AlertGroupGaugeInterval = 15 * time.Second

// DeliveryQueueInterval is how often the Leader counts the pending deliveries for muster_delivery_queue, reads the
// health of the Destinations for muster_destination_broken and the Storms of the Routes for muster_storm_active.
const DeliveryQueueInterval = 15 * time.Second

// HeartbeatCheckInterval is how often the Leader runs the Heartbeat check, and StaleScanInterval the Stale scan.
const (
	HeartbeatCheckInterval = 10 * time.Second
	StaleScanInterval      = 30 * time.Second
)

// TelegramPollingInterval is how soon the Leader runs the Telegram polling again after it stopped with an error, such
// as a failed read of the Connections; while it runs, it polls without pause.
const TelegramPollingInterval = 10 * time.Second

// BridgePollingInterval is how soon the Leader runs the polling of Bridge events again after it stopped with an error,
// such as a failed read of the Connections; while it runs, it polls without pause.
const BridgePollingInterval = 10 * time.Second

// SlackSocketModeInterval is how soon the Leader runs the Slack sockets again after they stopped with an error, such as
// a failed read of the Connections; while they run, they hold their connections.
const SlackSocketModeInterval = 10 * time.Second

// OIDCSecretExpiryInterval is how often the Leader checks the expiry date of the OIDC client secret (C-19.FR-5); a
// change of the OIDC settings is seen at the next run.
const OIDCSecretExpiryInterval = 5 * time.Minute

// OutgoingHeartbeatPoll is how often the Leader looks whether the outgoing heartbeat is due; the sender measures
// outgoing_heartbeat.interval on the business clock, so the poll only bounds how late a send is.
const OutgoingHeartbeatPoll = 5 * time.Second

// RequestLogPruningInterval is how often the Leader prunes the Request log to its bounds (C-23.FR-4).
const RequestLogPruningInterval = time.Minute

// PruneBatch is the most rows one delete of short-lived pruning removes, so that a large backlog is deleted in short
// statements that hold their row locks briefly.
const PruneBatch = 1000

// Task is one Leader task: it runs when the lock is taken and then every Every, and at once when Wake delivers,
// until the lock is lost. Every Leader task is safe to run twice, because a frozen old Leader can overlap with its
// successor for a moment (ADR-0007).
type Task struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
	// Wake, when set, runs the task out of its interval.
	Wake <-chan struct{}
}

// Wakes runs Leader tasks out of their interval: in development mode, the development clock moved, and the
// Heartbeat check and the Stale scan act at the new time at once instead of at their next tick.
type Wakes struct {
	mu    sync.Mutex
	chans []chan struct{}
}

// Wake runs every task that listens now, or right after its current run; wakes that come meanwhile are merged.
func (w *Wakes) Wake() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.chans {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// channel is a new channel that Wake delivers to; nil, which never delivers, for nil Wakes.
func (w *Wakes) channel() <-chan struct{} {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	c := make(chan struct{}, 1)
	w.chans = append(w.chans, c)
	return c
}

// Work is what the Leader tasks of this release act on.
type Work struct {
	// Alive keeps the alive mark, records downtime and opens the recovery window.
	Alive *Alive
	// MaintainPartitions creates and drops partitions; it logs its own failures.
	MaintainPartitions func(ctx context.Context) error
	// PruneReplicas removes the records of replicas gone for replica.prune_after.
	PruneReplicas func(ctx context.Context) error

	// Short-lived pruning deletes the rows of short-lived tables that can no longer be used (schema.md, section 6),
	// per Organization, on the business clock, and logs and counts them.
	Organizations func(ctx context.Context) ([]int64, error)
	Business      clock.Clock
	Log           *logging.Logger
	// PruneAuth are the short-lived tables of internal/auth: sessions and sign-in throttles. A story that adds a
	// short-lived table adds a field for its domain here, lists it in shortLived and adds the table name to
	// metrics.ShortLivedTables.
	PruneAuth []PruneTable
	// PruneUsers are the short-lived tables of internal/users: password setup links.
	PruneUsers []PruneTable
	// PruneOIDC are the short-lived tables of internal/oidc: the OIDC redirects in flight.
	PruneOIDC []PruneTable
	// PruneDelivery are the short-lived tables of internal/delivery: the copy buffer of Telegram comment Threads.
	PruneDelivery []PruneTable
	// PruneAccountLinks are the short-lived tables of internal/accountlinks: the Account link requests.
	PruneAccountLinks []PruneTable
	// PruneConnections are the short-lived tables of internal/connections: the ids of the events of Bridge
	// Connections handled within bridge.event_dedup_window.
	PruneConnections []PruneTable

	// IngestBacklog sets muster_ingest_backlog from the pending Stored Snapshots of the Organizations.
	IngestBacklog func(ctx context.Context, orgs []int64) error
	// AlertRetention deletes, in batches, the Alerts of the Organization orgID resolved longer than
	// retention.alert_details before now (C-06.FR-19) and returns how many it deleted.
	AlertRetention func(ctx context.Context, orgID int64, now time.Time) (int64, error)

	// HeartbeatCheck makes the overdue live Heartbeats of the Organizations lost and exports muster_heartbeat_lost
	// (C-07.FR-4, C-07.FR-8).
	HeartbeatCheck func(ctx context.Context, orgs []int64) error
	// StaleScan runs the Stale scan of the Organization orgID (C-06.FR-9, C-07.FR-5).
	StaleScan func(ctx context.Context, orgID int64) error
	// AlertGroupGauges sets muster_alert_groups from the open Alert Groups of the Organization orgID (C-09).
	AlertGroupGauges func(ctx context.Context, orgID int64) error
	// AlertGroupRetention deletes, in batches, the details and the summary rows of the Alert Groups of the
	// Organization orgID past their retention periods at now (C-09.FR-16) and returns how many of each it deleted.
	AlertGroupRetention func(ctx context.Context, orgID int64, now time.Time) (details, summaries int64, err error)
	// DeliveryQueue sets muster_delivery_queue from the pending deliveries and due Thread replies of the Organization
	// orgID, muster_destination_broken from the health of its Destinations and muster_storm_active from the Storms of
	// its Routes (C-11.FR-6, FR-9, FR-17).
	DeliveryQueue func(ctx context.Context, orgID int64) error
	// ThreadReplyRetention deletes, in batches, the Thread replies of the Organization orgID that are sent, dropped or
	// not delivered and older than retention.alert_details at now, and returns how many it deleted.
	ThreadReplyRetention func(ctx context.Context, orgID int64, now time.Time) (int64, error)
	// WebhookEventRetention deletes, in batches, the outgoing webhook events of the Organization orgID that are
	// delivered or not delivered and older than retention.alert_details at now, and returns how many it deleted.
	WebhookEventRetention func(ctx context.Context, orgID int64, now time.Time) (int64, error)
	// OIDCSecretExpiry raises and resolves MusterOIDCSecretExpiring for the Organization orgID (C-19.FR-5).
	OIDCSecretExpiry func(ctx context.Context, orgID int64) error
	// OutgoingHeartbeat sends the outgoing heartbeat of the Organization orgID when it is due (C-19.FR-7).
	OutgoingHeartbeat func(ctx context.Context, orgID int64) error
	// TelegramPolling runs the long polling of the Telegram Connections of the Organization orgID until ctx ends
	// (C-14.FR-1): it stops every poller before it returns, and a failed read of the Connections returns early.
	TelegramPolling func(ctx context.Context, orgID int64) error
	// RerenderAfterRotation recomputes, in batches, the Desired state of the deliveries of the open Alert Groups of
	// the Organization orgID whose buttons a key other than the active one signed, after a key activation (C-20.FR-7,
	// ADR-0011); it first switches this replica to the active key.
	RerenderAfterRotation func(ctx context.Context, orgID int64) error
	// KeyringChanged wakes the re-render after a rotation when a key was activated, on any replica's notification.
	KeyringChanged *Wakes
	// BridgePolling runs the polling of the events of the Bridge Connections in poll mode of the Organization orgID
	// until ctx ends (C-22.FR-11): it stops every poll before it returns, and a failed read of the Connections returns
	// early.
	BridgePolling func(ctx context.Context, orgID int64) error
	// RequestLogPruning deletes, in batches, the records of the Request log of the Organization orgID older than
	// request_log.max_age at now or past the newest request_log.max_records of their owner, and returns how many it
	// deleted (C-23.FR-4).
	RequestLogPruning func(ctx context.Context, orgID int64, now time.Time) (int64, error)
	// SlackSocketMode holds the Socket Mode connections of the Slack Connections in socket mode of the Organization
	// orgID until ctx ends (C-24.FR-9): it closes every one of them before it returns, and a failed read of the
	// Connections returns early.
	SlackSocketMode func(ctx context.Context, orgID int64) error
	// ClockMoved, in development mode, wakes the alive mark, the Heartbeat check, the Stale scan, the Alert Group
	// retention, the retention of Thread replies and of outgoing webhook events, the short-lived pruning, the OIDC client
	// secret expiry check, the outgoing heartbeat and the pruning of the Request log when the development clock moved, so
	// that the alive mark keeps up with the business clock and the Leader is not taken for absent; nil otherwise.
	ClockMoved *Wakes
}

// PruneTable is one short-lived table of short-lived pruning.
type PruneTable struct {
	// Name is the table, as the log line and the table label of the metric name it.
	Name string
	// Delete deletes at most limit rows of the Organization orgID that can no longer be used at now and returns how
	// many it deleted. It skips rows that are locked, so a busy row waits for the next run.
	Delete func(ctx context.Context, orgID int64, now time.Time, limit int32) (int64, error)
}

func (w Work) shortLived() []PruneTable {
	return slices.Concat(w.PruneAuth, w.PruneUsers, w.PruneOIDC, w.PruneDelivery, w.PruneAccountLinks,
		w.PruneConnections)
}

// pruneShortLived runs every short-lived table in every Organization at the same now, deleting in batches until a
// batch comes back short. A failed table does not stop the others. Running it twice deletes nothing more, because
// each delete only takes rows that already qualify.
func (w Work) pruneShortLived(ctx context.Context) error {
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations to prune: %w", err)
	}
	now := w.Business.Now()
	var errs []error
	for _, t := range w.shortLived() {
		var rows int64
		for _, org := range orgs {
			n, err := pruneTable(ctx, t, org, now)
			rows += n
			if err != nil {
				errs = append(errs, fmt.Errorf("prune %s: %w", t.Name, err))
				break
			}
		}
		if rows > 0 {
			metrics.ShortLivedRowsPruned.With(t.Name).AddInt64(rows)
			w.Log.Log(ctx, logging.ShortLivedPruned, logging.F("table", t.Name), logging.F("rows", rows))
		}
	}
	return errors.Join(errs...)
}

func pruneTable(ctx context.Context, t PruneTable, orgID int64, now time.Time) (int64, error) {
	var rows int64
	for {
		n, err := t.Delete(ctx, orgID, now, PruneBatch)
		rows += n
		if err != nil || n < PruneBatch {
			return rows, err
		}
	}
}

// Tasks returns the closed list of Leader tasks (ADR-0007): partition maintenance and retention, the alive mark, the
// pruning of replica records, the pruning of short-lived state, the ingestion backlog, the retention of the Alerts
// view, the Heartbeat check, the Stale scan, the count of open Alert Groups, the Alert Group retention, the count of
// pending deliveries, the retention of Thread replies and of outgoing webhook events, the long polling of Telegram
// Connections, the OIDC client secret expiry check, the outgoing heartbeat (C-02.FR-10), the re-render of the open Root
// messages after a key rotation (C-20.FR-7), which also runs when a key was activated, the polling of the events of
// Bridge Connections in poll mode (C-22.FR-11), the pruning of the Request log (C-23.FR-4) and the Socket Mode
// connections of Slack Connections (C-24.FR-9). Later capabilities add theirs at the end of the list. The Keeper calls
// the result at every leadership, so each one starts with a takeover; the Heartbeat check waits for it, so that it
// measures the timeouts from the end of a downtime the takeover records (C-07.FR-4).
func Tasks(w Work) func() []Task {
	heartbeatWake, staleWake, retentionWake := w.ClockMoved.channel(), w.ClockMoved.channel(), w.ClockMoved.channel()
	replyRetentionWake, eventRetentionWake := w.ClockMoved.channel(), w.ClockMoved.channel()
	shortLivedWake := w.ClockMoved.channel()
	oidcExpiryWake, outgoingHeartbeatWake := w.ClockMoved.channel(), w.ClockMoved.channel()
	aliveWake, requestLogWake := w.ClockMoved.channel(), w.ClockMoved.channel()
	keyringWake := w.KeyringChanged.channel()
	return func() []Task {
		var takenOver atomic.Bool
		return []Task{
			{Name: "partition_maintenance", Every: MaintenanceInterval, Run: func(ctx context.Context) error {
				_ = w.MaintainPartitions(ctx) // logged as partition_maintenance_failed and retried at the next run
				return nil
			}},
			{Name: "alive_mark", Every: AliveMarkInterval, Wake: aliveWake, Run: func(ctx context.Context) error {
				if takenOver.Load() {
					return w.Alive.Mark(ctx)
				}
				if err := w.Alive.TakeOver(ctx); err != nil {
					return err
				}
				takenOver.Store(true)
				return nil
			}},
			{Name: "replica_pruning", Every: MaintenanceInterval, Run: w.PruneReplicas},
			{Name: "short_lived_pruning", Every: MaintenanceInterval, Wake: shortLivedWake, Run: w.pruneShortLived},
			{Name: "ingest_backlog", Every: BacklogInterval, Run: w.ingestBacklog},
			{Name: "alert_retention", Every: MaintenanceInterval, Run: w.alertRetention},
			{Name: "heartbeat_check", Every: HeartbeatCheckInterval, Wake: heartbeatWake,
				Run: func(ctx context.Context) error {
					if !takenOver.Load() {
						return nil // the next run, once the takeover recorded any downtime
					}
					return w.heartbeatCheck(ctx)
				}},
			{Name: "stale_scan", Every: StaleScanInterval, Wake: staleWake, Run: w.staleScan},
			{Name: "alert_group_gauges", Every: AlertGroupGaugeInterval, Run: w.alertGroupGauges},
			{Name: "alert_group_retention", Every: MaintenanceInterval, Wake: retentionWake,
				Run: w.alertGroupRetention},
			{Name: "delivery_queue", Every: DeliveryQueueInterval, Run: w.deliveryQueue},
			{Name: "thread_reply_retention", Every: MaintenanceInterval, Wake: replyRetentionWake,
				Run: w.threadReplyRetention},
			{Name: "webhook_event_retention", Every: MaintenanceInterval, Wake: eventRetentionWake,
				Run: w.webhookEventRetention},
			{Name: "telegram_polling", Every: TelegramPollingInterval, Run: w.telegramPolling},
			{Name: "oidc_secret_expiry", Every: OIDCSecretExpiryInterval, Wake: oidcExpiryWake,
				Run: func(ctx context.Context) error {
					return w.perOrganization(ctx, "the oidc secret expiry check", w.OIDCSecretExpiry)
				}},
			{Name: "outgoing_heartbeat", Every: OutgoingHeartbeatPoll, Wake: outgoingHeartbeatWake,
				Run: func(ctx context.Context) error {
					return w.perOrganization(ctx, "the outgoing heartbeat", w.OutgoingHeartbeat)
				}},
			{Name: "rerender_after_rotation", Every: MaintenanceInterval, Wake: keyringWake,
				Run: func(ctx context.Context) error {
					return w.perOrganization(ctx, "the re-render after a key rotation", w.RerenderAfterRotation)
				}},
			{Name: "bridge_polling", Every: BridgePollingInterval, Run: w.bridgePolling},
			{Name: "request_log_pruning", Every: RequestLogPruningInterval, Wake: requestLogWake,
				Run: w.requestLogPruning},
			{Name: "slack_socket_mode", Every: SlackSocketModeInterval, Run: w.slackSocketMode},
		}
	}
}

// perOrganization runs f for every Organization; a failed Organization does not stop the others, and a nil f does
// nothing.
func (w Work) perOrganization(ctx context.Context, what string, f func(ctx context.Context, orgID int64) error) error {
	if f == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for %s: %w", what, err)
	}
	var errs []error
	for _, org := range orgs {
		errs = append(errs, f(ctx, org))
	}
	return errors.Join(errs...)
}

// alertGroupGauges counts the open Alert Groups of every Organization; counting twice sets the same values.
func (w Work) alertGroupGauges(ctx context.Context) error {
	if w.AlertGroupGauges == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations to count the alert groups of: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		errs = append(errs, w.AlertGroupGauges(ctx, org))
	}
	return errors.Join(errs...)
}

// deliveryQueue counts the pending deliveries of every Organization; counting twice sets the same values.
func (w Work) deliveryQueue(ctx context.Context) error {
	if w.DeliveryQueue == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations to count the delivery queues of: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		errs = append(errs, w.DeliveryQueue(ctx, org))
	}
	return errors.Join(errs...)
}

// threadReplyRetention runs the retention of Thread replies in every Organization at the same now, on the business
// clock; a failed Organization does not stop the others. Running it twice deletes nothing more.
func (w Work) threadReplyRetention(ctx context.Context) error {
	if w.ThreadReplyRetention == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for the thread reply retention: %w", err)
	}
	now := w.Business.Now()
	var errs []error
	for _, org := range orgs {
		_, err := w.ThreadReplyRetention(ctx, org, now)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// webhookEventRetention runs the retention of outgoing webhook events in every Organization at the same now, on the
// business clock; a failed Organization does not stop the others. Running it twice deletes nothing more.
func (w Work) webhookEventRetention(ctx context.Context) error {
	if w.WebhookEventRetention == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for the webhook event retention: %w", err)
	}
	now := w.Business.Now()
	var errs []error
	for _, org := range orgs {
		_, err := w.WebhookEventRetention(ctx, org, now)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// alertGroupRetention runs the Alert Group retention in every Organization at the same now, on the business clock, and
// logs what it deleted; a failed Organization does not stop the others. Running it twice deletes nothing more.
func (w Work) alertGroupRetention(ctx context.Context) error {
	if w.AlertGroupRetention == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for the alert group retention: %w", err)
	}
	now := w.Business.Now()
	var errs []error
	var details, summaries int64
	for _, org := range orgs {
		d, s, err := w.AlertGroupRetention(ctx, org, now)
		details, summaries = details+d, summaries+s
		errs = append(errs, err)
	}
	if details > 0 || summaries > 0 {
		w.Log.Log(ctx, logging.AlertGroupsPurged, logging.F("details", details), logging.F("summaries", summaries))
	}
	return errors.Join(errs...)
}

// heartbeatCheck runs the Heartbeat check over every Organization.
func (w Work) heartbeatCheck(ctx context.Context) error {
	if w.HeartbeatCheck == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for the heartbeat check: %w", err)
	}
	return w.HeartbeatCheck(ctx, orgs)
}

// staleScan runs the Stale scan in every Organization; a failed Organization does not stop the others.
func (w Work) staleScan(ctx context.Context) error {
	if w.StaleScan == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for the stale scan: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		errs = append(errs, w.StaleScan(ctx, org))
	}
	return errors.Join(errs...)
}

// ingestBacklog counts the pending Stored Snapshots of every Organization; counting twice sets the same value.
func (w Work) ingestBacklog(ctx context.Context) error {
	if w.IngestBacklog == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations to count the backlog of: %w", err)
	}
	return w.IngestBacklog(ctx, orgs)
}

// alertRetention runs the retention of the Alerts view in every Organization at the same now, on the business clock;
// a failed Organization does not stop the others. Running it twice deletes nothing more.
func (w Work) alertRetention(ctx context.Context) error {
	if w.AlertRetention == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for the alert retention: %w", err)
	}
	now := w.Business.Now()
	var errs []error
	for _, org := range orgs {
		if _, err := w.AlertRetention(ctx, org, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// telegramPolling polls the Telegram Connections of every Organization at once until ctx ends, which is when the lock
// is lost or the process stops. Running it on a frozen old Leader and its successor at once is safe: Telegram answers
// one poller per bot with 409, and the update offset only rises.
func (w Work) telegramPolling(ctx context.Context) error {
	return w.everyOrganizationAtOnce(ctx, "poll telegram for", w.TelegramPolling)
}

// bridgePolling polls the events of the Bridge Connections of every Organization at once until ctx ends, which is when
// the lock is lost or the process stops. Running it on a frozen old Leader and its successor at once is safe: an event
// polled twice is handled once by its id, and a cursor stored late only makes the bridge send events again.
func (w Work) bridgePolling(ctx context.Context) error {
	return w.everyOrganizationAtOnce(ctx, "poll bridge events for", w.BridgePolling)
}

// slackSocketMode holds the Socket Mode connections of the Slack Connections of every Organization at once until ctx
// ends, which is when the lock is lost or the process stops. Running it on a frozen old Leader and its successor at
// once is safe: two connections of one Connection share its presses, and Slack sends each press to one of them.
func (w Work) slackSocketMode(ctx context.Context) error {
	return w.everyOrganizationAtOnce(ctx, "hold the slack sockets of", w.SlackSocketMode)
}

// requestLogPruning prunes the Request log of every Organization at the same now, on the business clock, and logs and
// counts what it deleted; a failed Organization does not stop the others. It deletes only what is past a bound, so
// running it twice, or on a frozen old Leader and its successor at once, deletes nothing more.
func (w Work) requestLogPruning(ctx context.Context) error {
	if w.RequestLogPruning == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations to prune the request log of: %w", err)
	}
	now := w.Business.Now()
	var errs []error
	var rows int64
	for _, org := range orgs {
		n, err := w.RequestLogPruning(ctx, org, now)
		rows += n
		errs = append(errs, err)
	}
	if rows > 0 {
		metrics.RequestLogPruned.With().AddInt64(rows)
		w.Log.Log(ctx, logging.RequestLogPruned, logging.F("rows", rows))
	}
	return errors.Join(errs...)
}

// everyOrganizationAtOnce runs f for every Organization at once and waits for each to return; a nil f does nothing.
func (w Work) everyOrganizationAtOnce(ctx context.Context, what string,
	f func(ctx context.Context, orgID int64) error,
) error {
	if f == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations to %s: %w", what, err)
	}
	errs := make([]error, len(orgs))
	var wg sync.WaitGroup
	for i, org := range orgs {
		wg.Go(func() { errs[i] = f(ctx, org) })
	}
	wg.Wait()
	return errors.Join(errs...)
}
