// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package metrics

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	vm "github.com/VictoriaMetrics/metrics"

	"github.com/muster-io/muster/internal/buildinfo"
)

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want one containing %q", want)
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, want) {
			t.Fatalf("panic %q, want one containing %q", msg, want)
		}
	}()
	f()
}

func TestCatalogueValid(t *testing.T) {
	defs, err := Catalogue()
	if err != nil {
		t.Fatalf("the registry is invalid:\n%v", err)
	}
	for _, d := range defs {
		if d.Name == "muster_build_info" {
			if d.Kind != KindGauge || len(d.Labels) != 2 || d.Labels[0].Name != "version" || d.Labels[1].Name != "commit" {
				t.Errorf("muster_build_info is declared as %+v", d)
			}
			return
		}
	}
	t.Error("muster_build_info is not registered")
}

func TestValidate(t *testing.T) {
	good := func() Definition {
		return Definition{
			Name:       "muster_test_delivery_seconds",
			Kind:       KindHistogram,
			Help:       "A test histogram.",
			Labels:     []Label{entity("destination"), closed("outcome", "the result", "ok", "fatal")},
			Buckets:    []float64{0.1, 1, 5},
			Capability: "C-11",
		}
	}
	tests := []struct {
		name   string
		mutate func(d *Definition)
		want   string
	}{
		{"free-form label", func(d *Definition) { d.Labels = append(d.Labels, Label{Name: "alertname", Description: "x"}) },
			`label "alertname" is neither an entity label (integration, route, destination) nor declared with a closed value set`},
		{"entity label that is no entity", func(d *Definition) { d.Labels[0] = entity("user") },
			`label "user" is not an entity label`},
		{"empty closed set", func(d *Definition) { d.Labels[1] = closed("outcome", "the result") }, "has an empty value set"},
		{"repeated value", func(d *Definition) { d.Labels[1] = closed("outcome", "the result", "ok", "ok") },
			`has an empty or repeated value "ok"`},
		{"repeated label", func(d *Definition) { d.Labels = append(d.Labels, entity("destination")) },
			`label "destination" is declared twice`},
		{"label named le", func(d *Definition) { d.Labels[1] = closed("le", "x", "1") }, `label "le" is not a valid label name`},
		{"label without description", func(d *Definition) { d.Labels[1].Description = "" }, "has no description"},
		{"info label outside info", func(d *Definition) { d.Labels[1] = info("name", "x") }, "is an info label outside an *_info gauge"},
		{"vmrange histogram", func(d *Definition) { d.Kind = "vmrange_histogram" }, "histograms are le histograms only"},
		{"histogram without buckets", func(d *Definition) { d.Buckets = nil }, "a histogram needs explicit le buckets"},
		{"unsorted buckets", func(d *Definition) { d.Buckets = []float64{1, 0.5} }, "le buckets:"},
		{"buckets on a gauge", func(d *Definition) { d.Kind = KindGauge }, "only a histogram has buckets"},
		{"counter name", func(d *Definition) { d.Kind, d.Buckets = KindCounter, nil }, "a counter's name ends in _total"},
		{"total on a histogram", func(d *Definition) { d.Name = "muster_test_total" }, "only a counter's name ends in _total"},
		{"name", func(d *Definition) { d.Name = "delivery_seconds" }, "the name is not snake_case with the muster_ prefix"},
		{"help", func(d *Definition) { d.Help = "" }, "the help is empty"},
		{"capability", func(d *Definition) { d.Capability = "delivery" }, `capability "delivery" is not a capability ID`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := good()
			tt.mutate(&d)
			_, err := catalogue([]*Definition{&d})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}
	a, b := good(), good()
	if _, err := catalogue([]*Definition{&a, &b}); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("a duplicate metric: got %v", err)
	}
	if _, err := catalogue([]*Definition{&a}); err != nil {
		t.Errorf("a good metric: %v", err)
	}
}

// withRegistry gives the test fresh sets and an empty registry, and restores the real ones afterwards.
func withRegistry(t *testing.T) {
	t.Helper()
	oldSet, oldLeaderSet, oldDiscardSet, oldRegistry := set, leaderSet, discardSet, registry
	set, leaderSet, discardSet, registry = vm.NewSet(), vm.NewSet(), vm.NewSet(), nil
	t.Cleanup(func() { set, leaderSet, discardSet, registry = oldSet, oldLeaderSet, oldDiscardSet, oldRegistry })
}

func exposition(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	set.WritePrometheus(&b)
	return b.String()
}

// scrape returns the status, the content type and the body of a GET of h.
func scrape(t *testing.T, h http.Handler) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/metrics", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
}

