// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/links"
)

// Links is what the API needs of internal/links: Lookup tables and Link rules.
type Links interface {
	ListTables(ctx context.Context, f links.ListFilter) (links.TablePage, error)
	GetTable(ctx context.Context, publicID string) (links.Table, error)
	CreateTable(ctx context.Context, r links.Requester, in links.TableInput) (links.Table, error)
	UpdateTable(ctx context.Context, r links.Requester, publicID string, version *int64, in links.TableInput) (
		links.Table, error)
	DeleteTable(ctx context.Context, r links.Requester, publicID string, version *int64) error
	ListRules(ctx context.Context, f links.ListFilter) (links.RulePage, error)
	GetRule(ctx context.Context, publicID string) (links.Rule, error)
	CreateRule(ctx context.Context, r links.Requester, in links.RuleInput) (links.Rule, error)
	UpdateRule(ctx context.Context, r links.Requester, publicID string, version *int64, in links.RuleInput) (
		links.Rule, error)
	DeleteRule(ctx context.Context, r links.Requester, publicID string, version *int64) error
}

// The names of the cursors of listLookupTables and listLinkRules.
const (
	lookupTablesCursor = "lookup-tables"
	linkRulesCursor    = "link-rules"
)

// idKey is the sort key of a cursor of a list in the order of creation.
type idKey struct {
	ID int64 `json:"i"`
}

// linksRequester is who asks for a change of a Lookup table or a Link rule through the API.
func linksRequester(ctx context.Context) (links.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return links.Requester{}, err
	}
	return links.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// listFilter reads the cursor of list and the limit of a list request.
func listFilter(cursor *gen.Cursor, limit *gen.Limit, list string) (links.ListFilter, error) {
	f := links.ListFilter{Limit: pageSize(limit)}
	var key idKey
	if ok, err := decodeCursor(cursor, list, &key); err != nil {
		return f, err
	} else if ok {
		f.After = &key.ID
	}
	return f, nil
}

// optionalVersion reads an optional If-Match.
func optionalVersion(v *gen.IfMatchOptional) (*int64, error) {
	if v == nil {
		return nil, nil
	}
	return ifMatch(*v)
}

var errBodyMissing = fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")

