// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The mark of the live updates in a real browser (C-09.FR-26): each state's dot, text and status region, where the text
// shows, the popover, a dialog named by the state, with the explanation of reference.md and, in offline, the time of the
// loss in the user's time zone and "Reload page"; in English and in Russian; and the mark following the page's store.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { type LiveSnapshot, type LiveState, createLiveStatus } from "../lib/live-status";
import { LiveIndicator, LiveMark } from "./live-indicator";
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";

/** The user's time zone, which has no daylight saving time: 02:07 UTC is 11:07 there. */
const ZONE = "Asia/Tokyo";
const LOST = "2026-10-19T02:07:00Z";
const LOST_TEXT =
  "No live updates since 11:07. What you see may be out of date; it is read again as soon as the connection is back.";
const RU_LOST_TEXT =
  "Нет обновлений в реальном времени с 11:07. Данные на странице могут быть устаревшими; они перечитаются, как только связь вернётся.";

const SESSION: SessionRead = {
  session: {
    state: "active",
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the mark reads only the user's time zone
    user: { id: "US0000000000ME", name: "Me", time_zone: ZONE } as Session["user"],
    csrf_token: "csrf",
    expires_at: "2026-10-20T09:00:00Z",
    idle_expires_at: "2026-10-20T09:00:00Z",
    method: "local",
    permissions: [],
  },
  ended: false,
};

function snapshot(state: LiveState, overdue = false): LiveSnapshot {
  return { state, lostAt: LOST, overdue };
}

function providers(children: ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, SESSION);
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
}

function renderMark(s: LiveSnapshot, reload?: () => void) {
  return render(providers(<LiveMark snapshot={s} reload={reload} />));
}

/** Whether an element is shown only to screen readers: the sr-only box is one pixel wide. */
function screenReaderOnly(element: Element): boolean {
  return element.getBoundingClientRect().width <= 1;
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await page.viewport(414, 896);
  await i18n.changeLanguage("en");
  vi.restoreAllMocks();
});

