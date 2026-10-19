// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The warning of the sign-in page in a real browser: absent in a secure context, such as the test page on localhost,
// and shown, in English and in Russian, when the page is not a secure context, as on plain http to another host.

import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { InsecureContextAlert } from "./insecure-context-alert";

const TITLE = "Sign-in needs HTTPS or localhost";
const DESCRIPTION =
  "This page is open over plain http, where the browser drops the session cookie, so signing in brings you back here. Open Muster over HTTPS, or through an SSH tunnel to localhost.";

const secure = Object.getOwnPropertyDescriptor(window, "isSecureContext");

function setSecureContext(value: boolean) {
  Object.defineProperty(window, "isSecureContext", { value, configurable: true });
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(() => {
  if (secure === undefined) {
    Reflect.deleteProperty(window, "isSecureContext");
  } else {
    Object.defineProperty(window, "isSecureContext", secure);
  }
});

function renderAlert() {
  return render(
    <I18nextProvider i18n={i18n}>
      <InsecureContextAlert />
      <p>rendered</p>
    </I18nextProvider>,
  );
}

describe("InsecureContextAlert", () => {
  test("is absent in a secure context", async () => {
    expect(window.isSecureContext).toBe(true);
    const screen = await renderAlert();
    await expect.element(screen.getByText("rendered")).toBeVisible();
    expect(page.getByRole("alert").elements()).toHaveLength(0);
    expect(document.body.textContent).not.toContain(TITLE);
  });

  test("explains why sign-in cannot work when the page is not a secure context", async () => {
    setSecureContext(false);
    const screen = await renderAlert();
    const alert = screen.getByRole("alert");
    await expect.element(alert.getByText(TITLE, { exact: true })).toBeVisible();
    await expect.element(alert.getByText(DESCRIPTION, { exact: true })).toBeVisible();
    await i18n.changeLanguage("ru");
    await expect
      .element(alert.getByText("Для входа нужен HTTPS или localhost", { exact: true }))
      .toBeVisible();
    await expect
      .element(
        alert.getByText(
          "Страница открыта по обычному http, где браузер отбрасывает cookie сеанса, поэтому после входа вы снова окажетесь здесь. Откройте Muster по HTTPS или через SSH-туннель на localhost.",
          { exact: true },
        ),
      )
      .toBeVisible();
  });
});
