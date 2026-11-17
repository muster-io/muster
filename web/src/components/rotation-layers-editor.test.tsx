// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Rotation layers of a Schedule in a real browser (C-27.FR-2, FR-13): top first, each with its name, people in
// turn, Shift length (a day, a week or hours, sent as `shift_seconds`), handover time and day, start date and limits,
// where a "to" earlier than "from" reads "until {to} the next day"; "Add Rotation layer"; a person refused as
// `not_pageable` is marked at the person; a Responder reads the summary without controls — in English and in Russian.

import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { OLGA, PETR, SERGEY, rotationLayer, hasText } from "../test/fixtures/schedules";
import {
  type LayerIssues,
  type LayerValues,
  RotationLayersEditor,
  RotationLayersSummary,
  checkLayers,
  layerIssuesOf,
  layerValues,
  layersInput,
  newLayer,
} from "./rotation-layers-editor";

const PEOPLE = [PETR, SERGEY, OLGA];

const AROUND = rotationLayer();
const WORKING = rotationLayer({
  id: "RL0000000000BB",
  number: 2,
  name: "Working hours 11-19",
  user_ids: [OLGA.id, PETR.id],
  users: [OLGA, PETR],
  limits: {
    days: ["monday", "tuesday", "wednesday", "thursday", "friday"],
    start: "11:00",
    end: "19:00",
  },
});

let latest: LayerValues[] = [];

function Editor({ initial, issues = {} }: { initial: LayerValues[]; issues?: LayerIssues }) {
  const [layers, setLayers] = useState(initial);
  return (
    <RotationLayersEditor
      layers={layers}
      onChange={(next) => {
        latest = next;
        setLayers(next);
      }}
      people={PEOPLE}
      team={PEOPLE}
      issues={issues}
      today="2026-11-16"
    />
  );
}

