// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Bridge Connection as a Notification gateway (C-30.FR-1, FR-8): its roles from `info`, "Notification gateway" with
// its health — "Healthy", or the banner "Broken since HH:MM: {reason}. Muster tries again every {interval}." — kept
// apart from the health of its Destinations, and the warning on a Connection that declares only the role gateway while
// it still has Destinations, saved while its bridge was a messenger: they no longer receive messages.

import type { TFunction } from "i18next";
import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { BridgeConnection, BridgeInfoRole } from "../api/gen/model";
import { formatDuration, useTimeFormat } from "../lib/time";
import { unreachable } from "./destination-test-panel";
import { BROKEN_PROBE_INTERVAL_SECONDS, HealthBadge } from "./destination-health";
import { DocsLink } from "./docs-link";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";

/** The name of a role of a bridge. */
export function roleName(t: TFunction, role: BridgeInfoRole): string {
  switch (role) {
    case "messenger":
      return t("connections.roles.messenger");
    case "gateway":
      return t("connections.roles.gateway");
    default:
      return unreachable(role);
  }
}

/** The roles as a list of names: "Messenger, Notification gateway"; a bridge that declares none is a messenger. */
export function rolesText(t: TFunction, roles: readonly BridgeInfoRole[] | undefined): string {
  return (roles === undefined || roles.length === 0 ? (["messenger"] as const) : roles)
    .map((r) => roleName(t, r))
    .join(", ");
}

/** Whether the Connection is a Notification gateway, or Broken as one. */
export function isGateway(c: Pick<BridgeConnection, "roles" | "gateway">): boolean {
  return c.roles.includes("gateway") || c.gateway !== null;
}

/** Whether the Connection's only role is gateway, so that it cannot be a Destination. */
export function isGatewayOnly(c: Pick<BridgeConnection, "roles">): boolean {
  return c.roles.includes("gateway") && !c.roles.includes("messenger");
}

/** The reason without a closing full stop, which the sentence adds, and the probe interval. */
function brokenParts(t: TFunction, reason: string | null | undefined) {
  return {
    reason: (reason ?? "").trim().replace(/\.$/u, "") || t("destinations.health.noReason"),
    interval: formatDuration(t, BROKEN_PROBE_INTERVAL_SECONDS),
  };
}

/** The roles, the health of the gateway and its Broken banner, and the warning about Destinations left behind. */
export function GatewayHealth({ connection }: { connection: BridgeConnection }) {
  const { t } = useTranslation();
  const { time } = useTimeFormat();
  if (!isGateway(connection)) {
    return null;
  }
  const state = connection.gateway;
  const broken = state?.broken === true;
  const left = isGatewayOnly(connection) ? connection.destination_count : 0;
  return (
    <Card data-testid="gateway-health">
      <CardHeader>
        <CardTitle>
          <h2>{t("connections.roles.gateway")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex min-w-0 flex-col gap-3 text-sm">
        <p className="flex flex-wrap items-baseline gap-x-3 gap-y-1" data-testid="gateway-roles">
          <span>{t("connections.roles.list", { roles: rolesText(t, connection.roles) })}</span>
          {/* A bridge that is a messenger first keeps the guide of the messenger beside its title. */}
          {!isGatewayOnly(connection) && (
            <DocsLink
              to="page.notification_gateway"
              kind="guide"
              about={t("docs.guides.notificationGateway")}
            />
          )}
        </p>
        <HealthBadge
          health={
            broken
              ? { state: "broken", since: state?.broken_since, reason: state?.reason }
              : { state: "healthy" }
          }
        />
        {broken && state !== null && (
          <div
            className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-card px-3 py-2"
            data-testid="gateway-broken-banner"
          >
            <TriangleAlertIcon
              aria-hidden="true"
              className="mt-0.5 size-4 shrink-0 text-destructive"
            />
            <p className="min-w-0 wrap-anywhere">
              {state.broken_since === null
                ? t("connections.roles.brokenNoSince", brokenParts(t, state.reason))
                : t("connections.roles.brokenSince", {
                    time: time(state.broken_since),
                    ...brokenParts(t, state.reason),
                  })}
            </p>
          </div>
        )}
        {left > 0 && (
          <div
            className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2"
            data-testid="gateway-only-warning"
          >
            <TriangleAlertIcon
              aria-hidden="true"
              className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
            />
            <p className="min-w-0 wrap-anywhere">
              {t("connections.roles.destinationsLeft", { count: left })}
            </p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
