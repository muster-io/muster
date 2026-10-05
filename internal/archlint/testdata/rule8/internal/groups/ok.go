package groups

import (
	vm "github.com/VictoriaMetrics/metrics"

	own "github.com/muster-io/muster/internal/metrics"
)

// Good: the registry's own NewHistogram and the Prometheus constructors.
var (
	g = own.NewHistogram("g")
	h = vm.NewPrometheusHistogram("h")
	i = set.GetOrCreatePrometheusHistogram("i")
)
