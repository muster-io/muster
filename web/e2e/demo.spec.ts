// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The README demo, recorded by make demo with playwright.demo.config.ts and left out of make e2e. With the Alert Group
// list open, the fake Alertmanager sends a critical alert of the service checkout; the new Alert Group appears, is
// opened from the list and acknowledged from its page, its Timeline is shown, and it resolves when Alertmanager sends
// the alert as resolved. The pauses give the viewer time to read; the whole run stays under 30 seconds of video. The
// Admin signs in through the API in the page's own context, so the recording starts on the list, not on the form. A
// drawn pointer shows where the clicks land, as the video does not record the mouse.

import { expect, test, type Locator, type Page } from "@playwright/test";

import { ADMIN_LOGIN, ADMIN_PASSWORD, fakeIntegration, fam, notify } from "./support";

const ALERT = {
  labels: {
    service: "checkout",
    severity: "critical",
    environment: "production",
    cluster: "prod-eu-1",
  },
  annotations: {
    summary: "Error rate of checkout is above 5 %",
    description: "5xx responses are 7.2 % of all requests over the last 5 minutes.",
  },
};

let integrationId = "";

test.beforeAll(async () => {
  integrationId = await fakeIntegration("demo");
  await fam("PUT", "/groups/demo", {
    receiver: "demo",
    route: "{}",
    labels: { alertname: "HighErrorRate" },
  });
});

/** Draws a pointer that follows the mouse, which the video does not record. */
async function drawPointer(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.addEventListener("DOMContentLoaded", () => {
      const dot = document.createElement("div");
      Object.assign(dot.style, {
        position: "fixed",
        left: "0",
        top: "0",
        width: "18px",
        height: "18px",
        margin: "-9px 0 0 -9px",
        borderRadius: "50%",
        background: "rgba(239, 68, 68, 0.55)",
        border: "2px solid rgba(255, 255, 255, 0.9)",
        boxShadow: "0 0 0 1px rgba(0, 0, 0, 0.35)",
        pointerEvents: "none",
        zIndex: "2147483647",
        transform: "translate(-100px, -100px)",
      });
      document.body.append(dot);
      window.addEventListener(
        "mousemove",
        (e) => {
          dot.style.transform = `translate(${e.clientX}px, ${e.clientY}px)`;
        },
        { capture: true, passive: true },
      );
    });
  });
}

/** Moves the pointer to the element in visible steps, waits a moment and clicks it. */
async function glideAndClick(page: Page, target: Locator): Promise<void> {
  await target.scrollIntoViewIfNeeded();
  const box = await target.boundingBox();
  if (box !== null) {
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: 20 });
    await page.waitForTimeout(400);
  }
  await target.click();
}

test("an Alert Group arrives, is acknowledged from its page and resolves", async ({ page }) => {
  await drawPointer(page);
  const signedIn = await page.request.post("/api/v1/sessions", {
    data: { login: ADMIN_LOGIN, password: ADMIN_PASSWORD },
  });
  expect(signedIn.status(), await signedIn.text()).toBe(201);

  await page.goto("/alert-groups");
  await expect(page.getByTestId("user-menu-name")).toBeVisible();
  await page.mouse.move(640, 300);
  await page.waitForTimeout(1500);

  // Alertmanager sends the alert; the list announces the new Alert Group without moving, and shows it on request.
  await fam("PUT", "/groups/demo/alerts/a1", ALERT);
  await notify(integrationId, "demo", { reason: "first notification" });
  const announced = page.getByTestId("new-alert-groups");
  await expect(announced).toBeVisible({ timeout: 10_000 });
  await page.waitForTimeout(1200);
  await glideAndClick(page, announced);
  const row = page.getByRole("link", { name: /^#\d+$/ }).first();
  await expect(row).toBeVisible();
  await page.waitForTimeout(1800);

  // The Alert Group page, opened from the list, then acknowledged.
  await glideAndClick(page, row);
  const status = page.getByTestId("alert-group-status");
  await expect(status).toHaveText("Firing");
  await page.waitForTimeout(2000);
  await glideAndClick(page, page.getByRole("button", { name: "Acknowledge", exact: true }));
  await expect(status).toHaveText("Acknowledged");
  await page.waitForTimeout(2000);

  // The Timeline records what happened.
  await page.getByTestId("timeline-entry").first().scrollIntoViewIfNeeded();
  await page.waitForTimeout(2500);

  // Alertmanager sends the alert as resolved; the page follows.
  await page.evaluate(() => {
    window.scrollTo({ top: 0, behavior: "smooth" });
  });
  await page.waitForTimeout(800);
  await fam("PUT", "/groups/demo/alerts/a1", { ...ALERT, status: "resolved" });
  await notify(integrationId, "demo", { reason: "resolved" });
  await expect(status).toHaveText("Resolved", { timeout: 10_000 });
  await page.waitForTimeout(3000);
});
