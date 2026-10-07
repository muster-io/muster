// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Commands on Alert Groups from the UI (C-10.FR-1, FR-2, FR-14, FR-16): which buttons an Alert Group offers, taken
// only from its allowed_commands; the refusal messages by their Problem code; running a Command and putting the
// CommandResult into the page and the list; the texts of bulk results; and the limits the dialogs check.

import {
  type InfiniteData,
  type QueryClient,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import type { TFunction } from "i18next";

import {
  acknowledgeAlertGroup,
  getGetAlertGroupCountsQueryKey,
  getGetAlertGroupQueryKey,
  getGetAlertGroupQueryOptions,
  getGetAlertGroupStatisticsQueryKey,
  getGetAlertGroupTimelineQueryKey,
  getListAlertGroupNotesQueryKey,
  getListRelatedAlertGroupsQueryKey,
  resolveAlertGroup,
  snoozeAlertGroup,
  unacknowledgeAlertGroup,
  unresolveAlertGroup,
  unsnoozeAlertGroup,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import type {
  AlertGroup,
  AlertGroupList,
  AlertGroupRef,
  BulkCommandItem,
  CommandName,
  CommandResult,
  SnoozeRequest,
} from "../api/gen/model";
import { isApiError, problemText } from "./api";
import { formatDuration } from "./time";

/** alert_group.bulk_max: the most Alert Groups of one bulk command. */
export const BULK_MAX = 100;

/** The mutation key of bulk commands, so that the list keeps its selection while one is under way. */
export const BULK_COMMAND_KEY = ["bulk-command"] as const;

/** alert_group.note_max_length: the most characters of a Note. */
export const NOTE_MAX_LENGTH = 4000;

/** The Commands that have a button, in the order they show; still_on_it (S-050) and add_note (the Note box) have none. */
export const BUTTON_COMMANDS = [
  "acknowledge",
  "unacknowledge",
  "resolve",
  "unresolve",
  "snooze",
  "unsnooze",
] as const satisfies readonly CommandName[];
export type ButtonCommand = (typeof BUTTON_COMMANDS)[number];

/** The buttons of an Alert Group: exactly the Commands its allowed_commands lists, never more. */
export function buttonCommands(group: Pick<AlertGroup, "allowed_commands">): ButtonCommand[] {
  return BUTTON_COMMANDS.filter((c) => group.allowed_commands.includes(c));
}

/** The newer open Alert Group that takes the place of Unresolve, when the page names one. */
export function newerAlertGroup(group: Pick<AlertGroup, "notices">): AlertGroupRef | undefined {
  return group.notices?.find((n) => n.kind === "newer_alert_group_exists")?.related_alert_group;
}

/** The classes that make a dialog a full-screen sheet below Tailwind's sm width (C-09.FR-24). */
export const SHEET =
  "max-sm:top-0 max-sm:left-0 max-sm:h-dvh max-sm:max-h-dvh max-sm:w-full max-sm:max-w-full max-sm:translate-x-0 max-sm:translate-y-0 max-sm:content-start max-sm:rounded-none max-h-[calc(100dvh-2rem)] overflow-y-auto";

/** The number of characters of a text as the API counts them: code points, not UTF-16 units. */
export function characterCount(text: string): number {
  // Each surrogate pair is one character.
  return text.length - (text.match(/[\uD800-\uDBFF][\uDC00-\uDFFF]/g)?.length ?? 0);
}

/**
 * A Snooze duration as a quick choice: "1 h", "4 h", "24 h", "30 min"; whole days from two days on, such as "3 d", so
 * that one day stays "24 h" as the On-call profile names it.
 */
export function snoozeDurationLabel(t: TFunction, seconds: number): string {
  const day = 24 * 60 * 60;
  if (seconds > day && seconds % day === 0) {
    return t("duration.days", { value: seconds / day });
  }
  return formatDuration(t, seconds);
}

/** The Problem code of a refused Command, or undefined for any other failure. */
export function refusalCode(err: unknown): string | undefined {
  return isApiError(err) && err.status === 409 ? err.code : undefined;
}

/** The newer open Alert Group a refused Unresolve names. */
export function refusalRelated(err: unknown): AlertGroupRef | undefined {
  return isApiError(err) && err.code === "newer_alert_group_exists"
    ? err.related_alert_group
    : undefined;
}

/** The text of a refusal code of a Command, or undefined for a code this page has no text for. */
export function refusalCodeText(
  t: TFunction,
  code: string | null | undefined,
  related?: AlertGroupRef,
): string | undefined {
  switch (code) {
    case "already_resolved":
      return t("commands.refusals.alreadyResolved");
    case "not_acknowledged":
      return t("commands.refusals.notAcknowledged");
    case "not_snoozed":
      return t("commands.refusals.notSnoozed");
    case "not_resolved":
      return t("commands.refusals.notResolved");
    case "all_alerts_resolved":
      return t("commands.refusals.allAlertsResolved");
    case "resolved_automatically":
      return t("commands.refusals.resolvedAutomatically");
    case "route_deleted":
      return t("commands.refusals.routeDeleted");
    case "newer_alert_group_exists":
      return related === undefined
        ? t("commands.refusals.newerExistsUnknown")
        : t("commands.refusals.newerExists", { number: related.number });
    case "owner_must_be_user":
      return t("commands.refusals.ownerMustBeUser");
    default:
      return undefined;
  }
}

/** The text of a failed Command: its refusal, "not permitted", or the shared texts of failures. */
export function refusalText(t: TFunction, err: unknown): string {
  if (isApiError(err)) {
    if (err.status === 403 && err.code !== "csrf_invalid") {
      return t("commands.refusals.forbidden");
    }
    if (err.status === 409) {
      const text = refusalCodeText(t, err.code, err.related_alert_group);
      if (text !== undefined) {
        return text;
      }
    }
    if (err.errors?.some((e) => e.pointer.endsWith("/until"))) {
      return t("commands.snooze.past");
    }
    if (err.errors?.some((e) => e.pointer === "/note" && e.code === "too_long")) {
      return t("notes.tooLong", { max: NOTE_MAX_LENGTH });
    }
  }
  return problemText(t, err);
}

/** The text of one Alert Group of a bulk result: "#N: done", "#N: {refusal}", "#N: skipped: owned by {Owner}". */
export function bulkItemText(t: TFunction, item: BulkCommandItem, ownerName?: string): string {
  const outcome = (() => {
    switch (item.outcome) {
      case "done":
        return t("commands.bulk.outcomes.done");
      case "unchanged":
        return t("commands.bulk.outcomes.unchanged");
      case "skipped":
        return ownerName === undefined
          ? t("commands.bulk.outcomes.skipped")
          : t("commands.bulk.outcomes.skippedOwned", { name: ownerName });
      case "failed":
        return item.code === "not_found"
          ? t("commands.bulk.outcomes.notFound")
          : t("commands.bulk.outcomes.failed");
      default:
        return refusalCodeText(t, item.code) ?? t("commands.bulk.outcomes.refused");
    }
  })();
  const subject =
    item.number === null || item.number === undefined ? item.alert_group_id : `#${item.number}`;
  return t("commands.bulk.item", { subject, outcome });
}

/** What a Command button sends; Resolve may carry a Note and Snooze names its end. */
export type CommandInput =
  | { command: "acknowledge" | "unacknowledge" | "unresolve" | "unsnooze" }
  | { command: "resolve"; note?: string }
  | { command: "snooze"; snooze: SnoozeRequest };

function send(alertGroupId: string, input: CommandInput): Promise<CommandResult> {
  switch (input.command) {
    case "acknowledge":
      return acknowledgeAlertGroup(alertGroupId);
    case "unacknowledge":
      return unacknowledgeAlertGroup(alertGroupId);
    case "resolve":
      return resolveAlertGroup(alertGroupId, input.note === undefined ? {} : { note: input.note });
    case "unresolve":
      return unresolveAlertGroup(alertGroupId);
    case "snooze":
      return snoozeAlertGroup(alertGroupId, input.snooze);
    default:
      return unsnoozeAlertGroup(alertGroupId);
  }
}

/** Whether a query key is a page set of the Alert Group list. */
function isListPages(key: readonly unknown[]): boolean {
  return key[0] === "/api/v1/alert-groups" && key.at(-1) === "pages";
}

/** Whether a read of an Alert Group is at least as new as the one shown, so that an older answer never replaces it. */
export function notOlder(fresh: AlertGroup, shown: AlertGroup | undefined): boolean {
  return (
    shown === undefined || Date.parse(fresh.last_changed_at) >= Date.parse(shown.last_changed_at)
  );
}

/**
 * Replaces an Alert Group in every loaded page of the list, keeping its label columns; the rows do not move. A row
 * changed later than the answer stays.
 */
export function putListRow(queryClient: QueryClient, group: AlertGroup): void {
  queryClient.setQueriesData<InfiniteData<AlertGroupList, string | undefined>>(
    { predicate: (q) => isListPages(q.queryKey) },
    (data) =>
      data === undefined
        ? data
        : {
            ...data,
            pages: data.pages.map((page) => ({
              ...page,
              items: page.items.map((g) =>
                g.id === group.id && notOlder(group, g)
                  ? { ...group, label_values: g.label_values }
                  : g,
              ),
            })),
          },
  );
}

/** What a Command changed besides the Alert Group itself: its Timeline, Notes, counts and statistics. */
export function invalidateAfterCommand(queryClient: QueryClient, alertGroupId: string): void {
  for (const queryKey of [
    getGetAlertGroupTimelineQueryKey(alertGroupId),
    getListAlertGroupNotesQueryKey(alertGroupId),
    getListRelatedAlertGroupsQueryKey(alertGroupId),
    getGetAlertGroupCountsQueryKey(),
    getGetAlertGroupStatisticsQueryKey(),
  ]) {
    void queryClient.invalidateQueries({ queryKey });
  }
}

/** The mutation key of the Commands of an Alert Group, so that its buttons wait while one is under way. */
export function commandKey(alertGroupId: string): readonly unknown[] {
  return ["alert-group-command", alertGroupId];
}

/**
 * Runs Commands on an Alert Group. The answer's Alert Group replaces the page and the list row at once, with no
 * optimistic guess before it; the live hint that follows reads them again. A refusal reads the Alert Group again, as it
 * changed under the user.
 */
export function useCommand(alertGroupId: string, onDone?: () => void) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: commandKey(alertGroupId),
    mutationFn: (input: CommandInput) => send(alertGroupId, input),
    onSuccess: (result) => {
      const key = getGetAlertGroupQueryKey(alertGroupId);
      queryClient.setQueryData<AlertGroup>(key, (shown) =>
        notOlder(result.alert_group, shown) ? result.alert_group : shown,
      );
      // A read that started before the Command would answer with the Alert Group as it was: it gives way to a new one.
      void queryClient.invalidateQueries({ queryKey: key });
      putListRow(queryClient, result.alert_group);
      invalidateAfterCommand(queryClient, alertGroupId);
      onDone?.();
    },
    onError: (err) => {
      if (refusalCode(err) === undefined) {
        return;
      }
      // The page and the list row show the Alert Group as it is now.
      void queryClient
        .fetchQuery({ ...getGetAlertGroupQueryOptions(alertGroupId), staleTime: 0 })
        .then((group) => putListRow(queryClient, group))
        .catch(() => {
          // What cannot be read stays as it was.
        });
    },
  });
}
