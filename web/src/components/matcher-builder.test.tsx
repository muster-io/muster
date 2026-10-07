// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Matcher builder in a real browser: the rows with the four operators, the focus as rows come and go, the
// Matchers a form sends, and a Problem pointer such as /matchers/1/value landing under the field of the row it was
// sent from, in English and Russian.

import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { ApiError } from "../lib/api";
import {
  MatcherBuilder,
  type MatcherRow,
  type MatcherRowErrors,
  matcherErrors,
  matcherRow,
  matcherText,
  matchersOf,
  sentRows,
} from "./matcher-builder";

const REGEX_DETAIL = "error parsing regexp: missing closing ): `api-(`";

function refusal(...errors: { pointer: string; code: string; detail?: string }[]): ApiError {
  return new ApiError(
    422,
    { type: "https://muster-io.github.io/muster/problems/validation-failed", errors },
    undefined,
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("the Matchers a form sends", () => {
  test("leaves out empty rows and trims label names", () => {
    const rows = [
      matcherRow(" alertname ", "=", "Disk"),
      matcherRow(),
      matcherRow("pod", "=~", "api-.*"),
    ];
    expect(matchersOf(rows)).toEqual([
      { label: "alertname", op: "=", value: "Disk" },
      { label: "pod", op: "=~", value: "api-.*" },
    ]);
    expect(sentRows(rows).map((r) => r.label)).toEqual([" alertname ", "pod"]);
  });

  test("shows a Matcher in Alertmanager syntax", () => {
    expect(matcherText({ label: "pod", op: "!~", value: 'api-"x"' })).toBe('pod!~"api-\\"x\\""');
  });
});

describe("matcherErrors", () => {
  test("maps a pointer onto the row it was sent from, past an empty row", () => {
    const rows = [
      matcherRow("alertname", "=", "Disk"),
      matcherRow(),
      matcherRow("pod", "=~", "api-("),
    ];
    const errors = matcherErrors(
      refusal(
        { pointer: "/matchers/1/value", code: "invalid_regex", detail: REGEX_DETAIL },
        { pointer: "/name", code: "required" },
        { pointer: "/matchers/7/label", code: "invalid_format" },
      ),
      sentRows(rows),
    );
    expect([...errors.keys()]).toEqual([rows[2]?.key]);
    expect(errors.get(rows[2]?.key ?? -1)).toEqual({
      value: { code: "invalid_regex", detail: REGEX_DETAIL },
    });
  });

  test("ignores what is not a Problem", () => {
    expect(matcherErrors(new Error("network"), [matcherRow("a")]).size).toBe(0);
  });
});

function Harness({
  initial,
  errorsOf,
}: {
  initial: MatcherRow[];
  errorsOf?: (rows: readonly MatcherRow[]) => Map<number, MatcherRowErrors>;
}) {
  const [rows, setRows] = useState(initial);
  return (
    <form>
      <MatcherBuilder
        id="m"
        rows={rows}
        onChange={setRows}
        errors={errorsOf?.(rows) ?? new Map()}
      />
    </form>
  );
}

async function renderBuilder(
  initial: MatcherRow[],
  errorsOf?: (rows: readonly MatcherRow[]) => Map<number, MatcherRowErrors>,
) {
  await render(
    <I18nextProvider i18n={i18n}>
      <Harness initial={initial} errorsOf={errorsOf} />
    </I18nextProvider>,
  );
}

describe("MatcherBuilder", () => {
  test("puts the server's error under the value of the row the pointer names", async () => {
    const rows = [matcherRow("alertname", "=", "Disk"), matcherRow("pod", "=~", "api-(")];
    const err = refusal({
      pointer: "/matchers/1/value",
      code: "invalid_regex",
      detail: REGEX_DETAIL,
    });
    await renderBuilder(rows, (current) => matcherErrors(err, sentRows(current)));
    const value = page.getByRole("textbox", { name: "Value of matcher 2" });
    await expect.element(value).toHaveAttribute("aria-invalid", "true");
    await expect
      .element(value)
      .toHaveAccessibleDescription(`The regular expression is not valid: ${REGEX_DETAIL}`);
    const second = page.getByTestId("matcher-row").nth(1);
    await expect
      .element(second.getByTestId("matcher-value-error"))
      .toHaveTextContent(`The regular expression is not valid: ${REGEX_DETAIL}`);
    expect(
      page.getByTestId("matcher-row").nth(0).getByTestId("matcher-value-error").elements(),
    ).toHaveLength(0);
    await expect
      .element(page.getByRole("textbox", { name: "Value of matcher 1" }))
      .toHaveAttribute("aria-invalid", "false");

    await i18n.changeLanguage("ru");
    await expect
      .element(second.getByTestId("matcher-value-error"))
      .toHaveTextContent(`Некорректное регулярное выражение: ${REGEX_DETAIL}`);
    await expect.element(page.getByRole("textbox", { name: "Значение матчера 2" })).toBeVisible();
  });

  test("a missing label name shows under the label", async () => {
    const rows = [matcherRow("", "=", "x")];
    await renderBuilder(
      rows,
      (current) => new Map([[current[0]?.key ?? -1, { label: { code: "required" } }]]),
    );
    await expect
      .element(page.getByRole("textbox", { name: "Label of matcher 1" }))
      .toHaveAccessibleDescription("Enter a label name.");
  });

  test("offers the four operators, focuses a new row and moves the focus when a row goes", async () => {
    await renderBuilder([matcherRow("alertname", "=", "Disk")]);
    const op = page.getByRole("combobox", { name: "Operator of matcher 1" });
    const select = op.element();
    expect(
      select instanceof HTMLSelectElement ? [...select.options].map((o) => o.value) : [],
    ).toEqual(["=", "!=", "=~", "!~"]);
    await userEvent.selectOptions(op, "=~");
    await expect
      .element(page.getByRole("textbox", { name: "Value of matcher 1" }))
      .toHaveAttribute("placeholder", "regular expression");

    await page.getByRole("button", { name: "Add matcher" }).click();
    await expect.element(page.getByRole("textbox", { name: "Label of matcher 2" })).toHaveFocus();
    await userEvent.keyboard("pod");
    await expect.element(page.getByRole("button", { name: "Remove matcher pod" })).toBeVisible();

    // Removing the first row moves the focus to the row after it; removing the last one to "Add matcher".
    await page.getByRole("button", { name: "Remove matcher alertname" }).click();
    await expect.element(page.getByRole("textbox", { name: "Label of matcher 1" })).toHaveFocus();
    await expect
      .element(page.getByRole("textbox", { name: "Label of matcher 1" }))
      .toHaveValue("pod");
    await page.getByRole("button", { name: "Remove matcher pod" }).click();
    await expect.element(page.getByRole("button", { name: "Add matcher" })).toHaveFocus();
    expect(page.getByTestId("matcher-row").elements()).toHaveLength(0);
  });
});
