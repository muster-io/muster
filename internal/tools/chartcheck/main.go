// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command chartcheck checks the chart rendered with the all-options values, which enable the PrometheusRule, the
// VMRule and both deliveries of the dashboard and keep the default rule values (C-19.FR-3, C-19.FR-4, C-19.FR-9):
//
//   - the PrometheusRule and the VMRule have the same spec: one group muster with the rules of the chart rules table of
//     reference.md, each with its severity, for and default expression, a summary, a description and the runbook_url
//     that the Deployment's MUSTER_RUNBOOK_BASE_URL and the appVersion give, as internalalerts.RunbookVersion does;
//   - the dashboard ConfigMap holds muster.json with the uid muster, the title Muster, the variables datasource,
//     namespace, job, integration, route and destination, and the rows Overview, Ingestion, Delivery and Platform,
//     each with panels whose queries use the data source variable;
//   - the regex of each entity variable takes the id as the value and the name as the text from the series of its
//     query_result as Grafana prints them, with the labels in the sorted order Prometheus returns them in;
//   - every metric of a rule expression, a panel query or a variable query is in the metric catalogue, a histogram
//     through its _bucket, _sum or _count series; up, process_* and go_* need no entry;
//   - a GrafanaDashboard references the dashboard ConfigMap and has an instanceSelector.
//
// It prints one line per problem and exits with 1; make helm-check runs it. With -extract-rules it checks nothing and
// writes the spec of the PrometheusRule as a Prometheus rule file, which make rules-test passes to promtool; with
// -tests every rule must have its unit test <dir>/<alertname>.yaml and every unit test a rule.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/VictoriaMetrics/metricsql"
	"go.yaml.in/yaml/v3"

	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/metrics"
)

const (
	chartRulesHeading = "## Chart rules"
	ruleGroup         = "muster"
	baseURLEnv        = "MUSTER_RUNBOOK_BASE_URL"
	versionLabel      = "app.kubernetes.io/version"
	runbookDir        = "/operations/runbooks/"
	dashboardKey      = "muster.json"
	datasourceUID     = "${datasource}"
)

var (
	// The variables and rows of the dashboard, in order (C-19.FR-4).
	dashboardVariables = []string{"datasource", "namespace", "job", "integration", "route", "destination"}
	dashboardRows      = []string{"Overview", "Ingestion", "Delivery", "Platform"}
	// The variables that list the entities of their *_info series: the id is the value, the name the text.
	entityVariables = []string{"integration", "route", "destination"}

	// Grafana's interval macros stand for a range; any range parses.
	macroRe       = regexp.MustCompile(`\$\{?__(rate_interval|interval|range)\}?`)
	labelValuesRe = regexp.MustCompile(`(?s)^label_values\((.*),\s*[A-Za-z_][A-Za-z0-9_]*\s*\)$`)
	labelOnlyRe   = regexp.MustCompile(`^label_values\(\s*[A-Za-z_][A-Za-z0-9_]*\s*\)$`)
	queryResultRe = regexp.MustCompile(`(?s)^query_result\((.*)\)$`)
	byLabelsRe    = regexp.MustCompile(`^[A-Za-z_]+\s+by\s*\(([^)]*)\)`)
	jsRegexRe     = regexp.MustCompile(`(?s)^/(.*)/([gimsuy]*)$`)
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("chartcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifest := flags.String("manifest", "", "the rendered chart (required)")
	reference := flags.String("reference", "design/prd/l1/reference.md",
		"the PRD page whose Chart rules table lists the chart rules")
	extractTo := flags.String("extract-rules", "",
		"write the rule groups of the PrometheusRule to this file for promtool, and check nothing else")
	tests := flags.String("tests", "",
		"with -extract-rules: the directory of the rule unit tests, one <alertname>.yaml per rule")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	switch {
	case *manifest == "" || flags.NArg() > 0:
		fmt.Fprintln(stderr, "chartcheck: -manifest names the rendered chart, and nothing else is an argument")
		return 2
	case *tests != "" && *extractTo == "":
		fmt.Fprintln(stderr, "chartcheck: -tests needs -extract-rules")
		return 2
	}
	objs, err := readManifest(*manifest)
	if err != nil {
		fmt.Fprintf(stderr, "chartcheck: %v\n", err)
		return 1
	}
	if *extractTo != "" {
		return extract(objs, *manifest, *extractTo, *tests, stdout, stderr)
	}
	ref, err := readReference(*reference)
	if err != nil {
		fmt.Fprintf(stderr, "chartcheck: %v\n", err)
		return 1
	}
	kinds, err := metricKinds()
	if err != nil {
		fmt.Fprintf(stderr, "chartcheck: %v\n", err)
		return 1
	}
	c := &checker{kinds: kinds}
	rules := c.checkRules(objs, ref)
	c.checkDashboard(objs)
	if len(c.problems) > 0 {
		for _, p := range c.problems {
			fmt.Fprintln(stdout, p)
		}
		return 1
	}
	fmt.Fprintf(stdout, "chart rules and dashboard match reference.md and the metric catalogue: %d rules, %d dashboard queries\n",
		rules, c.queries)
	return 0
}

// object is one resource of the rendered chart, with the parts the checks read.
type object struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec yaml.Node `yaml:"spec"`
	Data yaml.Node `yaml:"data"`
}

