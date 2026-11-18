// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "I am an Assignee" on the Home page (C-32.FR-18, C-32.AC-10): the open Alert Groups where an Escalation chain Paged the
// signed-in User and reached them, each with its status, its Assignees, the chains and their steps, and the buttons
// "Acknowledge" and "Snooze…" of the Alert Group page. An Alert Group leaves the list once acknowledged, snoozed or
// resolved: the list is read again when a Command ends and when an Alert Group changes. A row stacks on a phone, so that
// nothing scrolls sideways.

import { useIsMutating, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import {
  getListAlertGroupsQueryKey,
  useListAlertGroups,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import type { AlertGroup, AlertGroupChain, ListAlertGroupsParams } from "../api/gen/model";
import { problemText } from "../lib/api";
import { ASSIGNEE_ME } from "../lib/alert-group-search";
import { commandKey } from "../lib/commands";
import { onHint } from "../lib/live";
import { useTimeFormat } from "../lib/time";
import { ChainName, chainState } from "./alert-group-chains";
import { StatusBadge, assigneesText } from "./alert-group-header";
import { CommandButtons } from "./command-buttons";

/** How many Alert Groups the section lists at most. */
const LIMIT = 50;
/** How often the list is read again, so that it follows the business clock and the chains. */
const REFRESH_MS = 15_000;

const PARAMS: ListAlertGroupsParams = { assignee: ASSIGNEE_ME, limit: LIMIT };
/** The key every Command's mutation starts with; a Command of any Alert Group matches it. */
const [COMMAND_KEY_ROOT] = commandKey("");
const ANY_COMMAND: readonly unknown[] = [COMMAND_KEY_ROOT];

/** What a chain is at, short: its step while it runs, "ran out" or "stopped" after. */
function chainLine(t: TFunction, run: AlertGroupChain, dateTime: (iso: string) => string): string {
  return run.state === "running" && run.step !== null
    ? t("alertGroups.chains.step", { n: run.step.number })
    : chainState(t, run, dateTime);
}

function Row({ group }: { group: AlertGroup }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  return (
    <li
      className="flex min-w-0 flex-col gap-2 px-3.5 py-3 not-last:border-b"
      data-testid="assignee-row"
    >
      <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <StatusBadge status={group.status} />
        <Link
          to="/alert-groups/$alertGroupId"
          params={{ alertGroupId: group.id }}
          className="min-w-0 font-semibold wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
          data-testid="assignee-row-title"
        >
          {t("escalation.runs.alertGroup", { number: group.number, title: group.title })}
        </Link>
      </span>
      <span className="text-sm wrap-anywhere" data-testid="assignee-row-assignees">
        {assigneesText(t, group.assignees)}
      </span>
      {group.escalation_chains.length > 0 && (
        <ul className="flex flex-col gap-0.5 text-sm text-muted-foreground">
          {group.escalation_chains.map((run) => (
            <li
              key={run.run_id}
              className="flex flex-wrap items-baseline gap-x-1.5"
              data-testid="assignee-row-chain"
            >
              <span>{t("alertGroups.chains.chain")}</span>
              <ChainName run={run} />
              <span aria-hidden="true">·</span>
              <span>{chainLine(t, run, dateTime)}</span>
            </li>
          ))}
        </ul>
      )}
      <CommandButtons group={group} only={["acknowledge", "snooze"]} dialogMark />
    </li>
  );
}

/** The list is read again when a Command ends, and when an Alert Group changed. */
function useRefresh(refetch: () => unknown) {
  const queryClient = useQueryClient();
  const running = useIsMutating({ mutationKey: ANY_COMMAND });
  const before = useRef(running);
  useEffect(() => {
    if (before.current > 0 && running === 0) {
      void refetch();
    }
    before.current = running;
  }, [running, refetch]);
  useEffect(
    () =>
      onHint("alert-group", () => {
        void queryClient.invalidateQueries({ queryKey: getListAlertGroupsQueryKey(PARAMS) });
      }),
    [queryClient],
  );
}

/** "I am an Assignee" of the Home page. */
export function IAmAnAssigneeSection() {
  const { t } = useTranslation();
  const read = useListAlertGroups(PARAMS, {
    query: { refetchInterval: REFRESH_MS, staleTime: 0 },
  });
  useRefresh(read.refetch);
  // An Alert Group acknowledged a moment ago may still be in an answer that was on its way: it shows no Assignees.
  const rows = (read.data?.items ?? []).filter((g) => g.assignees.length > 0);
  return (
    <section
      className="flex min-w-0 flex-col gap-3 rounded-lg border bg-card p-5"
      aria-labelledby="i-am-an-assignee-title"
      data-testid="i-am-an-assignee"
    >
      <div className="flex flex-col gap-0.5">
        <h2 id="i-am-an-assignee-title" className="text-lg font-semibold">
          {t("home.assignee.title")}
        </h2>
        <p className="text-sm text-muted-foreground">{t("home.assignee.hint")}</p>
      </div>
      {read.isError ? (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, read.error)}
        </p>
      ) : read.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {t("common.loading")}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="assignee-empty">
          {t("home.assignee.empty")}
        </p>
      ) : (
        <ul className="flex flex-col rounded-lg border" data-testid="assignee-list">
          {rows.map((group) => (
            <Row key={group.id} group={group} />
          ))}
        </ul>
      )}
    </section>
  );
}
