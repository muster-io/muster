// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The outgoing webhook Destination form against `muster dev` and its fake receiving endpoint: Verification steps 1, 2
// and 7 of its story. "Create destination" → "Outgoing webhook" has the mode tabs "Events", "Template" and "Both", and
// no Connection and no "Check". In "Template", "Create" posts to the fake chat with an extraction rule and "Update" puts
// to the message by its extracted id; a broken body of "Update" is refused at its field with its line and column, and
// once fixed the Destination is created and its Signing secret shown once, with "Copy". A reader of Russian sees the
// form at phone width. "Delete" says what happens in template and in events mode, and the Destination leaves the list.
// No page scrolls sideways at 360 px; no Content Security Policy violation.

import { type Browser, expect, test, type Page } from "@playwright/test";

import {
  type Api,
  adminApi,
  expectNoHorizontalScroll,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const NAME = "wh-chat";
const CHAT = "http://127.0.0.1:18093/chat/ops/messages";
const BODY = "{{ .Nope }";
const MISSING = "{{ .Nope }}";
const FIXED = '{"text": {{ .AlertGroup.Title | toJson }}}';

interface Named {
  id: string;
  name: string;
}

let destinationId = "";

/** Deletes the Destinations this spec creates, whatever an earlier run left. */
async function cleanUp(admin: Api): Promise<void> {
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => x.name === NAME)) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

