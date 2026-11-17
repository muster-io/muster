// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Users pages against `muster dev`: Verification steps 1-3 of the admin pages story, the other actions of a user's
// page (reset TOTP, convert to local, a new setup link), the filters in the URL, and the pages without their
// Permissions.

import { expect, test, type Page } from "@playwright/test";

import {
  ADMIN_LOGIN,
  APP,
  Api,
  adminApi,
  confirmContact,
  ensureGateway,
  ensureLocalUser,
  expectNoHorizontalScroll,
  nextIdpUser,
  shot,
  signInAdmin,
  totpCode,
  watchCsp,
} from "./support";

const SETUP_LINK = /^http:\/\/localhost:8080\/password-setup#token=\S+$/;

function nav(page: Page) {
  return page.getByRole("navigation", { name: "Main" });
}

/**
 * The filters of the users list. The URL changes before the list replaces the page it came from, so a bare "Role" may
 * still find the Role select of a user's page; the filters find only the list's.
 */
function filters(page: Page) {
  return page.getByRole("search", { name: "User filters" });
}

/** The row of the users table that holds a text. */
function row(page: Page, text: string) {
  return page.getByRole("table", { name: "Users" }).getByRole("row").filter({ hasText: text });
}

test("creates a user with a setup link, disables, enables and deletes it", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  for (const entry of ["Access", "Organization"]) {
    await expect(nav(page).getByRole("link", { name: entry, exact: true })).toBeVisible();
  }
  for (const entry of ["Users", "OIDC", "Audit log", "Profile"]) {
    await expect(nav(page).getByRole("link", { name: entry, exact: true })).toHaveCount(0);
  }

  await nav(page).getByRole("link", { name: "Access", exact: true }).click();
  await expect(page).toHaveURL(`${APP}/admin/users`);
  await expect(
    page.getByRole("navigation", { name: "Access" }).getByRole("link", { name: "Users" }),
  ).toHaveAttribute("data-status", "active");
  await page.getByRole("button", { name: "Create user" }).click();
  const create = page.getByRole("dialog");
  await create.getByLabel("Name").fill("Dana");
  await create.getByLabel("Login").fill("dana");
  await create.getByLabel("Role").selectOption({ label: "Viewer" });
  await create.getByRole("button", { name: "Create", exact: true }).click();
  await expect(create.getByRole("heading", { name: "Password setup link" })).toBeVisible();
  await expect(create.getByTestId("setup-link-url")).toHaveValue(SETUP_LINK);
  await expect(create.getByText("This link is shown once.")).toBeVisible();
  await expect(create.getByRole("button", { name: "Copy" })).toBeVisible();
  await shot(page, "users-setup-link");
  await create.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  const dana = row(page, "dana");
  await expect(dana).toHaveCount(1);
  await expect(dana.getByRole("cell").nth(1)).toHaveText("Viewer");
  await expect(dana.getByRole("cell").nth(2)).toHaveText("Local");
  await expect(dana.getByRole("cell").nth(3)).toHaveText("Password");
  await expect(dana.getByRole("cell").nth(4)).toHaveText("Never");
  await expect(dana.getByRole("cell").nth(5)).toHaveText("Off");
  await expect(dana.getByRole("cell").nth(6)).toHaveText("Active");
  await shot(page, "users-list");

  // The filters live in the URL.
  await filters(page).getByLabel("Role").selectOption({ label: "Viewer" });
  await expect(page).toHaveURL(/role=viewer/);
  await filters(page).getByLabel("Search").fill("dan");
  await expect(page).toHaveURL(/q=dan/);
  await expect(row(page, "dana")).toHaveCount(1);
  await expect(row(page, ADMIN_LOGIN)).toHaveCount(0);
  await page.reload();
  await expect(filters(page).getByLabel("Search")).toHaveValue("dan");
  await expect(filters(page).getByLabel("Role")).toHaveValue("viewer");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "users-list-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  await row(page, "dana").getByRole("link", { name: "Dana" }).click();
  await expect(page.getByRole("heading", { name: "Dana", level: 1 })).toBeVisible();
  await expect(page.getByTestId("user-status")).toHaveText("Active");
  await page.getByRole("button", { name: "Disable" }).click();
  await expect(page.getByTestId("user-status")).toHaveText("Disabled");
  await page.getByRole("button", { name: "Enable" }).click();
  await expect(page.getByTestId("user-status")).toHaveText("Active");
  await shot(page, "user-page");

  await page.getByRole("button", { name: "Delete" }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm.getByText(/The name becomes deleted-user-\S+/)).toBeVisible();
  await shot(page, "user-delete-confirm");
  await confirm.getByRole("button", { name: "Delete" }).click();
  await expect(page).toHaveURL(`${APP}/admin/users`);
  await filters(page).getByLabel("Role").selectOption({ label: "Viewer" });
  await expect(page).toHaveURL(`${APP}/admin/users?role=viewer`);
  await filters(page).getByLabel("Status").selectOption({ label: "Deleted" });
  await expect(page).toHaveURL(`${APP}/admin/users?role=viewer&status=deleted`);
  await expect(row(page, "deleted-user-")).toHaveCount(1);
  await expect(row(page, "deleted-user-").getByRole("cell").nth(6)).toHaveText("Deleted");
  expect(csp).toEqual([]);
});

