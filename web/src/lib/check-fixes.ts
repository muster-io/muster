// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The "How to fix" links of the Connection and Destination checks (C-21.FR-11): the entry of docs-links.json for each
// code a step or a check can fail with and for each warning of a Connection check, as reference.md's "Next to problems"
// lists them. Each messenger names every code of the enum explicitly, so the type checker refuses a table that misses
// one: a key, null for the codes reference.md lists as having no link — request_failed, whose message names an error no
// page can foresee, and webhook_set, a step that passed — or NOT_GIVEN for a code that the messenger's check never
// gives. A code the API gains does not compile until every messenger names it.

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

/** A code that the check of a messenger never gives. */
export const NOT_GIVEN = "not_given";

/** What a code of a messenger's check links to: a key, null for a code without a link, or NOT_GIVEN. */
type Fix = DocsKey | null | typeof NOT_GIVEN;

/** The codes every check can give: an address that cannot be reached, the outbound address policy, any other error. */
const SHARED = {
  dns_failed: "fix.network",
  connect_failed: "fix.network",
  tls_failed: "fix.network",
  timeout: "fix.network",
  proxy_refused: "fix.network",
  blocked: "fix.blocked",
  request_failed: null,
} as const;

/** The guide of each code of the Connection check, by messenger. */
export const CONNECTION_FIXES = {
  mattermost: {
    ...SHARED,
    token_invalid: "fix.token.mattermost",
    not_mattermost: "fix.mattermost_server",
    wrong_path_prefix: "fix.mattermost_server",
    unexpected_status: "fix.mattermost_server",
    not_bot_api: NOT_GIVEN,
    webhook_set: NOT_GIVEN,
    not_bridge: NOT_GIVEN,
    protocol_version_unknown: NOT_GIVEN,
    protocol_mismatch: NOT_GIVEN,
    event_delivery_not_offered: NOT_GIVEN,
    wrong_token_type: NOT_GIVEN,
    missing_scope: NOT_GIVEN,
    app_mismatch: NOT_GIVEN,
    not_slack: NOT_GIVEN,
  },
  telegram: {
    ...SHARED,
    token_invalid: "fix.token.telegram",
    not_bot_api: "fix.telegram_base_url",
    wrong_path_prefix: "fix.telegram_base_url",
    unexpected_status: "fix.telegram_base_url",
    webhook_set: null,
    not_mattermost: NOT_GIVEN,
    not_bridge: NOT_GIVEN,
    protocol_version_unknown: NOT_GIVEN,
    protocol_mismatch: NOT_GIVEN,
    event_delivery_not_offered: NOT_GIVEN,
    wrong_token_type: NOT_GIVEN,
    missing_scope: NOT_GIVEN,
    app_mismatch: NOT_GIVEN,
    not_slack: NOT_GIVEN,
  },
  slack: {
    ...SHARED,
    token_invalid: "fix.token.slack",
    wrong_token_type: "fix.slack_tokens",
    app_mismatch: "fix.slack_tokens",
    missing_scope: "fix.slack_scopes",
    not_slack: "fix.slack_server",
    unexpected_status: "fix.slack_server",
    // Slack's address is fixed: its dry probe never names a path prefix.
    wrong_path_prefix: NOT_GIVEN,
    not_bot_api: NOT_GIVEN,
    webhook_set: NOT_GIVEN,
    not_mattermost: NOT_GIVEN,
    not_bridge: NOT_GIVEN,
    protocol_version_unknown: NOT_GIVEN,
    protocol_mismatch: NOT_GIVEN,
    event_delivery_not_offered: NOT_GIVEN,
  },
  bridge: {
    ...SHARED,
    token_invalid: "fix.token.bridge",
    not_bridge: "fix.bridge_server",
    wrong_path_prefix: "fix.bridge_server",
    unexpected_status: "fix.bridge_server",
    protocol_version_unknown: "fix.bridge_check",
    protocol_mismatch: "fix.bridge_check",
    event_delivery_not_offered: "fix.bridge_check",
    not_bot_api: NOT_GIVEN,
    webhook_set: NOT_GIVEN,
    not_mattermost: NOT_GIVEN,
    wrong_token_type: NOT_GIVEN,
    missing_scope: NOT_GIVEN,
    app_mismatch: NOT_GIVEN,
    not_slack: NOT_GIVEN,
  },
} as const satisfies Record<ConnectionType, Record<ConnectionCheckCode, Fix>>;

