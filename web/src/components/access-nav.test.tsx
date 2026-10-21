// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sub-navigation of Access in a real browser: Users, Service accounts and OIDC in order, each with its Permission,
// the page /admin/access leads to, and the landmark it is.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
  isRedirect,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import type { Permission, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { ACCESS_NAVIGATION, AccessNav, accessEntries, firstAccessPage } from "./access-nav";
import { leadToFirstPage } from "./section-nav";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

const ADMIN: Permission[] = [
  "users:read",
  "users:write",
  "service-accounts:read",
  "oidc:read",
  "organization:write",
  "audit-log:read",
];
const RESPONDER: Permission[] = ["alert-groups:read", "lookup-tables:read", "link-rules:read"];

/** The context of a beforeLoad whose QueryClient holds a session with these Permissions. */
function beforeLoadContext(permissions: Permission[]) {
  const queryClient = new QueryClient();
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the beforeLoad reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-09T09:00:00Z",
      idle_expires_at: "2026-10-09T09:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return { context: { queryClient } };
}

async function renderNav(permissions: Permission[], path = "/admin/users/abc") {
  const rootRoute = createRootRoute({
    component: () => <AccessNav permissions={permissions} />,
  });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={new QueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

describe("access navigation", () => {
  test("lists Users, Service accounts and OIDC in order, each with its Permission", () => {
    expect(ACCESS_NAVIGATION.map((e) => [e.to, e.permission])).toEqual([
      ["/admin/users", "users:read"],
      ["/admin/service-accounts", "service-accounts:read"],
      ["/admin/oidc", "oidc:read"],
    ]);
  });

  test("an Admin sees every entry in a landmark named Access, Users active on a user's page", async () => {
    const screen = await renderNav(ADMIN);
    const nav = screen.getByRole("navigation", { name: "Access" });
    await expect.element(nav).toBeVisible();
    const names = nav
      .getByRole("link")
      .elements()
      .map((e) => e.textContent);
    expect(names).toEqual(["Users", "Service accounts", "OIDC"]);
    await expect
      .element(nav.getByRole("link", { name: "Users" }))
      .toHaveAttribute("data-status", "active");
    await expect
      .element(nav.getByRole("link", { name: "OIDC" }))
      .not.toHaveAttribute("data-status", "active");
    await expect
      .element(nav.getByRole("link", { name: "Service accounts" }))
      .toHaveAttribute("href", "/admin/service-accounts");
  });

  test("the texts in Russian", async () => {
    await i18n.changeLanguage("ru");
    const screen = await renderNav(ADMIN);
    const nav = screen.getByRole("navigation", { name: "Доступ" });
    const names = nav
      .getByRole("link")
      .elements()
      .map((e) => e.textContent);
    expect(names).toEqual(["Пользователи", "Сервисные аккаунты", "OIDC"]);
  });

  test("each entry shows only with its Permission", () => {
    expect(accessEntries(["oidc:read"]).map((e) => e.label)).toEqual(["oidc"]);
    expect(accessEntries(["service-accounts:read", "oidc:read"]).map((e) => e.label)).toEqual([
      "serviceAccounts",
      "oidc",
    ]);
    expect(accessEntries(RESPONDER)).toEqual([]);
  });

  test("/admin/access leads to the first entry the session can open", () => {
    expect(firstAccessPage(ADMIN)).toBe("/admin/users");
    expect(firstAccessPage(["oidc:read"])).toBe("/admin/oidc");
    expect(firstAccessPage(["service-accounts:read", "oidc:read"])).toBe("/admin/service-accounts");
    expect(firstAccessPage(RESPONDER)).toBeUndefined();
  });

  test("the beforeLoad of /admin/access redirects to the first entry, and lets a session without one through", () => {
    const beforeLoad = leadToFirstPage(ACCESS_NAVIGATION);

    let thrown: unknown;
    try {
      beforeLoad(beforeLoadContext(ADMIN));
    } catch (error) {
      thrown = error;
    }
    expect(isRedirect(thrown)).toBe(true);
    if (isRedirect(thrown)) {
      expect(thrown.options.href).toBe("/admin/users");
      expect(thrown.options.replace).toBe(true);
    }

    expect(() => beforeLoad(beforeLoadContext(RESPONDER))).not.toThrow();
  });

  test("a session that can open no entry gets no sub-navigation", async () => {
    const screen = await renderNav(RESPONDER);
    await expect.element(screen.getByRole("navigation")).not.toBeInTheDocument();
  });
});
