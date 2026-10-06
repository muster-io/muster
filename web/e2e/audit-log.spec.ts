// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Audit log page against `muster dev`: Verification step 9 of the admin pages story, with the default time range,
// the filters in the URL, a Secret shown only as changed and the values that deleting a user erased.

import { expect, test, type Page } from "@playwright/test";

import {
  ADMIN_LOGIN,
  APP,
  adminApi,
  expectNoHorizontalScroll,
  shot,
  signInAdmin,
  watchCsp,
} from "./support";

// The demo client secret of `muster dev` and the one this spec enters; no page may show either.
const DEMO_SECRET = "muster-dev-oidc-secret";
const NEW_SECRET = "rotated-7f3c9a1e5b";

function nav(page: Page) {
  return page.getByRole("navigation", { name: "Main" });
}

function rows(page: Page) {
  return page.getByRole("table", { name: "Audit log" }).getByRole("row");
}

/** The day n days before today in Europe/Berlin, the browser's time zone of the specs, as YYYY-MM-DD. */
function berlinDay(daysBefore: number): string {
  return new Intl.DateTimeFormat("en-CA", { timeZone: "Europe/Berlin" }).format(
    new Date(Date.now() - daysBefore * 24 * 60 * 60 * 1000),
  );
}

test("filters the Audit log and shows a Secret only as changed", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);

  // A new client secret, so that the newest oidc_settings.updated entry is the Admin's.
  await nav(page).getByRole("link", { name: "OIDC" }).click();
  await page
    .getByTestId("secret-oidc-client-secret")
    .getByRole("button", { name: "Replace" })
    .click();
  await page.getByLabel("Client secret", { exact: true }).fill(NEW_SECRET);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("oidc-status")).toHaveText("Saved.");
  await expect(page.getByLabel("Client secret", { exact: true })).toHaveCount(0);
  expect(await page.content()).not.toContain(NEW_SECRET);
  expect(await page.content()).not.toContain(DEMO_SECRET);

  await nav(page).getByRole("link", { name: "Audit log" }).click();
  await expect(page).toHaveURL(`${APP}/admin/audit-log`);
  // By default, the last 7 days.
  await expect(page.getByLabel("From", { exact: true })).toHaveValue(berlinDay(7));
  await expect(page.getByLabel("To", { exact: true })).toHaveValue("");
  await expect(rows(page).nth(1)).toBeVisible();

  await page.getByLabel("Action", { exact: true }).fill("oidc_settings.updated");
  await page.getByLabel("Action", { exact: true }).press("Enter");
  await expect(page).toHaveURL(/[?&]action=oidc_settings\.updated/);
  const newest = rows(page).nth(1);
  await expect(newest.getByTestId("audit-actor")).toHaveText("admin");
  await expect(newest).toContainText("oidc_settings.updated");
  await expect(newest).toContainText("Web UI");
  await expect(newest.getByTestId("audit-diff")).toContainText("Client secret:changed");
  expect(await page.content()).not.toContain(NEW_SECRET);
  expect(await page.content()).not.toContain(DEMO_SECRET);
  await shot(page, "audit-log-secret-changed");

  // The filter is in the URL: a reload keeps it, and so does a link.
  await page.reload();
  await expect(page.getByLabel("Action", { exact: true })).toHaveValue("oidc_settings.updated");
  await expect(rows(page).nth(1).getByTestId("audit-actor")).toHaveText("admin");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "audit-log-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  // A day range and the actor filter.
  await page.getByLabel("To", { exact: true }).fill(berlinDay(0));
  await expect(page).toHaveURL(new RegExp(`[?&]to=${berlinDay(0)}`));
  await expect(rows(page).nth(1).getByTestId("audit-actor")).toHaveText("admin");
  await page.getByLabel("From", { exact: true }).fill(berlinDay(-1));
  await page.getByLabel("To", { exact: true }).fill(berlinDay(-1));
  await expect(page.getByText("No entries match these filters.")).toBeVisible();
  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(page).toHaveURL(`${APP}/admin/audit-log`);
  await page.getByLabel("Actor", { exact: true }).selectOption({ label: `admin (${ADMIN_LOGIN})` });
  await expect(page).toHaveURL(/[?&]actor=/);
  await expect(rows(page).nth(1).getByTestId("audit-actor")).toHaveText("admin");
  expect(csp).toEqual([]);
});

// The later specs sign in through the fake IdP, which accepts any secret; the demo secret is put back all the same.
test.afterAll(async () => {
  const admin = await adminApi();
  const settings = await admin.call<
    Record<string, unknown> & { etag: string; proxy: Record<string, unknown> }
  >("GET", "/api/v1/oidc-settings");
  const {
    client_secret_status: _status,
    warnings: _warnings,
    updated_at: _updated,
    etag,
    proxy,
    ...rest
  } = settings;
  const { password_status: _password, ...proxyInput } = proxy;
  await admin.call(
    "PUT",
    "/api/v1/oidc-settings",
    { ...rest, proxy: proxyInput, client_secret: DEMO_SECRET },
    { "If-Match": etag },
  );
  await admin.dispose();
});

test("shows the values that deleting a user erased as [erased]", async ({ page }) => {
  const admin = await adminApi();
  const created = await admin.call<{ user: { id: string } }>("POST", "/api/v1/users", {
    name: "Erin Example",
    login: "erin",
    email: "erin@example.org",
    role: "responder",
  });
  await admin.call("DELETE", `/api/v1/users/${created.user.id}`);
  await admin.dispose();

  await signInAdmin(page);
  await page.goto(
    `/admin/audit-log?action=user.created&resource_type=user&resource_id=${created.user.id}`,
  );
  await expect(page.getByText(`Resource: ${created.user.id}`)).toBeVisible();
  const entry = rows(page).nth(1);
  await expect(entry).toContainText(`deleted-user-${created.user.id}`);
  const diff = entry.getByTestId("audit-diff");
  await expect(diff).toContainText("Name:");
  await expect(diff).toContainText("Login:");
  await expect(diff).toContainText("Email:");
  await expect(diff.locator('[data-erased="true"]').first()).toHaveText("[erased]");
  await expect(diff).not.toContainText("erin@example.org");
  await expect(diff).not.toContainText("No entries");
  await expect(diff).not.toContainText("Erin Example");
  await shot(page, "audit-log-erased");

  // The resource filter is cleared from its chip.
  await page.getByRole("button", { name: "Show the entries about every resource" }).click();
  await expect(page).toHaveURL(`${APP}/admin/audit-log?action=user.created&resource_type=user`);
});
