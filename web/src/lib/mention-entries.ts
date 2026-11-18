// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Mention entries of a Destination (C-28.FR-1, FR-2): the values the form edits, what each type of Destination
// offers for a new entry, and the words of targets, events, unavailable reasons and what an entry resolves to now. The
// server's `available` and `unavailable_reason` decide for an entry that is stored; this file computes only what the
// "Add" list shows for a new one, from the Destination's type and what its Connection last found, and the server
// repeats the check on save.

import type { TFunction } from "i18next";

import type {
  Connection,
  ConnectionType,
  Destination,
  DestinationType,
  MentionEntry,
  MentionEntryInput,
  MentionEvent,
  MentionNotDeliveredReason,
  MentionResolution,
  MentionTarget,
  MentionTargetInputEveryone,
  MentionTargetKind,
  MentionUnavailableReason,
} from "../api/gen/model";
import { fieldErrorText } from "./api";

/** The Loud events of an entry, in the order of the grid's columns. */
export const MENTION_EVENTS = [
  "new_alert_group",
  "new_alerts",
  "reopen",
  "ack_timeout",
  "snooze_ended",
  "rise_to_urgent",
  "storm_started",
] as const satisfies readonly MentionEvent[];

/** The most entries a Destination takes (C-28.FR-6). */
export const MAX_MENTION_ENTRIES = 100;

/** The words of everyone in the chat, in the order of the list. */
export const EVERYONE_WORDS = [
  "channel",
  "here",
  "all",
  "everyone",
] as const satisfies readonly MentionTargetInputEveryone[];

/** An entry as the form holds it: its target, with the User or the Team it names, and its Loud events. */
export interface MentionEntryValue {
  target: MentionTarget;
  events: MentionEvent[];
}

/** The entries of a stored Destination, as the form edits them. */
export function entryValues(d: Destination | undefined): MentionEntryValue[] {
  const read: readonly MentionEntry[] = d?.mention_entries ?? [];
  return read.map((e) => ({
    target: e.target,
    events: MENTION_EVENTS.filter((event) => e.events.includes(event)),
  }));
}

/** The target as the request writes it: the fields of its kind only. */
function targetInput(target: MentionTarget): MentionEntryInput["target"] {
  switch (target.kind) {
    case "everyone":
      return { kind: "everyone", everyone: target.everyone };
    case "user":
      return { kind: "user", user_id: target.user_id };
    case "group":
      return target.group_id === undefined
        ? { kind: "group", group: target.group }
        : { kind: "group", group: target.group, group_id: target.group_id };
    case "team":
    case "on_call_of_team":
      return { kind: target.kind, team_id: target.team_id };
    default:
      return { kind: target.kind };
  }
}

/** The entries of a save. */
export function entryInputs(values: readonly MentionEntryValue[]): MentionEntryInput[] {
  return values.map((v) => ({
    target: targetInput(v.target),
    events: MENTION_EVENTS.filter((event) => v.events.includes(event)),
  }));
}

/** Identifies a target among the entries of a Destination, which list each target once. */
export function targetKey(target: MentionTarget): string {
  switch (target.kind) {
    case "everyone":
      return `everyone:${target.everyone}`;
    case "user":
      return `user:${target.user_id}`;
    case "group":
      return `group:${target.group_id ?? ""}:${target.group}`;
    case "team":
    case "on_call_of_team":
      return `${target.kind}:${target.team_id}`;
    default:
      return target.kind;
  }
}

/** What the "Add" list needs to know of the Destination to say which targets it can offer. */
export interface MentionAbilities {
  type: DestinationType;
  /** The Connection of the Destination once read; unknown abilities are taken as present, the server decides. */
  connection?: Connection;
}

function bridgeInfo(connection: Connection | undefined) {
  return connection?.type === "bridge" ? connection : undefined;
}

