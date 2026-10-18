// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Request log against `muster dev`, its fake Mattermost server, fake webhook receiver and fake bridge: Verification
// steps 1 to 5 of its story. An Alert Group of the Route "rl" is published to "alerts" through "Dev Mattermost", and a
// person presses a button of its post in the fake server. Connections → "Dev Mattermost" → "Request log" lists the
// createPost first, sent, with 201 and its latency; "Received" lists only presses, with the arrow named "Received", and
// "2xx" keeps the createPost, each filter in the URL. The record shows the request body as indented JSON with its
// "channel_id", Authorization as [redacted], the answer 201, the Destination and the Alert Group, and "Copy body" puts
// the body on the clipboard. "Requests" of "alerts" on the Alert Group page lists the same createPost. A test message to
// the outgoing webhook "rl-hook", whose endpoint answers 500, lists its event with 500 and the endpoint's body under
// "Requests of this check" and in the Destination's own tab; the check of a new Bridge Connection before saving lists
// its getInfo as "Check before saving". A reader of Russian reads the tab, a record and the Alert Group page at phone
// width. No page scrolls sideways at 360 px; no Content Security Policy violation.

import { type Browser, expect, type Locator, type Page, test } from "@playwright/test";

import {
  type Api,
  BRIDGE_SECOND_TOKEN,
  BRIDGE_URL,
  FMM,
  adminApi,
  connectionNamed,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  resetFakeBridge,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const FWH = "http://127.0.0.1:18093";
const TEAM = "rl";
const HOOK = "rl-hook";
/** A person of the fake Mattermost server whom no spec before this one links on "Dev Mattermost". */
const PRESSER = "u-dana";

const NOBODY = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (k) => [k, NOBODY],
  ),
);

interface Named {
  id: string;
  name: string;
}

interface RecordRow {
  id: string;
  operation: string;
}

const ids = { connection: "", alerts: "", hook: "", integration: "" };
const group = { id: "", number: 0 };

