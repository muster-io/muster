// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The warning of a rejected Snapshot in a real browser: its banner names the error the Stored Snapshot has, in English
// and Russian, with a link to the documentation, and the mark in the Integrations list shows the same text.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import type { IntegrationWarning } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { IntegrationWarnings, WarningMark } from "./integration-warnings";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

async function renderIn(children: ReactNode) {
  const queryClient = new QueryClient();
  const read: SessionRead = { session: null, ended: false };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>,
  );
}

const FAILED: IntegrationWarning = {
  kind: "snapshot_failed",
  error: "the webhook has no groupKey",
  snapshot_id: "SS00000000000A",
  since: "2026-11-05T13:42:37Z",
};

describe("snapshot_failed", () => {
  test("the banner names the error and links to the fix, in both languages", async () => {
    const screen = await renderIn(<IntegrationWarnings warnings={[FAILED]} />);
    const banner = screen.getByTestId("integration-warning");
    await expect
      .element(banner)
      .toHaveTextContent(
        "The latest Snapshot was rejected: the webhook has no groupKey. The sender still got 202, and the Snapshot created no alert. Fix the body the sender posts. How to fix",
      );
    await expect
      .element(
        screen.getByRole("link", { name: "How to fix: A rejected Snapshot (opens in a new tab)" }),
      )
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/integrations/alertmanager/#what-the-body-needs",
      );
    await i18n.changeLanguage("ru");
    await expect
      .element(banner)
      .toHaveTextContent(
        "Последний снимок отклонён: the webhook has no groupKey. Отправитель всё равно получил 202, а снимок не создал ни одного алерта. Исправьте тело, которое он отправляет. Как исправить",
      );
  });

  test("the mark of the list counts it as a warning", async () => {
    const screen = await renderIn(<WarningMark warnings={[FAILED]} />);
    await expect
      .element(screen.getByTestId("integration-warning-mark"))
      .toHaveTextContent(
        "1 warningThe latest Snapshot was rejected: the webhook has no groupKey. The sender still got 202, and the Snapshot created no alert. Fix the body the sender posts.",
      );
  });
});
