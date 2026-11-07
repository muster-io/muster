// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The delivery state of an Alert Group and the "Delivery problem" filter against `muster dev` and its fake Mattermost
// server: Verification step 7 of their story. The fake answers the next edit of a post with 400, so acknowledging an
// Alert Group of the Route "ds" ends as "Not delivered: …" in its Delivery section; the list with "Delivery problem"
// (kept in the URL) lists it with the mark, and in words on a phone; a later change is delivered, and the row loses
// the mark; "Delivered" opens the post in the fake server in a new tab. A reader of Russian sees the section at phone
// width. A delivery that ends seconds after its change shows its end in the open page and in the list without a
// reload, through the alert-group hint of the delivery worker. No page scrolls sideways at 360 px; no Content Security
// Policy violation.

import { type Browser, expect, test, type Page } from "@playwright/test";

import {
  type Api,
  TEAM_SPEC_PASSWORD,
  adminApi,
  alertGroupOf,
  createRoutingRoute,
  dropRouting,
  ensureLocalUser,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  removeIntegration,
  seedRouting,
  setTeamDestinations,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

/** The control API of the fake Mattermost server of `muster dev`. */
const FMM = "http://127.0.0.1:18065/_fake";
const SERVER_URL = "http://127.0.0.1:18065";
const TEAM = "ds-team";
const PROBLEM = "Delivery problem: open the Alert Group to see which Destination";

interface Named {
  id: string;
  name: string;
}

interface Delivery {
  state: string;
  error?: string | null;
  message_url?: string | null;
}

let integrationId = "";
let destinationId = "";
let connectionId = "";

const NOBODY = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (k) => [k, NOBODY],
  ),
);

async function cleanUp(admin: Api): Promise<void> {
  await removeIntegration(admin, "ds-ui");
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => x.name === "ds")) {
    await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${r.id}`);
  }
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => x.name === "ds-alerts")) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
  const connections = await admin.call<{ items: Named[] }>("GET", "/api/v1/connections?limit=500");
  for (const c of connections.items.filter((x) => x.name === "mm-ds")) {
    await admin.call("DELETE", `/api/v1/connections/${c.id}`);
  }
}

async function deliveries(alertGroupId: string): Promise<Delivery[]> {
  const admin = await adminApi();
  try {
    return (
      await admin.call<{ items: Delivery[] }>(
        "GET",
        `/api/v1/alert-groups/${alertGroupId}/deliveries`,
      )
    ).items;
  } finally {
    await admin.dispose();
  }
}

async function stateOf(alertGroupId: string): Promise<string> {
  return (await deliveries(alertGroupId))[0]?.state ?? "none";
}

/** The Alert Group of the Alert with the label cluster. */
async function groupOf(cluster: string): Promise<{ id: string; number: number }> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { alert_group?: { id: string; number: number } }[] }>(
      "GET",
      `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent(`cluster="${cluster}"`)}`,
    );
    const group = page.items[0]?.alert_group;
    expect(group, `the alert group of ${cluster}`).toBeDefined();
    return group ?? { id: "", number: 0 };
  } finally {
    await admin.dispose();
  }
}

async function fault(body: Record<string, unknown>): Promise<void> {
  const res = await fetch(`${FMM}/faults`, { method: "POST", body: JSON.stringify(body) });
  expect(res.ok, `POST /_fake/faults: ${res.status}`).toBe(true);
}

function row(page: Page, number: number) {
  return page
    .getByRole("table")
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: `#${number}`, exact: true }) });
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    const connection = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
      type: "mattermost",
      name: "mm-ds",
      server_url: SERVER_URL,
      bot_token: "mm-dev-token",
      proxy: { enabled: false },
      limiter: { limit: 50, per_seconds: 1 },
    });
    connectionId = connection.id;
    const created = await admin.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "mattermost",
        name: "ds-alerts",
        connection_id: connectionId,
        team_id: "team-dev",
        channel_id: "ch-alerts-prod",
        mentions: MENTIONS,
        limiter: { limit: 50, per_seconds: 1 },
      },
    );
    destinationId = created.destination.id;
  } finally {
    await admin.dispose();
  }
  integrationId = await fakeIntegration("ds-ui");
  const api = await adminApi();
  try {
    const profiles = await api.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    await api.call("POST", "/api/v1/routes", {
      name: "ds",
      matchers: [{ label: "team", op: "=", value: TEAM }],
      group_key: ["alertname", "cluster"],
      destination_ids: [destinationId],
      policy: profiles.items.find((p) => p.id === "paging")?.policy,
    });
  } finally {
    await api.dispose();
  }
  await fam("PUT", "/groups/ds", { receiver: "ds-ui", route: "{}", labels: { alertname: "Ds" } });
  for (const c of ["d1", "d2"]) {
    await fam("PUT", `/groups/ds/alerts/${c}`, { labels: { team: TEAM, cluster: c } });
  }
  await notify(integrationId, "ds", { reason: "first notification" });
});

