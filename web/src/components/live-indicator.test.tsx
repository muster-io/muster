// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The mark of the live updates in a real browser (C-09.FR-26): each state's dot, text and status region, where the text
// shows (below the sm breakpoint only the dot, in every state, on a tap target of 32 px), the dot's own shape in each
// state, so that no state is told by colour alone, the popover, a dialog named by its title, the state's text, with the
// explanation of reference.md and, in offline, the time of the loss in the user's time zone and "Reload page"; in
// English and in Russian; and the mark following the page's store.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { type Locator, page } from "vitest/browser";
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

/**
 * Whether every shape of the dot lies inside its viewBox of 8 × 8, its stroke included, so that nothing of it is clipped.
 */
function insideViewBox(svg: Element): boolean {
  return [...svg.querySelectorAll<SVGGeometryElement>("circle, path")].every((shape) => {
    const box = shape.getBBox();
    const half = Number(shape.getAttribute("stroke-width") ?? 0) / 2;
    return (
      box.x - half >= 0 &&
      box.y - half >= 0 &&
      box.x + box.width + half <= 8 &&
      box.y + box.height + half <= 8
    );
  });
}

/** The popover shows the state's text as its title, which names the dialog: no aria-label of its own. */
async function expectTitled(dialog: Locator, name: string): Promise<void> {
  const title = dialog.getByRole("heading", { name, exact: true });
  await expect.element(title).toBeVisible();
  await expect.element(title).toHaveTextContent(name);
  await expect.element(dialog).toHaveAccessibleName(name);
  await expect.element(dialog).toHaveAttribute("aria-labelledby", title.element().id);
  await expect.element(dialog).not.toHaveAttribute("aria-label");
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await page.viewport(414, 896);
  await i18n.changeLanguage("en");
  vi.restoreAllMocks();
});

/** Waits until the page is as wide as the viewport just set, so that a check of the layout sees the new width. */
async function viewport(width: number, height: number): Promise<void> {
  await page.viewport(width, height);
  await vi.waitFor(() => expect(window.innerWidth).toBe(width));
}

