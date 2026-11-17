// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Contact for calls and SMS" of the profile in a real browser, against a stubbed API (C-30.FR-3, FR-9, C-30.AC-6):
// without a Notification gateway the hint and "Unavailable: a Notification gateway is needed (a bridge for calls and
// SMS)" with its link and, for Admins only, "Connect a bridge", and no phone field; with one the phone field and "Send
// code", then "Code sent through the Notification gateway to {contact}.", the code field, "Confirm" and "Another
// number"; a wrong code shows the attempts left, too many code requests the time to wait; the confirmed contact reads
// "{contact} · calls and SMS · confirmed" with "Remove" and the note about acknowledging from a call; a contact that
// replaces a confirmed one keeps the old one listed; one block for each gateway — in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { GatewayContact, Permission } from "../api/gen/model";
import i18n from "../i18n";
import { bridgeConnection } from "../test/fixtures/connections";
import { PETR, hasText, problem, reply, seedSession } from "../test/fixtures/schedules";
import { GatewayContactSection } from "./gateway-contact-section";

const CODE = "482913";
const GATEWAY = bridgeConnection({
  id: "CN00000000GW01",
  name: "Example Gateway",
  roles: ["gateway"],
});
const SECOND = bridgeConnection({
  id: "CN00000000GW02",
  name: "Backup Gateway",
  roles: ["gateway"],
});

function contact(overrides: Partial<GatewayContact> = {}): GatewayContact {
  return {
    id: "GC00000000AA01",
    connection: { id: GATEWAY.id, name: GATEWAY.name },
    contact: "+10000000002",
    confirmed: false,
    confirmed_at: null,
    replaces: null,
    created_at: "2026-11-17T10:00:00Z",
    ...overrides,
  };
}

interface Api {
  gateways: (typeof GATEWAY)[];
  contacts: GatewayContact[];
  /** The answer of the next code request instead of the contact. */
  createError?: Response;
  /** The list of the caller's contacts answers 500. */
  listFails?: boolean;
  attempts: number;
  calls: { method: string; url: string; body: unknown }[];
}

let api: Api;

function install() {
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = init?.method ?? "GET";
    const body: Record<string, string> =
      typeof init?.body === "string" ? JSON.parse(init.body) : {};
    api.calls.push({ method, url, body });
    if (url.startsWith("/api/v1/connections")) {
      return Promise.resolve(reply(200, { items: api.gateways, next_cursor: null }));
    }
    if (url === "/api/v1/me/gateway-contacts" && method === "GET" && api.listFails === true) {
      return Promise.resolve(reply(500, problem(500)));
    }
    if (url === "/api/v1/me/gateway-contacts" && method === "GET") {
      return Promise.resolve(reply(200, { items: api.contacts }));
    }
    if (url === "/api/v1/me/gateway-contacts" && method === "POST") {
      if (api.createError !== undefined) {
        return Promise.resolve(api.createError);
      }
      const asked = body;
      const gateway = api.gateways.find((g) => g.id === asked.connection_id) ?? GATEWAY;
      const confirmed = api.contacts.find(
        (c) => c.confirmed && c.connection.id === asked.connection_id,
      );
      api.contacts = api.contacts.filter((c) => c.confirmed || c.connection.id !== gateway.id);
      const created = contact({
        id: "GC00000000AA02",
        connection: { id: gateway.id, name: gateway.name },
        contact: asked.contact,
        replaces: confirmed?.id ?? null,
      });
      api.contacts.push(created);
      api.attempts = 5;
      return Promise.resolve(reply(202, created));
    }
    if (url.endsWith("/confirmation")) {
      if (body.code !== CODE) {
        api.attempts -= 1;
        return Promise.resolve(
          reply(
            422,
            problem(422, {
              code: "validation_failed",
              errors: [{ pointer: "/code", code: "contact_code_invalid" }],
              attempts_remaining: api.attempts,
            }),
          ),
        );
      }
      const waiting = api.contacts.find((c) => !c.confirmed);
      const done = {
        ...waiting,
        confirmed: true,
        confirmed_at: "2026-11-17T10:01:00Z",
        replaces: null,
      };
      api.contacts = api.contacts
        .filter((c) => c.id !== waiting?.id && c.id !== waiting?.replaces)
        .concat(contact(done));
      return Promise.resolve(reply(200, done));
    }
    if (method === "DELETE") {
      const id = url.split("/").pop();
      api.contacts = api.contacts.filter((c) => c.id !== id);
      return Promise.resolve(reply(204));
    }
    return Promise.resolve(reply(404, problem(404)));
  });
}

async function renderSection(permissions: Permission[] = ["connections:read"]) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  seedSession(queryClient, PETR, "responder", permissions);
  const root = createRootRoute({ component: () => <GatewayContactSection /> });
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

