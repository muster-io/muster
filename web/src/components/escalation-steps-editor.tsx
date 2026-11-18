// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The steps of an Escalation chain (C-32.FR-3, FR-17; C-30.FR-9): "Steps", numbered, each in words with its limits as
// chips, and, for an Admin, the fields of its kind, "Add step" with the seven kinds — "Call through the Notification
// gateway" greyed with the note of S-150 while no gateway exists —, "Whom 'Page the whole Team' calls" with the choices
// the chain's Destinations cannot mention greyed with their reason, and the hint about limits and about "Normal and
// important". A wait is held in whole minutes. A Responder only reads the steps. A refusal of the server shows at the
// step it names.

import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { ArrowDownIcon, ArrowUpIcon, PlusIcon, XIcon } from "lucide-react";
import { type ReactNode, useId, useMemo } from "react";
import { useTranslation } from "react-i18next";

import { useGetOrganization } from "../api/gen/endpoints/organization/organization";
import { getListSchedulesQueryKey, listSchedules } from "../api/gen/endpoints/schedules/schedules";
import { useGetTeam } from "../api/gen/endpoints/teams/teams";
import { getListUsersQueryKey, listUsers } from "../api/gen/endpoints/users/users";
import type {
  Destination,
  DestinationRef,
  EntityRef,
  EscalationStep,
  EscalationStepKind,
  Schedule,
  User,
  UserRef,
  WorkingHours,
} from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import {
  type ChipHours,
  EVERYONE_CHOICES,
  REPEAT_MAX_TIMES,
  STEPS_MAX,
  STEP_KINDS,
  type StepDraft,
  type StepLookups,
  type TeamTarget,
  WAIT_MAX_MINUTES,
  kindName,
  limitChips,
  newStep,
  stepText,
  takesLimits,
} from "../lib/escalation-steps";
import { type MentionAbilities, reasonText, unavailableReason } from "../lib/mention-entries";
import { useCan } from "./app-shell";
import { DocsLink } from "./docs-link";
import { useDestinationChoices } from "./route-destinations";
import { GatewayUnavailable } from "./gateway-unavailable";
import { useKnownGateways } from "./notification-rules-section";
import { useTeamList } from "./team-filter";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** A refusal of the server at a step: its field ("user_ids/0", "gateway_id", …), the code and why, if it says. */
export interface StepIssue {
  index: number;
  field: string;
  code: string;
  reason?: string;
}

/** The refusals of a save by the step they name (/steps/{i}/{field}); the others stay with the caller. */
export function stepIssuesOf(
  errors: readonly { pointer: string; code: string; reason?: string }[],
): { issues: StepIssue[]; rest: string[] } {
  const issues: StepIssue[] = [];
  const rest: string[] = [];
  for (const e of errors) {
    const match = /^\/steps\/(\d+)(?:\/(.+))?$/.exec(e.pointer);
    if (match?.[1] === undefined) {
      if (e.pointer.startsWith("/steps")) {
        issues.push({ index: -1, field: "", code: e.code });
      } else {
        rest.push(e.pointer);
      }
      continue;
    }
    issues.push({
      index: Number(match[1]),
      field: match[2] ?? "",
      code: e.code,
      ...(e.reason === undefined ? {} : { reason: e.reason }),
    });
  }
  return { issues, rest };
}

/** The text of a refusal at a step. */
export function stepIssueText(t: TFunction, issue: StepIssue): string {
  switch (issue.code) {
    case "not_pageable":
      return issue.reason === "viewer"
        ? t("escalation.errors.viewer")
        : issue.reason === "disabled"
          ? t("escalation.errors.disabled")
          : t("escalation.errors.notPageable");
    case "gateway_required":
      return t("escalation.errors.gatewayRequired");
    case "webhook_events_required":
      return t("escalation.errors.webhookEventsRequired");
    case "unknown_id":
      return t("escalation.errors.unknownId");
    default:
      return fieldErrorText(t, issue.code);
  }
}

/** What the fields of the steps choose among, and the words they are shown in. */
export interface StepChoices {
  schedules: Schedule[];
  users: User[];
  teams: { id: string; name: string }[];
  gateways: EntityRef[];
  gatewaysSettled: boolean;
  webhooks: Destination[];
  lookups: StepLookups;
  /** What a Destination's Connection can mention, for the chain's Destinations. */
  abilities: MentionAbilities[];
}

