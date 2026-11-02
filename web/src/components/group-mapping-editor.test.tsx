// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The L1 editor of the Role mapping in a real browser (C-03.FR-5): it shows the claim of every entry read-only, by a
// short name with the full claim in a tooltip, in English and Russian and at 360 px, keeps the claim of every entry it
// loads and sends it back unchanged when its group or Role is edited, another entry is removed or a new one is
// added, so that an entry of another claim is never saved against the groups claim; a new entry reads the groups claim.

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
  claimShortName,
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

  test("each row shows its claim by a short name, read-only, with the full claim in a tooltip", async () => {
    const screen = await render(<Harness sent={[]} />);
    const claims = screen.getByTestId("group-mapping-claim");
    expect(claims.elements().map((e) => e.textContent)).toEqual([
      "Claim: owner",
      "Claim: groups",
      "Claim: groups",
    ]);
    expect(claims.elements()[0]?.getAttribute("title")).toBe(`Reads the claim ${OWNER}`);
    expect(claims.elements()[1]?.getAttribute("title")).toBe(
      "Reads the groups claim of these settings",
    );
    expect(claims.elements()[0]?.querySelector("input")).toBeNull();
    expect(claimShortName("https://gitlab.org/claims/groups/maintainer/")).toBe("maintainer");
    expect(claimShortName("roles")).toBe("roles");
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    const screen = await render(<Harness sent={[]} />);
    expect(screen.getByTestId("group-mapping-claim").elements()[1]?.textContent).toBe(
      "Из claim группы",
    );
  });

  test("at 360 px the row keeps its claim without a horizontal scrollbar", async () => {
    await page.viewport(360, 640);
    const screen = await render(<Harness sent={[]} />);
    await expect.element(screen.getByText("Claim: owner")).toBeVisible();
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(360);
  });

  test("edits, removals and additions keep the claims of the other entries", async () => {
    const sent: OidcGroupMapping[][] = [];
    const screen = await render(<Harness sent={sent} />);
    await expect.element(screen.getByText(OWNER)).not.toBeInTheDocument();

    await userEvent.fill(page.getByLabelText("IdP group 1"), "example/platform/*");
    expect(sent.at(-1)?.[0]).toEqual({ claim: OWNER, group: "example/platform/*", role: "admin" });

    await userEvent.selectOptions(page.getByLabelText("Role for IdP group 1"), "responder");
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
    await userEvent.fill(page.getByLabelText("IdP group 3"), "corp/*");
    expect(sent.at(-1)).toEqual([
      { group: "example/platform/*", role: "viewer" },
      { claim: "groups", group: "oncall", role: "responder" },
      { group: "corp/*", role: "viewer" },
    ]);
  });
});
