// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The steps of an Escalation chain in a real browser, against a stubbed API (C-32.FR-3, C-30.FR-9, C-32.AC-14): the
// numbered steps in words with their limits as chips, "Add step" with the seven kinds and "Call through the
// Notification gateway" greyed with the note while no gateway exists, a wait in minutes, "Whom 'Page the whole Team'
// calls" with the choices a Telegram Destination cannot mention greyed with their reason, the refusals of the server
// at the step they name, and the read-only steps of a Responder — in English and in Russian.

import { useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";

import type { EscalationStep } from "../api/gen/model";
import i18n from "../i18n";
import { bridgeConnection, telegramConnection } from "../test/fixtures/connections";
import {
  type ChainApi,
  PAYMENTS,
  RESPONDER_PERMISSIONS,
  installChainApi,
  newChainApi,
  paymentsStep,
  paymentsSteps,
  renderWithApp,
} from "../test/fixtures/escalation-chains";
import { telegramDestination } from "../test/fixtures/destinations";
import { hasText } from "../test/fixtures/schedules";
import { type StepDraft, draftOf, stepInput } from "../lib/escalation-steps";
import { EscalationStepsEditor, type StepIssue } from "./escalation-steps-editor";

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

const GATEWAY = bridgeConnection({ id: "CN00000000GW01", name: "Example Gateway" });

function Harness({
  stored = paymentsSteps(),
  issues = [],
  readOnly = false,
  onSteps,
}: {
  stored?: EscalationStep[];
  issues?: StepIssue[];
  readOnly?: boolean;
  onSteps?: (steps: StepDraft[]) => void;
}) {
  const [steps, setSteps] = useState(() => stored.map(draftOf));
  return (
    <EscalationStepsEditor
      steps={steps}
      {...(readOnly
        ? {}
        : {
            onChange: (next: StepDraft[]) => {
              setSteps(next);
              onSteps?.(next);
            },
          })}
      stored={stored}
      destinations={api.destinations.map((d) => ({
        id: d.id,
        name: d.name,
        type: d.type,
        health: d.health,
      }))}
      owningTeam={{ id: PAYMENTS.id, name: PAYMENTS.name }}
      issues={issues}
    />
  );
}

const steps = () => page.getByTestId("chain-steps");
const targetOptions = (): HTMLOptionElement[] =>
  Array.from(document.querySelector<HTMLSelectElement>("select[id$='-target']")?.options ?? []);
const stepTexts = () =>
  Array.from(document.querySelectorAll('[data-testid="chain-step-text"]')).map(
    (el) => el.textContent,
  );

describe("the steps in words", () => {
  test("number the seven steps of scenario 1 with their limits as chips", async () => {
    await renderWithApp(<Harness />);
    await expect.element(page.getByRole("heading", { name: "Steps" })).toBeVisible();
    await vi.waitFor(() =>
      expect(stepTexts()).toEqual([
        "Page the On-call of payments Main · normal",
        "Wait 10 minutes",
        "Page the On-call of payments Main · important",
        "Wait 15 minutes",
        "Page the On-call of payments Second line · important",
        "Page the whole Team payments",
        "Repeat from step 3, up to 2 times",
      ]),
    );
    const numbers = Array.from(document.querySelectorAll('[data-testid="chain-step-number"]')).map(
      (el) => el.textContent,
    );
    expect(numbers).toEqual(Array.from({ length: 7 }, (_, i) => `Step ${i + 1}`));
    await vi.waitFor(() => {
      const found = Array.from(
        document.querySelectorAll('[data-kind="page_team"] [data-testid="chain-step-chips"] li'),
      ).map((el) => el.textContent);
      expect(found).toEqual([
        "Severity: any",
        "only in the working hours of payments (Mon–Fri 11–19)",
      ]);
    });
    const critical = Array.from(
      document.querySelectorAll('[data-kind="page_on_call"] [data-testid="chain-step-chips"]'),
    ).map((el) => el.textContent);
    expect(critical[0]).toBe("Severity: anyalways");
    expect(critical[1]).toBe("Severity = criticalalways");
  });

  test("say the hint about limits and what normal and important mean", async () => {
    await renderWithApp(<Harness />);
    await hasText(
      page.getByTestId("chain-steps-hint"),
      "Any step may be limited to working hours and to a Severity condition. Steps through a Notification gateway do not depend on a messenger, but need the gateway.",
    );
    await hasText(
      page.getByTestId("chain-importance"),
      "Normal and important. What exactly happens is decided by each person's Notification rules. Muster sends no direct messages: a Page is a Mention in the Thread of the Root message.",
    );
  });

  test("read in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderWithApp(<Harness />);
    await expect.element(page.getByRole("heading", { name: "Шаги" })).toBeVisible();
    await vi.waitFor(() =>
      expect(stepTexts()).toEqual([
        "Позвать дежурного payments Main · обычно",
        "Ждать 10 минут",
        "Позвать дежурного payments Main · важно",
        "Ждать 15 минут",
        "Позвать дежурного payments Second line · важно",
        "Позвать всю команду payments",
        "Повторить с шага 3, повторов: 2",
      ]),
    );
    await hasText(
      page.getByTestId("chain-steps-hint"),
      "Любой шаг можно ограничить рабочим временем и условием по важности. Шаги через шлюз оповещений не зависят от мессенджера, но нужен сам шлюз.",
    );
    await hasText(page.getByTestId("chain-importance"), "Обычно и важно.");
  });
});

