// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Outgoing heartbeat against `muster dev` and its fake webhook receiver: Verification steps 4, 5 and 7 of
// its story. The suggestion on the System status leads to the page, the URL is set through the write-only field and
// shows "Set, changed {time}"; once the development clock makes the heartbeat due, the fake receives it, the page shows
// "Last result: OK" and the System status "Last sent {time}: OK"; a 503 of the receiver reads "a temporary error
// (answered 503)" with the server's text as its detail, until the next heartbeat. A change made elsewhere shows the
// conflict message
// until "Reload"; the page fits 360 px with the proxy form open; "Clear" brings the suggestion back. No Content Security
// Policy violation.

import { expect, type Page, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  advance,
  expectNoHorizontalScroll,
  shot,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const FWH = "http://127.0.0.1:18093";
const PING = `${FWH}/hook/dev/ping`;
const OTHER = `${FWH}/hook/dev/other`;
const TIME_ZONE = "Europe/Berlin";
const SUGGESTION = "Set up an outgoing heartbeat so that a stopped Muster is noticed.";
const HINT =
  "Muster calls this URL every minute while a replica leads. Use a dead man's switch that alerts when the calls stop.";
const STALE = "Someone else changed these settings. Reload to see them.";

interface Organization {
  outgoing_heartbeat: { url_status: { set: boolean; updated_at?: string | null } };
}

interface HeartbeatResult {
  configured: boolean;
  last_attempt_at?: string | null;
  last_ok?: boolean | null;
}

/** A time as the pages show it to the development Admin: date and time in the browser's time zone. */
function fmt(iso: string): string {
  return new Intl.DateTimeFormat("en-US", {
    dateStyle: "medium",
    timeStyle: "short",
    hourCycle: "h23",
    timeZone: TIME_ZONE,
  }).format(new Date(iso));
}

/** Sets or clears the outgoing heartbeat URL, keeping every other field of the Organization as read. */
async function heartbeatUrl(admin: Api, url: string | null): Promise<void> {
  const res = await admin.context.get("/api/v1/organization");
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the Organization as the API reads it
  const org = (await res.json()) as Record<string, unknown> & {
    outgoing_heartbeat: { proxy: Record<string, unknown> };
  };
  const { id: _id, etag: _etag, outgoing_heartbeat: heartbeat, ...rest } = org;
  const { password_status: _password, ...proxy } = heartbeat.proxy;
  await admin.call(
    "PUT",
    "/api/v1/organization",
    { ...rest, outgoing_heartbeat: { url, proxy } },
    { "If-Match": res.headers().etag ?? "" },
  );
}

async function withAdmin<T>(f: (admin: Api) => Promise<T>): Promise<T> {
  const admin = await adminApi();
  try {
    return await f(admin);
  } finally {
    await admin.dispose();
  }
}

function organization(): Promise<Organization> {
  return withAdmin((admin) => admin.call<Organization>("GET", "/api/v1/organization"));
}

function lastResult(): Promise<HeartbeatResult> {
  return withAdmin(
    async (admin) =>
      (await admin.call<{ outgoing_heartbeat: HeartbeatResult }>("GET", "/api/v1/system-status"))
        .outgoing_heartbeat,
  );
}

/** The paths the fake receiver got under /hook/dev. */
async function received(): Promise<string[]> {
  const res = await fetch(`${FWH}/_fake/received/dev`);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own list of requests
  return ((await res.json()) as { path: string }[]).map((r) => r.path);
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

/** An entry of the sub-navigation of Organization. */
function sub(page: Page, name: string) {
  return page
    .getByRole("navigation", { name: "Organization" })
    .getByRole("link", { name, exact: true });
}

function urlStatus(page: Page) {
  return page.getByTestId("secret-outgoing-heartbeat-url-status");
}

test.beforeAll(async () => {
  await withAdmin((admin) => heartbeatUrl(admin, null));
});

test.afterAll(async () => {
  await withAdmin((admin) => heartbeatUrl(admin, null));
});

test("sets the outgoing heartbeat URL from the suggestion and shows its result", async ({
  page,
}) => {
  test.setTimeout(150_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 4. The suggestion → Organization → Outgoing heartbeat → "Set a value" → the URL → "Save" → "Set, changed {time}".
  await nav(page, "Organization").click();
  await sub(page, "System status").click();
  await page.getByRole("link", { name: SUGGESTION }).click();
  await expect(page).toHaveURL(/\/admin\/organization\/outgoing-heartbeat$/);
  await expect(page.getByRole("heading", { name: "Outgoing heartbeat", level: 1 })).toBeVisible();
  await expect(urlStatus(page)).toHaveText("Not set");
  await expect(page.getByText(HINT)).toBeVisible();
  await expect(page.getByTestId("outgoing-heartbeat-last-result")).toHaveText(
    "Last result: nothing is sent without a URL",
  );
  await page.getByRole("button", { name: "Set a value", exact: true }).click();
  await expect(page.getByLabel("Heartbeat URL")).toHaveAttribute("type", "password");
  await expect(page.getByLabel("Heartbeat URL")).toHaveAccessibleDescription(
    /Muster calls this URL every minute while a replica leads\. Use a dead man's switch/,
  );
  await page.getByLabel("Heartbeat URL").fill(PING);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("outgoing-heartbeat-status")).toHaveText("Saved.");
  const changed = (await organization()).outgoing_heartbeat.url_status.updated_at;
  expect(changed, "the time the URL changed").toBeTruthy();
  await expect(urlStatus(page)).toHaveText(`Set, changed ${fmt(changed ?? "")}`);
  await shot(page, "outgoing-heartbeat-set");

  // 5. The clock makes the heartbeat due → the fake receives it, "Last result: OK", and "Last sent {time}: OK".
  await advance(60);
  await expect(page.getByTestId("outgoing-heartbeat-last-result")).toHaveText(
    /^Last result: OK, sent /,
    {
      timeout: 30_000,
    },
  );
  await expect.poll(received).toContain("/hook/dev/ping");
  await sub(page, "System status").click();
  const sent = page.getByTestId("system-status-heartbeat");
  await expect
    .poll(async () => {
      const result = await lastResult();
      const shown = await sent.textContent();
      return result.last_ok === true && result.last_attempt_at
        ? shown === `Last sent ${fmt(result.last_attempt_at)}: OK`
        : false;
    })
    .toBe(true);
  await expect(page.getByRole("link", { name: SUGGESTION })).toHaveCount(0);
  await shot(page, "system-status-heartbeat-ok");

  // A failed heartbeat reads by its class, with the server's text as its detail; the next one recovers.
  const faults = `${FWH}/_fake/faults`;
  const set = await fetch(faults, {
    method: "POST",
    body: JSON.stringify({ path: "/hook/dev/ping", status: 503 }),
  });
  expect(set.ok, `POST /_fake/faults: ${set.status}`).toBe(true);
  try {
    await advance(60);
    await expect(sent).toHaveText(/^Last sent .+: a temporary error \(answered 503\)$/, {
      timeout: 30_000,
    });
    await expect(page.getByTestId("system-status-heartbeat-detail")).toHaveText(
      "Details: answered 503 (transient)",
    );
    await shot(page, "system-status-heartbeat-failed");
  } finally {
    await fetch(faults, { method: "DELETE" });
  }
  await advance(60);
  await expect(sent).toHaveText(/: OK$/, { timeout: 30_000 });
  await expect(page.getByTestId("system-status-heartbeat-detail")).toHaveCount(0);

  // A change made elsewhere while the form is being edited shows the conflict message until "Reload".
  await sub(page, "Outgoing heartbeat").click();
  await expect(urlStatus(page)).toHaveText(/^Set, changed /);
  await page.getByRole("button", { name: "Replace", exact: true }).click();
  await page.getByLabel("Heartbeat URL").fill(`${PING}?unsaved`);
  await withAdmin((admin) => heartbeatUrl(admin, OTHER));
  await expect(page.getByText(STALE)).toBeVisible();
  await page.getByRole("button", { name: "Reload", exact: true }).click();
  await expect(page.getByText(STALE)).toHaveCount(0);
  const reloaded = (await organization()).outgoing_heartbeat.url_status.updated_at;
  await expect(urlStatus(page)).toHaveText(`Set, changed ${fmt(reloaded ?? "")}`);
  expect(csp).toEqual([]);
});

test("fits 360 px with the proxy form open, and Clear brings the suggestion back", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await page.setViewportSize({ width: 360, height: 740 });
  await signInAdmin(page);
  await page.goto("/admin/organization/outgoing-heartbeat");
  await expect(page.getByRole("heading", { name: "Outgoing heartbeat", level: 1 })).toBeVisible();
  await expect(urlStatus(page)).toHaveText(/^Set, changed /);

  // 7. At 360 × 740, with the URL field and the proxy form open, the page scrolls only vertically.
  await page.getByRole("button", { name: "Replace", exact: true }).click();
  await page.getByLabel("Use a proxy").check();
  await expect(page.getByLabel("Address")).toBeVisible();
  await expectNoHorizontalScroll(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(360);
  await shot(page, "outgoing-heartbeat-phone");

  // "Clear" → "Save": the URL is not set, and the System status suggests one again.
  await page.getByLabel("Use a proxy").uncheck();
  await page.getByRole("button", { name: "Keep the current one", exact: true }).click();
  await page.getByRole("button", { name: "Clear", exact: true }).click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("outgoing-heartbeat-status")).toHaveText("Saved.");
  await expect(urlStatus(page)).toHaveText("Not set");
  expect((await organization()).outgoing_heartbeat.url_status.set).toBe(false);
  await page.goto("/admin/system-status");
  await expect(page.getByRole("link", { name: SUGGESTION })).toBeVisible();
  expect(csp).toEqual([]);
});
