// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package logging

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The closed registry of log events (C-02.FR-18, ADR-0014). Every event Muster logs is declared below with newEvent;
// code outside this package cannot create one. Levels are chosen by the reader: ERROR when someone must look within
// the hour, WARN when the line is needed for an investigation, INFO when an investigation is impossible without it.
// Each capability adds its own events; adding one is a reviewed change, and make generate rebuilds the reference page.

// ProcessStarted is the first line of every run of the server.
var ProcessStarted = newEvent("process_started", LevelInfo, "C-02",
	"The server process started, with the version and commit of the binary.",
	"version", "commit")

// LibraryMessage is a line a third-party library wrote to the standard library logger, once CaptureStdlib redirected
// it into the domain logger.
var LibraryMessage = newEvent("library_message", LevelWarn, "C-02",
	"A third-party library wrote a line of its own, such as a failed read of the process metrics.",
	"message")

// ProcessStopped is the last line of a run of the server that ended without an error.
var ProcessStopped = newEvent("process_stopped", LevelInfo, "C-02",
	"The server process stopped after a graceful shutdown.")

// ShutdownRequested is logged when the server receives SIGTERM or an interrupt and starts its graceful shutdown.
var ShutdownRequested = newEvent("shutdown_requested", LevelWarn, "C-02",
	"The server received SIGTERM or an interrupt: readiness answers 503, work in progress finishes and the listeners "+
		"drain within the grace period, in seconds.",
	"grace_seconds")

// ShutdownGraceExceeded is logged when the listeners did not drain within the grace period and were closed.
var ShutdownGraceExceeded = newEvent("shutdown_grace_exceeded", LevelWarn, "C-02",
	"Requests were still running when the shutdown grace period ended; their connections were closed.",
	"grace_seconds")

// StartupFailed is logged when the server or a subcommand stops during startup, with the reason.
var StartupFailed = newEvent("startup_failed", LevelError, "C-02",
	"Startup stopped: the database cannot be reached or fails a check, a migration failed, or a listener cannot "+
		"listen. The error says what to fix.",
	"error")

// ListenersStarted is logged once the server serves, with the address of each listener.
var ListenersStarted = newEvent("listeners_started", LevelInfo, "C-02",
	"The listeners serve and the process is ready; app and ingest are the same address when one port serves both.",
	"app", "ingest", "internal")

// ListenerFailed is logged when a listener stops serving while the process runs; the process then stops.
var ListenerFailed = newEvent("listener_failed", LevelError, "C-02",
	"A listener stopped serving with an error; the process stops so that it is restarted.",
	"listener", "error")

// DatabaseSettingsConflict is logged when a connection is configured both as a URL and as fields.
var DatabaseSettingsConflict = newEvent("database_settings_conflict", LevelWarn, "C-02",
	"A database connection is set both as a URL and as fields: the URL is used and the fields are ignored.",
	"used", "ignored")

// DatabaseConnectionSecurity reports whether a database connection is encrypted, at WARN when it is not.
var DatabaseConnectionSecurity = newEventAt([]Level{LevelInfo, LevelWarn}, "database_connection_security", "C-02",
	"Whether a database connection (main or session) is encrypted, with the sslmode in effect; WARN when it is not "+
		"encrypted, which prefer allows without an error.",
	"connection", "sslmode", "encrypted")

// MigrationsApplied is logged when muster migrate or the start-up migration applied migrations.
var MigrationsApplied = newEvent("migrations_applied", LevelInfo, "C-02",
	"Migrations were applied under the migration lock, from one schema version to another.",
	"from", "to")

// MigrationsCurrent is logged when the schema already has the newest version the binary knows.
var MigrationsCurrent = newEvent("migrations_current", LevelInfo, "C-02",
	"No migration was applied: the schema already has the newest version this binary knows.",
	"version")

// SchemaTooNew is logged when the database schema is newer than the binary knows; the process refuses to run.
var SchemaTooNew = newEvent("schema_too_new", LevelError, "C-02",
	"The database schema is newer than this binary knows, after an upgrade was rolled back: run a release that "+
		"knows the schema version.",
	"database_version", "known_version")

// SchemaTooOld is logged when the server starts on a schema older than the binary needs, without migrating.
var SchemaTooOld = newEvent("schema_too_old", LevelError, "C-02",
	"The database schema is older than this binary needs: run muster migrate or set MUSTER_MIGRATE_ON_START.",
	"database_version", "known_version")

