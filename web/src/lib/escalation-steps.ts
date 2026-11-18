// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The steps of an Escalation chain as the page edits and reads them (C-32.FR-3): the seven kinds, a step as the form
// holds it (a draft) and as the request writes it, a step in words ("Page the On-call of payments Main · normal"), and
// the chips of its limits ("Severity: any", "always", "only in the working hours of payments (Mon–Fri 11–19)").
// "Wait" is held in whole minutes and sent as `wait_seconds` (minutes × 60, S-154).

import type { TFunction } from "i18next";

import type {
  EscalationStep,
  EscalationStepInput,
  EscalationStepKind,
  PageImportance,
  SeverityCondition,
  UserRef,
  WorkingHours,
} from "../api/gen/model";
import { workingDaysText } from "../components/working-hours-editor";

/** The kinds of step in the order "Add step" offers them. */
export const STEP_KINDS = [
  "page_on_call",
  "page_people",
  "page_team",
  "wait",
  "webhook",
  "repeat",
  "call",
] as const satisfies readonly EscalationStepKind[];

/** The most steps a chain takes (`escalation.limits`). */
export const STEPS_MAX = 20;
/** The longest wait, in minutes (`escalation.limits`). */
export const WAIT_MAX_MINUTES = 1440;
/** The most times a step repeats (`escalation.limits`). */
export const REPEAT_MAX_TIMES = 10;

/** The words for everyone in the chat that a step may write, in the order of the list. */
export const EVERYONE_CHOICES = ["channel", "here", "all", "everyone"] as const;
export type EveryoneChoice = (typeof EVERYONE_CHOICES)[number];

/** Whom "Page the whole Team" calls: the Team mention, or one of the words for everyone in the chat. */
export type TeamTarget = "team" | EveryoneChoice;

/** A step as the form holds it: every field of every kind, of which a step uses those of its kind. */
export interface StepDraft {
  key: string;
  kind: EscalationStepKind;
  importance: PageImportance;
  scheduleId: string;
  userIds: string[];
  /** The Team of a Team mention; empty for the chain's owning Team. */
  teamId: string;
  target: TeamTarget;
  callTarget: "on_call" | "people";
  gatewayId: string;
  /** Whole minutes of a wait, as typed. */
  minutes: string;
  destinationId: string;
  repeatFrom: string;
  repeatTimes: string;
  workingHoursOnly: boolean;
  severity: SeverityCondition;
  /** The step as read, for a kind this version does not know: it is shown by its kind and saved unchanged. */
  raw?: EscalationStepInput;
}

/** Whether the kind is one this version edits. */
export function isKnownKind(kind: string): boolean {
  return STEP_KINDS.some((k) => k === kind);
}

let keyCounter = 0;

export function newStepKey(): string {
  keyCounter += 1;
  return `step-${keyCounter}`;
}

/** A new step of a kind with the defaults of the API: normal, any severity, at any time. */
export function newStep(kind: EscalationStepKind, position: number): StepDraft {
  return {
    key: newStepKey(),
    kind,
    importance: "normal",
    scheduleId: "",
    userIds: [],
    teamId: "",
    target: "team",
    callTarget: "on_call",
    gatewayId: "",
    minutes: kind === "wait" ? "10" : "",
    destinationId: "",
    repeatFrom: kind === "repeat" ? String(Math.max(1, position - 1)) : "",
    repeatTimes: kind === "repeat" ? "1" : "",
    workingHoursOnly: false,
    severity: "any",
  };
}

/** A stored step as the form holds it; a wait is read back in minutes. */
export function draftOf(step: EscalationStep): StepDraft {
  const seconds = step.wait_seconds;
  return {
    ...newStep(step.kind, step.number),
    importance: step.importance ?? "normal",
    scheduleId: step.schedule_id ?? "",
    userIds: step.user_ids ?? [],
    teamId: step.team_id ?? "",
    target: step.everyone ?? "team",
    callTarget: step.call_target ?? "on_call",
    gatewayId: step.gateway_id ?? step.gateway?.id ?? "",
    minutes: seconds === undefined ? "" : String(Math.round(seconds / 60)),
    destinationId: step.destination_id ?? "",
    repeatFrom: step.repeat_from === undefined ? "" : String(step.repeat_from),
    repeatTimes: step.repeat_times === undefined ? "" : String(step.repeat_times),
    workingHoursOnly: step.working_hours_only ?? false,
    severity: step.severity_condition ?? "any",
    ...(isKnownKind(step.kind) ? {} : { raw: rawOf(step) }),
  };
}

