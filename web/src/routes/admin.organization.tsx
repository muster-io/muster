// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization (C-20.FR-1): the layout of every /admin/organization/* page, which puts the sub-navigation above the
// page. System status keeps its own address and renders the same sub-navigation itself.

import { Outlet, createFileRoute } from "@tanstack/react-router";

import { OrganizationLayout } from "../components/organization-nav";

export const Route = createFileRoute("/admin/organization")({
  staticData: { shell: true },
  component: OrganizationPages,
});

function OrganizationPages() {
  return (
    <OrganizationLayout>
      <Outlet />
    </OrganizationLayout>
  );
}
