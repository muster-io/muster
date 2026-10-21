// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Keyring status in a real browser: each state of a key, the replicas that hold or lack it, and the sentence of a
// key that can be removed; both notices, a removable key and a key missing on replicas, link to the key rotation guide
// with "How to fix" (C-21.FR-11).

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import type { Locator } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { KeyringKey } from "../api/gen/model";
import i18n from "../i18n";
import { KeyringStatus } from "./keyring-status";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

function inProviders(children: ReactNode) {
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
}

/** A "How to fix" link to the key rotation guide of the running version, opened in a new tab. */
async function expectKeyRotationFix(link: Locator, text: string) {
  await expect.element(link).toHaveTextContent(text);
  await expect.element(link).toHaveAttribute("target", "_blank");
  await expect.element(link).toHaveAttribute("rel", "noopener noreferrer");
  const href = new URL(link.element().getAttribute("href") ?? "");
  expect(href.origin).toBe("https://muster-io.github.io");
  expect(href.pathname).toBe("/muster/latest/operations/key-rotation/");
  expect(href.hash).toBe("");
}

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
      inProviders(
        <KeyringStatus
          keys={[
            key("k-active", "active", ["replica-a", "replica-b"]),
            key("k-needed", "needed", ["replica-a"], ["replica-b"]),
            key("k-new", "not_active_yet", ["replica-a"]),
            key("k-old", "removable", ["replica-a"]),
          ]}
        />,
      ),
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

  test("links both notices to the key rotation guide, and no other state", async () => {
    const screen = await render(
      inProviders(
        <KeyringStatus
          keys={[
            key("k-active", "active", ["replica-a", "replica-b"]),
            key("k-needed", "needed", ["replica-a"]),
            key("k-new", "not_active_yet", ["replica-a"], ["replica-b"]),
            key("k-old", "removable", ["replica-a"]),
          ]}
        />,
      ),
    );
    const table = screen.getByRole("table", { name: "Keys" });
    const row = (id: string) => table.getByRole("row").filter({ hasText: id });
    await expect.element(row("k-old").getByText(/can be removed/)).toBeVisible();
    await expectKeyRotationFix(
      row("k-old").getByRole("link", {
        name: "How to fix: Removing an unused master key (opens in a new tab)",
      }),
      "How to fix",
    );
    await expect.element(row("k-new").getByText("Missing on replica-b")).toBeVisible();
    await expectKeyRotationFix(
      row("k-new").getByRole("link", {
        name: "How to fix: A master key missing on replicas (opens in a new tab)",
      }),
      "How to fix",
    );
    expect(row("k-active").getByRole("link").elements()).toHaveLength(0);
    expect(row("k-needed").getByRole("link").elements()).toHaveLength(0);
    expect(table.getByRole("link").elements()).toHaveLength(2);
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    const screen = await render(
      inProviders(<KeyringStatus keys={[key("k-old", "removable", ["replica-a"])]} />),
    );
    await expect
      .element(
        screen.getByText(
          "Ключ k-old больше не используется, его можно убрать из MUSTER_SECRET_KEYS.",
        ),
      )
      .toBeVisible();
    await expect.element(screen.getByText("Есть на replica-a")).toBeVisible();
    await expectKeyRotationFix(
      screen.getByRole("link", {
        name: "Как исправить: Удаление неиспользуемого мастер-ключа (откроется в новой вкладке)",
      }),
      "Как исправить",
    );
  });
});
