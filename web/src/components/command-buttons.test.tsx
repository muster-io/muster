// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Command buttons in a real browser, against a stubbed API: exactly the buttons of each allowed_commands set and
// none for a Viewer, the Takeover wording, the newer Alert Group's link in place of Unresolve, the refusal messages of
// a 409 (a race of Unresolve included, with its link) and of a 403, one request for a double press, the "…" menu of a
// list row, and the Russian texts.

import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import { getGetAlertGroupQueryKey } from "../api/gen/endpoints/alert-groups/alert-groups";
import type { AlertGroup, CommandName, Problem, Session } from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { bulkItemText, buttonCommands, refusalText } from "../lib/commands";
// The dialogs and the menu are laid out by their classes, as in the application.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { CommandButtons, CommandMenu, commandLabel } from "./command-buttons";

const ME = "US0000000000ME";
const ALICE = { id: "US0000000000AL", name: "Alice", deactivated: false };

function group(
  status: AlertGroup["status"],
  allowed: CommandName[],
  extra: Partial<AlertGroup> = {},
) {
  const g: AlertGroup = {
    id: "AG0000000000AA",
    number: 412,
    title: "Disk full",
    status,
    severity_level: "warning",
    urgent: false,
    route: { id: "RT0000000000OP", name: "ops" },
    integrations: [],
    started_at: "2026-10-07T09:00:00Z",
    last_changed_at: "2026-10-07T09:00:00Z",
    reopen_count: 0,
    firing_alert_count: 1,
    resolved_alert_count: 0,
    unclaimed: false,
    delivery_problem: false,
    allowed_commands: allowed,
    ...extra,
  };
  return g;
}

interface Answer {
  status: number;
  body: unknown;
}

let requests: { method: string; path: string; body: string }[] = [];
let answer: (path: string) => Answer | Promise<Answer>;

function json({ status, body }: Answer): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status < 300 ? "application/json" : "application/problem+json" },
  });
}

