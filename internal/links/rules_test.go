// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package links

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/links/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

func dbgenRule(id int64, publicID, name string) dbgen.GetLinkRuleRow {
	return dbgen.GetLinkRuleRow{ID: id, PublicID: publicID, Name: name, ScopeType: ScopeAlertGroup,
		UrlTemplate: "https://x"}
}

func dashboard() RuleInput {
	return RuleInput{Name: " Dashboard ", Matchers: []Matcher{{Label: "cluster", Op: "=~", Value: ".+"}},
		Scope:       Scope{Type: ScopeAlertGroup},
		URLTemplate: `{{ lookup "grafana" .Labels.cluster "address" }}/d/latency?var-ns={{ .Labels.namespace }}`}
}

// webhook is an Alertmanager webhook with one firing Alert of the labels.
func webhook(labels string) []byte {
	return []byte(`{"status":"firing","receiver":"lab","groupLabels":{"alertname":"HighLatency"},"alerts":[` +
		`{"status":"firing","labels":` + labels + `,"annotations":{},"startsAt":"2026-10-08T11:00:00Z",` +
		`"generatorURL":"http://prometheus:9090/graph?g0.expr=up%3D%3D0","fingerprint":"f1"}]}`)
}

// TestLinkRules: a rule is created, read, listed, updated and deleted with its Matchers and scope, each change with
// its Audit log entry and diff; the version follows If-Match.
func TestLinkRules(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	created, err := s.CreateRule(ctx, by, dashboard())
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Dashboard" || created.Builtin || len(created.Matchers) != 1 || created.Scope.Type != ScopeAlertGroup ||
		!strings.HasPrefix(created.PublicID, string(publicid.LinkRule)) || created.Version != 1 {
		t.Fatalf("created %+v", created)
	}
	if f.audit[0].Action != ActionRuleCreated || f.audit[0].ResourceType.String != ResourceRule {
		t.Fatalf("audit %+v", f.audit[0])
	}
	if _, err := s.CreateRule(ctx, by, dashboard()); !errors.Is(err, ErrRuleNameTaken) {
		t.Errorf("same name: %v", err)
	}
	in := RuleInput{Name: "Pod logs", Scope: Scope{Type: ScopeLabelValue, Label: "pod"},
		URLTemplate: "https://logs.example.org/{{ .Value }}"}
	stale := int64(5)
	if _, err := s.UpdateRule(ctx, by, created.PublicID, &stale, in); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale: %v", err)
	}
	v := created.Version
	updated, err := s.UpdateRule(ctx, by, created.PublicID, &v, in)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Name != "Pod logs" || len(updated.Matchers) != 0 ||
		updated.Scope != (Scope{Type: ScopeLabelValue, Label: "pod"}) {
		t.Fatalf("updated %+v", updated)
	}
	changes := diffOf(t, f.audit[len(f.audit)-1])
	pointers := make([]string, len(changes))
	for i, c := range changes {
		pointers[i] = c.Pointer
	}
	if !slices.Equal(pointers, []string{"/name", "/matchers", "/scope/type", "/scope/label", "/url_template"}) {
		t.Errorf("update diff %v", pointers)
	}
	if same, err := s.UpdateRule(ctx, by, created.PublicID, nil, in); err != nil || same.Version != 2 {
		t.Errorf("no changes: %+v %v", same, err)
	}
	other, err := s.CreateRule(ctx, by, RuleInput{Name: "Other", Scope: Scope{Type: ScopeAlertGroup},
		URLTemplate: "https://o.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRule(ctx, by, other.PublicID, nil, in); !errors.Is(err, ErrRuleNameTaken) {
		t.Errorf("rename to a taken name: %v", err)
	}
	page, err := s.ListRules(ctx, ListFilter{Limit: 1})
	if err != nil || len(page.Rules) != 1 || page.Next == nil || page.Rules[0].Name != "Pod logs" {
		t.Fatalf("first page %+v %v", page, err)
	}
	if page, err = s.ListRules(ctx, ListFilter{After: page.Next}); err != nil || len(page.Rules) != 1 || page.Next != nil {
		t.Fatalf("second page %+v %v", page, err)
	}
	if err := s.DeleteRule(ctx, by, other.PublicID, &stale); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale delete: %v", err)
	}
	if err := s.DeleteRule(ctx, by, other.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if last := f.audit[len(f.audit)-1]; last.Action != ActionRuleDeleted || last.ResourceName.String != "Other" {
		t.Errorf("delete audit %+v", last)
	}
	for _, id := range []string{other.PublicID, "nope", "TBAAAAAAAAAAAA"} {
		if _, err := s.GetRule(ctx, id); !errors.Is(err, ErrRuleNotFound) {
			t.Errorf("get %s: %v", id, err)
		}
		if err := s.DeleteRule(ctx, by, id, nil); !errors.Is(err, ErrRuleNotFound) {
			t.Errorf("delete %s: %v", id, err)
		}
		if _, err := s.UpdateRule(ctx, by, id, nil, in); !errors.Is(err, ErrRuleNotFound) {
			t.Errorf("update %s: %v", id, err)
		}
	}
}

