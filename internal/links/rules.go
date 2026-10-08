// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package links

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/links/dbgen"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/templates"
)

// The scopes of a Link rule: one link for the Alert Group, or one per distinct value of a label among its Alerts.
const (
	ScopeAlertGroup = "alert_group"
	ScopeLabelValue = "label_value"
)

// DryRunSample is template.dry_run_sample: the most recent Stored Snapshots a URL template is dry-run against.
const DryRunSample = 20

// labelName is the form of a Prometheus label name, which the scope label_value takes.
var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Matcher is one Matcher of a Link rule, in the Alertmanager syntax.
type Matcher struct {
	Label string `json:"label"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// Scope is the scope of a Link rule; Label is set for label_value only.
type Scope struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
}

// Rule is a Link rule as the API reads it.
type Rule struct {
	ID          int64
	PublicID    string
	Name        string
	Builtin     bool
	Matchers    []Matcher
	Scope       Scope
	URLTemplate string
	CreatedAt   time.Time
	Version     int64
	// compiled are its Matchers, compiled.
	compiled []matchers.Matcher
}

// RuleInput is what createLinkRule and updateLinkRule write.
type RuleInput struct {
	Name        string
	Matchers    []Matcher
	Scope       Scope
	URLTemplate string
}

// RulePage is a page of Link rules; Next, the id to continue after, is nil on the last page.
type RulePage struct {
	Rules []Rule
	Next  *int64
}

// ruleView is a Link rule as its Audit log diff shows it.
type ruleView struct {
	Name        string    `json:"name"`
	Matchers    []Matcher `json:"matchers"`
	Scope       Scope     `json:"scope"`
	URLTemplate string    `json:"url_template"`
}

func viewOfRule(r Rule) ruleView {
	return ruleView{Name: r.Name, Matchers: r.Matchers, Scope: r.Scope, URLTemplate: r.URLTemplate}
}

func ruleResource(r Rule) audit.Resource {
	return audit.Resource{Type: ResourceRule, PublicID: r.PublicID, Name: r.Name}
}

func ruleOf(r dbgen.GetLinkRuleRow) Rule {
	out := Rule{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Builtin: r.Builtin, Matchers: []Matcher{},
		Scope: Scope{Type: r.ScopeType, Label: r.ScopeLabel.String}, URLTemplate: r.UrlTemplate,
		CreatedAt: r.CreatedAt.UTC(), Version: r.Version}
	return out
}

// ListRules lists the Link rules in the order they were created.
func (s *Service) ListRules(ctx context.Context, f ListFilter) (RulePage, error) {
	limit := min(max(f.Limit, 1), 1000)
	p := dbgen.ListLinkRulesParams{OrgID: s.orgID, PageSize: int32(limit) + 1}
	if f.After != nil {
		p.AfterID = pgtype.Int8{Int64: *f.After, Valid: true}
	}
	rows, err := s.store.ListLinkRules(ctx, p)
	if err != nil {
		return RulePage{}, fmt.Errorf("list the link rules: %w", err)
	}
	var page RulePage
	for i, r := range rows {
		if i == limit {
			last := page.Rules[limit-1].ID
			page.Next = &last
			break
		}
		page.Rules = append(page.Rules, ruleOf(dbgen.GetLinkRuleRow(r)))
	}
	if err := s.withMatchers(ctx, s.store, page.Rules); err != nil {
		return RulePage{}, err
	}
	return page, nil
}

// allRules reads every Link rule with its compiled Matchers, in the order they were created.
func (s *Service) allRules(ctx context.Context, q Queries) ([]Rule, error) {
	rows, err := q.ListLinkRules(ctx, dbgen.ListLinkRulesParams{OrgID: s.orgID, PageSize: MaxRules})
	if err != nil {
		return nil, fmt.Errorf("read the link rules: %w", err)
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, ruleOf(dbgen.GetLinkRuleRow(r)))
	}
	if err := s.withMatchers(ctx, q, out); err != nil {
		return nil, err
	}
	return out, nil
}

// MaxRules is the most Link rules an Alert Group's links are computed from.
const MaxRules = 1000

// withMatchers reads and compiles the Matchers of the rules.
func (s *Service) withMatchers(ctx context.Context, q Queries, list []Rule) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	byID := make(map[int64]int, len(list))
	for i, r := range list {
		ids[i], byID[r.ID] = r.ID, i
	}
	rows, err := q.ListLinkRuleMatchers(ctx, dbgen.ListLinkRuleMatchersParams{OrgID: s.orgID, LinkRuleIds: ids})
	if err != nil {
		return fmt.Errorf("read the matchers of the link rules: %w", err)
	}
	for _, m := range rows {
		i, ok := byID[m.LinkRuleID]
		if !ok {
			continue
		}
		list[i].Matchers = append(list[i].Matchers, Matcher{Label: m.Label, Op: m.Op, Value: m.Value})
		c, err := matchers.New(m.Label, matchers.Op(m.Op), m.Value)
		if err != nil {
			return fmt.Errorf("compile a matcher of the link rule %s: %w", list[i].PublicID, err)
		}
		list[i].compiled = append(list[i].compiled, c)
	}
	return nil
}

// GetRule reads the Link rule publicID.
func (s *Service) GetRule(ctx context.Context, publicID string) (Rule, error) {
	return s.getRule(ctx, s.store, publicID)
}

func (s *Service) getRule(ctx context.Context, q Queries, publicID string) (Rule, error) {
	id, err := publicid.Parse(publicid.LinkRule, publicID)
	if err != nil {
		return Rule{}, ErrRuleNotFound
	}
	r, err := q.GetLinkRule(ctx, dbgen.GetLinkRuleParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, ErrRuleNotFound
	}
	if err != nil {
		return Rule{}, fmt.Errorf("read the link rule %s: %w", id, err)
	}
	list := []Rule{ruleOf(r)}
	if err := s.withMatchers(ctx, q, list); err != nil {
		return Rule{}, err
	}
	return list[0], nil
}

// lockRule locks the Link rule publicID for a change and reads it.
func (s *Service) lockRule(ctx context.Context, q Queries, publicID string) (Rule, error) {
	id, err := publicid.Parse(publicid.LinkRule, publicID)
	if err != nil {
		return Rule{}, ErrRuleNotFound
	}
	if _, err := q.LockLinkRule(ctx, dbgen.LockLinkRuleParams{OrgID: s.orgID, PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Rule{}, ErrRuleNotFound
		}
		return Rule{}, fmt.Errorf("lock the link rule %s: %w", id, err)
	}
	return s.getRule(ctx, q, id)
}

// checkRule refuses a RuleInput that is not valid: the name, the Matchers as a Route's, the scope — label_value with
// a label name, alert_group without — and a URL template that is not empty.
func checkRule(in RuleInput) ([]matchers.Matcher, error) {
	if err := checkName("/name", in.Name); err != nil {
		return nil, err
	}
	compiled := make([]matchers.Matcher, 0, len(in.Matchers))
	for i, m := range in.Matchers {
		pointer := "/matchers/" + strconv.Itoa(i)
		if m.Label == "" {
			return nil, &FieldError{Pointer: pointer + "/label", Code: CodeInvalidFormat,
				Detail: "The label name is empty."}
		}
		c, err := matchers.New(m.Label, matchers.Op(m.Op), m.Value)
		if err != nil {
			if re, ok := errors.AsType[*matchers.RegexpError](err); ok {
				return nil, &FieldError{Pointer: pointer + "/value", Code: CodeInvalidRegex, Detail: re.Error()}
			}
			return nil, &FieldError{Pointer: pointer + "/op", Code: CodeInvalidFormat,
				Detail: "The operator is one of =, !=, =~ and !~."}
		}
		compiled = append(compiled, c)
	}
	switch in.Scope.Type {
	case ScopeAlertGroup:
		if in.Scope.Label != "" {
			return nil, &FieldError{Pointer: "/scope/label", Code: CodeInvalidFormat,
				Detail: "The scope alert_group takes no label."}
		}
	case ScopeLabelValue:
		if !labelName.MatchString(in.Scope.Label) {
			return nil, &FieldError{Pointer: "/scope/label", Code: CodeInvalidFormat,
				Detail: "The scope label_value takes a label name: a letter or an underscore, then letters, digits " +
					"and underscores."}
		}
	default:
		return nil, &FieldError{Pointer: "/scope/type", Code: CodeInvalidFormat,
			Detail: "The scope is alert_group or label_value."}
	}
	if strings.TrimSpace(in.URLTemplate) == "" {
		return nil, &FieldError{Pointer: "/url_template", Code: CodeInvalidFormat, Detail: "The URL template is empty."}
	}
	return compiled, nil
}

// dryRun parses the URL template of in and renders it, as its scope renders it, against the most recent Stored
// Snapshots of the Organization that its Matchers match — template.dry_run_sample of them — or the built-in example
// when none does (C-12.FR-5, FR-9); the first failure is a FieldError at /url_template with its line and column.
func (s *Service) dryRun(ctx context.Context, in RuleInput, compiled []matchers.Matcher) error {
	if _, err := s.parse(in.URLTemplate); err != nil {
		return templateField(err)
	}
	samples, err := s.samples(ctx, compiled)
	if err != nil {
		return err
	}
	r := Rule{Name: in.Name, Scope: in.Scope, URLTemplate: in.URLTemplate}
	for _, sample := range samples {
		// Each sample is a render of its own, with its own lookups.
		if _, err := s.ruleLinks(r, s.newLookups(ctx, s.store).env(), sample.Data); err != nil {
			return templateField(err)
		}
	}
	return nil
}

// templateField is the error of a template at /url_template.
func templateField(err error) error {
	e, ok := errors.AsType[*templates.Error](err)
	if !ok {
		return err
	}
	return &FieldError{Pointer: "/url_template", Code: e.Code, Detail: e.Detail, Line: e.Line, Column: e.Column}
}

// samples are the most recent Stored Snapshots that are Alertmanager webhooks with Alerts and whose common labels the
// Matchers match, or the example.
func (s *Service) samples(ctx context.Context, compiled []matchers.Matcher) ([]Input, error) {
	days, err := s.store.GetLinkSnapshotRetention(ctx, s.orgID)
	if err != nil {
		return nil, fmt.Errorf("read retention.stored_snapshots: %w", err)
	}
	bodies, err := s.store.ListRecentSnapshots(ctx, dbgen.ListRecentSnapshotsParams{OrgID: s.orgID,
		NotBefore: s.business.Now().UTC().AddDate(0, 0, -int(days)), Lim: DryRunSample})
	if err != nil {
		return nil, fmt.Errorf("read the recent stored snapshots: %w", err)
	}
	var out []Input
	for _, b := range bodies {
		d, err := templates.FromWebhook(b)
		if err != nil || len(d.Alerts) == 0 {
			continue
		}
		in := s.Sample(d)
		if matchers.All(compiled, in.Data.CommonLabels) {
			out = append(out, in)
		}
	}
	if len(out) == 0 {
		out = []Input{s.example()}
	}
	return out, nil
}

// CreateRule creates a Link rule after its dry run. A name another rule has is ErrRuleNameTaken.
func (s *Service) CreateRule(ctx context.Context, r Requester, in RuleInput) (Rule, error) {
	in.Name = strings.TrimSpace(in.Name)
	compiled, err := checkRule(in)
	if err != nil {
		return Rule{}, err
	}
	if err := s.dryRun(ctx, in, compiled); err != nil {
		return Rule{}, err
	}
	var created Rule
	err = s.store.InTx(ctx, func(q Queries) error {
		id := publicid.New(publicid.LinkRule)
		ruleID, err := q.InsertLinkRule(ctx, dbgen.InsertLinkRuleParams{OrgID: s.orgID, PublicID: id, Name: in.Name,
			ScopeType: in.Scope.Type, ScopeLabel: scopeLabel(in.Scope), UrlTemplate: in.URLTemplate,
			Now: s.business.Now().UTC()})
		if err != nil {
			return fmt.Errorf("create the link rule: %w", nameTaken(err, ruleNameKey, ErrRuleNameTaken))
		}
		if err := s.writeMatchers(ctx, q, ruleID, in.Matchers); err != nil {
			return err
		}
		if created, err = s.getRule(ctx, q, id); err != nil {
			return err
		}
		return s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionRuleCreated, Resource: ruleResource(created), Diff: audit.Created(viewOfRule(created)),
			SourceAddress: r.Address})
	})
	if err != nil {
		return Rule{}, err
	}
	return created, nil
}

func scopeLabel(sc Scope) pgtype.Text {
	if sc.Type != ScopeLabelValue {
		return pgtype.Text{}
	}
	return pgtype.Text{String: sc.Label, Valid: true}
}

// writeMatchers writes the Matchers of the Link rule ruleID in their order.
func (s *Service) writeMatchers(ctx context.Context, q Queries, ruleID int64, ms []Matcher) error {
	for i, m := range ms {
		if err := q.InsertLinkRuleMatcher(ctx, dbgen.InsertLinkRuleMatcherParams{LinkRuleID: ruleID, OrgID: s.orgID,
			Position: int64(i), Label: m.Label, Op: m.Op, Value: m.Value}); err != nil {
			return fmt.Errorf("write the matchers of the link rule: %w", err)
		}
	}
	return nil
}

// UpdateRule replaces the fields of the Link rule publicID after the dry run of its URL template; a non-nil version
// must be its current one (If-Match). The built-in rule keeps its name and scope (ErrBuiltinImmutable).
func (s *Service) UpdateRule(ctx context.Context, r Requester, publicID string, version *int64, in RuleInput) (Rule,
	error) {
	in.Name = strings.TrimSpace(in.Name)
	compiled, err := checkRule(in)
	if err != nil {
		return Rule{}, err
	}
	if err := s.dryRun(ctx, in, compiled); err != nil {
		return Rule{}, err
	}
	var updated Rule
	err = s.store.InTx(ctx, func(q Queries) error {
		before, err := s.lockRule(ctx, q, publicID)
		if err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		if before.Builtin && (in.Name != before.Name || in.Scope != before.Scope) {
			return ErrBuiltinImmutable
		}
		next := before
		next.Name, next.Matchers, next.Scope, next.URLTemplate = in.Name, in.Matchers, in.Scope, in.URLTemplate
		if next.Matchers == nil {
			next.Matchers = []Matcher{}
		}
		changes := audit.Diff(viewOfRule(before), viewOfRule(next))
		if len(changes) == 0 {
			updated = before
			return nil
		}
		if err := q.UpdateLinkRule(ctx, dbgen.UpdateLinkRuleParams{OrgID: s.orgID, ID: before.ID, Name: next.Name,
			ScopeType: next.Scope.Type, ScopeLabel: scopeLabel(next.Scope), UrlTemplate: next.URLTemplate,
			Now: s.business.Now().UTC()}); err != nil {
			return fmt.Errorf("update the link rule %s: %w", before.PublicID,
				nameTaken(err, ruleNameKey, ErrRuleNameTaken))
		}
		if err := q.DeleteLinkRuleMatchers(ctx, dbgen.DeleteLinkRuleMatchersParams{OrgID: s.orgID,
			LinkRuleID: before.ID}); err != nil {
			return fmt.Errorf("replace the matchers of the link rule %s: %w", before.PublicID, err)
		}
		if err := s.writeMatchers(ctx, q, before.ID, next.Matchers); err != nil {
			return err
		}
		if updated, err = s.getRule(ctx, q, before.PublicID); err != nil {
			return err
		}
		return s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionRuleUpdated, Resource: ruleResource(updated), Diff: changes, SourceAddress: r.Address})
	})
	if err != nil {
		return Rule{}, err
	}
	return updated, nil
}

// DeleteRule deletes the Link rule publicID; a non-nil version must be its current one. The built-in rule is
// ErrBuiltinImmutable.
func (s *Service) DeleteRule(ctx context.Context, r Requester, publicID string, version *int64) error {
	return s.store.InTx(ctx, func(q Queries) error {
		before, err := s.lockRule(ctx, q, publicID)
		if err != nil {
			return err
		}
		if before.Builtin {
			return ErrBuiltinImmutable
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		if err := q.DeleteLinkRule(ctx, dbgen.DeleteLinkRuleParams{OrgID: s.orgID, ID: before.ID}); err != nil {
			return fmt.Errorf("delete the link rule %s: %w", before.PublicID, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionRuleDeleted, Resource: ruleResource(before),
			Diff: audit.Diff(viewOfRule(before), ruleView{}), SourceAddress: r.Address})
	})
}
