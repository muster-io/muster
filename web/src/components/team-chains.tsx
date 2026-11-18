// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Escalation chain of the Team" on the Team page (C-32.FR-2, C-25.FR-2): the chains the Team names, each with "Run for"
// — the Severity condition of the Team's own reference to the chain —, run on the Alert Groups of the Team where their
// Route runs the Teams' chains, and the chains the Team owns, with "New chain" and the list of chains filtered by the
// Team. Saved as a whole with the Team's `chain_entries`. Without teams:write the section only shows them. A refusal
// of a chain shows at its row; a newer version of the Team (412) is told by the page, not here.

import { useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { XIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { useListEscalationChains } from "../api/gen/endpoints/escalation-chains/escalation-chains";
import type { Team } from "../api/gen/model";
import { isApiError, isStale, problemText } from "../lib/api";
import { useCan } from "./app-shell";
import { runForText } from "./escalation-chain-form";
import {
  CHAIN_ENTRIES_MAX,
  type ChainRow,
  chainErrorText,
  chainRowsOf,
  useChainChoices,
} from "./route-chains";
import { ConditionSelect } from "./route-destinations";
import { Alert, AlertDescription } from "./ui/alert";
import { Button, buttonVariants } from "./ui/button";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

function sameRows(a: readonly ChainRow[], b: readonly ChainRow[]): boolean {
  return (
    a.length === b.length &&
    a.every(
      (e, i) => e.chain_id === b[i]?.chain_id && e.severity_condition === b[i]?.severity_condition,
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
    const match = /^\/chain_entries\/(\d+)(?:\/chain_id)?$/.exec(item.pointer);
    if (match?.[1] !== undefined && !found.has(Number(match[1]))) {
      found.set(Number(match[1]), item.code);
    }
  }
  return found;
}

/** The chains the Team owns, by name, with "New chain" for an Admin. */
function OwnedChains({ team }: { team: Team }) {
  const { t } = useTranslation();
  const canWrite = useCan("escalation-chains:write");
  const owned = useListEscalationChains({ team: team.id, limit: 500 });
  return (
    <div className="flex min-w-0 flex-col gap-2" data-testid="team-owned-chains">
      <h3 className="text-sm font-medium">{t("teams.chains.owned")}</h3>
      {owned.data === undefined ? (
        <p
          className={owned.isError ? "text-sm text-destructive" : "text-sm text-muted-foreground"}
          role={owned.isError ? "alert" : "status"}
        >
          {owned.isError ? problemText(t, owned.error) : t("common.loading")}
        </p>
      ) : owned.data.items.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="team-owned-chains-none">
          {t("teams.chains.ownedNone")}
        </p>
      ) : (
        <ul className="flex flex-col gap-1 text-sm">
          {owned.data.items.map((chain) => (
            <li key={chain.id} data-testid="team-owned-chain" className="wrap-anywhere">
              <Link
                to="/escalation-chains/$chainId"
                params={{ chainId: chain.id }}
                className="font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
              >
                {chain.name}
              </Link>
              <span className="text-muted-foreground">
                {" · "}
                {t("teams.chains.steps", { count: chain.steps.length })}
              </span>
            </li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap items-center gap-3">
        {canWrite && (
          <Link
            to="/escalation-chains/new"
            search={{ team: team.id }}
            className={buttonVariants({ variant: "outline", size: "sm" })}
            data-testid="team-new-chain"
          >
            {t("teams.chains.new")}
          </Link>
        )}
        <Link
          to="/escalation-chains"
          search={{ team: team.id }}
          className="text-sm text-primary underline-offset-4 hover:underline focus-visible:underline"
          data-testid="team-all-chains"
        >
          {t("teams.chains.all")}
        </Link>
      </div>
    </div>
  );
}

export interface TeamChainsProps {
  /** The Team as the page was read, or as it saved last. */
  team: Team;
  canWrite: boolean;
  /** Sends the chains of the Team, replacing them as a whole; resolves with the saved Team. */
  save: (entries: ChainRow[]) => Promise<Team>;
}

export function TeamChains({ team, canWrite, save }: TeamChainsProps) {
  const { t } = useTranslation();
  const choices = useChainChoices();
  const [initial, setInitial] = useState(() => chainRowsOf(team));
  const [rows, setRows] = useState(initial);
  const [saved, setSaved] = useState(false);
  const submit = useMutation({ mutationFn: (entries: ChainRow[]) => save(entries) });
  const dirty = !sameRows(rows, initial);
  const sent = submit.variables ?? [];
  // The refusal is about the entries as they were sent; it stays on the rows until the entries change.
  const refused = sameRows(sent, rows) ? entryRefusals(submit.error) : new Map<number, string>();
  const shown = refused.size > 0;
  const ids = rows.map((r) => r.chain_id);
  const rest = choices.items.filter((c) => !ids.includes(c.id));
  const change = (next: ChainRow[]) => {
    setRows(next);
    setSaved(false);
    submit.reset();
  };
  const nameOf = (chainId: string): string =>
    choices.items.find((c) => c.id === chainId)?.name ??
    team.chains.find((c) => c.id === chainId)?.name ??
    chainId;
  const body = (
    <>
      <p className="text-sm text-muted-foreground">{t("teams.chains.hint")}</p>
      {rows.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="team-chains-empty">
          {t("teams.chains.empty")}
        </p>
      ) : (
        <div className="overflow-x-auto rounded-md border">
          <table className="w-full border-collapse text-left text-sm" data-testid="team-chains">
            <caption className="sr-only">{t("teams.chains.list")}</caption>
            <thead className="bg-muted/50">
              <tr>
                <th scope="col" className="px-2 py-1.5 font-medium text-muted-foreground">
                  {t("teams.chains.chain")}
                </th>
                <th scope="col" className="px-2 py-1.5 font-medium text-muted-foreground">
                  {t("escalation.runFor.title")}
                </th>
                {canWrite && (
                  <th scope="col" className="px-2 py-1.5">
                    <span className="sr-only">{t("teams.chains.remove")}</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {rows.map((entry, index) => {
                const name = nameOf(entry.chain_id);
                const code = refused.get(index);
                return (
                  <tr key={entry.chain_id} className="border-t align-top" data-testid="team-chain">
                    <td className="px-2 py-1.5 wrap-anywhere">
                      <Link
                        to="/escalation-chains/$chainId"
                        params={{ chainId: entry.chain_id }}
                        className="font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
                      >
                        {name}
                      </Link>
                      {code !== undefined && (
                        <p className="text-sm text-destructive" role="alert">
                          {chainErrorText(t, code)}
                        </p>
                      )}
                    </td>
                    <td className="px-2 py-1.5">
                      {canWrite ? (
                        <ConditionSelect
                          id={`team-chain-condition-${index}`}
                          testId={`team-chain-condition-${entry.chain_id}`}
                          label={t("teams.chains.runForChain", { name })}
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
                        <span data-testid={`team-chain-condition-${entry.chain_id}`}>
                          {runForText(t, entry.severity_condition)}
                        </span>
                      )}
                    </td>
                    {canWrite && (
                      <td className="px-2 py-1.5 text-right">
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-xs"
                          aria-label={t("teams.chains.removeNamed", { name })}
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
          <Label htmlFor="team-chains-add">{t("teams.chains.add")}</Label>
          <NativeSelect
            id="team-chains-add"
            className="w-full"
            value=""
            disabled={rest.length === 0 || rows.length >= CHAIN_ENTRIES_MAX}
            onChange={(e) => {
              if (e.target.value !== "" && !ids.includes(e.target.value)) {
                change([...rows, { chain_id: e.target.value, severity_condition: "any" }]);
              }
            }}
          >
            <NativeSelectOption value="">
              {choices.loaded && choices.items.length === 0
                ? t("teams.chains.noneExist")
                : t("teams.chains.choose")}
            </NativeSelectOption>
            {rest.map((c) => (
              <NativeSelectOption key={c.id} value={c.id}>
                {c.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
      )}
    </>
  );
  const owned = <OwnedChains team={team} />;
  if (!canWrite) {
    return (
      <div className="flex min-w-0 flex-col gap-3">
        {body}
        {owned}
      </div>
    );
  }
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <form
        noValidate
        className="flex min-w-0 flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          setSaved(false);
          submit.mutate(rows, {
            onSuccess: () => {
              setInitial(rows);
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
            data-testid="team-chains-status"
          >
            {saved && !dirty ? t("common.saved") : ""}
          </span>
        </div>
      </form>
      {owned}
    </div>
  );
}
