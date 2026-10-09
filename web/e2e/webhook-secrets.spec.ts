// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Secrets and the Signing secret of an outgoing webhook against `muster dev`: Verification steps 3 to 6 of its
// story. The Admin adds the Secret token and the list shows its name, "Set" and a date, and no value anywhere on the
// page; a literal Authorization header is saved with the warning at that header, which goes once the header reads the
// Secret; "Regenerate" shows a new Signing secret once and the page says the previous one still signs until it is
// retired. A Viewer sees the header with its .Secrets reference, the Secret's name and no actions. No page scrolls
// sideways at 360 px; no Content Security Policy violation.

import { type Browser, expect, test } from "@playwright/test";

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

const NAME = "wh-secrets";
const VALUE = "s3cr3t";
const LITERAL =
  "Store credentials as Secrets: this value is shown to everyone who can read Destinations.";
const REFERENCE = "Bearer {{ .Secrets.token }}";

interface Named {
  id: string;
  name: string;
}

let destinationId = "";

async function cleanUp(admin: Api): Promise<void> {
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => x.name === NAME)) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    const created = await admin.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "webhook",
        name: NAME,
        mode: "events",
        events: { url: "http://127.0.0.1:18093/hook/wh-secrets", headers: [] },
        proxy: { enabled: false },
        mentions: {
          new_alert_group: { everyone: "none", user_ids: [], groups: [] },
          new_alerts: { everyone: "none", user_ids: [], groups: [] },
          reopen: { everyone: "none", user_ids: [], groups: [] },
          ack_timeout: { everyone: "none", user_ids: [], groups: [] },
          snooze_ended: { everyone: "none", user_ids: [], groups: [] },
          rise_to_urgent: { everyone: "none", user_ids: [], groups: [] },
        },
        limiter: { limit: 5, per_seconds: 1 },
      },
    );
    destinationId = created.destination.id;
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

