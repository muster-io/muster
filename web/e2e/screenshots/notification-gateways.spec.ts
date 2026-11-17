// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Notification gateways" (docs/messengers/notification-gateways.md): the Connection page of a
// gateway, and the profile's confirmed contact. The Admin connects the Notification gateway "Phone calls", on
// https://bridge.example.org with the token of the fake bridge's gateway bot, polling, which muster dev routes to the
// fake, and checks it; then adds the contact +10000000001 in their profile and confirms it with the code the fake
// gateway received. The demo gateway "Example Gateway" is removed while the spec runs, so that the profile shows one
// gateway, and made again after it. The spec removes what it created.

import { expect, test } from "@playwright/test";

import {
  adminApi,
  BRIDGE_EXAMPLE_URL,
  checkConnection,
  ensureGateway,
  GATEWAY_NAME,
  GATEWAY_TOKEN,
  openProfile,
  removeConnection,
  resetFakeBridge,
  sentCode,
  signInAdmin,
} from "../support";
import { capture, nav } from "./capture";

const DOCS = "messengers/notification-gateways";
const GATEWAY = "Phone calls";
const CONTACT = "+10000000001";

test.describe.configure({ mode: "serial" });

async function cleanUp(): Promise<void> {
  const admin = await adminApi();
  try {
    const mine = await admin.call<{ items: { id: string }[] }>(
      "GET",
      "/api/v1/me/gateway-contacts",
    );
    for (const contact of mine.items) {
      await admin.call("DELETE", `/api/v1/me/gateway-contacts/${contact.id}`);
    }
    await removeConnection(admin, GATEWAY);
  } finally {
    await admin.dispose();
  }
  await resetFakeBridge();
}

test.beforeAll(async () => {
  await resetFakeBridge();
  const admin = await adminApi();
  try {
    await removeConnection(admin, GATEWAY);
    await removeConnection(admin, GATEWAY_NAME);
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  await cleanUp();
  const admin = await adminApi();
  try {
    await ensureGateway(admin);
  } finally {
    await admin.dispose();
  }
});

test("connects a Notification gateway and confirms a contact", async ({ page }) => {
  test.setTimeout(180_000);
  const admin = await adminApi();
  try {
    const created = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
      type: "bridge",
      name: GATEWAY,
      base_url: BRIDGE_EXAMPLE_URL,
      bot_token: GATEWAY_TOKEN,
      event_delivery: "poll",
      proxy: { enabled: false },
      limiter: { limit: 10, per_seconds: 1 },
    });
    await checkConnection(admin, created.id);

    await signInAdmin(page);

    // The Connection page of the gateway: its role, its health and what its bridge declares.
    await nav(page, "Connections").click();
    await page
      .getByRole("row")
      .filter({ hasText: GATEWAY })
      .getByRole("link", { name: GATEWAY })
      .click();
    await expect(page.getByRole("heading", { name: GATEWAY, level: 1 })).toBeVisible();
    await page.getByRole("button", { name: "Check connection" }).click();
    const health = page.getByTestId("gateway-health");
    await expect(health.getByTestId("gateway-roles")).toHaveText("Roles: Notification gateway");
    await expect(health).toContainText("Healthy");
    await capture(page, DOCS, "01-gateway-connection", { locator: health, padding: 16 });

    // The profile: the contact is added, the code the gateway received is typed back, and the contact is confirmed.
    await openProfile(page);
    const section = page.getByTestId("gateway-contact-section");
    await expect(section.getByRole("heading", { name: GATEWAY })).toBeVisible();
    await section.getByRole("textbox", { name: "Phone" }).fill(CONTACT);
    await section.getByRole("button", { name: "Send code" }).click();
    await expect(section.getByTestId("gateway-code-sent")).toHaveText(
      `Code sent through the Notification gateway to ${CONTACT}.`,
    );
    await section.getByRole("textbox", { name: "Code" }).fill(await sentCode(CONTACT));
    await section.getByRole("button", { name: "Confirm" }).click();
    await expect(section.getByTestId("gateway-contact-summary")).toHaveText(
      `${CONTACT} · calls and SMS · confirmed`,
    );
    await expect(section.getByTestId("gateway-contact-note")).toBeVisible();
    // The note below the contact shows the placeholder {name} of its "Already taken by …" text as it is, which the image
    // leaves out.
    await capture(page, DOCS, "02-confirmed-contact", {
      locator: section,
      padding: 16,
      hide: section.getByTestId("gateway-contact-note"),
    });
  } finally {
    await admin.dispose();
  }
});
