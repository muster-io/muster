// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

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
	"github.com/muster-io/muster/internal/teams/dbgen"
)

// fakeChain is an Escalation chain as the Teams read it, and fakeTeamChain a row of team_escalation_chains.
type fakeChain struct {
	id    int64
	name  string
	owner *ChainOwner
}

type fakeTeamChain struct {
	chain     int64
	condition string
}

func (s *fakeStore) ListTeamOwnedChains(_ context.Context, arg dbgen.ListTeamOwnedChainsParams) (
	[]dbgen.ListTeamOwnedChainsRow, error) {
	if err := s.fail["ListTeamOwnedChains"]; err != nil {
		return nil, err
	}
	return s.ownedChains[arg.TeamID.Int64], nil
}

func (s *fakeStore) ListTeamChains(_ context.Context, arg dbgen.ListTeamChainsParams) ([]dbgen.ListTeamChainsRow,
	error) {
	if err := s.fail["ListTeamChains"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListTeamChainsRow
	for _, team := range slices.Sorted(slices.Values(arg.TeamIds)) {
		var rows []dbgen.ListTeamChainsRow
		for _, tc := range s.teamChains[team] {
			for p, c := range s.chains {
				if c.id != tc.chain {
					continue
				}
				r := dbgen.ListTeamChainsRow{TeamID: team, SeverityCondition: tc.condition, PublicID: p, Name: c.name}
				if c.owner != nil {
					r.OwnerPublicID = pgtype.Text{String: c.owner.PublicID, Valid: true}
					r.OwnerName = pgtype.Text{String: c.owner.Name, Valid: true}
					r.OwnerSlug = pgtype.Text{String: c.owner.Slug, Valid: true}
				}
				rows = append(rows, r)
			}
		}
		slices.SortFunc(rows, func(a, b dbgen.ListTeamChainsRow) int { return cmp.Compare(a.PublicID, b.PublicID) })
		out = append(out, rows...)
	}
	return out, nil
}

func (s *fakeStore) ResolveTeamChains(_ context.Context, arg dbgen.ResolveTeamChainsParams) (
	[]dbgen.ResolveTeamChainsRow, error) {
	if err := s.fail["ResolveTeamChains"]; err != nil {
		return nil, err
	}
	var out []dbgen.ResolveTeamChainsRow
	for _, p := range arg.PublicIds {
		if c, ok := s.chains[p]; ok && arg.OrgID == orgID {
			out = append(out, dbgen.ResolveTeamChainsRow{ID: c.id, PublicID: p})
		}
	}
	return out, nil
}

func (s *fakeStore) DeleteTeamChainsExcept(_ context.Context, arg dbgen.DeleteTeamChainsExceptParams) error {
	if err := s.fail["DeleteTeamChainsExcept"]; err != nil {
		return err
	}
	if s.teamChains == nil {
		s.teamChains = map[int64][]fakeTeamChain{}
	}
	s.teamChains[arg.TeamID] = slices.DeleteFunc(slices.Clone(s.teamChains[arg.TeamID]), func(tc fakeTeamChain) bool {
		return !slices.Contains(arg.KeepIds, tc.chain)
	})
	return nil
}

func (s *fakeStore) UpsertTeamChains(_ context.Context, arg dbgen.UpsertTeamChainsParams) error {
	if err := s.fail["UpsertTeamChains"]; err != nil {
		return err
	}
	if s.teamChains == nil {
		s.teamChains = map[int64][]fakeTeamChain{}
	}
	for i, id := range arg.ChainIds {
		j := slices.IndexFunc(s.teamChains[arg.TeamID], func(tc fakeTeamChain) bool { return tc.chain == id })
		if j < 0 {
			s.teamChains[arg.TeamID] = append(s.teamChains[arg.TeamID], fakeTeamChain{chain: id,
				condition: arg.Conditions[i]})
			continue
		}
		s.teamChains[arg.TeamID][j].condition = arg.Conditions[i]
	}
	return nil
}

const (
	chainA = "EC0000000000AA"
	chainB = "EC0000000000BB"
)

// TestTeamChainEntries is C-32.FR-2 and C-25.FR-1 on Teams: a creation and an update name Escalation chains with their
// conditions, any by default; the read lists them by public_id and the chains by name with their owners; an update
// that omits them keeps them; the Audit log diff carries them; unknown, malformed and doubled chains and conditions
// outside the three are refused at their pointers; the deletion of the Team removes its rows; failing queries fail.
func TestTeamChainEntries(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	store.chains = map[string]fakeChain{
		chainA: {id: 1, name: "Zeta", owner: &ChainOwner{PublicID: "TM0000000000OO", Name: "Owner", Slug: "owner"}},
		chainB: {id: 2, name: "alpha"},
	}
	in := Input{Name: "Payments", Slug: "payments",
		ChainEntries: &[]ChainEntry{{ChainID: chainA, Condition: ConditionCritical}, {ChainID: strings.ToLower(chainB)}}}
	team, err := svc.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	want := []ChainEntry{{ChainID: chainA, Condition: ConditionCritical}, {ChainID: chainB, Condition: ConditionAny}}
	if !slices.Equal(team.ChainEntries, want) || len(team.Chains) != 2 || team.Chains[0].Name != "alpha" ||
		team.Chains[1].OwningTeam == nil || team.Chains[1].OwningTeam.Slug != "owner" {
		t.Fatalf("created %+v, %+v", team.ChainEntries, team.Chains)
	}
	var diff []audit.Change
	_ = json.Unmarshal(store.audit[len(store.audit)-1].Diff, &diff)
	if !slices.ContainsFunc(diff, func(c audit.Change) bool { return c.Pointer == "/chain_entries" }) {
		t.Errorf("creation diff %+v", diff)
	}
	page, err := svc.List(ctx, ListFilter{})
	if err != nil || len(page.Teams) != 1 || !slices.Equal(page.Teams[0].ChainEntries, want) {
		t.Fatalf("list %+v, %v", page.Teams, err)
	}

	got, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Payments", Slug: "payments"})
	if err != nil || got.Version != team.Version || !slices.Equal(got.ChainEntries, want) {
		t.Fatalf("omitted %+v, %v", got.ChainEntries, err)
	}
	upd := Input{Name: "Payments", Slug: "payments", ChainEntries: &[]ChainEntry{{ChainID: chainB,
		Condition: ConditionBelowCritical}}}
	if got, err = svc.Update(ctx, by, team.PublicID, nil, upd); err != nil ||
		!slices.Equal(got.ChainEntries, *upd.ChainEntries) || len(got.Chains) != 1 {
		t.Fatalf("changed %+v, %v", got.ChainEntries, err)
	}
	_ = json.Unmarshal(store.audit[len(store.audit)-1].Diff, &diff)
	if len(diff) != 1 || diff[0].Pointer != "/chain_entries" {
		t.Errorf("update diff %+v", diff)
	}

	for name, c := range map[string]struct {
		entries []ChainEntry
		pointer string
		code    string
	}{
		"twice":     {[]ChainEntry{{ChainID: chainA}, {ChainID: strings.ToLower(chainA)}}, "/chain_entries/1/chain_id", CodeDuplicate},
		"unknown":   {[]ChainEntry{{ChainID: chainA}, {ChainID: "EC0000000000XX"}}, "/chain_entries/1/chain_id", CodeUnknownID},
		"malformed": {[]ChainEntry{{ChainID: "nope"}}, "/chain_entries/0/chain_id", CodeUnknownID},
		"condition": {[]ChainEntry{{ChainID: chainA, Condition: "warning"}}, "/chain_entries/0/severity_condition", CodeInvalidFormat},
	} {
		bad := Input{Name: "Payments", Slug: "payments", ChainEntries: &c.entries}
		if f := fieldError(t, func() error {
			_, err := svc.Update(ctx, by, team.PublicID, nil, bad)
			return err
		}()); f.Pointer != c.pointer || f.Code != c.code {
			t.Errorf("update %s: %+v", name, f)
		}
		bad.Name, bad.Slug = "Other "+name, "other-"+name
		if f := fieldError(t, func() error {
			_, err := svc.Create(ctx, by, bad)
			return err
		}()); f.Pointer != c.pointer || f.Code != c.code {
			t.Errorf("create %s: %+v", name, f)
		}
	}
	for _, query := range []string{"ResolveTeamChains", "DeleteTeamChainsExcept", "UpsertTeamChains",
		"ListTeamChains"} {
		store.fail = map[string]error{query: errBoom}
		upd.ChainEntries = &[]ChainEntry{{ChainID: chainA}}
		if _, err := svc.Update(ctx, by, team.PublicID, nil, upd); !errors.Is(err, errBoom) {
			t.Errorf("update with %s failing: %v", query, err)
		}
		slug := "failing-" + strings.ToLower(query)
		bad := Input{Name: slug, Slug: slug, ChainEntries: &[]ChainEntry{{ChainID: chainA}}}
		if _, err := svc.Create(ctx, by, bad); !errors.Is(err, errBoom) {
			t.Errorf("create with %s failing: %v", query, err)
		}
	}
	store.fail = map[string]error{"ListTeamChains": errBoom}
	if _, err := svc.List(ctx, ListFilter{}); !errors.Is(err, errBoom) {
		t.Errorf("list with ListTeamChains failing: %v", err)
	}
	store.fail = map[string]error{"DeleteTeamChainsExcept": errBoom}
	if err := svc.Delete(ctx, by, team.PublicID, nil); !errors.Is(err, errBoom) {
		t.Errorf("delete with DeleteTeamChainsExcept failing: %v", err)
	}
	store.fail = map[string]error{}
	if err := svc.Delete(ctx, by, team.PublicID, nil); err != nil || len(store.teamChains[team.ID]) != 0 {
		t.Errorf("delete = %v, %+v left", err, store.teamChains[team.ID])
	}
}

// TestDeleteOwningTeam is C-25.FR-10 with chains: deleting a Team that owns Escalation chains is refused with
// team_in_use, which names the Schedules and then the chains in in_use_by (type escalation_chain), and changes
// nothing.
func TestDeleteOwningTeam(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	team, err := svc.Create(ctx, by, Input{Name: "Checkout", Slug: "checkout"})
	if err != nil {
		t.Fatal(err)
	}
	store.schedules = map[int64][]dbgen.ListTeamSchedulesRow{team.ID: {{PublicID: "SC0000000000AA", Name: "Main"}}}
	store.ownedChains = map[int64][]dbgen.ListTeamOwnedChainsRow{team.ID: {{PublicID: chainA, Name: "checkout"}}}
	err = svc.Delete(ctx, by, team.PublicID, nil)
	use, ok := errors.AsType[*InUseError](err)
	if !ok || len(use.Holders) != 2 || use.Holders[1] != (Holder{Type: HolderChain, PublicID: chainA,
		Name: "checkout"}) {
		t.Fatalf("a team owning a chain = %v %+v", err, use)
	}
	store.schedules = nil
	if err := svc.Delete(ctx, by, team.PublicID, nil); !errors.Is(err, ErrInUse) {
		t.Errorf("only the chain = %v", err)
	}
	store.fail["ListTeamOwnedChains"] = errBoom
	if err := svc.Delete(ctx, by, team.PublicID, nil); !errors.Is(err, errBoom) {
		t.Errorf("the chains fail = %v", err)
	}
	delete(store.fail, "ListTeamOwnedChains")
	store.ownedChains = nil
	if err := svc.Delete(ctx, by, team.PublicID, nil); err != nil || !store.teams[0].deleted {
		t.Errorf("without chains = %v", err)
	}
}

// TestTeamChainHints: a save of a Team sends the hint escalation-chain of every chain whose entry it added, removed or
// changed, and none for a chain it keeps as it was; the deletion of the Team sends it for every chain it named; a
// failing hint fails the change.
func TestTeamChainHints(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	store.chains = map[string]fakeChain{chainA: {id: 1, name: "a"}, chainB: {id: 2, name: "b"}}
	hintsOf := func(from int) []string {
		var out []string
		for _, h := range store.hints[from:] {
			if h.Type == hintChain {
				out = append(out, h.ID)
			}
		}
		return out
	}
	in := Input{Name: "Hints", Slug: "hints", ChainEntries: &[]ChainEntry{{ChainID: chainB}, {ChainID: chainA}}}
	from := len(store.hints)
	team, err := svc.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := hintsOf(from); !slices.Equal(got, []string{chainA, chainB}) {
		t.Errorf("creation hints %v", got)
	}
	in.ChainEntries = &[]ChainEntry{{ChainID: chainA, Condition: ConditionCritical}, {ChainID: chainB}}
	from = len(store.hints)
	if _, err := svc.Update(ctx, by, team.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	if got := hintsOf(from); !slices.Equal(got, []string{chainA}) {
		t.Errorf("update hints %v", got)
	}
	// A rename, a new slug and a change of the Destination entries change what each chain reads of the Team.
	store.destinations = map[string]*fakeDestination{"DS0000000000PC": {id: 41, name: "payments-critical",
		typ: "mattermost", health: "healthy"}}
	for name, edit := range map[string]func(*Input){
		"rename":       func(in *Input) { in.Name = "Hints renamed" },
		"slug":         func(in *Input) { in.Slug = "hints-renamed" },
		"destinations": func(in *Input) { in.DestinationEntries = &[]DestinationEntry{{DestinationID: "DS0000000000PC"}} },
	} {
		next := in
		edit(&next)
		from = len(store.hints)
		if _, err := svc.Update(ctx, by, team.PublicID, nil, next); err != nil {
			t.Fatal(err)
		}
		if got := hintsOf(from); !slices.Equal(got, []string{chainA, chainB}) {
			t.Errorf("%s hints %v", name, got)
		}
		in = next
	}
	from = len(store.hints)
	if err := svc.Delete(ctx, by, team.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if got := hintsOf(from); !slices.Equal(got, []string{chainA, chainB}) {
		t.Errorf("deletion hints %v", got)
	}
	// Each failure rolls back: the Teams, their chains and the hints are as they were.
	other, err := svc.Create(ctx, by, Input{Name: "Other", Slug: "other", ChainEntries: &[]ChainEntry{{ChainID: chainA}}})
	if err != nil {
		t.Fatal(err)
	}
	store.failHint = hintChain
	if _, err := svc.Update(ctx, by, other.PublicID, nil, Input{Name: "Other", Slug: "other",
		ChainEntries: &[]ChainEntry{{ChainID: chainB}}}); !errors.Is(err, errBoom) {
		t.Errorf("update with a failing chain hint = %v", err)
	}
	if got, err := svc.Get(ctx, other.PublicID); err != nil || !slices.Equal(got.ChainEntries,
		[]ChainEntry{{ChainID: chainA, Condition: ConditionAny}}) {
		t.Errorf("after the failed update %+v, %v", got.ChainEntries, err)
	}
	if err := svc.Delete(ctx, by, other.PublicID, nil); !errors.Is(err, errBoom) {
		t.Errorf("delete with a failing chain hint = %v", err)
	}
	if _, err := svc.Get(ctx, other.PublicID); err != nil {
		t.Errorf("the failed deletion deleted the team: %v", err)
	}
	if _, err := svc.Create(ctx, by, Input{Name: "Third", Slug: "third",
		ChainEntries: &[]ChainEntry{{ChainID: chainA}}}); !errors.Is(err, errBoom) {
		t.Errorf("create with a failing chain hint = %v", err)
	}
}

// TestOwnedChainHints: a new name or slug of a Team sends the hint escalation-chain of every chain it owns, whose read
// shows it as its owning Team; a change of the description does not, and a failing read of the chains fails the save.
func TestOwnedChainHints(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	team, err := svc.Create(ctx, by, Input{Name: "Owner", Slug: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	store.ownedChains = map[int64][]dbgen.ListTeamOwnedChainsRow{team.ID: {{PublicID: chainB, Name: "b"},
		{PublicID: chainA, Name: "a"}}}
	hintsOf := func(from int) []string {
		var out []string
		for _, h := range store.hints[from:] {
			if h.Type == hintChain {
				out = append(out, h.ID)
			}
		}
		return out
	}
	from := len(store.hints)
	if _, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Owner", Slug: "owner",
		Description: ptr("only this")}); err != nil {
		t.Fatal(err)
	}
	if got := hintsOf(from); len(got) != 0 {
		t.Errorf("hints of a new description %v", got)
	}
	for _, in := range []Input{{Name: "Owner renamed", Slug: "owner"}, {Name: "Owner renamed", Slug: "owner-2"}} {
		from = len(store.hints)
		if _, err := svc.Update(ctx, by, team.PublicID, nil, in); err != nil {
			t.Fatal(err)
		}
		if got := hintsOf(from); !slices.Equal(got, []string{chainA, chainB}) {
			t.Errorf("hints of %+v: %v", in, got)
		}
	}
	store.fail["ListTeamOwnedChains"] = errBoom
	if _, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Owner again", Slug: "owner-2"}); !errors.Is(err,
		errBoom) {
		t.Errorf("a failing read of the owned chains = %v", err)
	}
}
