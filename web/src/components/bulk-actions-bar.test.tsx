// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The bulk bar in a real browser, against a stubbed API: each Command only with its Permission, more than
// alert_group.bulk_max selected disables them all with the reason, and a bulk Acknowledge sends the selection and
// lists each Alert Group's outcome, an Owner's name for a skipped one.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Permission, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { BULK_MAX } from "../lib/commands";
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { BulkActionsBar } from "./bulk-actions-bar";

let sent: unknown[] = [];

beforeEach(async () => {
  sent = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((_input, init) => {
    // The bar reads a skipped Alert Group again for its Owner; only the command is a request with a body.
    if (typeof init?.body === "string") {
      sent.push(JSON.parse(init.body));
    }
    return Promise.resolve(
      new Response(
        JSON.stringify({
          results: [
            { alert_group_id: "AG0000000000A1", number: 1, outcome: "done" },
            {
              alert_group_id: "AG0000000000A2",
              number: 2,
              outcome: "skipped",
              code: "owned_by_other",
              message: "skipped: owned by Alice",
            },
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

async function renderBar(ids: string[], permissions: Permission[]) {
  const queryClient = new QueryClient();
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the bar reads only the Permissions
      user: { id: "US0000000000ME" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-08T09:00:00Z",
      idle_expires_at: "2026-10-08T09:00:00Z",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const done = vi.fn();
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <BulkActionsBar
          ids={ids}
          owners={new Map([["AG0000000000A2", "Alice"]])}
          allShown={false}
          onToggleAllShown={() => {}}
          onClear={() => {}}
          onDone={done}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { done };
}

const ALL: Permission[] = [
  "alert-groups:acknowledge",
  "alert-groups:resolve",
  "alert-groups:snooze",
];

describe("BulkActionsBar", () => {
  test("offers each Command only with its Permission", async () => {
    await renderBar(["AG0000000000A1"], ["alert-groups:resolve"]);
    const group = page.getByRole("group", { name: "Commands for the selected Alert Groups" });
    await expect.element(group).toBeVisible();
    expect(
      group
        .getByRole("button")
        .elements()
        .map((b) => b.textContent),
    ).toEqual(["Resolve"]);
  });

  test("more than the limit disables every Command and says why", async () => {
    const ids = Array.from({ length: BULK_MAX + 1 }, (_, i) => `AG${String(i).padStart(12, "0")}`);
    await renderBar(ids, ALL);
    await expect.element(page.getByTestId("bulk-count")).toHaveTextContent("101 selected");
    await expect
      .element(page.getByTestId("bulk-too-many"))
      .toHaveTextContent("Select at most 100 Alert Groups.");
    for (const name of ["Acknowledge", "Resolve", "Snooze", "Unsnooze"]) {
      await expect.element(page.getByRole("button", { name, exact: true })).toBeDisabled();
    }
  });

  test("a bulk Acknowledge sends the selection and lists every outcome", async () => {
    const { done } = await renderBar(["AG0000000000A1", "AG0000000000A2"], ALL);
    await page.getByRole("button", { name: "Acknowledge", exact: true }).click();
    const result = page.getByRole("dialog", { name: "Result: Acknowledge" });
    await expect.element(result).toBeVisible();
    expect(
      result
        .getByTestId("bulk-result-item")
        .elements()
        .map((e) => e.textContent),
    ).toEqual(["#1: done", "#2: skipped: owned by Alice"]);
    expect(sent).toEqual([
      { command: "acknowledge", alert_group_ids: ["AG0000000000A1", "AG0000000000A2"] },
    ]);
    expect(done).toHaveBeenCalledWith(["AG0000000000A1", "AG0000000000A2"]);
  });
});
