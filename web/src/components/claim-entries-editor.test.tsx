// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The claim and group entries in a real browser (C-25.FR-4, C-03.FR-5): a row per entry with the claim in a combobox
// over the claims of the last connection check with the groups claim first, any claim typed freely, an empty claim
// reading the groups claim, the Role per row only when asked, a refusal at the claim or the group of its entry, the
// focus after a removal, in English and Russian and 360 px wide.

import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import {
  ClaimEntriesEditor,
  type ClaimEntryRow,
  type EntryErrors,
  newEntryKey,
  offeredClaims,
} from "./claim-entries-editor";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

function Harness({
  initial = [],
  sent,
  errors,
  withRole = false,
}: {
  initial?: ClaimEntryRow[];
  sent?: ClaimEntryRow[][];
  errors?: ReadonlyMap<number, EntryErrors>;
  withRole?: boolean;
}) {
  const [rows, setRows] = useState(initial);
  return (
    <I18nextProvider i18n={i18n}>
      <ClaimEntriesEditor
        value={rows}
        claims={["roles", "groups", "https://gitlab.org/claims/groups/owner", "roles"]}
        groupsClaim="groups"
        withRole={withRole}
        errors={errors}
        hideLegend
        onChange={(next) => {
          setRows(next);
          sent?.push(next);
        }}
        texts={{
          legend: i18n.t("teams.idp.legend"),
          hint: i18n.t("teams.idp.hint"),
          empty: i18n.t("teams.idp.empty"),
          add: i18n.t("teams.idp.add"),
          claim: (number) => i18n.t("teams.idp.claim", { number }),
          group: (number) => i18n.t("teams.idp.group", { number }),
          role: (number) => i18n.t("oidc.mapping.role", { number }),
          remove: (group) => i18n.t("teams.idp.remove", { group }),
        }}
      />
    </I18nextProvider>
  );
}

const row = (group: string, claim?: string): ClaimEntryRow => ({
  key: newEntryKey(),
  group,
  ...(claim === undefined ? {} : { claim }),
});

describe("the claim entries editor", () => {
  test("offers the groups claim first and each discovered claim once", () => {
    expect(offeredClaims(["roles", "groups", "x", "roles"], "groups")).toEqual([
      "groups",
      "roles",
      "x",
    ]);
    expect(offeredClaims([], "groups")).toEqual(["groups"]);
    expect(offeredClaims(["a"], "")).toEqual(["a"]);
  });

  test("shows the empty state and adds a row whose claim is a combobox over the offered claims", async () => {
    const sent: ClaimEntryRow[][] = [];
    await render(<Harness sent={sent} />);
    await expect
      .element(page.getByText("No groups: people are in the Team only when added by hand."))
      .toBeVisible();
    await page.getByRole("button", { name: "Add", exact: true }).click();
    const claim = page.getByRole("combobox", { name: "Claim 1" });
    await expect.element(claim).toHaveFocus();
    await expect.element(claim).toHaveValue("");
    const list = document.getElementById(claim.element().getAttribute("list") ?? "");
    expect([...(list?.querySelectorAll("option") ?? [])].map((o) => o.value)).toEqual([
      "groups",
      "roles",
      "https://gitlab.org/claims/groups/owner",
    ]);
    expect(claim.element().getAttribute("placeholder")).toBe("groups");
    // A claim that discovery does not list may be typed.
    await userEvent.fill(claim, "https://example.org/claims/teams");
    await userEvent.fill(
      page.getByLabelText("Identity provider group 1", { exact: true }),
      "teams/payments/*",
    );
    expect(sent.at(-1)).toMatchObject([
      { claim: "https://example.org/claims/teams", group: "teams/payments/*" },
    ]);
    expect(sent.at(-1)?.[0]?.role).toBeUndefined();
    expect(page.getByLabelText("Role", { exact: false }).elements()).toHaveLength(0);
  });

  test("a removal hands the focus to the next row, and the last one to Add", async () => {
    await render(<Harness initial={[row("a"), row("b")]} />);
    await page.getByRole("button", { name: "Remove group a" }).click();
    await expect.element(page.getByRole("combobox", { name: "Claim 1" })).toHaveFocus();
    expect(
      page.getByLabelText("Identity provider group 1", { exact: true }).element(),
    ).toHaveProperty("value", "b");
    await page.getByRole("button", { name: "Remove group b" }).click();
    await expect.element(page.getByRole("button", { name: "Add", exact: true })).toHaveFocus();
  });

  test("shows the refusal of the server at the group and at the claim of its entry", async () => {
    await render(
      <Harness
        initial={[row("a"), row("b/*/c", "roles")]}
        errors={new Map([[1, { group: "This value has the wrong format.", claim: "Bad claim." }]])}
      />,
    );
    const group = page.getByLabelText("Identity provider group 2", { exact: true });
    expect(group.element().getAttribute("aria-invalid")).toBe("true");
    await expect.element(page.getByText("This value has the wrong format.")).toBeVisible();
    expect(
      page.getByRole("combobox", { name: "Claim 2" }).element().getAttribute("aria-invalid"),
    ).toBe("true");
    await expect.element(page.getByText("Bad claim.")).toBeVisible();
    expect(
      page
        .getByLabelText("Identity provider group 1", { exact: true })
        .element()
        .getAttribute("aria-invalid"),
    ).toBe("false");
  });

  test("picks a Role per row when asked to", async () => {
    const sent: ClaimEntryRow[][] = [];
    await render(<Harness initial={[{ ...row("admins"), role: "admin" }]} sent={sent} withRole />);
    await userEvent.selectOptions(
      page.getByLabelText("Role for IdP group 1", { exact: true }),
      "responder",
    );
    expect(sent.at(-1)?.[0]?.role).toBe("responder");
    await page.getByRole("button", { name: "Add", exact: true }).click();
    expect(sent.at(-1)?.[1]?.role).toBe("viewer");
  });

  test("reads in Russian and fits 360 px", async () => {
    await i18n.changeLanguage("ru");
    await page.viewport(360, 640);
    await render(
      <Harness initial={[row("teams/payments/*", "https://gitlab.org/claims/groups/owner")]} />,
    );
    await expect.element(page.getByLabelText("Группа провайдера 1", { exact: true })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Добавить", exact: true })).toBeVisible();
    await expect
      .element(page.getByRole("button", { name: "Убрать группу teams/payments/*" }))
      .toBeVisible();
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(360);
  });
});
