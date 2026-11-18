// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Escalation chain of the Team" in a real browser, against a stubbed API (C-32.FR-2, C-25.FR-2): the chains the Team
// names with "Run for", the chains it owns with "New chain" for an Admin and the link to the list filtered by the Team,
// "Add Escalation chain" and "Remove" saved as a whole, a refusal at the row it names, and the read-only section of a
// Responder — in English and in Russian.

import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";

import type { Team } from "../api/gen/model";
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
import type { ChainRow } from "./route-chains";
import { TeamChains } from "./team-chains";

let api: ChainApi;

const OWN = chain({ id: "EC0000000PAYM1", name: "payments" });
const SHARED = chain({
  id: "EC0000SHARED01",
  name: "night-shift",
  owning_team: null,
  owning_team_id: null,
});

const NAMING: Team = {
  ...PAYMENTS,
  chain_entries: [{ chain_id: SHARED.id, severity_condition: "critical" }],
  chains: [{ id: SHARED.id, name: SHARED.name, owning_team: null }],
};

beforeEach(async () => {
  api = newChainApi();
  api.chains = [OWN, SHARED];
  api.respond = (call) => {
    if (call.url.startsWith("/api/v1/escalation-chains") && call.url.includes("team=")) {
      return new Response(JSON.stringify({ items: [OWN], next_cursor: null }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return undefined;
  };
  await i18n.changeLanguage("en");
  installChainApi(api);
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

function renderSection(
  props: {
    canWrite?: boolean;
    save?: (entries: ChainRow[]) => Promise<Team>;
    team?: Team;
  } = {},
) {
  const team = props.team ?? NAMING;
  const canWrite = props.canWrite ?? true;
  return renderWithApp(
    <TeamChains
      team={team}
      canWrite={canWrite}
      save={props.save ?? ((entries) => Promise.resolve({ ...team, chain_entries: entries }))}
    />,
    canWrite ? undefined : RESPONDER_PERMISSIONS,
  );
}

describe("the chains of the Team", () => {
  test("shows the chains it names with Run for and the chains it owns", async () => {
    await renderSection();
    await hasText(page.getByTestId("team-chain"), "night-shift");
    await expect
      .element(page.getByTestId(`team-chain-condition-${SHARED.id}`))
      .toHaveValue("critical");
    await hasText(page.getByTestId("team-owned-chains"), "payments · 7 steps");
    await expect
      .element(page.getByTestId("team-new-chain"))
      .toHaveAttribute("href", `/escalation-chains/new?team=${PAYMENTS.id}`);
    await expect
      .element(page.getByTestId("team-all-chains"))
      .toHaveAttribute("href", `/escalation-chains?team=${PAYMENTS.id}`);
  });

  test("adds a chain with any severity and saves the chains as a whole", async () => {
    const save = vi.fn((entries: ChainRow[]) =>
      Promise.resolve({ ...NAMING, chain_entries: entries }),
    );
    await renderSection({ save });
    await vi.waitFor(() =>
      expect(document.querySelectorAll("#team-chains-add option").length).toBe(2),
    );
    await userEvent.selectOptions(page.getByLabelText("Add Escalation chain"), OWN.id);
    await userEvent.selectOptions(
      page.getByTestId(`team-chain-condition-${OWN.id}`),
      "below_critical",
    );
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() =>
      expect(save).toHaveBeenCalledWith([
        { chain_id: SHARED.id, severity_condition: "critical" },
        { chain_id: OWN.id, severity_condition: "below_critical" },
      ]),
    );
    await hasText(page.getByTestId("team-chains-status"), "Saved.");
  });

  test("removes a chain", async () => {
    const save = vi.fn((entries: ChainRow[]) =>
      Promise.resolve({ ...NAMING, chain_entries: entries }),
    );
    await renderSection({ save });
    await userEvent.click(page.getByRole("button", { name: "Remove night-shift" }));
    await hasText(page.getByTestId("team-chains-empty"), "The Team names no Escalation chain.");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(save).toHaveBeenCalledWith([]));
  });

  test("a refusal shows at the row it names", async () => {
    await renderSection({
      save: () =>
        Promise.reject(
          new ApiError(
            422,
            { errors: [{ pointer: "/chain_entries/0/chain_id", code: "unknown_id" }] },
            undefined,
          ),
        ),
    });
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await hasText(page.getByTestId("team-chain"), "This chain no longer exists.");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderSection();
    await hasText(page.getByTestId("team-chains"), "Запускать для");
    await hasText(page.getByTestId("team-owned-chains"), "Цепочки, которыми владеет команда");
    await hasText(page.getByTestId("team-owned-chains"), "7 шагов");
  });
});

describe("a Responder", () => {
  test("reads the chains and the conditions and cannot change them", async () => {
    await renderSection({ canWrite: false });
    await hasText(page.getByTestId(`team-chain-condition-${SHARED.id}`), "Severity = critical");
    await expect.element(page.getByRole("button", { name: "Save" })).not.toBeInTheDocument();
    await expect.element(page.getByLabelText("Add Escalation chain")).not.toBeInTheDocument();
    await expect.element(page.getByTestId("team-new-chain")).not.toBeInTheDocument();
    await expect.element(page.getByTestId("team-all-chains")).toBeVisible();
  });
});
