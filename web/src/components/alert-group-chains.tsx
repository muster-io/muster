// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Escalation chains of an Alert Group's page (C-32.FR-18, C-09.FR-14): one row for each run since the chains last
// started, with the chain's name linked to its page, the step it is at and when its next step is due, or "ran out" or
// "stopped". A chain that was deleted since shows the name it had, with no link. Nothing shows while the Alert Group has
// had no chain. The rows stack on a phone, so that nothing scrolls sideways.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import type { AlertGroupChain } from "../api/gen/model";
import { useTimeFormat } from "../lib/time";
import { DocsLink } from "./docs-link";

/** What a run is doing: "step 2 · next step at …", "ran out" or "stopped". */
export function chainState(
  t: TFunction,
  run: AlertGroupChain,
  dateTime: (iso: string) => string,
): string {
  if (run.state === "ran_out") {
    return t("alertGroups.chains.ranOut");
  }
  if (run.state === "stopped") {
    return t("alertGroups.chains.stopped");
  }
  const parts: string[] = [];
  if (run.step !== null) {
    parts.push(t("alertGroups.chains.step", { n: run.step.number }));
  }
  if (run.next_step_at !== null) {
    parts.push(t("alertGroups.chains.nextStep", { time: dateTime(run.next_step_at) }));
  }
  return parts.join(" · ");
}

/** The name of a run's chain, linked to its page; a chain deleted since shows the name it had. */
export function ChainName({ run }: { run: AlertGroupChain }) {
  if (run.chain === null) {
    return <span className="font-medium wrap-anywhere">{run.chain_name}</span>;
  }
  return (
    <Link
      to="/escalation-chains/$chainId"
      params={{ chainId: run.chain.id }}
      className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {run.chain.name}
    </Link>
  );
}

export function AlertGroupChains({ chains }: { chains: readonly AlertGroupChain[] }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const id = useId();
  if (chains.length === 0) {
    return null;
  }
  return (
    <section
      aria-labelledby={`${id}-title`}
      className="flex min-w-0 flex-col gap-2"
      data-testid="alert-group-chains"
    >
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 id={`${id}-title`} className="text-sm font-semibold">
          {t("alertGroups.chains.title")}
        </h2>
        <DocsLink
          to="field.alert_group_chains"
          kind="learn_more"
          about={t("docs.fields.alertGroupChains")}
        />
      </div>
      <ul className="flex flex-col rounded-lg border">
        {chains.map((run) => (
          <li
            key={run.run_id}
            className="flex min-w-0 flex-col gap-0.5 px-3 py-2 not-last:border-b"
            data-testid="alert-group-chain"
            data-state={run.state}
          >
            <span className="flex flex-wrap items-baseline gap-x-2 text-sm">
              <span className="text-muted-foreground">{t("alertGroups.chains.chain")}</span>
              <ChainName run={run} />
            </span>
            <span
              className={
                run.state === "running"
                  ? "text-sm wrap-anywhere"
                  : "text-sm text-muted-foreground wrap-anywhere"
              }
              data-testid="alert-group-chain-state"
            >
              {chainState(t, run, dateTime)}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
