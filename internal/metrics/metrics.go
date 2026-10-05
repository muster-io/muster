// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package metrics is the metric registry (C-02.FR-19, ADR-0014): every metric Muster exports is declared in
// catalogue.go with its type, labels, capability and, for histograms, its le buckets, and is reachable only through the
// handles the declarations return.
package metrics

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	vm "github.com/VictoriaMetrics/metrics"
)

// Kind is the type of a metric. Histograms are always Prometheus le histograms with explicit buckets.
type Kind string

const (
	KindCounter   Kind = "counter"
	KindGauge     Kind = "gauge"
	KindHistogram Kind = "histogram"
)

// LabelKind says where a label's values come from. A label of no kind is free-form, which the registry refuses.
type LabelKind string

const (
	// LabelEntity carries the public_id of a configuration entity: integration, route or destination. Its value may
	// be empty where the metric says the entity is absent.
	LabelEntity LabelKind = "entity"
	// LabelClosed takes one of the values listed with the label.
	LabelClosed LabelKind = "closed"
	// LabelInfo carries what an *_info gauge describes, such as the version of the binary or the name of an entity;
	// no other metric may have it.
	LabelInfo LabelKind = "info"
)

// EntityLabels are the only labels that name configuration entities (ADR-0014).
var EntityLabels = []string{"integration", "route", "destination"}

// Label is one label of a metric.
type Label struct {
	Name        string
	Kind        LabelKind
	Values      []string // the closed set of a LabelClosed label
	Description string   // what the values are, for the reference page
}

// Definition declares one metric.
type Definition struct {
	Name       string
	Kind       Kind
	Help       string
	Labels     []Label
	Buckets    []float64 // the le upper bounds of a histogram
	Capability string    // the capability that exports it, such as C-02
	LeaderOnly bool      // exported only while the replica is the Leader (ADR-0007)
}

var (
	// set holds the series every replica exports, leaderSet those of Leader-only metrics, and discardSet the refused
	// series, which are never exported.
	set        = vm.NewSet()
	leaderSet  = vm.NewSet()
	discardSet = vm.NewSet()

	registry []*Definition

	// strict makes a refused series panic, so that the test doing it fails. Outside tests the observation goes to a
	// series that is never exported: a wrong label value never stops the alerting path.
	strict = testing.Testing()

	nameRe       = regexp.MustCompile(`^muster_[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	labelRe      = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	capabilityRe = regexp.MustCompile(`^C-[0-9]{2}$`)

	labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
)

func entity(name string) Label {
	return Label{Name: name, Kind: LabelEntity, Description: "the public_id of the " + name}
}

func closed(name, description string, values ...string) Label {
	return Label{Name: name, Kind: LabelClosed, Values: values, Description: description}
}

func info(name, description string) Label {
	return Label{Name: name, Kind: LabelInfo, Description: description}
}

func register(d Definition) *Definition {
	def := &d
	registry = append(registry, def)
	return def
}

// Counter is a registered counter.
type Counter struct{ def *Definition }

// Gauge is a registered gauge.
type Gauge struct{ def *Definition }

// Histogram is a registered le histogram.
type Histogram struct{ def *Definition }

func newCounter(d Definition) Counter {
	d.Kind = KindCounter
	return Counter{register(d)}
}

func newGauge(d Definition) Gauge {
	d.Kind = KindGauge
	return Gauge{register(d)}
}

func newHistogram(d Definition) Histogram {
	d.Kind = KindHistogram
	return Histogram{register(d)}
}

// With returns the series of c with the label values in declaration order.
func (c Counter) With(values ...string) *vm.Counter {
	name, s := series(c.def, values)
	return s.GetOrCreateCounter(name)
}

// Float returns the series of c as a counter of fractional amounts, such as seconds; one counter is either integer or
// float, never both.
func (c Counter) Float(values ...string) *vm.FloatCounter {
	name, s := series(c.def, values)
	return s.GetOrCreateFloatCounter(name)
}

// Func makes the series of c read f at every scrape, for a count that a library keeps, such as the acquires of the
// database pool; f must never decrease. A series keeps the first f it gets.
func (c Counter) Func(f func() float64, values ...string) {
	name, s := series(c.def, values)
	s.GetOrCreateGauge(name, f)
}

// With returns the series of g with the label values in declaration order, to be set by the caller.
func (g Gauge) With(values ...string) *vm.Gauge {
	name, s := series(g.def, values)
	return s.GetOrCreateGauge(name, nil)
}

// Func makes the series of g read f at every scrape; a series keeps the first f it gets.
func (g Gauge) Func(f func() float64, values ...string) {
	name, s := series(g.def, values)
	s.GetOrCreateGauge(name, f)
}

// Delete removes the series of g, such as the *_info series of a deleted entity.
func (g Gauge) Delete(values ...string) {
	name, s := series(g.def, values)
	s.UnregisterMetric(name)
}

// With returns the series of h with the label values in declaration order.
func (h Histogram) With(values ...string) *vm.PrometheusHistogram {
	name, s := series(h.def, values)
	return s.GetOrCreatePrometheusHistogramExt(name, h.def.Buckets)
}

// series names the series of d with values, and the set it belongs to: the set of every replica, the Leader's set,
// or the set that is never written when the values are refused.
func series(d *Definition, values []string) (string, *vm.Set) {
	if err := checkValues(d, values); err != nil {
		if strict {
			panic(err.Error())
		}
		return d.Name, discardSet
	}
	s := set
	if d.LeaderOnly {
		s = leaderSet
	}
	if len(values) == 0 {
		return d.Name, s
	}
	var b strings.Builder
	b.WriteString(d.Name)
	b.WriteByte('{')
	for i, l := range d.Labels {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(l.Name)
		b.WriteString(`="`)
		_, _ = labelEscaper.WriteString(&b, values[i])
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String(), s
}

func checkValues(d *Definition, values []string) error {
	if len(values) != len(d.Labels) {
		return fmt.Errorf("metrics: %s takes %d label values, got %d", d.Name, len(d.Labels), len(values))
	}
	for i, l := range d.Labels {
		v := values[i]
		switch {
		case v == "" && l.Kind != LabelEntity:
			return fmt.Errorf("metrics: %s: the label %s is empty", d.Name, l.Name)
		case l.Kind == LabelClosed && !slices.Contains(l.Values, v):
			return fmt.Errorf("metrics: %s: %q is not a value of the label %s", d.Name, v, l.Name)
		}
	}
	return nil
}

// Catalogue returns the registered metrics sorted by name, and an error naming every invalid declaration.
func Catalogue() ([]Definition, error) {
	return catalogue(registry)
}

func catalogue(defs []*Definition) ([]Definition, error) {
	out := make([]Definition, 0, len(defs))
	for _, d := range defs {
		c := *d
		c.Labels = slices.Clone(d.Labels)
		for i := range c.Labels {
			c.Labels[i].Values = slices.Clone(c.Labels[i].Values)
		}
		c.Buckets = slices.Clone(d.Buckets)
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Definition) int { return strings.Compare(a.Name, b.Name) })
	return out, validate(out)
}

