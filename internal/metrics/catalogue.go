// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package metrics

import (
	"net/http"
	"slices"

	"github.com/muster-io/muster/api"
)

// The metric registry (C-02.FR-19, ADR-0014). Every metric Muster exports is declared below; reference.md assigns each
// one to the capability that exports it, and each capability adds its own. Labels name configuration entities only by
// public_id (entity), the labels of *_info gauges carry what they describe (info), and every other label takes its
// values from a closed set listed here; alert labels, Alert Group numbers, users and the Organization never appear. Histograms take explicit le buckets chosen for their kind of
// latency. Adding a metric or a label value is a reviewed change, and make generate rebuilds the reference page.

// BuildInfo is always 1; its labels carry the version and commit of the binary (C-01.FR-12).
var BuildInfo = newGauge(Definition{
	Name: "muster_build_info",
	Help: "Always 1; the labels carry the version and commit of the running binary.",
	Labels: []Label{
		info("version", "the release version of the binary, such as 1.4.0"),
		info("commit", "the Git commit the binary was built from"),
	},
	Capability: "C-02",
})

// DBPoolConnections counts the connections of the main database pool by state.
var DBPoolConnections = newGauge(Definition{
	Name:       "muster_db_pool_connections",
	Help:       "Connections of the main database pool of this replica, by state.",
	Labels:     []Label{closed("state", "the state of the connection", "acquired", "idle", "constructing")},
	Capability: "C-02",
})

// DBPoolMaxConnections is the size limit of the main database pool.
var DBPoolMaxConnections = newGauge(Definition{
	Name:       "muster_db_pool_max_connections",
	Help:       "The most connections the main database pool of this replica opens.",
	Capability: "C-02",
})

// DBPoolAcquires counts the connections taken from the main database pool.
var DBPoolAcquires = newCounter(Definition{
	Name:       "muster_db_pool_acquires_total",
	Help:       "Connections taken from the main database pool of this replica.",
	Capability: "C-02",
})

// DBPoolAcquireWait sums the time spent waiting for a connection of the main database pool.
var DBPoolAcquireWait = newCounter(Definition{
	Name:       "muster_db_pool_acquire_wait_seconds_total",
	Help:       "Seconds spent waiting for a connection of the main database pool of this replica, summed over every acquire.",
	Capability: "C-02",
})

// Leader is 1 on the replica that holds the Leader lock and 0 on the others (ADR-0007).
var Leader = newGauge(Definition{
	Name:       "muster_leader",
	Help:       "1 while this replica is the Leader and runs the Leader tasks, 0 otherwise.",
	Capability: "C-02",
})

// ClockSkew is this replica's clock against the database clock.
var ClockSkew = newGauge(Definition{
	Name:       "muster_clock_skew_seconds",
	Help:       "This replica's clock minus the database clock, corrected by half the round trip; positive when the replica is ahead.",
	Capability: "C-02",
})

// ShortLivedTables are the table values of muster_short_lived_rows_pruned_total: the short-lived tables that the
// short_lived_pruning Leader task prunes. A story that adds a table to the task adds it here.
var ShortLivedTables = []string{"sessions", "sign_in_throttles", "password_setups", "oidc_auth_requests"}

// ShortLivedRowsPruned counts the rows the short_lived_pruning Leader task deleted, by table.
var ShortLivedRowsPruned = newCounter(Definition{
	Name:       "muster_short_lived_rows_pruned_total",
	Help:       "Short-lived rows that could no longer be used and that the Leader deleted, by table.",
	Labels:     []Label{closed("table", "the short-lived table the rows were deleted from", ShortLivedTables...)},
	Capability: "C-02",
})

// clientLabels are the labels of the outbound HTTP metrics: the client classes and the outcomes of ADR-0015.
var clientLabels = []Label{
	closed("client", "the client class of ADR-0015", "delivery", "interactive", "background", "heartbeat"),
	closed("outcome", "the classified outcome of the request",
		"ok", "retry_after", "transient", "fatal", "unknown", "blocked", "redirect"),
}

// ClientRequests counts the outbound HTTP requests by client class and outcome.
var ClientRequests = newCounter(Definition{
	Name:       "muster_client_requests_total",
	Help:       "Outbound HTTP requests, one per attempt, by client class and classified outcome.",
	Labels:     clientLabels,
	Capability: "C-02",
})

