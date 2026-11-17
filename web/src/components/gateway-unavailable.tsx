// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The note shown where a call could be chosen while no Notification gateway exists (C-30.FR-9): "Unavailable: a
// Notification gateway is needed (a bridge for calls and SMS)", the link "How to connect a Notification gateway" to
// the documentation and, for a user with connections:write, "Connect a bridge" to the new Connection form with the
// type Bridge. The profile, the Notification rules (S-153) and the chain form (S-158) use it.

import { Link } from "@tanstack/react-router";
import { LockIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { docsUrl, useDocsVersion } from "../lib/docs";
import { useCan } from "./app-shell";
import { buttonVariants } from "./ui/button";

export function GatewayUnavailable({ children }: { children?: ReactNode }) {
  const { t } = useTranslation();
  const version = useDocsVersion();
  const canConnect = useCan("connections:write");
  const howTo = t("gatewayUnavailable.howTo");
  return (
    <div
      className="flex flex-wrap items-start gap-3 rounded-lg border border-dashed bg-muted/40 p-3.5 text-sm"
      data-testid="gateway-unavailable"
    >
      <LockIcon aria-hidden="true" className="mt-0.5 size-4.5 shrink-0 text-muted-foreground" />
      <div className="flex min-w-0 flex-1 basis-64 flex-col gap-1.5">
        <span className="font-medium">{t("gatewayUnavailable.title")}</span>
        {children}
        <a
          href={docsUrl("page.notification_gateway", version)}
          target="_blank"
          rel="noopener noreferrer"
          referrerPolicy="no-referrer"
          aria-label={`${howTo} (${t("gatewayUnavailable.newTab")})`}
          className="w-fit rounded-sm text-primary underline decoration-primary/40 underline-offset-4 outline-none hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="docs-link"
          data-docs="page.notification_gateway"
        >
          {howTo}
        </a>
      </div>
      {canConnect && (
        <Link
          to="/connections/new"
          search={{ type: "bridge" }}
          className={buttonVariants({ size: "sm" })}
          data-testid="gateway-connect"
        >
          {t("gatewayUnavailable.connect")}
        </Link>
      )}
    </div>
  );
}
