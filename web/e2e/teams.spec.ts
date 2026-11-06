// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Teams pages against `muster dev` (Verification of the Teams story): the Teams list and "New Team"; the Team page
// with "People" (a person added by hand, a person who came through the identity provider group), "Identity provider
// groups" and "Working hours"; the same pages for a Responder, who changes nothing; the pages in Russian and 360 px
// wide; "Owning Team" on the Route and Destination forms, and the filter "Team" with "My Teams" on both lists; the Team
// label with the unknown values of it, "Create Team" for a value, and "Which Alerts belong to this Team".

import { expect, test, type Browser, type Page } from "@playwright/test";

import {
  APP,
  type Api,
  adminApi,
  alertGroupOf,
  createRoute,
  deleteRoute,
  expectNoHorizontalScroll,
  nextIdpUser,
  removeIntegration,
  removeTeamsBySlug,
  seedTeamAlerts,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const PASSWORD = "teams-password-1";
const BOB = "teams-bob";
const RU_ADMIN = "teams-ru";
// The person who signs in through the identity provider; other specs use "olga", so the name is another.
const PETRA = "petra";
const PETRA_SUB = "u-teams-petra";
const NONE = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = {
  new_alert_group: NONE,
  new_alerts: NONE,
  reopen: NONE,
  ack_timeout: NONE,
  snooze_ended: NONE,
  rise_to_urgent: NONE,
};

interface Item {
  id: string;
  name: string;
}

/** Creates a local user with a password unless it exists, and returns its id. */
async function ensureUser(admin: Api, login: string, role: string): Promise<string> {
  const directory = await admin.call<{ items: { id: string; login?: string }[] }>(
    "GET",
    `/api/v1/user-directory?q=${encodeURIComponent(login)}`,
  );
  const found = directory.items.find((u) => u.login === login);
  if (found !== undefined) {
    return found.id;
  }
  const { id, token } = await admin.createUser(login, role);
  await admin.call("POST", "/api/v1/password-setups", { token, password: PASSWORD });
  return id;
}

/** Deletes every Team, so that a spec starts from the empty list. */
async function removeTeams(admin: Api): Promise<void> {
  const list = await admin.call<{ items: Item[] }>("GET", "/api/v1/teams?limit=500");
  for (const team of list.items) {
    await admin.call("DELETE", `/api/v1/teams/${team.id}`);
  }
}

async function createTeam(admin: Api, name: string, userIds: string[] = []): Promise<string> {
  const team = await admin.call<{ id: string }>("POST", "/api/v1/teams", {
    name,
    slug: name,
    own_working_hours: false,
  });
  for (const id of userIds) {
    await admin.call("POST", `/api/v1/teams/${team.id}/users`, { user_id: id });
  }
  return team.id;
}

function nav(page: Page) {
  return page.getByRole("navigation", { name: "Main" });
}

/** The section of the Team page with the test id. */
function section(
  page: Page,
  id: "team-details" | "team-people" | "team-idp" | "team-hours-section",
) {
  return page.getByTestId(id);
}

async function openTeams(page: Page): Promise<void> {
  await nav(page).getByRole("link", { name: "Teams", exact: true }).click();
  await expect(page).toHaveURL(`${APP}/teams`);
  await expect(page.getByRole("heading", { name: "Teams", level: 1 })).toBeVisible();
}

test("creates a Team, adds a person by hand, a group of the identity provider and Working hours", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  await removeTeams(admin);
  const bobId = await ensureUser(admin, BOB, "responder");
  await admin.call("POST", "/api/v1/oidc-settings/checks");
  await signInAdmin(page);

  // Step 1: the list is empty; "Teams" is in the navigation, with its guide.
  await expect(nav(page).getByRole("link", { name: "Teams", exact: true })).toBeVisible();
  await openTeams(page);
  await expect(page.getByText("No Teams yet.")).toBeVisible();
  await expect(
    page.getByText(
      "A Team groups people and owns Routes and Destinations. It grants no access: Roles are the same across the Organization.",
    ),
  ).toBeVisible();
  await expect(page.getByRole("link", { name: /^Guide: Teams/ })).toHaveAttribute(
    "href",
    /\/concepts\/teams\/$/,
  );
  await shot(page, "teams-empty");

  // "New Team": the slug follows the name; a name or slug that is wrong shows at its field.
  await page.getByRole("link", { name: "New Team" }).click();
  await expect(page).toHaveURL(`${APP}/teams/new`);
  await page.getByLabel("Name", { exact: true }).fill("payments");
  await expect(page.getByLabel("Slug", { exact: true })).toHaveValue("payments");
  await expect(
    page.getByText(
      "The value of the Team label that names the Team: 1 to 63 characters, lowercase letters, digits, - _ and ., starting with a letter or a digit.",
    ),
  ).toBeVisible();
  await page.getByLabel("Slug", { exact: true }).fill("Not A Slug");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByLabel("Slug", { exact: true })).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByText("This value has the wrong format.")).toBeVisible();
  await shot(page, "team-new-invalid-slug");
  await page.getByLabel("Slug", { exact: true }).fill("payments");
  await page.getByLabel("Description", { exact: true }).fill("Card payments");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/teams\/TM[0-9A-Z]+$/);
  const teamUrl = page.url();
  await expect(page.getByRole("heading", { name: "payments", level: 1 })).toBeVisible();
  await expect(section(page, "team-people")).toContainText("Nobody is in this Team yet.");

  // A second Team with the same name is refused at the name.
  await page.goto("/teams/new");
  await page.getByLabel("Name", { exact: true }).fill("payments");
  await page.getByLabel("Slug", { exact: true }).fill("payments-two");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Another Team has this name.")).toBeVisible();
  await expect(page.getByLabel("Name", { exact: true })).toHaveAttribute("aria-invalid", "true");
  await page.goto(teamUrl);

  // Step 2: "People" → "Add person" → bob.
  const people = section(page, "team-people");
  await people.getByRole("button", { name: "Add person" }).click();
  await people.getByLabel("User", { exact: true }).selectOption({ label: BOB });
  await people.getByRole("button", { name: "Add", exact: true }).click();
  const bob = people.getByRole("row").filter({ hasText: BOB });
  await expect(bob).toContainText("Responder");
  await expect(bob).toContainText("added by hand");
  await expect(bob).toContainText("not linked");
  await expect(bob.getByRole("button", { name: "Remove" })).toBeVisible();
  await shot(page, "team-people-by-hand");

  // Step 3: "Identity provider groups" → "Add": the claim combobox offers `groups` first.
  const idp = section(page, "team-idp");
  await expect(idp).toContainText("No groups: people are in the Team only when added by hand.");
  await expect(
    idp.getByText(
      "Checked at every sign-in, like the Role mapping: each entry reads its own claim, and a/b/* takes the whole subtree. The Role of the people does not change.",
    ),
  ).toBeVisible();
  await expect(
    idp.getByRole("link", { name: "Learn more: Identity provider groups (opens in a new tab)" }),
  ).toHaveAttribute("href", /\/concepts\/teams\/#membership-from-the-identity-provider$/);
  await idp.getByRole("button", { name: "Add", exact: true }).click();
  const claim = idp.getByRole("combobox", { name: "Claim 1" });
  const offered = await claim.evaluate((el) => {
    const list = document.getElementById(el.getAttribute("list") ?? "");
    return [...(list?.querySelectorAll("option") ?? [])].map((o) => o.getAttribute("value"));
  });
  expect(offered[0]).toBe("groups");
  expect(offered).toEqual(expect.arrayContaining(["groups", "sub"]));
  // A refusal of the server shows at the group of the entry.
  await idp.getByLabel("Identity provider group 1", { exact: true }).fill("teams/*/payments");
  await idp.getByRole("button", { name: "Save", exact: true }).click();
  await expect(idp.getByLabel("Identity provider group 1", { exact: true })).toHaveAttribute(
    "aria-invalid",
    "true",
  );
  await expect(idp.getByText("This value has the wrong format.")).toBeVisible();
  await shot(page, "team-idp-refused");
  await idp.getByLabel("Identity provider group 1", { exact: true }).fill("teams/payments/*");
  await idp.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("team-idp-status")).toHaveText("Saved.");

  // petra signs in through the identity provider with a group the entry takes.
  const petraContext = await browser.newContext();
  const petra = await petraContext.newPage();
  await nextIdpUser(PETRA_SUB, PETRA, ["oncall", "teams/payments/oncall"]);
  await petra.goto("/sign-in");
  await petra.getByText("Sign in with Example SSO").click();
  await expect(petra.getByTestId("user-menu-name")).toHaveText(PETRA);
  await petraContext.close();
  await page.reload();
  const petraRow = section(page, "team-people").getByRole("row").filter({ hasText: PETRA });
  await expect(petraRow).toContainText("group teams/payments/*");
  await expect(petraRow).not.toContainText("added by hand");
  await expect(petraRow.getByRole("button")).toHaveCount(0);
  await expect(
    section(page, "team-people").getByRole("row").filter({ hasText: BOB }),
  ).toBeVisible();
  await shot(page, "team-people-idp");

  // Step 4: Working hours, following the Organization until "Own" is chosen.
  const hours = section(page, "team-hours-section");
  await expect(hours.getByTestId("working-hours-summary")).toHaveText(
    "Organization default: Mon–Fri 09:00–18:00, UTC",
  );
  await expect(hours.getByRole("link", { name: /^Learn more: Working hours/ })).toHaveAttribute(
    "href",
    /\/concepts\/teams\/#working-hours$/,
  );
  // Saves of its own never show the banner "Someone else changed this Team", not even for a moment.
  await page.evaluate(() => {
    new MutationObserver(() => {
      if (document.querySelector('[data-testid="team-stale"]') !== null) {
        document.body.dataset.staleSeen = "yes";
      }
    }).observe(document.body, { childList: true, subtree: true });
  });
  await hours.getByRole("checkbox", { name: "Own" }).check();
  await hours.getByLabel("From", { exact: true }).fill("11:00");
  await hours.getByLabel("To", { exact: true }).fill("19:00");
  await hours.getByLabel("Time zone", { exact: true }).selectOption("Europe/Berlin");
  await hours.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("team-hours-status")).toHaveText("Saved.");
  await expect(page.getByTestId("team-stale")).toHaveCount(0);
  expect(await page.evaluate(() => document.body.dataset.staleSeen ?? "no")).toBe("no");
  await page.reload();
  await expect(
    section(page, "team-hours-section").getByRole("checkbox", { name: "Own" }),
  ).toBeChecked();
  await expect(section(page, "team-hours-section").getByLabel("From", { exact: true })).toHaveValue(
    "11:00",
  );
  await expect(section(page, "team-hours-section").getByLabel("To", { exact: true })).toHaveValue(
    "19:00",
  );
  await expect(
    section(page, "team-hours-section").getByLabel("Time zone", { exact: true }),
  ).toHaveValue("Europe/Berlin");
  // An end that is not later than the start shows the message at "To".
  await section(page, "team-hours-section").getByLabel("To", { exact: true }).fill("10:00");
  await section(page, "team-hours-section")
    .getByRole("button", { name: "Save", exact: true })
    .click();
  await expect(
    section(page, "team-hours-section").getByLabel("To", { exact: true }),
  ).toHaveAttribute("aria-invalid", "true");
  await expect(section(page, "team-hours-section")).toContainText(
    "The end must be later than the start.",
  );
  await shot(page, "team-hours-refused");

  // The Teams list counts the people.
  await openTeams(page);
  const row = page
    .getByRole("table", { name: "Teams" })
    .getByRole("row")
    .filter({ hasText: "payments" });
  await expect(row.getByTestId("team-people-count")).toHaveText("2 people");
  await expect(row).toContainText("payments");
  await shot(page, "teams-list");
  expect(csp).toEqual([]);
  expect(bobId).not.toBe("");
  await admin.dispose();
});

