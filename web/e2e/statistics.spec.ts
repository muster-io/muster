// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The statistics page and the Lifecycle section of the Route editor against `muster dev`: Verification steps 1 to 3 of
// their story, and the views By team and By people with the filters, the URL and the sorting (S-175). The fake Alertmanager sends the group s1 to the Integration "stats": three Alerts of the Route "st",
// each its own Alert Group, resolved 10, 20 and 30 minutes after they started by the development clock. The page
// shows 3 Alert Groups with a median time to resolve of 20 min, on one day, by Route and by Integration, with no
// Content Security Policy violation; the Lifecycle section shows the Paging values and saves a new Reopen window.

import { expect, test, type Page } from "@playwright/test";

import {
  APP,
  Api,
  TEAM_SPEC_PASSWORD,
  adminApi,
  advance,
  createRoute,
  devClockOffset,
  deleteRoute,
  expectNoHorizontalScroll,
  ensureLocalUser,
  ensureTeam,
  fakeIntegration,
  fam,
  notify,
  shot,
  signIn,
  signInAdmin,
  sql,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

let integrationId = "";
let routeId = "";

test.beforeAll(async () => {
  integrationId = await fakeIntegration("stats");
  routeId = await createRoute("st", "st", ["alertname", "n"]);
  await fam("PUT", "/groups/s1", {
    receiver: "stats",
    route: "{}",
    labels: { alertname: "Stat" },
  });
  for (const n of [1, 2, 3]) {
    await fam("PUT", `/groups/s1/alerts/x${n}`, { labels: { team: "st", n: String(n) } });
  }
  await notify(integrationId, "s1", { reason: "first notification" });
  for (const n of [1, 2, 3]) {
    await advance(600);
    await fam("PUT", `/groups/s1/alerts/x${n}`, {
      labels: { team: "st", n: String(n) },
      status: "resolved",
    });
    await notify(integrationId, "s1", { reason: "some alerts resolved" });
  }
});

test.afterAll(async () => {
  if (routeId !== "") {
    await deleteRoute(routeId);
  }
});

/** The day of an instant in the time zone of the specs (playwright.config.ts), as YYYY-MM-DD. */
function berlinDay(instant: Date): string {
  return new Intl.DateTimeFormat("en-CA", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    timeZone: "Europe/Berlin",
  }).format(instant);
}

/** The row of an item of the statistics table. */
function itemRow(page: Page, name: string) {
  return page.getByTestId("statistics-row").filter({
    has: page.getByTestId("statistics-name").getByText(name, { exact: true }),
  });
}

