// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Labels in the message" of the Route editor in a real browser (C-29.FR-2, C-29.AC-7): the effective labels as removable
// chips with the hint and "Learn more"; removing or adding a label makes the list the Route's own; "+ label" offers the
// labels "From recent Alert Groups of the Route" with an example value, not the ones already chosen, and takes any other
// name typed; "Reset to default" goes back to the computed list, or says that it shows once saved; the read-only list,
// the empty states, a failed read of the suggestions, a new Route, and the Russian texts.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import {
  MESSAGE_LABELS_MAX,
  MessageLabelsEditor,
  type MessageLabelsValue,
  sameLabels,
} from "./message-labels-editor";

const DEFAULT = ["system", "team", "namespace"];

const SUGGESTIONS = {
  items: [
    { label: "cluster", example_value: "prod-eu", alert_group_count: 3 },
    { label: "pod", example_value: "checkout-api-7d9f4", alert_group_count: 3 },
    { label: "namespace", example_value: "checkout", alert_group_count: 3 },
  ],
};

let suggestions: () => Promise<Response>;

function json(body: unknown, status = 200): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
    }),
  );
}

/** Waits until an element's text holds a text: toHaveTextContent with a string compares all of it. */
const hasText = (el: { element: () => Element }, text: string) =>
  vi.waitFor(() => expect(el.element().textContent).toContain(text));

beforeEach(async () => {
  await i18n.changeLanguage("en");
  suggestions = () => json(SUGGESTIONS);
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.includes("/label-suggestions")) {
      return suggestions();
    }
    return json({ items: [] });
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

function Harness({
  initial = { own: false, names: DEFAULT },
  defaultNames = DEFAULT,
  routeId = "RT0000000000IN",
  readOnly = false,
  error,
  replaced = false,
}: {
  replaced?: boolean;
  initial?: MessageLabelsValue;
  defaultNames?: readonly string[] | null;
  routeId?: string;
  readOnly?: boolean;
  error?: string;
}) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <MessageLabelsEditor
        id="labels"
        value={value}
        onChange={setValue}
        defaultNames={defaultNames}
        routeId={routeId}
        readOnly={readOnly}
        error={error}
        replaced={replaced}
      />
      <output data-testid="sent">{JSON.stringify(value)}</output>
    </>
  );
}

async function renderEditor(props: Parameters<typeof Harness>[0] = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, {
    session: null,
    ended: false,
  } satisfies SessionRead);
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Harness {...props} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const chips = () => page.getByTestId("message-label");
const sent = () => page.getByTestId("sent");
const names = () =>
  chips()
    .elements()
    .map((c) => c.textContent?.trim());

describe("sameLabels", () => {
  test("compares the list only while it is the Route's own", () => {
    expect(sameLabels({ own: false, names: ["a"] }, { own: false, names: ["b"] })).toBe(true);
    expect(sameLabels({ own: false, names: ["a"] }, { own: true, names: ["a"] })).toBe(false);
    expect(sameLabels({ own: true, names: ["a", "b"] }, { own: true, names: ["a", "b"] })).toBe(
      true,
    );
    expect(sameLabels({ own: true, names: ["a", "b"] }, { own: true, names: ["b", "a"] })).toBe(
      false,
    );
  });
});

