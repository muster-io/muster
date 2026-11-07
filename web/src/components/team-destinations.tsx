// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Destinations of the Team" on the Team page (C-26.FR-1, AC-9): the Destinations where the Alert Groups of the Team
// arrive from the Routes that deliver to the Teams from the label, each with its Connection and its Severity
// condition, saved as a whole with the Team's `destination_entries`. An Admin adds a Destination from the list and
// removes one; the line under the table names the Routes that deliver to the Teams, or says that none does. Without
// teams:write the section only shows them. A refusal of an entry shows at its row; a newer version of the Team (412)
// is told by the page, not here.

import { useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { XIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { useListRoutes } from "../api/gen/endpoints/routes/routes";
import type { Team } from "../api/gen/model";
import { isApiError, isStale, problemText } from "../lib/api";
import { useCan } from "./app-shell";
import { connectionPlace } from "./destination-label";
import {
  ConditionSelect,
  ENTRIES_MAX,
  type Entry,
  entriesOf,
  routeDestinationErrorText,
  severityConditionText,
  useDestinationChoices,
} from "./route-destinations";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

function sameEntries(a: readonly Entry[], b: readonly Entry[]): boolean {
  return (
    a.length === b.length &&
    a.every(
      (e, i) =>
        e.destination_id === b[i]?.destination_id &&
        e.severity_condition === b[i]?.severity_condition,
    )
  );
}

/** The codes the API refused entries with, by the position of the entry they were sent at. */
function entryRefusals(err: unknown): Map<number, string> {
  const found = new Map<number, string>();
  if (!isApiError(err)) {
    return found;
  }
  for (const item of err.errors ?? []) {
    const match = /^\/destination_entries\/(\d+)(?:\/destination_id)?$/.exec(item.pointer);
    if (match?.[1] !== undefined && !found.has(Number(match[1]))) {
      found.set(Number(match[1]), item.code);
    }
  }
  return found;
}

/** The line with the Routes that deliver to the Teams from the label (needs routes:read). */
function DeliveringRoutes({ team }: { team: Team }) {
  const { t } = useTranslation();
  const canRoutes = useCan("routes:read");
  const routes = useListRoutes(undefined, { query: { enabled: canRoutes } });
  if (!canRoutes) {
    return null;
  }
  if (routes.data === undefined) {
    return routes.isError ? (
      <p className="text-sm text-destructive" role="alert">
        {problemText(t, routes.error)}
      </p>
    ) : (
      <p className="text-sm text-muted-foreground" role="status">
        {t("common.loading")}
      </p>
    );
  }
  const names = routes.data.items.filter((r) => r.deliver_to_teams).map((r) => r.name);
  return (
    <>
      {routes.isError && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, routes.error)}
        </p>
      )}
      <p className="text-sm text-muted-foreground" data-testid="team-destinations-routes">
        {names.length === 0
          ? t("teams.destinations.noRoutes")
          : t("teams.destinations.routes", { team: team.name, routes: names.join(", ") })}
      </p>
    </>
  );
}

export interface TeamDestinationsProps {
  /** The Team as the page was read, or as it saved last. */
  team: Team;
  canWrite: boolean;
  /** Sends the Destinations of the Team, replacing them as a whole; resolves with the saved Team. */
  save: (entries: Entry[]) => Promise<Team>;
}

