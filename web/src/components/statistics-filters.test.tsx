// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The filters of the statistics page in a real browser, against a stubbed API (C-09.FR-30, C-09.AC-32): the pickers of
// the Alert Group list, the Team with "My Teams", and "Acknowledged first by" with "Anyone", "Me" and the Users of the
// directory, no Service account among them; none of the list's other filters, no status tabs and no search; the Russian
// texts.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import type { StatisticsSearch } from "../lib/statistics-search";
import { StatisticsFilters } from "./statistics-filters";

const USERS = [
  { id: "SR0000000000AL", name: "alice", login: "alice", deactivated: false },
  { id: "SR0000000000BO", name: "bob", login: "bob", deactivated: true },
];

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    switch (url.pathname) {
      case "/api/v1/routes":
        return Promise.resolve(
          json({ items: [{ id: "RT0000000000K8", name: "k8s", is_default: false }] }),
        );
      case "/api/v1/integrations":
        return Promise.resolve(
          json({ items: [{ id: "NT0000000000AM", name: "prod-am" }], next_cursor: null }),
        );
      case "/api/v1/teams":
        return Promise.resolve(
          json({
            items: [{ id: "TM0000000000PA", name: "payments", slug: "payments" }],
            next_cursor: null,
          }),
        );
      case "/api/v1/user-directory":
        return Promise.resolve(json({ items: USERS, next_cursor: null }));
      default:
        return Promise.resolve(json({}));
    }
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

const SESSION: SessionRead = {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the filters read only the Permissions
  session: { state: "active", permissions: ["routes:read", "integrations:read"] } as Session,
  ended: false,
};

/** The filters with the search kept as the page keeps it; `patches` records what each change asked for. */
async function renderFilters(initial: StatisticsSearch = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, SESSION);
  const patches: Partial<StatisticsSearch>[] = [];
  const matchers: string[][] = [];
  function Page() {
    const [search, setSearch] = useState<StatisticsSearch>(initial);
    return (
      <StatisticsFilters
        search={search}
        onChange={(patch) => {
          patches.push(patch);
          setSearch((prev) => ({ ...prev, ...patch }));
        }}
        applyMatchers={(next) => {
          matchers.push(next);
          setSearch((prev) => ({ ...prev, label: next.length > 0 ? next : undefined }));
          return Promise.resolve();
        }}
      />
    );
  }
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Page />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { patches, matchers };
}

describe("StatisticsFilters", () => {
  test("offers the filters of the list that count for statistics, and none of its others", async () => {
    await renderFilters();
    for (const label of [
      "Routes",
      "Integrations",
      "Severity",
      "Urgent",
      "Team",
      "Reopened",
      "Acknowledged first by",
    ]) {
      await expect.element(page.getByLabelText(label, { exact: true })).toBeVisible();
    }
    await expect.element(page.getByLabelText("Label filters")).toBeVisible();
    for (const absent of [
      "Resolved by",
      "Owner",
      "Snoozed with no end",
      "Delivery problem",
      "Unclaimed",
    ]) {
      await expect.element(page.getByLabelText(absent)).not.toBeInTheDocument();
    }
    await expect.element(page.getByRole("tab")).not.toBeInTheDocument();
    await expect.element(page.getByRole("searchbox", { name: "Search" })).not.toBeInTheDocument();
  });

  test("a chosen filter is passed on as the search parameter the list and the API name", async () => {
    const { patches } = await renderFilters();
    await page.getByLabelText("Routes", { exact: true }).selectOptions("RT0000000000K8");
    await page.getByLabelText("Severity", { exact: true }).selectOptions("critical");
    await page.getByLabelText("Urgent", { exact: true }).selectOptions("yes");
    await page.getByLabelText("Reopened", { exact: true }).selectOptions("no");
    await page.getByLabelText("Team", { exact: true }).selectOptions("mine");
    expect(patches).toEqual([
      { route: ["RT0000000000K8"] },
      { severity: ["critical"] },
      { urgent: true },
      { reopened: false },
      { team: "mine" },
    ]);
    await expect.element(page.getByLabelText("Team", { exact: true })).toHaveValue("mine");
  });

  test("Team offers All, My Teams and each Team", async () => {
    await renderFilters();
    const options = page.getByLabelText("Team", { exact: true }).getByRole("option");
    await expect.element(options.nth(0)).toHaveTextContent("All");
    await expect.element(options.nth(1)).toHaveTextContent("My Teams");
    await expect.element(options.nth(2)).toHaveTextContent("payments");
  });

  test("Acknowledged first by offers Anyone, Me and the Users, and picks me or a User", async () => {
    const { patches } = await renderFilters();
    const select = page.getByLabelText("Acknowledged first by");
    const options = select.getByRole("option");
    await expect.element(options.nth(0)).toHaveTextContent("Anyone");
    await expect.element(options.nth(1)).toHaveTextContent("Me");
    await expect.element(options.nth(2)).toHaveTextContent("alice");
    await expect.element(options.nth(3)).toHaveTextContent("bob (deactivated)");
    expect(options.elements()).toHaveLength(4);
    await select.selectOptions("me");
    await select.selectOptions("SR0000000000AL");
    await select.selectOptions("");
    expect(patches).toEqual([
      { first_acknowledged_by: "me" },
      { first_acknowledged_by: "SR0000000000AL" },
      { first_acknowledged_by: undefined },
    ]);
  });

  test("shows what the URL holds, adds Matchers and clears every filter", async () => {
    const { patches, matchers } = await renderFilters({
      severity: ["warning"],
      urgent: true,
      label: ['namespace="payments"'],
      first_acknowledged_by: "SR0000000000AL",
    });
    await expect.element(page.getByLabelText("Urgent", { exact: true })).toHaveValue("yes");
    await expect.element(page.getByText('namespace="payments"')).toBeVisible();
    await expect
      .element(page.getByLabelText("Acknowledged first by"))
      .toHaveValue("SR0000000000AL");
    await page.getByLabelText("Label filters").fill('cluster=~"t[1-3]"');
    await page.getByRole("button", { name: "Add matcher" }).click();
    await expect.poll(() => matchers).toEqual([['namespace="payments"', 'cluster=~"t[1-3]"']]);
    await page.getByRole("button", { name: "Clear filters" }).click();
    expect(patches).toEqual([
      {
        route: undefined,
        integration: undefined,
        severity: undefined,
        urgent: undefined,
        team: undefined,
        reopened: undefined,
        label: undefined,
        first_acknowledged_by: undefined,
      },
    ]);
    await expect
      .element(page.getByRole("button", { name: "Clear filters" }))
      .not.toBeInTheDocument();
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderFilters();
    const select = page.getByLabelText("Первым подтвердил");
    await expect.element(select).toBeVisible();
    await expect.element(select.getByRole("option").nth(0)).toHaveTextContent("Кто угодно");
    await expect.element(select.getByRole("option").nth(1)).toHaveTextContent("Я");
    await expect.element(page.getByLabelText("Команда", { exact: true })).toBeVisible();
  });
});
