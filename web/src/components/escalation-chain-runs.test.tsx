// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "How it went" in a real browser, against a stubbed API (C-32.FR-18, C-32.AC-17): the recent runs of a chain with their
// Alert Groups, the Pages — by Mention, and calls through the gateway with its name —, the Pages that were not
// delivered with their reason, the steps that were skipped with theirs, and the acknowledgement that stopped the
// chain, or that it ran out — in English and in Russian, and the empty and failed reads.

import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";

import type { EscalationRun } from "../api/gen/model";
import i18n from "../i18n";
import {
  type ChainApi,
  installChainApi,
  newChainApi,
  paymentsSteps,
  renderWithApp,
  run,
  runPage,
  runStep,
} from "../test/fixtures/escalation-chains";
import { PETR, SERGEY, hasText, reply } from "../test/fixtures/schedules";
import { EscalationChainRuns } from "./escalation-chain-runs";

let api: ChainApi;

const MATTERMOST = {
  id: "DS0000000PAYNO",
  name: "payments-notice",
  type: "mattermost",
  health: { state: "healthy" },
} as const;

beforeEach(async () => {
  api = newChainApi();
  await i18n.changeLanguage("en");
  installChainApi(api);
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

function renderRuns() {
  return renderWithApp(
    <EscalationChainRuns
      chainId="EC0000000PAYM1"
      steps={paymentsSteps()}
      destinations={[MATTERMOST]}
    />,
  );
}

/** The texts of the skipped steps shown, in their order. */
const skippedTexts = (): (string | null)[] =>
  Array.from(document.querySelectorAll('[data-testid="run-step-skipped"]')).map(
    (el) => el.textContent,
  );

/** The run of scenario 2 and 3 of C-32: step 1 pages petr who has no account, step 3 is skipped, ivan acknowledged. */
function acknowledgedRun(): EscalationRun {
  return run({
    state: "stopped",
    ended_at: "2026-11-17T03:24:00Z",
    stopped_by: {
      command: "acknowledge",
      actor: { kind: "user", id: "SR0000000IVAN", name: "ivan", login: "ivan", deactivated: false },
      destination: null,
    },
    steps: [
      runStep(1, {
        pages: [
          runPage({ users: [PETR], outcome: "not_delivered", reason: "no_account_link" }),
          runPage({ users: [SERGEY], outcome: "delivered" }),
        ],
      }),
      runStep(2, { kind: "wait" }),
      runStep(3, { outcome: "skipped", reason: "severity" }),
    ],
  });
}

describe("the runs", () => {
  test("list the Alert Group, the Pages, the undelivered Page, the skipped step and the acknowledgement", async () => {
    api.runs = [acknowledgedRun()];
    await renderRuns();
    const runs = page.getByTestId("chain-runs");
    await expect.element(runs.getByRole("heading", { name: "How it went" })).toBeVisible();
    await hasText(runs, "#12 KubernetesContainerOOMKilled");
    await hasText(runs, "Step 1");
    await hasText(
      page.getByTestId("run-page-reason"),
      "Page not delivered: @petr has no linked Mattermost account",
    );
    await hasText(runs, "Mention: @sergey · delivered");
    await hasText(page.getByTestId("run-step-skipped"), "Step 3 skipped: only for critical");
    await hasText(page.getByTestId("run-end"), "Stopped: acknowledged by @ivan");
    await expect
      .element(runs.getByRole("link", { name: "#12 KubernetesContainerOOMKilled" }))
      .toHaveAttribute("href", "/alert-groups/AG0000000000A1");
  });

  test("name the gateway of a call and say why it was not delivered", async () => {
    api.runs = [
      run({
        steps: [
          runStep(1, {
            kind: "call",
            pages: [
              runPage({
                kind: "call",
                gateway: { id: "CN00000000GW01", name: "Example Gateway" },
                users: [PETR],
                outcome: "not_delivered",
                reason: "no_confirmed_contact",
              }),
              runPage({
                kind: "call",
                gateway: { id: "CN00000000GW01", name: "Example Gateway" },
                users: [SERGEY],
                outcome: "pending",
              }),
            ],
          }),
        ],
      }),
    ];
    await renderRuns();
    await hasText(
      page.getByTestId("chain-runs"),
      "Call through Example Gateway: @petr · not delivered",
    );
    await hasText(
      page.getByTestId("run-page-reason"),
      "Call not delivered: @petr has no confirmed contact at the gateway",
    );
    await hasText(
      page.getByTestId("chain-runs"),
      "Call through Example Gateway: @sergey · pending",
    );
    await hasText(page.getByTestId("run-end"), "Still running");
  });

  test("give one reason for all the people of a Page, and the Team and the word of a Page of a Team", async () => {
    api.runs = [
      run({
        state: "ran_out",
        ended_at: "2026-11-17T04:00:00Z",
        steps: [
          runStep(1, {
            kind: "page_people",
            pages: [
              runPage({
                users: [PETR, SERGEY],
                outcome: "not_delivered",
                reason: "no_account_link",
              }),
            ],
          }),
          runStep(6, {
            kind: "page_team",
            pages: [
              runPage({
                users: [],
                team: { id: "TM0000000000PA", name: "payments", slug: "payments", deleted: false },
              }),
              runPage({ users: [], everyone: "here" }),
            ],
          }),
        ],
      }),
    ];
    await renderRuns();
    await hasText(
      page.getByTestId("run-page-reason"),
      "Page not delivered: @petr, @sergey have no linked Mattermost account",
    );
    await hasText(page.getByTestId("chain-runs"), "Team mention: payments · delivered");
    await hasText(page.getByTestId("chain-runs"), "Mention of everyone: @here · delivered");
    await hasText(page.getByTestId("run-end"), "Ran out: nobody acknowledged");
  });

  test("read the reason of a skipped step from the condition the chain has", async () => {
    api.runs = [
      run({
        steps: [
          runStep(3, { outcome: "skipped", reason: "outside_working_hours" }),
          runStep(5, { outcome: "skipped", reason: "schedule_gap" }),
          runStep(2, { outcome: "skipped", reason: "severity", kind: "wait" }),
        ],
      }),
    ];
    await renderRuns();
    await vi.waitFor(() =>
      expect(skippedTexts()).toEqual([
        "Step 3 skipped: outside the working hours",
        "Step 5 skipped: the Schedule has a gap",
        "Step 2 skipped: its Severity condition was not met",
      ]),
    );
  });

  test("read in Russian", async () => {
    await i18n.changeLanguage("ru");
    api.runs = [acknowledgedRun()];
    await renderRuns();
    await expect.element(page.getByRole("heading", { name: "Как прошла" })).toBeVisible();
    await hasText(
      page.getByTestId("run-page-reason"),
      "Призыв не доставлен: у @petr нет привязки к Mattermost",
    );
    await hasText(page.getByTestId("run-step-skipped"), "Шаг 3 пропущен: только для critical");
    await hasText(page.getByTestId("run-end"), "Остановлена: подтвердил(а) @ivan");
  });

  test("show what a later version adds as it is, and never an empty reason", async () => {
    api.runs = [
      run({
        state: "stopped",
        stopped_by: {
          // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a command a later version adds
          command: "pause" as unknown as "snooze",
          actor: null,
          destination: null,
        },
        steps: [
          runStep(1, {
            pages: [
              // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a kind a later version adds
              runPage({ kind: "sms" as unknown as "call" }),
            ],
          }),
          runStep(2, { outcome: "skipped", reason: null }),
        ],
      }),
    ];
    await renderRuns();
    await hasText(page.getByTestId("run-page"), "sms");
    await hasText(page.getByTestId("run-step-skipped"), "Step 2 skipped");
    expect(document.querySelector('[data-testid="run-step-skipped"]')?.textContent).toBe(
      "Step 2 skipped",
    );
    await hasText(page.getByTestId("run-end"), "pause");
    await hasText(
      page.getByTestId("chain-runs"),
      "Conditions of skipped steps are those the chain has now.",
    );
  });

  test("say that no Alert Group has run", async () => {
    await renderRuns();
    await hasText(
      page.getByTestId("chain-runs-empty"),
      "No Alert Group has run through this chain yet.",
    );
  });

  test("a failed read shows the problem", async () => {
    api.respond = (call) =>
      call.url.includes("/runs")
        ? reply(500, { type: "about:blank", title: "x", status: 500 })
        : undefined;
    await renderRuns();
    await expect.element(page.getByRole("alert")).toBeVisible();
  });
});