// SchemaDirty is logged when a migration failed halfway and left the schema dirty; the process refuses to run.
var SchemaDirty = newEvent("schema_dirty", LevelError, "C-02",
	"A migration failed halfway and the schema is marked dirty at its version: repair the schema from a backup or "+
		"by hand, then clear the dirty flag in schema_migrations.",
	"database_version")

// KeyringLoaded is logged once the key canary decrypts, with the ids of the keys held and of the active key.
var KeyringLoaded = newEvent("keyring_loaded", LevelInfo, "C-02",
	"The master keys of MUSTER_SECRET_KEYS were loaded and decrypt the key canary; the ids are derived from the keys "+
		"and never reveal them.",
	"key_ids", "active_key_id")

// KeyCanaryFailed is logged when the Keyring cannot decrypt the key canary; the replica stops.
var KeyCanaryFailed = newEvent("key_canary_failed", LevelError, "C-02",
	"The master keys cannot decrypt the key canary: the active key is missing from MUSTER_SECRET_KEYS, or the keys "+
		"belong to another database. The replica stops; set the keys this database was written with.",
	"active_key_id", "key_ids", "error")

// ActiveKeyNotHeld is logged when a running replica sees an active key it does not hold; the replica stops.
var ActiveKeyNotHeld = newEvent("active_key_not_held", LevelError, "C-02",
	"The active master key is not in this replica's MUSTER_SECRET_KEYS, which happens when a key was activated while "+
		"this replica's record had expired. The replica stops; add the key to its environment.",
	"active_key_id", "key_ids", "error")

// ReplicaRecordFailed is logged when a replica cannot refresh or, at shutdown, delete its key record.
var ReplicaRecordFailed = newEvent("replica_record_failed", LevelWarn, "C-02",
	"A replica could not refresh its record of the keys it holds, usually because the database is unavailable; it "+
		"retries at the next refresh and stops counting as live once the record is older than replica.live_expiry. "+
		"At shutdown, the record could not be deleted; it stops counting as live after replica.live_expiry.",
	"replica", "error")

// OrganizationCreated is logged when the first start creates the Organization with its defaults.
var OrganizationCreated = newEvent("organization_created", LevelInfo, "C-02",
	"The first start created the Organization with the defaults of its settings and its outbound address policy.",
	"organization")

// PartitionsMaintained is logged when partition maintenance created or dropped partitions.
var PartitionsMaintained = newEvent("partitions_maintained", LevelInfo, "C-02",
	"Partition maintenance created the partitions Muster will need (daily for Stored Snapshots and their bodies, "+
		"monthly for Timeline entries, delivery events and the Audit log) or dropped partitions whose whole range is "+
		"older than their retention period; the fields list the partitions by name.",
	"created", "dropped")

// PartitionMaintenanceFailed is logged when partition maintenance failed; it is retried at the next run.
var PartitionMaintenanceFailed = newEvent("partition_maintenance_failed", LevelWarn, "C-02",
	"Partition maintenance failed, for example because a lock was not granted within 2 seconds; the Leader retries "+
		"at the next hourly run. A partition that is still missing when its day or month begins makes writes to its "+
		"table fail.",
	"error")

// LeadershipAcquired is logged when this replica takes the Leader lock and starts the Leader tasks.
var LeadershipAcquired = newEvent("leadership_acquired", LevelInfo, "C-02",
	"This replica took the Leader lock and runs the Leader tasks: partition maintenance, the alive mark, the "+
		"pruning of replica records and the pruning of short-lived state.",
	"replica")

// LeadershipLost is logged when the Leader stops leading because its lock session failed or went silent.
var LeadershipLost = newEvent("leadership_lost", LevelWarn, "C-02",
	"The Leader could not confirm its lock within leader.fencing_timeout, or its lock session failed: it stopped "+
		"every Leader task, closed the lock connection and competes for the lock again.",
	"replica", "error")

// LeaderTaskFailed is logged when a run of a Leader task failed; the task runs again at its next interval.
var LeaderTaskFailed = newEvent("leader_task_failed", LevelWarn, "C-02",
	"A run of a Leader task failed, usually because the database was unavailable; it runs again at its next "+
		"interval.",
	"task", "error")

// DowntimeRecorded is logged when a new Leader finds that no replica was running for longer than
// leader.absence_notice and records the period.
var DowntimeRecorded = newEvent("downtime_recorded", LevelWarn, "C-02",
	"Muster was unavailable: no Leader wrote the alive mark and no replica refreshed its record for the period "+
		"between started_at and ended_at. The new Leader recorded it and opened the recovery window "+
		"(recovery.banner_duration).",
	"started_at", "ended_at", "duration_seconds")

