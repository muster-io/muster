// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Escalation chain page against `muster dev` (Verification of the chain page story, C-32.AC-14, AC-17): on the Team
// page "New chain" builds the seven steps of scenario 1 — "Call through the Notification gateway" greyed while the demo
// gateway is removed, the chip of the working hours of the Team —, the chain is named on the Team and its page says
// where it writes and for which Severity it runs, with the gateway back the call step is offered, a Telegram
// Destination of the Team warns that a step cannot mention everyone there; "How it went" lists a run that was
// acknowledged with its Page that was not delivered and the step that was skipped; a Responder only reads the chain;
// and the pages read in Russian and fit 360 px.

import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  type Api,
  GATEWAY_NAME,
  NO_MENTIONS,
  TEAM_SPEC_PASSWORD,
  adminApi,
  advance,
  alertGroupOf,
  createChain,
  createSchedule,
  dropRouting,
  ensureGateway,
  ensureLocalUser,
  expectNoHorizontalScroll,
  removeChains,
  removeConnection,
  removeNamed,
  removeSchedulesOf,
  seedRouting,
  setTeamChains,
  setTeamDestinations,
  shot,
  signIn,
  signInAdmin,
  teamIdOf,
  watchCsp,
  type RoutingSeed,
} from "./support";

test.describe.configure({ mode: "serial" });

const KEY = "ec";
const CHAIN = "ec-payments";
const RUN_CHAIN = "ec-run";
const PETR = "ec-petr";
const RESPONDER = "ec-resp";
const RU_ADMIN = "ec-ru";
const TELEGRAM = "ec-tg";

let seed: RoutingSeed;
let teamId = "";
let chainId = "";