describe("Labels in the message", () => {
  test("shows the effective labels as chips, the hint and Learn more", async () => {
    await renderEditor();
    await expect
      .element(page.getByRole("heading", { name: "Labels in the message" }))
      .toBeVisible();
    expect(names()).toEqual(["system", "team", "namespace"]);
    await expect
      .element(
        page.getByText("Labels without a value in the Alert Group are not shown.", {
          exact: false,
        }),
      )
      .toBeVisible();
    await expect
      .element(page.getByText("By default: the Group key without", { exact: false }))
      .toBeVisible();
    expect(
      [...document.querySelectorAll('[data-testid="message-labels"] p code')].map(
        (c) => c.textContent,
      ),
    ).toEqual(["alertname", "namespace"]);
    const link = page.getByTestId("docs-link");
    await expect.element(link).toHaveAttribute("data-docs", "field.message_labels");
    await expect
      .element(link)
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/configure/messages-and-templates/#labels-in-the-message",
      );
    // The default list: no reset to offer.
    expect(page.getByRole("button", { name: "Reset to default" }).elements()).toHaveLength(0);
    await expect
      .element(sent())
      .toHaveTextContent('{"own":false,"names":["system","team","namespace"]}');
  });

  test("removing a label makes the list the Route's own, with the other labels in their order", async () => {
    await renderEditor();
    await page.getByRole("button", { name: "Remove team from the message" }).click();
    await expect.element(sent()).toHaveTextContent('{"own":true,"names":["system","namespace"]}');
    expect(names()).toEqual(["system", "namespace"]);
    await expect.element(page.getByRole("button", { name: "Reset to default" })).toBeVisible();
  });

  test("offers the labels of recent Alert Groups with an example value, without the chosen ones, and adds one", async () => {
    await renderEditor();
    const open = page.getByRole("button", { name: "+ label" });
    await expect.element(open).toHaveAttribute("aria-expanded", "false");
    await open.click();
    await expect.element(open).toHaveAttribute("aria-expanded", "true");
    await expect.element(page.getByText("From recent Alert Groups of the Route")).toBeVisible();
    const offered = page.getByTestId("message-labels-suggestion");
    await expect.element(offered.first()).toBeVisible();
    // `namespace` is already in the message.
    expect(offered.elements().map((e) => e.textContent?.trim())).toEqual([
      "cluster — e.g. prod-eu",
      "pod — e.g. checkout-api-7d9f4",
    ]);
    await page.getByRole("button", { name: "cluster — e.g. prod-eu" }).click();
    await expect
      .element(sent())
      .toHaveTextContent('{"own":true,"names":["system","team","namespace","cluster"]}');
    expect(names()).toEqual(["system", "team", "namespace", "cluster"]);
    expect(page.getByRole("button", { name: "cluster — e.g. prod-eu" }).elements()).toHaveLength(0);
  });

  test("adds a name typed, with Enter or the button, refuses a duplicate and an empty name", async () => {
    await renderEditor();
    await page.getByRole("button", { name: "+ label" }).click();
    const field = page.getByRole("textbox", { name: "Label name" });
    const add = page.getByRole("button", { name: "Add label" });
    await expect.element(add).toBeDisabled();
    await field.fill("  region ");
    await field.click();
    await userEvent.keyboard("{Enter}");
    await expect
      .element(sent())
      .toHaveTextContent('{"own":true,"names":["system","team","namespace","region"]}');
    await field.fill("team");
    await expect.element(page.getByText("This label is already in the message.")).toBeVisible();
    await expect.element(add).toBeDisabled();
    await field.fill("zone");
    await add.click();
    expect(names()).toEqual(["system", "team", "namespace", "region", "zone"]);
  });

  test("filters the suggestions by what is typed and says when none match", async () => {
    await renderEditor();
    await page.getByRole("button", { name: "+ label" }).click();
    await page.getByRole("textbox", { name: "Label name" }).fill("pod");
    const offered = page.getByTestId("message-labels-suggestion");
    await hasText(offered, "pod — e.g. checkout-api-7d9f4");
    expect(offered.elements()).toHaveLength(1);
    await page.getByRole("textbox", { name: "Label name" }).fill("zzz");
    await expect
      .element(page.getByText("No suggested label matches. Add the name you typed."))
      .toBeVisible();
  });

  test("says when the Route has no recent Alert Groups, and when every suggestion is in the message", async () => {
    suggestions = () => json({ items: [] });
    await renderEditor();
    await page.getByRole("button", { name: "+ label" }).click();
    await expect
      .element(
        page.getByText(
          "The Route has no recent Alert Groups to suggest labels from. Type a label name instead.",
        ),
      )
      .toBeVisible();
  });

  test("says when every suggested label is already chosen", async () => {
    suggestions = () => json({ items: [SUGGESTIONS.items[2]] });
    await renderEditor();
    await page.getByRole("button", { name: "+ label" }).click();
    await expect
      .element(page.getByText("All suggested labels are already in the message."))
      .toBeVisible();
  });

  test("shows why the suggestions failed and still takes a typed name", async () => {
    suggestions = () => json({ title: "Forbidden", status: 403, code: "forbidden" }, 403);
    await renderEditor();
    await page.getByRole("button", { name: "+ label" }).click();
    await expect.element(page.getByRole("alert")).toBeVisible();
    await page.getByRole("textbox", { name: "Label name" }).fill("zone");
    await page.getByRole("button", { name: "Add label" }).click();
    expect(names()).toEqual(["system", "team", "namespace", "zone"]);
  });

  test("Reset to default goes back to the computed list", async () => {
    await renderEditor({ initial: { own: true, names: ["cluster"] } });
    expect(names()).toEqual(["cluster"]);
    await page.getByRole("button", { name: "Reset to default" }).click();
    await expect
      .element(sent())
      .toHaveTextContent('{"own":false,"names":["system","team","namespace"]}');
    expect(names()).toEqual(["system", "team", "namespace"]);
    expect(page.getByRole("button", { name: "Reset to default" }).elements()).toHaveLength(0);
  });

  test("a removal moves the focus to the next chip, then the previous one, then + label, and says what was removed", async () => {
    await renderEditor({ initial: { own: true, names: ["a", "b", "c"] } });
    await page.getByRole("button", { name: "Remove b from the message" }).click();
    await expect
      .element(page.getByRole("button", { name: "Remove c from the message" }))
      .toHaveFocus();
    await expect.element(page.getByText("Removed b")).toBeInTheDocument();
    await page.getByRole("button", { name: "Remove c from the message" }).click();
    await expect
      .element(page.getByRole("button", { name: "Remove a from the message" }))
      .toHaveFocus();
    await page.getByRole("button", { name: "Remove a from the message" }).click();
    await expect.element(page.getByRole("button", { name: "+ label" })).toHaveFocus();
  });

  test("says that a Root message template of its own replaces the line of key labels", async () => {
    await renderEditor({ replaced: true });
    await expect
      .element(page.getByTestId("message-labels-replaced"))
      .toHaveTextContent(
        "This Route's Root message template is custom and replaces the line of key labels, so these labels are not shown.",
      );
    await i18n.changeLanguage("ru");
    await expect
      .element(page.getByTestId("message-labels-replaced"))
      .toHaveTextContent(
        "Шаблон корневого сообщения этого маршрута свой и заменяет строку ключевых меток, поэтому эти метки не показываются.",
      );
  });

  test("says nothing of the sort while the Root message is built in", async () => {
    await renderEditor();
    expect(page.getByTestId("message-labels-replaced").elements()).toHaveLength(0);
  });

  test("with no default list read, Reset to default says that it shows once saved, and + label waits", async () => {
    await renderEditor({ initial: { own: true, names: ["cluster"] }, defaultNames: null });
    await page.getByRole("button", { name: "Reset to default" }).click();
    expect(page.getByRole("button", { name: "+ label" }).elements()).toHaveLength(0);
    await expect.element(sent()).toHaveTextContent('{"own":false,"names":[]}');
    expect(chips().elements()).toHaveLength(0);
    await expect
      .element(page.getByText("The default list shows here once you save the Route."))
      .toBeVisible();
  });

  test("an empty list says so", async () => {
    await renderEditor({ initial: { own: true, names: [] } });
    await expect
      .element(page.getByText("No labels: the message has no line of key labels."))
      .toBeVisible();
  });

  test("stops at the most labels a Route lists", async () => {
    const many = Array.from({ length: MESSAGE_LABELS_MAX }, (_, i) => `label_${i}`);
    await renderEditor({ initial: { own: true, names: many } });
    await page.getByRole("button", { name: "+ label" }).click();
    await page.getByRole("textbox", { name: "Label name" }).fill("one_more");
    await expect.element(page.getByRole("button", { name: "Add label" })).toBeDisabled();
    await expect.element(page.getByText("A message lists at most 50 labels.")).toBeVisible();
  });

  test("shows the code of a refusal", async () => {
    await renderEditor({ error: "duplicate" });
    await expect.element(page.getByRole("alert")).toBeVisible();
  });

  test("a new Route shows no controls and says the default is computed on save", async () => {
    await render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={new QueryClient()}>
          <MessageLabelsEditor
            id="labels"
            value={{ own: false, names: [] }}
            onChange={() => {}}
            defaultNames={null}
          />
        </QueryClientProvider>
      </I18nextProvider>,
    );
    await hasText(
      page.getByTestId("message-labels-unknown"),
      "The default list is computed from the Group key once the Route is saved.",
    );
    expect(page.getByRole("button", { name: "+ label" }).elements()).toHaveLength(0);
  });

  test("the read-only list has no controls and says whose list it is", async () => {
    await renderEditor({ readOnly: true, initial: { own: true, names: ["cluster", "pod"] } });
    expect(names()).toEqual(["cluster", "pod"]);
    expect(page.getByRole("button").elements()).toHaveLength(0);
    await hasText(page.getByTestId("message-labels-source"), "This Route has a list of its own.");
  });

  test("the read-only default list names its source, and an empty one says so", async () => {
    await renderEditor({ readOnly: true, initial: { own: false, names: [] }, defaultNames: [] });
    await hasText(
      page.getByTestId("message-labels-source"),
      "The list follows the Group key and the Organization's settings.",
    );
    await expect
      .element(page.getByText("No labels: the message has no line of key labels."))
      .toBeVisible();
  });

  test("speaks Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderEditor();
    await expect.element(page.getByRole("heading", { name: "Метки в сообщении" })).toBeVisible();
    await expect
      .element(page.getByText("Метки без значения в группе не показываются.", { exact: false }))
      .toBeVisible();
    await page.getByRole("button", { name: "+ метка" }).click();
    await expect.element(page.getByText("Из недавних групп маршрута")).toBeVisible();
    await expect
      .element(page.getByRole("button", { name: "cluster — напр. prod-eu" }))
      .toBeVisible();
    await page.getByRole("button", { name: "Убрать метку team из сообщения" }).click();
    await expect.element(page.getByRole("button", { name: "Как по умолчанию" })).toBeVisible();
  });
});