/** The guide of each code of the Destination check, by messenger; the outgoing webhook has no Destination check. */
export const DESTINATION_FIXES = {
  mattermost: {
    ...SHARED,
    token_invalid: "fix.token.mattermost",
    bot_not_in_channel: "fix.mattermost_destination",
    channel_in_other_team: "fix.mattermost_destination",
    channel_archived: "fix.mattermost_destination",
    channel_not_found: NOT_GIVEN,
    not_a_channel: NOT_GIVEN,
    comments_disabled: NOT_GIVEN,
    changed_since_saved: NOT_GIVEN,
    bot_not_channel_admin: NOT_GIVEN,
    bot_may_not_post: NOT_GIVEN,
    bot_may_not_edit: NOT_GIVEN,
    bot_not_group_admin: NOT_GIVEN,
    channel_forbidden: NOT_GIVEN,
    missing_scope: NOT_GIVEN,
  },
  telegram: {
    ...SHARED,
    token_invalid: "fix.token.telegram",
    channel_not_found: "fix.telegram_destination",
    not_a_channel: "fix.telegram_destination",
    changed_since_saved: "fix.telegram_destination",
    comments_disabled: "fix.telegram_comments",
    bot_not_channel_admin: "fix.telegram_admin",
    bot_may_not_post: "fix.telegram_admin",
    bot_may_not_edit: "fix.telegram_admin",
    bot_not_group_admin: "fix.telegram_admin",
    bot_not_in_channel: NOT_GIVEN,
    channel_in_other_team: NOT_GIVEN,
    channel_archived: NOT_GIVEN,
    channel_forbidden: NOT_GIVEN,
    missing_scope: NOT_GIVEN,
  },
  slack: {
    ...SHARED,
    token_invalid: "fix.token.slack",
    missing_scope: "fix.slack_scopes",
    bot_not_in_channel: "fix.slack_destination",
    channel_not_found: "fix.slack_destination",
    channel_archived: "fix.slack_destination",
    channel_in_other_team: NOT_GIVEN,
    not_a_channel: NOT_GIVEN,
    comments_disabled: NOT_GIVEN,
    changed_since_saved: NOT_GIVEN,
    bot_not_channel_admin: NOT_GIVEN,
    bot_may_not_post: NOT_GIVEN,
    bot_may_not_edit: NOT_GIVEN,
    bot_not_group_admin: NOT_GIVEN,
    channel_forbidden: NOT_GIVEN,
  },
  bridge: {
    ...SHARED,
    token_invalid: "fix.token.bridge",
    channel_forbidden: "fix.bridge_destination",
    bot_not_in_channel: NOT_GIVEN,
    channel_in_other_team: NOT_GIVEN,
    channel_archived: NOT_GIVEN,
    channel_not_found: NOT_GIVEN,
    not_a_channel: NOT_GIVEN,
    comments_disabled: NOT_GIVEN,
    changed_since_saved: NOT_GIVEN,
    bot_not_channel_admin: NOT_GIVEN,
    bot_may_not_post: NOT_GIVEN,
    bot_may_not_edit: NOT_GIVEN,
    bot_not_group_admin: NOT_GIVEN,
    missing_scope: NOT_GIVEN,
  },
} as const satisfies Record<ConnectionType, Record<DestinationCheckCode, Fix>>;

/** The guide of each warning of the Connection check. */
export const WARNING_FIXES = {
  press_answers_in_thread: "fix.press_answers_in_thread",
  another_client_connected: "fix.another_client_connected",
} as const satisfies Record<ConnectionCheckResultWarningsItem, DocsKey>;

/** The codes reference.md lists as having no link. */
export const CODES_WITHOUT_LINK: readonly string[] = ["request_failed", "webhook_set"];

type Values<T> = T[keyof T];

/** A key of a guide that a check's code or warning links to. */
export type CheckFixKey = Exclude<
  | Values<Values<typeof CONNECTION_FIXES>>
  | Values<Values<typeof DESTINATION_FIXES>>
  | Values<typeof WARNING_FIXES>,
  null | typeof NOT_GIVEN
>;

function linked(fix: CheckFixKey | null | typeof NOT_GIVEN): CheckFixKey | undefined {
  return fix === null || fix === NOT_GIVEN ? undefined : fix;
}

/** The guide of a code of a Connection check of a type, or none. */
export function connectionCheckFix(
  type: ConnectionType,
  code: NullableConnectionCheckCode | undefined,
): CheckFixKey | undefined {
  return code === null || code === undefined ? undefined : linked(CONNECTION_FIXES[type][code]);
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
  return linked(DESTINATION_FIXES[type][code]);
}

/** The guide of a warning of a Connection check. */
export function checkWarningFix(
  warning: ConnectionCheckResultWarningsItem,
): CheckFixKey | undefined {
  return WARNING_FIXES[warning];
}

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
  "fix.slack_server": (t) => t("docs.fixes.slackServer"),
  "fix.bridge_server": (t) => t("docs.fixes.bridgeServer"),
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