export function TeamDestinations({ team, canWrite, save }: TeamDestinationsProps) {
  const { t } = useTranslation();
  const choices = useDestinationChoices();
  const canDestinations = useCan("destinations:read");
  const [initial, setInitial] = useState(() => entriesOf(team));
  const [rows, setRows] = useState(initial);
  const [saved, setSaved] = useState(false);
  const submit = useMutation({ mutationFn: (entries: Entry[]) => save(entries) });
  const dirty = !sameEntries(rows, initial);
  const sent = submit.variables ?? [];
  // The refusal is about the entries as they were sent; it stays on the rows until the entries change.
  const refused = sameEntries(sent, rows) ? entryRefusals(submit.error) : new Map<number, string>();
  const shown = refused.size > 0;
  const ids = rows.map((r) => r.destination_id);
  const rest = choices.items.filter((d) => !ids.includes(d.id));
  const duplicate = rows.findIndex((r, i) => ids.indexOf(r.destination_id) !== i);
  const change = (next: Entry[]) => {
    setRows(next);
    setSaved(false);
    submit.reset();
  };
  const body = (
    <>
      {rows.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="team-destinations-empty">
          {t("teams.destinations.empty")}
        </p>
      ) : (
        <div className="overflow-x-auto rounded-md border">
          <table
            className="w-full border-collapse text-left text-sm"
            data-testid="team-destinations"
          >
            <caption className="sr-only">{t("teams.destinations.list")}</caption>
            <thead className="bg-muted/50">
              <tr>
                <th scope="col" className="px-2 py-1.5 font-medium text-muted-foreground">
                  {t("teams.destinations.destination")}
                </th>
                <th scope="col" className="px-2 py-1.5 font-medium text-muted-foreground">
                  {t("teams.destinations.connection")}
                </th>
                <th scope="col" className="px-2 py-1.5 font-medium text-muted-foreground">
                  {t("teams.destinations.condition")}
                </th>
                {canWrite && (
                  <th scope="col" className="px-2 py-1.5">
                    <span className="sr-only">{t("teams.destinations.remove")}</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {rows.map((entry, index) => {
                const destination = choices.items.find((d) => d.id === entry.destination_id);
                const name = destination?.name ?? entry.destination_id;
                const code = refused.get(index) ?? (index === duplicate ? "duplicate" : undefined);
                return (
                  <tr
                    key={entry.destination_id}
                    className="border-t align-top"
                    data-testid="team-destination"
                  >
                    <td className="px-2 py-1.5 wrap-anywhere">
                      {canDestinations && destination !== undefined ? (
                        <Link
                          to="/destinations/$destinationId"
                          params={{ destinationId: destination.id }}
                          className="font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
                        >
                          {name}
                        </Link>
                      ) : (
                        <span className="font-medium">{name}</span>
                      )}
                      {code !== undefined && (
                        <p className="text-sm text-destructive" role="alert">
                          {routeDestinationErrorText(t, code)}
                        </p>
                      )}
                    </td>
                    <td className="px-2 py-1.5 wrap-anywhere text-muted-foreground">
                      {destination === undefined
                        ? ""
                        : (connectionPlace(destination, choices.connections) ?? "")}
                    </td>
                    <td className="px-2 py-1.5">
                      {canWrite ? (
                        <ConditionSelect
                          id={`team-destination-condition-${index}`}
                          testId={`team-destination-condition-${entry.destination_id}`}
                          label={t("routes.destinations.conditionFor", { name })}
                          value={entry.severity_condition}
                          onChange={(next) =>
                            change(
                              rows.map((e, i) =>
                                i === index ? { ...e, severity_condition: next } : e,
                              ),
                            )
                          }
                        />
                      ) : (
                        <span data-testid={`team-destination-condition-${entry.destination_id}`}>
                          {severityConditionText(t, entry.severity_condition)}
                        </span>
                      )}
                    </td>
                    {canWrite && (
                      <td className="px-2 py-1.5 text-right">
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-xs"
                          aria-label={t("teams.destinations.removeNamed", { name })}
                          onClick={() => change(rows.filter((_, i) => i !== index))}
                        >
                          <XIcon aria-hidden="true" />
                        </Button>
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {canWrite && (
        <div className="flex max-w-xl min-w-0 flex-col gap-1.5">
          <Label htmlFor="team-destinations-add">{t("teams.destinations.add")}</Label>
          <NativeSelect
            id="team-destinations-add"
            className="w-full"
            value=""
            disabled={rest.length === 0 || rows.length >= ENTRIES_MAX}
            onChange={(e) => {
              if (e.target.value !== "" && !ids.includes(e.target.value)) {
                change([...rows, { destination_id: e.target.value, severity_condition: "any" }]);
              }
            }}
          >
            <NativeSelectOption value="">
              {choices.loaded && choices.items.length === 0
                ? t("teams.destinations.noneExist")
                : t("teams.destinations.choose")}
            </NativeSelectOption>
            {rest.map((d) => (
              <NativeSelectOption key={d.id} value={d.id}>
                {d.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
      )}
      <DeliveringRoutes team={team} />
    </>
  );
  if (!canWrite) {
    return <div className="flex min-w-0 flex-col gap-3">{body}</div>;
  }
  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        setSaved(false);
        if (duplicate >= 0 || rows.length > ENTRIES_MAX) {
          return;
        }
        submit.mutate(rows, {
          onSuccess: (updated) => {
            const next = entriesOf(updated);
            setInitial(next);
            setRows(next);
            setSaved(true);
          },
        });
      }}
    >
      {body}
      {submit.isError && !isStale(submit.error) && !shown && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, submit.error)}
          </AlertDescription>
        </Alert>
      )}
      <div className="flex items-center gap-3">
        <Button type="submit" disabled={submit.isPending}>
          {t("common.save")}
        </Button>
        <span
          role="status"
          className="text-sm text-muted-foreground"
          data-testid="team-destinations-status"
        >
          {saved && !dirty ? t("common.saved") : ""}
        </span>
      </div>
    </form>
  );
}
