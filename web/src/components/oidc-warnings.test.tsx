// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The OIDC warnings in a real browser (C-03.FR-6): nobody_can_sign_in keeps "Nobody will be able to sign in through
// OIDC", and groups_claim_missing has its own text, which names the saved groups claim; both link to the groups claim
// guide with "How to fix".

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { OidcWarnings } from "./oidc-warnings";

const NOBODY = "Nobody will be able to sign in through OIDC.";

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

describe("OIDC warnings", () => {
  test("groups_claim_missing names the groups claim and is not the nobody-can-sign-in text", async () => {
    const screen = await render(
      inProviders(
        <OidcWarnings warnings={[{ kind: "groups_claim_missing" }]} groupsClaim="idp-groups" />,
      ),
    );
    const warning = screen.getByTestId("oidc-warning");
    await expect
      .element(
        warning.getByText(
          "No sign-in has carried the groups claim ‘idp-groups’ yet, so group mappings have not matched anyone yet.",
          { exact: true },
        ),
      )
      .toBeVisible();
    await expect.element(warning).not.toHaveTextContent(NOBODY);
    const fix = warning.getByRole("link", {
      name: "How to fix: Groups claim not seen yet (opens in a new tab)",
    });
    await expect.element(fix).toHaveTextContent("How to fix");
    const href = new URL(fix.element().getAttribute("href") ?? "");
    expect(href.pathname).toBe("/muster/latest/sign-in/oidc-keycloak/");
    expect(href.hash).toBe("#2-add-the-groups-claim");
  });

  test("nobody_can_sign_in keeps its text, beside groups_claim_missing", async () => {
    const screen = await render(
      inProviders(
        <OidcWarnings
          warnings={[{ kind: "nobody_can_sign_in" }, { kind: "groups_claim_missing" }]}
          groupsClaim="groups"
        />,
      ),
    );
    const warnings = screen.getByTestId("oidc-warning");
    expect(warnings.elements()).toHaveLength(2);
    const nobody = warnings.filter({ hasText: NOBODY });
    await expect.element(nobody).toBeVisible();
    await expect
      .element(
        nobody.getByRole("link", {
          name: "How to fix: Nobody can sign in through OIDC (opens in a new tab)",
        }),
      )
      .toBeVisible();
    await expect
      .element(warnings.filter({ hasText: "No sign-in has carried the groups claim ‘groups’ yet" }))
      .toBeVisible();
  });

  test("groups_claim_missing names its own claim, one warning per claim", async () => {
    const owner = "https://gitlab.org/claims/groups/owner";
    const screen = await render(
      inProviders(
        <OidcWarnings
          warnings={[
            { kind: "groups_claim_missing", claim: "groups" },
            { kind: "groups_claim_missing", claim: owner },
          ]}
          groupsClaim="groups"
        />,
      ),
    );
    const warnings = screen.getByTestId("oidc-warning");
    expect(warnings.elements()).toHaveLength(2);
    await expect.element(warnings.filter({ hasText: `‘${owner}’` })).toBeVisible();
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    const screen = await render(
      inProviders(
        <OidcWarnings warnings={[{ kind: "groups_claim_missing" }]} groupsClaim="groups" />,
      ),
    );
    await expect
      .element(
        screen
          .getByTestId("oidc-warning")
          .getByText(
            "Ещё ни при одном входе не пришёл claim с группами «groups», поэтому сопоставления групп пока ни к кому не применялись.",
            { exact: true },
          ),
      )
      .toBeVisible();
    const fix = screen.getByTestId("oidc-warning").getByRole("link", {
      name: "Как исправить: Claim с группами ещё не получен (откроется в новой вкладке)",
    });
    await expect.element(fix).toHaveTextContent("Как исправить");
    const href = new URL(fix.element().getAttribute("href") ?? "");
    expect(href.pathname).toBe("/muster/latest/sign-in/oidc-keycloak/");
    expect(href.hash).toBe("#2-add-the-groups-claim");
  });
});