test("shows the statistics per Route and per Integration, with no CSP violation", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  const nav = page.getByRole("navigation", { name: "Main" });
  const links = await nav.getByRole("link").allTextContents();
  expect(links.indexOf("Statistics")).toBe(links.indexOf("Teams") + 1);
  await nav.getByRole("link", { name: "Statistics" }).click();
  await expect(page.getByRole("heading", { name: "Statistics", level: 1 })).toBeVisible();
  await expect(page.getByRole("radio", { name: "By route" })).toBeChecked();
  await expect(page.getByLabel("Period")).toHaveValue("7d");

  // 1. The Route "st": 3 Alert Groups, a median time to resolve of 20 min, and one day with 3.
  const st = itemRow(page, "st");
  await expect(st.getByTestId("statistics-count")).toHaveText("3");
  await expect(st.getByTestId("statistics-resolve-median")).toHaveText("20 min");
  await expect(st.getByTestId("statistics-resolve-p95")).toHaveText("29 min");
  await expect(st.getByTestId("statistics-ack-median")).toHaveText("—");
  await st.getByRole("button", { name: "st" }).click();
  const days = page.getByRole("table", { name: "Alert Groups per day of start: st" });
  await expect(days.getByTestId("statistics-day")).toHaveCount(1);
  await expect(days.getByTestId("day-count")).toHaveText("3");
  await expect(days.getByTestId("day-resolve")).toHaveText("20 min");
  await shot(page, "statistics-by-route");

  // Only the chosen Routes, kept in the URL.
  await page.getByTestId("filter-route").getByRole("combobox").selectOption({ label: "st" });
  await expect
    .poll(() => new URL(page.url()).searchParams.get("route"))
    .toBe(JSON.stringify([routeId]));
  await expect(page.getByTestId("statistics-row")).toHaveCount(1);
  await page.reload();
  await expect(page.getByTestId("statistics-row")).toHaveCount(1);
  await expect(itemRow(page, "st").getByTestId("statistics-count")).toHaveText("3");

  // 2. By Integration: the same three Alert Groups under "stats".
  await page.getByRole("radio", { name: "By integration" }).click();
  await expect(page.getByRole("radio", { name: "By integration" })).toBeChecked();
  await expect(page).toHaveURL(/by=integration/);
  // The chosen Route is a filter of every view: it stays when the view changes.
  await expect(page).toHaveURL(/route=/);
  await expect(page.getByTestId("filter-route").getByRole("list")).toContainText("st");
  const stats = itemRow(page, "stats");
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");
  await expect(stats.getByTestId("statistics-resolve-median")).toHaveText("20 min");
  await shot(page, "statistics-by-integration");

  // The other periods, kept in the URL.
  await page.getByLabel("Period").selectOption("30d");
  await expect(page).toHaveURL(/period=30d/);
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");
  await page.getByLabel("Period").selectOption("90d");
  await expect(page).toHaveURL(/period=90d/);
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");

  // A custom period of whole days around the day of the business clock, in the browser's time zone.
  await page.getByLabel("Period").selectOption("custom");
  await expect(page.getByLabel("From", { exact: true })).not.toHaveValue("");
  const now = Date.now() + (await devClockOffset()) * 1000;
  const day = (offset: number) => berlinDay(new Date(now + offset * 24 * 60 * 60 * 1000));
  await page.getByLabel("From", { exact: true }).fill(day(-1));
  await page.getByLabel("To", { exact: true }).fill(day(1));
  await expect(page).toHaveURL(new RegExp(`from=${day(-1)}`));
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");
  await page.reload();
  await expect(itemRow(page, "stats").getByTestId("statistics-count")).toHaveText("3");

  // A custom period without both days, or one that ends before it starts, is not sent.
  await page.getByLabel("From", { exact: true }).fill("");
  await expect(page.getByText("Choose both days of the period.")).toBeVisible();
  await page.getByLabel("From", { exact: true }).fill("2026-10-20");
  await page.getByLabel("To", { exact: true }).fill("2026-10-10");
  await expect(page.getByText("The period must start before it ends.")).toBeVisible();
  await page.getByLabel("Period").selectOption("7d");
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");

  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "statistics-360");
  await page.setViewportSize({ width: 1280, height: 800 });
  expect(csp).toEqual([]);
});

test("the Lifecycle section shows the profile's values and saves new ones", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);

  // A new Route takes the values of its profile.
  await page.goto("/routes/new");
  await page.getByTestId("route-profile-paging").click();
  const fresh = page.getByTestId("route-lifecycle");
  await expect(fresh.getByLabel("Reopen window")).toHaveValue("15");
  await expect(fresh.getByLabel("Grace period")).toHaveValue("15");
  await expect(
    fresh.getByRole("switch", { name: "A rise to Urgent removes the acknowledgement" }),
  ).toBeChecked();

  // 3. The Route "st": 15 and 15 minutes and the switch on; a Reopen window of 30 is saved and read back.
  await page.goto(`/routes/${routeId}`);
  const lifecycle = page.getByRole("group", { name: "Lifecycle" });
  await expect(lifecycle.getByLabel("Reopen window")).toHaveValue("15");
  await expect(lifecycle.getByLabel("Grace period")).toHaveValue("15");
  await expect(lifecycle.getByText("minutes")).toHaveCount(2);
  await expect(
    lifecycle.getByText(
      "An alert with the same key firing this soon after Muster resolved the Alert Group reopens it.",
    ),
  ).toBeVisible();
  await expect(
    lifecycle.getByText(
      "After a person resolves an Alert Group, alerts that still fire this long start a new one.",
    ),
  ).toBeVisible();
  await expect(
    lifecycle.getByRole("switch", { name: "A rise to Urgent removes the acknowledgement" }),
  ).toBeChecked();
  await shot(page, "route-lifecycle");

  // A negative value is refused in the form.
  await lifecycle.getByLabel("Reopen window").fill("-1");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(lifecycle.getByText("Enter a number of minutes, 0 or more.")).toBeVisible();

  await lifecycle.getByLabel("Reopen window").fill("30");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await page.goto(`/routes/${routeId}`);
  await page.reload();
  await expect(
    page.getByRole("group", { name: "Lifecycle" }).getByLabel("Reopen window"),
  ).toHaveValue("30");
  const admin = await adminApi();
  try {
    const stored = await admin.call<{
      policy: { reopen_window_seconds: number; grace_period_seconds: number };
    }>("GET", `/api/v1/routes/${routeId}`);
    expect(stored.policy.reopen_window_seconds).toBe(1800);
    expect(stored.policy.grace_period_seconds).toBe(900);
  } finally {
    await admin.dispose();
  }
  expect(csp).toEqual([]);
});