// ReplicasPruned is logged when the Leader removed the records of replicas gone for longer than replica.prune_after.
var ReplicasPruned = newEvent("replicas_pruned", LevelInfo, "C-02",
	"The Leader removed the records of replicas that had not refreshed them for replica.prune_after.",
	"replicas")

// ShortLivedPruned is logged when the short-lived pruning Leader task deleted expired rows from a table.
var ShortLivedPruned = newEvent("short_lived_pruned", LevelInfo, "C-02",
	"The hourly short-lived pruning of the Leader deleted, in batches, rows of a short-lived table that can no "+
		"longer be used, such as sessions that ended or expired auth.session_prune_after ago, sign-in throttles "+
		"without a failure for auth.signin_throttle_prune_after and password setup links that expired "+
		"auth.password_setup_prune_after ago; rows is how many it deleted from the table.",
	"table", "rows")

// ClockSkew is logged when this replica's clock differs from the database clock by more than
// process.clock_skew_warning.
var ClockSkew = newEvent("clock_skew", LevelWarn, "C-02",
	"This replica's clock differs from the database clock by more than process.clock_skew_warning; skew_seconds is "+
		"positive when the replica's clock is ahead. Synchronise the clocks with NTP.",
	"skew_seconds", "warning_seconds")

// OutboundBlocked is logged when the outbound address policy refused a request.
var OutboundBlocked = newEvent("outbound_blocked", LevelWarn, "C-02",
	"The outbound address policy refused a request, which fails without retry; rule names the rule and the address "+
		"or proxy it refused. Correct the configured address, or change the policy and its allowed networks.",
	"client", "rule", "scheme", "host")

// OutboundUnverifiedAddress is logged when a request through a proxy was sent with only its host name checked.
var OutboundUnverifiedAddress = newEvent("outbound_unverified_address", LevelInfo, "C-02",
	"A request went through the client's proxy to a host name that Muster cannot resolve itself, so only the name "+
		"was checked against the allowed and denied lists, not its addresses.",
	"client", "scheme", "host")

// AuditEntry is the copy of every Audit log entry on stdout.
var AuditEntry = newEvent("audit_entry", LevelInfo, "C-03",
	"An Audit log entry, as written to audit_log: the actor (its kind, the public_id of the user or Service account, "+
		"or the --actor name of a CLI command), the token used, the Transport, the action <resource>.<verb>, the "+
		"resource, the before/after diff of what was configured (a Secret shows only that it changed) and details.",
	"entry", "at", "actor_kind", "actor", "actor_name", "token_name", "transport", "action", "resource_type",
	"resource", "resource_name", "source_address", "diff", "details")

// BootstrapAdminIgnored is logged at startup when the bootstrap variables are set but the Organization has an Admin.
var BootstrapAdminIgnored = newEvent("bootstrap_admin_ignored", LevelWarn, "C-03",
	"The bootstrap Admin variables are set, but the Organization already has an Admin, so they were ignored and no "+
		"user was created; remove them from the environment.",
	"variables")

// BootstrapAdminMissing is logged at startup when the Organization has no Admin and no bootstrap variables.
var BootstrapAdminMissing = newEvent("bootstrap_admin_missing", LevelWarn, "C-03",
	"The Organization has no Admin and the bootstrap variables are not set, so nobody can sign in to administer "+
		"Muster: set MUSTER_BOOTSTRAP_ADMIN_EMAIL and MUSTER_BOOTSTRAP_ADMIN_PASSWORD (or _FILE) and restart, or run "+
		"muster admin reset-password.")

// APIRequestFailed is logged when an API request fails with an unexpected error and answers 500.
var APIRequestFailed = newEvent("api_request_failed", LevelError, "C-03",
	"An API request failed with an unexpected error, usually because the database was unavailable, and answered "+
		"500 internal; operation is the operationId of the specification.",
	"operation", "error")

// LiveUpdatesListenFailed is logged when a replica loses, or cannot open, the LISTEN connection of the live-update
// hints.
var LiveUpdatesListenFailed = newEvent("live_updates_listen_failed", LevelWarn, "C-03",
	"A replica lost the session connection that listens for live-update hints, or could not open it, usually because "+
		"the database was unavailable; it connects again with a growing delay, up to 30 seconds. Until then the "+
		"browsers connected to it miss the hints of changes and see them at their next read.",
	"error")

