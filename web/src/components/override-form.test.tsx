// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Override form in a real browser (C-27.FR-3, FR-6, C-27.AC-2): "I give my Shift to…" on olga's segment Wednesday
// 11:00–19:00 of the row "Result" with petr as the person who takes it saves an Override of exactly that segment;
// "I take the Shift of…" makes the caller the person who takes it; from and to cannot leave the segment; a refusal of
// the server — `not_in_team`, `not_pageable` — shows its message in the form; changing an Override that has begun
// leaves only its end — in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { OnCallSegment } from "../api/gen/model";
import i18n from "../i18n";
import {
  OLGA,
  PETR,
  SERGEY,
  problem,
  reply,
  scheduleOverride,
  segment,
  hasText,
} from "../test/fixtures/schedules";
import { OverrideEditForm, OverrideForm } from "./override-form";

const ID = "SC0000000000MA";
const ZONE = "Europe/Berlin";
const NOW = new Date("2026-11-16T09:30:00Z");
// olga's segment of Wednesday 11:00–19:00 Berlin time.
const WEDNESDAY = segment("2026-11-18T10:00:00Z", "2026-11-18T18:00:00Z", OLGA);
const GAP = segment("2026-11-23T09:00:00Z", "2026-11-23T10:00:00Z", null);

interface Call {
  method: string;
  url: string;
  ifMatch: string | null;
  body: Record<string, unknown>;
}

let calls: Call[];
let answer: (call: Call) => Response;

