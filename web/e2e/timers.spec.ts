// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Ack timeouts, Unclaimed, Reminders and "Still on it" in the UI against `muster dev`: Verification steps 1 to 9 of
// their story. The Route editor shows the "Ack timeout and Reminders" section with the profile's values and creates
// the Route "tm" (team="tm") to the Mattermost Destination "tm-alerts"; the fake Alertmanager fires tm-g1 through the
// Integration "tm-lab", and the development clock, advanced step by step until every due timer fired and its messages
// were sent, brings the three ack timeout notices and Unclaimed: the page shows the badge, the banner and the Timeline
// entries without a reload, the list filters it with "Unclaimed". The Responder "tm-bob" acknowledges, the page shows
// the next Reminder, and after it "Still on it", which only he sees. On the Route "tm-au" with auto-unacknowledge two
// unanswered Reminders give the Alert Group back; the advance outlasts the session idle timeout, so both sign in
// again. Names carry "tm" so that they never meet those of other specs of the run, and at the end Alertmanager resolves
// both Alert Groups, so that no timer of theirs fires during the clock advances of the specs after this one. No page,
// the Route editor included, scrolls sideways at 360 × 740; no Content Security Policy violation.

import { type Browser, expect, test, type Page } from "@playwright/test";

import {
  APP,
  Api,
  SUBPIXEL,
  adminApi,
  advance,
  deleteRoute,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  scalar,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const CLOCK = "http://localhost:8082/_dev/clock";
const SERVER_URL = "http://127.0.0.1:18065";
const BOB = "tm-bob";
const BOB_PASSWORD = "tm-bob-password-1";
const ZONE = "Europe/Berlin";
const NOBODY = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (k) => [k, NOBODY],
  ),
);
const HOUR = 3600;
const PHONE = { width: 360, height: 740 };
const TOKEN_NAME = "tm-timers";

interface Named {
  id: string;
  name: string;
}

interface Group {
  id: string;
  number: number;
}

interface AlertGroupRead {
  status: string;
  unclaimed: boolean;
  owner?: { name: string };
  next_notice?: { kind: string; at: string };
}

let integrationId = "";
let destinationId = "";
/** The Personal access tokens of the Admin and of tm-bob: the clock moves by days, past any session. */
let tokenA = "";
let tokenB = "";
let g1: Group = { id: "", number: 0 };
/** The fake groups the spec fired, with the labels of their Alert, and the ids of their Alert Groups. */
const fired: { name: string; labels: Record<string, string> }[] = [];
const groupIds: string[] = [];