test("adds a Secret, warns of a literal credential and rotates the Signing secret", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/destinations/${destinationId}`);
  await expect(page.getByRole("heading", { name: NAME, level: 1 })).toBeVisible();

  // 3. Secrets → "Add secret" → token = s3cr3t → "Save" → "token", "Set" and a date, and no value on the page.
  const secrets = page.getByTestId("destination-secrets");
  await expect(secrets.getByText("No Secrets yet.")).toBeVisible();
  await secrets.getByRole("button", { name: "Add secret" }).click();
  await secrets.getByLabel("Name", { exact: true }).fill("token");
  await expect(secrets.getByText("Reference it as {{ .Secrets.token }}.")).toBeVisible();
  const value = secrets.getByLabel("Value", { exact: true });
  await expect(value).toHaveValue("");
  await value.fill(VALUE);
  await secrets.getByRole("button", { name: "Save", exact: true }).click();
  await expect(secrets.getByTestId("secret-name")).toHaveText("token");
  await expect(secrets.getByTestId("secret-status")).toHaveText(/^Set, changed .*\d/);
  await expect(secrets.getByTestId("secret-reference")).toHaveText("{{ .Secrets.token }}");
  await expect(secrets.getByTestId("secret-form")).toHaveCount(0);
  expect(await page.content()).not.toContain(VALUE);
  const values = await page
    .locator("input, textarea")
    .evaluateAll((inputs) =>
      inputs.map((i) =>
        i instanceof HTMLInputElement || i instanceof HTMLTextAreaElement ? i.value : "",
      ),
    );
  expect(values).not.toContain(VALUE);
  await shot(page, "webhook-secrets-added");

  // 4. "Events" → Authorization = Bearer abc → "Save" → the warning at that header → the Secret's reference → gone.
  await expect(page.getByRole("tab", { name: "Events" })).toHaveAttribute("aria-selected", "true");
  const events = page.getByTestId("webhook-events");
  await events.getByRole("button", { name: "Add header" }).click();
  await events.getByLabel("Name of header 1").fill("Authorization");
  const header = events.getByLabel("Value of header 1");
  await header.fill("Bearer abc");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("destination-status")).toHaveText("Saved.");
  await expect(events.getByTestId("literal-credential")).toHaveText(LITERAL);
  await expect(header).toHaveAccessibleDescription(LITERAL);
  await shot(page, "webhook-literal-credential");
  await header.fill(REFERENCE);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("destination-status")).toHaveText("Saved.");
  await expect(page.getByTestId("literal-credential")).toHaveCount(0);
  await expect(page.getByTestId("destination-stale")).toHaveCount(0);

  // A change of the form stays while the Secrets and the Signing secret change below.
  await page.getByLabel("Requests").fill("6");

  // 5. Signing secret → "Regenerate" → confirm → a new secret once → the previous one still signs → retire it.
  const signing = page.getByTestId("signing-secret");
  await expect(signing.getByTestId("signing-secret-status")).toHaveText(
    /^Signing secret: set, changed /,
  );
  await signing.getByRole("button", { name: "Regenerate" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Regenerate" }).click();
  const dialog = page.getByTestId("signing-secret-dialog");
  const secret = await dialog.getByLabel("Secret", { exact: true }).inputValue();
  expect(secret).toMatch(/^whsec_/);
  await expect(dialog.getByText("You will not see this secret again.")).toBeVisible();
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(await page.content()).not.toContain(secret);
  const previous = page.getByTestId("signing-secret-previous");
  await expect(previous).toContainText(/The previous secret still signs, since .*\d/);
  await shot(page, "webhook-signing-previous");
  await previous.getByRole("button", { name: "Retire previous secret" }).click();
  await expect(previous).toHaveCount(0);

  // The save of the change goes through: the form took the versions of the Signing secret changes silently.
  await expect(page.getByLabel("Requests")).toHaveValue("6");
  await expect(page.getByTestId("destination-replaced")).toHaveCount(0);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("destination-status")).toHaveText("Saved.");

  // A change of the settings elsewhere is not taken silently: with changes of its own, the form is stale.
  await page.getByLabel("Requests").fill("7");
  const admin = await adminApi();
  try {
    const stored = await admin.call<Record<string, unknown> & { etag: string }>(
      "GET",
      `/api/v1/destinations/${destinationId}`,
    );
    const {
      id: _id,
      health: _health,
      routes: _routes,
      created_at: _created,
      etag,
      signing_secret_status: _signing,
      warnings: _warnings,
      template_error: _templateError,
      proxy: _proxy,
      ...settings
    } = stored;
    await admin.call(
      "PUT",
      `/api/v1/destinations/${destinationId}`,
      { ...settings, proxy: { enabled: false }, limiter: { limit: 9, per_seconds: 1 } },
      { "If-Match": etag },
    );
  } finally {
    await admin.dispose();
  }
  await secrets.getByRole("button", { name: "Replace token" }).click();
  await secrets.getByLabel("Value", { exact: true }).fill("rotated");
  await secrets.getByRole("button", { name: "Save", exact: true }).click();
  // The Secrets share the Destination's version: theirs is stale too, and they are read again.
  await expect(secrets.getByTestId("secrets-stale")).toContainText(
    "The Secrets changed meanwhile.",
  );
  await secrets.getByTestId("secrets-stale").getByRole("button", { name: "Reload" }).click();
  await secrets.getByRole("button", { name: "Save", exact: true }).click();
  await expect(secrets.getByTestId("secret-form")).toHaveCount(0);
  await expect(page.getByTestId("destination-stale")).toBeVisible();
  await expect(page.getByLabel("Requests")).toHaveValue("7");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("destination-stale")).toContainText(
    "Someone else changed this Destination.",
  );
  await page.getByTestId("destination-stale").getByRole("button", { name: "Reload" }).click();
  await expect(page.getByLabel("Requests")).toHaveValue("9");

  await page.setViewportSize({ width: 360, height: 740 });
  await expect(page.getByTestId("destination-secrets")).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "webhook-secrets-360");
  expect(csp).toEqual([]);
});

test("a Viewer sees the reference and the Secret's name, and no actions", async ({ browser }) => {
  expect(destinationId).not.toBe("");
  const admin = await adminApi();
  const { token } = await admin.createUser("wh-viewer", "viewer");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "wh-viewer-pass-1" });
  await admin.dispose();
  // The Viewer stays: users.spec.ts counts the deleted Viewers of the run.
  await viewer(browser);
});

async function viewer(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "wh-viewer", "wh-viewer-pass-1");
  await page.goto(`/destinations/${destinationId}`);
  // 6. The header with its reference, the Secret's name, and no "Regenerate", "Add secret" or "Delete".
  await expect(page.getByTestId("destination-read-only")).toBeVisible();
  await expect(page.getByLabel("Value of header 1")).toHaveValue(REFERENCE);
  await expect(page.getByTestId("secret-name")).toHaveText("token");
  for (const name of [
    "Regenerate",
    "Add secret",
    "Delete",
    "Save",
    "Replace token",
    "Remove token",
  ]) {
    await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
  }
  expect(await page.content()).not.toContain(VALUE);
  await expectNoHorizontalScroll(page);
  await shot(page, "webhook-viewer-360");
  expect(csp).toEqual([]);
  await context.close();
}
