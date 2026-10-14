// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	repoReference = "../../../design/prd/l1/reference.md"
	repoDashboard = "../../../deploy/helm/muster/files/dashboard.json"
	repoTests     = "../../../deploy/helm/muster/tests/rules"
	testBaseURL   = "https://docs.example.org/muster/"
	dashboardCM   = "m-muster-dashboard"
	successPrefix = "chart rules and dashboard match reference.md and the metric catalogue: 12 rules, "
)

// fixture is a rendered chart that passes every check until a test changes it.
type fixture struct {
	t                *testing.T
	version          string // app.kubernetes.io/version of the rule resources; "" leaves the label out
	segment          string // the version segment of the runbook_urls the fixture writes
	baseURL          string // MUSTER_RUNBOOK_BASE_URL; "" leaves it out
	group            string
	rules            []map[string]any
	vmRules          []map[string]any // nil: the rules of the PrometheusRule
	noPrometheusRule bool
	noVMRule         bool
	dashboard        map[string]any // nil: no dashboard ConfigMap
	dashboardRaw     string         // replaces the JSON of dashboard when set
	grafana          map[string]any // the spec of the GrafanaDashboard; nil: none
}

func newFixture(t *testing.T, version, segment string) *fixture {
	t.Helper()
	data, err := os.ReadFile(repoDashboard)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, version: version, segment: segment, baseURL: testBaseURL, group: "muster", dashboard: d,
		grafana: map[string]any{
			"instanceSelector": map[string]any{"matchLabels": map[string]any{"dashboards": "grafana"}},
			"configMapRef":     map[string]any{"name": dashboardCM, "key": "muster.json"},
		}}
	f.rules = f.referenceRules()
	return f
}

func (f *fixture) url(alert string) string {
	return strings.TrimSuffix(testBaseURL, "/") + "/" + f.segment + "/operations/runbooks/" + alert + "/"
}

// referenceRules are the rules of reference.md as the chart renders them, a long expression folded over two lines.
func (f *fixture) referenceRules() []map[string]any {
	f.t.Helper()
	ref, err := readReference(repoReference)
	if err != nil {
		f.t.Fatal(err)
	}
	var rules []map[string]any
	for _, r := range ref {
		rules = append(rules, map[string]any{
			"alert":  r.name,
			"expr":   strings.Replace(r.expr, " unless ", "\n  unless ", 1),
			"for":    r.forDuration,
			"labels": map[string]any{"severity": r.severity},
			"annotations": map[string]any{
				"summary":     "Summary of " + r.name,
				"description": "What fired and the first thing to check.",
				"runbook_url": f.url(r.name),
			},
		})
	}
	return rules
}

func (f *fixture) rule(alert string) map[string]any {
	f.t.Helper()
	for _, r := range f.rules {
		if r["alert"] == alert {
			return r
		}
	}
	f.t.Fatalf("no rule %s", alert)
	return nil
}

func (f *fixture) panels() []any {
	return f.dashboard["panels"].([]any)
}

func (f *fixture) panel(title string) map[string]any {
	f.t.Helper()
	for _, p := range f.panels() {
		if p := p.(map[string]any); p["title"] == title {
			return p
		}
	}
	f.t.Fatalf("no panel %q", title)
	return nil
}

func (f *fixture) target(title string) map[string]any {
	return f.panel(title)["targets"].([]any)[0].(map[string]any)
}

func (f *fixture) variables() map[string]any {
	return f.dashboard["templating"].(map[string]any)
}

func (f *fixture) variable(name string) map[string]any {
	f.t.Helper()
	for _, v := range f.variables()["list"].([]any) {
		if v := v.(map[string]any); v["name"] == name {
			return v
		}
	}
	f.t.Fatalf("no variable %s", name)
	return nil
}