/** Calls the API with a Personal access token. */
async function pat<T = unknown>(
  token: string,
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const res = await fetch(`${APP}${path}`, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  expect(res.ok, `${method} ${path}: ${res.status} ${text}`).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the spec describes the body; the assertions check it
  return JSON.parse(text === "" ? "null" : text) as T;
}

/** The business clock of `muster dev`, in milliseconds. */
async function clockNow(): Promise<number> {
  const res = await fetch(CLOCK);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the development clock answers {now, offset_seconds}
  const body = (await res.json()) as { now: string };
  return Date.parse(body.now);
}

/**
 * Waits until every timer of the spec's Alert Groups due on the development clock has fired and the Root messages and
 * thread replies they caused reached "tm-alerts", so that the next advance never collapses notices that were due one
 * by one. Rows of other specs never hold it up.
 */
async function settle(): Promise<void> {
  const now = new Date(await clockNow()).toISOString();
  await expect
    .poll(
      async () =>
        Number(
          await scalar(
            `SELECT (SELECT count(*) FROM timers t JOIN alert_groups g ON g.id = t.alert_group_id
                  WHERE g.public_id = ANY($2::text[]) AND t.deadline <= $1)
              + (SELECT count(*) FROM thread_replies r
                  JOIN alert_groups g ON g.id = r.alert_group_id
                  JOIN destinations d ON d.id = r.destination_id
                  WHERE g.public_id = ANY($2::text[]) AND d.public_id = $3 AND r.state = 'pending')
              + (SELECT count(*) FROM deliveries x
                  JOIN alert_groups g ON g.id = x.alert_group_id
                  JOIN destinations d ON d.id = x.destination_id
                  WHERE g.public_id = ANY($2::text[]) AND d.public_id = $3 AND x.state = 'pending')`,
            [now, groupIds, destinationId],
          ),
        ),
      { timeout: 60_000, message: "the due timers of the spec and their messages" },
    )
    .toBe(0);
}

/** Moves the development clock by each number of seconds in turn, settling after each. */
async function adv(...seconds: number[]): Promise<void> {
  for (const s of seconds) {
    await advance(s);
    await settle();
  }
}

/** A time as the page shows it to a user of the time zone: "Oct 12, 2026, 16:45". */
function fmt(iso: string, timeZone: string): string {
  return new Intl.DateTimeFormat("en", {
    dateStyle: "medium",
    timeStyle: "short",
    hourCycle: "h23",
    timeZone,
  }).format(new Date(iso));
}

/** The time zone the page uses for a user: the profile's, or the browser's. */
async function zoneOf(token: string): Promise<string> {
  const me = await pat<{ user: { time_zone?: string | null } }>(token, "GET", "/api/v1/me");
  return me.user.time_zone ?? ZONE;
}

async function readGroup(id: string): Promise<AlertGroupRead> {
  return pat<AlertGroupRead>(tokenA, "GET", `/api/v1/alert-groups/${id}`);
}

/** The next notice of an Alert Group once the server has scheduled one of the kind. */
async function nextNotice(id: string, kind: string): Promise<string> {
  let at = "";
  await expect
    .poll(async () => {
      const next = (await readGroup(id)).next_notice;
      at = next?.at ?? "";
      return next?.kind;
    })
    .toBe(kind);
  return at;
}

/** Expects an instant to be seconds after the business clock's now, within a minute. */
async function expectAhead(at: string, seconds: number): Promise<void> {
  const ahead = (Date.parse(at) - (await clockNow())) / 1000;
  expect(Math.abs(ahead - seconds), `${at} is ${ahead} s ahead, want ${seconds}`).toBeLessThan(60);
}

/** Starts a firing Alert Group of one warning Alert in the fake group name, on the Route of team. */
async function fire(name: string, alertname: string, team: string): Promise<Group> {
  const labels = { team, cluster: name, severity: "warning" };
  await fam("PUT", `/groups/${name}`, { receiver: "tm-lab", route: "{}", labels: { alertname } });
  await fam("PUT", `/groups/${name}/alerts/a`, { labels });
  fired.push({ name, labels });
  await notify(integrationId, name, { reason: "first notification" });
  const page = await pat<{ items: { alert_group?: Group }[] }>(
    tokenA,
    "GET",
    `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent(`cluster="${name}"`)}`,
  );
  const group = page.items[0]?.alert_group;
  expect(group, `the alert group of ${name}`).toBeDefined();
  if (group !== undefined) {
    groupIds.push(group.id);
  }
  return group ?? { id: "", number: 0 };
}

/**
 * Alertmanager resolves the Alert of every fake group the spec fired, and the spec waits until their Alert Groups are
 * resolved and their messages sent: no ack timeout or Reminder of theirs is left for a later spec's clock advance.
 */
async function resolveFired(): Promise<void> {
  for (const f of fired) {
    await fam("PUT", `/groups/${f.name}/alerts/a`, { labels: f.labels, status: "resolved" });
    await notify(integrationId, f.name, { reason: "all alerts resolved" });
  }
  for (const id of groupIds) {
    await expect.poll(async () => (await readGroup(id)).status).toBe("resolved");
  }
  await settle();
}

/** Creates a local user with a password, unless a spec before this one did. */
async function ensureUser(admin: Api, login: string, password: string): Promise<void> {
  const directory = await admin.call<{ items: { login?: string }[] }>(
    "GET",
    `/api/v1/user-directory?q=${encodeURIComponent(login)}`,
  );
  if (!directory.items.some((u) => u.login === login)) {
    const { token } = await admin.createUser(login, "responder");
    await admin.call("POST", "/api/v1/password-setups", { token, password });
  }
}

/** A Personal access token with every Permission of the signed-in user, without an end. */
async function personalToken(api: Api): Promise<string> {
  const me = await api.call<{ permissions: string[] }>("GET", "/api/v1/me");
  const created = await api.call<{ value: string }>("POST", "/api/v1/me/personal-access-tokens", {
    name: TOKEN_NAME,
    permissions: me.permissions,
  });
  return created.value;
}

/** Revokes the spec's Personal access tokens of the signed-in user, so that the specs after it see none. */
async function revokeTokens(api: Api): Promise<void> {
  const tokens = await api.call<{ items: Named[] }>("GET", "/api/v1/me/personal-access-tokens");
  for (const t of tokens.items.filter((x) => x.name === TOKEN_NAME)) {
    await api.call("DELETE", `/api/v1/me/personal-access-tokens/${t.id}`);
  }
}

async function cleanUp(admin: Api): Promise<void> {
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => x.name === "tm" || x.name === "tm-au")) {
    await deleteRoute(r.id);
  }
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => x.name === "tm-alerts")) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
  const connections = await admin.call<{ items: Named[] }>("GET", "/api/v1/connections?limit=500");
  for (const c of connections.items.filter((x) => x.name === "tm-mm")) {
    await admin.call("DELETE", `/api/v1/connections/${c.id}`);
  }
}