beforeEach(async () => {
  await i18n.changeLanguage("en");
  calls = [];
  answer = () => reply(201, scheduleOverride());
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const raw = init?.body;
    const parsed: unknown = typeof raw === "string" ? JSON.parse(raw) : {};
    const call: Call = {
      method: init?.method ?? "GET",
      url,
      ifMatch: new Headers(init?.headers).get("if-match"),
      body: typeof parsed === "object" && parsed !== null ? { ...parsed } : {},
    };
    calls.push(call);
    return Promise.resolve(answer(call));
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

const ME = { id: OLGA.id, name: "olga", login: "olga", deactivated: false };

async function renderForm(picked: OnCallSegment | null, saved: () => void = () => undefined) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <OverrideForm
          scheduleId={ID}
          zone={ZONE}
          now={NOW}
          me={ME}
          people={[PETR, SERGEY, OLGA]}
          segment={picked}
          onCancel={() => undefined}
          onSaved={saved}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const HINT =
  "Choose a segment in the row 'Result' and the person who takes it; from 11:00 to 19:00 they are On-call on top of every Rotation layer.";

describe("Add Override", () => {
  test("asks for a segment first, and cannot be saved without one", async () => {
    await renderForm(null);
    await hasText(
      page.getByTestId("override-hint"),
      "Choose a segment in the row 'Result' and the person who takes it.",
    );
    await expect.element(page.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  test("I give my Shift to… on olga's segment with petr saves an Override of exactly that segment", async () => {
    const saved = vi.fn();
    await renderForm(WEDNESDAY, saved);
    await userEvent.click(page.getByRole("radio", { name: "I give my Shift to…" }));
    await expect.element(page.getByTestId("override-hint")).toHaveTextContent(HINT);
    await userEvent.selectOptions(page.getByLabelText("Who takes it"), "@petr");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({
      method: "POST",
      url: `/api/v1/schedules/${ID}/overrides`,
      body: {
        user_id: PETR.id,
        replaces_user_id: OLGA.id,
        start: "2026-11-18T10:00:00.000Z",
        end: "2026-11-18T18:00:00.000Z",
      },
    });
    await vi.waitFor(() => expect(saved).toHaveBeenCalled());
  });

  test("does not offer the person whose Shift it is as the one who takes it", async () => {
    await renderForm(WEDNESDAY);
    await userEvent.click(page.getByRole("radio", { name: "I give my Shift to…" }));
    const options = [...page.getByLabelText("Who takes it").element().querySelectorAll("option")];
    expect(options.map((o) => o.textContent)).toEqual(["Choose a person…", "@petr", "@sergey"]);
  });

  test("I take the Shift of… makes the caller the person who takes it", async () => {
    await renderForm(segment("2026-11-18T10:00:00Z", "2026-11-18T18:00:00Z", PETR));
    await hasText(page.getByTestId("override-taker"), "you, @olga");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.body).toMatchObject({ user_id: OLGA.id, replaces_user_id: PETR.id });
  });

  test("takes a gap, which has no person whose Shift it takes", async () => {
    await renderForm(GAP);
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.body).toMatchObject({ user_id: OLGA.id, replaces_user_id: null });
  });

  test("starts a segment that has begun at now, not in the past", async () => {
    await renderForm(segment("2026-11-16T00:00:00Z", "2026-11-17T09:00:00Z", PETR));
    await expect.element(page.getByLabelText("From")).toHaveValue("2026-11-16T10:30");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.body.start).toBe(NOW.toISOString());
  });

  test("sends a part of the segment when from and to are narrowed, and never more", async () => {
    await renderForm(WEDNESDAY);
    await userEvent.fill(page.getByLabelText("From"), "2026-11-18T12:00");
    await userEvent.fill(page.getByLabelText("To"), "2026-11-18T17:00");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.body).toMatchObject({
      start: "2026-11-18T11:00:00.000Z",
      end: "2026-11-18T16:00:00.000Z",
    });
  });

  test("cannot leave the segment: from before it, to after it, or to before from", async () => {
    await renderForm(WEDNESDAY);
    for (const [field, value] of [
      ["From", "2026-11-18T10:00"],
      ["To", "2026-11-18T20:00"],
    ] as const) {
      await userEvent.fill(page.getByLabelText(field), value);
      await hasText(
        page.getByTestId("override-outside"),
        "From and to must stay within the segment.",
      );
      await expect.element(page.getByRole("button", { name: "Save" })).toBeDisabled();
      await userEvent.fill(
        page.getByLabelText(field),
        field === "From" ? "2026-11-18T11:00" : "2026-11-18T19:00",
      );
      await expect.element(page.getByTestId("override-outside")).not.toBeInTheDocument();
    }
    await userEvent.fill(page.getByLabelText("From"), "2026-11-18T15:00");
    await userEvent.fill(page.getByLabelText("To"), "2026-11-18T14:00");
    await expect.element(page.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(calls).toHaveLength(0);
  });

  test("starts the fields again when another segment is picked, and keeps the mode", async () => {
    const queryClient = new QueryClient();
    const view = (picked: OnCallSegment) => (
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <OverrideForm
            scheduleId={ID}
            zone={ZONE}
            now={NOW}
            me={ME}
            people={[PETR, SERGEY, OLGA]}
            segment={picked}
            onCancel={() => undefined}
            onSaved={() => undefined}
          />
        </QueryClientProvider>
      </I18nextProvider>
    );
    const screen = await render(view(WEDNESDAY));
    await userEvent.click(page.getByRole("radio", { name: "I give my Shift to…" }));
    await screen.rerender(view(GAP));
    await expect.element(page.getByLabelText("From")).toHaveValue("2026-11-23T10:00");
    await expect.element(page.getByRole("radio", { name: "I give my Shift to…" })).toBeChecked();
  });
});

describe("a refusal of the server", () => {
  test("not_in_team shows its message in the form", async () => {
    answer = () => reply(409, problem(409, { code: "not_in_team" }));
    await renderForm(GAP);
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect
      .element(page.getByText("You are not in this Team.", { exact: false }))
      .toBeVisible();
  });

  test("not_pageable shows why at the person who takes the Shift", async () => {
    answer = () =>
      reply(
        422,
        problem(422, {
          errors: [{ pointer: "/user_id", code: "not_pageable", reason: "disabled" }],
        }),
      );
    await renderForm(WEDNESDAY);
    await userEvent.click(page.getByRole("radio", { name: "I give my Shift to…" }));
    await userEvent.selectOptions(page.getByLabelText("Who takes it"), "@sergey");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect.element(page.getByText("A disabled User cannot be On-call.")).toBeVisible();
  });
});

describe("a refusal in the mode I take the Shift of…", () => {
  test("shows a refusal of the person who takes it, who is the caller, as a whole", async () => {
    answer = () =>
      reply(
        422,
        problem(422, {
          errors: [{ pointer: "/user_id", code: "not_pageable", reason: "not_in_team" }],
        }),
      );
    await renderForm(GAP);
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect.element(page.getByText("This person is not in the Team.")).toBeVisible();
  });

  test("forgets a refusal when the mode changes", async () => {
    answer = () => reply(409, problem(409, { code: "not_in_team" }));
    await renderForm(GAP);
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect
      .element(page.getByText("You are not in this Team.", { exact: false }))
      .toBeVisible();
    await userEvent.click(page.getByRole("radio", { name: "I give my Shift to…" }));
    await expect
      .element(page.getByText("You are not in this Team.", { exact: false }))
      .not.toBeInTheDocument();
  });
});

describe("the segment that has begun", () => {
  test("keeps its fields when the clock moves on a minute, and still sends now as the start", async () => {
    const queryClient = new QueryClient();
    const begun = segment("2026-11-16T00:00:00Z", "2026-11-17T09:00:00Z", PETR);
    const view = (at: Date) => (
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <OverrideForm
            scheduleId={ID}
            zone={ZONE}
            now={at}
            me={ME}
            people={[PETR, SERGEY, OLGA]}
            segment={begun}
            onCancel={() => undefined}
            onSaved={() => undefined}
          />
        </QueryClientProvider>
      </I18nextProvider>
    );
    const screen = await render(view(new Date("2026-11-16T09:30:40Z")));
    await expect.element(page.getByLabelText("From")).toHaveValue("2026-11-16T10:30");
    await screen.rerender(view(new Date("2026-11-16T09:31:40Z")));
    await expect.element(page.getByTestId("override-outside")).not.toBeInTheDocument();
    await expect.element(page.getByRole("button", { name: "Save" })).toBeEnabled();
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.body.start).toBe("2026-11-16T09:30:40.000Z");
  });
});

describe("in Russian", () => {
  test("reads the modes, the hint and the fields in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderForm(WEDNESDAY);
    await expect.element(page.getByRole("radio", { name: "Возьму смену за…" })).toBeVisible();
    await userEvent.click(page.getByRole("radio", { name: "Отдам смену…" }));
    await expect
      .element(page.getByTestId("override-hint"))
      .toHaveTextContent(
        "Выберите отрезок в строке «Итог» и того, кто его берёт; с 11:00 до 19:00 он дежурит поверх всех слоёв.",
      );
    await expect.element(page.getByLabelText("Кто берёт")).toBeVisible();
  });
});