async function fake(url: string, method: string, body?: unknown): Promise<void> {
  const res = await fetch(url, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  expect(res.ok, `${method} ${url}: ${res.status}`).toBe(true);
}

async function cleanUp(admin: Api): Promise<void> {
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => x.name === TEAM)) {
    await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${r.id}`);
  }
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => x.name === "alerts" || x.name === HOOK)) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
}

/** The records of the Request log that a query of listRequestLog names, newest first. */
async function records(query: string): Promise<RecordRow[]> {
  const admin = await adminApi();
  try {
    return (await admin.call<{ items: RecordRow[] }>("GET", `/api/v1/request-log?${query}`)).items;
  } finally {
    await admin.dispose();
  }
}

async function delivery(): Promise<{ state: string; message_url?: string | null }> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { state: string; message_url?: string | null }[] }>(
      "GET",
      `/api/v1/alert-groups/${group.id}/deliveries`,
    );
    return page.items[0] ?? { state: "none" };
  } finally {
    await admin.dispose();
  }
}

/** Presses "Ack" on the Root message in the fake server and waits until the Request log holds the press. */
async function pressAck(): Promise<void> {
  const url = (await delivery()).message_url ?? "";
  const postId = url.split("/").pop() ?? "";
  expect(postId, `the post of ${url}`).not.toBe("");
  const before = (await records(`connection=${ids.connection}&operation=press`)).length;
  const res = await fetch(`${FMM}/press`, {
    method: "POST",
    body: JSON.stringify({ post_id: postId, action_id: "ack", user_id: PRESSER }),
  });
  expect(res.ok, `POST /_fake/press: ${res.status}`).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own answer
  expect(((await res.json()) as { status: number }).status).toBe(200);
  await expect
    .poll(async () => (await records(`connection=${ids.connection}&operation=press`)).length)
    .toBeGreaterThan(before);
}

function sections(page: Page): Locator {
  return page.getByRole("tablist", { name: "Sections of the page" });
}

function rows(scope: Page | Locator): Locator {
  return scope.locator("tbody tr");
}

/**
 * A screenshot of what the viewport shows, for a dialog: a full page would add the page under the dialog, which the
 * dialog covers.
 */
async function dialogShot(page: Page, name: string): Promise<void> {
  const dir = process.env.MUSTER_E2E_SCREENSHOTS;
  if (dir !== undefined && dir !== "") {
    await page.screenshot({ path: `${dir}/${name}.png` });
  }
}

/** Nothing is wider than the 360 px of the phone. */
async function expectPhoneWidth(page: Page): Promise<void> {
  await expectNoHorizontalScroll(page);
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(width, `scrollWidth ${width}`).toBeLessThanOrEqual(360);
}

test.beforeAll(async () => {
  await resetFakeBridge();
  await fake(`${FWH}/_fake/faults`, "DELETE");
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    ids.connection = await connectionNamed(admin, "Dev Mattermost");
    const alerts = await admin.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "mattermost",
        name: "alerts",
        connection_id: ids.connection,
        team_id: "team-dev",
        channel_id: "ch-alerts",
        mentions: MENTIONS,
        limiter: { limit: 50, per_seconds: 1 },
      },
    );
    ids.alerts = alerts.destination.id;
    const hook = await admin.call<{ destination: { id: string } }>("POST", "/api/v1/destinations", {
      type: "webhook",
      name: HOOK,
      mode: "events",
      events: { url: `${FWH}/hook/rl`, headers: [] },
      proxy: { enabled: false },
      mentions: MENTIONS,
      limiter: { limit: 50, per_seconds: 1 },
    });
    ids.hook = hook.destination.id;
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    await admin.call("POST", "/api/v1/routes", {
      name: TEAM,
      matchers: [{ label: "team", op: "=", value: TEAM }],
      urgent: false,
      group_key: ["alertname", "cluster"],
      destination_ids: [ids.alerts],
      policy: profiles.items.find((p) => p.id === "on_call")?.policy,
    });
  } finally {
    await admin.dispose();
  }
  ids.integration = await fakeIntegration("rl");
  await fam("PUT", "/groups/rl", {
    receiver: "rl",
    route: "{}",
    labels: { alertname: "RequestLogCheck" },
  });
  await fam("PUT", "/groups/rl/alerts/i1", {
    labels: { team: TEAM, cluster: "rl1", severity: "warning" },
  });
  await notify(ids.integration, "rl", { reason: "first notification" });
  const admin2 = await adminApi();
  try {
    const page = await admin2.call<{ items: { alert_group?: { id: string; number: number } }[] }>(
      "GET",
      `/api/v1/integrations/${ids.integration}/alerts?label=${encodeURIComponent('cluster="rl1"')}`,
    );
    const found = page.items[0]?.alert_group;
    expect(found, "the Alert Group of rl1").toBeDefined();
    group.id = found?.id ?? "";
    group.number = found?.number ?? 0;
  } finally {
    await admin2.dispose();
  }
  await expect.poll(async () => (await delivery()).state, { timeout: 30_000 }).toBe("delivered");
  await expect
    .poll(
      async () =>
        (
          await records(
            `destination=${ids.alerts}&alert_group=${group.id}&operation=createPost&limit=1`,
          )
        ).length,
    )
    .toBe(1);
});

test.afterAll(async () => {
  await fake(`${FWH}/_fake/faults`, "DELETE");
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test("lists, filters and opens the requests of a Connection and of an Alert Group's delivery", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await signInAdmin(page);

  // 1. Connections → "Dev Mattermost" → "Request log": the newest row is the createPost, sent, 201, with its latency.
  await page.goto("/connections");
  await page.getByRole("link", { name: "Dev Mattermost", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Dev Mattermost", level: 1 })).toBeVisible();
  await sections(page).getByRole("tab", { name: "Request log" }).click();
  await expect(page).toHaveURL(/[?&]tab=request-log(&|$)/);
  const table = page.getByRole("table", { name: "Request log" });
  const first = rows(table).first();
  await expect(first.getByRole("img", { name: "Sent", exact: true })).toBeVisible();
  await expect(first.getByTestId("request-operation")).toHaveText("createPost");
  await expect(first.getByTestId("request-status")).toHaveText("201");
  await expect(first.getByTestId("request-latency")).toHaveText(/^\d+ ms$/);
  await shot(page, "request-log-connection");

  // A press reaches Muster: "Received" lists presses only, each with the arrow named "Received".
  await pressAck();
  const filters = page.getByRole("search", { name: "Request log filters" });
  await filters.getByLabel("Direction").selectOption({ label: "Received" });
  await expect(page).toHaveURL(/[?&]direction=inbound(&|$)/);
  const operations = table.getByTestId("request-operation");
  await expect(operations.first()).toHaveText("press");
  await expect(table.getByRole("img", { name: "Sent", exact: true })).toHaveCount(0);
  const shown = await rows(table).count();
  await expect(operations.filter({ hasText: /^press$/ })).toHaveCount(shown);
  await expect(table.getByRole("img", { name: "Received", exact: true })).toHaveCount(shown);

  // Any direction and the status class 2xx: the createPost stays, and every status is a 2xx.
  await filters.getByLabel("Direction").selectOption({ label: "Any direction" });
  await filters.getByLabel("Status").selectOption({ label: "2xx" });
  await expect(page).toHaveURL(/[?&]status_class=2xx(&|$)/);
  await expect(page).not.toHaveURL(/direction=/);
  const createPost = table.getByRole("button", { name: "createPost", exact: true }).first();
  await expect(createPost).toBeVisible();
  await expect
    .poll(async () =>
      (await table.getByTestId("request-status").allTextContents()).every((s) => /^2\d\d$/.test(s)),
    )
    .toBe(true);

  // 2. The record: the request body as indented JSON, Authorization as [redacted], the answer 201; "Copy body" copies
  // the body as Muster sent it.
  const [newest] = await records(`connection=${ids.connection}&operation=createPost&limit=1`);
  const admin = await adminApi();
  const stored = await admin.call<{ request_body: string }>(
    "GET",
    `/api/v1/request-log/${newest?.id ?? ""}`,
  );
  await admin.dispose();
  await createPost.click();
  const record = page.getByRole("dialog", { name: "Sent createPost" });
  const request = record.getByRole("region", { name: "Request" });
  const answer = record.getByRole("region", { name: "Answer" });
  await expect(request.getByTestId("record-request-line")).toHaveText(/^POST \S+\/api\/v4\/posts$/);
  await expect(request.getByTestId("record-request-body")).toContainText(
    '"channel_id": "ch-alerts"',
  );
  const authorization = request
    .getByTestId("record-request-headers")
    .getByTestId("record-header")
    .filter({ has: page.locator("dt", { hasText: /^Authorization$/ }) });
  await expect(authorization.locator("dd")).toHaveText("[redacted]");
  await expect(answer.getByTestId("record-status")).toHaveText("201");
  await expect(record.getByTestId("record-destination")).toHaveText("alerts");
  await expect(record.getByTestId("record-alert-group")).toHaveText(`#${group.number}`);
  await request.getByRole("button", { name: "Copy body" }).click();
  await expect(request.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(stored.request_body);
  await dialogShot(page, "request-log-record");
  // Escape closes the record and keeps the list.
  await page.keyboard.press("Escape");
  await expect(record).toHaveCount(0);
  await expect(createPost).toBeVisible();

  // 3. The Alert Group page → delivery section → "Requests" on "alerts" lists the same createPost.
  await page.goto(`/alert-groups/${group.id}`);
  const row = page
    .getByTestId("alert-group-deliveries")
    .getByTestId("delivery-row")
    .filter({ hasText: "alerts" });
  const requests = row.getByRole("button", { name: "Requests", exact: true });
  await requests.click();
  const dialog = page.getByRole("dialog", { name: "Requests: alerts" });
  await expect(dialog.getByRole("button", { name: "createPost", exact: true })).toBeVisible();
  await dialogShot(page, "request-log-alert-group");

  // At 360 px: the Alert Group page with the dialog open and closed, the tab and the record scroll vertically only.
  await page.setViewportSize({ width: 360, height: 740 });
  await expect(dialog.getByRole("button", { name: "createPost", exact: true })).toBeVisible();
  await expectPhoneWidth(page);
  await dialogShot(page, "request-log-alert-group-360");
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(requests).toBeVisible();
  await expectPhoneWidth(page);
  await shot(page, "request-log-alert-group-page-360");
  await page.goto(`/connections/${ids.connection}?tab=request-log`);
  await expect(rows(table).first().getByTestId("request-operation")).toBeVisible();
  await expectPhoneWidth(page);
  await shot(page, "request-log-connection-360");
  await table.getByRole("button", { name: "createPost", exact: true }).first().click();
  await expect(request.getByTestId("record-request-body")).toBeVisible();
  await expectPhoneWidth(page);
  await dialogShot(page, "request-log-record-360");
  expect(csp).toEqual([]);
});

