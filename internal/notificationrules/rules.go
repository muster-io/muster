// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package notificationrules holds the Notification rules of C-31: every User's normal and important rule, each an
// ordered list of actions — a Mention in the Thread, a call through a Notification gateway, a wait — within
// notification_rule.limits, and the four states of a User's rules. A User without rules of their own follows the
// Organization's current rules (organization.notification_rules), the state organization; "Call" and "Don't call"
// keep the Organization's rules as they were when the User chose them, with or without a call (journal D569); custom
// rules are sequences set through the API. Every change is recorded in the Audit log as notification_rules.updated with
// a before/after diff. A call action names its gateway by its public_id; one whose gateway was deleted, or at which
// the User has no confirmed contact any more, stays in the rule and reads unavailable.
package notificationrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/notificationrules/dbgen"
	"github.com/muster-io/muster/internal/publicid"
	usersdb "github.com/muster-io/muster/internal/users/dbgen"
)

// The kinds of an action.
const (
	KindMention = "mention"
	KindCall    = "call"
	KindWait    = "wait"
)

// The limits of a rule, notification_rule.limits (P-85): at most MaxActions actions, each wait a whole number of
// minutes from MinWaitSeconds to MaxWaitSeconds.
const (
	MaxActions     = 10
	MinWaitSeconds = 60
	MaxWaitSeconds = 3600
)

// The states of a User's rules: as in the Organization, "Call", "Don't call" and custom (C-31.FR-2).
const (
	StateOrganization = "organization"
	StateCall         = "call"
	StateNoCall       = "no_call"
	StateCustom       = "custom"
)

// Why a call action is unavailable: its gateway was deleted or is no Notification gateway any more, or the User has
// no confirmed contact at it.
const (
	ReasonGatewayDeleted     = "gateway_deleted"
	ReasonNoConfirmedContact = "no_confirmed_contact"
)

// The codes of field errors, from the catalogue of the API (x-problem-codes).
const (
	CodeRequired        = "required"
	CodeInvalidFormat   = "invalid_format"
	CodeOutOfRange      = "out_of_range"
	CodeUnknownID       = "unknown_id"
	CodeGatewayRequired = "gateway_required"
	CodeContactRequired = "contact_required"
)

// The Audit log action and resource type of a User's rules; the resource is named by the User.
const (
	ActionUpdated = "notification_rules.updated"
	ResourceType  = "notification_rules"
)

const statusDeleted = "deleted"

var (
	// ErrVersionMismatch is an If-Match that names another version of the rules.
	ErrVersionMismatch = errors.New("the notification rules changed since they were read")
	// ErrUserNotFound is a User that does not exist or is deleted.
	ErrUserNotFound = errors.New("no such user")
)