/** Removes what the spec made, in the order the references allow: Routes, chains, Schedules, then the Teams. */
async function cleanUp(admin: Api): Promise<void> {
  const routes = await admin.call<{
    items: { id: string; matchers: { value: string }[] }[];
  }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) =>
    x.matchers.some((m) => m.value.startsWith(`infra-${KEY}-`)),
  )) {
    await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${r.id}`);
  }
  await removeChains(admin, "ec-");
  await removeSchedulesOf(admin, [`payments-${KEY}`, `search-${KEY}`, `devops-${KEY}`]);
  await dropRouting(KEY);
  await removeNamed(admin, "/api/v1/destinations", TELEGRAM, "?limit=500");
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    seed = await seedRouting(KEY);
    teamId = await teamIdOf(admin, seed.slugs.pay);
    const petr = await ensureLocalUser(admin, PETR, "responder");
    await admin.call("POST", `/api/v1/teams/${teamId}/users`, { user_id: petr });
    await createSchedule(admin, teamId, "Main", petr);
    await createSchedule(admin, teamId, "Second line", petr);
    await setTeamDestinations(admin, seed.slugs.pay, [
      { destination_id: seed.destination("payments-critical").id, severity_condition: "critical" },
      {
        destination_id: seed.destination("payments-notice").id,
        severity_condition: "below_critical",
      },
    ]);
    await ensureLocalUser(admin, RESPONDER, "responder");
    await ensureLocalUser(admin, RU_ADMIN, "admin");
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    await ensureGateway(admin);
    await cleanUp(admin);
    // The Admin of the Russian spec must not outlive it: the OIDC spec counts the active Admins.
    for (const login of [RU_ADMIN, RESPONDER, PETR]) {
      const found = await admin.call<{ items: { id: string; login?: string }[] }>(
        "GET",
        `/api/v1/user-directory?q=${encodeURIComponent(login)}`,
      );
      for (const u of found.items.filter((x) => x.login === login)) {
        await admin.call("DELETE", `/api/v1/users/${u.id}`);
      }
    }
  } finally {
    await admin.dispose();
  }
});

/** The step of the chain page by its position, from 0. */
function step(page: Page, index: number): Locator {
  return page.getByTestId("chain-step").nth(index);
}

async function addStep(page: Page, kind: string): Promise<void> {
  await page.getByTestId("chain-add-step").getByRole("button", { name: kind, exact: true }).click();
}

test("an Admin builds the chain of the Team with its seven steps, names it on the Team and reads it back", async ({
  page,
}) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  await removeConnection(admin, GATEWAY_NAME);
  await signInAdmin(page);

  // The Teams list links the chains; the Team page has "Escalation chain of the Team" with "New chain".
  await page.goto("/teams");
  await expect(page.getByRole("link", { name: "Escalation chains", exact: true })).toBeVisible();
  await page.getByRole("link", { name: seed.slugs.pay, exact: true }).click();
  const section = page.getByTestId("team-chains-section");
  await expect(
    section.getByRole("heading", { name: "Escalation chain of the Team" }),
  ).toBeVisible();
  await section.getByRole("link", { name: "New chain", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/escalation-chains/new\\?team=${teamId}$`));
  await expect(page.getByLabel("Owning Team")).toHaveValue(teamId);
  await page.getByLabel("Name", { exact: true }).fill(CHAIN);

  // "Add step" offers the seven kinds, the call greyed with its reason while no Notification gateway exists.
  const add = page.getByTestId("chain-add-step");
  for (const kind of [
    "Page the On-call of a Schedule",
    "Page people",
    "Page the whole Team",
    "Wait",
    "Send to an outgoing webhook",
    "Repeat from step",
  ]) {
    await expect(add.getByRole("button", { name: kind, exact: true })).toBeEnabled();
  }
  await expect(
    add.getByRole("button", { name: "Call through the Notification gateway", exact: true }),
  ).toBeDisabled();
  await expect(page.getByTestId("gateway-unavailable")).toContainText(
    "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)",
  );
  await expect(page.getByTestId("chain-steps-hint")).toHaveText(
    "Any step may be limited to working hours and to a Severity condition. Steps through a Notification gateway do not depend on a messenger, but need the gateway.",
  );
  await expect(page.getByTestId("chain-importance")).toContainText(
    "Normal and important. What exactly happens is decided by each person's Notification rules. Muster sends no direct messages: a Page is a Mention in the Thread of the Root message.",
  );

  // Scenario 1 of C-32: the seven steps of payments.
  const main = `${seed.slugs.pay} · Main`;
  const second = `${seed.slugs.pay} · Second line`;
  await addStep(page, "Page the On-call of a Schedule");
  await step(page, 0).getByLabel("Schedule", { exact: true }).selectOption({ label: main });
  await addStep(page, "Wait");
  await step(page, 1).getByLabel("Minutes").fill("10");
  await addStep(page, "Page the On-call of a Schedule");
  await step(page, 2).getByLabel("Schedule", { exact: true }).selectOption({ label: main });
  await step(page, 2).getByLabel("How to Page").selectOption("important");
  await step(page, 2).getByLabel("Severity condition").selectOption("critical");
  await addStep(page, "Wait");
  await step(page, 3).getByLabel("Minutes").fill("15");
  await addStep(page, "Page the On-call of a Schedule");
  await step(page, 4).getByLabel("Schedule", { exact: true }).selectOption({ label: second });
  await step(page, 4).getByLabel("How to Page").selectOption("important");
  await step(page, 4).getByLabel("Severity condition").selectOption("critical");
  await addStep(page, "Page the whole Team");
  await step(page, 5).getByLabel(`Only in the working hours of ${seed.slugs.pay}`).check();
  await addStep(page, "Repeat from step");
  await step(page, 6).getByLabel("Repeat from step", { exact: true }).fill("3");
  await step(page, 6).getByLabel("Times", { exact: true }).fill("2");

  // The steps in words, numbered, with their limits as chips.
  await expect(page.getByTestId("chain-step-text")).toHaveText([
    `Page the On-call of ${seed.slugs.pay} Main · normal`,
    "Wait 10 minutes",
    `Page the On-call of ${seed.slugs.pay} Main · important`,
    "Wait 15 minutes",
    `Page the On-call of ${seed.slugs.pay} Second line · important`,
    `Page the whole Team ${seed.slugs.pay}`,
    "Repeat from step 3, up to 2 times",
  ]);
  await expect(page.getByTestId("chain-step-number")).toHaveText(
    Array.from({ length: 7 }, (_, i) => `Step ${i + 1}`),
  );
  await expect(step(page, 2).getByTestId("chain-step-chips")).toContainText("Severity = critical");
  await expect(step(page, 2).getByTestId("chain-step-chips")).toContainText("always");
  await expect(step(page, 5).getByTestId("chain-step-chips")).toContainText("Severity: any");
  await expect(step(page, 5).getByTestId("chain-step-chips")).toContainText(
    new RegExp(
      `only in the working hours of ${seed.slugs.pay} \\(Mon–Fri \\d+(:\\d\\d)?–\\d+(:\\d\\d)?\\)`,
    ),
  );
  await shot(page, "chain-new");

  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/escalation-chains\/EC[0-9A-Z]+$/);
  chainId = page.url().split("/").at(-1) ?? "";
  await expect(page.getByRole("heading", { name: CHAIN, level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: /^Guide: Escalation chains/ })).toHaveAttribute(
    "href",
    /\/concepts\/escalation-chains\/$/,
  );
  await expect(
    page.getByTestId("chain-steps").getByRole("link", { name: /^Learn more: Steps of an/ }),
  ).toHaveAttribute("href", /\/concepts\/escalation-chains\/#steps$/);

  // It reads back as it was saved, the waits in minutes.
  await page.reload();
  await expect(page.getByTestId("chain-step")).toHaveCount(7);
  await expect(page.getByTestId("chain-step-text").nth(1)).toHaveText("Wait 10 minutes");
  await expect(step(page, 3).getByLabel("Minutes")).toHaveValue("15");
  await expect(step(page, 6).getByLabel("Repeat from step", { exact: true })).toHaveValue("3");
  await expect(page.getByTestId("chain-writes")).toContainText("Writes in no Thread yet");

  // The Team names the chain, with the condition critical: the page says where it writes and for which Severity.
  await page.goto(`/teams/${teamId}`);
  const named = page.getByTestId("team-chains-section");
  await named.getByLabel("Add Escalation chain").selectOption({ label: CHAIN });
  await named.getByTestId(`team-chain-condition-${chainId}`).selectOption("critical");
  await named.getByRole("button", { name: "Save", exact: true }).click();
  await expect(named.getByTestId("team-chains-status")).toHaveText("Saved.");
  await expect(named.getByTestId("team-owned-chain")).toContainText(CHAIN);
  await page.goto(`/escalation-chains/${chainId}`);
  await expect(page.getByTestId("chain-writes")).toContainText(
    new RegExp(
      "Writes only in the Threads of payments-(critical|notice)-ec and payments-(notice|critical)-ec\\. Starts at publication\\.",
    ),
  );
  await expect(page.getByTestId("chain-stops")).toContainText(
    "Stops when the Alert Group is acknowledged, snoozed or resolved; starts over on Unacknowledge, the end of a Snooze, a Reopen, and a rise to critical that ends an acknowledgement or a Snooze.",
  );
  await expect(page.getByTestId("chain-run-for")).toContainText(
    `Team ${seed.slugs.pay}: Severity = critical`,
  );
  await expect(page.getByTestId("chain-warnings")).toHaveCount(0);

  // With the demo gateway back, "Call through the Notification gateway" is available.
  await ensureGateway(admin);
  await page.reload();
  await expect(
    page
      .getByTestId("chain-add-step")
      .getByRole("button", { name: "Call through the Notification gateway", exact: true }),
  ).toBeEnabled();
  await expect(page.getByTestId("gateway-unavailable")).toHaveCount(0);

  // A Telegram Destination of the Team: step 6 cannot mention the Team there, and the choice is greyed.
  const connections = await admin.call<{ items: { id: string; name: string }[] }>(
    "GET",
    "/api/v1/connections?type=telegram&limit=500",
  );
  const telegram = connections.items.find((c) => c.name === "Example Telegram");
  expect(telegram, "the Telegram Connection of muster dev").toBeDefined();
  const tg = await admin.call<{ destination: { id: string } }>("POST", "/api/v1/destinations", {
    type: "telegram",
    name: TELEGRAM,
    connection_id: telegram?.id,
    channel_id: "@muster_alerts",
    mentions: NO_MENTIONS,
    limiter: { limit: 30, per_seconds: 1 },
  });
  await setTeamDestinations(admin, seed.slugs.pay, [
    { destination_id: seed.destination("payments-critical").id, severity_condition: "critical" },
    {
      destination_id: seed.destination("payments-notice").id,
      severity_condition: "below_critical",
    },
    { destination_id: tg.destination.id },
  ]);
  await page.reload();
  await expect(page.getByTestId("chain-warning")).toContainText(
    `Step 6 cannot mention the Team or everyone in ${TELEGRAM}.`,
  );
  // While another Destination can mention, the choices stay and the warning says what the Telegram one cannot do; once
  // only the Telegram Destination is left, they are greyed.
  const whom = step(page, 5).getByLabel("Whom 'Page the whole Team' calls");
  await expect(whom.locator("option").nth(1)).toBeEnabled();
  await setTeamDestinations(admin, seed.slugs.pay, [{ destination_id: tg.destination.id }]);
  await page.reload();
  await expect(whom.locator("option").nth(1)).toBeDisabled();
  await expect(whom.locator("option").nth(1)).toContainText(
    "@channel — unavailable: Telegram cannot mention everyone or groups",
  );
  await shot(page, "chain-page");
  await setTeamDestinations(admin, seed.slugs.pay, [
    { destination_id: seed.destination("payments-critical").id, severity_condition: "critical" },
    {
      destination_id: seed.destination("payments-notice").id,
      severity_condition: "below_critical",
    },
  ]);

  await admin.dispose();
  expect(csp).toEqual([]);
});

