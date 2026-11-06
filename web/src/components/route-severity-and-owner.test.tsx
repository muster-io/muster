// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The section "Severity, Escalation chains and owner" of the Route editor in a real browser: the Minimum severity with
// its three values, "None" selected by default, the hint and "Learn more", the value a save sends (and no `urgent`),
// a server error at the field and the Russian texts; and "Owning Team" with every Team and "None", its hint, and the id
// a save sends (null for none).

import { zodResolver } from "@hookform/resolvers/zod";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useForm, useWatch } from "react-hook-form";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";
import { z } from "zod";

import type { MinimumSeverity, RouteInput } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { team } from "../test/fixtures/teams";
import { formValues, inputOf } from "./route-form";
import {
  RouteSeverityAndOwner,
  type SeverityAndOwnerValues,
  severityAndOwnerSchema,
} from "./route-severity-and-owner";

const schema = z.object(severityAndOwnerSchema);

const BASE: RouteInput = {
  name: "hb",
  description: "",
  matchers: [{ label: "svc", op: "=", value: "hb" }],
  group_key: ["alertname"],
  destination_ids: [],
  policy: {
    reopen_window_seconds: 900,
    grace_period_seconds: 900,
    urgent_rise_removes_ack: true,
    snooze_durations_seconds: [3600],
    thread_batching_window_seconds: 60,
    storm_threshold: 20,
    language: "en",
    templates: { root_message: null, line: null, ack_timeout_notice: null },
    ack_timeout: { enabled: true, first_interval_seconds: 900 },
    reminders: { enabled: true, first_interval_seconds: 14400, cap_seconds: 86400 },
    auto_unacknowledge: false,
  },
};

function Harness({
  initial,
  onSave,
  error,
  owner = "",
  ownerError,
}: {
  initial: MinimumSeverity;
  onSave: (input: RouteInput) => void;
  error?: string;
  owner?: string;
  ownerError?: string;
}) {
  const form = useForm<SeverityAndOwnerValues>({
    resolver: zodResolver(schema),
    defaultValues: { minimum_severity: initial, owning_team_id: owner },
  });
  const owningTeamId = useWatch({ control: form.control, name: "owning_team_id" });
  return (
    <form
      noValidate
      onSubmit={form.handleSubmit((v) =>
        onSave(
          inputOf(BASE, { ...formValues({ ...BASE, minimum_severity: initial }), ...v }, "", false),
        ),
      )}
    >
      <RouteSeverityAndOwner
        id="route-severity"
        minimumSeverity={form.register("minimum_severity")}
        error={error}
        owningTeam={{
          value: owningTeamId,
          error: ownerError,
          onChange: (next) => form.setValue("owning_team_id", next, { shouldDirty: true }),
        }}
      />
      <button type="submit">Save</button>
    </form>
  );
}

async function renderSection(
  initial: MinimumSeverity = "none",
  error?: string,
  owner = "",
  ownerError?: string,
) {
  const onSave = vi.fn<(input: RouteInput) => void>();
  // "Learn more" reads the documentation version from the session, which the root route has read.
  const queryClient = new QueryClient();
  queryClient.setQueryData(SESSION_QUERY_KEY, {
    session: null,
    ended: false,
  } satisfies SessionRead);
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Harness
          initial={initial}
          onSave={onSave}
          error={error}
          owner={owner}
          ownerError={ownerError}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { onSave };
}

const select = () => page.getByLabelText("Minimum severity", { exact: true });

