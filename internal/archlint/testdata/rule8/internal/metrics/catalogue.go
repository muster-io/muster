package metrics

import vm "github.com/VictoriaMetrics/metrics"

var set = vm.NewSet()

// Good: Prometheus le histograms, through the package functions and through a Set.
var (
	requestDuration = vm.NewPrometheusHistogram("muster_request_duration_seconds")
	claimDuration   = set.NewPrometheusHistogram("muster_claim_duration_seconds")
	renderDuration  = vm.GetOrCreatePrometheusHistogram("muster_render_duration_seconds")
	sendDuration    = set.GetOrCreatePrometheusHistogram("muster_send_duration_seconds")
	ingested        = vm.NewCounter("muster_alerts_ingested_total")
)

// NewHistogram is the registry's own constructor; calling it is not calling VictoriaMetrics.
func NewHistogram(name string) *vm.PrometheusHistogram {
	return set.NewPrometheusHistogram(name)
}
