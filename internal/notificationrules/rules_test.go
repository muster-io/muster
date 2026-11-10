// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package notificationrules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/audit"
	adb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/notificationrules/dbgen"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

const (
	orgID = 7
	petr  = 1
	ivan  = 2
	gone  = 3
	gwA   = "CN0000000000GA"
	gwB   = "CN0000000000GB"
)

// fakeUser is a User of the fake.
type fakeUser struct {
	publicID, name, status string
}

// fakeRow is a stored rules row.
type fakeRow struct {
	state             string
	gateway           int64
	normal, important []byte
	version           int64
}

// fakeStore is the Users, the rules, the Organization's rules and the gateways of one Organization in memory, with
// the semantics of query.sql; a transaction that fails is rolled back. fail makes the named query fail.
type fakeStore struct {
	users    map[int64]fakeUser
	rows     map[int64]fakeRow
	org      []byte
	orgVer   int64 // the version of the organization resource
	gateways []dbgen.ListRuleGatewaysRow
	contacts map[int64][]int64 // the gateways at which each User has a confirmed contact
	audits   []adb.InsertAuditEntryParams
	seq      int64
	locks    []int64
	fail     map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users: map[int64]fakeUser{petr: {"SR0000000000P1", "Petr", "active"}, ivan: {"SR0000000000P2", "Ivan", "active"},
			gone: {"SR0000000000P3", "deleted-user-SR0000000000P3", statusDeleted}},
		rows: map[int64]fakeRow{}, org: Defaults().JSON(), orgVer: 1,
		gateways: []dbgen.ListRuleGatewaysRow{{ID: 10, PublicID: gwA, Name: "Gateway A", Live: true}},
		contacts: map[int64][]int64{petr: {10}}, fail: map[string]error{},
	}
}

func (f *fakeStore) copyOf() *fakeStore {
	c := *f
	c.users, c.rows, c.contacts = map[int64]fakeUser{}, map[int64]fakeRow{}, map[int64][]int64{}
	for k, v := range f.users {
		c.users[k] = v
	}
	for k, v := range f.rows {
		c.rows[k] = v
	}
	for k, v := range f.contacts {
		c.contacts[k] = v
	}
	c.gateways = append([]dbgen.ListRuleGatewaysRow{}, f.gateways...)
	c.audits = append([]adb.InsertAuditEntryParams{}, f.audits...)
	return &c
}

func (f *fakeStore) InTx(_ context.Context, fn func(Queries) error) error {
	if err := f.fail["InTx"]; err != nil {
		return err
	}
	before := f.copyOf()
	if err := fn(f); err != nil {
		*f = *before
		return err
	}
	return nil
}

func (f *fakeStore) user(name string, org, id int64) (fakeUser, error) {
	if err := f.fail[name]; err != nil {
		return fakeUser{}, err
	}
	u, ok := f.users[id]
	if !ok || org != orgID {
		return fakeUser{}, pgx.ErrNoRows
	}
	return u, nil
}

func (f *fakeStore) LockRulesUser(_ context.Context, arg dbgen.LockRulesUserParams) (dbgen.LockRulesUserRow, error) {
	u, err := f.user("LockRulesUser", arg.OrgID, arg.ID)
	f.locks = append(f.locks, arg.ID)
	return dbgen.LockRulesUserRow{ID: arg.ID, PublicID: u.publicID, Name: u.name, Status: u.status}, err
}

func (f *fakeStore) GetRulesUser(_ context.Context, arg dbgen.GetRulesUserParams) (dbgen.GetRulesUserRow, error) {
	if err := f.fail["GetRulesUser"]; err != nil {
		return dbgen.GetRulesUserRow{}, err
	}
	for id, u := range f.users {
		if u.publicID == arg.PublicID && arg.OrgID == orgID {
			return dbgen.GetRulesUserRow{ID: id, PublicID: u.publicID, Name: u.name, Status: u.status}, nil
		}
	}
	return dbgen.GetRulesUserRow{}, pgx.ErrNoRows
}

