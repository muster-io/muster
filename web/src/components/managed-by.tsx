// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The "Managed by" mark (C-33.FR-12): the name of the tool that keeps a configuration object as code. ManagedByBadge
// is shown next to the page title, ManagedByWarning above the form (informational: it never disables or asks to
// confirm anything), and ManagedByDeleteLine is the line a delete dialog adds. Each renders nothing for an object
// without a mark, which includes a read from an older server. The tool is shown as the API gives it, untranslated.

import type { TFunction } from "i18next";
import { InfoIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Alert, AlertDescription } from "./ui/alert";

/** The kinds of object that carry a mark: the singular of the page title, which the warning names. */
export type ManagedObject =
  | "route"
  | "team"
  | "escalationChain"
  | "integration"
  | "schedule"
  | "connection"
  | "destination"
  | "lookupTable"
  | "linkRule"
  | "organization"
  | "outboundPolicy"
  | "oidcSettings";

/** The tool of a mark, or undefined when the object has none. */
export type ManagedBy = string | null | undefined;

function objectName(t: TFunction, object: ManagedObject): string {
  switch (object) {
    case "route":
      return t("managedBy.objects.route");
    case "team":
      return t("managedBy.objects.team");
    case "escalationChain":
      return t("managedBy.objects.escalationChain");
    case "integration":
      return t("managedBy.objects.integration");
    case "schedule":
      return t("managedBy.objects.schedule");
    case "connection":
      return t("managedBy.objects.connection");
    case "destination":
      return t("managedBy.objects.destination");
    case "lookupTable":
      return t("managedBy.objects.lookupTable");
    case "linkRule":
      return t("managedBy.objects.linkRule");
    case "organization":
      return t("managedBy.objects.organization");
    case "outboundPolicy":
      return t("managedBy.objects.outboundPolicy");
    default:
      return t("managedBy.objects.oidcSettings");
  }
}

/** "Managed by {tool}", next to the title of the page. */
export function ManagedByBadge({ tool }: { tool: ManagedBy }) {
  const { t } = useTranslation();
  if (tool === null || tool === undefined || tool === "") {
    return null;
  }
  return (
    <span
      data-testid="managed-by-badge"
      className="inline-flex w-fit max-w-full items-center rounded-full border border-border bg-muted px-2 py-0.5 text-xs font-medium break-all text-muted-foreground"
    >
      {t("managedBy.badge", { tool })}
    </span>
  );
}

/** The warning above a form: an informational banner, never an error, that disables nothing. */
export function ManagedByWarning({ object, tool }: { object: ManagedObject; tool: ManagedBy }) {
  const { t } = useTranslation();
  if (tool === null || tool === undefined || tool === "") {
    return null;
  }
  return (
    <Alert role="note" data-testid="managed-by-warning">
      <InfoIcon aria-hidden="true" />
      <AlertDescription className="text-current">
        {t("managedBy.warning", { object: objectName(t, object), tool })}
      </AlertDescription>
    </Alert>
  );
}

/** The line a delete dialog adds: the tool creates the object again on its next run. */
export function ManagedByDeleteLine({ tool }: { tool: ManagedBy }) {
  const { t } = useTranslation();
  const line = managedByDeleteLine(t, tool);
  if (line === null) {
    return null;
  }
  return (
    <p data-testid="managed-by-delete-line" className="text-sm text-muted-foreground">
      {line}
    </p>
  );
}

/** The text of the delete line, or null without a mark. */
export function managedByDeleteLine(t: TFunction, tool: ManagedBy): string | null {
  if (tool === null || tool === undefined || tool === "") {
    return null;
  }
  return t("managedBy.deleteLine", { tool });
}
