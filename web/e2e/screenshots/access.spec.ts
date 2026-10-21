// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Sign-in" (docs/sign-in/index.md), "Sign-in with Keycloak", "TOTP" and "API tokens and automation"
// (docs/configure/api-tokens.md). The Admin creates the local Responder "Dana Lee" under Access → Users, whose password
// the spec sets through the setup link; Dana signs in in a browser of her own, sees her profile, enrols in TOTP — the
// codes come from totpCode of support.ts —, signs in again with a code, and creates a Personal access token. The Admin
// never enrols. The Admin creates the Service account "terraform" and a token for it, fills the OIDC settings with
// example values and takes them without saving, as the issuer is not routed, and sets the TOTP policy to local users.
// The policy is put back through a session signed in before it changed, since a new sign-in of the Admin would then
// have to enrol first. It deletes Dana, with her TOTP and token, and the Service account.

import { expect, test, type Browser, type Page } from "@playwright/test";

import { adminApi, type Api, freshCode, removeNamed, shownSecret, signInAdmin } from "../support";
import { capture, cardOf, nav, organizationNav, scrollToTop } from "./capture";

const SIGN_IN = "sign-in/index";
const OIDC = "sign-in/oidc-keycloak";
const TOTP = "sign-in/totp";
const TOKENS = "configure/api-tokens";
const USER = { name: "Dana Lee", login: "dana", email: "dana@example.org" };
const PASSWORD = "dana-password-1";
const SERVICE_ACCOUNT = "terraform";

test.describe.configure({ mode: "serial" });

/** An Admin session signed in before the TOTP policy changes, which can put it back. */
let keeper: Api | undefined;
type Policy = "nobody" | "local_users" | "everyone";
let policy: Policy = "nobody";

async function cleanUp(admin: Api): Promise<void> {
  const users = await admin.call<{ items: { id: string; login: string; status: string }[] }>(
    "GET",
    `/api/v1/users?search=${USER.login}`,
  );
  for (const u of users.items.filter((x) => x.login === USER.login && x.status !== "deleted")) {
    await admin.call("DELETE", `/api/v1/users/${u.id}`);
  }
  await removeNamed(admin, "/api/v1/service-accounts", SERVICE_ACCOUNT);
}

/** Puts the TOTP policy back to what it was before the spec, with the ETag read just before. */
async function restorePolicy(admin: Api): Promise<void> {
  const org = await admin.call<{ totp_required: Policy }>("GET", "/api/v1/organization");
  if (org.totp_required !== policy) {
    await admin.setTotpPolicy(policy);
  }
}

function access(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Access" }).getByRole("link", { name, exact: true });
}

/** Dana's own browser, signed out, with the page options of the screenshot project. */
async function danaPage(browser: Browser): Promise<Page> {
  const { baseURL, viewport, locale, timezoneId, colorScheme, deviceScaleFactor } =
    test.info().project.use;
  const context = await browser.newContext({
    baseURL,
    viewport,
    locale,
    timezoneId,
    colorScheme,
    deviceScaleFactor,
  });
  return context.newPage();
}

test.beforeAll(async () => {
  keeper = await adminApi();
  const org = await keeper.call<{ totp_required: Policy }>("GET", "/api/v1/organization");
  policy = org.totp_required;
  await cleanUp(keeper);
});

test.afterAll(async () => {
  if (keeper === undefined) {
    return;
  }
  try {
    await restorePolicy(keeper);
    await cleanUp(keeper);
  } finally {
    await keeper.dispose();
  }
});

