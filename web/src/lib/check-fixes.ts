// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The "How to fix" links of the Connection and Destination checks (C-21.FR-11): the entry of docs-links.json for each
// code a step or a check can fail with and for each warning of a Connection check, as reference.md's "Next to problems"
// lists them. A code is linked for every type, for some types, or for none: request_failed names an error no page can
// foresee and webhook_set is a step that passed. A code that reference.md gives no row for a type has no link for it
// until the maintainer settles it: Telegram unexpected_status; Bridge wrong_path_prefix and unexpected_status; Slack
// unexpected_status. Each table names every code of its enum, so a code the API gains does not compile until it gets a
// row or joins the codes without one.

import type { TFunction } from "i18next";

import type {
  ConnectionCheckResultWarningsItem,
  ConnectionType,
  DestinationType,
  NullableConnectionCheckCode,
  NullableDestinationCheckCode,
} from "../api/gen/model";
import type { DocsKey } from "./docs";

/** A code of a step of the Connection check. */
export type ConnectionCheckCode = NonNullable<NullableConnectionCheckCode>;

/** A code of a check of the Destination check. */
export type DestinationCheckCode = NonNullable<NullableDestinationCheckCode>;

/** The fix of a code: one key for every type, a key for some types, or null for a code without a link. */
type Fix = DocsKey | Partial<Record<ConnectionType, DocsKey>> | null;

/** The guide to a token for each messenger: where its bot, or a bridge's token, is made. */
const TOKEN = {
  mattermost: "fix.token.mattermost",
  telegram: "fix.token.telegram",
  slack: "fix.token.slack",
  bridge: "fix.token.bridge",
} as const satisfies Record<ConnectionType, DocsKey>;

const CONNECTION_FIXES = {
  token_invalid: TOKEN,
  dns_failed: "fix.network",
  connect_failed: "fix.network",
  tls_failed: "fix.network",
  timeout: "fix.network",
  proxy_refused: "fix.network",
  blocked: "fix.blocked",
  not_bot_api: { telegram: "fix.telegram_base_url" },
  wrong_path_prefix: { telegram: "fix.telegram_base_url", mattermost: "fix.mattermost_server" },
  unexpected_status: { mattermost: "fix.mattermost_server" },
  not_mattermost: { mattermost: "fix.mattermost_server" },
  wrong_token_type: { slack: "fix.slack_tokens" },
  app_mismatch: { slack: "fix.slack_tokens" },
  not_slack: { slack: "fix.slack_tokens" },
  missing_scope: { slack: "fix.slack_scopes" },
  not_bridge: { bridge: "fix.bridge_check" },
  protocol_version_unknown: { bridge: "fix.bridge_check" },
  protocol_mismatch: { bridge: "fix.bridge_check" },
  event_delivery_not_offered: { bridge: "fix.bridge_check" },
  request_failed: null,
  webhook_set: null,
} as const satisfies Record<ConnectionCheckCode, Fix>;

const DESTINATION_FIXES = {
  token_invalid: TOKEN,
  dns_failed: "fix.network",
  connect_failed: "fix.network",
  tls_failed: "fix.network",
  timeout: "fix.network",
  proxy_refused: "fix.network",
  blocked: "fix.blocked",
  bot_not_in_channel: { mattermost: "fix.mattermost_destination", slack: "fix.slack_destination" },
  channel_in_other_team: { mattermost: "fix.mattermost_destination" },
  channel_archived: { mattermost: "fix.mattermost_destination", slack: "fix.slack_destination" },
  channel_not_found: { telegram: "fix.telegram_destination", slack: "fix.slack_destination" },
  not_a_channel: { telegram: "fix.telegram_destination" },
  changed_since_saved: { telegram: "fix.telegram_destination" },
  comments_disabled: { telegram: "fix.telegram_comments" },
  bot_not_channel_admin: { telegram: "fix.telegram_admin" },
  bot_may_not_post: { telegram: "fix.telegram_admin" },
  bot_may_not_edit: { telegram: "fix.telegram_admin" },
  bot_not_group_admin: { telegram: "fix.telegram_admin" },
  missing_scope: { slack: "fix.slack_scopes" },
  channel_forbidden: { bridge: "fix.bridge_destination" },
  request_failed: null,
} as const satisfies Record<DestinationCheckCode, Fix>;

