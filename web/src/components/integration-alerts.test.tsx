// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Alerts view of an Integration in a real browser, against a stubbed API: the columns, the reasons of resolved
// Alerts, the Static label warning, labels with markup and template syntax shown as text, the state tabs, and a
// Matcher the server refuses, which leaves the rows and the filters as they were.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { IntegrationAlert } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { IntegrationAlerts, orderedLabels, severityText } from "./integration-alerts";
import type { AlertSearch } from "./integration-alerts-search";

const GONE_TEXT =
  "Alertmanager no longer reports this alert — it resolved without notice, was silenced or inhibited in Alertmanager, or the Alertmanager routing changed";
const MARKUP = "<img src=x onerror=alert(1)>";
const TEMPLATE = "{{count}} $t(errors.generic) <b>bold</b>";

const FIRING: IntegrationAlert = {
  fingerprint: "a1",
  labels: { instance: "db-a", cluster: "a", alertname: "DiskFull" },
  state: "firing",
  resolve_reason: null,
  resolve_reason_text: null,
  starts_at: "2026-10-07T08:00:00Z",
  resolved_at: null,
  last_seen_at: "2026-10-07T09:00:00Z",
  alertmanager_groups: ['{}/{team="db"}:{alertname="DiskFull"}'],
  static_label_warnings: ["cluster"],
};
const GONE: IntegrationAlert = {
  fingerprint: "c1",
  labels: { alertname: "DiskFull", instance: "db-c", cluster: "b" },
  state: "resolved",
  resolve_reason: "gone",
  resolve_reason_text: GONE_TEXT,
  starts_at: "2026-10-07T08:00:00Z",
  resolved_at: "2026-10-07T08:30:00Z",
  last_seen_at: "2026-10-07T08:20:00Z",
  alertmanager_groups: [],
  static_label_warnings: [],
};
const MARKED: IntegrationAlert = {
  fingerprint: "m1",
  labels: { payload: MARKUP, note: TEMPLATE, alertname: "Markup" },
  state: "firing",
  starts_at: "2026-10-07T08:00:00Z",
  last_seen_at: "2026-10-07T09:00:00Z",
  alertmanager_groups: ["g1", "g2"],
};

let requests: URL[] = [];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status < 300 ? "application/json" : "application/problem+json" },
  });
}

