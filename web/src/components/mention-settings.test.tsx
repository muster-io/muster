// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// What is left of the L1 Mention settings: the form of a new Destination that reads and fixtures still use, nobody for
// each of the six kinds. The editor of the Mentions is mention-entries-editor.tsx.

import { describe, expect, test } from "vitest";

import { defaultMentions, nobody } from "./mention-settings";

describe("defaultMentions", () => {
  test("holds nobody for each of the six kinds of L1, each its own object", () => {
    const mentions = defaultMentions();
    expect(Object.keys(mentions)).toEqual([
      "new_alert_group",
      "new_alerts",
      "reopen",
      "ack_timeout",
      "snooze_ended",
      "rise_to_urgent",
    ]);
    for (const setting of Object.values(mentions)) {
      expect(setting).toEqual(nobody());
    }
    mentions.reopen.user_ids.push("US0000000000AA");
    expect(defaultMentions().reopen.user_ids).toEqual([]);
  });
});
