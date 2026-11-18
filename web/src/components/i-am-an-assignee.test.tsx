// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "I am an Assignee" of the Home page in a real browser, against a stubbed API (C-32.FR-18, C-32.AC-10): the Alert
// Groups where the signed-in User is an Assignee, asked with assignee=me, each with its status, its Assignees, its chain
// and step and the buttons "Acknowledge" and "Snooze…"; an acknowledged Alert Group leaves the list; the empty and
// failed reads; the Russian texts; and the section at 360 CSS pixels without scrolling sideways.

import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";

import type { AlertGroup } from "../api/gen/model";
import i18n from "../i18n";
import { alertGroup, alertGroupChain, assignee } from "../test/fixtures/alert-groups";
import { renderWithApp } from "../test/fixtures/escalation-chains";
import { reply } from "../test/fixtures/schedules";
// The layout is by classes, as in the application.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { IAmAnAssigneeSection } from "./i-am-an-assignee";

const OOM = "AG0000000000OM";

function oomKilled(extra: Partial<AlertGroup> = {}): AlertGroup {
  return alertGroup({
    id: OOM,
    number: 1287,
    title: "KubernetesContainerOOMKilled",
    allowed_commands: ["acknowledge", "resolve", "snooze"],
    assignees: [assignee("ivan", "devops"), assignee("petr", "payments")],
    escalation_chains: [alertGroupChain()],
    ...extra,
  });
}

let listed: AlertGroup[] = [];
let failList = false;
let requests: { method: string; url: URL }[] = [];

beforeEach(async () => {
  listed = [oomKilled()];
  failList = false;
  requests = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    const method = (init?.method ?? "GET").toUpperCase();
    requests.push({ method, url });
    if (method === "GET" && url.pathname === "/api/v1/alert-groups") {
      return Promise.resolve(
        failList
          ? reply(500, { type: "about:blank", title: "Boom", status: 500 })
          : reply(200, { items: listed, next_cursor: null }),
      );
    }
    if (method === "POST" && url.pathname === `/api/v1/alert-groups/${OOM}/acknowledge`) {
      // The Alert Group is acknowledged: it has no Assignees any more and leaves the list.
      listed = [];
      return Promise.resolve(
        reply(200, {
          outcome: "done",
          alert_group: oomKilled({ status: "acknowledged", assignees: [], allowed_commands: [] }),
        }),
      );
    }
    return Promise.resolve(reply(404, {}));
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

const PERMISSIONS = [
  "alert-groups:read",
  "alert-groups:acknowledge",
  "alert-groups:snooze",
] as const;

const rows = () => page.getByTestId("assignee-row");

describe("IAmAnAssigneeSection", () => {
  test("lists the Alert Groups the User is an Assignee of with their status, Assignees, chain and step", async () => {
    await renderWithApp(<IAmAnAssigneeSection />, [...PERMISSIONS]);
    await expect.element(page.getByRole("heading", { name: "I am an Assignee" })).toBeVisible();
    await expect
      .element(
        page.getByText(
          "Alert Groups that an Escalation chain Paged you about. They leave this list once acknowledged.",
        ),
      )
      .toBeVisible();
    await expect.element(rows()).toHaveLength(1);
    const row = rows().first();
    await expect
      .element(row.getByTestId("assignee-row-title"))
      .toHaveTextContent("#1287 KubernetesContainerOOMKilled");
    await expect.element(row.getByTestId("alert-group-status")).toHaveTextContent("Firing");
    await expect
      .element(row.getByTestId("assignee-row-assignees"))
      .toHaveTextContent("@ivan (devops), @petr (payments)");
    await expect
      .element(row.getByTestId("assignee-row-chain"))
      .toHaveTextContent("Chaindevops·step 1");
    await expect.element(row.getByRole("button", { name: "Acknowledge" })).toBeVisible();
    await expect.element(row.getByRole("button", { name: "Snooze…" })).toBeVisible();
    // Only the two Commands of the page's bar that matter to an Assignee.
    expect(row.getByRole("button", { name: "Resolve" }).elements()).toHaveLength(0);
    const list = requests.find((r) => r.url.pathname === "/api/v1/alert-groups");
    expect(list?.url.searchParams.get("assignee")).toBe("me");
  });

  test("an acknowledged Alert Group leaves the list", async () => {
    await renderWithApp(<IAmAnAssigneeSection />, [...PERMISSIONS]);
    await expect.element(rows()).toHaveLength(1);
    await rows().first().getByRole("button", { name: "Acknowledge" }).click();
    await expect.element(page.getByTestId("assignee-empty")).toBeVisible();
    await expect.element(rows()).toHaveLength(0);
    expect(
      requests.some(
        (r) => r.method === "POST" && r.url.pathname === `/api/v1/alert-groups/${OOM}/acknowledge`,
      ),
    ).toBe(true);
  });

  test("says so when nobody Paged the User, and when the list cannot be read", async () => {
    listed = [];
    await renderWithApp(<IAmAnAssigneeSection />, [...PERMISSIONS]);
    await expect
      .element(page.getByTestId("assignee-empty"))
      .toHaveTextContent("No Escalation chain has Paged you about an open Alert Group.");
  });

  test("shows the problem when the list cannot be read", async () => {
    failList = true;
    await renderWithApp(<IAmAnAssigneeSection />, [...PERMISSIONS]);
    await expect.element(page.getByRole("alert")).toBeVisible();
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderWithApp(<IAmAnAssigneeSection />, [...PERMISSIONS]);
    await expect.element(page.getByRole("heading", { name: "Назначено мне" })).toBeVisible();
    await expect
      .element(
        page.getByText("Группы, куда вас позвала цепочка. После подтверждения они уходят отсюда."),
      )
      .toBeVisible();
    const row = rows().first();
    await expect.element(row.getByTestId("alert-group-status")).toHaveTextContent("Горит");
    await expect
      .element(row.getByTestId("assignee-row-assignees"))
      .toHaveTextContent("@ivan (devops), @petr (payments)");
    await expect
      .element(row.getByTestId("assignee-row-chain"))
      .toHaveTextContent("Цепочкаdevops·шаг 1");
    await expect.element(row.getByRole("button", { name: "Подтвердить" })).toBeVisible();
    await expect.element(row.getByRole("button", { name: "Отложить…" })).toBeVisible();
  });

  test("fits 360 CSS pixels without scrolling sideways", async () => {
    listed = [
      oomKilled({
        title: "KubernetesContainerOOMKilledWithAVeryLongNameThatHasNoSpacesAtAllToBreakAt",
      }),
    ];
    await renderWithApp(
      <div style={{ width: "360px", boxSizing: "border-box", padding: "16px" }} id="phone">
        <IAmAnAssigneeSection />
      </div>,
      [...PERMISSIONS],
    );
    await expect.element(rows()).toHaveLength(1);
    const phone = page.getByTestId("i-am-an-assignee").element().closest("#phone");
    expect(phone).not.toBeNull();
    expect(phone?.scrollWidth).toBeLessThanOrEqual(phone?.clientWidth ?? 0);
  });
});
