// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The button set editor in a real browser: the choice between the Organization's set and its own (or all buttons and a
// list), a tick box for Acknowledge, Resolve, each distinct Snooze duration of the Routes and both presets, the preview
// of a firing and an acknowledged Root message that follows each tick box, in English and in Russian, and the
// durations of the Paging profile when no Route has any.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Permission, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import {
  type ButtonSetValue,
  acknowledgeItem,
  presetItem,
  resolveItem,
  snoozeItem,
} from "../lib/button-sets";
import { ButtonSetEditor } from "./button-set-editor";

const HOUR = 3600;

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function route(durations: number[]) {
  return {
    id: "RT0000000000AA",
    name: "t",
    destination_ids: [] as string[],
    policy: { snooze_durations_seconds: durations },
  };
}

interface Reads {
  routes?: number[][];
  paging?: number[];
  organization?: Record<string, unknown>;
}

function mockReads({
  routes = [[HOUR, 4 * HOUR], [24 * HOUR]],
  paging = [900],
  organization,
}: Reads) {
  return vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/route-profiles")) {
      return Promise.resolve(
        json({ items: [{ id: "paging", policy: { snooze_durations_seconds: paging } }] }),
      );
    }
    if (url.startsWith("/api/v1/routes")) {
      return Promise.resolve(json({ items: routes.map(route) }));
    }
    if (url.startsWith("/api/v1/organization")) {
      return Promise.resolve(
        json({ workday_start: "09:00", week_start: "monday", ...organization }),
      );
    }
    return Promise.resolve(new Response("{}", { status: 404 }));
  });
}

function providers(children: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const permissions: Permission[] = ["routes:read"];
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the editor reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin", time_zone: "UTC" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-09T09:00:00Z",
      idle_expires_at: "2026-10-09T09:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
}

