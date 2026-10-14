// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// /admin/access (C-03.FR-18) leads to the first page of the sub-navigation of Access the session can open; a session
// that can open none of them sees that it has no access.

import { createFileRoute } from "@tanstack/react-router";

import { ACCESS_NAVIGATION } from "../components/access-nav";
import { NoPermission } from "../components/app-shell";
import { leadToFirstPage } from "../components/section-nav";

export const Route = createFileRoute("/admin/access")({
  staticData: { shell: true },
  beforeLoad: leadToFirstPage(ACCESS_NAVIGATION),
  component: NoPermission,
});
