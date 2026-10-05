package groups

import (
	"github.com/VictoriaMetrics/metrics"
	vm "github.com/VictoriaMetrics/metrics"
)

var set = metrics.NewSet()

// Bad: vmrange histograms, however they are reached.
var (
	a       = metrics.NewHistogram("a")                  // want: 8
	b       = metrics.GetOrCreateHistogram("b")          // want: 8
	c       = set.NewHistogram("c")                      // want: 8
	d       = set.GetOrCreateHistogram("d")              // want: 8
	e       = vm.NewHistogram("e")                       // want: 8
	f       = metrics.NewSet().GetOrCreateHistogram("f") // want: 8
	newHist = metrics.NewHistogram                       // want: 8
)