/**
 * Reads what the steps name: the Schedules of every Team, the people who may be Paged (Admins only: users:read), the
 * Teams, the Notification gateways and the outgoing webhooks that take events. What the stored steps name is known
 * without reading, so that a Responder sees the words too.
 */
export function useStepChoices(
  stored: readonly EscalationStep[],
  destinations: readonly DestinationRef[],
  owningTeam: string | undefined,
  editing: boolean,
): StepChoices {
  const canSchedules = useCan("schedules:read");
  const canUsers = useCan("users:read");
  const schedules = useQuery({
    queryKey: getListSchedulesQueryKey({ limit: 500 }),
    queryFn: ({ signal }) => listSchedules({ limit: 500 }, { signal }),
    enabled: canSchedules,
    staleTime: 30_000,
  });
  const users = useQuery({
    queryKey: getListUsersQueryKey({ limit: 500 }),
    queryFn: ({ signal }) => listUsers({ limit: 500 }, { signal }),
    enabled: canUsers && editing,
    staleTime: 30_000,
  });
  const teams = useTeamList();
  const gateways = useKnownGateways([]);
  const all = useDestinationChoices();
  return useMemo(() => {
    const scheduleMap = new Map<string, { name: string; team: string }>();
    const userMap = new Map<string, UserRef>();
    const teamMap = new Map<string, string>();
    const gatewayMap = new Map<string, string>();
    const destinationMap = new Map<string, string>();
    for (const step of stored) {
      if (step.schedule !== undefined) {
        scheduleMap.set(step.schedule.id, {
          name: step.schedule.name,
          team: step.schedule.team.name,
        });
      }
      for (const u of step.users ?? []) {
        userMap.set(u.id, u);
      }
      if (step.team !== undefined) {
        teamMap.set(step.team.id, step.team.name);
      }
      if (step.gateway !== undefined) {
        gatewayMap.set(step.gateway.id, step.gateway.name);
      }
      if (step.destination !== undefined) {
        destinationMap.set(step.destination.id, step.destination.name);
      }
    }
    for (const s of schedules.data?.items ?? []) {
      scheduleMap.set(s.id, { name: s.name, team: s.team.name });
    }
    for (const u of users.data?.items ?? []) {
      userMap.set(u.id, { id: u.id, name: u.name, login: u.login, deactivated: false });
    }
    for (const team of teams.data ?? []) {
      teamMap.set(team.id, team.name);
    }
    for (const g of gateways.gateways) {
      gatewayMap.set(g.id, g.name);
    }
    for (const d of all.items) {
      destinationMap.set(d.id, d.name);
    }
    const webhooks = all.items.filter(
      (d) => d.type === "webhook" && (d.mode === "events" || d.mode === "both"),
    );
    const abilities: MentionAbilities[] = destinations.map((ref) => {
      const full = all.items.find((d) => d.id === ref.id);
      const connection =
        full === undefined || full.type === "webhook"
          ? undefined
          : all.connections.get(full.connection_id);
      return { type: ref.type, ...(connection === undefined ? {} : { connection }) };
    });
    return {
      schedules: schedules.data?.items ?? [],
      users: users.data?.items ?? [],
      teams: teams.data ?? [],
      gateways: gateways.gateways,
      gatewaysSettled: gateways.settled,
      webhooks,
      abilities,
      lookups: {
        schedules: scheduleMap,
        users: userMap,
        teams: teamMap,
        gateways: gatewayMap,
        destinations: destinationMap,
        ...(owningTeam === undefined ? {} : { owningTeam }),
      },
    };
  }, [
    stored,
    destinations,
    owningTeam,
    schedules.data,
    users.data,
    teams.data,
    gateways.gateways,
    gateways.settled,
    all.items,
    all.connections,
  ]);
}

/** The Working hours the chips name: the owning Team's, else the Organization's. */
export function useChipHours(owningTeamId: string, owningTeamName: string | undefined): ChipHours {
  const canOrganization = useCan("organization:read");
  const team = useGetTeam(owningTeamId, { query: { enabled: owningTeamId !== "" } });
  const organization = useGetOrganization({
    query: { enabled: owningTeamId === "" && canOrganization },
  });
  const hours: WorkingHours | undefined =
    owningTeamId === "" ? organization.data?.working_hours : team.data?.working_hours;
  return {
    ...(owningTeamId === "" ? {} : { team: team.data?.name ?? owningTeamName ?? owningTeamId }),
    ...(hours === undefined ? {} : { hours }),
  };
}

