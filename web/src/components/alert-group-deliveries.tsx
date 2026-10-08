// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Delivery section of the Alert Group page (C-11.FR-16, FR-10; C-09.FR-14; reference.md, banners): one row per
// Destination with its name, health and delivery state — "Delivered" with a link to the message where the messenger
// gives one, "Pending", "Not delivered: {error}", "Waiting: {Destination} is Broken", "Deleted in the messenger",
// "Not posted: …" or "No longer updated here" — and the marks "Thread not attached to the Root message" and "Possible
// duplicate". The error comes from the messenger and is text only; a link opens in a new tab, and only an http or https
// one. The alert-group hint reads the section again; while a delivery is pending or waits for a Broken Destination it
// is read again every few seconds, as its end sends no hint of its own.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { ExternalLinkIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { useListAlertGroupDeliveries } from "../api/gen/endpoints/alert-groups/alert-groups";
import type { AlertGroupDelivery } from "../api/gen/model";
import { problemText } from "../lib/api";
import { useCan } from "./app-shell";
import { HealthBadge } from "./destination-health";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { cn } from "./ui/utils";

/** How often a pending delivery, or one waiting for a Broken Destination, is read again. */
const PENDING_REFRESH_MS = 3_000;
const WAITING_REFRESH_MS = 15_000;

/** An absolute http or https URL, the only kind of link the section opens; anything else is not linked. */
export function safeMessageUrl(url: string | null | undefined): string | undefined {
  if (url === null || url === undefined || url.trim() === "") {
    return undefined;
  }
  try {
    const parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}

/** The text of a delivery state; "Delivered" is the link where the messenger gives one. */
export function deliveryStateText(t: TFunction, delivery: AlertGroupDelivery): string {
  switch (delivery.state) {
    case "delivered":
      return t("deliveries.state.delivered");
    case "pending":
      return t("deliveries.state.pending");
    case "not_delivered":
      return delivery.error
        ? t("deliveries.state.notDelivered", { error: delivery.error })
        : t("deliveries.state.notDeliveredNoError");
    case "waiting_for_broken_destination":
      return t("deliveries.state.waiting", { destination: delivery.destination.name });
    case "deleted_in_messenger":
      return t("deliveries.state.deletedInMessenger");
    case "withheld":
      return t("deliveries.state.withheld");
    default:
      return t("deliveries.state.retired");
  }
}

function State({ delivery }: { delivery: AlertGroupDelivery }) {
  const { t } = useTranslation();
  const text = deliveryStateText(t, delivery);
  const url = delivery.state === "delivered" ? safeMessageUrl(delivery.message_url) : undefined;
  if (url !== undefined) {
    return (
      <a
        href={url}
        target="_blank"
        rel="noopener noreferrer"
        className="inline-flex items-center gap-1 text-primary underline-offset-4 hover:underline focus-visible:underline"
        data-testid="delivery-state"
      >
        {text}
        <ExternalLinkIcon aria-hidden="true" className="size-3.5" />
        <span className="sr-only"> {t("deliveries.newTab")}</span>
      </a>
    );
  }
  const problem = delivery.state === "not_delivered" || delivery.state === "deleted_in_messenger";
  return (
    <span
      className={cn("wrap-anywhere", problem && "text-destructive")}
      data-testid="delivery-state"
    >
      {text}
    </span>
  );
}

function DestinationName({ delivery }: { delivery: AlertGroupDelivery }) {
  const canRead = useCan("destinations:read");
  const { destination } = delivery;
  if (!canRead) {
    return <span className="font-medium wrap-anywhere">{destination.name}</span>;
  }
  return (
    <Link
      to="/destinations/$destinationId"
      params={{ destinationId: destination.id }}
      className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {destination.name}
    </Link>
  );
}

function Mark({ children, testId }: { children: ReactNode; testId: string }) {
  return (
    <span
      className="rounded-md border border-warning/60 bg-warning-surface px-1.5 py-0.5 text-xs text-foreground"
      data-testid={testId}
    >
      {children}
    </span>
  );
}

function Row({ delivery }: { delivery: AlertGroupDelivery }) {
  const { t } = useTranslation();
  return (
    <li
      className="flex min-w-0 flex-col gap-1 border-t py-2.5 first:border-t-0 first:pt-0 last:pb-0"
      data-testid="delivery-row"
      data-state={delivery.state}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
        <DestinationName delivery={delivery} />
        <HealthBadge health={delivery.destination.health} />
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <State delivery={delivery} />
        {delivery.thread_not_attached && (
          <Mark testId="delivery-thread-not-attached">{t("deliveries.threadNotAttached")}</Mark>
        )}
        {delivery.possible_duplicate && (
          <Mark testId="delivery-possible-duplicate">{t("deliveries.possibleDuplicate")}</Mark>
        )}
      </div>
    </li>
  );
}

export function AlertGroupDeliveries({ alertGroupId }: { alertGroupId: string }) {
  const { t } = useTranslation();
  const query = useListAlertGroupDeliveries(alertGroupId, {
    query: {
      refetchInterval: (q) => {
        const items = q.state.data?.items ?? [];
        if (items.some((d) => d.state === "pending")) {
          return PENDING_REFRESH_MS;
        }
        return items.some((d) => d.state === "waiting_for_broken_destination")
          ? WAITING_REFRESH_MS
          : false;
      },
    },
  });
  const items = query.data?.items ?? [];
  return (
    <Card data-testid="alert-group-deliveries">
      <CardHeader>
        <CardTitle>
          <h2>{t("deliveries.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {query.data === undefined ? (
          <p className="text-sm text-muted-foreground" role="status">
            {query.isError ? problemText(t, query.error) : t("common.loading")}
          </p>
        ) : items.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("deliveries.none")}</p>
        ) : (
          <ul className="flex flex-col" aria-label={t("deliveries.title")}>
            {items.map((d) => (
              <Row key={d.destination.id} delivery={d} />
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
