// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package metrics

import (
	"cmp"
	"net/http"

	vm "github.com/VictoriaMetrics/metrics"

	"github.com/muster-io/muster/internal/buildinfo"
)

const contentType = "text/plain; version=0.0.4; charset=utf-8"

// Handler serves the registry, then the process and Go runtime metrics, in the Prometheus text format. The Leader-only
// metrics are written only while leading reports true; a nil leading never does. Muster never pushes: this handler is
// the only way out for metrics.
func Handler(leading func() bool) http.Handler {
	BuildInfo.With(cmp.Or(buildinfo.Version, "unknown"), cmp.Or(buildinfo.Commit, "unknown")).Set(1)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		set.WritePrometheus(w)
		if leading != nil && leading() {
			leaderSet.WritePrometheus(w)
		}
		vm.WriteProcMetrics(w)
		vm.WriteGoMetrics(w)
	})
}
