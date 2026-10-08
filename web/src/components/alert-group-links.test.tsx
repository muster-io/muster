// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Links block in a real browser: a Link rule's link by the rule's name, even when it is named like a built-in
// link; the runbook_url, dashboard_url and generatorURL links by their names in the UI language, in English and
// Russian; only http(s) links, each opening in a new tab; nothing when no link is left.

import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import type { AlertGroupLink } from "../api/gen/model";
import i18n from "../i18n";
import { AlertGroupLinks } from "./alert-group-links";

const LINKS: AlertGroupLink[] = [
  {
    kind: "link_rule",
    name: "Dashboard",
    link_rule_id: "KR0000000000AA",
    url: "https://grafana.example.org/d/latency",
  },
  {
    kind: "link_rule",
    name: "Logs: api-1",
    link_rule_id: "KR0000000000BB",
    url: "https://logs.example.org/api-1",
  },
  { kind: "runbook", name: "Runbook", url: "https://wiki.example.org/latency" },
  { kind: "dashboard", name: "Dashboard", url: "https://grafana.example.org/d/annotated" },
  { kind: "source", name: "Source", url: "http://prometheus:9090/graph" },
  { kind: "runbook", name: "Runbook", url: "javascript:alert(1)" },
];

function renderLinks(links: AlertGroupLink[] | undefined) {
  return render(
    <I18nextProvider i18n={i18n}>
      <AlertGroupLinks links={links} />
    </I18nextProvider>,
  );
}

function names(screen: Awaited<ReturnType<typeof renderLinks>>): string[] {
  return screen
    .getByTestId("alert-group-link")
    .elements()
    .map((a) => a.querySelector("span")?.textContent ?? "");
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("AlertGroupLinks", () => {
  test("names a Link rule's link by the rule and a built-in link by its kind", async () => {
    const screen = await renderLinks(LINKS);
    await expect.element(screen.getByRole("navigation", { name: "Links" })).toBeVisible();
    expect(names(screen)).toEqual(["Dashboard", "Logs: api-1", "Runbook", "Dashboard", "Source"]);
    const links = screen.getByTestId("alert-group-link").elements();
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      "https://grafana.example.org/d/latency",
      "https://logs.example.org/api-1",
      "https://wiki.example.org/latency",
      "https://grafana.example.org/d/annotated",
      "http://prometheus:9090/graph",
    ]);
    for (const a of links) {
      expect(a.getAttribute("target")).toBe("_blank");
      expect(a.getAttribute("rel")).toBe("noopener noreferrer");
    }
  });

  test("translates the built-in links and keeps the rules' names in Russian", async () => {
    await i18n.changeLanguage("ru");
    const screen = await renderLinks(LINKS);
    await expect.element(screen.getByRole("navigation", { name: "Ссылки" })).toBeVisible();
    expect(names(screen)).toEqual(["Dashboard", "Logs: api-1", "Ранбук", "Дашборд", "Источник"]);
  });

  test("shows nothing without an http(s) link", async () => {
    const screen = await renderLinks([LINKS[5]!]);
    expect(screen.container.querySelector("nav")).toBeNull();
    const empty = await renderLinks(undefined);
    expect(empty.container.querySelector("nav")).toBeNull();
  });
});
