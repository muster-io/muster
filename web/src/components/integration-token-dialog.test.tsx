// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Creating an Integration token in a real browser: the value and the Alertmanager configuration show once, each with a
// copy button and the warning, and closing the dialog leaves neither anywhere in the page or the query cache.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { IntegrationTokenCreated } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
// The dialog is laid out by its classes, as in the application; without them its backdrop covers the form.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { IntegrationTokenDialog } from "./integration-token-dialog";

// A made-up value in the format of the API; no real token.
const VALUE = "mstr_int_testvalue0123456789abcdefghijklmnopqrstuv";
const SNIPPET = [
  "receivers:",
  "  - name: muster-prod-eu",
  "    webhook_configs:",
  "      - url: http://localhost:8081/api/v1/ingest",
  "        send_resolved: true",
  "        max_alerts: 0",
  "        http_config:",
  "          authorization:",
  "            type: Bearer",
  `            credentials: ${VALUE}`,
  "",
].join("\n");

function answerFor(name: string): IntegrationTokenCreated {
  return {
    token: { id: "ITK7M3QX9P2RTA", name, created_at: "2026-10-06T10:00:00Z", last_used_at: null },
    value: VALUE,
    alertmanager_snippet: SNIPPET,
    heartbeat_snippet: null,
  };
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

async function renderDialog(
  answer: (name: string) => Promise<IntegrationTokenCreated> = (name) =>
    Promise.resolve(answerFor(name)),
) {
  const queryClient = new QueryClient();
  const read: SessionRead = { session: null, ended: false };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const sent: string[] = [];
  const created = vi.fn();
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <IntegrationTokenDialog
          integrationName="prod-eu"
          create={(name) => {
            sent.push(name);
            return answer(name);
          }}
          onCreated={created}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { sent, created, queryClient };
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
  const inputs = [...document.querySelectorAll("input, textarea")].some(
    (i) => i instanceof HTMLInputElement && i.value.includes(VALUE),
  );
  return inputs || (document.body.textContent ?? "").includes(VALUE);
}

describe("integration token dialog", () => {
  test("shows the value and the snippet once, with copy buttons, and drops both when closed", async () => {
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    const { sent, created, queryClient } = await renderDialog();
    await page.getByRole("button", { name: "Create token" }).click();
    const dialog = page.getByRole("dialog");
    await expect
      .element(dialog.getByText("You will not see this token again."))
      .not.toBeInTheDocument();
    await dialog.getByLabelText("Name").fill("rotation-1");
    await dialog.getByRole("button", { name: "Create", exact: true }).click();

    await expect.element(dialog.getByTestId("integration-token-value")).toHaveTextContent(VALUE);
    const snippet = dialog.getByTestId("integration-token-snippet");
    const text = snippet.element().textContent ?? "";
    expect(text).toContain("send_resolved: true");
    expect(text).toContain("max_alerts: 0");
    expect(text).toContain(`credentials: ${VALUE}`);
    await expect.element(dialog.getByText("You will not see this token again.")).toBeVisible();
    expect(sent).toEqual(["rotation-1"]);
    expect(created).toHaveBeenCalledOnce();
    // The request's result in the cache holds neither the value nor the snippet.
    const cached = queryClient
      .getMutationCache()
      .getAll()
      .map((m) => JSON.stringify(m.state.data ?? null));
    expect(cached.join("")).not.toContain(VALUE);

    // Each block has its own Copy; the snippet's copies the whole configuration.
    const copies = dialog.getByRole("button", { name: "Copy" });
    expect(copies.all()).toHaveLength(2);
    await copies.nth(1).click();
    expect(writeText).toHaveBeenLastCalledWith(SNIPPET);
    await copies.nth(0).click();
    expect(writeText).toHaveBeenLastCalledWith(VALUE);

    await dialog.getByRole("button", { name: "Done" }).click();
    await expect.element(page.getByRole("dialog")).not.toBeInTheDocument();
    expect(valueInPage()).toBe(false);
    expect((document.body.textContent ?? "").includes("send_resolved")).toBe(false);

    // Opened again, the dialog starts with an empty form, not the value.
    await page.getByRole("button", { name: "Create token" }).click();
    await expect.element(page.getByRole("dialog").getByLabelText("Name")).toHaveValue("");
    expect(valueInPage()).toBe(false);
    writeText.mockRestore();
  });

  test("the name is optional, and Escape drops the value", async () => {
    const { sent } = await renderDialog();
    await page.getByRole("button", { name: "Create token" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Create", exact: true }).click();
    await expect.element(dialog.getByTestId("integration-token-value")).toHaveTextContent(VALUE);
    expect(sent).toEqual([""]);

    await dialog.getByTestId("integration-token-snippet").click();
    await userEvent.keyboard("{Escape}");
    await expect.element(page.getByRole("dialog")).not.toBeInTheDocument();
    expect(valueInPage()).toBe(false);
  });

  test("a value that arrives after the dialog was closed is dropped", async () => {
    const { promise: pending, resolve: answer } = deferred<IntegrationTokenCreated>();
    const { sent, created } = await renderDialog(() => pending);
    await page.getByRole("button", { name: "Create token" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabelText("Name").fill("late");
    await dialog.getByRole("button", { name: "Create", exact: true }).click();
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    // The X button closes the dialog while the request is on its way.
    await dialog.getByRole("button", { name: "Close" }).click();
    await expect.element(page.getByRole("dialog")).not.toBeInTheDocument();
    answer(answerFor("late"));
    await vi.waitFor(() => expect(created).toHaveBeenCalledOnce());
    expect(valueInPage()).toBe(false);

    await page.getByRole("button", { name: "Create token" }).click();
    await expect.element(page.getByRole("dialog").getByLabelText("Name")).toHaveValue("");
    expect(valueInPage()).toBe(false);
  });
});
