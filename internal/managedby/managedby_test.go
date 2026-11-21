// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package managedby

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
)

func TestValidate(t *testing.T) {
	for _, tt := range []struct {
		in string
		ok bool
	}{
		{"terraform", true},
		{"opentofu", true},
		{"pulumi.v2", true},
		{strings.Repeat("a", 32), true},
		{"", false},
		{"Terraform", false},
		{"-tf", false},
		{"a b", false},
		{strings.Repeat("a", 33), false},
	} {
		err := Validate(tt.in)
		if tt.ok && err != nil {
			t.Errorf("Validate(%q) = %v, want nil", tt.in, err)
		}
		if !tt.ok && !errors.Is(err, ErrInvalidFormat) {
			t.Errorf("Validate(%q) = %v, want ErrInvalidFormat", tt.in, err)
		}
	}
}

func TestInput(t *testing.T) {
	stored := "terraform"
	for _, tt := range []struct {
		name  string
		in    Input
		want  *string
		check error
	}{
		{"omitted keeps", Input{}, &stored, nil},
		{"null clears", Clear, nil, nil},
		{"value replaces", Set("opentofu"), new("opentofu"), nil},
		{"invalid value", Set("Terraform"), new("Terraform"), ErrInvalidFormat},
	} {
		got := tt.in.Apply(&stored)
		if (got == nil) != (tt.want == nil) || got != nil && *got != *tt.want {
			t.Errorf("%s: Apply = %v, want %v", tt.name, got, tt.want)
		}
		if err := tt.in.Check(); !errors.Is(err, tt.check) {
			t.Errorf("%s: Check = %v, want %v", tt.name, err, tt.check)
		}
	}
	if got := (Input{}).Apply(nil); got != nil {
		t.Errorf("omitted on a creation: Apply = %q, want nil", *got)
	}
}

func TestOnly(t *testing.T) {
	mark := audit.Change{Pointer: "/managed_by", Before: "terraform"}
	name := audit.Change{Pointer: "/name", Before: "a", After: "b"}
	for _, tt := range []struct {
		changes []audit.Change
		want    bool
	}{
		{nil, false},
		{[]audit.Change{mark}, true},
		{[]audit.Change{name}, false},
		{[]audit.Change{mark, name}, false},
		{[]audit.Change{{Pointer: "/managed_by/x"}}, false},
	} {
		if got := Only(tt.changes); got != tt.want {
			t.Errorf("Only(%+v) = %v, want %v", tt.changes, got, tt.want)
		}
	}
}

func TestText(t *testing.T) {
	if got := Text(nil); got.Valid {
		t.Errorf("Text(nil) = %+v", got)
	}
	if got := Text(new("terraform")); got != (pgtype.Text{String: "terraform", Valid: true}) {
		t.Errorf("Text(terraform) = %+v", got)
	}
	if got := Of(pgtype.Text{}); got != nil {
		t.Errorf("Of(null) = %q", *got)
	}
	if got := Of(pgtype.Text{String: "terraform", Valid: true}); got == nil || *got != "terraform" {
		t.Errorf("Of(terraform) = %v", got)
	}
}
