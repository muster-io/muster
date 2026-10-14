// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Service accounts (C-03.FR-18): the layout of /admin/service-accounts and /admin/service-accounts/{id}, which puts the
// sub-navigation of Access above the page.

import { Outlet, createFileRoute } from "@tanstack/react-router";

import { AccessLayout } from "../components/access-nav";

export const Route = createFileRoute("/admin/service-accounts")({
  staticData: { shell: true },
  component: ServiceAccountsPages,
});

function ServiceAccountsPages() {
  return (
    <AccessLayout>
      <Outlet />
    </AccessLayout>
  );
}
