// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The bar of a selection in the Alert Group list (C-10.FR-14): how many Alert Groups are selected and "Acknowledge",
// "Resolve" (with an optional Note), "Snooze" ("Until" or "No end", no Route durations) and "Unsnooze", each only with
// its Permission. More than alert_group.bulk_max selected disables them with "Select at most 100 Alert Groups.". The
// result lists every Alert Group with its outcome; the rows read themselves again afterwards. It fits 360 pixels.

import { useMutation } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { getAlertGroup, runBulkCommand } from "../api/gen/endpoints/alert-groups/alert-groups";
import type { BulkCommandRequest, BulkCommandRequestCommand } from "../api/gen/model";
import { BULK_COMMAND_KEY, BULK_MAX } from "../lib/commands";
import { useCan } from "./app-shell";
import { RefusalMessage, personName } from "./command-buttons";
import { type BulkResult, BulkResultDialog } from "./bulk-result-dialog";
import { ResolveDialog } from "./resolve-dialog";
import { SnoozeDialog } from "./snooze-dialog";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";

export interface BulkActionsBarProps {
  /** The selected Alert Groups, in the order of the list. */
  ids: readonly string[];
  /** The Owners of the selected Alert Groups as the list shows them, by id. */
  owners: ReadonlyMap<string, string>;
  /** Whether every shown row is selected, and the action that selects them all or none. */
  allShown: boolean;
  onToggleAllShown: () => void;
  onClear: () => void;
  /** Called with the Alert Groups a command ran on, so that their rows read themselves again. */
  onDone: (ids: readonly string[]) => void;
}

/** Whether a bulk command needs no dialog. */
type Direct = Extract<BulkCommandRequestCommand, "acknowledge" | "unsnooze">;

export function BulkActionsBar({
  ids,
  owners,
  allShown,
  onToggleAllShown,
  onClear,
  onDone,
}: BulkActionsBarProps) {
  const { t } = useTranslation();
  const canAck = useCan("alert-groups:acknowledge");
  const canResolve = useCan("alert-groups:resolve");
  const canSnooze = useCan("alert-groups:snooze");
  const [dialog, setDialog] = useState<"resolve" | "snooze" | null>(null);
  const [result, setResult] = useState<BulkResult | null>(null);
  const label = (command: BulkCommandRequestCommand) => {
    switch (command) {
      case "acknowledge":
        return t("commands.buttons.acknowledge");
      case "resolve":
        return t("commands.buttons.resolve");
      case "snooze":
        return t("commands.buttons.snooze");
      default:
        return t("commands.buttons.unsnooze");
    }
  };
  const inFlight = useRef(false);
  const run = useMutation({
    mutationKey: BULK_COMMAND_KEY,
    mutationFn: async (request: BulkCommandRequest) => {
      const answer = await runBulkCommand(request);
      // A skipped Alert Group is named with its Owner as it is now, which may differ from the list's row.
      const now = new Map(owners);
      await Promise.all(
        answer.results
          .filter((r) => r.outcome === "skipped" && r.code === "owned_by_other")
          .map(async (r) => {
            const group = await getAlertGroup(r.alert_group_id).catch(() => undefined);
            if (group?.owner !== undefined) {
              now.set(r.alert_group_id, personName(t, group.owner));
            }
          }),
      );
      return { answer, owners: now };
    },
    onSuccess: ({ answer, owners: named }, request) => {
      setDialog(null);
      setResult({ command: label(request.command), items: answer.results, owners: named });
      onDone(request.alert_group_ids);
    },
    onSettled: () => {
      inFlight.current = false;
    },
  });
  const count = ids.length;
  const tooMany = count > BULK_MAX;
  const disabled = count === 0 || tooMany || run.isPending;
  const send = (request: Omit<BulkCommandRequest, "alert_group_ids">) => {
    // A second press before the first shows as pending sends nothing.
    if (!disabled && !inFlight.current) {
      inFlight.current = true;
      run.mutate({ ...request, alert_group_ids: [...ids] });
    }
  };
  const direct = (command: Direct) => {
    run.reset();
    send({ command });
  };
  const open = (name: "resolve" | "snooze") => {
    run.reset();
    setDialog(name);
  };
  const close = (isOpen: boolean) => {
    if (!isOpen) {
      setDialog(null);
      run.reset();
    }
  };
  const error = run.isError ? (
    <Alert variant="destructive" role="alert">
      <AlertDescription className="text-current wrap-anywhere">
        <RefusalMessage error={run.error} />
      </AlertDescription>
    </Alert>
  ) : undefined;
  return (
    <div
      className="sticky bottom-0 z-10 flex min-w-0 flex-col gap-2 rounded-lg border bg-background p-2 shadow-sm"
      role="region"
      aria-label={t("commands.bulk.region")}
      data-testid="bulk-bar"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span
          className="text-sm font-medium whitespace-nowrap"
          role="status"
          data-testid="bulk-count"
        >
          {t("commands.bulk.selected", { count })}
        </span>
        <Button variant="ghost" size="sm" onClick={onToggleAllShown}>
          {allShown ? t("commands.bulk.selectNone") : t("commands.bulk.selectAll")}
        </Button>
        {count > 0 && (
          <Button variant="ghost" size="sm" onClick={onClear}>
            {t("commands.bulk.clear")}
          </Button>
        )}
      </div>
      <div
        className="flex min-w-0 flex-wrap gap-2"
        role="group"
        aria-label={t("commands.bulk.actions")}
        aria-busy={run.isPending}
      >
        {canAck && (
          <Button size="sm" disabled={disabled} onClick={() => direct("acknowledge")}>
            {t("commands.buttons.acknowledge")}
          </Button>
        )}
        {canResolve && (
          <Button
            size="sm"
            disabled={disabled}
            aria-haspopup="dialog"
            onClick={() => open("resolve")}
          >
            {t("commands.buttons.resolve")}
          </Button>
        )}
        {canSnooze && (
          <Button
            size="sm"
            variant="outline"
            disabled={disabled}
            aria-haspopup="dialog"
            onClick={() => open("snooze")}
          >
            {t("commands.buttons.snooze")}
          </Button>
        )}
        {canSnooze && (
          <Button
            size="sm"
            variant="outline"
            disabled={disabled}
            onClick={() => direct("unsnooze")}
          >
            {t("commands.buttons.unsnooze")}
          </Button>
        )}
      </div>
      {tooMany && (
        <p className="text-sm text-destructive" role="alert" data-testid="bulk-too-many">
          {t("commands.bulk.tooMany", { max: BULK_MAX })}
        </p>
      )}
      {dialog === null && error}
      <ResolveDialog
        open={dialog === "resolve"}
        onOpenChange={close}
        title={t("commands.bulk.resolveTitle", { count })}
        pending={run.isPending}
        error={error}
        onResolve={(note) => send({ command: "resolve", note })}
      />
      <SnoozeDialog
        open={dialog === "snooze"}
        onOpenChange={close}
        title={t("commands.bulk.snoozeTitle", { count })}
        bulk
        pending={run.isPending}
        error={error}
        onSnooze={(snooze) => send({ command: "snooze", snooze })}
      />
      <BulkResultDialog result={result} onClose={() => setResult(null)} />
    </div>
  );
}
