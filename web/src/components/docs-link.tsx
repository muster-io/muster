// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The links to the documentation site (C-21.FR-11): "Guide" next to a page title, "Learn more" next to a field and
// "How to fix" next to a problem, each with an icon and an accessible name that says what it is about and that it
// opens in a new tab. A link opens the page of the running version in a new tab and sends no Referer; the browser
// loads nothing from the site until the link is followed. PageTitle is the h1 of a page with its Guide link beside it,
// wrapping under the title on a narrow screen. Only the pages of an active, full session use them.

import type { TFunction } from "i18next";
import { BookOpenIcon, CircleHelpIcon, WrenchIcon } from "lucide-react";
import type { ComponentProps } from "react";
import { useTranslation } from "react-i18next";

import { type DocsKey, docsUrl, useDocsVersion } from "../lib/docs";
import { cn } from "./ui/utils";

export type DocsLinkKind = "guide" | "learn_more" | "how_to_fix";

const ICONS = { guide: BookOpenIcon, learn_more: CircleHelpIcon, how_to_fix: WrenchIcon } as const;

export interface DocsLinkProps {
  /** The entry of docs-links.json the link opens. */
  to: DocsKey;
  kind: DocsLinkKind;
  /** The words the accessible name adds: "Guide: {about} (opens in a new tab)". */
  about: string;
  className?: string;
}

export function DocsLink({ to, kind, about, className }: DocsLinkProps) {
  const { t } = useTranslation();
  const version = useDocsVersion();
  const Icon = ICONS[kind];
  let text: string;
  let label: string;
  switch (kind) {
    case "learn_more":
      text = t("docs.learnMore");
      label = t("docs.learnMoreAbout", { about });
      break;
    case "how_to_fix":
      text = t("docs.howToFix");
      label = t("docs.howToFixAbout", { about });
      break;
    default:
      text = t("docs.guide");
      label = t("docs.guideAbout", { about });
  }
  return (
    <a
      href={docsUrl(to, version)}
      target="_blank"
      rel="noopener noreferrer"
      referrerPolicy="no-referrer"
      aria-label={label}
      className={cn(
        "inline-flex w-fit items-center gap-1 rounded-sm text-sm font-normal whitespace-nowrap text-primary underline decoration-primary/40 underline-offset-4 outline-none hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring",
        className,
      )}
      data-testid="docs-link"
      data-docs={to}
    >
      <Icon aria-hidden="true" className="size-3.5 shrink-0" />
      {text}
    </a>
  );
}

/** A key of docs-links.json for the Guide link beside a page title. */
export type PageDocsKey = Extract<DocsKey, `page.${string}`>;

/** What the guide of each page is about, which its accessible name adds: "Guide: Telegram Connection". */
const GUIDE_ABOUT = {
  "page.alert_groups": (t) => t("docs.guides.alertGroups"),
  "page.alert_group": (t) => t("docs.guides.alertGroup"),
  "page.integrations": (t) => t("docs.guides.integrations"),
  "page.stored_snapshot": (t) => t("docs.guides.storedSnapshots"),
  "page.routes": (t) => t("docs.guides.routes"),
  "page.connections": (t) => t("docs.guides.connections"),
  "page.connection.mattermost": (t) => t("docs.guides.connection.mattermost"),
  "page.connection.telegram": (t) => t("docs.guides.connection.telegram"),
  "page.connection.slack": (t) => t("docs.guides.connection.slack"),
  "page.connection.bridge": (t) => t("docs.guides.connection.bridge"),
  "page.destinations": (t) => t("docs.guides.destinations"),
  "page.destination.mattermost": (t) => t("docs.guides.destination.mattermost"),
  "page.destination.telegram": (t) => t("docs.guides.destination.telegram"),
  "page.destination.slack": (t) => t("docs.guides.destination.slack"),
  "page.destination.bridge": (t) => t("docs.guides.destination.bridge"),
  "page.destination.webhook_events": (t) => t("docs.guides.destination.webhookEvents"),
  "page.destination.webhook_template": (t) => t("docs.guides.destination.webhookTemplate"),
} satisfies Record<PageDocsKey, (t: TFunction) => string>;

export interface PageTitleProps extends ComponentProps<"h1"> {
  /** The entry of docs-links.json of the page's guide; it also names what the guide is about. */
  docs: PageDocsKey;
}

/** The h1 of a page with the Guide link beside it; the link wraps under the title when the line is too narrow. */
export function PageTitle({ docs, className, children, ...heading }: PageTitleProps) {
  const { t } = useTranslation();
  const about = GUIDE_ABOUT[docs](t);
  return (
    <div className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-1" data-testid="page-title">
      <h1
        {...heading}
        className={cn("min-w-0 text-2xl font-semibold tracking-tight wrap-anywhere", className)}
      >
        {children}
      </h1>
      <DocsLink to={docs} kind="guide" about={about} />
    </div>
  );
}
