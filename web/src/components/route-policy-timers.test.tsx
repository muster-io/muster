// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The "Ack timeout and Reminders" section of the Route editor in a real browser: the times its fields give (15, 45 and
// 105 minutes for 15; 4, 12, 28, 52 and 76 hours for 4 and 24), the fields hidden while their switch is off,
// auto-unacknowledge disabled while the Reminders are off, the conversion to seconds on save with the checks of the
// API (at least 1 second once rounded), the API's refusals of the switches and of hidden fields, the profiles' values,
// the read-only view and the Russian plurals.

import { zodResolver } from "@hookform/resolvers/zod";
import { useForm, useWatch } from "react-hook-form";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";
import { z } from "zod";

import type { RoutePolicy } from "../api/gen/model";
import i18n from "../i18n";
import {
  RoutePolicyTimers,
  RoutePolicyTimersReadOnly,
  type TimersValues,
  ackTimeoutNoticeTimes,
  refineTimers,
  reminderTimes,
  timersSchema,
  timersValues,
  withTimers,
} from "./route-policy-timers";

const HOUR = 3600;

/** The On-call profile's policy, as defaults.md gives it. */
const ON_CALL: RoutePolicy = {
  reopen_window_seconds: 900,
  grace_period_seconds: 900,
  urgent_rise_removes_ack: true,
  snooze_durations_seconds: [HOUR, 4 * HOUR, 24 * HOUR],
  thread_batching_window_seconds: 60,
  storm_threshold: 20,
  language: "en",
  templates: { root_message: null, line: null, ack_timeout_notice: null },
  ack_timeout: { enabled: true, first_interval_seconds: 900 },
  reminders: { enabled: true, first_interval_seconds: 4 * HOUR, cap_seconds: 24 * HOUR },
  auto_unacknowledge: false,
};

/** The Informational profile's policy: no ack timeout and no Reminders. */
const INFORMATIONAL: RoutePolicy = {
  ...ON_CALL,
  ack_timeout: { ...ON_CALL.ack_timeout, enabled: false },
  reminders: { ...ON_CALL.reminders, enabled: false },
};

const schema = z.object(timersSchema).superRefine(refineTimers);

type ServerErrors = Partial<Record<keyof TimersValues, string>>;

function Harness({
  policy,
  onSave,
  serverErrors = {},
}: {
  policy: RoutePolicy;
  onSave: (p: RoutePolicy) => void;
  /** Error codes as the Route editor sets them from the pointers of a refusal. */
  serverErrors?: ServerErrors;
}) {
  const form = useForm<TimersValues>({
    resolver: zodResolver(schema),
    defaultValues: timersValues(policy),
  });
  const watched = useWatch({ control: form.control });
  const values: TimersValues = { ...timersValues(policy), ...watched };
  const errors = form.formState.errors;
  return (
    <form noValidate onSubmit={form.handleSubmit((v) => onSave(withTimers(policy, v)))}>
      <RoutePolicyTimers
        id="t"
        fields={{
          ackEnabled: form.register("ack_timeout_enabled"),
          ackFirst: form.register("ack_timeout_first_minutes", { valueAsNumber: true }),
          remindersEnabled: form.register("reminders_enabled"),
          remindersFirst: form.register("reminders_first_hours", { valueAsNumber: true }),
          remindersCap: form.register("reminders_cap_hours", { valueAsNumber: true }),
          autoUnacknowledge: form.register("auto_unacknowledge"),
        }}
        values={values}
        errors={{
          ...serverErrors,
          ack_timeout_first_minutes:
            errors.ack_timeout_first_minutes?.message ?? serverErrors.ack_timeout_first_minutes,
          reminders_first_hours:
            errors.reminders_first_hours?.message ?? serverErrors.reminders_first_hours,
          reminders_cap_hours:
            errors.reminders_cap_hours?.message ?? serverErrors.reminders_cap_hours,
        }}
      />
      <button type="submit">Save</button>
    </form>
  );
}

async function renderSection(policy: RoutePolicy = ON_CALL, serverErrors: ServerErrors = {}) {
  const onSave = vi.fn<(p: RoutePolicy) => void>();
  const screen = await render(
    <I18nextProvider i18n={i18n}>
      <Harness policy={policy} onSave={onSave} serverErrors={serverErrors} />
    </I18nextProvider>,
  );
  return { screen, onSave };
}