const section = () => page.getByTestId("gateway-contact-section");
beforeEach(async () => {
  api = { gateways: [GATEWAY], contacts: [], attempts: 5, calls: [] };
  await i18n.changeLanguage("en");
  install();
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("without a Notification gateway", () => {
  test("shows the hint and the note, no phone field, and no button for a Responder", async () => {
    api.gateways = [];
    await renderSection();
    await expect
      .element(section().getByRole("heading", { name: "Contact for calls and SMS" }))
      .toBeVisible();
    await hasText(
      section(),
      "Muster itself does not call, send SMS or check phone numbers: a Notification gateway (a bridge for calls and SMS) does, and it does not depend on a messenger. An Admin sees the contact and can remove it, but not change it.",
    );
    await hasText(
      page.getByTestId("gateway-unavailable"),
      "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)",
    );
    await expect.element(page.getByText("How to connect a Notification gateway")).toBeVisible();
    expect(page.getByLabelText("Phone").elements()).toHaveLength(0);
    expect(page.getByTestId("gateway-connect").elements()).toHaveLength(0);
  });

  test("offers Connect a bridge to an Admin, in Russian too", async () => {
    api.gateways = [];
    await i18n.changeLanguage("ru");
    await renderSection(["connections:read", "connections:write"]);
    await hasText(section(), "Недоступно: нужен шлюз оповещений (мост для звонков и SMS)");
    await expect.element(page.getByRole("link", { name: "Подключить мост" })).toBeVisible();
    await expect.element(page.getByText("Как подключить шлюз оповещений")).toBeVisible();
    expect(page.getByLabelText("Телефон").elements()).toHaveLength(0);
  });
});

describe("with a Notification gateway", () => {
  test("sends a code, refuses a wrong one with the attempts left and confirms the right one", async () => {
    await renderSection();
    await expect.element(page.getByRole("heading", { name: "Example Gateway" })).toBeVisible();
    await userEvent.fill(page.getByLabelText("Phone"), " +10000000002 ");
    await page.getByRole("button", { name: "Send code" }).click();
    await expect
      .element(page.getByTestId("gateway-code-sent"))
      .toHaveTextContent("Code sent through the Notification gateway to +10000000002.");
    expect(
      api.calls.find((c) => c.method === "POST" && c.url === "/api/v1/me/gateway-contacts")?.body,
    ).toEqual({ connection_id: GATEWAY.id, contact: "+10000000002" });
    await expect.element(page.getByRole("button", { name: "Another number" })).toBeVisible();

    await userEvent.fill(page.getByLabelText("Code"), "000000");
    await page.getByRole("button", { name: "Confirm" }).click();
    await hasText(section(), "The code is wrong or no longer valid. 4 attempts left.");
    await userEvent.fill(page.getByLabelText("Code"), "111111");
    await page.getByRole("button", { name: "Confirm" }).click();
    await hasText(section(), "The code is wrong or no longer valid. 3 attempts left.");

    await userEvent.fill(page.getByLabelText("Code"), CODE);
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect
      .element(page.getByTestId("gateway-contact-summary"))
      .toHaveTextContent("+10000000002 · calls and SMS · confirmed");
    await expect.element(page.getByRole("button", { name: /^Remove/ })).toBeVisible();
    await expect
      .element(page.getByTestId("gateway-contact-note"))
      .toHaveTextContent(
        "From a call you can acknowledge an Alert Group only while nobody has it; otherwise the call answers “Already taken by petr”.",
      );
    expect(page.getByLabelText("Phone").elements()).toHaveLength(0);
  });

  test("says no attempts are left once the server gives none", async () => {
    api.contacts = [contact()];
    api.attempts = 1;
    await renderSection();
    await userEvent.fill(page.getByLabelText("Code"), "000000");
    await page.getByRole("button", { name: "Confirm" }).click();
    await hasText(section(), "No attempts left. Send a new code.");
    await expect.element(page.getByRole("button", { name: "Confirm" })).toBeDisabled();
  });

  test("shows the time to wait on 429", async () => {
    api.createError = new Response(
      JSON.stringify(
        problem(429, { code: "contact_code_requests_exceeded", retry_after_seconds: 130 }),
      ),
      {
        status: 429,
        headers: { "Content-Type": "application/problem+json", "Retry-After": "130" },
      },
    );
    await renderSection();
    await userEvent.fill(page.getByLabelText("Phone"), "+10000000002");
    await page.getByRole("button", { name: "Send code" }).click();
    await hasText(section(), "Too many code requests. Try again in 3 minutes.");
    expect(page.getByLabelText("Code").elements()).toHaveLength(0);
  });

  test("a 429 with another code is the generic text, not the code-request one", async () => {
    api.createError = new Response(JSON.stringify(problem(429, { code: "rate_limited" })), {
      status: 429,
      headers: { "Content-Type": "application/problem+json", "Retry-After": "5" },
    });
    await renderSection();
    await userEvent.fill(page.getByLabelText("Phone"), "+10000000002");
    await page.getByRole("button", { name: "Send code" }).click();
    await expect.element(page.getByRole("alert")).toBeVisible();
    expect(section().element().textContent).not.toContain("Too many code requests");
  });

  test("asks for a phone before sending", async () => {
    await renderSection();
    await page.getByRole("button", { name: "Send code" }).click();
    await hasText(section(), "Fill in this field.");
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
  });

  test("another number goes to the phone field, and Cancel back to the code step", async () => {
    api.contacts = [contact()];
    await renderSection();
    await page.getByRole("button", { name: "Another number" }).click();
    await expect.element(page.getByLabelText("Phone")).toBeVisible();
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect.element(page.getByLabelText("Code")).toBeVisible();
    expect(page.getByLabelText("Phone").elements()).toHaveLength(0);
  });

  test("shows the error of a failed list, not the note that a gateway is needed", async () => {
    api.listFails = true;
    await renderSection();
    await expect.element(page.getByTestId("gateway-contact-error")).toBeVisible();
    expect(page.getByTestId("gateway-unavailable").elements()).toHaveLength(0);
    expect(page.getByLabelText("Phone").elements()).toHaveLength(0);
  });

  test("keeps the confirmed contact listed while a new one waits for its code", async () => {
    api.contacts = [
      contact({
        id: "GC00000000AA01",
        contact: "+10000000001",
        confirmed: true,
        confirmed_at: "2026-11-16T10:00:00Z",
      }),
      contact({ id: "GC00000000AA02", contact: "+10000000002", replaces: "GC00000000AA01" }),
    ];
    await renderSection();
    await expect
      .element(page.getByTestId("gateway-contact-summary"))
      .toHaveTextContent("+10000000001 · calls and SMS · confirmed");
    await expect
      .element(page.getByTestId("gateway-replaced"))
      .toHaveTextContent("Still the one called until +10000000002 is confirmed.");
    await expect.element(page.getByLabelText("Code")).toBeVisible();
  });

  test("removes a confirmed contact after a confirmation", async () => {
    api.contacts = [contact({ confirmed: true, confirmed_at: "2026-11-16T10:00:00Z" })];
    await renderSection();
    await page.getByRole("button", { name: "Remove the contact +10000000002" }).click();
    await hasText(
      page.getByRole("dialog"),
      "Remove the contact +10000000002 at Example Gateway? Calls and SMS no longer reach this person through it.",
    );
    // The dialog's backdrop is still inert-animating under the pointer; the DOM click is the button's own.
    await vi.waitFor(() => {
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Remove", exact: true })
        .element()
        .dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await expect.element(page.getByLabelText("Phone")).toBeVisible();
    expect(api.calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  test("has one block for each gateway, in the order of the list", async () => {
    api.gateways = [GATEWAY, SECOND];
    api.contacts = [contact({ confirmed: true, confirmed_at: "2026-11-16T10:00:00Z" })];
    await renderSection();
    const blocks = page.getByTestId("gateway-block");
    await vi.waitFor(() => expect(blocks.elements()).toHaveLength(2));
    await hasText(blocks.nth(0), "Example Gateway");
    await hasText(blocks.nth(0), "+10000000002 · calls and SMS · confirmed");
    await hasText(blocks.nth(1), "Backup Gateway");
    expect(blocks.nth(1).getByLabelText("Phone").elements()).toHaveLength(1);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderSection();
    await userEvent.fill(page.getByLabelText("Телефон"), "+10000000002");
    await page.getByRole("button", { name: "Отправить код" }).click();
    await expect
      .element(page.getByTestId("gateway-code-sent"))
      .toHaveTextContent("Код отправлен через шлюз оповещений на +10000000002.");
    await userEvent.fill(page.getByLabelText("Код"), "000000");
    await page.getByRole("button", { name: "Подтвердить" }).click();
    await hasText(section(), "Код неверный или больше не действует. Осталось 4 попытки.");
    await userEvent.fill(page.getByLabelText("Код"), CODE);
    await page.getByRole("button", { name: "Подтвердить" }).click();
    await expect
      .element(page.getByTestId("gateway-contact-summary"))
      .toHaveTextContent("+10000000002 · звонок и SMS · подтверждён");
    await expect
      .element(page.getByTestId("gateway-contact-note"))
      .toHaveTextContent(
        "Из звонка можно подтвердить группу, только если у неё ещё нет владельца; иначе звонок ответит «уже взял(а) petr».",
      );
    await expect.element(page.getByRole("button", { name: /^Удалить/ })).toBeVisible();
  });
});