test("How it went lists the run of an Alert Group that was acknowledged, its undelivered Page and its skipped step", async ({
  page,
}) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  const petr = await ensureLocalUser(admin, PETR, "responder");
  const runChain = await createChain(admin, RUN_CHAIN, teamId, [
    { kind: "page_people", user_ids: [petr], importance: "normal" },
    { kind: "wait", wait_seconds: 600 },
    {
      kind: "page_people",
      user_ids: [petr],
      importance: "important",
      severity_condition: "critical",
    },
    { kind: "wait", wait_seconds: 1800 },
  ]);
  const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
    "GET",
    "/api/v1/route-profiles",
  );
  await admin.call("POST", "/api/v1/routes", {
    name: `Infrastructure-${KEY}`,
    matchers: [{ label: "category", op: "=", value: seed.category }],
    group_key: ["alertname", "system", "team"],
    destination_entries: [
      { destination_id: seed.destination("infra-notice").id, severity_condition: "any" },
    ],
    chain_entries: [{ chain_id: runChain, severity_condition: "any" }],
    deliver_to_teams: false,
    policy: profiles.items.find((p) => p.id === "paging")?.policy,
  });
  const alertname = seed.alertName("Oom");
  await seed.fire([{ alertname, team: seed.slugs.pay, severity: "warning" }]);
  const alertGroup = await alertGroupOf(seed.integrationId, alertname);

  // Step 1 Pages petr, who has no Mattermost account; ten minutes later step 3 is skipped for a warning.
  await expect
    .poll(async () => {
      const runs = await admin.call<{ items: { steps: { number: number }[] }[] }>(
        "GET",
        `/api/v1/escalation-chains/${runChain}/runs`,
      );
      return runs.items[0]?.steps.map((s) => s.number) ?? [];
    })
    .toEqual([1, 2]);
  await advance(600);
  await expect
    .poll(async () => {
      const runs = await admin.call<{ items: { steps: { number: number }[] }[] }>(
        "GET",
        `/api/v1/escalation-chains/${runChain}/runs`,
      );
      return runs.items[0]?.steps.map((s) => s.number) ?? [];
    })
    .toEqual([1, 2, 3, 4]);
  await admin.call("POST", `/api/v1/alert-groups/${alertGroup}/acknowledge`);
  await expect
    .poll(async () => {
      const runs = await admin.call<{ items: { state: string }[] }>(
        "GET",
        `/api/v1/escalation-chains/${runChain}/runs`,
      );
      return runs.items[0]?.state;
    })
    .toBe("stopped");

  await signInAdmin(page);
  await page.goto(`/escalation-chains/${runChain}`);
  const runs = page.getByTestId("chain-runs");
  await expect(runs.getByRole("heading", { name: "How it went" })).toBeVisible();
  const run = runs.getByTestId("chain-run").first();
  await expect(run).toContainText(alertname);
  await expect(run.getByTestId("run-step").first()).toContainText("Step 1");
  await expect(run.getByTestId("run-page-reason")).toHaveText(
    `Page not delivered: @${PETR} has no linked Mattermost account`,
  );
  await expect(run.getByTestId("run-step-skipped")).toHaveText("Step 3 skipped: only for critical");
  await expect(run.getByTestId("run-end")).toHaveText(/^Stopped: acknowledged by .+/);
  await expect(run.getByRole("link", { name: new RegExp(alertname) })).toHaveAttribute(
    "href",
    `/alert-groups/${alertGroup}`,
  );
  await shot(page, "chain-how-it-went");

  await admin.dispose();
  expect(csp).toEqual([]);
});

