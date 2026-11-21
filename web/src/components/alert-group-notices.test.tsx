// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The notice of an Alert Group whose details the retention removed (C-09.FR-16, #660), in a real browser: it names the
// Alerts, the Timeline and the delivery state among what was removed, with the period in days, in English and in
// Russian with its plural.

import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { AlertGroupNotices } from "./alert-group-notices";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

async function renderRemoved(days: number) {
  await render(
    <I18nextProvider i18n={i18n}>
      <AlertGroupNotices notices={[{ kind: "details_removed", retention_days: days }]} />
    </I18nextProvider>,
  );
}

describe("AlertGroupNotices", () => {
  test("says that the Alerts, the Timeline and the delivery state were removed", async () => {
    await renderRemoved(90);
    await expect
      .element(page.getByTestId("notice-details_removed"))
      .toHaveTextContent(
        "Alerts, Timeline and delivery state of this Alert Group were removed after 90 days; only its summary and Notes are kept.",
      );
  });

  test("says it in Russian, with the plural of the period", async () => {
    await i18n.changeLanguage("ru");
    await renderRemoved(91);
    await expect
      .element(page.getByTestId("notice-details_removed"))
      .toHaveTextContent(
        "Алерты, хронология и состояние доставки этой группы удалены через 91 день; остались только сводка и заметки.",
      );
  });
});
