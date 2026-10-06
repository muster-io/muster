// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The OIDC settings and Organization → Security against `muster dev`: Verification steps 4-8 and 10 of the admin
// pages story, with the write-only client secret, the warnings, the check through the proxy, the conflict of a stale
// save and the warning that the last active Admin keeps the Role.

import { expect, test, type Browser, type Page } from "@playwright/test";

import {
  ADMIN_LOGIN,
  ADMIN_PASSWORD,
  APP,
  Api,
  expectNoHorizontalScroll,
  nextIdpUser,
  shot,
  signInLocally,
  watchCsp,
} from "./support";

// The client secret of the demo configuration of `muster dev`; no page may show it.
const DEMO_SECRET = "muster-dev-oidc-secret";
const NOBODY = "Nobody will be able to sign in through OIDC.";

function nav(page: Page) {
  return page.getByRole("navigation", { name: "Main" });
}

async function signInAdmin(page: Page): Promise<void> {
  await page.goto("/sign-in");
  await signInLocally(page, ADMIN_LOGIN, ADMIN_PASSWORD);
  await expect(page.getByTestId("user-menu-name")).toHaveText("admin");
}

/** A day n days from today (UTC), as the date input takes it and as the warning shows it. */
function daysAhead(n: number): { iso: string; shown: string } {
  const day = new Date(Date.now() + n * 24 * 60 * 60 * 1000);
  const iso = day.toISOString().slice(0, 10);
  const shown = new Intl.DateTimeFormat("en", { dateStyle: "medium", timeZone: "UTC" }).format(
    new Date(`${iso}T00:00:00Z`),
  );
  return { iso, shown };
}

async function save(page: Page): Promise<void> {
  const saved = page.waitForResponse(
    (r) => r.url().endsWith("/api/v1/oidc-settings") && r.request().method() === "PUT",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  expect((await saved).status()).toBe(200);
  await expect(page.getByTestId("oidc-status")).toHaveText("Saved.");
}

test("edits the OIDC settings: secret expiry, an empty mapping, the proxy and the check", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await nav(page).getByRole("link", { name: "OIDC" }).click();
  await expect(page).toHaveURL(`${APP}/admin/oidc`);
  await expect(page.getByLabel("Issuer URL")).toHaveValue("http://127.0.0.1:18090");
  await expect(page.getByLabel("Client ID")).toHaveValue("muster-dev");
  // The client secret: only that it is set and when it changed.
  await expect(page.getByTestId("secret-oidc-client-secret-status")).toHaveText(/^Set, changed .+/);
  expect(await page.content()).not.toContain(DEMO_SECRET);
  await expect(page.locator('input[type="password"]')).toHaveCount(0);

  // Step 4: the expiry warning appears 10 days ahead and is gone 30 days ahead (C-03.AC-7).
  const soon = daysAhead(10);
  await page.getByLabel("Client secret expires on").fill(soon.iso);
  await save(page);
  await expect(
    page.getByText(
      `The client secret expires on ${soon.shown}. Issue a new one in the identity provider and enter it here.`,
    ),
  ).toBeVisible();
  await shot(page, "oidc-secret-expiring");
  await page.getByLabel("Client secret expires on").fill(daysAhead(30).iso);
  await save(page);
  await expect(page.getByText("The client secret expires on")).toHaveCount(0);
  await page.getByLabel("Client secret expires on").fill("");
  await save(page);

  // Step 5: no mapping and no Role for unmatched users.
  await expect(page.getByLabel("IdP group 1", { exact: true })).toHaveValue("muster-admins");
  await expect(page.getByLabel("IdP group 2", { exact: true })).toHaveValue("oncall");
  await page.getByRole("button", { name: "Remove IdP group oncall" }).click();
  await page.getByRole("button", { name: "Remove IdP group muster-admins" }).click();
  await expect(page.getByTestId("group-mapping-empty")).toHaveText("No IdP groups are mapped.");
  await page.getByLabel("Role for users without a matching group").selectOption({ label: "None" });
  await save(page);
  await expect(page.getByTestId("oidc-warning").filter({ hasText: NOBODY })).toBeVisible();
  await shot(page, "oidc-nobody-can-sign-in");
  await page.getByRole("button", { name: "Add IdP group" }).click();
  await page.getByLabel("IdP group 1", { exact: true }).fill("muster-admins");
  await page.getByLabel("Role for IdP group 1").selectOption({ label: "Admin" });
  await page.getByRole("button", { name: "Add IdP group" }).click();
  await page.getByLabel("IdP group 2", { exact: true }).fill("oncall");
  await page.getByLabel("Role for IdP group 2").selectOption({ label: "Responder" });
  await save(page);
  await expect(page.getByText(NOBODY)).toHaveCount(0);

  // Step 6: the check through the SOCKS5 proxy of `muster dev`.
  await page.getByLabel("Use a proxy").check();
  await page.getByLabel("Type").selectOption({ label: "SOCKS5" });
  await page.getByLabel("Address").fill("127.0.0.1:18092");
  await expect(page.getByTestId("secret-oidc-proxy-password-status")).toHaveText("Not set");
  await save(page);
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("oidc-check-result")).toHaveText(
    /^Connected through the proxy in \d+ ms\.$/,
  );
  await expect(page.getByText("http://127.0.0.1:18090", { exact: true })).toBeVisible();
  await shot(page, "oidc-check-proxy");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "oidc-settings-360");
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByLabel("Use a proxy").uncheck();
  await save(page);
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("oidc-check-result")).toHaveText(
    /^Connected directly in \d+ ms\.$/,
  );

  // A wrong value comes back on its field.
  await page.getByLabel("Issuer URL").fill("not a url");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByLabel("Issuer URL")).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByText("Fix the marked fields and save again.")).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("Issuer URL")).toHaveValue("http://127.0.0.1:18090");
  expect(await page.content()).not.toContain(DEMO_SECRET);
  expect(csp).toEqual([]);
});