test("a Responder reads the chain and changes nothing", async ({ page }) => {
  const csp = watchCsp(page);
  await signIn(page, RESPONDER, TEAM_SPEC_PASSWORD);
  await page.goto(`/escalation-chains/${chainId}`);
  await expect(page.getByRole("heading", { name: CHAIN, level: 1 })).toBeVisible();
  await expect(page.getByTestId("chain-read-only")).toContainText(
    "Only Admins change Escalation chains.",
  );
  await expect(page.getByTestId("chain-step")).toHaveCount(7);
  await expect(page.getByTestId("chain-step-text").nth(1)).toHaveText("Wait 10 minutes");
  await expect(page.getByRole("button", { name: "Save", exact: true })).toHaveCount(0);
  await expect(page.getByTestId("chain-add-step")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Delete chain" })).toHaveCount(0);

  // The list is read by everyone; "New chain" is for Admins.
  await page.goto("/escalation-chains");
  await expect(page.getByRole("link", { name: CHAIN, exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "New chain" })).toHaveCount(0);
  expect(csp).toEqual([]);
});

test("the list filters the chains by Team and My Teams", async ({ page }) => {
  const admin = await adminApi();
  await createChain(admin, "ec-search", await teamIdOf(admin, seed.slugs.sea), [
    { kind: "wait", wait_seconds: 60 },
  ]);
  await admin.dispose();
  await signInAdmin(page);
  await page.goto("/escalation-chains");
  await expect(page.getByRole("link", { name: CHAIN, exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "ec-search", exact: true })).toBeVisible();
  await page.getByLabel("Team", { exact: true }).selectOption({ label: seed.slugs.pay });
  await expect(page).toHaveURL(new RegExp(`team=${teamId}`));
  await expect(page.getByRole("link", { name: CHAIN, exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "ec-search", exact: true })).toHaveCount(0);
  await page.getByLabel("Team", { exact: true }).selectOption({ label: "My Teams" });
  await expect(page).toHaveURL(/team=mine/);
  await expect(page.getByRole("link", { name: CHAIN, exact: true })).toHaveCount(0);
});

test("the chain page and How it went read in Russian and fit 360 px", async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, RU_ADMIN, TEAM_SPEC_PASSWORD);

  // English first: nothing scrolls sideways.
  await page.goto(`/escalation-chains/${chainId}`);
  await expect(page.getByRole("heading", { name: CHAIN, level: 1 })).toBeVisible();
  await expect(page.getByTestId("chain-step")).toHaveCount(7);
  await expectNoHorizontalScroll(page);
  await page.goto(`/escalation-chains/new?team=${teamId}`);
  await expect(page.getByTestId("chain-add-step")).toBeVisible();
  await expectNoHorizontalScroll(page);

  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/escalation-chains");
  await expect(page.getByRole("heading", { name: "Цепочки эскалации", level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: /^Руководство: Цепочки эскалации/ })).toBeVisible();
  await expectNoHorizontalScroll(page);

  await page.goto(`/escalation-chains/${chainId}`);
  await expect(page.getByRole("heading", { name: CHAIN, level: 1 })).toBeVisible();
  await expect(page.getByTestId("chain-writes")).toContainText(
    /Пишет только в треды payments-(critical|notice)-ec и payments-(notice|critical)-ec\. Стартует при публикации\./,
  );
  await expect(page.getByTestId("chain-stops")).toContainText(
    "Останавливается при подтверждении, отложении или закрытии группы; запускается заново при снятии подтверждения, конце отложения, переоткрытии и росте до critical, который снимает подтверждение или отложение.",
  );
  await expect(page.getByRole("heading", { name: "Запускать для" })).toBeVisible();
  await expect(page.getByTestId("chain-run-for")).toContainText("важность = critical");
  await expect(page.getByRole("heading", { name: "Шаги" })).toBeVisible();
  await expect(page.getByTestId("chain-step-text")).toHaveText([
    `Позвать дежурного ${seed.slugs.pay} Main · обычно`,
    "Ждать 10 минут",
    `Позвать дежурного ${seed.slugs.pay} Main · важно`,
    "Ждать 15 минут",
    `Позвать дежурного ${seed.slugs.pay} Second line · важно`,
    `Позвать всю команду ${seed.slugs.pay}`,
    "Повторить с шага 3, повторов: 2",
  ]);
  await expect(step(page, 5).getByTestId("chain-step-chips")).toContainText(
    /только в рабочее время команды .+ \(пн–пт \d+(:\d\d)?–\d+(:\d\d)?\)/,
  );
  await expect(
    page.getByTestId("chain-add-step").getByRole("button", {
      name: "Позвонить через шлюз оповещений",
      exact: true,
    }),
  ).toBeEnabled();
  await expect(page.getByTestId("chain-importance")).toContainText("Обычно и важно.");
  await expectNoHorizontalScroll(page);

  // "How it went" of the run, in Russian.
  await page.goto(`/escalation-chains/${await chainIdOf(RUN_CHAIN)}`);
  await expect(page.getByRole("heading", { name: "Как прошла" })).toBeVisible();
  await expect(page.getByTestId("run-page-reason")).toHaveText(
    `Призыв не доставлен: у @${PETR} нет привязки к Mattermost`,
  );
  await expect(page.getByTestId("run-step-skipped")).toHaveText(
    "Шаг 3 пропущен: только для critical",
  );
  await expect(page.getByTestId("run-end")).toHaveText(/^Остановлена: подтвердил\(а\) .+/);
  await expectNoHorizontalScroll(page);
  await shot(page, "chain-ru-360");
  expect(csp).toEqual([]);
  await context.close();
});

/** The id of the chain named name. */
async function chainIdOf(name: string): Promise<string> {
  const admin = await adminApi();
  try {
    const list = await admin.call<{ items: { id: string; name: string }[] }>(
      "GET",
      "/api/v1/escalation-chains?limit=500",
    );
    return list.items.find((c) => c.name === name)?.id ?? "";
  } finally {
    await admin.dispose();
  }
}

test("a chain that a Team names cannot be deleted, and one that nobody names can", async ({
  page,
}) => {
  await signInAdmin(page);
  await page.goto(`/escalation-chains/${chainId}`);
  await page.getByRole("button", { name: "Delete chain" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText(
    "A Route or a Team still names this chain. Remove it there first.",
  );
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();

  const admin = await adminApi();
  await setTeamChains(admin, seed.slugs.pay, []);
  await admin.dispose();
  await page.reload();
  await page.getByRole("button", { name: "Delete chain" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page).toHaveURL(/\/escalation-chains$/);
  await expect(page.getByRole("link", { name: CHAIN, exact: true })).toHaveCount(0);
});
