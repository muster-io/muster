// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The mapping of severity values in a real browser: a value mapped twice is refused next to the second row, rows are
// added and removed, and "Map" on a value seen adds it to the mapping.

import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import type { SeverityMappingEntry, SeverityValueList } from "../api/gen/model";
import i18n from "../i18n";
import { SeverityMappingEditor, duplicateIndexes } from "./severity-mapping-editor";
import { SeverityValues } from "./severity-values";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

const DUPLICATE = "This value is already mapped.";

function Harness({ initial, seen }: { initial: SeverityMappingEntry[]; seen?: SeverityValueList }) {
  const [mapping, setMapping] = useState(initial);
  const [focus, setFocus] = useState<{ index: number; seq: number }>();
  return (
    <div>
      <SeverityMappingEditor
        value={mapping}
        onChange={setMapping}
        errors={[]}
        focusLevelOf={focus}
      />
      {seen !== undefined && (
        <SeverityValues
          list={seen}
          mapping={mapping}
          onMap={(value) => {
            setMapping([...mapping, { value, level: "warning" }]);
            setFocus({ index: mapping.length, seq: (focus?.seq ?? 0) + 1 });
          }}
        />
      )}
      <output data-testid="mapping">{mapping.map((m) => `${m.value}=${m.level}`).join(",")}</output>
    </div>
  );
}

async function renderHarness(initial: SeverityMappingEntry[], seen?: SeverityValueList) {
  return render(
    <I18nextProvider i18n={i18n}>
      <Harness initial={initial} seen={seen} />
    </I18nextProvider>,
  );
}

describe("severity mapping editor", () => {
  test("duplicateIndexes marks the later rows of a value", () => {
    expect(
      duplicateIndexes([
        { value: "P1", level: "critical" },
        { value: "P2", level: "warning" },
        { value: " P1", level: "info" },
        { value: "", level: "info" },
        { value: "", level: "info" },
      ]),
    ).toEqual(new Set([2]));
  });

  test("a value mapped twice is refused next to the second row", async () => {
    const screen = await renderHarness([
      { value: "P1", level: "critical" },
      { value: "P2", level: "warning" },
    ]);
    await expect.element(screen.getByText(DUPLICATE)).not.toBeInTheDocument();
    await screen.getByRole("textbox", { name: "Value 2" }).fill("P1");
    const second = screen.getByRole("textbox", { name: "Value 2" });
    await expect.element(second).toHaveAttribute("aria-invalid", "true");
    await expect.element(second).toHaveAccessibleDescription(DUPLICATE);
    await expect
      .element(screen.getByRole("textbox", { name: "Value 1" }))
      .toHaveAttribute("aria-invalid", "false");
    expect(screen.getByText(DUPLICATE).elements()).toHaveLength(1);
  });

  test("adds a row with the focus on its value, and removes a row", async () => {
    const screen = await renderHarness([{ value: "P1", level: "critical" }]);
    await screen.getByRole("button", { name: "Add value" }).click();
    const value = screen.getByRole("textbox", { name: "Value 2" });
    await expect.element(value).toHaveFocus();
    await value.fill("P3");
    await screen.getByRole("combobox", { name: "Level of P3" }).selectOptions("info");
    await expect.element(screen.getByTestId("mapping")).toHaveTextContent("P1=critical,P3=info");
    await screen.getByRole("button", { name: "Remove P1" }).click();
    await expect.element(screen.getByTestId("mapping")).toHaveTextContent("P3=info");
  });

  test("Map on a value seen adds it with the focus on its level", async () => {
    const screen = await renderHarness([{ value: "P1", level: "critical" }], {
      window_days: 14,
      items: [
        { value: "P1", count: 4, level: "critical", mapped: true },
        { value: "P5", count: 1, level: "warning", mapped: false },
      ],
    });
    await expect
      .element(screen.getByText("These values have no mapping and count as warning: P5."))
      .toBeVisible();
    await expect.element(screen.getByText("Values seen in the last 14 days")).toBeVisible();
    const row = screen.getByRole("row").filter({ hasText: "P5" });
    await expect.element(row.getByText("No mapping — counts as warning")).toBeVisible();
    await expect.element(row.getByText("warning", { exact: true })).toBeVisible();
    await expect.element(screen.getByRole("button", { name: "Map P1" })).not.toBeInTheDocument();
    await screen.getByRole("button", { name: "Map P5" }).click();
    await expect.element(screen.getByRole("combobox", { name: "Level of P5" })).toHaveFocus();
    await expect.element(screen.getByTestId("mapping")).toHaveTextContent("P1=critical,P5=warning");
    await expect.element(screen.getByRole("button", { name: "Map P5" })).not.toBeInTheDocument();
  });
});