test("shows the statistics and the Lifecycle section in Russian", async ({ page }) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  const { token } = await admin.createUser("st-olga", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "st-olga-password-1" });
  await admin.dispose();
  await signIn(page, "st-olga", "st-olga-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page
    .getByRole("navigation", { name: "Основная" })
    .getByRole("link", { name: "Статистика" })
    .click();
  await expect(page.getByRole("heading", { name: "Статистика", level: 1 })).toBeVisible();
  await expect(page.getByRole("radio", { name: "По маршрутам" })).toBeChecked();
  await expect(page.getByRole("columnheader", { name: "Время до закрытия" })).toBeVisible();
  const st = itemRow(page, "st");
  await expect(st.getByTestId("statistics-resolve-median")).toHaveText("20 мин");
  await st.getByRole("button", { name: "st" }).click();
  await expect(
    page.getByRole("table", { name: "Группы алертов по дням начала: st" }),
  ).toBeVisible();
  await shot(page, "statistics-ru");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "statistics-ru-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  const reader = await adminApi();
  const stored = await reader.call<{ policy: { reopen_window_seconds: number } }>(
    "GET",
    `/api/v1/routes/${routeId}`,
  );
  await reader.dispose();
  await page.goto(`/routes/${routeId}`);
  const lifecycle = page.getByRole("group", { name: "Жизненный цикл" });
  await expect(lifecycle.getByLabel("Окно переоткрытия")).toHaveValue(
    String(stored.policy.reopen_window_seconds / 60),
  );
  await expect(lifecycle.getByLabel("Льготный период")).toHaveValue("15");
  await expect(
    lifecycle.getByRole("switch", { name: "Переход в срочные снимает подтверждение" }),
  ).toBeChecked();
  await shot(page, "route-lifecycle-ru");
  expect(csp).toEqual([]);
});

// The views By team and By people (S-175): the data of C-09.AC-27, AC-28, AC-31 and AC-32, made through the fake
// Alertmanager and the API as different people, the Alert Groups of a cluster each, with the development clock.
// The logins carry the prefix "stp-" so that no other spec meets these people; the names show as the logins do.

const P = {
  alice: "stp-alice",
  bob: "stp-bob",
  carol: "stp-carol",
  viewer: "stp-viewer",
  terraform: "stp-terraform",
  payments: "stp-payments",
  search: "stp-search",
};

/** The label that keeps the Alert Groups of this spec apart from the other specs'. */
const SCOPE = "stp";

interface PeopleData {
  integrationId: string;
  routeId: string;
  searchId: string;
  groups: Record<string, string>;
  aliceId: string;
  bobId: string;
}

let people: PeopleData;

/** The URL of the page: arrays and booleans as the router writes them, text as it is. */
function statsUrl(params: Record<string, string | boolean | string[] | undefined>): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined) {
      query.set(key, typeof value === "string" ? value : JSON.stringify(value));
    }
  }
  return `/statistics?${query.toString()}`;
}

/** The numbers of a column, added up. */
async function total(page: Page, testId: string): Promise<number> {
  return (await page.getByTestId(testId).allTextContents()).reduce((n, v) => n + Number(v), 0);
}

/** The filters of the Alert Groups of C-09.AC-32: critical, in the namespace payments, of this spec's Route. */
function filtersOf(data: PeopleData) {
  return {
    severity: ["critical"],
    label: ['namespace="payments"', 'cluster=~"fp."'],
    route: [data.routeId],
    integration: [data.integrationId],
  };
}

/** The values of the row of a person of By people: Acknowledged first, Resolved, median and 95th percentile. */
async function personRow(page: Page, name: string): Promise<string[]> {
  const row = page.getByTestId("statistics-people-row").filter({
    has: page.getByTestId("people-name").getByText(name, { exact: true }),
  });
  return Promise.all(
    ["people-first", "people-resolved", "people-median", "people-p95"].map(async (id) =>
      ((await row.getByTestId(id).textContent()) ?? "").trim(),
    ),
  );
}

/** Calls the API as a Service account with its token. */
async function asServiceAccount(token: string, path: string): Promise<void> {
  const res = await fetch(`${APP}${path}`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: "{}",
  });
  expect(res.ok, `${path}: ${res.status} ${await res.text()}`).toBe(true);
}

