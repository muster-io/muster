// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Personal access tokens against `muster dev`: Verification steps 1-4, 6 and 7 of the API tokens pages story. A token
// made in the profile is shown once, works with the API, shows its last use, is revoked; an action made with a token
// shows in the Audit log as "{user} via token {name}"; an OIDC account without an offline token sees until when its
// tokens work.

import { expect, test, type Page } from "@playwright/test";

import {
  ADMIN_LOGIN,
  ADMIN_PASSWORD,
  APP,
  Api,
  FAKE_IDP,
  expectNoHorizontalScroll,
  nextIdpUser,
  scalar,
  shot,
  signInLocally,
  watchCsp,
} from "./support";

// The API by the IPv4 loopback address, so that the last use names 127.0.0.1.
const API_V4 = "http://127.0.0.1:8080/api/v1";

/**
 * Signs the Admin in. The sign-in spec ends with a throttled source address; a success resets it, so a refused attempt
 * is made again once the wait the page names has passed.
 */
async function signInAdmin(page: Page): Promise<void> {
  await page.goto("/sign-in");
  const tooMany = page.getByText(/^Too many attempts\. Try again in (\d+) seconds?\.$/);
  const menu = page.getByTestId("user-menu-name");
  for (let attempt = 0; attempt < 5; attempt++) {
    await signInLocally(page, ADMIN_LOGIN, ADMIN_PASSWORD);
    await expect(menu.or(tooMany)).toBeVisible();
    if (await menu.isVisible()) {
      break;
    }
    const seconds = Number(/(\d+) second/.exec((await tooMany.textContent()) ?? "")?.[1] ?? "1");
    await page.waitForTimeout(seconds * 1000 + 200);
  }
  await expect(menu).toHaveText("admin");
}

function tokens(page: Page) {
  return page.getByRole("list", { name: "Personal access tokens" });
}

function tokenRow(page: Page, name: string) {
  return tokens(page)
    .getByTestId("token-row")
    .filter({ has: page.getByTestId("token-name").getByText(name, { exact: true }) });
}