test("adds a User who enrols in TOTP, tokens, the OIDC settings and the TOTP policy", async ({
  page,
  browser,
}) => {
  test.setTimeout(300_000);
  await signInAdmin(page);

  // Add a User: Access → Users → Create user, and the password setup link.
  await nav(page, "Access").click();
  await access(page, "Users").click();
  await page.getByRole("button", { name: "Create user" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill(USER.name);
  await dialog.getByLabel("Login").fill(USER.login);
  await dialog.getByLabel("Email (optional)").fill(USER.email);
  await dialog.getByLabel("Role").selectOption({ label: "Responder" });
  await capture(page, SIGN_IN, "01-create-user", {
    locator: dialog,
    highlight: dialog.getByRole("button", { name: "Create", exact: true }),
  });
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog.getByRole("heading", { name: "Password setup link" })).toBeVisible();
  const link = await dialog.getByTestId("setup-link-url").inputValue();
  await capture(page, SIGN_IN, "02-setup-link", { locator: dialog });
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const setup = new URL(link).hash.replace(/^#token=/, "");
  const res = await page.request.post("/api/v1/password-setups", {
    data: { token: setup, password: PASSWORD },
  });
  expect(res.ok(), await res.text()).toBe(true);
  await page.goto("/admin/users");
  const row = page
    .getByRole("table", { name: "Users" })
    .getByRole("row")
    .filter({ hasText: USER.login });
  await expect(row).toHaveCount(1);
  await capture(page, SIGN_IN, "03-users", { waitFor: row });

  // Dana signs in with her login and password.
  const dana = await danaPage(browser);
  await dana.goto("/sign-in");
  await dana.getByLabel("Login").fill(USER.login);
  await dana.getByLabel("Password", { exact: true }).fill(PASSWORD);
  await capture(dana, SIGN_IN, "04-sign-in", {
    highlight: dana.getByRole("button", { name: "Sign in", exact: true }),
  });
  await dana.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(dana.getByTestId("user-menu-name")).toHaveText(USER.name);

  // Your profile: the details, the language and time zone, the password, the sessions.
  await dana.getByTestId("user-menu-name").click();
  await dana.getByRole("menuitem", { name: "Profile", exact: true }).click();
  await expect(dana.getByRole("heading", { name: "Profile", level: 1 })).toBeVisible();
  await capture(dana, SIGN_IN, "05-profile", {
    waitFor: dana.getByRole("heading", { name: "Language and time zone" }),
  });
  const sessions = cardOf(dana.getByRole("heading", { name: "Sessions", exact: true }));
  await expect(sessions.getByText("1 active session.")).toBeVisible();
  await scrollToTop(cardOf(dana.getByRole("heading", { name: "Password", exact: true })));
  await capture(dana, SIGN_IN, "06-password-and-sessions", { waitFor: sessions });

  // Enrol in TOTP from the profile: the QR code and the secret, the recovery codes, TOTP on.
  const totp = cardOf(dana.getByRole("heading", { name: "Two-factor authentication (TOTP)" }));
  await totp.getByRole("button", { name: "Set up TOTP" }).click();
  const qr = dana.getByRole("img", { name: "QR code for your authenticator app" });
  await expect(qr).toBeVisible();
  const secret = await shownSecret(dana);
  await scrollToTop(totp);
  // The secret is covered; it also takes fixed characters, whose edges the cover would otherwise let show.
  const shown = (await dana.getByTestId("totp-secret").textContent()) ?? "";
  let i = 0;
  const placeholder = shown.replace(/\S/g, () => "JBSWY3DPEHPK3PXP"[i++ % 16] ?? "A");
  await capture(dana, TOTP, "01-set-up", {
    locator: totp,
    mask: [qr, dana.getByTestId("totp-secret")],
    replace: [[shown, placeholder]],
  });
  await dana.getByLabel("Code from the app").fill(await freshCode(dana, secret, 0));
  await dana.getByRole("button", { name: "Confirm" }).click();
  const codes = dana.getByTestId("recovery-codes");
  await expect(codes.getByRole("listitem")).toHaveCount(10);
  await capture(dana, TOTP, "02-recovery-codes", {
    locator: cardOf(codes),
    mask: [codes],
  });
  await dana.getByRole("button", { name: "Continue" }).click();
  await expect(
    totp.getByText(/^TOTP has been on since .+\. 10 recovery codes left\.$/),
  ).toBeVisible();
  await capture(dana, TOTP, "03-totp-on", { locator: totp });

  // The next sign-in asks for a code.
  await dana.getByTestId("user-menu-name").click();
  await dana.getByRole("menuitem", { name: "Sign out" }).click();
  // Signing out leads to the sign-in page by itself.
  await expect(dana.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  await signInPrompt(dana);
  await capture(dana, TOTP, "04-code-prompt", {
    highlight: dana.getByLabel("Code", { exact: true }),
  });
  await dana.getByLabel("Code", { exact: true }).fill(await freshCode(dana, secret, 1));
  await dana.getByRole("button", { name: "Continue" }).click();
  await expect(dana.getByTestId("user-menu-name")).toHaveText(USER.name);

  // Create a Personal access token: Dana's profile, the dialog, the token shown once.
  await dana.goto("/profile");
  const tokens = cardOf(dana.getByRole("heading", { name: "Personal access tokens" }));
  await tokens.getByRole("button", { name: "Create token" }).click();
  dialog = dana.getByRole("dialog");
  await dialog.getByLabel("Name").fill("laptop-scripts");
  for (const permission of ["alert-groups:read", "alert-groups:acknowledge"]) {
    await dialog.getByLabel(permission, { exact: true }).check();
  }
  await dialog.getByRole("button", { name: "90 days" }).click();
  await capture(dana, TOKENS, "01-create-token", {
    locator: dialog,
    highlight: dialog.getByRole("button", { name: "Create", exact: true }),
  });
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog.getByText("You will not see this token again.")).toBeVisible();
  await expect(dialog.getByTestId("token-value")).toHaveValue(/^mstr_pat_/);
  await capture(dana, TOKENS, "02-token-created", { locator: dialog });
  await dialog.getByRole("button", { name: "Done" }).click();
  await dana.context().close();

  // Create a Service account: Access → Service accounts, the dialog, its token.
  await page.goto("/admin/access");
  await access(page, "Service accounts").click();
  await page.getByRole("button", { name: "Create service account" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill(SERVICE_ACCOUNT);
  await dialog.getByLabel("Role").selectOption({ label: "Admin" });
  await capture(page, TOKENS, "03-create-service-account", {
    locator: dialog,
    highlight: dialog.getByRole("button", { name: "Create", exact: true }),
  });
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: SERVICE_ACCOUNT, level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Create token" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("pipeline");
  await dialog.getByRole("button", { name: "90 days" }).click();
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog.getByTestId("token-value")).toHaveValue(/^mstr_sat_/);
  // The token expires at the start of the day 90 days on, a time that does not follow now: it shows for the fixed now
  // of the images, Wednesday, October 14, 2026, also behind the dialog.
  const expires = page.getByTestId("token-row").getByText(/^Expires /);
  await expect(expires).toBeVisible();
  const expiry: [string, string][] = [
    [(await expires.textContent()) ?? "", "Expires Jan 12, 2027, 00:00"],
  ];
  await capture(page, TOKENS, "04-service-account-token", { locator: dialog, replace: expiry });
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await capture(page, TOKENS, "05-service-account", {
    waitFor: page.getByTestId("token-row").first(),
    replace: expiry,
  });

  // 4. Configure Muster: the OIDC settings filled with example values, taken before saving and never saved.
  await access(page, "OIDC").click();
  await expect(page.getByRole("heading", { name: "OIDC", level: 1 })).toBeVisible();
  await page.getByLabel("Issuer URL").fill("https://sso.example.org/realms/muster");
  await page.getByLabel("Provider name").fill("Keycloak");
  await page.getByLabel("Client ID").fill("muster");
  await page.getByLabel("Scopes").fill("profile email");
  await page.getByLabel("Groups claim").fill("groups");
  await expect(page.getByLabel("IdP group 1", { exact: true })).toHaveValue("muster-admins");
  await expect(page.getByLabel("IdP group 2", { exact: true })).toHaveValue("oncall");
  await capture(page, OIDC, "01-identity-provider", {
    waitFor: page.getByRole("heading", { name: "Identity provider" }),
  });
  const roles = cardOf(page.getByRole("heading", { name: "Roles", exact: true }));
  await scrollToTop(roles);
  await capture(page, OIDC, "02-roles", { waitFor: roles });
  await page.goto("/admin/access");

  // The policy: Organization → Security, TOTP required for local users, saved and put back.
  await nav(page, "Organization").click();
  await organizationNav(page, "Security").click();
  await page.getByRole("radio", { name: "Local users" }).check();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("security-status")).toHaveText("Saved.");
  await capture(page, TOTP, "05-policy", { waitFor: page.getByTestId("security-status") });
  if (keeper !== undefined) {
    await restorePolicy(keeper);
  }
});

/** Signs Dana in up to the TOTP prompt. */
async function signInPrompt(dana: Page): Promise<void> {
  await dana.getByLabel("Login").fill(USER.login);
  await dana.getByLabel("Password", { exact: true }).fill(PASSWORD);
  await dana.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(dana).toHaveURL(/\/sign-in\/totp$/);
  await expect(dana.getByLabel("Code", { exact: true })).toBeVisible();
}