func (f *fakeStore) GetRulesUserByID(_ context.Context, arg dbgen.GetRulesUserByIDParams) (dbgen.GetRulesUserByIDRow,
	error) {
	u, err := f.user("GetRulesUserByID", arg.OrgID, arg.ID)
	return dbgen.GetRulesUserByIDRow{ID: arg.ID, PublicID: u.publicID, Name: u.name, Status: u.status}, err
}

func (f *fakeStore) GetOrganizationRules(_ context.Context, org int64) (dbgen.GetOrganizationRulesRow, error) {
	if err := f.fail["GetOrganizationRules"]; err != nil || org != orgID {
		return dbgen.GetOrganizationRulesRow{}, errors.Join(err, pgx.ErrNoRows)
	}
	return dbgen.GetOrganizationRulesRow{NotificationRules: f.org, Version: f.orgVer}, nil
}

func (f *fakeStore) GetRules(_ context.Context, arg dbgen.GetRulesParams) (dbgen.GetRulesRow, error) {
	if err := f.fail["GetRules"]; err != nil {
		return dbgen.GetRulesRow{}, err
	}
	r, ok := f.rows[arg.UserID]
	if !ok || arg.OrgID != orgID {
		return dbgen.GetRulesRow{}, pgx.ErrNoRows
	}
	out := dbgen.GetRulesRow{State: r.state, Normal: r.normal, Important: r.important, Version: r.version}
	out.GatewayConnectionID.Int64, out.GatewayConnectionID.Valid = r.gateway, r.gateway != 0
	return out, nil
}

func (f *fakeStore) ListRuleGateways(_ context.Context, arg dbgen.ListRuleGatewaysParams) (
	[]dbgen.ListRuleGatewaysRow, error) {
	if err := f.fail["ListRuleGateways"]; err != nil || arg.OrgID != orgID {
		return nil, errors.Join(err, errors.New("another organization"))
	}
	out := make([]dbgen.ListRuleGatewaysRow, len(f.gateways))
	for i, g := range f.gateways {
		out[i] = g
		for _, c := range f.contacts[arg.UserID] {
			if c == g.ID {
				out[i].Contact = true
			}
		}
	}
	return out, nil
}

func (f *fakeStore) UpsertRules(_ context.Context, arg dbgen.UpsertRulesParams) (int64, error) {
	if err := f.fail["UpsertRules"]; err != nil {
		return 0, err
	}
	if (arg.State == StateCall) != arg.GatewayConnectionID.Valid {
		return 0, &pgconn.PgError{Code: "23514", ConstraintName: "notification_rules_gateway_check"}
	}
	f.seq++
	f.rows[arg.UserID] = fakeRow{state: arg.State, gateway: arg.GatewayConnectionID.Int64, normal: arg.Normal,
		important: arg.Important, version: f.seq}
	return f.seq, nil
}

func (f *fakeStore) DeleteRules(_ context.Context, arg dbgen.DeleteRulesParams) (int64, error) {
	if err := f.fail["DeleteRules"]; err != nil {
		return 0, err
	}
	if _, ok := f.rows[arg.UserID]; !ok || arg.OrgID != orgID {
		return 0, nil
	}
	delete(f.rows, arg.UserID)
	return 1, nil
}

func (f *fakeStore) InsertAuditEntry(_ context.Context, arg adb.InsertAuditEntryParams) error {
	if err := f.fail["InsertAuditEntry"]; err != nil {
		return err
	}
	f.audits = append(f.audits, arg)
	return nil
}

func newService(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	f := newFakeStore()
	c := clock.NewManual(t0)
	return New(Config{OrgID: orgID, Store: f, Audit: audit.NewWriter(logging.New(&bytes.Buffer{},
		logging.LevelInfo), c), Business: c}), f
}

var by = Requester{Actor: audit.User(9, "SR0000000000AD"), Transport: audit.TransportAPI,
	Address: netip.MustParseAddr("192.0.2.1")}

func mention() ActionInput { return ActionInput{Kind: KindMention} }

func wait(seconds int) ActionInput { return ActionInput{Kind: KindWait, WaitSeconds: &seconds} }

func call(gw string) ActionInput {
	if gw == "" {
		return ActionInput{Kind: KindCall}
	}
	return ActionInput{Kind: KindCall, GatewayID: &gw}
}