/** Why the Destination's Connection cannot mention a target now; null when it can or is not known to fail. */
export function unavailableReason(
  abilities: MentionAbilities,
  target: { kind: MentionTargetKind; everyone?: MentionTargetInputEveryone },
): MentionUnavailableReason | null {
  const { type, connection } = abilities;
  const group =
    target.kind === "group" || target.kind === "team" || target.kind === "teams_from_label";
  switch (type) {
    case "telegram":
      return target.kind === "everyone" || group ? "telegram_no_groups" : null;
    case "mattermost":
      return target.kind === "everyone" && target.everyone === "everyone"
        ? "messenger_has_no_token"
        : null;
    case "slack":
      if (target.kind === "everyone") {
        return target.everyone === "channel" || target.everyone === "here"
          ? null
          : "messenger_has_no_token";
      }
      if (group && connection?.type === "slack" && connection.usergroups_read !== true) {
        return "usergroups_read_missing";
      }
      return null;
    case "bridge": {
      const bridge = bridgeInfo(connection);
      if (bridge === undefined) {
        return null;
      }
      const capabilities = bridge.info?.capabilities;
      if (target.kind === "everyone") {
        return target.everyone !== undefined &&
          capabilities?.everyone_mentions?.includes(target.everyone) === true
          ? null
          : "bridge_not_declared";
      }
      return group && capabilities?.group_mentions !== true ? "bridge_no_group_mentions" : null;
    }
    default:
      return null;
  }
}

/** The name of a messenger as a sentence uses it. */
export function messengerName(t: TFunction, type: ConnectionType | DestinationType): string {
  switch (type) {
    case "mattermost":
      return t("mentions.messengers.mattermost");
    case "slack":
      return t("mentions.messengers.slack");
    case "telegram":
      return t("mentions.messengers.telegram");
    case "bridge":
      return t("mentions.messengers.bridge");
    default:
      return t("mentions.messengers.webhook");
  }
}

/** The text of a reason a target is unavailable, for the Destination of the given type. */
export function reasonText(
  t: TFunction,
  reason: MentionUnavailableReason,
  type: DestinationType,
  everyone?: MentionTargetInputEveryone,
): string {
  const token = everyone === undefined ? "" : `@${everyone}`;
  switch (reason) {
    case "telegram_no_groups":
      return t("mentions.reasons.telegramNoGroups");
    case "usergroups_read_missing":
      return t("mentions.reasons.usergroupsReadMissing");
    case "bridge_no_group_mentions":
      return t("mentions.reasons.bridgeNoGroupMentions");
    case "bridge_not_declared":
      return t("mentions.reasons.bridgeNotDeclared", { token });
    default:
      return t("mentions.reasons.messengerHasNoToken", {
        messenger: messengerName(t, type),
        token,
      });
  }
}

/** The text of a Loud event, as the column of the grid names it. */
export function eventName(t: TFunction, event: MentionEvent): string {
  switch (event) {
    case "new_alert_group":
      return t("mentions.events.newAlertGroup");
    case "new_alerts":
      return t("mentions.events.newAlerts");
    case "reopen":
      return t("mentions.events.reopen");
    case "ack_timeout":
      return t("mentions.events.ackTimeout");
    case "snooze_ended":
      return t("mentions.events.snoozeEnded");
    case "rise_to_urgent":
      return t("mentions.events.riseToUrgent");
    default:
      return t("mentions.events.stormStarted");
  }
}

function teamName(t: TFunction, team: { name: string; deleted: boolean }): string {
  return team.deleted ? t("mentions.deleted", { name: team.name }) : team.name;
}

