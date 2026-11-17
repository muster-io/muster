// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A User's Notification rules on their page in a real browser (C-31.FR-4): both rules in words with the switch and
// "Reset to the Organization's rules", which users:write changes, and read-only for a viewer without it; a call only
// for a gateway at which the User has a confirmed contact, the note naming the User otherwise.

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

import type { Permission, User } from "../api/gen/model";
import i18n from "../i18n";
import {
  GATEWAY,
  type RulesApi,
  contactAt,
  installRulesApi,
  mention,
  newRulesApi,
} from "../test/fixtures/notification-rules";
import { OLGA, PETR, hasText, seedSession } from "../test/fixtures/schedules";
import { UserNotificationRules } from "./user-notification-rules";

const USER: User = {
  id: PETR.id,
  name: "Petr Example",
  login: "petr",
  role: "responder",
  source: "local",
  sign_in_method: "local",
  status: "active",
  totp_enabled: false,
  created_at: "2026-10-13T09:00:00Z",
  etag: '"1"',
};

let api: RulesApi;

async function renderRules(canWrite: boolean) {
  const permissions: Permission[] = ["connections:read", "users:read"];
  if (canWrite) {
    permissions.push("users:write");
  }
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  seedSession(queryClient, OLGA, "admin", permissions);
  const root = createRootRoute({
    component: () => <UserNotificationRules user={USER} canWrite={canWrite} />,
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
}

const card = () => page.getByTestId("user-notification-rules");
const important = () => page.getByTestId("rule-important");
const radio = (name: string) => page.getByRole("radio", { name, exact: true });

beforeEach(async () => {
  api = newRulesApi(`/api/v1/users/${USER.id}/notification-rules`);
  api.contacts = [contactAt(GATEWAY, "+10000000002")];
  await i18n.changeLanguage("en");
  installRulesApi(api);
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("an Admin", () => {
  test("sees the rules in words and changes them with the switch, sending the version read", async () => {
    await renderRules(true);
    await expect.element(card().getByRole("heading", { name: "Notification rules" })).toBeVisible();
    await hasText(
      page.getByTestId("rule-normal"),
      "Normal: a Mention in the Thread of the Root message.",
    );
    await userEvent.click(radio("Call"));
    await hasText(
      important(),
      "Important: a Mention in the Thread and a call to +10000000002 at the same time, again after 5 minutes.",
    );
    const put = api.calls.find((c) => c.method === "PUT");
    expect(put?.url).toBe(`/api/v1/users/${USER.id}/notification-rules`);
    expect(put?.body).toEqual({ state: "call", gateway_id: GATEWAY.id });
    expect(put?.ifMatch).toBe('"1"');

    await userEvent.click(radio("As in the Organization"));
    await hasText(
      important(),
      "Important: a Mention in the Thread and again after 5 minutes if you did not answer.",
    );
    const last = api.calls.filter((c) => c.method === "PUT").at(-1);
    expect(last?.body).toEqual({ state: "organization" });
    expect(last?.ifMatch).toBe('"2"');
  });

  test("resets custom rules to the Organization's", async () => {
    api.stored = {
      ...api.stored,
      state: "custom",
      normal: [mention, mention, mention],
      important: [mention],
    };
    await renderRules(true);
    await hasText(page.getByTestId("rule-normal"), "3 Mentions in the Thread at the same time.");
    expect(page.getByRole("radio").elements()).toHaveLength(0);
    await userEvent.click(page.getByRole("button", { name: "Reset to the Organization's rules" }));
    await expect.element(radio("As in the Organization")).toBeChecked();
  });

  test("a User without a confirmed contact cannot be called, and the note names them", async () => {
    api.contacts = [];
    await renderRules(true);
    await hasText(
      page.getByTestId("rules-no-contact"),
      "Petr Example has no confirmed contact for calls and SMS.",
    );
    await expect.element(radio("Call")).toBeDisabled();
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderRules(true);
    await expect
      .element(card().getByRole("heading", { name: "Правила уведомлений" }))
      .toBeVisible();
    await userEvent.click(radio("Звонить"));
    await hasText(
      important(),
      "Важное: упоминание в треде и звонок на +10000000002 одновременно, ещё раз через 5 минут.",
    );
  });
});

describe("anyone without users:write", () => {
  test("reads the rules and the switch, and cannot change them", async () => {
    api.stored = { ...api.stored, state: "no_call", normal: [mention], important: [mention] };
    await renderRules(false);
    await hasText(page.getByTestId("rule-normal"), "Normal: a Mention in the Thread");
    await expect.element(radio("Don't call")).toBeChecked();
    await expect.element(radio("Call")).toBeDisabled();
    await expect.element(radio("As in the Organization")).toBeDisabled();
  });

  test("sees no Reset on custom rules", async () => {
    api.stored = { ...api.stored, state: "custom", normal: [mention], important: [mention] };
    await renderRules(false);
    await hasText(page.getByTestId("rules-custom"), "These rules are custom");
    expect(
      page.getByRole("button", { name: "Reset to the Organization's rules" }).elements(),
    ).toHaveLength(0);
  });
});
