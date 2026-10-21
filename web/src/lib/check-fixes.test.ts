// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The "How to fix" links of the checks (C-21.FR-11, C-21.AC-8): every code of NullableConnectionCheckCode,
// NullableDestinationCheckCode and every warning of the Connection check, as the generated client has them, is either
// linked as reference.md's "Next to problems" says, by the type of the Connection, or one of the codes it lists as
// having no link; a code the client gains without either fails here.

import { describe, expect, test } from "vitest";

import {
  ConnectionCheckResultWarningsItem,
  ConnectionType,
  NullableConnectionCheckCode,
  NullableDestinationCheckCode,
} from "../api/gen/model";
import {
  CODES_WITHOUT_LINK,
  checkWarningFix,
  connectionCheckFix,
  destinationCheckFix,
} from "./check-fixes";
import type { DocsKey } from "./docs";
import links from "./docs-links.json";

type Expected = Record<string, Partial<Record<ConnectionType, DocsKey>>>;

const ALL_TYPES = Object.values(ConnectionType);

function every(key: DocsKey): Partial<Record<ConnectionType, DocsKey>> {
  return Object.fromEntries(ALL_TYPES.map((type) => [type, key]));
}

const TOKEN: Partial<Record<ConnectionType, DocsKey>> = {
  mattermost: "fix.token.mattermost",
  telegram: "fix.token.telegram",
  slack: "fix.token.slack",
  bridge: "fix.token.bridge",
};

// The rows of reference.md's "Next to problems", written out for the Connection check.
const CONNECTION: Expected = {
  token_invalid: TOKEN,
  dns_failed: every("fix.network"),
  connect_failed: every("fix.network"),
  tls_failed: every("fix.network"),
  timeout: every("fix.network"),
  proxy_refused: every("fix.network"),
  blocked: every("fix.blocked"),
  not_bot_api: { telegram: "fix.telegram_base_url" },
  wrong_path_prefix: { telegram: "fix.telegram_base_url", mattermost: "fix.mattermost_server" },
  not_mattermost: { mattermost: "fix.mattermost_server" },
  unexpected_status: { mattermost: "fix.mattermost_server" },
  wrong_token_type: { slack: "fix.slack_tokens" },
  app_mismatch: { slack: "fix.slack_tokens" },
  not_slack: { slack: "fix.slack_tokens" },
  missing_scope: { slack: "fix.slack_scopes" },
  not_bridge: { bridge: "fix.bridge_check" },
  protocol_version_unknown: { bridge: "fix.bridge_check" },
  protocol_mismatch: { bridge: "fix.bridge_check" },
  event_delivery_not_offered: { bridge: "fix.bridge_check" },
};

// The same rows for the Destination check.
const DESTINATION: Expected = {
  token_invalid: TOKEN,
  dns_failed: every("fix.network"),
  connect_failed: every("fix.network"),
  tls_failed: every("fix.network"),
  timeout: every("fix.network"),
  proxy_refused: every("fix.network"),
  blocked: every("fix.blocked"),
  missing_scope: { slack: "fix.slack_scopes" },
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
  channel_forbidden: { bridge: "fix.bridge_destination" },
};

const WARNINGS: Record<string, DocsKey> = {
  press_answers_in_thread: "fix.press_answers_in_thread",
  another_client_connected: "fix.another_client_connected",
};

test("the codes without a link are reference.md's", () => {
  expect(CODES_WITHOUT_LINK.toSorted()).toEqual(["request_failed", "webhook_set"]);
});

describe("connectionCheckFix", () => {
  const codes = Object.values(NullableConnectionCheckCode);

  test.each(codes)("%s is linked or listed as having no link", (code) => {
    expect(code in CONNECTION || CODES_WITHOUT_LINK.includes(code)).toBe(true);
  });

  test.each(codes.flatMap((code) => ALL_TYPES.map((type) => [code, type] as const)))(
    "%s of a %s Connection",
    (code, type) => {
      const want = CONNECTION[code]?.[type];
      expect(connectionCheckFix(type, code)).toBe(want);
      if (want !== undefined) {
        expect(links).toHaveProperty([want]);
      }
    },
  );

  test("a step without a code has no link", () => {
    expect(connectionCheckFix("telegram", null)).toBeUndefined();
    expect(connectionCheckFix("telegram", undefined)).toBeUndefined();
  });
});

describe("destinationCheckFix", () => {
  const codes = Object.values(NullableDestinationCheckCode);

  test.each(codes)("%s is linked or listed as having no link", (code) => {
    expect(code in DESTINATION || CODES_WITHOUT_LINK.includes(code)).toBe(true);
  });

  test.each(codes.flatMap((code) => ALL_TYPES.map((type) => [code, type] as const)))(
    "%s of a %s Destination",
    (code, type) => {
      const want = DESTINATION[code]?.[type];
      expect(destinationCheckFix(type, code)).toBe(want);
      if (want !== undefined) {
        expect(links).toHaveProperty([want]);
      }
    },
  );

  test("a refused token takes the token guide of the Destination's Connection", () => {
    expect(destinationCheckFix("telegram", "token_invalid")).toBe("fix.token.telegram");
    expect(destinationCheckFix("slack", "token_invalid")).toBe("fix.token.slack");
  });

  test("a check that passed has no link", () => {
    expect(destinationCheckFix("mattermost", null)).toBeUndefined();
  });

  test("an outgoing webhook, which has no Destination check, has no link", () => {
    expect(destinationCheckFix("webhook", "timeout")).toBeUndefined();
  });
});

describe("checkWarningFix", () => {
  test.each(Object.values(ConnectionCheckResultWarningsItem))("%s is linked", (warning) => {
    expect(warning in WARNINGS).toBe(true);
    expect(checkWarningFix(warning)).toBe(WARNINGS[warning]);
  });
});
