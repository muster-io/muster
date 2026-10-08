// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The preview of a template in a real browser: Mattermost Markdown drawn as elements with raw HTML kept as text, links
// opened only when they are http(s), in a new tab without access to the page; Telegram HTML shown as its text; a failed
// template showing its error in place of the message; and "Shortened to fit".

import type { UseQueryResult } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { TemplatePreviewResult } from "../api/gen/model";
import i18n from "../i18n";
import {
  MarkdownText,
  TemplatePreview,
  safeUrl,
  type TemplatePreviewState,
} from "./template-preview";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("safeUrl", () => {
  test("keeps absolute http(s) links only", () => {
    expect(safeUrl("https://grafana.example.org/d/latency?var-ns=api")).toBe(
      "https://grafana.example.org/d/latency?var-ns=api",
    );
    expect(safeUrl("http://prometheus:9090/graph")).toBe("http://prometheus:9090/graph");
    expect(safeUrl("javascript:alert(1)")).toBeUndefined();
    expect(safeUrl("JaVaScRiPt:alert(1)")).toBeUndefined();
    expect(safeUrl("data:text/html,<b>x</b>")).toBeUndefined();
    expect(safeUrl("/relative")).toBeUndefined();
  });
});

describe("MarkdownText", () => {
  test("keeps underscores and asterisks inside words as text", async () => {
    await render(<MarkdownText text={"k8s_cluster_name: pod_a_b, 2*3*4 and _really_ *so*"} />);
    expect(document.querySelector("p")?.textContent).toBe(
      "k8s_cluster_name: pod_a_b, 2*3*4 and really so",
    );
    expect([...document.querySelectorAll("em")].map((e) => e.textContent)).toEqual([
      "really",
      "so",
    ]);
  });

  test("draws the Markdown of a message as elements and never as HTML", async () => {
    const alerted = vi.spyOn(window, "alert").mockImplementation(() => {});
    await render(
      <I18nextProvider i18n={i18n}>
        <MarkdownText
          text={[
            "🔴 [#7 HighLatency](https://muster.example.org/alert-groups/AG1)",
            "_Latency above 1 s_",
            "- **pod**: b \\_x\\_",
            "- ~~pod: a~~",
            "<img src=x onerror=alert(1)> [bad](javascript:alert(1)) `code`",
          ].join("\n")}
        />
      </I18nextProvider>,
    );
    const heading = page.getByRole("link", { name: "#7 HighLatency (opens in a new tab)" });
    await expect
      .element(heading)
      .toHaveAttribute("href", "https://muster.example.org/alert-groups/AG1");
    await expect.element(heading).toHaveAttribute("target", "_blank");
    await expect.element(heading).toHaveAttribute("rel", "noopener noreferrer");
    expect(document.querySelector("em")?.textContent).toBe("Latency above 1 s");
    expect(document.querySelector("strong")?.textContent).toBe("pod");
    expect(document.querySelector("s")?.textContent).toBe("pod: a");
    expect(document.querySelectorAll("li")[0]?.textContent).toBe("pod: b _x_");
    expect(document.querySelector("code")?.textContent).toBe("code");
    await expect
      .element(page.getByText("<img src=x onerror=alert(1)>", { exact: false }))
      .toBeVisible();
    expect(document.querySelector("img")).toBeNull();
    expect(page.getByRole("link", { name: "bad" }).query()).toBeNull();
    expect(alerted).not.toHaveBeenCalled();
    alerted.mockRestore();
  });
});

function preview(data: TemplatePreviewResult, template: string): TemplatePreviewState {
  const fields = { data, isError: false, error: null, isFetching: false };
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the panel reads only these fields of the query
  return { query: fields as UseQueryResult<TemplatePreviewResult>, template };
}

async function renderPreview(
  kind: "root_message" | "link_rule",
  result: TemplatePreviewResult,
  template = "",
) {
  return render(
    <I18nextProvider i18n={i18n}>
      <TemplatePreview id="preview" kind={kind} preview={preview(result, template)} />
    </I18nextProvider>,
  );
}

describe("TemplatePreview", () => {
  test("shows Telegram HTML as its text, and says when it was shortened", async () => {
    await renderPreview("root_message", {
      valid: true,
      output: "<b>#1 HighErrorRate</b>\n<i>Error rate above 5%</i>",
      format: "html",
      truncated: true,
      sample: "stored_snapshot",
      errors: [],
    });
    await expect
      .element(page.getByTestId("template-preview-output"))
      .toHaveTextContent("<b>#1 HighErrorRate</b> <i>Error rate above 5%</i>");
    expect(document.querySelector("b")).toBeNull();
    await expect.element(page.getByText("Shortened to fit")).toBeVisible();
  });

  test("shows the error of a failed template in place of the message", async () => {
    await renderPreview(
      "root_message",
      {
        valid: false,
        output: null,
        errors: [
          {
            pointer: "/template",
            code: "unknown_function",
            detail: 'function "env" is not defined',
            line: 1,
            column: 4,
          },
        ],
      },
      '{{ env "HOME" }}',
    );
    const failed = page.getByTestId("template-preview-error");
    await expect.element(failed.getByText("The template failed:")).toBeVisible();
    await expect
      .element(failed.getByText("Line 1, column 4: Unknown function: env", { exact: true }))
      .toBeVisible();
    expect(page.getByTestId("template-preview-output").query()).toBeNull();
  });

  test("shows the link of a Link rule, and the example it was rendered against", async () => {
    await renderPreview("link_rule", {
      valid: true,
      output: "https://grafana.example.org/d/latency?var-ns=api",
      format: null,
      sample: "example",
      errors: [],
    });
    const link = page.getByRole("link", {
      name: "https://grafana.example.org/d/latency?var-ns=api (opens in a new tab)",
    });
    await expect.element(link).toHaveAttribute("rel", "noopener noreferrer");
    await expect
      .element(page.getByText("Rendered against a built-in example", { exact: false }))
      .toBeVisible();
  });
});