test.afterAll(async () => {
  await fetch(`${FMM}/faults`, { method: "DELETE" });
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test("a failed edit shows Not delivered, the Delivery problem filter lists it, and a later delivery clears it", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  const g1 = await groupOf("d1");
  await expect.poll(() => stateOf(g1.id), { timeout: 30_000 }).toBe("delivered");

  // The next edit of a post is answered with 400, which ends the delivery as Not delivered.
  await fault({
    path: "/api/v4/posts/*/patch",
    status: 400,
    body: '{"message":"bad patch"}',
    times: 1,
  });
  await signInAdmin(page);
  await page.goto(`/alert-groups/${g1.id}`);
  const section = page.getByTestId("alert-group-deliveries");
  await expect(section.getByTestId("delivery-state")).toHaveText("Delivered (opens in a new tab)");
  await page.getByRole("button", { name: "Acknowledge", exact: true }).click();
  await expect(section.getByTestId("delivery-state")).toHaveText(/^Not delivered: .*400/);
  const failed = (await deliveries(g1.id))[0];
  expect(failed?.state).toBe("not_delivered");
  await expect(section.getByTestId("delivery-state")).toHaveText(`Not delivered: ${failed?.error}`);
  await expect(section.getByRole("link", { name: "ds-alerts" })).toBeVisible();
  await shot(page, "delivery-not-delivered");

  // The filter "Delivery problem", kept in the URL, lists it with the mark.
  await page.goto(`/alert-groups?q=%23${g1.number}`);
  await expect(row(page, g1.number).getByTestId("delivery-problem-mark")).toHaveAttribute(
    "title",
    PROBLEM,
  );
  const filter = page.getByTestId("alert-group-filters").getByLabel("Delivery problem");
  await filter.click();
  await expect(filter).toBeChecked();
  await expect.poll(() => new URL(page.url()).searchParams.get("delivery_problem")).toBe("true");
  await page.goto("/alert-groups?delivery_problem=true");
  await expect(
    page.getByTestId("alert-group-filters").getByLabel("Delivery problem"),
  ).toBeChecked();
  await expect(row(page, g1.number)).toHaveCount(1);
  await expect(row(page, g1.number).getByTestId("delivery-problem-mark")).toBeVisible();
  const g2 = await groupOf("d2");
  await expect(row(page, g2.number)).toHaveCount(0);
  await shot(page, "list-delivery-problem");

  // On a phone the mark is words in the row.
  const phone = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const phonePage = await phone.newPage();
  const phoneCsp = watchCsp(phonePage);
  await signInAdmin(phonePage);
  await phonePage.goto("/alert-groups?delivery_problem=true");
  const compact = phonePage.getByTestId("alert-group-row").filter({ hasText: `#${g1.number}` });
  await expect(compact.getByTestId("delivery-problem-text")).toHaveText(PROBLEM);
  await expectNoHorizontalScroll(phonePage);
  await shot(phonePage, "list-delivery-problem-360");
  await phonePage.goto(`/alert-groups/${g1.id}`);
  await expect(phonePage.getByTestId("delivery-state")).toHaveText(/^Not delivered: /);
  await expectNoHorizontalScroll(phonePage);
  await shot(phonePage, "delivery-360");
  expect(phoneCsp).toEqual([]);
  await phone.close();

  // A later change is delivered: the row loses the mark and leaves the filter.
  await page.goto(`/alert-groups/${g1.id}`);
  await page.getByRole("button", { name: "Unacknowledge", exact: true }).click();
  await expect(section.getByTestId("delivery-state")).toHaveText("Delivered (opens in a new tab)");
  await page.goto(`/alert-groups?delivery_problem=true&q=%23${g1.number}`);
  await expect(page.getByText("No Alert Groups match these filters.")).toBeVisible();
  await page.goto(`/alert-groups?q=%23${g1.number}`);
  await expect(row(page, g1.number)).toHaveCount(1);
  await expect(row(page, g1.number).getByTestId("delivery-problem-mark")).toHaveCount(0);

  // "Delivered" opens the post in the fake server in a new tab.
  await page.goto(`/alert-groups/${g1.id}`);
  const link = section.getByRole("link", { name: /^Delivered/ });
  await expect(link).toHaveAttribute("target", "_blank");
  await expect(link).toHaveAttribute("rel", "noopener noreferrer");
  const [popup] = await Promise.all([page.waitForEvent("popup"), link.click()]);
  expect(popup.url()).toMatch(/^http:\/\/127\.0\.0\.1:18065\/dev\/pl\/[a-z0-9]+$/);
  await popup.close();
  expect(csp).toEqual([]);
});

test("a reader of Russian sees the Delivery section at phone width", async ({ browser }) => {
  const admin = await adminApi();
  const { token } = await admin.createUser("ds-viewer-ru", "viewer");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "ds-viewer-ru-1" });
  await admin.dispose();
  // The Viewer stays: users.spec.ts counts the deleted Viewers of the run.
  await russianViewer(browser);
});

