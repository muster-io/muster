// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The L1 editor of the Role mapping in a real browser (C-03.FR-5): it shows no claim, but keeps the claim of every
// entry it loads and sends it back unchanged when its group or Role is edited, another entry is removed or a new one is
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
