// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Command buttons of an Alert Group (C-10.FR-1, FR-2, FR-4, FR-7, FR-16): built from allowed_commands only — the
// page never works out itself what is allowed. On the page they form a bar right under the header; in a list row
// they sit in a "…" menu. Acknowledge reads "Take over from {Owner}" when another user owns the Alert Group and needs
// no confirmation. Resolve, Unresolve and Snooze open their dialogs. While a newer open Alert Group of the same key
// exists, its link takes the place of Unresolve. A refusal shows its message, linked to the newer Alert Group when it
// names one; the buttons wait while a Command is under way, so that nothing is sent twice.

import { useIsMutating, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { EllipsisIcon } from "lucide-react";
import { useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";

import { getGetRouteQueryOptions } from "../api/gen/endpoints/routes/routes";
import type { AlertGroup, UserRef } from "../api/gen/model";
import { useSession } from "../lib/api";
import {
  type ButtonCommand,
  type CommandInput,
  buttonCommands,
  commandKey,
  newerAlertGroup,
  refusalCode,
  refusalRelated,
  refusalText,
  useCommand,
} from "../lib/commands";
import { useCan } from "./app-shell";
import { ResolveDialog, UnresolveDialog } from "./resolve-dialog";
import { SnoozeDialog } from "./snooze-dialog";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { cn } from "./ui/utils";

/** A user's name, with "(deactivated)" for a deleted user. */
export function personName(t: TFunction, user: Pick<UserRef, "name" | "deactivated">): string {
  return user.deactivated ? t("alertGroups.deactivated", { name: user.name }) : user.name;
}

/** The label of a Command button; Acknowledge of another user's Alert Group is a Takeover. */
export function commandLabel(
  t: TFunction,
  command: ButtonCommand,
  owner: UserRef | undefined,
  me: string | undefined,
): string {
  switch (command) {
    case "acknowledge":
      return owner !== undefined && owner.id !== me
        ? t("commands.buttons.takeOver", { name: personName(t, owner) })
        : t("commands.buttons.acknowledge");
    case "unacknowledge":
      return t("commands.buttons.unacknowledge");
    case "resolve":
      return t("commands.buttons.resolve");
    case "unresolve":
      return t("commands.buttons.unresolve");
    case "snooze":
      return t("commands.buttons.snooze");
    default:
      return t("commands.buttons.unsnooze");
  }
}

const LINK = "font-medium text-primary underline-offset-4 hover:underline focus-visible:underline";

/** The message of a failed Command; a refused Unresolve links the newer Alert Group it names. */
export function RefusalMessage({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const related = refusalRelated(error);
  if (related !== undefined) {
    return (
      <Trans
        i18nKey="commands.refusals.newerExistsLinked"
        values={{ number: related.number }}
        // Not "link", which the parser of Trans takes for the empty HTML element and leaves without its text.
        components={{
          groupLink: (
            <Link
              to="/alert-groups/$alertGroupId"
              params={{ alertGroupId: related.id }}
              className={LINK}
            />
          ),
        }}
      />
    );
  }
  return <>{refusalText(t, error)}</>;
}

/** The refusal of the last Command as an alert. */
function RefusalAlert({ error }: { error: unknown }) {
  return (
    <Alert variant={refusalCode(error) === undefined ? "destructive" : "default"} role="alert">
      <AlertDescription className="text-current wrap-anywhere" data-testid="command-refusal">
        <RefusalMessage error={error} />
      </AlertDescription>
    </Alert>
  );
}

/** The Route's Snooze durations for the dialog, read while it is open and the session may read Routes. */
function useSnoozeDurations(
  routeId: string,
  open: boolean,
): { durations: number[]; loading: boolean } {
  const canRoutes = useCan("routes:read");
  const route = useQuery({
    ...getGetRouteQueryOptions(routeId),
    enabled: open && canRoutes,
  });
  return {
    durations: route.data?.policy.snooze_durations_seconds ?? [],
    // A Route that cannot be read leaves "Until" and "No end".
    loading: canRoutes && route.isPending && route.fetchStatus !== "idle",
  };
}

type DialogName = "resolve" | "unresolve" | "snooze" | null;

/**
 * The state the buttons and the menu share: the Command, its dialog and the refusal of the last attempt. A press while
 * a Command is under way, even one the render has not shown yet, sends nothing.
 */
/** How a Command ended: done, refused after a direct press, or refused inside its dialog, which shows why. */
type Settled = "done" | "refused" | "refusedInDialog";

function useCommands(group: AlertGroup, onSettled?: (how: Settled) => void) {
  const [dialog, setDialog] = useState<DialogName>(null);
  const inFlight = useRef(false);
  const command = useCommand(group.id, () => setDialog(null));
  // Every Command of the Alert Group waits while one is under way, from this view or another.
  const busy = useIsMutating({ mutationKey: commandKey(group.id) }) > 0;
  const run = (input: CommandInput, fromDialog: boolean) => {
    if (busy || inFlight.current) {
      return;
    }
    inFlight.current = true;
    command.mutate(input, {
      onSettled: (_data, error) => {
        inFlight.current = false;
        onSettled?.(error === null ? "done" : fromDialog ? "refusedInDialog" : "refused");
      },
    });
  };
  const press = (c: ButtonCommand) => {
    command.reset();
    if (c === "resolve" || c === "unresolve" || c === "snooze") {
      setDialog(c);
      return;
    }
    run({ command: c }, false);
  };
  return { dialog, setDialog, command, busy, run, press };
}

/** The open dialog of a Command; nothing while none is open, so that a list row carries no dialogs. */
function CommandDialog({
  group,
  state,
}: {
  group: AlertGroup;
  state: ReturnType<typeof useCommands>;
}) {
  const { t } = useTranslation();
  const { dialog, setDialog, command, busy, run } = state;
  const snooze = useSnoozeDurations(group.route.id, dialog === "snooze");
  const close = (open: boolean) => {
    if (!open) {
      setDialog(null);
      command.reset();
    }
  };
  const error = command.isError ? <RefusalAlert error={command.error} /> : undefined;
  const number = group.number;
  switch (dialog) {
    case "resolve":
      return (
        <ResolveDialog
          open
          onOpenChange={close}
          title={t("commands.resolve.title", { number })}
          pending={busy}
          error={error}
          onResolve={(note) => run({ command: "resolve", note }, true)}
        />
      );
    case "unresolve":
      return (
        <UnresolveDialog
          open
          onOpenChange={close}
          title={t("commands.unresolve.title", { number })}
          pending={busy}
          error={error}
          onConfirm={() => run({ command: "unresolve" }, true)}
        />
      );
    case "snooze":
      return (
        <SnoozeDialog
          open
          onOpenChange={close}
          title={t("commands.snooze.title", { number })}
          durations={snooze.durations}
          loading={snooze.loading}
          pending={busy}
          error={error}
          onSnooze={(request) => run({ command: "snooze", snooze: request }, true)}
        />
      );
    default:
      return null;
  }
}

/**
 * The bar of Commands on the Alert Group page. A Viewer, whose allowed_commands is empty, sees nothing. An open dialog
 * and a refusal stay when the Alert Group, read again, offers no Command any more.
 */
export function CommandButtons({ group }: { group: AlertGroup }) {
  const { t } = useTranslation();
  const me = useSession()?.user.id;
  const bar = useRef<HTMLDivElement>(null);
  // A Command changes or disables the buttons: the focus goes back to the bar instead of being lost with the pressed one.
  const state = useCommands(group, (how) => {
    if (how !== "refusedInDialog") {
      bar.current?.focus();
    }
  });
  const newer = newerAlertGroup(group);
  const commands = buttonCommands(group).filter((c) => c !== "unresolve" || newer === undefined);
  const { command, busy, press, dialog } = state;
  if (commands.length === 0 && newer === undefined && dialog === null && !command.isError) {
    return null;
  }
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div
        ref={bar}
        tabIndex={-1}
        role="group"
        aria-label={t("commands.label")}
        aria-busy={busy}
        className="flex min-w-0 flex-wrap items-center gap-2 rounded-lg outline-none"
        data-testid="command-buttons"
      >
        {commands.map((c) => (
          <Button
            key={c}
            variant={c === "acknowledge" || c === "resolve" ? "default" : "outline"}
            className="max-w-full min-w-0"
            disabled={busy}
            aria-haspopup={
              c === "resolve" || c === "unresolve" || c === "snooze" ? "dialog" : undefined
            }
            onClick={() => press(c)}
            data-testid={`command-${c}`}
          >
            <span className="truncate">{commandLabel(t, c, group.owner, me)}</span>
          </Button>
        ))}
        {newer !== undefined && (
          <Link
            to="/alert-groups/$alertGroupId"
            params={{ alertGroupId: newer.id }}
            className={cn("text-sm", LINK)}
            data-testid="newer-alert-group"
          >
            {t("commands.newerExists", { number: newer.number })}
          </Link>
        )}
      </div>
      {command.isError && dialog === null && <RefusalAlert error={command.error} />}
      <CommandDialog group={group} state={state} />
    </div>
  );
}

/** The "…" menu of a list row, with the same Commands; a refusal shows under it. */
export function CommandMenu({ group }: { group: AlertGroup }) {
  const { t } = useTranslation();
  const me = useSession()?.user.id;
  const trigger = useRef<HTMLButtonElement>(null);
  // A refusal of a Command the menu sent puts the focus back on the menu's button.
  const state = useCommands(group, (how) => {
    if (how === "refused") {
      trigger.current?.focus();
    }
  });
  const commands = buttonCommands(group).filter(
    (c) => c !== "unresolve" || newerAlertGroup(group) === undefined,
  );
  const { command, busy, press, dialog } = state;
  if (commands.length === 0 && dialog === null && !command.isError) {
    return null;
  }
  return (
    <div className="flex min-w-0 flex-col items-end gap-1">
      {commands.length > 0 && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                ref={trigger}
                variant="ghost"
                size="icon-sm"
                disabled={busy}
                aria-busy={busy}
                aria-label={t("commands.menu", { number: group.number })}
                data-testid="command-menu"
              />
            }
          >
            <EllipsisIcon aria-hidden="true" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-auto min-w-44">
            {commands.map((c) => (
              <DropdownMenuItem key={c} onClick={() => press(c)} data-testid={`command-${c}`}>
                {commandLabel(t, c, group.owner, me)}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      {command.isError && dialog === null && (
        <p
          className="max-w-48 text-right text-xs text-destructive wrap-anywhere"
          role="alert"
          data-testid="command-refusal"
        >
          <RefusalMessage error={command.error} />
        </p>
      )}
      <CommandDialog group={group} state={state} />
    </div>
  );
}
