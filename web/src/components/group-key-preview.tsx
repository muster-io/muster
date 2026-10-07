// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Group key preview (C-08.FR-5): how the Alerts that the Stored Snapshots of a period reported firing, and that
// the Route would take at its place in the evaluation order, group with the current and with the proposed Group key —
// how many Alert Groups, and the largest of them with their key values and Alert counts. While a Route is created the
// request carries its unsaved Matchers and the preview shows the proposed side alone. Key values come from alerts and
// show as text only.

import { useMutation } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { previewGroupKey } from "../api/gen/endpoints/routes/routes";
import type {
  GroupKeyPreview,
  GroupKeyPreviewRequest,
  GroupKeyPreviewSide,
} from "../api/gen/model";
import { fieldErrorText, isApiError, problemText } from "../lib/api";
import { isMatcherPointer } from "./matcher-builder";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The periods offered, in seconds; routing.group_key_preview_period (24 hours) is the default. */
export const PREVIEW_PERIODS = [3600, 86400, 604800, 1209600] as const;
type Period = (typeof PREVIEW_PERIODS)[number];
const DEFAULT_PERIOD: Period = 86400;

function periodLabel(t: TFunction, seconds: Period): string {
  switch (seconds) {
    case 3600:
      return t("routes.preview.periods.hour");
    case 86400:
      return t("routes.preview.periods.day");
    case 604800:
      return t("routes.preview.periods.week");
    default:
      return t("routes.preview.periods.twoWeeks");
  }
}

/** What the preview asks for besides the period: the saved Route, the Matchers and the proposed key. */
export type PreviewQuery = Omit<GroupKeyPreviewRequest, "period_seconds">;

/** The text of a refusal that is about the preview itself rather than a field of the form. */
function refusalText(t: TFunction, err: unknown): string {
  if (isApiError(err)) {
    const period = err.errors?.find((e) => e.pointer === "/period_seconds");
    if (period !== undefined) {
      return t("routes.preview.periodOutOfRange");
    }
    const other = err.errors?.find(
      (e) => !isMatcherPointer(e.pointer) && !e.pointer.startsWith("/proposed_group_key"),
    );
    if (other !== undefined) {
      return fieldErrorText(t, other.code);
    }
  }
  return problemText(t, err);
}

/** Whether a refusal is about fields of the form only, which show it under those fields. */
function onFormFields(err: unknown): boolean {
  return (
    isApiError(err) &&
    (err.errors?.length ?? 0) > 0 &&
    (err.errors ?? []).every(
      (e) => isMatcherPointer(e.pointer) || e.pointer.startsWith("/proposed_group_key"),
    )
  );
}

/** The announced summary of a preview: the counts of both sides. */
function summary(t: TFunction, result: GroupKeyPreview): string {
  const proposed = t("routes.preview.proposed", { count: result.proposed.alert_group_count });
  return result.current === undefined
    ? proposed
    : `${t("routes.preview.current", { count: result.current.alert_group_count })}. ${proposed}`;
}

/**
 * The columns of a side: the labels its examples carry, in the order of the key the form knows, so that a key changed
 * elsewhere since the form read it still shows its own columns.
 */
function sideKey(side: GroupKeyPreviewSide, known: readonly string[]): string[] {
  const labels = Object.keys(side.examples[0]?.group_key_values ?? {});
  if (labels.length === 0) {
    return [...known];
  }
  return [...known.filter((l) => labels.includes(l)), ...labels.filter((l) => !known.includes(l))];
}

