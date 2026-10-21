// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Organization settings" (docs/configure/organization.md), the System status of "Monitoring" and
// "Where to find it" of "Request log". The Admin saves a change on each page of the Organization — General, Severity
// levels, Alert handling, Security, Outbound addresses, Retention — after the spec set the outgoing heartbeat to the fake
// webhook receiver through the API, so that the System status shows it sent; then reads the Audit log of those changes, the System status, and
// the Request log of the Mattermost Connection "Mattermost" on https://chat.example.org, which the spec creates through
// the API and checks. Outbound addresses keep 127.0.0.0/8, so the fakes stay reachable. In afterAll, a session signed
// in before the changes puts every value back to what it read first, with the ETag it reads just before, and the spec
// deletes the Connection.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
  type Api,
  CHAT_URL,
  checkConnection,
  MM_SECOND_TOKEN,
  removeConnection,
  signInAdmin,
} from "../support";
import { capture, cardOf, nav, organizationNav, scrollToTop } from "./capture";

const ORGANIZATION = "configure/organization";
const MONITORING = "operations/monitoring";
const REQUEST_LOG = "operations/request-log";
const CONNECTION = "Mattermost";
const LOOPBACK = "127.0.0.0/8";

test.describe.configure({ mode: "serial" });

interface Organization extends Record<string, unknown> {
  outgoing_heartbeat: { url_status: { set: boolean }; proxy: Record<string, unknown> };
}

interface OutboundPolicy extends Record<string, unknown> {
  allowed: string[];
}

interface SystemStatus {
  commit?: string;
  replicas: { id: string; key_ids: string[] }[];
}

/** A session signed in before the changes, and what the Organization and its outbound policy were. */
let keeper: Api | undefined;
let organization: Organization | undefined;
let outbound: OutboundPolicy | undefined;

/** Reads a resource with its ETag. */
// oxlint-disable-next-line typescript/no-unnecessary-type-parameters -- the caller names the type of the body
async function read<T>(admin: Api, path: string): Promise<{ body: T; etag: string }> {
  const res = await admin.context.get(path);
  expect(res.ok(), `GET ${path}`).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the resource as the API reads it
  return { body: (await res.json()) as T, etag: res.headers().etag ?? "" };
}

/**
 * Sets the outgoing heartbeat URL, every other field of the Organization as read. Through the API, as the page of the
 * outgoing heartbeat would save a proxy type that the API cannot remove again.
 */
async function heartbeatUrl(admin: Api, url: string): Promise<void> {
  const { body, etag } = await read<Organization>(admin, "/api/v1/organization");
  const { id: _id, etag: _etag, outgoing_heartbeat: heartbeat, ...rest } = body;
  const { password_status: _password, ...proxy } = heartbeat.proxy;
  await admin.call(
    "PUT",
    "/api/v1/organization",
    { ...rest, outgoing_heartbeat: { url, proxy } },
    { "If-Match": etag },
  );
}

/**
 * Puts the Organization and its outbound policy back to what they were, each with the ETag read just before, and
 * deletes the Connection. Each step runs whatever the others do; the first failure is thrown at the end.
 */
async function restore(admin: Api): Promise<void> {
  const steps: (() => Promise<void>)[] = [
    async () => {
      if (organization === undefined) {
        return;
      }
      const { etag } = await read<Organization>(admin, "/api/v1/organization");
      const { id: _id, etag: _etag, outgoing_heartbeat: heartbeat, ...rest } = organization;
      const { password_status: _password, ...proxy } = heartbeat.proxy;
      await admin.call(
        "PUT",
        "/api/v1/organization",
        { ...rest, outgoing_heartbeat: { url: null, proxy } },
        { "If-Match": etag },
      );
    },
    async () => {
      if (outbound === undefined) {
        return;
      }
      const { etag } = await read<OutboundPolicy>(admin, "/api/v1/organization/outbound-policy");
      const { etag: _etag, always_blocked: _blocked, ...policy } = outbound;
      await admin.call("PUT", "/api/v1/organization/outbound-policy", policy, { "If-Match": etag });
    },
    async () => {
      await removeConnection(admin, CONNECTION);
    },
  ];
  const errors: unknown[] = [];
  for (const step of steps) {
    try {
      await step();
    } catch (error) {
      errors.push(error);
    }
  }
  if (errors.length > 0) {
    throw errors[0];
  }
}