/** Creates a token through the profile's dialog and returns the value it showed once. */
async function createToken(
  page: Page,
  name: string,
  permissions: string[] | "all",
  expiry?: string,
): Promise<string> {
  await page.getByRole("button", { name: "Create token" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill(name);
  if (expiry === undefined) {
    await expect(dialog.getByText("This token never expires.")).toBeVisible();
  } else {
    await dialog.getByRole("button", { name: expiry }).click();
    await expect(dialog.getByText("This token never expires.")).toHaveCount(0);
  }
  if (permissions === "all") {
    await dialog.getByRole("button", { name: "Select all" }).click();
  } else {
    for (const permission of permissions) {
      await dialog.getByLabel(permission, { exact: true }).check();
    }
  }
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog.getByText("You will not see this token again.")).toBeVisible();
  const value = await dialog.getByTestId("token-value").inputValue();
  expect(value).toMatch(/^mstr_pat_\S+$/);
  return value;
}

/** The Permissions of the signed-in user, as getMe answers them. */
async function heldPermissions(page: Page): Promise<string[]> {
  const res = await page.request.get("/api/v1/me");
  expect(res.ok()).toBe(true);
  const body: unknown = await res.json();
  const permissions =
    typeof body === "object" && body !== null && "permissions" in body ? body.permissions : [];
  return Array.isArray(permissions) ? permissions.map(String) : [];
}

async function usersWith(value: string): Promise<number> {
  const res = await fetch(`${API_V4}/users`, { headers: { Authorization: `Bearer ${value}` } });
  return res.status;
}

test("creates, uses and revokes Personal access tokens, and the Audit log names the token", async ({
  page,
  context,
}) => {
  const csp = watchCsp(page);
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await signInAdmin(page);
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Profile" })
    .click();
  await expect(page.getByRole("heading", { name: "Personal access tokens" })).toBeVisible();
  await expect(page.getByText("You have no Personal access tokens.")).toBeVisible();

  // 1. A token without expiry: the form warns, the value shows once, the list never shows it.
  await page.getByRole("button", { name: "Create token" }).click();
  const form = page.getByRole("dialog");
  // Only the Permissions the Admin holds are offered.
  await expect(form.getByRole("checkbox")).toHaveCount((await heldPermissions(page)).length);
  await shot(page, "tokens-create-dialog");
  await form.getByRole("button", { name: "Cancel" }).click();
  const first = await createToken(page, "laptop-scripts", ["users:read", "alert-groups:read"]);
  const dialog = page.getByRole("dialog");
  await shot(page, "tokens-created-once");
  await dialog.getByRole("button", { name: "Copy" }).click();
  await expect(dialog.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(first);
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const laptop = tokenRow(page, "laptop-scripts");
  await expect(laptop).toHaveCount(1);
  await expect(laptop.getByText("This token never expires.")).toBeVisible();
  await expect(laptop.getByText("Never used")).toBeVisible();
  await expect(laptop.getByText("alert-groups:read")).toBeVisible();
  await expect(laptop.getByText("users:read")).toBeVisible();
  expect(await page.content()).not.toContain(first);

  // 2. The token works with the API, and the list shows its last use and address.
  expect(await usersWith(first)).toBe(200);
  await page.reload();
  await expect(tokenRow(page, "laptop-scripts").getByTestId("token-last-use")).toHaveText(
    /^Last used .+ from 127\.0\.0\.1$/,
  );
  expect(await page.content()).not.toContain(first);

  // 3. A token with an expiry has no warning. It holds every Permission of the Admin: a token may only give a Role
  // whose Permissions it holds, and step 6 creates a Viewer with it.
  const second = await createToken(page, "with-expiry", "all", "90 days");
  await page.getByRole("dialog").getByRole("button", { name: "Done" }).click();
  const withExpiry = tokenRow(page, "with-expiry");
  await expect(withExpiry.getByText(/^Expires .+/)).toBeVisible();
  await expect(withExpiry.getByText("This token never expires.")).toHaveCount(0);
  await shot(page, "tokens-list");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "tokens-list-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  // 4. Revoke asks first; the token is gone and the API refuses it.
  await tokenRow(page, "laptop-scripts").getByRole("button", { name: "Revoke" }).click();
  const confirm = page.getByRole("dialog");
  await expect(
    confirm.getByText("Scripts that use this token stop working at once.", { exact: false }),
  ).toBeVisible();
  await confirm.getByRole("button", { name: "Revoke" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(tokenRow(page, "laptop-scripts")).toHaveCount(0);
  await expect(tokenRow(page, "with-expiry")).toHaveCount(1);
  expect(await usersWith(first)).toBe(401);

  // 6. An action made with a token shows in the Audit log with the token's name.
  const res = await fetch(`${API_V4}/users`, {
    method: "POST",
    headers: { Authorization: `Bearer ${second}`, "Content-Type": "application/json" },
    body: JSON.stringify({ name: "via-token", login: "via-token", role: "viewer" }),
  });
  expect(res.status).toBe(201);
  await page.goto("/admin/audit-log?action=user.created");
  const newest = page.getByRole("table", { name: "Audit log" }).getByRole("row").nth(1);
  await expect(newest.getByTestId("audit-actor")).toHaveText("admin via token with-expiry");
  await expect(newest).toContainText("API");
  await shot(page, "tokens-audit-log");
  expect(csp).toEqual([]);
});

test("the Permission picker offers a Responder only the Responder's Permissions", async ({
  page,
}) => {
  const admin = await Api.signIn(ADMIN_LOGIN, ADMIN_PASSWORD);
  const { token } = await admin.createUser("rita", "responder");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "rita-password-1" });
  await admin.dispose();
  await page.goto("/sign-in");
  await signInLocally(page, "rita", "rita-password-1");
  await expect(page.getByTestId("user-menu-name")).toHaveText("rita");
  await page.goto("/profile");
  await page.getByRole("button", { name: "Create token" }).click();
  const dialog = page.getByRole("dialog");
  const held = await heldPermissions(page);
  expect(held).toContain("alert-groups:acknowledge");
  expect(held).not.toContain("users:read");
  const offered = await dialog
    .getByRole("checkbox")
    .evaluateAll((boxes) =>
      boxes.map((b) => document.querySelector(`label[for="${b.id}"]`)?.textContent ?? ""),
    );
  expect(offered.toSorted()).toEqual(held.toSorted());
  await expect(dialog.getByLabel("users:read", { exact: true })).toHaveCount(0);
  await expect(dialog.getByLabel("service-accounts:write", { exact: true })).toHaveCount(0);
  await page.setViewportSize({ width: 360, height: 740 });
  await shot(page, "tokens-create-dialog-360");
  await page.setViewportSize({ width: 1280, height: 800 });
});

test.afterAll(async () => {
  await fetch(`${FAKE_IDP}/_fake/config`, {
    method: "POST",
    body: JSON.stringify({ grant_offline_access: true }),
  });
});

test("an OIDC account without an offline token sees until when its tokens work", async ({
  page,
}) => {
  const res = await fetch(`${FAKE_IDP}/_fake/config`, {
    method: "POST",
    body: JSON.stringify({ grant_offline_access: false }),
  });
  expect(res.ok).toBe(true);
  await nextIdpUser("u-web-olga", "olga", ["oncall"]);
  await page.goto("/sign-in");
  await page.getByText("Sign in with Dev IdP").click();
  await expect(page.getByTestId("user-menu-name")).toHaveText("olga");
  await page.goto("/profile");
  await expect(page.getByRole("heading", { name: "Personal access tokens" })).toBeVisible();

  // The last sign-in plus auth.oidc_token_grace, in the browser's time zone of the specs.
  const grace = Number(await scalar("SELECT oidc_token_grace_seconds FROM organizations"));
  expect(grace).toBeGreaterThan(0);
  const signedIn = await scalar("SELECT last_sign_in_at FROM users WHERE lower(login) = 'olga'");
  if (!(signedIn instanceof Date)) {
    throw new Error(`no last sign-in for olga: ${String(signedIn)}`);
  }
  const until = new Date(signedIn.getTime() + grace * 1000);
  const day = new Intl.DateTimeFormat("en", {
    dateStyle: "medium",
    timeZone: "Europe/Berlin",
  }).format(until);
  const note = page.getByTestId("token-grace");
  await expect(note).toHaveText(
    /^Your tokens work until .+ unless you sign in through OIDC again\.$/,
  );
  await expect(note).toContainText(day);
  await shot(page, "tokens-oidc-grace");
  await expect(page).toHaveURL(`${APP}/profile`);
});