/** A page of its own signed in as tm-bob, at a desktop width or as a phone. */
async function bobPage(browser: Browser, phone = false): Promise<Page> {
  const context = await browser.newContext({
    baseURL: APP,
    locale: "en-US",
    timezoneId: ZONE,
    ...(phone ? { viewport: PHONE, hasTouch: true } : {}),
  });
  const page = await context.newPage();
  await signIn(page, BOB, BOB_PASSWORD);
  return page;
}

/** A phone of the Admin's. */
async function adminPhone(browser: Browser): Promise<Page> {
  const context = await browser.newContext({
    baseURL: APP,
    locale: "en-US",
    timezoneId: ZONE,
    viewport: PHONE,
    hasTouch: true,
  });
  const page = await context.newPage();
  await signInAdmin(page);
  return page;
}

function entry(page: Page, text: string) {
  return page.getByTestId("timeline-entry").filter({
    has: page.getByTestId("timeline-text").getByText(text, { exact: true }),
  });
}

/** The right edge of an element is inside the phone's width. */
async function expectInside(page: Page, testId: string): Promise<void> {
  const box = await page.getByTestId(testId).boundingBox();
  expect(box, testId).not.toBeNull();
  expect((box?.x ?? 0) + (box?.width ?? 0), testId).toBeLessThanOrEqual(PHONE.width + SUBPIXEL);
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    await ensureUser(admin, BOB, BOB_PASSWORD);
    const connection = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
      type: "mattermost",
      name: "tm-mm",
      server_url: SERVER_URL,
      bot_token: "mm-dev-token",
      proxy: { enabled: false },
      limiter: { limit: 50, per_seconds: 1 },
    });
    const created = await admin.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "mattermost",
        name: "tm-alerts",
        connection_id: connection.id,
        team_id: "team-dev",
        channel_id: "ch-alerts-prod",
        mentions: MENTIONS,
        limiter: { limit: 50, per_seconds: 1 },
      },
    );
    destinationId = created.destination.id;
    tokenA = await personalToken(admin);
  } finally {
    await admin.dispose();
  }
  const bob = await Api.signIn(BOB, BOB_PASSWORD);
  try {
    tokenB = await personalToken(bob);
  } finally {
    await bob.dispose();
  }
  integrationId = await fakeIntegration("tm-lab");
});

test.afterAll(async () => {
  await resolveFired();
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    await revokeTokens(admin);
  } finally {
    await admin.dispose();
  }
  const bob = await Api.signIn(BOB, BOB_PASSWORD);
  try {
    await revokeTokens(bob);
  } finally {
    await bob.dispose();
  }
});