async function save(page: Page, status: string): Promise<void> {
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId(status)).toHaveText("Saved.");
}

test.beforeAll(async () => {
  keeper = await adminApi();
  organization = (await read<Organization>(keeper, "/api/v1/organization")).body;
  outbound = (await read<OutboundPolicy>(keeper, "/api/v1/organization/outbound-policy")).body;
  // The outgoing heartbeat URL is write-only: the spec can put it back only to unset.
  expect(organization.outgoing_heartbeat.url_status.set).toBe(false);
  expect(outbound.allowed).toContain(LOOPBACK);
  await removeConnection(keeper, CONNECTION);
  const created = await keeper.call<{ id: string }>("POST", "/api/v1/connections", {
    type: "mattermost",
    name: CONNECTION,
    server_url: CHAT_URL,
    bot_token: MM_SECOND_TOKEN,
    proxy: { enabled: false },
    limiter: { limit: 50, per_seconds: 1 },
  });
  await checkConnection(keeper, created.id);
});

test.afterAll(async () => {
  if (keeper === undefined) {
    return;
  }
  try {
    await restore(keeper);
  } finally {
    await keeper.dispose();
  }
});

test("saves each page of the Organization, reads the Audit log, the System status and the Request log", async ({
  page,
}) => {
  test.setTimeout(240_000);
  // The outgoing heartbeat to the fake webhook receiver, which the System status shows sent; set first, so that the
  // newest entries of the Audit log are the changes made on the pages.
  expect(keeper, "the session of the spec").toBeDefined();
  if (keeper !== undefined) {
    await heartbeatUrl(keeper, "http://127.0.0.1:18093/hook/dead-mans-switch/ping");
  }
  await signInAdmin(page);
  await nav(page, "Organization").click();

  // General: the time zone of messages and the start of the working day.
  await organizationNav(page, "General").click();
  await page.getByLabel("Search time zones").fill("tokyo");
  await page.getByLabel("Time zone of messages").selectOption("Asia/Tokyo");
  await page.getByLabel("Start of the working day").fill("08:30");
  await save(page, "general-status");
  await capture(page, ORGANIZATION, "01-general", { waitFor: page.getByTestId("general-status") });

  // Severity levels: the value P1 mapped to critical.
  await organizationNav(page, "Severity levels").click();
  await page.getByRole("button", { name: "Add value" }).click();
  const value = page.getByRole("textbox", { name: /^Value \d+$/ }).last();
  await value.fill("P1");
  await page.getByLabel("Level of P1", { exact: true }).selectOption("critical");
  await save(page, "severity-levels-status");
  const mapping = cardOf(page.getByText("Mapping", { exact: true }).first());
  await capture(page, ORGANIZATION, "02-severity-levels", { locator: mapping });

  // Alert handling: the Instance label node added.
  await organizationNav(page, "Alert handling").click();
  await page.getByRole("button", { name: "Add label" }).click();
  await page
    .getByRole("textbox", { name: /^Instance labels, entry \d+$/ })
    .last()
    .fill("node");
  await save(page, "alert-handling-status");
  await capture(page, ORGANIZATION, "03-alert-handling", {
    waitFor: page.getByTestId("alert-handling-status"),
  });

  // Security: the token grace of 14 days.
  await organizationNav(page, "Security").click();
  await page.getByLabel("Token grace for OIDC users without an offline token").fill("14");
  await save(page, "security-status");
  await capture(page, ORGANIZATION, "04-security", {
    waitFor: page.getByTestId("security-status"),
  });

  // Outbound addresses: an allowed network beside 127.0.0.0/8.
  await organizationNav(page, "Outbound addresses").click();
  await expect(page.getByRole("textbox", { name: /^Allowed, entry \d+$/ }).first()).toHaveValue(
    LOOPBACK,
  );
  await page.getByRole("button", { name: "Add allowed entry" }).click();
  await page
    .getByRole("textbox", { name: /^Allowed, entry \d+$/ })
    .last()
    .fill("10.20.0.0/16");
  await save(page, "outbound-status");
  await page.evaluate(() => window.scrollTo(0, 0));
  await capture(page, ORGANIZATION, "05-outbound-addresses", {
    waitFor: page.getByTestId("outbound-status"),
  });

  // Retention: Stored Snapshots for 30 days.
  await organizationNav(page, "Retention").click();
  await page.getByLabel("Stored Snapshots (days)").fill("30");
  await save(page, "retention-status");
  await capture(page, ORGANIZATION, "06-retention", {
    waitFor: page.getByTestId("retention-status"),
  });

  // Audit log: the newest entries, those about the Organization, and the changes of one entry. The images end at the
  // rows of this spec's own changes: the older rows, of other specs of the run, show times that vary from run to run.
  await organizationNav(page, "Audit log").click();
  const table = page.getByRole("table", { name: "Audit log" });
  const rows = table.getByRole("row");
  await expect(rows.nth(1)).toContainText("organization.updated");
  // Only the rows the image shows lay out the columns: older rows, of other specs of the run, would widen them.
  const older = table.locator("tbody tr:nth-child(n+5)");
  await capture(page, ORGANIZATION, "07-audit-log", { bottom: rows.nth(4), hide: older });

  await page.getByLabel("Resource type", { exact: true }).fill("organization");
  await page.getByLabel("Resource type", { exact: true }).press("Enter");
  await expect(page).toHaveURL(/resource_type=organization/);
  // The filtered list has replaced the whole one before rows are hidden for the shot.
  await expect(table.getByText("outbound_policy.updated", { exact: true })).toHaveCount(0);
  await expect(rows.nth(4)).toContainText("organization.updated");
  await capture(page, ORGANIZATION, "08-audit-log-filtered", { bottom: rows.nth(4), hide: older });
  const general = rows.filter({ hasText: "Asia/Tokyo" }).first();
  await scrollToTop(general);
  await capture(page, ORGANIZATION, "09-audit-log-entry", {
    locator: general,
    hide: table.locator("tbody tr").filter({ hasNotText: "Asia/Tokyo" }),
  });

  // System status, with the replica's id, its keys and the commit that change from run to run fixed.
  const admin = keeper ?? (await adminApi());
  const status = await admin.call<SystemStatus>("GET", "/api/v1/system-status");
  const replace: [string, string][] = [];
  for (const [i, r] of status.replicas.entries()) {
    replace.push([r.id, `muster-${i}-k7m3qx9p`]);
    for (const [j, key] of r.key_ids.entries()) {
      replace.push([key, ["k-3f9c2a7d1e4b5c60", "k-8d2e4f6a1b3c5d70"][j] ?? key]);
    }
  }
  if (status.commit !== undefined) {
    replace.push([status.commit, "3f9c2a7d1e4b"]);
  }
  await organizationNav(page, "System status").click();
  await expect(page.getByTestId("replica")).toHaveCount(status.replicas.length);
  await expect(page.getByTestId("system-status-heartbeat")).toContainText("OK", {
    timeout: 30_000,
  });
  await capture(page, MONITORING, "01-system-status", {
    waitFor: page.getByTestId("replica"),
    replace,
  });
  // From the section after the Replicas, which the first image shows, down to the outgoing heartbeat at the end.
  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
  await expect(page.getByTestId("system-status-heartbeat")).toBeInViewport({ ratio: 1 });
  await capture(page, MONITORING, "02-system-status-more", {
    top: page.getByRole("region", { name: "Recovery after downtime" }),
    waitFor: page.getByTestId("system-status-heartbeat"),
    replace,
  });

  // Request log: the Connection's Request log tab and one record.
  await nav(page, "Connections").click();
  await page.getByRole("link", { name: CONNECTION, exact: true }).click();
  await expect(page.getByRole("heading", { name: CONNECTION, level: 1 })).toBeVisible();
  await page
    .getByRole("tablist", { name: "Sections of the page" })
    .getByRole("tab", { name: "Request log" })
    .click();
  const log = page.getByRole("table", { name: "Request log" });
  await expect(log.getByTestId("request-operation").first()).toBeVisible();
  await capture(page, REQUEST_LOG, "01-connection-request-log", { waitFor: log });
  await log.getByRole("button", { name: "getMe", exact: true }).first().click();
  const record = page.getByRole("dialog", { name: "Sent getMe" });
  await expect(
    record.getByRole("region", { name: "Answer" }).getByTestId("record-status"),
  ).toHaveText("200");
  // The fake Mattermost server answers for https://chat.example.org, the Connection's address, which the image shows.
  await capture(page, REQUEST_LOG, "02-record", {
    locator: record,
    replace: [
      ["http://127.0.0.1:18065", CHAT_URL],
      ["127.0.0.1:18065", new URL(CHAT_URL).host],
    ],
  });
});