describe("the mark", () => {
  test.each([
    // [state, overdue, text, shape of the dot, its colour classes, text shown from the sm breakpoint up]
    ["connecting", false, "Connecting…", "ring", ["text-muted-foreground"], false],
    ["connecting", true, "Connecting…", "ring", ["text-muted-foreground"], true],
    ["live", false, "Live", "filled", ["text-emerald-600", "dark:text-emerald-400"], true],
    [
      "reconnecting",
      false,
      "Reconnecting…",
      "half",
      ["text-warning", "animate-pulse", "motion-reduce:animate-none"],
      true,
    ],
    ["offline", false, "Offline", "slashed", ["text-destructive"], true],
  ] as const)(
    "%s (overdue %s): the text %s in a status region, below the sm breakpoint only the dot, from it up where the text shows",
    async (state, overdue, text, shape, colour, shownFromSm) => {
      await viewport(360, 740);
      const screen = await renderMark(snapshot(state, overdue));
      const region = screen.getByRole("status");
      await expect.element(region).toHaveAttribute("data-testid", "live-indicator");
      await expect.element(region).toHaveAttribute("data-state", state);
      await expect.element(region).toHaveAttribute("aria-live", "polite");
      await expect.element(region).toHaveTextContent(text);
      // The button is inside the region, named by the text, which is in the DOM once.
      const button = region.getByRole("button", { name: text });
      await expect.element(button).toBeVisible();
      expect(region.element().textContent).toBe(text);
      // The dot is a picture of 8 × 8 px hidden from assistive technology, with the state's shape and colour.
      const dotElement = screen.getByTestId("live-indicator-dot");
      expect(dotElement.element()).toBeInstanceOf(SVGSVGElement);
      await expect.element(dotElement).toHaveAttribute("aria-hidden", "true");
      await expect.element(dotElement).toHaveAttribute("focusable", "false");
      await expect.element(dotElement).toHaveAttribute("viewBox", "0 0 8 8");
      await expect.element(dotElement).toHaveAttribute("data-shape", shape);
      await expect.element(dotElement).toHaveClass("size-2", "shrink-0", ...colour);
      const dotBox = dotElement.element().getBoundingClientRect();
      expect([dotBox.width, dotBox.height]).toEqual([8, 8]);
      expect(insideViewBox(dotElement.element())).toBe(true);
      // Below sm only the dot shows, on a button of at least 32 × 32 px, whatever the state.
      const textElement = screen.getByTestId("live-indicator-text").element();
      expect(screenReaderOnly(textElement)).toBe(true);
      const box = button.element().getBoundingClientRect();
      expect(box.width).toBeGreaterThanOrEqual(32);
      expect(box.height).toBeGreaterThanOrEqual(32);
      // From sm up the text shows beside the dot, but while connecting only once overdue.
      await viewport(1280, 800);
      expect(screenReaderOnly(textElement)).toBe(!shownFromSm);
      await expect.element(button).toHaveAccessibleName(text);
    },
  );

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
      await expectTitled(dialog, name);
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
    await expectTitled(dialog, "Offline");
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
      ["offline", "Нет связи"],
    ] as const) {
      await viewport(360, 740);
      const screen = await renderMark(snapshot(state, true));
      await expect.element(screen.getByRole("status")).toHaveTextContent(text);
      await expect.element(screen.getByRole("button", { name: text })).toBeVisible();
      const textElement = screen.getByTestId("live-indicator-text").element();
      expect(screenReaderOnly(textElement)).toBe(true);
      await viewport(1280, 800);
      expect(screenReaderOnly(textElement)).toBe(false);
      await screen.unmount();
    }

    await viewport(360, 740);
    const live = await renderMark(snapshot("live"));
    await live.getByRole("button", { name: "Онлайн" }).click();
    await expectTitled(page.getByRole("dialog", { name: "Онлайн", exact: true }), "Онлайн");
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
    await expectTitled(
      page.getByRole("dialog", { name: "Подключение…", exact: true }),
      "Подключение…",
    );
    await expect
      .element(
        page.getByText(
          "Подключаемся к обновлениям в реальном времени. Данные на странице могут быть устаревшими.",
          { exact: true },
        ),
      )
      .toBeVisible();
    await connecting.unmount();

    const reconnecting = await renderMark(snapshot("reconnecting"));
    await reconnecting.getByRole("button", { name: "Переподключение…" }).click();
    const reconnectingDialog = page.getByRole("dialog", { name: "Переподключение…", exact: true });
    await expectTitled(reconnectingDialog, "Переподключение…");
    await expect.element(reconnectingDialog).toHaveAccessibleDescription(RU_LOST_TEXT);
    await reconnecting.unmount();

    const reload = vi.fn();
    const offline = await renderMark(snapshot("offline"), reload);
    await expect.element(offline.getByRole("status")).toHaveTextContent("Нет связи");
    await offline.getByRole("button", { name: "Нет связи" }).click();
    const offlineDialog = page.getByRole("dialog", { name: "Нет связи", exact: true });
    await expectTitled(offlineDialog, "Нет связи");
    await expect.element(offlineDialog).toHaveAccessibleDescription(RU_LOST_TEXT);
    await expect.element(page.getByText(RU_LOST_TEXT, { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Перезагрузить страницу" }).click();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  test("each state has its own shape, so that no state is told by the colour of the dot alone", async () => {
    await viewport(360, 740);
    const shapes = new Map<LiveState, string | null>();
    for (const state of ["connecting", "live", "reconnecting", "offline"] as const) {
      const screen = await renderMark(snapshot(state));
      shapes.set(
        state,
        screen.getByTestId("live-indicator-dot").element().getAttribute("data-shape"),
      );
      await screen.unmount();
    }
    expect(Object.fromEntries(shapes)).toEqual({
      connecting: "ring",
      live: "filled",
      reconnecting: "half",
      offline: "slashed",
    });
    expect(new Set(shapes.values()).size).toBe(4);
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
