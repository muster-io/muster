// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Teams of a Destination on its page (C-26.FR-1): the Teams that name it, linked with teams:read; a deleted Team and a
// reader without teams:read as plain names; "No Team names this Destination." when none does; the Russian text.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Permission, Session, TeamRef } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { mattermostDestination } from "../test/fixtures/destinations";
import { teamRef } from "../test/fixtures/teams";
import { TeamsOf } from "../routes/destinations.$destinationId";

async function renderTeams(teams: TeamRef[], permissions: Permission[]) {
  const queryClient = new QueryClient();
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the section reads only the Permissions
      user: { id: "US0000000000ME", name: "Me" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T19:00:00Z",
      idle_expires_at: "2026-10-13T19:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const root = createRootRoute({
    component: () => <TeamsOf destination={mattermostDestination({ teams })} />,
  });
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("the Teams of a Destination", () => {
  test("links the Teams that name it, a deleted Team as a name", async () => {
    await renderTeams(
      [
        teamRef({ id: "TM0000000000PA", name: "payments" }),
        teamRef({ id: "TM0000000000OL", name: "old", deleted: true }),
      ],
      ["teams:read"],
    );
    await expect.element(page.getByRole("heading", { name: "Teams" })).toBeVisible();
    await expect
      .element(page.getByRole("link", { name: "payments" }))
      .toHaveAttribute("href", "/teams/TM0000000000PA");
    expect(page.getByRole("link", { name: "old" }).elements()).toHaveLength(0);
    await expect.element(page.getByText("old", { exact: true })).toBeVisible();
  });

  test("shows names without teams:read", async () => {
    await renderTeams([teamRef({ name: "payments" })], []);
    await expect.element(page.getByText("payments", { exact: true })).toBeVisible();
    expect(page.getByRole("link").elements()).toHaveLength(0);
  });

  test("says so when no Team names it, in Russian too", async () => {
    await renderTeams([], ["teams:read"]);
    await expect.element(page.getByText("No Team names this Destination.")).toBeVisible();
    await i18n.changeLanguage("ru");
    await expect
      .element(page.getByText("Ни одна команда не указывает это место доставки."))
      .toBeVisible();
    await expect.element(page.getByRole("heading", { name: "Команды" })).toBeVisible();
  });
});