test("resets TOTP, converts an OIDC user to local and issues a new setup link", async ({
  browser,
  page,
}) => {
  const admin = await adminApi();
  // A local user with TOTP.
  const { token } = await admin.createUser("tina");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "tina-password-1" });
  const tina = await Api.signIn("tina", "tina-password-1");
  const { secret } = await tina.call<{ secret: string }>("POST", "/api/v1/me/totp");
  await tina.call("POST", "/api/v1/me/totp/confirmation", { code: totpCode(secret) });
  await tina.dispose();
  // A user created through OIDC.
  await nextIdpUser("u-web-olga", "olga", ["oncall"]);
  const olga = await browser.newPage();
  await olga.goto(`${APP}/sign-in`);
  await olga.getByText("Sign in with Example SSO").click();
  await expect(olga.getByTestId("user-menu-name")).toHaveText("olga");
  await olga.close();
  await admin.dispose();

  await signInAdmin(page);
  await page.goto("/admin/users?q=tina");
  await row(page, "tina").getByRole("link", { name: "tina" }).click();
  await expect(page.getByTestId("user-totp")).toHaveText("On");
  await page.getByRole("button", { name: "Reset TOTP" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Reset TOTP" }).click();
  await expect(page.getByText("TOTP was reset.")).toBeVisible();
  await expect(page.getByTestId("user-totp")).toHaveText("Off");
  await page.getByRole("button", { name: "New password setup link" }).click();
  const link = page.getByRole("dialog");
  await expect(link.getByRole("heading", { name: "Password setup link" })).toBeVisible();
  await expect(link.getByTestId("setup-link-url")).toHaveValue(SETUP_LINK);
  await link.getByRole("button", { name: "Done" }).click();

  await page.goto("/admin/users?source=oidc");
  await expect(row(page, "olga").getByRole("cell").nth(2)).toHaveText("OIDC");
  await row(page, "olga").getByRole("link", { name: "olga" }).click();
  await expect(page.getByTestId("user-method")).toHaveText("OIDC");
  // The IdP decides the Role of an OIDC account while Role sync is on.
  await expect(page.getByLabel("Role")).toBeDisabled();
  await expect(page.getByRole("button", { name: "New password setup link" })).toHaveCount(0);
  await page.getByRole("button", { name: "Convert to local" }).click();
  const convert = page.getByRole("dialog");
  await expect(convert.getByText(/will no longer sign in through OIDC/)).toBeVisible();
  await convert.getByRole("button", { name: "Convert to local" }).click();
  await expect(page.getByRole("dialog").getByTestId("setup-link-url")).toHaveValue(SETUP_LINK);
  await page.getByRole("dialog").getByRole("button", { name: "Done" }).click();
  await expect(page.getByTestId("user-method")).toHaveText("Password");
  await expect(page.getByRole("button", { name: "New password setup link" })).toBeVisible();
});

