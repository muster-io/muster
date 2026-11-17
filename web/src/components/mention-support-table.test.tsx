// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "What each messenger supports" in a real browser (C-28.FR-2): the table of the requirement, a row per type of
// Destination, in English and in Russian, with its link to the documentation.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { SESSION_QUERY_KEY } from "../lib/api";
import { MentionSupportTable } from "./mention-support-table";

function renderTable() {
  const queryClient = new QueryClient();
  queryClient.setQueryData(SESSION_QUERY_KEY, { session: null, ended: false });
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <MentionSupportTable />
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

const row = (id: string) => page.getByTestId(`mention-support-${id}`);

describe("What each messenger supports", () => {
  test("shows the table of C-28.FR-2, a row per type", async () => {
    await renderTable();
    await expect
      .element(page.getByRole("heading", { name: /What each messenger supports/ }))
      .toBeVisible();
    const columns = page.getByRole("columnheader");
    await expect.element(columns.nth(0)).toHaveTextContent("Messenger");
    await expect.element(columns.nth(1)).toHaveTextContent("Everyone in the chat");
    await expect.element(columns.nth(2)).toHaveTextContent("Messenger groups and Teams");
    await expect.element(columns.nth(3)).toHaveTextContent("People and On-call");

    const cells = (id: string) => row(id).getByRole("cell");
    await expect.element(row("mattermost").getByRole("rowheader")).toHaveTextContent("Mattermost");
    await expect.element(cells("mattermost").nth(0)).toHaveTextContent("@channel, @here, @all");
    await expect.element(cells("mattermost").nth(1)).toHaveTextContent("Mattermost groups by name");
    await expect.element(cells("slack").nth(0)).toHaveTextContent("<!channel>, <!here>");
    await expect
      .element(cells("slack").nth(1))
      .toHaveTextContent(
        "Slack user groups, when the Connection's check found the scope usergroups:read",
      );
    await expect.element(cells("telegram").nth(0)).toHaveTextContent("None");
    await expect.element(cells("telegram").nth(1)).toHaveTextContent("None");
    await expect.element(cells("telegram").nth(2)).toHaveTextContent("Linked people");
    await expect
      .element(cells("bridge").nth(0))
      .toHaveTextContent("Those of @channel, @here, @all and @everyone that the bridge declares");
    await expect
      .element(cells("bridge").nth(2))
      .toHaveTextContent(
        "Linked people; a bridge without native mentions shows their plain name, which notifies nobody",
      );
    await expect.element(cells("webhook").nth(1)).toHaveTextContent("Every target, as data");
  });

  test("has the Russian texts and a Learn more link", async () => {
    await i18n.changeLanguage("ru");
    await renderTable();
    await expect
      .element(page.getByRole("heading", { name: /Поддержка мессенджерами/ }))
      .toBeVisible();
    await expect
      .element(row("telegram").getByRole("cell").nth(2))
      .toHaveTextContent("Привязанные люди");
    await expect
      .element(row("mattermost").getByRole("cell").nth(1))
      .toHaveTextContent("Группы Mattermost по имени");
    await expect
      .element(page.getByRole("link", { name: /Подробнее: Поддержка мессенджерами/ }))
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/concepts/mentions/#what-each-messenger-supports",
      );
  });
});
