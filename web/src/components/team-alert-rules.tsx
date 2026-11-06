// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Which Alerts belong to this Team" on the Team page (C-25.FR-5): an Alert belongs to the Team when the Team label has
// the Team's slug as its value, or when any one of the Team matchers picks it. Both are kept, with no switch between
// them: the label with the slug, which changes with the slug, and the matchers, in the input of Routes' Matchers, with
// the hints about labels other than `namespace` and about test clusters, and an example of how an Alert Group shows the
// Team. The Matchers are checked as the API checks them before they are sent, and a refusal of a Matcher shows under
// the field it names. Without teams:write the section only shows them.

import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { type Matcher, MatcherOp, type Team } from "../api/gen/model";
import { isApiError, isStale, problemText } from "../lib/api";
import {
  MatcherBuilder,
  type MatcherRow,
  type MatcherRowErrors,
  isMatcherPointer,
  matcherErrors,
  matcherRows,
  matchersOf,
  sentRows,
} from "./matcher-builder";
import { teamRuleText } from "./team-membership";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";

/** The most Team matchers a Team has, as the API takes them. */
export const MATCHERS_MAX = 50;

/** The checks the API repeats on the Team matchers, before they are sent. */
const matchersSchema = z
  .array(
    z.object({
      label: z.string().min(1, "required"),
      op: z.enum(MatcherOp),
      value: z.string(),
    }),
  )
  .max(MATCHERS_MAX, "too_long");

/** The errors the Matchers failed locally, by the key of the row, and a code for the list as a whole. */
function localChecks(rows: readonly MatcherRow[]): {
  rowErrors: Map<number, MatcherRowErrors>;
  list?: string;
} {
  const rowErrors = new Map<number, MatcherRowErrors>();
  let list: string | undefined;
  const sent = sentRows(rows);
  const parsed = matchersSchema.safeParse(matchersOf(rows));
  if (!parsed.success) {
    for (const issue of parsed.error.issues) {
      const [index, field] = issue.path;
      const row = typeof index === "number" ? sent[index] : undefined;
      if (row !== undefined && field === "label") {
        rowErrors.set(row.key, { ...rowErrors.get(row.key), label: { code: issue.message } });
      } else if (row === undefined) {
        list = issue.message;
      }
    }
  }
  return { rowErrors, list };
}

function sameMatchers(a: readonly Matcher[], b: readonly Matcher[]): boolean {
  return (
    a.length === b.length &&
    a.every((m, i) => m.label === b[i]?.label && m.op === b[i]?.op && m.value === b[i]?.value)
  );
}

export interface TeamAlertRulesProps {
  /** The Team as the page was read, or as it saved last. */
  team: Team;
  /** The Team label of the Organization. */
  label: string;
  canWrite: boolean;
  /** Sends the Team matchers, replacing them as a whole; resolves with the saved Team. */
  save: (matchers: Matcher[]) => Promise<Team>;
}

export function TeamAlertRules({ team, label, canWrite, save }: TeamAlertRulesProps) {
  const { t } = useTranslation();
  const [initial, setInitial] = useState<readonly Matcher[]>(team.matchers);
  const [rows, setRows] = useState(() => matcherRows(team.matchers));
  const [saved, setSaved] = useState(false);
  // The checks the Matchers failed before they were sent, until a row changes; and the rows the last request carried,
  // to which the pointers of a refusal belong.
  const [local, setLocal] = useState<ReturnType<typeof localChecks>>({ rowErrors: new Map() });
  const [sentWith, setSentWith] = useState<MatcherRow[]>([]);
  const submit = useMutation({ mutationFn: save });
  const dirty = !sameMatchers(matchersOf(rows), initial);
  const refused = matcherErrors(submit.error, sentWith);
  const errors = new Map<number, MatcherRowErrors>(local.rowErrors);
  for (const [key, found] of refused) {
    errors.set(key, { ...errors.get(key), ...found });
  }
  const shownOnRows = errors.size > 0;
  // A refusal that names no Matcher: the list is too long, or the server failed.
  const otherRefusal =
    submit.isError &&
    !isStale(submit.error) &&
    !(
      isApiError(submit.error) &&
      (submit.error.errors ?? []).some((e) => isMatcherPointer(e.pointer))
    );
  return (
    <div className="flex min-w-0 flex-col gap-5">
      <div className="flex min-w-0 flex-col gap-1.5" data-testid="team-rule-label">
        <h3 className="text-sm font-medium">{t("teams.rules.byLabel")}</h3>
        <p className="font-mono text-sm wrap-anywhere">
          <span>{label}</span> = <span data-testid="team-rule-slug">{team.slug}</span>
        </p>
        <p className="text-sm text-muted-foreground">{t("teams.rules.byLabelHint", { label })}</p>
      </div>
      <form
        noValidate
        className="flex min-w-0 flex-col gap-3"
        data-testid="team-rule-matchers"
        onSubmit={(e) => {
          e.preventDefault();
          setSaved(false);
          const checks = localChecks(rows);
          setLocal(checks);
          if (checks.rowErrors.size > 0 || checks.list !== undefined) {
            return;
          }
          const sent = sentRows(rows);
          setSentWith(sent);
          submit.mutate(matchersOf(rows), {
            onSuccess: (updated) => {
              const next = matcherRows(updated.matchers);
              setInitial(updated.matchers);
              setRows(next);
              setSaved(true);
            },
          });
        }}
      >
        <h3 className="text-sm font-medium">{t("teams.rules.byMatchers")}</h3>
        <p className="text-sm text-muted-foreground">{t("teams.rules.matchersHint")}</p>
        <MatcherBuilder
          id="team-matchers"
          rows={rows}
          readOnly={!canWrite}
          errors={errors}
          onChange={(next) => {
            setRows(next);
            setLocal({ rowErrors: new Map() });
            setSaved(false);
            submit.reset();
          }}
        />
        <p className="text-sm text-muted-foreground">{t("teams.rules.sharedNamespaceHint")}</p>
        {canWrite && (
          <div className="flex flex-col gap-3">
            {local.list !== undefined && (
              <p className="text-sm text-destructive" role="alert">
                {t("teams.rules.tooMany", { max: MATCHERS_MAX })}
              </p>
            )}
            {otherRefusal && !shownOnRows && (
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
                data-testid="team-rules-status"
              >
                {saved && !dirty ? t("common.saved") : ""}
              </span>
            </div>
          </div>
        )}
      </form>
      <div className="flex min-w-0 flex-col gap-1.5" data-testid="team-rule-example">
        <h3 className="text-sm font-medium">{t("teams.rules.example")}</h3>
        <div className="flex min-w-0 flex-wrap gap-x-2 gap-y-0.5 rounded-md border bg-muted/40 px-3 py-2 text-sm">
          <span className="font-medium">{t("alertGroups.fields.teams")}:</span>{" "}
          <span className="wrap-anywhere" data-testid="team-rule-example-teams">
            {team.name} — {teamRuleText(t, "label", null)}
          </span>
        </div>
      </div>
    </div>
  );
}