test("lists the requests of a test message and of a check before saving", async ({ page }) => {
  test.setTimeout(90_000);
  const csp = watchCsp(page);
  await fake(`${FWH}/_fake/faults`, "POST", {
    path: "/hook/rl",
    status: 500,
    body: '{"error":"endpoint down"}',
  });
  await signInAdmin(page);

  // 4. The outgoing webhook → "Send test message" → "Requests of this check": the event, answered 500 with its body.
  await page.goto(`/destinations/${ids.hook}`);
  await expect(page.getByRole("heading", { name: HOOK, level: 1 })).toBeVisible();
  const panel = page.getByTestId("destination-test-preview");
  await panel.getByRole("tab", { name: "Test" }).click();
  await panel.getByLabel("Based on").selectOption({ label: "Example" });
  const answered = page.waitForResponse(
    (r) => r.url().endsWith("/tests") && r.request().method() === "POST",
  );
  await panel.getByRole("button", { name: "Send test message" }).click();
  await answered;
  await expect(panel.getByTestId("test-result")).toBeVisible();
  await panel.getByRole("button", { name: "Requests of this check" }).click();
  const dialog = page.getByRole("dialog", { name: "Requests of this check" });
  const event = rows(dialog)
    .filter({ has: page.getByRole("button", { name: "event", exact: true }) })
    .first();
  await expect(event.getByTestId("request-status")).toHaveText("500");
  await expect(event.getByTestId("request-check")).toHaveText("Test message");
  await event.getByRole("button", { name: "event", exact: true }).click();
  const record = page.getByRole("dialog", { name: "Sent event" });
  const answer = record.getByRole("region", { name: "Answer" });
  await expect(answer.getByTestId("record-status")).toHaveText("500");
  await expect(answer.getByTestId("record-response-body")).toContainText(
    '"error": "endpoint down"',
  );
  await expect(record.getByTestId("record-check")).toHaveText("Test message");
  await dialogShot(page, "request-log-test-message");
  await page.keyboard.press("Escape");
  await expect(record).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);

  // The tab "Request log" of the Destination lists the event too.
  await sections(page).getByRole("tab", { name: "Request log" }).click();
  await expect(page).toHaveURL(/[?&]tab=request-log(&|$)/);
  const first = rows(page.getByRole("table", { name: "Request log" })).first();
  await expect(first.getByTestId("request-operation")).toHaveText("event");
  await expect(first.getByTestId("request-status")).toHaveText("500");
  await expect(first.getByTestId("request-check")).toHaveText("Test message");
  await shot(page, "request-log-destination");

  // 5. Connections → New → Bridge with the second bot's token → "Check connection" → "Requests of this check": getInfo,
  // marked "Check before saving".
  await page.goto("/connections/new");
  await page.getByRole("radio", { name: "Bridge", exact: true }).check();
  await page.getByTestId("bridge-connection-fields").getByLabel("Base URL").fill(BRIDGE_URL);
  await page
    .getByTestId("secret-connection-bot-token")
    .getByRole("button", { name: "Set a value" })
    .click();
  await page.getByLabel("Token", { exact: true }).fill(BRIDGE_SECOND_TOKEN);
  await page.getByRole("button", { name: "Check connection", exact: true }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText("The check passed.");
  await page.getByRole("button", { name: "Requests of this check" }).click();
  const check = page.getByRole("dialog", { name: "Requests of this check" });
  const info = rows(check).filter({
    has: page.getByRole("button", { name: "getInfo", exact: true }),
  });
  await expect(info).toHaveCount(1);
  await expect(info.getByTestId("request-check")).toHaveText("Check before saving");
  await expect(info.getByTestId("request-status")).toHaveText("200");
  await dialogShot(page, "request-log-draft-check");
  expect(csp).toEqual([]);
});

