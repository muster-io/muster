// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The "Managed by" mark in a real browser (C-33.FR-12): the badge, the warning of a form and the line of a delete
// dialog, in English and Russian, with the tool untranslated, and nothing for an object without a mark.

import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { ManagedByBadge, ManagedByDeleteLine, ManagedByWarning } from "./managed-by";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

function renderIn(children: ReactNode) {
  return render(<I18nextProvider i18n={i18n}>{children}</I18nextProvider>);
}

describe("ManagedByBadge", () => {
  test("shows the tool in English and in Russian", async () => {
    const screen = await renderIn(<ManagedByBadge tool="terraform" />);
    await expect
      .element(screen.getByTestId("managed-by-badge"))
      .toHaveTextContent("Managed by terraform");
    await i18n.changeLanguage("ru");
    await expect
      .element(screen.getByTestId("managed-by-badge"))
      .toHaveTextContent("Управляется terraform");
  });

  test("shows nothing for null or for a read without the field", async () => {
    const screen = await renderIn(
      <>
        <ManagedByBadge tool={null} />
        <ManagedByBadge tool={undefined} />
      </>,
    );
    expect(screen.container.querySelector("[data-testid='managed-by-badge']")).toBeNull();
  });
});

describe("ManagedByWarning", () => {
  test("names the object and the tool, as an informational note", async () => {
    const screen = await renderIn(<ManagedByWarning object="route" tool="terraform" />);
    const warning = screen.getByTestId("managed-by-warning");
    await expect
      .element(warning)
      .toHaveTextContent(
        "This route is managed by terraform. Changes you save here are undone by the next run of terraform: change its code instead.",
      );
    await expect.element(warning).toHaveAttribute("role", "note");
  });

  test("names the escalation chain in Russian", async () => {
    await i18n.changeLanguage("ru");
    const screen = await renderIn(<ManagedByWarning object="escalationChain" tool="terraform" />);
    await expect
      .element(screen.getByTestId("managed-by-warning"))
      .toHaveTextContent(
        "Этим объектом (цепочка эскалации) управляет terraform. Изменения, сохранённые здесь, отменит следующий запуск terraform: меняйте его код.",
      );
  });

  test("shows nothing without a mark", async () => {
    const screen = await renderIn(
      <>
        <ManagedByWarning object="route" tool={null} />
        <ManagedByWarning object="team" tool={undefined} />
      </>,
    );
    expect(screen.container.querySelector("[data-testid='managed-by-warning']")).toBeNull();
  });
});

describe("ManagedByDeleteLine", () => {
  test("says that the tool creates the object again, in both languages", async () => {
    const screen = await renderIn(<ManagedByDeleteLine tool="terraform" />);
    await expect
      .element(screen.getByTestId("managed-by-delete-line"))
      .toHaveTextContent("terraform creates it again on its next run.");
    await i18n.changeLanguage("ru");
    await expect
      .element(screen.getByTestId("managed-by-delete-line"))
      .toHaveTextContent("terraform создаст его снова при следующем запуске.");
  });

  test("shows nothing without a mark", async () => {
    const screen = await renderIn(<ManagedByDeleteLine tool={null} />);
    expect(screen.container.querySelector("[data-testid='managed-by-delete-line']")).toBeNull();
  });
});