test("a Responder reads the Teams and changes nothing", async ({ page }) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  try {
    await signIn(page, BOB, PASSWORD);
    await expect(nav(page).getByRole("link", { name: "Teams", exact: true })).toBeVisible();
    await openTeams(page);
    await expect(page.getByRole("link", { name: "New Team" })).toHaveCount(0);
    await page
      .getByRole("table", { name: "Teams" })
      .getByRole("link", { name: "payments" })
      .click();
    await expect(page.getByRole("heading", { name: "payments", level: 1 })).toBeVisible();
    // Step 8: no "Add person", "Remove", "Save" or "Delete Team".
    await expect(
      section(page, "team-people").getByRole("row").filter({ hasText: PETRA }),
    ).toBeVisible();
    for (const name of ["Add person", "Remove", "Save", "Delete Team", "Add"]) {
      await expect(page.getByRole("button", { name })).toHaveCount(0);
    }
    await expect(section(page, "team-idp")).toContainText("teams/payments/*");
    await expect(section(page, "team-hours-section")).toContainText("Europe/Berlin");
    await expect(page.getByLabel("Name", { exact: true })).toBeDisabled();
    await shot(page, "team-responder");
    await page.setViewportSize({ width: 360, height: 740 });
    await expectNoHorizontalScroll(page);
    await shot(page, "team-responder-360");
  } finally {
    await admin.dispose();
  }
  expect(csp).toEqual([]);
});