/** A stored step as a request writes it: without its number and the objects its ids name. */
function rawOf(step: EscalationStep): EscalationStepInput {
  const {
    number: _number,
    schedule: _s,
    users: _u,
    team: _t,
    gateway: _g,
    destination: _d,
    ...rest
  } = step;
  return rest;
}

/** Whether a kind takes the limits "working hours only" and a Severity condition; a kind not known takes none. */
export function takesLimits(kind: EscalationStepKind): boolean {
  return isKnownKind(kind) && kind !== "wait" && kind !== "repeat";
}

/** The whole number a field holds, or undefined while it holds something else. */
export function wholeNumber(text: string): number | undefined {
  const trimmed = text.trim();
  return /^\d+$/.test(trimmed) ? Number(trimmed) : undefined;
}

/** The step as a request writes it: the fields of its kind only. */
export function stepInput(d: StepDraft): EscalationStepInput {
  const limits = takesLimits(d.kind)
    ? { working_hours_only: d.workingHoursOnly, severity_condition: d.severity }
    : {};
  switch (d.kind) {
    case "page_on_call":
      return { kind: d.kind, schedule_id: d.scheduleId, importance: d.importance, ...limits };
    case "page_people":
      return { kind: d.kind, user_ids: d.userIds, importance: d.importance, ...limits };
    case "page_team":
      return {
        kind: d.kind,
        ...(d.target === "team"
          ? d.teamId === ""
            ? {}
            : { team_id: d.teamId }
          : { everyone: d.target }),
        ...limits,
      };
    case "call":
      return {
        kind: d.kind,
        call_target: d.callTarget,
        ...(d.callTarget === "on_call" ? { schedule_id: d.scheduleId } : { user_ids: d.userIds }),
        ...(d.gatewayId === "" ? {} : { gateway_id: d.gatewayId }),
        ...limits,
      };
    case "wait":
      return { kind: d.kind, wait_seconds: (wholeNumber(d.minutes) ?? 0) * 60 };
    case "webhook":
      return { kind: d.kind, destination_id: d.destinationId, ...limits };
    case "repeat":
      return {
        kind: d.kind,
        repeat_from: wholeNumber(d.repeatFrom) ?? 0,
        repeat_times: wholeNumber(d.repeatTimes) ?? 0,
      };
    default:
      // A kind a later version added is sent back as it was read.
      return d.raw ?? { kind: d.kind };
  }
}

/** The name of a kind of step, as "Add step" offers it. */
export function kindName(t: TFunction, kind: EscalationStepKind): string {
  switch (kind) {
    case "page_on_call":
      return t("escalation.kinds.pageOnCall");
    case "page_people":
      return t("escalation.kinds.pagePeople");
    case "page_team":
      return t("escalation.kinds.pageTeam");
    case "wait":
      return t("escalation.kinds.wait");
    case "webhook":
      return t("escalation.kinds.webhook");
    case "repeat":
      return t("escalation.kinds.repeat");
    case "call":
      return t("escalation.kinds.call");
    default:
      return String(kind);
  }
}

export function importanceName(t: TFunction, importance: PageImportance): string {
  return importance === "important" ? t("escalation.important") : t("escalation.normal");
}

/** The people of a step by their logins, "@ivan, @petr"; a person the lookup does not know shows their id. */
export function peopleText(ids: readonly string[], users: ReadonlyMap<string, UserRef>): string {
  return ids
    .map((id) => {
      const user = users.get(id);
      if (user === undefined) {
        return id;
      }
      return user.login !== undefined && user.login !== "" ? `@${user.login}` : user.name;
    })
    .join(", ");
}

/** What the words of a step name by id: the objects the page has read. */
export interface StepLookups {
  schedules: ReadonlyMap<string, { name: string; team: string }>;
  users: ReadonlyMap<string, UserRef>;
  teams: ReadonlyMap<string, string>;
  gateways: ReadonlyMap<string, string>;
  destinations: ReadonlyMap<string, string>;
  /** The chain's owning Team by name; none for a chain of the Organization. */
  owningTeam?: string;
}

