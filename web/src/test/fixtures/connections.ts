// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Complete Connection reads for the web tests, one builder per Connection type. Every required field holds a neutral
// value — the bot token stored, no proxy, no Destinations, nothing a check finds yet, the limiter the form proposes,
// the callback address of the Connection's id — and a test passes, field by field, only what it cares about; the type
// stays the builder's. A required field added to a read is set here, the one place the TypeScript check names,
// instead of in every test that builds a Connection.

import type {
  BridgeConnection,
  MattermostConnection,
  SlackConnection,
  TelegramConnection,
} from "../../api/gen/model";

/** The fields a test may set: every field of the read but its type. */
type ConnectionOverrides<T extends { type: string }> = Partial<Omit<T, "type">>;

const CREATED_AT = "2026-10-13T09:00:00Z";

function callbackUrl(type: "mattermost" | "slack" | "bridge", id: string): string {
  return `http://127.0.0.1:8081/api/v1/callbacks/${type}/${id}`;
}

export function mattermostConnection(
  overrides: ConnectionOverrides<MattermostConnection> = {},
): MattermostConnection {
  const id = overrides.id ?? "CN0000000000MM";
  return {
    id,
    name: "Example Mattermost",
    server_url: "http://127.0.0.1:18065",
    limiter: { limit: 5, per_seconds: 1 },
    bot_token_status: { set: true, updated_at: CREATED_AT },
    proxy: { enabled: false },
    destination_count: 0,
    callback_url: callbackUrl("mattermost", id),
    created_at: CREATED_AT,
    etag: '"1"',
    ...overrides,
    type: "mattermost",
  };
}

export function telegramConnection(
  overrides: ConnectionOverrides<TelegramConnection> = {},
): TelegramConnection {
  return {
    id: "CN0000000000TG",
    name: "Example Telegram",
    bot_api_base_url: "http://127.0.0.1:18081",
    update_mode: "long_polling",
    limiter: { limit: 15, per_seconds: 1 },
    bot_token_status: { set: true, updated_at: CREATED_AT },
    proxy: { enabled: false },
    destination_count: 0,
    warnings: [],
    created_at: CREATED_AT,
    etag: '"1"',
    ...overrides,
    type: "telegram",
  };
}

/** A Slack Connection in Socket Mode before its first check: no bot, workspace or app known yet. */
export function slackConnection(
  overrides: ConnectionOverrides<SlackConnection> = {},
): SlackConnection {
  const id = overrides.id ?? "CN0000000000SL";
  return {
    id,
    name: "Example Slack",
    event_mode: "socket",
    limiter: { limit: 50, per_seconds: 60 },
    bot_token_status: { set: true, updated_at: CREATED_AT },
    app_token_status: { set: true, updated_at: CREATED_AT },
    slack_signing_secret_status: { set: false },
    callback_url: callbackUrl("slack", id),
    bot_name: null,
    bot_user_id: null,
    team_id: null,
    team_name: null,
    app_id: null,
    proxy: { enabled: false },
    destination_count: 0,
    created_at: CREATED_AT,
    etag: '"1"',
    ...overrides,
    type: "slack",
  };
}

/** A Bridge Connection that polls and whose info no check has read yet. */
export function bridgeConnection(
  overrides: ConnectionOverrides<BridgeConnection> = {},
): BridgeConnection {
  const id = overrides.id ?? "CN0000000000BR";
  return {
    id,
    name: "Example Bridge",
    base_url: "http://127.0.0.1:18094",
    event_delivery: "poll",
    limiter: { limit: 10, per_seconds: 1 },
    bot_token_status: { set: true, updated_at: CREATED_AT },
    push_secret_status: { set: false },
    callback_url: callbackUrl("bridge", id),
    info: null,
    info_read_at: null,
    proxy: { enabled: false },
    destination_count: 0,
    created_at: CREATED_AT,
    etag: '"1"',
    ...overrides,
    type: "bridge",
  };
}