describe("the mark", () => {
  test.each([
    // [state, overdue, text, dot classes, text shown at 414 px]
    ["connecting", false, "Connecting…", ["bg-muted-foreground"], false],
    ["connecting", true, "Connecting…", ["bg-muted-foreground"], true],
    ["live", false, "Live", ["bg-emerald-600", "dark:bg-emerald-400"], false],
    [
      "reconnecting",
      false,
      "Reconnecting…",
      ["bg-warning", "animate-pulse", "motion-reduce:animate-none"],
      true,
    ],
    ["offline", false, "Offline", ["bg-destructive"], true],
  ] as const)(
    "%s (overdue %s): the text %s in a status region, the dot, and where the text shows",
    async (state, overdue, text, dot, shown) => {
      const screen = await renderMark(snapshot(state, overdue));
      const region = screen.getByRole("status");
      await expect.element(region).toHaveAttribute("data-testid", "live-indicator");
      await expect.element(region).toHaveAttribute("data-state", state);
      await expect.element(region).toHaveAttribute("aria-live", "polite");
      await expect.element(region).toHaveTextContent(text);
      // The button is inside the region, named by the text, which is in the DOM once.
      await expect.element(region.getByRole("button", { name: text })).toBeVisible();
      expect(region.element().textContent).toBe(text);
      const dotElement = screen.getByTestId("live-indicator-dot");
      await expect.element(dotElement).toHaveAttribute("aria-hidden", "true");
      await expect.element(dotElement).toHaveClass(...dot);
      expect(screenReaderOnly(screen.getByTestId("live-indicator-text").element())).toBe(!shown);
    },
  );

  test("in live the text shows from the sm breakpoint up, below it only the dot", async () => {
    await page.viewport(360, 740);
    const screen = await renderMark(snapshot("live"));
    const text = screen.getByTestId("live-indicator-text");
    await expect.element(screen.getByRole("status")).toHaveTextContent("Live");
    expect(screenReaderOnly(text.element())).toBe(true);
    expect(screen.getByTestId("live-indicator-dot").element().getBoundingClientRect().width).toBe(
      8,
    );
    await page.viewport(1280, 800);
    await vi.waitFor(() => expect(screenReaderOnly(text.element())).toBe(false));
    await expect.element(text).toBeVisible();
  });

  test.each([
    ["live", "Live", "Live updates are on: lists and pages change as soon as something happens."],
    ["connecting", "Connecting…", "Connecting to live updates. What you see may be out of date."],
    ["reconnecting", "Reconnecting…", LOST_TEXT],
  ] as const)(
    "pressed in %s, it explains the state in a dialog named %s, without Reload page",
    async (state, name, text) => {
      const reload = vi.fn();
      const screen = await renderMark(snapshot(state), reload);
      await screen.getByRole("button").click();
      const dialog = page.getByRole("dialog", { name, exact: true });
      await expect.element(dialog).toBeVisible();
      await expect.element(dialog).toHaveAccessibleName(name);
      await expect.element(dialog).toHaveAccessibleDescription(text);
      await expect.element(dialog.getByText(text, { exact: true })).toBeVisible();
      await expect
        .element(page.getByRole("button", { name: "Reload page" }))
        .not.toBeInTheDocument();
      expect(reload).not.toHaveBeenCalled();
    },
  );

  test("pressed in offline, it names the time of the loss in the user's time zone and reloads the page", async () => {
    const reload = vi.fn();
    const screen = await renderMark(snapshot("offline"), reload);
    await screen.getByRole("button", { name: "Offline" }).click();
    const dialog = page.getByRole("dialog", { name: "Offline", exact: true });
    await expect.element(dialog).toHaveAccessibleName("Offline");
    await expect.element(dialog).toHaveAccessibleDescription(LOST_TEXT);
    await expect.element(dialog.getByText(LOST_TEXT, { exact: true })).toBeVisible();
    const button = page.getByRole("button", { name: "Reload page" });
    await expect.element(button).toBeVisible();
    expect(reload).not.toHaveBeenCalled();
    await button.click();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    for (const [state, text] of [
      ["connecting", "Подключение…"],
      ["live", "Онлайн"],
      ["reconnecting", "Переподключение…"],
    ] as const) {
      const screen = await renderMark(snapshot(state, true));
      await expect.element(screen.getByRole("status")).toHaveTextContent(text);
      await expect.element(screen.getByRole("button", { name: text })).toBeVisible();
      await screen.unmount();
    }

    const live = await renderMark(snapshot("live"));
    await live.getByRole("button", { name: "Онлайн" }).click();
    await expect
      .element(
        page.getByText(
          "Обновления в реальном времени включены: списки и страницы меняются, как только что-то происходит.",
          { exact: true },
        ),
      )
      .toBeVisible();
    await live.unmount();

    const connecting = await renderMark(snapshot("connecting"));
    await connecting.getByRole("button", { name: "Подключение…" }).click();
    await expect
      .element(
        page.getByText(
          "Подключаемся к обновлениям в реальном времени. Данные на странице могут быть устаревшими.",
          { exact: true },
        ),
      )
      .toBeVisible();
    await connecting.unmount();

    const reload = vi.fn();
    const offline = await renderMark(snapshot("offline"), reload);
    await expect.element(offline.getByRole("status")).toHaveTextContent("Нет связи");
    await offline.getByRole("button", { name: "Нет связи" }).click();
    await expect
      .element(page.getByRole("dialog", { name: "Нет связи", exact: true }))
      .toHaveAccessibleDescription(RU_LOST_TEXT);
    await expect.element(page.getByText(RU_LOST_TEXT, { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Перезагрузить страницу" }).click();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  test("follows the store of the live updates", async () => {
    const status = createLiveStatus();
    status.start();
    const screen = await render(providers(<LiveIndicator status={status} />));
    const region = screen.getByTestId("live-indicator");
    await expect.element(region).toHaveAttribute("data-state", "connecting");
    status.opened();
    await expect.element(region).toHaveAttribute("data-state", "live");
    await expect.element(region).toHaveTextContent("Live");
    status.start();
    await expect.element(region).toHaveAttribute("data-state", "connecting");
    status.stop();
  });
});