/** A step in one sentence: "Page the On-call of payments Main · normal", "Wait 10 minutes", "Repeat from step 3, up to 2 times". */
export function stepText(t: TFunction, d: StepDraft, lookups: StepLookups): string {
  const schedule = (): string => {
    const found = lookups.schedules.get(d.scheduleId);
    return found === undefined ? d.scheduleId : `${found.team} ${found.name}`;
  };
  const gateway = lookups.gateways.get(d.gatewayId);
  switch (d.kind) {
    case "page_on_call":
      return `${t("escalation.words.pageOnCall", { schedule: schedule() })} · ${importanceName(t, d.importance)}`;
    case "page_people":
      return `${t("escalation.words.pagePeople", { people: peopleText(d.userIds, lookups.users) })} · ${importanceName(t, d.importance)}`;
    case "page_team": {
      if (d.target !== "team") {
        return t("escalation.words.pageEveryone", { word: `@${d.target}` });
      }
      const team = d.teamId === "" ? lookups.owningTeam : lookups.teams.get(d.teamId);
      return team === undefined || team === ""
        ? t("escalation.words.pageTeamUnknown")
        : t("escalation.words.pageTeam", { team });
    }
    case "call": {
      if (d.callTarget === "on_call") {
        return gateway === undefined
          ? t("escalation.words.callOnCall", { schedule: schedule() })
          : t("escalation.words.callOnCallThrough", { schedule: schedule(), gateway });
      }
      const people = peopleText(d.userIds, lookups.users);
      return gateway === undefined
        ? t("escalation.words.callPeople", { people })
        : t("escalation.words.callPeopleThrough", { people, gateway });
    }
    case "wait":
      return t("escalation.words.wait", { count: wholeNumber(d.minutes) ?? 0 });
    case "webhook":
      return t("escalation.words.webhook", {
        destination: lookups.destinations.get(d.destinationId) ?? d.destinationId,
      });
    case "repeat":
      return t("escalation.words.repeat", {
        from: wholeNumber(d.repeatFrom) ?? 0,
        count: wholeNumber(d.repeatTimes) ?? 0,
      });
    default:
      return String(d.kind);
  }
}

/** The condition of a Severity chip: "Severity: any", "Severity = critical", "Severity < critical". */
export function severityChip(t: TFunction, condition: SeverityCondition): string {
  switch (condition) {
    case "any":
      return t("escalation.chips.severityAny");
    case "critical":
      return t("escalation.chips.severityCritical");
    case "below_critical":
      return t("escalation.chips.severityBelowCritical");
    default:
      return condition satisfies never;
  }
}

/** The Working hours a chip names: those of the owning Team, else of the Organization, as the chain's page knows them. */
export interface ChipHours {
  /** The owning Team's name; none for a chain of the Organization. */
  team?: string;
  hours?: WorkingHours;
}

/** A time of day as a chip writes it: "11" for a whole hour, "09:30" otherwise. */
function clock(time: string): string {
  const match = /^0?(\d{1,2}):00$/.exec(time);
  return match?.[1] ?? time;
}

/** "Mon–Fri 11–19", the hours of a chip without their time zone; minutes show only when they are not zero. */
export function hoursShort(t: TFunction, hours: WorkingHours): string {
  return `${workingDaysText(t, hours.days)} ${clock(hours.start)}–${clock(hours.end)}`;
}

/** The chips of a step's limits: its hours — "always" or "only in the working hours of …" — and its Severity. */
export function limitChips(t: TFunction, d: StepDraft, at: ChipHours): string[] {
  if (!takesLimits(d.kind)) {
    return [];
  }
  const hours = at.hours === undefined ? "" : ` (${hoursShort(t, at.hours)})`;
  const when = d.workingHoursOnly
    ? at.team === undefined
      ? t("escalation.chips.hoursOrganization", { hours })
      : t("escalation.chips.hoursTeam", { team: at.team, hours })
    : t("escalation.chips.always");
  return [severityChip(t, d.severity), when];
}
