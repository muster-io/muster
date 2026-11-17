// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Personal notification rules" of the profile in a real browser, against a stubbed API (C-31.FR-5, C-31.AC-5, AC-6,
// C-30.FR-9): both rules in words; the switch unavailable without a Notification gateway and with a gateway but no
// confirmed contact, available once a contact is confirmed; "Call" shows the important rule as a Mention with a call
// at the same time, a wait of 5 minutes and a Mention, and "As in the Organization" brings the Organization's rules
// back; with two gateways the profile asks which one the call uses; custom rules show in words without the switch and
// with "Reset to the Organization's rules" — in English and in Russian.

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

import type { Permission } from "../api/gen/model";
import i18n from "../i18n";
import {
  GATEWAY,
  type RulesApi,
  SECOND_GATEWAY,
  contactAt,
  installRulesApi,
  mention,
  newRulesApi,
} from "../test/fixtures/notification-rules";
import { PETR, hasText, seedSession } from "../test/fixtures/schedules";
import { NotificationRulesSection } from "./notification-rules-section";

let api: RulesApi;

async function renderSection(permissions: Permission[] = ["connections:read"]) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  seedSession(queryClient, PETR, "responder", permissions);
  const root = createRootRoute({ component: () => <NotificationRulesSection /> });
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

const section = () => page.getByTestId("notification-rules-section");
const normal = () => page.getByTestId("rule-normal");
const important = () => page.getByTestId("rule-important");
const radio = (name: string) => page.getByRole("radio", { name, exact: true });

const NORMAL = "Normal: a Mention in the Thread of the Root message.";
const IMPORTANT =
  "Important: a Mention in the Thread and again after 5 minutes if you did not answer.";
const WITH_CALL =
  "Important: a Mention in the Thread and a call to +10000000002 at the same time, again after 5 minutes.";

beforeEach(async () => {
  api = newRulesApi();
  await i18n.changeLanguage("en");
  installRulesApi(api);
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the rules in words", () => {
  test("shows the title, the hint, both rules and the switch with its three states", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002")];
    await renderSection();
    await expect
      .element(section().getByRole("heading", { name: "Personal notification rules" }))
      .toBeVisible();
    await hasText(
      section(),
      "What happens when an Escalation step calls you 'normally' or 'importantly'. Muster sends no direct messages.",
    );
    await hasText(normal(), NORMAL);
    await hasText(important(), IMPORTANT);
    await expect
      .element(section().getByRole("link", { name: /^Learn more: Notification rules/ }))
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/concepts/notification-rules/",
      );
    await hasText(page.getByTestId("rules-switch"), "Also call through the Notification gateway");
    await expect.element(radio("As in the Organization")).toBeChecked();
    await expect.element(radio("Call")).toBeEnabled();
    await expect.element(radio("Don't call")).toBeEnabled();
  });

  test("reads in Russian", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002")];
    await i18n.changeLanguage("ru");
    await renderSection();
    await expect
      .element(section().getByRole("heading", { name: "Личные правила уведомлений" }))
      .toBeVisible();
    await hasText(
      section(),
      "Что происходит, когда шаг цепочки зовёт вас «обычно» или «важно». Личных сообщений Muster не шлёт.",
    );
    await hasText(normal(), "Обычное: упоминание в треде корневого сообщения.");
    await hasText(
      important(),
      "Важное: упоминание в треде и ещё раз через 5 минут, если вы не ответили.",
    );
    await hasText(page.getByTestId("rules-switch"), "Ещё звонок через шлюз оповещений");
    await expect.element(radio("Как в организации")).toBeChecked();
    await expect.element(radio("Звонить")).toBeEnabled();
    await expect.element(radio("Не звонить")).toBeEnabled();
  });
});

describe("availability of the switch", () => {
  test("without a Notification gateway it is unavailable, with the note", async () => {
    api.gateways = [];
    await renderSection();
    await hasText(
      page.getByTestId("gateway-unavailable"),
      "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)",
    );
    await expect.element(radio("As in the Organization")).toBeDisabled();
    await expect.element(radio("Call")).toBeDisabled();
    await expect.element(radio("Don't call")).toBeDisabled();
    await hasText(important(), IMPORTANT);
  });

  test("without a Notification gateway, in Russian", async () => {
    api.gateways = [];
    await i18n.changeLanguage("ru");
    await renderSection();
    await hasText(
      page.getByTestId("gateway-unavailable"),
      "Недоступно: нужен шлюз оповещений (мост для звонков и SMS)",
    );
    await expect.element(radio("Звонить")).toBeDisabled();
  });

  test("with a gateway and no confirmed contact it asks for the contact first", async () => {
    await renderSection();
    await hasText(
      page.getByTestId("rules-no-contact"),
      "Confirm your contact for calls and SMS below first.",
    );
    await expect.element(radio("Call")).toBeDisabled();
    await expect.element(radio("As in the Organization")).toBeDisabled();
  });

  test("an unconfirmed contact does not make it available, in Russian too", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002", { confirmed: false, confirmed_at: null })];
    await i18n.changeLanguage("ru");
    await renderSection();
    await hasText(
      page.getByTestId("rules-no-contact"),
      "Сначала подтвердите контакт для звонков/SMS ниже.",
    );
    await expect.element(radio("Звонить")).toBeDisabled();
  });

  test("once the contact is confirmed it is available", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002")];
    await renderSection();
    await expect.element(radio("Call")).toBeEnabled();
    expect(page.getByTestId("rules-no-contact").elements()).toHaveLength(0);
  });
});