test("a reader of Russian reads the Request log at phone width", async ({ browser }) => {
  const admin = await adminApi();
  const { token } = await admin.createUser("rl-viewer-ru", "viewer");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "rl-viewer-ru-1" });
  await admin.dispose();
  // The Viewer stays: users.spec.ts counts the deleted Viewers of the run.
  await russianViewer(browser);
});

async function russianViewer(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "rl-viewer-ru", "rl-viewer-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  // The tab «Журнал запросов» of "Dev Mattermost", sent and received requests, and a record.
  await page.goto(`/connections/${ids.connection}`);
  await page
    .getByRole("tablist", { name: "Разделы страницы" })
    .getByRole("tab", { name: "Журнал запросов" })
    .click();
  const table = page.getByRole("table", { name: "Журнал запросов" });
  await expect(table.getByRole("img", { name: "Отправлен", exact: true }).first()).toBeVisible();
  await expectPhoneWidth(page);
  await shot(page, "request-log-ru-360");
  const filters = page.getByRole("search", { name: "Фильтры журнала запросов" });
  await filters.getByLabel("Направление").selectOption({ label: "Получен" });
  await expect(table.getByTestId("request-operation").first()).toHaveText("press");
  await expect(table.getByRole("img", { name: "Получен", exact: true }).first()).toBeVisible();
  await expect(table.getByRole("img", { name: "Отправлен", exact: true })).toHaveCount(0);
  await filters.getByLabel("Направление").selectOption({ label: "Любое направление" });
  await table.getByRole("button", { name: "createPost", exact: true }).first().click();
  const record = page.getByRole("dialog", { name: "Отправлен createPost" });
  await expect(record.getByRole("region", { name: "Запрос" })).toBeVisible();
  await expect(record.getByRole("region", { name: "Ответ" })).toBeVisible();
  await expect(
    record.getByRole("region", { name: "Запрос" }).getByRole("button", { name: "Копировать тело" }),
  ).toBeVisible();
  await expectPhoneWidth(page);
  await dialogShot(page, "request-log-record-ru-360");
  await page.keyboard.press("Escape");
  await expect(record).toHaveCount(0);

  // The Alert Group page: «Запросы» on "alerts" opens the createPost.
  await page.goto(`/alert-groups/${group.id}`);
  const requests = page
    .getByTestId("alert-group-deliveries")
    .getByTestId("delivery-row")
    .filter({ hasText: "alerts" })
    .getByRole("button", { name: "Запросы", exact: true });
  await expect(requests).toBeVisible();
  await expectPhoneWidth(page);
  await requests.click();
  const dialog = page.getByRole("dialog", { name: "Запросы: alerts" });
  await expect(dialog.getByRole("button", { name: "createPost", exact: true })).toBeVisible();
  await expectPhoneWidth(page);
  await dialogShot(page, "request-log-alert-group-ru-360");
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  expect(csp).toEqual([]);
  await context.close();
}