/** The text of a target: "@here", "@bob", "Group @payments_team", "Team payments", "On-call of payments"… */
export function targetLabel(t: TFunction, target: MentionTarget): string {
  switch (target.kind) {
    case "everyone":
      return `@${target.everyone ?? ""}`;
    case "user": {
      const user = target.user;
      if (user === undefined) {
        return t("alertGroups.filters.ownerUnknown");
      }
      const login = user.login !== undefined && user.login !== "" ? `@${user.login}` : user.name;
      return user.deactivated ? t("alertGroups.deactivated", { name: login }) : login;
    }
    case "group":
      return t("mentions.targets.group", { name: `@${target.group ?? ""}` });
    case "team":
      return t("mentions.targets.team", {
        name: target.team === undefined ? "" : teamName(t, target.team),
      });
    case "teams_from_label":
      return t("mentions.targets.teamsFromLabel");
    case "on_call_of_team":
      return t("mentions.targets.onCallOfTeam", {
        name: target.team === undefined ? "" : teamName(t, target.team),
      });
    default:
      return t("mentions.targets.onCallOfTeamsFromLabel");
  }
}

function mentionsText(t: TFunction, resolution: MentionResolution): string {
  if (resolution.mentions.length === 0) {
    return t("mentions.nobody");
  }
  return resolution.mentions.map((m) => `@${m.name}`).join(", ");
}

/** The words after "now:" — what an entry mentions at this moment, as the server resolved it. */
export function resolvesToText(t: TFunction, language: string, entry: MentionEntry): string {
  const fromLabel =
    entry.target.kind === "teams_from_label" || entry.target.kind === "on_call_of_teams_from_label";
  if (!fromLabel) {
    const first = entry.resolves_to[0];
    return first === undefined ? t("mentions.nobody") : mentionsText(t, first);
  }
  const named = entry.resolves_to.filter((r) => r.team !== null && r.mentions.length > 0);
  const nobody = entry.resolves_to.filter((r) => r.team !== null && r.mentions.length === 0);
  const parts = named.map((r) =>
    t("mentions.teamMentions", { team: r.team?.name ?? "", mentions: mentionsText(t, r) }),
  );
  if (nobody.length > 0) {
    const names = new Intl.ListFormat(language, { style: "long", type: "conjunction" }).format(
      nobody.map((r) => r.team?.name ?? ""),
    );
    parts.push(t("mentions.teamMentions", { team: names, mentions: t("mentions.nobody") }));
  }
  return parts.length === 0 ? t("mentions.nobody") : parts.join("; ");
}

/**
 * The words of a warning's reason: "has no linked Mattermost account", "the bridge cannot mention people" or "the
 * message had no room for their Mention".
 */
export function notDeliveredReasonText(
  t: TFunction,
  reason: MentionNotDeliveredReason,
  messenger?: ConnectionType,
): string {
  if (reason === "bridge_cannot_mention_people") {
    return t("mentions.notDelivered.bridgeCannotMentionPeople");
  }
  if (reason === "message_too_long") {
    return t("mentions.notDelivered.messageTooLong");
  }
  return messenger === undefined
    ? t("mentions.notDelivered.noAccountLinkHere")
    : t("mentions.notDelivered.noAccountLink", { messenger: messengerName(t, messenger) });
}

/** A Mattermost group name or the handle of a Slack user group, as the server checks it. */
const GROUP_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

export function isGroupName(text: string): boolean {
  return GROUP_NAME.test(text);
}

/** The text of a refused entry. */
export function mentionErrorText(t: TFunction, code: string): string {
  switch (code) {
    case "mention_unsupported":
    case "unsupported":
      return t("mentions.errors.unsupported");
    case "unknown_id":
      return t("mentions.errors.unknownId");
    case "duplicate":
      return t("mentions.errors.duplicate");
    case "invalid_format":
      return t("mentions.errors.invalidGroup");
    case "required":
      return t("mentions.errors.required");
    default:
      return fieldErrorText(t, code);
  }
}

/** The position of the entry a pointer such as /mention_entries/2/target/user_id names, or undefined. */
export function entryIndexOf(pointer: string): number | undefined {
  const match = /^\/mention_entries\/(\d+)(\/|$)/.exec(pointer);
  return match === null ? undefined : Number(match[1]);
}

/** Whether a pointer is the entries or below them. */
export function isEntriesPointer(pointer: string): boolean {
  return pointer === "/mention_entries" || pointer.startsWith("/mention_entries/");
}