function builder(page: Page, name: string) {
  return page.getByRole("group", { name, exact: true });
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test("creates a template-mode outgoing webhook and shows its Signing secret once", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await signInAdmin(page);

  // 1. Destinations → "Create destination" → "Outgoing webhook" → the tabs; no Connection and no "Check".
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Outgoing webhook" }).check();
  const fields = page.getByTestId("webhook-fields");
  await expect(fields.getByRole("tab")).toHaveText(["Events", "Template", "Both"]);
  await expect(fields.getByRole("tab", { name: "Events" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.getByLabel("Connection", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Check", exact: true })).toHaveCount(0);
  await expect(page.getByText("Proxy", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Requests")).toHaveValue("5");
  await expect(page.getByLabel("Period in seconds")).toHaveValue("1");
  await shot(page, "webhook-destination-new");

  // 2. "Template" → "Create" and "Update" → a broken body of "Update" is refused at its line and column.
  await fields.getByRole("tab", { name: "Template" }).click();
  for (const name of ["Create", "Update", "Open thread", "Reply in thread"]) {
    await expect(builder(page, name)).toBeVisible();
  }
  await page.getByRole("textbox", { name: "Name", exact: true }).fill(NAME);
  const create = builder(page, "Create");
  await expect(create.getByLabel("Method")).toHaveValue("POST");
  await create.getByLabel("URL", { exact: true }).fill(CHAT);
  await page.getByRole("textbox", { name: "Body of Create" }).fill(FIXED);
  await create.getByRole("button", { name: "Add rule" }).click();
  await create.getByLabel("Name of rule 1").fill("id");
  await create.getByLabel("JSONPath of rule 1").fill("$.data.id");
  await expect(
    create.getByText("Read it as {{ .Response.<name> }}.", { exact: false }),
  ).toBeVisible();
  const update = builder(page, "Update");
  await expect(update.getByLabel("Method")).toHaveValue("PUT");
  await update.getByLabel("URL", { exact: true }).fill(`${CHAT}/{{ .Response.id }}`);
  const body = page.getByRole("textbox", { name: "Body of Update" });
  await body.fill(BODY);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(body).toHaveAttribute("aria-invalid", "true");
  // An action that is never closed does not parse; text/template names its line only.
  await expect(update.getByTestId("template-errors")).toHaveText(
    'Line 1: unexpected "}" in operand',
  );
  await expect(update.locator('[data-error-line="true"]')).toHaveText("1");
  await expect(page).toHaveURL(/\/destinations\/new$/);
  // A field the data does not have fails the dry run, at its line and column.
  await body.fill(MISSING);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(update.getByTestId("template-errors")).toHaveText(/^Line 1, column \d+: .*Nope/);
  await expect(update.locator('[data-error="true"]')).toBeVisible();
  await expect(page).toHaveURL(/\/destinations\/new$/);
  await shot(page, "webhook-destination-template-error");
  await body.fill(FIXED);
  await expect(body).toHaveAttribute("aria-invalid", "false");
  await page.getByRole("button", { name: "Save", exact: true }).click();

  // The Signing secret, once, with "Copy" → "Close" → the Destination's page.
  const dialog = page.getByTestId("signing-secret-dialog");
  await expect(dialog.getByText("You will not see this secret again.")).toBeVisible();
  const secret = await dialog.getByLabel("Secret", { exact: true }).inputValue();
  expect(secret).toMatch(/^whsec_[A-Za-z0-9+/]+=*$/);
  await shot(page, "webhook-signing-secret-dialog");
  await dialog.getByRole("button", { name: "Copy" }).click();
  await expect(dialog.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(secret);
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await expect(page).toHaveURL(/\/destinations\/DS[0-9A-Z]+$/);
  destinationId = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: NAME, level: 1 })).toBeVisible();
  await expect(page.getByTestId("destination-type")).toHaveText("Outgoing webhook");
  await expect(page.getByTestId("signing-secret-status")).toHaveText(
    /^Signing secret: set, changed /,
  );
  await expect(page.getByRole("tab", { name: "Template" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.getByRole("textbox", { name: "Body of Update" })).toHaveValue(FIXED);
  // The secret is nowhere on the page once the dialog closed.
  expect(await page.content()).not.toContain(secret);
  const admin = await adminApi();
  try {
    const stored = await admin.call<{ mode: string; template: { create: { extract: unknown } } }>(
      "GET",
      `/api/v1/destinations/${destinationId}`,
    );
    expect(stored.mode).toBe("template");
    expect(stored.template.create.extract).toEqual([{ name: "id", path: "$.data.id" }]);
    expect(JSON.stringify(stored)).not.toContain(secret);
  } finally {
    await admin.dispose();
  }

  await page.setViewportSize({ width: 360, height: 740 });
  await page.reload();
  await expect(page.getByTestId("signing-secret-status")).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "webhook-destination-360");
  expect(csp).toEqual([]);
});

test("a reader of Russian sees the outgoing webhook at phone width", async ({ browser }) => {
  expect(destinationId).not.toBe("");
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("wh-admin-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "wh-admin-ru-pass-1" });
  await admin.dispose();
  try {
    await russianAdmin(browser);
  } finally {
    const cleanup = await adminApi();
    try {
      await cleanup.call("DELETE", `/api/v1/users/${userId}`);
    } finally {
      await cleanup.dispose();
    }
  }
});

async function russianAdmin(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "wh-admin-ru", "wh-admin-ru-pass-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
  await page.goto(`/destinations/${destinationId}`);
  await expect(page.getByTestId("destination-type")).toHaveText("Исходящий вебхук");
  await expect(page.getByRole("tab")).toHaveText(["События", "Шаблон", "Оба режима"]);
  await expect(page.getByRole("group", { name: "Создание", exact: true })).toBeVisible();
  await expect(page.getByRole("group", { name: "Ответ в треде", exact: true })).toBeVisible();
  await expect(page.getByTestId("signing-secret-status")).toHaveText(/^Секрет подписи: задан/);
  await expect(page.getByRole("button", { name: "Добавить секрет" })).toBeVisible();
  await page.getByRole("button", { name: "Удалить", exact: true }).click();
  await expect(page.getByTestId("destination-delete-description")).toContainText(
    "Открытые сообщения получат последнее обновление, затем секреты будут удалены.",
  );
  await page.getByRole("button", { name: "Отмена" }).click();
  await expectNoHorizontalScroll(page);
  await shot(page, "webhook-destination-ru-360");
  expect(csp).toEqual([]);
  await context.close();
}

test("deleting says what happens in template and events mode", async ({ page }) => {
  expect(destinationId).not.toBe("");
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/destinations/${destinationId}`);

  // Template mode: open messages get a last update first.
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByTestId("destination-delete-description")).toHaveText(
    "Muster stops sending here, and the Destination leaves every Route. Open messages get a last update, then the secrets are deleted. This cannot be undone.",
  );
  await page.getByRole("button", { name: "Cancel" }).click();

  // 7. Events mode → "Delete" → queued events are not sent, the secrets go now → the list no longer shows it.
  await page.getByRole("tab", { name: "Events" }).click();
  await page.getByLabel("URL", { exact: true }).fill("http://127.0.0.1:18093/hook/ops");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("destination-status")).toHaveText("Saved.");
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByTestId("destination-delete-description")).toHaveText(
    "Muster stops sending here, and the Destination leaves every Route. Queued events will not be sent. The Signing secret and the Secrets are deleted now. This cannot be undone.",
  );
  await shot(page, "webhook-destination-delete");
  await page.getByRole("dialog").getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page).toHaveURL(/\/destinations$/);
  await expect(page.getByRole("link", { name: NAME, exact: true })).toHaveCount(0);
  expect(csp).toEqual([]);
});
