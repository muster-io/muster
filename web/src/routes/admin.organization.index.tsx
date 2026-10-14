// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// /admin/organization leads to the first page of the sub-navigation the session can open. The root route has read the
// session already; a session that can open none of them sees that it has no access.

import { createFileRoute } from "@tanstack/react-router";

import { NoPermission } from "../components/app-shell";
import { ORGANIZATION_NAVIGATION } from "../components/organization-nav";
import { leadToFirstPage } from "../components/section-nav";

export const Route = createFileRoute("/admin/organization/")({
  staticData: { shell: true },
  beforeLoad: leadToFirstPage(ORGANIZATION_NAVIGATION),
  component: NoPermission,
});