async function renderEdit(begun: boolean) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const override = scheduleOverride(
    begun ? { start: "2026-11-16T08:00:00Z", end: "2026-11-16T18:00:00Z" } : {},
  );
  answer = () => reply(200, { ...override, etag: '"2"' });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <OverrideEditForm
          scheduleId={ID}
          zone={ZONE}
          now={NOW}
          override={override}
          people={[PETR, SERGEY, OLGA]}
          onCancel={() => undefined}
          onSaved={() => undefined}
          onReload={() => undefined}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return override;
}

describe("changing an Override", () => {
  test("leaves only the end to change once it has begun, and sends its version", async () => {
    const override = await renderEdit(true);
    await expect.element(page.getByLabelText("Who takes it")).toBeDisabled();
    await expect.element(page.getByLabelText("From")).toBeDisabled();
    await expect.element(page.getByLabelText("To")).toBeEnabled();
    await userEvent.fill(page.getByLabelText("To"), "2026-11-16T15:00");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({
      method: "PUT",
      url: `/api/v1/schedules/${ID}/overrides/${override.id}`,
      ifMatch: '"1"',
      body: {
        user_id: PETR.id,
        replaces_user_id: OLGA.id,
        start: "2026-11-16T08:00:00.000Z",
        end: "2026-11-16T14:00:00.000Z",
      },
    });
  });

  test("lets everything change before it begins", async () => {
    await renderEdit(false);
    await expect.element(page.getByLabelText("Who takes it")).toBeEnabled();
    await expect.element(page.getByLabelText("From")).toBeEnabled();
  });
});