test("the Teams pages read in Russian and fit 360 px", async ({ browser }) => {
  const admin = await adminApi();
  await ensureUser(admin, RU_ADMIN, "admin");
  await admin.dispose();
  const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, RU_ADMIN, PASSWORD);
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/teams");
  await expect(page.getByRole("heading", { name: "Команды", level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: "Новая команда" })).toBeVisible();
  await expect(page.getByRole("link", { name: /^Руководство: Команды/ })).toBeVisible();
  await expect(page.getByTestId("team-people-count")).toHaveText("2 участника");
  await expectNoHorizontalScroll(page);
  await shot(page, "teams-list-ru-360");

  await page.getByRole("link", { name: "payments", exact: true }).click();
  await expect(page.getByRole("heading", { name: "payments", level: 1 })).toBeVisible();
  const people = section(page, "team-people");
  await expect(people.getByRole("heading", { name: "Участники" })).toBeVisible();
  await expect(people.getByRole("button", { name: "Добавить участника" })).toBeVisible();
  await expect(people.getByRole("row").filter({ hasText: BOB })).toContainText("вручную");
  await expect(people.getByRole("row").filter({ hasText: BOB })).toContainText("Исполнитель");
  await expect(people.getByRole("row").filter({ hasText: PETRA })).toContainText(
    "группа teams/payments/*",
  );
  const idp = section(page, "team-idp");
  await expect(
    idp.getByRole("heading", { name: "Группы провайдера учётных записей" }),
  ).toBeVisible();
  await expect(
    idp.getByText(
      "Проверяются при каждом входе, как сопоставления ролей: у каждой строки свой claim, a/b/* берёт всё поддерево. Роль участника при этом не меняется.",
    ),
  ).toBeVisible();
  await expect(idp.getByLabel("Claim 1", { exact: true })).toBeVisible();
  await expect(idp.getByLabel("Группа провайдера 1", { exact: true })).toHaveValue(
    "teams/payments/*",
  );
  const hours = section(page, "team-hours-section");
  await expect(hours.getByRole("heading", { name: "Рабочее время" })).toBeVisible();
  await expect(hours.getByRole("checkbox", { name: "Своё" })).toBeChecked();
  await expect(hours.getByLabel("С", { exact: true })).toHaveValue("11:00");
  await expect(hours.getByLabel("До", { exact: true })).toHaveValue("19:00");
  await expect(hours.getByLabel("Часовой пояс", { exact: true })).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "team-ru-360");

  // Without "Own" the line reads the Organization's hours in Russian.
  await hours.getByRole("checkbox", { name: "Своё" }).uncheck();
  await expect(hours.getByTestId("working-hours-summary")).toHaveText(
    "В организации по умолчанию: пн–пт 09:00–18:00, UTC",
  );
  await shot(page, "team-hours-default-ru-360");

  await page.goto("/teams/new");
  await expect(page.getByRole("heading", { name: "Новая команда", level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Сохранить" }).click();
  await expect(page.getByLabel("Название", { exact: true })).toHaveAttribute(
    "aria-invalid",
    "true",
  );
  await expectNoHorizontalScroll(page);

  await page.goto("/profile");
  await page.getByLabel("Язык").selectOption("en");
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  expect(csp).toEqual([]);
  await context.close();
});

/** Opens a Route's page from the list and chooses its owning Team. */
async function ownRoute(page: Page, route: string, team: string): Promise<void> {
  await page.goto("/routes");
  await page.getByRole("link", { name: route, exact: true }).click();
  const severity = page.getByTestId("route-severity-section");
  await expect(severity).toContainText("Owning Team");
  await expect(
    severity.getByText("For ownership and the filter of the UI; does not affect delivery."),
  ).toBeVisible();
  await severity.getByLabel("Owning Team", { exact: true }).selectOption({ label: team });
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page).toHaveURL(`${APP}/routes`);
}