test("the end of a delivery reaches the open page and the list without a reload", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  const g1 = await groupOf("d1");
  await expect.poll(() => stateOf(g1.id), { timeout: 30_000 }).toBe("delivered");
  await signInAdmin(page);
  await page.goto(`/alert-groups/${g1.id}`);
  const state = page.getByTestId("alert-group-deliveries").getByTestId("delivery-state");
  await expect(state).toHaveText("Delivered (opens in a new tab)");
  const listContext = await browser.newContext();
  const list = await listContext.newPage();
  await signInAdmin(list);
  await list.goto(`/alert-groups?q=%23${g1.number}`);
  await expect(row(list, g1.number)).toHaveCount(1);
  await expect(row(list, g1.number).getByTestId("delivery-problem-mark")).toHaveCount(0);

  // The edit is refused only after a few seconds: the dispatcher's hint shows it pending, and only the delivery
  // worker's hint can show its end, since nothing polls and nothing reloads.
  await fault({
    path: "/api/v4/posts/*/patch",
    status: 400,
    body: '{"message":"bad patch"}',
    delay_ms: 3000,
    times: 1,
  });
  const admin = await adminApi();
  try {
    await admin.call("POST", `/api/v1/alert-groups/${g1.id}/acknowledge`);
    await expect(state).toHaveText("Pending");
    await expect(state).toHaveText(/^Not delivered: .*400/, { timeout: 15_000 });
    await expect(row(list, g1.number).getByTestId("delivery-problem-mark")).toBeVisible();

    // A later change is delivered: the section and the row follow it the same way.
    await admin.call("POST", `/api/v1/alert-groups/${g1.id}/unacknowledge`);
    await expect(state).toHaveText("Delivered (opens in a new tab)", { timeout: 15_000 });
    await expect(row(list, g1.number).getByTestId("delivery-problem-mark")).toHaveCount(0);
  } finally {
    await admin.dispose();
  }
  await shot(page, "delivery-live");
  await listContext.close();
  expect(csp).toEqual([]);
});

async function russianViewer(browser: Browser): Promise<void> {
  const g2 = await groupOf("d2");
  await expect.poll(() => stateOf(g2.id), { timeout: 30_000 }).toBe("delivered");
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "ds-viewer-ru", "ds-viewer-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
  await page.goto(`/alert-groups/${g2.id}`);
  const section = page.getByTestId("alert-group-deliveries");
  await expect(section.getByRole("heading", { name: "Доставка" })).toBeVisible();
  await expect(section.getByTestId("delivery-state")).toHaveText(
    "Доставлено (откроется в новой вкладке)",
  );
  await expect(section.getByTestId("destination-health")).toHaveText("Работает");
  await expectNoHorizontalScroll(page);
  await page.goto("/alert-groups");
  await page.getByRole("button", { name: /^Фильтры/ }).click();
  await expect(page.getByLabel("Проблема доставки")).toBeVisible();
  await shot(page, "delivery-ru-360");
  expect(csp).toEqual([]);
  await context.close();
}

const ENTRY_PREFIX = "dse";

