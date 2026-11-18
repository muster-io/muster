// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Escalation chains" of the Route editor (C-32.FR-2, FR-17; C-08.FR-1): the chains the Route names, each with its
// Severity condition, a native select that adds one, and the hint "Pages only in the Threads of the Route's
// Destinations. The Teams from the label Page with their own chains." A Route with a chain, no Destination and "Also
// to the Teams from the label" off shows "Only a Notification gateway can reach anyone from this Escalation chain: the
// Route has no Destination.", which goes once a Destination is named. A refusal of a chain shows at its row.

import { Link } from "@tanstack/react-router";
import { TriangleAlertIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useListEscalationChains } from "../api/gen/endpoints/escalation-chains/escalation-chains";
import type { ChainEntry, EscalationChain, EscalationChainRef } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { useCan } from "./app-shell";
import { DocsLink } from "./docs-link";
import { ConditionSelect, severityConditionText } from "./route-destinations";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** A chain of a Route with its condition, as the form holds it. */
export type ChainRow = Required<ChainEntry>;

/** The most chains a Route names, as the API checks it (`chain_entries`, maxItems). */
export const CHAIN_ENTRIES_MAX = 50;

/** The chains of a stored Route: its `chain_entries`, each with its condition. */
export function chainRowsOf(stored: { chain_entries?: readonly ChainEntry[] }): ChainRow[] {
  return (stored.chain_entries ?? []).map((e) => ({
    chain_id: e.chain_id,
    severity_condition: e.severity_condition ?? "any",
  }));
}

/** The text of a refused chain of a Route. */
export function chainErrorText(t: ReturnType<typeof useTranslation>["t"], code: string): string {
  switch (code) {
    case "unknown_id":
      return t("routes.chains.unknown");
    case "duplicate":
      return t("routes.chains.duplicate");
    default:
      return fieldErrorText(t, code);
  }
}

/** Every chain a form can offer, by name (the API's largest page). */
export function useChainChoices(): { items: EscalationChain[]; loaded: boolean } {
  const canRead = useCan("escalation-chains:read");
  const list = useListEscalationChains({ limit: 500 }, { query: { enabled: canRead } });
  return { items: list.data?.items ?? [], loaded: list.isSuccess };
}

export interface RouteChainsProps {
  /** The base of the ids of the inputs, unique on the page. */
  id: string;
  value: readonly ChainRow[];
  onChange?: (next: ChainRow[]) => void;
  /** The chains the stored Route names, which stay named while the list does not show them. */
  known?: readonly EscalationChainRef[];
  /** How many Destinations the Route names now. */
  destinationCount: number;
  /** Whether the Route also goes to the Teams from the label. */
  deliverToTeams: boolean;
  /** Error codes of a refused save, by the chain they are about. */
  errors?: Record<string, string>;
  readOnly?: boolean;
}

export function RouteChains({
  id,
  value,
  onChange,
  known = [],
  destinationCount,
  deliverToTeams,
  errors = {},
  readOnly = false,
}: RouteChainsProps) {
  const { t } = useTranslation();
  const choices = useChainChoices();
  const ids = value.map((e) => e.chain_id);
  const rest = choices.items.filter((c) => !ids.includes(c.id));
  const nameOf = (chainId: string): string =>
    choices.items.find((c) => c.id === chainId)?.name ??
    known.find((c) => c.id === chainId)?.name ??
    chainId;
  const warn = value.length > 0 && destinationCount === 0 && !deliverToTeams;
  const body = (
    <>
      <div className="mb-1 flex flex-wrap items-baseline gap-x-2 text-sm font-medium">
        <span id={`${id}-title`}>{t("routes.chains.title")}</span>
        <DocsLink to="field.route_chains" kind="learn_more" about={t("docs.fields.routeChains")} />
        <Link
          to="/escalation-chains"
          className="text-sm font-normal text-primary underline-offset-4 hover:underline focus-visible:underline"
        >
          {t("routes.chains.all")}
        </Link>
      </div>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {t("routes.chains.hint")}
      </p>
      {value.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="route-chains-none">
          {t("routes.chains.none")}
        </p>
      ) : (
        <ul className="flex flex-col gap-2" aria-label={t("routes.chains.list")}>
          {value.map((entry, index) => {
            const error = errors[entry.chain_id];
            const name = nameOf(entry.chain_id);
            return (
              <li
                key={entry.chain_id}
                className="flex min-w-0 flex-col gap-2 rounded-lg border px-3 py-2"
                data-testid="route-chain"
              >
                <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
                  <Link
                    to="/escalation-chains/$chainId"
                    params={{ chainId: entry.chain_id }}
                    className="min-w-0 font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
                  >
                    {name}
                  </Link>
                  {!readOnly && onChange !== undefined ? (
                    <div className="ml-auto flex min-w-0 items-center gap-1">
                      <ConditionSelect
                        id={`${id}-condition-${index}`}
                        testId={`route-chain-condition-${entry.chain_id}`}
                        label={t("routes.chains.conditionFor", { name })}
                        value={entry.severity_condition}
                        onChange={(next) =>
                          onChange(
                            value.map((e, i) =>
                              i === index ? { ...e, severity_condition: next } : e,
                            ),
                          )
                        }
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-xs"
                        aria-label={t("routes.chains.remove", { name })}
                        onClick={() => onChange(value.filter((_, i) => i !== index))}
                      >
                        <XIcon aria-hidden="true" />
                      </Button>
                    </div>
                  ) : (
                    <span
                      className="ml-auto text-sm text-muted-foreground"
                      data-testid={`route-chain-condition-${entry.chain_id}`}
                    >
                      {severityConditionText(t, entry.severity_condition)}
                    </span>
                  )}
                </div>
                {error !== undefined && (
                  <p className="text-sm text-destructive" role="alert">
                    {chainErrorText(t, error)}
                  </p>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {warn && (
        <p
          className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm text-foreground"
          role="status"
          data-testid="route-chains-warning"
        >
          <TriangleAlertIcon
            aria-hidden="true"
            className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
          />
          <span className="min-w-0 wrap-anywhere">{t("routes.chains.noDestination")}</span>
        </p>
      )}
      {!readOnly && onChange !== undefined && (
        <div className="flex max-w-xl min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-add`}>{t("routes.chains.add")}</Label>
          <NativeSelect
            id={`${id}-add`}
            className="w-full"
            value=""
            disabled={rest.length === 0 || value.length >= CHAIN_ENTRIES_MAX}
            aria-describedby={`${id}-hint`}
            onChange={(e) => {
              if (e.target.value !== "" && !ids.includes(e.target.value)) {
                onChange([...value, { chain_id: e.target.value, severity_condition: "any" }]);
              }
            }}
          >
            <NativeSelectOption value="">
              {choices.loaded && choices.items.length === 0
                ? t("routes.chains.noneExist")
                : t("routes.chains.choose")}
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
  return readOnly ? (
    <section
      aria-labelledby={`${id}-title`}
      className="flex min-w-0 flex-col gap-2"
      data-testid="route-chains"
    >
      {body}
    </section>
  ) : (
    <div
      role="group"
      aria-labelledby={`${id}-title`}
      className="flex min-w-0 flex-col gap-2"
      data-testid="route-chains"
    >
      {body}
    </div>
  );
}