func TestSeries(t *testing.T) {
	withRegistry(t)
	requests := newCounter(Definition{Name: "muster_test_requests_total", Help: "Requests.", Capability: "C-05",
		Labels: []Label{entity("integration"), closed("outcome", "the result", "accepted", "too_large")}})
	latency := newHistogram(Definition{Name: "muster_test_latency_seconds", Help: "Latency.", Capability: "C-11",
		Buckets: []float64{0.5, 1}})
	queue := newGauge(Definition{Name: "muster_test_queue", Help: "Queue.", Capability: "C-11"})
	pool := newGauge(Definition{Name: "muster_test_pool", Help: "Pool.", Capability: "C-02"})
	wait := newCounter(Definition{Name: "muster_test_wait_seconds_total", Help: "Wait.", Capability: "C-02"})
	acquires := newCounter(Definition{Name: "muster_test_acquires_total", Help: "Acquires.", Capability: "C-02"})
	errorsTotal := newCounter(Definition{Name: "muster_test_errors_total", Help: "Errors.", Capability: "C-12",
		Labels: []Label{entity("route"), entity("destination")}})
	infoGauge := newGauge(Definition{Name: "muster_test_info", Help: "Info.", Capability: "C-02",
		Labels: []Label{info("version", "the version")}})

	requests.With("int_7Kq2", "accepted").Add(2)
	latency.With().Update(0.7)
	queue.With().Set(3)
	pool.Func(func() float64 { return 7 })
	wait.Float().Add(1.5)
	acquires.Func(func() float64 { return 42 })
	errorsTotal.With("rt_1", "").Inc()
	infoGauge.With(`1.0"\` + "\n").Set(1)
	infoGauge.With("gone").Set(1)
	infoGauge.Delete("gone")

	got := exposition(t)
	for _, want := range []string{
		`muster_test_requests_total{integration="int_7Kq2",outcome="accepted"} 2`,
		`muster_test_latency_seconds_bucket{le="0.5"} 0`,
		`muster_test_latency_seconds_bucket{le="1"} 1`,
		`muster_test_latency_seconds_bucket{le="+Inf"} 1`,
		`muster_test_queue 3`,
		`muster_test_pool 7`,
		`muster_test_wait_seconds_total 1.5`,
		`muster_test_acquires_total 42`,
		`muster_test_errors_total{route="rt_1",destination=""} 1`,
		`muster_test_info{version="1.0\"\\\n"} 1`,
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("the exposition lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "gone") {
		t.Errorf("a deleted series was exported:\n%s", got)
	}
	if strings.Contains(got, "vmrange") {
		t.Errorf("a vmrange histogram was written:\n%s", got)
	}
	defs, err := Catalogue()
	if err != nil || len(defs) != 8 || defs[0].Name != "muster_test_acquires_total" {
		t.Errorf("Catalogue() = %v, %v", defs, err)
	}

	mustPanic(t, "takes 2 label values, got 1", func() { requests.With("int_7Kq2") })
	mustPanic(t, `"rejected" is not a value of the label outcome`, func() { requests.With("int_7Kq2", "rejected") })
	mustPanic(t, "the label outcome is empty", func() { requests.With("int_7Kq2", "") })
	mustPanic(t, "the label version is empty", func() { infoGauge.With("") })
}

func TestSeriesRefusedOutsideTests(t *testing.T) {
	withRegistry(t)
	strict = false
	t.Cleanup(func() { strict = true })
	requests := newCounter(Definition{Name: "muster_test_requests_total", Help: "Requests.", Capability: "C-05",
		Labels: []Label{closed("outcome", "the result", "accepted")}})
	requests.With("rejected").Inc()
	if got := exposition(t); got != "" {
		t.Errorf("a refused series was exported:\n%s", got)
	}
}

func TestHandler(t *testing.T) {
	status, ct, text := scrape(t, Handler(nil))
	if status != http.StatusOK || ct != contentType {
		t.Errorf("status %d, content type %q", status, ct)
	}
	buildInfo := fmt.Sprintf("muster_build_info{version=%q,commit=%q} 1\n", buildinfo.Version, buildinfo.Commit)
	for _, want := range []string{buildInfo, "\ngo_goroutines ", "\ngo_memstats_alloc_bytes ", "\nprocess_cpu_seconds_total "} {
		if !strings.Contains(text, want) {
			t.Errorf("the response lacks %q:\n%s", want, text)
		}
	}
}

func TestHandlerLeaderOnly(t *testing.T) {
	withRegistry(t)
	backlog := newGauge(Definition{Name: "muster_test_backlog", Help: "Backlog.", Capability: "C-06", LeaderOnly: true})
	backlog.With().Set(5)
	leading := false
	h := Handler(func() bool { return leading })
	for _, tt := range []struct {
		leading bool
		want    bool
	}{{false, false}, {true, true}} {
		leading = tt.leading
		if _, _, text := scrape(t, h); strings.Contains(text, "muster_test_backlog 5\n") != tt.want {
			t.Errorf("leading %v: muster_test_backlog written = %v, want %v", tt.leading, !tt.want, tt.want)
		}
	}
	if got := exposition(t); strings.Contains(got, "muster_test_backlog") {
		t.Errorf("a Leader-only series is in the set of every replica:\n%s", got)
	}
}

func TestHandlerEmptyBuildInfo(t *testing.T) {
	withRegistry(t)
	oldBuildInfo := BuildInfo
	t.Cleanup(func() { BuildInfo = oldBuildInfo })
	BuildInfo = newGauge(Definition{Name: "muster_build_info", Help: "Build.", Capability: "C-02",
		Labels: []Label{info("version", "v"), info("commit", "c")}})
	oldVersion, oldCommit := buildinfo.Version, buildinfo.Commit
	buildinfo.Version, buildinfo.Commit = "", ""
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = oldVersion, oldCommit })
	if _, _, text := scrape(t, Handler(nil)); !strings.Contains(text, `muster_build_info{version="unknown",commit="unknown"} 1`) {
		t.Errorf("an empty version or commit is not written as unknown:\n%s", text)
	}
}

// catalogueRowRe matches the rows of the metrics catalogue of reference.md, which refgen compares field by field.
var catalogueRowRe = regexp.MustCompile("(?m)^\\| `(muster_[a-z0-9_]+)` \\|")

// TestCatalogueConstructed covers C-19.FR-2: every metric of the catalogue of reference.md is declared in the registry,
// whose declarations are package variables and so are constructed when the server starts.
func TestCatalogueConstructed(t *testing.T) {
	data, err := os.ReadFile("../../design/prd/l1/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "## Metrics catalogue")
	end := strings.Index(text, "## Chart rules")
	if start < 0 || end < start {
		t.Fatal("the metrics catalogue section was not found")
	}
	defs, err := Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	rows := catalogueRowRe.FindAllStringSubmatch(text[start:end], -1)
	if len(rows) == 0 {
		t.Fatal("no catalogue rows")
	}
	for _, m := range rows {
		if !slices.Contains(names, m[1]) {
			t.Errorf("%s is in the catalogue but not constructed", m[1])
		}
	}
}

// The label policy of C-19.AC-3 and ADR-0014: the only label names a series may carry.
var (
	policyEntity = []string{"integration", "route", "destination"}
	policyClosed = []string{"client", "outcome", "state", "route_pattern", "method", "code", "kind", "reason", "status",
		"by", "command", "transport", "template", "table"}
	policyForbidden = []string{"alertname", "severity", "user", "number", "org_id", "organization"}
)

func allowedLabel(metric, label string) bool {
	switch {
	case slices.Contains(policyEntity, label), slices.Contains(policyClosed, label):
		return true
	case label == "name":
		return strings.HasSuffix(metric, "_info")
	case label == "version", label == "commit":
		return metric == "muster_build_info"
	}
	return false
}

// TestLabelPolicy covers C-19.AC-3: every label of the registry, and every label of a scrape with one series of each
// metric, is an entity id, a closed set, the name of an *_info gauge, the version and commit of the build, or le.
func TestLabelPolicy(t *testing.T) {
	for _, l := range policyForbidden {
		if allowedLabel("muster_x_info", l) {
			t.Fatalf("the policy allows %s", l)
		}
	}
	defs, err := Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		for _, l := range d.Labels {
			if !allowedLabel(d.Name, l.Name) {
				t.Errorf("%s has the label %s, which the policy does not allow", d.Name, l.Name)
			}
		}
	}

	oldSet, oldLeaderSet, oldDiscardSet := set, leaderSet, discardSet
	set, leaderSet, discardSet = vm.NewSet(), vm.NewSet(), vm.NewSet()
	t.Cleanup(func() { set, leaderSet, discardSet = oldSet, oldLeaderSet, oldDiscardSet })
	for _, d := range registry {
		values := make([]string, 0, len(d.Labels))
		for _, l := range d.Labels {
			switch l.Kind {
			case LabelClosed:
				values = append(values, l.Values[0])
			case LabelEntity:
				values = append(values, "NTAAAAAAAAAAAA")
			default:
				values = append(values, "x")
			}
		}
		switch d.Kind {
		case KindCounter:
			Counter{d}.With(values...).Inc()
		case KindGauge:
			Gauge{d}.With(values...).Set(1)
		case KindHistogram:
			Histogram{d}.With(values...).Update(1)
		}
	}
	_, _, text := scrape(t, Handler(func() bool { return true }))
	labelRe := regexp.MustCompile(`([a-z_]+)="`)
	seen := map[string]bool{}
	for line := range strings.SplitSeq(text, "\n") {
		if !strings.HasPrefix(line, "muster_") {
			continue
		}
		metric, _, _ := strings.Cut(line, "{")
		metric = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(metric, "_bucket"), "_sum"), "_count")
		for _, m := range labelRe.FindAllStringSubmatch(line, -1) {
			seen[m[1]] = true
			if m[1] != "le" && !allowedLabel(metric, m[1]) {
				t.Errorf("the scrape has the label %s on %s", m[1], line)
			}
		}
	}
	if !seen["le"] || !seen["destination"] || !seen["version"] {
		t.Errorf("the scrape lacks series: labels %v", seen)
	}
}
