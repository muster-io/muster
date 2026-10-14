// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The main navigation against `muster dev`: Verification steps 1-6 of the compact navigation story. An Admin sees eight
// entries that fit 1280 px wide in English and Russian; "Access" groups Users, Service accounts and OIDC, the Audit log
// sits under Organization and Profile opens from the user menu; the old addresses keep working; a Responder and a
// Viewer see seven entries and are refused the admin pages.

import { expect, test, type Page } from "@playwright/test";

import {
  APP,
  adminApi,
  expectNoHorizontalScroll,
  openProfile,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const NO_PERMISSION = "You do not have permission to see this page.";
const SEVEN = [
  "Alert Groups",
  "Statistics",
  "Integrations",
  "Routes",
  "Connections",
  "Destinations",
  "Organization",
];
const EIGHT = [...SEVEN.slice(0, 6), "Access", "Organization"];

function main(page: Page, name = "Main") {
  return page.getByRole("navigation", { name });
}

function entry(page: Page, name: string, label = "Main") {
  return main(page, label).getByRole("link", { name, exact: true });
}

function sub(page: Page, section: string, name: string) {
  return page.getByRole("navigation", { name: section }).getByRole("link", { name, exact: true });
}

/** The main navigation fits: nothing of it is clipped. */
async function expectFits(page: Page, label: string) {
  const widths = await main(page, label).evaluate((el) => ({
    scroll: el.scrollWidth,
    client: el.clientWidth,
  }));
  expect(
    widths.scroll,
    `${label}: scrollWidth ${widths.scroll} > clientWidth ${widths.client}`,
  ).toBeLessThanOrEqual(widths.client);
}

/** Creates a local user with a Role and its password through the setup link. */
async function user(login: string, role: string, password: string) {
  const admin = await adminApi();
  try {
    const { token } = await admin.createUser(login, role);
    await admin.call("POST", "/api/v1/password-setups", { token, password });
  } finally {
    await admin.dispose();
  }
}

test("an Admin's main navigation has eight entries that fit 1280 px wide", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 1. Eight entries, in order, that fit; Profile opens from the user menu.
  await expect(main(page).getByRole("link")).toHaveText(EIGHT);
  await expectFits(page, "Main");
  await shot(page, "navigation-admin-1280");
  await openProfile(page);
  await expect(page).toHaveURL(`${APP}/profile`);
  await expect(page.getByRole("heading", { name: "Profile", level: 1 })).toBeVisible();

  // 2. Access leads to Users and groups Users, Service accounts and OIDC.
  await entry(page, "Access").click();
  await expect(page).toHaveURL(`${APP}/admin/users`);
  await expect(entry(page, "Access")).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("navigation", { name: "Access" }).getByRole("link")).toHaveText([
    "Users",
    "Service accounts",
    "OIDC",
  ]);
  await page
    .getByRole("table", { name: "Users" })
    .getByRole("link", { name: "admin", exact: true })
    .click();
  await expect(page).toHaveURL(/\/admin\/users\/[^/]+$/);
  await expect(page.getByRole("heading", { name: "admin", level: 1 })).toBeVisible();
  await expect(sub(page, "Access", "Users")).toHaveAttribute("data-status", "active");
  await expect(entry(page, "Access")).toHaveAttribute("aria-current", "page");
  await sub(page, "Access", "Service accounts").click();
  await expect(page).toHaveURL(`${APP}/admin/service-accounts`);
  await sub(page, "Access", "OIDC").click();
  await expect(page).toHaveURL(`${APP}/admin/oidc`);
  await expect(page.getByRole("heading", { name: "OIDC", level: 1 })).toBeVisible();
  await shot(page, "navigation-access");
  await page.goto("/admin/access");
  await expect(page).toHaveURL(`${APP}/admin/users`);

  // 3. The Audit log is the last entry of Organization, which stays marked on it.
  await entry(page, "Organization").click();
  await expect(page).toHaveURL(`${APP}/admin/organization/general`);
  const organization = page.getByRole("navigation", { name: "Organization" }).getByRole("link");
  await expect(organization.nth(-2)).toHaveText("System status");
  await expect(organization.last()).toHaveText("Audit log");
  await sub(page, "Organization", "Audit log").click();
  await expect(page).toHaveURL(`${APP}/admin/audit-log`);
  await expect(page.getByRole("heading", { name: "Audit log", level: 1 })).toBeVisible();
  await expect(entry(page, "Organization")).toHaveAttribute("aria-current", "page");
  await expect(entry(page, "Access")).not.toHaveAttribute("aria-current");

  // 4. The old addresses open their pages.
  for (const [address, heading] of [
    ["/admin/users", "Users"],
    ["/admin/service-accounts", "Service accounts"],
    ["/admin/oidc", "OIDC"],
    ["/admin/audit-log", "Audit log"],
    ["/profile", "Profile"],
  ] as const) {
    await page.goto(address);
    await expect(page.getByRole("heading", { name: heading, level: 1 })).toBeVisible();
  }
  expect(csp).toEqual([]);
});

