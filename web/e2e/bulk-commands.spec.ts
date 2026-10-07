// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Bulk commands from the Alert Group list against `muster dev`: Verification step 6 of their story. The fake
// Alertmanager sends the group bulk to the Integration "cmd-bulk" on the Route "bulk", three firing Alert Groups, one
// of them acknowledged by the Admin. Bob selects all three and acknowledges them: two are done and the Admin's is
// skipped. Then a Snooze of the selection offers no Route durations, an Unsnooze and a Resolve with a Note run on all
// three, and a second Resolve lists each as already resolved.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
  createRoute,
  deleteRoute,
  devClockOffset,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  shot,
  signIn,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

// Bob of the story; profile.spec.ts creates a "bob" of its own in the same run.
const BOB = "cmd-bob";
const BOB_PASSWORD = "cmd-bob-password-1";

let integrationId = "";
let routeId = "";
const groups: { id: string; number: number }[] = [];

/** Creates a local user with a password, unless a spec before this one did. */
async function ensureUser(login: string, role: string, password: string): Promise<void> {
  const admin = await adminApi();
  try {
    const directory = await admin.call<{ items: { login?: string }[] }>(
      "GET",
      `/api/v1/user-directory?q=${encodeURIComponent(login)}`,
    );
    const found = directory.items.some((u) => u.login === login);
    if (!found) {
      const { token } = await admin.createUser(login, role);
      await admin.call("POST", "/api/v1/password-setups", { token, password });
    }
  } finally {
    await admin.dispose();
  }
}

async function groupOf(cluster: string): Promise<{ id: string; number: number }> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { alert_group?: { id: string; number: number } }[] }>(
      "GET",
      `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent(`cluster="${cluster}"`)}`,
    );
    const group = page.items[0]?.alert_group;
    expect(group, `the alert group of ${cluster}`).toBeDefined();
    return group ?? { id: "", number: 0 };
  } finally {
    await admin.dispose();
  }
}

/** The list of the Route "bulk" in a tab. */
async function openList(page: Page, tab?: string): Promise<void> {
  const route = encodeURIComponent(JSON.stringify([routeId]));
  await page.goto(`/alert-groups?route=${route}${tab === undefined ? "" : `&tab=${tab}`}`);
  await expect(page.getByRole("table", { name: "Alert Groups" }).getByRole("row")).toHaveCount(4);
}

/** Selects every shown row and presses a bulk command. */
async function runOnAll(page: Page, command: string): Promise<void> {
  const bar = page.getByTestId("bulk-bar");
  await bar.getByRole("button", { name: "Select all shown", exact: true }).click();
  await expect(page.getByTestId("bulk-count")).toHaveText("3 selected");
  await bar.getByRole("button", { name: command, exact: true }).click();
}

/** The lines of a bulk result, in any order: the rows of one start keep no fixed order. */
async function resultLines(page: Page, command: string): Promise<string[]> {
  const items = page
    .getByRole("dialog", { name: `Result: ${command}` })
    .getByTestId("bulk-result-item");
  await expect(items).toHaveCount(3);
  return (await items.allTextContents()).toSorted();
}

const sorted = (lines: string[]) => lines.toSorted();

test.beforeAll(async () => {
  await ensureUser(BOB, "responder", BOB_PASSWORD);
  integrationId = await fakeIntegration("cmd-bulk");
  routeId = await createRoute("bulk", "cmd-bulk", ["alertname", "cluster"]);
  await fam("PUT", "/groups/bulk", {
    receiver: "cmd-bulk",
    route: "{}",
    labels: { alertname: "Bulk" },
  });
  for (const c of ["b1", "b2", "b3"]) {
    await fam("PUT", `/groups/bulk/alerts/${c}`, { labels: { team: "cmd-bulk", cluster: c } });
  }
  await notify(integrationId, "bulk", { reason: "first notification" });
  for (const c of ["b1", "b2", "b3"]) {
    groups.push(await groupOf(c));
  }
  const admin = await adminApi();
  await admin.call("POST", `/api/v1/alert-groups/${groups[2]?.id}/acknowledge`);
  await admin.dispose();
});

test.afterAll(async () => {
  if (routeId !== "") {
    await deleteRoute(routeId);
  }
});

