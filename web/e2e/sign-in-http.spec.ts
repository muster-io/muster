// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sign-in page on plain http to a host other than localhost, where the browser drops the Secure session cookie:
// the page says so above the form, which stays usable; on http://localhost it does not. Chromium resolves muster.test
// to the loopback address `muster dev` listens on, so the same server answers, but the page is not a secure context.

import { expect, test } from "@playwright/test";

import { APP, shot, watchCsp } from "./support";

const PLAIN_HTTP = "http://muster.test:8080";
const TITLE = "Sign-in needs HTTPS or localhost";
const DESCRIPTION =
  "This page is open over plain http, where the browser drops the session cookie, so signing in brings you back here. Open Muster over HTTPS, or through an SSH tunnel to localhost.";

test.use({ launchOptions: { args: ["--host-resolver-rules=MAP muster.test 127.0.0.1"] } });

test("explains on plain http to another host that sign-in needs HTTPS or localhost", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await page.goto(`${PLAIN_HTTP}/sign-in`);
  await expect(page.getByRole("heading", { name: "Sign in to Muster" })).toBeVisible();
  expect(await page.evaluate(() => window.isSecureContext)).toBe(false);
  const warning = page.getByRole("alert").filter({ hasText: TITLE });
  await expect(warning).toBeVisible();
  await expect(warning.getByText(TITLE, { exact: true })).toBeVisible();
  await expect(warning.getByText(DESCRIPTION, { exact: true })).toBeVisible();
  // Above the form, which stays usable.
  const login = page.getByLabel("Login");
  const warningBox = await warning.boundingBox();
  const loginBox = await login.boundingBox();
  expect(warningBox).not.toBeNull();
  expect(loginBox).not.toBeNull();
  expect((warningBox?.y ?? 0) + (warningBox?.height ?? 0)).toBeLessThanOrEqual(loginBox?.y ?? 0);
  await expect(login).toBeEditable();
  await expect(page.getByLabel("Password", { exact: true })).toBeEditable();
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeEnabled();
  await shot(page, "sign-in-plain-http");
  expect(csp).toEqual([]);
});

test("shows no warning on http://localhost", async ({ page }) => {
  await page.goto(`${APP}/sign-in`);
  await expect(page.getByRole("heading", { name: "Sign in to Muster" })).toBeVisible();
  expect(await page.evaluate(() => window.isSecureContext)).toBe(true);
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  await expect(page.getByText(TITLE)).toHaveCount(0);
});
