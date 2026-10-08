// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The template editor in a real browser: the Go-template highlighting, an error marked at its line and column with
// "Unknown function: {name}" (C-12.AC-2) or the parser's detail, positions counted in characters as the server counts
// them, and nothing the Content Security Policy would refuse — no style element and no style attribute.

import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { ProblemError } from "../api/gen/model";
import i18n from "../i18n";
import { TemplateEditor, nameAt, offsetOf, positionedErrorText, tokenize } from "./template-editor";

const ENV: ProblemError = {
  pointer: "/template",
  code: "unknown_function",
  detail: 'function "env" is not defined',
  line: 1,
  column: 4,
};

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("tokenize", () => {
  test("splits text, delimiters and the pieces of an action", () => {
    const tokens = tokenize(
      '{{ .Labels.pod }} on {{- range $i, $a := .Alerts }}{{ printf "%d" 5 }}',
    );
    expect(tokens.filter((t) => t.kind !== "text")).toEqual([
      { kind: "delimiter", text: "{{" },
      { kind: "field", text: ".Labels" },
      { kind: "field", text: ".pod" },
      { kind: "delimiter", text: "}}" },
      { kind: "delimiter", text: "{{-" },
      { kind: "keyword", text: "range" },
      { kind: "variable", text: "$i" },
      { kind: "punctuation", text: "," },
      { kind: "variable", text: "$a" },
      { kind: "punctuation", text: ":" },
      { kind: "punctuation", text: "=" },
      { kind: "field", text: ".Alerts" },
      { kind: "delimiter", text: "}}" },
      { kind: "delimiter", text: "{{" },
      { kind: "function", text: "printf" },
      { kind: "string", text: '"%d"' },
      { kind: "number", text: "5" },
      { kind: "delimiter", text: "}}" },
    ]);
  });

  test("keeps every character, in order, even in an action that is never closed", () => {
    for (const src of ['a {{ "}}" }} b', "{{/* c }} */}}x", "{{ .A -}}\n{{ .B", "🔥 {{ . }}"]) {
      expect(
        tokenize(src)
          .map((t) => t.text)
          .join(""),
      ).toBe(src);
    }
  });
});

describe("positions", () => {
  test("count lines and characters, not UTF-16 code units", () => {
    const src = "🔥🔥 x\n{{ env }}";
    expect(offsetOf(src, 1, 4)).toBe(5);
    expect(offsetOf(src, 2, 4)).toBe(src.indexOf("env"));
    expect(nameAt(src, 2, 4)).toBe("env");
    expect(nameAt(src, 9, 1)).toBe("");
  });

  test("an unknown function names itself from the template, a syntax error shows its detail", async () => {
    expect(positionedErrorText(i18n.t, ENV, '{{ env "HOME" }}')).toBe(
      "Line 1, column 4: Unknown function: env",
    );
    expect(
      positionedErrorText(
        i18n.t,
        { pointer: "/template", code: "template_syntax", detail: "unclosed action", line: 2 },
        "",
      ),
    ).toBe("Line 2: unclosed action");
    await i18n.changeLanguage("ru");
    expect(positionedErrorText(i18n.t, ENV, '{{ env "HOME" }}')).toBe(
      "Строка 1, столбец 4: Неизвестная функция: env",
    );
  });
});

function Editor({ initial, errors }: { initial: string; errors?: ProblemError[] }) {
  const [value, setValue] = useState(initial);
  return (
    <TemplateEditor
      id="editor"
      label="Root message"
      value={value}
      onChange={setValue}
      errors={errors}
    />
  );
}

async function renderEditor(initial: string, errors?: ProblemError[]) {
  return render(
    <I18nextProvider i18n={i18n}>
      <Editor initial={initial} errors={errors} />
    </I18nextProvider>,
  );
}

describe("TemplateEditor", () => {
  test("marks an error at its line and column with its text", async () => {
    await renderEditor('{{ env "HOME" }}', [ENV]);
    const editor = page.getByRole("textbox", { name: "Root message" });
    await expect.element(editor).toHaveAttribute("aria-invalid", "true");
    await expect
      .element(page.getByTestId("template-errors"))
      .toHaveTextContent("Line 1, column 4: Unknown function: env");
    await expect
      .element(editor)
      .toHaveAccessibleDescription("Line 1, column 4: Unknown function: env");
    const marked = document.querySelector('[data-error="true"]');
    expect(marked?.textContent).toBe("env");
    expect(document.querySelector('[data-error-line="true"]')?.textContent).toBe("1");
  });

  test("marks the error on a later line", async () => {
    const src = "line one\n{{ .Labels.pod }} {{ toJson . }}\n";
    await renderEditor(src, [{ ...ENV, line: 2, column: 22 }]);
    expect(document.querySelector('[data-error="true"]')?.textContent).toBe("toJson");
    expect(document.querySelector('[data-error-line="true"]')?.textContent).toBe("2");
    await expect
      .element(page.getByTestId("template-errors"))
      .toHaveTextContent("Line 2, column 22: Unknown function: toJson");
  });

  test("draws the typed text highlighted under the textarea, without style elements or attributes", async () => {
    const styles = document.querySelectorAll("style").length;
    await renderEditor("");
    const editor = page.getByRole("textbox", { name: "Root message" });
    await userEvent.fill(editor, "{{ .Labels.pod }} on {{ .Labels.cluster }}");
    await expect.element(editor).toHaveValue("{{ .Labels.pod }} on {{ .Labels.cluster }}");
    const layer = page.getByTestId("template-layer");
    await expect.element(layer).toHaveTextContent("{{ .Labels.pod }} on {{ .Labels.cluster }}");
    expect(layer.element().querySelectorAll(".text-primary").length).toBe(4);
    await expect.element(editor).toHaveAttribute("aria-invalid", "false");
    expect(document.querySelectorAll("style").length).toBe(styles);
    expect(page.getByTestId("template-editor").element().querySelectorAll("[style]").length).toBe(
      0,
    );
  });
});