func readManifest(path string) ([]object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []object
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var o object
		if err := doc.Decode(&o); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if o.Kind != "" {
			out = append(out, o)
		}
	}
}

func ofKind(objs []object, kind string) []object {
	var out []object
	for _, o := range objs {
		if o.Kind == kind {
			out = append(out, o)
		}
	}
	return out
}

type ruleSpec struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []rule `yaml:"rules"`
	} `yaml:"groups"`
}

type rule struct {
	Alert       string            `yaml:"alert"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

func (s ruleSpec) rules() []rule {
	var out []rule
	for _, g := range s.Groups {
		out = append(out, g.Rules...)
	}
	return out
}

// extract writes the spec of the one PrometheusRule to out, as the rule file of promtool, and compares its rule names
// with the unit tests of testDir when it is set.
func extract(objs []object, manifest, out, testDir string, stdout, stderr io.Writer) int {
	prom := ofKind(objs, "PrometheusRule")
	if len(prom) != 1 {
		fmt.Fprintf(stderr, "chartcheck: %s has %d PrometheusRule resources, not one\n", manifest, len(prom))
		return 1
	}
	var spec ruleSpec
	if err := prom[0].Spec.Decode(&spec); err != nil {
		fmt.Fprintf(stderr, "chartcheck: the spec of the PrometheusRule: %v\n", err)
		return 1
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&prom[0].Spec); err != nil {
		fmt.Fprintf(stderr, "chartcheck: %v\n", err)
		return 1
	}
	if err := enc.Close(); err != nil {
		fmt.Fprintf(stderr, "chartcheck: %v\n", err)
		return 1
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o600); err != nil {
		fmt.Fprintf(stderr, "chartcheck: %v\n", err)
		return 1
	}
	var names []string
	for _, r := range spec.rules() {
		names = append(names, r.Alert)
	}
	if testDir == "" {
		fmt.Fprintf(stdout, "chart rules extracted: %d\n", len(names))
		return 0
	}
	problems, err := checkTests(names, testDir)
	if err != nil {
		fmt.Fprintf(stderr, "chartcheck: %v\n", err)
		return 1
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stdout, p)
		}
		return 1
	}
	fmt.Fprintf(stdout, "chart rules extracted: %d, each with its unit test\n", len(names))
	return 0
}

// checkTests compares the rule names with the unit tests <dir>/<alertname>.yaml: the rules without a test, then the
// tests without a rule, each sorted.
func checkTests(names []string, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	tests := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".yaml" {
			tests[strings.TrimSuffix(e.Name(), ".yaml")] = true
		}
	}
	rules := map[string]bool{}
	for _, n := range names {
		rules[n] = true
	}
	var problems []string
	for _, n := range slices.Sorted(maps.Keys(rules)) {
		if !tests[n] {
			problems = append(problems, fmt.Sprintf("chart rule %s has no unit test %s.yaml", n, filepath.Join(dir, n)))
		}
	}
	for _, n := range slices.Sorted(maps.Keys(tests)) {
		if !rules[n] {
			problems = append(problems, fmt.Sprintf("unit test %s.yaml names no chart rule", filepath.Join(dir, n)))
		}
	}
	return problems, nil
}

// referenceRule is a row of the chart rules table of reference.md.
type referenceRule struct {
	name, severity, expr, forDuration string
}

func readReference(path string) ([]referenceRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rows, err := tableRows(bytes.NewReader(data), chartRulesHeading)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []referenceRule
	for _, cells := range rows {
		if len(cells) < 4 {
			return nil, fmt.Errorf("%s: the chart rule row %q has fewer than four columns", path, strings.Join(cells, " | "))
		}
		out = append(out, referenceRule{
			name:        strings.Trim(cells[0], "`"),
			severity:    strings.Trim(cells[1], "`"),
			expr:        normalize(strings.Trim(cells[2], "`")),
			forDuration: strings.Trim(cells[3], "`"),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no rules under %q", path, chartRulesHeading)
	}
	return out, nil
}

