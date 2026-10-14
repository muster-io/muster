// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sub-navigation of Organization in a real browser: its entries in order, each with its Permission, the page an
// address without a sub-entry leads to, and the landmark it is.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import type { Permission } from "../api/gen/model";
import i18n from "../i18n";
import {
  ORGANIZATION_NAVIGATION,
  OrganizationNav,
  firstOrganizationPage,
  organizationEntries,
} from "./organization-nav";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

const ADMIN: Permission[] = [
  "organization:write",
  "organization:read",
  "lookup-tables:read",
  "link-rules:read",
  "system-status:read",
];
const RESPONDER: Permission[] = ["alert-groups:read", "lookup-tables:read", "link-rules:read"];

async function renderNav(permissions: Permission[]) {
  const rootRoute = createRootRoute({
    component: () => <OrganizationNav permissions={permissions} />,
  });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/admin/organization/keyring"] }),
  });
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={new QueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

describe("organization navigation", () => {
  test("lists the eleven entries in order, each with its Permission", () => {
    expect(ORGANIZATION_NAVIGATION.map((e) => [e.to, e.permission])).toEqual([
      ["/admin/organization/general", "organization:write"],
      ["/admin/organization/severity-levels", "organization:write"],
      ["/admin/organization/alert-handling", "organization:write"],
      ["/admin/organization/security", "organization:write"],
      ["/admin/organization/outbound", "organization:read"],
      ["/admin/organization/retention", "organization:write"],
      ["/admin/organization/keyring", "organization:read"],
      ["/admin/organization/outgoing-heartbeat", "organization:write"],
      ["/admin/organization/lookup-tables", "lookup-tables:read"],
      ["/admin/organization/link-rules", "link-rules:read"],
      ["/admin/system-status", "system-status:read"],
    ]);
  });

  test("an Admin sees every entry in a landmark named Organization", async () => {
    const screen = await renderNav(ADMIN);
    const nav = screen.getByRole("navigation", { name: "Organization" });
    await expect.element(nav).toBeVisible();
    const names = nav
      .getByRole("link")
      .elements()
      .map((e) => e.textContent);
    expect(names).toEqual([
      "General",
      "Severity levels",
      "Alert handling",
      "Security",
      "Outbound addresses",
      "Retention",
      "Keyring",
      "Outgoing heartbeat",
      "Lookup tables",
      "Link rules",
      "System status",
    ]);
    await expect
      .element(nav.getByRole("link", { name: "Keyring" }))
      .toHaveAttribute("data-status", "active");
    await expect
      .element(nav.getByRole("link", { name: "System status" }))
      .toHaveAttribute("href", "/admin/system-status");
  });

  test("a Responder sees only Lookup tables and Link rules", async () => {
    expect(organizationEntries(RESPONDER).map((e) => e.label)).toEqual([
      "lookupTables",
      "linkRules",
    ]);
    const screen = await renderNav(RESPONDER);
    const nav = screen.getByRole("navigation", { name: "Organization" });
    await expect.element(nav.getByRole("link", { name: "Lookup tables" })).toBeVisible();
    expect(nav.getByRole("link").elements()).toHaveLength(2);
  });

  test("the address of Organization leads to the first entry the session can open", () => {
    expect(firstOrganizationPage(ADMIN)).toBe("/admin/organization/general");
    expect(firstOrganizationPage(RESPONDER)).toBe("/admin/organization/lookup-tables");
    expect(firstOrganizationPage(["system-status:read"])).toBe("/admin/system-status");
    expect(firstOrganizationPage(["alert-groups:read"])).toBeUndefined();
  });
});
