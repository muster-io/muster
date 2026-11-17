// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The iCal subscription of the Schedule page in a real browser (C-27.FR-10, C-27.AC-7): "New link" shows the link once with
// "Copy the link now: it will not be shown again", a reload shows no link, only "New link" and "Revoke", "New link" while
// one exists asks first, and "Revoke" leaves "New link" alone — in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { PETR, hasText, reply, seedSession } from "../test/fixtures/schedules";
// The dialog is laid out by its classes, as in the application.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { IcalSubscription } from "./ical-subscription";

const URL_ISSUED = "http://localhost:8080/ical/mstr_cal_0123456789abcdef.ics";
let exists = false;
let calls: string[] = [];

beforeEach(async () => {
  exists = false;
  calls = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = init?.method ?? (input instanceof Request ? input.method : "GET");
    if (!url.endsWith("/api/v1/me/ical-feed")) {
      return Promise.resolve(reply(404, { type: "not-found", title: "Not found", status: 404 }));
    }
    calls.push(method);
    if (method === "POST") {
      exists = true;
      return Promise.resolve(reply(201, { url: URL_ISSUED, issued_at: "2026-11-17T09:00:00Z" }));
    }
    if (method === "DELETE") {
      exists = false;
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    return Promise.resolve(
      reply(200, { exists, issued_at: exists ? "2026-11-17T09:00:00Z" : null }),
    );
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

async function renderSection() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedSession(queryClient, PETR, "responder", ["schedules:read"]);
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <IcalSubscription />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

describe("iCal subscription", () => {
  test("shows the title, the hint and New link alone before a link exists", async () => {
    await renderSection();
    await expect.element(page.getByRole("heading", { name: "iCal subscription" })).toBeVisible();
    await expect
      .element(
        page.getByText(
          "A personal link for your calendar application. It is shown once; a revoked link stops working at once.",
        ),
      )
      .toBeVisible();
    await expect.element(page.getByRole("button", { name: "New link" })).toBeVisible();
    expect(page.getByRole("button", { name: "Revoke" }).elements()).toHaveLength(0);
  });

  test("New link shows the link once; a reload shows none, then Revoke leaves New link alone", async () => {
    const first = await renderSection();
    await page.getByRole("button", { name: "New link" }).click();
    await hasText(page.getByTestId("ical-link"), URL_ISSUED);
    await expect
      .element(page.getByText("Copy the link now: it will not be shown again"))
      .toBeVisible();
    await expect.element(page.getByRole("button", { name: "Revoke" })).toBeVisible();

    await first.unmount();
    await renderSection();
    await expect.element(page.getByRole("button", { name: "Revoke" })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "New link" })).toBeVisible();
    expect(page.getByTestId("ical-link").elements()).toHaveLength(0);
    expect(document.body.textContent).not.toContain("mstr_cal_");

    await page.getByRole("button", { name: "Revoke" }).click();
    await vi.waitFor(() =>
      expect(page.getByRole("button", { name: "Revoke" }).elements()).toHaveLength(0),
    );
    await expect.element(page.getByRole("button", { name: "New link" })).toBeVisible();
    expect(calls).toEqual(["GET", "POST", "GET", "DELETE"]);
  });

  test("New link while one exists asks first, and Cancel issues nothing", async () => {
    exists = true;
    await renderSection();
    await page.getByRole("button", { name: "New link" }).click();
    await expect.element(page.getByRole("heading", { name: "Issue a new link?" })).toBeVisible();
    await page.getByRole("button", { name: "Cancel" }).click();
    expect(calls).toEqual(["GET"]);
    await page.getByRole("button", { name: "New link" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "New link" }).click();
    await hasText(page.getByTestId("ical-link"), URL_ISSUED);
    expect(calls).toEqual(["GET", "POST"]);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderSection();
    await expect.element(page.getByRole("heading", { name: "Подписка iCal" })).toBeVisible();
    await expect
      .element(
        page.getByText(
          "Личная ссылка для календаря. Показывается один раз; отозванная перестаёт работать сразу.",
        ),
      )
      .toBeVisible();
    await page.getByRole("button", { name: "Новая ссылка" }).click();
    await expect
      .element(page.getByText("Скопируйте ссылку сейчас — больше её не покажем"))
      .toBeVisible();
    await expect.element(page.getByRole("button", { name: "Отозвать" })).toBeVisible();
  });
});
