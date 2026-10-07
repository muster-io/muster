// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Matcher input in a real browser: the syntax check as typed, the chips, and the server's Problem for `label`
// under the field with the typed text kept.

import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { ApiError } from "../lib/api";
import {
  LabelMatchersInput,
  type MatcherSyntaxError,
  checkMatcher,
  matcherProblem,
} from "./label-matchers-input";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("checkMatcher", () => {
  const cases: [string, MatcherSyntaxError | null][] = [
    ['namespace="payments"', null],
    ['pod=~"api-.*"', null],
    ["pod!~api", null],
    ['  env != "prod"  ', null],
    ['"service name"="web"', null],
    ['msg="say \\"hi\\""', null],
    ["empty=", null],
    ['pod=~"["', null],
    ['\u0085a="b"\u0085', null],
    ['="x"', "name"],
    ['""="x"', "name"],
    ["1abc=x", "name"],
    ['"abc', "unclosed_name"],
    ["pod", "operator"],
    ["pod~x", "operator"],
    ['pod=~"[', "unclosed_value"],
    ['a="b" c', "after_value"],
    ['a=b"c', "quote_in_value"],
  ];
  test.each(cases)("%s", (text, expected) => {
    expect(checkMatcher(text)).toBe(expected);
  });
});

describe("matcherProblem", () => {
  test("finds the index and the code of a label error", () => {
    const err = new ApiError(
      400,
      {
        type: "validation-failed",
        errors: [{ pointer: "/query/label/2", code: "invalid_regex", detail: "missing ]" }],
      },
      undefined,
    );
    expect(matcherProblem(err)).toEqual({ index: 2, code: "invalid_regex", detail: "missing ]" });
  });

  test("ignores other errors", () => {
    expect(matcherProblem(new Error("x"))).toBeUndefined();
    const other = new ApiError(
      400,
      { errors: [{ pointer: "/query/limit", code: "out_of_range" }] },
      undefined,
    );
    expect(matcherProblem(other)).toBeUndefined();
  });
});

function Harness({
  initial = [],
  apply,
  problem,
}: {
  initial?: string[];
  apply: (next: string[]) => Promise<void>;
  problem?: unknown;
}) {
  const [value, setValue] = useState<string[]>(initial);
  return (
    <I18nextProvider i18n={i18n}>
      <LabelMatchersInput
        id="m"
        value={value}
        problem={problem}
        onChange={async (next) => {
          await apply(next);
          setValue(next);
        }}
      />
    </I18nextProvider>
  );
}

const field = () => page.getByRole("textbox", { name: "Label filters" });
const add = () => page.getByRole("button", { name: "Add matcher" });

describe("LabelMatchersInput", () => {
  test("checks the syntax as typed and applies a Matcher as a chip", async () => {
    const apply = vi.fn(() => Promise.resolve());
    await render(<Harness apply={apply} />);
    await userEvent.type(field(), "pod");
    await expect
      .element(page.getByTestId("matcher-error"))
      .toHaveTextContent("Follow the label name with =, !=, =~ or !~.");
    await expect.element(field()).toHaveAttribute("aria-invalid", "true");
    await expect.element(add()).toBeDisabled();
    await userEvent.type(field(), '=~"api-.*');
    await expect
      .element(page.getByTestId("matcher-error"))
      .toHaveTextContent("Close the quoted value with a quote.");
    await userEvent.type(field(), '"{Enter}');
    expect(apply).toHaveBeenCalledWith(['pod=~"api-.*"']);
    await expect.element(page.getByTestId("matcher-chip")).toHaveTextContent('pod=~"api-.*"');
    await expect.element(field()).toHaveValue("");
    await expect.element(page.getByTestId("matcher-error")).not.toBeInTheDocument();

    await userEvent.type(field(), 'pod=~"api-.*"');
    await expect
      .element(page.getByTestId("matcher-error"))
      .toHaveTextContent("This matcher is already applied.");
    await userEvent.clear(field());
    await page.getByRole("button", { name: 'Remove matcher pod=~"api-.*"' }).click();
    expect(apply).toHaveBeenLastCalledWith([]);
    await expect.element(page.getByTestId("matcher-chip")).not.toBeInTheDocument();

    // Template syntax in a Matcher stays literal in the texts it reaches.
    const template = 'note="{{count}} $t(errors.generic) <b>x</b>"';
    await field().fill(template);
    await add().click();
    await expect
      .element(page.getByRole("button", { name: `Remove matcher ${template}`, exact: true }))
      .toBeInTheDocument();
    expect(document.querySelector("b")).toBeNull();
  });

  test("shows the server's error under the field and keeps the text and the applied filters", async () => {
    const apply = vi.fn((next: string[]) =>
      next.length > 1
        ? Promise.reject(
            new ApiError(
              400,
              {
                type: "validation-failed",
                errors: [
                  {
                    pointer: "/query/label/1",
                    code: "invalid_regex",
                    detail: "error parsing regexp: missing closing ]: `[)$`",
                  },
                ],
              },
              undefined,
            ),
          )
        : Promise.resolve(),
    );
    await render(<Harness initial={['env="prod"']} apply={apply} />);
    await field().fill('pod=~"["');
    await add().click();
    await expect
      .element(page.getByTestId("matcher-error"))
      .toHaveTextContent(
        "The regular expression is not valid: error parsing regexp: missing closing ]: `[)$`",
      );
    await expect.element(field()).toHaveValue('pod=~"["');
    await expect.element(field()).toHaveAttribute("aria-invalid", "true");
    expect(page.getByTestId("matcher-chip").elements()).toHaveLength(1);
    await expect.element(page.getByTestId("matcher-chip")).toHaveTextContent('env="prod"');

    // Typing again clears the server's error.
    await userEvent.type(field(), "x");
    await expect
      .element(page.getByTestId("matcher-error"))
      .toHaveTextContent("Nothing may follow the quoted value.");
  });

  test("names the applied Matcher that the list's error is about", async () => {
    const problem = new ApiError(
      400,
      { errors: [{ pointer: "/query/label/0", code: "invalid_format" }] },
      undefined,
    );
    await render(<Harness initial={["a=b"]} apply={() => Promise.resolve()} problem={problem} />);
    await expect
      .element(page.getByTestId("matcher-error"))
      .toHaveTextContent(
        'Matcher a=b: A label filter is one Alertmanager matcher such as namespace="payments" or pod=~"api-.*".',
      );
  });
});
