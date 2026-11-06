// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Team label and the unknown values on the Teams page in a real browser, against a stubbed API (C-25.FR-7,
// C-25.AC-10): the label with its hint and the link to change it, the section named after the label with each value,
// when it was last seen and "Create Team", which opens the form with the value as the slug, or, for a value no slug can
// take, with the note about Team matchers; nothing for a person who may not create Teams, nothing without values;
// a failed read says why; in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import { getGetOrganizationQueryKey } from "../api/gen/endpoints/organization/organization";
import type { Permission, Session, TeamLabelValueList } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { TeamLabelLine, TeamLabelValues } from "./team-label-values";

/** Waits until the text of an element holds a text. */
const contains = (el: { element: () => Element }, text: string) =>
  expect.poll(() => el.element().textContent ?? "").toContain(text);

const NOW = Date.now();
const iso = (minutesAgo: number) => new Date(NOW - minutesAgo * 60_000).toISOString();

let values: TeamLabelValueList;
let status = 200;
let orgLabel = "team";
let orgStatus = 200;

beforeEach(async () => {
  await i18n.changeLanguage("en");
  status = 200;
  orgLabel = "team";
  orgStatus = 200;
  values = {
    label: "team",
    window_days: 7,
    items: [
      { value: "checkout", last_seen_at: iso(5), alert_count: 3, valid_slug: true },
      { value: "Payments", last_seen_at: iso(120), alert_count: 1, valid_slug: false },
    ],
  };
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const [code, body] = url.endsWith("/organization")
      ? [orgStatus, { team_label: orgLabel }]
      : [status, values];
    return Promise.resolve(
      code === 200
        ? new Response(JSON.stringify(body), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          })
        : new Response(JSON.stringify({ type: "about:blank", title: "Boom", status: code }), {
            status: code,
            headers: { "Content-Type": "application/problem+json" },
          }),
    );
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

async function renderPage(
  permissions: Permission[] = ["teams:read", "teams:write", "organization:write"],
) {
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the page reads only the Permissions
      user: { id: "US0000000000ME", name: "Me", time_zone: "UTC" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T19:00:00Z",
      idle_expires_at: "2026-10-13T19:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions,
    },
    ended: false,
  };
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const root = createRootRoute({
    component: () => (
      <>
        <TeamLabelLine />
        <TeamLabelValues />
      </>
    ),
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
  await expect.element(page.getByTestId("team-label")).toBeVisible();
  return { queryClient, router };
}

describe("the Team label and its unknown values", () => {
  test("show the label with its hint, and the values with when they were last seen", async () => {
    await renderPage();
    await contains(page.getByTestId("team-label-name"), "team");
    await contains(
      page.getByTestId("team-label"),
      "An Alert belongs to a Team by the value of the Team label or by the Team's matchers. Teams do not limit access: Roles are the same for the whole Organization.",
    );
    await expect
      .element(page.getByRole("link", { name: "Change in the Organization settings" }))
      .toHaveAttribute("href", "/admin/organization/general");
    await expect
      .element(page.getByRole("heading", { name: "Unknown values of team" }))
      .toBeVisible();
    await contains(
      page.getByTestId("team-label-values"),
      "Alerts arrived with these values, but there is no such Team: no Destinations of a Team are added and nobody is Paged.",
    );
    const rows = page.getByTestId("team-label-value").elements();
    expect(rows.map((r) => r.querySelector("code")?.textContent)).toEqual([
      "team=checkout",
      "team=Payments",
    ]);
    const first = page.getByTestId("team-label-value").first();
    await contains(first, "(5 min ago)");
    await contains(first, "3 Alerts in the last 7 days");
    await contains(page.getByTestId("team-label-value").nth(1), "1 Alert in the last 7 days");
  });

  test("open the new Team form with the value as the slug, or with the note for a value that is no slug", async () => {
    await renderPage();
    await expect
      .element(page.getByRole("link", { name: "Create Team for the value checkout" }))
      .toHaveAttribute("href", "/teams/new?slug=checkout");
    await expect
      .element(page.getByRole("link", { name: "Create Team for the value Payments" }))
      .toHaveAttribute("href", "/teams/new?value=Payments");
    expect(page.getByRole("link", { name: "Create Team", exact: true }).elements()).toHaveLength(0);
  });

  test("name the section after the Team label of the Organization", async () => {
    values = { ...values, label: "service_team" };
    orgLabel = "service_team";
    await renderPage();
    await expect
      .element(page.getByRole("heading", { name: "Unknown values of service_team" }))
      .toBeVisible();
    await contains(page.getByTestId("team-label-name"), "service_team");
  });

  test("take the label from the Organization, and show none until it is known", async () => {
    // The values carry another label than the Organization's, as in the moment after a change.
    values = { ...values, label: "team" };
    orgLabel = "service_team";
    await renderPage();
    await expect
      .poll(() => page.getByTestId("team-label-name").element().textContent)
      .toBe("service_team");
  });

  test("say why the Organization could not be read, and name no label", async () => {
    orgStatus = 500;
    await renderPage();
    await expect.element(page.getByTestId("team-label-error")).toBeVisible();
    expect(page.getByTestId("team-label-name").element().textContent).toBe("…");
  });

  test("keep the label and say why when a later read of the Organization fails", async () => {
    const { queryClient } = await renderPage();
    await expect.poll(() => page.getByTestId("team-label-name").element().textContent).toBe("team");
    orgStatus = 500;
    await queryClient.invalidateQueries({ queryKey: getGetOrganizationQueryKey() });
    await expect.element(page.getByTestId("team-label-error")).toBeVisible();
    expect(page.getByTestId("team-label-name").element().textContent).toBe("team");
  });

  test("offer no Create Team and no link to the settings to a person who may not", async () => {
    await renderPage(["teams:read"]);
    await expect.element(page.getByTestId("team-label-values")).toBeVisible();
    expect(page.getByRole("link", { name: /Create Team/ }).elements()).toHaveLength(0);
    expect(
      page.getByRole("link", { name: "Change in the Organization settings" }).elements(),
    ).toHaveLength(0);
  });

  test("show no section without values", async () => {
    values = { ...values, items: [] };
    await renderPage();
    expect(page.getByTestId("team-label-values").elements()).toHaveLength(0);
  });

  test("say why the values could not be read", async () => {
    status = 500;
    await renderPage();
    await expect.element(page.getByRole("alert")).toBeVisible();
    expect(page.getByTestId("team-label-values").elements()).toHaveLength(0);
  });

  test("speak Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderPage();
    await contains(
      page.getByTestId("team-label"),
      "Алерт относится к команде по значению метки команды или по её матчерам. Доступ команды не ограничивают: роли общие для организации.",
    );
    await expect
      .element(page.getByRole("heading", { name: "Неизвестные значения team" }))
      .toBeVisible();
    await contains(
      page.getByTestId("team-label-values"),
      "Алерты приходили с этими значениями, но такой команды нет: места доставки команды не добавляются, никого не зовут.",
    );
    await expect
      .element(page.getByRole("link", { name: "Создать команду для значения checkout" }))
      .toBeVisible();
    await contains(page.getByTestId("team-label-value").first(), "3 алерта за последние 7 дн.");
  });
});