test("a bulk Acknowledge skips the Alert Group another user owns", async ({ page }) => {
  const csp = watchCsp(page);
  const [b1, b2, b3] = groups;
  await signIn(page, BOB, BOB_PASSWORD);
  await openList(page);

  // 6. "Select", three rows, "Acknowledge": two done, the Admin's skipped.
  await page.getByRole("button", { name: "Select", exact: true }).click();
  const bar = page.getByTestId("bulk-bar");
  await expect(page.getByTestId("bulk-count")).toHaveText("0 selected");
  await expect(bar.getByRole("button", { name: "Acknowledge", exact: true })).toBeDisabled();
  for (const g of [b1, b2, b3]) {
    await page.getByRole("checkbox", { name: `Select #${g?.number}`, exact: true }).check();
  }
  await expect(page.getByTestId("bulk-count")).toHaveText("3 selected");
  await shot(page, "bulk-selection");
  await bar.getByRole("button", { name: "Acknowledge", exact: true }).click();
  const result = page.getByRole("dialog", { name: "Result: Acknowledge" });
  expect(await resultLines(page, "Acknowledge")).toEqual(
    sorted([
      `#${b1?.number}: done`,
      `#${b2?.number}: done`,
      `#${b3?.number}: skipped: owned by admin`,
    ]),
  );
  await expect(result.getByText("Done for 2 of 3 Alert Groups.")).toBeVisible();
  await shot(page, "bulk-result");
  await result.getByRole("button", { name: "Close", exact: true }).last().click();
  await expect(result).toHaveCount(0);
  await expect(page.getByTestId("bulk-count")).toHaveText("0 selected");
  const table = page.getByRole("table", { name: "Alert Groups" });
  await expect(table.getByTestId("owner")).toHaveCount(3);
  await expect
    .poll(async () => sorted(await table.getByTestId("owner").allTextContents()))
    .toEqual(["admin", "cmd-bob", "cmd-bob"]);

  // No Takeover was recorded for the Admin's Alert Group.
  const admin = await adminApi();
  const timeline = await admin.call<{ items: { event?: string }[] }>(
    "GET",
    `/api/v1/alert-groups/${b3?.id}/timeline`,
  );
  await admin.dispose();
  expect(timeline.items.some((e) => e.event === "takeover")).toBe(false);
  expect(csp).toEqual([]);
});

test("a bulk Snooze, Unsnooze and Resolve, then a Resolve of resolved ones", async ({ page }) => {
  const csp = watchCsp(page);
  await signIn(page, BOB, BOB_PASSWORD);
  // The default end of "Until" counts from the browser's clock; it follows the business clock, as outside development.
  await page.clock.setSystemTime(Date.now() + (await devClockOffset()) * 1000);
  await openList(page);
  await page.getByRole("button", { name: "Select", exact: true }).click();

  // Snooze: no Route durations, "Until" by default.
  await runOnAll(page, "Snooze");
  const snooze = page.getByRole("dialog", { name: "Snooze 3 Alert Groups" });
  await expect(snooze.getByRole("radio")).toHaveCount(2);
  await expect(snooze.getByRole("radio", { name: "Until", exact: true })).toBeChecked();
  await snooze.getByRole("button", { name: "Snooze", exact: true }).click();
  const done = sorted(groups.map((g) => `#${g.number}: done`));
  expect(await resultLines(page, "Snooze")).toEqual(done);
  let result = page.getByRole("dialog", { name: "Result: Snooze" });
  await result.getByRole("button", { name: "Close", exact: true }).last().click();

  await runOnAll(page, "Unsnooze");
  expect(await resultLines(page, "Unsnooze")).toEqual(done);
  result = page.getByRole("dialog", { name: "Result: Unsnooze" });
  await result.getByRole("button", { name: "Close", exact: true }).last().click();

  // Resolve with a Note.
  await runOnAll(page, "Resolve");
  const resolve = page.getByRole("dialog", { name: "Resolve 3 Alert Groups" });
  await resolve.getByLabel("Add a note (optional)").fill("Fixed by the rollback.");
  await resolve.getByRole("button", { name: "Resolve", exact: true }).click();
  expect(await resultLines(page, "Resolve")).toEqual(done);
  result = page.getByRole("dialog", { name: "Result: Resolve" });
  await result.getByRole("button", { name: "Close", exact: true }).last().click();

  // The resolved ones, again: each refused with its message, none stopping the others.
  await openList(page, "all");
  await page.getByRole("button", { name: "Select", exact: true }).click();
  await runOnAll(page, "Resolve");
  await page
    .getByRole("dialog", { name: "Resolve 3 Alert Groups" })
    .getByRole("button", { name: "Resolve", exact: true })
    .click();
  result = page.getByRole("dialog", { name: "Result: Resolve" });
  expect(await resultLines(page, "Resolve")).toEqual(
    sorted(groups.map((g) => `#${g.number}: This Alert Group is already resolved.`)),
  );
  await expect(result.getByText("Done for 0 of 3 Alert Groups.")).toBeVisible();
  await expectNoHorizontalScroll(page);
  expect(csp).toEqual([]);
});
