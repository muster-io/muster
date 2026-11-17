// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The L1 form of the Mention settings (C-12.FR-8), which a read keeps until 0.3.0 (C-28.FR-5): nobody for every kind.
// The Destination form no longer edits it — it edits and sends Mention entries (mention-entries-editor.tsx) — and the
// fixtures that read a Destination as L1 did use `defaultMentions`.

import type { MentionSetting, MentionSettings } from "../api/gen/model";

/** Nobody: the default of every kind, a new object each time. */
export function nobody(): MentionSetting {
  return { everyone: "none", user_ids: [], groups: [] };
}

/** The settings of a new Destination: nobody for every kind. */
export function defaultMentions(): MentionSettings {
  return {
    new_alert_group: nobody(),
    new_alerts: nobody(),
    reopen: nobody(),
    ack_timeout: nobody(),
    snooze_ended: nobody(),
    rise_to_urgent: nobody(),
  };
}