test("without users:write the user's page offers no action", async ({ page }) => {
  // No Role has users:read without users:write, so the session is narrowed in the browser: the page hides the
  // actions, and the API would refuse them anyway.
  await page.route("**/api/v1/sessions/current", async (route) => {
    const response = await route.fetch();
    const body: unknown = await response.json();
    if (typeof body === "object" && body !== null && "permissions" in body) {
      const permissions = Array.isArray(body.permissions) ? body.permissions : [];
      body.permissions = permissions.filter((p) => p !== "users:write");
    }
    await route.fulfill({ response, json: body });
  });
  await signInAdmin(page);
  await page.goto("/admin/users");
  await expect(row(page, ADMIN_LOGIN)).toHaveCount(1);
  await expect(page.getByRole("button", { name: "Create user" })).toHaveCount(0);
  await row(page, ADMIN_LOGIN).getByRole("link").click();
  await expect(page.getByTestId("user-login")).toHaveText(ADMIN_LOGIN);
  for (const action of [
    "Disable",
    "Enable",
    "Delete",
    "Reset TOTP",
    "Revoke iCal feed",
    "Convert to local",
    "New password setup link",
    "Save",
  ]) {
    await expect(page.getByRole("button", { name: action })).toHaveCount(0);
  }
  await expect(page.getByLabel("Role")).toHaveCount(0);
});

test("an admin revokes a user's iCal feed", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await signInAdmin(page);
  await page.goto("/admin/users");
  await row(page, ADMIN_LOGIN).getByRole("link").click();
  await page.getByRole("button", { name: "Revoke iCal feed" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Revoke iCal feed" }).click();
  await expect(page.getByText("The iCal feed link no longer works.")).toBeVisible();
});

test("pages through the users with the cursor of the list", async ({ page }) => {
  const admin = await adminApi();
  for (let i = 0; i < 55; i++) {
    await admin.call("POST", "/api/v1/users", {
      name: `page-user-${String(i).padStart(2, "0")}`,
      login: `page-user-${String(i).padStart(2, "0")}`,
      role: "viewer",
    });
  }
  await admin.dispose();
  await signInAdmin(page);
  const pages: string[] = [];
  const offsets: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/api/v1/users?")) {
      const params = new URL(r.url()).searchParams;
      pages.push(params.get("cursor") ?? "");
      if (params.has("offset")) {
        offsets.push(r.url());
      }
    }
  });
  await page.goto("/admin/users?q=page-user");
  const rows = page.getByRole("table", { name: "Users" }).getByRole("row");
  await expect(rows).toHaveCount(51);
  await page.getByRole("button", { name: "Load more" }).click();
  await expect(rows).toHaveCount(56);
  await expect(page.getByRole("button", { name: "Load more" })).toHaveCount(0);
  // The second page is asked for by the cursor of the first, never by an offset.
  expect(new Set(pages.filter((c) => c !== "")).size).toBe(1);
  expect(offsets).toEqual([]);
});

test("a user's page shows their contacts at Notification gateways, with no way to add one, and an Admin removes one after a confirmation, which the Audit log shows", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const login = "gwu-petr";
  const phone = "+10000000003";
  const admin = await adminApi();
  let userId = "";
  try {
    userId = await ensureLocalUser(admin, login, "responder");
    await confirmContact(login, await ensureGateway(admin), phone);
  } finally {
    await admin.dispose();
  }
  await signInAdmin(page);
  await page.goto(`/admin/users/${userId}`);
  const card = page.getByTestId("user-gateway-contacts");
  await expect(card.getByRole("heading", { name: "Contacts for calls and SMS" })).toBeVisible();
  const contacts = card.getByTestId("user-gateway-contact");
  await expect(contacts).toHaveCount(1);
  await expect(contacts).toContainText("Example Gateway");
  await expect(contacts).toContainText(phone);
  await expect(contacts).toHaveAttribute("data-state", "confirmed");
  await expect(card.getByRole("button", { name: "Send code" })).toHaveCount(0);
  await expect(card.getByRole("textbox")).toHaveCount(0);
  await shot(page, "user-gateway-contacts");

  await card.getByRole("button", { name: `Remove the contact ${phone}` }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText(`Remove the contact ${phone} of ${login} at Example Gateway?`);
  await dialog.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(card.getByText("No contacts at Notification gateways.")).toBeVisible();

  await page.goto("/admin/audit-log?action=gateway_contact.deleted");
  const entries = page.getByRole("table", { name: "Audit log" }).getByRole("row");
  const deleted = entries.filter({ hasText: "gateway_contact.deleted" }).first();
  await expect(deleted).toBeVisible();
  await expect(deleted).toContainText("Contact:");
  await expect(deleted).toContainText(phone);
});