/** The names of the Routes the list shows. */
function routeNames(page: Page) {
  return page.getByTestId("route-row").locator("[data-route-link]");
}

async function pickTeam(page: Page, label: string): Promise<void> {
  await page.getByTestId("filter-team").getByLabel("Team", { exact: true }).selectOption({ label });
}

async function viaBrowser(browser: Browser, login: string): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await signIn(page, login, PASSWORD);
  return page;
}

test("Owning Team is chosen on a Route and a Destination, and the lists filter by Team and My Teams", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  await removeTeams(admin);
  const bobId = await ensureUser(admin, BOB, "responder");
  const payments = await createTeam(admin, "payments", [bobId]);
  const search = await createTeam(admin, "search");
  const routeIds = [
    await createRoute("owned-by-payments", "owned-pay", ["alertname"]),
    await createRoute("owned-by-search", "owned-search", ["alertname"]),
  ];
  const destinations: string[] = [];
  try {
    for (const [name, owner] of [
      ["hook-payments", payments],
      ["hook-search", search],
    ] as const) {
      const created = await admin.call<{ destination: { id: string } }>(
        "POST",
        "/api/v1/destinations",
        {
          type: "webhook",
          name,
          mode: "events",
          events: { url: "http://127.0.0.1:18099/hook", headers: [] },
          proxy: { enabled: false },
          mentions: MENTIONS,
          limiter: { limit: 50, per_seconds: 1 },
          owning_team_id: owner,
        },
      );
      destinations.push(created.destination.id);
    }

    await signInAdmin(page);
    // Step 7: the Route form has "Owning Team" with its hint; the owner is saved.
    await ownRoute(page, "owned-by-payments", "payments");
    await ownRoute(page, "owned-by-search", "search");
    await page.getByRole("link", { name: "owned-by-payments", exact: true }).click();
    await expect(
      page.getByTestId("route-severity-section").getByLabel("Owning Team", { exact: true }),
    ).toHaveValue(payments);
    await shot(page, "route-owning-team");

    // The Routes list: "My Teams" for the Admin, who is in no Team, is empty.
    await page.goto("/routes");
    await expect(page.getByTestId("filter-team")).toBeVisible();
    await expect(routeNames(page).filter({ hasText: "owned-by-payments" })).toBeVisible();
    await expect(
      page
        .getByTestId("route-row")
        .filter({ hasText: "owned-by-payments" })
        .getByTestId("route-owning-team"),
    ).toHaveText("Owning Team: payments");
    await pickTeam(page, "My Teams");
    await expect(page).toHaveURL(`${APP}/routes?team=mine`);
    await expect(page.getByTestId("route-list-empty")).toHaveText("No Routes of this Team.");
    await expect(routeNames(page)).toHaveCount(0);
    await shot(page, "routes-my-teams-empty");
    await pickTeam(page, "payments");
    await expect(page).toHaveURL(`${APP}/routes?team=${payments}`);
    await expect(routeNames(page)).toHaveText(["owned-by-payments"]);
    await pickTeam(page, "search");
    await expect(routeNames(page)).toHaveText(["owned-by-search"]);
    await pickTeam(page, "All");
    await expect(routeNames(page).filter({ hasText: "owned-by-search" })).toBeVisible();

    // A Destination's form has "Owning Team"; the list filters the same way.
    await page.goto(`/destinations/${destinations[0]}`);
    await expect(page.getByLabel("Owning Team", { exact: true })).toHaveValue(payments);
    await expect(
      page.getByText("For ownership and the filter of the UI; does not affect delivery."),
    ).toBeVisible();
    await shot(page, "destination-owning-team");
    await page.getByLabel("Owning Team", { exact: true }).selectOption("");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByTestId("destination-status")).toHaveText("Saved.");
    await page.getByLabel("Owning Team", { exact: true }).selectOption({ label: "payments" });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByTestId("destination-status")).toHaveText("Saved.");

    await page.goto("/destinations");
    await pickTeam(page, "My Teams");
    await expect(page).toHaveURL(`${APP}/destinations?team=mine`);
    await expect(page.getByText("No Destinations of this Team.")).toBeVisible();
    await pickTeam(page, "search");
    await expect(page.getByRole("link", { name: "hook-search" })).toBeVisible();
    await expect(page.getByRole("link", { name: "hook-payments" })).toHaveCount(0);
    await shot(page, "destinations-by-team");

    // bob is in "payments" and not in "search": "My Teams" lists only what "payments" owns.
    const bob = await viaBrowser(browser, BOB);
    await bob.goto("/routes");
    await pickTeam(bob, "My Teams");
    await expect(routeNames(bob)).toHaveText(["owned-by-payments"]);
    await expect(bob.getByTestId("route-row").getByTestId("route-owning-team")).toHaveText(
      "Owning Team: payments",
    );
    await shot(bob, "routes-my-teams-bob");
    await bob.goto("/destinations");
    await pickTeam(bob, "My Teams");
    await expect(bob.getByRole("link", { name: "hook-payments" })).toBeVisible();
    await expect(bob.getByRole("link", { name: "hook-search" })).toHaveCount(0);
    // A Responder reads the owner on the Route, without the control that changes it.
    await bob.goto("/routes");
    await bob.getByRole("link", { name: "owned-by-payments", exact: true }).click();
    await expect(bob.getByTestId("route-owner")).toHaveText("payments");
    await bob.context().close();
  } finally {
    for (const id of destinations) {
      await admin.call("DELETE", `/api/v1/destinations/${id}`);
    }
    for (const id of routeIds) {
      await deleteRoute(id);
    }
    await removeTeams(admin);
    await admin.dispose();
  }
  expect(csp).toEqual([]);
});