func (f *fixture) write() string {
	f.t.Helper()
	ruleLabels := map[string]any{"app.kubernetes.io/name": "muster"}
	if f.version != "" {
		ruleLabels["app.kubernetes.io/version"] = f.version
	}
	ruleSpec := func(rules []map[string]any) map[string]any {
		return map[string]any{"groups": []any{map[string]any{"name": f.group, "rules": rules}}}
	}
	env := []any{map[string]any{"name": "MUSTER_LOG_LEVEL", "value": "info"}}
	if f.baseURL != "" {
		env = append(env, map[string]any{"name": "MUSTER_RUNBOOK_BASE_URL", "value": f.baseURL})
	}
	docs := []any{
		map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "m-muster"},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{"name": "muster", "env": env}}}}}},
		map[string]any{"kind": "ConfigMap", "metadata": map[string]any{"name": "m-muster-other"},
			"data": map[string]any{"other.json": "{}"}},
	}
	if !f.noPrometheusRule {
		docs = append(docs, map[string]any{"kind": "PrometheusRule",
			"metadata": map[string]any{"name": "m-muster", "labels": ruleLabels}, "spec": ruleSpec(f.rules)})
	}
	if !f.noVMRule {
		vm := f.vmRules
		if vm == nil {
			vm = f.rules
		}
		docs = append(docs, map[string]any{"kind": "VMRule",
			"metadata": map[string]any{"name": "m-muster", "labels": ruleLabels}, "spec": ruleSpec(vm)})
	}
	if f.dashboard != nil || f.dashboardRaw != "" {
		raw := f.dashboardRaw
		if raw == "" {
			data, err := json.Marshal(f.dashboard)
			if err != nil {
				f.t.Fatal(err)
			}
			raw = string(data)
		}
		docs = append(docs, map[string]any{"kind": "ConfigMap", "metadata": map[string]any{"name": dashboardCM},
			"data": map[string]any{"muster.json": raw}})
	}
	if f.grafana != nil {
		docs = append(docs, map[string]any{"kind": "GrafanaDashboard",
			"metadata": map[string]any{"name": "m-muster"}, "spec": f.grafana})
	}
	var b strings.Builder
	b.WriteString("---\n# Source: muster/templates/empty.yaml\n")
	for _, d := range docs {
		data, err := yaml.Marshal(d)
		if err != nil {
			f.t.Fatal(err)
		}
		b.WriteString("---\n")
		b.Write(data)
	}
	path := filepath.Join(f.t.TempDir(), "chart.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func runWith(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (f *fixture) check() (int, string) {
	f.t.Helper()
	code, stdout, stderr := runWith(f.t, "-manifest", f.write(), "-reference", repoReference)
	if stderr != "" {
		f.t.Errorf("stderr = %q", stderr)
	}
	return code, stdout
}

// matchLines compares output lines with want, where a line of want ending in * is a prefix.
func matchLines(got string, want []string) bool {
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != len(want) {
		return false
	}
	for i, w := range want {
		if p, ok := strings.CutSuffix(w, "*"); ok {
			if !strings.HasPrefix(lines[i], p) {
				return false
			}
		} else if lines[i] != w {
			return false
		}
	}
	return true
}

