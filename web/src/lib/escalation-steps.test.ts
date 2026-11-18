// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The steps of an Escalation chain as the page words and sends them (C-32.FR-3): a step in words in English and in
// Russian with its plural forms, the chips of its limits — the working hours of the owning Team or of the
// Organization —, a wait held in minutes and sent in seconds, and a step read back as it was sent.

import { afterEach, describe, expect, test } from "vitest";

import type { EscalationStep } from "../api/gen/model";
import i18n from "../i18n";
import { workingHours } from "../test/fixtures/teams";
import {
  type StepLookups,
  draftOf,
  limitChips,
  newStep,
  stepInput,
  stepText,
  takesLimits,
  wholeNumber,
} from "./escalation-steps";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

const LOOKUPS: StepLookups = {
  schedules: new Map([["SC1", { name: "Main", team: "payments" }]]),
  users: new Map([
    ["SR1", { id: "SR1", name: "ivan", login: "ivan", deactivated: false }],
    ["SR2", { id: "SR2", name: "petr", login: "petr", deactivated: false }],
  ]),
  teams: new Map([["TM1", "payments"]]),
  gateways: new Map([["GW1", "Example Gateway"]]),
  destinations: new Map([["DS1", "hook"]]),
  owningTeam: "payments",
};

describe("a step in words", () => {
  test("reads in English", () => {
    const t = i18n.t.bind(i18n);
    expect(stepText(t, { ...newStep("page_on_call", 1), scheduleId: "SC1" }, LOOKUPS)).toBe(
      "Page the On-call of payments Main · normal",
    );
    expect(
      stepText(
        t,
        { ...newStep("page_on_call", 1), scheduleId: "SC1", importance: "important" },
        LOOKUPS,
      ),
    ).toBe("Page the On-call of payments Main · important");
    expect(stepText(t, { ...newStep("page_people", 1), userIds: ["SR1", "SR2"] }, LOOKUPS)).toBe(
      "Page @ivan, @petr · normal",
    );
    expect(stepText(t, newStep("page_team", 1), LOOKUPS)).toBe("Page the whole Team payments");
    expect(stepText(t, { ...newStep("page_team", 1), target: "here" }, LOOKUPS)).toBe(
      "Page everyone in the chat (@here)",
    );
    expect(stepText(t, { ...newStep("wait", 1), minutes: "1" }, LOOKUPS)).toBe("Wait 1 minute");
    expect(stepText(t, { ...newStep("wait", 1), minutes: "10" }, LOOKUPS)).toBe("Wait 10 minutes");
    expect(
      stepText(t, { ...newStep("repeat", 7), repeatFrom: "3", repeatTimes: "2" }, LOOKUPS),
    ).toBe("Repeat from step 3, up to 2 times");
    expect(stepText(t, { ...newStep("webhook", 1), destinationId: "DS1" }, LOOKUPS)).toBe(
      "Send to the outgoing webhook hook",
    );
    expect(
      stepText(
        t,
        { ...newStep("call", 1), callTarget: "people", userIds: ["SR1"], gatewayId: "GW1" },
        LOOKUPS,
      ),
    ).toBe("Call @ivan through Example Gateway");
  });

  test("reads in Russian with the plural forms of a wait", async () => {
    await i18n.changeLanguage("ru");
    const t = i18n.t.bind(i18n);
    const wait = (minutes: string) => stepText(t, { ...newStep("wait", 1), minutes }, LOOKUPS);
    expect(wait("1")).toBe("Ждать 1 минуту");
    expect(wait("2")).toBe("Ждать 2 минуты");
    expect(wait("5")).toBe("Ждать 5 минут");
    expect(wait("21")).toBe("Ждать 21 минуту");
    expect(stepText(t, { ...newStep("page_on_call", 1), scheduleId: "SC1" }, LOOKUPS)).toBe(
      "Позвать дежурного payments Main · обычно",
    );
    expect(stepText(t, newStep("page_team", 1), LOOKUPS)).toBe("Позвать всю команду payments");
  });
});

