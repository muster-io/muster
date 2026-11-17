// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Notification rule in words (C-31.FR-5): the actions between two waits happen at the same moment, so a rule reads as
// its first moment and then each later moment "after N minutes". A rule that is one Mention reads "a Mention in the
// Thread of the Root message"; a Mention and the same Mention again after a wait reads "a Mention in the Thread and
// again after 5 minutes if you did not answer"; a call reads "a call to {contact}", or "a call to Notification gateway
// (unavailable)" for an action whose contact or gateway is gone.

import type { TFunction } from "i18next";

import type { GatewayContact, NotificationRuleAction } from "../api/gen/model";

/** The actions that happen together, and the minutes waited before them. */
interface Moment {
  wait: number;
  actions: NotificationRuleAction[];
}

function moments(rule: readonly NotificationRuleAction[]): Moment[] {
  const out: Moment[] = [];
  let wait = 0;
  let open: Moment | null = null;
  for (const action of rule) {
    if (action.kind === "wait") {
      wait += Math.max(1, Math.round((action.wait_seconds ?? 0) / 60));
      open = null;
      continue;
    }
    if (open === null) {
      open = { wait, actions: [] };
      out.push(open);
      wait = 0;
    }
    open.actions.push(action);
  }
  return out;
}

/** The id of the gateway a call names, from its read form or its request form. */
export function callGatewayId(action: NotificationRuleAction): string | undefined {
  return action.gateway?.id ?? action.gateway_id;
}

/** The confirmed contact of a person at a gateway, if any. */
export function confirmedContact(
  contacts: readonly GatewayContact[],
  gatewayId: string | undefined,
): GatewayContact | undefined {
  return contacts.find((c) => c.confirmed && c.connection.id === gatewayId);
}

function callWords(
  t: TFunction,
  action: NotificationRuleAction,
  contacts: readonly GatewayContact[] | null,
): string {
  const contact = contacts === null ? undefined : confirmedContact(contacts, callGatewayId(action));
  if (contact !== undefined) {
    return t("notificationRules.words.callTo", { contact: contact.contact });
  }
  if (contacts === null && action.gateway?.name !== undefined) {
    return t("notificationRules.words.callThrough", { gateway: action.gateway.name });
  }
  if (contacts === null && action.available !== false) {
    return t("notificationRules.words.callThroughAny");
  }
  return t("notificationRules.words.callGone");
}

function momentWords(
  t: TFunction,
  actions: readonly NotificationRuleAction[],
  contacts: readonly GatewayContact[] | null,
): string {
  const mentions = actions.filter((a) => a.kind === "mention").length;
  const parts: string[] = [];
  if (mentions > 0) {
    parts.push(t("notificationRules.words.mention", { count: mentions }));
  }
  for (const action of actions) {
    if (action.kind === "call") {
      parts.push(callWords(t, action, contacts));
    }
  }
  return parts.join(` ${t("notificationRules.words.and")} `);
}

function isMention(m: Moment): boolean {
  return m.actions.length === 1 && m.actions[0]?.kind === "mention";
}

/**
 * A rule in words, lower case and without a full stop. `contacts` are the confirmed contacts of the person the rule
 * belongs to, which name the number a call goes to; null for a rule that belongs to nobody, the Organization's, whose
 * calls name their gateway.
 */
export function ruleWords(
  t: TFunction,
  rule: readonly NotificationRuleAction[],
  contacts: readonly GatewayContact[] | null,
): string {
  const all = moments(rule);
  const first = all[0];
  if (first === undefined) {
    return "";
  }
  if (all.length === 1 && first.wait === 0 && isMention(first)) {
    return t("notificationRules.words.mentionRoot");
  }
  let text = momentWords(t, first.actions, contacts);
  if (first.actions.length > 1) {
    text = `${text} ${t("notificationRules.words.sameTime")}`;
  }
  if (first.wait > 0) {
    text = t("notificationRules.words.then", { what: text, count: first.wait });
  }
  const later = all.slice(1);
  if (later.length === 0) {
    return text;
  }
  // A lone Mention is "again" only after a moment that had a Mention.
  const mentioned = first.actions.some((a) => a.kind === "mention");
  const pieces = later.map((m) =>
    isMention(m) && mentioned
      ? t("notificationRules.words.again", { count: m.wait })
      : t("notificationRules.words.then", {
          what: momentWords(t, m.actions, contacts),
          count: m.wait,
        }),
  );
  const repeats = isMention(first) && first.wait === 0 && later.every(isMention);
  if (repeats) {
    return t("notificationRules.words.unanswered", {
      text: t("notificationRules.words.repeat", { first: text, rest: pieces.join(", ") }),
    });
  }
  return `${text}, ${pieces.join(", ")}`;
}
