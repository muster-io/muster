// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The mark of the live updates in the header against `muster dev`: Verification steps 4 to 8 of its story. The spec
// routes the live-updates URL instead of cutting the browser's network: the first stream is held until the page clock
// is paused, then answered with `retry: 3000` and an event stream that ends at once, and the browser's reconnects are
// aborted. The page clock moves the SPA's timers only, so the mark turns "Reconnecting…" after runFor(5 s) and
// "Offline" after runFor(25 s) more, while the browser keeps reconnecting every 3 s on the real clock. Once the route
// is removed, a reconnect reaches `muster dev`, the mark turns "Live" and the list is read again, its Firing count
// including the Alert Group the fake Alertmanager created meanwhile. A loss of 4 s on the page clock leaves "Live". At
// 360 × 740, in English and in Russian, the offline text shows in the header of the list and of an Alert Group page
// without horizontal scrolling, "Reload page" loads the page again, and in live only the dot shows. The requests of the
// page are the stream and its reads.

import { type Page, type Request, type Route, expect, test } from "@playwright/test";

import {
  APP,
  adminApi,
  createRoute,
  deleteRoute,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const LIVE_UPDATES = "/api/v1/live-updates";
const FIRST_TITLE = "LiveMarkFirst";
const OLGA = "live-olga";
const OLGA_PASSWORD = "live-olga-password-1";

let integrationId = "";
let routeId = "";

test.beforeAll(async () => {
  integrationId = await fakeIntegration("live-mark");
  routeId = await createRoute("live-mark", "live-mark", ["alertname"]);
  await fam("PUT", "/groups/lm1", {
    receiver: "live-mark",
    route: "{}",
    labels: { alertname: FIRST_TITLE },
  });
  await fam("PUT", "/groups/lm1/alerts/a0", { labels: { team: "live-mark", pod: "a0" } });
  await notify(integrationId, "lm1", { reason: "first notification" });
});

test.afterAll(async () => {
  if (routeId !== "") {
    await deleteRoute(routeId);
  }
});

function isLiveUpdates(url: URL): boolean {
  return url.pathname === LIVE_UPDATES;
}

/** A one-time signal between the spec and a route handler. */
class Signal {
  fire: () => void = () => {};
  readonly fired = new Promise<void>((resolve) => {
    this.fire = resolve;
  });
}

/** A request of the stream that the spec holds: arrived fires when it comes, released lets it be answered. */
function gate() {
  return { arrived: new Signal(), released: new Signal() };
}

/**
 * Routes the stream of the page. The first request is held until released, then answered with `retry: 3000` and an
 * event stream that ends at once, as when the stream is lost. The second, the browser's reconnect 3 s later, is
 * aborted, or with "pass" held until released and then let through to `muster dev`; later ones are aborted.
 */
async function routeStream(page: Page, second: "abort" | "pass") {
  const gates = { first: gate(), second: gate() };
  let seen = 0;
  const handler = async (route: Route) => {
    seen += 1;
    if (seen === 1) {
      gates.first.arrived.fire();
      await gates.first.released.fired;
      await route.fulfill({
        status: 200,
        headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-store" },
        body: "retry: 3000\n\n",
      });
      return;
    }
    if (seen === 2) {
      gates.second.arrived.fire();
      if (second === "pass") {
        await gates.second.released.fired;
        await route.continue();
        return;
      }
    }
    await route.abort();
  };
  await page.route(isLiveUpdates, handler);
  return { ...gates, unroute: () => page.unroute(isLiveUpdates, handler) };
}

/** Stops the page clock, so that only runFor moves the SPA's timers. */
async function pauseClock(page: Page): Promise<void> {
  const now = await page.evaluate(() => Date.now());
  await page.clock.pauseAt(now + 1_000);
}

function mark(page: Page) {
  return page.getByTestId("live-indicator");
}

/** Whether the mark's text is on the screen, not only for screen readers, whose box is one pixel wide. */
async function textShown(page: Page): Promise<boolean> {
  const box = await mark(page).getByTestId("live-indicator-text").boundingBox();
  return box !== null && box.width > 1;
}

function tab(page: Page, name: string) {
  return page.getByRole("tab", { name: new RegExp(`^${name} \\d+$`) });
}

async function count(page: Page, name: string): Promise<number> {
  return Number(await tab(page, name).getByTestId("tab-count").textContent());
}

/** A read of the page, which an invalidation sends: a GET of the API other than the stream. */
function isRead(request: Request): boolean {
  const url = new URL(request.url());
  return (
    request.method() === "GET" &&
    url.pathname.startsWith("/api/v1/") &&
    url.pathname !== LIVE_UPDATES
  );
}

/** What the page may request: the stream, the reads of the API and the SPA's own files, all GETs to Muster. */
function allowed(request: Request): boolean {
  const url = new URL(request.url());
  return (
    request.method() === "GET" &&
    url.origin === APP &&
    (url.pathname === LIVE_UPDATES ||
      url.pathname.startsWith("/api/v1/") ||
      !url.pathname.startsWith("/api/"))
  );
}

test("the mark follows the stream, and the list is read again once it is back", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await page.clock.install();
  await signInAdmin(page);

  const requests: Request[] = [];
  page.on("request", (r) => requests.push(r));

  // 4. The stream ends and its reconnects fail: "Live", then "Reconnecting…" after 5 s and "Offline" after 30 s.
  const lost = await routeStream(page, "abort");
  await page.goto("/alert-groups?tab=%22firing%22");
  await expect(tab(page, "Firing")).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("link", { name: FIRST_TITLE, exact: true })).toBeVisible();
  const firing = await count(page, "Firing");
  await lost.first.arrived.fired;
  await expect(mark(page)).toHaveAttribute("data-state", "connecting");
  await pauseClock(page);
  const fromLoss = requests.length;
  lost.first.released.fire();
  await expect(mark(page)).toHaveAttribute("data-state", "live");
  await expect(mark(page)).toHaveRole("status");
  await expect(mark(page)).toHaveText("Live");
  expect(await textShown(page)).toBe(true);
  // The browser reconnects 3 s later on the real clock: the stream was lost, and the mark still says "Live".
  await lost.second.arrived.fired;
  await expect(mark(page)).toHaveAttribute("data-state", "live");
  await page.clock.runFor(5_000);
  await expect(mark(page)).toHaveAttribute("data-state", "reconnecting");
  await expect(mark(page)).toHaveText("Reconnecting…");
  await expect(mark(page).getByTestId("live-indicator-dot")).toBeVisible();
  await shot(page, "live-indicator-reconnecting-en");
  await page.clock.runFor(25_000);
  await expect(mark(page)).toHaveAttribute("data-state", "offline");
  await expect(mark(page)).toHaveText("Offline");

  // Pressed, the mark explains the state with the time of the loss and offers to reload the page. Offline lasts until
  // the stream opens, so the clock may run again.
  await page.clock.resume();
  await mark(page).getByRole("button", { name: "Offline" }).click();
  await expect(
    page.getByText(
      /^No live updates since \d{2}:\d{2}\. What you see may be out of date; it is read again as soon as the connection is back\.$/,
    ),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Reload page" })).toBeVisible();
  await shot(page, "live-indicator-offline-en");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Reload page" })).toHaveCount(0);

  // 5. A new Alert Group meanwhile; once the stream can be reached again, the mark turns "Live" and the list is read
  // again, without a reload.
  await fam("PUT", "/groups/lm2", {
    receiver: "live-mark",
    route: "{}",
    labels: { alertname: "LiveMarkWhileOffline" },
  });
  await fam("PUT", "/groups/lm2/alerts/b0", { labels: { team: "live-mark", pod: "b0" } });
  await notify(integrationId, "lm2", { reason: "first notification" });
  expect(await count(page, "Firing")).toBe(firing);
  // While the mark changed, the page sent nothing but the browser's reconnects of the stream.
  const whileLost = requests.slice(fromLoss).filter((r) => !isLiveUpdates(new URL(r.url())));
  expect(whileLost.map((r) => `${r.method()} ${r.url()}`)).toEqual([]);
  await lost.unroute();
  await expect(mark(page)).toHaveAttribute("data-state", "live");
  await expect(mark(page)).toHaveText("Live");
  await expect.poll(() => count(page, "Firing")).toBe(firing + 1);

  // 6. A loss of 4 s on the page clock, the next reconnect let through: "Live" throughout.
  const brief = await routeStream(page, "pass");
  await page.goto("/alert-groups?tab=%22firing%22");
  await expect(page.getByRole("link", { name: FIRST_TITLE, exact: true })).toBeVisible();
  await brief.first.arrived.fired;
  await pauseClock(page);
  brief.first.released.fire();
  await expect(mark(page)).toHaveAttribute("data-state", "live");
  await brief.second.arrived.fired;
  for (let second = 0; second < 4; second++) {
    await page.clock.runFor(1_000);
    await expect(mark(page)).toHaveAttribute("data-state", "live");
  }
  const readAgain = page.waitForRequest(isRead);
  brief.second.released.fire();
  // The open reads the page again; the timers of the loss are cancelled, so 5 s and 30 s pass without a change.
  await readAgain;
  await page.clock.runFor(1_000);
  await expect(mark(page)).toHaveAttribute("data-state", "live");
  await page.clock.runFor(30_000);
  await expect(mark(page)).toHaveAttribute("data-state", "live");
  await brief.unroute();

  // 8. Through steps 4 to 6 the page requested the stream, the reads of the API and its static files, nothing else.
  const streams = requests.filter((r) => isLiveUpdates(new URL(r.url())));
  expect(streams.length).toBeGreaterThanOrEqual(4);
  expect(requests.filter(isRead).length).toBeGreaterThan(0);
  expect(
    requests.filter((r) => !allowed(r)).map((r) => `${r.method()} ${r.resourceType()} ${r.url()}`),
  ).toEqual([]);
  expect(csp).toEqual([]);
});