func normalize(expr string) string {
	return strings.Join(strings.Fields(expr), " ")
}

func metricKinds() (map[string]metrics.Kind, error) {
	defs, err := metrics.Catalogue()
	if err != nil {
		return nil, fmt.Errorf("invalid metric registry: %w", err)
	}
	kinds := make(map[string]metrics.Kind, len(defs))
	for _, d := range defs {
		kinds[d.Name] = d.Kind
	}
	return kinds, nil
}

type checker struct {
	kinds    map[string]metrics.Kind
	problems []string
	queries  int // of the dashboard
}

func (c *checker) add(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

// checkRules checks the two rule resources against each other and the rules against reference.md, and returns the
// number of rules.
func (c *checker) checkRules(objs []object, ref []referenceRule) int {
	prom, vm := ofKind(objs, "PrometheusRule"), ofKind(objs, "VMRule")
	if len(prom) != 1 {
		c.add("the chart renders %d PrometheusRule resources, not one", len(prom))
	}
	if len(vm) != 1 {
		c.add("the chart renders %d VMRule resources, not one", len(vm))
	}
	var source object
	switch {
	case len(prom) > 0:
		source = prom[0]
	case len(vm) > 0:
		source = vm[0]
	default:
		return 0
	}
	if len(prom) == 1 && len(vm) == 1 {
		var a, b any
		errA, errB := prom[0].Spec.Decode(&a), vm[0].Spec.Decode(&b)
		if errA != nil || errB != nil || !reflect.DeepEqual(a, b) {
			c.add("the VMRule's spec differs from the PrometheusRule's")
		}
	}
	var spec ruleSpec
	if err := source.Spec.Decode(&spec); err != nil {
		c.add("the spec of the %s cannot be read: %v", source.Kind, err)
		return 0
	}
	var groups []string
	for _, g := range spec.Groups {
		groups = append(groups, g.Name)
	}
	if !slices.Equal(groups, []string{ruleGroup}) {
		c.add("the rule groups are [%s], not [%s]", strings.Join(groups, " "), ruleGroup)
	}
	rules := spec.rules()
	c.checkNames(rules, ref)

	want := map[string]referenceRule{}
	for _, r := range ref {
		want[r.name] = r
	}
	runbooks := c.runbookBase(objs, source)
	for _, r := range rules {
		c.checkRule(r, want, runbooks)
	}
	for _, r := range rules {
		c.checkQuery("chart rule "+r.Alert, r.Expr)
	}
	return len(rules)
}

func (c *checker) checkNames(rules []rule, ref []referenceRule) {
	count := map[string]int{}
	for _, r := range rules {
		count[r.Alert]++
	}
	want := map[string]bool{}
	for _, r := range ref {
		want[r.name] = true
	}
	for _, n := range slices.Sorted(maps.Keys(want)) {
		if count[n] == 0 {
			c.add("missing chart rule: %s", n)
		}
	}
	var extra, duplicate []string
	for _, r := range rules {
		if !want[r.Alert] && !slices.Contains(extra, r.Alert) {
			extra = append(extra, r.Alert)
		}
		if count[r.Alert] > 1 && !slices.Contains(duplicate, r.Alert) {
			duplicate = append(duplicate, r.Alert)
		}
	}
	slices.Sort(extra)
	slices.Sort(duplicate)
	for _, n := range extra {
		c.add("extra chart rule: %s", n)
	}
	for _, n := range duplicate {
		c.add("duplicate chart rule: %s", n)
	}
}

// runbookBase is the base of every runbook_url: the Deployment's MUSTER_RUNBOOK_BASE_URL, the documentation version
// of the appVersion and the runbooks directory. It is "" after a problem.
func (c *checker) runbookBase(objs []object, source object) string {
	base := ""
	for _, d := range ofKind(objs, "Deployment") {
		var spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []struct {
							Name  string `yaml:"name"`
							Value string `yaml:"value"`
						} `yaml:"env"`
					} `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		}
		if err := d.Spec.Decode(&spec); err != nil || len(spec.Template.Spec.Containers) == 0 {
			continue
		}
		for _, e := range spec.Template.Spec.Containers[0].Env {
			if e.Name == baseURLEnv {
				base = e.Value
			}
		}
	}
	version, hasVersion := source.Metadata.Labels[versionLabel]
	switch {
	case base == "":
		c.add("the Deployment does not set %s", baseURLEnv)
		return ""
	case !hasVersion:
		c.add("the %s has no label %s", source.Kind, versionLabel)
		return ""
	}
	return strings.TrimSuffix(base, "/") + "/" + internalalerts.RunbookVersion(version) + runbookDir
}

func (c *checker) checkRule(r rule, want map[string]referenceRule, runbooks string) {
	name := "chart rule " + r.Alert
	if ref, ok := want[r.Alert]; ok {
		if got := r.Labels["severity"]; got != ref.severity {
			c.add("%s: severity %s is not %s of reference.md", name, got, ref.severity)
		}
		if r.For != ref.forDuration {
			c.add("%s: for %s is not %s of reference.md", name, r.For, ref.forDuration)
		}
		if got := normalize(r.Expr); got != ref.expr {
			c.add("%s: expr %s is not the default expression of reference.md, %s", name, got, ref.expr)
		}
	} else if r.Labels["severity"] == "" {
		c.add("%s: the label severity is empty", name)
	}
	for _, a := range []string{"summary", "description"} {
		if strings.TrimSpace(r.Annotations[a]) == "" {
			c.add("%s: the annotation %s is empty", name, a)
		}
	}
	if runbooks == "" {
		return
	}
	if got, url := r.Annotations["runbook_url"], runbooks+r.Alert+"/"; got != url {
		c.add("%s: runbook_url %s is not %s", name, got, url)
	}
}

// checkQuery checks the metric names of one PromQL query of owner.
func (c *checker) checkQuery(owner, query string) {
	expr, err := metricsql.Parse(macroRe.ReplaceAllString(query, "5m"))
	if err != nil {
		c.add("%s: the query cannot be parsed: %v", owner, err)
		return
	}
	names := map[string]bool{}
	metricsql.VisitAll(expr, func(e metricsql.Expr) {
		me, ok := e.(*metricsql.MetricExpr)
		if !ok {
			return
		}
		for _, filters := range me.LabelFilterss {
			for _, f := range filters {
				if f.Label == "__name__" {
					names[f.Value] = true
				}
			}
		}
	})
	for _, n := range slices.Sorted(maps.Keys(names)) {
		if problem := c.metricProblem(n); problem != "" {
			c.add("%s: %s", owner, problem)
		}
	}
}

// metricProblem says why a query may not use the metric name, or returns "".
func (c *checker) metricProblem(name string) string {
	if name == "up" || strings.HasPrefix(name, "process_") || strings.HasPrefix(name, "go_") {
		return ""
	}
	if kind, ok := c.kinds[name]; ok {
		if kind == metrics.KindHistogram {
			return fmt.Sprintf("metric %s is a histogram: query its _bucket, _sum or _count series", name)
		}
		return ""
	}
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		if base, ok := strings.CutSuffix(name, suffix); ok && c.kinds[base] == metrics.KindHistogram {
			return ""
		}
	}
	return fmt.Sprintf("metric %s is not in the metric catalogue", name)
}

type dashboard struct {
	UID        string `json:"uid"`
	Title      string `json:"title"`
	Templating struct {
		List []variable `json:"list"`
	} `json:"templating"`
	Panels []panel `json:"panels"`
}

type datasource struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}

type variable struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Query      json.RawMessage `json:"query"`
	Regex      string          `json:"regex"`
	Datasource *datasource     `json:"datasource"`
}

type panel struct {
	Type       string      `json:"type"`
	Title      string      `json:"title"`
	Datasource *datasource `json:"datasource"`
	Targets    []target    `json:"targets"`
	Panels     []panel     `json:"panels"`
}

type target struct {
	RefID      string      `json:"refId"`
	Expr       string      `json:"expr"`
	Datasource *datasource `json:"datasource"`
}

func (d *datasource) uid() string {
	if d == nil {
		return ""
	}
	return d.UID
}

// query is a PromQL query of the dashboard and what it belongs to.
type query struct {
	owner, expr string
}

func (c *checker) checkDashboard(objs []object) {
	var configMap string
	var raw []string
	for _, o := range ofKind(objs, "ConfigMap") {
		var data map[string]string
		if err := o.Data.Decode(&data); err != nil {
			continue
		}
		if d, ok := data[dashboardKey]; ok {
			configMap = o.Metadata.Name
			raw = append(raw, d)
		}
	}
	if len(raw) != 1 {
		c.add("the chart renders %d ConfigMaps with %s, not one", len(raw), dashboardKey)
		if len(raw) == 0 {
			return
		}
	}
	var d dashboard
	if err := json.Unmarshal([]byte(raw[0]), &d); err != nil {
		c.add("the dashboard %s cannot be read: %v", dashboardKey, err)
		return
	}
	if d.UID != "muster" {
		c.add("the dashboard's uid is %q, not muster", d.UID)
	}
	if d.Title != "Muster" {
		c.add("the dashboard's title is %q, not Muster", d.Title)
	}
	queries := c.checkVariables(d.Templating.List)
	queries = append(queries, c.checkPanels(d.Panels)...)
	c.queries = len(queries)
	for _, q := range queries {
		c.checkQuery(q.owner, q.expr)
	}
	c.checkGrafanaDashboards(objs, configMap)
}

func (c *checker) checkVariables(list []variable) []query {
	var names []string
	for _, v := range list {
		names = append(names, v.Name)
	}
	if !slices.Equal(names, dashboardVariables) {
		c.add("the dashboard variables are [%s], not [%s]", strings.Join(names, " "), strings.Join(dashboardVariables, " "))
	}
	var queries []query
	for _, v := range list {
		owner := "dashboard variable " + v.Name
		q := variableQuery(v.Query)
		if v.Name == "datasource" {
			if v.Type != "datasource" || q != "prometheus" {
				c.add("%s: type %s and query %q are not datasource and prometheus", owner, v.Type, q)
			}
			continue
		}
		if slices.Contains(dashboardVariables, v.Name) && v.Type != "query" {
			c.add("%s: type %s is not query", owner, v.Type)
		}
		if v.Type != "query" {
			continue
		}
		if got := v.Datasource.uid(); got != datasourceUID {
			c.add("%s: data source %q is not %s", owner, got, datasourceUID)
		}
		if strings.TrimSpace(q) == "" {
			c.add("%s has no query", owner)
			continue
		}
		if expr, ok := metricQuery(q); ok {
			queries = append(queries, query{owner, expr})
		}
		if slices.Contains(entityVariables, v.Name) {
			c.checkEntityRegex(owner, v.Name, q, v.Regex)
		}
	}
	return queries
}

// checkEntityRegex applies the regex of the entity variable to a series of its query_result(<aggregation> by
// (<labels>) (...)) as Grafana prints it, {<label>="<value>",...} <value> <timestamp>, with the labels sorted as
// Prometheus returns them; the regex must take the id as its group value and the name as its group text.
func (c *checker) checkEntityRegex(owner, entity, q, regex string) {
	m := queryResultRe.FindStringSubmatch(strings.TrimSpace(q))
	var by []string
	if m != nil {
		if b := byLabelsRe.FindStringSubmatch(strings.TrimSpace(m[1])); b != nil {
			for _, l := range strings.Split(b[1], ",") {
				by = append(by, strings.TrimSpace(l))
			}
		}
	}
	if !slices.Contains(by, entity) || !slices.Contains(by, "name") {
		c.add("%s: query %s is not query_result(<aggregation> by (%s, name) (...))", owner, q, entity)
		return
	}
	if strings.TrimSpace(regex) == "" {
		c.add("%s has no regex", owner)
		return
	}
	id, name := entity+"_id", entity+" name"
	slices.Sort(by)
	pairs := make([]string, 0, len(by))
	for _, l := range by {
		value := "x"
		switch l {
		case entity:
			value = id
		case "name":
			value = name
		}
		pairs = append(pairs, fmt.Sprintf("%s=%q", l, value))
	}
	series := "{" + strings.Join(pairs, ",") + "} 1 0"
	re, err := jsRegex(regex)
	if err != nil {
		c.add("%s: regex %s cannot be compiled: %v", owner, regex, err)
		return
	}
	vi, ti := re.SubexpIndex("value"), re.SubexpIndex("text")
	if vi < 0 || ti < 0 {
		c.add("%s: regex %s has no groups value and text", owner, regex)
		return
	}
	if g := re.FindStringSubmatch(series); g == nil || g[vi] != id || g[ti] != name {
		c.add("%s: regex %s does not take the value %s and the text %s from the label-sorted series %s",
			owner, regex, id, name, series)
	}
}

// jsRegex compiles a regex of a Grafana variable: /<pattern>/<flags> or, without the slashes, a pattern that must
// match the whole text. RE2 reads JavaScript's named groups (?<name>...).
func jsRegex(regex string) (*regexp.Regexp, error) {
	m := jsRegexRe.FindStringSubmatch(regex)
	if m == nil {
		return regexp.Compile("^(?:" + regex + ")$")
	}
	var flags string
	for _, f := range m[2] {
		if strings.ContainsRune("ims", f) {
			flags += string(f)
		}
	}
	if flags != "" {
		return regexp.Compile("(?" + flags + ")" + m[1])
	}
	return regexp.Compile(m[1])
}

// variableQuery is the query of a variable: a string, or an object with a query.
func variableQuery(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Query string `json:"query"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.Query
	}
	return ""
}

