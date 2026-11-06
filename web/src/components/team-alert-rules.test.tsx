// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Which Alerts belong to this Team" in a real browser (C-25.FR-5): the Team label with the slug and the Team matchers
// are both on the page, with no switch between them, with the two hints and the example of an Alert Group's Teams line;
// the matchers are checked as the API checks them before they are sent, a Matcher the server refuses shows under its
// field, a save sends the matchers as a whole and the page starts from what came back; without teams:write the section
// only shows them; in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Matcher, Team } from "../api/gen/model";
import i18n from "../i18n";
import { ApiError } from "../lib/api";
import { team } from "../test/fixtures/teams";
import { MATCHERS_MAX, TeamAlertRules } from "./team-alert-rules";

/** Waits until the text of an element holds a text. */
const contains = (el: { element: () => Element }, text: string) =>
  expect.poll(() => el.element().textContent ?? "").toContain(text);

const MATCHER: Matcher = { label: "namespace", op: "=~", value: "checkout|billing" };

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

async function renderRules(options: {
  team?: Team;
  canWrite?: boolean;
  save?: (matchers: Matcher[]) => Promise<Team>;
}) {
  const current = options.team ?? team({ slug: "payments", name: "payments" });
  const save = vi.fn(
    options.save ?? ((matchers: Matcher[]) => Promise.resolve({ ...current, matchers })),
  );
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <TeamAlertRules
          team={current}
          label="team"
          canWrite={options.canWrite ?? true}
          save={save}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { save };
}

const section = () => page.getByTestId("team-rule-matchers");

function refused(pointer: string, code: string, detail?: string) {
  return new ApiError(
    422,
    { title: "Validation failed", errors: [{ pointer, code, detail }] },
    undefined,
  );
}

describe("Which Alerts belong to this Team", () => {
  test("shows the Team label with the slug and the matchers together, with the hints and the example", async () => {
    await renderRules({ team: team({ name: "Payments", slug: "payments", matchers: [MATCHER] }) });
    await contains(page.getByTestId("team-rule-label"), "team = payments");
    await contains(
      section(),
      "A matcher may look at any label of the Alert, not only namespace. Any one matcher is enough.",
    );
    await contains(
      section(),
      "On test clusters with a shared namespace, match by pod or app, or put the team label on the pods.",
    );
    await expect.element(page.getByLabelText("Label of matcher 1")).toHaveValue("namespace");
    await expect.element(page.getByLabelText("Value of matcher 1")).toHaveValue("checkout|billing");
    await contains(page.getByTestId("team-rule-example"), "Teams: Payments — by the Team label");
    // Both are on the page at once: there is no switch between them.
    expect(page.getByRole("radio").elements()).toHaveLength(0);
    expect(page.getByRole("tab").elements()).toHaveLength(0);
  });

  test("names the Team label of the Organization", async () => {
    const current = team({ slug: "payments" });
    const queryClient = new QueryClient();
    await render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <TeamAlertRules
            team={current}
            label="service_team"
            canWrite
            save={() => Promise.resolve(current)}
          />
        </QueryClientProvider>
      </I18nextProvider>,
    );
    await contains(page.getByTestId("team-rule-label"), "service_team = payments");
  });

  test("saves the matchers as a whole and starts from what came back", async () => {
    const { save } = await renderRules({});
    await page.getByRole("button", { name: "Add matcher" }).click();
    await page.getByLabelText("Label of matcher 1").fill("namespace");
    await page.getByLabelText("Operator of matcher 1").selectOptions("=~");
    await page.getByLabelText("Value of matcher 1").fill("checkout|billing");
    await section().getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toEqual([MATCHER]);
    await contains(page.getByTestId("team-rules-status"), "Saved.");
    await expect.element(page.getByLabelText("Value of matcher 1")).toHaveValue("checkout|billing");
    // Removing the only matcher sends an empty list, which clears them.
    await page.getByRole("button", { name: "Remove matcher namespace" }).click();
    await expect.poll(() => page.getByTestId("team-rules-status").element().textContent).toBe("");
    await section().getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(2));
    expect(save.mock.calls[1]?.[0]).toEqual([]);
  });

  test("does not send a matcher without a label", async () => {
    const { save } = await renderRules({});
    await page.getByRole("button", { name: "Add matcher" }).click();
    await page.getByLabelText("Value of matcher 1").fill("checkout");
    await section().getByRole("button", { name: "Save", exact: true }).click();
    await contains(page.getByTestId("matcher-label-error"), "Enter a label name.");
    expect(save).not.toHaveBeenCalled();
    await page.getByLabelText("Label of matcher 1").fill("namespace");
    await expect.element(page.getByTestId("matcher-label-error")).not.toBeInTheDocument();
  });

  test("does not send more matchers than the API takes", async () => {
    const many = Array.from({ length: MATCHERS_MAX + 1 }, (_, i) => ({
      label: "pod",
      op: "=" as const,
      value: `p${i}`,
    }));
    const { save } = await renderRules({ team: team({ matchers: many }) });
    await section().getByRole("button", { name: "Save", exact: true }).click();
    await contains(page.getByRole("alert"), "A Team has at most 50 matchers.");
    expect(save).not.toHaveBeenCalled();
  });

  test("shows the refusal of a regular expression under the field it names, and sends nothing else", async () => {
    const { save } = await renderRules({
      team: team({ matchers: [{ label: "pod", op: "=~", value: "[" }] }),
      save: () =>
        Promise.reject(refused("/matchers/0/value", "invalid_regex", "missing closing ]: `[`")),
    });
    await section().getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    await contains(
      page.getByTestId("matcher-value-error"),
      "The regular expression is not valid: missing closing ]: `[`",
    );
    await expect
      .element(page.getByLabelText("Value of matcher 1"))
      .toHaveAttribute("aria-invalid", "true");
    await page.getByLabelText("Value of matcher 1").fill("api-.*");
    await expect.element(page.getByTestId("matcher-value-error")).not.toBeInTheDocument();
  });

  test("leaves a conflict to the page: no alert of its own for a version that changed", async () => {
    await renderRules({
      team: team({ matchers: [MATCHER] }),
      save: () => Promise.reject(new ApiError(412, { title: "Precondition failed" }, undefined)),
    });
    await userEvent.fill(page.getByLabelText("Value of matcher 1"), "billing");
    await section().getByRole("button", { name: "Save", exact: true }).click();
    await new Promise((done) => setTimeout(done, 150));
    expect(page.getByRole("alert").elements()).toHaveLength(0);
  });

  test("only shows the matchers without teams:write", async () => {
    await renderRules({ team: team({ matchers: [MATCHER] }), canWrite: false });
    await contains(page.getByTestId("matcher-text"), 'namespace=~"checkout|billing"');
    expect(page.getByRole("button", { name: "Save", exact: true }).elements()).toHaveLength(0);
    expect(page.getByRole("button", { name: "Add matcher" }).elements()).toHaveLength(0);
  });

  test("speaks Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderRules({});
    await contains(
      section(),
      "Матчер может смотреть на любую метку алерта, не только на namespace. Подходит любой из матчеров.",
    );
    await contains(
      section(),
      "На стендах с общим namespace матчите по pod или app либо ставьте метку команды на поды.",
    );
    await contains(page.getByTestId("team-rule-example"), "Команды: payments — по метке команды");
  });
});
