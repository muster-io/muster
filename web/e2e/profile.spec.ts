// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The profile against `muster dev`: Verification steps 4, 7, 8 and 9 of the shell and sign-in story, with every
// section of the profile, "Link OIDC" and the background re-check that ends the session.

import { expect, test } from "@playwright/test";

import {
  APP,
  GATEWAY_NAME,
  TEAM_SPEC_PASSWORD,
  adminApi,
  disableIdpUser,
  ensureGateway,
  ensureLocalUser,
  expectNoHorizontalScroll,
  freshCode,
  nextIdpUser,
  openProfile,
  removeConnection,
  scalar,
  sentCode,
  shot,
  shownSecret,
  signIn,
  signInAdmin,
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

test("edits every section of the profile", async ({ page }) => {
  const csp = watchCsp(page);
  await localUser("frank", "frank-password-1");
  await page.goto("/sign-in");
  await signInLocally(page, "frank", "frank-password-1");
  await openProfile(page);
  await expect(page).toHaveURL(`${APP}/profile`);
  await expect(page.getByText("You sign in with your login and password.")).toBeVisible();

  // Name
  await page.getByLabel("Name").fill("Frank Example");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("user-menu-name")).toHaveText("Frank Example");

  // Language: the UI switches at once, and back.
  await page.getByLabel("Language").selectOption("ru");
  await expect(
    page.getByRole("button", { name: "Меню пользователя: Frank Example" }),
  ).toBeVisible();
  await expect(page.getByRole("heading", { name: "Профиль", level: 1 })).toBeVisible();
  await shot(page, "profile-ru");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByLabel("Язык").selectOption("en");
  await expect(page.getByRole("button", { name: "User menu: Frank Example" })).toBeVisible();

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
  await page.getByText("Sign in with Example SSO").click();
  await expect(page).toHaveURL(`${APP}/sign-in?error=login_taken`);
  await expect(
    page.getByText(
      "An account with this login already exists. Sign in with it and link OIDC in your profile.",
    ),
  ).toBeVisible();
  await shot(page, "sign-in-login-taken");

  await signInLocally(page, "Alice", "alice-password-1");
  await openProfile(page);
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
  await openProfile(bob);
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
    // Opening the Profile from the user menu is the next action; the page may leave for the sign-in page meanwhile.
    await page
      .getByTestId("user-menu-name")
      .click({ timeout: 5_000 })
      .catch(() => {});
    await page
      .getByRole("menuitem", { name: "Profile", exact: true })
      .click({ timeout: 5_000 })
      .catch(() => {});
  }
  await expect(page).toHaveURL(/\/sign-in\?reason=session_ended/);
  await expect(page.getByText("Your session ended. Sign in again.")).toBeVisible();
  await shot(page, "sign-in-session-ended");
});

test("the contact for calls and SMS: unavailable without a Notification gateway, then added and confirmed through the fake gateway, in English and Russian", async ({
  page,
  browser,
}) => {
  test.setTimeout(150_000);
  const login = "gwc-petr";
  const phone = "+10000000002";
  const admin = await adminApi();
  try {
    await ensureLocalUser(admin, login, "responder");
    await removeConnection(admin, GATEWAY_NAME);

    // Without a gateway: the hint and the note, no phone field, and no button for a Responder.
    await signIn(page, login, TEAM_SPEC_PASSWORD);
    await openProfile(page);
    const section = page.getByTestId("gateway-contact-section");
    await expect(section.getByRole("heading", { name: "Contact for calls and SMS" })).toBeVisible();
    await expect(section).toContainText(
      "Muster itself does not call, send SMS or check phone numbers: a Notification gateway (a bridge for calls and SMS) does, and it does not depend on a messenger. An Admin sees the contact and can remove it, but not change it.",
    );
    await expect(section).toContainText(
      "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)",
    );
    await expect(
      section.getByRole("link", { name: /^How to connect a Notification gateway/ }),
    ).toHaveAttribute(
      "href",
      "https://muster-io.github.io/muster/latest/messengers/notification-gateways/#connect-a-notification-gateway",
    );
    await expect(section.getByRole("link", { name: "Connect a bridge" })).toHaveCount(0);
    await expect(section.getByLabel("Phone")).toHaveCount(0);
    await shot(page, "profile-contact-unavailable");

    // The Admin sees "Connect a bridge", which opens the new Connection form with the type Bridge.
    const adminPage = await browser.newPage();
    try {
      await signInAdmin(adminPage);
      await adminPage.goto("/profile");
      const connect = adminPage
        .getByTestId("gateway-contact-section")
        .getByRole("link", { name: "Connect a bridge" });
      await expect(connect).toBeVisible();
      await connect.click();
      await expect(adminPage).toHaveURL(/\/connections\/new\?type=bridge/);
      await expect(adminPage.getByRole("radio", { name: "Bridge" })).toBeChecked();
    } finally {
      await adminPage.close();
    }

    // With the gateway back: the phone field, then the code, a wrong code, the right code.
    await ensureGateway(admin);
    await page.reload();
    await expect(section.getByRole("heading", { name: "Example Gateway" })).toBeVisible();
    await section.getByLabel("Phone").fill(phone);
    await section.getByRole("button", { name: "Send code" }).click();
    await expect(section.getByTestId("gateway-code-sent")).toHaveText(
      `Code sent through the Notification gateway to ${phone}.`,
    );
    await expect(section.getByRole("button", { name: "Another number" })).toBeVisible();
    await section.getByLabel("Code").fill("000000");
    await section.getByRole("button", { name: "Confirm" }).click();
    await expect(section).toContainText("The code is wrong or no longer valid. 4 attempts left.");
    await section.getByLabel("Code").fill(await sentCode(phone));
    await section.getByRole("button", { name: "Confirm" }).click();
    await expect(section.getByTestId("gateway-contact-summary")).toHaveText(
      `${phone} · calls and SMS · confirmed`,
    );
    await expect(
      section.getByRole("button", { name: `Remove the contact ${phone}` }),
    ).toBeVisible();
    await expect(section.getByTestId("gateway-contact-note")).toHaveText(
      "From a call you can acknowledge an Alert Group only while nobody has it; otherwise the call answers “Already taken by {name}”.",
    );
    await expect(section.getByLabel("Phone")).toHaveCount(0);
    await shot(page, "profile-contact-confirmed");

    // Russian, and phone width.
    await page.getByLabel("Language").selectOption("ru");
    await expect(section.getByRole("heading", { name: "Контакт для звонков/SMS" })).toBeVisible();
    await expect(section.getByTestId("gateway-contact-summary")).toHaveText(
      `${phone} · звонок и SMS · подтверждён`,
    );
    await expect(section.getByTestId("gateway-contact-note")).toHaveText(
      "Из звонка можно подтвердить группу, только если у неё ещё нет владельца; иначе звонок ответит «уже взял(а) {name}».",
    );
    await expect(section.getByRole("button", { name: `Удалить контакт ${phone}` })).toBeVisible();
    await page.setViewportSize({ width: 360, height: 740 });
    await expectNoHorizontalScroll(page);
    await shot(page, "profile-contact-confirmed-ru-360");
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.getByLabel("Язык").selectOption("en");
  } finally {
    await ensureGateway(admin);
    await admin.dispose();
  }
});
