// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/routing/dbgen"
)

// The Escalation chains of the fake store: the chains by public_id with their ids, names and owners, and the chains
// each Route names with their conditions.
type fakeChain struct {
	id    int64
	name  string
	owner *TeamRef
}

type routeChain struct {
	chain     int64
	condition string
}

func (s *fakeStore) chainsOf() map[string]fakeChain {
	if s.chains == nil {
		s.chains = map[string]fakeChain{}
	}
	return s.chains
}

func (s *fakeStore) routeChainsOf() map[int64][]routeChain {
	if s.routeChains == nil {
		s.routeChains = map[int64][]routeChain{}
	}
	return s.routeChains
}

func (s *fakeStore) ListRouteChains(_ context.Context, arg dbgen.ListRouteChainsParams) ([]dbgen.ListRouteChainsRow,
	error) {
	if err := s.call("ListRouteChains"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRouteChainsRow
	for _, route := range slices.Sorted(slices.Values(arg.RouteIds)) {
		var rows []dbgen.ListRouteChainsRow
		for _, rc := range s.routeChainsOf()[route] {
			for p, c := range s.chainsOf() {
				if c.id != rc.chain {
					continue
				}
				r := dbgen.ListRouteChainsRow{RouteID: route, SeverityCondition: rc.condition, PublicID: p, Name: c.name}
				if c.owner != nil {
					r.OwnerPublicID = pgtype.Text{String: c.owner.PublicID, Valid: true}
					r.OwnerName = pgtype.Text{String: c.owner.Name, Valid: true}
					r.OwnerSlug = pgtype.Text{String: c.owner.Slug, Valid: true}
				}
				rows = append(rows, r)
			}
		}
		slices.SortFunc(rows, func(a, b dbgen.ListRouteChainsRow) int { return cmp.Compare(a.PublicID, b.PublicID) })
		out = append(out, rows...)
	}
	return out, nil
}

func (s *fakeStore) ResolveRouteChains(_ context.Context, arg dbgen.ResolveRouteChainsParams) (
	[]dbgen.ResolveRouteChainsRow, error) {
	if err := s.call("ResolveRouteChains"); err != nil {
		return nil, err
	}
	var out []dbgen.ResolveRouteChainsRow
	for _, p := range arg.PublicIds {
		if c, ok := s.chainsOf()[p]; ok && arg.OrgID == orgID {
			out = append(out, dbgen.ResolveRouteChainsRow{ID: c.id, PublicID: p})
		}
	}
	return out, nil
}

func (s *fakeStore) DeleteRouteChainsExcept(_ context.Context, arg dbgen.DeleteRouteChainsExceptParams) error {
	if err := s.call("DeleteRouteChainsExcept"); err != nil {
		return err
	}
	m := s.routeChainsOf()
	m[arg.RouteID] = slices.DeleteFunc(slices.Clone(m[arg.RouteID]), func(rc routeChain) bool {
		return !slices.Contains(arg.KeepIds, rc.chain)
	})
	return nil
}

func (s *fakeStore) UpsertRouteChains(_ context.Context, arg dbgen.UpsertRouteChainsParams) error {
	if err := s.call("UpsertRouteChains"); err != nil {
		return err
	}
	m := s.routeChainsOf()
	for i, id := range arg.ChainIds {
		j := slices.IndexFunc(m[arg.RouteID], func(rc routeChain) bool { return rc.chain == id })
		if j < 0 {
			m[arg.RouteID] = append(m[arg.RouteID], routeChain{chain: id, condition: arg.Conditions[i]})
			continue
		}
		m[arg.RouteID][j].condition = arg.Conditions[i]
	}
	return nil
}

const (
	chainA = "EC0000000000AA"
	chainB = "EC0000000000BB"
	chainX = "EC0000000000XX"
)

// TestRouteChainEntries is C-32.FR-2 and C-08.FR-1 on Routes: a creation and an update name chains with their
// conditions, any by default; the read lists them by public_id and the chains by name with their owners; an update
// that omits them keeps them; the Audit log diff carries them; unknown, malformed and doubled chains and conditions
// outside the three are refused at their pointers; a failing query fails the save.
func TestRouteChainEntries(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	owner := &TeamRef{PublicID: "TM0000000000PA", Name: "payments", Slug: "payments"}
	store.chainsOf()[chainA] = fakeChain{id: 1, name: "Zeta", owner: owner}
	store.chainsOf()[chainB] = fakeChain{id: 2, name: "alpha"}

	in := input("chains")
	in.ChainEntries = &[]ChainEntry{{ChainID: chainA, Condition: ConditionCritical}, {ChainID: strings.ToLower(chainB)}}
	created, err := svc.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	want := []ChainEntry{{ChainID: chainA, Condition: ConditionCritical}, {ChainID: chainB, Condition: ConditionAny}}
	if !slices.Equal(created.ChainEntries, want) || len(created.Chains) != 2 || created.Chains[0].Name != "alpha" ||
		created.Chains[1].OwningTeam == nil || created.Chains[1].OwningTeam.Slug != "payments" {
		t.Fatalf("created %+v, %+v", created.ChainEntries, created.Chains)
	}
	var diff []audit.Change
	_ = json.Unmarshal(store.audit[len(store.audit)-1].Diff, &diff)
	if !slices.ContainsFunc(diff, func(c audit.Change) bool { return c.Pointer == "/chain_entries" }) {
		t.Errorf("creation diff %+v", diff)
	}

	// Omitted keeps them; a change of condition and a removal write them.
	keep := input("chains")
	got, err := svc.Update(ctx, by, created.PublicID, nil, keep)
	if err != nil || got.Version != created.Version || !slices.Equal(got.ChainEntries, want) {
		t.Fatalf("omitted %+v, %v", got.ChainEntries, err)
	}
	upd := input("chains")
	upd.ChainEntries = &[]ChainEntry{{ChainID: chainA, Condition: ConditionBelowCritical}}
	if got, err = svc.Update(ctx, by, created.PublicID, nil, upd); err != nil ||
		!slices.Equal(got.ChainEntries, *upd.ChainEntries) || got.Version == created.Version {
		t.Fatalf("changed %+v, %v", got.ChainEntries, err)
	}
	_ = json.Unmarshal(store.audit[len(store.audit)-1].Diff, &diff)
	if len(diff) != 1 || diff[0].Pointer != "/chain_entries" {
		t.Errorf("update diff %+v", diff)
	}
	upd.ChainEntries = &[]ChainEntry{}
	if got, err = svc.Update(ctx, by, created.PublicID, nil, upd); err != nil || len(got.ChainEntries) != 0 ||
		len(got.Chains) != 0 {
		t.Fatalf("emptied %+v, %v", got.ChainEntries, err)
	}

	for name, c := range map[string]struct {
		entries []ChainEntry
		pointer string
		code    string
	}{
		"twice":     {[]ChainEntry{{ChainID: chainA}, {ChainID: strings.ToLower(chainA)}}, "/chain_entries/1/chain_id", CodeDuplicate},
		"unknown":   {[]ChainEntry{{ChainID: chainA}, {ChainID: chainX}}, "/chain_entries/1/chain_id", CodeUnknownID},
		"malformed": {[]ChainEntry{{ChainID: "nope"}}, "/chain_entries/0/chain_id", CodeUnknownID},
		"condition": {[]ChainEntry{{ChainID: chainA, Condition: "warning"}}, "/chain_entries/0/severity_condition", CodeInvalidFormat},
	} {
		bad := input("chains")
		bad.ChainEntries = &c.entries
		if f := fieldError(t, func() error {
			_, err := svc.Update(ctx, by, created.PublicID, nil, bad)
			return err
		}()); f.Pointer != c.pointer || f.Code != c.code {
			t.Errorf("update %s: %+v", name, f)
		}
		bad.Name = "chains " + name
		if f := fieldError(t, func() error {
			_, err := svc.Create(ctx, by, bad)
			return err
		}()); f.Pointer != c.pointer || f.Code != c.code {
			t.Errorf("create %s: %+v", name, f)
		}
	}

	for _, query := range []string{"ResolveRouteChains", "DeleteRouteChainsExcept", "UpsertRouteChains",
		"ListRouteChains"} {
		store.fail = map[string]error{query: errBoom}
		bad := input("chains " + query)
		bad.ChainEntries = &[]ChainEntry{{ChainID: chainB}}
		if _, err := svc.Create(ctx, by, bad); !errors.Is(err, errBoom) {
			t.Errorf("create with %s failing: %v", query, err)
		}
		bad.Name = "chains"
		if _, err := svc.Update(ctx, by, created.PublicID, nil, bad); !errors.Is(err, errBoom) {
			t.Errorf("update with %s failing: %v", query, err)
		}
	}
	store.fail = map[string]error{"ListRouteChains": errBoom}
	if _, err := svc.List(ctx); !errors.Is(err, errBoom) {
		t.Errorf("list with ListRouteChains failing: %v", err)
	}
}

// chainHints are the public_ids of the escalation-chain hints after the first from.
func chainHints(s *fakeStore, from int) []string {
	var out []string
	for _, h := range s.hints[from:] {
		if h.Type == hintChain {
			out = append(out, h.ID)
		}
	}
	return out
}

// TestRouteChainHints: a save of a Route sends the hint escalation-chain of every chain whose entry it added, removed
// or changed, whose referrers read otherwise, and none for a chain it keeps as it was; the deletion of the Route sends
// it for every chain it named.
func TestRouteChainHints(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	store.chainsOf()[chainA] = fakeChain{id: 1, name: "a"}
	store.chainsOf()[chainB] = fakeChain{id: 2, name: "b"}
	store.chainsOf()[chainX] = fakeChain{id: 3, name: "x"}
	in := input("hints")
	in.ChainEntries = &[]ChainEntry{{ChainID: chainB}, {ChainID: chainA}}
	from := len(store.hints)
	created, err := svc.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := chainHints(store, from); !slices.Equal(got, []string{chainA, chainB}) {
		t.Errorf("creation hints %v", got)
	}
	in.ChainEntries = &[]ChainEntry{{ChainID: chainA, Condition: ConditionCritical}, {ChainID: chainX}}
	from = len(store.hints)
	if _, err := svc.Update(ctx, by, created.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	if got := chainHints(store, from); !slices.Equal(got, []string{chainA, chainB, chainX}) {
		t.Errorf("update hints %v", got)
	}
	in.Description = ptr("only the description")
	from = len(store.hints)
	if _, err := svc.Update(ctx, by, created.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	if got := chainHints(store, from); len(got) != 0 {
		t.Errorf("hints of a save that keeps the chains %v", got)
	}
	// A rename and a change of the Destination entries change what each chain reads of the Route.
	store.dests["DS0000000000HA"] = 77
	for name, edit := range map[string]func(*Input){
		"rename":       func(in *Input) { in.Name = "hints renamed" },
		"destinations": func(in *Input) { in.DestinationEntries = &[]DestinationEntry{{DestinationID: "DS0000000000HA"}} },
	} {
		next := in
		edit(&next)
		from = len(store.hints)
		if _, err := svc.Update(ctx, by, created.PublicID, nil, next); err != nil {
			t.Fatal(err)
		}
		if got := chainHints(store, from); !slices.Equal(got, []string{chainA, chainX}) {
			t.Errorf("%s hints %v", name, got)
		}
		in = next
	}
	store.failHint = hintChain
	in.ChainEntries = &[]ChainEntry{{ChainID: chainA}}
	if _, err := svc.Update(ctx, by, created.PublicID, nil, in); !errors.Is(err, errBoom) {
		t.Errorf("update with a failing chain hint = %v", err)
	}
	if err := svc.Delete(ctx, by, created.PublicID, nil); !errors.Is(err, errBoom) {
		t.Errorf("delete with a failing chain hint = %v", err)
	}
	in.Name = "hints failing"
	in.DestinationEntries = nil
	if _, err := svc.Create(ctx, by, in); !errors.Is(err, errBoom) {
		t.Errorf("create with a failing chain hint = %v", err)
	}
	store.failHint = ""
	from = len(store.hints)
	if err := svc.Delete(ctx, by, created.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if got := chainHints(store, from); !slices.Equal(got, []string{chainA, chainX}) {
		t.Errorf("deletion hints %v", got)
	}
}
