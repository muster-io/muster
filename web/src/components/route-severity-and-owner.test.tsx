// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The section "Severity, Escalation chains and owner" of the Route editor in a real browser: the Minimum severity with
// its three values, "None" selected by default, the hint and "Learn more", the value a save sends (and no `urgent`),
// a server error at the field, the Route profiles' value and the Russian texts.

import { zodResolver } from "@hookform/resolvers/zod";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";
import { z } from "zod";

import type { MinimumSeverity, RouteInput } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
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
}: {
  initial: MinimumSeverity;
  onSave: (input: RouteInput) => void;
  error?: string;
}) {
  const form = useForm<SeverityAndOwnerValues>({
    resolver: zodResolver(schema),
    defaultValues: { minimum_severity: initial },
  });
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
      />
      <button type="submit">Save</button>
    </form>
  );
}

async function renderSection(initial: MinimumSeverity = "none", error?: string) {
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
        <Harness initial={initial} onSave={onSave} error={error} />
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
});