function Harness({
  kind,
  initial,
  onChange,
  error,
}: {
  kind: "destination" | "organization";
  initial: ButtonSetValue;
  onChange?: (next: ButtonSetValue) => void;
  error?: string;
}) {
  const [value, setValue] = useState(initial);
  return (
    <ButtonSetEditor
      id="buttons"
      kind={kind}
      value={value}
      destinationName="alerts"
      error={error}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

const FOLLOWS: ButtonSetValue = { follows: true, items: [] };

function preview(status: "firing" | "acknowledged") {
  return page.getByTestId(`button-set-preview-${status}`);
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("a Destination's button set", () => {
  test("follows the Organization by default and previews all its buttons", async () => {
    mockReads({});
    await render(providers(<Harness kind="destination" initial={FOLLOWS} />));
    await expect.element(page.getByText("Buttons", { exact: true }).first()).toBeVisible();
    await expect
      .element(
        page.getByText(
          "Which buttons the Root message shows. Hidden actions stay in Muster and in the Text commands of Bridges.",
        ),
      )
      .toBeVisible();
    await expect.element(page.getByLabelText("As in the Organization")).toBeChecked();
    await expect
      .element(page.getByTestId("button-set-source"))
      .toHaveAttribute("data-value", "organization");
    await expect.element(page.getByText("What alerts shows")).toBeVisible();
    await expect
      .element(preview("firing"))
      .toHaveTextContent(
        "AckResolveSnooze 1 hSnooze 4 hSnooze 24 hUntil tomorrow 09:00Until Monday 09:00",
      );
    await expect
      .element(preview("acknowledged"))
      .toHaveTextContent(
        "UnackResolveSnooze 1 hSnooze 4 hSnooze 24 hUntil tomorrow 09:00Until Monday 09:00",
      );
    // The tick boxes are for an own set only.
    await expect.element(page.getByTestId("button-set-item-acknowledge")).not.toBeInTheDocument();
  });

  test("shows what the Organization's own list shows while it follows", async () => {
    mockReads({
      organization: { buttons: { all: false, items: [resolveItem(), snoozeItem(HOUR)] } },
    });
    await render(providers(<Harness kind="destination" initial={FOLLOWS} />));
    await expect.element(preview("firing")).toHaveTextContent("ResolveSnooze 1 h");
  });

  test("offers a tick box for Acknowledge, Resolve, each distinct duration and both presets", async () => {
    mockReads({
      routes: [
        [HOUR, 4 * HOUR],
        [24 * HOUR, HOUR],
      ],
    });
    const changes: ButtonSetValue[] = [];
    await render(
      providers(<Harness kind="destination" initial={FOLLOWS} onChange={(v) => changes.push(v)} />),
    );
    await userEvent.click(page.getByLabelText("Own for this Destination"));
    // Its own set starts as everything the Destination shows now.
    for (const key of [
      "acknowledge",
      "resolve",
      "snooze:3600",
      "snooze:14400",
      "snooze:86400",
      "preset:tomorrow",
      "preset:week_start",
    ]) {
      await expect.element(page.getByTestId(`button-set-item-${key}`)).toBeChecked();
    }
    await expect.element(page.getByLabelText("Snooze 24 h")).toBeVisible();
    await expect.element(page.getByLabelText("Until tomorrow 09:00")).toBeVisible();
    expect(changes.at(-1)?.follows).toBe(false);
  });

  test("previews each tick box at once and sends the items in the order of a message", async () => {
    mockReads({});
    const changes: ButtonSetValue[] = [];
    await render(
      providers(<Harness kind="destination" initial={FOLLOWS} onChange={(v) => changes.push(v)} />),
    );
    await userEvent.click(page.getByLabelText("Own for this Destination"));
    await expect
      .element(page.getByTestId("button-set-source"))
      .toHaveAttribute("data-value", "own");
    for (const key of ["resolve", "snooze:3600", "snooze:14400", "preset:week_start"]) {
      await userEvent.click(page.getByTestId(`button-set-item-${key}`));
    }
    await expect.element(preview("firing")).toHaveTextContent("AckSnooze 24 hUntil tomorrow 09:00");
    await expect
      .element(preview("acknowledged"))
      .toHaveTextContent("UnackSnooze 24 hUntil tomorrow 09:00");
    expect(changes.at(-1)).toEqual({
      follows: false,
      items: [acknowledgeItem(), snoozeItem(24 * HOUR), presetItem("tomorrow")],
    });
    // Ticking a box again brings its button back.
    await userEvent.click(page.getByTestId("button-set-item-resolve"));
    await expect
      .element(preview("firing"))
      .toHaveTextContent("AckResolveSnooze 24 hUntil tomorrow 09:00");
  });

  test("says so when nothing is ticked", async () => {
    mockReads({ routes: [[HOUR]] });
    await render(providers(<Harness kind="destination" initial={{ follows: false, items: [] }} />));
    await expect.element(preview("firing")).toHaveTextContent("No buttons");
    await expect.element(preview("acknowledged")).toHaveTextContent("No buttons");
  });

  test("keeps a stored duration that no Route has as a tick box, which the preview does not show", async () => {
    mockReads({ routes: [[HOUR]] });
    const changes: ButtonSetValue[] = [];
    await render(
      providers(
        <Harness
          kind="destination"
          initial={{ follows: false, items: [acknowledgeItem(), snoozeItem(2 * HOUR)] }}
          onChange={(v) => changes.push(v)}
        />,
      ),
    );
    await expect.element(page.getByTestId("button-set-item-snooze:7200")).toBeChecked();
    await expect.element(preview("firing")).toHaveTextContent("Ack");
    await expect.element(preview("firing")).not.toHaveTextContent("Snooze 2 h");
    await userEvent.click(page.getByTestId("button-set-item-resolve"));
    expect(changes.at(-1)?.items).toEqual([acknowledgeItem(), resolveItem(), snoozeItem(2 * HOUR)]);
  });

  test("previews the durations of the Route that sends to the Destination, in the Route's order", async () => {
    mockReads({ routes: [[HOUR, 4 * HOUR], [24 * HOUR]] });
    vi.spyOn(window, "fetch").mockImplementation((input) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url.startsWith("/api/v1/route-profiles")) {
        return Promise.resolve(json({ items: [] }));
      }
      if (url.startsWith("/api/v1/routes")) {
        return Promise.resolve(
          json({
            items: [
              { ...route([HOUR, 4 * HOUR]), destination_ids: ["DS0000000000OT"] },
              { ...route([5400, 24 * HOUR]), destination_ids: ["DS0000000000AA"] },
            ],
          }),
        );
      }
      return Promise.resolve(json({ workday_start: "09:00", week_start: "monday" }));
    });
    await render(
      providers(
        <ButtonSetEditor
          id="buttons"
          kind="destination"
          value={FOLLOWS}
          destinationId="DS0000000000AA"
          onChange={() => {}}
        />,
      ),
    );
    await expect
      .element(preview("firing"))
      .toHaveTextContent(
        "AckResolveSnooze 90 minSnooze 24 hUntil tomorrow 09:00Until Monday 09:00",
      );
  });

  test("keeps a duration the set named as a tick box after it is unticked, so that it can be ticked again", async () => {
    mockReads({ routes: [[HOUR]] });
    await render(
      providers(
        <Harness kind="destination" initial={{ follows: false, items: [snoozeItem(2 * HOUR)] }} />,
      ),
    );
    await userEvent.click(page.getByTestId("button-set-item-snooze:7200"));
    await expect.element(page.getByTestId("button-set-item-snooze:7200")).not.toBeChecked();
    await userEvent.click(page.getByTestId("button-set-item-snooze:7200"));
    await expect.element(page.getByTestId("button-set-item-snooze:7200")).toBeChecked();
  });

  test("returns to the Organization's set and forgets the ticks", async () => {
    mockReads({});
    const changes: ButtonSetValue[] = [];
    await render(
      providers(
        <Harness
          kind="destination"
          initial={{ follows: false, items: [acknowledgeItem()] }}
          onChange={(v) => changes.push(v)}
        />,
      ),
    );
    await userEvent.click(page.getByLabelText("As in the Organization"));
    expect(changes.at(-1)).toEqual(FOLLOWS);
    await expect
      .element(preview("firing"))
      .toHaveTextContent(
        "AckResolveSnooze 1 hSnooze 4 hSnooze 24 hUntil tomorrow 09:00Until Monday 09:00",
      );
  });

  test("offers the Paging profile's durations when no Route has any", async () => {
    mockReads({ routes: [[], []], paging: [HOUR, 24 * HOUR] });
    await render(providers(<Harness kind="destination" initial={FOLLOWS} />));
    await expect
      .element(preview("firing"))
      .toHaveTextContent("AckResolveSnooze 1 hSnooze 24 hUntil tomorrow 09:00Until Monday 09:00");
  });

  test("names the presets after the Organization's start of the working day and first day of the week", async () => {
    mockReads({ organization: { workday_start: "08:30", week_start: "sunday" } });
    await render(providers(<Harness kind="destination" initial={FOLLOWS} />));
    await expect
      .element(preview("firing"))
      .toHaveTextContent(
        "AckResolveSnooze 1 hSnooze 4 hSnooze 24 hUntil tomorrow 08:30Until Sunday 08:30",
      );
  });

  test("shows the refusal of the set at the section", async () => {
    mockReads({});
    await render(providers(<Harness kind="destination" initial={FOLLOWS} error="out_of_range" />));
    await expect
      .element(page.getByTestId("button-set-error"))
      .toHaveTextContent("This value is out of range.");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    mockReads({});
    await render(providers(<Harness kind="destination" initial={FOLLOWS} />));
    await expect.element(page.getByText("Кнопки", { exact: true }).first()).toBeVisible();
    await expect.element(page.getByLabelText("Как в Организации")).toBeChecked();
    await expect.element(page.getByText("Так увидят в alerts")).toBeVisible();
    await expect
      .element(preview("firing"))
      .toHaveTextContent(
        "ПодтвердитьЗакрытьОтложить на 1 чОтложить на 4 чОтложить на 24 чДо завтра 09:00До понедельника 09:00",
      );
    await userEvent.click(page.getByLabelText("Свои для этого места доставки"));
    await userEvent.click(page.getByTestId("button-set-item-resolve"));
    await expect
      .element(preview("acknowledged"))
      .toHaveTextContent(
        "Снять подтверждениеОтложить на 1 чОтложить на 4 чОтложить на 24 чДо завтра 09:00До понедельника 09:00",
      );
  });

  test("links to the documentation of the button sets", async () => {
    mockReads({});
    await render(providers(<Harness kind="destination" initial={FOLLOWS} />));
    await expect
      .element(page.getByRole("link", { name: /^Learn more: Button sets/ }))
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/concepts/statuses-and-commands/#button-sets",
      );
  });
});

