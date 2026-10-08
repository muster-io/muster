// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Links block of an Alert Group's page (C-09.FR-14, C-12.FR-9): the links of its Link rules by the rule's name,
// then the annotations runbook_url and dashboard_url and generatorURL by their names in the UI language ("Runbook",
// "Dashboard", "Source"), each opening in a new tab without access to this page. Only http(s) links are shown; the
// block is hidden when there are none. It wraps at phone width.

import type { TFunction } from "i18next";
import { ExternalLinkIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { AlertGroupLink } from "../api/gen/model";
import { safeUrl } from "./template-preview";

/** The text of a link: a Link rule's name as the API gives it, a built-in link's name in the UI language. */
function linkName(t: TFunction, link: AlertGroupLink): string {
  switch (link.kind) {
    case "runbook":
      return t("links.kind.runbook");
    case "dashboard":
      return t("links.kind.dashboard");
    case "source":
      return t("links.kind.source");
    default:
      return link.name;
  }
}

export function AlertGroupLinks({ links }: { links: readonly AlertGroupLink[] | undefined }) {
  const { t } = useTranslation();
  const shown = (links ?? []).flatMap((l) => {
    const url = safeUrl(l.url);
    return url === undefined ? [] : [{ name: linkName(t, l), url }];
  });
  if (shown.length === 0) {
    return null;
  }
  return (
    <nav aria-label={t("links.title")} className="min-w-0" data-testid="alert-group-links">
      <h2 className="sr-only">{t("links.title")}</h2>
      <ul className="flex min-w-0 flex-wrap gap-2">
        {shown.map((link, index) => (
          <li key={`${index}-${link.url}`} className="min-w-0 max-w-full">
            <a
              href={link.url}
              target="_blank"
              rel="noopener noreferrer"
              className="relative inline-flex max-w-full min-w-0 items-center gap-1.5 rounded-md border px-2.5 py-1 text-sm font-medium text-primary outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring"
              title={link.url}
              data-testid="alert-group-link"
            >
              <ExternalLinkIcon className="size-3.5 shrink-0" aria-hidden="true" />
              <span className="min-w-0 wrap-anywhere">{link.name}</span>
              <span className="sr-only">{t("links.newTab")}</span>
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
}
