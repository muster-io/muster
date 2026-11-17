// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "On call now" in a real browser (C-27.FR-8, FR-9, C-27.AC-6, AC-9): the Home page's section with "All Teams" and "By the
// main Schedule of each Team", each Team's person, Schedule and "until", the marks "unreachable in Mattermost" and "next:
// gap", "No Schedule", "My Teams" with only the Teams of the signed-in User, the Team page's row alone and the cell of the
// Teams list — in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { OnCallNow, TeamOnCall } from "../api/gen/model";
import i18n from "../i18n";
import { PETR, hasText, person, reply, seedSession } from "../test/fixtures/schedules";
import { teamRef } from "../test/fixtures/teams";
import { OnCallCell, OnCallNowSection, TeamOnCallNow } from "./on-call-now";

const IVAN = person("ivan");
const DEVOPS = teamRef({ id: "TM00000000DEVO", name: "devops", slug: "devops" });
const PAYMENTS = teamRef({ id: "TM0000000PAYM", name: "payments", slug: "payments" });
const SEARCH = teamRef({ id: "TM000000SEARC", name: "search", slug: "search" });
// Monday 2026-11-23 10:00 in Berlin.
const MONDAY = "2026-11-23T09:00:00Z";

const DEVOPS_ROW: TeamOnCall = {
  team: DEVOPS,
  schedule: { id: "SC0000000DEVO", name: "Main", team: DEVOPS },
  no_schedule: false,
  user: IVAN,
  rotation_layer: { id: "RL0000000000AA", name: "Around the clock" },
  until: MONDAY,
  next: { user: PETR, start: MONDAY, end: "2026-11-30T09:00:00Z" },
  unreachable: [],
};
const PAYMENTS_ROW: TeamOnCall = {
  team: PAYMENTS,
  schedule: { id: "SC0000000PAYM", name: "Main", team: PAYMENTS },
  no_schedule: false,
  user: PETR,
  rotation_layer: { id: "RL0000000000AA", name: "Around the clock" },
  until: MONDAY,
  next: { user: null, start: MONDAY, end: "2026-11-24T09:00:00Z" },
  unreachable: [
    {
      destination: {
        id: "DS00PAYNOTICE",
        name: "payments-notice",
        type: "mattermost",
        health: { state: "healthy" },
      },
      messenger: "mattermost",
      reason: "no_account_link",
    },
  ],
};
const SEARCH_ROW: TeamOnCall = {
  team: SEARCH,
  schedule: null,
  no_schedule: true,
  user: null,
  rotation_layer: null,
  until: null,
  next: null,
  unreachable: [],
};

let asked: string[] = [];

