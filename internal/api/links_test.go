// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/links"
)

const (
	tableID = "TBAAAAAAAAAAAA"
	ruleID  = "KRAAAAAAAAAAAA"
)

// fakeLinks stands for internal/links: one Lookup table and the built-in rule, with the refusals of the domain by
// name.
type fakeLinks struct {
	tables   []links.TableInput
	rules    []links.RuleInput
	versions []*int64
	by       []links.Requester
	filters  []links.ListFilter
	next     *int64
	err      error
}

func (f *fakeLinks) table() links.Table {
	return links.Table{ID: 1, PublicID: tableID, Name: "grafana", Columns: []string{"address", "datasource_uid"},
		Entries: []links.Entry{{Key: "prod", Values: map[string]string{"address": "https://g.example.org",
			"datasource_uid": "PROM1"}}}, CreatedAt: t0, Version: 3}
}

func (f *fakeLinks) rule() links.Rule {
	return links.Rule{ID: 1, PublicID: ruleID, Name: links.ExploreName, Builtin: true, Matchers: []links.Matcher{},
		Scope: links.Scope{Type: links.ScopeAlertGroup}, URLTemplate: links.ExploreTemplate, CreatedAt: t0, Version: 1}
}

func (f *fakeLinks) refuse(name string) error {
	switch name {
	case "taken":
		return links.ErrTableNameTaken
	case "rule-taken":
		return links.ErrRuleNameTaken
	case "mismatch":
		return &links.FieldError{Pointer: "/entries/0/values", Code: links.CodeColumnMismatch, Detail: "x"}
	case "template":
		return &links.FieldError{Pointer: "/url_template", Code: links.CodeTemplateSyntax, Detail: "y", Line: 2,
			Column: 7}
	}
	return f.err
}

func (f *fakeLinks) ListTables(_ context.Context, fl links.ListFilter) (links.TablePage, error) {
	f.filters = append(f.filters, fl)
	return links.TablePage{Tables: []links.Table{f.table()}, Next: f.next}, f.err
}

func (f *fakeLinks) GetTable(_ context.Context, id string) (links.Table, error) {
	if id != tableID {
		return links.Table{}, links.ErrTableNotFound
	}
	return f.table(), f.err
}

func (f *fakeLinks) CreateTable(_ context.Context, r links.Requester, in links.TableInput) (links.Table, error) {
	f.by, f.tables = append(f.by, r), append(f.tables, in)
	return f.table(), f.refuse(in.Name)
}

func (f *fakeLinks) UpdateTable(_ context.Context, r links.Requester, _ string, version *int64, in links.TableInput) (
	links.Table, error) {
	f.by, f.tables, f.versions = append(f.by, r), append(f.tables, in), append(f.versions, version)
	if version != nil && *version != 3 {
		return links.Table{}, links.ErrVersionMismatch
	}
	t := f.table()
	t.Version++
	return t, f.refuse(in.Name)
}

func (f *fakeLinks) DeleteTable(_ context.Context, r links.Requester, id string, version *int64) error {
	f.by, f.versions = append(f.by, r), append(f.versions, version)
	if id != tableID {
		return links.ErrTableNotFound
	}
	if version == nil {
		return links.ErrInUse
	}
	return f.err
}

func (f *fakeLinks) ListRules(_ context.Context, fl links.ListFilter) (links.RulePage, error) {
	f.filters = append(f.filters, fl)
	r := f.rule()
	pods := links.Rule{PublicID: "KRBBBBBBBBBBBB", Name: "Pods", Matchers: []links.Matcher{{Label: "a", Op: "=~",
		Value: "b"}}, Scope: links.Scope{Type: links.ScopeLabelValue, Label: "pod"}, URLTemplate: "x", Version: 2}
	return links.RulePage{Rules: []links.Rule{r, pods}, Next: f.next}, f.err
}

