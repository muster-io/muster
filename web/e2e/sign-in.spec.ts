// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sign-in pages against `muster dev`: Verification steps 1-3, 5, 6 and 9 of the shell and sign-in story, with the
// local and the OIDC sign-in, the refusals, the password setup links and the second factor.

import { expect, test } from "@playwright/test";

import {
  ADMIN_LOGIN,
  ADMIN_PASSWORD,
  APP,
  adminApi,
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

test("opens the sign-in page, signs the Admin in and shows the recovery banner live", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await page.goto("/");
  await expect(page).toHaveURL(`${APP}/sign-in`);
  await expect(page.getByRole("heading", { name: "Sign in to Muster" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  await expect(page.getByText("Sign in with Dev IdP")).toBeVisible();
  await shot(page, "sign-in-light");
  // A reload shows the dark theme without the transitions of the switch.
  await page.emulateMedia({ colorScheme: "dark" });
  await page.reload();
  await expect(page.getByText("Sign in with Dev IdP")).toBeVisible();
  await shot(page, "sign-in-dark");
  await page.emulateMedia({ colorScheme: "light" });

  await signInLocally(page, ADMIN_LOGIN, ADMIN_PASSWORD);
  await expect(page).toHaveURL(`${APP}/`);
  await expect(page.getByTestId("user-menu-name")).toHaveText("admin");

  // The notice starts and ends through its data, without a reload; the hint arrives within the 5 s check interval.
  await sql("UPDATE runtime_state SET recovery_until = now() + interval '1 minute'");
  const until = await scalar("SELECT recovery_until FROM runtime_state");
  if (!(until instanceof Date)) {
    throw new Error(`recovery_until is ${String(until)}`);
  }
  const time = new Intl.DateTimeFormat("en", {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone: "Europe/Berlin",
  }).format(until);
  const banner = page.getByText(
    `Muster is recovering after downtime. Data may be incomplete until ${time}.`,
  );
  await expect(banner).toBeVisible({ timeout: 15_000 });
  await shot(page, "home-recovery-banner");
  await sql("UPDATE runtime_state SET recovery_until = now() - interval '1 second'");
  await expect(banner).toBeHidden({ timeout: 15_000 });
  expect(csp).toEqual([]);
});

test("keeps return_to inside the application", async ({ page }) => {
  const csp = watchCsp(page);
  await page.goto("/profile");
  await expect(page).toHaveURL(`${APP}/sign-in?return_to=%2Fprofile`);
  await signInLocally(page, ADMIN_LOGIN, ADMIN_PASSWORD);
  await expect(page).toHaveURL(`${APP}/profile`);
  await page.getByRole("button", { name: /admin/ }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page).toHaveURL(`${APP}/sign-in`);

  await page.goto("/sign-in?return_to=//evil.example/x");
  await signInLocally(page, ADMIN_LOGIN, ADMIN_PASSWORD);
  await expect(page).toHaveURL(`${APP}/`);
  expect(csp).toEqual([]);
});

test("shows the refusal of a wrong password", async ({ page }) => {
  await page.goto("/sign-in");
  await signInLocally(page, ADMIN_LOGIN, "not-the-password");
  await expect(page.getByText("Wrong login, password or code.")).toBeVisible();
  await shot(page, "sign-in-refusal");
  // A success resets the throttle of the account and the address.
  await signInLocally(page, ADMIN_LOGIN, ADMIN_PASSWORD);
  await expect(page.getByTestId("user-menu-name")).toHaveText("admin");
});

test("signs in through the fake IdP and shows the refusal texts of the callback", async ({
  page,
}) => {
  await nextIdpUser("u-web-oscar", "oscar", ["oncall"]);
  await page.goto("/sign-in");
  await page.getByText("Sign in with Dev IdP").click();
  await expect(page).toHaveURL(`${APP}/`);
  await expect(page.getByTestId("user-menu-name")).toHaveText("oscar");
  await page.getByRole("button", { name: /oscar/ }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page).toHaveURL(`${APP}/sign-in`);

  await nextIdpUser("u-web-mallory", "mallory", ["contractors"]);
  await page.getByText("Sign in with Dev IdP").click();
  await expect(page).toHaveURL(`${APP}/sign-in?error=no_access`);
  await expect(
    page.getByText("You have no access to Muster. Contact your administrator."),
  ).toBeVisible();
  await shot(page, "sign-in-no-access");

  for (const [code, text] of [
    ["account_disabled", "Your account is disabled. Contact your administrator."],
    ["oidc_disabled", "OIDC sign-in is turned off. Sign in with your login and password."],
    ["invalid_request", "The sign-in did not complete. Try again."],
    [
      "idp_error",
      "The identity provider could not be reached or refused the request. Try again later.",
    ],
  ] as const) {
    await page.goto(`/sign-in?error=${code}`);
    await expect(page.getByText(text)).toBeVisible();
  }
});

test("completes a setup link and shows the texts of a used and an expired link", async ({
  browser,
  page,
}) => {
  const admin = await adminApi();
  const dave = await admin.createUser("dave");
  await page.goto(`/password-setup#token=${dave.token}`);
  // The page takes the token out of the address.
  await expect(page).toHaveURL(`${APP}/password-setup`);
  await page.getByLabel("New password").fill("dave-password-1");
  await page.getByLabel("Repeat the password").fill("dave-password-1");
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(page.getByText("Your password is set.")).toBeVisible();

  const again = await browser.newPage();
  await again.goto(`${APP}/password-setup#token=${dave.token}`);
  await again.getByLabel("New password").fill("dave-password-2");
  await again.getByLabel("Repeat the password").fill("dave-password-2");
  await again.getByRole("button", { name: "Set password" }).click();
  await expect(
    again.getByText("This link was already used. Ask your administrator for a new one."),
  ).toBeVisible();
  await again.close();

  const erin = await admin.createUser("erin");
  await sql(
    "UPDATE password_setups SET expires_at = now() - interval '1 minute' WHERE user_id = (SELECT id FROM users WHERE login = 'erin')",
  );
  // A link opens in a new tab, as from a message.
  const expired = await browser.newPage();
  await expired.goto(`${APP}/password-setup#token=${erin.token}`);
  await expired.getByLabel("New password").fill("erin-password-1");
  await expired.getByLabel("Repeat the password").fill("erin-password-1");
  await expired.getByRole("button", { name: "Set password" }).click();
  await expect(
    expired.getByText("This link has expired. Ask your administrator for a new one."),
  ).toBeVisible();
  await shot(expired, "password-setup-expired");
  await expired.close();
  await admin.dispose();
});

test("TOTP required for everyone: a new user reaches only the enrolment, then signs in with a code", async ({
  page,
}) => {
  const admin = await adminApi();
  await admin.setTotpPolicy("everyone");
  try {
    const carol = await admin.createUser("carol");
    await page.goto(`/password-setup#token=${carol.token}`);
    await page.getByLabel("New password").fill("carol-password-1");
    await page.getByLabel("Repeat the password").fill("carol-password-1");
    await page.getByRole("button", { name: "Set password" }).click();
    await page.getByRole("link", { name: "Sign in" }).click();
    await expect(page).toHaveURL(`${APP}/sign-in`);

    await signInLocally(page, "carol", "carol-password-1");
    await expect(page).toHaveURL(`${APP}/totp-enrolment`);
    for (const address of ["/profile", "/", "/sign-in"]) {
      await page.goto(address);
      await expect(page).toHaveURL(`${APP}/totp-enrolment`);
    }
    await expect(
      page.getByRole("img", { name: "QR code for your authenticator app" }),
    ).toBeVisible();
    const secret = await shownSecret(page);
    expect(secret).toMatch(/^[A-Z2-7]{16,}$/);
    await shot(page, "totp-enrolment");
    await page.setViewportSize({ width: 360, height: 740 });
    await expectNoHorizontalScroll(page);
    await shot(page, "totp-enrolment-360");
    await page.setViewportSize({ width: 1280, height: 800 });

    await page.getByLabel("Code from the app").fill(await freshCode(page, secret, 0));
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect(page.getByText("Save these recovery codes")).toBeVisible();
    const codes = page.getByTestId("recovery-codes").getByRole("listitem");
    await expect(codes).toHaveCount(10);
    const recovery = (await codes.first().textContent()) ?? "";
    await shot(page, "totp-recovery-codes");
    await page.getByRole("button", { name: "Continue" }).click();
    await expect(page).toHaveURL(`${APP}/`);
    await expect(page.getByTestId("user-menu-name")).toHaveText("carol");

    // With TOTP, the code is asked after the password, then the recovery code works instead.
    await page.getByRole("button", { name: /carol/ }).click();
    await page.getByRole("menuitem", { name: "Sign out" }).click();
    await signInLocally(page, "carol", "carol-password-1");
    await expect(page).toHaveURL(`${APP}/sign-in/totp`);
    await expect(page.getByText("Enter the code from your authenticator app.")).toBeVisible();
    await shot(page, "sign-in-totp");
    await page.getByLabel("Code", { exact: true }).fill(await freshCode(page, secret, 1));
    await page.getByRole("button", { name: "Continue" }).click();
    await expect(page).toHaveURL(`${APP}/`);

    await page.getByRole("button", { name: /carol/ }).click();
    await page.getByRole("menuitem", { name: "Sign out" }).click();
    await signInLocally(page, "carol", "carol-password-1");
    await page.getByRole("button", { name: "Use a recovery code instead" }).click();
    await page.getByLabel("Recovery code").fill(recovery);
    await page.getByRole("button", { name: "Continue" }).click();
    await expect(page).toHaveURL(`${APP}/`);
  } finally {
    await admin.setTotpPolicy("nobody");
    await admin.dispose();
  }
});

test("the sign-in page scrolls vertically only at 360 px", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  await page.goto("/sign-in?error=login_taken");
  await expect(
    page.getByText(
      "An account with this login already exists. Sign in with it and link OIDC in your profile.",
    ),
  ).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "sign-in-360");
});

// Last: the failures slow down later sign-ins from this address.
test("shows how long to wait after too many attempts", async ({ page }) => {
  await page.goto("/sign-in");
  const tooMany = page.getByText(/^Too many attempts\. Try again in \d+ seconds?\.$/);
  for (let i = 0; i < 6 && !(await tooMany.isVisible()); i++) {
    await signInLocally(page, "nobody-here", "wrong-password");
    await expect(page.getByText(/Wrong login, password or code\.|Too many attempts/)).toBeVisible();
  }
  await expect(tooMany).toBeVisible();
});
