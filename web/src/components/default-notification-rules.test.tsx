// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Organization's default Notification rules in a real browser (C-20.FR-1, C-31.FR-2): the normal and the important
// rule in words, the note that they change through the API, and the link to the documentation — in English and in
// Russian, and with a changed wait.

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

import type { NotificationRuleAction, Organization } from "../api/gen/model";
import i18n from "../i18n";
import { GATEWAY, callAt, mention, wait } from "../test/fixtures/notification-rules";
import { PETR, hasText, seedSession } from "../test/fixtures/schedules";
import { DefaultNotificationRules } from "./default-notification-rules";

function organization(
  normal: NotificationRuleAction[],
  important: NotificationRuleAction[],
): Pick<Organization, "notification_rules"> {
  return { notification_rules: { normal, important } };
}

async function renderDefaults(org: Pick<Organization, "notification_rules">) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedSession(queryClient, PETR, "admin", ["organization:write"]);
  const root = createRootRoute({
    component: () => <DefaultNotificationRules organization={org} />,
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

const card = () => page.getByTestId("default-notification-rules");

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("the default rules", () => {
  test("show both rules in words, the note about the API and the link", async () => {
    await renderDefaults(organization([mention], [mention, wait(5), mention]));
    await expect
      .element(card().getByRole("heading", { name: "Default notification rules" }))
      .toBeVisible();
    await hasText(
      page.getByTestId("default-rule-normal"),
      "Normal: a Mention in the Thread of the Root message.",
    );
    await hasText(
      page.getByTestId("default-rule-important"),
      "Important: a Mention in the Thread and again after 5 minutes if you did not answer.",
    );
    await hasText(card(), "They change through the API until an editor comes.");
    await hasText(page.getByTestId("default-rules-note"), "updateOrganization");
    await expect
      .element(card().getByRole("link", { name: /^Learn more: Notification rules/ }))
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/concepts/notification-rules/",
      );
  });

  test("follow a changed wait and name the gateway of a call", async () => {
    await renderDefaults(organization([mention], [mention, callAt(GATEWAY), wait(10), mention]));
    await hasText(
      page.getByTestId("default-rule-important"),
      "Important: a Mention in the Thread and a call through Example Gateway at the same time, again after 10 minutes.",
    );
  });

  test("read in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderDefaults(organization([mention], [mention, wait(5), mention]));
    await expect
      .element(card().getByRole("heading", { name: "Правила уведомлений по умолчанию" }))
      .toBeVisible();
    await hasText(
      page.getByTestId("default-rule-normal"),
      "Обычное: упоминание в треде корневого сообщения.",
    );
    await hasText(card(), "Пока их меняют через API; редактор появится позже.");
    await hasText(
      page.getByTestId("default-rule-important"),
      "Важное: упоминание в треде и ещё раз через 5 минут, если вы не ответили.",
    );
  });
});
