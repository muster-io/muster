// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The banner area of the shell (C-03.FR-18, C-02.FR-24): the Organization-wide notices the caller may see, with the
// texts of reference.md. The live hint system-notices re-reads them, so a banner comes and goes without a reload. An
// Admin, who can act on them, also gets "How to fix" with the guide of each (C-21.FR-11).

import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useListSystemNotices } from "../api/gen/endpoints/system/system";
import type { SystemNotice } from "../api/gen/model";
import { useSession } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { DocsLink } from "./docs-link";

function NoticeText({ notice }: { notice: SystemNotice }) {
  const { t } = useTranslation();
  const { time } = useTimeFormat();
  switch (notice.kind) {
    case "recovering_after_downtime":
      return notice.until
        ? t("notices.recoveringAfterDowntime", { time: time(notice.until) })
        : t("notices.recoveringAfterDowntimeNoEnd");
    case "no_replica_leading":
      return t("notices.noReplicaLeading");
    default:
      return null;
  }
}

/** The guide of a notice, for an Admin; the same Permission lets the server show Admins their notices. */
function NoticeFix({ notice }: { notice: SystemNotice }) {
  const { t } = useTranslation();
  const admin = useSession()?.permissions.includes("system-status:read") === true;
  if (!admin) {
    return null;
  }
  switch (notice.kind) {
    case "recovering_after_downtime":
      return (
        <DocsLink to="fix.after_downtime" kind="how_to_fix" about={t("docs.fixes.afterDowntime")} />
      );
    case "no_replica_leading":
      return <DocsLink to="fix.no_leader" kind="how_to_fix" about={t("docs.fixes.noLeader")} />;
    default:
      return null;
  }
}

export function NoticeBanners() {
  const { data } = useListSystemNotices();
  const notices = data?.items ?? [];
  return (
    <div role="status" aria-live="polite" className="empty:hidden">
      {notices.map((notice) => (
        <div
          key={notice.kind}
          data-notice={notice.kind}
          className="border-b border-warning/60 bg-warning-surface text-foreground"
        >
          <p className="mx-auto flex max-w-6xl items-start gap-2 px-3 py-2 text-sm sm:px-6">
            <TriangleAlertIcon
              aria-hidden="true"
              className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
            />
            <span className="min-w-0">
              <span>
                <NoticeText notice={notice} />
              </span>{" "}
              <NoticeFix notice={notice} />
            </span>
          </p>
        </div>
      ))}
    </div>
  );
}