const WARNING_FIXES = {
  press_answers_in_thread: "fix.press_answers_in_thread",
  another_client_connected: "fix.another_client_connected",
} as const satisfies Record<ConnectionCheckResultWarningsItem, DocsKey>;

/** The codes reference.md lists as having no link. */
export const CODES_WITHOUT_LINK: readonly string[] = ["request_failed", "webhook_set"];

function pick(
  fix: CheckFixKey | Partial<Record<ConnectionType, CheckFixKey>> | null | undefined,
  type: ConnectionType,
): CheckFixKey | undefined {
  if (fix === null || fix === undefined) {
    return undefined;
  }
  return typeof fix === "string" ? fix : fix[type];
}

/** The guide of a code of a Connection check of a type, or none. */
export function connectionCheckFix(
  type: ConnectionType,
  code: NullableConnectionCheckCode | undefined,
): CheckFixKey | undefined {
  return code === null || code === undefined ? undefined : pick(CONNECTION_FIXES[code], type);
}

/**
 * The guide of a code of a Destination check, by the type of the Destination, which is its Connection's: a refused
 * token takes the token guide of that messenger. The outgoing webhook has no Destination check.
 */
export function destinationCheckFix(
  type: DestinationType,
  code: NullableDestinationCheckCode | undefined,
): CheckFixKey | undefined {
  if (type === "webhook" || code === null || code === undefined) {
    return undefined;
  }
  return pick(DESTINATION_FIXES[code], type);
}

/** The guide of a warning of a Connection check. */
export function checkWarningFix(
  warning: ConnectionCheckResultWarningsItem,
): CheckFixKey | undefined {
  return WARNING_FIXES[warning];
}

type Leaf<V> = V extends string ? V : V extends Record<string, infer K> ? K : never;

/** A key of a guide that a check's code or warning links to. */
export type CheckFixKey = Leaf<
  | (typeof CONNECTION_FIXES)[ConnectionCheckCode]
  | (typeof DESTINATION_FIXES)[DestinationCheckCode]
  | (typeof WARNING_FIXES)[ConnectionCheckResultWarningsItem]
>;

/** What the guide of a check's code or warning is about, which the accessible name of its "How to fix" adds. */
const CHECK_FIX_ABOUT = {
  "fix.token.mattermost": (t) => t("docs.fixes.token"),
  "fix.token.telegram": (t) => t("docs.fixes.token"),
  "fix.token.slack": (t) => t("docs.fixes.token"),
  "fix.token.bridge": (t) => t("docs.fixes.token"),
  "fix.network": (t) => t("docs.fixes.network"),
  "fix.blocked": (t) => t("docs.fixes.blocked"),
  "fix.telegram_base_url": (t) => t("docs.fixes.telegramBaseUrl"),
  "fix.mattermost_server": (t) => t("docs.fixes.mattermostServer"),
  "fix.slack_tokens": (t) => t("docs.fixes.slackTokens"),
  "fix.slack_scopes": (t) => t("docs.fixes.slackScopes"),
  "fix.bridge_check": (t) => t("docs.fixes.bridgeCheck"),
  "fix.press_answers_in_thread": (t) => t("docs.fixes.pressAnswersInThread"),
  "fix.another_client_connected": (t) => t("docs.fixes.anotherClientConnected"),
  "fix.mattermost_destination": (t) => t("docs.fixes.destinationChannel"),
  "fix.slack_destination": (t) => t("docs.fixes.destinationChannel"),
  "fix.bridge_destination": (t) => t("docs.fixes.destinationChannel"),
  "fix.telegram_destination": (t) => t("docs.fixes.telegramChannel"),
  "fix.telegram_comments": (t) => t("docs.fixes.telegramComments"),
  "fix.telegram_admin": (t) => t("docs.fixes.telegramAdmin"),
} satisfies Record<CheckFixKey, (t: TFunction) => string>;

/** The words a check's "How to fix" adds to its accessible name: "How to fix: Comments are not enabled". */
export function checkFixAbout(t: TFunction, key: CheckFixKey): string {
  return CHECK_FIX_ABOUT[key](t);
}
