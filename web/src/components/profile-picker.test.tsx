// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The choice of a Route profile in a real browser, against a stubbed API: the cards read Paging and Informational (in
// Russian «С призывом» and «Информационный»), and the id on_call, which a 0.1 replica still answers during a rolling
// update, reads as Paging.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { RouteProfile } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { ProfilePicker } from "./profile-picker";

const POLICY: RouteProfile["policy"] = {
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
};

function profile(id: string): RouteProfile {
  return {
    // A 0.1 replica's id is outside the enum the client types.
    id: id as RouteProfile["id"],
    name: id,
    minimum_severity: "none",
    urgent: false,
    group_key: ["alertname"],
    policy: POLICY,
  };
}

async function renderPicker(ids: string[]) {
  vi.spyOn(window, "fetch").mockImplementation(() =>
    Promise.resolve(
      new Response(JSON.stringify({ items: ids.map(profile) }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, {
    session: null,
    ended: false,
  } satisfies SessionRead);
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <ProfilePicker onPick={() => {}} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the Route profile cards", () => {
  test("name the profiles Paging and Informational", async () => {
    await renderPicker(["paging", "informational"]);
    await expect.element(page.getByTestId("route-profile-paging")).toHaveTextContent("Paging");
    await expect
      .element(page.getByTestId("route-profile-informational"))
      .toHaveTextContent("Informational");
  });

  test("read the id on_call of a 0.1 replica as Paging", async () => {
    await renderPicker(["on_call", "informational"]);
    const card = page.getByTestId("route-profile-on_call");
    await expect.element(card).toHaveTextContent("Paging");
    await expect.element(card).not.toHaveTextContent("Informational");
  });

  test("read Paging as «С призывом» in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderPicker(["on_call", "informational"]);
    await expect.element(page.getByTestId("route-profile-on_call")).toHaveTextContent("С призывом");
  });
});