test("the Route editor shows the ack timeout and Reminders of the profile and creates the Route", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/routes");
  await page.getByRole("link", { name: "Create route" }).click();
  await page.getByRole("button", { name: "Paging" }).click();

  // 1. Paging: the ack timeout on at 15 minutes with its three notices, the Reminders at 4 and 24 hours, no
  //    auto-unacknowledge.
  const section = page.getByRole("group", { name: "Ack timeout and Reminders" });
  await expect(section).toBeVisible();
  const ack = section.getByRole("switch", { name: "Ack timeout", exact: true });
  const reminders = section.getByRole("switch", { name: "Reminders", exact: true });
  const auto = section.getByRole("switch", { name: "Auto-unacknowledge" });
  await expect(ack).toBeChecked();
  await expect(section.getByLabel("First notice after")).toHaveValue("15");
  await expect(section.getByTestId("ack-timeout-times")).toHaveText(
    "Notices at 15, 45 and 105 minutes",
  );
  await expect(reminders).toBeChecked();
  await expect(section.getByLabel("First Reminder after")).toHaveValue("4");
  await expect(section.getByLabel("At most every")).toHaveValue("24");
  await expect(section.getByTestId("reminder-times")).toHaveText(
    "Reminders at 4, 12, 28, 52 and 76 hours",
  );
  await expect(auto).not.toBeChecked();
  await expect(auto).toBeEnabled();
  await expect(
    section.getByText(
      "Take the Alert Group back when its Owner leaves two Reminders in a row unanswered.",
    ),
  ).toBeVisible();
  await section.scrollIntoViewIfNeeded();
  await shot(page, "route-timers-paging");

  // Informational: both switches off, their fields hidden, auto-unacknowledge disabled.
  await page.getByRole("button", { name: "Choose another profile" }).click();
  await page.getByRole("button", { name: "Informational" }).click();
  await expect(ack).not.toBeChecked();
  await expect(reminders).not.toBeChecked();
  await expect(section.getByLabel("First notice after")).toHaveCount(0);
  await expect(section.getByLabel("First Reminder after")).toHaveCount(0);
  await expect(auto).toBeDisabled();
  await expect(section.getByText("Turn on Reminders to use it.")).toBeVisible();

  // Back to Paging: the Route "tm" for team="tm" to "tm-alerts".
  await page.getByRole("button", { name: "Choose another profile" }).click();
  await page.getByRole("button", { name: "Paging" }).click();
  await expect(ack).toBeChecked();
  await page.getByRole("textbox", { name: "Name" }).fill("tm");
  await page.getByRole("button", { name: "Add matcher" }).click();
  await page.getByRole("textbox", { name: "Label of matcher 1" }).fill("team");
  await page.getByRole("textbox", { name: "Value of matcher 1" }).fill("tm");
  await page.getByLabel("Add a Destination").selectOption(destinationId);
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page).toHaveURL(/\/routes$/);

  const routes = await pat<{
    items: (Named & { policy: { ack_timeout: unknown; reminders: unknown } })[];
  }>(tokenA, "GET", "/api/v1/routes");
  const tm = routes.items.find((r) => r.name === "tm");
  expect(tm?.policy.ack_timeout).toEqual({ enabled: true, first_interval_seconds: 900 });
  expect(tm?.policy.reminders).toEqual({
    enabled: true,
    first_interval_seconds: 4 * HOUR,
    cap_seconds: 24 * HOUR,
  });
  expect(csp).toEqual([]);
});

test("the ack timeout and Reminders section fits a phone", async ({ browser }) => {
  // 9. At 360 × 740 the section of a new Paging Route fits without scrolling sideways.
  const phone = await adminPhone(browser);
  const csp = watchCsp(phone);
  await phone.goto("/routes");
  await phone.getByRole("link", { name: "Create route" }).click();
  await phone.getByRole("button", { name: "Paging" }).click();
  const section = phone.getByRole("group", { name: "Ack timeout and Reminders" });
  await expect(section).toBeVisible();
  await expect(section.getByRole("switch", { name: "Ack timeout", exact: true })).toBeChecked();
  await expect(section.getByTestId("reminder-times")).toBeVisible();
  await section.scrollIntoViewIfNeeded();
  await expectInside(phone, "route-timers");
  await expectNoHorizontalScroll(phone);
  await shot(phone, "route-timers-paging-360");
  expect(csp).toEqual([]);
  await phone.context().close();
});