/** Runs a command of the Alert Group as a person. */
function cmd(who: Api, id: string, command: string) {
  return who.call(
    "POST",
    `/api/v1/alert-groups/${id}/${command}`,
    command === "resolve" ? {} : undefined,
  );
}

async function seedPeople(): Promise<PeopleData> {
  const stpIntegration = await fakeIntegration("stp");
  // The Informational profile: no ack timeout and no Reminders while the clock moves.
  const stpRoute = await createRoute(
    "stp",
    SCOPE,
    ["alertname", "cluster"],
    "informational",
    "scope",
  );
  const admin = await adminApi();
  const aliceId = await ensureLocalUser(admin, P.alice, "responder");
  const bobId = await ensureLocalUser(admin, P.bob, "responder");
  await ensureLocalUser(admin, P.carol, "responder");
  await ensureLocalUser(admin, P.viewer, "viewer");
  const sa = await admin.call<{ id: string }>("POST", "/api/v1/service-accounts", {
    name: P.terraform,
    role: "responder",
  });
  const token = await admin.call<{ value: string }>(
    "POST",
    `/api/v1/service-accounts/${sa.id}/tokens`,
    { name: "stp" },
  );
  await ensureTeam(admin, P.payments, [aliceId]);
  const searchId = await ensureTeam(admin, P.search);
  const alice = await Api.signIn(P.alice, TEAM_SPEC_PASSWORD);
  const bob = await Api.signIn(P.bob, TEAM_SPEC_PASSWORD);
  const carol = await Api.signIn(P.carol, TEAM_SPEC_PASSWORD);

  const fire = (cluster: string, alert: string, severity: string, extra: Record<string, string>) =>
    fam("PUT", `/groups/${cluster}/alerts/${alert}`, {
      labels: { scope: SCOPE, cluster, severity, ...extra },
    });
  /** Starts the Alert Group of the cluster with one Alert of the Severity level and the extra labels. */
  const start = async (
    cluster: string,
    severity = "warning",
    extra: Record<string, string> = {},
  ): Promise<string> => {
    await fam("PUT", `/groups/${cluster}`, {
      receiver: "stp",
      route: "{}",
      labels: { alertname: cluster },
    });
    await fire(cluster, "a", severity, extra);
    await notify(stpIntegration, cluster, { reason: "first notification" });
    let id = "";
    await expect
      .poll(async () => {
        const list = await admin.call<{ items: { alert_group?: { id: string } }[] }>(
          "GET",
          `/api/v1/integrations/${stpIntegration}/alerts?label=${encodeURIComponent(`cluster="${cluster}"`)}`,
        );
        id = list.items[0]?.alert_group?.id ?? "";
        return id;
      })
      .not.toBe("");
    return id;
  };
  const resolveBySystem = async (cluster: string) => {
    await fam("DELETE", `/groups/${cluster}/alerts/a`);
    await notify(stpIntegration, cluster, { reason: "resolved" });
  };
  const groups: Record<string, string> = {};

  // C-09.AC-27: the first acknowledgement stays through Takeover, Unacknowledge and Reopen.
  groups.sp0 = await start("sp0");
  await advance(300);
  await cmd(alice, groups.sp0, "acknowledge");
  await cmd(bob, groups.sp0, "acknowledge");
  await cmd(bob, groups.sp0, "unacknowledge");
  await cmd(carol, groups.sp0, "acknowledge");
  await resolveBySystem("sp0");
  await advance(60);
  await fire("sp0", "b", "warning", {});
  await notify(stpIntegration, "sp0", { reason: "new alerts" });

  // C-09.AC-28: By people.
  groups.sp1 = await start("sp1");
  await advance(120);
  await cmd(alice, groups.sp1, "acknowledge");
  groups.sp2 = await start("sp2");
  await advance(240);
  await cmd(alice, groups.sp2, "acknowledge");
  groups.sp3 = await start("sp3");
  await advance(1800);
  await cmd(alice, groups.sp3, "acknowledge");
  groups.sp4 = await start("sp4");
  await advance(600);
  await cmd(bob, groups.sp4, "acknowledge");
  await cmd(bob, groups.sp1, "resolve");
  await cmd(bob, groups.sp2, "resolve");
  await asServiceAccount(token.value, `/api/v1/alert-groups/${groups.sp4}/resolve`);
  await resolveBySystem("sp3");

  // C-09.AC-31: an Alert Group of both Teams, one of payments and one without a Team.
  groups.tp1 = await start("tp1", "warning", { team: P.payments });
  await fire("tp1", "b", "warning", { team: P.search });
  await notify(stpIntegration, "tp1", { reason: "new alerts" });
  groups.tp2 = await start("tp2", "warning", { team: P.payments });
  await start("tp3");
  await cmd(alice, groups.tp1, "acknowledge");
  await cmd(bob, groups.tp2, "acknowledge");

  // C-09.AC-32: the filters reach every view; two of the four are critical in the namespace payments.
  groups.fp1 = await start("fp1", "critical", { namespace: "payments", team: P.payments });
  groups.fp2 = await start("fp2", "critical", { namespace: "payments", team: P.payments });
  await start("fp3", "warning", { namespace: "payments", team: P.payments });
  await start("fp4", "critical", { namespace: "search", team: P.payments });
  await cmd(alice, groups.fp1, "acknowledge");
  await cmd(bob, groups.fp2, "acknowledge");
  await Promise.all([alice, bob, carol, admin].map((a) => a.dispose()));
  return {
    integrationId: stpIntegration,
    routeId: stpRoute,
    searchId,
    groups,
    aliceId,
    bobId,
  };
}

