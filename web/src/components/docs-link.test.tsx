// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The documentation links in a real browser (C-21.FR-11, C-21.AC-8): an icon and the text "Guide", "Learn more" or
// "How to fix" in English and Russian, an accessible name that adds what the link is about and that it opens in a new
// tab, target=_blank and rel="noopener noreferrer", the version of the session in the address ("latest" until it has
// been read); PageTitle puts the link beside the h1 and under it at 360 px; nothing is loaded from the site.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { DOCS_SITE } from "../lib/docs";
// The title and its link are laid out by their classes, as in the application.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { DocsLink, PageTitle } from "./docs-link";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await page.viewport(1280, 800);
});

function session(docsVersion: string): SessionRead {
  return {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the links read only the docs_version
      user: { id: "US0000000000ME", name: "Me" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T09:00:00Z",
      idle_expires_at: "2026-10-13T09:00:00Z",
      docs_version: docsVersion,
      method: "local",
      permissions: [],
    },
    ended: false,
  };
}

async function renderIn(children: ReactNode, read?: SessionRead) {
  const queryClient = new QueryClient();
  if (read !== undefined) {
    queryClient.setQueryData(SESSION_QUERY_KEY, read);
  }
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>,
  );
}

describe("DocsLink", () => {
  test("a Guide link opens the page of the session's version in a new tab without a Referer", async () => {
    const screen = await renderIn(
      <DocsLink to="page.routes" kind="guide" about="Routes" />,
      session("0.1"),
    );
    const link = screen.getByRole("link", { name: "Guide: Routes (opens in a new tab)" });
    await expect.element(link).toHaveTextContent("Guide");
    await expect
      .element(link)
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/0.1/concepts/routes-and-group-keys/",
      );
    await expect.element(link).toHaveAttribute("target", "_blank");
    await expect.element(link).toHaveAttribute("rel", "noopener noreferrer");
    await expect.element(link).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(link.element().querySelector("svg[aria-hidden='true']")).not.toBeNull();
  });

  test("before the session has been read the link goes to latest", async () => {
    const screen = await renderIn(
      <DocsLink to="page.destinations" kind="guide" about="Destinations" />,
    );
    await expect
      .element(screen.getByRole("link"))
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/messengers/#what-each-destination-type-can-do",
      );
  });

  test.each([
    [
      "guide",
      "Guide",
      "Руководство",
      "Guide: Routes (opens in a new tab)",
      "Руководство: Маршруты (откроется в новой вкладке)",
    ],
    [
      "learn_more",
      "Learn more",
      "Подробнее",
      "Learn more: Routes (opens in a new tab)",
      "Подробнее: Маршруты (откроется в новой вкладке)",
    ],
    [
      "how_to_fix",
      "How to fix",
      "Как исправить",
      "How to fix: Routes (opens in a new tab)",
      "Как исправить: Маршруты (откроется в новой вкладке)",
    ],
  ] as const)("the %s link reads %s and %s", async (kind, english, russian, nameEn, nameRu) => {
    const screen = await renderIn(
      <DocsLink to="page.routes" kind={kind} about="Routes" />,
      session("latest"),
    );
    await expect.element(screen.getByRole("link", { name: nameEn })).toHaveTextContent(english);
    await i18n.changeLanguage("ru");
    await screen.rerender(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={new QueryClient()}>
          <DocsLink to="page.routes" kind={kind} about="Маршруты" />
        </QueryClientProvider>
      </I18nextProvider>,
    );
    await expect.element(screen.getByRole("link", { name: nameRu })).toHaveTextContent(russian);
  });

  test("the browser loads nothing from the site until the link is followed", async () => {
    performance.clearResourceTimings();
    await renderIn(<DocsLink to="page.routes" kind="guide" about="Routes" />, session("latest"));
    await expect.element(page.getByRole("link")).toBeVisible();
    const site = new URL(DOCS_SITE);
    const fromSite = performance
      .getEntriesByType("resource")
      .filter((e) => new URL(e.name).origin === site.origin);
    expect(fromSite).toEqual([]);
    const linked = Array.from(document.querySelectorAll<HTMLLinkElement>("link[href]")).filter(
      (l) => new URL(l.href, document.baseURI).origin === site.origin,
    );
    expect(linked).toEqual([]);
  });
});

describe("PageTitle", () => {
  test.each([
    ["page.alert_group", "Alert Group statuses and commands", "Статусы и команды групп алертов"],
    ["page.connection.telegram", "Telegram Connection", "Подключение Telegram"],
    ["page.connection.bridge", "Bridge Connection", "Подключение через мост"],
    ["page.destination.slack", "Slack Destination", "Место доставки Slack"],
    [
      "page.destination.webhook_template",
      "Outgoing webhook templates",
      "Шаблоны исходящего вебхука",
    ],
    ["page.destination.webhook_events", "Outgoing webhook events", "События исходящего вебхука"],
    ["page.integrations", "Integrations", "Интеграции"],
  ] as const)("the Guide of %s is about %s", async (docs, english, russian) => {
    const screen = await renderIn(<PageTitle docs={docs}>Title</PageTitle>, session("latest"));
    await expect
      .element(
        screen.getByRole("link", { name: `Guide: ${english} (opens in a new tab)`, exact: true }),
      )
      .toHaveTextContent("Guide");
    await i18n.changeLanguage("ru");
    await expect
      .element(
        screen.getByRole("link", {
          name: `Руководство: ${russian} (откроется в новой вкладке)`,
          exact: true,
        }),
      )
      .toHaveTextContent("Руководство");
  });

  test("renders the h1 with its Guide link beside it", async () => {
    const screen = await renderIn(
      <PageTitle docs="page.routes">Routes</PageTitle>,
      session("latest"),
    );
    const heading = screen.getByRole("heading", { level: 1, name: "Routes" });
    await expect.element(heading).toBeVisible();
    const link = screen.getByRole("link", { name: "Guide: Routes (opens in a new tab)" });
    await expect.element(link).toBeVisible();
    expect(heading.element().contains(link.element())).toBe(false);
    const h = heading.element().getBoundingClientRect();
    const l = link.element().getBoundingClientRect();
    expect(l.left).toBeGreaterThanOrEqual(h.right);
  });

  test("wraps the link under a long title at 360 px without a horizontal scrollbar", async () => {
    await page.viewport(360, 800);
    const title = "A very long name of a Destination that takes the whole line";
    const screen = await renderIn(
      <div className="w-full max-w-full px-4">
        <PageTitle docs="page.destination.telegram">{title}</PageTitle>
      </div>,
      session("latest"),
    );
    const heading = screen.getByRole("heading", { level: 1, name: title });
    const link = screen.getByRole("link", {
      name: "Guide: Telegram Destination (opens in a new tab)",
    });
    await expect.element(link).toBeVisible();
    const h = heading.element().getBoundingClientRect();
    const l = link.element().getBoundingClientRect();
    expect(l.top).toBeGreaterThanOrEqual(h.bottom - 1);
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(360);
  });
});