// LiveUpdatesListenRestored is logged when a replica listens for live-update hints again after a loss.
var LiveUpdatesListenRestored = newEvent("live_updates_listen_restored", LevelInfo, "C-03",
	"A replica listens for live-update hints again after live_updates_listen_failed; it sent every connected browser "+
		"the hints of everything, so that they read it again.")

// SystemNoticesCheckFailed is logged when a replica cannot read the state of the Organization-wide notices.
var SystemNoticesCheckFailed = newEvent("system_notices_check_failed", LevelWarn, "C-03",
	"A replica could not read the state of the Organization-wide notices, usually because the database was "+
		"unavailable, so the live-updates streams get no hint of a notice that started or ended; logged once until "+
		"a check succeeds again.",
	"error")

// OIDCSignInFailed is logged when an OIDC sign-in or link ends with idp_error: the identity provider or the way to it
// failed, or its answer did not verify.
var OIDCSignInFailed = newEvent("oidc_sign_in_failed", LevelWarn, "C-03",
	"An OIDC sign-in ended at the sign-in page, or the linking of an OIDC identity at the profile page, with "+
		"idp_error: the identity provider reported an error, could not be reached through the back channel or its "+
		"proxy, or answered with an ID token that did not verify. The reason names the step, prefixed with link for a "+
		"link; the error is masked and carries no token or secret.",
	"reason", "error")

// OIDCCheckFailed is logged when a background re-check of an OIDC user could not reach a decision.
var OIDCCheckFailed = newEvent("oidc_check_failed", LevelWarn, "C-03",
	"A background re-check of an OIDC user at the identity provider found it unavailable — unreachable, a timeout, "+
		"408, 429 or 5xx, or the 10-second budget of the refresh spent — or could not run. Nothing changes for the "+
		"user: sessions and Personal access tokens keep working, and the next attempt comes one "+
		"auth.oidc_recheck_interval later, so an incident at the identity provider never signs everybody out. user is "+
		"the public_id of the user, empty when a round of the worker failed; reason names the step; the error is "+
		"masked and carries no token or secret.",
	"user", "reason", "error")

// SnapshotAccepted is logged when the ingestion endpoint stored a webhook body as a Stored Snapshot and answered 202.
var SnapshotAccepted = newEvent("snapshot_accepted", LevelInfo, "C-05",
	"An ingestion request was stored as a pending Stored Snapshot and answered 202: integration and stored_snapshot "+
		"are public_ids, size_bytes the size of the body, route_pattern the route it came by — never the path, which "+
		"may carry the token.",
	"integration", "stored_snapshot", "size_bytes", "route_pattern")

// IngestRejected is logged when the ingestion endpoint refused a request: 401 or 413.
var IngestRejected = newEvent("ingest_rejected", LevelInfo, "C-05",
	"An ingestion or Heartbeat request was refused: outcome unauthorized (401, a missing, wrong or revoked token, an "+
		"API token, or one of a deleted Integration) or too_large (413, a body above ingest.body_limit). integration "+
		"is the public_id of the token's Integration, or unknown; route_pattern is the route it came by — never the "+
		"path, which may carry the token — and client_address the sender. Refused ingestion requests are also "+
		"counted in muster_ingest_requests_total, which the chart rule MusterIngestRejected reports.",
	"integration", "outcome", "route_pattern", "client_address")

// IngestFailed is logged when the ingestion endpoint could not read the token or store the body and answered 500.
var IngestFailed = newEvent("ingest_failed", LevelError, "C-05",
	"An ingestion or Heartbeat request answered 500 because the database could not look up the token, store the "+
		"Stored Snapshot or record the Heartbeat signal, usually because it was unavailable; Alertmanager retries "+
		"an ingestion request, and the next signal replaces a lost one. integration is the public_id of the token's "+
		"Integration, or unknown when the lookup failed; route_pattern is the route it came by.",
	"integration", "route_pattern", "error")