/** Opens the Security page in a page of its own, signed in as the Admin. */
async function securityPage(browser: Browser): Promise<Page> {
  const page = await browser.newPage();
  await signInAdmin(page);
  return page;
}

test("Organization → Security saves the TOTP policy and refuses a save over a newer version", async ({
  browser,
}) => {
  const api = await Api.signIn(ADMIN_LOGIN, ADMIN_PASSWORD);
  // Both pages sign in before the policy covers the Admin, who has no TOTP.
  const first = await securityPage(browser);
  const second = await securityPage(browser);
  try {
    await nav(first).getByRole("link", { name: "Security" }).click();
    await expect(first).toHaveURL(`${APP}/admin/organization/security`);
    await expect(first.getByRole("radio", { name: "Nobody" })).toBeChecked();
    await first.getByRole("radio", { name: "Everyone" }).check();
    await first.getByRole("button", { name: "Save" }).click();
    await expect(first.getByTestId("security-status")).toHaveText("Saved.");
    await shot(first, "security");

    await second.goto(`${APP}/admin/organization/security`);
    await expect(second.getByRole("radio", { name: "Everyone" })).toBeChecked();
    await second.getByRole("radio", { name: "Nobody" }).check();
    await second.getByRole("button", { name: "Save" }).click();
    await expect(second.getByTestId("security-status")).toHaveText("Saved.");

    await first.getByRole("radio", { name: "Local users" }).check();
    await first.getByRole("button", { name: "Save" }).click();
    await expect(
      first.getByText("Someone else changed these settings. Reload to see them."),
    ).toBeVisible();
    await shot(first, "security-conflict");
    await first.getByRole("button", { name: "Reload" }).click();
    await expect(first.getByRole("radio", { name: "Nobody" })).toBeChecked();
    await expect(first.getByText("Someone else changed these settings.")).toHaveCount(0);
  } finally {
    await api.setTotpPolicy("nobody");
    await api.dispose();
    await first.close();
    await second.close();
  }
});

test("warns while the last active Admin keeps the Role, and a Responder sees none of the admin pages", async ({
  browser,
  page,
}) => {
  const csp = watchCsp(page);
  // ada signs in through the IdP as an Admin, then the bootstrap Admin becomes a Responder.
  await nextIdpUser("u-web-ada", "ada", ["muster-admins"]);
  await page.goto("/sign-in");
  await page.getByText("Sign in with Dev IdP").click();
  await expect(page.getByTestId("user-menu-name")).toHaveText("ada");
  const api = await Api.signIn(ADMIN_LOGIN, ADMIN_PASSWORD);
  const { items } = await api.call<{ items: { id: string; etag: string; name: string }[] }>(
    "GET",
    `/api/v1/users?q=${encodeURIComponent(ADMIN_LOGIN)}`,
  );
  const bootstrap = items[0]!;
  await api.call(
    "PUT",
    `/api/v1/users/${bootstrap.id}`,
    { name: bootstrap.name, email: ADMIN_LOGIN, role: "responder" },
    { "If-Match": bootstrap.etag },
  );
  await api.dispose();

  // Step 8: the IdP maps ada, now the only active Admin, to Responder.
  await nextIdpUser("u-web-ada", "ada", ["oncall"]);
  await page.goto("/api/v1/sessions/oidc/start");
  await expect(page).toHaveURL(`${APP}/`);
  await nav(page).getByRole("link", { name: "OIDC" }).click();
  const kept =
    "ada stays Admin: they are the last active Admin, and the identity provider maps them to Responder. Make another user Admin first.";
  await expect(page.getByText(kept)).toBeVisible();
  await shot(page, "oidc-last-admin-kept");

  await nav(page).getByRole("link", { name: "Users" }).click();
  await page
    .getByRole("table", { name: "Users" })
    .getByRole("link", { name: "admin", exact: true })
    .click();
  await expect(page.getByRole("heading", { name: "admin", level: 1 })).toBeVisible();
  await expect(page.getByTestId("user-role")).toHaveText("Responder");
  await page.getByLabel("Role").selectOption({ label: "Admin" });
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("user-role")).toHaveText("Admin");

  // ada signs in again with the same groups: Role sync now applies.
  await page.goto("/api/v1/sessions/oidc/start");
  await expect(page).toHaveURL(`${APP}/`);

  // Step 10: as a Responder, ada sees none of the admin pages.
  for (const entry of ["Users", "OIDC", "Security", "Audit log"]) {
    await expect(nav(page).getByRole("link", { name: entry })).toHaveCount(0);
  }
  for (const address of [
    "/admin/users",
    "/admin/oidc",
    "/admin/organization/security",
    "/admin/audit-log",
  ]) {
    await page.goto(address);
    await expect(page.getByText("You do not have permission to see this page.")).toBeVisible();
  }
  await shot(page, "no-permission");

  const admin = await browser.newPage();
  await signInAdmin(admin);
  await admin.goto(`${APP}/admin/oidc`);
  await expect(admin.getByLabel("Issuer URL")).toHaveValue("http://127.0.0.1:18090");
  await expect(admin.getByText(kept)).toHaveCount(0);
  await admin.goto(`${APP}/admin/users?q=ada`);
  const ada = admin
    .getByRole("table", { name: "Users" })
    .getByRole("row")
    .filter({ hasText: "ada" });
  await expect(ada.getByRole("cell").nth(1)).toHaveText("Responder");
  await admin.close();
  expect(csp).toEqual([]);
});
