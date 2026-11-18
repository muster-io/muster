// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Escalation chain page in a real browser, against a stubbed API (C-32.FR-1, FR-2, FR-3, FR-17; C-32.AC-14): where
// the chain writes and when it stops, "Run for" with the conditions of the Routes and Teams that name it, the warnings
// of the chain and of its steps (a Telegram Destination that cannot be mentioned for everyone, a chain that Pages
// nobody), the steps saved as the form shows them — a wait of ten minutes sent as 600 seconds and read back as ten
// minutes —, the refusals at the field and the step they name, a new chain that starts with its Team, and the read-only
// page of a Responder — in English and in Russian.

import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";

import type { EscalationChain, EscalationChainInput } from "../api/gen/model";
import i18n from "../i18n";
import { ApiError } from "../lib/api";
import {
  type ChainApi,
  PAYMENTS,
  RESPONDER_PERMISSIONS,
  chain,
  installChainApi,
  newChainApi,
  renderWithApp,
} from "../test/fixtures/escalation-chains";
import { hasText } from "../test/fixtures/schedules";
import { teamRef } from "../test/fixtures/teams";
import { EscalationChainForm } from "./escalation-chain-form";

let api: ChainApi;

beforeEach(async () => {
  api = newChainApi();
  await i18n.changeLanguage("en");
  installChainApi(api);
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

const CRITICAL = {
  id: "DS0000000PAYCR",
  name: "payments-critical",
  type: "mattermost",
  health: { state: "healthy" },
} as const;
const NOTICE = {
  id: "DS0000000PAYNO",
  name: "payments-notice",
  type: "mattermost",
  health: { state: "healthy" },
} as const;
const TELEGRAM = {
  id: "DS0000000PAYTG",
  name: "payments-tg",
  type: "telegram",
  health: { state: "healthy" },
} as const;

/** The texts of the elements with the test id, in their order. */
const textsOf = (testId: string): (string | null)[] =>
  Array.from(document.querySelectorAll(`[data-testid="${testId}"]`)).map((el) => el.textContent);

const SAVED_INPUT = (calls: unknown[]): EscalationChainInput => {
  const input = calls.at(-1);
  if (typeof input !== "object" || input === null || !("steps" in input)) {
    throw new Error("nothing was saved");
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the form sends an EscalationChainInput
  return input as EscalationChainInput;
};

function renderForm(
  props: {
    chain?: EscalationChain;
    save?: (input: EscalationChainInput) => Promise<EscalationChain | void>;
    readOnly?: boolean;
    initialTeam?: string;
  } = {},
) {
  const chainRead = "chain" in props ? props.chain : chain();
  return renderWithApp(
    <EscalationChainForm
      {...(chainRead === undefined ? {} : { chain: chainRead })}
      {...(props.initialTeam === undefined ? {} : { initialTeam: props.initialTeam })}
      readOnly={props.readOnly ?? false}
      submitLabel="Save"
      save={props.save ?? (() => Promise.resolve(chainRead))}
    />,
    props.readOnly ? RESPONDER_PERMISSIONS : undefined,
  );
}

describe("where the chain writes and when it stops", () => {
  test("names its Destinations, says when it stops and starts over, and Run for", async () => {
    await renderForm({
      chain: chain({
        destinations: [CRITICAL, NOTICE],
        referrers: {
          routes: [
            {
              route: { id: "RT0000000INFRA", name: "Infrastructure" },
              severity_condition: "any",
            },
          ],
          teams: [
            { team: teamRef({ name: "payments" }), severity_condition: "critical" },
            {
              team: teamRef({ id: "TM00000000DEVO", name: "devops", slug: "devops" }),
              severity_condition: "below_critical",
            },
          ],
        },
      }),
    });
    await hasText(
      page.getByTestId("chain-writes"),
      "Writes only in the Threads of payments-critical and payments-notice. Starts at publication.",
    );
    await hasText(
      page.getByTestId("chain-stops"),
      "Stops when the Alert Group is acknowledged, snoozed or resolved; starts over on Unacknowledge, the end of a Snooze, a Reopen, and a rise to critical that ends an acknowledgement or a Snooze.",
    );
    await vi.waitFor(() =>
      expect(textsOf("chain-referrer")).toEqual([
        "Route Infrastructure: any severity",
        "Team payments: Severity = critical",
        "Team devops: Severity < critical",
      ]),
    );
    await expect.element(page.getByRole("heading", { name: "Run for" })).toBeVisible();
  });

  test("says in Russian where it writes and when it stops", async () => {
    await i18n.changeLanguage("ru");
    await renderForm({
      chain: chain({
        destinations: [CRITICAL, NOTICE],
        referrers: {
          routes: [],
          teams: [{ team: teamRef({ name: "payments" }), severity_condition: "critical" }],
        },
      }),
    });
    await hasText(
      page.getByTestId("chain-writes"),
      "Пишет только в треды payments-critical и payments-notice. Стартует при публикации.",
    );
    await hasText(
      page.getByTestId("chain-stops"),
      "Останавливается при подтверждении, отложении или закрытии группы; запускается заново при снятии подтверждения, конце отложения, переоткрытии и росте до critical, который снимает подтверждение или отложение.",
    );
    await expect.element(page.getByRole("heading", { name: "Запускать для" })).toBeVisible();
    await hasText(page.getByTestId("chain-run-for"), "Команда payments: важность = critical");
  });

  test("says that a chain nobody names writes nowhere", async () => {
    await renderForm();
    await hasText(page.getByTestId("chain-writes"), "Writes in no Thread yet");
    await hasText(page.getByTestId("chain-run-for-none"), "runs on nothing");
  });
});

describe("the warnings of the chain", () => {
  test("warn that a Telegram Destination cannot be mentioned for everyone, and that a chain Pages nobody", async () => {
    await renderForm({
      chain: chain({
        destinations: [TELEGRAM],
        warnings: [
          { kind: "pages_nobody", step: null, destination: null, reason: null },
          {
            kind: "everyone_unsupported",
            step: 6,
            destination: { id: TELEGRAM.id, name: TELEGRAM.name },
            reason: "telegram_no_groups",
          },
          { kind: "team_deleted", step: 4, destination: null, reason: null },
        ],
      }),
    });
    await vi.waitFor(() =>
      expect(textsOf("chain-warning")).toEqual([
        "This chain Pages nobody: none of its steps Pages the On-call, people or a Team, or calls.",
        "Step 6 cannot mention the Team or everyone in payments-tg. Telegram cannot mention everyone or groups",
        "Step 4 Pages a Team that was deleted.",
      ]),
    );
  });

  test("shows no block of warnings for a chain without any", async () => {
    await renderForm();
    await expect.element(page.getByTestId("chain-warnings")).not.toBeInTheDocument();
  });
});

describe("saving the chain", () => {
  test("sends the steps as the form shows them and reads them back", async () => {
    const sent: unknown[] = [];
    await renderForm({
      save: (input) => {
        sent.push(input);
        return Promise.resolve(chain({ etag: '"2"' }));
      },
    });
    await vi.waitFor(() =>
      expect(document.querySelectorAll('[data-testid="chain-step"]').length).toBe(7),
    );
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    const input = SAVED_INPUT(sent);
    expect(input.name).toBe("payments");
    expect(input.owning_team_id).toBe(PAYMENTS.id);
    expect(input.steps).toHaveLength(7);
    expect(input.steps[1]).toEqual({ kind: "wait", wait_seconds: 600 });
    expect(input.steps[3]).toEqual({ kind: "wait", wait_seconds: 900 });
    expect(input.steps[6]).toEqual({ kind: "repeat", repeat_from: 3, repeat_times: 2 });
    expect(input.steps[5]).toMatchObject({
      kind: "page_team",
      team_id: PAYMENTS.id,
      working_hours_only: true,
    });
    await hasText(page.getByTestId("chain-save-status"), "Saved.");
    await expect.element(page.getByLabelText("Minutes").first()).toHaveValue(10);
  });

  test("a changed wait is sent in seconds", async () => {
    const sent: unknown[] = [];
    await renderForm({
      save: (input) => {
        sent.push(input);
        return Promise.resolve(chain());
      },
    });
    await vi.waitFor(() =>
      expect(document.querySelectorAll('[data-testid="chain-step"]').length).toBe(7),
    );
    await userEvent.fill(page.getByLabelText("Minutes").first(), "5");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(SAVED_INPUT(sent).steps[1]).toEqual({ kind: "wait", wait_seconds: 300 });
  });

  test("a refusal at a step shows at the step", async () => {
    await renderForm({
      save: () =>
        Promise.reject(
          new ApiError(
            422,
            {
              errors: [
                { pointer: "/steps/0/schedule_id", code: "unknown_id" },
                { pointer: "/steps/1/wait_seconds", code: "out_of_range" },
              ],
            },
            undefined,
          ),
        ),
    });
    await vi.waitFor(() =>
      expect(document.querySelectorAll('[data-testid="chain-step"]').length).toBe(7),
    );
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await hasText(page.getByTestId("chain-steps"), "This choice no longer exists.");
    await hasText(page.getByTestId("chain-steps"), "This value is out of range.");
  });

  test("a name that is taken shows at the name", async () => {
    await renderForm({
      save: () => Promise.reject(new ApiError(409, { code: "name_taken" }, undefined)),
    });
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect
      .element(page.getByText("An Escalation chain with this name already exists."))
      .toBeVisible();
  });

  test("a version changed meanwhile offers to reload", async () => {
    await renderForm({
      save: () => Promise.reject(new ApiError(412, {}, undefined)),
    });
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect.element(page.getByTestId("chain-stale")).toBeVisible();
  });
});

describe("a new chain", () => {
  test("starts with the Team it was opened from and asks for a name", async () => {
    const sent: unknown[] = [];
    await renderForm({
      chain: undefined,
      initialTeam: PAYMENTS.id,
      save: (input) => {
        sent.push(input);
        return Promise.resolve(chain());
      },
    });
    await expect.element(page.getByLabelText("Owning Team")).toHaveValue(PAYMENTS.id);
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect.element(page.getByText("Fill in this field.")).toBeVisible();
    expect(sent).toHaveLength(0);
    await userEvent.fill(page.getByLabelText("Name"), "payments");
    await userEvent.click(
      page.getByTestId("chain-add-step").getByRole("button", { name: "Wait", exact: true }),
    );
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(SAVED_INPUT(sent)).toEqual({
      name: "payments",
      description: "",
      owning_team_id: PAYMENTS.id,
      steps: [{ kind: "wait", wait_seconds: 600 }],
    });
  });
});

describe("a Responder", () => {
  test("reads the chain and its steps and changes nothing", async () => {
    await renderForm({ readOnly: true });
    await expect.element(page.getByTestId("chain-read-only")).toBeVisible();
    await hasText(page.getByTestId("chain-read-only"), "Only Admins change Escalation chains.");
    await vi.waitFor(() =>
      expect(document.querySelectorAll('[data-testid="chain-step"]').length).toBe(7),
    );
    await expect.element(page.getByRole("button", { name: "Save" })).not.toBeInTheDocument();
    await expect.element(page.getByTestId("chain-add-step")).not.toBeInTheDocument();
  });
});
