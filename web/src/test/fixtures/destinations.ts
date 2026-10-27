// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Complete Destination reads for the web tests, one builder per Destination type. Every required field holds a neutral
// value — healthy, no Routes, nobody mentioned, the limiter the form proposes — and a test passes, field by field, only
// what it cares about; the type stays the builder's. A required field added to a read is set here, the one place the
// TypeScript check names, instead of in every test that builds a Destination.

import type {
  BridgeDestination,
  DestinationReadFields,
  MattermostDestination,
  SlackDestination,
  TelegramDestination,
  WebhookDestination,
} from "../../api/gen/model";
import { defaultMentions } from "../../components/mention-settings";

/** The fields a test may set: every field of the read but its type. */
type DestinationOverrides<T extends { type: string }> = Partial<Omit<T, "type">>;

/** The read-only fields every Destination type shares, neutral. */
function readFields(): Omit<DestinationReadFields, "id"> {
  return {
    health: { state: "healthy" },
    routes: [],
    created_at: "2026-10-13T09:00:00Z",
    etag: '"1"',
  };
}

export function mattermostDestination(
  overrides: DestinationOverrides<MattermostDestination> = {},
): MattermostDestination {
  return {
    ...readFields(),
    id: "DS0000000000MM",
    name: "alerts",
    connection_id: "CN0000000000MM",
    team_id: "team-dev",
    channel_id: "ch-alerts",
    mentions: defaultMentions(),
    limiter: { limit: 5, per_seconds: 1 },
    ...overrides,
    type: "mattermost",
  };
}

export function telegramDestination(
  overrides: DestinationOverrides<TelegramDestination> = {},
): TelegramDestination {
  return {
    ...readFields(),
    id: "DS0000000000TG",
    name: "alerts",
    connection_id: "CN0000000000TG",
    channel_id: "@muster_alerts",
    mentions: defaultMentions(),
    limiter: { limit: 10, per_seconds: 60 },
    ...overrides,
    type: "telegram",
  };
}

export function slackDestination(
  overrides: DestinationOverrides<SlackDestination> = {},
): SlackDestination {
  return {
    ...readFields(),
    id: "DS0000000000SL",
    name: "alerts",
    connection_id: "CN0000000000SL",
    channel_id: "C0FAKEALERTS",
    mentions: defaultMentions(),
    limiter: { limit: 30, per_seconds: 60 },
    ...overrides,
    type: "slack",
  };
}

export function bridgeDestination(
  overrides: DestinationOverrides<BridgeDestination> = {},
): BridgeDestination {
  return {
    ...readFields(),
    id: "DS0000000000BR",
    name: "alerts",
    connection_id: "CN0000000000BR",
    channel_id: "ch-alerts",
    mentions: defaultMentions(),
    limiter: { limit: 30, per_seconds: 60 },
    ...overrides,
    type: "bridge",
  };
}

/** An outgoing webhook in the mode `events`, with no headers, no proxy and no Signing secret. */
export function webhookDestination(
  overrides: DestinationOverrides<WebhookDestination> = {},
): WebhookDestination {
  return {
    ...readFields(),
    id: "DS0000000000WH",
    name: "hook",
    mode: "events",
    events: { url: "http://127.0.0.1:18093/hook", headers: [] },
    mentions: defaultMentions(),
    limiter: { limit: 5, per_seconds: 1 },
    proxy: { enabled: false },
    signing_secret_status: { set: false },
    warnings: [],
    ...overrides,
    type: "webhook",
  };
}