// metricQuery is the PromQL expression of a variable query; label_values(<label>) has none.
func metricQuery(q string) (string, bool) {
	q = strings.TrimSpace(q)
	if m := queryResultRe.FindStringSubmatch(q); m != nil {
		return m[1], true
	}
	if labelOnlyRe.MatchString(q) {
		return "", false
	}
	if m := labelValuesRe.FindStringSubmatch(q); m != nil {
		return m[1], true
	}
	return q, true
}

// checkPanels checks the rows and the panels, the panels of a collapsed row included, and returns their queries.
func (c *checker) checkPanels(panels []panel) []query {
	type row struct {
		title  string
		panels int
	}
	var rows []row
	var queries []query
	for _, p := range panels {
		if p.Type == "row" {
			rows = append(rows, row{title: p.Title, panels: len(p.Panels)})
			for _, nested := range p.Panels {
				queries = append(queries, c.checkPanel(nested)...)
			}
			continue
		}
		if len(rows) == 0 {
			c.add("dashboard panel %q is above the first row", p.Title)
		} else {
			rows[len(rows)-1].panels++
		}
		queries = append(queries, c.checkPanel(p)...)
	}
	titles := make([]string, 0, len(rows))
	for _, r := range rows {
		titles = append(titles, r.title)
	}
	if !slices.Equal(titles, dashboardRows) {
		c.add("the dashboard rows are [%s], not [%s]", strings.Join(titles, " "), strings.Join(dashboardRows, " "))
	}
	for _, r := range rows {
		if r.panels == 0 {
			c.add("dashboard row %q has no panel", r.title)
		}
	}
	return queries
}

