// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The warnings above the Mentions grid (C-28.FR-3): each person and entry whose latest "Mention not delivered" is not
// followed by a delivered Mention — "Mention not delivered: @bob, On-call of the Team from the label, has no linked
// Mattermost account" or "… the bridge cannot mention people" — and each group or everyone in the chat that a bridge's
// line of Mentions left out — "Mention not delivered: @payments_team, Team from the label, the message had no room for
// their Mention" — as the server computed them from the delivery events, and each stored entry whose Connection lost
// an ability, which mentions nobody now (C-28.FR-2).

import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { DestinationType, MentionEntry, MentionWarning } from "../api/gen/model";
import { notDeliveredReasonText, reasonText, targetLabel } from "../lib/mention-entries";

export function MentionWarnings({
  type,
  warnings,
  entries,
}: {
  type: DestinationType;
  warnings: readonly MentionWarning[];
  entries: readonly MentionEntry[];
}) {
  const { t } = useTranslation();
  const lost = entries.filter((e) => !e.available && e.unavailable_reason !== null);
  if (warnings.length === 0 && lost.length === 0) {
    return null;
  }
  return (
    <ul className="flex min-w-0 flex-col gap-1.5" data-testid="mention-warnings">
      {warnings.map((w) => {
        const entry = targetLabel(t, w.entry.target);
        // A group or everyone in the chat that a bridge's line of Mentions left out has no person (#626).
        const login =
          w.user === undefined
            ? (w.mention?.name ?? "")
            : w.user.login !== undefined && w.user.login !== ""
              ? w.user.login
              : w.user.name.trim();
        return (
          <li
            key={`${w.user?.id ?? `${w.mention?.kind}:${w.mention?.name}`}:${w.entry.position}`}
            className="flex min-w-0 items-start gap-2 rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-sm"
            data-testid="mention-warning"
          >
            <TriangleAlertIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            <span className="wrap-anywhere">
              {t("mentions.warnings.notDelivered", {
                login,
                entry,
                reason: notDeliveredReasonText(t, w.reason, w.messenger),
              })}
            </span>
          </li>
        );
      })}
      {lost.map((e) => (
        <li
          key={`lost:${e.target.kind}:${targetLabel(t, e.target)}`}
          className="flex min-w-0 items-start gap-2 rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-sm"
          data-testid="mention-warning"
        >
          <TriangleAlertIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span className="wrap-anywhere">
            {t("mentions.warnings.unavailable", {
              entry: targetLabel(t, e.target),
              reason:
                e.unavailable_reason === null
                  ? ""
                  : reasonText(t, e.unavailable_reason, type, e.target.everyone),
            })}
          </span>
        </li>
      ))}
    </ul>
  );
}
