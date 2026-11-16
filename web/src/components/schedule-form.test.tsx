// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The form of a Schedule in a real browser (C-27.FR-1, FR-2, FR-7, FR-13): "New Schedule" with its Team, name, time zone
// and Rotation layers; the details and layers of a stored one; "main" and the gap alert "By default", "On" or "Off";
// the checks before a save; a name another Schedule of the Team has, and a person refused as `not_pageable`, shown at
// the field and at the person — in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { ScheduleInput, TeamUser } from "../api/gen/model";
import i18n from "../i18n";
import { ApiError } from "../lib/api";
import {
  OLGA,
  PETR,
  SERGEY,
  problem,
  reply,
  rotationLayer,
  schedule,
  hasText,
} from "../test/fixtures/schedules";
import { team, teamUser } from "../test/fixtures/teams";
import { ScheduleForm } from "./schedule-form";

const TEAM = "TM0000000000PA";

const MEMBERS: TeamUser[] = [PETR, SERGEY, OLGA].map((user) => teamUser({ user }));

let sent: ScheduleInput[];

beforeEach(async () => {
  await i18n.changeLanguage("en");
  sent = [];
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/teams?")) {
      return Promise.resolve(
        reply(200, { items: [team({ id: TEAM, name: "payments" })], next_cursor: null }),
      );
    }
    if (url === `/api/v1/teams/${TEAM}/users`) {
      return Promise.resolve(reply(200, { items: MEMBERS }));
    }
    return Promise.resolve(reply(200, { items: [], next_cursor: null }));
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

async function renderForm(
  save: (input: ScheduleInput) => Promise<unknown>,
  props: { schedule?: ReturnType<typeof schedule>; initialTeam?: string } = {},
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <ScheduleForm
          submitLabel="Save"
          save={save}
          {...(props.schedule === undefined ? {} : { schedule: props.schedule })}
          {...(props.initialTeam === undefined ? {} : { initialTeam: props.initialTeam })}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const ok = (input: ScheduleInput) => {
  sent.push(input);
  return Promise.resolve(schedule());
};

/** An ApiError the way the client throws one: the status and the Problem, as the form branches on them. */
function refused(status: number, extra: Record<string, unknown>): Promise<never> {
  return Promise.reject(new ApiError(status, problem(status, extra), undefined));
}

describe("New Schedule", () => {
  test("creates a Schedule with its Team, name, time zone and one Rotation layer of people in turn", async () => {
    await renderForm(ok);
    await userEvent.selectOptions(page.getByLabelText("Team"), "payments");
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "Second line");
    await userEvent.selectOptions(
      page.getByLabelText("Time zone", { exact: true }),
      "Europe/Berlin",
    );
    await expect.element(page.getByLabelText("People in turn")).toBeVisible();
    await userEvent.selectOptions(page.getByLabelText("People in turn"), "@olga");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toMatchObject({
      team_id: TEAM,
      name: "Second line",
      time_zone: "Europe/Berlin",
      main: false,
      gap_alert: "by_default",
      rotation_layers: [
        { name: "Rotation layer 1", user_ids: [OLGA.id], shift_length: "week", limits: null },
      ],
    });
  });

  test("checks the Team, the name and a person before it sends anything", async () => {
    await renderForm(ok);
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect.element(page.getByText("Fill in this field.").first()).toBeVisible();
    expect(page.getByText("Fill in this field.").elements().length).toBeGreaterThanOrEqual(3);
    expect(sent).toHaveLength(0);
  });

  test("starts with the Team the link named", async () => {
    await renderForm(ok, { initialTeam: TEAM });
    await expect.element(page.getByLabelText("Team")).toHaveValue(TEAM);
  });

  test("shows a name that another Schedule of the Team has at the name", async () => {
    await renderForm(() => refused(409, { code: "name_taken" }), { initialTeam: TEAM });
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "Main");
    await userEvent.selectOptions(page.getByLabelText("People in turn"), "@olga");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect.element(page.getByText("The Team has a Schedule with this name.")).toBeVisible();
  });

  test("shows a person the server refused as not pageable at the person", async () => {
    await renderForm(
      () =>
        refused(422, {
          errors: [
            { pointer: "/rotation_layers/0/user_ids/0", code: "not_pageable", reason: "viewer" },
          ],
        }),
      { initialTeam: TEAM },
    );
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "Second line");
    await userEvent.selectOptions(page.getByLabelText("People in turn"), "@olga");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await hasText(page.getByTestId("rotation-layer-people"), "A Viewer cannot be On-call.");
  });
});