test("a Team is deleted from its page, and the page of a Team that is gone says so", async ({
  page,
}) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  try {
    await removeTeams(admin);
    const id = await createTeam(admin, "disposable");
    await signInAdmin(page);
    await page.goto(`/teams/${id}`);
    await page.getByRole("button", { name: "Delete Team" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Delete Team disposable?");
    await dialog.getByRole("button", { name: "Delete Team" }).click();
    await expect(page).toHaveURL(`${APP}/teams`);
    await expect(page.getByText("No Teams yet.")).toBeVisible();
    // A page of a Team that is gone says so.
    await page.goto(`/teams/${id}`);
    await expect(page.getByText("It no longer exists. Reload the page.")).toBeVisible();
  } finally {
    await admin.dispose();
  }
  expect(csp).toEqual([]);
});

/** The row of the unknown value team=value, whichever other values are listed. */
function valueRow(page: Page, value: string) {
  return page
    .getByTestId("team-label-value")
    .filter({ has: page.locator("code", { hasText: new RegExp(`^team=${value}$`) }) });
}

test("lists the unknown values of the Team label, makes a Team of one and sets which Alerts belong to a Team", async ({
  page,
}) => {
  test.setTimeout(240_000);
  const csp = watchCsp(page);
  const admin = await adminApi();
  await removeTeams(admin);
  await removeIntegration(admin, "teams-labels");
  await admin.dispose();
  // The Teams "payments" (bob is in it) and "search", and Alert Groups with team="payments", "search", "checkout" and
  // "Payments".
  const integrationId = await seedTeamAlerts("teams-labels", "TL");
  await signInAdmin(page);
  await openTeams(page);

  // 1. The Team label with the hint; "Unknown values of team" lists checkout and Payments with their times.
  await expect(page.getByTestId("team-label-name")).toHaveText("team");
  await expect(page.getByTestId("team-label")).toContainText(
    "An Alert belongs to a Team by the value of the Team label or by the Team's matchers. Teams do not limit access: Roles are the same for the whole Organization.",
  );
  await expect(
    page
      .getByTestId("team-label")
      .getByRole("link", { name: "Change in the Organization settings" }),
  ).toHaveAttribute("href", "/admin/organization/general");
  const values = page.getByTestId("team-label-values");
  await expect(values.getByRole("heading", { name: "Unknown values of team" })).toBeVisible();
  await expect(values).toContainText(
    "Alerts arrived with these values, but there is no such Team: no Destinations of a Team are added and nobody is Paged.",
  );
  await expect(valueRow(page, "checkout")).toContainText("Last seen");
  await expect(valueRow(page, "checkout")).toContainText("ago)");
  await expect(valueRow(page, "Payments")).toContainText("1 Alert in the last 7 days");
  await expect(valueRow(page, "payments")).toHaveCount(0);
  await expect(valueRow(page, "search")).toHaveCount(0);
  await shot(page, "teams-unknown-values");

  // 3. Payments is no slug: the form has an empty slug and the note about a Team matcher.
  await valueRow(page, "Payments").getByRole("link", { name: "Create Team" }).click();
  await expect(page).toHaveURL(`${APP}/teams/new?value=Payments`);
  await expect(page.getByLabel("Slug", { exact: true })).toHaveValue("");
  await expect(page.getByTestId("team-value-note")).toHaveText(
    'Payments cannot be the slug of a Team. A Team gets Alerts with this value only through a Team matcher such as team="Payments".',
  );
  await shot(page, "team-new-for-value");

  // 2. "Create Team" next to checkout: the slug is checkout; Save makes the Team, which the Alert Group then lists.
  await openTeams(page);
  await valueRow(page, "checkout").getByRole("link", { name: "Create Team" }).click();
  await expect(page).toHaveURL(`${APP}/teams/new?slug=checkout`);
  await expect(page.getByLabel("Slug", { exact: true })).toHaveValue("checkout");
  await expect(page.getByTestId("team-value-note")).toHaveCount(0);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("heading", { name: "checkout", level: 1 })).toBeVisible();
  await openTeams(page);
  await expect(valueRow(page, "checkout")).toHaveCount(0);
  await nav(page).getByRole("link", { name: "Alert Groups", exact: true }).click();
  const checkoutGroup = await alertGroupOf(integrationId, "TLCheckout");
  await expect(async () => {
    await page.goto(`/alert-groups/${checkoutGroup}`);
    await expect(page.getByTestId("alert-group-teams")).toContainText(
      "checkout — by the Team label",
      { timeout: 3_000 },
    );
  }).toPass({ timeout: 60_000 });

  // 4. The Team page: the Team label with the slug, the matchers, the hints and the example.
  await openTeams(page);
  await page.getByRole("link", { name: "payments", exact: true }).click();
  const rules = page.getByTestId("team-rules");
  await expect(
    rules.getByRole("heading", { name: "Which Alerts belong to this Team" }),
  ).toBeVisible();
  await expect(rules.getByTestId("team-rule-label")).toContainText("team = payments");
  await expect(rules).toContainText(
    "A matcher may look at any label of the Alert, not only namespace. Any one matcher is enough.",
  );
  await expect(rules).toContainText(
    "On test clusters with a shared namespace, match by pod or app, or put the team label on the pods.",
  );
  await expect(rules.getByTestId("team-rule-example")).toContainText(
    "Teams: payments — by the Team label",
  );
  // A Matcher without a label, and a regular expression that does not compile, stop at their fields.
  await rules.getByRole("button", { name: "Add matcher" }).click();
  await rules.getByLabel("Value of matcher 1").fill("x");
  await rules.getByRole("button", { name: "Save", exact: true }).click();
  await expect(rules.getByTestId("matcher-label-error")).toHaveText("Enter a label name.");
  await rules.getByLabel("Label of matcher 1").fill("pod");
  await rules.getByLabel("Operator of matcher 1").selectOption("=~");
  await rules.getByLabel("Value of matcher 1").fill("[");
  await rules.getByRole("button", { name: "Save", exact: true }).click();
  await expect(rules.getByTestId("matcher-value-error")).toContainText(
    "The regular expression is not valid",
  );
  await expect(rules.getByTestId("team-rules-status")).toHaveText("");
  // Then the matcher of the story.
  await rules.getByLabel("Label of matcher 1").fill("namespace");
  await rules.getByLabel("Value of matcher 1").fill("checkout|billing");
  await rules.getByRole("button", { name: "Save", exact: true }).click();
  await expect(rules.getByTestId("team-rules-status")).toHaveText("Saved.");
  await shot(page, "team-which-alerts-belong");
  await page.reload();
  await expect(page.getByTestId("team-rules").getByLabel("Value of matcher 1")).toHaveValue(
    "checkout|billing",
  );
  await expect(page.getByTestId("team-stale")).toHaveCount(0);

  const cleanup = await adminApi();
  await removeIntegration(cleanup, "teams-labels");
  await removeTeamsBySlug(cleanup, ["payments", "search", "checkout"]);
  await cleanup.dispose();
  expect(csp).toEqual([]);
});