// ListLookupTables is listLookupTables: the Lookup tables with their rows, in the order they were created.
func (s *Server) ListLookupTables(ctx context.Context, req gen.ListLookupTablesRequestObject) (
	gen.ListLookupTablesResponseObject, error) {
	f, err := listFilter(req.Params.Cursor, req.Params.Limit, lookupTablesCursor)
	if err != nil {
		return nil, err
	}
	page, err := s.links.ListTables(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.LookupTableList{Items: make([]gen.LookupTable, 0, len(page.Tables))}
	for _, t := range page.Tables {
		out.Items = append(out.Items, lookupTableOf(t))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(lookupTablesCursor, idKey{ID: *page.Next}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListLookupTables200JSONResponse(out), nil
}

// CreateLookupTable is createLookupTable.
func (s *Server) CreateLookupTable(ctx context.Context, req gen.CreateLookupTableRequestObject) (
	gen.CreateLookupTableResponseObject, error) {
	r, err := linksRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	t, err := s.links.CreateTable(ctx, r, tableInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag, location := etag(t.Version), BasePath+"/lookup-tables/"+t.PublicID
	return gen.CreateLookupTable201JSONResponse{Body: lookupTableOf(t),
		Headers: gen.CreateLookupTable201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// GetLookupTable is getLookupTable.
func (s *Server) GetLookupTable(ctx context.Context, req gen.GetLookupTableRequestObject) (
	gen.GetLookupTableResponseObject, error) {
	t, err := s.links.GetTable(ctx, req.LookupTableId)
	if err != nil {
		return nil, err
	}
	tag := etag(t.Version)
	return gen.GetLookupTable200JSONResponse{Body: lookupTableOf(t),
		Headers: gen.GetLookupTable200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateLookupTable is updateLookupTable, with If-Match: the rows replace the stored ones as a whole.
func (s *Server) UpdateLookupTable(ctx context.Context, req gen.UpdateLookupTableRequestObject) (
	gen.UpdateLookupTableResponseObject, error) {
	r, err := linksRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	t, err := s.links.UpdateTable(ctx, r, req.LookupTableId, version, tableInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(t.Version)
	return gen.UpdateLookupTable200JSONResponse{Body: lookupTableOf(t),
		Headers: gen.UpdateLookupTable200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteLookupTable is deleteLookupTable: refused with 409 in_use while a Link rule reads the table.
func (s *Server) DeleteLookupTable(ctx context.Context, req gen.DeleteLookupTableRequestObject) (
	gen.DeleteLookupTableResponseObject, error) {
	r, err := linksRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := optionalVersion(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if err := s.links.DeleteTable(ctx, r, req.LookupTableId, version); err != nil {
		return nil, err
	}
	return gen.DeleteLookupTable204Response{}, nil
}

func tableInputOf(b gen.LookupTableBase) links.TableInput {
	in := links.TableInput{Name: b.Name, Description: b.Description, Columns: b.Columns,
		Entries: make([]links.Entry, len(b.Entries))}
	for i, e := range b.Entries {
		in.Entries[i] = links.Entry{Key: e.Key, Values: e.Values}
	}
	return in
}

func lookupTableOf(t links.Table) gen.LookupTable {
	tag, description := etag(t.Version), t.Description
	out := gen.LookupTable{Id: t.PublicID, Name: t.Name, Description: &description, Columns: t.Columns,
		Entries: make([]gen.LookupEntry, len(t.Entries)), CreatedAt: t.CreatedAt, Etag: &tag}
	if out.Columns == nil {
		out.Columns = []string{}
	}
	for i, e := range t.Entries {
		out.Entries[i] = gen.LookupEntry{Key: e.Key, Values: e.Values}
	}
	return out
}

// ListLinkRules is listLinkRules: the Link rules in the order they were created, the built-in "Explore" first.
func (s *Server) ListLinkRules(ctx context.Context, req gen.ListLinkRulesRequestObject) (
	gen.ListLinkRulesResponseObject, error) {
	f, err := listFilter(req.Params.Cursor, req.Params.Limit, linkRulesCursor)
	if err != nil {
		return nil, err
	}
	page, err := s.links.ListRules(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.LinkRuleList{Items: make([]gen.LinkRule, 0, len(page.Rules))}
	for _, r := range page.Rules {
		out.Items = append(out.Items, linkRuleOf(r))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(linkRulesCursor, idKey{ID: *page.Next}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListLinkRules200JSONResponse(out), nil
}

// CreateLinkRule is createLinkRule: the URL template is dry-run first.
func (s *Server) CreateLinkRule(ctx context.Context, req gen.CreateLinkRuleRequestObject) (
	gen.CreateLinkRuleResponseObject, error) {
	r, err := linksRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	rule, err := s.links.CreateRule(ctx, r, ruleInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag, location := etag(rule.Version), BasePath+"/link-rules/"+rule.PublicID
	return gen.CreateLinkRule201JSONResponse{Body: linkRuleOf(rule),
		Headers: gen.CreateLinkRule201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// GetLinkRule is getLinkRule.
func (s *Server) GetLinkRule(ctx context.Context, req gen.GetLinkRuleRequestObject) (
	gen.GetLinkRuleResponseObject, error) {
	rule, err := s.links.GetRule(ctx, req.LinkRuleId)
	if err != nil {
		return nil, err
	}
	tag := etag(rule.Version)
	return gen.GetLinkRule200JSONResponse{Body: linkRuleOf(rule),
		Headers: gen.GetLinkRule200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateLinkRule is updateLinkRule, with If-Match: the built-in rule keeps its name and scope.
func (s *Server) UpdateLinkRule(ctx context.Context, req gen.UpdateLinkRuleRequestObject) (
	gen.UpdateLinkRuleResponseObject, error) {
	r, err := linksRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	rule, err := s.links.UpdateRule(ctx, r, req.LinkRuleId, version, ruleInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(rule.Version)
	return gen.UpdateLinkRule200JSONResponse{Body: linkRuleOf(rule),
		Headers: gen.UpdateLinkRule200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteLinkRule is deleteLinkRule: the built-in rule answers 409 builtin_immutable.
func (s *Server) DeleteLinkRule(ctx context.Context, req gen.DeleteLinkRuleRequestObject) (
	gen.DeleteLinkRuleResponseObject, error) {
	r, err := linksRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := optionalVersion(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if err := s.links.DeleteRule(ctx, r, req.LinkRuleId, version); err != nil {
		return nil, err
	}
	return gen.DeleteLinkRule204Response{}, nil
}

func ruleInputOf(b gen.LinkRuleBase) links.RuleInput {
	in := links.RuleInput{Name: b.Name, Matchers: make([]links.Matcher, len(b.Matchers)),
		Scope: links.Scope{Type: string(b.Scope.Type), Label: value(b.Scope.Label)}, URLTemplate: b.UrlTemplate}
	for i, m := range b.Matchers {
		in.Matchers[i] = links.Matcher{Label: m.Label, Op: string(m.Op), Value: m.Value}
	}
	return in
}

func linkRuleOf(r links.Rule) gen.LinkRule {
	tag := etag(r.Version)
	out := gen.LinkRule{Id: r.PublicID, Name: r.Name, Builtin: r.Builtin, Matchers: make([]gen.Matcher, len(r.Matchers)),
		Scope: gen.LinkRuleScope{Type: gen.LinkRuleScopeType(r.Scope.Type)}, UrlTemplate: r.URLTemplate,
		CreatedAt: r.CreatedAt, Etag: &tag}
	if r.Scope.Type == links.ScopeLabelValue {
		out.Scope.Label.Set(r.Scope.Label)
	} else {
		out.Scope.Label.SetNull()
	}
	for i, m := range r.Matchers {
		out.Matchers[i] = gen.Matcher{Label: m.Label, Op: gen.MatcherOp(m.Op), Value: m.Value}
	}
	return out
}