test("a user's page shows their Notification rules with the switch for an Admin, who changes them with a diff in the Audit log, and read-only without users:write", async ({
  page,
  browser,
}) => {
  test.setTimeout(120_000);
  const login = "nr-sergey";
  const phone = "+10000000006";
  const admin = await adminApi();
  let userId = "";
  try {
    userId = await ensureLocalUser(admin, login, "responder");
    await admin.setUserNotificationRules(userId, { state: "organization" });
    await confirmContact(login, await ensureGateway(admin), phone);
  } finally {
    await admin.dispose();
  }
  await signInAdmin(page);
  await page.goto(`/admin/users/${userId}`);
  const card = page.getByTestId("user-notification-rules");
  await expect(card.getByRole("heading", { name: "Notification rules" })).toBeVisible();
  await expect(card.getByTestId("rule-normal")).toHaveText(
    "Normal: a Mention in the Thread of the Root message.",
  );
  await expect(card.getByTestId("rule-important")).toHaveText(
    "Important: a Mention in the Thread and again after 5 minutes if you did not answer.",
  );
  await expect(card.getByRole("radio", { name: "As in the Organization" })).toBeChecked();
  await card.getByRole("radio", { name: "Call", exact: true }).click();
  await expect(card.getByTestId("rule-important")).toHaveText(
    `Important: a Mention in the Thread and a call to ${phone} at the same time, again after 5 minutes.`,
  );
  await expect(card.getByRole("radio", { name: "Call", exact: true })).toBeChecked();
  await shot(page, "user-notification-rules");

  // The Audit log shows the change with its diff.
  await page.goto("/admin/audit-log?action=notification_rules.updated");
  const entries = page.getByRole("table", { name: "Audit log" }).getByRole("row");
  const updated = entries.filter({ hasText: login }).first();
  await expect(updated).toContainText("notification_rules.updated");
  await expect(updated.getByTestId("audit-diff")).toContainText(
    /Also call through the Notification gateway:\s*organization\s*→\s*changed to\s*call/,
  );
  await expect(updated.getByTestId("audit-diff")).toContainText("Important:");
  await shot(page, "audit-notification-rules");

  // The Admin puts it back.
  await page.goto(`/admin/users/${userId}`);
  await card.getByRole("radio", { name: "As in the Organization" }).click();
  await expect(card.getByRole("radio", { name: "As in the Organization" })).toBeChecked();
  await expect(card.getByTestId("rule-important")).toHaveText(
    "Important: a Mention in the Thread and again after 5 minutes if you did not answer.",
  );

  // Without users:write the section is read-only: the switch cannot be pressed.
  const context = await browser.newContext();
  const reader = await context.newPage();
  await reader.route("**/api/v1/sessions/current", async (route) => {
    const response = await route.fetch();
    const body: unknown = await response.json();
    if (typeof body === "object" && body !== null && "permissions" in body) {
      const permissions = Array.isArray(body.permissions) ? body.permissions : [];
      body.permissions = permissions.filter((p) => p !== "users:write");
    }
    await route.fulfill({ response, json: body });
  });
  await signInAdmin(reader);
  await reader.goto(`/admin/users/${userId}`);
  const readOnly = reader.getByTestId("user-notification-rules");
  await expect(readOnly.getByTestId("rule-normal")).toBeVisible();
  await expect(readOnly.getByRole("radio", { name: "As in the Organization" })).toBeChecked();
  await expect(readOnly.getByRole("radio", { name: "Call", exact: true })).toBeDisabled();
  await expect(readOnly.getByRole("radio", { name: "Don't call" })).toBeDisabled();
  await context.close();
});