// TestLinkRuleChecks: the name, the Matchers as a Route's, the scope and a URL template that parses, calls only
// registered functions and passes its dry run.
func TestLinkRuleChecks(t *testing.T) {
	s, _ := newService(t, newFake())
	base := dashboard()
	with := func(f func(*RuleInput)) RuleInput {
		in := base
		f(&in)
		return in
	}
	for _, tc := range []struct {
		in            RuleInput
		pointer, code string
		line          int
	}{
		{with(func(in *RuleInput) { in.Name = "" }), "/name", CodeInvalidFormat, 0},
		{with(func(in *RuleInput) { in.Matchers = []Matcher{{Label: "", Op: "=", Value: "x"}} }),
			"/matchers/0/label", CodeInvalidFormat, 0},
		{with(func(in *RuleInput) { in.Matchers = []Matcher{{Label: "a", Op: "==", Value: "x"}} }),
			"/matchers/0/op", CodeInvalidFormat, 0},
		{with(func(in *RuleInput) { in.Matchers = []Matcher{{Label: "a", Op: "=~", Value: "("}} }),
			"/matchers/0/value", CodeInvalidRegex, 0},
		{with(func(in *RuleInput) { in.Scope = Scope{Type: ScopeAlertGroup, Label: "pod"} }), "/scope/label",
			CodeInvalidFormat, 0},
		{with(func(in *RuleInput) { in.Scope = Scope{Type: ScopeLabelValue, Label: "1pod"} }), "/scope/label",
			CodeInvalidFormat, 0},
		{with(func(in *RuleInput) { in.Scope = Scope{Type: "route"} }), "/scope/type", CodeInvalidFormat, 0},
		{with(func(in *RuleInput) { in.URLTemplate = "  " }), "/url_template", CodeInvalidFormat, 0},
		{with(func(in *RuleInput) { in.URLTemplate = "https://x/{{ .Labels.a" }), "/url_template", CodeTemplateSyntax,
			1},
		{with(func(in *RuleInput) { in.URLTemplate = "https://x/\n{{ env \"HOME\" }}" }), "/url_template",
			CodeUnknownFunction, 2},
		{with(func(in *RuleInput) { in.URLTemplate = "https://x/{{ (index .Alerts 5).Labels.pod }}" }),
			"/url_template", CodeTemplateSyntax, 1},
		{with(func(in *RuleInput) {
			in.Scope = Scope{Type: ScopeLabelValue, Label: "pod"}
			in.URLTemplate = `{{ if eq .Value "checkout-2" }}{{ index .Alerts 9 }}{{ end }}`
		}), "/url_template", CodeTemplateSyntax, 0},
	} {
		_, err := s.CreateRule(t.Context(), by, tc.in)
		fe := fieldError(t, err)
		if fe.Pointer != tc.pointer || fe.Code != tc.code || (tc.line > 0 && fe.Line != tc.line) {
			t.Errorf("%s: %+v, want %s %s line %d", tc.pointer, fe, tc.pointer, tc.code, tc.line)
		}
		if _, err := s.UpdateRule(t.Context(), by, "KRAAAAAAAAAAAA", nil, tc.in); err == nil {
			t.Errorf("%s: the update passed", tc.pointer)
		}
	}
}