func validate(defs []Definition) error {
	var errs []error
	seen := map[string]bool{}
	for _, d := range defs {
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("metric %q: %s", d.Name, fmt.Sprintf(format, args...)))
		}
		if !nameRe.MatchString(d.Name) {
			bad("the name is not snake_case with the muster_ prefix")
		}
		if seen[d.Name] {
			bad("declared twice")
		}
		seen[d.Name] = true
		switch d.Kind {
		case KindCounter:
			if !strings.HasSuffix(d.Name, "_total") {
				bad("a counter's name ends in _total")
			}
		case KindGauge, KindHistogram:
			if strings.HasSuffix(d.Name, "_total") {
				bad("only a counter's name ends in _total")
			}
		default:
			bad("type %q is not counter, gauge or histogram; histograms are le histograms only", d.Kind)
		}
		if d.Kind == KindHistogram {
			if len(d.Buckets) == 0 {
				bad("a histogram needs explicit le buckets")
			} else if err := vm.ValidateBuckets(d.Buckets); err != nil {
				bad("le buckets: %v", err)
			}
		} else if len(d.Buckets) > 0 {
			bad("only a histogram has buckets")
		}
		if strings.TrimSpace(d.Help) == "" {
			bad("the help is empty")
		}
		if !capabilityRe.MatchString(d.Capability) {
			bad("capability %q is not a capability ID such as C-02", d.Capability)
		}
		labels := map[string]bool{}
		for _, l := range d.Labels {
			if labels[l.Name] {
				bad("label %q is declared twice", l.Name)
			}
			labels[l.Name] = true
			for _, problem := range checkLabel(d, l) {
				bad("label %q %s", l.Name, problem)
			}
		}
	}
	return errors.Join(errs...)
}

func checkLabel(d Definition, l Label) []string {
	var problems []string
	if !labelRe.MatchString(l.Name) || l.Name == "le" {
		problems = append(problems, "is not a valid label name")
	}
	switch l.Kind {
	case LabelEntity:
		if !slices.Contains(EntityLabels, l.Name) {
			problems = append(problems, "is not an entity label (integration, route, destination)")
		}
	case LabelClosed:
		if len(l.Values) == 0 {
			problems = append(problems, "has an empty value set")
		}
		values := map[string]bool{}
		for _, v := range l.Values {
			if v == "" || values[v] {
				problems = append(problems, fmt.Sprintf("has an empty or repeated value %q", v))
			}
			values[v] = true
		}
	case LabelInfo:
		if d.Kind != KindGauge || !strings.HasSuffix(d.Name, "_info") {
			problems = append(problems, "is an info label outside an *_info gauge")
		}
	default:
		problems = append(problems, "is neither an entity label (integration, route, destination) nor declared with a closed value set")
	}
	if strings.TrimSpace(l.Description) == "" {
		problems = append(problems, "has no description")
	}
	return problems
}