// SnapshotProcessed is logged once for every Stored Snapshot that processing applied, never once per Alert.
var SnapshotProcessed = newEvent("snapshot_processed", LevelInfo, "C-06",
	"A Stored Snapshot was processed: integration and stored_snapshot are public_ids, group_key the Alertmanager "+
		"group it came for, alerts how many Alerts it listed, fired, resolved, gone and continued how many new "+
		"firings, resolutions from Alertmanager or by the deletion of the Integration, Alerts resolved as Gone and "+
		"Continuations it made, dropped the "+
		"resolves of fingerprints that fire nowhere, truncated its truncatedAlerts, routes the public_ids of the "+
		"Routes that took its newly firing Alerts, alert_groups the #N of the Alert Groups it created or changed, "+
		"and duration_ms how long processing took.",
	"integration", "stored_snapshot", "group_key", "alerts", "fired", "resolved", "gone", "continued", "dropped",
	"truncated", "routes", "alert_groups", "duration_ms")

// SnapshotFailed is logged when a Stored Snapshot could not be processed and was marked failed.
var SnapshotFailed = newEvent("snapshot_failed", LevelWarn, "C-06",
	"A Stored Snapshot could not be processed and was marked failed with the error: its body is not JSON, lacks the "+
		"fields of an Alertmanager webhook, or processing failed. It is not retried by itself, the Snapshots behind "+
		"it are processed, and muster ingest replay processes it again after a fix. integration and "+
		"stored_snapshot are public_ids.",
	"integration", "stored_snapshot", "error")

// SnapshotProcessingInterrupted is logged when the processing worker could not finish a round or an Integration
// because the database failed; the Stored Snapshot stays pending and is processed again.
var SnapshotProcessingInterrupted = newEvent("snapshot_processing_interrupted", LevelWarn, "C-06",
	"The processing worker stopped a round or an Integration because the database failed or the connection was "+
		"lost; the Stored Snapshots stay pending and are processed at the next attempt, after a growing pause. "+
		"integration is the public_id of the Integration, empty when the round failed.",
	"integration", "error")

// InternalAlertRaised is logged when processing fires an Internal alert of the built-in Integration.
var InternalAlertRaised = newEvent("internal_alert_raised", LevelInfo, "C-06",
	"An Internal alert fired as an Alert of the built-in Muster Integration: alertname names it, fingerprint is the "+
		"Alert's, and entity the public_id of the Integration, Route or Destination it is about, empty when it is "+
		"about none.",
	"alertname", "fingerprint", "entity")

// InternalAlertResolved is logged when processing resolves an Internal alert of the built-in Integration.
var InternalAlertResolved = newEvent("internal_alert_resolved", LevelInfo, "C-06",
	"An Internal alert resolved because its condition cleared or its entity was deleted: alertname names it, "+
		"fingerprint is the Alert's, and entity the public_id of the Integration, Route or Destination it is about, "+
		"empty when it is about none.",
	"alertname", "fingerprint", "entity")

// AlertsStale is logged when the Stale scan resolved Alerts of an Integration as Stale; a scan that resolves nothing
// logs nothing.
var AlertsStale = newEvent("alerts_stale", LevelInfo, "C-06",
	"The Stale scan resolved Alerts of an Integration with a live Heartbeat as Stale, because Alertmanager had not "+
		"listed them for stale_after of live time in any Alertmanager group: integration is the public_id of the "+
		"Integration and count how many Alerts resolved.",
	"integration", "count")

// HeartbeatLive is logged when the Heartbeat of an Integration becomes live: its first signal, or the first after it
// was lost.
var HeartbeatLive = newEvent("heartbeat_live", LevelInfo, "C-07",
	"The Heartbeat of an Integration became live: integration is its public_id, and first is true for the first "+
		"signal after the Heartbeat was turned on and false for a signal that ended a loss, which resolves "+
		"MusterHeartbeatLost.",
	"integration", "first")

// HeartbeatLost is logged when the Leader's Heartbeat check finds that no signal arrived within the timeout.
var HeartbeatLost = newEvent("heartbeat_lost", LevelWarn, "C-07",
	"No Heartbeat signal arrived for an Integration within its Heartbeat timeout, so it is Heartbeat lost and "+
		"MusterHeartbeatLost is raised: integration is its public_id and last_signal_at the time of its last signal.",
	"integration", "last_signal_at")

// GroupKeyPreviewed is logged when a Group key preview was computed.
var GroupKeyPreviewed = newEvent("group_key_previewed", LevelInfo, "C-08",
	"A Group key preview read the Stored Snapshots of a period: route is the public_id of the saved Route it "+
		"previewed, empty for a Route that is not saved yet, period_seconds the period, snapshots_read how many "+
		"distinct bodies it read, truncated whether routing.group_key_preview_max_alerts stopped it with Stored "+
		"Snapshots of the period, or Alerts the Route takes, left unread, and duration_ms how long it took.",
	"route", "period_seconds", "snapshots_read", "truncated", "duration_ms")