test("the delivery state lists by which entry each Destination was reached, and the Timeline shows each Destination joined", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  const seed = await seedRouting(ENTRY_PREFIX);
  const infraCritical = seed.destination("infra-critical");
  const infraNotice = seed.destination("infra-notice");
  const paymentsCritical = seed.destination("payments-critical");
  const paymentsNotice = seed.destination("payments-notice");
  const searchNotice = seed.destination("search-notice");
  const alert = seed.alertName("Entry");
  const state = (name: string) =>
    page
      .getByTestId("alert-group-deliveries")
      .getByTestId("delivery-row")
      .filter({ has: page.getByTestId("delivery-destination").getByText(name, { exact: true }) });
  const entries = (name: string) => state(name).getByTestId("delivery-entry");
  const joined = (name: string) =>
    page.getByTestId("timeline-entry").filter({
      has: page.getByTestId("timeline-text").getByText(`Joined ${name}`, { exact: true }),
    });
  try {
    const admin = await adminApi();
    try {
      await ensureLocalUser(admin, `${ENTRY_PREFIX}-ru`, "responder");
      await setTeamDestinations(admin, seed.slugs.pay, [
        { destination_id: paymentsCritical.id, severity_condition: "critical" },
        { destination_id: paymentsNotice.id, severity_condition: "below_critical" },
      ]);
      await setTeamDestinations(admin, seed.slugs.sea, [
        { destination_id: searchNotice.id, severity_condition: "any" },
      ]);
    } finally {
      await admin.dispose();
    }
    // The key lacks the Team label, so that the Alerts of the Teams share one Alert Group.
    await createRoutingRoute(seed, `Entries-${ENTRY_PREFIX}`, {
      entries: [
        { destination_id: infraCritical.id, severity_condition: "critical" },
        { destination_id: infraNotice.id, severity_condition: "below_critical" },
      ],
      deliverToTeams: true,
      groupKey: ["alertname", "system"],
    });
    await seed.fire([{ alertname: alert, id: "pay", team: seed.slugs.pay, severity: "warning" }]);
    const group = await alertGroupOf(seed.integrationId, alert);

    await signInAdmin(page);
    await page.goto(`/alert-groups/${group}`);
    // A warning of payments: the Route's infra-notice and the Team's payments-notice, each with its condition.
    await expect(state(infraNotice.name)).toHaveCount(1, { timeout: 30_000 });
    await expect(entries(infraNotice.name)).toHaveText(["Route · Severity < critical"]);
    await expect(state(paymentsNotice.name)).toHaveCount(1);
    await expect(entries(paymentsNotice.name)).toHaveText([
      `Team ${seed.slugs.pay} · Severity < critical`,
    ]);
    await expect(state(paymentsNotice.name).getByTestId("delivery-entries")).toContainText(
      "Reached by:",
    );
    await expect(state(infraCritical.name)).toHaveCount(0);
    await expect(state(paymentsCritical.name)).toHaveCount(0);
    await expect(state(infraNotice.name).getByTestId("delivery-state")).toHaveText(
      "Delivered (opens in a new tab)",
      { timeout: 30_000 },
    );
    await shot(page, "delivery-entries");

    // An Alert of search into the same Alert Group: search-notice joins, and the Timeline says so.
    await seed.fire([{ alertname: alert, id: "sea", team: seed.slugs.sea, severity: "warning" }]);
    await expect(state(searchNotice.name)).toHaveCount(1, { timeout: 30_000 });
    await expect(entries(searchNotice.name)).toHaveText([`Team ${seed.slugs.sea} · Any severity`]);
    await expect(joined(searchNotice.name)).toHaveCount(1);
    await expect(joined(searchNotice.name).getByTestId("timeline-actor")).toHaveText("Muster");

    // A critical Alert of payments raises the level: infra-critical and payments-critical join, with the
    // conditions that now met them, and the first Destinations keep their entries.
    await seed.fire([
      { alertname: alert, id: "pay-c", team: seed.slugs.pay, severity: "critical" },
    ]);
    await expect(entries(infraCritical.name)).toHaveText(["Route · Severity = critical"], {
      timeout: 30_000,
    });
    await expect(entries(paymentsCritical.name)).toHaveText([
      `Team ${seed.slugs.pay} · Severity = critical`,
    ]);
    await expect(joined(infraCritical.name)).toHaveCount(1);
    await expect(joined(paymentsCritical.name)).toHaveCount(1);
    await expect(entries(infraNotice.name)).toHaveText(["Route · Severity < critical"]);
    await expect(joined(searchNotice.name)).toHaveCount(1);

    // The same page in Russian at 360 px, and no sideways scrolling.
    const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
    const ru = await context.newPage();
    const ruCsp = watchCsp(ru);
    await signIn(ru, `${ENTRY_PREFIX}-ru`, TEAM_SPEC_PASSWORD);
    await ru.goto("/profile");
    await ru.getByLabel(/^(Language|Язык)$/).selectOption("ru");
    await expect(ru.getByRole("navigation", { name: "Основная" })).toBeVisible();
    await ru.goto(`/alert-groups/${group}`);
    const ruRow = ru
      .getByTestId("alert-group-deliveries")
      .getByTestId("delivery-row")
      .filter({
        has: ru.getByTestId("delivery-destination").getByText(paymentsNotice.name, { exact: true }),
      });
    await expect(ruRow.getByTestId("delivery-entry")).toHaveText([
      `Команда ${seed.slugs.pay} · важность < critical`,
    ]);
    await expect(
      ru
        .getByTestId("timeline-text")
        .getByText(`Группа пришла в ${searchNotice.name}`, { exact: true }),
    ).toBeVisible();
    await expectNoHorizontalScroll(ru);
    await shot(ru, "delivery-entries-ru-360");
    expect(ruCsp).toEqual([]);
    await context.close();
  } finally {
    await dropRouting(ENTRY_PREFIX);
  }
  expect(csp).toEqual([]);
});