func rule(actions ...ActionInput) *[]ActionInput { return &actions }

// kinds is a rule as its kinds, waits by their seconds and calls by their gateway: "mention,300,call GA".
func kinds(r []ActionView) string {
	parts := make([]string, len(r))
	for i, a := range r {
		switch a.Kind {
		case KindWait:
			parts[i] = strconv.Itoa(a.WaitSeconds)
		case KindCall:
			parts[i] = "call " + strings.TrimPrefix(a.GatewayID, "CN0000000000")
			if !a.Available {
				parts[i] += " " + a.Reason
			}
		default:
			parts[i] = a.Kind
		}
	}
	return strings.Join(parts, ",")
}

func fieldError(t *testing.T, err error) string {
	t.Helper()
	fe, ok := errors.AsType[*FieldError](err)
	if !ok {
		t.Fatalf("not a field error: %v", err)
	}
	return fe.Pointer + " " + fe.Code
}

// TestValidate is C-31.FR-1 and notification_rule.limits (P-85): a rule is an ordered list of mention, call and wait
// with at least one Mention or call and at most 10 actions; a wait is a whole number of minutes from 1 to 60, as
// wait_seconds a multiple of 60 from 60 to 3600; a call names its gateway, which may be left out while one Notification
// gateway exists, and with Contacts needs a confirmed contact there; fields a kind does not take are dropped.
func TestValidate(t *testing.T) {
	one := Gateways{{ID: 10, PublicID: gwA, Name: "Gateway A", Live: true, Contact: true}}
	two := append(Gateways{{ID: 11, PublicID: gwB, Name: "Gateway B", Live: true}}, one...)
	ok, err := Validate(SetInput{Normal: rule(ActionInput{Kind: KindMention, GatewayID: new(gwA)}),
		Important: rule(call(""), mention(), wait(300), ActionInput{Kind: KindWait, WaitSeconds: new(3600),
			GatewayID: new(gwB)}, call("cn0000000000ga"))}, "", Check{Gateways: one, Contacts: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(ok.JSON()); got != `{"normal":[{"kind":"mention"}],"important":[{"kind":"call","gateway_id":`+
		`"CN0000000000GA"},{"kind":"mention"},{"kind":"wait","wait_seconds":300},{"kind":"wait","wait_seconds":3600},`+
		`{"kind":"call","gateway_id":"CN0000000000GA"}]}` {
		t.Errorf("stored %s", got)
	}
	eleven := make([]ActionInput, 11)
	for i := range eleven {
		eleven[i] = mention()
	}
	ten := eleven[:10]
	if _, err := Validate(SetInput{Normal: &ten, Important: rule(mention(), wait(60))}, "", Check{}); err != nil {
		t.Errorf("10 actions and a wait of 1 minute: %v", err)
	}
	for _, c := range []struct {
		name      string
		normal    *[]ActionInput
		important *[]ActionInput
		check     Check
		want      string
	}{
		{"11 actions", &eleven, rule(mention()), Check{}, "/normal out_of_range"},
		{"0 seconds", rule(mention(), wait(0)), rule(mention()), Check{}, "/normal/1/wait_seconds out_of_range"},
		{"90 seconds", rule(mention()), rule(mention(), wait(90)), Check{}, "/important/1/wait_seconds out_of_range"},
		{"61 minutes", rule(mention()), rule(wait(3660), mention()), Check{}, "/important/0/wait_seconds out_of_range"},
		{"no wait_seconds", rule(mention(), ActionInput{Kind: KindWait}), rule(mention()), Check{},
			"/normal/1/wait_seconds required"},
		{"only waits", rule(wait(60), wait(120)), rule(mention()), Check{}, "/normal required"},
		{"empty", rule(), rule(mention()), Check{}, "/normal required"},
		{"missing", rule(mention()), nil, Check{}, "/important required"},
		{"unknown kind", rule(ActionInput{Kind: "sms"}), rule(mention()), Check{}, "/normal/0/kind invalid_format"},
		{"no gateway", rule(call("")), rule(mention()), Check{}, "/normal/0 gateway_required"},
		{"no gateway by id", rule(mention()), rule(call(gwA)), Check{}, "/important/0 gateway_required"},
		{"two gateways", rule(call("")), rule(mention()), Check{Gateways: two}, "/normal/0/gateway_id required"},
		{"not a gateway", rule(call("CN0000000000ZZ")), rule(mention()), Check{Gateways: one},
			"/normal/0/gateway_id unknown_id"},
		{"not an id", rule(call("nope")), rule(mention()), Check{Gateways: one}, "/normal/0/gateway_id unknown_id"},
		{"no contact", rule(mention()), rule(mention(), call(gwB)), Check{Gateways: two, Contacts: true},
			"/important/1 contact_required"},
		{"no contact, only one", rule(call("")), rule(mention()), Check{Gateways: Gateways{{ID: 11, PublicID: gwB,
			Live: true}}, Contacts: true}, "/normal/0 contact_required"},
	} {
		_, fe := Validate(SetInput{Normal: c.normal, Important: c.important}, "", c.check)
		if fe == nil || fe.Pointer+" "+fe.Code != c.want {
			t.Errorf("%s: %v, want %s", c.name, fe, c.want)
		}
	}
	if fe := (&FieldError{Pointer: "/normal", Detail: "x"}); fe.Error() != "/normal: x" {
		t.Errorf("Error() = %q", fe.Error())
	}

	// The Organization's rules need no contact; a call whose gateway the stored rules name stays.
	if _, fe := Validate(SetInput{Normal: rule(call(gwB)), Important: rule(mention())}, "/notification_rules",
		Check{Gateways: two}); fe != nil {
		t.Errorf("an Organization's call: %v", fe)
	}
	deleted := Gateways{{ID: 10, PublicID: gwA, Live: false}}
	kept := Set{Normal: []Action{{Kind: KindMention}}, Important: []Action{{Kind: KindCall, GatewayID: gwA}}}
	if _, fe := Validate(SetInput{Normal: rule(call(gwA)), Important: rule(mention())}, "",
		Check{Gateways: deleted, Contacts: true, Kept: kept}); fe != nil {
		t.Errorf("a kept call: %v", fe)
	}
	if _, fe := Validate(SetInput{Normal: rule(call(gwA)), Important: rule(mention())}, "",
		Check{Gateways: deleted, Contacts: true}); fe == nil || fe.Code != CodeGatewayRequired {
		t.Errorf("a call of a deleted gateway, not kept: %v", fe)
	}
}

// TestDescribe is C-31.FR-1: an action whose gateway was deleted, or at which the User no longer has a confirmed
// contact, stays in the rule and reads available false with gateway_deleted or no_confirmed_contact; the
// Organization's rules never read no_confirmed_contact.
func TestDescribe(t *testing.T) {
	s := Set{Normal: []Action{{Kind: KindMention}}, Important: []Action{{Kind: KindCall, GatewayID: gwA},
		{Kind: KindCall, GatewayID: gwB}, {Kind: KindCall, GatewayID: "CN0000000000ZZ"}, {Kind: KindWait,
			WaitSeconds: 60}}}
	gws := Gateways{{ID: 10, PublicID: gwA, Name: "A", Live: true}, {ID: 11, PublicID: gwB, Name: "B"}}
	v := Describe(s, gws, true)
	if got := kinds(v.Important); got != "call GA no_confirmed_contact,call GB gateway_deleted,"+
		"call ZZ gateway_deleted,60" {
		t.Errorf("a User's = %s", got)
	}
	if v.Important[0].Gateway == nil || v.Important[0].Gateway.Name != "A" || v.Important[2].Gateway != nil ||
		v.Important[3].Gateway != nil || !v.Normal[0].Available {
		t.Errorf("views %+v", v)
	}
	if got := kinds(Describe(s, gws, false).Important); got != "call GA,call GB gateway_deleted,call ZZ gateway_deleted,60" {
		t.Errorf("the Organization's = %s", got)
	}
	gws[0].Contact = true
	if got := kinds(Describe(s, gws, true).Important[:1]); got != "call GA" {
		t.Errorf("with a contact = %s", got)
	}
	if _, err := ParseSet([]byte(`[`)); err == nil {
		t.Error("ParseSet of no JSON")
	}
	if p, err := ParseSet([]byte(`{}`)); err != nil || p.Normal == nil || p.Important == nil {
		t.Errorf("ParseSet of {} = %+v, %v", p, err)
	}
}

// audited is the diff of the Audit log entry i and its resource.
func audited(t *testing.T, f *fakeStore, i int) (string, string) {
	t.Helper()
	if len(f.audits) <= i {
		t.Fatalf("%d entries", len(f.audits))
	}
	a := f.audits[i]
	if a.Action != ActionUpdated || a.ResourceType.String != ResourceType || a.OrgID != orgID {
		t.Errorf("entry %+v", a)
	}
	return string(a.Diff), a.ResourcePublicID.String + " " + a.ResourceName.String
}

// TestStates is C-31.FR-2, FR-4, FR-5, C-31.AC-3, AC-4 and AC-6 against the fake: a User without rules reads the
// state organization with the Organization's current rules and follows a change of them; reset brings back that state
// from call and custom; an Admin's custom sequences read custom; If-Match is checked; every change is an Audit log
// entry notification_rules.updated with a before/after diff, one that changes nothing writes nothing; a deleted User
// is not found; deleting a User's rules returns them to the Organization's.
func TestStates(t *testing.T) {
	svc, f := newService(t)
	ctx := t.Context()
	r, err := svc.Get(ctx, ivan)
	if err != nil || r.State != StateOrganization || r.Version != 3 || r.Gateway != nil ||
		kinds(r.Rules.Normal) != "mention" || kinds(r.Rules.Important) != "mention,300,mention" ||
		r.UserPublicID != "SR0000000000P2" {
		t.Fatalf("ivan = %+v, %v", r, err)
	}

	// call: the important rule a call through the gateway at the same moment as the first Mention.
	r, err = svc.Update(ctx, by, petr, new(int64(3)), Update{State: StateCall})
	if err != nil || r.State != StateCall || r.Gateway == nil || r.Gateway.PublicID != gwA ||
		kinds(r.Rules.Important) != "call GA,mention,300,mention" || kinds(r.Rules.Normal) != "mention" ||
		r.Version != 2 {
		t.Fatalf("call = %+v, %v", r, err)
	}
	diff, res := audited(t, f, 0)
	if res != "SR0000000000P1 Petr" || diff != `[{"pointer":"/state","before":"organization","after":"call"},`+
		`{"pointer":"/gateway_id","after":"CN0000000000GA"},{"pointer":"/important","before":`+
		`[{"kind":"mention"},{"kind":"wait","wait_seconds":300},{"kind":"mention"}],"after":[{"kind":"call",`+
		`"gateway_id":"CN0000000000GA"},{"kind":"mention"},{"kind":"wait","wait_seconds":300},{"kind":"mention"}]}]` {
		t.Errorf("the entry of call: %s %s", res, diff)
	}
	if _, err := svc.Update(ctx, by, petr, new(int64(3)), Update{State: StateNoCall}); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("a stale If-Match: %v", err)
	}
	if _, err := svc.Update(ctx, by, petr, nil, Update{State: StateCall}); err != nil || len(f.audits) != 1 {
		t.Errorf("the same state again: %v, %d entries", err, len(f.audits))
	}

	// D569: the Organization changes its important rule; petr keeps his, ivan follows at once.
	f.org = []byte(`{"normal":[{"kind":"mention"}],"important":[{"kind":"mention"},{"kind":"wait",` +
		`"wait_seconds":600},{"kind":"mention"}]}`)
	if r, _ := svc.Get(ctx, ivan); kinds(r.Rules.Important) != "mention,600,mention" {
		t.Errorf("ivan after the change = %s", kinds(r.Rules.Important))
	}
	if r, _ := svc.Get(ctx, petr); kinds(r.Rules.Important) != "call GA,mention,300,mention" {
		t.Errorf("petr after the change = %s", kinds(r.Rules.Important))
	}
	if set, err := svc.Effective(ctx, ivan); err != nil || len(set.Important) != 3 ||
		set.Important[1].WaitSeconds != 600 {
		t.Errorf("Effective(ivan) = %+v, %v", set, err)
	}

	// A removed contact: the call stays and reads unavailable.
	f.contacts[petr] = nil
	if r, _ := svc.Get(ctx, petr); kinds(r.Rules.Important) != "call GA no_confirmed_contact,mention,300,mention" {
		t.Errorf("without the contact = %s", kinds(r.Rules.Important))
	}
	f.contacts[petr] = []int64{10}

	// An Admin sets three Mentions: custom.
	r, err = svc.UpdateOfUser(ctx, by, "sr0000000000p1", &r.Version, Update{State: StateCustom,
		Rules: SetInput{Normal: rule(mention(), mention(), mention()), Important: rule(mention())}})
	if err != nil || r.State != StateCustom || r.Gateway != nil || kinds(r.Rules.Normal) != "mention,mention,mention" {
		t.Fatalf("custom = %+v, %v", r, err)
	}
	if got, err := svc.GetOfUser(ctx, "SR0000000000P1"); err != nil || got.State != StateCustom ||
		got.Version != r.Version {
		t.Errorf("GetOfUser = %+v, %v", got, err)
	}
	diff, _ = audited(t, f, 1)
	if !strings.HasPrefix(diff, `[{"pointer":"/state","before":"call","after":"custom"},{"pointer":"/gateway_id",`+
		`"before":"CN0000000000GA"},{"pointer":"/normal","before":[{"kind":"mention"}],"after":`) {
		t.Errorf("the entry of custom: %s", diff)
	}
	_, err = svc.UpdateOfUser(ctx, by, "SR0000000000P1", nil, Update{State: StateCustom,
		Rules: SetInput{Normal: rule(mention(), wait(90)), Important: rule(mention())}})
	if got := fieldError(t, err); got != "/normal/1/wait_seconds out_of_range" {
		t.Errorf("a wait of 90 s: %s", got)
	}
	_, err = svc.UpdateOfUser(ctx, by, "SR0000000000P1", nil, Update{State: StateCustom,
		Rules: SetInput{Normal: rule(mention())}})
	if got := fieldError(t, err); got != "/important required" {
		t.Errorf("custom without important: %s", got)
	}

	// Reset brings back the Organization's current rules.
	r, err = svc.Reset(ctx, by, petr)
	if err != nil || r.State != StateOrganization || r.Version != 3 || kinds(r.Rules.Important) != "mention,600,mention" {
		t.Fatalf("reset = %+v, %v", r, err)
	}
	if _, ok := f.rows[petr]; ok || len(f.audits) != 3 {
		t.Errorf("after the reset: row %v, %d entries", f.rows[petr], len(f.audits))
	}
	if _, err := svc.Reset(ctx, by, petr); err != nil || len(f.audits) != 3 {
		t.Errorf("reset again: %v, %d entries", err, len(f.audits))
	}

	// A deleted User, and one of no Organization.
	for name, err := range map[string]error{
		"get":     second(svc.GetOfUser(ctx, "SR0000000000P3")),
		"update":  second(svc.UpdateOfUser(ctx, by, "SR0000000000P3", nil, Update{State: StateNoCall})),
		"unknown": second(svc.GetOfUser(ctx, "SR0000000000P9")),
		"not id":  second(svc.GetOfUser(ctx, "nope")),
		"mine":    second(svc.Update(ctx, by, gone, nil, Update{State: StateNoCall})),
		"by id":   second(svc.Get(ctx, 99)),
	} {
		if !errors.Is(err, ErrUserNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.Update(ctx, by, petr, nil, Update{State: "sometimes"}); fieldError(t, err) != "/state invalid_format" {
		t.Errorf("an unknown state: %v", err)
	}

	// The User's deletion deletes their rules.
	if _, err := svc.Update(ctx, by, ivan, nil, Update{State: StateNoCall}); err != nil {
		t.Fatal(err)
	}
	if after, err := svc.RemoveUserRules(ctx, fakeTx{f}, ivan); err != nil || after != nil {
		t.Errorf("RemoveUserRules = %v", err)
	}
}

func second[T any](_ T, err error) error { return err }

// fakeTx is the transaction of a User's deletion over the fake: it runs DELETE FROM notification_rules.
type fakeTx struct{ f *fakeStore }

func (x fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(sql, "DELETE FROM notification_rules") || args[0] != int64(orgID) {
		return pgconn.CommandTag{}, errors.New("unexpected " + sql)
	}
	if err := x.f.fail["DeleteRules"]; err != nil {
		return pgconn.CommandTag{}, err
	}
	delete(x.f.rows, args[1].(int64))
	return pgconn.NewCommandTag("DELETE 1"), nil
}

func (fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("no query")
}

func (fakeTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

// TestRulesFailures: a failing query fails the read or the change and writes nothing.
func TestRulesFailures(t *testing.T) {
	for _, name := range []string{"InTx", "LockRulesUser", "GetRules", "GetOrganizationRules", "ListRuleGateways",
		"UpsertRules", "InsertAuditEntry"} {
		svc, f := newService(t)
		f.fail[name] = errors.New("db down")
		if _, err := svc.Update(t.Context(), by, petr, nil, Update{State: StateCall}); err == nil ||
			errors.Is(err, ErrUserNotFound) || len(f.rows) != 0 || len(f.audits) != 0 {
			t.Errorf("%s: %v, %d rows", name, err, len(f.rows))
		}
	}
	for _, name := range []string{"GetRulesUserByID", "GetRules", "GetOrganizationRules", "ListRuleGateways"} {
		svc, f := newService(t)
		f.fail[name] = errors.New("db down")
		if _, err := svc.Get(t.Context(), petr); err == nil || errors.Is(err, ErrUserNotFound) {
			t.Errorf("Get with %s failing: %v", name, err)
		}
	}
	svc, f := newService(t)
	f.fail["GetRulesUser"] = errors.New("db down")
	if _, err := svc.GetOfUser(t.Context(), "SR0000000000P1"); err == nil {
		t.Error("GetOfUser without the user")
	}
	if _, err := svc.UpdateOfUser(t.Context(), by, "SR0000000000P1", nil, Update{State: StateNoCall}); err == nil {
		t.Error("UpdateOfUser without the user")
	}
	svc, f = newService(t)
	if _, err := svc.Update(t.Context(), by, petr, nil, Update{State: StateNoCall}); err != nil {
		t.Fatal(err)
	}
	f.fail["DeleteRules"] = errors.New("db down")
	if _, err := svc.Reset(t.Context(), by, petr); err == nil || len(f.rows) != 1 {
		t.Errorf("a failing reset: %v", err)
	}
	if _, err := svc.RemoveUserRules(t.Context(), fakeTx{f}, petr); err == nil {
		t.Error("a failing RemoveUserRules")
	}
	f.fail = map[string]error{}
	f.rows[petr] = fakeRow{state: StateCustom, normal: []byte(`{`), important: []byte(`[]`)}
	if _, err := svc.Get(t.Context(), petr); err == nil {
		t.Error("a broken normal rule")
	}
	f.rows[petr] = fakeRow{state: StateCustom, normal: []byte(`[]`), important: []byte(`{`)}
	if _, err := svc.Effective(t.Context(), petr); err == nil {
		t.Error("a broken important rule")
	}
	f.org = []byte(`[`)
	if _, err := svc.Effective(t.Context(), ivan); err == nil {
		t.Error("broken Organization rules")
	}
	if _, err := svc.Update(t.Context(), by, ivan, nil, Update{State: StateCall}); err == nil {
		t.Error("call over broken Organization rules")
	}
	var stored Set
	if err := json.Unmarshal(Defaults().JSON(), &stored); err != nil || len(stored.Important) != 3 {
		t.Errorf("Defaults = %+v, %v", stored, err)
	}
}

// TestETagVersions is the ETag of a User's rules: twice the version of their stored row, or in the state organization
// twice the version of the organization resource plus one, so that the two never collide, a stored row never repeats
// one, and a change of the Organization changes the ETag of every User who follows it: an If-Match read before the
// change is stale after it.
func TestETagVersions(t *testing.T) {
	svc, f := newService(t)
	ctx := t.Context()
	r, err := svc.Get(ctx, ivan)
	if err != nil || r.Version != 3 {
		t.Fatalf("ivan = %d, %v", r.Version, err)
	}
	read := r.Version
	f.orgVer = 2 // the Organization changed
	if r, _ := svc.Get(ctx, ivan); r.Version != 5 {
		t.Errorf("after the Organization's change: %d", r.Version)
	}
	if _, err := svc.Update(ctx, by, ivan, &read, Update{State: StateNoCall}); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("an If-Match from before the Organization's change: %v", err)
	}
	r, err = svc.Update(ctx, by, ivan, new(int64(5)), Update{State: StateNoCall})
	if err != nil || r.Version != 2 {
		t.Fatalf("no_call = %d, %v", r.Version, err)
	}
	if r, err := svc.Reset(ctx, by, ivan); err != nil || r.Version != 5 {
		t.Errorf("reset = %d, %v", r.Version, err)
	}
	if r, err := svc.Update(ctx, by, ivan, new(int64(5)), Update{State: StateNoCall}); err != nil || r.Version != 4 {
		t.Errorf("no_call again = %d, %v; a stored row never repeats an ETag", r.Version, err)
	}
	if RowETagVersion(7)%2 != 0 || OrganizationETagVersion(7)%2 != 1 {
		t.Error("the encoding")
	}
}

// TestKeptCalls: only as many calls through a gateway as the owner's own stored rules have pass without their checks;
// a User in the state organization keeps none, not even those of the Organization's rules they follow; a further call
// through a kept gateway is checked.
func TestKeptCalls(t *testing.T) {
	svc, f := newService(t)
	ctx := t.Context()
	f.org = []byte(`{"normal":[{"kind":"mention"}],"important":[{"kind":"mention"},{"kind":"call","gateway_id":` +
		`"CN0000000000GA"}]}`)
	custom := func(user int64, normal ...ActionInput) error {
		_, err := svc.Update(ctx, by, user, nil, Update{State: StateCustom,
			Rules: SetInput{Normal: rule(normal...), Important: rule(mention())}})
		return err
	}
	// ivan follows the Organization, which calls through GA; he has no contact there.
	if got := fieldError(t, custom(ivan, call(gwA))); got != "/normal/0 contact_required" {
		t.Errorf("a call of the Organization's gateway without a contact: %s", got)
	}
	// petr stores one call through GA, then loses his contact: that call stays, a second one is checked.
	if err := custom(petr, mention(), call(gwA)); err != nil {
		t.Fatal(err)
	}
	f.contacts[petr] = nil
	if err := custom(petr, call(gwA), mention()); err != nil {
		t.Errorf("the kept call moved: %v", err)
	}
	if got := fieldError(t, custom(petr, call(gwA), call(gwA))); got != "/normal/1 contact_required" {
		t.Errorf("a second call through the kept gateway: %s", got)
	}
	// The gateway deleted: the kept call stays, a second one is unknown_id while another gateway exists.
	f.gateways = append(f.gateways, dbgen.ListRuleGatewaysRow{ID: 11, PublicID: gwB, Name: "Gateway B", Live: true})
	f.gateways[0].Live = false
	if err := custom(petr, call(gwA)); err != nil {
		t.Errorf("the kept call of a deleted gateway: %v", err)
	}
	if got := fieldError(t, custom(petr, call(gwA), call(gwA))); got != "/normal/1/gateway_id unknown_id" {
		t.Errorf("a second call through the deleted gateway: %s", got)
	}
	// Validate directly: counts across both rules.
	kept := Set{Normal: []Action{{Kind: KindCall, GatewayID: gwA}}, Important: []Action{{Kind: KindMention}}}
	deleted := Gateways{{ID: 10, PublicID: gwA}, {ID: 11, PublicID: gwB, Live: true}}
	if _, fe := Validate(SetInput{Normal: rule(mention()), Important: rule(call(gwA))}, "",
		Check{Gateways: deleted, Kept: kept}); fe != nil {
		t.Errorf("the kept call in the other rule: %v", fe)
	}
	if _, fe := Validate(SetInput{Normal: rule(call(gwA)), Important: rule(call(gwA))}, "",
		Check{Gateways: deleted, Kept: kept}); fe == nil || fe.Pointer != "/important/0/gateway_id" ||
		fe.Code != CodeUnknownID {
		t.Errorf("two calls, one kept: %v", fe)
	}
}
