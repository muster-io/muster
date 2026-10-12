// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The header of the Alert Group page in a real browser: the next notice from next_notice ("Next ack timeout notice
// at …", "Next Reminder at …", nothing without one) in the user's time zone, and the badge "Unclaimed" exactly while
// the Alert Group is Unclaimed, in English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { AlertGroup, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { formatDateTime } from "../lib/time";
import { AlertGroupHeader } from "./alert-group-header";

const ZONE = "Europe/Berlin";
const AT = "2026-10-12T14:45:00Z";

function group(extra: Partial<AlertGroup> = {}): AlertGroup {
  return {
    id: "AG0000000000AA",
    number: 412,
    title: "Disk full",
    status: "firing",
    severity_level: "warning",
    urgent: false,
    route: { id: "RT0000000000OP", name: "ops" },
    integrations: [],
    started_at: "2026-10-12T09:00:00Z",
    last_changed_at: "2026-10-12T09:00:00Z",
    reopen_count: 0,
    firing_alert_count: 1,
    resolved_alert_count: 0,
    unclaimed: false,
    delivery_problem: false,
    allowed_commands: [],
    ...extra,
  };
}

const SESSION: SessionRead = {
  session: {
    state: "active",
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the header reads only the user's time zone
    user: { id: "US0000000000ME", name: "Me", time_zone: ZONE } as Session["user"],
    csrf_token: "csrf",
    expires_at: "2026-10-13T09:00:00Z",
    idle_expires_at: "2026-10-13T09:00:00Z",
    method: "local",
    permissions: ["routes:read"],
  },
  ended: false,
};

async function renderHeader(g: AlertGroup) {
  const rootRoute = createRootRoute({ component: () => <AlertGroupHeader group={g} /> });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, SESSION);
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  await expect.element(page.getByTestId("alert-group-header")).toBeVisible();
}

const nextNotice = () => page.getByTestId("alert-group-next-notice");
const badge = () => page.getByTestId("unclaimed-badge");

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("AlertGroupHeader", () => {
  test("shows the next ack timeout notice in the user's time zone", async () => {
    await renderHeader(group({ next_notice: { kind: "ack_timeout", at: AT } }));
    await expect
      .element(nextNotice())
      .toHaveTextContent(`Next ack timeout notice at ${formatDateTime(AT, ZONE, "en")}`);
    expect(formatDateTime(AT, ZONE, "en")).toContain("16:45");
    // The time left shows on hover.
    expect(nextNotice().element().getAttribute("title")).toMatch(/^In /);
  });

  test("shows the next Reminder", async () => {
    await renderHeader(
      group({ status: "acknowledged", next_notice: { kind: "reminder", at: AT } }),
    );
    await expect
      .element(nextNotice())
      .toHaveTextContent(`Next Reminder at ${formatDateTime(AT, ZONE, "en")}`);
  });

  test("shows no next notice without one", async () => {
    await renderHeader(group());
    expect(nextNotice().query()).toBeNull();
  });

  test("shows the badge Unclaimed only while the Alert Group is Unclaimed", async () => {
    await renderHeader(group({ unclaimed: true }));
    await expect.element(badge()).toHaveTextContent("Unclaimed");
    expect(badge().all()).toHaveLength(1);
  });

  test("shows no badge for an Alert Group that is not Unclaimed", async () => {
    await renderHeader(group({ next_notice: { kind: "ack_timeout", at: AT } }));
    await expect.element(nextNotice()).toBeVisible();
    expect(badge().query()).toBeNull();
  });

  test("speaks Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderHeader(group({ unclaimed: true, next_notice: { kind: "reminder", at: AT } }));
    await expect.element(badge()).toHaveTextContent("Не взята в работу");
    await expect
      .element(nextNotice())
      .toHaveTextContent(`Следующее напоминание: ${formatDateTime(AT, ZONE, "ru")}`);
  });
});
