// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The profile against `muster dev`: Verification steps 4, 7, 8 and 9 of the shell and sign-in story, with every
// section of the profile, "Link OIDC" and the background re-check that ends the session.

import { expect, test, type Page } from "@playwright/test";

import {
  APP,
  adminApi,
  disableIdpUser,
  expectNoHorizontalScroll,
  freshCode,
  nextIdpUser,
  scalar,
  shot,
  shownSecret,
  signInLocally,
  sql,
  watchCsp,
} from "./support";

/** Creates a local user through the Admin and sets the password of its setup link through the API. */
async function localUser(login: string, password: string): Promise<void> {
  const admin = await adminApi();
  const { token } = await admin.createUser(login);
  await admin.call("POST", "/api/v1/password-setups", { token, password });
  await admin.dispose();
}

function nav(page: Page) {
  return page.getByRole("navigation");
}

test("edits every section of the profile", async ({ page }) => {
  const csp = watchCsp(page);
  await localUser("frank", "frank-password-1");
  await page.goto("/sign-in");
  await signInLocally(page, "frank", "frank-password-1");
  await nav(page).getByRole("link", { name: "Profile" }).click();
  await expect(page).toHaveURL(`${APP}/profile`);
  await expect(page.getByText("You sign in with your login and password.")).toBeVisible();

  // Name
  await page.getByLabel("Name").fill("Frank Example");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("user-menu-name")).toHaveText("Frank Example");

  // Language: the UI switches at once, and back.
  await page.getByLabel("Language").selectOption("ru");
  await expect(nav(page).getByRole("link", { name: "Профиль" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Профиль", level: 1 })).toBeVisible();
  await shot(page, "profile-ru");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByLabel("Язык").selectOption("en");
  await expect(nav(page).getByRole("link", { name: "Profile" })).toBeVisible();

  // Time zone
  const saved = page.waitForResponse(
    (r) => r.url().endsWith("/api/v1/me") && r.request().method() === "PUT",
  );
  await page.getByLabel("Time zone").selectOption("Asia/Tokyo");
  expect((await saved).status()).toBe(200);
  await expect(page.getByTestId("preferences-status")).toHaveText("Saved.");
  await page.reload();
  await expect(page.getByLabel("Time zone")).toHaveValue("Asia/Tokyo");

  // Password
  await page.getByLabel("Current password").fill("wrong-password-x");
  await page.getByLabel("New password", { exact: true }).fill("frank-password-2");
  await page.getByLabel("Repeat the new password").fill("frank-password-2");
  await page.getByRole("button", { name: "Change password" }).click();
  await expect(page.getByText("The current password is wrong.")).toBeVisible();
  await page.getByLabel("Current password").fill("frank-password-1");
  await page.getByLabel("New password", { exact: true }).fill("frank-password-2");
  await page.getByLabel("Repeat the new password").fill("frank-password-2");
  await page.getByRole("button", { name: "Change password" }).click();
  await expect(
    page.getByText("Your password is changed. Your other sessions were signed out."),
  ).toBeVisible();

  // TOTP: enrol, regenerate the recovery codes, remove with the password.
  await expect(page.getByText("TOTP is off.")).toBeVisible();
  await page.getByRole("button", { name: "Set up TOTP" }).click();
  const secret = await shownSecret(page);
  await page.getByLabel("Code from the app").fill(await freshCode(page, secret, 0));
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(page.getByText("Save these recovery codes")).toBeVisible();
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(
    page.getByText(/^TOTP has been on since .+\. 10 recovery codes left\.$/),
  ).toBeVisible();
  await page.getByRole("button", { name: "Regenerate recovery codes" }).click();
  await page.getByLabel("Code from the app").fill(await freshCode(page, secret, 1));
  await page.getByRole("button", { name: "Regenerate", exact: true }).click();
  await expect(page.getByTestId("recovery-codes").getByRole("listitem")).toHaveCount(10);
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Remove TOTP" }).click();
  await page.getByLabel("Current password").last().fill("frank-password-2");
  await page.getByRole("button", { name: "Remove TOTP" }).click();
  await expect(page.getByText("TOTP is off.")).toBeVisible();

  // Sessions, then the layout at 360 px.
  await expect(page.getByText("This session")).toBeVisible();
  await shot(page, "profile");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "profile-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  await page.getByRole("button", { name: "Sign out everywhere" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Sign out everywhere" }).click();
  await expect(page).toHaveURL(`${APP}/sign-in`);
  expect(csp).toEqual([]);
});

test("refuses a taken login, links OIDC after the warning and ends the session the IdP refuses", async ({
  browser,
  page,
}) => {
  const csp = watchCsp(page);
  await localUser("Alice", "alice-password-1");
  await nextIdpUser("u-web-alice", "alice", ["oncall"]);
  await page.goto("/sign-in");
  await page.getByText("Sign in with Dev IdP").click();
  await expect(page).toHaveURL(`${APP}/sign-in?error=login_taken`);
  await expect(
    page.getByText(
      "An account with this login already exists. Sign in with it and link OIDC in your profile.",
    ),
  ).toBeVisible();
  await shot(page, "sign-in-login-taken");

  await signInLocally(page, "Alice", "alice-password-1");
  await nav(page).getByRole("link", { name: "Profile" }).click();
  await page.getByRole("button", { name: "Link OIDC" }).click();
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByText(
      "After linking you will sign in only through OIDC; your password will be removed.",
    ),
  ).toBeVisible();
  await shot(page, "profile-link-warning");
  await dialog.getByRole("button", { name: "Continue" }).click();
  await expect(page).toHaveURL(`${APP}/profile`);
  await expect(page.getByTestId("sign-in-method")).toHaveText("You sign in through OIDC.");
  await expect(page.getByRole("heading", { name: "Password" })).toHaveCount(0);
  expect(csp).toEqual([]);

  // Bob's link of Alice's identity is refused.
  await localUser("bob", "bob-password-123");
  const bob = await browser.newPage();
  await bob.goto(`${APP}/sign-in`);
  await signInLocally(bob, "bob", "bob-password-123");
  await nav(bob).getByRole("link", { name: "Profile" }).click();
  await bob.getByRole("button", { name: "Link OIDC" }).click();
  await bob.getByRole("dialog").getByRole("button", { name: "Continue" }).click();
  await expect(bob).toHaveURL(`${APP}/profile?error=identity_linked_elsewhere`);
  await expect(
    bob.getByText("This identity already belongs to another Muster account, so it was not linked."),
  ).toBeVisible();
  await bob.close();

  // The IdP disables Alice; her re-check is made due, and her next action lands on the sign-in page.
  await disableIdpUser("u-web-alice");
  await sql(
    "UPDATE oidc_checks SET deadline = now() WHERE user_id = (SELECT id FROM users WHERE lower(login) = 'alice')",
  );
  // Asking the API would take the reason out of the browser's next answer, so the database says when it happened.
  await expect
    .poll(
      () =>
        scalar(
          "SELECT count(*)::int FROM sessions s JOIN users u ON u.id = s.user_id WHERE lower(u.login) = 'alice' AND s.ended_at IS NULL",
        ),
      { timeout: 30_000 },
    )
    .toBe(0);
  if (!page.url().includes("/sign-in")) {
    await nav(page)
      .getByRole("link", { name: "Profile" })
      .click({ timeout: 5_000 })
      .catch(() => {});
  }
  await expect(page).toHaveURL(/\/sign-in\?reason=session_ended/);
  await expect(page.getByText("Your session ended. Sign in again.")).toBeVisible();
  await shot(page, "sign-in-session-ended");
});
