// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Escalation chains" of the Route editor in a real browser, against a stubbed API (C-32.FR-2, FR-17, C-32.AC-17,
// C-08.FR-1): the chains with their Severity conditions and the hint, "Add Escalation chain" and "Remove", the warning of
// a Route with a chain, no Destination and "Also to the Teams from the label" off — gone once a Destination is named or
// the option is on —, the refusal of a chain at its row, and the read-only section — in English and in Russian.

import { useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";

import i18n from "../i18n";
import {
  type ChainApi,
  chain,
  installChainApi,
  newChainApi,
  renderWithApp,
} from "../test/fixtures/escalation-chains";
import { hasText } from "../test/fixtures/schedules";
import { type ChainRow, RouteChains } from "./route-chains";

let api: ChainApi;

const DEVOPS_CHAIN = chain({
  id: "EC00000DEVOPS1",
  name: "devops",
  owning_team: null,
  owning_team_id: null,
});
const PAYMENTS_CHAIN = chain({ id: "EC0000000PAYM1", name: "payments" });

beforeEach(async () => {
  api = newChainApi();
  api.chains = [DEVOPS_CHAIN, PAYMENTS_CHAIN];
  await i18n.changeLanguage("en");
  installChainApi(api);
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

const WARNING =
  "Only a Notification gateway can reach anyone from this Escalation chain: the Route has no Destination.";

function Harness({
  initial = [],
  destinationCount = 0,
  deliverToTeams = false,
  readOnly = false,
  errors,
  onChange,
}: {
  initial?: ChainRow[];
  destinationCount?: number;
  deliverToTeams?: boolean;
  readOnly?: boolean;
  errors?: Record<string, string>;
  onChange?: (next: ChainRow[]) => void;
}) {
  const [rows, setRows] = useState(initial);
  const [destinations, setDestinations] = useState(destinationCount);
  return (
    <>
      <button type="button" onClick={() => setDestinations(1)}>
        Name a Destination
      </button>
      <RouteChains
        id="route-chains"
        value={rows}
        {...(readOnly
          ? {}
          : {
              onChange: (next: ChainRow[]) => {
                setRows(next);
                onChange?.(next);
              },
            })}
        destinationCount={destinations}
        deliverToTeams={deliverToTeams}
        {...(errors === undefined ? {} : { errors })}
        readOnly={readOnly}
      />
    </>
  );
}

const section = () => page.getByTestId("route-chains");

describe("the chains of a Route", () => {
  test("names the chains with their conditions and says where they Page", async () => {
    await renderWithApp(
      <Harness
        initial={[
          { chain_id: DEVOPS_CHAIN.id, severity_condition: "any" },
          { chain_id: PAYMENTS_CHAIN.id, severity_condition: "critical" },
        ]}
        destinationCount={1}
      />,
    );
    await hasText(section(), "Escalation chains");
    await hasText(
      section(),
      "Pages only in the Threads of the Route's Destinations. The Teams from the label Page with their own chains.",
    );
    await expect
      .element(page.getByTestId(`route-chain-condition-${DEVOPS_CHAIN.id}`))
      .toHaveValue("any");
    await expect
      .element(page.getByTestId(`route-chain-condition-${PAYMENTS_CHAIN.id}`))
      .toHaveValue("critical");
    await expect
      .element(
        section().getByRole("link", { name: /^Learn more: Naming chains on Routes and Teams/ }),
      )
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/concepts/escalation-chains/#naming-chains-on-routes-and-teams",
      );
    await expect.element(page.getByTestId("route-chains-warning")).not.toBeInTheDocument();
  });

  test("adds a chain with any severity, changes its condition and removes it", async () => {
    let last: ChainRow[] = [];
    await renderWithApp(<Harness destinationCount={1} onChange={(next) => (last = next)} />);
    await hasText(section(), "The Route names no Escalation chain.");
    await vi.waitFor(() =>
      expect(document.querySelectorAll("#route-chains-add option").length).toBe(3),
    );
    await userEvent.selectOptions(page.getByLabelText("Add Escalation chain"), DEVOPS_CHAIN.id);
    await vi.waitFor(() =>
      expect(last).toEqual([{ chain_id: DEVOPS_CHAIN.id, severity_condition: "any" }]),
    );
    await userEvent.selectOptions(
      page.getByTestId(`route-chain-condition-${DEVOPS_CHAIN.id}`),
      "below_critical",
    );
    await vi.waitFor(() =>
      expect(last).toEqual([{ chain_id: DEVOPS_CHAIN.id, severity_condition: "below_critical" }]),
    );
    await userEvent.click(page.getByRole("button", { name: "Remove the chain devops" }));
    await vi.waitFor(() => expect(last).toEqual([]));
  });
});

describe("the warning of a Route without a Destination", () => {
  test("shows with a chain, no Destination and the Teams from the label off", async () => {
    await renderWithApp(
      <Harness initial={[{ chain_id: DEVOPS_CHAIN.id, severity_condition: "any" }]} />,
    );
    await hasText(page.getByTestId("route-chains-warning"), WARNING);
  });

  test("goes once a Destination is named", async () => {
    await renderWithApp(
      <Harness
        initial={[{ chain_id: DEVOPS_CHAIN.id, severity_condition: "any" }]}
        destinationCount={0}
      />,
    );
    await hasText(page.getByTestId("route-chains-warning"), WARNING);
    await userEvent.click(page.getByRole("button", { name: "Name a Destination" }));
    await expect.element(page.getByTestId("route-chains-warning")).not.toBeInTheDocument();
  });

  test("does not show while the Route also goes to the Teams from the label, or names no chain", async () => {
    await renderWithApp(
      <Harness
        initial={[{ chain_id: DEVOPS_CHAIN.id, severity_condition: "any" }]}
        deliverToTeams
      />,
    );
    await expect.element(page.getByTestId("route-chain")).toBeVisible();
    await expect.element(page.getByTestId("route-chains-warning")).not.toBeInTheDocument();
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderWithApp(
      <Harness initial={[{ chain_id: DEVOPS_CHAIN.id, severity_condition: "any" }]} />,
    );
    await hasText(
      page.getByTestId("route-chains-warning"),
      "Только шлюз оповещений может до кого-то дозвониться из этой цепочки: у маршрута нет места доставки.",
    );
    await hasText(
      section(),
      "Зовёт только в тредах мест доставки маршрута. Команды из метки зовут своими цепочками.",
    );
    await hasText(section(), "Цепочки эскалации");
  });
});

describe("refusals and reading", () => {
  test("a refusal of a chain shows at its row", async () => {
    await renderWithApp(
      <Harness
        initial={[{ chain_id: DEVOPS_CHAIN.id, severity_condition: "any" }]}
        destinationCount={1}
        errors={{ [DEVOPS_CHAIN.id]: "unknown_id" }}
      />,
    );
    await hasText(page.getByTestId("route-chain"), "This chain no longer exists.");
  });

  test("the read-only section names the chains with their conditions and has no picker", async () => {
    await renderWithApp(
      <Harness
        readOnly
        initial={[{ chain_id: PAYMENTS_CHAIN.id, severity_condition: "critical" }]}
        destinationCount={1}
      />,
    );
    await hasText(
      page.getByTestId(`route-chain-condition-${PAYMENTS_CHAIN.id}`),
      "Severity = critical",
    );
    await expect.element(page.getByLabelText("Add Escalation chain")).not.toBeInTheDocument();
  });
});
