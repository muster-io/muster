// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
)

// ListRoles is listRoles: the three Roles with their Permissions (C-03.FR-2).
func (s *Server) ListRoles(context.Context, gen.ListRolesRequestObject) (gen.ListRolesResponseObject, error) {
	roles := s.sessions.Roles()
	items := make([]gen.Role, 0, len(auth.RoleNames))
	for _, name := range auth.RoleNames {
		items = append(items, gen.Role{Name: gen.RoleName(name), Permissions: permissionsOf(roles.Permissions(name))})
	}
	return gen.ListRoles200JSONResponse(gen.RoleList{Items: items}), nil
}
