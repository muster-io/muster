// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Delivery section of the Alert Group page in a real browser: the text of every delivery state and mark, a
// message link that opens only http and https addresses in a new tab without the opener, and the error of the
// messenger as text.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { AlertGroupDelivery, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { AlertGroupDeliveries, deliveryStateText, safeMessageUrl } from "./alert-group-deliveries";

function delivery(extra: Partial<AlertGroupDelivery>): AlertGroupDelivery {
  return {
    destination: {
      id: "DS0000000000AA",
      name: "alerts",
      type: "mattermost",
      health: { state: "healthy" },
    },
    state: "delivered",
    thread_not_attached: false,
    possible_duplicate: false,
    ...extra,
  };
}

function providers(children: ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the section reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-09T09:00:00Z",
      idle_expires_at: "2026-10-09T09:00:00Z",
      method: "local",
      permissions: ["alert-groups:read"],
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

function text(extra: Partial<AlertGroupDelivery>): string {
  return deliveryStateText(i18n.t, delivery(extra));
}

describe("the Delivery section", () => {
  test("names every state as the reference does", () => {
    expect(text({ state: "delivered" })).toBe("Delivered");
    expect(text({ state: "pending" })).toBe("Pending");
    expect(text({ state: "not_delivered", error: "answered 400" })).toBe(
      "Not delivered: answered 400",
    );
    expect(text({ state: "waiting_for_broken_destination" })).toBe("Waiting: alerts is Broken");
    expect(text({ state: "deleted_in_messenger" })).toBe("Deleted in the messenger");
    expect(text({ state: "withheld" })).toBe(
      "Not posted: resolved during a Storm or while the Destination was Broken",
    );
    expect(text({ state: "retired" })).toBe("No longer updated here");
  });

  test("links only http and https addresses", () => {
    expect(safeMessageUrl("http://127.0.0.1:18065/dev/pl/abc")).toBe(
      "http://127.0.0.1:18065/dev/pl/abc",
    );
    expect(safeMessageUrl("https://t.me/c/1/2")).toBe("https://t.me/c/1/2");
    expect(safeMessageUrl("javascript:alert(1)")).toBeUndefined();
    expect(safeMessageUrl(" JavaScript:alert(1)")).toBeUndefined();
    expect(safeMessageUrl("data:text/html,x")).toBeUndefined();
    expect(safeMessageUrl("/relative")).toBeUndefined();
    expect(safeMessageUrl(null)).toBeUndefined();
  });

  test("shows the rows with their marks, a safe link and the error as text", async () => {
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [
            delivery({
              message_url: "http://127.0.0.1:18065/dev/pl/abc",
              thread_not_attached: true,
              possible_duplicate: true,
            }),
            delivery({
              destination: {
                id: "DS0000000000AB",
                name: "prod",
                type: "mattermost",
                health: { state: "broken", since: "2026-10-09T08:00:00Z", reason: "403" },
              },
              state: "not_delivered",
              error: "<b>answered 400</b>",
              message_url: "javascript:alert(1)",
            }),
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    await render(providers(<AlertGroupDeliveries alertGroupId="AG0000000000AA" />));
    const link = page.getByRole("link", { name: "Delivered (opens in a new tab)" });
    await expect.element(link).toHaveAttribute("href", "http://127.0.0.1:18065/dev/pl/abc");
    await expect.element(link).toHaveAttribute("target", "_blank");
    await expect.element(link).toHaveAttribute("rel", "noopener noreferrer");
    await expect
      .element(page.getByTestId("delivery-thread-not-attached"))
      .toHaveTextContent("Thread not attached to the Root message");
    await expect
      .element(page.getByTestId("delivery-possible-duplicate"))
      .toHaveTextContent("Possible duplicate");
    await expect
      .element(page.getByTestId("delivery-state").nth(1))
      .toHaveTextContent("Not delivered: <b>answered 400</b>");
    expect(
      page.getByTestId("delivery-row").nth(1).element().querySelector("a[href^='javascript']"),
    ).toBeNull();
    await expect.element(page.getByTestId("destination-health").nth(1)).toHaveTextContent("Broken");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    expect(deliveryStateText(i18n.t, delivery({ state: "waiting_for_broken_destination" }))).toBe(
      "Ожидает: место доставки alerts сломано",
    );
    expect(deliveryStateText(i18n.t, delivery({ state: "retired" }))).toBe(
      "Здесь больше не обновляется",
    );
  });
});