describe("the chips of the limits", () => {
  const hours = workingHours({ start: "11:00", end: "19:00" });

  test("name the working hours of the owning Team and the Severity condition", () => {
    const t = i18n.t.bind(i18n);
    const limited = { ...newStep("page_team", 6), workingHoursOnly: true };
    expect(limitChips(t, limited, { team: "payments", hours })).toEqual([
      "Severity: any",
      "only in the working hours of payments (Mon–Fri 11–19)",
    ]);
    expect(
      limitChips(t, { ...limited, severity: "critical" }, { team: "payments", hours }),
    ).toEqual(["Severity = critical", "only in the working hours of payments (Mon–Fri 11–19)"]);
    expect(limitChips(t, newStep("page_team", 6), { team: "payments", hours })).toEqual([
      "Severity: any",
      "always",
    ]);
  });

  test("name the Organization for a chain without an owning Team", () => {
    const t = i18n.t.bind(i18n);
    const limited = {
      ...newStep("page_people", 1),
      workingHoursOnly: true,
      severity: "below_critical",
    } as const;
    expect(limitChips(t, limited, { hours })).toEqual([
      "Severity < critical",
      "only in the working hours of the Organization (Mon–Fri 11–19)",
    ]);
  });

  test("are none for a wait and a repeat", () => {
    const t = i18n.t.bind(i18n);
    expect(takesLimits("wait")).toBe(false);
    expect(limitChips(t, newStep("wait", 2), {})).toEqual([]);
    expect(limitChips(t, newStep("repeat", 3), {})).toEqual([]);
  });

  test("read in Russian", async () => {
    await i18n.changeLanguage("ru");
    const t = i18n.t.bind(i18n);
    expect(
      limitChips(
        t,
        { ...newStep("page_team", 6), workingHoursOnly: true },
        { team: "payments", hours },
      ),
    ).toEqual(["Важность: любая", "только в рабочее время команды payments (пн–пт 11–19)"]);
  });
});

describe("what a step sends and reads back", () => {
  test("a wait is sent in seconds and read back in minutes", () => {
    const sent = stepInput({ ...newStep("wait", 2), minutes: "15" });
    expect(sent).toEqual({ kind: "wait", wait_seconds: 900 });
    const stored: EscalationStep = { ...sent, number: 2 };
    expect(draftOf(stored).minutes).toBe("15");
  });

  test("a step sends the fields of its kind only, with its limits", () => {
    expect(
      stepInput({
        ...newStep("page_on_call", 3),
        scheduleId: "SC1",
        importance: "important",
        severity: "critical",
        workingHoursOnly: true,
      }),
    ).toEqual({
      kind: "page_on_call",
      schedule_id: "SC1",
      importance: "important",
      working_hours_only: true,
      severity_condition: "critical",
    });
    expect(stepInput({ ...newStep("page_team", 6), target: "channel" })).toEqual({
      kind: "page_team",
      everyone: "channel",
      working_hours_only: false,
      severity_condition: "any",
    });
    expect(stepInput({ ...newStep("page_team", 6), teamId: "TM1" })).toEqual({
      kind: "page_team",
      team_id: "TM1",
      working_hours_only: false,
      severity_condition: "any",
    });
    expect(stepInput({ ...newStep("repeat", 7), repeatFrom: "3", repeatTimes: "2" })).toEqual({
      kind: "repeat",
      repeat_from: 3,
      repeat_times: 2,
    });
  });

  test("a call names its gateway only when one is chosen", () => {
    const call = { ...newStep("call", 1), scheduleId: "SC1" };
    expect(stepInput(call)).toEqual({
      kind: "call",
      call_target: "on_call",
      schedule_id: "SC1",
      working_hours_only: false,
      severity_condition: "any",
    });
    expect(
      stepInput({ ...call, callTarget: "people", userIds: ["SR1"], gatewayId: "GW1" }),
    ).toEqual({
      kind: "call",
      call_target: "people",
      user_ids: ["SR1"],
      gateway_id: "GW1",
      working_hours_only: false,
      severity_condition: "any",
    });
  });

  test("a step read back draws the gateway it names when one exists", () => {
    const stored: EscalationStep = {
      kind: "call",
      number: 1,
      call_target: "on_call",
      schedule_id: "SC1",
      gateway: { id: "GW1", name: "Example Gateway" },
    };
    expect(draftOf(stored).gatewayId).toBe("GW1");
  });

  test("a whole number is digits only", () => {
    expect(wholeNumber(" 12 ")).toBe(12);
    expect(wholeNumber("1.5")).toBeUndefined();
    expect(wholeNumber("")).toBeUndefined();
  });
});