func (f *fakeLinks) GetRule(_ context.Context, id string) (links.Rule, error) {
	if id != ruleID {
		return links.Rule{}, links.ErrRuleNotFound
	}
	return f.rule(), f.err
}

func (f *fakeLinks) CreateRule(_ context.Context, r links.Requester, in links.RuleInput) (links.Rule, error) {
	f.by, f.rules = append(f.by, r), append(f.rules, in)
	return f.rule(), f.refuse(in.Name)
}

func (f *fakeLinks) UpdateRule(_ context.Context, r links.Requester, _ string, version *int64, in links.RuleInput) (
	links.Rule, error) {
	f.by, f.rules, f.versions = append(f.by, r), append(f.rules, in), append(f.versions, version)
	if in.Name == "Renamed" {
		return links.Rule{}, links.ErrBuiltinImmutable
	}
	return f.rule(), f.refuse(in.Name)
}

func (f *fakeLinks) DeleteRule(_ context.Context, r links.Requester, id string, version *int64) error {
	f.by, f.versions = append(f.by, r), append(f.versions, version)
	if id == ruleID {
		return links.ErrBuiltinImmutable
	}
	return f.err
}

// The tokens of the links tests: one that reads and writes Lookup tables and Link rules, one that reads them.
const (
	linksWriter = "mstr_pat_links_writer"
	linksReader = "mstr_pat_links_reader"
)

func newLinksAPI(t *testing.T) (*testAPI, *fakeLinks) {
	t.Helper()
	x, ft, _, _ := newTokensAPI(t)
	owner := ft.idents[fullToken].Session
	ft.idents[linksWriter] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"lookup-tables:read",
		"lookup-tables:write", "link-rules:read", "link-rules:write", "templates:preview"}, Transport: audit.TransportAPI,
		Token: &auth.Token{ID: 51, Name: "links"}}
	ft.idents[linksReader] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"lookup-tables:read",
		"link-rules:read"}, Transport: audit.TransportAPI, Token: &auth.Token{ID: 52, Name: "read"}}
	fl := &fakeLinks{}
	x.srv.links = fl
	return x, fl
}

const tableBody = `{"name":"grafana","description":"by environment","columns":["address","datasource_uid"],
	"entries":[{"key":"prod","values":{"address":"https://g.example.org","datasource_uid":"PROM1"}}]}`