beforeEach(async () => {
  asked = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    asked.push(url);
    if (url.includes("/api/v1/on-call-now")) {
      const body: OnCallNow = {
        items: url.includes("teams=mine") ? [DEVOPS_ROW] : [DEVOPS_ROW, PAYMENTS_ROW, SEARCH_ROW],
      };
      return Promise.resolve(reply(200, body));
    }
    return Promise.resolve(reply(404, { type: "not-found", title: "Not found", status: 404 }));
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

async function renderWith(children: ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedSession(queryClient, IVAN, "admin", ["schedules:read", "teams:read"]);
  const root = createRootRoute({ component: () => children });
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

describe("On call now on the Home page", () => {
  test("shows each Team with the person, the Schedule, until when and the marks", async () => {
    await renderWith(<OnCallNowSection />);
    await expect.element(page.getByRole("heading", { name: "On call now" })).toBeVisible();
    await expect
      .element(page.getByRole("button", { name: "All Teams" }))
      .toHaveAttribute("aria-pressed", "true");
    await expect.element(page.getByText("By the main Schedule of each Team.")).toBeVisible();

    const devops = page.getByTestId("on-call-row").nth(0);
    await hasText(devops, "devops");
    await hasText(devops, "@ivan");
    await hasText(devops, "Main");
    await hasText(devops, "until Mon 10:00");
    expect(page.getByTestId("on-call-marks").elements()).toHaveLength(1);

    const payments = page.getByTestId("on-call-row").nth(1);
    await hasText(payments, "payments");
    await hasText(payments, "@petr");
    await hasText(payments, "unreachable in Mattermost");
    await hasText(payments, "next: gap");

    const search = page.getByTestId("on-call-row").nth(2);
    await hasText(search, "search");
    await hasText(search, "No Schedule");
    expect(search.element().textContent).not.toContain("until");
    expect(asked.some((url) => url.includes("teams=all"))).toBe(true);
  });

  test("follows the unreachable mark with the call that works when a gateway reaches the person", async () => {
    const reaching: TeamOnCall = {
      ...PAYMENTS_ROW,
      unreachable: PAYMENTS_ROW.unreachable.map((u) => ({ ...u, gateway_reaches: true })),
    };
    vi.spyOn(window, "fetch").mockImplementation(() =>
      Promise.resolve(reply(200, { items: [DEVOPS_ROW, reaching] })),
    );
    await renderWith(<OnCallNowSection />);
    const payments = page.getByTestId("on-call-row").nth(1);
    await hasText(payments, "unreachable in Mattermost");
    await hasText(payments, "A call through the Notification gateway works.");
    expect(page.getByTestId("on-call-gateway-reaches").elements()).toHaveLength(1);
    await i18n.changeLanguage("ru");
    await hasText(payments, "Звонок через шлюз оповещений доходит.");
  });

  test("shows no such note without gateway_reaches", async () => {
    await renderWith(<OnCallNowSection />);
    await hasText(page.getByTestId("on-call-row").nth(1), "unreachable in Mattermost");
    expect(page.getByTestId("on-call-gateway-reaches").elements()).toHaveLength(0);
  });

  test("My Teams reads only the Teams of the signed-in User", async () => {
    await renderWith(<OnCallNowSection />);
    await expect.element(page.getByText("No Schedule")).toBeVisible();
    await page.getByRole("button", { name: "My Teams" }).click();
    await expect
      .element(page.getByRole("button", { name: "My Teams" }))
      .toHaveAttribute("aria-pressed", "true");
    await vi.waitFor(() => expect(page.getByTestId("on-call-row").elements()).toHaveLength(1));
    await hasText(page.getByTestId("on-call-row"), "devops");
    expect(asked.some((url) => url.includes("teams=mine"))).toBe(true);
    expect(page.getByText("payments").elements()).toHaveLength(0);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderWith(<OnCallNowSection />);
    await expect.element(page.getByRole("heading", { name: "Сейчас дежурит" })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Все команды" })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Мои команды" })).toBeVisible();
    await expect.element(page.getByText("По главному расписанию каждой команды.")).toBeVisible();
    await hasText(page.getByTestId("on-call-row").nth(0), "до пн 10:00");
    const payments = page.getByTestId("on-call-row").nth(1);
    await hasText(payments, "недостижим в Mattermost");
    await hasText(payments, "дальше дыра");
    await hasText(page.getByTestId("on-call-row").nth(2), "Без расписания");
  });
});

describe("On call now of a Team and of the Teams list", () => {
  test("the Team page shows the row of its Team alone", async () => {
    await renderWith(<TeamOnCallNow teamId={PAYMENTS.id} />);
    await expect.element(page.getByRole("heading", { name: "On call now" })).toBeVisible();
    const row = page.getByTestId("on-call-row");
    await hasText(row, "payments");
    await hasText(row, "@petr");
    await hasText(row, "unreachable in Mattermost");
    await hasText(row, "next: gap");
    expect(page.getByTestId("on-call-row").elements()).toHaveLength(1);
  });

  test("the Teams list shows the person, or No Schedule", async () => {
    await renderWith(
      <>
        <OnCallCell teamId={DEVOPS.id} />
        <OnCallCell teamId={PAYMENTS.id} />
        <OnCallCell teamId={SEARCH.id} />
      </>,
    );
    await vi.waitFor(() => expect(page.getByTestId("team-on-call").elements()).toHaveLength(3));
    await hasText(page.getByTestId("team-on-call").nth(0), "@ivan");
    await hasText(page.getByTestId("team-on-call").nth(1), "@petr");
    await hasText(page.getByTestId("team-on-call").nth(1), "unreachable in Mattermost");
    await hasText(page.getByTestId("team-on-call").nth(2), "No Schedule");
  });
});
