// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The running Escalation chains of an Alert Group's page in a real browser (C-32.FR-18, C-32.AC-14): each with its name
// linked to its chain page, its current step and when its next step is due, or "ran out" or "stopped", a chain deleted
// since with the name it had and no link, the link "Learn more" to Concepts → Escalation chains, and nothing for an
// Alert Group that had no chain — in English and in Russian.

import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";

import i18n from "../i18n";
import { formatDateTime } from "../lib/time";
import { alertGroupChain } from "../test/fixtures/alert-groups";
import { renderWithApp } from "../test/fixtures/escalation-chains";
import { AlertGroupChains } from "./alert-group-chains";

const ZONE = "Europe/Berlin";
const NEXT = "2026-10-12T10:05:00Z";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

const rows = () => page.getByTestId("alert-group-chain");

describe("AlertGroupChains", () => {
  test("shows each running chain with its name linked to its page, its step and its next step", async () => {
    await renderWithApp(
      <AlertGroupChains
        chains={[
          alertGroupChain({ next_step_at: NEXT, step: { number: 2, kind: "wait" } }),
          alertGroupChain({
            run_id: "ER0000000000BB",
            chain: { id: "EC0000000PAYM1", name: "payments", owning_team: null },
            chain_name: "payments",
            next_step_at: null,
            step: { number: 1, kind: "page_on_call" },
          }),
        ]}
      />,
    );
    await expect.element(rows().first()).toBeVisible();
    await expect.element(rows()).toHaveLength(2);
    const devops = rows().first();
    await expect
      .element(devops.getByRole("link", { name: "devops" }))
      .toHaveAttribute("href", "/escalation-chains/EC0000000DEVO1");
    await expect
      .element(devops.getByTestId("alert-group-chain-state"))
      .toHaveTextContent(`step 2 · next step at ${formatDateTime(NEXT, ZONE, "en")}`);
    await expect
      .element(rows().nth(1).getByRole("link", { name: "payments" }))
      .toHaveAttribute("href", "/escalation-chains/EC0000000PAYM1");
    await expect
      .element(rows().nth(1).getByTestId("alert-group-chain-state"))
      .toHaveTextContent("step 1");
  });

  test("says a chain ran out or was stopped, and names a deleted chain with no link", async () => {
    await renderWithApp(
      <AlertGroupChains
        chains={[
          alertGroupChain({ state: "ran_out", step: null, next_step_at: null }),
          alertGroupChain({
            run_id: "ER0000000000BB",
            chain: null,
            chain_name: "old-chain",
            state: "stopped",
            step: null,
            next_step_at: null,
          }),
        ]}
      />,
    );
    await expect.element(rows().first()).toBeVisible();
    await expect
      .element(rows().first().getByTestId("alert-group-chain-state"))
      .toHaveTextContent("ran out");
    const deleted = rows().nth(1);
    await expect
      .element(deleted.getByTestId("alert-group-chain-state"))
      .toHaveTextContent("stopped");
    expect(deleted.element().textContent).toContain("old-chain");
    await expect.element(deleted.getByRole("link")).not.toBeInTheDocument();
  });

  test("links Learn more to Concepts → Escalation chains, in English and Russian", async () => {
    await renderWithApp(<AlertGroupChains chains={[alertGroupChain()]} />);
    const link = page.getByTestId("docs-link");
    await expect.element(link).toHaveAttribute("data-docs", "field.alert_group_chains");
    await expect
      .element(link)
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/concepts/escalation-chains/#assignees",
      );
    await expect.element(link).toHaveTextContent("Learn more");
    await i18n.changeLanguage("ru");
    await expect.element(link).toHaveTextContent("Подробнее");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderWithApp(
      <AlertGroupChains
        chains={[
          alertGroupChain({ next_step_at: NEXT }),
          alertGroupChain({
            run_id: "ER0000000000BB",
            state: "ran_out",
            step: null,
            next_step_at: null,
          }),
          alertGroupChain({
            run_id: "ER0000000000CC",
            state: "stopped",
            step: null,
            next_step_at: null,
          }),
        ]}
      />,
    );
    await expect.element(rows().first()).toBeVisible();
    await expect.element(page.getByRole("heading", { name: "Цепочки эскалации" })).toBeVisible();
    await expect
      .element(rows().first().getByTestId("alert-group-chain-state"))
      .toHaveTextContent(`шаг 1 · следующий шаг в ${formatDateTime(NEXT, ZONE, "ru")}`);
    await expect
      .element(rows().nth(1).getByTestId("alert-group-chain-state"))
      .toHaveTextContent("закончилась");
    await expect
      .element(rows().nth(2).getByTestId("alert-group-chain-state"))
      .toHaveTextContent("остановлена");
    expect(rows().first().element().textContent).toMatch(/^Цепочка/);
  });

  test("shows nothing for an Alert Group that had no chain", async () => {
    await renderWithApp(<AlertGroupChains chains={[]} />);
    await expect.element(page.getByTestId("alert-group-chains")).not.toBeInTheDocument();
  });
});
