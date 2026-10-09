// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Check" of a Destination (C-13.FR-10, C-13.AC-8; C-14.FR-14): the Destination check through the interactive path,
// each check with "ok" or its message — for Telegram "Channel exists", "Discussion group", "Bot rights in the channel"
// and "Bot rights in the discussion group" — and "Check passed" or "Check failed". A passing check ends the Broken
// state: the page takes the health of the result at once, and the destination hint confirms it. A busy messenger
// answers 503 with Retry-After: "The messenger is busy; try again in N s."

import { useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { CircleCheckIcon, CircleXIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  getGetDestinationQueryKey,
  getListDestinationsQueryKey,
  useCheckDestination,
} from "../api/gen/endpoints/destinations/destinations";
import type {
  Destination,
  DestinationCheckItem,
  DestinationCheckItemName,
  DestinationCheckResult,
} from "../api/gen/model";
import { checkErrorText } from "./connection-check";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { cn } from "./ui/utils";

export function checkItemName(t: TFunction, name: DestinationCheckItemName): string {
  switch (name) {
    case "token":
      return t("destinations.check.items.token");
    case "bot_in_channel":
      return t("destinations.check.items.botInChannel");
    case "channel_exists":
      return t("destinations.check.items.channelExists");
    case "discussion_group":
      return t("destinations.check.items.discussionGroup");
    case "bot_rights_channel":
      return t("destinations.check.items.botRightsChannel");
    default:
      return t("destinations.check.items.botRightsGroup");
  }
}

function Item({ item }: { item: DestinationCheckItem }) {
  const { t } = useTranslation();
  return (
    <li className="flex min-w-0 items-start gap-2 text-sm" data-testid="destination-check-item">
      {item.ok ? (
        <CircleCheckIcon
          aria-hidden="true"
          className="mt-0.5 size-4 shrink-0 text-emerald-700 dark:text-emerald-400"
        />
      ) : (
        <CircleXIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      )}
      <span className="min-w-0 wrap-anywhere">
        <span className="font-medium">{checkItemName(t, item.name)}:</span>{" "}
        {item.ok ? (
          t("destinations.check.ok")
        ) : (
          <span className="text-destructive">
            <span className="sr-only">{t("destinations.check.itemFailed")} </span>
            {item.message ?? t("destinations.check.noDetails")}
          </span>
        )}
      </span>
    </li>
  );
}

function Result({ result }: { result: DestinationCheckResult }) {
  const { t } = useTranslation();
  return (
    <div className="flex min-w-0 flex-col gap-3" data-testid="destination-check-result">
      <div
        className={cn(
          "rounded-lg border px-4 py-2.5 text-sm",
          result.ok ? "bg-card" : "border-destructive/40 text-destructive",
        )}
        data-testid="destination-check-summary"
      >
        {result.ok ? t("destinations.check.passed") : t("destinations.check.failed")}
      </div>
      {result.checks.length > 0 && (
        <ul className="flex flex-col gap-1.5" aria-label={t("destinations.check.itemsLabel")}>
          {result.checks.map((item, index) => (
            <Item key={`${item.name}-${index}`} item={item} />
          ))}
        </ul>
      )}
    </div>
  );
}

export interface DestinationCheckProps {
  destination: Destination;
  /** The form has unsaved changes; the check uses the saved Destination. */
  dirty: boolean;
}

export function DestinationCheck({ destination, dirty }: DestinationCheckProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const check = useCheckDestination({
    mutation: {
      onSuccess: (result) => {
        // The health of the result is the Destination's now; the page shows it without waiting for the hint.
        queryClient.setQueryData<Destination>(getGetDestinationQueryKey(destination.id), (old) =>
          old === undefined ? old : { ...old, health: result.health },
        );
        void queryClient.invalidateQueries({ queryKey: getGetDestinationQueryKey(destination.id) });
        void queryClient.invalidateQueries({ queryKey: getListDestinationsQueryKey() });
      },
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("destinations.check.title")}</h2>
        </CardTitle>
        <CardDescription>{t("destinations.check.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="outline"
            disabled={check.isPending}
            onClick={() => check.mutate({ destinationId: destination.id })}
          >
            {check.isPending ? t("destinations.check.running") : t("destinations.check.start")}
          </Button>
          {dirty && (
            <span className="text-sm text-muted-foreground">{t("destinations.check.unsaved")}</span>
          )}
        </div>
        {/* One live region, which stays mounted, announces the result or the refusal once. */}
        <div role="status" aria-live="polite" className="flex flex-col gap-3 empty:hidden">
          {check.isError && (
            <div
              className="rounded-lg border border-destructive/40 px-4 py-2.5 text-sm wrap-anywhere text-destructive"
              data-testid="destination-check-error"
            >
              {checkErrorText(t, check.error)}
            </div>
          )}
          {check.isSuccess && <Result result={check.data} />}
        </div>
      </CardContent>
    </Card>
  );
}
