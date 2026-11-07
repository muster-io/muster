// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The compact layout of a Root message as each messenger lays it out, in a real browser (C-29.FR-4, FR-8, AC-7): the
// title with the status word, the fields, the texts, the key labels with "N values", the Alerts with "…and K more" and
// the resolved ones struck through, the people, the notices and the links in Mattermost, Slack and Telegram form; the
// Bridge's JSON beside the CommonMark body, or the note that a Route with a template of its own has no layout; values
// drawn as text, links opened only when they are http(s); English and Russian.

import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { MessageLayout } from "../api/gen/model";
import i18n from "../i18n";
import { messageLayout } from "../test/fixtures/message-layout";
import {
  BridgeLayoutPreview,
  type LayoutMarkup,
  MessageLayoutPreview,
  keyLabelTexts,
} from "./message-layout-preview";

/** Waits until an element's text holds a text: toHaveTextContent with a string compares all of it. */
const hasText = (el: { element: () => Element }, text: string) =>
  vi.waitFor(() => expect(el.element().textContent).toContain(text));

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

async function renderLayout(layout: MessageLayout, markup: LayoutMarkup) {
  await render(
    <I18nextProvider i18n={i18n}>
      <MessageLayoutPreview layout={layout} markup={markup} />
    </I18nextProvider>,
  );
}

describe("keyLabelTexts", () => {
  test("shows a label as name=value, and one whose Alerts differ as name: N values", () => {
    const layout = messageLayout({
      labels: [
        { key: "system", value: "kubernetes" },
        { key: "namespace", value: "2 values" },
        { key: "node", value: "5 значений" },
        { key: "env", value: "values" },
      ],
    });
    expect(keyLabelTexts(layout)).toEqual([
      "system=kubernetes",
      "namespace: 2 values",
      "node: 5 значений",
      "env=values",
    ]);
  });
});

describe.each<[LayoutMarkup, string]>([
  ["markdown", "layout-mattermost"],
  ["mrkdwn", "layout-slack"],
  ["html", "layout-telegram"],
])("the layout in %s", (markup, root) => {
  test("shows the title with the status, the fields, the texts, the labels, the Alerts, the people and the links", async () => {
    await renderLayout(messageLayout(), markup);
    const card = page.getByTestId(root);
    await hasText(
      card.getByTestId("layout-title"),
      markup === "markdown"
        ? "#1287 KubernetesContainerOOMKilled (opens in a new tab) — Firing"
        : "#1287 KubernetesContainerOOMKilled — Firing",
    );
    const fields = card.getByTestId("layout-fields");
    for (const text of [
      "Severity",
      "warning",
      "Route",
      "Infrastructure",
      "Teams",
      "payments",
      "Started",
      "03:12",
    ]) {
      await hasText(fields, text);
    }
    await expect.element(card.getByText("Container restarted for lack of memory")).toBeVisible();
    await expect
      .element(card.getByText("A container in checkout went over", { exact: false }))
      .toBeVisible();
    for (const label of ["system=kubernetes", "team=payments", "namespace=checkout"]) {
      await hasText(card.getByTestId("layout-labels"), label);
    }
    await hasText(
      card.getByTestId("layout-alerts"),
      "pod: checkout-api-7d9f4, checkout-api-5b2c1, checkout-worker-8e6a0",
    );
    expect(card.getByTestId("layout-alerts").element().querySelector("s")?.textContent).toBe(
      "checkout-worker-8e6a0",
    );
    await hasText(card, "Assignees: @ivan (devops), @petr (payments)");
    await expect
      .element(page.getByText("The buttons of the Destination are added", { exact: false }))
      .toBeVisible();
  });

  test("lists the Alerts not named as '…and K more', and leaves out what the layout lacks", async () => {
    await renderLayout(
      messageLayout({
        alerts: {
          label: "node",
          values: [{ value: "node-a1", resolved: false }],
          total: 12,
        },
        summary: "",
        description: "",
        labels: [],
        assignees: [],
        notices: ["Storm: 30 new Alert Groups a minute"],
      }),
      markup,
    );
    const card = page.getByTestId(root);
    await hasText(card.getByTestId("layout-alerts"), "node: node-a1 …and 11 more");
    await hasText(card, "Storm: 30 new Alert Groups a minute");
    expect(card.getByTestId("layout-labels").elements()).toHaveLength(0);
    expect(card.getByText("Assignees", { exact: false }).elements()).toHaveLength(0);
  });

  test("shows no Alerts line for one Alert, and no fields when there are none", async () => {
    await renderLayout(messageLayout({ alerts: null, fields: [] }), markup);
    const card = page.getByTestId(root);
    expect(card.getByTestId("layout-alerts").elements()).toHaveLength(0);
    expect(card.getByTestId("layout-fields").elements()).toHaveLength(0);
  });

  test("draws values as text and opens only http(s) links, in a new tab", async () => {
    await renderLayout(
      messageLayout({
        title: "<img src=x onerror=alert(1)>",
        summary: "**not bold** [x](https://evil.example)",
      }),
      markup,
    );
    expect(document.querySelector("img")).toBeNull();
    expect(document.querySelector("strong")).toBeNull();
    await expect.element(page.getByText("**not bold** [x](https://evil.example)")).toBeVisible();
    const open = page.getByRole("link", { name: "Open in Muster (opens in a new tab)" });
    await expect
      .element(open)
      .toHaveAttribute("href", "https://muster.example.org/alert-groups/AG1287");
    await expect.element(open).toHaveAttribute("rel", "noopener noreferrer");
    expect(page.getByRole("link", { name: "Source" }).elements()).toHaveLength(0);
    await hasText(page.getByTestId(root), "Source");
  });
});