/** Whether every Destination the chain writes in cannot mention the target: it is greyed, with the first reason. */
function unavailableFor(
  choices: StepChoices,
  target: TeamTarget,
): { reason: ReturnType<typeof unavailableReason>; type: MentionAbilities["type"] } | null {
  if (choices.abilities.length === 0) {
    return null;
  }
  const found = choices.abilities.map((a) => ({
    type: a.type,
    reason: unavailableReason(
      a,
      target === "team" ? { kind: "team" } : { kind: "everyone", everyone: target },
    ),
  }));
  const first = found[0];
  return found.every((f) => f.reason !== null) && first !== undefined ? first : null;
}

function targetName(t: TFunction, target: TeamTarget): string {
  return target === "team" ? t("escalation.fields.teamMention") : `@${target}`;
}

interface StepFieldsProps {
  id: string;
  step: StepDraft;
  choices: StepChoices;
  owningTeam: { id: string; name: string } | undefined;
  issues: StepIssue[];
  onChange: (next: StepDraft) => void;
}

function Field({
  id,
  label,
  children,
  error,
  className = "",
}: {
  id: string;
  label: string;
  children: ReactNode;
  error?: string | undefined;
  className?: string;
}) {
  return (
    <div className={`flex min-w-0 flex-col gap-1.5 ${className}`}>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error !== undefined && (
        <p id={`${id}-error`} className="text-sm text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

function PeoplePicker({
  id,
  step,
  choices,
  issues,
  onChange,
}: Pick<StepFieldsProps, "id" | "step" | "choices" | "issues" | "onChange">) {
  const { t } = useTranslation();
  const rest = choices.users.filter(
    (u) => u.role !== "viewer" && u.status === "active" && !step.userIds.includes(u.id),
  );
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <Label htmlFor={`${id}-person`}>{t("escalation.fields.people")}</Label>
      {step.userIds.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" data-testid="step-people">
          {step.userIds.map((userId, i) => {
            const person = choices.lookups.users.get(userId);
            const text = person === undefined ? userId : `@${person.login ?? person.name}`;
            const own = issues.find((x) => x.field === `user_ids/${i}`);
            return (
              <li
                key={userId}
                className={`flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-sm ${
                  own === undefined ? "bg-muted" : "border-destructive"
                }`}
              >
                <span className="wrap-anywhere">{text}</span>
                {own !== undefined && (
                  <span className="text-destructive" role="alert">
                    {stepIssueText(t, own)}
                  </span>
                )}
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  aria-label={t("escalation.fields.removePerson", { name: text })}
                  onClick={() =>
                    onChange({ ...step, userIds: step.userIds.filter((x) => x !== userId) })
                  }
                >
                  <XIcon aria-hidden="true" />
                </Button>
              </li>
            );
          })}
        </ul>
      )}
      <NativeSelect
        id={`${id}-person`}
        className="w-full sm:w-64"
        value=""
        onChange={(e) => {
          if (e.target.value !== "") {
            onChange({ ...step, userIds: [...step.userIds, e.target.value] });
          }
        }}
      >
        <NativeSelectOption value="">{t("escalation.fields.addPerson")}</NativeSelectOption>
        {rest.map((u) => (
          <NativeSelectOption key={u.id} value={u.id}>
            {`${u.name} (@${u.login})`}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </div>
  );
}

function ScheduleSelect({
  id,
  step,
  choices,
  issues,
  onChange,
}: Pick<StepFieldsProps, "id" | "step" | "choices" | "issues" | "onChange">) {
  const { t } = useTranslation();
  const error = issues.find((x) => x.field === "schedule_id");
  const known = choices.schedules.some((s) => s.id === step.scheduleId);
  const stored = choices.lookups.schedules.get(step.scheduleId);
  return (
    <Field
      id={`${id}-schedule`}
      label={t("escalation.fields.schedule")}
      error={error === undefined ? undefined : stepIssueText(t, error)}
    >
      <NativeSelect
        id={`${id}-schedule`}
        className="w-full sm:w-72"
        value={step.scheduleId}
        aria-invalid={error !== undefined}
        aria-describedby={error === undefined ? undefined : `${id}-schedule-error`}
        onChange={(e) => onChange({ ...step, scheduleId: e.target.value })}
      >
        <NativeSelectOption value="">{t("escalation.fields.chooseSchedule")}</NativeSelectOption>
        {!known && step.scheduleId !== "" && (
          <NativeSelectOption value={step.scheduleId}>
            {stored === undefined ? step.scheduleId : `${stored.team} · ${stored.name}`}
          </NativeSelectOption>
        )}
        {choices.schedules.map((s) => (
          <NativeSelectOption key={s.id} value={s.id}>
            {`${s.team.name} · ${s.name}`}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </Field>
  );
}

function ImportanceSelect({
  id,
  step,
  onChange,
}: Pick<StepFieldsProps, "id" | "step" | "onChange">) {
  const { t } = useTranslation();
  return (
    <Field id={`${id}-importance`} label={t("escalation.fields.importance")}>
      <NativeSelect
        id={`${id}-importance`}
        className="w-full sm:w-48"
        value={step.importance}
        onChange={(e) =>
          onChange({ ...step, importance: e.target.value === "important" ? "important" : "normal" })
        }
      >
        <NativeSelectOption value="normal">{t("escalation.normal")}</NativeSelectOption>
        <NativeSelectOption value="important">{t("escalation.important")}</NativeSelectOption>
      </NativeSelect>
    </Field>
  );
}

function TeamTargetFields({
  id,
  step,
  choices,
  owningTeam,
  issues,
  onChange,
}: Pick<StepFieldsProps, "id" | "step" | "choices" | "owningTeam" | "issues" | "onChange">) {
  const { t } = useTranslation();
  const teamError = issues.find((x) => x.field === "team_id");
  const targets: TeamTarget[] = ["team", ...EVERYONE_CHOICES];
  return (
    <>
      <Field id={`${id}-target`} label={t("escalation.fields.whom")}>
        <NativeSelect
          id={`${id}-target`}
          className="w-full sm:w-72"
          value={step.target}
          onChange={(e) => {
            const next = targets.find((x) => x === e.target.value);
            if (next !== undefined) {
              onChange({ ...step, target: next });
            }
          }}
        >
          {targets.map((target) => {
            const off = unavailableFor(choices, target);
            const reason = off?.reason ?? null;
            return (
              <NativeSelectOption
                key={target}
                value={target}
                disabled={reason !== null && step.target !== target}
              >
                {reason === null || off === null
                  ? targetName(t, target)
                  : t("escalation.fields.unavailableChoice", {
                      choice: targetName(t, target),
                      reason: reasonText(
                        t,
                        reason,
                        off.type,
                        target === "team" ? undefined : target,
                      ),
                    })}
              </NativeSelectOption>
            );
          })}
        </NativeSelect>
      </Field>
      {step.target === "team" && (
        <Field
          id={`${id}-team`}
          label={t("escalation.fields.team")}
          error={teamError === undefined ? undefined : stepIssueText(t, teamError)}
        >
          <NativeSelect
            id={`${id}-team`}
            className="w-full sm:w-64"
            value={step.teamId}
            aria-invalid={teamError !== undefined}
            aria-describedby={teamError === undefined ? undefined : `${id}-team-error`}
            onChange={(e) => onChange({ ...step, teamId: e.target.value })}
          >
            <NativeSelectOption value="">
              {owningTeam === undefined
                ? t("escalation.fields.chooseTeam")
                : t("escalation.fields.owningTeam", { team: owningTeam.name })}
            </NativeSelectOption>
            {choices.teams.map((team) => (
              <NativeSelectOption key={team.id} value={team.id}>
                {team.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
      )}
    </>
  );
}

function StepFields(props: StepFieldsProps) {
  const { id, step, choices, issues, onChange } = props;
  const { t } = useTranslation();
  const issue = (field: string) => issues.find((x) => x.field === field);
  const text = (field: string) => {
    const found = issue(field);
    return found === undefined ? undefined : stepIssueText(t, found);
  };
  const common = { id, step, choices, issues, onChange };
  switch (step.kind) {
    case "page_on_call":
      return (
        <div className="flex flex-wrap gap-3">
          <ScheduleSelect {...common} />
          <ImportanceSelect id={id} step={step} onChange={onChange} />
        </div>
      );
    case "page_people":
      return (
        <div className="flex flex-wrap gap-3">
          <PeoplePicker {...common} />
          <ImportanceSelect id={id} step={step} onChange={onChange} />
        </div>
      );
    case "page_team":
      return (
        <div className="flex flex-wrap gap-3">
          <TeamTargetFields {...common} owningTeam={props.owningTeam} />
        </div>
      );
    case "call":
      return (
        <div className="flex flex-wrap gap-3">
          <Field id={`${id}-call-target`} label={t("escalation.fields.callTarget")}>
            <NativeSelect
              id={`${id}-call-target`}
              className="w-full sm:w-64"
              value={step.callTarget}
              onChange={(e) =>
                onChange({
                  ...step,
                  callTarget: e.target.value === "people" ? "people" : "on_call",
                })
              }
            >
              <NativeSelectOption value="on_call">
                {t("escalation.fields.callOnCall")}
              </NativeSelectOption>
              <NativeSelectOption value="people">
                {t("escalation.fields.callPeople")}
              </NativeSelectOption>
            </NativeSelect>
          </Field>
          {step.callTarget === "on_call" ? (
            <ScheduleSelect {...common} />
          ) : (
            <PeoplePicker {...common} />
          )}
          {choices.gateways.length > 1 && (
            <Field
              id={`${id}-gateway`}
              label={t("escalation.fields.gateway")}
              error={text("gateway_id")}
            >
              <NativeSelect
                id={`${id}-gateway`}
                className="w-full sm:w-64"
                value={step.gatewayId}
                aria-invalid={issue("gateway_id") !== undefined}
                aria-describedby={
                  issue("gateway_id") === undefined ? undefined : `${id}-gateway-error`
                }
                onChange={(e) => onChange({ ...step, gatewayId: e.target.value })}
              >
                <NativeSelectOption value="">
                  {t("escalation.fields.chooseGateway")}
                </NativeSelectOption>
                {choices.gateways.map((g) => (
                  <NativeSelectOption key={g.id} value={g.id}>
                    {g.name}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </Field>
          )}
        </div>
      );
    case "wait":
      return (
        <Field
          id={`${id}-minutes`}
          label={t("escalation.fields.minutes")}
          error={text("wait_seconds")}
        >
          <Input
            id={`${id}-minutes`}
            type="number"
            inputMode="numeric"
            min={1}
            max={WAIT_MAX_MINUTES}
            step={1}
            className="w-28"
            value={step.minutes}
            aria-invalid={issue("wait_seconds") !== undefined}
            aria-describedby={
              issue("wait_seconds") === undefined ? undefined : `${id}-minutes-error`
            }
            onChange={(e) => onChange({ ...step, minutes: e.target.value })}
          />
        </Field>
      );
    case "webhook": {
      const known = choices.webhooks.some((d) => d.id === step.destinationId);
      return (
        <Field
          id={`${id}-destination`}
          label={t("escalation.fields.webhook")}
          error={text("destination_id")}
        >
          <NativeSelect
            id={`${id}-destination`}
            className="w-full sm:w-72"
            value={step.destinationId}
            aria-invalid={issue("destination_id") !== undefined}
            aria-describedby={
              issue("destination_id") === undefined ? undefined : `${id}-destination-error`
            }
            onChange={(e) => onChange({ ...step, destinationId: e.target.value })}
          >
            <NativeSelectOption value="">{t("escalation.fields.chooseWebhook")}</NativeSelectOption>
            {!known && step.destinationId !== "" && (
              <NativeSelectOption value={step.destinationId}>
                {choices.lookups.destinations.get(step.destinationId) ?? step.destinationId}
              </NativeSelectOption>
            )}
            {choices.webhooks.map((d) => (
              <NativeSelectOption key={d.id} value={d.id}>
                {d.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
      );
    }
    case "repeat":
      return (
        <div className="flex flex-wrap gap-3">
          <Field
            id={`${id}-from`}
            label={t("escalation.fields.repeatFrom")}
            error={text("repeat_from")}
          >
            <Input
              id={`${id}-from`}
              type="number"
              inputMode="numeric"
              min={1}
              step={1}
              className="w-24"
              value={step.repeatFrom}
              aria-invalid={issue("repeat_from") !== undefined}
              aria-describedby={issue("repeat_from") === undefined ? undefined : `${id}-from-error`}
              onChange={(e) => onChange({ ...step, repeatFrom: e.target.value })}
            />
          </Field>
          <Field
            id={`${id}-times`}
            label={t("escalation.fields.repeatTimes")}
            error={text("repeat_times")}
          >
            <Input
              id={`${id}-times`}
              type="number"
              inputMode="numeric"
              min={1}
              max={REPEAT_MAX_TIMES}
              step={1}
              className="w-24"
              value={step.repeatTimes}
              aria-invalid={issue("repeat_times") !== undefined}
              aria-describedby={
                issue("repeat_times") === undefined ? undefined : `${id}-times-error`
              }
              onChange={(e) => onChange({ ...step, repeatTimes: e.target.value })}
            />
          </Field>
        </div>
      );
    default:
      // A kind a later version added shows by its kind and has no fields.
      return null;
  }
}

function LimitFields({
  id,
  step,
  chips,
  onChange,
}: {
  id: string;
  step: StepDraft;
  chips: ChipHours;
  onChange: (next: StepDraft) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
      <div className="flex items-center gap-2">
        <input
          id={`${id}-hours`}
          type="checkbox"
          className="size-4 accent-primary"
          checked={step.workingHoursOnly}
          onChange={(e) => onChange({ ...step, workingHoursOnly: e.target.checked })}
        />
        <Label htmlFor={`${id}-hours`} className="leading-snug">
          {chips.team === undefined
            ? t("escalation.fields.hoursOrganization")
            : t("escalation.fields.hoursTeam", { team: chips.team })}
        </Label>
      </div>
      <Field id={`${id}-severity`} label={t("escalation.fields.severity")}>
        <NativeSelect
          id={`${id}-severity`}
          className="w-full sm:w-56"
          value={step.severity}
          onChange={(e) =>
            onChange({
              ...step,
              severity:
                e.target.value === "critical"
                  ? "critical"
                  : e.target.value === "below_critical"
                    ? "below_critical"
                    : "any",
            })
          }
        >
          <NativeSelectOption value="any">{t("escalation.runFor.any")}</NativeSelectOption>
          <NativeSelectOption value="critical">
            {t("escalation.runFor.critical")}
          </NativeSelectOption>
          <NativeSelectOption value="below_critical">
            {t("escalation.runFor.belowCritical")}
          </NativeSelectOption>
        </NativeSelect>
      </Field>
    </div>
  );
}

export interface EscalationStepsEditorProps {
  steps: StepDraft[];
  /** Undefined shows the steps only: a Responder reads them. */
  onChange?: (next: StepDraft[]) => void;
  /** The steps as stored: what they name is known without a read. */
  stored: readonly EscalationStep[];
  /** The Destinations the chain writes in, as stored. */
  destinations: readonly DestinationRef[];
  /** The owning Team as the form holds it. */
  owningTeam: { id: string; name: string } | undefined;
  issues?: StepIssue[];
}

export function EscalationStepsEditor({
  steps,
  onChange,
  stored,
  destinations,
  owningTeam,
  issues = [],
}: EscalationStepsEditorProps) {
  const { t } = useTranslation();
  const id = useId();
  const editing = onChange !== undefined;
  const choices = useStepChoices(stored, destinations, owningTeam?.name, editing);
  const chips = useChipHours(owningTeam?.id ?? "", owningTeam?.name);
  const noGateway = choices.gatewaysSettled && choices.gateways.length === 0;
  const replace = (index: number, next: StepDraft) =>
    onChange?.(steps.map((s, i) => (i === index ? next : s)));
  const move = (index: number, by: -1 | 1) => {
    const next = [...steps];
    const other = next[index + by];
    const own = next[index];
    if (other === undefined || own === undefined) {
      return;
    }
    next[index + by] = own;
    next[index] = other;
    onChange?.(next);
  };
  const add = (kind: EscalationStepKind) => onChange?.([...steps, newStep(kind, steps.length + 1)]);
  const whole = issues.find((x) => x.index === -1);
  return (
    <section
      aria-labelledby={`${id}-title`}
      className="flex min-w-0 flex-col gap-3"
      data-testid="chain-steps"
    >
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 id={`${id}-title`} className="text-lg font-semibold">
          {t("escalation.steps.title")}
        </h2>
        <DocsLink to="field.chain_steps" kind="learn_more" about={t("docs.fields.chainSteps")} />
      </div>
      <p className="text-sm text-muted-foreground" data-testid="chain-steps-hint">
        {t("escalation.steps.limitsHint")}
      </p>
      <div
        className="rounded-lg border bg-muted/30 px-3 py-2 text-sm"
        data-testid="chain-importance"
      >
        <span className="font-medium">{t("escalation.steps.importanceTitle")}. </span>
        <span className="text-muted-foreground">{t("escalation.steps.importanceText")}</span>
      </div>
      {whole !== undefined && (
        <p className="text-sm text-destructive" role="alert">
          {stepIssueText(t, whole)}
        </p>
      )}
      {steps.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="chain-steps-empty">
          {t("escalation.steps.empty")}
        </p>
      ) : (
        <ol className="flex flex-col gap-3" data-testid="chain-step-list">
          {steps.map((step, index) => {
            const own = issues.filter((x) => x.index === index);
            const stepId = `${id}-${step.key}`;
            const chipTexts = limitChips(t, step, chips);
            return (
              <li
                key={step.key}
                className="flex min-w-0 flex-col gap-3 rounded-lg border px-3 py-3"
                data-testid="chain-step"
                data-kind={step.kind}
              >
                <div className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-1">
                  <span
                    className="rounded-md bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary"
                    data-testid="chain-step-number"
                  >
                    {t("escalation.steps.number", { number: index + 1 })}
                  </span>
                  <span className="min-w-0 font-medium wrap-anywhere" data-testid="chain-step-text">
                    {stepText(t, step, choices.lookups)}
                  </span>
                </div>
                {chipTexts.length > 0 && (
                  <ul className="flex flex-wrap gap-1.5" data-testid="chain-step-chips">
                    {chipTexts.map((chip) => (
                      <li
                        key={chip}
                        className="max-w-full rounded-md bg-muted px-1.5 py-0.5 text-xs wrap-anywhere"
                      >
                        {chip}
                      </li>
                    ))}
                  </ul>
                )}
                {editing && (
                  <>
                    <StepFields
                      id={stepId}
                      step={step}
                      choices={choices}
                      owningTeam={owningTeam}
                      issues={own}
                      onChange={(next) => replace(index, next)}
                    />
                    {takesLimits(step.kind) && (
                      <LimitFields
                        id={stepId}
                        step={step}
                        chips={chips}
                        onChange={(next) => replace(index, next)}
                      />
                    )}
                    {own
                      .filter((x) => x.field === "" || x.field === "kind")
                      .map((x) => (
                        <p key={x.code} className="text-sm text-destructive" role="alert">
                          {stepIssueText(t, x)}
                        </p>
                      ))}
                    <div className="flex flex-wrap gap-1">
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        disabled={index === 0}
                        aria-label={t("escalation.steps.moveUp", { number: index + 1 })}
                        onClick={() => move(index, -1)}
                      >
                        <ArrowUpIcon aria-hidden="true" />
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        disabled={index === steps.length - 1}
                        aria-label={t("escalation.steps.moveDown", { number: index + 1 })}
                        onClick={() => move(index, 1)}
                      >
                        <ArrowDownIcon aria-hidden="true" />
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        aria-label={t("escalation.steps.remove", { number: index + 1 })}
                        onClick={() => onChange(steps.filter((_, i) => i !== index))}
                      >
                        <XIcon aria-hidden="true" />
                        {t("escalation.steps.removeAction")}
                      </Button>
                    </div>
                  </>
                )}
              </li>
            );
          })}
        </ol>
      )}
      {editing && (
        <fieldset className="flex min-w-0 flex-col gap-2" data-testid="chain-add-step">
          <legend className="mb-1 text-sm font-medium">{t("escalation.steps.add")}</legend>
          <div className="flex flex-wrap gap-2">
            {STEP_KINDS.map((kind) => {
              // The call is greyed but stays focusable, and its reason is the note below, which it points at.
              const greyed = kind === "call" && noGateway;
              return (
                <Button
                  key={kind}
                  type="button"
                  variant="outline"
                  size="sm"
                  data-kind={kind}
                  className="aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
                  disabled={steps.length >= STEPS_MAX}
                  aria-disabled={greyed ? true : undefined}
                  aria-describedby={greyed ? `${id}-gateway-note` : undefined}
                  onClick={() => {
                    if (!greyed) {
                      add(kind);
                    }
                  }}
                >
                  <PlusIcon aria-hidden="true" />
                  {kindName(t, kind)}
                </Button>
              );
            })}
          </div>
          {noGateway && (
            <div id={`${id}-gateway-note`}>
              <GatewayUnavailable />
            </div>
          )}
        </fieldset>
      )}
    </section>
  );
}