// ClientRequestDuration observes how long each outbound HTTP request took, its body included.
var ClientRequestDuration = newHistogram(Definition{
	Name:       "muster_client_request_duration_seconds",
	Help:       "Duration of outbound HTTP requests, one per attempt, from the dial to the end of the body, by client class and outcome.",
	Labels:     clientLabels,
	Buckets:    []float64{0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	Capability: "C-02",
})

// RouteUnmatched is the route_pattern of an API request whose path and method match no operation of the
// specification.
const RouteUnmatched = "unmatched"

// MethodOther is the method label of a request whose method is none of the listed ones.
const MethodOther = "OTHER"

// apiLabels are the labels shared by the API request metrics: the spec's path template and the HTTP method.
var apiLabels = []Label{
	closed("route_pattern", "the path template of the operation in the API specification, such as "+
		"/alert-groups/{alert_group_id}, or unmatched for a path no operation has",
		append(api.PathTemplates(), RouteUnmatched)...),
	closed("method", "the HTTP method of the request", http.MethodGet, http.MethodHead, http.MethodPost,
		http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, MethodOther),
}

// StatusCodes are the code values of muster_api_requests_total: the statuses the API answers with.
var StatusCodes = []string{"200", "201", "202", "204", "302", "304", "400", "401", "403", "404", "405", "409", "410",
	"412", "413", "415", "422", "428", "429", "500", "501", "502", "503", "504"}

// APIRequests counts the requests to the API of the app listener by operation, method and status.
var APIRequests = newCounter(Definition{
	Name:       "muster_api_requests_total",
	Help:       "Requests to the API on the app listener, by the operation's path template, the method and the status code.",
	Labels:     append(slices.Clone(apiLabels), closed("code", "the HTTP status code of the answer", StatusCodes...)),
	Capability: "C-03",
})

// APIRequestDuration observes how long the API took to answer.
var APIRequestDuration = newHistogram(Definition{
	Name:       "muster_api_request_duration_seconds",
	Help:       "Time from the arrival of an API request on the app listener to the end of its answer, by path template and method.",
	Labels:     apiLabels,
	Buckets:    []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	Capability: "C-03",
})

// OIDCChecks counts the background re-checks of OIDC users at the identity provider, by outcome.
var OIDCChecks = newCounter(Definition{
	Name: "muster_oidc_checks_total",
	Help: "Background re-checks of OIDC users at the identity provider, by outcome.",
	Labels: []Label{closed("outcome", "ok is a refresh the identity provider granted, refused one it refused (the "+
		"user's sessions ended), unavailable one that reached no decision, skipped a user without a live session or a "+
		"usable Personal access token, not sent to the identity provider", "ok", "refused", "unavailable", "skipped")},
	Capability: "C-03",
})

// LoginFailures counts the failed sign-ins that were evaluated, by method.
var LoginFailures = newCounter(Definition{
	Name: "muster_login_failures_total",
	Help: "Failed sign-ins that were evaluated, by method; attempts refused by the sign-in throttle are not counted.",
	Labels: []Label{closed("method", "local is a wrong login or password, oidc a refused OIDC sign-in, totp a wrong "+
		"TOTP or recovery code", "local", "oidc", "totp")},
	Capability: "C-03",
})

// IngestOutcomes are the outcome values of muster_ingest_requests_total.
var IngestOutcomes = []string{"accepted", "unauthorized", "too_large"}

// IngestRequests counts the requests to the ingestion endpoint by Integration and outcome (C-05.FR-4).
var IngestRequests = newCounter(Definition{
	Name: "muster_ingest_requests_total",
	Help: "Requests to the ingestion endpoint, by the token's Integration and the outcome.",
	Labels: []Label{
		{Name: "integration", Kind: LabelEntity, Description: "the public_id of the token's Integration, a revoked " +
			"token's included, or unknown when the token matches no Integration that is not deleted"},
		closed("outcome", "accepted is a body stored as a Stored Snapshot, unauthorized a missing or wrong token, "+
			"too_large a body above ingest.body_limit", IngestOutcomes...),
	},
	Capability: "C-05",
})

// IngestRequestDuration observes how long the ingestion endpoint took to answer.
var IngestRequestDuration = newHistogram(Definition{
	Name:       "muster_ingest_request_duration_seconds",
	Help:       "Time from the arrival of an ingestion request to the end of its answer, the commit of the Stored Snapshot included.",
	Buckets:    []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	Capability: "C-05",
})

// IngestProcessingDelay observes the time from the receipt of a Stored Snapshot to the end of its processing.
var IngestProcessingDelay = newHistogram(Definition{
	Name:       "muster_ingest_processing_delay_seconds",
	Help:       "Time from the receipt of a Stored Snapshot to the end of its processing, processed or failed, by Integration.",
	Labels:     []Label{entity("integration")},
	Buckets:    []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
	Capability: "C-06",
})

// IngestBacklog is the number of pending Stored Snapshots, exported by the Leader.
var IngestBacklog = newGauge(Definition{
	Name:       "muster_ingest_backlog",
	Help:       "Stored Snapshots waiting for processing in every Organization, counted by the Leader.",
	Capability: "C-06",
	LeaderOnly: true,
})

// IngestTruncatedSnapshots counts the Snapshots with truncatedAlerts above 0 (C-06.FR-6).
var IngestTruncatedSnapshots = newCounter(Definition{
	Name:       "muster_ingest_truncated_snapshots_total",
	Help:       "Processed Snapshots that Alertmanager truncated (truncatedAlerts above 0), by Integration.",
	Labels:     []Label{entity("integration")},
	Capability: "C-06",
})

// IngestFailedSnapshots counts the Stored Snapshots that could not be processed (C-06.FR-20).
var IngestFailedSnapshots = newCounter(Definition{
	Name: "muster_ingest_failed_snapshots_total",
	Help: "Stored Snapshots that could not be processed and were marked failed — a body that is not an " +
		"Alertmanager webhook, or an error of processing — by Integration.",
	Labels:     []Label{entity("integration")},
	Capability: "C-06",
})

// AlertsResolved counts the Alerts that resolved, by Integration and reason.
var AlertsResolved = newCounter(Definition{
	Name: "muster_alerts_resolved_total",
	Help: "Alerts that resolved, by Integration and reason.",
	Labels: []Label{
		entity("integration"),
		closed("reason", "resolved is a resolve from Alertmanager, for the Alert or its whole Alertmanager group; gone "+
			"an Alert Alertmanager no longer lists; stale one not seen for stale_after with a live Heartbeat; "+
			"integration_deleted one of a deleted Integration", "resolved", "gone", "stale", "integration_deleted"),
	},
	Capability: "C-06",
})

// IngestResolvedDropped counts the resolves of fingerprints that fire nowhere (C-06.FR-4).
var IngestResolvedDropped = newCounter(Definition{
	Name: "muster_ingest_resolved_dropped_total",
	Help: "Alerts sent as resolved whose fingerprint fires nowhere and that were dropped, by Integration; a re-sent " +
		"resolve of an Alert already resolved is not counted.",
	Labels:     []Label{entity("integration")},
	Capability: "C-06",
})

// HeartbeatLost is 1 while the Heartbeat of an Integration is lost and 0 while it is on and not lost, exported by the
// Leader, whose Heartbeat check sets it (C-07.FR-8).
var HeartbeatLost = newGauge(Definition{
	Name: "muster_heartbeat_lost",
	Help: "1 while the Integration is Heartbeat lost, 0 while its Heartbeat is on and not lost; set by the Leader's " +
		"Heartbeat check.",
	Labels:     []Label{entity("integration")},
	Capability: "C-07",
	LeaderOnly: true,
})

// IntegrationInfo is 1 for every Integration that is not deleted; its labels carry the Integration's name.
var IntegrationInfo = newGauge(Definition{
	Name: "muster_integration_info",
	Help: "Always 1, one series per Integration that is not deleted; the labels carry its public_id and name.",
	Labels: []Label{
		entity("integration"),
		info("name", "the name of the Integration"),
	},
	Capability: "C-05",
})

// RouteInfo is 1 for every Route that is not deleted, the Default route included; its labels carry the Route's name.
var RouteInfo = newGauge(Definition{
	Name: "muster_route_info",
	Help: "Always 1, one series per Route that is not deleted, the Default route included; the labels carry its " +
		"public_id and name.",
	Labels: []Label{
		entity("route"),
		info("name", "the name of the Route"),
	},
	Capability: "C-08",
})

// AlertGroups is the number of open Alert Groups per Route and status, exported by the Leader.
var AlertGroups = newGauge(Definition{
	Name: "muster_alert_groups",
	Help: "Open Alert Groups by Route and status, counted by the Leader; an Alert Group moved to the Default route " +
		"counts there.",
	Labels: []Label{
		entity("route"),
		closed("status", "the status of the open Alert Groups", "firing", "acknowledged", "snoozed"),
	},
	Capability: "C-09",
	LeaderOnly: true,
})

// AlertGroupsCreated counts the Alert Groups that started, by Route.
var AlertGroupsCreated = newCounter(Definition{
	Name:       "muster_alert_groups_created_total",
	Help:       "Alert Groups that started, by Route.",
	Labels:     []Label{entity("route")},
	Capability: "C-09",
})

// AlertGroupsReopened counts the Reopens of Alert Groups, by Route.
var AlertGroupsReopened = newCounter(Definition{
	Name:       "muster_alert_groups_reopened_total",
	Help:       "Alert Groups that reopened within their Route's Reopen window, by Route.",
	Labels:     []Label{entity("route")},
	Capability: "C-09",
})

// AlertGroupsResolved counts the resolutions of Alert Groups, by Route and by who resolved them.
var AlertGroupsResolved = newCounter(Definition{
	Name: "muster_alert_groups_resolved_total",
	Help: "Alert Groups that were resolved, by Route and by who resolved them.",
	Labels: []Label{
		entity("route"),
		closed("by", "user is a person's resolve, system the resolve when the last Alert resolved", "user", "system"),
	},
	Capability: "C-09",
})

// AlertGroupTimeToResolve observes, at each resolution, the time from the start of the Alert Group.
var AlertGroupTimeToResolve = newHistogram(Definition{
	Name:       "muster_alert_group_time_to_resolve_seconds",
	Help:       "Time from the start of an Alert Group to its resolution, observed at each resolution, by Route.",
	Labels:     []Label{entity("route")},
	Buckets:    []float64{60, 300, 600, 1800, 3600, 7200, 14400, 28800, 43200, 86400},
	Capability: "C-09",
})

// CommandNames are the command values of muster_commands_total: the Commands of C-10 and "Still on it".
var CommandNames = []string{"acknowledge", "unacknowledge", "resolve", "unresolve", "snooze", "unsnooze", "add_note",
	"still_on_it"}

// Commands counts the Commands run on Alert Groups, by command, Transport and outcome; a bulk command counts each
// Alert Group.
var Commands = newCounter(Definition{
	Name: "muster_commands_total",
	Help: "Commands run on Alert Groups, one per Alert Group, by command, Transport and outcome.",
	Labels: []Label{
		closed("command", "the Command", CommandNames...),
		closed("transport", "how the Command reached Muster", "ui", "api", "mattermost", "telegram", "system"),
		closed("outcome", "done changed the Alert Group, unchanged was an idempotent repeat, refused a refusal or a "+
			"missing Permission, skipped an Alert Group a bulk Acknowledge left to its Owner, failed an Alert Group "+
			"that was not found", "done", "unchanged", "refused", "skipped", "failed"),
	},
	Capability: "C-10",
})

// AlertGroupTimeToAck observes, at the first acknowledgement of an Alert Group, the time from its start.
var AlertGroupTimeToAck = newHistogram(Definition{
	Name: "muster_alert_group_time_to_ack_seconds",
	Help: "Time from the start of an Alert Group to its first acknowledgement, observed once per Alert Group, by " +
		"Route.",
	Labels:     []Label{entity("route")},
	Buckets:    []float64{60, 300, 600, 1800, 3600, 7200, 14400, 28800, 43200, 86400},
	Capability: "C-10",
})

// DeliveryKinds are the kind values of muster_delivery_attempts_total: what an attempt sends.
var DeliveryKinds = []string{"publication", "update", "thread_reply", "storm_summary", "final_edit", "webhook_event"}

// DeliveryOutcomes are the outcome values of muster_delivery_attempts_total: delivered, or the error class of the
// answer.
var DeliveryOutcomes = []string{"delivered", "markup_rejected", "retry_after", "transient", "fatal", "unknown",
	"template_error"}

// DeliveryAttempts counts the calls the delivery worker made, by Destination, kind and outcome (C-11.FR-17).
var DeliveryAttempts = newCounter(Definition{
	Name: "muster_delivery_attempts_total",
	Help: "Calls of the delivery worker to a Destination, one per attempt, by Destination, what it sent and its " +
		"outcome.",
	Labels: []Label{
		entity("destination"),
		closed("kind", "publication is a new Root message, update an edit of one, thread_reply a reply in its Thread, "+
			"storm_summary and final_edit those messages, webhook_event an event of an outgoing webhook",
			DeliveryKinds...),
		closed("outcome", "delivered, or the error class of the answer", DeliveryOutcomes...),
	},
	Capability: "C-11",
})

// DeliveryLatency observes, at the call that delivers a change caused by a Snapshot, the time since its receipt.
var DeliveryLatency = newHistogram(Definition{
	Name: "muster_delivery_latency_seconds",
	Help: "Time from the receipt of the Snapshot behind a change of a Root message to the messenger API call that " +
		"delivered it, by Destination; changes made by Commands or timers are not observed. The service-level " +
		"indicator of NFR-2.",
	Labels:     []Label{entity("destination")},
	Buckets:    []float64{0.25, 0.5, 1, 2, 3, 5, 10, 30, 60, 300, 900},
	Capability: "C-11",
})

// DeliveryQueue is the number of pending deliveries and due Thread replies per Destination, exported by the Leader.
var DeliveryQueue = newGauge(Definition{
	Name: "muster_delivery_queue",
	Help: "Pending deliveries and Thread replies that are due, per Destination that is not deleted, counted by the " +
		"Leader.",
	Labels:     []Label{entity("destination")},
	Capability: "C-11",
	LeaderOnly: true,
})

// DestinationBroken is 1 while a Destination is Broken and 0 while it is healthy, exported by the Leader (C-11.FR-9).
var DestinationBroken = newGauge(Definition{
	Name: "muster_destination_broken",
	Help: "1 while the Destination is Broken, 0 while it is healthy, per Destination that is not deleted; set by the " +
		"Leader.",
	Labels:     []Label{entity("destination")},
	Capability: "C-11",
	LeaderOnly: true,
})

// StormActive is 1 while a Route has an active Storm and 0 otherwise, exported by the Leader (C-11.FR-6).
var StormActive = newGauge(Definition{
	Name:       "muster_storm_active",
	Help:       "1 while the Route has an active Storm, 0 otherwise, per Route that is not deleted; set by the Leader.",
	Labels:     []Label{entity("route")},
	Capability: "C-11",
	LeaderOnly: true,
})

// DestinationInfo is 1 for every Destination that is not deleted; its labels carry the Destination's name.
var DestinationInfo = newGauge(Definition{
	Name: "muster_destination_info",
	Help: "Always 1, one series per Destination that is not deleted; the labels carry its public_id and name.",
	Labels: []Label{
		entity("destination"),
		info("name", "the name of the Destination"),
	},
	Capability: "C-11",
})

// TemplateKinds are the values of the label template: the templates of C-12.FR-4.
var TemplateKinds = []string{"root_message", "line", "ack_timeout_notice", "link_rule", "webhook_request"}

// TemplateErrors counts the template renders that failed at runtime, by the Route or the Destination that owns the
// template, the other one empty (C-12.FR-6, ADR-0012).
var TemplateErrors = newCounter(Definition{
	Name: "muster_template_errors_total",
	Help: "Templates that failed while rendering, by the Route or the Destination that owns the template (the other " +
		"label empty) and the kind of template; a message falls back to the Fallback template, an outgoing webhook " +
		"request is not sent.",
	Labels: []Label{
		entity("route"),
		entity("destination"),
		closed("template", "the kind of template", TemplateKinds...),
	},
	Capability: "C-12",
})

// TemplateRenderDuration observes the time of every template render, by the kind of template (C-12.FR-13).
var TemplateRenderDuration = newHistogram(Definition{
	Name: "muster_template_render_duration_seconds",
	Help: "Time to render a template in the sandbox, by the kind of template; a Root message is observed as a whole, " +
		"its built-in default included.",
	Labels:     []Label{closed("template", "the kind of template", TemplateKinds...)},
	Buckets:    []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
	Capability: "C-12",
})