describe("what the form cannot place", () => {
  test("shows a refusal of a field it has no control for as a whole", async () => {
    await renderForm(
      () => refused(422, { errors: [{ pointer: "/gap_alert", code: "unsupported" }] }),
      {
        initialTeam: TEAM,
      },
    );
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "Second line");
    await userEvent.selectOptions(page.getByLabelText("People in turn"), "@olga");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect.element(page.getByText("This setting cannot be changed yet.")).toBeVisible();
  });

  test("forgets what the server said about a person once the list of people changes", async () => {
    await renderForm(
      () =>
        refused(422, {
          errors: [
            { pointer: "/rotation_layers/0/user_ids/0", code: "not_pageable", reason: "viewer" },
          ],
        }),
      { initialTeam: TEAM },
    );
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "Second line");
    await userEvent.selectOptions(page.getByLabelText("People in turn"), "@olga");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect.element(page.getByText("A Viewer cannot be On-call.")).toBeVisible();
    await userEvent.click(page.getByRole("button", { name: "Remove @olga from the turn" }));
    await expect.element(page.getByText("A Viewer cannot be On-call.")).not.toBeInTheDocument();
  });

  test("offers to read the Schedule again when it changed meanwhile", async () => {
    const reloads: number[] = [];
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <ScheduleForm
            submitLabel="Save"
            schedule={schedule()}
            save={() => refused(412, {})}
            onReload={() => {
              reloads.push(1);
            }}
          />
        </QueryClientProvider>
      </I18nextProvider>,
    );
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await expect
      .element(page.getByText("Someone else changed this Schedule. Reload to see the changes."))
      .toBeVisible();
    await userEvent.click(page.getByRole("button", { name: "Reload" }));
    expect(reloads).toHaveLength(1);
  });
});

describe("a stored Schedule", () => {
  const STORED = schedule({
    rotation_layers: [
      rotationLayer(),
      rotationLayer({
        id: "RL0000000000BB",
        number: 2,
        name: "Working hours 11-19",
        user_ids: [OLGA.id],
        users: [OLGA],
      }),
    ],
  });

  test("shows the details and the layers, top first, and keeps main on", async () => {
    await renderForm(ok, { schedule: STORED });
    await expect.element(page.getByLabelText("Name", { exact: true })).toHaveValue("Main");
    const main = page.getByRole("checkbox", { name: "Main Schedule" });
    await expect.element(main).toBeChecked();
    await expect.element(main).toBeDisabled();
    await expect
      .element(page.getByTestId("rotation-layer").nth(0).getByLabelText("Rotation layer name"))
      .toHaveValue("Working hours 11-19");
  });

  test("sets the gap alert to On and saves the layers lowest first with their ids", async () => {
    await renderForm(ok, { schedule: STORED });
    await userEvent.selectOptions(page.getByLabelText("Gap alert"), "On");
    await userEvent.click(page.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.gap_alert).toBe("on");
    expect(sent[0]?.rotation_layers.map((l) => [l.id, l.name])).toEqual([
      ["RL0000000000AA", "Around the clock"],
      ["RL0000000000BB", "Working hours 11-19"],
    ]);
  });

  test("offers the three gap alert settings, with its hint and a Learn more link", async () => {
    await renderForm(ok, { schedule: STORED });
    const select = page.getByLabelText("Gap alert");
    await expect.element(select).toHaveValue("by_default");
    expect(select.element().querySelectorAll("option")).toHaveLength(3);
    await expect
      .element(
        page.getByText("By default the alert is on while this is the Team's main Schedule.", {
          exact: false,
        }),
      )
      .toBeVisible();
    await expect
      .element(page.getByTestId("docs-link"))
      .toHaveAttribute("data-docs", "field.gap_alert");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderForm(ok, { schedule: STORED });
    await expect.element(page.getByLabelText("Оповещение о дыре")).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Добавить слой" })).toBeVisible();
    await expect.element(page.getByRole("checkbox", { name: "Главное расписание" })).toBeVisible();
  });
});
