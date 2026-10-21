// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Check" of a Destination (C-13.FR-10, C-13.AC-8; C-14.FR-14; C-22.FR-3): the Destination check through the
// interactive path, each check with "ok" or what failed, translated by its code with the server's English message below
// as its detail — for Telegram "Channel exists", "Discussion group", "Bot rights in the channel" and "Bot rights in the
// discussion group", for a Bridge and Slack "Channel" — and "Check passed" or "Check failed". The scopes a Slack bot
// token lacks come from the item's missing_scopes and are named in the reader's language (D396); a Slack channel without
// the bot says to invite it, naming the bot of the Connection (C-24.FR-3). A passing check ends the Broken
// state: the page takes the health of the result at once, and the destination hint confirms it. A busy messenger
// answers 503 with Retry-After: "The messenger is busy; try again in N s." A result names its check run, and "Requests
// of this check" opens the requests it made (C-23.FR-6). A failed check ends with "How to fix" where reference.md names a
// guide for its code and the Destination's type (C-21.FR-11).

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
  DestinationType,
} from "../api/gen/model";
import i18n from "../i18n";
import { checkFixAbout, destinationCheckFix } from "../lib/check-fixes";
import { type CheckCode, checkCodeText, checkErrorText } from "./connection-check";
import { unreachable } from "./destination-test-panel";
import { DocsLink } from "./docs-link";
import { RequestLogButton } from "./request-log";
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
    case "bot_rights_group":
      return t("destinations.check.items.botRightsGroup");
    case "channel":
      return t("destinations.check.items.channel");
    default:
      return unreachable(name);
  }
}

/** What a Destination check's code needs besides itself: the scopes Slack named, and the bot of a Slack Destination. */
export interface DestinationCheckContext {
  /** The scopes a Slack bot token lacks (missing_scopes), which the text of missing_scope names. */
  scopes?: readonly string[] | null;
  /**
   * The bot of a Slack Destination's Connection, which "Invite the bot to the channel: /invite @{bot}" names; null
   * while no check found it, undefined for another type.
   */
  slackBot?: string | null;
}

/**
 * What a Destination check found, by its code, in the reader's language: a Slack channel without the bot says to invite
 * the bot (C-24.FR-3, reference.md), any other code as the check of a Connection names it.
 */
export function destinationCheckText(
  t: TFunction,
  code: CheckCode,
  context: DestinationCheckContext = {},
): string {
  if (code === "bot_not_in_channel" && context.slackBot !== undefined) {
    return context.slackBot
      ? t("destinations.slack.inviteBot", { bot: context.slackBot })
      : t("destinations.slack.inviteBotUnnamed");
  }
  return checkCodeText(t, code, context.scopes);
}

/**
 * The server's English message as the detail of the text of its code, when it says more than that code's English
 * text; null without a code, which shows the message itself.
 */
export function destinationCheckDetail(
  code: CheckCode | null | undefined,
  message: string | null | undefined,
  context: DestinationCheckContext = {},
): string | null {
  if (
    code === null ||
    code === undefined ||
    message === null ||
    message === undefined ||
    message === ""
  ) {
    return null;
  }
  return message === destinationCheckText(i18n.getFixedT("en"), code, context) ? null : message;
}

function Item({
  item,
  type,
  slackBot,
}: {
  item: DestinationCheckItem;
  /** The type of the Destination, which is its Connection's. */
  type: DestinationType;
  slackBot?: string | null;
}) {
  const { t } = useTranslation();
  const code = item.code ?? null;
  const context: DestinationCheckContext = { scopes: item.missing_scopes, slackBot };
  const detail = item.ok ? null : destinationCheckDetail(code, item.message, context);
  // A failed check links to the guide of its code, at the end of its line; a check that passed never does.
  const fix = item.ok ? undefined : destinationCheckFix(type, code);
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
      <span className="flex min-w-0 flex-col gap-0.5 wrap-anywhere">
        <span>
          <span className="font-medium">{checkItemName(t, item.name)}:</span>{" "}
          {item.ok ? (
            t("destinations.check.ok")
          ) : (
            <span className="text-destructive" data-testid="destination-check-item-reason">
              <span className="sr-only">{t("destinations.check.itemFailed")} </span>
              {code === null
                ? (item.message ?? t("destinations.check.noDetails"))
                : destinationCheckText(t, code, context)}
            </span>
          )}
          {fix !== undefined && (
            <>
              {" "}
              <DocsLink to={fix} kind="how_to_fix" about={checkFixAbout(t, fix)} />
            </>
          )}
        </span>
        {detail !== null && (
          <span
            className="text-xs wrap-anywhere text-muted-foreground"
            data-testid="destination-check-item-detail"
          >
            {t("checks.detail", { detail })}
          </span>
        )}
      </span>
    </li>
  );
}

function Result({
  result,
  type,
  slackBot,
}: {
  result: DestinationCheckResult;
  type: DestinationType;
  slackBot?: string | null;
}) {
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
            <Item key={`${item.name}-${index}`} item={item} type={type} slackBot={slackBot} />
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
  /** The bot of a Slack Destination's Connection, null before a check found it; undefined for another type. */
  slackBot?: string | null;
}

export function DestinationCheck({ destination, dirty, slackBot }: DestinationCheckProps) {
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
          {check.isSuccess && (
            <Result result={check.data} type={destination.type} slackBot={slackBot} />
          )}
        </div>
        {check.isSuccess && (
          <div>
            <RequestLogButton
              scope={{ destination: destination.id, run: check.data.request_log_run }}
              label={t("requestLog.ofThisCheck")}
              title={t("requestLog.ofThisCheck")}
            />
          </div>
        )}
      </CardContent>
    </Card>
  );
}