// AlertGroupStatusChanged is logged when an Alert Group changed status, by a system transition or a Command.
var AlertGroupStatusChanged = newEvent("alert_group_status_changed", LevelInfo, "C-09",
	"An Alert Group changed status: group is its #N, route the public_id of its Route, from and to the statuses "+
		"(from is empty for a new Alert Group), reason the reason of an automatic change and transport how the "+
		"change reached Muster.",
	"group", "route", "from", "to", "reason", "transport")

// AlertContinued is logged for each Continuation of an Alert that fires in an Alert Group.
var AlertContinued = newEvent("alert_continued", LevelInfo, "C-09",
	"An Alert firing in an Alert Group got a new startsAt without being resolved in between (a Continuation, never "+
		"a new firing): group is the #N of its Alert Group and fingerprint the Alert's.",
	"group", "fingerprint")

// CommandExecuted is logged when a Command ran on an Alert Group.
var CommandExecuted = newEvent("command_executed", LevelInfo, "C-10",
	"A Command ran on an Alert Group: command is its name, group the public_id of the Alert Group, actor the "+
		"public_id of the User or Service account, transport how it reached Muster and outcome done, or unchanged "+
		"for an idempotent repeat such as Acknowledge by the current Owner.",
	"command", "group", "actor", "transport", "outcome")

// CommandRefused is logged when a Command was refused; a refusal writes nothing else.
var CommandRefused = newEvent("command_refused", LevelInfo, "C-10",
	"A Command was refused and changed nothing: command is its name, group the public_id of the Alert Group (empty "+
		"for a bulk command refused as a whole), actor the public_id of the User or Service account, transport how "+
		"it reached Muster and code forbidden for a missing Permission or the code of the command-refused problem.",
	"command", "group", "actor", "transport", "code")

// TimerFailed is logged when a timer worker could not fire a timer; it fires again once its lease runs out.
var TimerFailed = newEvent("timer_failed", LevelWarn, "C-09",
	"A timer could not fire: kind is the timer kind and error what failed. The timer fires again on any replica "+
		"once its lease runs out.",
	"kind", "error")

// AlertGroupsPurged is logged when the Alert Group retention of the Leader deleted details or summary rows.
var AlertGroupsPurged = newEvent("alert_groups_purged", LevelInfo, "C-09",
	"The Alert Group retention of the Leader deleted, in batches, the Alerts inside Alert Groups that ended "+
		"retention.alert_details ago (details, the rows deleted) and the summary rows of Alert Groups resolved "+
		"retention.alert_group_summaries ago, with their Notes (summaries, the Alert Groups deleted).",
	"details", "summaries")

// DeliveryAttempt is logged for every call of the delivery worker to a Destination (C-11.FR-17).
var DeliveryAttempt = newEvent("delivery_attempt", LevelInfo, "C-11",
	"The delivery worker called a Destination: destination is its public_id, group the #N of the Alert Group, empty "+
		"for a Storm summary, kind what it sent (publication, update, thread_reply, storm_summary for any call of a "+
		"Storm summary, or final_edit for the last edit of a Root message in a Destination it left), outcome "+
		"delivered or the error class of the answer, attempt the number of this attempt, duration_ms how long the "+
		"call took and retry_after_ms the wait a RetryAfter asked for, 0 otherwise.",
	"destination", "group", "kind", "outcome", "attempt", "duration_ms", "retry_after_ms")

// DeliveryWorkFailed is logged when the delivery worker could not attempt a delivery or a Thread reply, or probe a
// Broken Destination.
var DeliveryWorkFailed = newEvent("delivery_work_failed", LevelWarn, "C-11",
	"The delivery worker could not attempt a delivery or a Thread reply, or probe a Broken Destination (work is "+
		"delivery, thread_reply or probe), because of error, such as a lost database connection. Any replica "+
		"attempts it again once its lease runs out, or at the next probe.",
	"work", "error")

// DestinationBroken is logged when a Destination becomes Broken (C-11.FR-9).
var DestinationBroken = newEvent("destination_broken", LevelWarn, "C-11",
	"A Destination became Broken: destination is its public_id, cause fatal after a Fatal error or unavailable after "+
		"the Transient budget ran out, and reason the masked error the messenger answered. Its deliveries wait, "+
		"MusterDestinationBroken fires and a probe runs every delivery.broken_probe_interval.",
	"destination", "cause", "reason")

