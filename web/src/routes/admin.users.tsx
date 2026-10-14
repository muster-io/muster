// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Users (C-03.FR-18): the layout of /admin/users and /admin/users/{id}, which puts the sub-navigation of Access above
// the page.

import { Outlet, createFileRoute } from "@tanstack/react-router";

import { AccessLayout } from "../components/access-nav";

export const Route = createFileRoute("/admin/users")({
  staticData: { shell: true },
  component: UsersPages,
});

function UsersPages() {
  return (
    <AccessLayout>
      <Outlet />
    </AccessLayout>
  );
}