// TestLookupTablesAPI is C-12.FR-9 at the API: the ten operations of Lookup tables and Link rules answer with ETag
// and Location, take If-Match, and map the refusals of the domain — column_mismatch, name_taken, in_use,
// builtin_immutable, a template error with its position — to their problems; writes need the write Permissions.
func TestLookupTablesAPI(t *testing.T) {
	x, fl := newLinksAPI(t)
	a := x.as(t, linksWriter, http.MethodPost, "/api/v1/lookup-tables", tableBody)
	m := a.json(t)
	if a.status != http.StatusCreated || a.header.Get("ETag") != `"3"` ||
		a.header.Get("Location") != "/api/v1/lookup-tables/"+tableID || m["id"] != tableID || m["etag"] != `"3"` {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	in := fl.tables[0]
	if in.Name != "grafana" || *in.Description != "by environment" || len(in.Columns) != 2 ||
		in.Entries[0].Values["datasource_uid"] != "PROM1" || fl.by[0].Actor.Kind != audit.ActorUser {
		t.Errorf("input %+v", in)
	}
	for body, want := range map[string]struct {
		status int
		code   string
	}{
		`{"name":"taken","columns":["a"],"entries":[]}`:    {http.StatusConflict, "name_taken"},
		`{"name":"mismatch","columns":["a"],"entries":[]}`: {http.StatusUnprocessableEntity, "column_mismatch"},
		`{"name":"x","columns":[],"entries":[]}`:           {http.StatusBadRequest, ""},
		`{"name":"x","columns":["a","a"],"entries":[]}`:    {http.StatusBadRequest, ""},
	} {
		a = x.as(t, linksWriter, http.MethodPost, "/api/v1/lookup-tables", body)
		if a.status != want.status || (want.code == "name_taken" && a.code(t) != want.code) {
			t.Errorf("%s = %d %s", body, a.status, a.body)
		}
		if want.code == "column_mismatch" {
			errs, _ := a.json(t)["errors"].([]any)
			if e, _ := errs[0].(map[string]any); e["code"] != "column_mismatch" || e["pointer"] != "/entries/0/values" {
				t.Errorf("mismatch %s", a.body)
			}
		}
	}
	path := "/api/v1/lookup-tables/" + tableID
	if a = x.as(t, linksReader, http.MethodGet, path, ""); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"3"` || a.json(t)["name"] != "grafana" {
		t.Errorf("get = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksReader, http.MethodGet, "/api/v1/lookup-tables/TBZZZZZZZZZZZZ", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("get unknown = %d", a.status)
	}
	if a = x.as(t, linksReader, http.MethodPost, "/api/v1/lookup-tables", tableBody); a.status !=
		http.StatusForbidden {
		t.Errorf("create by a viewer = %d", a.status)
	}
	if a = x.as(t, linksWriter, http.MethodPut, path, tableBody); a.status != http.StatusPreconditionRequired {
		t.Errorf("update without If-Match = %d", a.status)
	}
	if a = x.as(t, linksWriter, http.MethodPut, path, tableBody, "If-Match", `"7"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("stale update = %d", a.status)
	}
	if a = x.as(t, linksWriter, http.MethodPut, path, tableBody, "If-Match", `"3"`); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"4"` {
		t.Errorf("update = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksWriter, http.MethodDelete, path, ""); a.status != http.StatusConflict ||
		a.code(t) != "in_use" {
		t.Errorf("delete in use = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksWriter, http.MethodDelete, path, "", "If-Match", `"3"`); a.status != http.StatusNoContent ||
		*fl.versions[len(fl.versions)-1] != 3 {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksWriter, http.MethodDelete, path, "", "If-Match", `nope`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("delete with a bad If-Match = %d", a.status)
	}
	if a = x.as(t, linksWriter, http.MethodDelete, "/api/v1/lookup-tables/TBZZZZZZZZZZZZ", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("delete unknown = %d", a.status)
	}
	next := int64(5)
	fl.next = &next
	a = x.as(t, linksReader, http.MethodGet, "/api/v1/lookup-tables?limit=1", "")
	cursor, _ := a.json(t)["next_cursor"].(string)
	if a.status != http.StatusOK || cursor == "" || fl.filters[0].Limit != 1 {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	fl.next = nil
	a = x.as(t, linksReader, http.MethodGet, "/api/v1/lookup-tables?cursor="+url.QueryEscape(cursor), "")
	if a.status != http.StatusOK || *fl.filters[1].After != 5 || a.json(t)["next_cursor"] != nil {
		t.Errorf("page 2 = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksReader, http.MethodGet, "/api/v1/link-rules?cursor="+url.QueryEscape(cursor), ""); a.status != http.StatusBadRequest {
		t.Errorf("a cursor of another list = %d", a.status)
	}
	fl.err = errors.New("down")
	if a = x.as(t, linksReader, http.MethodGet, "/api/v1/lookup-tables", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failing list = %d", a.status)
	}
	if a = x.as(t, linksWriter, http.MethodPost, "/api/v1/lookup-tables", tableBody); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failing create = %d", a.status)
	}
	if a = x.as(t, linksWriter, http.MethodPut, path, tableBody, "If-Match", `"3"`); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failing update = %d", a.status)
	}
	fl.err = nil
	fl.err = links.ErrVersionMismatch
	if a = x.as(t, linksWriter, http.MethodDelete, path, "", "If-Match", `"3"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("stale delete = %d", a.status)
	}
}