// TestLinkRuleDryRun: the URL template is dry-run against the most recent Stored Snapshots whose common labels its
// Matchers match, or the example when none does; a failing read of the samples fails the save.
func TestLinkRuleDryRun(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	f.snapshots = [][]byte{[]byte("not json"), webhook(`{"alertname":"HighLatency","cluster":"prod","namespace":"api"}`),
		[]byte(`{"status":"firing","alerts":[]}`)}
	broken := RuleInput{Name: "Broken", Scope: Scope{Type: ScopeAlertGroup},
		URLTemplate: `https://x.example.org/{{ if eq .Labels.namespace "api" }}{{ (index .Alerts 5).Labels.pod }}{{ end }}`}
	if _, err := s.CreateRule(ctx, by, broken); fieldError(t, err).Code != CodeTemplateSyntax {
		t.Fatalf("a sample with namespace=api: %v", err)
	}
	broken.Matchers = []Matcher{{Label: "cluster", Op: "=", Value: "dev"}}
	if _, err := s.CreateRule(ctx, by, broken); err != nil {
		t.Fatalf("no sample matches, the example passes: %v", err)
	}
	f.fail["ListRecentSnapshots"] = errors.New("boom")
	if _, err := s.CreateRule(ctx, by, RuleInput{Name: "x", Scope: Scope{Type: ScopeAlertGroup},
		URLTemplate: "https://x"}); err == nil {
		t.Error("a failing read of the samples")
	}
	delete(f.fail, "ListRecentSnapshots")
	f.fail["GetLinkSnapshotRetention"] = errors.New("boom")
	if _, err := s.CreateRule(ctx, by, RuleInput{Name: "x", Scope: Scope{Type: ScopeAlertGroup},
		URLTemplate: "https://x"}); err == nil {
		t.Error("a failing read of the retention")
	}
}

// TestExploreRuleUpgrade: the ensure step gives a built-in rule that still has a previous built-in template the
// current one, as a new version with its public_id, Matchers and name kept, once; an edited template is kept.
func TestExploreRuleUpgrade(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	builtin := dbgenRule(1, "KRAAAAAAAAAAAA", ExploreName)
	builtin.Builtin, builtin.UrlTemplate, builtin.Version = true, previousExploreTemplates[0], 3
	f.rules = []*ruleRow{{GetLinkRuleRow: builtin,
		matchers: []dbgen.ListLinkRuleMatchersRow{{Label: "env", Op: "!=", Value: "dev"}}}}
	for range 2 {
		if err := EnsureExplore(ctx, f, orgID, t0); err != nil {
			t.Fatal(err)
		}
	}
	r := f.rules[0]
	if len(f.rules) != 1 || r.UrlTemplate != ExploreTemplate || r.Version != 4 || r.PublicID != "KRAAAAAAAAAAAA" ||
		r.Name != ExploreName || len(r.matchers) != 1 {
		t.Fatalf("upgraded %+v", f.rules)
	}
	r.UrlTemplate = previousExploreTemplates[0] + " "
	if err := EnsureExplore(ctx, f, orgID, t0); err != nil || r.UrlTemplate != previousExploreTemplates[0]+" " ||
		r.Version != 4 {
		t.Errorf("an edited template is kept: %v %+v", err, r)
	}
	if ExploreTemplate == previousExploreTemplates[0] {
		t.Error("the current template is not a previous one")
	}
}

// TestExploreRule: the ensure step creates the built-in rule once, run twice changes nothing, a rule of its name
// stops the start; the built-in rule cannot be deleted, renamed or given another scope, but its URL template and
// Matchers can be edited.
func TestExploreRule(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	for range 2 {
		if err := EnsureExplore(ctx, f, orgID, t0); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.rules) != 1 || !f.rules[0].Builtin || f.rules[0].Name != ExploreName ||
		f.rules[0].UrlTemplate != ExploreTemplate {
		t.Fatalf("rules %+v", f.rules)
	}
	explore, err := s.GetRule(ctx, f.rules[0].PublicID)
	if err != nil || !explore.Builtin {
		t.Fatal(err)
	}
	if err := s.DeleteRule(ctx, by, explore.PublicID, nil); !errors.Is(err, ErrBuiltinImmutable) {
		t.Errorf("delete: %v", err)
	}
	in := RuleInput{Name: ExploreName, Scope: Scope{Type: ScopeAlertGroup}, URLTemplate: ExploreTemplate}
	for _, change := range []func(*RuleInput){
		func(in *RuleInput) { in.Name = "Grafana" },
		func(in *RuleInput) { in.Scope = Scope{Type: ScopeLabelValue, Label: "cluster"} },
	} {
		c := in
		change(&c)
		if _, err := s.UpdateRule(ctx, by, explore.PublicID, nil, c); !errors.Is(err, ErrBuiltinImmutable) {
			t.Errorf("update %+v: %v", c, err)
		}
	}
	in.Matchers = []Matcher{{Label: "env", Op: "!=", Value: "dev"}}
	in.URLTemplate = ExploreTemplate + " "
	edited, err := s.UpdateRule(ctx, by, explore.PublicID, nil, in)
	if err != nil || !edited.Builtin || len(edited.Matchers) != 1 || edited.URLTemplate != in.URLTemplate {
		t.Fatalf("edited %+v %v", edited, err)
	}
	if err := EnsureExplore(ctx, f, orgID, t0); err != nil || len(f.rules) != 1 || f.rules[0].Version != 2 {
		t.Errorf("an ensure after an edit changes nothing: %v %+v", err, f.rules)
	}
	g := newFake()
	g.rules = []*ruleRow{{GetLinkRuleRow: dbgenRule(1, "KRAAAAAAAAAAAA", ExploreName)}}
	if err := EnsureExplore(ctx, g, orgID, t0); !errors.Is(err, ErrRuleNameTaken) {
		t.Errorf("a rule of its name: %v", err)
	}
	g.fail["EnsureExploreRule"] = errors.New("boom")
	if err := EnsureExplore(ctx, g, orgID, t0); err == nil {
		t.Error("a failing ensure")
	}
}

