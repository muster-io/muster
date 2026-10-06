// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
)

// permissionSystemStatus is the Permission that shows the notices for Admins.
const permissionSystemStatus auth.Permission = "system-status:read"

// ListSystemNotices is listSystemNotices: the active Organization-wide notices (C-03.FR-18, C-02.FR-24) — "recovering
// after downtime" for every signed-in identity, "no replica is leading" only for one that holds system-status:read.
func (s *Server) ListSystemNotices(ctx context.Context, _ gen.ListSystemNoticesRequestObject) (
	gen.ListSystemNoticesResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	notices, err := s.notices.Visible(ctx, id.Can(permissionSystemStatus))
	if err != nil {
		return nil, err
	}
	out := gen.SystemNoticeList{Items: make([]gen.SystemNotice, 0, len(notices))}
	for _, n := range notices {
		item := gen.SystemNotice{Kind: gen.SystemNoticeKind(n.Kind), Audience: gen.SystemNoticeAudience(n.Audience)}
		if n.Since.IsZero() {
			item.Since.SetNull()
		} else {
			item.Since.Set(n.Since.UTC())
		}
		if n.Until.IsZero() {
			item.Until.SetNull()
		} else {
			item.Until.Set(n.Until.UTC())
		}
		out.Items = append(out.Items, item)
	}
	return gen.ListSystemNotices200JSONResponse(out), nil
}
