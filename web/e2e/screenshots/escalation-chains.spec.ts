// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Escalation chains" (docs/concepts/escalation-chains.md, docs/concepts/how-an-alert-reaches-people.md),
// all in docs/assets/screenshots/concepts/escalation-chains/. Through the API the spec sets up scenario 2 of C-32 as the
// end-to-end suite does (seedAssignees): the Route "Infrastructure" naming the chain "devops", the Team "payments" naming
// the chain "payments", the Mattermost Destinations of routing to Teams and a firing warning Alert Group of payments,
// whose chains Page ivan (devops) and petr (payments), both with a linked Mattermost account, and olga, who has none.
// The chain "payments" is then saved with the seven steps of scenario 1 on the Schedules "Main" and "Second line" of the
// Team; the run that started earlier keeps the steps it started with. The Admin reads the Alert Group page — its
// Assignees and its running chains — and the chain page with its steps, what it does and who runs it; it then
// acknowledges the Alert Group, and the chain page shows "How it went" with the run that the acknowledgement stopped. The
// development clock does not move, so this is no clock spec. The spec removes what it created. The names carry the key
// of the run, which the images show without it.

import { expect, test } from "@playwright/test";

import {
  adminApi,
  Api,
  alertGroupOf,
  createSchedule,
  dropAssignees,
  ensureLocalUser,
  removeAccountLinks,
  removeSchedulesOf,
  seedAssignees,
  signInAdmin,
  TEAM_SPEC_PASSWORD,
  teamIdOf,
  waitAssignees,
  type AssigneeSeed,
} from "../support";
import { capture } from "./capture";

const CHAINS = "concepts/escalation-chains";
const KEY = "escdocs";
const BASES = [
  "infra-critical",
  "infra-notice",
  "payments-critical",
  "payments-notice",
  "search-notice",
];
/**
 * What the images show instead of the names that carry the key of the run, the longest first; beforeAll puts the name
 * of the Alert and of the Integration in front once the seed has made them.
 */
const SHOWN: [string, string][] = [
  ...BASES.map((base): [string, string] => [`${base}-${KEY}`, base]),
  [`Infrastructure-${KEY}`, "Infrastructure"],
  [`${KEY}-payments`, "payments"],
  [`${KEY}-devops`, "devops"],
  [`${KEY}-ivan`, "ivan"],
  [`${KEY}-petr`, "petr"],
  [`${KEY}-olga`, "olga"],
  [`payments-${KEY}`, "payments"],
  [`devops-${KEY}`, "devops"],
  [`search-${KEY}`, "search"],
];

test.describe.configure({ mode: "serial" });

let assign: AssigneeSeed;
let paymentsChain = "";

interface ChainRead {
  id: string;
  name: string;
  etag: string;
}

/** The chain of the run that is named name, as the list reads it. */
async function chainNamed(admin: Api, name: string): Promise<ChainRead | undefined> {
  const list = await admin.call<{ items: ChainRead[] }>(
    "GET",
    "/api/v1/escalation-chains?limit=500",
  );
  return list.items.find((c) => c.name === name);
}

async function saveSteps(admin: Api, chain: ChainRead, steps: unknown[]): Promise<void> {
  await admin.call(
    "PUT",
    `/api/v1/escalation-chains/${chain.id}`,
    { name: chain.name, steps },
    { "If-Match": chain.etag },
  );
}

/** Takes the Schedules of the Team off the chain and deletes them, so that the Team can be deleted. */
async function clearSchedules(): Promise<void> {
  const admin = await adminApi();
  try {
    const chain = await chainNamed(admin, `${KEY}-payments`);
    if (chain !== undefined) {
      await saveSteps(admin, chain, [{ kind: "wait", wait_seconds: 60 }]);
    }
    await removeSchedulesOf(admin, [`payments-${KEY}`]);
  } finally {
    await admin.dispose();
  }
}

