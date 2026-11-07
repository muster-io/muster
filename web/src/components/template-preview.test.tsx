// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The preview of a template in a real browser: Mattermost Markdown drawn as elements with raw HTML kept as text, links
// opened only when they are http(s), in a new tab without access to the page; Telegram HTML shown as its text; a failed
// template showing its error in place of the message; and "Shortened to fit".

import { QueryClient, QueryClientProvider, type UseQueryResult } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { TemplatePreviewResult } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { messageLayout } from "../test/fixtures/message-layout";
import {
  FormatSwitch,
  MarkdownText,
  MarkupTabs,
  type PreviewFormat,
  type RootPreviewContext,
  TemplatePreview,
  safeUrl,
  type TemplatePreviewState,
  useTemplatePreview,
} from "./template-preview";
import { DEFAULT_SAMPLE } from "./sample-picker";

/** Waits until an element's text holds a text: toHaveTextContent with a string compares all of it. */
const hasText = (el: { element: () => Element }, text: string) =>
  vi.waitFor(() => expect(el.element().textContent).toContain(text));

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

  test("shows Slack's mrkdwn as its text, with the note that a Mention in a template notifies nobody", async () => {
    await renderPreview("root_message", {
      valid: true,
      output: "*#1 HighErrorRate* <!here> <@U0FAKEALICE>",
      format: "mrkdwn",
      truncated: false,
      sample: "stored_snapshot",
      errors: [],
    });
    await expect
      .element(page.getByTestId("template-preview-output"))
      .toHaveTextContent("*#1 HighErrorRate* <!here> <@U0FAKEALICE>");
    expect(document.querySelector("strong")).toBeNull();
    await expect
      .element(page.getByTestId("template-preview-mrkdwn-note"))
      .toHaveTextContent(
        "Shown as the mrkdwn Slack receives. A Mention written in the template, such as <!here>, <!channel> or <@U…>, is sent so that it notifies nobody: only the Destination's Mention settings notify.",
      );
  });

  test("offers Slack's markup beside Mattermost's and Telegram's", async () => {
    const onChange = vi.fn();
    await render(
      <I18nextProvider i18n={i18n}>
        <FormatSwitch name="format" value="markdown" onChange={onChange} />
      </I18nextProvider>,
    );
    await expect.element(page.getByRole("radio", { name: "Mattermost" })).toBeChecked();
    await expect.element(page.getByRole("radio", { name: "Telegram" })).toBeVisible();
    await page.getByRole("radio", { name: "Slack" }).click();
    expect(onChange).toHaveBeenCalledWith("mrkdwn");
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

const CONTEXT: RootPreviewContext = {
  tabsId: "root-markup",
  markup: "markdown",
  rootCustom: false,
  lineCustom: false,
  labelsUnsaved: false,
};

function rootResult(overrides: Partial<TemplatePreviewResult> = {}): TemplatePreviewResult {
  return {
    valid: true,
    output: "**#1287** KubernetesContainerOOMKilled",
    format: "markdown",
    truncated: false,
    sample: "stored_snapshot",
    layout: messageLayout(),
    length: 812,
    messenger_limit: 16383,
    errors: [],
    ...overrides,
  };
}

async function renderRoot(context: Partial<RootPreviewContext>, result: TemplatePreviewResult) {
  return render(
    <I18nextProvider i18n={i18n}>
      <TemplatePreview
        id="root-preview"
        kind="root_message"
        preview={preview(result, "")}
        context={{ ...CONTEXT, ...context }}
      />
    </I18nextProvider>,
  );
}

describe("the preview of a Root message", () => {
  test("draws the layout in each tab's messenger form with the length against its limit", async () => {
    await renderRoot({ markup: "markdown" }, rootResult());
    await expect.element(page.getByTestId("layout-mattermost")).toBeVisible();
    await hasText(page.getByTestId("template-preview-length"), "812 of 16383 characters");
    expect(page.getByTestId("template-preview-output").elements()).toHaveLength(0);
  });

  test("Telegram shows 'of 4096 characters', and warns in red when the message is over the limit", async () => {
    await renderRoot(
      { markup: "html" },
      rootResult({ format: "html", length: 4300, messenger_limit: 4096 }),
    );
    await expect.element(page.getByTestId("layout-telegram")).toBeVisible();
    const length = page.getByTestId("template-preview-length");
    await hasText(length, "4300 of 4096 characters");
    expect(length.element().querySelector("p")?.className).toContain("text-destructive");
    await hasText(
      page.getByRole("alert"),
      "Longer than the limit: Muster shortens the message when it sends it.",
    );
  });

  test("Slack shows the section limit, and under it no warning", async () => {
    await renderRoot(
      { markup: "mrkdwn" },
      rootResult({ format: "mrkdwn", length: 900, messenger_limit: 3000 }),
    );
    await expect.element(page.getByTestId("layout-slack")).toBeVisible();
    await hasText(page.getByTestId("template-preview-length"), "900 of 3000 characters");
    expect(page.getByRole("alert").elements()).toHaveLength(0);
  });

  test("Bridge shows the layout JSON and a length without a limit", async () => {
    await renderRoot(
      { markup: "commonmark" },
      rootResult({ format: "commonmark", length: 640, messenger_limit: null }),
    );
    await hasText(page.getByTestId("layout-json"), '"number": 1287');
    await hasText(
      page.getByTestId("template-preview-length"),
      "640 characters; the bridge sets its own limit",
    );
  });

  test("a result laid out for another tab is not shown under this one: the tab waits for its own", async () => {
    await renderRoot({ markup: "commonmark" }, rootResult({ format: "markdown" }));
    await hasText(page.getByRole("status"), "Loading");
    expect(page.getByTestId("layout-mattermost").elements()).toHaveLength(0);
    expect(page.getByTestId("layout-json").elements()).toHaveLength(0);
    expect(page.getByTestId("template-preview-length").elements()).toHaveLength(0);
  });

  test("a Root message template of its own shows its text in place of the layout, and a bridge no layout", async () => {
    await renderRoot(
      { markup: "markdown", rootCustom: true },
      rootResult({ output: "Own body", layout: null }),
    );
    await hasText(page.getByTestId("template-preview-output"), "Own body");
    expect(page.getByTestId("message-layout-preview").elements()).toHaveLength(0);
  });

  test("a bridge gets no layout from a Route with an Alert line template, which the other tabs say", async () => {
    const view = await renderRoot(
      { markup: "commonmark", lineCustom: true },
      rootResult({ format: "commonmark" }),
    );
    await expect.element(page.getByTestId("layout-bridge-none")).toBeVisible();
    expect(page.getByTestId("layout-json").elements()).toHaveLength(0);
    await view.rerender(
      <I18nextProvider i18n={i18n}>
        <TemplatePreview
          id="root-preview"
          kind="root_message"
          preview={preview(rootResult(), "")}
          context={{ ...CONTEXT, lineCustom: true }}
        />
      </I18nextProvider>,
    );
    await hasText(
      page.getByTestId("template-preview-line-note"),
      "The Alert line template of this Route puts each Alert on a line of its own.",
    );
  });

  test("says that the preview shows the labels saved on the Route while the form's differ", async () => {
    await renderRoot({ labelsUnsaved: true }, rootResult());
    await hasText(
      page.getByTestId("template-preview-labels-unsaved"),
      "The preview shows the labels saved on the Route. Save to see the change.",
    );
  });

  test("has no layout to draw when the template failed: it shows the error", async () => {
    await renderRoot(
      {},
      { valid: false, output: null, errors: [{ pointer: "/template", code: "syntax" }] },
    );
    await expect.element(page.getByTestId("template-preview-error")).toBeVisible();
    expect(page.getByTestId("template-preview-length").elements()).toHaveLength(0);
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderRoot(
      { markup: "html" },
      rootResult({ format: "html", length: 100, messenger_limit: 4096 }),
    );
    await hasText(page.getByTestId("template-preview-length"), "100 из 4096 символов");
    await i18n.changeLanguage("en");
  });
});

function Tabs({ onChange }: { onChange: (f: PreviewFormat) => void }) {
  const [value, setValue] = useState<PreviewFormat>("markdown");
  return (
    <MarkupTabs
      id="t"
      panelId="p"
      value={value}
      onChange={(f) => {
        onChange(f);
        setValue(f);
      }}
    />
  );
}

describe("MarkupTabs", () => {
  test("offers Mattermost, Slack, Telegram and Bridge as tabs, and the arrow keys move between them", async () => {
    const onChange = vi.fn();
    await render(
      <I18nextProvider i18n={i18n}>
        <Tabs onChange={onChange} />
      </I18nextProvider>,
    );
    const tabs = page.getByRole("tab");
    expect(tabs.elements().map((t) => t.textContent)).toEqual([
      "Mattermost",
      "Slack",
      "Telegram",
      "Bridge",
    ]);
    await expect
      .element(page.getByRole("tab", { name: "Mattermost" }))
      .toHaveAttribute("aria-selected", "true");
    await page.getByRole("tab", { name: "Telegram" }).click();
    expect(onChange).toHaveBeenLastCalledWith("html");
    await expect
      .element(page.getByRole("tab", { name: "Telegram" }))
      .toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{ArrowRight}");
    expect(onChange).toHaveBeenLastCalledWith("commonmark");
    await expect.element(page.getByRole("tab", { name: "Bridge" })).toHaveFocus();
    await userEvent.keyboard("{ArrowRight}");
    expect(onChange).toHaveBeenLastCalledWith("markdown");
    await userEvent.keyboard("{ArrowLeft}");
    expect(onChange).toHaveBeenLastCalledWith("commonmark");
    // Only the selected tab is in the tab order.
    await expect
      .element(page.getByRole("tab", { name: "Slack" }))
      .toHaveAttribute("tabindex", "-1");
  });

  test("names the tabs in Russian", async () => {
    await i18n.changeLanguage("ru");
    await render(
      <I18nextProvider i18n={i18n}>
        <Tabs onChange={() => {}} />
      </I18nextProvider>,
    );
    await expect
      .element(page.getByRole("tablist", { name: "Разметка предпросмотра" }))
      .toBeVisible();
    await i18n.changeLanguage("en");
  });
});

function Live() {
  const [template, setTemplate] = useState("");
  const state = useTemplatePreview(
    { kind: "root_message", template, sample: DEFAULT_SAMPLE, format: "markdown" },
    true,
  );
  return (
    <>
      <button type="button" onClick={() => setTemplate("x")}>
        change
      </button>
      <TemplatePreview id="live" kind="root_message" preview={state} context={CONTEXT} />
    </>
  );
}

describe("a preview whose refresh failed", () => {
  let fail = false;

  beforeEach(() => {
    fail = false;
    vi.spyOn(window, "fetch").mockImplementation(() =>
      fail
        ? Promise.resolve(
            new Response(
              JSON.stringify({ title: "Unavailable", status: 503, code: "unavailable" }),
              {
                status: 503,
                headers: { "Content-Type": "application/problem+json" },
              },
            ),
          )
        : Promise.resolve(
            new Response(JSON.stringify(rootResult()), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          ),
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  test("keeps the previous result on screen, dimmed and marked as out of date, with the error", async () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(SESSION_QUERY_KEY, {
      session: null,
      ended: false,
    } satisfies SessionRead);
    await render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <Live />
        </QueryClientProvider>
      </I18nextProvider>,
    );
    await expect.element(page.getByTestId("layout-mattermost")).toBeVisible();
    fail = true;
    await page.getByRole("button", { name: "change" }).click();
    await expect.element(page.getByTestId("template-preview-refresh-error")).toBeVisible();
    const stale = page.getByTestId("template-preview-stale");
    await hasText(
      stale,
      "The last refresh failed. This is the previous result, which may be out of date.",
    );
    expect(stale.element().className).toContain("opacity-50");
    await expect.element(stale.getByTestId("layout-mattermost")).toBeVisible();
  });
});
