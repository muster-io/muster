// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Heartbeat in the UI against `muster dev`: the Verification steps of the Heartbeat settings story. The demo
// Integration is live from the fake Alertmanager's Heartbeat; "edge" is created without one, a token shows how to get
// the Heartbeat configuration, the Heartbeat is turned on in the form, a second token shows the configuration with
// "Copy", signals sent with it make the badge live, the development clock makes it lost and a signal brings it back,
// while a Viewer reads the badges and the lost banner in Russian and 360 px wide.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
  advance,
  expectNoHorizontalScroll,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

// The fake Alertmanager's Heartbeat sender of the demo Integration.
const DEMO_HEARTBEAT = "http://127.0.0.1:19093/_fake/heartbeats/muster/send";
const HEARTBEAT_URL = "http://localhost:8081/api/v1/heartbeat";
const TIME_ZONE = "Europe/Berlin";

const NOT_CONFIGURED =
  "No Heartbeat: Muster will not notice when Alertmanager goes quiet, and Stale resolution is off.";
const WAITING = "Waiting for the first Heartbeat signal. Stale resolution is off until it arrives.";

/** Sends one Heartbeat signal with an Integration token, as Alertmanager's receiver would. */
async function signal(token: string): Promise<void> {
  const res = await fetch(HEARTBEAT_URL, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(res.status).toBe(204);
}

function integrationRow(page: Page, name: string, table = "Integrations") {
  return page
    .getByRole("table", { name: table })
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name, exact: true }) });
}

/**
 * The time of the last signal as the lost banner shows it: HH:MM in the time zone, with the date when not today and
 * the year when not this year. The signal carries the business clock's time, which earlier specs moved ahead, and the
 * page compares it with the browser's real time, so near midnight, or at the turn of the year, both parts show.
 */
function shownSince(iso: string, locale: string): string {
  const day = (d: Date) => d.toLocaleDateString("en-CA", { timeZone: TIME_ZONE });
  const now = new Date();
  const options: Intl.DateTimeFormatOptions =
    day(new Date(iso)) === day(now)
      ? { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: TIME_ZONE }
      : {
          day: "numeric",
          month: "short",
          ...(day(new Date(iso)).slice(0, 4) === day(now).slice(0, 4) ? {} : { year: "numeric" }),
          hour: "2-digit",
          minute: "2-digit",
          hourCycle: "h23",
          timeZone: TIME_ZONE,
        };
  return new Intl.DateTimeFormat(locale, options).format(new Date(iso));
}

/** The Heartbeat of an Integration as the API reads it. */
async function heartbeatOf(id: string): Promise<{
  enabled: boolean;
  timeout_seconds: number;
  last_signal_at?: string | null;
}> {
  const admin = await adminApi();
  try {
    const integration = await admin.call<{
      heartbeat: { enabled: boolean; timeout_seconds: number; last_signal_at?: string | null };
    }>("GET", `/api/v1/integrations/${id}`);
    return integration.heartbeat;
  } finally {
    await admin.dispose();
  }
}