describe("the layout of a Mattermost message", () => {
  test("shows the status word of the Route's language and marks the bar by status", async () => {
    await renderLayout(
      messageLayout({ status: { key: "acknowledged", word: "Acknowledged" } }),
      "markdown",
    );
    await hasText(
      page.getByTestId("layout-title"),
      "#1287 KubernetesContainerOOMKilled (opens in a new tab) — Acknowledged",
    );
    expect(page.getByTestId("layout-mattermost").element().className).toContain("border-l-primary");
  });
});

describe("the layout in Russian", () => {
  test("shows the Russian note about the buttons and the words of the layout as given", async () => {
    await i18n.changeLanguage("ru");
    await renderLayout(
      messageLayout({
        status: { key: "firing", word: "Горит" },
        fields: [{ name: "Важность", value: "warning" }],
        alerts: { label: "pod", values: [{ value: "a", resolved: false }], total: 4 },
      }),
      "html",
    );
    await hasText(page.getByTestId("layout-title"), "#1287 KubernetesContainerOOMKilled — Горит");
    await hasText(page.getByTestId("layout-fields"), "Важность: warning");
    await hasText(page.getByTestId("layout-alerts"), "pod: a …и ещё 3");
    await expect
      .element(page.getByText("Назначены: @ivan (devops), @petr (payments)"))
      .toBeVisible();
    await expect
      .element(page.getByText("Кнопки места доставки добавляются при отправке сообщения."))
      .toBeVisible();
  });
});

describe("the Bridge tab", () => {
  test("shows the layout a bridge receives as JSON beside the CommonMark body", async () => {
    const layout = messageLayout({
      owner: { name: "@olga", ref: "u1", team: { name: "payments" } },
    });
    await render(
      <I18nextProvider i18n={i18n}>
        <BridgeLayoutPreview layout={layout} body={"**#1287** KubernetesContainerOOMKilled"} />
      </I18nextProvider>,
    );
    const json = page.getByTestId("layout-json");
    await hasText(json, '"number": 1287');
    await hasText(json, '"title": "KubernetesContainerOOMKilled"');
    await hasText(json, '"key": "firing"');
    await hasText(json, '"ref": "u1"');
    expect(JSON.parse(json.element().textContent ?? "")).toEqual(layout);
    await hasText(page.getByTestId("layout-body"), "**#1287** KubernetesContainerOOMKilled");
  });

  test("a Route with a template of its own has no layout, and says so", async () => {
    await render(
      <I18nextProvider i18n={i18n}>
        <BridgeLayoutPreview layout={null} body="Own body" />
      </I18nextProvider>,
    );
    await hasText(
      page.getByTestId("layout-bridge-none"),
      "A Route with a template of its own sends a bridge the body only, without a layout.",
    );
    expect(page.getByTestId("layout-json").elements()).toHaveLength(0);
    await hasText(page.getByTestId("layout-body"), "Own body");
  });
});