test("the next notice, Unclaimed with its filter, the next Reminder and Still on it", async ({
  page,
  browser,
}) => {
  test.setTimeout(300_000);
  const csp = watchCsp(page);
  const adminZone = await zoneOf(tokenA);
  const bobZone = await zoneOf(tokenB);

  // 2. tm-g1 fires: the page shows the first ack timeout notice 15 minutes ahead.
  g1 = await fire("tm-g1", "DiskFull", "tm");
  const first = await nextNotice(g1.id, "ack_timeout");
  await expectAhead(first, 900);
  await settle();
  await signInAdmin(page);
  await page.goto(`/alert-groups/${g1.id}`);
  await expect(page.getByTestId("alert-group-next-notice")).toHaveText(
    `Next ack timeout notice at ${fmt(first, adminZone)}`,
  );
  await expect(page.getByTestId("unclaimed-badge")).toHaveCount(0);
  await shot(page, "timers-next-notice");

  // 3. The three notices: without a reload the badge, the banner and the Timeline entries appear.
  await adv(900, 1800, 3600);
  await expect(page.getByTestId("alert-group-header").getByTestId("unclaimed-badge")).toHaveText(
    "Unclaimed",
  );
  await expect(page.getByTestId("notice-unclaimed")).toHaveText("⚠ Nobody has taken this");
  await expect(page.getByTestId("alert-group-next-notice")).toHaveCount(0);
  for (const n of [1, 2, 3]) {
    const notice = entry(page, `Ack timeout notice ${n} of 3`);
    await expect(notice.getByTestId("timeline-loud")).toHaveText("Loud");
    await expect(notice.getByTestId("timeline-mentions")).toHaveText("Mentions: ack_timeout");
  }
  const unclaimed = entry(page, "Unclaimed");
  await expect(unclaimed.getByTestId("timeline-quiet")).toHaveText("Quiet");
  await expect(unclaimed.getByTestId("timeline-loud")).toHaveCount(0);
  await shot(page, "timers-unclaimed");

  // 9. At 360 × 740 the header with the badge and the banner fits.
  const phone = await adminPhone(browser);
  const phoneCsp = watchCsp(phone);
  await phone.goto(`/alert-groups/${g1.id}`);
  await expect(phone.getByTestId("unclaimed-badge")).toBeVisible();
  await expect(phone.getByTestId("notice-unclaimed")).toBeVisible();
  await expectInside(phone, "unclaimed-badge");
  await expectNoHorizontalScroll(phone);
  await shot(phone, "timers-unclaimed-360");
  await phone.goto("/alert-groups?unclaimed=true");
  const phoneRow = phone
    .getByTestId("alert-group-row")
    .filter({ has: phone.getByText(`#${g1.number}`, { exact: true }) });
  await expect(phoneRow.getByTestId("unclaimed-badge")).toBeVisible();
  await expectNoHorizontalScroll(phone);
  await shot(phone, "timers-list-unclaimed-360");
  expect(phoneCsp).toEqual([]);
  await phone.context().close();

  // 4. The list: "Unclaimed" is kept in the URL and the row of tm-g1 shows the badge.
  await page.goto("/alert-groups");
  await page.getByTestId("filter-unclaimed").click();
  await expect(page).toHaveURL(/unclaimed=true/);
  await expect(page.getByTestId("filter-unclaimed")).toHaveAttribute("aria-pressed", "true");
  const row = page
    .getByRole("table")
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: `#${g1.number}`, exact: true }) });
  await expect(row.getByTestId("unclaimed-badge")).toHaveText("Unclaimed");
  await expect(page.getByRole("columnheader", { name: "Unclaimed" })).toBeVisible();
  await shot(page, "timers-list-unclaimed");

  // 5. tm-bob acknowledges: the badge and the banner go on both pages without a reload; the next Reminder shows.
  await page.goto(`/alert-groups/${g1.id}`);
  await expect(page.getByTestId("unclaimed-badge")).toBeVisible();
  const bob = await bobPage(browser);
  const bobCsp = watchCsp(bob);
  await bob.goto(`/alert-groups/${g1.id}`);
  await bob.getByRole("button", { name: "Acknowledge", exact: true }).click();
  await expect(bob.getByTestId("alert-group-owner")).toHaveText(`Owner: ${BOB}`);
  for (const p of [bob, page]) {
    await expect(p.getByTestId("unclaimed-badge")).toHaveCount(0);
    await expect(p.getByTestId("notice-unclaimed")).toHaveCount(0);
  }
  const reminder = await nextNotice(g1.id, "reminder");
  await expectAhead(reminder, 4 * HOUR);
  await expect(bob.getByTestId("alert-group-next-notice")).toHaveText(
    `Next Reminder at ${fmt(reminder, bobZone)}`,
  );
  await expect(page.getByTestId("alert-group-next-notice")).toHaveText(
    `Next Reminder at ${fmt(reminder, adminZone)}`,
  );
  await settle();

  // 6. The first Reminder: tm-bob sees "Still on it"; 7. the Admin does not.
  await adv(4 * HOUR);
  const still = bob.getByRole("button", { name: "Still on it", exact: true });
  await expect(still).toBeVisible();
  await expect(entry(bob, "Reminder 1").getByTestId("timeline-mentions")).toHaveText(
    "Mentions: owner",
  );
  await page.reload();
  await expect(entry(page, "Reminder 1")).toBeVisible();
  await expect(page.getByTestId("command-still_on_it")).toHaveCount(0);

  // 9. At 360 × 740 the header with the next Reminder and "Still on it" fits.
  const bobPhone = await bobPage(browser, true);
  const bobPhoneCsp = watchCsp(bobPhone);
  await bobPhone.goto(`/alert-groups/${g1.id}`);
  await expect(bobPhone.getByRole("button", { name: "Still on it", exact: true })).toBeVisible();
  await expect(bobPhone.getByTestId("alert-group-next-notice")).toBeVisible();
  await expectInside(bobPhone, "command-buttons");
  await expectInside(bobPhone, "alert-group-next-notice");
  await expectNoHorizontalScroll(bobPhone);
  await shot(bobPhone, "timers-still-on-it-360");
  expect(bobPhoneCsp).toEqual([]);
  await bobPhone.context().close();

  // "Still on it" goes in the Timeline with tm-bob, the button goes, and the next Reminder stays 8 hours after the
  // first one.
  await shot(bob, "timers-still-on-it");
  await still.click();
  const answered = entry(bob, "Still on it");
  await expect(answered.getByTestId("timeline-actor")).toHaveText(`${BOB} via the UI`);
  await expect(answered.getByTestId("timeline-quiet")).toHaveText("Quiet");
  await expect(still).toHaveCount(0);
  const second = await nextNotice(g1.id, "reminder");
  await expectAhead(second, 8 * HOUR);
  await expect(bob.getByTestId("alert-group-next-notice")).toHaveText(
    `Next Reminder at ${fmt(second, bobZone)}`,
  );
  await expect(entry(page, "Still on it")).toBeVisible();
  await expect(page.getByTestId("command-still_on_it")).toHaveCount(0);
  await settle();
  expect(bobCsp).toEqual([]);
  await bob.context().close();
  expect(csp).toEqual([]);
});