describe("the switch", () => {
  test("Call makes the important rule a Mention with a call, a wait and a Mention; As in the Organization gives the Organization's back", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002")];
    await renderSection();
    await userEvent.click(radio("Call"));
    await hasText(important(), WITH_CALL);
    await hasText(normal(), NORMAL);
    await expect.element(radio("Call")).toBeChecked();
    await hasText(page.getByTestId("rules-call-to"), "to +10000000002");
    const put = api.calls.find((c) => c.method === "PUT");
    expect(put?.body).toEqual({ state: "call", gateway_id: GATEWAY.id });
    expect(put?.ifMatch).toBe('"1"');

    await userEvent.click(radio("As in the Organization"));
    await hasText(important(), IMPORTANT);
    await expect.element(radio("As in the Organization")).toBeChecked();
    expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/reset"))).toBe(true);
  });

  test("Call reads in Russian", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002")];
    await i18n.changeLanguage("ru");
    await renderSection();
    await userEvent.click(radio("Звонить"));
    await hasText(
      important(),
      "Важное: упоминание в треде и звонок на +10000000002 одновременно, ещё раз через 5 минут.",
    );
  });

  test("Don't call keeps the Organization's rules as they were", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002")];
    await renderSection();
    await userEvent.click(radio("Don't call"));
    await expect.element(radio("Don't call")).toBeChecked();
    await hasText(important(), IMPORTANT);
    expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({ state: "no_call" });
  });

  test("with two gateways at which there are confirmed contacts it asks which one the call uses", async () => {
    api.gateways = [GATEWAY, SECOND_GATEWAY];
    api.contacts = [contactAt(GATEWAY, "+10000000002"), contactAt(SECOND_GATEWAY, "+10000000009")];
    await renderSection();
    const which = page.getByLabelText("Call through");
    await expect.element(which).toBeVisible();
    await userEvent.selectOptions(which, SECOND_GATEWAY.id);
    await userEvent.click(radio("Call"));
    await hasText(
      important(),
      "Important: a Mention in the Thread and a call to +10000000009 at the same time, again after 5 minutes.",
    );
    expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
      state: "call",
      gateway_id: SECOND_GATEWAY.id,
    });
    await userEvent.selectOptions(which, GATEWAY.id);
    await hasText(important(), WITH_CALL);
  });

  test("a refused change of the gateway leaves the select at the gateway the rules name", async () => {
    api.gateways = [GATEWAY, SECOND_GATEWAY];
    api.contacts = [contactAt(GATEWAY, "+10000000002"), contactAt(SECOND_GATEWAY, "+10000000009")];
    await renderSection();
    const which = page.getByLabelText("Call through");
    await userEvent.selectOptions(which, SECOND_GATEWAY.id);
    await userEvent.click(radio("Call"));
    await hasText(page.getByTestId("rules-call-to"), "to +10000000009");
    api.updateStatus = 422;
    await userEvent.selectOptions(which, GATEWAY.id);
    await expect.element(section().getByRole("alert")).toBeVisible();
    await expect.element(which).toHaveValue(SECOND_GATEWAY.id);
    await hasText(page.getByTestId("rules-call-to"), "to +10000000009");
  });

  test("with one gateway it asks nothing", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002")];
    await renderSection();
    await expect.element(radio("Call")).toBeEnabled();
    expect(page.getByLabelText("Call through").elements()).toHaveLength(0);
  });

  test("a stale version shows a refusal and the rules as they are", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002")];
    await renderSection();
    await expect.element(radio("Call")).toBeEnabled();
    const reads = () => api.calls.filter((c) => c.method === "GET" && c.url === api.path).length;
    const before = reads();
    api.updateStatus = 412;
    await userEvent.click(radio("Call"));
    await hasText(section(), "The rules changed in the meantime.");
    await vi.waitFor(() => expect(reads()).toBeGreaterThan(before));
    await expect.element(radio("As in the Organization")).toBeChecked();
  });
});

describe("custom rules", () => {
  test("show in words without the switch, and Reset brings the switch back", async () => {
    api.contacts = [contactAt(GATEWAY, "+10000000002")];
    api.stored = {
      ...api.stored,
      state: "custom",
      normal: [mention, mention, mention],
      important: [mention, mention, mention],
    };
    await renderSection();
    await hasText(normal(), "Normal: 3 Mentions in the Thread at the same time.");
    await hasText(page.getByTestId("rules-custom"), "These rules are custom");
    expect(page.getByTestId("rules-switch").elements()).toHaveLength(0);
    expect(page.getByRole("radio").elements()).toHaveLength(0);
    await userEvent.click(page.getByRole("button", { name: "Reset to the Organization's rules" }));
    await expect.element(radio("As in the Organization")).toBeChecked();
    await hasText(normal(), NORMAL);
    await hasText(important(), IMPORTANT);
    expect(page.getByTestId("rules-custom").elements()).toHaveLength(0);
  });

  test("read in Russian", async () => {
    api.stored = {
      ...api.stored,
      state: "custom",
      normal: [mention, mention, mention],
      important: [mention],
    };
    await i18n.changeLanguage("ru");
    await renderSection();
    await hasText(normal(), "Обычное: 3 упоминания в треде одновременно.");
    await expect
      .element(page.getByRole("button", { name: "Вернуть правила организации" }))
      .toBeVisible();
  });
});