/** Creates a token in the dialog of the Integration page and returns the dialog with the created token. */
async function createToken(page: Page, name: string) {
  await page.getByRole("button", { name: "Create token" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill(name);
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog.getByTestId("integration-token-value")).toBeVisible();
  return dialog;
}

test("the Heartbeat from the form to live, lost and live again", async ({ page, browser }) => {
  const csp = watchCsp(page);
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await signInAdmin(page);

  // 1. The demo Integration is live: the fake Alertmanager signals every minute; one signal now spares the wait for
  // the first one after the start, or for the next one after a move of the clock by an earlier spec.
  const sent = await fetch(DEMO_HEARTBEAT, { method: "POST" });
  expect(sent.ok, await sent.text()).toBe(true);
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Integrations" })
    .click();
  await expect(page.getByRole("columnheader", { name: "Heartbeat" })).toBeVisible();
  await expect(integrationRow(page, "dev-alertmanager").getByTestId("heartbeat-badge")).toHaveText(
    "Live",
  );
  // The built-in Integration has no Heartbeat.
  await expect(integrationRow(page, "Muster").getByTestId("heartbeat-badge")).toHaveCount(0);
  await shot(page, "heartbeat-list-live");

  // 2. "edge" without a Heartbeat; a token then has no Heartbeat configuration and says how to get it.
  await page.getByRole("link", { name: "Create integration" }).click();
  const heartbeatSwitch = page.getByRole("switch", { name: "Watch for Heartbeat signals" });
  await expect(heartbeatSwitch).not.toBeChecked();
  await expect(page.getByLabel("Timeout")).toHaveCount(0);
  await heartbeatSwitch.check();
  await expect(page.getByLabel("Timeout")).toHaveValue("5");
  await expect(
    page.getByText("The integration gets its Heartbeat URL when it is created."),
  ).toBeVisible();
  await shot(page, "heartbeat-form-create");
  await heartbeatSwitch.uncheck();
  await page.getByLabel("Name", { exact: true }).fill("edge");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: "edge", level: 1 })).toBeVisible();
  const badge = page.getByTestId("heartbeat-badge");
  const banner = page.getByTestId("heartbeat-banner");
  await expect(badge).toHaveText("Heartbeat: Not configured");
  await expect(banner).toHaveText(NOT_CONFIGURED);
  await shot(page, "heartbeat-not-configured");
  const integrationId = new URL(page.url()).pathname.split("/").pop() ?? "";

  let dialog = await createToken(page, "no-heartbeat");
  await expect(
    dialog.getByText("Turn the Heartbeat on and create a token to get its configuration."),
  ).toBeVisible();
  await expect(dialog.getByTestId("integration-heartbeat-snippet")).toHaveCount(0);
  await shot(page, "heartbeat-token-off");
  await dialog.getByRole("button", { name: "Done" }).click();

  // 3. Turn the Heartbeat on: the timeout is pre-filled with 5 minutes and the URL says how to pass the token.
  await page.getByRole("link", { name: "Edit" }).click();
  await heartbeatSwitch.check();
  await expect(page.getByLabel("Timeout")).toHaveValue("5");
  await expect(page.getByLabel("Heartbeat URL")).toHaveValue(HEARTBEAT_URL);
  await expect(
    page.getByText(
      "Send one of this integration's tokens as Authorization: Bearer, or append it as /<token>.",
    ),
  ).toBeVisible();
  // A timeout of nothing is refused in the form.
  await page.getByLabel("Timeout").fill("0");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Enter a number of minutes greater than 0.")).toBeVisible();
  // The timeout is sent in seconds.
  await page.getByLabel("Timeout").fill("10");
  await shot(page, "heartbeat-form-edit");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(badge).toHaveText("Heartbeat: Waiting");
  await expect(banner).toHaveText(WAITING);
  await shot(page, "heartbeat-waiting");
  expect(await heartbeatOf(integrationId)).toMatchObject({ enabled: true, timeout_seconds: 600 });

  // The form keeps the Heartbeat on and its timeout; back to 5 minutes for the steps below.
  await page.getByRole("link", { name: "Edit" }).click();
  await expect(heartbeatSwitch).toBeChecked();
  await expect(page.getByLabel("Timeout")).toHaveValue("10");
  await page.getByLabel("Timeout").fill("5");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(badge).toHaveText("Heartbeat: Waiting");
  expect(await heartbeatOf(integrationId)).toMatchObject({ enabled: true, timeout_seconds: 300 });

  // 4. A token now comes with the Heartbeat configuration, copied as shown.
  dialog = await createToken(page, "heartbeat");
  const token = (await dialog.getByTestId("integration-token-value").textContent()) ?? "";
  expect(token).toMatch(/^mstr_int_/);
  await expect(dialog.getByText("Heartbeat configuration", { exact: true })).toBeVisible();
  const snippetBlock = dialog.getByTestId("integration-heartbeat-snippet");
  await expect(snippetBlock).toContainText("vector(1)");
  await expect(snippetBlock).toContainText("/api/v1/heartbeat");
  await expect(
    dialog.getByText("Turn the Heartbeat on and create a token to get its configuration."),
  ).toHaveCount(0);
  const snippet = (await snippetBlock.textContent()) ?? "";
  await dialog.locator('button[aria-describedby="integration-heartbeat-snippet-label"]').click();
  await expect(dialog.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(snippet);
  await shot(page, "heartbeat-token-snippet");
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // 5. One signal: live without a reload, and no banner.
  await signal(token);
  await expect(badge).toHaveText("Heartbeat: Live");
  await expect(banner).toHaveCount(0);
  await shot(page, "heartbeat-live");

  // 6. Six minutes without a signal: lost since the signal of step 5, in the user's time zone.
  const lastSignal = (await heartbeatOf(integrationId)).last_signal_at ?? "";
  expect(lastSignal).not.toBe("");
  await advance(360);
  await expect(badge).toHaveText("Heartbeat: Lost");
  await expect(banner).toHaveText(
    `No contact with Alertmanager since ${shownSince(lastSignal, "en-US")}. Nothing is resolved as Stale until contact returns.`,
  );
  await shot(page, "heartbeat-lost");
  await page.goto("/integrations");
  await expect(integrationRow(page, "edge").getByTestId("heartbeat-badge")).toHaveText("Lost");
  await shot(page, "heartbeat-list-lost");

  // A Viewer reads the badge and the lost banner, in Russian and 360 px wide, and edits nothing.
  const viewerApi = await adminApi();
  const { token: setup } = await viewerApi.createUser("hb-viewer", "viewer");
  await viewerApi.call("POST", "/api/v1/password-setups", {
    token: setup,
    password: "hb-viewer-password-1",
  });
  await viewerApi.dispose();
  const viewerContext = await browser.newContext();
  const viewerPage = await viewerContext.newPage();
  const viewerCsp = watchCsp(viewerPage);
  await signIn(viewerPage, "hb-viewer", "hb-viewer-password-1");
  await viewerPage.goto("/profile");
  await viewerPage.getByLabel("Language").selectOption("ru");
  await expect(viewerPage.getByRole("navigation", { name: "Основная" })).toBeVisible();
  await viewerPage.goto("/integrations");
  await expect(viewerPage.getByRole("columnheader", { name: "Heartbeat" })).toBeVisible();
  await expect(
    integrationRow(viewerPage, "edge", "Интеграции").getByTestId("heartbeat-badge"),
  ).toHaveText("Нет связи");
  await expect(
    integrationRow(viewerPage, "dev-alertmanager", "Интеграции").getByTestId("heartbeat-badge"),
  ).toHaveText(/^(На связи|Нет связи)$/);
  await shot(viewerPage, "heartbeat-list-ru");
  await viewerPage.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(viewerPage);
  await shot(viewerPage, "heartbeat-list-ru-360");
  await viewerPage.setViewportSize({ width: 1280, height: 800 });
  await viewerPage.goto(`/integrations/${integrationId}`);
  await expect(viewerPage.getByTestId("heartbeat-badge")).toHaveText("Heartbeat: Нет связи");
  await expect(viewerPage.getByTestId("heartbeat-banner")).toHaveText(
    `Связи с Alertmanager нет с ${shownSince(lastSignal, "ru")}. Пока она не восстановится, ничего не закрывается как устаревшее.`,
  );
  for (const name of ["Изменить", "Удалить", "Создать токен"]) {
    await expect(
      viewerPage
        .getByRole("button", { name, exact: true })
        .or(viewerPage.getByRole("link", { name, exact: true })),
    ).toHaveCount(0);
  }
  await shot(viewerPage, "heartbeat-lost-ru");
  await viewerPage.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(viewerPage);
  await shot(viewerPage, "heartbeat-lost-ru-360");
  expect(viewerCsp).toEqual([]);
  await viewerContext.close();

  // 7. A signal: live again, and the banner is gone.
  await page.goto(`/integrations/${integrationId}`);
  await expect(badge).toHaveText("Heartbeat: Lost");
  await signal(token);
  await expect(badge).toHaveText("Heartbeat: Live");
  await expect(banner).toHaveCount(0);
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "heartbeat-live-360");
  expect(csp).toEqual([]);
});
