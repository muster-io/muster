// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Integrations against `muster dev`: the Verification steps of the Integrations pages story — the list with the demo
// Integration, creating "prod-eu" with a Static label, a token shown once with its Alertmanager configuration, webhooks
// from the fake Alertmanager in the Stored Snapshots, a stale edit, revoking, deleting, and a Responder's view.

import { expect, test, type Browser, type Page } from "@playwright/test";

import {
  APP,
  adminApi,
  expectNoHorizontalScroll,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

const FAKE_ALERTMANAGER = "http://127.0.0.1:19093/_fake";
const INGEST_URL = "http://localhost:8081/api/v1/ingest";

/** Asks the fake Alertmanager to send to a registered receiver and returns the status Muster answered. */
async function fakeSend(body: Record<string, unknown>): Promise<number> {
  const res = await fetch(`${FAKE_ALERTMANAGER}/send`, {
    method: "POST",
    body: JSON.stringify(body),
  });
  const text = await res.text();
  expect(res.status, text).toBe(200);
  const out: unknown = JSON.parse(text);
  if (
    typeof out !== "object" ||
    out === null ||
    !("status" in out) ||
    typeof out.status !== "number"
  ) {
    throw new Error(`unexpected answer of the fake Alertmanager: ${text}`);
  }
  return out.status;
}

/** Registers a receiver of the fake Alertmanager that sends to the ingestion endpoint with token. */
async function registerReceiver(name: string, token: string): Promise<void> {
  const res = await fetch(`${FAKE_ALERTMANAGER}/receivers`, {
    method: "POST",
    body: JSON.stringify({ name, url: INGEST_URL, token }),
  });
  expect(res.status).toBe(204);
}

function integrationRow(page: Page, name: string) {
  return page
    .getByRole("table", { name: "Integrations" })
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name, exact: true }) });
}

function tokenRow(page: Page, name: string) {
  return page
    .getByTestId("integration-token-row")
    .filter({ has: page.getByTestId("integration-token-name").getByText(name, { exact: true }) });
}

async function at360(page: Page, name: string): Promise<void> {
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, name);
  await page.setViewportSize({ width: 1280, height: 800 });
}

/** A second signed-in Admin that saves a change of the description first (Verification step 5). */
async function saveElsewhere(browser: Browser, integrationId: string): Promise<void> {
  const context = await browser.newContext({ baseURL: APP, locale: "en-US" });
  const other = await context.newPage();
  await signInAdmin(other);
  await other.goto(`/integrations/${integrationId}/edit`);
  await other.getByLabel("Description").fill("changed elsewhere");
  await other.getByRole("button", { name: "Save" }).click();
  await expect(other.getByTestId("integration-description")).toHaveText("changed elsewhere");
  await context.close();
}