const ruleBody = `{"name":"Dashboard","matchers":[{"label":"cluster","op":"=~","value":".+"}],
	"scope":{"type":"label_value","label":"pod"},"url_template":"https://g/{{ .Value }}"}`

// TestLinkRulesAPI: the rules with their scope, the built-in rule's refusals and a template error at /url_template
// with its line and column.
func TestLinkRulesAPI(t *testing.T) {
	x, fl := newLinksAPI(t)
	a := x.as(t, linksWriter, http.MethodPost, "/api/v1/link-rules", ruleBody)
	m := a.json(t)
	if a.status != http.StatusCreated || a.header.Get("Location") != "/api/v1/link-rules/"+ruleID ||
		m["builtin"] != true || m["etag"] != `"1"` {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	scope, _ := m["scope"].(map[string]any)
	if scope["type"] != "alert_group" || scope["label"] != nil {
		t.Errorf("scope %v", scope)
	}
	in := fl.rules[0]
	if in.Name != "Dashboard" || in.Scope != (links.Scope{Type: links.ScopeLabelValue, Label: "pod"}) ||
		in.Matchers[0] != (links.Matcher{Label: "cluster", Op: "=~", Value: ".+"}) || in.URLTemplate != "https://g/{{ .Value }}" {
		t.Errorf("input %+v", in)
	}
	a = x.as(t, linksWriter, http.MethodPost, "/api/v1/link-rules",
		`{"name":"template","matchers":[],"scope":{"type":"alert_group"},"url_template":"x"}`)
	errs, _ := a.json(t)["errors"].([]any)
	if e, _ := errs[0].(map[string]any); a.status != http.StatusUnprocessableEntity || e["code"] != "template_syntax" ||
		e["line"] != 2.0 || e["column"] != 7.0 || e["pointer"] != "/url_template" {
		t.Errorf("template error = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksWriter, http.MethodPost, "/api/v1/link-rules",
		`{"name":"rule-taken","matchers":[],"scope":{"type":"alert_group"},"url_template":"x"}`); a.status !=
		http.StatusConflict || a.code(t) != "name_taken" {
		t.Errorf("taken = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksWriter, http.MethodPost, "/api/v1/link-rules",
		`{"name":"x","matchers":[],"scope":{"type":"route"},"url_template":"x"}`); a.status != http.StatusBadRequest {
		t.Errorf("bad scope = %d", a.status)
	}
	a = x.as(t, linksReader, http.MethodGet, "/api/v1/link-rules", "")
	items, _ := a.json(t)["items"].([]any)
	if a.status != http.StatusOK || len(items) != 2 {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	pods := items[1].(map[string]any)
	if s, _ := pods["scope"].(map[string]any); s["label"] != "pod" || pods["etag"] != `"2"` {
		t.Errorf("pods %v", pods)
	}
	path := "/api/v1/link-rules/" + ruleID
	if a = x.as(t, linksReader, http.MethodGet, path, ""); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"1"` {
		t.Errorf("get = %d", a.status)
	}
	if a = x.as(t, linksReader, http.MethodGet, "/api/v1/link-rules/KRZZZZZZZZZZZZ", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("get unknown = %d", a.status)
	}
	if a = x.as(t, linksWriter, http.MethodPut, path, ruleBody); a.status != http.StatusPreconditionRequired {
		t.Errorf("update without If-Match = %d", a.status)
	}
	if a = x.as(t, linksWriter, http.MethodPut, path, ruleBody, "If-Match", `"1"`); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"1"` || *fl.versions[0] != 1 {
		t.Errorf("update = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksWriter, http.MethodPut, path, `{"name":"Renamed","matchers":[],
		"scope":{"type":"alert_group"},"url_template":"x"}`, "If-Match", `"1"`); a.status != http.StatusConflict ||
		a.code(t) != "builtin_immutable" {
		t.Errorf("rename the built-in = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksWriter, http.MethodDelete, path, ""); a.status != http.StatusConflict ||
		a.code(t) != "builtin_immutable" {
		t.Errorf("delete the built-in = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksWriter, http.MethodDelete, "/api/v1/link-rules/KRBBBBBBBBBBBB", "", "If-Match", `"2"`); a.status !=
		http.StatusNoContent {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	if a = x.as(t, linksWriter, http.MethodDelete, "/api/v1/link-rules/KRBBBBBBBBBBBB", "", "If-Match", `x`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("delete with a bad If-Match = %d", a.status)
	}
	if a = x.as(t, linksReader, http.MethodDelete, "/api/v1/link-rules/KRBBBBBBBBBBBB", ""); a.status !=
		http.StatusForbidden {
		t.Errorf("delete by a viewer = %d", a.status)
	}
	fl.err = links.ErrRuleNotFound
	if a = x.as(t, linksWriter, http.MethodDelete, "/api/v1/link-rules/KRBBBBBBBBBBBB", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("delete unknown = %d", a.status)
	}
	fl.err = errors.New("down")
	for _, call := range []func() int{
		func() int { return x.as(t, linksReader, http.MethodGet, "/api/v1/link-rules", "").status },
		func() int { return x.as(t, linksWriter, http.MethodPost, "/api/v1/link-rules", ruleBody).status },
		func() int {
			return x.as(t, linksWriter, http.MethodPut, path, ruleBody, "If-Match", `"1"`).status
		},
	} {
		if status := call(); status != http.StatusInternalServerError {
			t.Errorf("failing call = %d", status)
		}
	}
	fl.err = nil
	next := int64(2)
	fl.next = &next
	if a = x.as(t, linksReader, http.MethodGet, "/api/v1/link-rules", ""); a.json(t)["next_cursor"] == nil {
		t.Errorf("next cursor %s", a.body)
	}
	if a = x.as(t, linksReader, http.MethodGet, "/api/v1/link-rules?cursor=bad", ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("bad cursor = %d", a.status)
	}
	if a = x.as(t, linksReader, http.MethodGet, "/api/v1/lookup-tables?cursor=bad", ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("bad cursor = %d", a.status)
	}
}

// TestLinkRulePreviewAPI (C-12.AC-6): a Link rule's preview has no built-in source and no markup.
func TestLinkRulePreviewAPI(t *testing.T) {
	x, _ := newLinksAPI(t)
	fake := &fakeTemplates{}
	x.srv.templates = fake
	a := x.as(t, linksWriter, http.MethodPost, "/api/v1/template-previews",
		`{"kind":"link_rule","template":"https://g/{{ .Labels.cluster }}","stored_snapshot_id":"SSAAAAAAAAAAAA"}`)
	m := a.json(t)
	if a.status != http.StatusOK || m["valid"] != true || m["source"] != nil || fake.reqs[0].Kind != "link_rule" ||
		fake.reqs[0].SnapshotID != "SSAAAAAAAAAAAA" {
		t.Errorf("preview = %d %s", a.status, a.body)
	}
}

// TestAlertGroupLinksAPI (C-09.FR-14): getAlertGroup returns the links of the Alert Group by name and URL.
func TestAlertGroupLinksAPI(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	fg.view.Links = []groups.Link{{Name: "Runbook", URL: "https://wiki.example.org/latency"}}
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID, "")
	ls, _ := a.json(t)["links"].([]any)
	if l, _ := ls[0].(map[string]any); a.status != http.StatusOK || len(ls) != 1 || l["name"] != "Runbook" ||
		l["url"] != "https://wiki.example.org/latency" {
		t.Errorf("links = %d %s", a.status, a.body)
	}
	fg.view.Links = nil
	if a = x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID, ""); string(a.body) == "" ||
		a.json(t)["links"] == nil {
		t.Errorf("no links = %s", a.body)
	}
}
