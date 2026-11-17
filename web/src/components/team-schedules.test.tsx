// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Schedules of a Team's page in a real browser (C-27.FR-1): each Schedule with "main", its Rotation layers, its
// On-call now and its warnings, linked to its page, "New Schedule" for Admins only and the hint about the main Schedule,
// in English and in Russian.

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

import type { Permission, Schedule } from "../api/gen/model";
import i18n from "../i18n";
import {
  OLGA,
  PETR,
  hasText,
  onCall,
  reply,
  rotationLayer,
  schedule,
  seedSession,
  segment,
  warning,
} from "../test/fixtures/schedules";
import { TeamSchedules } from "./team-schedules";

const TEAM = "TM0000000000PA";
const MAIN = schedule({
  id: "SC0000000000MA",
  name: "Main",
  main: true,
  rotation_layers: [
    rotationLayer({ id: "RL0000000000AA", name: "Around the clock" }),
    rotationLayer({ id: "RL0000000000BB", name: "Working hours 11-19", number: 2 }),
  ],
});
const SECOND = schedule({
  id: "SC00000000SEC",
  name: "Second line",
  main: false,
  rotation_layers: [rotationLayer({ name: "Nights" })],
});

const MAIN_ON_CALL = onCall({
  from: "2026-11-20T10:00:00Z",
  to: "2026-12-04T10:00:00Z",
  segments: [
    segment("2026-11-20T10:00:00Z", "2026-11-23T09:00:00Z", PETR),
    segment("2026-11-23T09:00:00Z", "2026-11-24T09:00:00Z", null),
    segment("2026-11-24T09:00:00Z", "2026-12-04T10:00:00Z", OLGA),
  ],
  warnings: [warning({ kind: "gap", start: "2026-11-23T09:00:00Z", end: "2026-11-24T09:00:00Z" })],
});
const SECOND_ON_CALL = onCall({
  from: "2026-11-20T10:00:00Z",
  to: "2026-12-04T10:00:00Z",
  segments: [segment("2026-11-20T10:00:00Z", "2026-12-04T10:00:00Z", OLGA)],
});

beforeEach(async () => {
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.includes("/api/v1/schedules?")) {
      const items: Schedule[] = [MAIN, SECOND];
      return Promise.resolve(reply(200, { items, next_cursor: null }));
    }
    if (url.includes(`/api/v1/schedules/${MAIN.id}/on-call`)) {
      return Promise.resolve(reply(200, MAIN_ON_CALL));
    }
    if (url.includes(`/api/v1/schedules/${SECOND.id}/on-call`)) {
      return Promise.resolve(reply(200, SECOND_ON_CALL));
    }
    return Promise.resolve(reply(404, { type: "not-found", title: "Not found", status: 404 }));
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

async function renderSection(permissions: Permission[]) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedSession(queryClient, PETR, "admin", permissions);
  const root = createRootRoute({ component: () => <TeamSchedules teamId={TEAM} /> });
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

describe("Schedules of a Team", () => {
  test("lists each Schedule with main, its Rotation layers, its On-call and its warnings", async () => {
    await renderSection(["schedules:read", "schedules:write"]);
    await expect.element(page.getByRole("heading", { name: "Schedules" })).toBeVisible();
    await expect
      .element(
        page.getByText(
          "The main Schedule names the Team's On-call shown under 'On call now'; Escalation steps may Page the On-call of any of them.",
        ),
      )
      .toBeVisible();
    await expect
      .element(page.getByRole("link", { name: "Main", exact: true }))
      .toHaveAttribute("href", "/schedules/SC0000000000MA");
    await expect
      .element(page.getByRole("link", { name: "New Schedule" }))
      .toHaveAttribute("href", `/schedules/new?team=${TEAM}`);

    const main = page.getByTestId("team-schedule").nth(0);
    await vi.waitFor(() => expect(main.element().textContent).toContain("On-call: @petr"));
    await hasText(main, "main");
    await hasText(main, "2 Rotation layers · Around the clock, Working hours 11-19");
    await hasText(main, "until Mon 10:00");
    await hasText(main, "Gap: Mon 10:00 – Tue 10:00");

    const second = page.getByTestId("team-schedule").nth(1);
    await vi.waitFor(() => expect(second.element().textContent).toContain("On-call: @olga"));
    await hasText(second, "1 Rotation layer · Nights");
    await hasText(second, "No warnings");
    expect(second.element().textContent).not.toContain("until");
    expect(second.getByTestId("schedule-main").elements()).toHaveLength(0);
  });

  test("offers New Schedule to Admins only", async () => {
    await renderSection(["schedules:read"]);
    await expect.element(page.getByRole("link", { name: "Main", exact: true })).toBeVisible();
    expect(page.getByRole("link", { name: "New Schedule" }).elements()).toHaveLength(0);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderSection(["schedules:read", "schedules:write"]);
    await expect.element(page.getByRole("heading", { name: "Расписания" })).toBeVisible();
    await expect
      .element(
        page.getByText(
          "Главное расписание показывается на главной в «Сейчас дежурит»; шаги цепочки могут звать дежурного любого из них.",
        ),
      )
      .toBeVisible();
    await expect.element(page.getByRole("link", { name: "Новое расписание" })).toBeVisible();
    const main = page.getByTestId("team-schedule").nth(0);
    await vi.waitFor(() => expect(main.element().textContent).toContain("Дежурит: @petr"));
    await hasText(main, "2 слоя ротации");
    await hasText(main, "до пн 10:00");
  });
});
