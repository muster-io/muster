// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The note about a missing Notification gateway in a real browser (C-30.FR-9, C-30.AC-6): "Unavailable: a Notification
// gateway is needed (a bridge for calls and SMS)" with "How to connect a Notification gateway", which opens the section
// of the documentation, and "Connect a bridge" for a user with connections:write only, which opens the Connection form
// with the type Bridge — in English and in Russian.

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

import type { Permission } from "../api/gen/model";
import i18n from "../i18n";
import { PETR, hasText, seedSession } from "../test/fixtures/schedules";
import { GatewayUnavailable } from "./gateway-unavailable";

async function renderNote(permissions: Permission[]) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedSession(queryClient, PETR, "responder", permissions);
  const root = createRootRoute({ component: () => <GatewayUnavailable /> });
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

describe("GatewayUnavailable", () => {
  test("says a gateway is needed and links the guide, without a button for a user who cannot connect", async () => {
    await renderNote(["connections:read"]);
    const note = page.getByTestId("gateway-unavailable");
    await hasText(
      note,
      "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)",
    );
    const link = page.getByRole("link", { name: /How to connect a Notification gateway/ });
    await expect
      .element(link)
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/messengers/notification-gateways/#connect-a-notification-gateway",
      );
    await expect.element(link).toHaveAttribute("target", "_blank");
    expect(page.getByTestId("gateway-connect").elements()).toHaveLength(0);
  });

  test("offers Connect a bridge to the Connection form with the type Bridge for connections:write", async () => {
    await renderNote(["connections:read", "connections:write"]);
    const connect = page.getByRole("link", { name: "Connect a bridge" });
    await expect.element(connect).toBeVisible();
    const href = connect.element().getAttribute("href");
    expect(href).toContain("/connections/new");
    expect(href).toContain("type=bridge");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderNote(["connections:write"]);
    await hasText(
      page.getByTestId("gateway-unavailable"),
      "Недоступно: нужен шлюз оповещений (мост для звонков и SMS)",
    );
    await expect.element(page.getByText("Как подключить шлюз оповещений")).toBeVisible();
    await expect.element(page.getByRole("link", { name: "Подключить мост" })).toBeVisible();
  });
});
