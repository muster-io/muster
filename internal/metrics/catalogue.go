// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package metrics

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