function ExampleTable({
  side,
  groupKey,
  caption,
}: {
  side: GroupKeyPreviewSide;
  groupKey: readonly string[];
  caption: string;
}) {
  const { t } = useTranslation();
  if (side.examples.length === 0) {
    return null;
  }
  return (
    <div className="overflow-x-auto rounded-md border">
      <table className="w-full border-collapse text-left text-sm">
        <caption className="sr-only">{caption}</caption>
        <thead className="bg-muted/50">
          <tr>
            {groupKey.length === 0 ? (
              <th scope="col" className="px-2 py-1.5 font-medium text-muted-foreground">
                {t("routes.preview.allAlerts")}
              </th>
            ) : (
              groupKey.map((label) => (
                <th
                  key={label}
                  scope="col"
                  className="px-2 py-1.5 font-mono text-xs font-medium wrap-anywhere text-muted-foreground"
                >
                  {label}
                </th>
              ))
            )}
            <th
              scope="col"
              className="px-2 py-1.5 text-right font-medium whitespace-nowrap text-muted-foreground"
            >
              {t("routes.preview.alerts")}
            </th>
          </tr>
        </thead>
        <tbody>
          {side.examples.map((example) => (
            <tr
              key={JSON.stringify(groupKey.map((l) => example.group_key_values[l] ?? ""))}
              className="border-t align-top"
              data-testid="preview-example"
            >
              {groupKey.length === 0 ? (
                <td className="px-2 py-1.5 text-muted-foreground">
                  {t("routes.preview.oneGroup")}
                </td>
              ) : (
                groupKey.map((label) => {
                  const value = example.group_key_values[label] ?? "";
                  return (
                    <td key={label} className="px-2 py-1.5 font-mono text-xs wrap-anywhere">
                      {value === "" ? (
                        <>
                          <span aria-hidden="true" className="text-muted-foreground">
                            ""
                          </span>
                          <span className="sr-only">{t("routes.preview.emptyValue")}</span>
                        </>
                      ) : (
                        value
                      )}
                    </td>
                  );
                })
              )}
              <td className="px-2 py-1.5 text-right tabular-nums">{example.alert_count}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Side({
  title,
  side,
  groupKey,
  testId,
}: {
  title: string;
  side: GroupKeyPreviewSide;
  groupKey: readonly string[];
  testId: string;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <p className="text-sm font-medium" data-testid={testId}>
        {title}
      </p>
      <ExampleTable side={side} groupKey={groupKey} caption={title} />
    </div>
  );
}

export function GroupKeyPreviewPanel({
  query,
  currentKey,
  onProblem,
}: {
  query: PreviewQuery;
  /** The saved Group key, for the columns of the current side. */
  currentKey?: readonly string[];
  /** Tells the form about a refusal, so that errors of its fields show under them; null clears them. */
  onProblem: (err: unknown) => void;
}) {
  const { t } = useTranslation();
  const periodId = useId();
  const titleId = useId();
  const [period, setPeriod] = useState<Period>(DEFAULT_PERIOD);
  const [shown, setShown] = useState<{ request: string; key: string[]; result: GroupKeyPreview }>();
  const request = JSON.stringify({ ...query, period_seconds: period });
  const preview = useMutation({
    mutationFn: (body: GroupKeyPreviewRequest) => previewGroupKey(body),
    onMutate: () => onProblem(null),
    onSuccess: (result, body) =>
      setShown({ request: JSON.stringify(body), key: [...body.proposed_group_key], result }),
    onError: (err) => onProblem(err),
  });
  const result = shown?.result;
  const outdated = shown !== undefined && shown.request !== request;
  return (
    <section
      aria-labelledby={titleId}
      className="flex min-w-0 flex-col gap-3 rounded-lg border p-3"
      data-testid="group-key-preview"
    >
      <div className="flex flex-col gap-1">
        <h3 id={titleId} className="text-sm font-medium">
          {t("routes.preview.title")}
        </h3>
        <p className="text-sm text-muted-foreground">{t("routes.preview.hint")}</p>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={periodId}>{t("routes.preview.period")}</Label>
          <NativeSelect
            id={periodId}
            value={String(period)}
            onChange={(e) => {
              const next = PREVIEW_PERIODS.find((p) => String(p) === e.target.value);
              if (next !== undefined) {
                setPeriod(next);
              }
            }}
          >
            {PREVIEW_PERIODS.map((p) => (
              <NativeSelectOption key={p} value={String(p)}>
                {periodLabel(t, p)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        <Button
          type="button"
          variant="outline"
          disabled={preview.isPending}
          onClick={() => preview.mutate({ ...query, period_seconds: period })}
        >
          {preview.isPending ? t("common.loading") : t("routes.preview.run")}
        </Button>
      </div>
      {/* A short summary is announced; the tables are read on demand. */}
      <p className="sr-only" role="status">
        {result !== undefined && !preview.isPending ? summary(t, result) : ""}
      </p>
      <div className="flex min-w-0 flex-col gap-3 empty:hidden">
        {result !== undefined && !preview.isPending && (
          <>
            {result.current !== undefined && (
              <Side
                title={t("routes.preview.current", { count: result.current.alert_group_count })}
                side={result.current}
                groupKey={sideKey(result.current, currentKey ?? [])}
                testId="preview-current"
              />
            )}
            <Side
              title={t("routes.preview.proposed", { count: result.proposed.alert_group_count })}
              side={result.proposed}
              groupKey={shown?.key ?? []}
              testId="preview-proposed"
            />
            {result.truncated && (
              <p className="text-sm text-muted-foreground">{t("routes.preview.truncated")}</p>
            )}
            {outdated && (
              <p className="text-sm text-muted-foreground">{t("routes.preview.outdated")}</p>
            )}
          </>
        )}
      </div>
      {preview.isError && !onFormFields(preview.error) && (
        <p className="text-sm break-words text-destructive" role="alert">
          {refusalText(t, preview.error)}
        </p>
      )}
      {preview.isError && onFormFields(preview.error) && (
        <p className="text-sm text-destructive" role="alert">
          {t("routes.preview.fixFields")}
        </p>
      )}
    </section>
  );
}