test("creates an Integration with a token, receives webhooks, edits, revokes and deletes", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await signInAdmin(page);

  // 1. The navigation shows "Integrations"; the list shows the demo Integration.
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Integrations" })
    .click();
  await expect(page).toHaveURL(`${APP}/integrations`);
  const demo = integrationRow(page, "dev-alertmanager");
  await expect(demo.getByRole("cell").nth(1)).toHaveText("Webhook only");
  await shot(page, "integrations-list");

  // 2. Create "prod-eu" with the Static label cluster=prod-eu; the duplicate window is pre-filled.
  await page.getByRole("link", { name: "Create integration" }).click();
  await expect(page).toHaveURL(`${APP}/integrations/new`);
  await page.getByLabel("Name", { exact: true }).fill("prod-eu");
  await page.getByRole("button", { name: "Add label" }).click();
  await expect(page.getByLabel("Label name")).toBeFocused();
  await page.getByLabel("Label name").fill("cluster");
  await page.getByLabel("Label value").fill("prod-eu");
  await expect(page.getByLabel("Duplicate window")).toHaveValue("45");
  await expect(
    page.getByText(
      "Snapshots of the same Alertmanager group that arrive this close together count as one.",
    ),
  ).toBeVisible();
  const mode = page.getByTestId("integration-connection-mode");
  await expect(mode).toContainText("Webhook only");
  await expect(mode).toContainText(
    "An Alert that Alertmanager resolves closes at once. An Alert that Alertmanager stops listing is Gone after up to two repeat intervals, never sooner than 5 minutes, and Stale after three learned repeat intervals.",
  );
  await shot(page, "integration-form");
  await at360(page, "integration-form-360");

  // A label name Alertmanager would refuse is caught on its row.
  await page.getByRole("button", { name: "Add label" }).click();
  await page.getByLabel("Label name").nth(1).fill("bad-name");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(
    page.getByText(
      "A label name starts with a letter or an underscore and has only letters, digits and underscores.",
    ),
  ).toBeVisible();
  await page.getByRole("button", { name: "Remove label bad-name" }).click();

  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: "prod-eu", level: 1 })).toBeVisible();
  const integrationId = new URL(page.url()).pathname.split("/")[2] ?? "";
  expect(integrationId).toMatch(/^\w+$/);
  await expect(page.getByTestId("integration-connection-mode")).toContainText("Webhook only");
  await expect(page.getByTestId("integration-connection-mode")).toContainText(
    "An Alert that Alertmanager resolves closes at once.",
  );
  await expect(page.getByTestId("integration-last-snapshot")).toHaveText("Last Snapshot: never");
  await expect(page.getByTestId("integration-snapshot-count")).toHaveText("Snapshots received: 0");
  await expect(page.getByTestId("static-label")).toHaveText(["cluster=prod-eu"]);
  await expect(page.getByTestId("integration-duplicate-window")).toHaveText("45 seconds");

  // A name is taken once.
  await page.goto("/integrations/new");
  await page.getByLabel("Name", { exact: true }).fill("prod-eu");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByText("Another integration has this name.")).toBeVisible();
  await page.goto(`/integrations/${integrationId}`);

  // 3. A token "rotation-1": the value and the snippet show once, the snippet is copied.
  await page.getByRole("button", { name: "Create token" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("rotation-1");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog.getByText("You will not see this token again.")).toBeVisible();
  const value = (await dialog.getByTestId("integration-token-value").textContent()) ?? "";
  expect(value).toMatch(/^mstr_int_\S+$/);
  const snippet = (await dialog.getByTestId("integration-token-snippet").textContent()) ?? "";
  expect(snippet).toContain("send_resolved: true");
  expect(snippet).toContain("max_alerts: 0");
  expect(snippet).toContain(`credentials: ${value}`);
  expect(snippet).toContain(`url: ${INGEST_URL}`);
  await shot(page, "integration-token-dialog");
  await dialog.getByRole("button", { name: "Copy" }).nth(1).click();
  await expect(dialog.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(snippet);
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "integration-token-dialog-360");
  await page.setViewportSize({ width: 1280, height: 800 });
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(tokenRow(page, "rotation-1")).toContainText("Never used");
  expect(await page.content()).not.toContain(value);

  // 4. Two webhooks from the fake Alertmanager, one of them not JSON.
  await registerReceiver("prod-eu", value);
  expect(await fakeSend({ receiver: "prod-eu" })).toBe(202);
  expect(await fakeSend({ receiver: "prod-eu", raw: "not json", content_type: "text/plain" })).toBe(
    202,
  );
  await page.reload();
  await expect(page.getByTestId("integration-last-snapshot")).not.toHaveText(
    "Last Snapshot: never",
  );
  await expect(page.getByTestId("integration-last-snapshot")).toHaveText(/^Last Snapshot: .+/);
  await expect(tokenRow(page, "rotation-1")).toContainText("Last used");
  // Processing takes both: the webhook is processed, the body that is not JSON fails (C-06.FR-20). The two are of
  // different Alertmanager groups, so they are processed at the same time and the failure may be counted first; the
  // table is read once per load, so it is reloaded until both are done.
  const snapshots = page.getByRole("table", { name: "Stored Snapshots" });
  await expect(async () => {
    await page.reload();
    await expect(snapshots.getByTestId("snapshot-state")).toHaveText(
      [/^Failed: the body is not valid JSON: /, "Processed"],
      { timeout: 1000 },
    );
  }).toPass();
  await page.reload();
  await expect(page.getByTestId("integration-snapshot-count")).toHaveText("Snapshots received: 2");
  await shot(page, "integration-page");
  await at360(page, "integration-page-360");
  // Newest first: the first row is the second webhook.
  await snapshots.getByRole("row").nth(1).getByRole("link").click();
  await expect(page.getByRole("heading", { name: "Stored Snapshot", level: 1 })).toBeVisible();
  await expect(page.getByTestId("snapshot-body")).toHaveText("not json");
  await expect(page.getByTestId("snapshot-content-type")).toHaveText("text/plain");
  await expect(page.getByTestId("snapshot-state")).toHaveText(
    /^Failed: the body is not valid JSON: /,
  );
  await expect(page.getByTestId("snapshot-received")).toHaveText(/^Received .+/);
  await page.getByRole("link", { name: "prod-eu" }).click();

  // 5. Edit the duplicate window; a save over a newer version is refused.
  await page.getByRole("link", { name: "Edit" }).click();
  await expect(page.getByLabel("Duplicate window")).toHaveValue("45");
  await page.getByLabel("Duplicate window").fill("60");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("integration-duplicate-window")).toHaveText("60 seconds");
  await page.getByRole("link", { name: "Edit" }).click();
  await page.getByLabel("Description").fill("changed here");
  await saveElsewhere(browser, integrationId);
  await page.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByText("Someone else changed this integration. Reload to see the changes."),
  ).toBeVisible();
  await shot(page, "integration-edit-stale");
  await page.getByRole("button", { name: "Reload" }).click();
  await expect(page.getByLabel("Description")).toHaveValue("changed elsewhere");
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByTestId("integration-description")).toHaveText("changed elsewhere");

  // 6. Revoke "rotation-1": Alertmanager can no longer send with it.
  await tokenRow(page, "rotation-1").getByRole("button", { name: "Revoke" }).click();
  const revoke = page.getByRole("dialog");
  await expect(
    revoke.getByText("Alertmanager stops being able to send with this token at once."),
  ).toBeVisible();
  await revoke.getByRole("button", { name: "Revoke" }).click();
  await expect(tokenRow(page, "rotation-1")).toHaveCount(0);
  await expect(page.getByText("This integration has no tokens.")).toBeVisible();
  expect(await fakeSend({ receiver: "prod-eu", raw: "{}" })).toBe(401);

  // 7. Delete: its tokens stop working at once and it leaves the list.
  const admin = await adminApi();
  const second = await admin.call<{ value: string }>(
    "POST",
    `/api/v1/integrations/${integrationId}/tokens`,
    { name: "rotation-2" },
  );
  await registerReceiver("prod-eu", second.value);
  expect(await fakeSend({ receiver: "prod-eu", raw: "{}" })).toBe(202);
  await page.reload();
  await page.getByRole("button", { name: "Delete" }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm.getByRole("heading", { name: "Delete integration prod-eu?" })).toBeVisible();
  await expect(confirm.getByText(/Its tokens stop working at once\./)).toBeVisible();
  // The Alert of the webhook above is in an open Alert Group, which the deletion resolves (C-09.FR-21).
  await expect(confirm.getByTestId("integration-delete-description")).toHaveText(
    "Its tokens stop working at once. Its Stored Snapshots are kept for 14 days. 1 open Alert Group will be resolved.",
  );
  await shot(page, "integration-delete-dialog");
  await confirm.getByRole("button", { name: "Delete" }).click();
  await expect(page).toHaveURL(`${APP}/integrations`);
  await expect(integrationRow(page, "dev-alertmanager")).toHaveCount(1);
  await expect(integrationRow(page, "prod-eu")).toHaveCount(0);
  expect(await fakeSend({ receiver: "prod-eu", raw: "{}" })).toBe(401);
  await admin.dispose();
  expect(csp).toEqual([]);
});

test("a Responder sees the Integrations without changing them or reading Stored Snapshots", async ({
  page,
}) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  const { token } = await admin.createUser("rhea", "responder");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "rhea-password-1" });
  await admin.dispose();

  // 8. The list and the pages are visible, with no control that changes them and no Stored Snapshots.
  await signIn(page, "rhea", "rhea-password-1");
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Integrations" })
    .click();
  await expect(integrationRow(page, "dev-alertmanager")).toHaveCount(1);
  await expect(page.getByRole("link", { name: "Create integration" })).toHaveCount(0);
  await integrationRow(page, "dev-alertmanager").getByRole("link").click();
  await expect(page.getByRole("heading", { name: "dev-alertmanager", level: 1 })).toBeVisible();
  await expect(page.getByTestId("static-label")).toHaveText(["cluster=dev"]);
  await expect(tokenRow(page, "dev")).toHaveCount(1);
  await expect(page.getByRole("button", { name: "Create token" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Revoke/ })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Edit" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Delete" })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Stored Snapshots" })).toHaveCount(0);
  await shot(page, "integration-page-responder");
  await page.goto("/integrations/new");
  await expect(page.getByText("You do not have permission to see this page.")).toBeVisible();
  expect(csp).toEqual([]);
});
