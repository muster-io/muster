// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Role mapping editor in a real browser (C-03.FR-5): every row has the claim it reads as a combobox over the claims
// of the last connection check, an entry without a claim reads the groups claim, and the claim of every entry it loads
// is sent back unchanged when its group or Role is edited, another entry is removed or a new one is added, so that an
// entry of another claim is never saved against the groups claim; a new entry reads the groups claim.

import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { OidcGroupMapping } from "../api/gen/model";
import i18n from "../i18n";
import {
  GroupMappingEditor,
  type MappingRow,
  mappingRows,
  mappingsOf,
} from "./group-mapping-editor";

const OWNER = "https://gitlab.org/claims/groups/owner";

const LOADED: OidcGroupMapping[] = [
  { claim: OWNER, group: "example/platform/infra", role: "admin" },
  { group: "example/platform/*", role: "viewer" },
  { claim: "groups", group: "oncall", role: "responder" },
];

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

/** The editor over the loaded entries; sent holds what a save would send after every change. */
function Harness({ sent }: { sent: OidcGroupMapping[][] }) {
  const [rows, setRows] = useState<MappingRow[]>(() => mappingRows(LOADED));
  return (
    <I18nextProvider i18n={i18n}>
      <GroupMappingEditor
        claims={[OWNER, "groups", "roles"]}
        groupsClaim="groups"
        value={rows}
        onChange={(next) => {
          setRows(next);
          sent.push(mappingsOf(next));
        }}
      />
    </I18nextProvider>
  );
}

describe("group mapping editor", () => {
  test("a load sent back unchanged keeps every claim", () => {
    expect(mappingsOf(mappingRows(LOADED))).toEqual(LOADED);
    expect(mappingsOf(mappingRows([{ claim: "", group: " a ", role: "viewer" }]))).toEqual([
      { group: "a", role: "viewer" },
    ]);
  });

  test("each row has its claim in a combobox that offers the groups claim first", async () => {
    await render(<Harness sent={[]} />);
    const values = [1, 2, 3].map((n) => {
      const el = page.getByLabelText(`Claim ${n}`, { exact: true }).element();
      return el instanceof HTMLInputElement ? el.value : "";
    });
    expect(values).toEqual([OWNER, "", "groups"]);
    expect(
      page.getByLabelText("Claim 1", { exact: true }).element().getAttribute("placeholder"),
    ).toBe("groups");
    const list = document.getElementById(
      page.getByLabelText("Claim 1", { exact: true }).element().getAttribute("list") ?? "",
    );
    expect([...(list?.querySelectorAll("option") ?? [])].map((o) => o.value)).toEqual([
      "groups",
      OWNER,
      "roles",
    ]);
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    await render(<Harness sent={[]} />);
    await expect.element(page.getByLabelText("Claim 2", { exact: true })).toBeVisible();
    await expect.element(page.getByLabelText("Группа провайдера 1", { exact: true })).toBeVisible();
  });

  test("at 360 px the rows keep their claims without a horizontal scrollbar", async () => {
    await page.viewport(360, 640);
    await render(<Harness sent={[]} />);
    await expect.element(page.getByLabelText("Claim 1", { exact: true })).toBeVisible();
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(360);
  });

  test("a typed claim is sent with the entry, and a cleared one reads the groups claim", async () => {
    const sent: OidcGroupMapping[][] = [];
    await render(<Harness sent={sent} />);
    await userEvent.fill(page.getByLabelText("Claim 2", { exact: true }), "roles");
    expect(sent.at(-1)?.[1]).toEqual({
      claim: "roles",
      group: "example/platform/*",
      role: "viewer",
    });
    await userEvent.fill(page.getByLabelText("Claim 1", { exact: true }), "");
    expect(sent.at(-1)?.[0]).toEqual({ group: "example/platform/infra", role: "admin" });
  });

  test("edits, removals and additions keep the claims of the other entries", async () => {
    const sent: OidcGroupMapping[][] = [];
    await render(<Harness sent={sent} />);

    await userEvent.fill(page.getByLabelText("IdP group 1", { exact: true }), "example/platform/*");
    expect(sent.at(-1)?.[0]).toEqual({ claim: OWNER, group: "example/platform/*", role: "admin" });

    await userEvent.selectOptions(
      page.getByLabelText("Role for IdP group 1", { exact: true }),
      "responder",
    );
    expect(sent.at(-1)?.[0]).toEqual({
      claim: OWNER,
      group: "example/platform/*",
      role: "responder",
    });

    await page.getByRole("button", { name: "Remove IdP group example/platform/*" }).first().click();
    expect(sent.at(-1)).toEqual([
      { group: "example/platform/*", role: "viewer" },
      { claim: "groups", group: "oncall", role: "responder" },
    ]);

    await page.getByRole("button", { name: "Add IdP group" }).click();
    await userEvent.fill(page.getByLabelText("IdP group 3", { exact: true }), "corp/*");
    expect(sent.at(-1)).toEqual([
      { group: "example/platform/*", role: "viewer" },
      { claim: "groups", group: "oncall", role: "responder" },
      { group: "corp/*", role: "viewer" },
    ]);
  });

  test("a refusal shows at the group or the claim of its entry", async () => {
    await render(
      <I18nextProvider i18n={i18n}>
        <GroupMappingEditor
          groupsClaim="groups"
          value={mappingRows(LOADED)}
          errors={new Map([[1, { group: "This value has the wrong format.", claim: "Bad claim" }]])}
          onChange={() => {}}
        />
      </I18nextProvider>,
    );
    expect(
      page.getByLabelText("IdP group 2", { exact: true }).element().getAttribute("aria-invalid"),
    ).toBe("true");
    await expect.element(page.getByText("This value has the wrong format.")).toBeVisible();
    await expect.element(page.getByText("Bad claim")).toBeVisible();
  });
});