// DestinationRecovered is logged when a Broken Destination becomes healthy again (C-11.FR-9, FR-19).
var DestinationRecovered = newEvent("destination_recovered", LevelInfo, "C-11",
	"A Broken Destination is healthy again after a successful probe or Destination check: destination is its "+
		"public_id and broken_for_s how long it was Broken, in seconds. Its deliveries resume with the current state.",
	"destination", "broken_for_s")

// DeliveryPossibleDuplicate is logged when a Publication starts again after an earlier start that was never recorded
// (C-11.FR-12).
var DeliveryPossibleDuplicate = newEvent("delivery_possible_duplicate", LevelWarn, "C-11",
	"A Publication started again after an earlier start whose outcome was never recorded, such as after a crash: "+
		"destination is the public_id of the Destination and group the public_id of the Alert Group. The messenger "+
		"may show the Root message twice.",
	"destination", "group")

// DeliveryNotDelivered is logged when a delivery or a Thread reply ends as Not delivered (C-11.FR-10).
var DeliveryNotDelivered = newEvent("delivery_not_delivered", LevelWarn, "C-11",
	"A delivery ended as Not delivered after an answer the adapter could not classify, because no adapter serves "+
		"the Destination type, or because the Connection of its deleted Destination was deleted: destination is the "+
		"public_id of the Destination, group the public_id of the Alert Group, empty for a Storm summary, kind what "+
		"was sent (publication, update, thread_reply, storm_summary or final_edit) and error_class unknown. The next "+
		"change of the Alert Group starts a new delivery.",
	"destination", "group", "kind", "error_class")

// StormStarted is logged when a Route's new Alert Groups start a Storm (C-11.FR-6).
var StormStarted = newEvent("storm_started", LevelInfo, "C-11",
	"The new Alert Groups of a Route exceeded route.storm_threshold within a minute and started a Storm: route is the "+
		"public_id of the Route, and alert_groups and urgent are the Storm's own counts at its start, as storm_ended "+
		"has them at its end: the new Alert Groups it counted, 1 (the one that started it), and how many of them were "+
		"Urgent, 1 or 0. Each Destination of the Route gets a Storm summary and only Urgent Alert Groups are posted "+
		"one by one until it calms down. Logged once the change that started it committed.",
	"route", "alert_groups", "urgent")

// StormEnded is logged when a Storm ends after delivery.storm_calm_period below the threshold (C-11.FR-6).
var StormEnded = newEvent("storm_ended", LevelInfo, "C-11",
	"A Storm ended after its Route stayed at or below route.storm_threshold for delivery.storm_calm_period: route is the "+
		"public_id of the Route, and alert_groups and urgent are the Storm's own counts at its end, as storm_started "+
		"has them at its start: the new Alert Groups it counted and how many of them were Urgent. The Storm summary "+
		"shows its final state and the Alert Groups still open are posted Quietly.",
	"route", "alert_groups", "urgent")

// IngestReplayed is logged when muster ingest replay set Stored Snapshots back to pending.
var IngestReplayed = newEvent("ingest_replayed", LevelInfo, "C-06",
	"muster ingest replay set the Stored Snapshots received within the period back to pending, for processing again: "+
		"actor is the --actor name, since the period as given, integration the public_id of the Integration it was "+
		"limited to, empty for all, and count how many Stored Snapshots it set back.",
	"actor", "since", "integration", "count")

// DevClockLoaded is logged when a process in development mode reads the development clock: at start and after each
// change on any replica.
var DevClockLoaded = newEvent("dev_clock_loaded", LevelInfo, "C-01",
	"Development mode only: this process runs on the development clock, offset_seconds ahead of the system time, as "+
		"stored in the database for every replica; logged at start and after each advance.",
	"offset_seconds")

// DevClockLoadFailed is logged when a process in development mode could not read the development clock after a
// change.
var DevClockLoadFailed = newEvent("dev_clock_load_failed", LevelWarn, "C-01",
	"Development mode only: the development clock could not be read after a change on another replica; this process "+
		"keeps its offset until the next change or the next LISTEN.",
	"error")

// Event is a registered log event. Its zero value is not registered, and the logger refuses it.
type Event struct {
	def *eventDef
}

type eventDef struct {
	name  string
	level Level
	// also are the other levels of an event that LogAt writes at a level chosen by the caller.
	also        []Level
	capability  string
	description string
	fields      []string
}

func (d *eventDef) levels() []Level {
	return append([]Level{d.level}, d.also...)
}

