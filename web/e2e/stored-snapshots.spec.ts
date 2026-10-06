// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Stored Snapshots against `muster dev`: bodies sent to the ingestion endpoint with an Integration token appear on the
// Integration page, filtered by state and time range in the URL, and the viewer shows each exactly as received and as
// inert text — JSON (formatted on request), markup, a body that is not UTF-8 (as base64), invisible characters (as
// escapes) and a large body (in part, copied whole). Then the pages in Russian.

import { expect, test, type Page } from "@playwright/test";

import {
  APP,
  adminApi,
  expectNoHorizontalScroll,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

const INGEST_URL = "http://localhost:8081/api/v1/ingest";

const WEBHOOK = '{"version":"4","groupKey":"{}:{alertname=\\"T\\"}","status":"firing","alerts":[]}';
const MARKUP = "<img src=x onerror=alert(1)><script>alert(2)</script><b>bold</b>";
const BINARY = new Uint8Array<ArrayBuffer>(new ArrayBuffer(4));
BINARY.set([0xff, 0xfe, 0x00, 0x41]);
const BIDI = "abc\u202Edef";
const LARGE = "x".repeat(300_000);

async function ingest(
  token: string,
  body: string | Uint8Array<ArrayBuffer>,
  contentType?: string,
): Promise<void> {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (contentType !== undefined) {
    headers["Content-Type"] = contentType;
  }
  const res = await fetch(INGEST_URL, { method: "POST", headers, body });
  expect(res.status).toBe(202);
}

/** An Integration with a token, and one Snapshot of each kind sent with it, oldest first. */
async function prepare(name: string): Promise<{ id: string; token: string }> {
  const admin = await adminApi();
  const integration = await admin.call<{ id: string }>("POST", "/api/v1/integrations", {
    name,
    connection_mode: "webhook_only",
    static_labels: {},
    duplicate_window_seconds: 45,
    heartbeat: { enabled: false },
  });
  const created = await admin.call<{ value: string }>(
    "POST",
    `/api/v1/integrations/${integration.id}/tokens`,
    { name: "snapshots" },
  );
  await admin.dispose();
  await ingest(created.value, WEBHOOK, "application/json");
  await ingest(created.value, MARKUP, "text/html");
  await ingest(created.value, BINARY, "application/octet-stream");
  await ingest(created.value, BIDI, "text/plain");
  await ingest(created.value, LARGE, "text/plain");
  return { id: integration.id, token: created.value };
}

function snapshotRows(page: Page) {
  return page.getByRole("table", { name: "Stored Snapshots" }).getByRole("row");
}

/** Opens the Stored Snapshot of a row, counted from the newest (1). */
async function open(page: Page, integrationId: string, row: number): Promise<void> {
  await page.goto(`/integrations/${integrationId}`);
  await snapshotRows(page).nth(row).getByRole("link").click();
  await expect(page.getByTestId("snapshot-body")).toBeVisible();
}

test("lists, filters and shows Stored Snapshots as inert text", async ({ page }) => {
  const csp = watchCsp(page);
  const dialogs: string[] = [];
  page.on("dialog", (d) => {
    dialogs.push(d.message());
    void d.dismiss();
  });
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  const { id } = await prepare("snapshots-test");
  await signInAdmin(page);

  // The table: newest first, every one pending, with its size and group key.
  await page.goto(`/integrations/${id}`);
  await expect(
    page.getByRole("table", { name: "Stored Snapshots" }).getByTestId("snapshot-state"),
  ).toHaveText(["Pending", "Pending", "Pending", "Pending", "Pending"]);
  await expect(snapshotRows(page).nth(1).getByRole("cell").nth(1)).toHaveText("300 kB");

  // Filters in the URL: a state with no Snapshots, then a time range that starts tomorrow.
  await page.getByLabel("State").selectOption({ label: "Failed" });
  await expect(page).toHaveURL(/snapshot_state=failed/);
  await expect(page.getByText("No Stored Snapshots match these filters.")).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("State")).toHaveValue("failed");
  await expect(page.getByText("No Stored Snapshots match these filters.")).toBeVisible();
  await page.getByLabel("State").selectOption({ label: "Pending" });
  await expect(snapshotRows(page)).toHaveCount(6);
  const tomorrow = new Date(Date.now() + 36 * 60 * 60 * 1000).toISOString().slice(0, 10);
  await page.getByLabel("From").fill(tomorrow);
  await expect(page).toHaveURL(new RegExp(`snapshot_from=${tomorrow}`));
  await expect(page.getByText("No Stored Snapshots match these filters.")).toBeVisible();
  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(snapshotRows(page)).toHaveCount(6);
  await shot(page, "stored-snapshots-table");

  // JSON: as received, formatted on request.
  await open(page, id, 5);
  await expect(page.getByTestId("snapshot-body")).toHaveText(WEBHOOK);
  await expect(page.getByTestId("snapshot-content-type")).toHaveText("application/json");
  await page.getByRole("button", { name: "Format JSON" }).click();
  await expect(page.getByRole("button", { name: "Format JSON" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(page.getByTestId("snapshot-body")).toContainText('"version": "4",');
  await shot(page, "stored-snapshot-json");

  // Markup stays text: no element and no script comes of it.
  await open(page, id, 4);
  await expect(page.getByTestId("snapshot-body")).toHaveText(MARKUP);
  await expect(page.getByTestId("snapshot-content-type")).toHaveText("text/html");
  await expect(page.locator('img[src="x"]')).toHaveCount(0);
  await expect(page.getByTestId("snapshot-body").locator("*")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Format JSON" })).toHaveCount(0);
  await shot(page, "stored-snapshot-markup");

  // Not UTF-8: base64.
  await open(page, id, 3);
  await expect(page.getByText("The body is not valid UTF-8 and is shown as base64.")).toBeVisible();
  await expect(page.getByTestId("snapshot-body")).toHaveText(
    Buffer.from(BINARY).toString("base64"),
  );
  await shot(page, "stored-snapshot-base64");

  // Invisible characters show as escapes; Copy copies the body as received.
  await open(page, id, 2);
  await expect(page.getByTestId("snapshot-body")).toHaveText("abc\\u202Edef");
  await expect(page.getByTestId("snapshot-body").locator("[data-escape]")).toHaveText("\\u202E");
  await page.getByRole("button", { name: "Copy" }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(BIDI);

  // A large body shows in part until "Show all"; Copy copies all of it.
  await open(page, id, 1);
  await expect(page.getByText("Showing the first 65,536 of 300,000 characters.")).toBeVisible();
  expect(((await page.getByTestId("snapshot-body").textContent()) ?? "").length).toBe(65_536);
  await page.getByRole("button", { name: "Copy" }).click();
  expect((await page.evaluate(() => navigator.clipboard.readText())).length).toBe(300_000);
  await page.getByRole("button", { name: "Show all" }).click();
  await expect(page.getByText(/^Showing the first/)).toHaveCount(0);
  expect(((await page.getByTestId("snapshot-body").textContent()) ?? "").length).toBe(300_000);

  // 360 px: the viewer and the table do not scroll sideways.
  await open(page, id, 4);
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "stored-snapshot-markup-360");
  await page.goto(`/integrations/${id}`);
  await expect(snapshotRows(page)).toHaveCount(6);
  await expectNoHorizontalScroll(page);
  await page.setViewportSize({ width: 1280, height: 800 });

  expect(dialogs).toEqual([]);
  expect(csp).toEqual([]);
});

test("shows the Integration pages in Russian", async ({ page }) => {
  const csp = watchCsp(page);
  const { id } = await prepare("snapshots-ru");
  const admin = await adminApi();
  const { token } = await admin.createUser("nadia", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "nadia-password-1" });
  await admin.dispose();
  await signIn(page, "nadia", "nadia-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  const nav = page.getByRole("navigation", { name: "Основная" });
  await nav.getByRole("link", { name: "Интеграции" }).click();
  await expect(page).toHaveURL(`${APP}/integrations`);
  await expect(page.getByRole("heading", { name: "Интеграции", level: 1 })).toBeVisible();
  await expect(page.getByRole("cell", { name: "Только вебхук" }).first()).toBeVisible();
  await shot(page, "integrations-list-ru");

  await page.getByRole("link", { name: "Создать интеграцию" }).click();
  await expect(page.getByLabel("Окно дубликатов")).toHaveValue("45");
  await shot(page, "integration-form-ru");

  await page.goto(`/integrations/${id}`);
  await expect(page.getByTestId("integration-snapshot-count")).toHaveText(
    /^Получено снимков: \d+$/,
  );
  await expect(page.getByTestId("integration-duplicate-window")).toHaveText("45 секунд");
  await expect(
    page.getByRole("table", { name: "Сохранённые снимки" }).getByTestId("snapshot-state").first(),
  ).toHaveText("Ожидает");
  await shot(page, "integration-page-ru");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "integration-page-ru-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  await page.getByRole("button", { name: "Создать токен" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Создать", exact: true }).click();
  await expect(dialog.getByText("Больше вы этот токен не увидите.")).toBeVisible();
  await expect(dialog.getByText("Конфигурация Alertmanager")).toBeVisible();
  await shot(page, "integration-token-dialog-ru");
  await dialog.getByRole("button", { name: "Готово" }).click();
  await expect(
    page.getByTestId("integration-token-name").getByText("Токен без названия"),
  ).toBeVisible();

  await page.getByRole("button", { name: "Удалить" }).click();
  await expect(page.getByTestId("integration-delete-description")).toHaveText(
    "Её токены сразу перестанут работать. Её сохранённые снимки останутся на 14 дней.",
  );
  await shot(page, "integration-delete-dialog-ru");
  await page.getByRole("dialog").getByRole("button", { name: "Отмена" }).click();

  await page
    .getByRole("table", { name: "Сохранённые снимки" })
    .getByRole("row")
    .nth(3)
    .getByRole("link")
    .click();
  await expect(
    page.getByText("Тело не является корректным UTF-8 и показано в base64."),
  ).toBeVisible();
  await expect(page.getByRole("heading", { name: "Сохранённый снимок", level: 1 })).toBeVisible();
  await shot(page, "stored-snapshot-base64-ru");
  expect(csp).toEqual([]);
});
