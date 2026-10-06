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
var ShortLivedTables = []string{"sessions", "sign_in_throttles", "password_setups"}

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

// LoginFailures counts the failed sign-ins that were evaluated, by method.
var LoginFailures = newCounter(Definition{
	Name: "muster_login_failures_total",
	Help: "Failed sign-ins that were evaluated, by method; attempts refused by the sign-in throttle are not counted.",
	Labels: []Label{closed("method", "local is a wrong login or password, oidc a refused OIDC sign-in, totp a wrong "+
		"TOTP or recovery code", "local", "oidc", "totp")},
	Capability: "C-03",
})
