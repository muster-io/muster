// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package auth

import (
	"context"
	"fmt"
	"slices"

	"github.com/muster-io/muster/internal/auth/dbgen"
)

// Permission is one thing a Role allows, named <resource>:<verb> (C-03.FR-2).
type Permission string

// The values of x-permission that are not Permissions.
const (
	// PermissionNone is a public operation, or one the request authenticates itself.
	PermissionNone = "none"
	// PermissionAuthenticated is any signed-in identity.
	PermissionAuthenticated = "authenticated"
	// PermissionIntegrationToken is ingestion and Heartbeat, with an Integration token.
	PermissionIntegrationToken = "integration-token"
)

// The Roles of L1, in the order the API lists them.
const (
	RoleAdmin     = "admin"
	RoleResponder = "responder"
	RoleViewer    = "viewer"
)

// RoleNames are the Roles, highest first.
var RoleNames = []string{RoleAdmin, RoleResponder, RoleViewer}

// Roles is the allocation of Permissions to Roles, loaded at startup from role_permissions; every check is made
// against a Permission, never against a Role.
type Roles map[string][]Permission

// LoadRoles reads the allocation; every Role of RoleNames must be in it.
func LoadRoles(ctx context.Context, q interface {
	ListRolePermissions(ctx context.Context) ([]dbgen.RolePermission, error)
}) (Roles, error) {
	rows, err := q.ListRolePermissions(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the permissions of the roles: %w", err)
	}
	r := Roles{}
	for _, row := range rows {
		r[row.Role] = append(r[row.Role], Permission(row.Permission))
	}
	for _, name := range RoleNames {
		if len(r[name]) == 0 {
			return nil, fmt.Errorf("the role %s has no permissions in role_permissions", name)
		}
		slices.Sort(r[name])
	}
	return r, nil
}

// Permissions returns the Permissions of role, sorted; an unknown Role has none.
func (r Roles) Permissions(role string) []Permission {
	return slices.Clone(r[role])
}

// Has reports whether role holds p.
func (r Roles) Has(role string, p Permission) bool {
	_, found := slices.BinarySearch(r[role], p)
	return found
}