const TEXTS = {
  en: {
    live: "Live",
    reconnecting: "Reconnecting…",
    offline: "Offline",
    lost: /^No live updates since \d{2}:\d{2}\. What you see may be out of date; it is read again as soon as the connection is back\.$/,
    reload: "Reload page",
    heading: "Alert Groups",
  },
  ru: {
    live: "Онлайн",
    reconnecting: "Переподключение…",
    offline: "Нет связи",
    lost: /^Нет обновлений в реальном времени с \d{2}:\d{2}\. Данные на странице могут быть устаревшими; они перечитаются, как только связь вернётся\.$/,
    reload: "Перезагрузить страницу",
    heading: "Группы алертов",
  },
} as const;

/** Signs in a user who reads Muster in the language: the Admin in English, the Responder live-olga in Russian. */
async function signInIn(page: Page, language: "en" | "ru"): Promise<void> {
  if (language === "en") {
    await signInAdmin(page);
    return;
  }
  const admin = await adminApi();
  try {
    const { token } = await admin.createUser(OLGA, "responder");
    await admin.call("POST", "/api/v1/password-setups", { token, password: OLGA_PASSWORD });
  } finally {
    await admin.dispose();
  }
  await signIn(page, OLGA, OLGA_PASSWORD);
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
}

for (const language of ["en", "ru"] as const) {
  test(`360 px wide in ${language === "en" ? "English" : "Russian"}: the offline text in the header of the list and of an Alert Group page, in live only the dot`, async ({
    page,
  }) => {
    const text = TEXTS[language];
    const csp = watchCsp(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.clock.install();
    await signInIn(page, language);

    // 7. The list: "Reconnecting…", then the offline text in the header, without horizontal scrolling.
    const lost = await routeStream(page, "abort");
    await page.goto("/alert-groups");
    await expect(page.getByRole("heading", { name: text.heading, level: 1 })).toBeVisible();
    const row = page.getByTestId("alert-group-row").filter({ hasText: FIRST_TITLE });
    await expect(row).toHaveCount(1);
    await lost.first.arrived.fired;
    await pauseClock(page);
    lost.first.released.fire();
    await expect(mark(page)).toHaveAttribute("data-state", "live");
    await lost.second.arrived.fired;
    await page.clock.runFor(5_000);
    await expect(mark(page)).toHaveAttribute("data-state", "reconnecting");
    await expect(mark(page)).toHaveText(text.reconnecting);
    expect(await textShown(page)).toBe(true);
    await expectNoHorizontalScroll(page);
    await shot(page, `live-indicator-reconnecting-${language}-360`);
    await page.clock.runFor(25_000);
    await expect(mark(page)).toHaveAttribute("data-state", "offline");
    const header = page.getByRole("banner");
    await expect(header.getByText(text.offline, { exact: true })).toBeVisible();
    expect(await textShown(page)).toBe(true);
    await expectNoHorizontalScroll(page);
    await shot(page, `live-indicator-offline-${language}-360-list`);

    await page.clock.resume();
    await mark(page).getByRole("button", { name: text.offline }).click();
    await expect(page.getByText(text.lost)).toBeVisible();
    await expect(page.getByRole("button", { name: text.reload })).toBeVisible();
    await expectNoHorizontalScroll(page);
    await shot(page, `live-indicator-popover-${language}-360`);
    await page.keyboard.press("Escape");
    await expect(page.getByRole("button", { name: text.reload })).toHaveCount(0);

    // The Alert Group page, opened from the list: still offline, the text in its header.
    await row.getByRole("link").click();
    await expect(page.getByRole("heading", { name: FIRST_TITLE, level: 1 })).toBeVisible();
    await expect(mark(page)).toHaveAttribute("data-state", "offline");
    await expect(header.getByText(text.offline, { exact: true })).toBeVisible();
    await expectNoHorizontalScroll(page);
    await shot(page, `live-indicator-offline-${language}-360-page`);

    // "Reload page" loads the page again, which starts connecting; the stream is still out of reach.
    await mark(page).getByRole("button", { name: text.offline }).click();
    const reloaded = page.waitForEvent("load");
    await page.getByRole("button", { name: text.reload }).click();
    await reloaded;
    await expect(page.getByRole("heading", { name: FIRST_TITLE, level: 1 })).toBeVisible();
    await expect(mark(page)).toHaveAttribute("data-state", "connecting");

    // Live: only the dot shows, the region still holds the text for screen readers; from 640 px up the text shows. The
    // browser's next attempt reaches `muster dev` within 3 s, or the shell's new stream within 10 s.
    await lost.unroute();
    await expect(mark(page)).toHaveAttribute("data-state", "live", { timeout: 20_000 });
    await expect(mark(page)).toHaveRole("status");
    await expect(mark(page)).toHaveText(text.live);
    await expect(mark(page).getByRole("button", { name: text.live })).toBeVisible();
    await expect(mark(page).getByTestId("live-indicator-dot")).toBeVisible();
    expect(await textShown(page)).toBe(false);
    await expectNoHorizontalScroll(page);
    await shot(page, `live-indicator-live-${language}-360`);
    await page.setViewportSize({ width: 1280, height: 800 });
    await expect.poll(() => textShown(page)).toBe(true);
    await expect(header.getByText(text.live, { exact: true })).toBeVisible();
    await shot(page, `live-indicator-live-${language}-1280`);
    expect(csp).toEqual([]);
  });
}
