// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Bridge Connection as a Notification gateway in a real browser (C-30.FR-1, FR-8, C-30.AC-1): its roles, "Healthy",
// the banner "Broken since HH:MM: {reason}. Muster tries again every 5 min." and the warning about the Destinations
// left on a Connection that declares only the role gateway, with plurals — in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { BridgeConnection } from "../api/gen/model";
import i18n from "../i18n";
import { bridgeConnection } from "../test/fixtures/connections";
import { hasText } from "../test/fixtures/schedules";
import { GatewayHealth, isGateway, isGatewayOnly, rolesText } from "./gateway-health";

function gateway(overrides: Partial<Omit<BridgeConnection, "type">> = {}): BridgeConnection {
  return bridgeConnection({
    id: "CN0000000000GW",
    name: "Example Gateway",
    roles: ["gateway"],
    gateway: null,
    ...overrides,
  });
}

async function renderHealth(connection: BridgeConnection) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <GatewayHealth connection={connection} />
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

describe("the roles of a bridge", () => {
  test("a bridge is a gateway with the role gateway, or while Broken as one", () => {
    expect(isGateway(gateway())).toBe(true);
    expect(isGateway(gateway({ roles: ["messenger"] }))).toBe(false);
    expect(
      isGateway(
        gateway({
          roles: ["messenger"],
          gateway: {
            broken: true,
            broken_since: "2026-11-17T10:00:00Z",
            reason: "x",
            next_probe_at: null,
          },
        }),
      ),
    ).toBe(true);
    expect(isGatewayOnly(gateway())).toBe(true);
    expect(isGatewayOnly(gateway({ roles: ["messenger", "gateway"] }))).toBe(false);
  });

  test("names the roles, a bridge without any as a messenger", () => {
    expect(rolesText(i18n.t, ["messenger", "gateway"])).toBe("Messenger, Notification gateway");
    expect(rolesText(i18n.t, [])).toBe("Messenger");
  });
});

describe("GatewayHealth", () => {
  test("shows nothing for a messenger", async () => {
    await renderHealth(gateway({ roles: ["messenger"] }));
    expect(page.getByTestId("gateway-health").elements()).toHaveLength(0);
  });

  test("shows the role and Healthy for a gateway in order, with no warning without Destinations", async () => {
    await renderHealth(gateway());
    const card = page.getByTestId("gateway-health");
    await expect.element(card.getByRole("heading", { name: "Notification gateway" })).toBeVisible();
    await expect
      .element(page.getByTestId("gateway-roles"))
      .toHaveTextContent("Roles: Notification gateway");
    await expect.element(page.getByTestId("destination-health")).toHaveTextContent("Healthy");
    expect(page.getByTestId("gateway-broken-banner").elements()).toHaveLength(0);
    expect(page.getByTestId("gateway-only-warning").elements()).toHaveLength(0);
    // A gateway-only bridge links its own guide beside the title, not here.
    expect(page.getByTestId("docs-link").elements()).toHaveLength(0);
  });

  test("shows the Broken banner with the time, the reason and the interval", async () => {
    await renderHealth(
      gateway({
        gateway: {
          broken: true,
          broken_since: "2026-11-17T10:15:00Z",
          reason: "the gateway answered 401.",
          next_probe_at: "2026-11-17T10:20:00Z",
        },
      }),
    );
    await expect.element(page.getByTestId("destination-health")).toHaveTextContent("Broken");
    const banner = page.getByTestId("gateway-broken-banner");
    await vi.waitFor(() =>
      expect(banner.element().textContent).toMatch(
        /^Broken since \d{2}:\d{2}: the gateway answered 401\. Muster tries again every 5\smin\.$/u,
      ),
    );
    await i18n.changeLanguage("ru");
    await vi.waitFor(() =>
      expect(banner.element().textContent).toMatch(
        /^Сломан с \d{2}:\d{2}: the gateway answered 401\. Muster пробует снова каждые 5\sмин\.$/u,
      ),
    );
  });

  test("shows the Broken banner without a time when the gateway has none", async () => {
    await renderHealth(
      gateway({
        gateway: { broken: true, broken_since: null, reason: null, next_probe_at: null },
      }),
    );
    await vi.waitFor(() =>
      expect(page.getByTestId("gateway-broken-banner").element().textContent).toMatch(
        /^Broken: no details\. Muster tries again every 5\smin\.$/u,
      ),
    );
  });

  test("a bridge with both roles links the gateway guide from its roles", async () => {
    await renderHealth(gateway({ roles: ["messenger", "gateway"] }));
    await hasText(page.getByTestId("gateway-roles"), "Roles: Messenger, Notification gateway");
    await expect
      .element(page.getByTestId("docs-link"))
      .toHaveAttribute("data-docs", "page.notification_gateway");
    expect(page.getByTestId("gateway-only-warning").elements()).toHaveLength(0);
  });

  test("warns about the Destinations left on a gateway-only Connection, with plurals", async () => {
    await renderHealth(gateway({ destination_count: 1 }));
    await expect
      .element(page.getByTestId("gateway-only-warning"))
      .toHaveTextContent(
        "This bridge now declares only the role gateway: its 1 Destination no longer receives messages. Remove it or change the bridge.",
      );
  });

  test("warns about several Destinations in English and Russian", async () => {
    await renderHealth(gateway({ destination_count: 2 }));
    const warning = page.getByTestId("gateway-only-warning");
    await expect
      .element(warning)
      .toHaveTextContent(
        "This bridge now declares only the role gateway: its 2 Destinations no longer receive messages. Remove them or change the bridge.",
      );
    await i18n.changeLanguage("ru");
    await expect
      .element(warning)
      .toHaveTextContent(
        "Мост теперь объявляет только роль gateway: его места доставки (2) больше не получают сообщений. Удалите их или измените мост.",
      );
    await expect
      .element(page.getByTestId("gateway-roles"))
      .toHaveTextContent("Роли: Шлюз оповещений");
  });
});