describe("the Organization's button set", () => {
  test("shows all buttons by default and lists them on request", async () => {
    mockReads({});
    const changes: ButtonSetValue[] = [];
    await render(
      providers(
        <Harness kind="organization" initial={FOLLOWS} onChange={(v) => changes.push(v)} />,
      ),
    );
    await expect.element(page.getByText("Buttons of Root messages")).toBeVisible();
    await expect.element(page.getByLabelText("All buttons")).toBeChecked();
    await expect.element(page.getByText("What a Root message shows")).toBeVisible();
    await userEvent.click(page.getByLabelText("Only the buttons I tick"));
    await userEvent.click(page.getByTestId("button-set-item-resolve"));
    await expect
      .element(preview("firing"))
      .toHaveTextContent(
        "AckSnooze 1 hSnooze 4 hSnooze 24 hUntil tomorrow 09:00Until Monday 09:00",
      );
    expect(changes.at(-1)?.follows).toBe(false);
    expect(changes.at(-1)?.items).not.toContainEqual(resolveItem());
    await userEvent.click(page.getByLabelText("All buttons"));
    expect(changes.at(-1)).toEqual(FOLLOWS);
  });

  test("previews every button again after All buttons is chosen over a saved list", async () => {
    mockReads({ organization: { buttons: { all: false, items: [resolveItem()] } } });
    await render(
      providers(
        <Harness kind="organization" initial={{ follows: false, items: [resolveItem()] }} />,
      ),
    );
    await expect.element(preview("firing")).toHaveTextContent("Resolve");
    await userEvent.click(page.getByLabelText("All buttons"));
    await expect
      .element(preview("firing"))
      .toHaveTextContent(
        "AckResolveSnooze 1 hSnooze 4 hSnooze 24 hUntil tomorrow 09:00Until Monday 09:00",
      );
    // Listing again starts from every button, not from the saved list.
    await userEvent.click(page.getByLabelText("Only the buttons I tick"));
    await expect.element(page.getByTestId("button-set-item-acknowledge")).toBeChecked();
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    mockReads({});
    await render(providers(<Harness kind="organization" initial={FOLLOWS} />));
    await expect.element(page.getByText("Кнопки корневых сообщений")).toBeVisible();
    await expect.element(page.getByLabelText("Все кнопки")).toBeChecked();
  });
});
