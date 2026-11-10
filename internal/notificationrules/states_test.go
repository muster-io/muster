// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package notificationrules

import (
	"testing"

	"github.com/muster-io/muster/internal/notificationrules/dbgen"
)

// set is a Set of a test from its stored JSON.
func set(t *testing.T, raw string) Set {
	t.Helper()
	s, err := ParseSet([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestWithCall is C-31.FR-2, FR-5 and C-31.AC-6: with the default rules the state call makes the important rule
// exactly a call through the gateway, a Mention, a wait of 5 minutes and a Mention — the call and the first Mention at
// the same moment — and leaves the normal rule a Mention; an important rule that already calls through that gateway
// gets no second call, one that calls through another gets this one too; a call that would pass 10 actions is refused.
func TestWithCall(t *testing.T) {
	got, fe := WithCall(Defaults(), gwA)
	if fe != nil || string(got.JSON()) != `{"normal":[{"kind":"mention"}],"important":[{"kind":"call","gateway_id":`+
		`"CN0000000000GA"},{"kind":"mention"},{"kind":"wait","wait_seconds":300},{"kind":"mention"}]}` {
		t.Errorf("from the defaults = %s, %v", got.JSON(), fe)
	}
	calls := set(t, `{"normal":[{"kind":"mention"}],"important":[{"kind":"mention"},{"kind":"wait",`+
		`"wait_seconds":300},{"kind":"call","gateway_id":"CN0000000000GA"}]}`)
	if got, fe := WithCall(calls, gwA); fe != nil || string(got.JSON()) != string(calls.JSON()) {
		t.Errorf("already calls = %s, %v", got.JSON(), fe)
	}
	if got, fe := WithCall(calls, gwB); fe != nil || len(got.Important) != 4 || got.Important[0].GatewayID != gwB {
		t.Errorf("calls through another = %s, %v", got.JSON(), fe)
	}
	full := Defaults()
	full.Important = make([]Action, MaxActions)
	for i := range full.Important {
		full.Important[i] = Action{Kind: KindMention}
	}
	if _, fe := WithCall(full, gwA); fe == nil || fe.Pointer != "/state" || fe.Code != CodeOutOfRange {
		t.Errorf("10 actions: %v", fe)
	}
	// The Organization's rules are copied, never changed.
	org := Defaults()
	got, _ = WithCall(org, gwA)
	got.Normal[0].Kind = KindWait
	if org.Normal[0].Kind != KindMention || len(org.Important) != 3 {
		t.Errorf("the Organization's rules changed: %+v", org)
	}
}

// TestWithoutCalls is C-31.FR-2 and Open question 1: "Don't call" keeps the Organization's rules without any call; a
// rule left without a Mention gets one in place of its first call and keeps its waits.
func TestWithoutCalls(t *testing.T) {
	for _, c := range []struct{ org, want string }{
		{string(Defaults().JSON()), string(Defaults().JSON())},
		{`{"normal":[{"kind":"call","gateway_id":"CN0000000000GA"},{"kind":"mention"}],"important":[{"kind":"call",` +
			`"gateway_id":"CN0000000000GA"},{"kind":"wait","wait_seconds":300},{"kind":"call","gateway_id":` +
			`"CN0000000000GB"},{"kind":"wait","wait_seconds":60}]}`,
			`{"normal":[{"kind":"mention"}],"important":[{"kind":"mention"},{"kind":"wait","wait_seconds":300},` +
				`{"kind":"wait","wait_seconds":60}]}`},
		{`{"normal":[{"kind":"wait","wait_seconds":120},{"kind":"call","gateway_id":"CN0000000000GA"}],` +
			`"important":[{"kind":"mention"},{"kind":"call","gateway_id":"CN0000000000GA"}]}`,
			`{"normal":[{"kind":"wait","wait_seconds":120},{"kind":"mention"}],"important":[{"kind":"mention"}]}`},
	} {
		if got := string(WithoutCalls(set(t, c.org)).JSON()); got != c.want {
			t.Errorf("WithoutCalls(%s) = %s, want %s", c.org, got, c.want)
		}
	}
}

// TestSwitch is C-31.FR-4, FR-5, C-31.AC-3 and AC-6 through the Service: the state call without gateway_id takes the
// only gateway at which the User has a confirmed contact; with two such gateways it is required at /gateway_id; while
// no gateway exists gateway_required, for a gateway without a contact contact_required and for another Connection
// unknown_id, all at /gateway_id; on an Organization whose important rule already calls through that gateway no
// second call is added, and no_call on it removes the call; custom calls need a contact at their gateway.
func TestSwitch(t *testing.T) {
	svc, f := newService(t)
	ctx := t.Context()
	f.gateways = append(f.gateways, dbgen.ListRuleGatewaysRow{ID: 11, PublicID: gwB, Name: "Gateway B", Live: true})
	if r, err := svc.Update(ctx, by, petr, nil, Update{State: StateCall}); err != nil || r.Gateway.PublicID != gwA {
		t.Errorf("the only gateway with a contact = %+v, %v", r, err)
	}
	f.contacts[petr] = []int64{10, 11}
	_, err := svc.Update(ctx, by, petr, nil, Update{State: StateCall})
	if got := fieldError(t, err); got != "/gateway_id required" {
		t.Errorf("two gateways: %s", got)
	}
	r, err := svc.Update(ctx, by, petr, nil, Update{State: StateCall, GatewayID: new("cn0000000000gb")})
	if err != nil || r.Gateway.PublicID != gwB || kinds(r.Rules.Important) != "call GB,mention,300,mention" {
		t.Errorf("gateway B = %+v, %v", r, err)
	}
	for _, c := range []struct {
		name    string
		user    int64
		gateway *string
		setup   func()
		want    string
	}{
		{"no contact", ivan, nil, func() {}, "/gateway_id contact_required"},
		{"no contact at it", ivan, new(gwB), func() {}, "/gateway_id contact_required"},
		{"not a gateway", petr, new("CN0000000000ZZ"), func() {}, "/gateway_id unknown_id"},
		{"no gateway", petr, new(gwA), func() { f.gateways = nil }, "/gateway_id gateway_required"},
	} {
		c.setup()
		_, err := svc.Update(ctx, by, c.user, nil, Update{State: StateCall, GatewayID: c.gateway})
		if got := fieldError(t, err); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	_, err = svc.Update(ctx, by, petr, nil, Update{State: StateCustom, Rules: SetInput{Normal: rule(mention()),
		Important: rule(call(gwA))}})
	if got := fieldError(t, err); got != "/important/0 gateway_required" {
		t.Errorf("a custom call without a gateway: %s", got)
	}

	// An Organization whose important rule already calls through gateway A.
	svc, f = newService(t)
	f.org = []byte(`{"normal":[{"kind":"mention"}],"important":[{"kind":"mention"},{"kind":"call","gateway_id":` +
		`"CN0000000000GA"},{"kind":"wait","wait_seconds":300},{"kind":"mention"}]}`)
	if r, err := svc.Update(ctx, by, petr, nil, Update{State: StateCall}); err != nil ||
		kinds(r.Rules.Important) != "mention,call GA,300,mention" {
		t.Errorf("call on an Organization that calls = %+v, %v", r, err)
	}
	if r, err := svc.Update(ctx, by, ivan, nil, Update{State: StateNoCall}); err != nil || r.Gateway != nil ||
		kinds(r.Rules.Important) != "mention,300,mention" {
		t.Errorf("no_call on an Organization that calls = %+v, %v", r, err)
	}
	_, err = svc.Update(ctx, by, ivan, nil, Update{State: StateCustom, Rules: SetInput{Normal: rule(mention()),
		Important: rule(call(gwA))}})
	if got := fieldError(t, err); got != "/important/0 contact_required" {
		t.Errorf("a custom call without a contact: %s", got)
	}
	f.org = []byte(`{"normal":[{"kind":"mention"}],"important":[` + `{"kind":"mention"},{"kind":"mention"},` +
		`{"kind":"mention"},{"kind":"mention"},{"kind":"mention"},{"kind":"mention"},{"kind":"mention"},` +
		`{"kind":"mention"},{"kind":"mention"},{"kind":"mention"}]}`)
	_, err = svc.Update(ctx, by, petr, nil, Update{State: StateCall})
	if got := fieldError(t, err); got != "/state out_of_range" {
		t.Errorf("call on 10 actions: %s", got)
	}
}
