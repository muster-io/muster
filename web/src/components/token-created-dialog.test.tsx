// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Creating a token in a real browser: the form offers only the Permissions held and warns while no expiry is chosen,
// the value shows once with the warning and a copy button, and closing the dialog leaves it nowhere in the page.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Permission } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
// The dialog is laid out by its classes, as in the application; without them its backdrop covers the form.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { type TokenInput, TokenCreateDialog } from "./token-created-dialog";

// A made-up value in the format of the API; no real token.
const VALUE = "mstr_pat_testvalue0123456789abcdefghijklmnopqrstuv";

const HELD: Permission[] = ["users:read", "alert-groups:read", "alert-groups:acknowledge"];

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

async function renderDialog(
  held: Permission[] | undefined,
  answer: () => Promise<{ value: string }> = () => Promise.resolve({ value: VALUE }),
) {
  const queryClient = new QueryClient();
  const read: SessionRead = { session: null, ended: false };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const sent: TokenInput[] = [];
  const created = vi.fn();
  const screen = await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <TokenCreateDialog
          description="Test"
          held={held}
          create={(input) => {
            sent.push(input);
            return answer();
          }}
          onCreated={created}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { screen, sent, created, queryClient };
}

function ignore(): void {}

/** A promise and the function that resolves it. */
function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve: (value: T) => void = ignore;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

/** Whether the value is anywhere in the page: its text or the value of a field. */
function valueInPage(): boolean {
  const inputs = [...document.querySelectorAll("input")].some((i) => i.value.includes(VALUE));
  return inputs || (document.body.textContent ?? "").includes(VALUE);
}

describe("token created dialog", () => {
  test("shows the value once, with a copy button, and drops it when closed", async () => {
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    const { sent, created, queryClient } = await renderDialog(HELD);
    await page.getByRole("button", { name: "Create token" }).click();
    const dialog = page.getByRole("dialog");

    // Only the Permissions held are offered, and none is chosen yet.
    const boxes = dialog.getByRole("checkbox");
    await expect.element(boxes.nth(0)).toBeVisible();
    expect(boxes.all().map((b) => b.element().getAttribute("id"))).toEqual([
      "token-permissions-alert-groups-read",
      "token-permissions-alert-groups-acknowledge",
      "token-permissions-users-read",
    ]);
    await expect.element(dialog.getByLabelText("users:write")).not.toBeInTheDocument();

    // Without an expiry date the form warns.
    await expect.element(dialog.getByText("This token never expires.")).toBeVisible();
    await dialog.getByLabelText("Name").fill("laptop-scripts");
    await dialog.getByRole("button", { name: "Create", exact: true }).click();
    await expect.element(dialog.getByText("Choose at least one Permission.")).toBeVisible();
    expect(sent).toEqual([]);

    await dialog.getByLabelText("users:read").click();
    await dialog.getByLabelText("alert-groups:read").click();
    await dialog.getByRole("button", { name: "Create", exact: true }).click();

    await expect.element(dialog.getByTestId("token-value")).toHaveValue(VALUE);
    await expect.element(dialog.getByText("You will not see this token again.")).toBeVisible();
    expect(sent).toEqual([
      {
        name: "laptop-scripts",
        expires_at: null,
        permissions: ["alert-groups:read", "users:read"],
      },
    ]);
    expect(created).toHaveBeenCalledOnce();
    // The request's result in the cache holds no value.
    const cached = queryClient
      .getMutationCache()
      .getAll()
      .map((m) => JSON.stringify(m.state.data ?? null));
    expect(cached.join("")).not.toContain(VALUE);

    await dialog.getByRole("button", { name: "Copy" }).click();
    expect(writeText).toHaveBeenCalledWith(VALUE);
    await expect.element(dialog.getByRole("button", { name: "Copied" })).toBeVisible();

    await dialog.getByRole("button", { name: "Done" }).click();
    await expect.element(page.getByRole("dialog")).not.toBeInTheDocument();
    expect(valueInPage()).toBe(false);

    // Opened again, the dialog starts with an empty form, not the value.
    await page.getByRole("button", { name: "Create token" }).click();
    await expect.element(page.getByRole("dialog").getByLabelText("Name")).toHaveValue("");
    expect(valueInPage()).toBe(false);
    writeText.mockRestore();
  });

  test("a quick choice sets the expiry and the warning goes", async () => {
    const { sent } = await renderDialog(undefined);
    await page.getByRole("button", { name: "Create token" }).click();
    const dialog = page.getByRole("dialog");
    // A Service account token has no Permissions of its own.
    await expect.element(dialog.getByRole("checkbox")).not.toBeInTheDocument();
    await dialog.getByLabelText("Name").fill("ci");
    await dialog.getByRole("button", { name: "90 days" }).click();
    await expect.element(dialog.getByText("This token never expires.")).not.toBeInTheDocument();
    await expect
      .element(dialog.getByRole("button", { name: "90 days" }))
      .toHaveAttribute("aria-pressed", "true");
    await dialog.getByRole("button", { name: "Create", exact: true }).click();
    await expect.element(dialog.getByTestId("token-value")).toHaveValue(VALUE);
    expect(sent).toHaveLength(1);
    const expires = new Date(sent[0]?.expires_at ?? "").getTime();
    const days = (expires - Date.now()) / (24 * 60 * 60 * 1000);
    expect(days).toBeGreaterThan(88);
    expect(days).toBeLessThanOrEqual(90);

    // Escape closes it too, and the value goes with it.
    await dialog.getByTestId("token-value").click();
    await userEvent.keyboard("{Escape}");
    await expect.element(page.getByRole("dialog")).not.toBeInTheDocument();
    expect(valueInPage()).toBe(false);
  });

  test("a value that arrives after the dialog was closed is dropped", async () => {
    const { promise: pending, resolve: answer } = deferred<{ value: string }>();
    const { sent, created } = await renderDialog(undefined, () => pending);
    await page.getByRole("button", { name: "Create token" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabelText("Name").fill("late");
    await dialog.getByRole("button", { name: "Create", exact: true }).click();
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    // The X button closes the dialog while the request is on its way.
    await dialog.getByRole("button", { name: "Close" }).click();
    await expect.element(page.getByRole("dialog")).not.toBeInTheDocument();
    answer({ value: VALUE });
    await vi.waitFor(() => expect(created).toHaveBeenCalledOnce());
    expect(valueInPage()).toBe(false);

    await page.getByRole("button", { name: "Create token" }).click();
    await expect.element(page.getByRole("dialog").getByLabelText("Name")).toHaveValue("");
    expect(valueInPage()).toBe(false);
  });
});