beforeEach(async () => {
  requests = [];
  answer = () => ({ status: 200, body: {} });
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    const method = (init?.method ?? "GET").toUpperCase();
    if (url.pathname === "/api/v1/routes/RT0000000000OP") {
      return json({
        status: 200,
        body: { id: "RT0000000000OP", policy: { snooze_durations_seconds: [3600, 14400, 86400] } },
      });
    }
    requests.push({
      method,
      path: url.pathname,
      body: typeof init?.body === "string" ? init.body : "",
    });
    return json(await answer(url.pathname));
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

const SESSION: SessionRead = {
  session: {
    state: "active",
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the buttons read only the user's id
    user: { id: ME, name: "Me" } as Session["user"],
    csrf_token: "csrf",
    expires_at: "2026-10-08T09:00:00Z",
    idle_expires_at: "2026-10-08T09:00:00Z",
    method: "local",
    permissions: ["routes:read"],
  },
  ended: false,
};

async function renderWith(node: ReactNode) {
  const rootRoute = createRootRoute({ component: () => node });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, SESSION);
  const screen = await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { screen, queryClient };
}

const bar = () => page.getByTestId("command-buttons");

/** The buttons of an Alert Group as the page reads it, so that a refusal's read of it reaches them. */
function LivePage({ initial }: { initial: AlertGroup }) {
  const query = useQuery({
    queryKey: getGetAlertGroupQueryKey(initial.id),
    queryFn: () => Promise.resolve(initial),
    initialData: initial,
    staleTime: Number.POSITIVE_INFINITY,
  });
  return <CommandButtons group={query.data} />;
}

/** The last Command sent; a refusal also reads the Alert Group again. */
function posted() {
  return requests.filter((r) => r.method === "POST").at(-1);
}

function ignore(): void {}

/** A promise and the function that resolves it. */
function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve: (value: T) => void = ignore;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function refused(code: string, extra: Partial<Problem> = {}): ApiError {
  return new ApiError(409, { code, ...extra }, undefined);
}

async function buttonTexts(): Promise<string[]> {
  await expect.element(bar()).toBeVisible();
  return [...bar().element().querySelectorAll("button")].map((b) => b.textContent ?? "");
}

function refusal(code: string, extra: Record<string, unknown> = {}): Answer {
  return {
    status: 409,
    body: {
      type: "https://x/problems/command-refused",
      title: "Refused",
      status: 409,
      code,
      ...extra,
    },
  };
}

describe("CommandButtons", () => {
  test("offers exactly the Commands of allowed_commands, in their order", async () => {
    const cases: [AlertGroup, string[]][] = [
      [
        group("firing", ["acknowledge", "resolve", "snooze", "add_note"]),
        ["Acknowledge", "Resolve", "Snooze"],
      ],
      [
        group("acknowledged", ["unacknowledge", "resolve", "snooze", "add_note"], {
          owner: { id: ME, name: "Me", deactivated: false },
        }),
        ["Unacknowledge", "Resolve", "Snooze"],
      ],
      [
        group("snoozed", ["acknowledge", "resolve", "snooze", "unsnooze", "add_note"]),
        ["Acknowledge", "Resolve", "Snooze", "Unsnooze"],
      ],
      [group("resolved", ["unresolve", "add_note"]), ["Unresolve"]],
    ];
    for (const [g, expected] of cases) {
      const { screen } = await renderWith(<CommandButtons group={g} />);
      expect(await buttonTexts(), g.status).toEqual(expected);
      await screen.unmount();
    }
  });

  test("a Viewer, with no allowed Commands, sees no buttons", async () => {
    await renderWith(
      <div data-testid="page">
        <CommandButtons group={group("firing", [])} />
      </div>,
    );
    await expect.element(page.getByTestId("page")).toBeInTheDocument();
    expect(page.getByTestId("command-buttons").query()).toBeNull();
    expect(document.querySelectorAll("button")).toHaveLength(0);
  });

  test("never offers a Command that allowed_commands leaves out, whatever the status", () => {
    expect(buttonCommands(group("resolved", ["add_note"]))).toEqual([]);
    expect(buttonCommands(group("firing", ["still_on_it", "add_note"]))).toEqual([]);
  });

  test("Acknowledge of another user's Alert Group reads Take over and is sent at once", async () => {
    const g = group("acknowledged", ["acknowledge", "unacknowledge", "resolve", "snooze"], {
      owner: ALICE,
    });
    answer = () => ({
      status: 200,
      body: {
        outcome: "done",
        alert_group: { ...g, owner: { id: ME, name: "Me", deactivated: false } },
      },
    });
    await renderWith(<CommandButtons group={g} />);
    const takeOver = page.getByRole("button", { name: "Take over from Alice" });
    await expect.element(takeOver).toBeVisible();
    await takeOver.click();
    await vi.waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]).toMatchObject({
      method: "POST",
      path: "/api/v1/alert-groups/AG0000000000AA/acknowledge",
    });
    expect(page.getByRole("dialog").query()).toBeNull();
    expect(commandLabel(i18n.t, "acknowledge", ALICE, ALICE.id)).toBe("Acknowledge");
    expect(commandLabel(i18n.t, "acknowledge", { ...ALICE, deactivated: true }, ME)).toBe(
      "Take over from Alice (deactivated)",
    );
  });

  test("a double press sends one request while the first is under way", async () => {
    const g = group("firing", ["acknowledge", "resolve", "snooze"]);
    const held = deferred<Answer>();
    const release = () => held.resolve({ status: 200, body: { outcome: "done", alert_group: g } });
    answer = () => held.promise;
    await renderWith(<CommandButtons group={g} />);
    const ack = page.getByRole("button", { name: "Acknowledge" });
    await ack.click();
    await expect.element(ack).toBeDisabled();
    await expect.element(page.getByRole("button", { name: "Resolve" })).toBeDisabled();
    await userEvent.click(ack.element(), { force: true });
    expect(requests).toHaveLength(1);
    release();
    await expect.element(ack).toBeEnabled();
    expect(requests).toHaveLength(1);
  });

  test("a newer open Alert Group is a link in place of Unresolve", async () => {
    await renderWith(
      <CommandButtons
        group={group("resolved", ["add_note"], {
          notices: [
            {
              kind: "newer_alert_group_exists",
              related_alert_group: { id: "AG0000000000BB", number: 415 },
            },
          ],
        })}
      />,
    );
    const link = page.getByRole("link", { name: "A newer Alert Group exists: #415" });
    await expect.element(link).toBeVisible();
    expect(link.element().getAttribute("href")).toBe("/alert-groups/AG0000000000BB");
    expect(page.getByRole("button", { name: "Unresolve" }).query()).toBeNull();
  });

  test("an Unresolve refused in a race shows the newer Alert Group with its link", async () => {
    answer = () =>
      refusal("newer_alert_group_exists", {
        related_alert_group: { id: "AG0000000000BB", number: 415 },
      });
    await renderWith(<CommandButtons group={group("resolved", ["unresolve", "add_note"])} />);
    await page.getByRole("button", { name: "Unresolve" }).click();
    const dialog = page.getByRole("dialog");
    await expect
      .element(dialog.getByText("Bring this Alert Group back as firing without an Owner?"))
      .toBeVisible();
    await dialog.getByRole("button", { name: "Unresolve" }).click();
    const message = dialog.getByTestId("command-refusal");
    await expect.element(message).toHaveTextContent("A newer open Alert Group #415 exists.");
    expect(message.getByRole("link", { name: "#415" }).element().getAttribute("href")).toBe(
      "/alert-groups/AG0000000000BB",
    );
    expect(posted()?.path).toBe("/api/v1/alert-groups/AG0000000000AA/unresolve");
  });

  test("a refusal stays in its dialog when the Alert Group, read again, offers no Command", async () => {
    const before = group("resolved", ["unresolve", "add_note"]);
    answer = (path) =>
      path.endsWith("/unresolve")
        ? refusal("resolved_automatically")
        : { status: 200, body: { ...before, allowed_commands: ["add_note"] } };
    await renderWith(<LivePage initial={before} />);
    await page.getByRole("button", { name: "Unresolve", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Unresolve #412" });
    await dialog.getByRole("button", { name: "Unresolve", exact: true }).click();
    await vi.waitFor(() => expect(requests.some((r) => r.method === "GET")).toBe(true));
    await expect
      .element(dialog.getByTestId("command-refusal"))
      .toHaveTextContent(
        "Muster resolved this Alert Group automatically; it reopens by itself when an alert returns.",
      );
    await expect.element(page.getByTestId("command-buttons")).toBeInTheDocument();
    expect(page.getByTestId("command-buttons").getByRole("button").elements()).toHaveLength(0);
  });

  test("shows the message of every refusal code and of a 403", () => {
    const cases: [ApiError, string][] = [
      [refused("already_resolved"), "This Alert Group is already resolved."],
      [refused("not_acknowledged"), "This Alert Group is not acknowledged."],
      [refused("not_snoozed"), "This Alert Group is not snoozed."],
      [refused("all_alerts_resolved"), "All alerts of this Alert Group have already resolved."],
      [
        refused("resolved_automatically"),
        "Muster resolved this Alert Group automatically; it reopens by itself when an alert returns.",
      ],
      [
        refused("newer_alert_group_exists", {
          related_alert_group: { id: "AG0000000000BB", number: 415 },
        }),
        "A newer open Alert Group #415 exists.",
      ],
      [refused("owner_must_be_user"), "A Service account cannot be an Owner."],
      [new ApiError(403, {}, undefined), "You are not permitted to do this."],
    ];
    for (const [err, text] of cases) {
      expect(refusalText(i18n.t, err)).toBe(text);
    }
  });

  test("a refused Resolve shows its message in the dialog, which stays open", async () => {
    answer = () => refusal("already_resolved");
    await renderWith(<CommandButtons group={group("firing", ["acknowledge", "resolve"])} />);
    await page.getByRole("button", { name: "Resolve" }).click();
    const dialog = page.getByRole("dialog", { name: "Resolve #412" });
    await dialog.getByLabelText("Add a note (optional)").fill("Rolled back the deploy.");
    await dialog.getByRole("button", { name: "Resolve" }).click();
    await expect
      .element(dialog.getByTestId("command-refusal"))
      .toHaveTextContent("This Alert Group is already resolved.");
    expect(JSON.parse(posted()?.body ?? "{}")).toEqual({ note: "Rolled back the deploy." });
  });

  test("Resolve without a Note sends an empty request and closes the dialog", async () => {
    const g = group("firing", ["acknowledge", "resolve"]);
    answer = () => ({
      status: 200,
      body: {
        outcome: "done",
        alert_group: { ...g, status: "resolved", allowed_commands: ["unresolve"] },
      },
    });
    await renderWith(<CommandButtons group={g} />);
    await page.getByRole("button", { name: "Resolve" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Resolve" }).click();
    await vi.waitFor(() => expect(page.getByRole("dialog").query()).toBeNull());
    expect(JSON.parse(posted()?.body ?? "")).toEqual({});
  });

  test("Snooze offers the Route's durations and sends the chosen end", async () => {
    const g = group("firing", ["snooze"]);
    answer = () => ({ status: 200, body: { outcome: "done", alert_group: g } });
    await renderWith(<CommandButtons group={g} />);
    await page.getByRole("button", { name: "Snooze" }).click();
    const dialog = page.getByRole("dialog", { name: "Snooze #412" });
    for (const name of ["1 h", "4 h", "24 h", "Until", "No end"]) {
      await expect.element(dialog.getByRole("radio", { name })).toBeInTheDocument();
    }
    await dialog.getByText("4 h", { exact: true }).click();
    const before = Date.now();
    await dialog.getByRole("button", { name: "Snooze" }).click();
    await vi.waitFor(() => expect(posted()?.path).toMatch(/\/snooze$/));
    const sent: unknown = JSON.parse(posted()?.body ?? "{}");
    const until = typeof sent === "object" && sent !== null && "until" in sent ? sent.until : "";
    const ahead = Date.parse(typeof until === "string" ? until : "") - before;
    expect(ahead).toBeGreaterThanOrEqual(4 * 3600 * 1000 - 1000);
    expect(ahead).toBeLessThan(4 * 3600 * 1000 + 60_000);
  });

  test("speaks Russian, with the Takeover", async () => {
    await i18n.changeLanguage("ru");
    await renderWith(
      <CommandButtons
        group={group("acknowledged", ["acknowledge", "unacknowledge", "resolve", "snooze"], {
          owner: ALICE,
        })}
      />,
    );
    expect(await buttonTexts()).toEqual([
      "Перехватить у владельца Alice",
      "Снять подтверждение",
      "Закрыть",
      "Отложить",
    ]);
  });
});

describe("CommandMenu", () => {
  test("a list row offers the same Commands in its menu", async () => {
    const g = group("firing", ["acknowledge", "resolve", "snooze", "add_note"]);
    answer = () => ({ status: 200, body: { outcome: "done", alert_group: g } });
    await renderWith(<CommandMenu group={g} />);
    await page.getByRole("button", { name: "Commands for #412" }).click();
    const items = page.getByRole("menuitem");
    await expect.element(items.first()).toBeVisible();
    expect(items.elements().map((e) => e.textContent)).toEqual([
      "Acknowledge",
      "Resolve",
      "Snooze",
    ]);
    await page.getByRole("menuitem", { name: "Acknowledge" }).click();
    await vi.waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]?.path).toBe("/api/v1/alert-groups/AG0000000000AA/acknowledge");
  });

  test("a refusal from the menu shows under it", async () => {
    answer = () => refusal("already_resolved");
    await renderWith(<CommandMenu group={group("firing", ["acknowledge"])} />);
    await page.getByRole("button", { name: "Commands for #412" }).click();
    await page.getByRole("menuitem", { name: "Acknowledge" }).click();
    await expect
      .element(page.getByTestId("command-refusal"))
      .toHaveTextContent("This Alert Group is already resolved.");
  });

  test("a Viewer's row has no menu", async () => {
    await renderWith(
      <div data-testid="row">
        <CommandMenu group={group("firing", [])} />
      </div>,
    );
    await expect.element(page.getByTestId("row")).toBeInTheDocument();
    expect(page.getByTestId("command-menu").query()).toBeNull();
  });
});

describe("bulkItemText", () => {
  test("names each Alert Group with its outcome", async () => {
    const t = i18n.t;
    expect(bulkItemText(t, { alert_group_id: "AG1", number: 7, outcome: "done" })).toBe("#7: done");
    expect(
      bulkItemText(
        t,
        { alert_group_id: "AG1", number: 7, outcome: "skipped", code: "owned_by_other" },
        "admin",
      ),
    ).toBe("#7: skipped: owned by admin");
    expect(
      bulkItemText(t, {
        alert_group_id: "AG1",
        number: 7,
        outcome: "refused",
        code: "already_resolved",
      }),
    ).toBe("#7: This Alert Group is already resolved.");
    expect(
      bulkItemText(t, {
        alert_group_id: "AGX",
        number: null,
        outcome: "failed",
        code: "not_found",
      }),
    ).toBe("AGX: not found");
    await i18n.changeLanguage("ru");
    expect(
      bulkItemText(
        t,
        { alert_group_id: "AG1", number: 7, outcome: "skipped", code: "owned_by_other" },
        "admin",
      ),
    ).toBe("#7: пропущена: владелец admin");
  });
});