func TestRunPasses(t *testing.T) {
	tests := map[string]struct {
		version, segment string
		change           func(f *fixture)
	}{
		"development build": {version: "0.0.0-dev", segment: "latest"},
		"release candidate": {version: "v1.4.0-rc.1", segment: "1.4"},
		"release":           {version: "2.10.3", segment: "2.10"},
		"collapsed row": {version: "0.0.0-dev", segment: "latest", change: func(f *fixture) {
			panels := f.panels()
			i := slices.IndexFunc(panels, func(p any) bool { return p.(map[string]any)["title"] == "Platform" })
			row := panels[i].(map[string]any)
			row["collapsed"] = true
			row["panels"] = slices.Clone(panels[i+1:])
			f.dashboard["panels"] = panels[:i+1]
		}},
		"histogram series, up, process and go metrics": {version: "0.0.0-dev", segment: "latest",
			change: func(f *fixture) {
				f.target("Delivery queues")["expr"] = `histogram_quantile(0.9, sum by (le) ` +
					`(rate(muster_delivery_latency_seconds_bucket{job=~"$job"}[${__rate_interval}]))) + ` +
					`sum(rate(muster_delivery_latency_seconds_sum[$__range])) / ` +
					`sum(rate(muster_delivery_latency_seconds_count[$__interval])) + ` +
					`max(up) + max(process_resident_memory_bytes) + max(go_goroutines)`
			}},
		"variable query as an object": {version: "0.0.0-dev", segment: "latest", change: func(f *fixture) {
			v := f.variable("job")
			v["query"] = map[string]any{"query": v["query"], "refId": "PrometheusVariableQueryEditor-VariableQuery"}
		}},
		"label values without a metric": {version: "0.0.0-dev", segment: "latest", change: func(f *fixture) {
			f.variable("namespace")["query"] = "label_values(namespace)"
		}},
		"entity regexes with flags, another label and no slashes": {version: "0.0.0-dev", segment: "latest",
			change: func(f *fixture) {
				d := f.variable("destination")
				d["query"] = map[string]any{"query": `query_result(max by (name, job, destination) (muster_destination_info))`}
				d["regex"] = `/DESTINATION="(?<value>[^"]*)".*name="(?<text>[^"]*)"/gi`
				f.variable("integration")["regex"] = `.*integration="(?<value>[^"]*)".*name="(?<text>[^"]*)".*`
			}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, tc.version, tc.segment)
			if tc.change != nil {
				tc.change(f)
			}
			code, stdout := f.check()
			if code != 0 || !strings.HasPrefix(stdout, successPrefix) {
				t.Errorf("run = %d:\n%s", code, stdout)
			}
		})
	}
}

