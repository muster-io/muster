// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The "How to fix" links of the checks (C-21.FR-11, C-21.AC-8): for every messenger, the tables name every code of
// NullableConnectionCheckCode and NullableDestinationCheckCode as the generated client has them, and every warning of
// the Connection check; a code is a key of docs-links.json, one of the codes reference.md lists as having no link, or
// a code that the messenger never gives. Representative rows of reference.md's "Next to problems" are checked as
// written there.

import { describe, expect, test } from "vitest";

import {
  ConnectionCheckResultWarningsItem,
  ConnectionType,
  NullableConnectionCheckCode,
  NullableDestinationCheckCode,
} from "../api/gen/model";
import {
  CODES_WITHOUT_LINK,
  CONNECTION_FIXES,
  DESTINATION_FIXES,
  NOT_GIVEN,
  WARNING_FIXES,
  checkWarningFix,
  connectionCheckFix,
  destinationCheckFix,
} from "./check-fixes";
import links from "./docs-links.json";

const TYPES = Object.values(ConnectionType);
const CONNECTION_CODES = Object.values(NullableConnectionCheckCode);
const DESTINATION_CODES = Object.values(NullableDestinationCheckCode);

function expectLinkOrNone(fix: string | null, code: string): void {
  if (fix === null) {
    expect(CODES_WITHOUT_LINK, code).toContain(code);
  } else if (fix !== NOT_GIVEN) {
    expect(links, code).toHaveProperty([fix]);
  }
}

test("the codes without a link are reference.md's", () => {
  expect(CODES_WITHOUT_LINK.toSorted()).toEqual(["request_failed", "webhook_set"]);
});

describe("connectionCheckFix", () => {
  test.each(TYPES)("the %s table names exactly the codes of the client", (type) => {
    expect(Object.keys(CONNECTION_FIXES[type]).toSorted()).toEqual(CONNECTION_CODES.toSorted());
  });

  test.each(TYPES.flatMap((type) => CONNECTION_CODES.map((code) => [type, code] as const)))(
    "%s %s is a key, a code without a link or a code the messenger never gives",
    (type, code) => {
      const fix = CONNECTION_FIXES[type][code];
      expectLinkOrNone(fix, code);
      const want = fix === null || fix === NOT_GIVEN ? undefined : fix;
      expect(connectionCheckFix(type, code)).toBe(want);
    },
  );

  test.each([
    ["mattermost", "token_invalid", "fix.token.mattermost"],
    ["telegram", "token_invalid", "fix.token.telegram"],
    ["slack", "token_invalid", "fix.token.slack"],
    ["bridge", "token_invalid", "fix.token.bridge"],
    ["telegram", "tls_failed", "fix.network"],
    ["slack", "blocked", "fix.blocked"],
    ["telegram", "not_bot_api", "fix.telegram_base_url"],
    ["telegram", "unexpected_status", "fix.telegram_base_url"],
    ["mattermost", "wrong_path_prefix", "fix.mattermost_server"],
    ["slack", "app_mismatch", "fix.slack_tokens"],
    ["slack", "missing_scope", "fix.slack_scopes"],
    ["slack", "not_slack", "fix.slack_server"],
    ["slack", "unexpected_status", "fix.slack_server"],
    ["bridge", "not_bridge", "fix.bridge_server"],
    ["bridge", "wrong_path_prefix", "fix.bridge_server"],
    ["bridge", "protocol_mismatch", "fix.bridge_check"],
  ] as const)("a %s step failing with %s links to %s", (type, code, key) => {
    expect(connectionCheckFix(type, code)).toBe(key);
  });

  test.each(TYPES)("request_failed of %s has no link", (type) => {
    expect(connectionCheckFix(type, "request_failed")).toBeUndefined();
  });

  test("webhook_set, a step that passed, and a step without a code have no link", () => {
    expect(connectionCheckFix("telegram", "webhook_set")).toBeUndefined();
    expect(connectionCheckFix("telegram", null)).toBeUndefined();
    expect(connectionCheckFix("telegram", undefined)).toBeUndefined();
  });
});

describe("destinationCheckFix", () => {
  test.each(TYPES)("the %s table names exactly the codes of the client", (type) => {
    expect(Object.keys(DESTINATION_FIXES[type]).toSorted()).toEqual(DESTINATION_CODES.toSorted());
  });

  test.each(TYPES.flatMap((type) => DESTINATION_CODES.map((code) => [type, code] as const)))(
    "%s %s is a key, a code without a link or a code the messenger never gives",
    (type, code) => {
      const fix = DESTINATION_FIXES[type][code];
      expectLinkOrNone(fix, code);
      const want = fix === null || fix === NOT_GIVEN ? undefined : fix;
      expect(destinationCheckFix(type, code)).toBe(want);
    },
  );

  test.each([
    ["telegram", "token_invalid", "fix.token.telegram"],
    ["slack", "token_invalid", "fix.token.slack"],
    ["mattermost", "bot_not_in_channel", "fix.mattermost_destination"],
    ["slack", "bot_not_in_channel", "fix.slack_destination"],
    ["telegram", "comments_disabled", "fix.telegram_comments"],
    ["telegram", "bot_not_group_admin", "fix.telegram_admin"],
    ["slack", "missing_scope", "fix.slack_scopes"],
    ["bridge", "channel_forbidden", "fix.bridge_destination"],
    ["mattermost", "timeout", "fix.network"],
  ] as const)("a %s check failing with %s links to %s", (type, code, key) => {
    expect(destinationCheckFix(type, code)).toBe(key);
  });

  test("request_failed, a check that passed and the outgoing webhook have no link", () => {
    expect(destinationCheckFix("bridge", "request_failed")).toBeUndefined();
    expect(destinationCheckFix("mattermost", null)).toBeUndefined();
    expect(destinationCheckFix("webhook", "timeout")).toBeUndefined();
  });
});

describe("checkWarningFix", () => {
  test("the table names exactly the warnings of the client", () => {
    expect(Object.keys(WARNING_FIXES).toSorted()).toEqual(
      Object.values(ConnectionCheckResultWarningsItem).toSorted(),
    );
  });

  test.each([
    ["press_answers_in_thread", "fix.press_answers_in_thread"],
    ["another_client_connected", "fix.another_client_connected"],
  ] as const)("%s links to %s", (warning, key) => {
    expect(checkWarningFix(warning)).toBe(key);
    expect(links).toHaveProperty([key]);
  });
});
