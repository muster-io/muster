// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Escalation chain page (C-32.FR-1, FR-2, FR-3, FR-17): the name, the description and the owning Team, where the
// chain writes ("Writes only in the Threads of {Destinations}. Starts at publication."), when it stops and starts over,
// "Run for" — the Routes and Teams that name it, each with its Severity condition —, the warnings of the chain and of
// its steps, and the steps. One form creates a chain and edits one; a refusal of the server shows at the field or the
// step it names. Without escalation-chains:write the page only shows the chain.

import { useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { TriangleAlertIcon } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import type {
  DestinationRef,
  EscalationChain,
  EscalationChainInput,
  EscalationChainWarning,
  SeverityCondition,
} from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { type StepDraft, draftOf, stepInput } from "../lib/escalation-steps";
import { reasonText } from "../lib/mention-entries";
import { EscalationStepsEditor, type StepIssue, stepIssuesOf } from "./escalation-steps-editor";
import { useTeamList } from "./team-filter";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

const NAME_MAX = 200;
const DESCRIPTION_MAX = 2000;

/** The condition of a reference to the chain, as "Run for" says it: "any severity", "Severity = critical". */
export function runForText(t: TFunction, condition: SeverityCondition): string {
  switch (condition) {
    case "any":
      return t("escalation.runFor.any");
    case "critical":
      return t("escalation.runFor.critical");
    case "below_critical":
      return t("escalation.runFor.belowCritical");
    default:
      return condition satisfies never;
  }
}

/** The names of the chain's Destinations as a list in the reader's language: "a and b", "a, b and c". */
function namesList(language: string, names: readonly string[]): string {
  try {
    return new Intl.ListFormat(language, { type: "conjunction" }).format(names);
  } catch {
    return names.join(", ");
  }
}

/** A warning of the chain or of one of its steps, in words. */
export function warningText(
  t: TFunction,
  warning: EscalationChainWarning,
  destinations: readonly DestinationRef[],
): string {
  const step = warning.step ?? 0;
  switch (warning.kind) {
    case "pages_nobody":
      return t("escalation.warnings.pagesNobody");
    case "team_deleted":
      return t("escalation.warnings.teamDeleted", { step });
    case "gateway_deleted":
      return t("escalation.warnings.gatewayDeleted", { step });
    case "destination_deleted":
      return t("escalation.warnings.destinationDeleted", {
        step,
        destination: warning.destination?.name ?? "",
      });
    case "everyone_unsupported": {
      const where = warning.destination;
      const type = destinations.find((d) => d.id === where?.id)?.type ?? "webhook";
      const why =
        warning.reason === null ? "" : ` ${reasonText(t, warning.reason, type, undefined)}`;
      return `${t("escalation.warnings.everyoneUnsupported", {
        step,
        destination: where?.name ?? "",
      })}${why}`;
    }
    default:
      // A kind a later version added shows as it is.
      return String(warning.kind);
  }
}

/** The warnings of a chain, the whole chain's first, as the API lists them. */
export function ChainWarnings({ chain }: { chain: EscalationChain }) {
  const { t } = useTranslation();
  if (chain.warnings.length === 0) {
    return null;
  }
  return (
    <section
      aria-label={t("escalation.warnings.title")}
      className="flex min-w-0 flex-col gap-2"
      data-testid="chain-warnings"
    >
      {chain.warnings.map((w) => (
        <p
          key={`${w.kind}-${w.step ?? 0}-${w.destination?.id ?? ""}`}
          className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm text-foreground"
          role="status"
          data-testid="chain-warning"
          data-kind={w.kind}
        >
          <TriangleAlertIcon
            aria-hidden="true"
            className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
          />
          <span className="min-w-0 wrap-anywhere">{warningText(t, w, chain.destinations)}</span>
        </p>
      ))}
    </section>
  );
}

/** "Writes only in the Threads of …", when the chain stops and starts over, and "Run for". */
function ChainBehaviour({ chain }: { chain: EscalationChain }) {
  const { t, i18n } = useTranslation();
  const names = chain.destinations.map((d) => d.name);
  const referrers = [
    ...chain.referrers.routes.map((r) => ({
      key: `route-${r.route.id}`,
      kind: "route" as const,
      id: r.route.id,
      name: r.route.name,
      condition: r.severity_condition,
    })),
    ...chain.referrers.teams.map((r) => ({
      key: `team-${r.team.id}`,
      kind: "team" as const,
      id: r.team.id,
      name: r.team.name,
      condition: r.severity_condition,
    })),
  ];
  return (
    <section className="flex min-w-0 flex-col gap-3" data-testid="chain-behaviour">
      <p className="text-sm" data-testid="chain-writes">
        {names.length === 0
          ? t("escalation.writes.none")
          : t("escalation.writes.text", { destinations: namesList(i18n.language, names) })}
      </p>
      <p className="text-sm text-muted-foreground" data-testid="chain-stops">
        {t("escalation.stops")}
      </p>
      <div className="flex min-w-0 flex-col gap-1.5">
        <h2 className="text-sm font-medium">{t("escalation.runFor.title")}</h2>
        {referrers.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="chain-run-for-none">
            {t("escalation.runFor.none")}
          </p>
        ) : (
          <ul className="flex flex-col gap-1 text-sm" data-testid="chain-run-for">
            {referrers.map((r) => (
              <li key={r.key} className="wrap-anywhere" data-testid="chain-referrer">
                {r.kind === "route" ? (
                  <Link
                    to="/routes/$routeId"
                    params={{ routeId: r.id }}
                    className="font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
                  >
                    {t("escalation.runFor.route", { name: r.name })}
                  </Link>
                ) : (
                  <Link
                    to="/teams/$teamId"
                    params={{ teamId: r.id }}
                    className="font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
                  >
                    {t("escalation.runFor.team", { name: r.name })}
                  </Link>
                )}
                {": "}
                <span className="text-muted-foreground">{runForText(t, r.condition)}</span>
              </li>
            ))}
          </ul>
        )}
        <p className="text-sm text-muted-foreground">{t("escalation.runFor.hint")}</p>
      </div>
    </section>
  );
}