async function renderEditor(initial: LayerValues[], issues?: LayerIssues) {
  await render(
    <I18nextProvider i18n={i18n}>
      <Editor initial={initial} {...(issues === undefined ? {} : { issues })} />
    </I18nextProvider>,
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("the editor", () => {
  test("shows the layers top first, each with its people in turn", async () => {
    // The form holds the top layer first: the working hours above the Rotation layer around the clock.
    await renderEditor([layerValues(WORKING), layerValues(AROUND)]);
    const cards = page.getByTestId("rotation-layer");
    await expect
      .element(cards.nth(0).getByLabelText("Rotation layer name"))
      .toHaveValue("Working hours 11-19");
    await expect
      .element(cards.nth(1).getByLabelText("Rotation layer name"))
      .toHaveValue("Around the clock");
    await hasText(cards.nth(1).getByTestId("rotation-layer-person").nth(1), "@sergey");
    await hasText(cards.nth(0), "Top Rotation layer");
  });

  test("adds a Rotation layer on top, named by its number", async () => {
    await renderEditor([layerValues(AROUND)]);
    await userEvent.click(page.getByRole("button", { name: "Add Rotation layer" }));
    await expect
      .element(page.getByTestId("rotation-layer").nth(0).getByLabelText("Rotation layer name"))
      .toHaveValue("Rotation layer 2");
    expect(latest).toHaveLength(2);
    expect(latest[0]?.id).toBeUndefined();
    expect(latest[1]?.id).toBe(AROUND.id);
  });

  test("adds a person to the turn, moves one and removes one", async () => {
    await renderEditor([layerValues(rotationLayer({ user_ids: [PETR.id], users: [PETR] }))]);
    await userEvent.selectOptions(page.getByLabelText("People in turn"), "@olga");
    await userEvent.selectOptions(page.getByLabelText("People in turn"), "@sergey");
    expect(latest[0]?.userIds).toEqual([PETR.id, OLGA.id, SERGEY.id]);
    await userEvent.click(page.getByRole("button", { name: "Move @sergey up in turn" }));
    expect(latest[0]?.userIds).toEqual([PETR.id, SERGEY.id, OLGA.id]);
    await userEvent.click(page.getByRole("button", { name: "Remove @petr from the turn" }));
    expect(latest[0]?.userIds).toEqual([SERGEY.id, OLGA.id]);
  });

  test("moves a layer down and removes it", async () => {
    await renderEditor([layerValues(WORKING), layerValues(AROUND)]);
    await userEvent.click(page.getByRole("button", { name: "Move Working hours 11-19 down" }));
    expect(latest.map((l) => l.name)).toEqual(["Around the clock", "Working hours 11-19"]);
    await userEvent.click(page.getByRole("button", { name: "Remove Around the clock" }));
    expect(latest.map((l) => l.name)).toEqual(["Working hours 11-19"]);
  });

  test("asks for hours with a Shift of hours, and the day of the handover with a Shift of a week", async () => {
    await renderEditor([layerValues(AROUND)]);
    await expect.element(page.getByLabelText("Handover day")).toBeVisible();
    await expect.element(page.getByLabelText("Hours per Shift")).not.toBeInTheDocument();
    await userEvent.selectOptions(page.getByLabelText("Shift length"), "Hours");
    await expect.element(page.getByLabelText("Hours per Shift")).toBeVisible();
    await expect.element(page.getByLabelText("Handover day")).not.toBeInTheDocument();
  });

  test("says until the next day when the limits cross midnight", async () => {
    await renderEditor([
      layerValues(rotationLayer({ limits: { days: ["friday"], start: "19:00", end: "09:00" } })),
    ]);
    await hasText(page.getByTestId("rotation-layer-crosses"), "until 09:00 the next day");
  });

  test("marks the person the server refused as not pageable", async () => {
    await renderEditor([layerValues(AROUND)], {
      "0.user_ids.1": { code: "not_pageable", reason: "disabled" },
    });
    await hasText(page.getByTestId("rotation-layer-people"), "A disabled User cannot be On-call.");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderEditor([layerValues(AROUND)]);
    await expect.element(page.getByRole("button", { name: "Добавить слой" })).toBeVisible();
    await expect.element(page.getByLabelText("Длина смены")).toBeVisible();
  });
});

describe("what the form sends", () => {
  test("sends the layers lowest first, with the hours as seconds and the day of the handover for weeks only", () => {
    const hours = layerValues(
      rotationLayer({
        id: "RL0000000000CC",
        name: "Night",
        shift_length: "hours",
        shift_seconds: 43200,
        handover_day: undefined,
      }),
    );
    expect(hours.hours).toBe("12");
    const sent = layersInput([hours, layerValues(AROUND)]);
    expect(sent.map((l) => l.name)).toEqual(["Around the clock", "Night"]);
    expect(sent[0]).toMatchObject({ id: AROUND.id, shift_length: "week", handover_day: "monday" });
    expect(sent[0]).not.toHaveProperty("shift_seconds");
    expect(sent[1]).toMatchObject({ shift_length: "hours", shift_seconds: 43200, limits: null });
    expect(sent[1]).not.toHaveProperty("handover_day");
  });

  test("sends the limits with days in the order of the week and the time of day when both are set", () => {
    const sent = layersInput([layerValues(WORKING)]);
    expect(sent[0]?.limits).toEqual({
      days: ["monday", "tuesday", "wednesday", "thursday", "friday"],
      start: "11:00",
      end: "19:00",
    });
  });

  test("keeps the id of a stored layer and sends none for a new one", () => {
    expect(layersInput([newLayer("New", "2026-11-16", [PETR.id])])[0]).not.toHaveProperty("id");
    expect(layersInput([layerValues(AROUND)])[0]).toHaveProperty("id", AROUND.id);
  });
});

describe("the checks", () => {
  test("asks for a name, a person, hours in range, a handover time, a start date and limits that make sense", () => {
    const base = layerValues(AROUND);
    const issues = checkLayers([
      { ...base, name: " ", userIds: [] },
      { ...base, shiftLength: "hours", hours: "0" },
      { ...base, shiftLength: "hours", hours: "700" },
      { ...base, handoverTime: "", startDate: "" },
      { ...base, limited: true, limitDays: [] },
      { ...base, limited: true, limitDays: ["monday"], limitStart: "09:00", limitEnd: "" },
    ]);
    expect(issues).toMatchObject({
      "0.name": { code: "required" },
      "0.user_ids": { code: "required" },
      "1.shift_seconds": { code: "out_of_range" },
      "2.shift_seconds": { code: "out_of_range" },
      "3.handover_time": { code: "required" },
      "3.start_date": { code: "required" },
      "4.limits": { code: "required" },
      "5.limits": { code: "invalid" },
    });
    expect(checkLayers([base])).toEqual({});
  });

  test("turns the pointers of the API, which count from the lowest layer, into the form's, which count from the top", () => {
    const { issues, rest } = layerIssuesOf(
      [
        { pointer: "/rotation_layers/0/user_ids/2", code: "not_pageable", reason: "viewer" },
        { pointer: "/rotation_layers/1/name", code: "too_long" },
        { pointer: "/rotation_layers/1/limits/days", code: "required" },
        { pointer: "/rotation_layers", code: "out_of_range" },
      ],
      2,
    );
    expect(issues["1.user_ids.2"]).toEqual({ code: "not_pageable", reason: "viewer" });
    expect(issues["0.name"]).toEqual({ code: "too_long" });
    expect(issues["0.limits"]).toEqual({ code: "required" });
    expect(rest).toEqual(["out_of_range"]);
  });
});

describe("the summary a Responder reads", () => {
  test("lists the layers top first with their limits, Shift length, handover and people, and no controls", async () => {
    await render(
      <I18nextProvider i18n={i18n}>
        <RotationLayersSummary layers={[AROUND, WORKING]} />
      </I18nextProvider>,
    );
    const items = page.getByTestId("rotation-layer-summary");
    await hasText(items.nth(0), "Working hours 11-19");
    await hasText(items.nth(0), "Mon–Fri 11:00–19:00");
    await hasText(items.nth(0), "@olga → @petr");
    await hasText(items.nth(1), "A Shift of a week");
    await hasText(items.nth(1), "handover Monday at 10:00");
    await expect.element(page.getByRole("button")).not.toBeInTheDocument();
  });

  test("reads a Shift of hours and a limit that crosses midnight", async () => {
    await render(
      <I18nextProvider i18n={i18n}>
        <RotationLayersSummary
          layers={[
            rotationLayer({
              shift_length: "hours",
              shift_seconds: 43200,
              handover_day: undefined,
              limits: { days: ["friday"], start: "19:00", end: "09:00" },
            }),
          ]}
        />
      </I18nextProvider>,
    );
    const item = page.getByTestId("rotation-layer-summary");
    await hasText(item, "A Shift of 12 hours");
    await hasText(item, "19:00, until 09:00 the next day");
  });
});
