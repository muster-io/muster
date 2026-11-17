// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Home page against `muster dev` (Verification of the story "Schedules on the Team page, On call now on the Home
// page…", scenario 5 of C-27): signing in lands on it at `/`, the navigation has no entry for it and the logo leads back
// to it; "On call now" with "All Teams" and "By the main Schedule of each Team" shows devops — @ivan, "Main", until
// Monday 10:00; payments — @petr "unreachable in Mattermost" and "next: gap"; search — "No Schedule"; "My Teams" shows
// only the Teams of the signed-in User; in Russian too, and at 360 px without horizontal scrolling. The page is compared
// with what the API says about the same moment.

import { type Locator, type Page, expect, test } from "@playwright/test";

import {
  APP,
  ON_CALL,
  onCallNames,
  TEAM_SPEC_PASSWORD,
  adminApi,
  berlinStamp,
  ensureLocalUser,
  expectNoHorizontalScroll,
  removeOnCall,
  seedOnCall,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const NAMES = onCallNames();
const RU_USER = "home-ru";
const DOCS_ORIGIN = "https://muster-io.github.io";

interface TeamOnCall {
  team: { slug: string };
  user: { login: string } | null;
  until: string | null;
  schedule: { name: string } | null;
}

test.beforeAll(async () => {
  test.setTimeout(120_000);
  const admin = await adminApi();
  try {
    await seedOnCall(admin);
    // A Responder: a second Admin would stop oidc-settings.spec.ts from finding the last active Admin.
    await ensureLocalUser(admin, RU_USER, "responder");
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    await removeOnCall(admin);
  } finally {
    await admin.dispose();
  }
});

const row = (page: Page, slug: string): Locator =>
  page.locator(`[data-testid="on-call-row"][data-team="${slug}"]`);

async function oracle(): Promise<Map<string, TeamOnCall>> {
  const admin = await adminApi();
  try {
    const now = await admin.call<{ items: TeamOnCall[] }>("GET", "/api/v1/on-call-now");
    return new Map(now.items.map((item) => [item.team.slug, item]));
  } finally {
    await admin.dispose();
  }
}

test("signing in lands on the Home page, which has no entry in the navigation and which the logo leads back to", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await expect(page).toHaveURL(`${APP}/`);
  await expect(page.getByRole("heading", { name: "Home", level: 1 })).toBeVisible();
  await expect(
    page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Home" }),
  ).toHaveCount(0);

  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Alert Groups" })
    .click();
  await expect(page).toHaveURL(`${APP}/alert-groups`);
  await page
    .getByRole("banner")
    .getByRole("link", { name: /^Muster/ })
    .click();
  await expect(page).toHaveURL(`${APP}/`);
  await expect(page.getByRole("heading", { name: "On call now", level: 2 })).toBeVisible();
  expect(csp).toEqual([]);
});

test("On call now shows each Team's On-call with the marks, and My Teams only the Teams of the User", async ({
  page,
}) => {
  const csp = watchCsp(page);
  const api = await oracle();
  const devops = api.get(NAMES.devops);
  expect(devops?.user?.login, "ivan is On-call in devops").toBe(ON_CALL.ivan);
  expect(devops?.until, "devops has an end of the Shift").not.toBeNull();
  expect(api.get(NAMES.payments)?.user?.login).toBe(ON_CALL.petr);
  expect(api.get(NAMES.search)?.schedule).toBeNull();

  await signInAdmin(page);
  await page.goto("/");
  const section = page.getByTestId("on-call-now");
  await expect(section.getByRole("heading", { name: "On call now", level: 2 })).toBeVisible();
  await expect(section.getByRole("button", { name: "All Teams" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(section.getByText("By the main Schedule of each Team.")).toBeVisible();
  const learn = section.getByRole("link", { name: "Learn more: On call now (opens in a new tab)" });
  const target = new URL((await learn.getAttribute("href")) ?? "");
  expect(target.origin).toBe(DOCS_ORIGIN);
  expect(target.pathname).toBe("/muster/latest/concepts/schedules/");

  const devopsRow = row(page, NAMES.devops);
  await expect(devopsRow).toContainText("@ivan");
  await expect(devopsRow).toContainText("Main");
  await expect(devopsRow.getByTestId("on-call-until")).toHaveText(
    `until ${berlinStamp(devops?.until ?? "")}`,
  );
  await expect(devopsRow.getByTestId("on-call-until")).toHaveText(/^until Mon 10:00$/);
  await expect(devopsRow.getByTestId("on-call-marks")).toHaveCount(0);

  const paymentsRow = row(page, NAMES.payments);
  await expect(paymentsRow).toContainText("@petr");
  await expect(paymentsRow.getByTestId("on-call-unreachable")).toHaveText(
    "unreachable in Mattermost",
  );
  await expect(paymentsRow.getByTestId("on-call-gap")).toHaveText("next: gap");

  const searchRow = row(page, NAMES.search);
  await expect(searchRow).toContainText("No Schedule");
  await expect(searchRow.getByTestId("on-call-until")).toHaveCount(0);
  await shot(page, "home-on-call-now");

  await section.getByRole("button", { name: "My Teams" }).click();
  await expect(section.getByRole("button", { name: "My Teams" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(row(page, NAMES.devops)).toBeVisible();
  await expect(row(page, NAMES.payments)).toHaveCount(0);
  await expect(row(page, NAMES.search)).toHaveCount(0);
  await shot(page, "home-on-call-now-mine");

  await section.getByRole("button", { name: "All Teams" }).click();
  await expect(row(page, NAMES.search)).toBeVisible();
  expect(csp).toEqual([]);
});

test("a Responder reads every Team under All Teams and only their own under My Teams", async ({
  page,
}) => {
  await signIn(page, ON_CALL.petr, TEAM_SPEC_PASSWORD);
  await expect(page).toHaveURL(`${APP}/`);
  const section = page.getByTestId("on-call-now");
  await expect(row(page, NAMES.devops)).toBeVisible();
  await section.getByRole("button", { name: "My Teams" }).click();
  await expect(row(page, NAMES.payments)).toBeVisible();
  await expect(row(page, NAMES.devops)).toHaveCount(0);
});

test("the Home page reads in Russian and fits 360 px", async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, RU_USER, TEAM_SPEC_PASSWORD);
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Главная", level: 1 })).toBeVisible();
  const section = page.getByTestId("on-call-now");
  await expect(section.getByRole("heading", { name: "Сейчас дежурит", level: 2 })).toBeVisible();
  await expect(section.getByRole("button", { name: "Все команды" })).toBeVisible();
  await expect(section.getByRole("button", { name: "Мои команды" })).toBeVisible();
  await expect(section.getByText("По главному расписанию каждой команды.")).toBeVisible();
  await expect(row(page, NAMES.devops).getByTestId("on-call-until")).toHaveText(/^до пн 10:00$/);
  await expect(row(page, NAMES.payments).getByTestId("on-call-unreachable")).toHaveText(
    "недостижим в Mattermost",
  );
  await expect(row(page, NAMES.payments).getByTestId("on-call-gap")).toHaveText("дальше дыра");
  await expect(row(page, NAMES.search)).toContainText("Без расписания");
  await expectNoHorizontalScroll(page);
  await shot(page, "home-ru-360");

  await section.getByRole("button", { name: "Мои команды" }).click();
  await expect(section.getByText("Вы не состоите ни в одной команде.")).toBeVisible();
  await expect(page.getByTestId("on-call-row")).toHaveCount(0);
  await expectNoHorizontalScroll(page);
  await context.close();
  expect(csp).toEqual([]);
});
