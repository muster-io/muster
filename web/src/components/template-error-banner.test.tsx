// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The banner of a template that keeps failing in a real browser (C-12.FR-6, C-15.FR-7, C-21.FR-11): since when in the
// user's time zone, the error, what happens instead — the fallback template of a Route, or no request for an outgoing
// webhook Destination — and "How to fix" to the runbook of MusterTemplateError, in English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Session, TemplateErrorState } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { TemplateErrorBanner } from "./template-error-banner";

const RUNBOOK =
  "https://muster-io.github.io/muster/latest/operations/runbooks/MusterTemplateError/";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

async function renderBanner(state: TemplateErrorState) {
  const queryClient = new QueryClient();
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the banner reads only the time zone
      user: { id: "US0000000000ME", name: "Me", time_zone: "Europe/Berlin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T09:00:00Z",
      idle_expires_at: "2026-10-13T09:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions: [],
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <TemplateErrorBanner state={state} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

describe("TemplateErrorBanner", () => {
  test("an outgoing webhook Destination sends no request and links to the runbook", async () => {
    await renderBanner({
      since: "2026-10-09T08:05:00Z",
      error: "the value id is missing",
      fallback: "not_sent",
    });
    await expect
      .element(page.getByTestId("template-error-banner"))
      .toHaveTextContent(
        "Template error since 10:05: the value id is missing. Requests are not sent. How to fix",
      );
    const link = page.getByRole("link", {
      name: "How to fix: Template error (opens in a new tab)",
    });
    const href = new URL(link.element().getAttribute("href") ?? "");
    expect(href.origin + href.pathname + href.hash).toBe(RUNBOOK);
    await expect.element(link).toHaveAttribute("target", "_blank");
  });

  test("a Route uses the fallback template, in Russian too", async () => {
    await renderBanner({
      since: "2026-10-09T08:05:00Z",
      error: 'map has no entry for key "pod"',
      fallback: "fallback_template",
    });
    const banner = page.getByTestId("template-error-banner");
    await expect
      .element(banner)
      .toHaveTextContent(
        'Template error since 10:05: map has no entry for key "pod". Messages use the fallback template. How to fix',
      );
    await i18n.changeLanguage("ru");
    await expect
      .element(banner)
      .toHaveTextContent(
        'Ошибка шаблона с 10:05: map has no entry for key "pod". Сообщения отправляются по запасному шаблону. Как исправить',
      );
    await expect
      .element(
        page.getByRole("link", {
          name: "Как исправить: Ошибка шаблона (откроется в новой вкладке)",
        }),
      )
      .toBeVisible();
  });
});