test("two unanswered Reminders unacknowledge automatically", async ({ page, browser }) => {
  test.setTimeout(300_000);
  const csp = watchCsp(page);

  // 8. The Route "tm-au" with auto-unacknowledge; tm-g2 acknowledged by tm-bob.
  const profiles = await pat<{ items: { id: string; policy: Record<string, unknown> }[] }>(
    tokenA,
    "GET",
    "/api/v1/route-profiles",
  );
  const policy = profiles.items.find((p) => p.id === "paging")?.policy;
  await pat(tokenA, "POST", "/api/v1/routes", {
    name: "tm-au",
    matchers: [{ label: "team", op: "=", value: "tm-au" }],
    group_key: ["alertname", "cluster"],
    destination_ids: [destinationId],
    policy: { ...policy, auto_unacknowledge: true },
  });
  const g2 = await fire("tm-g2", "TmDisk", "tm-au");
  await nextNotice(g2.id, "ack_timeout");
  await settle();
  await pat(tokenB, "POST", `/api/v1/alert-groups/${g2.id}/acknowledge`);
  await settle();
  expect((await readGroup(g2.id)).owner?.name).toBe(BOB);

  // Both are signed in before the advance.
  await signInAdmin(page);
  await page.goto(`/alert-groups/${g2.id}`);
  await expect(page.getByTestId("alert-group-owner")).toHaveText(`Owner: ${BOB}`);
  const bob = await bobPage(browser);
  const bobCsp = watchCsp(bob);
  await bob.goto(`/alert-groups/${g2.id}`);
  await expect(bob.getByTestId("alert-group-owner")).toHaveText(`Owner: ${BOB}`);

  // Reminders at hours 4 and 12 go unanswered; the third, due at hour 28, gives the Alert Group back instead.
  await adv(4 * HOUR, 8 * HOUR, 16 * HOUR);
  await expect.poll(async () => (await readGroup(g2.id)).status).toBe("firing");

  // The advance outlasted auth.session_idle_timeout: both sign in again.
  for (const p of [page, bob]) {
    await p.goto(`/alert-groups/${g2.id}`);
    await expect(p).toHaveURL(/\/sign-in/);
  }
  await signInAdmin(page);
  await signIn(bob, BOB, BOB_PASSWORD);
  for (const p of [page, bob]) {
    await p.goto(`/alert-groups/${g2.id}`);
    await expect(p.getByTestId("alert-group-status")).toHaveText("Firing");
    await expect(p.getByTestId("alert-group-owner")).toHaveCount(0);
    const released = entry(p, "Unacknowledged automatically: No answer to the last two Reminders");
    await expect(released.getByTestId("timeline-loud")).toHaveText("Loud");
    await expect(released.getByTestId("timeline-mentions")).toHaveText("Mentions: owner");
    await expect(released).toContainText("Previous owner:");
    await expect(released).toContainText(BOB);
    await expect(p.getByTestId("command-still_on_it")).toHaveCount(0);
  }
  await expect(entry(page, "Reminder 2")).toBeVisible();
  await shot(page, "timers-auto-unacknowledged");
  expect(bobCsp).toEqual([]);
  await bob.context().close();
  expect(csp).toEqual([]);
});