describe("Add step", () => {
  test("offers the seven kinds, the call greyed with the note while there is no gateway", async () => {
    await renderWithApp(<Harness stored={[]} />);
    const add = page.getByTestId("chain-add-step");
    for (const name of [
      "Page the On-call of a Schedule",
      "Page people",
      "Page the whole Team",
      "Wait",
      "Send to an outgoing webhook",
      "Repeat from step",
    ]) {
      await expect.element(add.getByRole("button", { name, exact: true })).toBeEnabled();
    }
    const call = add.getByRole("button", { name: "Call through the Notification gateway" });
    await expect.element(call).toHaveAttribute("aria-disabled", "true");
    await expect
      .element(call)
      .toHaveAttribute("aria-describedby", expect.stringContaining("gateway-note"));
    await hasText(
      page.getByTestId("gateway-unavailable"),
      "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)",
    );
  });

  test("offers the call once a gateway exists", async () => {
    api.gateways = [GATEWAY];
    await renderWithApp(<Harness stored={[]} />);
    const call = page
      .getByTestId("chain-add-step")
      .getByRole("button", { name: "Call through the Notification gateway" });
    await expect.element(call).toBeEnabled();
    await expect.element(page.getByTestId("gateway-unavailable")).not.toBeInTheDocument();
  });

  test("shows the note in Russian without a gateway", async () => {
    await i18n.changeLanguage("ru");
    await renderWithApp(<Harness stored={[]} />);
    await expect
      .element(
        page.getByTestId("chain-add-step").getByRole("button", {
          name: "Позвонить через шлюз оповещений",
        }),
      )
      .toHaveAttribute("aria-disabled", "true");
    await hasText(
      page.getByTestId("gateway-unavailable"),
      "Недоступно: нужен шлюз оповещений (мост для звонков и SMS)",
    );
  });

  test("the greyed call adds no step", async () => {
    await renderWithApp(<Harness stored={[]} />);
    await expect
      .element(
        page
          .getByTestId("chain-add-step")
          .getByRole("button", { name: "Call through the Notification gateway" }),
      )
      .toHaveAttribute("aria-disabled", "true");
    // A click that reaches it, as a keyboard press on the focused button does, adds nothing.
    document.querySelector<HTMLButtonElement>('[data-kind="call"]')?.click();
    expect(stepTexts()).toEqual([]);
  });

  test("a step of a kind this version does not know shows its kind and is sent unchanged", async () => {
    let sent: StepDraft[] = [];
    // A kind a later version adds, read as JSON, which the type does not know.
    const unknown: EscalationStep = JSON.parse('{"kind":"sms","number":1,"phone":"+1"}');
    await renderWithApp(<Harness stored={[unknown]} onSteps={(next) => (sent = next)} />);
    await vi.waitFor(() => expect(stepTexts()).toEqual(["sms"]));
    await expect.element(page.getByTestId("chain-step-chips")).not.toBeInTheDocument();
    expect(sent).toEqual([]);
    const draft = draftOf(unknown);
    expect(stepInput(draft)).toEqual({ kind: "sms", phone: "+1" });
  });

  test("adds a wait of ten minutes that is read back in minutes", async () => {
    let sent: StepDraft[] = [];
    await renderWithApp(<Harness stored={[]} onSteps={(next) => (sent = next)} />);
    await userEvent.click(
      page.getByTestId("chain-add-step").getByRole("button", { name: "Wait", exact: true }),
    );
    await expect.element(page.getByLabelText("Minutes")).toHaveValue(10);
    await userEvent.fill(page.getByLabelText("Minutes"), "15");
    await vi.waitFor(() => expect(sent.at(-1)?.minutes).toBe("15"));
    await vi.waitFor(() => expect(stepTexts()).toEqual(["Wait 15 minutes"]));
  });

  test("moves and removes steps", async () => {
    await renderWithApp(<Harness />);
    await vi.waitFor(() => expect(stepTexts()).toHaveLength(7));
    await userEvent.click(page.getByRole("button", { name: "Move step 2 up" }));
    await vi.waitFor(() => expect(stepTexts()[0]).toBe("Wait 10 minutes"));
    await userEvent.click(page.getByRole("button", { name: "Remove step 1" }));
    await vi.waitFor(() => expect(stepTexts()).toHaveLength(6));
  });
});

