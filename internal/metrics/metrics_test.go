// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package metrics

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	errorsTotal := newCounter(Definition{Name: "muster_test_errors_total", Help: "Errors.", Capability: "C-12",
		Labels: []Label{entity("route"), entity("destination")}})
	infoGauge := newGauge(Definition{Name: "muster_test_info", Help: "Info.", Capability: "C-02",
		Labels: []Label{info("version", "the version")}})

	requests.With("int_7Kq2", "accepted").Add(2)
	latency.With().Update(0.7)
	queue.With().Set(3)
	pool.Func(func() float64 { return 7 })
	wait.Float().Add(1.5)
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
	if err != nil || len(defs) != 7 || defs[0].Name != "muster_test_errors_total" {
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