// FieldError is a field of an update that is not valid, at a JSON pointer of the request body.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// Action is one action of a rule as stored: a Mention, a call through the gateway GatewayID (a public_id), or a wait of
// WaitSeconds.
type Action struct {
	Kind        string `json:"kind"`
	GatewayID   string `json:"gateway_id,omitempty"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
}

// Set is a normal and an important rule.
type Set struct {
	Normal    []Action `json:"normal"`
	Important []Action `json:"important"`
}

// Defaults are the default rules of organization.notification_rules (C-31.FR-2): normal, a Mention; important, a
// Mention, a wait of 5 minutes and a Mention. Migration 0037 writes the same value as the column's default.
func Defaults() Set {
	return Set{
		Normal: []Action{{Kind: KindMention}},
		Important: []Action{{Kind: KindMention}, {Kind: KindWait, WaitSeconds: 300},
			{Kind: KindMention}},
	}
}

// ParseSet reads a Set as stored.
func ParseSet(raw []byte) (Set, error) {
	var s Set
	if err := json.Unmarshal(raw, &s); err != nil {
		return Set{}, fmt.Errorf("read the notification rules: %w", err)
	}
	if s.Normal == nil {
		s.Normal = []Action{}
	}
	if s.Important == nil {
		s.Important = []Action{}
	}
	return s, nil
}

// JSON is the Set as stored.
func (s Set) JSON() []byte {
	b, _ := json.Marshal(s) // strings and numbers only
	return b
}

func parseRule(raw []byte) ([]Action, error) {
	out := []Action{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("read a notification rule: %w", err)
	}
	return out, nil
}

func ruleJSON(r []Action) []byte {
	if r == nil {
		r = []Action{}
	}
	b, _ := json.Marshal(r) // strings and numbers only
	return b
}

// ActionInput is an action of a request body; fields its kind does not take are ignored.
type ActionInput struct {
	Kind        string
	GatewayID   *string
	WaitSeconds *int
}

// SetInput is the two rules of a request body; nil is a missing rule.
type SetInput struct {
	Normal    *[]ActionInput
	Important *[]ActionInput
}

// Gateway is a Bridge Connection that a call action may name, deleted ones included: Live while it is a Notification
// gateway — not deleted, its info declaring the role gateway — and Contact while the User has a confirmed contact
// there.
type Gateway struct {
	ID       int64
	PublicID string
	Name     string
	Live     bool
	Contact  bool
}

// Gateways are the Bridge Connections a call may name.
type Gateways []Gateway

func (g Gateways) byPublicID(id string) (Gateway, bool) {
	for _, x := range g {
		if x.PublicID == id {
			return x, true
		}
	}
	return Gateway{}, false
}

func (g Gateways) byID(id int64) (Gateway, bool) {
	for _, x := range g {
		if x.ID == id {
			return x, true
		}
	}
	return Gateway{}, false
}

func (g Gateways) live() []Gateway {
	var out []Gateway
	for _, x := range g {
		if x.Live {
			out = append(out, x)
		}
	}
	return out
}

// Check is what a validation checks a call action against: the gateways; with Contacts, that the User has a confirmed
// contact at the gateway, which the Organization's rules do not need (C-31.FR-4, C-20.FR-1). Kept are the stored rules
// of the same owner — the User's own row, never the Organization's rules they follow, or the Organization's own rules:
// as many calls through a gateway as Kept has, across both rules, pass even when their gateway was deleted or their
// contact removed, so that such an action stays in a rule saved again; a further call through it is checked.
type Check struct {
	Gateways Gateways
	Contacts bool
	Kept     Set
}

// Validate validates both rules of in, at the pointers prefix+"/normal" and prefix+"/important", and returns them as
// stored: a call without gateway_id names the only Notification gateway, and fields an action's kind does not take are
// dropped. A missing rule is required; more than MaxActions actions are out_of_range at the rule; a wait outside
// notification_rule.limits, or not a whole number of minutes, is out_of_range at its wait_seconds; a rule without a
// Mention or a call is required at the rule. A call while no Notification gateway exists is gateway_required at the
// action, without gateway_id while several exist required at its gateway_id, through a Connection that is no
// Notification gateway unknown_id there, and with Contacts through a gateway at which the User has no confirmed contact
// contact_required at the action.
func Validate(in SetInput, prefix string, c Check) (Set, *FieldError) {
	kept := keptCalls(c.Kept)
	normal, fe := validateRule(in.Normal, prefix+"/normal", c, kept)
	if fe != nil {
		return Set{}, fe
	}
	important, fe := validateRule(in.Important, prefix+"/important", c, kept)
	if fe != nil {
		return Set{}, fe
	}
	return Set{Normal: normal, Important: important}, nil
}

func validateRule(in *[]ActionInput, pointer string, c Check, kept map[string]int) ([]Action, *FieldError) {
	if in == nil {
		return nil, &FieldError{Pointer: pointer, Code: CodeRequired, Detail: "The rule is missing."}
	}
	if len(*in) > MaxActions {
		return nil, &FieldError{Pointer: pointer, Code: CodeOutOfRange,
			Detail: fmt.Sprintf("A rule has at most %d actions.", MaxActions)}
	}
	out := make([]Action, 0, len(*in))
	pages := false
	for i, a := range *in {
		at := pointer + "/" + strconv.Itoa(i)
		switch a.Kind {
		case KindMention:
			out = append(out, Action{Kind: KindMention})
			pages = true
		case KindWait:
			if a.WaitSeconds == nil {
				return nil, &FieldError{Pointer: at + "/wait_seconds", Code: CodeRequired,
					Detail: "A wait needs wait_seconds."}
			}
			w := *a.WaitSeconds
			if w < MinWaitSeconds || w > MaxWaitSeconds || w%60 != 0 {
				return nil, &FieldError{Pointer: at + "/wait_seconds", Code: CodeOutOfRange,
					Detail: "A wait is a whole number of minutes from 1 to 60: a multiple of 60 from 60 to 3600 seconds."}
			}
			out = append(out, Action{Kind: KindWait, WaitSeconds: w})
		case KindCall:
			gw, fe := callGateway(a.GatewayID, at, c)
			if fe != nil {
				norm, err := publicid.Parse(publicid.Connection, deref(a.GatewayID))
				if a.GatewayID == nil || err != nil || kept[norm] == 0 {
					return nil, fe
				}
				kept[norm]--
				gw = norm
			}
			out = append(out, Action{Kind: KindCall, GatewayID: gw})
			pages = true
		default:
			return nil, &FieldError{Pointer: at + "/kind", Code: CodeInvalidFormat,
				Detail: "An action is mention, call or wait."}
		}
	}
	if !pages {
		return nil, &FieldError{Pointer: pointer, Code: CodeRequired,
			Detail: "A rule has at least one Mention or call."}
	}
	return out, nil
}

// callGateway is the gateway a call action at the pointer at names.
func callGateway(id *string, at string, c Check) (string, *FieldError) {
	live := c.Gateways.live()
	if id == nil {
		switch len(live) {
		case 0:
			return "", gatewayRequired(at)
		case 1:
			return live[0].PublicID, contactOf(live[0], at, c)
		}
		return "", &FieldError{Pointer: at + "/gateway_id", Code: CodeRequired,
			Detail: "Several Notification gateways exist; name the one the call goes through."}
	}
	norm, err := publicid.Parse(publicid.Connection, *id)
	gw, ok := c.Gateways.byPublicID(norm)
	switch {
	case err == nil && ok && gw.Live:
		return gw.PublicID, contactOf(gw, at, c)
	case len(live) == 0:
		return "", gatewayRequired(at)
	}
	return "", &FieldError{Pointer: at + "/gateway_id", Code: CodeUnknownID,
		Detail: "No Notification gateway has this id."}
}

func gatewayRequired(pointer string) *FieldError {
	return &FieldError{Pointer: pointer, Code: CodeGatewayRequired,
		Detail: "A call needs a Notification gateway, a Bridge Connection in the role gateway; none exists."}
}

// contactOf is contact_required at pointer when c checks contacts and the User has no confirmed contact at gw.
func contactOf(gw Gateway, pointer string, c Check) *FieldError {
	if !c.Contacts || gw.Contact {
		return nil
	}
	return &FieldError{Pointer: pointer, Code: CodeContactRequired,
		Detail: "Confirm a contact at this Notification gateway before a call through it."}
}

// keptCalls counts the calls of the Set s per gateway, across both rules.
func keptCalls(s Set) map[string]int {
	out := map[string]int{}
	for _, r := range [][]Action{s.Normal, s.Important} {
		for _, a := range r {
			if a.Kind == KindCall {
				out[a.GatewayID]++
			}
		}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// GatewayRef names a gateway in a read.
type GatewayRef struct {
	PublicID string
	Name     string
}

// ActionView is an action as a read shows it: a call with its gateway, and whether it can run now and why not.
type ActionView struct {
	Action
	// Gateway is the gateway of a call; nil for a Connection that no longer exists, and for the other kinds.
	Gateway *GatewayRef
	// Available is false for a call whose gateway was deleted or is no Notification gateway any more
	// (ReasonGatewayDeleted), or at which the User has no confirmed contact (ReasonNoConfirmedContact).
	Available bool
	Reason    string
}

// SetView is a Set as a read shows it.
type SetView struct {
	Normal    []ActionView
	Important []ActionView
}

// Describe is the Set s as a read shows it, against the gateways gws; with contacts, a call through a gateway at which
// the User has no confirmed contact reads unavailable, which the Organization's rules never do.
func Describe(s Set, gws Gateways, contacts bool) SetView {
	return SetView{Normal: describeRule(s.Normal, gws, contacts), Important: describeRule(s.Important, gws, contacts)}
}

func describeRule(r []Action, gws Gateways, contacts bool) []ActionView {
	out := make([]ActionView, len(r))
	for i, a := range r {
		v := ActionView{Action: a, Available: true}
		if a.Kind == KindCall {
			gw, ok := gws.byPublicID(a.GatewayID)
			if ok {
				v.Gateway = &GatewayRef{PublicID: gw.PublicID, Name: gw.Name}
			}
			switch {
			case !ok || !gw.Live:
				v.Available, v.Reason = false, ReasonGatewayDeleted
			case contacts && !gw.Contact:
				v.Available, v.Reason = false, ReasonNoConfirmedContact
			}
		}
		out[i] = v
	}
	return out
}

// Rules are a User's rules as read: their state, the gateway of the state call, the sequences in effect and the
// version of If-Match (see ETagVersion).
type Rules struct {
	UserPublicID string
	State        string
	Gateway      *GatewayRef
	Rules        SetView
	Version      int64
}

// Update is a change of a User's rules: the state, with the gateway of the state call, nil for the only one at which
// the User has a confirmed contact, or with the custom sequences. Fields the state does not take are ignored.
type Update struct {
	State     string
	GatewayID *string
	Rules     SetInput
}

// Requester is who changes the rules and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// DBTX is the pool or transaction the queries run in.
type DBTX = dbgen.DBTX

// Pool is the main pool: the queries, and the transactions of a change.
type Pool interface {
	DBTX
	db.Beginner
}

// Queries are the queries of the package, with the insert of the Audit log.
type Queries interface {
	LockRulesUser(ctx context.Context, arg dbgen.LockRulesUserParams) (dbgen.LockRulesUserRow, error)
	GetRulesUser(ctx context.Context, arg dbgen.GetRulesUserParams) (dbgen.GetRulesUserRow, error)
	GetRulesUserByID(ctx context.Context, arg dbgen.GetRulesUserByIDParams) (dbgen.GetRulesUserByIDRow, error)
	GetOrganizationRules(ctx context.Context, orgID int64) (dbgen.GetOrganizationRulesRow, error)
	GetRules(ctx context.Context, arg dbgen.GetRulesParams) (dbgen.GetRulesRow, error)
	ListRuleGateways(ctx context.Context, arg dbgen.ListRuleGatewaysParams) ([]dbgen.ListRuleGatewaysRow, error)
	UpsertRules(ctx context.Context, arg dbgen.UpsertRulesParams) (int64, error)
	DeleteRules(ctx context.Context, arg dbgen.DeleteRulesParams) (int64, error)
	audit.Store
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(p Pool) Store {
	return pgStore{pgQueries: queriesOver(p), pool: p}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
}

func queriesOver(d DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d)}
}

type pgStore struct {
	pgQueries
	pool Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(queriesOver(tx)) })
}

// Config is what the Service needs.
type Config struct {
	OrgID int64
	Store Store
	Audit *audit.Writer
	// Business is the business clock: the time of a change.
	Business clock.Clock
}

// Service reads and changes the Notification rules of the Users of an Organization.
type Service struct {
	cfg Config
}

// New returns the Service of cfg.
func New(cfg Config) *Service {
	return &Service{cfg: cfg}
}

// user is the User whose rules are read or changed.
type user struct {
	ID       int64
	PublicID string
	Name     string
	Status   string
}

// stored are a User's rules as stored, or those of the Organization in the state organization.
type stored struct {
	state   string
	gateway int64
	set     Set
	version int64
}

// Get reads the rules of the User userID, the signed-in one (C-31.FR-4).
func (s *Service) Get(ctx context.Context, userID int64) (Rules, error) {
	u, err := s.cfg.Store.GetRulesUserByID(ctx, dbgen.GetRulesUserByIDParams{OrgID: s.cfg.OrgID, ID: userID})
	if err != nil {
		return Rules{}, userError(err)
	}
	return s.get(ctx, s.cfg.Store, user(u))
}

// GetOfUser reads the rules of the User an Admin names; a deleted User is ErrUserNotFound.
func (s *Service) GetOfUser(ctx context.Context, userPublicID string) (Rules, error) {
	u, err := s.lookup(ctx, s.cfg.Store, userPublicID)
	if err != nil {
		return Rules{}, err
	}
	return s.get(ctx, s.cfg.Store, u)
}

// Effective is the sequences in effect for the User userID, which the runs of the rules follow (C-31.FR-3): the
// Organization's current rules in the state organization, the stored ones otherwise.
func (s *Service) Effective(ctx context.Context, userID int64) (Set, error) {
	st, err := s.stored(ctx, s.cfg.Store, userID)
	if err != nil {
		return Set{}, err
	}
	return st.set, nil
}

// Update changes the rules of the User userID, the signed-in one, from their web session (C-31.FR-4, FR-5).
func (s *Service) Update(ctx context.Context, r Requester, userID int64, version *int64, in Update) (Rules, error) {
	return s.update(ctx, r, func(q Queries) (user, error) { return s.lock(ctx, q, userID) }, version, in)
}

// UpdateOfUser changes the rules of the User an Admin names (C-31.FR-4).
func (s *Service) UpdateOfUser(ctx context.Context, r Requester, userPublicID string, version *int64,
	in Update) (Rules, error) {
	return s.update(ctx, r, func(q Queries) (user, error) {
		u, err := s.lookup(ctx, q, userPublicID)
		if err != nil {
			return user{}, err
		}
		return s.lock(ctx, q, u.ID)
	}, version, in)
}

// Reset returns the User userID to the state organization, "Reset to the Organization's rules" (C-31.FR-2).
func (s *Service) Reset(ctx context.Context, r Requester, userID int64) (Rules, error) {
	return s.Update(ctx, r, userID, nil, Update{State: StateOrganization})
}

// RemoveUserRules deletes the rules of the User userID in the transaction tx of their deletion (C-31.FR-5,
// C-03.FR-13). It records nothing: the deletion of the User is in the Audit log. It returns nothing to run after the
// commit.
func (s *Service) RemoveUserRules(ctx context.Context, tx usersdb.DBTX, userID int64) (func(context.Context), error) {
	if _, err := dbgen.New(tx).DeleteRules(ctx, dbgen.DeleteRulesParams{OrgID: s.cfg.OrgID,
		UserID: userID}); err != nil {
		return nil, fmt.Errorf("delete the notification rules of the user %d: %w", userID, err)
	}
	return nil, nil
}

// update applies in to the rules of the User that who locks: the version of If-Match, nil for any, must be theirs;
// the state call and no_call are computed from the Organization's current rules and stored, the state organization
// deletes the stored rules. A change is recorded in the Audit log with its before/after diff; one that changes
// nothing writes nothing.
func (s *Service) update(ctx context.Context, r Requester, who func(Queries) (user, error), version *int64,
	in Update) (Rules, error) {
	var out Rules
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		u, err := who(q)
		if err != nil {
			return err
		}
		before, err := s.stored(ctx, q, u.ID)
		if err != nil {
			return err
		}
		if version != nil && *version != before.version {
			return ErrVersionMismatch
		}
		gws, err := s.gateways(ctx, q, u.ID)
		if err != nil {
			return err
		}
		after, err := s.apply(ctx, q, before, gws, in)
		if err != nil {
			return err
		}
		diff := audit.Diff(viewOf(before, gws), viewOf(after, gws))
		if len(diff) == 0 {
			out, err = s.get(ctx, q, u)
			return err
		}
		if after.state == StateOrganization {
			if _, err := q.DeleteRules(ctx, dbgen.DeleteRulesParams{OrgID: s.cfg.OrgID, UserID: u.ID}); err != nil {
				return fmt.Errorf("delete the notification rules of %s: %w", u.PublicID, err)
			}
		} else {
			if _, err := q.UpsertRules(ctx, dbgen.UpsertRulesParams{OrgID: s.cfg.OrgID, UserID: u.ID,
				State: after.state, GatewayConnectionID: pgtype.Int8{Int64: after.gateway, Valid: after.gateway != 0},
				Normal: ruleJSON(after.set.Normal), Important: ruleJSON(after.set.Important),
				Now: s.cfg.Business.Now().UTC()}); err != nil {
				return fmt.Errorf("store the notification rules of %s: %w", u.PublicID, err)
			}
		}
		if err := s.cfg.Audit.Record(ctx, q, audit.Entry{
			OrgID: s.cfg.OrgID, Actor: r.Actor, Transport: r.Transport, Action: ActionUpdated,
			Resource: audit.Resource{Type: ResourceType, PublicID: u.PublicID, Name: u.Name},
			Diff:     diff, SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		out, err = s.get(ctx, q, u)
		return err
	})
	if err != nil {
		return Rules{}, err
	}
	return out, nil
}

// apply is the rules after in, from before and the Organization's current rules.
func (s *Service) apply(ctx context.Context, q Queries, before stored, gws Gateways, in Update) (stored, error) {
	switch in.State {
	case StateOrganization:
		org, _, err := s.organization(ctx, q)
		return stored{state: StateOrganization, set: org}, err
	case StateCall:
		gw, fe := switchGateway(in.GatewayID, gws)
		if fe != nil {
			return stored{}, fe
		}
		org, _, err := s.organization(ctx, q)
		if err != nil {
			return stored{}, err
		}
		set, fe := WithCall(org, gw.PublicID)
		if fe != nil {
			return stored{}, fe
		}
		return stored{state: StateCall, gateway: gw.ID, set: set}, nil
	case StateNoCall:
		org, _, err := s.organization(ctx, q)
		return stored{state: StateNoCall, set: WithoutCalls(org)}, err
	case StateCustom:
		// Only the User's own stored calls are kept: those of the Organization's rules they follow are not theirs.
		var own Set
		if before.state != StateOrganization {
			own = before.set
		}
		set, fe := Validate(in.Rules, "", Check{Gateways: gws, Contacts: true, Kept: own})
		if fe != nil {
			return stored{}, fe
		}
		return stored{state: StateCustom, set: set}, nil
	}
	return stored{}, &FieldError{Pointer: "/state", Code: CodeInvalidFormat,
		Detail: "The state is organization, call, no_call or custom."}
}

// get reads the rules of u as a read shows them.
func (s *Service) get(ctx context.Context, q Queries, u user) (Rules, error) {
	st, err := s.stored(ctx, q, u.ID)
	if err != nil {
		return Rules{}, err
	}
	gws, err := s.gateways(ctx, q, u.ID)
	if err != nil {
		return Rules{}, err
	}
	out := Rules{UserPublicID: u.PublicID, State: st.state, Rules: Describe(st.set, gws, true), Version: st.version}
	if gw, ok := gws.byID(st.gateway); ok && st.gateway != 0 {
		out.Gateway = &GatewayRef{PublicID: gw.PublicID, Name: gw.Name}
	}
	return out, nil
}

// stored reads the stored rules of the User userID, or the Organization's current ones in the state organization.
func (s *Service) stored(ctx context.Context, q Queries, userID int64) (stored, error) {
	row, err := q.GetRules(ctx, dbgen.GetRulesParams{OrgID: s.cfg.OrgID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		org, version, err := s.organization(ctx, q)
		return stored{state: StateOrganization, set: org, version: OrganizationETagVersion(version)}, err
	}
	if err != nil {
		return stored{}, fmt.Errorf("read the notification rules: %w", err)
	}
	out := stored{state: row.State, gateway: row.GatewayConnectionID.Int64, version: RowETagVersion(row.Version)}
	if out.set.Normal, err = parseRule(row.Normal); err != nil {
		return stored{}, err
	}
	if out.set.Important, err = parseRule(row.Important); err != nil {
		return stored{}, err
	}
	return out, nil
}

// organization reads the Organization's current rules and the version of the organization resource.
func (s *Service) organization(ctx context.Context, q Queries) (Set, int64, error) {
	row, err := q.GetOrganizationRules(ctx, s.cfg.OrgID)
	if err != nil {
		return Set{}, 0, fmt.Errorf("read the organization's notification rules: %w", err)
	}
	set, err := ParseSet(row.NotificationRules)
	return set, row.Version, err
}

// RowETagVersion and OrganizationETagVersion are the version of If-Match, the number the ETag carries, of a User's
// rules: twice the version of their stored row, which comes from notification_rules_version_seq, or in the state
// organization, without a row, twice the version of the organization resource plus one. Even and odd never collide;
// the ETag of a stored row never repeats, and that of the state organization changes with every change of the
// Organization, whose rules the User follows.
func RowETagVersion(rowVersion int64) int64 { return 2 * rowVersion }

// OrganizationETagVersion is the version of If-Match of a User in the state organization; see RowETagVersion.
func OrganizationETagVersion(organizationVersion int64) int64 { return 2*organizationVersion + 1 }

// gateways lists the Bridge Connections a call may name, with the confirmed contacts of the User userID.
func (s *Service) gateways(ctx context.Context, q Queries, userID int64) (Gateways, error) {
	rows, err := q.ListRuleGateways(ctx, dbgen.ListRuleGatewaysParams{OrgID: s.cfg.OrgID, UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list the notification gateways: %w", err)
	}
	out := make(Gateways, len(rows))
	for i, r := range rows {
		out[i] = Gateway(r)
	}
	return out, nil
}

// lookup reads the User an Admin names; a deleted or unknown one is ErrUserNotFound.
func (s *Service) lookup(ctx context.Context, q Queries, publicID string) (user, error) {
	norm, err := publicid.Parse(publicid.User, publicID)
	if err != nil {
		return user{}, ErrUserNotFound
	}
	u, err := q.GetRulesUser(ctx, dbgen.GetRulesUserParams{OrgID: s.cfg.OrgID, PublicID: norm})
	if err != nil {
		return user{}, userError(err)
	}
	if u.Status == statusDeleted {
		return user{}, ErrUserNotFound
	}
	return user(u), nil
}

// lock locks the User userID whose rules change; a deleted one is ErrUserNotFound.
func (s *Service) lock(ctx context.Context, q Queries, userID int64) (user, error) {
	u, err := q.LockRulesUser(ctx, dbgen.LockRulesUserParams{OrgID: s.cfg.OrgID, ID: userID})
	if err != nil {
		return user{}, userError(err)
	}
	if u.Status == statusDeleted {
		return user{}, ErrUserNotFound
	}
	return user(u), nil
}

func userError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	return fmt.Errorf("read the user: %w", err)
}

// view is what the Audit log diff of a User's rules shows: the state, the gateway of the state call and both
// sequences in effect, those of the Organization in the state organization.
type view struct {
	State     string   `json:"state"`
	GatewayID *string  `json:"gateway_id"`
	Normal    []Action `json:"normal"`
	Important []Action `json:"important"`
}

func viewOf(st stored, gws Gateways) view {
	v := view{State: st.state, Normal: st.set.Normal, Important: st.set.Important}
	if gw, ok := gws.byID(st.gateway); ok && st.gateway != 0 {
		v.GatewayID = &gw.PublicID
	}
	return v
}