interface Values {
  name: string;
  description: string;
  owningTeamId: string;
  steps: StepDraft[];
}

function valuesOf(chain: EscalationChain | undefined, initialTeam: string): Values {
  return {
    name: chain?.name ?? "",
    description: chain?.description ?? "",
    owningTeamId: chain === undefined ? initialTeam : (chain.owning_team_id ?? ""),
    steps: (chain?.steps ?? []).map(draftOf),
  };
}

function snapshot(v: Values): string {
  return JSON.stringify({
    ...v,
    steps: v.steps.map(({ key: _key, ...rest }) => rest),
  });
}

export interface EscalationChainFormProps {
  /** The chain of an edit; none creates one. */
  chain?: EscalationChain;
  /** The Team a new chain starts with, by id. */
  initialTeam?: string;
  readOnly?: boolean;
  submitLabel: string;
  /** Sends the chain; resolves with the saved chain, a refusal is shown by the form. */
  save: (input: EscalationChainInput) => Promise<EscalationChain | void>;
  onCancel?: () => void;
  /** A newer version than the form's was read while the form had changes. */
  stale?: boolean;
  /** Reads the chain again, which a change another person made meanwhile asks for. */
  onReload?: () => void;
}

export function EscalationChainForm({
  chain,
  initialTeam = "",
  readOnly = false,
  submitLabel,
  save,
  onCancel,
  stale = false,
  onReload,
}: EscalationChainFormProps) {
  const { t } = useTranslation();
  const id = useId();
  const [initial, setInitial] = useState(() => valuesOf(chain, initialTeam));
  const [values, setValues] = useState(initial);
  const [saved, setSaved] = useState(false);
  const [nameError, setNameError] = useState<string>();
  const [fieldErrors, setFieldErrors] = useState<{ description?: string; team?: string }>({});
  const [issues, setIssues] = useState<StepIssue[]>([]);
  const teams = useTeamList();
  const dirty = snapshot(values) !== snapshot(initial);
  const owner = teams.data?.find((team) => team.id === values.owningTeamId);
  const ownerRef =
    values.owningTeamId === ""
      ? undefined
      : {
          id: values.owningTeamId,
          name: owner?.name ?? chain?.owning_team?.name ?? values.owningTeamId,
        };
  const set = (patch: Partial<Values>) => {
    setValues((v) => ({ ...v, ...patch }));
    setSaved(false);
  };
  const submit = useMutation({
    mutationFn: (input: EscalationChainInput) => save(input),
    onMutate: () => {
      setNameError(undefined);
      setFieldErrors({});
      setIssues([]);
    },
    onSuccess: (result) => {
      if (result !== undefined) {
        const next = valuesOf(result, "");
        setInitial(next);
        setValues(next);
      } else {
        setInitial(values);
      }
      setSaved(true);
    },
    onError: (err) => {
      if (!isApiError(err)) {
        return;
      }
      if (err.status === 409 && err.code === "name_taken") {
        setNameError("name_taken");
        return;
      }
      const errors = err.errors ?? [];
      const { issues: found } = stepIssuesOf(errors);
      setIssues(found);
      const next: { description?: string; team?: string } = {};
      for (const e of errors) {
        if (e.pointer === "/name") {
          setNameError(e.code);
        } else if (e.pointer === "/description") {
          next.description = e.code;
        } else if (e.pointer === "/owning_team_id") {
          next.team = e.code;
        }
      }
      setFieldErrors(next);
    },
  });
  const nameText =
    nameError === undefined
      ? undefined
      : nameError === "name_taken"
        ? t("escalation.errors.nameTaken")
        : fieldErrorText(t, nameError);

  if (readOnly) {
    return (
      <div className="flex min-w-0 flex-col gap-6" data-testid="chain-read-only">
        {chain !== undefined && <ChainWarnings chain={chain} />}
        <p className="text-sm text-muted-foreground">{t("escalation.form.readOnly")}</p>
        {chain !== undefined && (
          <>
            {chain.description !== "" && (
              <p
                className="text-sm break-words whitespace-pre-wrap"
                data-testid="chain-description"
              >
                {chain.description}
              </p>
            )}
            <p className="text-sm" data-testid="chain-owner">
              {t("escalation.form.owner")}:{" "}
              {chain.owning_team === null ? t("escalation.form.noOwner") : chain.owning_team.name}
            </p>
            <ChainBehaviour chain={chain} />
          </>
        )}
        <EscalationStepsEditor
          steps={values.steps}
          stored={chain?.steps ?? []}
          destinations={chain?.destinations ?? []}
          owningTeam={ownerRef}
        />
        {onCancel !== undefined && (
          <div>
            <Button type="button" variant="outline" onClick={onCancel}>
              {t("escalation.form.back")}
            </Button>
          </div>
        )}
      </div>
    );
  }

  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-6"
      onSubmit={(e) => {
        e.preventDefault();
        setSaved(false);
        if (values.name.trim() === "") {
          setNameError("required");
          return;
        }
        submit.mutate({
          name: values.name.trim(),
          description: values.description.trim(),
          owning_team_id: values.owningTeamId === "" ? null : values.owningTeamId,
          steps: values.steps.map(stepInput),
        });
      }}
    >
      {chain !== undefined && <ChainWarnings chain={chain} />}
      {(stale || isStale(submit.error)) && (
        <Alert variant="destructive" data-testid="chain-stale">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>{t("escalation.errors.stale")}</span>
            {onReload !== undefined && (
              <Button type="button" variant="outline" size="sm" onClick={onReload}>
                {t("common.reload")}
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-name`}>{t("escalation.form.name")}</Label>
        <Input
          id={`${id}-name`}
          autoComplete="off"
          spellCheck={false}
          maxLength={NAME_MAX}
          className="max-w-md"
          value={values.name}
          aria-invalid={nameText !== undefined}
          aria-describedby={nameText === undefined ? undefined : `${id}-name-error`}
          onChange={(e) => {
            set({ name: e.target.value });
            setNameError(undefined);
          }}
        />
        {nameText !== undefined && (
          <p id={`${id}-name-error`} className="text-sm text-destructive" role="alert">
            {nameText}
          </p>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-description`}>{t("escalation.form.description")}</Label>
        <textarea
          id={`${id}-description`}
          rows={2}
          maxLength={DESCRIPTION_MAX}
          className="w-full max-w-md min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1.5 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive dark:bg-input/30"
          value={values.description}
          aria-invalid={fieldErrors.description !== undefined}
          onChange={(e) => set({ description: e.target.value })}
        />
        {fieldErrors.description !== undefined && (
          <p className="text-sm text-destructive" role="alert">
            {fieldErrorText(t, fieldErrors.description)}
          </p>
        )}
      </div>
      <div className="flex min-w-0 flex-col gap-1.5" data-testid="chain-owning-team">
        <Label htmlFor={`${id}-team`}>{t("escalation.form.owner")}</Label>
        <NativeSelect
          id={`${id}-team`}
          className="w-full sm:w-64"
          value={values.owningTeamId}
          aria-invalid={fieldErrors.team !== undefined}
          aria-describedby={`${id}-team-hint`}
          onChange={(e) => set({ owningTeamId: e.target.value })}
        >
          <NativeSelectOption value="">{t("escalation.form.noOwner")}</NativeSelectOption>
          {values.owningTeamId !== "" && owner === undefined && (
            <NativeSelectOption value={values.owningTeamId}>
              {chain?.owning_team?.name ?? values.owningTeamId}
            </NativeSelectOption>
          )}
          {(teams.data ?? []).map((team) => (
            <NativeSelectOption key={team.id} value={team.id}>
              {team.name}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <p id={`${id}-team-hint`} className="text-sm text-muted-foreground">
          {t("escalation.form.ownerHint")}
        </p>
        {fieldErrors.team !== undefined && (
          <p className="text-sm text-destructive" role="alert">
            {fieldErrorText(t, fieldErrors.team)}
          </p>
        )}
      </div>
      {chain !== undefined && <ChainBehaviour chain={chain} />}
      <EscalationStepsEditor
        steps={values.steps}
        onChange={(steps) => {
          set({ steps });
          setIssues([]);
        }}
        stored={chain?.steps ?? []}
        destinations={chain?.destinations ?? []}
        owningTeam={ownerRef}
        issues={issues}
      />
      {submit.isError &&
        !isStale(submit.error) &&
        issues.length === 0 &&
        nameError === undefined && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {problemText(t, submit.error)}
            </AlertDescription>
          </Alert>
        )}
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={submit.isPending}>
          {submitLabel}
        </Button>
        {onCancel !== undefined && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
        )}
        <span
          role="status"
          className="text-sm text-muted-foreground"
          data-testid="chain-save-status"
        >
          {saved && !dirty ? t("common.saved") : ""}
        </span>
      </div>
    </form>
  );
}