test.describe("By team and By people", () => {
  test.describe.configure({ mode: "serial" });

  test.beforeAll(async () => {
    test.setTimeout(240_000);
    people = await seedPeople();
  });

  test.afterAll(async () => {
    await deleteRoute(people.routeId);
  });

  const sp = { by: "people", label: ['cluster=~"sp[1-4]"'] };

  test("By people shows who acknowledged first and who resolved, sorted by a column in the URL", async ({
    page,
  }) => {
    const csp = watchCsp(page);
    await signInAdmin(page);
    await page.goto("/statistics");
    for (const view of ["By route", "By integration", "By team", "By people"]) {
      await expect(page.getByRole("radio", { name: view })).toBeVisible();
    }
    // No status tabs and no search box; the filters of every view.
    await expect(page.getByRole("tab")).toHaveCount(0);
    await expect(page.getByRole("searchbox", { name: "Search" })).toHaveCount(0);
    await expect(page.getByText("The filters apply to every view.")).toBeVisible();
    await page.getByRole("radio", { name: "By people" }).click();
    await expect(page).toHaveURL(/by=people/);
    await page.getByLabel("Label filters").fill('cluster=~"sp[1-4]"');
    await page.getByRole("button", { name: "Add matcher" }).click();
    await expect(page).toHaveURL(/label=/);

    await expect(page.getByRole("table", { name: "Statistics by people" })).toBeVisible();
    await expect(page.getByTestId("people-name")).toHaveText([P.alice, P.bob, P.terraform]);
    expect(await personRow(page, P.alice)).toEqual(["3", "0", "4 min", "27 min"]);
    expect(await personRow(page, P.bob)).toEqual(["1", "2", "10 min", "10 min"]);
    expect(await personRow(page, P.terraform)).toEqual(["0", "1", "—", "—"]);
    await expect(page.getByTestId("people-service-account")).toHaveText("Service account");
    await expect(page.getByRole("columnheader", { name: "Acknowledged first" })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    await shot(page, "statistics-by-people");

    // "Resolved": bob, terraform, alice; again: the other way round.
    await page.getByTestId("sort-resolved").click();
    await expect(page).toHaveURL(/sort=-resolved/);
    await expect(page.getByTestId("people-name")).toHaveText([P.bob, P.terraform, P.alice]);
    await expect(page.getByRole("columnheader", { name: "Resolved" })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    await page.getByTestId("sort-resolved").click();
    await expect(page).toHaveURL(/sort=resolved(&|$)/);
    await expect(page.getByTestId("people-name")).toHaveText([P.alice, P.terraform, P.bob]);
    await expect(page.getByRole("columnheader", { name: "Resolved" })).toHaveAttribute(
      "aria-sort",
      "ascending",
    );
    for (const name of ["Name", "Acknowledged first", "Median", "95th percentile"]) {
      await expect(page.getByRole("columnheader", { name, exact: true })).toHaveAttribute(
        "aria-sort",
        "none",
      );
    }
    // The sorting survives a reload, and the default sorting leaves the URL.
    await page.reload();
    await expect(page.getByTestId("people-name")).toHaveText([P.alice, P.terraform, P.bob]);
    await page.getByTestId("sort-acknowledged_first").click();
    await expect(page).not.toHaveURL(/sort=/);
    await expect(page.getByTestId("people-name")).toHaveText([P.alice, P.bob, P.terraform]);
    await page.getByTestId("sort-acknowledged_first").click();
    await expect(page).toHaveURL(/sort=acknowledged_first/);
    await expect(page.getByTestId("people-name")).toHaveText([P.terraform, P.bob, P.alice]);

    // C-09.AC-27: after Takeover, Unacknowledge and Reopen the Alert Group counts for the first to acknowledge.
    await page.goto(statsUrl({ by: "people", label: ['cluster="sp0"'] }));
    await expect(page.getByTestId("people-name")).toHaveText([P.alice]);
    const [first, resolved, median] = await personRow(page, P.alice);
    expect([first, resolved, median]).toEqual(["1", "0", "5 min"]);
    expect(csp).toEqual([]);
  });

  test("By team counts an Alert Group of two Teams for each, and a Team opens By people", async ({
    page,
  }) => {
    await signInAdmin(page);
    await page.goto(statsUrl({ by: "team", label: ['cluster=~"tp[1-3]"'], period: "30d" }));
    await expect(page.getByRole("table", { name: "Statistics by team" })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: "Team" })).toBeVisible();
    const team = (name: string) =>
      page.getByTestId("statistics-row").filter({
        has: page.getByTestId("statistics-name").getByText(name, { exact: true }),
      });
    await expect(team(P.payments).getByTestId("statistics-count")).toHaveText("2");
    await expect(team(P.search).getByTestId("statistics-count")).toHaveText("1");
    await shot(page, "statistics-by-team");

    // The name is a link, not the way to the days: those open with "Per day".
    await expect(page.getByTestId("statistics-days")).toHaveCount(0);
    await team(P.payments).getByRole("button", { name: "Per day" }).click();
    const days = page.getByRole("table", { name: `Alert Groups per day of start: ${P.payments}` });
    await expect(days.getByTestId("day-count")).toHaveText("2");

    await team(P.search).getByRole("link", { name: P.search }).click();
    await expect(page).toHaveURL(/by=people/);
    await expect(page).toHaveURL(new RegExp(`team=${people.searchId}`));
    await expect(page).toHaveURL(/period=30d/);
    await expect(page.getByRole("radio", { name: "By people" })).toBeChecked();
    await expect(page.getByLabel("Team", { exact: true })).toHaveValue(people.searchId);
    await expect(page.getByLabel("Period")).toHaveValue("30d");
    await expect(
      page.getByTestId("statistics-filters").getByText('cluster=~"tp[1-3]"'),
    ).toBeVisible();
    // The people of the Alert Group the two Teams share: alice acknowledged it first.
    await expect(page.getByTestId("people-name")).toHaveText([P.alice]);
    expect((await personRow(page, P.alice)).slice(0, 2)).toEqual(["1", "0"]);
  });

  test("the filters apply to every view, stay when the view changes and live in the URL", async ({
    page,
    browser,
  }) => {
    await signInAdmin(page);
    await page.goto(statsUrl({ by: "route", ...filtersOf(people) }));
    for (const view of ["By route", "By integration", "By team"]) {
      await page.getByRole("radio", { name: view }).click();
      await expect(page.getByRole("radio", { name: view })).toBeChecked();
      await expect.poll(() => total(page, "statistics-count")).toBe(2);
      await expect(page.getByTestId("filter-severity").getByRole("list")).toContainText("critical");
      await expect(
        page.getByTestId("statistics-filters").getByText('cluster=~"fp."'),
      ).toBeVisible();
    }
    await page.getByRole("radio", { name: "By people" }).click();
    await expect(page.getByTestId("people-name")).toHaveText([P.alice, P.bob]);
    expect((await personRow(page, P.alice)).slice(0, 2)).toEqual(["1", "0"]);
    expect((await personRow(page, P.bob)).slice(0, 2)).toEqual(["1", "0"]);

    // A filter set on the page reaches the URL.
    await page.getByLabel("Urgent", { exact: true }).selectOption("yes");
    await expect(page).toHaveURL(/urgent=true/);
    await expect(page.getByTestId("people-name")).toHaveText([P.alice, P.bob]);
    await page.getByLabel("Reopened", { exact: true }).selectOption("yes");
    await expect(page).toHaveURL(/reopened=true/);
    await expect(
      page.getByText("Nobody acknowledged or resolved an Alert Group in this period."),
    ).toBeVisible();
    await page.getByLabel("Reopened", { exact: true }).selectOption("");
    await page.getByLabel("Urgent", { exact: true }).selectOption("");
    await expect(page).not.toHaveURL(/urgent=|reopened=/);

    // "Acknowledged first by": a User, with no Service account among the choices.
    const picker = page.getByLabel("Acknowledged first by");
    await expect(picker.getByRole("option", { name: P.terraform })).toHaveCount(0);
    await picker.selectOption({ label: P.bob });
    await expect(page).toHaveURL(new RegExp(`first_acknowledged_by=${people.bobId}`));
    await expect(page.getByTestId("people-name")).toHaveText([P.bob]);
    await picker.selectOption("");
    await expect(page).not.toHaveURL(/first_acknowledged_by/);

    // The URL opened in a new browser context, as a Viewer, shows the same view, values and filters.
    const url = page.url();
    const context = await browser.newContext();
    try {
      const viewer = await context.newPage();
      await signIn(viewer, P.viewer, TEAM_SPEC_PASSWORD);
      await viewer.goto(url);
      await expect(viewer.getByRole("radio", { name: "By people" })).toBeChecked();
      await expect(viewer.getByTestId("people-name")).toHaveText([P.alice, P.bob]);
      await expect(viewer.getByTestId("filter-severity").getByRole("list")).toContainText(
        "critical",
      );
      await expect(
        viewer.getByTestId("statistics-filters").getByText('cluster=~"fp."'),
      ).toBeVisible();
      expect(await personRow(viewer, P.alice)).toEqual(await personRow(page, P.alice));
      await viewer.goto(statsUrl({ by: "team", ...filtersOf(people) }));
      await expect(viewer.getByRole("table", { name: "Statistics by team" })).toBeVisible();
      await expect.poll(() => total(viewer, "statistics-count")).toBe(2);
      // C-09.AC-33: a Viewer sees what the Admin sees.
      await viewer.goto(statsUrl(sp));
      expect(await personRow(viewer, P.alice)).toEqual(["3", "0", "4 min", "27 min"]);
    } finally {
      await context.close();
    }
  });

  test("Acknowledged first by Me leaves the Alert Groups the signed-in User acknowledged first", async ({
    page,
  }) => {
    await signIn(page, P.alice, TEAM_SPEC_PASSWORD);
    const filters = {
      severity: ["critical"],
      label: ['namespace="payments"', 'cluster=~"fp."'],
      first_acknowledged_by: "me",
    };
    for (const view of ["route", "integration", "team"]) {
      await page.goto(statsUrl({ by: view, ...filters }));
      await expect(page.getByTestId("statistics-table")).toBeVisible();
      await expect.poll(() => total(page, "statistics-count")).toBe(1);
    }
    await page.goto(statsUrl({ by: "people", ...filters }));
    await expect(page.getByTestId("people-name")).toHaveText([P.alice]);
    expect((await personRow(page, P.alice)).slice(0, 2)).toEqual(["1", "0"]);
    await expect(page.getByLabel("Acknowledged first by")).toHaveValue("me");
  });

  test("shows the row Unknown last, whatever the sort", async ({ page }) => {
    await signInAdmin(page);
    // An Alert Group acknowledged before Muster recorded who acknowledged first.
    await sql("UPDATE alert_groups SET first_acknowledged_by_user_id = NULL WHERE public_id = $1", [
      people.groups.sp4,
    ]);
    try {
      await page.goto(statsUrl(sp));
      await expect(page.getByTestId("people-name")).toHaveText([
        P.alice,
        P.bob,
        P.terraform,
        "Unknown",
      ]);
      const unknown = page.getByTestId("statistics-people-row").last();
      await expect(unknown).toContainText(
        "Acknowledged before Muster recorded who acknowledged first.",
      );
      await expect(unknown.getByTestId("people-resolved")).toHaveText("—");
      expect(await personRow(page, P.bob)).toEqual(["0", "2", "—", "—"]);
      for (const sort of ["sort-resolved", "sort-resolved", "sort-name", "sort-name"]) {
        await page.getByTestId(sort).click();
        await expect(page.getByTestId("people-name").last()).toHaveText("Unknown");
      }
      // The count of time to acknowledge By route adds up with By people.
      await page.goto(statsUrl({ by: "route", label: sp.label }));
      await expect.poll(() => total(page, "statistics-ack-count")).toBe(4);
      // With "Acknowledged first by" set, the row is left out; Bob still resolved two of Alice's.
      await page.goto(statsUrl({ ...sp, first_acknowledged_by: people.aliceId }));
      await expect(page.getByTestId("people-name")).toHaveText([P.alice, P.bob]);
      expect(await personRow(page, P.bob)).toEqual(["0", "2", "—", "—"]);
    } finally {
      await sql(
        "UPDATE alert_groups SET first_acknowledged_by_user_id = (SELECT id FROM users WHERE public_id = $1) WHERE public_id = $2",
        [people.bobId, people.groups.sp4],
      );
    }
  });

  test("every view with the filters open has no horizontal scrolling, wide and at 360 px", async ({
    page,
  }) => {
    await signInAdmin(page);
    const filters = { severity: ["critical"], label: ['namespace="payments"'], urgent: true };
    for (const view of ["route", "integration", "team", "people"]) {
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.goto(statsUrl({ by: view, ...filters }));
      await expect(page.getByTestId("statistics-filters")).toBeVisible();
      await expectNoHorizontalScroll(page);
      await page.setViewportSize({ width: 360, height: 740 });
      await expect(page.getByTestId("filters-button")).toBeVisible();
      await expectNoHorizontalScroll(page);
      await page.getByTestId("filters-button").click();
      await expect(page.getByRole("dialog").getByTestId("statistics-filters")).toBeVisible();
      await expectNoHorizontalScroll(page);
      await shot(page, `statistics-${view}-360-filters`);
      await page.getByRole("button", { name: "Show statistics" }).click();
    }
    await page.setViewportSize({ width: 1280, height: 800 });
  });

  test("shows the views, the filters and the sorting in Russian", async ({ page }) => {
    const admin = await adminApi();
    const { token } = await admin.createUser("stp-olga", "admin");
    await admin.call("POST", "/api/v1/password-setups", { token, password: "stp-olga-password-1" });
    await admin.dispose();
    await signIn(page, "stp-olga", "stp-olga-password-1");
    await page.goto("/profile");
    await page.getByLabel("Language").selectOption("ru");
    await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

    await page.goto(statsUrl({ ...sp, sort: "-resolved" }));
    await expect(page.getByRole("radio", { name: "По людям" })).toBeChecked();
    for (const view of ["По маршрутам", "По интеграциям", "По командам"]) {
      await expect(page.getByRole("radio", { name: view })).toBeVisible();
    }
    await expect(page.getByRole("table", { name: "Статистика по людям" })).toBeVisible();
    await expect(page.getByText("Фильтры действуют на все виды.")).toBeVisible();
    await expect(page.getByLabel("Первым подтвердил")).toBeVisible();
    for (const name of ["Имя", "Подтверждено первым", "Закрыто", "Время до подтверждения"]) {
      await expect(page.getByRole("columnheader", { name, exact: true })).toBeVisible();
    }
    await expect(page.getByTestId("people-name")).toHaveText([P.bob, P.terraform, P.alice]);
    await expect(page.getByTestId("people-service-account")).toHaveText("Сервисный аккаунт");
    expect(await personRow(page, P.alice)).toEqual(["3", "0", "4 мин", "27 мин"]);
    await shot(page, "statistics-by-people-ru");

    await page.goto(statsUrl({ by: "team", label: ['cluster=~"tp[1-3]"'] }));
    await expect(page.getByRole("table", { name: "Статистика по командам" })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: "Команда" })).toBeVisible();
    await page.getByRole("button", { name: `По дням: ${P.payments}` }).click();
    await expect(
      page.getByRole("table", { name: `Группы алертов по дням начала: ${P.payments}` }),
    ).toBeVisible();
    await page.getByRole("link", { name: P.search }).click();
    await expect(page.getByRole("radio", { name: "По людям" })).toBeChecked();
    await expect(page.getByLabel("Команда", { exact: true })).toHaveValue(people.searchId);

    // Nobody acknowledged or resolved anything in a period without Alert Groups.
    await page.goto(statsUrl({ by: "people", label: ['cluster="nowhere"'] }));
    await expect(
      page.getByText("За этот период никто не подтверждал и не закрывал группы алертов."),
    ).toBeVisible();

    for (const view of ["route", "integration", "team", "people"]) {
      await page.setViewportSize({ width: 360, height: 740 });
      await page.goto(
        statsUrl({ by: view, severity: ["critical"], label: ['namespace="payments"'] }),
      );
      await expectNoHorizontalScroll(page);
      await page.getByTestId("filters-button").click();
      await expect(page.getByRole("dialog").getByTestId("statistics-filters")).toBeVisible();
      await expectNoHorizontalScroll(page);
      await page.getByRole("button", { name: "Показать статистику" }).click();
    }
    await page.setViewportSize({ width: 1280, height: 800 });
  });
});