/** The value and the text of each option of a select. */
function optionsOf(el: Element): string[][] {
  return [...el.querySelectorAll<HTMLOptionElement>("option")].map((o) => [o.value, o.text]);
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation(() =>
    Promise.resolve(
      new Response(
        JSON.stringify({
          items: [
            team({ id: "TM0000000000PA", name: "payments", slug: "payments" }),
            team({ id: "TM0000000000SE", name: "search", slug: "search" }),
          ],
          next_cursor: null,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    ),
  );
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the Minimum severity of a Route", () => {
  test("offers None, warning and critical, with None selected by default", async () => {
    await renderSection();
    await expect.element(page.getByText("Severity, Escalation chains and owner")).toBeVisible();
    expect(optionsOf(select().element())).toEqual([
      ["none", "None"],
      ["warning", "warning"],
      ["critical", "critical"],
    ]);
    await expect.element(select()).toHaveValue("none");
  });

  test("shows the hint and a Learn more to the section Minimum severity", async () => {
    await renderSection();
    await expect
      .element(
        page.getByText(
          "Only raises the Severity level of the Route's Alert Groups. Replaces the former 'Urgent': an Urgent Alert Group is a critical one.",
        ),
      )
      .toBeVisible();
    const link = page.getByRole("link", { name: /Learn more.*Minimum severity/ }).element();
    expect(link.getAttribute("href")).toContain(
      "/concepts/routes-and-group-keys/#minimum-severity",
    );
  });

  test("a save with critical sends minimum_severity and no urgent", async () => {
    const { onSave } = await renderSection();
    await select().selectOptions("critical");
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    const sent = onSave.mock.calls[0]?.[0];
    expect(sent?.minimum_severity).toBe("critical");
    expect(sent).not.toHaveProperty("urgent");
  });

  test("a Route read as warning starts on warning", async () => {
    await renderSection("warning");
    await expect.element(select()).toHaveValue("warning");
  });

  test("shows the code of a refusal at the field", async () => {
    await renderSection("none", "required");
    expect(select().element().getAttribute("aria-invalid")).toBe("true");
    expect(page.getByText("Fill in this field.").elements().length).toBe(1);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderSection();
    await expect.element(page.getByText("Важность, эскалация и владелец")).toBeVisible();
    const el = page.getByLabelText("Минимальная важность маршрута", { exact: true }).element();
    expect(optionsOf(el)).toEqual([
      ["none", "Нет"],
      ["warning", "warning"],
      ["critical", "critical"],
    ]);
    await expect
      .element(
        page.getByText(
          "Только поднимает важность групп маршрута. Вместо прежнего «Срочный»: срочная группа — это critical.",
        ),
      )
      .toBeVisible();
  });

  test("offers None and every Team as the owner, with the hint that it does not affect delivery", async () => {
    await renderSection();
    const owner = page.getByLabelText("Owning Team", { exact: true });
    await expect.element(owner).toHaveValue("");
    await vi.waitFor(() =>
      expect(optionsOf(owner.element())).toEqual([
        ["", "None"],
        ["TM0000000000PA", "payments"],
        ["TM0000000000SE", "search"],
      ]),
    );
    await expect
      .element(page.getByText("For ownership and the filter of the UI; does not affect delivery."))
      .toBeVisible();
  });

  test("a save sends the id of the owning Team, and null for none", async () => {
    const { onSave } = await renderSection("none", undefined, "TM0000000000SE");
    const owner = page.getByLabelText("Owning Team", { exact: true });
    await vi.waitFor(() => expect(owner.element().querySelectorAll("option").length).toBe(3));
    await expect.element(owner).toHaveValue("TM0000000000SE");
    await owner.selectOptions("TM0000000000PA");
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave.mock.calls[0]?.[0].owning_team_id).toBe("TM0000000000PA");
    await owner.selectOptions("");
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(onSave).toHaveBeenCalledTimes(2));
    expect(onSave.mock.calls[1]?.[0].owning_team_id).toBeNull();
  });

  test("shows the refusal of the owning Team at its field and reads in Russian", async () => {
    await renderSection("none", undefined, "", "unknown_id");
    const owner = page.getByLabelText("Owning Team", { exact: true });
    expect(owner.element().getAttribute("aria-invalid")).toBe("true");
    await i18n.changeLanguage("ru");
    await expect.element(page.getByLabelText("Команда-владелец", { exact: true })).toBeVisible();
    await expect
      .element(page.getByText("Для владения и фильтра в интерфейсе; на доставку не влияет."))
      .toBeVisible();
  });
});