func TestRunReportsProblems(t *testing.T) {
	extraRule := func(f *fixture, expr string) {
		f.rules = append(f.rules, map[string]any{"alert": "MusterExtra", "expr": expr, "for": "1m",
			"labels": map[string]any{"severity": "warning"},
			"annotations": map[string]any{"summary": "s", "description": "d",
				"runbook_url": f.url("MusterExtra")}})
	}
	tests := map[string]struct {
		change func(f *fixture)
		want   []string
	}{
		"missing rule": {
			change: func(f *fixture) {
				f.rules = slices.DeleteFunc(f.rules, func(r map[string]any) bool { return r["alert"] == "MusterClockSkew" })
			},
			want: []string{"missing chart rule: MusterClockSkew"},
		},
		"extra rule": {
			change: func(f *fixture) { extraRule(f, `up{job="muster"} == 0`) },
			want:   []string{"extra chart rule: MusterExtra"},
		},
		"duplicate rule": {
			change: func(f *fixture) { f.rules = append(f.rules, f.rule("MusterDown")) },
			want:   []string{"duplicate chart rule: MusterDown"},
		},
		"severity": {
			change: func(f *fixture) { f.rule("MusterDown")["labels"] = map[string]any{"severity": "warning"} },
			want:   []string{"chart rule MusterDown: severity warning is not critical of reference.md"},
		},
		"extra rule without severity": {
			change: func(f *fixture) {
				extraRule(f, `up{job="muster"} == 0`)
				f.rule("MusterExtra")["labels"] = map[string]any{}
			},
			want: []string{"extra chart rule: MusterExtra", "chart rule MusterExtra: the label severity is empty"},
		},
		"for": {
			change: func(f *fixture) { f.rule("MusterNoLeader")["for"] = "3m" },
			want:   []string{"chart rule MusterNoLeader: for 3m is not 2m of reference.md"},
		},
		"threshold": {
			change: func(f *fixture) {
				r := f.rule("MusterClockSkew")
				r["expr"] = strings.Replace(r["expr"].(string), "> 2", "> 5", 1)
			},
			want: []string{`chart rule MusterClockSkew: expr max by (instance) (abs(muster_clock_skew_seconds{job="muster"})) > 5 ` +
				`is not the default expression of reference.md, max by (instance) (abs(muster_clock_skew_seconds{job="muster"})) > 2`},
		},
		"empty description": {
			change: func(f *fixture) { f.rule("MusterDown")["annotations"].(map[string]any)["description"] = " " },
			want:   []string{"chart rule MusterDown: the annotation description is empty"},
		},
		"runbook_url": {
			change: func(f *fixture) {
				f.rule("MusterNoLeader")["annotations"].(map[string]any)["runbook_url"] = f.url("MusterDown")
			},
			want: []string{"chart rule MusterNoLeader: runbook_url https://docs.example.org/muster/latest/operations/runbooks/MusterDown/ " +
				"is not https://docs.example.org/muster/latest/operations/runbooks/MusterNoLeader/"},
		},
		"rule group": {
			change: func(f *fixture) { f.group = "other" },
			want:   []string{"the rule groups are [other], not [muster]"},
		},
		"VMRule differs": {
			change: func(f *fixture) {
				f.vmRules = f.referenceRules()
				f.vmRules[0]["for"] = "1m"
			},
			want: []string{"the VMRule's spec differs from the PrometheusRule's"},
		},
		"no VMRule": {
			change: func(f *fixture) { f.noVMRule = true },
			want:   []string{"the chart renders 0 VMRule resources, not one"},
		},
		"no PrometheusRule": {
			change: func(f *fixture) { f.noPrometheusRule = true },
			want:   []string{"the chart renders 0 PrometheusRule resources, not one"},
		},
		"no base URL": {
			change: func(f *fixture) { f.baseURL = "" },
			want:   []string{"the Deployment does not set MUSTER_RUNBOOK_BASE_URL"},
		},
		"no version label": {
			change: func(f *fixture) { f.version = "" },
			want:   []string{"the PrometheusRule has no label app.kubernetes.io/version"},
		},
		"rule metric": {
			change: func(f *fixture) { extraRule(f, "muster_no_such_metric > 0") },
			want: []string{"extra chart rule: MusterExtra",
				"chart rule MusterExtra: metric muster_no_such_metric is not in the metric catalogue"},
		},
		"rule cannot be parsed": {
			change: func(f *fixture) { extraRule(f, "sum(muster_leader") },
			want:   []string{"extra chart rule: MusterExtra", "chart rule MusterExtra: the query cannot be parsed: *"},
		},
		"uid": {
			change: func(f *fixture) { f.dashboard["uid"] = "other" },
			want:   []string{`the dashboard's uid is "other", not muster`},
		},
		"title": {
			change: func(f *fixture) { f.dashboard["title"] = "Other" },
			want:   []string{`the dashboard's title is "Other", not Muster`},
		},
		"variables reordered": {
			change: func(f *fixture) {
				list := f.variables()["list"].([]any)
				list[1], list[2] = list[2], list[1]
			},
			want: []string{"the dashboard variables are [datasource job namespace integration route destination], " +
				"not [datasource namespace job integration route destination]"},
		},
		"variable missing": {
			change: func(f *fixture) {
				list := f.variables()["list"].([]any)
				f.variables()["list"] = list[:len(list)-1]
			},
			want: []string{"the dashboard variables are [datasource namespace job integration route], " +
				"not [datasource namespace job integration route destination]"},
		},
		"data source variable": {
			change: func(f *fixture) { f.variable("datasource")["query"] = "loki" },
			want: []string{
				`dashboard variable datasource: type datasource and query "loki" are not datasource and prometheus`},
		},
		"variable type": {
			change: func(f *fixture) { f.variable("route")["type"] = "custom" },
			want:   []string{"dashboard variable route: type custom is not query"},
		},
		"variable data source": {
			change: func(f *fixture) { f.variable("job")["datasource"] = map[string]any{"uid": "prom"} },
			want:   []string{`dashboard variable job: data source "prom" is not ${datasource}`},
		},
		"variable without query": {
			change: func(f *fixture) { f.variable("job")["query"] = "" },
			want:   []string{"dashboard variable job has no query"},
		},
		"variable metric": {
			change: func(f *fixture) {
				f.variable("integration")["query"] = "query_result(max by (integration, name) (muster_integrations_info))"
			},
			want: []string{"dashboard variable integration: metric muster_integrations_info is not in the metric catalogue"},
		},
		"entity regex in the order the labels are written": {
			change: func(f *fixture) { f.variable("route")["regex"] = `/route="(?<value>[^"]*)".*name="(?<text>[^"]*)"/` },
			want: []string{`dashboard variable route: regex /route="(?<value>[^"]*)".*name="(?<text>[^"]*)"/ does not take ` +
				`the value route_id and the text route name from the label-sorted series {name="route name",route="route_id"} 1 0`},
		},
		"entity regex with the groups swapped": {
			change: func(f *fixture) {
				f.variable("integration")["regex"] = `/integration="(?<text>[^"]*)".*name="(?<value>[^"]*)"/`
			},
			want: []string{`dashboard variable integration: regex /integration="(?<text>[^"]*)".*name="(?<value>[^"]*)"/ ` +
				`does not take the value integration_id and the text integration name from the label-sorted series ` +
				`{integration="integration_id",name="integration name"} 1 0`},
		},
		"entity variable without a regex": {
			change: func(f *fixture) { f.variable("destination")["regex"] = "" },
			want:   []string{"dashboard variable destination has no regex"},
		},
		"entity regex without groups": {
			change: func(f *fixture) { f.variable("destination")["regex"] = `/destination="([^"]*)"/` },
			want:   []string{`dashboard variable destination: regex /destination="([^"]*)"/ has no groups value and text`},
		},
		"entity regex cannot be compiled": {
			change: func(f *fixture) { f.variable("route")["regex"] = `/(?<value>[/` },
			want:   []string{`dashboard variable route: regex /(?<value>[/ cannot be compiled: *`},
		},
		"entity variable without the name": {
			change: func(f *fixture) { f.variable("route")["query"] = "query_result(max by (route) (muster_route_info))" },
			want: []string{"dashboard variable route: query query_result(max by (route) (muster_route_info)) " +
				"is not query_result(<aggregation> by (route, name) (...))"},
		},
		"entity variable by label values": {
			change: func(f *fixture) {
				f.variable("integration")["query"] = "label_values(muster_integration_info, integration)"
			},
			want: []string{"dashboard variable integration: query label_values(muster_integration_info, integration) " +
				"is not query_result(<aggregation> by (integration, name) (...))"},
		},
		"row missing": {
			change: func(f *fixture) {
				f.dashboard["panels"] = slices.DeleteFunc(f.panels(), func(p any) bool {
					return p.(map[string]any)["title"] == "Platform"
				})
			},
			want: []string{"the dashboard rows are [Overview Ingestion Delivery], not [Overview Ingestion Delivery Platform]"},
		},
		"empty row": {
			change: func(f *fixture) {
				panels := f.panels()
				i := slices.IndexFunc(panels, func(p any) bool { return p.(map[string]any)["title"] == "Platform" })
				f.dashboard["panels"] = panels[:i+1]
			},
			want: []string{`dashboard row "Platform" has no panel`},
		},
		"panel above the first row": {
			change: func(f *fixture) { f.dashboard["panels"] = append([]any{f.panel("Delivery queues")}, f.panels()...) },
			want:   []string{`dashboard panel "Delivery queues" is above the first row`},
		},
		"panel data source": {
			change: func(f *fixture) { f.panel("Delivery queues")["datasource"] = map[string]any{"uid": "prometheus"} },
			want:   []string{`dashboard panel "Delivery queues": data source "prometheus" is not ${datasource}`},
		},
		"query data source": {
			change: func(f *fixture) { f.target("Delivery queues")["datasource"] = map[string]any{"uid": "other"} },
			want:   []string{`dashboard panel "Delivery queues": query A: data source "other" is not ${datasource}`},
		},
		"panel without query": {
			change: func(f *fixture) { f.panel("Delivery queues")["targets"] = []any{} },
			want:   []string{`dashboard panel "Delivery queues" has no query`},
		},
		"unknown metric": {
			change: func(f *fixture) { f.target("Delivery queues")["expr"] = `max(muster_no_such_metric{job=~"$job"})` },
			want:   []string{`dashboard panel "Delivery queues": metric muster_no_such_metric is not in the metric catalogue`},
		},
		"bucket of a counter": {
			change: func(f *fixture) {
				f.target("Delivery queues")["expr"] = `sum(rate(muster_ingest_requests_total_bucket[$__rate_interval]))`
			},
			want: []string{`dashboard panel "Delivery queues": metric muster_ingest_requests_total_bucket ` +
				`is not in the metric catalogue`},
		},
		"bare histogram": {
			change: func(f *fixture) {
				f.target("API latency p95")["expr"] = `sum(rate(muster_delivery_latency_seconds[$__rate_interval]))`
			},
			want: []string{`dashboard panel "API latency p95": metric muster_delivery_latency_seconds is a histogram: ` +
				`query its _bucket, _sum or _count series`},
		},
		"query cannot be parsed": {
			change: func(f *fixture) { f.target("Delivery queues")["expr"] = `sum(rate(muster_leader[$foo]))` },
			want:   []string{`dashboard panel "Delivery queues": the query cannot be parsed: *`},
		},
		"no dashboard": {
			change: func(f *fixture) {
				f.dashboard = nil
				f.grafana = nil
			},
			want: []string{"the chart renders 0 ConfigMaps with muster.json, not one"},
		},
		"dashboard is not JSON": {
			change: func(f *fixture) { f.dashboardRaw = "{" },
			want:   []string{"the dashboard muster.json cannot be read: *"},
		},
		"GrafanaDashboard reference": {
			change: func(f *fixture) {
				f.grafana["configMapRef"] = map[string]any{"name": "other", "key": "muster.json"}
			},
			want: []string{"the GrafanaDashboard m-muster references other/muster.json, not m-muster-dashboard/muster.json"},
		},
		"GrafanaDashboard without instanceSelector": {
			change: func(f *fixture) { delete(f.grafana, "instanceSelector") },
			want:   []string{"the GrafanaDashboard m-muster has no instanceSelector"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "0.0.0-dev", "latest")
			tc.change(f)
			code, stdout := f.check()
			if code != 1 || !matchLines(stdout, tc.want) {
				t.Errorf("run = %d:\n%s\nwant:\n%s", code, stdout, strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestRunbookVersionOfTheAppVersion(t *testing.T) {
	tests := map[string]struct{ version, segment, want string }{
		"latest on a release":        {"v1.4.0-rc.1", "latest", "1.4"},
		"major.minor on a dev build": {"0.0.0-dev", "0.0", "latest"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, tc.version, tc.segment)
			code, stdout := f.check()
			first, _, _ := strings.Cut(stdout, "\n")
			want := "chart rule MusterDown: runbook_url https://docs.example.org/muster/" + tc.segment +
				"/operations/runbooks/MusterDown/ is not https://docs.example.org/muster/" + tc.want +
				"/operations/runbooks/MusterDown/"
			if code != 1 || first != want || strings.Count(stdout, "\n") != 12 {
				t.Errorf("run = %d:\n%s", code, stdout)
			}
		})
	}
}

func TestMetricProblem(t *testing.T) {
	kinds, err := metricKinds()
	if err != nil {
		t.Fatal(err)
	}
	c := &checker{kinds: kinds}
	tests := map[string]string{
		"muster_leader":                              "",
		"muster_ingest_requests_total":               "",
		"muster_delivery_latency_seconds_bucket":     "",
		"muster_delivery_latency_seconds_sum":        "",
		"muster_delivery_latency_seconds_count":      "",
		"up":                                         "",
		"process_resident_memory_bytes":              "",
		"go_goroutines":                              "",
		"muster_delivery_latency_seconds":            "metric muster_delivery_latency_seconds is a histogram: query its _bucket, _sum or _count series",
		"muster_ingest_requests_total_bucket":        "metric muster_ingest_requests_total_bucket is not in the metric catalogue",
		"muster_leader_count":                        "metric muster_leader_count is not in the metric catalogue",
		"muster_no_such_metric":                      "metric muster_no_such_metric is not in the metric catalogue",
		"node_cpu_seconds_total":                     "metric node_cpu_seconds_total is not in the metric catalogue",
		"upstream":                                   "metric upstream is not in the metric catalogue",
		"muster_no_such_histogram_seconds_bucket":    "metric muster_no_such_histogram_seconds_bucket is not in the metric catalogue",
		"muster_alert_group_time_to_ack_seconds_sum": "",
	}
	for name, want := range tests {
		if got := c.metricProblem(name); got != want {
			t.Errorf("metricProblem(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestMetricQuery(t *testing.T) {
	tests := map[string]struct {
		want string
		ok   bool
	}{
		`label_values(muster_build_info{namespace=~"$namespace"}, job)`:     {`muster_build_info{namespace=~"$namespace"}`, true},
		` label_values(muster_build_info, namespace) `:                      {"muster_build_info", true},
		"label_values(namespace)":                                           {"", false},
		"query_result(max by (route, name) (muster_route_info))":            {"max by (route, name) (muster_route_info)", true},
		"max(muster_leader)":                                                {"max(muster_leader)", true},
		"query_result(sum(muster_leader)\n  by (instance))":                 {"sum(muster_leader)\n  by (instance)", true},
		`label_values(muster_route_info{route=~"a,b"}, name)`:               {`muster_route_info{route=~"a,b"}`, true},
		`label_values(sum by (job) (muster_leader{job=~"$job"}), instance)`: {`sum by (job) (muster_leader{job=~"$job"})`, true},
	}
	for q, tc := range tests {
		if got, ok := metricQuery(q); got != tc.want || ok != tc.ok {
			t.Errorf("metricQuery(%q) = %q, %v, want %q, %v", q, got, ok, tc.want, tc.ok)
		}
	}
	for raw, want := range map[string]string{`"up"`: "up", `{"query": "up", "refId": "A"}`: "up", `42`: "", `null`: ""} {
		if got := variableQuery(json.RawMessage(raw)); got != want {
			t.Errorf("variableQuery(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestReadReference(t *testing.T) {
	write := func(content string) string {
		path := filepath.Join(t.TempDir(), "reference.md")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	got, err := readReference(write("## Chart rules\n\n| Rule | Severity | Default expression | For | Meaning |\n" +
		"|---|---|---|---|---|\n| `MusterA` | critical | `max(a)  ==\t0` | 2m | x \\| y |\n"))
	want := []referenceRule{{name: "MusterA", severity: "critical", expr: "max(a) == 0", forDuration: "2m"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("readReference = %+v, %v", got, err)
	}
	errs := map[string]string{
		"fewer than four columns": "## Chart rules\n\n| Rule | Severity |\n|---|---|\n| `MusterA` | critical |\n",
		"no rules under":          "## Chart rules\n\n| Rule | Severity | Expression | For |\n|---|---|---|---|\n",
		"no table under":          "## Metrics\n",
	}
	for msg, content := range errs {
		if _, err := readReference(write(content)); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("readReference(%q) = %v, want an error with %q", content, err, msg)
		}
	}
	if _, err := readReference(filepath.Join(t.TempDir(), "none.md")); err == nil {
		t.Error("readReference of a missing file succeeded")
	}
	ref, err := readReference(repoReference)
	if err != nil || len(ref) != 12 {
		t.Errorf("readReference(%s) = %d rules, %v", repoReference, len(ref), err)
	}
}

func TestExtract(t *testing.T) {
	f := newFixture(t, "0.0.0-dev", "latest")
	manifest := f.write()
	out := filepath.Join(t.TempDir(), "rules.yaml")
	code, stdout, stderr := runWith(t, "-manifest", manifest, "-extract-rules", out, "-tests", repoTests)
	if code != 0 || stdout != "chart rules extracted: 12, each with its unit test\n" {
		t.Fatalf("run = %d: %s%s", code, stdout, stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var spec ruleSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	rules := spec.rules()
	if len(spec.Groups) != 1 || spec.Groups[0].Name != "muster" || len(rules) != 12 {
		t.Fatalf("extracted %d groups and %d rules:\n%s", len(spec.Groups), len(rules), data)
	}
	if r := rules[1]; r.Alert != "MusterNoLeader" || r.For != "2m" || r.Labels["severity"] != "critical" ||
		r.Annotations["runbook_url"] != f.url("MusterNoLeader") || r.Annotations["description"] == "" {
		t.Errorf("extracted rule = %+v", r)
	}

	if code, stdout, _ := runWith(t, "-manifest", manifest, "-extract-rules", out); code != 0 ||
		stdout != "chart rules extracted: 12\n" {
		t.Errorf("without -tests: run = %d: %s", code, stdout)
	}
}

func TestExtractComparesTests(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(repoTests)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "MusterDown.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"MusterOther.yaml", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "fixtures.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := newFixture(t, "0.0.0-dev", "latest").write()
	out := filepath.Join(t.TempDir(), "rules.yaml")
	code, stdout, _ := runWith(t, "-manifest", manifest, "-extract-rules", out, "-tests", dir)
	want := []string{"chart rule MusterDown has no unit test " + filepath.Join(dir, "MusterDown") + ".yaml",
		"unit test " + filepath.Join(dir, "MusterOther") + ".yaml names no chart rule"}
	if code != 1 || !matchLines(stdout, want) {
		t.Errorf("run = %d:\n%s", code, stdout)
	}
	if code, _, stderr := runWith(t, "-manifest", manifest, "-extract-rules", out,
		"-tests", filepath.Join(dir, "none")); code != 1 || stderr == "" {
		t.Errorf("missing tests directory: run = %d: %s", code, stderr)
	}
}

func TestExtractErrors(t *testing.T) {
	f := newFixture(t, "0.0.0-dev", "latest")
	f.noPrometheusRule = true
	code, _, stderr := runWith(t, "-manifest", f.write(), "-extract-rules", filepath.Join(t.TempDir(), "rules.yaml"))
	if code != 1 || !strings.Contains(stderr, "has 0 PrometheusRule resources, not one") {
		t.Errorf("no PrometheusRule: run = %d: %s", code, stderr)
	}
	manifest := newFixture(t, "0.0.0-dev", "latest").write()
	code, _, stderr = runWith(t, "-manifest", manifest, "-extract-rules", filepath.Join(t.TempDir(), "none", "rules.yaml"))
	if code != 1 || stderr == "" {
		t.Errorf("unwritable output: run = %d: %s", code, stderr)
	}
}

func TestRunErrors(t *testing.T) {
	manifest := newFixture(t, "0.0.0-dev", "latest").write()
	invalid := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(invalid, []byte("kind: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	badKind := filepath.Join(t.TempDir(), "bad-kind.yaml")
	if err := os.WriteFile(badKind, []byte("kind: {a: b}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		args []string
		code int
	}{
		"no flags":                {nil, 2},
		"unknown flag":            {[]string{"-unknown"}, 2},
		"an argument":             {[]string{"-manifest", manifest, "extra"}, 2},
		"tests without extract":   {[]string{"-manifest", manifest, "-tests", repoTests}, 2},
		"missing manifest":        {[]string{"-manifest", filepath.Join(t.TempDir(), "none.yaml")}, 1},
		"invalid manifest":        {[]string{"-manifest", invalid}, 1},
		"manifest of other shape": {[]string{"-manifest", badKind}, 1},
		"missing reference": {[]string{"-manifest", manifest, "-reference",
			filepath.Join(t.TempDir(), "none.md")}, 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if code, stdout, stderr := runWith(t, tc.args...); code != tc.code || stderr == "" {
				t.Errorf("run = %d, want %d: %s%s", code, tc.code, stdout, stderr)
			}
		})
	}
}