test("the main navigation fits in Russian, and both sub-navigations at 360 px", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/profile");
  try {
    // 5. Russian: the eight entries fit; Access and Organization in Russian; no sideways scroll at 360 px.
    await page.getByLabel("Language").selectOption("ru");
    await expect(main(page, "Основная").getByRole("link")).toHaveText([
      "Группы алертов",
      "Статистика",
      "Интеграции",
      "Маршруты",
      "Подключения",
      "Места доставки",
      "Доступ",
      "Организация",
    ]);
    await expectFits(page, "Основная");
    await shot(page, "navigation-admin-1280-ru");
    await entry(page, "Доступ", "Основная").click();
    await expect(page).toHaveURL(`${APP}/admin/users`);
    await expect(page.getByRole("navigation", { name: "Доступ" }).getByRole("link")).toHaveText([
      "Пользователи",
      "Сервисные аккаунты",
      "OIDC",
    ]);
    await entry(page, "Организация", "Основная").click();
    await expect(page).toHaveURL(`${APP}/admin/organization/general`);
    await expect(
      page.getByRole("navigation", { name: "Организация" }).getByRole("link").last(),
    ).toHaveText("Журнал аудита");
    await page.setViewportSize({ width: 360, height: 740 });
    for (const [address, section, last] of [
      ["/admin/users", "Доступ", "OIDC"],
      ["/admin/audit-log", "Организация", "Журнал аудита"],
    ] as const) {
      await page.goto(address);
      await expect(main(page, "Основная").getByRole("link").last()).toHaveText("Организация");
      await expect(
        page.getByRole("navigation", { name: section }).getByRole("link").last(),
      ).toHaveText(last);
      await expectNoHorizontalScroll(page);
    }
  } finally {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/profile");
    await page.getByLabel(/^(Language|Язык)$/).selectOption("en");
    await expect(main(page)).toBeVisible();
  }
  expect(csp).toEqual([]);
});

test("a Responder and a Viewer see seven entries and no admin page", async ({ page }) => {
  const csp = watchCsp(page);
  for (const [login, role] of [
    ["nav-rita", "responder"],
    ["nav-vera", "viewer"],
  ] as const) {
    // 6. Seven entries without Access; Organization with Lookup tables and Link rules; the admin pages refuse.
    const password = `${login}-password-1`;
    await user(login, role, password);
    await signIn(page, login, password);
    await expect(main(page).getByRole("link")).toHaveText(SEVEN);
    await entry(page, "Organization").click();
    await expect(page).toHaveURL(`${APP}/admin/organization/lookup-tables`);
    await expect(
      page.getByRole("navigation", { name: "Organization" }).getByRole("link"),
    ).toHaveText(["Lookup tables", "Link rules"]);
    for (const address of [
      "/admin/access",
      "/admin/users",
      "/admin/service-accounts",
      "/admin/oidc",
      "/admin/audit-log",
    ]) {
      await page.goto(address);
      await expect(page.getByText(NO_PERMISSION)).toBeVisible();
      await expect(page.getByRole("navigation", { name: "Access" })).toHaveCount(0);
    }
    await openProfile(page);
    await expect(page).toHaveURL(`${APP}/profile`);
    await expect(page.getByRole("heading", { name: "Profile", level: 1 })).toBeVisible();
    await shot(page, `navigation-${role}-1280`);
    await page.context().clearCookies();
  }
  expect(csp).toEqual([]);
});
