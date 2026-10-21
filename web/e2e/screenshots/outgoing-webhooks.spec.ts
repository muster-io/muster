// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Outgoing webhook: events mode" and "Outgoing webhook: template mode" (docs/outgoing-webhooks/):
// the Admin creates the events-mode Destination "automation" for the fake webhook receiver, sees its Signing secret
// once, adds the Secret token, regenerates the Signing secret and retires the previous one; then the template-mode
// Destination "team chat" for the fake chat behind the receiver, with its Create and Update requests. The images show
// the receiver at https://hooks.example.org instead of its loopback address (capture.ts). It deletes both.

import { expect, test, type Locator, type Page } from "@playwright/test";

import { adminApi, removeNamed, signInAdmin } from "../support";
import { capture, nav, scrollToTop } from "./capture";

const EVENTS = "outgoing-webhooks/events";
const TEMPLATES = "outgoing-webhooks/templates";
const AUTOMATION = "automation";
const CHAT = "team chat";
const RECEIVER = "http://127.0.0.1:18093";
const MESSAGE =
  '{"text":{{ printf "#%d %s (%s)" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status | toJson }}}';

test.describe.configure({ mode: "serial" });

async function cleanUp(): Promise<void> {
  const admin = await adminApi();
  try {
    for (const name of [AUTOMATION, CHAT]) {
      await removeNamed(admin, "/api/v1/destinations", name, "?limit=500");
    }
  } finally {
    await admin.dispose();
  }
}

/** The form of a new outgoing webhook Destination. */
async function newWebhook(page: Page): Promise<Locator> {
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Outgoing webhook" }).check();
  return page.getByTestId("webhook-fields");
}

/** Closes the dialog that shows the Signing secret of a new or regenerated Destination once. */
async function closeSigningSecret(page: Page): Promise<void> {
  const dialog = page.getByTestId("signing-secret-dialog");
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await expect(dialog).toHaveCount(0);
}

test.beforeAll(cleanUp);
test.afterAll(cleanUp);

test("creates outgoing webhooks in events and template mode, with Secrets and the Signing secret", async ({
  page,
}) => {
  test.setTimeout(240_000);
  await signInAdmin(page);

  // Events mode — Create the Destination: the form, the Signing secret shown once, the saved Destination.
  let fields = await newWebhook(page);
  await expect(fields.getByRole("tab", { name: "Events" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await page.getByRole("textbox", { name: "Name", exact: true }).fill(AUTOMATION);
  const events = page.getByTestId("webhook-events");
  await events.getByLabel("URL", { exact: true }).fill(`${RECEIVER}/hook/automation`);
  await events.getByRole("button", { name: "Add header" }).click();
  await events.getByLabel("Name of header 1").fill("Authorization");
  await events.getByLabel("Value of header 1").fill("Bearer {{ .Secrets.token }}");
  await page.evaluate(() => window.scrollTo(0, 0));
  await capture(page, EVENTS, "01-new-destination", { waitFor: events });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  let dialog = page.getByTestId("signing-secret-dialog");
  await expect(dialog.getByText("You will not see this secret again.")).toBeVisible();
  await expect(dialog.getByLabel("Secret", { exact: true })).toHaveValue(/^whsec_/);
  await capture(page, EVENTS, "02-signing-secret", { locator: dialog });
  await closeSigningSecret(page);
  await expect(page.getByRole("heading", { name: AUTOMATION, level: 1 })).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await capture(page, EVENTS, "03-saved-destination", {
    waitFor: page.getByRole("heading", { name: AUTOMATION, level: 1 }),
  });

  // Secrets: the Secret token added, then listed with its reference.
  const secrets = page.getByTestId("destination-secrets");
  await secrets.getByRole("button", { name: "Add secret" }).click();
  await secrets.getByLabel("Name", { exact: true }).fill("token");
  await secrets.getByLabel("Value", { exact: true }).fill("example-endpoint-token");
  await scrollToTop(secrets);
  await capture(page, EVENTS, "04-add-secret", { locator: secrets, padding: 16 });
  await secrets.getByRole("button", { name: "Save", exact: true }).click();
  await expect(secrets.getByTestId("secret-name")).toHaveText("token");
  await expect(secrets.getByTestId("secret-reference")).toHaveText("{{ .Secrets.token }}");
  await capture(page, EVENTS, "05-secrets", { locator: secrets, padding: 16 });

  // Regenerate and retire the Signing secret.
  const signing = page.getByTestId("signing-secret");
  await scrollToTop(signing);
  await signing.getByRole("button", { name: "Regenerate" }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm.getByText("Regenerate the Signing secret?")).toBeVisible();
  await capture(page, EVENTS, "06-regenerate", {
    locator: confirm,
    highlight: confirm.getByRole("button", { name: "Regenerate" }),
  });
  await confirm.getByRole("button", { name: "Regenerate" }).click();
  dialog = page.getByTestId("signing-secret-dialog");
  await expect(dialog.getByLabel("Secret", { exact: true })).toHaveValue(/^whsec_/);
  await closeSigningSecret(page);
  const previous = page.getByTestId("signing-secret-previous");
  await expect(previous).toBeVisible();
  await capture(page, EVENTS, "07-previous-secret", {
    locator: signing,
    padding: 16,
    highlight: previous.getByRole("button", { name: "Retire previous secret" }),
  });
  await previous.getByRole("button", { name: "Retire previous secret" }).click();
  await expect(previous).toHaveCount(0);

  // Template mode — Create the Destination: the requests Create and Update with the request builder.
  fields = await newWebhook(page);
  await fields.getByRole("tab", { name: "Template" }).click();
  await page.getByRole("textbox", { name: "Name", exact: true }).fill(CHAT);
  const create = page.getByRole("group", { name: "Create", exact: true });
  await expect(create.getByLabel("Method")).toHaveValue("POST");
  await create.getByLabel("URL", { exact: true }).fill(`${RECEIVER}/chat/ops/messages`);
  await page.getByRole("textbox", { name: "Body of Create" }).fill(MESSAGE);
  await create.getByRole("button", { name: "Add rule" }).click();
  await create.getByLabel("Name of rule 1").fill("id");
  await create.getByLabel("JSONPath of rule 1").fill("$.data.id");
  const update = page.getByRole("group", { name: "Update", exact: true });
  await expect(update.getByLabel("Method")).toHaveValue("PUT");
  await update
    .getByLabel("URL", { exact: true })
    .fill(`${RECEIVER}/chat/ops/messages/{{ .Response.id }}`);
  await page.getByRole("textbox", { name: "Body of Update" }).fill(MESSAGE);
  await scrollToTop(fields);
  await capture(page, TEMPLATES, "01-template-mode", { waitFor: create });
  await scrollToTop(create);
  await capture(page, TEMPLATES, "02-create-request", { locator: create, padding: 16 });
  await scrollToTop(update);
  await capture(page, TEMPLATES, "03-update-request", { locator: update, padding: 16 });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("signing-secret-dialog")).toBeVisible();
  await closeSigningSecret(page);
  await expect(page.getByRole("heading", { name: CHAT, level: 1 })).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await capture(page, TEMPLATES, "04-saved-destination", {
    waitFor: page.getByRole("heading", { name: CHAT, level: 1 }),
  });
});
