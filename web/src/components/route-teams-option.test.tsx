// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Also to the Teams from the label" of the Route editor in a real browser (C-26.FR-2, FR-12, AC-9): the option with its
// hint and "Learn more", "Run the Teams' Escalation chains" under it, which waits while the option is off; with the
// option on and no Team label in the Group key the hint that Alerts of different Teams share an Alert Group with
// "Add team to the key", which adds the label to the key and ends the hint; the read-only texts and the Russian ones.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { GroupKeyEditor } from "./group-key-editor";
import { RouteTeamsOption, SharedKeyHint } from "./route-teams-option";

function Harness({
  label = "team",
  deliver = false,
  chains = true,
  groupKey: initialKey = ["alertname"],
  readOnly = false,
  errors,
}: {
  label?: string;
  deliver?: boolean;
  chains?: boolean;
  groupKey?: string[];
  readOnly?: boolean;
  errors?: { deliverToTeams?: string; runTeamChains?: string };
}) {
  const [on, setOn] = useState(deliver);
  const [run, setRun] = useState(chains);
  const [groupKey, setGroupKey] = useState(initialKey);
  return (
    <>
      <GroupKeyEditor
        id="key"
        labelledBy="key-title"
        value={groupKey}
        onChange={setGroupKey}
        sharedKeyHint={
          <SharedKeyHint
            teamLabel={label}
            groupKey={groupKey}
            deliverToTeams={on}
            onAdd={() => setGroupKey([...groupKey, label])}
          />
        }
      />
      <RouteTeamsOption
        id="route-teams"
        teamLabel={label}
        deliverToTeams={on}
        runTeamChains={run}
        onDeliverToTeamsChange={setOn}
        onRunTeamChainsChange={setRun}
        errors={errors}
        readOnly={readOnly}
      />
    </>
  );
}

async function renderOption(props: Parameters<typeof Harness>[0] = {}) {
  const queryClient = new QueryClient();
  queryClient.setQueryData(SESSION_QUERY_KEY, {
    session: null,
    ended: false,
  } satisfies SessionRead);
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Harness {...props} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const deliverSwitch = () => page.getByTestId("route-deliver-to-teams");
const chainsSwitch = () => page.getByTestId("route-run-team-chains");
const hint = () => page.getByTestId("shared-key-hint");

const HINT =
  "The Alert Group also goes to the Destinations of the Teams whose values are in its Alerts, with their Severity conditions. A new Team adds channels and removes none.";
const SHARED =
  "Alerts of different Teams with the same key share one Alert Group, shown in the channels of all of them. Add team to the key if each Team needs its own.";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("Also to the Teams from the label", () => {
  test("shows the option with its hint, Learn more, and the sub-option waiting while it is off", async () => {
    await renderOption();
    await expect
      .element(page.getByLabelText("Also to the Teams from the label team", { exact: true }))
      .not.toBeChecked();
    await expect.element(page.getByText(HINT)).toBeVisible();
    await expect.element(page.getByLabelText("Run the Teams' Escalation chains")).toBeDisabled();
    const link = page.getByTestId("docs-link");
    await expect.element(link).toHaveAttribute("data-docs", "field.deliver_to_teams");
    await expect
      .element(link)
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/concepts/routing-to-teams/#also-to-the-teams-from-the-label",
      );
    expect(hint().elements()).toHaveLength(0);

    await deliverSwitch().click();
    await expect.element(deliverSwitch()).toBeChecked();
    await expect.element(chainsSwitch()).toBeEnabled();
    await expect.element(chainsSwitch()).toBeChecked();
    await chainsSwitch().click();
    await expect.element(chainsSwitch()).not.toBeChecked();
  });

  test("with the option on and no team in the key, says so and Add team to the key adds it", async () => {
    await renderOption({ deliver: true });
    await expect.element(hint().getByText(SHARED)).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Add team to the key" })).toBeVisible();
    await page.getByRole("button", { name: "Add team to the key" }).click();
    const chips = page.getByTestId("group-key-label");
    await expect.element(chips.nth(1)).toHaveTextContent("team");
    expect(chips.elements().map((c) => c.textContent?.trim())).toEqual(["alertname", "team"]);
    expect(hint().elements()).toHaveLength(0);
    // Turning the option off and on again with the label in the key says nothing.
    await deliverSwitch().click();
    await deliverSwitch().click();
    expect(hint().elements()).toHaveLength(0);
  });

  test("keeps quiet when the key already has the label or the option is off", async () => {
    await renderOption({ deliver: true, groupKey: ["alertname", "team"] });
    expect(hint().elements()).toHaveLength(0);
    await deliverSwitch().click();
    expect(hint().elements()).toHaveLength(0);
  });

  test("names the Organization's Team label in the texts", async () => {
    await renderOption({ label: "squad", deliver: true });
    await expect
      .element(page.getByLabelText("Also to the Teams from the label squad", { exact: true }))
      .toBeChecked();
    await expect.element(page.getByRole("button", { name: "Add squad to the key" })).toBeVisible();
  });

  test("shows a refusal of the option at the option", async () => {
    await renderOption({ errors: { deliverToTeams: "This value is not valid." } });
    await expect.element(page.getByText("This value is not valid.")).toBeVisible();
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderOption({ deliver: true });
    await expect
      .element(page.getByLabelText("Ещё командам из метки team", { exact: true }))
      .toBeChecked();
    await expect.element(page.getByLabelText("Запускать цепочки эскалации команд")).toBeEnabled();
    await expect
      .element(
        hint().getByText(
          "Алерты разных команд с одним ключом попадают в одну группу алертов, которую видно в каналах всех этих команд. Добавьте team в ключ, если каждой команде нужна своя.",
        ),
      )
      .toBeVisible();
    await expect.element(page.getByRole("button", { name: "Добавить team в ключ" })).toBeVisible();
  });

  test("without routes:write shows the option as text, no switches", async () => {
    await renderOption({ deliver: true, chains: false, readOnly: true });
    await expect.element(page.getByTestId("route-deliver-to-teams")).toHaveTextContent("On");
    await expect.element(page.getByTestId("route-run-team-chains")).toHaveTextContent("Off");
    expect(page.getByRole("switch").elements()).toHaveLength(0);
    await expect.element(page.getByText(HINT)).toBeVisible();
  });
});
