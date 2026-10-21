// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The banners of the shell in a real browser (C-21.FR-11, C-21.AC-8): an Admin gets "How to fix" beside "No replica is
// leading" and "Muster is recovering after downtime", each to its guide of the running version in a new tab, in English
// and in Russian; a user who is not an Admin reads the recovery banner without the link.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { Locator } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Permission, Session, SystemNotice } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { NoticeBanners } from "./notice-banners";

const NO_LEADER =
  "No replica is leading: Telegram and Bridge polling, Slack's Socket Mode, Heartbeat and Stale checks are paused.";
const RECOVERING = "Muster is recovering after downtime. Data may be incomplete for a while.";

const ADMIN: Permission[] = ["alert-groups:read", "system-status:read", "organization:write"];
const RESPONDER: Permission[] = ["alert-groups:read", "alert-groups:acknowledge"];

let notices: SystemNotice[] = [];

beforeEach(async () => {
  notices = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.endsWith("/api/v1/system-notices")) {
      return Promise.resolve(
        new Response(JSON.stringify({ items: notices }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    return Promise.reject(new Error(`unexpected request ${url}`));
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

function session(permissions: Permission[]): SessionRead {
  return {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the banners read only the Permissions
      user: { id: "US0000000000ME", name: "Me" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T09:00:00Z",
      idle_expires_at: "2026-10-13T09:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions,
    },
    ended: false,
  };
}

async function renderBanners(permissions: Permission[]) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, session(permissions));
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <NoticeBanners />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

/** A "How to fix" link with its text, opening the page and section of the running version in a new tab. */
async function expectFix(link: Locator, text: string, pathname: string, hash: string) {
  await expect.element(link).toHaveTextContent(text);
  await expect.element(link).toHaveAttribute("target", "_blank");
  await expect.element(link).toHaveAttribute("rel", "noopener noreferrer");
  const href = new URL(link.element().getAttribute("href") ?? "");
  expect(href.origin).toBe("https://muster-io.github.io");
  expect(href.pathname).toBe(pathname);
  expect(href.hash).toBe(hash);
}

describe("notice banners", () => {
  test("an Admin gets How to fix beside both banners", async () => {
    notices = [
      { kind: "recovering_after_downtime", audience: "all", since: null, until: null },
      { kind: "no_replica_leading", audience: "admins", since: null, until: null },
    ];
    const screen = await renderBanners(ADMIN);
    await expect.element(screen.getByText(NO_LEADER)).toBeVisible();
    await expect.element(screen.getByText(RECOVERING)).toBeVisible();

    const noLeader = screen.container.querySelector<HTMLElement>(
      '[data-notice="no_replica_leading"]',
    );
    const recovering = screen.container.querySelector<HTMLElement>(
      '[data-notice="recovering_after_downtime"]',
    );
    expect(noLeader?.querySelectorAll("a")).toHaveLength(1);
    expect(recovering?.querySelectorAll("a")).toHaveLength(1);
    await expectFix(
      screen.getByRole("link", {
        name: "How to fix: No replica is leading (opens in a new tab)",
      }),
      "How to fix",
      "/muster/latest/operations/runbooks/MusterNoLeader/",
      "",
    );
    await expectFix(
      screen.getByRole("link", {
        name: "How to fix: Recovery after downtime (opens in a new tab)",
      }),
      "How to fix",
      "/muster/latest/operations/high-availability/",
      "#after-downtime",
    );
  });

  test("a user who is not an Admin reads the recovery banner without a link", async () => {
    notices = [{ kind: "recovering_after_downtime", audience: "all", since: null, until: null }];
    const screen = await renderBanners(RESPONDER);
    await expect.element(screen.getByText(RECOVERING)).toBeVisible();
    expect(screen.container.querySelectorAll("a")).toHaveLength(0);
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    notices = [
      { kind: "recovering_after_downtime", audience: "all", since: null, until: null },
      { kind: "no_replica_leading", audience: "admins", since: null, until: null },
    ];
    const screen = await renderBanners(ADMIN);
    await expect
      .element(
        screen.getByText(
          "Нет ведущей реплики: опрос Telegram и мостов, Socket Mode в Slack, проверки Heartbeat и Stale приостановлены.",
        ),
      )
      .toBeVisible();
    await expectFix(
      screen.getByRole("link", {
        name: "Как исправить: Нет ведущей реплики (откроется в новой вкладке)",
      }),
      "Как исправить",
      "/muster/latest/operations/runbooks/MusterNoLeader/",
      "",
    );
    await expectFix(
      screen.getByRole("link", {
        name: "Как исправить: Восстановление после простоя (откроется в новой вкладке)",
      }),
      "Как исправить",
      "/muster/latest/operations/high-availability/",
      "#after-downtime",
    );
  });
});
