// Package metrics is a stub of github.com/VictoriaMetrics/metrics with the histogram constructors that rule 8 tells
// apart, so the fixture needs no network and no real dependency.
package metrics

type Histogram struct{}

type PrometheusHistogram struct{}

type Counter struct{}

type Set struct{}

func NewSet() *Set { return &Set{} }

func NewHistogram(name string) *Histogram { return &Histogram{} }

func GetOrCreateHistogram(name string) *Histogram { return &Histogram{} }

func NewPrometheusHistogram(name string) *PrometheusHistogram { return &PrometheusHistogram{} }

func GetOrCreatePrometheusHistogram(name string) *PrometheusHistogram { return &PrometheusHistogram{} }

func NewCounter(name string) *Counter { return &Counter{} }

func (s *Set) NewHistogram(name string) *Histogram { return &Histogram{} }

func (s *Set) GetOrCreateHistogram(name string) *Histogram { return &Histogram{} }

func (s *Set) NewPrometheusHistogram(name string) *PrometheusHistogram { return &PrometheusHistogram{} }

func (s *Set) GetOrCreatePrometheusHistogram(name string) *PrometheusHistogram {
	return &PrometheusHistogram{}
}