test.beforeAll(async () => {
  test.setTimeout(240_000);
  await clearSchedules();
  assign = await seedAssignees(KEY);
  await waitAssignees(assign.groupId, 2);
  // The Alert's name carries a random suffix and the Integration the key of the run; the images show neither.
  SHOWN.unshift(
    [assign.alertname, "KubernetesContainerOOMKilled"],
    [`${KEY}-routing`, "alertmanager"],
  );
  const admin = await adminApi();
  try {
    const petr = await ensureLocalUser(admin, assign.logins.petr, "responder");
    const team = await teamIdOf(admin, assign.teams.payments);
    const main = await createSchedule(admin, team, "Main", petr);
    const second = await createSchedule(admin, team, "Second line", petr);
    const chain = await chainNamed(admin, assign.chains.payments);
    expect(chain, `the chain ${assign.chains.payments}`).toBeDefined();
    if (chain === undefined) {
      return;
    }
    paymentsChain = chain.id;
    // Scenario 1 of C-32: the seven steps of payments.
    await saveSteps(admin, chain, [
      { kind: "page_on_call", schedule_id: main, importance: "normal" },
      { kind: "wait", wait_seconds: 600 },
      {
        kind: "page_on_call",
        schedule_id: main,
        importance: "important",
        severity_condition: "critical",
      },
      { kind: "wait", wait_seconds: 900 },
      {
        kind: "page_on_call",
        schedule_id: second,
        importance: "important",
        severity_condition: "critical",
      },
      { kind: "page_team", working_hours_only: true },
      { kind: "repeat", repeat_from: 3, repeat_times: 2 },
    ]);
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  await clearSchedules();
  await dropAssignees(KEY);
});

test("shows the Alert Group with its Assignees and chains, the chain page and How it went", async ({
  page,
}) => {
  test.setTimeout(240_000);
  await signInAdmin(page);

  // The Alert Group page: its Assignees with their Teams, and the chains that run with their next step.
  await page.goto(`/alert-groups/${assign.groupId}`);
  const header = page.getByTestId("alert-group-header");
  await expect(page.getByTestId("alert-group-assignees")).toContainText(
    `@${assign.logins.ivan} (${assign.teams.devops})`,
  );
  await expect(page.getByTestId("alert-group-assignees")).toContainText(
    `@${assign.logins.petr} (${assign.teams.payments})`,
  );
  await capture(page, CHAINS, "04-alert-group-assignees", {
    locator: header,
    padding: 16,
    replace: SHOWN,
  });
  const chains = page.getByTestId("alert-group-chains");
  await expect(chains.getByTestId("alert-group-chain")).toHaveCount(2);
  await expect(chains.getByTestId("alert-group-chain-state").first()).toHaveText(/^step \d+ · /);
  await capture(page, CHAINS, "05-alert-group-chains", {
    locator: chains,
    padding: 16,
    replace: SHOWN,
  });

  // The chain page of payments: its seven steps, what it does and for whom it runs.
  await page.goto(`/escalation-chains/${paymentsChain}`);
  await expect(page.getByTestId("chain-step")).toHaveCount(7);
  await expect(page.getByTestId("chain-step-text").nth(1)).toHaveText("Wait 10 minutes");
  // The seven steps are taller than the viewport, and the image takes them whole.
  await page.setViewportSize({ width: 1280, height: 2000 });
  await capture(page, CHAINS, "01-chain-steps", {
    locator: page.getByTestId("chain-steps"),
    padding: 16,
    replace: SHOWN,
  });
  const behaviour = page.getByTestId("chain-behaviour");
  await expect(page.getByTestId("chain-writes")).toContainText("Writes only in the Threads of");
  await expect(page.getByTestId("chain-run-for")).toContainText(`Team ${assign.teams.payments}`);
  await capture(page, CHAINS, "02-chain-behaviour", {
    locator: behaviour,
    padding: 16,
    replace: SHOWN,
  });

  // petr loses his Mattermost Account link, so that the Page of a second warning Alert of payments, CheckoutQueueBacklog,
  // is not delivered: "How it went" then shows the line "Page not delivered: @petr has no linked Mattermost account",
  // as the first Alert Group, where petr is linked, does not.
  const petrApi = await Api.signIn(assign.logins.petr, TEAM_SPEC_PASSWORD);
  try {
    await removeAccountLinks(petrApi, "mattermost");
  } finally {
    await petrApi.dispose();
  }
  const second = assign.seed.alertName("CheckoutQueueBacklog");
  SHOWN.unshift([second, "CheckoutQueueBacklog"]);
  await assign.seed.fire([{ alertname: second, team: assign.teams.payments, severity: "warning" }]);
  const secondGroup = await alertGroupOf(assign.seed.integrationId, second);

  // An acknowledgement stops both chains; "How it went" then lists the run of the chain with what stopped it.
  const admin = await adminApi();
  try {
    await expect
      .poll(
        async () => {
          const timeline = await admin.call<{ items: { event: string }[] }>(
            "GET",
            `/api/v1/alert-groups/${secondGroup}/timeline?kind=escalation&limit=200`,
          );
          return timeline.items.some((e) => e.event === "page_not_delivered");
        },
        { timeout: 60_000 },
      )
      .toBe(true);
    await admin.call("POST", `/api/v1/alert-groups/${assign.groupId}/acknowledge`);
    await admin.call("POST", `/api/v1/alert-groups/${secondGroup}/acknowledge`);
    await expect
      .poll(async () => {
        const runs = await admin.call<{ items: { state: string }[] }>(
          "GET",
          `/api/v1/escalation-chains/${paymentsChain}/runs`,
        );
        return runs.items[0]?.state;
      })
      .toBe("stopped");
  } finally {
    await admin.dispose();
  }
  await page.reload();
  const runs = page.getByTestId("chain-runs");
  await expect(runs.getByRole("heading", { name: "How it went" })).toBeVisible();
  await expect(runs.getByTestId("chain-run").first().getByTestId("run-end")).toHaveText(
    /^Stopped: acknowledged by .+/,
  );
  await expect(runs.getByTestId("run-page-reason")).toContainText("Page not delivered");
  await capture(page, CHAINS, "03-how-it-went", {
    locator: runs,
    padding: 16,
    replace: SHOWN,
  });
});
