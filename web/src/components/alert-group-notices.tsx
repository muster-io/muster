// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The notices of the Alert Group page (C-09.FR-5, FR-7, FR-16): the texts of reference.md for Alerts still firing after
// a manual resolve, a Replacement, details removed by retention, and an Alert Group firing again after a manual resolve
// of another one, linked to it through a search by its number.

import { Link } from "@tanstack/react-router";
import { InfoIcon, TriangleAlertIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";

import type { AlertGroupNotice } from "../api/gen/model";
import { Alert, AlertDescription } from "./ui/alert";

function Notice({
  tone,
  kind,
  children,
}: {
  tone: "warning" | "info";
  kind: string;
  children: ReactNode;
}) {
  const Icon = tone === "warning" ? TriangleAlertIcon : InfoIcon;
  return (
    <Alert role="note" data-testid={`notice-${kind}`}>
      <Icon aria-hidden="true" />
      <AlertDescription className="wrap-anywhere text-foreground">{children}</AlertDescription>
    </Alert>
  );
}

const CODE = "rounded bg-muted px-1 font-mono text-xs";

/** A label name as code; it ignores the children Trans gives it, so that the name never passes through the text. */
function LabelName({ name }: { name: string }) {
  return <code className={CODE}>{name}</code>;
}

function NoticeText({ notice }: { notice: AlertGroupNotice }) {
  const { t } = useTranslation();
  switch (notice.kind) {
    case "alerts_still_firing":
      return t("alertGroups.notices.stillFiring", { count: notice.count ?? 0 });
    case "replacement":
      return (
        // The label goes in as an element, never into the text that Trans parses.
        <Trans
          i18nKey="alertGroups.notices.replacement"
          components={{
            label: <LabelName name={notice.label ?? ""} />,
            code: <code className={CODE} />,
          }}
        />
      );
    case "details_removed":
      return t("alertGroups.notices.detailsRemoved", {
        period: t("alertGroups.notices.days", { count: notice.retention_days ?? 0 }),
      });
    case "firing_again_after_manual_resolve": {
      const number = notice.resolved_number ?? 0;
      return (
        <Trans
          i18nKey="alertGroups.notices.firingAgain"
          values={{ number }}
          components={{
            link: (
              <Link
                to="/alert-groups"
                search={{ q: `#${number}`, tab: "all" }}
                className="font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
              />
            ),
          }}
        />
      );
    }
    default:
      // A notice of a later version of the server, which this page has no text for.
      return null;
  }
}

export function AlertGroupNotices({ notices }: { notices: readonly AlertGroupNotice[] }) {
  if (notices.length === 0) {
    return null;
  }
  return (
    <div className="flex flex-col gap-2" data-testid="alert-group-notices">
      {notices.map((n) => (
        <Notice
          key={n.kind}
          kind={n.kind}
          tone={n.kind === "details_removed" || n.kind === "replacement" ? "info" : "warning"}
        >
          <NoticeText notice={n} />
        </Notice>
      ))}
    </div>
  );
}