// TestRuleStoreFailures: a failing query fails the change and writes no Audit log entry.
func TestRuleStoreFailures(t *testing.T) {
	ctx := t.Context()
	in := RuleInput{Name: "x", Matchers: []Matcher{{Label: "a", Op: "=", Value: "b"}},
		Scope: Scope{Type: ScopeAlertGroup}, URLTemplate: "https://x"}
	for _, name := range []string{"InsertLinkRule", "InsertLinkRuleMatcher", "GetLinkRule", "ListLinkRuleMatchers",
		"InsertAuditEntry"} {
		f := newFake()
		s, _ := newService(t, f)
		f.fail[name] = errors.New("boom")
		if _, err := s.CreateRule(ctx, by, in); err == nil || len(f.audit) != 0 {
			t.Errorf("create with %s failing: %v", name, err)
		}
	}
	for _, name := range []string{"LockLinkRule", "UpdateLinkRule", "DeleteLinkRuleMatchers", "InsertLinkRuleMatcher",
		"InsertAuditEntry", "DeleteLinkRule"} {
		f := newFake()
		s, _ := newService(t, f)
		r, err := s.CreateRule(ctx, by, in)
		if err != nil {
			t.Fatal(err)
		}
		f.fail[name] = errors.New("boom")
		changed := in
		changed.Name = "y"
		if name != "DeleteLinkRule" {
			if _, err := s.UpdateRule(ctx, by, r.PublicID, nil, changed); err == nil || len(f.audit) != 1 {
				t.Errorf("update with %s failing: %v", name, err)
			}
		}
		if name == "LockLinkRule" || name == "InsertAuditEntry" || name == "DeleteLinkRule" {
			if err := s.DeleteRule(ctx, by, r.PublicID, nil); err == nil {
				t.Errorf("delete with %s failing", name)
			}
		}
	}
	f := newFake()
	s, _ := newService(t, f)
	f.fail["ListLinkRules"] = errors.New("boom")
	if _, err := s.ListRules(ctx, ListFilter{}); err == nil {
		t.Error("a failing list")
	}
	delete(f.fail, "ListLinkRules")
	if _, err := s.CreateRule(ctx, by, in); err != nil {
		t.Fatal(err)
	}
	f.fail["ListLinkRuleMatchers"] = errors.New("boom")
	if _, err := s.ListRules(ctx, ListFilter{}); err == nil {
		t.Error("a failing read of the matchers")
	}
	delete(f.fail, "ListLinkRuleMatchers")
	f.rules[0].matchers[0].Op = "=~"
	f.rules[0].matchers[0].Value = "("
	if _, err := s.ListRules(ctx, ListFilter{}); err == nil {
		t.Error("a stored matcher that does not compile")
	}
}

// TestDryRunLookupsPerSample: each sample of a dry run has its own budget of lookups, as each render has.
func TestDryRunLookupsPerSample(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	for i := range 20 {
		f.snapshots = append(f.snapshots, webhook(`{"alertname":"A","cluster":"c`+strings.Repeat("x", i)+`"}`))
	}
	in := RuleInput{Name: "Many", Scope: Scope{Type: ScopeAlertGroup},
		URLTemplate: `https://x/{{ range $i := until 60 }}{{ lookup "t" (print $.Labels.cluster $i) "c" }}{{ end }}`}
	if _, err := s.CreateRule(ctx, by, in); err != nil {
		t.Errorf("60 lookups in each of 20 samples: %v", err)
	}
}
