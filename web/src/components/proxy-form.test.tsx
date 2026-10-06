// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The shared proxy form in a real browser: its states, the update it builds, and the write-only password, which shows
// only its status and is never put into an input.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import type { ProxyConfig, SecretStatus } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { formatDateTime } from "../lib/time";
import { ProxyForm, type ProxyValues, proxyErrors, proxyInput, proxyValues } from "./proxy-form";

const CHANGED_AT = "2026-10-01T09:30:00Z";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

/** Renders the form as a page does, and exposes the values it holds. */
async function renderForm(config: ProxyConfig, passwordStatus: SecretStatus | undefined) {
  const queryClient = new QueryClient();
  const read: SessionRead = { session: null, ended: false };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const current: { values: ProxyValues } = { values: proxyValues(config) };
  function Harness() {
    const [values, setValues] = useState(current.values);
    return (
      <ProxyForm
        idPrefix="test-proxy"
        value={values}
        passwordStatus={passwordStatus}
        onChange={(next) => {
          current.values = next;
          setValues(next);
        }}
      />
    );
  }
  const screen = await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Harness />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { screen, current };
}

function passwordInputs(): number {
  return document.querySelectorAll('input[type="password"]').length;
}

describe("proxy form", () => {
  test("without a proxy only the switch shows; turning it on shows every field", async () => {
    const { screen, current } = await renderForm({ enabled: false }, undefined);
    await expect.element(screen.getByLabelText("Use a proxy")).not.toBeChecked();
    await expect.element(screen.getByLabelText("Address")).not.toBeInTheDocument();

    await screen.getByLabelText("Use a proxy").click();
    await expect.element(screen.getByLabelText("Type")).toHaveValue("http");
    await expect.element(screen.getByLabelText("Address")).toBeVisible();
    await expect.element(screen.getByLabelText("Username")).toBeVisible();
    await expect.element(screen.getByText("Not set")).toBeVisible();
    expect(current.values.enabled).toBe(true);
    expect(proxyErrors(current.values)).toEqual({ address: "required" });

    await screen.getByLabelText("Type").selectOptions("socks5");
    await screen.getByLabelText("Address").fill(" 127.0.0.1:18092 ");
    expect(proxyErrors(current.values)).toEqual({});
    expect(proxyInput(current.values)).toEqual({
      enabled: true,
      type: "socks5",
      address: "127.0.0.1:18092",
      username: null,
    });
  });

  test("a stored password shows only that it is set and when it changed, never a value", async () => {
    const { screen, current } = await renderForm(
      { enabled: true, type: "http", address: "proxy.example.org:3128", username: "muster" },
      { set: true, updated_at: CHANGED_AT },
    );
    const changed = formatDateTime(
      CHANGED_AT,
      Intl.DateTimeFormat().resolvedOptions().timeZone,
      "en",
    );
    await expect.element(screen.getByText(`Set, changed ${changed}`)).toBeVisible();
    expect(passwordInputs()).toBe(0);
    // Kept: the update leaves the password out, so the server keeps the stored one.
    const kept = proxyInput(current.values);
    expect(kept).not.toHaveProperty("password");
    expect(kept.username).toBe("muster");

    // Replace opens an empty input; what is typed is sent.
    await screen.getByRole("button", { name: "Replace" }).click();
    const input = screen.getByLabelText("Password");
    await expect.element(input).toHaveValue("");
    await expect.element(input).toHaveAttribute("type", "password");
    await input.fill("new-proxy-password");
    expect(proxyInput(current.values).password).toBe("new-proxy-password");

    // Back to the stored one: the input goes away with what was typed.
    await screen.getByRole("button", { name: "Keep the current one" }).click();
    expect(passwordInputs()).toBe(0);
    expect(proxyInput(current.values)).not.toHaveProperty("password");

    // Clear sends null, and can be undone.
    await screen.getByRole("button", { name: "Clear" }).click();
    await expect.element(screen.getByText("Will be removed when you save.")).toBeVisible();
    expect(proxyInput(current.values).password).toBeNull();
    await screen.getByRole("button", { name: "Undo" }).click();
    await expect.element(screen.getByText(`Set, changed ${changed}`)).toBeVisible();
    expect(proxyInput(current.values)).not.toHaveProperty("password");
  });

  test("an unset password offers to set one and no Clear", async () => {
    const { screen } = await renderForm(
      { enabled: true, type: "https", address: "proxy.example.org:443" },
      { set: false, updated_at: null },
    );
    await expect.element(screen.getByText("Not set")).toBeVisible();
    await expect.element(screen.getByRole("button", { name: "Set a value" })).toBeVisible();
    await expect.element(screen.getByRole("button", { name: "Clear" })).not.toBeInTheDocument();
    await expect.element(screen.getByLabelText("Type")).toHaveValue("https");
  });

  test("read-only, the form disables its fields and offers no change of the password", async () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(SESSION_QUERY_KEY, { session: null, ended: false });
    const screen = await render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <ProxyForm
            idPrefix="ro-proxy"
            value={proxyValues({ enabled: true, type: "http", address: "proxy.example.org:3128" })}
            passwordStatus={{ set: true, updated_at: CHANGED_AT }}
            disabled
            onChange={() => {}}
          />
        </QueryClientProvider>
      </I18nextProvider>,
    );
    await expect.element(screen.getByLabelText("Use a proxy")).toBeDisabled();
    await expect.element(screen.getByLabelText("Address")).toBeDisabled();
    await expect.element(screen.getByLabelText("Type")).toBeDisabled();
    await expect.element(screen.getByText(/^Set, changed /)).toBeVisible();
    await expect.element(screen.getByRole("button", { name: "Replace" })).not.toBeInTheDocument();
    await expect.element(screen.getByRole("button", { name: "Clear" })).not.toBeInTheDocument();
    expect(passwordInputs()).toBe(0);
  });

  test("the form shows field errors and links them to their inputs", async () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(SESSION_QUERY_KEY, { session: null, ended: false });
    const screen = await render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <ProxyForm
            idPrefix="err-proxy"
            value={proxyValues({ enabled: true })}
            passwordStatus={undefined}
            errors={{ address: "Fill in this field." }}
            onChange={() => {}}
          />
        </QueryClientProvider>
      </I18nextProvider>,
    );
    const address = screen.getByLabelText("Address");
    await expect.element(address).toHaveAttribute("aria-invalid", "true");
    await expect.element(address).toHaveAccessibleDescription("Fill in this field.");
  });
});
