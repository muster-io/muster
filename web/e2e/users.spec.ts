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

/** The row of the users table that holds a text. */
function row(page: Page, text: string) {
  return page.getByRole("table", { name: "Users" }).getByRole("row").filter({ hasText: text });
}

test("creates a user with a setup link, disables, enables and deletes it", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  for (const entry of ["Users", "OIDC", "Security", "Audit log"]) {
    await expect(nav(page).getByRole("link", { name: entry })).toBeVisible();
  }

  await nav(page).getByRole("link", { name: "Users" }).click();
  await expect(page).toHaveURL(`${APP}/admin/users`);
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
  await page.getByLabel("Role").selectOption({ label: "Viewer" });
  await expect(page).toHaveURL(/role=viewer/);
  await page.getByLabel("Search").fill("dan");
  await expect(page).toHaveURL(/q=dan/);
  await expect(row(page, "dana")).toHaveCount(1);
  await expect(row(page, ADMIN_LOGIN)).toHaveCount(0);
  await page.reload();
  await expect(page.getByLabel("Search")).toHaveValue("dan");
  await expect(page.getByLabel("Role")).toHaveValue("viewer");
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
  await page.getByLabel("Role").selectOption({ label: "Viewer" });
  await page.getByLabel("Status").selectOption({ label: "Deleted" });
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
  await olga.getByText("Sign in with Dev IdP").click();
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
    "Convert to local",
    "New password setup link",
    "Save",
  ]) {
    await expect(page.getByRole("button", { name: action })).toHaveCount(0);
  }
  await expect(page.getByLabel("Role")).toHaveCount(0);
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
