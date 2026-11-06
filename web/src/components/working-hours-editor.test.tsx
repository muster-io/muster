// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Working hours of a Team in a real browser (C-25.FR-9): "Organization default: Mon–Fri 09:00–18:00, Europe/Berlin"
// until "Own" is chosen, then the days, "From", "To" and the time zone; a refusal at the field it names, and the
// Russian texts. The text of a list of days is checked as a function.

import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { WorkingHours } from "../api/gen/model";
import i18n from "../i18n";
import { workingHours } from "../test/fixtures/teams";
import {
  type HoursErrors,
  WorkingHoursEditor,
  hoursErrorText,
  workingDaysText,
  workingHoursText,
} from "./working-hours-editor";

const ORG = workingHours({ time_zone: "Europe/Berlin" });

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

function Harness({
  startOwn = false,
  onValue,
  errors,
}: {
  startOwn?: boolean;
  onValue?: (own: boolean, hours: WorkingHours) => void;
  errors?: HoursErrors;
}) {
  const [own, setOwn] = useState(startOwn);
  const [hours, setHours] = useState(ORG);
  return (
    <I18nextProvider i18n={i18n}>
      <WorkingHoursEditor
        id="hours"
        own={own}
        onOwnChange={(next) => {
          setOwn(next);
          onValue?.(next, hours);
        }}
        value={hours}
        onChange={(next) => {
          setHours(next);
          onValue?.(own, next);
        }}
        organization={ORG}
        errors={errors}
      />
    </I18nextProvider>
  );
}

describe("the Working hours editor", () => {
  test("names a list of days as a person reads it", () => {
    const t = i18n.t.bind(i18n);
    expect(workingDaysText(t, ["monday", "tuesday", "wednesday", "thursday", "friday"])).toBe(
      "Mon–Fri",
    );
    expect(workingDaysText(t, ["friday", "monday", "wednesday"])).toBe("Mon, Wed, Fri");
    expect(workingDaysText(t, ["saturday", "sunday"])).toBe("Sat, Sun");
    expect(workingDaysText(t, ["monday", "tuesday", "wednesday"])).toBe("Mon–Wed");
    expect(workingDaysText(t, [])).toBe("no days");
    expect(workingHoursText(t, ORG)).toBe("Mon–Fri 09:00–18:00, Europe/Berlin");
    expect(hoursErrorText(t, "out_of_range")).toBe("The end must be later than the start.");
    expect(hoursErrorText(t, "required")).toBe("Fill in this field.");
  });

  test("reads the Organization default until Own is chosen, then edits the days, From, To and the zone", async () => {
    const seen: [boolean, WorkingHours][] = [];
    await render(<Harness onValue={(own, hours) => seen.push([own, hours])} />);
    await expect
      .element(page.getByText("Organization default: Mon–Fri 09:00–18:00, Europe/Berlin"))
      .toBeVisible();
    expect(page.getByLabelText("From", { exact: true }).elements()).toHaveLength(0);

    await page.getByRole("checkbox", { name: "Own" }).click();
    await expect.element(page.getByLabelText("From", { exact: true })).toHaveValue("09:00");
    await expect.element(page.getByLabelText("To", { exact: true })).toHaveValue("18:00");
    await userEvent.fill(page.getByLabelText("From", { exact: true }), "11:00");
    await userEvent.fill(page.getByLabelText("To", { exact: true }), "19:00");
    await page.getByRole("checkbox", { name: "Fri" }).click();
    await page.getByRole("checkbox", { name: "Sat" }).click();
    await userEvent.selectOptions(
      page.getByLabelText("Time zone", { exact: true }),
      "Europe/Moscow",
    );
    expect(seen.at(-1)?.[0]).toBe(true);
    expect(seen.at(-1)?.[1]).toEqual({
      days: ["monday", "tuesday", "wednesday", "thursday", "saturday"],
      start: "11:00",
      end: "19:00",
      time_zone: "Europe/Moscow",
    });
  });

  test("shows a refusal at the field it names", async () => {
    await render(
      <Harness
        startOwn
        errors={{
          end: hoursErrorText(i18n.t.bind(i18n), "out_of_range"),
          days: "Fill in this field.",
        }}
      />,
    );
    const to = page.getByLabelText("To", { exact: true });
    expect(to.element().getAttribute("aria-invalid")).toBe("true");
    await expect.element(page.getByText("The end must be later than the start.")).toBeVisible();
    expect(
      page.getByLabelText("From", { exact: true }).element().getAttribute("aria-invalid"),
    ).toBe("false");
    await expect.element(page.getByText("Fill in this field.")).toBeVisible();
  });

  test("reads in Russian and fits 360 px", async () => {
    await i18n.changeLanguage("ru");
    await page.viewport(360, 640);
    await render(<Harness />);
    await expect
      .element(page.getByText("В организации по умолчанию: пн–пт 09:00–18:00, Europe/Berlin"))
      .toBeVisible();
    await page.getByRole("checkbox", { name: "Своё" }).click();
    await expect.element(page.getByLabelText("С", { exact: true })).toBeVisible();
    await expect.element(page.getByLabelText("До", { exact: true })).toBeVisible();
    await expect.element(page.getByLabelText("Часовой пояс", { exact: true })).toBeVisible();
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(360);
  });
});