describe("Whom 'Page the whole Team' calls", () => {
  test("greys what the Telegram Destination of the chain cannot mention, with the reason", async () => {
    const connection = telegramConnection({ id: "CN000000000TG1", name: "tg" });
    api.connections = [connection];
    api.destinations = [
      telegramDestination({
        id: "DS0000000000TG",
        name: "payments-tg",
        connection_id: connection.id,
      }),
    ];
    await renderWithApp(<Harness stored={[paymentsStep(6)]} />);
    await expect.element(page.getByLabelText("Whom 'Page the whole Team' calls")).toBeVisible();
    await vi.waitFor(() => {
      // The choice the step has now stays; the others are greyed with the reason.
      expect(targetOptions().map((o) => o.disabled)).toEqual([false, true, true, true, true]);
    });
    expect(targetOptions()[1]?.text).toBe(
      "@channel — unavailable: Telegram cannot mention everyone or groups",
    );
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderWithApp(<Harness stored={[paymentsStep(6)]} />);
    await expect.element(page.getByLabelText("Кого зовёт „Позвать всю команду“")).toBeVisible();
    await vi.waitFor(() => expect(targetOptions()[0]?.text).toBe("Упоминание команды"));
  });

  test("offers every choice while no Destination is known", async () => {
    await renderWithApp(<Harness stored={[paymentsStep(6)]} />);
    await vi.waitFor(() => {
      const options = targetOptions();
      expect(options.map((o) => o.text)).toEqual([
        "The Team mention",
        "@channel",
        "@here",
        "@all",
        "@everyone",
      ]);
      expect(options.every((o) => !o.disabled)).toBe(true);
    });
  });
});

describe("refusals and reading", () => {
  test("a refusal of the server shows at the step it names", async () => {
    await renderWithApp(
      <Harness
        stored={[paymentsStep(1)]}
        issues={[{ index: 0, field: "schedule_id", code: "unknown_id" }]}
      />,
    );
    await hasText(steps(), "This choice no longer exists.");
  });

  test("a Responder reads the steps and cannot add one", async () => {
    await renderWithApp(<Harness readOnly />, RESPONDER_PERMISSIONS);
    await vi.waitFor(() => expect(stepTexts()).toHaveLength(7));
    await expect.element(page.getByTestId("chain-add-step")).not.toBeInTheDocument();
    await expect
      .element(page.getByRole("button", { name: "Remove step 1" }))
      .not.toBeInTheDocument();
  });
});
