// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
)

// fakeLinks are the account_links rows joined with their Users, by identity space and external id.
type fakeLinks struct {
	rows map[[2]string]dbgen.LookupAccountLinkRow
	err  error
	org  int64
}

func (f *fakeLinks) LookupAccountLink(_ context.Context, arg dbgen.LookupAccountLinkParams) (
	dbgen.LookupAccountLinkRow, error) {
	f.org = arg.OrgID
	if f.err != nil {
		return dbgen.LookupAccountLinkRow{}, f.err
	}
	r, ok := f.rows[[2]string{arg.IdentitySpace, arg.ExternalID}]
	if !ok {
		return dbgen.LookupAccountLinkRow{}, pgx.ErrNoRows
	}
	return r, nil
}

func TestLookup(t *testing.T) {
	bob := dbgen.LookupAccountLinkRow{ID: 7, PublicID: "SRAAAAAAAAAAB1", Login: "bob", Name: "Bob", Role: "responder",
		Status: StatusActive}
	f := &fakeLinks{rows: map[[2]string]dbgen.LookupAccountLinkRow{{SpaceMattermost(5), "u-bob"}: bob}}
	s := &Service{orgID: 3, q: f}
	u, err := s.Lookup(t.Context(), SpaceMattermost(5), "u-bob")
	if err != nil || u != (User{ID: 7, PublicID: "SRAAAAAAAAAAB1", Login: "bob", Name: "Bob", Role: "responder",
		Status: StatusActive}) || f.org != 3 {
		t.Fatalf("Lookup = %+v, %v in org %d", u, err, f.org)
	}
	for _, c := range [][2]string{{SpaceMattermost(6), "u-bob"}, {SpaceMattermost(5), "u-alice"},
		{SpaceTelegram, "u-bob"}} {
		if _, err := s.Lookup(t.Context(), c[0], c[1]); !errors.Is(err, ErrNotLinked) {
			t.Errorf("Lookup(%q, %q) = %v, want ErrNotLinked", c[0], c[1], err)
		}
	}
	f.err = errors.New("boom")
	if _, err := s.Lookup(t.Context(), SpaceMattermost(5), "u-bob"); err == nil || errors.Is(err, ErrNotLinked) {
		t.Errorf("a failed read = %v", err)
	}
	if SpaceMattermost(12) != "mattermost:12" || New(1, nil) == nil {
		t.Error("the identity space of a Connection")
	}
}