func (c *checker) checkPanel(p panel) []query {
	owner := fmt.Sprintf("dashboard panel %q", p.Title)
	if got := p.Datasource.uid(); got != datasourceUID {
		c.add("%s: data source %q is not %s", owner, got, datasourceUID)
	}
	var queries []query
	for _, t := range p.Targets {
		if got := t.Datasource.uid(); got != datasourceUID {
			c.add("%s: query %s: data source %q is not %s", owner, t.RefID, got, datasourceUID)
		}
		if strings.TrimSpace(t.Expr) != "" {
			queries = append(queries, query{owner, t.Expr})
		}
	}
	if len(queries) == 0 {
		c.add("%s has no query", owner)
	}
	return queries
}

func (c *checker) checkGrafanaDashboards(objs []object, configMap string) {
	for _, g := range ofKind(objs, "GrafanaDashboard") {
		var spec struct {
			InstanceSelector map[string]any `yaml:"instanceSelector"`
			ConfigMapRef     struct {
				Name string `yaml:"name"`
				Key  string `yaml:"key"`
			} `yaml:"configMapRef"`
		}
		if err := g.Spec.Decode(&spec); err != nil {
			c.add("the spec of the GrafanaDashboard %s cannot be read: %v", g.Metadata.Name, err)
			continue
		}
		if spec.ConfigMapRef.Name != configMap || spec.ConfigMapRef.Key != dashboardKey {
			c.add("the GrafanaDashboard %s references %s/%s, not %s/%s", g.Metadata.Name,
				spec.ConfigMapRef.Name, spec.ConfigMapRef.Key, configMap, dashboardKey)
		}
		if len(spec.InstanceSelector) == 0 {
			c.add("the GrafanaDashboard %s has no instanceSelector", g.Metadata.Name)
		}
	}
}

// tableRows returns the cells of the rows of the first table under the line heading, without its header and
// separator rows.
func tableRows(r io.Reader, heading string) ([][]string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var rows [][]string
	found, inTable, seen := false, false, 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case !found:
			found = line == heading
		case strings.HasPrefix(line, "|"):
			inTable = true
			if seen++; seen > 2 {
				rows = append(rows, splitRow(line))
			}
		case inTable:
			return rows, nil
		case strings.HasPrefix(line, "#"):
			return nil, fmt.Errorf("no table under %q", heading)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !inTable {
		return nil, fmt.Errorf("no table under %q", heading)
	}
	return rows, nil
}

// splitRow splits a Markdown table row into its trimmed cells; an escaped pipe stays in its cell.
func splitRow(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	var cells []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			b.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(line[i])
		}
	}
	return append(cells, strings.TrimSpace(b.String()))
}