// EventInfo describes a registered event for the reference page.
type EventInfo struct {
	Name  string
	Level Level
	// Levels are all the levels of the event, Level first; an event with more than one is logged with LogAt.
	Levels      []Level
	Fields      []string
	Capability  string
	Description string
}

var (
	registry []*eventDef

	snakeCaseRe  = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	capabilityRe = regexp.MustCompile(`^C-[0-9]{2}$`)

	// reservedKeys are written by the logger itself on every line.
	reservedKeys = []string{"time", "level", "event", "msg"}
)

func newEvent(name string, level Level, capability, description string, fields ...string) Event {
	def := &eventDef{name: name, level: level, capability: capability, description: description, fields: fields}
	registry = append(registry, def)
	return Event{def: def}
}

// newEventAt declares an event whose level the caller chooses among levels, the first being its usual one.
func newEventAt(levels []Level, name, capability, description string, fields ...string) Event {
	e := newEvent(name, levels[0], capability, description, fields...)
	e.def.also = levels[1:]
	return e
}

// Name is the event's name, as written in the event key of its lines.
func (e Event) Name() string {
	if e.def == nil {
		return ""
	}
	return e.def.name
}

func (d *eventDef) declares(field string) bool {
	return slices.Contains(d.fields, field)
}

// Events returns the registered events sorted by name, and an error naming every invalid declaration.
func Events() ([]EventInfo, error) {
	return describe(registry)
}

func describe(defs []*eventDef) ([]EventInfo, error) {
	infos := make([]EventInfo, 0, len(defs))
	for _, d := range defs {
		infos = append(infos, EventInfo{
			Name:        d.name,
			Level:       d.level,
			Levels:      d.levels(),
			Fields:      slices.Clone(d.fields),
			Capability:  d.capability,
			Description: d.description,
		})
	}
	slices.SortFunc(infos, func(a, b EventInfo) int { return strings.Compare(a.Name, b.Name) })
	return infos, validate(infos)
}

func validate(infos []EventInfo) error {
	var errs []error
	seen := map[string]bool{}
	for _, e := range infos {
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("log event %q: %s", e.Name, fmt.Sprintf(format, args...)))
		}
		if !snakeCaseRe.MatchString(e.Name) {
			bad("the name is not snake_case")
		}
		if seen[e.Name] {
			bad("declared twice")
		}
		seen[e.Name] = true
		levels := map[Level]bool{}
		for _, l := range append([]Level{e.Level}, e.Levels...) {
			if !l.valid() {
				bad("level %d is not INFO, WARN or ERROR", int(l))
			}
		}
		for _, l := range e.Levels {
			if levels[l] {
				bad("level %s is declared twice", l)
			}
			levels[l] = true
		}
		if !capabilityRe.MatchString(e.Capability) {
			bad("capability %q is not a capability ID such as C-02", e.Capability)
		}
		if strings.TrimSpace(e.Description) == "" {
			bad("the description is empty")
		}
		fields := map[string]bool{}
		for _, f := range e.Fields {
			switch {
			case !snakeCaseRe.MatchString(f):
				bad("field %q is not snake_case", f)
			case slices.Contains(reservedKeys, f):
				bad("field %q is reserved for the logger", f)
			case fields[f]:
				bad("field %q is declared twice", f)
			}
			fields[f] = true
		}
	}
	return errors.Join(errs...)
}

// FallbackTemplateUsed is logged when a template of a Route failed while rendering a message and the Fallback
// template stood in for it (C-12.FR-6, ADR-0012).
var FallbackTemplateUsed = newEvent("fallback_template_used", LevelWarn, "C-12",
	"A template of a Route failed while rendering a message, so the message used the Fallback template — every label "+
		"under Muster's heading, footer and buttons: route is the public_id of the Route, group the public_id of the "+
		"Alert Group, template the kind of template and error what failed, with its line and column. The Route shows "+
		"a template error and MusterTemplateError fires until the template renders again. Logged once the change "+
		"committed.",
	"route", "group", "template", "error")

// TemplateRecovered is logged when a template of a Route that kept failing rendered again (C-12.FR-6).
var TemplateRecovered = newEvent("template_recovered", LevelInfo, "C-12",
	"A template of a Route that kept failing rendered again: route is the public_id of the Route and template the kind "+
		"of template. The Route's template error is cleared and MusterTemplateError resolves. Logged once the change "+
		"committed.",
	"route", "template")