const ackSwitch = () => page.getByRole("switch", { name: "Ack timeout", exact: true });
const remindersSwitch = () => page.getByRole("switch", { name: "Reminders", exact: true });
const autoSwitch = () => page.getByRole("switch", { name: "Auto-unacknowledge" });
const save = () => page.getByRole("button", { name: "Save" }).click();

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("the times of the policy", () => {
  test("an ack timeout notices at F, 3F and 7F", () => {
    expect(ackTimeoutNoticeTimes(15)).toEqual([15, 45, 105]);
    expect(ackTimeoutNoticeTimes(10)).toEqual([10, 30, 70]);
  });

  test("Reminders double their interval up to the cap", () => {
    expect(reminderTimes(4, 24)).toEqual([4, 12, 28, 52, 76]);
    expect(reminderTimes(1, 1)).toEqual([1, 2, 3, 4, 5]);
    expect(reminderTimes(2, 6, 3)).toEqual([2, 6, 12]);
  });
});

describe("RoutePolicyTimers", () => {
  test("shows the On-call values and the times they give", async () => {
    await renderSection();
    await expect
      .element(page.getByRole("group", { name: "Ack timeout and Reminders" }))
      .toBeVisible();
    await expect.element(ackSwitch()).toBeChecked();
    await expect.element(page.getByLabelText("First notice after")).toHaveValue(15);
    await expect
      .element(page.getByTestId("ack-timeout-times"))
      .toHaveTextContent("Notices at 15, 45 and 105 minutes");
    await expect.element(remindersSwitch()).toBeChecked();
    await expect.element(page.getByLabelText("First Reminder after")).toHaveValue(4);
    await expect.element(page.getByLabelText("At most every")).toHaveValue(24);
    await expect
      .element(page.getByTestId("reminder-times"))
      .toHaveTextContent("Reminders at 4, 12, 28, 52 and 76 hours");
    await expect.element(autoSwitch()).not.toBeChecked();
    await expect.element(autoSwitch()).toBeEnabled();
    await expect
      .element(
        page.getByText(
          "Take the Alert Group back when its Owner leaves two Reminders in a row unanswered.",
        ),
      )
      .toBeVisible();

    await page.getByLabelText("First notice after").fill("10");
    await expect
      .element(page.getByTestId("ack-timeout-times"))
      .toHaveTextContent("Notices at 10, 30 and 70 minutes");
  });

  test("the Informational profile has both switches off and auto-unacknowledge disabled", async () => {
    await renderSection(INFORMATIONAL);
    await expect.element(ackSwitch()).not.toBeChecked();
    await expect.element(remindersSwitch()).not.toBeChecked();
    expect(page.getByLabelText("First notice after").query()).toBeNull();
    expect(page.getByLabelText("First Reminder after").query()).toBeNull();
    await expect.element(autoSwitch()).toBeDisabled();
    await expect.element(page.getByText("Turn on Reminders to use it.")).toBeVisible();
  });

  test("a switch turned off hides its fields and keeps what was stored", async () => {
    const { onSave } = await renderSection({ ...ON_CALL, auto_unacknowledge: true });
    await ackSwitch().click();
    expect(page.getByLabelText("First notice after").query()).toBeNull();
    expect(page.getByTestId("ack-timeout-times").query()).toBeNull();

    await page.getByLabelText("At most every").fill("");
    await remindersSwitch().click();
    expect(page.getByLabelText("First Reminder after").query()).toBeNull();
    expect(page.getByLabelText("At most every").query()).toBeNull();
    expect(page.getByTestId("reminder-times").query()).toBeNull();
    await expect.element(autoSwitch()).toBeDisabled();
    await expect.element(autoSwitch()).toBeChecked();

    // The emptied field is hidden: it neither blocks the save nor changes the stored seconds.
    await save();
    await vi.waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave.mock.calls[0]?.[0]).toMatchObject({
      ack_timeout: { enabled: false, first_interval_seconds: 900 },
      reminders: { enabled: false, first_interval_seconds: 4 * HOUR, cap_seconds: 24 * HOUR },
      auto_unacknowledge: true,
    });

    // Turned on again, the fields come back with their values.
    await remindersSwitch().click();
    await expect.element(page.getByLabelText("First Reminder after")).toHaveValue(4);
    await expect.element(autoSwitch()).toBeEnabled();
  });

  test("saves in seconds and checks the fields as the API does", async () => {
    const { onSave } = await renderSection();
    await page.getByLabelText("First notice after").fill("");
    await page.getByLabelText("First Reminder after").fill("0");
    await page.getByLabelText("At most every").fill("12");
    await save();
    await expect.element(page.getByText("Fill in this field.")).toBeVisible();
    await expect.element(page.getByText("Enter a number of hours greater than 0.")).toBeVisible();
    expect(onSave).not.toHaveBeenCalled();

    await page.getByLabelText("First notice after").fill("20");
    await page.getByLabelText("First Reminder after").fill("13");
    await save();
    await expect
      .element(page.getByText("Enter a number of hours no smaller than “First Reminder after”."))
      .toBeVisible();
    expect(onSave).not.toHaveBeenCalled();

    await page.getByLabelText("First Reminder after").fill("2");
    await autoSwitch().click();
    await save();
    await vi.waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave.mock.calls[0]?.[0]).toEqual({
      ...ON_CALL,
      ack_timeout: { enabled: true, first_interval_seconds: 1200 },
      reminders: { enabled: true, first_interval_seconds: 2 * HOUR, cap_seconds: 12 * HOUR },
      auto_unacknowledge: true,
    });
  });

  test("refuses amounts below the 1 second the API takes", async () => {
    const { onSave } = await renderSection();
    // 0.001 minutes is 0.06 s and 0.0001 hours 0.36 s: both round to 0 s, which the API refuses.
    await page.getByLabelText("First notice after").fill("0.001");
    await page.getByLabelText("First Reminder after").fill("0.0001");
    await save();
    await expect.element(page.getByText("Enter a number of minutes greater than 0.")).toBeVisible();
    await expect.element(page.getByText("Enter a number of hours greater than 0.")).toBeVisible();
    expect(onSave).not.toHaveBeenCalled();

    // 0.01 minutes rounds to 1 s, 0.001 hours to 4 s: both pass.
    await page.getByLabelText("First notice after").fill("0.01");
    await page.getByLabelText("First Reminder after").fill("0.001");
    await save();
    await vi.waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave.mock.calls[0]?.[0]).toMatchObject({
      ack_timeout: { enabled: true, first_interval_seconds: 1 },
      reminders: { enabled: true, first_interval_seconds: 4, cap_seconds: 24 * HOUR },
    });
  });

  test("shows the API's refusals of the switches next to them", async () => {
    await renderSection(ON_CALL, {
      ack_timeout_enabled: "invalid",
      reminders_enabled: "unsupported",
      auto_unacknowledge: "invalid",
    });
    await expect.element(ackSwitch()).toHaveAttribute("aria-invalid", "true");
    await expect.element(ackSwitch()).toHaveAccessibleDescription(/This value is not valid\./);
    await expect
      .element(remindersSwitch())
      .toHaveAccessibleDescription(/This setting cannot be changed yet\./);
    await expect.element(autoSwitch()).toHaveAttribute("aria-invalid", "true");
    expect(page.getByText("This value is not valid.").all()).toHaveLength(2);
  });

  test("shows a refusal of a hidden field under its switch", async () => {
    await renderSection(INFORMATIONAL, {
      ack_timeout_first_minutes: "out_of_range",
      reminders_cap_hours: "out_of_range",
    });
    expect(page.getByLabelText("First notice after").query()).toBeNull();
    await expect
      .element(ackSwitch())
      .toHaveAccessibleDescription(/Enter a number of minutes greater than 0\./);
    await expect
      .element(remindersSwitch())
      .toHaveAccessibleDescription(/no smaller than “First Reminder after”/);
  });

  test("speaks Russian, with its plurals", async () => {
    await i18n.changeLanguage("ru");
    await renderSection();
    await expect
      .element(page.getByTestId("ack-timeout-times"))
      .toHaveTextContent("Уведомления через 15, 45 и 105 минут");
    await expect
      .element(page.getByTestId("reminder-times"))
      .toHaveTextContent("Напоминания через 4, 12, 28, 52 и 76 часов");
    await page.getByLabelText("Первое уведомление через").fill("3");
    await expect
      .element(page.getByTestId("ack-timeout-times"))
      .toHaveTextContent("Уведомления через 3, 9 и 21 минуту");
    await page.getByLabelText("Первое уведомление через").fill("0.5");
    await expect
      .element(page.getByTestId("ack-timeout-times"))
      .toHaveTextContent("Уведомления через 0,5, 1,5 и 3,5 минуты");
    await page.getByLabelText("Первое напоминание через").fill("1");
    await page.getByLabelText("Не реже чем раз в").fill("1");
    await expect
      .element(page.getByTestId("reminder-times"))
      .toHaveTextContent("Напоминания через 1, 2, 3, 4 и 5 часов");
    await page.getByLabelText("Не реже чем раз в").fill("8");
    await expect
      .element(page.getByTestId("reminder-times"))
      .toHaveTextContent("Напоминания через 1, 3, 7, 15 и 23 часа");
  });
});

describe("RoutePolicyTimersReadOnly", () => {
  test("lists the policy", async () => {
    await render(
      <I18nextProvider i18n={i18n}>
        <RoutePolicyTimersReadOnly policy={{ ...INFORMATIONAL, reminders: ON_CALL.reminders }} />
      </I18nextProvider>,
    );
    const section = page.getByTestId("route-timers");
    await expect.element(section).toBeVisible();
    const text = (tag: string) =>
      [...section.element().querySelectorAll(tag)].map((e) => e.textContent ?? "");
    expect(text("dt")).toEqual(["Ack timeout", "Reminders", "Auto-unacknowledge"]);
    expect(text("dd")).toEqual(["Off", "On, first after 4 h, at most every 24 h", "Off"]);
  });
});