test("the Teams page and the Team page with the Team label read in Russian and fit 360 px", async ({
  browser,
}) => {
  test.setTimeout(180_000);
  const admin = await adminApi();
  await removeTeams(admin);
  await removeIntegration(admin, "teams-labels-ru");
  await ensureUser(admin, RU_ADMIN, "admin");
  await admin.dispose();
  await seedTeamAlerts("teams-labels-ru", "TR");
  const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  try {
    await signIn(page, RU_ADMIN, PASSWORD);
    await page.goto("/profile");
    await page.getByLabel("Language").selectOption("ru");
    await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

    await page.goto("/teams");
    await expect(page.getByTestId("team-label")).toContainText(
      "Алерт относится к команде по значению метки команды или по её матчерам. Доступ команды не ограничивают: роли общие для организации.",
    );
    await expect(
      page
        .getByTestId("team-label")
        .getByRole("link", { name: "Изменить в настройках организации" }),
    ).toBeVisible();
    const values = page.getByTestId("team-label-values");
    await expect(values.getByRole("heading", { name: "Неизвестные значения team" })).toBeVisible();
    await expect(values).toContainText(
      "Алерты приходили с этими значениями, но такой команды нет: места доставки команды не добавляются, никого не зовут.",
    );
    await expect(valueRow(page, "checkout")).toContainText("Последний раз");
    await expectNoHorizontalScroll(page);
    await shot(page, "teams-unknown-values-ru-360");

    await valueRow(page, "Payments").getByRole("link", { name: "Создать команду" }).click();
    await expect(page.getByTestId("team-value-note")).toHaveText(
      'Payments не может быть slug команды. Алерты с этим значением попадут в команду только через матчер команды, например team="Payments".',
    );
    await expectNoHorizontalScroll(page);

    await page.goto("/teams");
    await page.getByRole("link", { name: "payments", exact: true }).click();
    const rules = page.getByTestId("team-rules");
    await expect(rules.getByRole("heading", { name: "Какие алерты — этой команды" })).toBeVisible();
    await expect(rules).toContainText(
      "Матчер может смотреть на любую метку алерта, не только на namespace. Подходит любой из матчеров.",
    );
    await expect(rules).toContainText(
      "На стендах с общим namespace матчите по pod или app либо ставьте метку команды на поды.",
    );
    await expect(rules.getByTestId("team-rule-example")).toContainText(
      "Команды: payments — по метке команды",
    );
    await rules.getByRole("button", { name: "Добавить матчер" }).click();
    await expectNoHorizontalScroll(page);
    await shot(page, "team-which-alerts-belong-ru-360");

    await page.goto("/profile");
    await page.getByLabel("Язык").selectOption("en");
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  } finally {
    const cleanup = await adminApi();
    await removeIntegration(cleanup, "teams-labels-ru");
    await removeTeamsBySlug(cleanup, ["payments", "search"]);
    await cleanup.dispose();
    await context.close();
  }
  expect(csp).toEqual([]);
});
