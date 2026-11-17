// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A User's contacts at Notification gateways in a real browser (C-30.FR-3, C-18.FR-6): the gateway, the contact and its
// state, "Remove" for users:write only, which asks first and then removes the contact through the User's endpoint,
// and no way to add one — in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { GatewayContact, User } from "../api/gen/model";
import i18n from "../i18n";
import { hasText, reply } from "../test/fixtures/schedules";
import { UserGatewayContacts } from "./user-gateway-contacts";

const USER: User = {
  id: "SR0000000PETR",
  name: "Petr Ivanov",
  login: "petr",
  role: "responder",
  source: "local",
  sign_in_method: "local",
  status: "active",
  totp_enabled: false,
  time_zone: "Europe/Berlin",
  created_at: "2026-10-13T09:00:00Z",
  etag: '"1"',
};

const CONFIRMED: GatewayContact = {
  id: "GC00000000AA01",
  connection: { id: "CN00000000GW01", name: "Example Gateway" },
  contact: "+10000000001",
  confirmed: true,
  confirmed_at: "2026-11-16T10:00:00Z",
  replaces: null,
  created_at: "2026-11-16T09:59:00Z",
};
const WAITING: GatewayContact = {
  ...CONFIRMED,
  id: "GC00000000AA02",
  contact: "+10000000002",
  confirmed: false,
  confirmed_at: null,
  replaces: CONFIRMED.id,
};

let items: GatewayContact[];
let calls: { method: string; url: string }[];

beforeEach(async () => {
  items = [CONFIRMED, WAITING];
  calls = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = init?.method ?? "GET";
    calls.push({ method, url });
    if (method === "DELETE") {
      items = items.filter((c) => c.id !== url.split("/").pop());
      return Promise.resolve(reply(204));
    }
    return Promise.resolve(reply(200, { items }));
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

async function renderContacts(canWrite: boolean) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <UserGatewayContacts user={USER} canWrite={canWrite} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const rows = () => page.getByTestId("user-gateway-contact");

describe("a User's contacts", () => {
  test("lists the gateway, the contact and its state, with no way to add one", async () => {
    await renderContacts(true);
    await expect
      .element(page.getByRole("heading", { name: "Contacts for calls and SMS" }))
      .toBeVisible();
    await vi.waitFor(() => expect(rows().elements()).toHaveLength(2));
    await expect
      .element(rows().nth(0))
      .toHaveTextContent("Example Gateway+10000000001confirmedRemove");
    await expect
      .element(rows().nth(1))
      .toHaveTextContent("Example Gateway+10000000002not confirmedRemove");
    expect(page.getByRole("button", { name: /Send code|Add/ }).elements()).toHaveLength(0);
    expect(page.getByRole("textbox").elements()).toHaveLength(0);
  });

  test("removes a contact after a confirmation", async () => {
    await renderContacts(true);
    await page.getByRole("button", { name: "Remove the contact +10000000002" }).click();
    await hasText(
      page.getByRole("dialog"),
      "Remove the contact +10000000002 of Petr Ivanov at Example Gateway? Calls and SMS no longer reach them through it.",
    );
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    // The dialog's backdrop is still inert-animating under the pointer; the DOM click is the button's own.
    await vi.waitFor(() => {
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Remove", exact: true })
        .element()
        .dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await vi.waitFor(() => expect(rows().elements()).toHaveLength(1));
    expect(calls.find((c) => c.method === "DELETE")?.url).toBe(
      `/api/v1/users/${USER.id}/gateway-contacts/${WAITING.id}`,
    );
  });

  test("has no Remove without users:write", async () => {
    await renderContacts(false);
    await vi.waitFor(() => expect(rows().elements()).toHaveLength(2));
    expect(page.getByRole("button", { name: /Remove/ }).elements()).toHaveLength(0);
  });

  test("says there are none, and reads in Russian", async () => {
    items = [];
    await i18n.changeLanguage("ru");
    await renderContacts(true);
    await expect
      .element(page.getByRole("heading", { name: "Контакты для звонков/SMS" }))
      .toBeVisible();
    await expect.element(page.getByText("Контактов в шлюзах оповещений нет.")).toBeVisible();
  });

  test("reads the states in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderContacts(true);
    await vi.waitFor(() => expect(rows().elements()).toHaveLength(2));
    await hasText(rows().nth(0), "подтверждён");
    await hasText(rows().nth(1), "не подтверждён");
    await expect
      .element(page.getByRole("button", { name: "Удалить контакт +10000000001" }))
      .toBeVisible();
  });
});