beforeEach(async () => {
  requests = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    requests.push(url);
    const labels = url.searchParams.getAll("label");
    if (labels.some((l) => l.includes("["))) {
      return Promise.resolve(
        json(400, {
          type: "https://muster-io.github.io/muster/problems/validation-failed",
          title: "Validation failed",
          status: 400,
          errors: [
            {
              pointer: `/query/label/${labels.findIndex((l) => l.includes("["))}`,
              code: "invalid_regex",
              detail: "error parsing regexp: missing closing ]: `[`",
            },
          ],
        }),
      );
    }
    const state = url.searchParams.get("state");
    const items = [FIRING, GONE, MARKED].filter(
      (a) =>
        (state === null || a.state === state) &&
        labels.every((l) => l !== 'instance="db-a"' || a.labels.instance === "db-a"),
    );
    return Promise.resolve(json(200, { items, next_cursor: null }));
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

function Harness({ onSearch }: { onSearch: (patch: Partial<AlertSearch>) => void }) {
  const [search, setSearch] = useState<AlertSearch>({});
  return (
    <IntegrationAlerts
      integrationId="NT7M3QX9P2RTAB"
      search={search}
      onSearch={(patch) => {
        onSearch(patch);
        setSearch((prev) => ({ ...prev, ...patch }));
      }}
    />
  );
}

async function renderView() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = { session: null, ended: false };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const onSearch = vi.fn();
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Harness onSearch={onSearch} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { onSearch };
}

const rows = () => page.getByRole("table", { name: "Alerts" }).getByRole("row");

function rowOf(text: string) {
  return rows().filter({ hasText: text });
}

describe("orderedLabels", () => {
  test("puts alertname first, then sorts by name", () => {
    expect(orderedLabels({ b: "2", alertname: "X", a: "1" })).toEqual([
      ["alertname", "X"],
      ["a", "1"],
      ["b", "2"],
    ]);
  });
});

function withSeverity(
  severity_level: IntegrationAlert["severity_level"],
  severity_raw?: string | null,
): IntegrationAlert {
  return { ...FIRING, severity_level, severity_raw };
}

describe("severityText", () => {
  const alert = withSeverity;

  test("shows the Severity level, and the value as received when it has no mapping", async () => {
    // severity="none" is info, severity="P5" has no mapping: warning shown with P5, no severity label is info.
    expect(severityText(i18n.t, alert("info", null))).toBe("info");
    expect(severityText(i18n.t, alert("warning", "P5"))).toBe("warning (P5)");
    expect(severityText(i18n.t, alert("critical"))).toBe("critical");
    expect(severityText(i18n.t, alert(undefined))).toBeNull();
    await i18n.changeLanguage("ru");
    expect(severityText(i18n.t, alert("warning", "P5"))).toBe("warning (P5)");
  });
});

describe("IntegrationAlerts", () => {
  test("lists firing Alerts with labels, the Static label warning and the groups on expand", async () => {
    await renderView();
    const dbA = rowOf("instance=db-a");
    await expect.element(dbA.getByTestId("alert-state")).toHaveTextContent("Firing");
    const labels = dbA
      .getByTestId("alert-label")
      .elements()
      .map((e) => e.textContent);
    expect(labels).toEqual(["alertname=DiskFull", "cluster=a", "instance=db-a"]);
    await expect
      .element(dbA.getByTestId("static-label-warning"))
      .toHaveTextContent("Static label cluster not applied: the alert has its own value.");
    const groups = dbA.getByTestId("alert-groups");
    await expect.element(groups.getByText("1 group")).toBeVisible();
    await expect
      .element(groups.getByText('{}/{team="db"}:{alertname="DiskFull"}'))
      .not.toBeVisible();
    await groups.getByText("1 group").click();
    await expect.element(groups.getByText('{}/{team="db"}:{alertname="DiskFull"}')).toBeVisible();
    expect(requests[0]?.searchParams.get("state")).toBe("firing");
    expect(rows().filter({ hasText: "instance=db-c" }).elements()).toHaveLength(0);
  });

  test("shows labels with markup and template syntax as text", async () => {
    const alerted = vi.spyOn(window, "alert").mockImplementation(() => {});
    await renderView();
    const marked = rowOf("alertname=Markup");
    await expect.element(marked.getByText(`payload=${MARKUP}`, { exact: true })).toBeVisible();
    await expect.element(marked.getByText(`note=${TEMPLATE}`, { exact: true })).toBeVisible();
    expect(document.querySelector("table img, table b")).toBeNull();
    await expect.element(marked.getByText("2 groups")).toBeVisible();
    expect(alerted).not.toHaveBeenCalled();
  });

  test("shows the reason of a resolved Alert, its full text on a tap and on hover, and the tabs filter by state", async () => {
    const { onSearch } = await renderView();
    await page.getByRole("tab", { name: "Resolved" }).click();
    expect(onSearch).toHaveBeenLastCalledWith({ alerts_state: "resolved" });
    const state = rowOf("instance=db-c").getByTestId("alert-state");
    await expect.element(state).toHaveTextContent("Resolved: Gone");
    await expect.element(state).toHaveAttribute("title", GONE_TEXT);
    const reason = rowOf("instance=db-c").getByTestId("alert-reason");
    await expect.element(reason).not.toBeVisible();
    await state.click();
    await expect.element(reason).toBeVisible();
    await expect.element(reason).toHaveTextContent(GONE_TEXT);

    // The full reason is in the language of the page.
    const RU_ABSENT =
      "Alertmanager больше не присылает этот алерт — он закрылся без уведомления, был заглушён или подавлен в Alertmanager, либо изменилась маршрутизация Alertmanager";
    await i18n.changeLanguage("ru");
    // The table is named in Russian now; the Resolved tab has the one row.
    const ruState = page.getByTestId("alert-state");
    await expect.element(ruState).toHaveAttribute("title", RU_ABSENT);
    await expect.element(ruState).toHaveTextContent("Закрыт: пропал");
    await expect.element(page.getByTestId("alert-reason")).toHaveTextContent(RU_ABSENT);
    await i18n.changeLanguage("en");
    expect(requests.at(-1)?.searchParams.get("state")).toBe("resolved");
    await expect
      .element(page.getByRole("tab", { name: "Resolved" }))
      .toHaveAttribute("aria-selected", "true");

    // The arrow keys move between the tabs; All sends no state.
    await page.getByRole("tab", { name: "Resolved" }).click();
    await userEvent.keyboard("{ArrowRight}");
    await expect.element(page.getByRole("tab", { name: "All" })).toHaveFocus();
    expect(onSearch).toHaveBeenLastCalledWith({ alerts_state: "all" });
    await expect.element(rowOf("instance=db-a")).toBeVisible();
    expect(requests.at(-1)?.searchParams.has("state")).toBe(false);
  });

  test("applies a Matcher the server takes and keeps the rows when it refuses one", async () => {
    const { onSearch } = await renderView();
    const field = page.getByRole("textbox", { name: "Label filters" });
    await userEvent.type(field, 'instance="db-a"{Enter}');
    expect(onSearch).toHaveBeenLastCalledWith({ alerts_label: ['instance="db-a"'] });
    await expect.element(page.getByTestId("matcher-chip")).toHaveTextContent('instance="db-a"');
    await expect.element(rowOf("instance=db-a")).toBeVisible();
    await expect.poll(() => rowOf("alertname=Markup").elements().length).toBe(0);
    // The page the Matcher was checked with is the page the view shows: it is read once.
    const withMatcher = requests.filter((r) => r.searchParams.getAll("label").length === 1);
    expect(withMatcher).toHaveLength(1);

    const calls = onSearch.mock.calls.length;
    await field.fill('pod=~"["');
    await userEvent.keyboard("{Enter}");
    await expect
      .element(page.getByTestId("matcher-error"))
      .toHaveTextContent(
        "The regular expression is not valid: error parsing regexp: missing closing ]: `[`",
      );
    expect(requests.at(-1)?.searchParams.getAll("label")).toEqual(['instance="db-a"', 'pod=~"["']);
    expect(onSearch.mock.calls.length).toBe(calls);
    await expect.element(rowOf("instance=db-a")).toBeVisible();
    expect(page.getByTestId("matcher-chip").elements()).toHaveLength(1);
    await expect.element(field).toHaveValue('pod=~"["');
  });
});
