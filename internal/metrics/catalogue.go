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
