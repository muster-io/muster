// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Keyring status in a real browser: each state of a key, the replicas that hold or lack it, and the sentence of a
// key that can be removed.

import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import type { KeyringKey } from "../api/gen/model";
import i18n from "../i18n";
import { KeyringStatus } from "./keyring-status";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

function key(
  id: string,
  state: KeyringKey["state"],
  held: string[],
  missing: string[] = [],
): KeyringKey {
  return {
    id,
    state,
    active: state === "active",
    held_by: held,
    missing_on: missing,
    needed: state === "needed",
    removable: state === "removable",
  };
}

describe("keyring status", () => {
  test("shows every state with the replicas", async () => {
    const screen = await render(
      <I18nextProvider i18n={i18n}>
        <KeyringStatus
          keys={[
            key("k-active", "active", ["replica-a", "replica-b"]),
            key("k-needed", "needed", ["replica-a"], ["replica-b"]),
            key("k-new", "not_active_yet", ["replica-a"]),
            key("k-old", "removable", ["replica-a"]),
          ]}
        />
      </I18nextProvider>,
    );
    const table = screen.getByRole("table", { name: "Keys" });
    const row = (id: string) => table.getByRole("row").filter({ hasText: id });
    await expect.element(row("k-active").getByText("Active", { exact: true })).toBeVisible();
    await expect.element(row("k-active").getByText("Held by replica-a, replica-b")).toBeVisible();
    await expect.element(row("k-needed").getByText("Still needed")).toBeVisible();
    const missing = row("k-needed").getByText("Missing on replica-b");
    await expect.element(missing).toBeVisible();
    await expect.element(missing).toHaveClass("text-destructive");
    await expect.element(row("k-new").getByText("Not active yet")).toBeVisible();
    await expect.element(row("k-new").getByText(/can be removed/)).not.toBeInTheDocument();
    await expect
      .element(
        row("k-old").getByText(
          "Key k-old is no longer used and can be removed from MUSTER_SECRET_KEYS.",
        ),
      )
      .toBeVisible();
    expect(screen.getByText(/can be removed/).elements()).toHaveLength(1);
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    const screen = await render(
      <I18nextProvider i18n={i18n}>
        <KeyringStatus keys={[key("k-old", "removable", ["replica-a"])]} />
      </I18nextProvider>,
    );
    await expect
      .element(
        screen.getByText(
          "Ключ k-old больше не используется, его можно убрать из MUSTER_SECRET_KEYS.",
        ),
      )
      .toBeVisible();
    await expect.element(screen.getByText("Есть на replica-a")).toBeVisible();
  });
});
