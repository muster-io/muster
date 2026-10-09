// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// One request of the template mode of an outgoing webhook (C-15.FR-3): its method, its URL, its headers and its body,
// Go templates that see the Alert Group, its Alerts, the extracted values (.Response), the event, .Notify, .Mentions
// and .Secrets; "Create" and "Open thread" also take extraction rules. A template refused on save is marked at its
// field with its line and column; the body is the template editor of the message templates.

import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { ExtractionRule, HeaderTemplate, RequestTemplateMethod } from "../api/gen/model";
import { ExtractionRules } from "./extraction-rules";
import {
  type FieldNotes,
  HeaderEditor,
  ListProblem,
  TemplateInput,
  shownProblem,
} from "./header-editor";
import { TemplateEditor } from "./template-editor";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

export const METHODS: readonly RequestTemplateMethod[] = ["GET", "POST", "PUT", "PATCH", "DELETE"];

/** A request as the form keeps it: an empty body sends none. */
export interface RequestValues {
  method: RequestTemplateMethod;
  url: string;
  headers: HeaderTemplate[];
  body: string;
  extract: ExtractionRule[];
}

export interface RequestBuilderProps {
  id: string;
  title: string;
  hint: string;
  /** The JSON pointer of the request, such as /template/create. */
  base: string;
  value: RequestValues;
  onChange: (value: RequestValues) => void;
  /** Whether the request takes extraction rules: "Create" and "Open thread". */
  extract: boolean;
  notes: FieldNotes;
  disabled?: boolean;
  /** Shown beside the title, such as the switch of an optional request. */
  action?: ReactNode;
  /** Whether the fields are shown: an optional request that is off shows its title only. */
  open?: boolean;
}

export function RequestBuilder({
  id,
  title,
  hint,
  base,
  value,
  onChange,
  extract,
  notes,
  disabled = false,
  action,
  open = true,
}: RequestBuilderProps) {
  const { t } = useTranslation();
  const set = (patch: Partial<RequestValues>) => onChange({ ...value, ...patch });
  const methodProblem = notes.problem(`${base}/method`);
  const bodyProblem = notes.problem(`${base}/body`);
  return (
    <fieldset
      className="flex min-w-0 flex-col gap-4 rounded-lg border p-3"
      data-testid={`request-${id}`}
      aria-describedby={`${id}-hint`}
    >
      <legend className="px-1 text-sm font-semibold">{title}</legend>
      <div className="-mt-2 flex flex-col gap-2">
        <p id={`${id}-hint`} className="text-sm text-muted-foreground">
          {hint}
        </p>
        {action}
      </div>
      {open && (
        <>
          <div className="grid min-w-0 gap-4 sm:grid-cols-[8rem_minmax(0,1fr)]">
            <div className="flex min-w-0 flex-col gap-2">
              <Label htmlFor={`${id}-method`}>{t("destinations.webhook.method")}</Label>
              <NativeSelect
                id={`${id}-method`}
                className="w-full"
                value={value.method}
                disabled={disabled}
                aria-invalid={methodProblem !== undefined}
                aria-describedby={methodProblem === undefined ? undefined : `${id}-method-error`}
                onChange={(e) => {
                  const method = METHODS.find((m) => m === e.target.value);
                  if (method !== undefined) {
                    set({ method });
                  }
                }}
              >
                {METHODS.map((m) => (
                  <NativeSelectOption key={m} value={m}>
                    {m}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
              <ListProblem id={`${id}-method-error`} pointer={`${base}/method`} notes={notes} />
            </div>
            <TemplateInput
              id={`${id}-url`}
              label={t("destinations.webhook.url")}
              placeholder="https://chat.example.org/api/messages"
              value={value.url}
              pointer={`${base}/url`}
              notes={notes}
              disabled={disabled}
              onChange={(url) => set({ url })}
            />
          </div>
          <HeaderEditor
            id={id}
            base={`${base}/headers`}
            value={value.headers}
            notes={notes}
            disabled={disabled}
            onChange={(headers) => set({ headers })}
          />
          <div className="flex min-w-0 flex-col gap-2">
            <Label htmlFor={`${id}-body`}>{t("destinations.webhook.body")}</Label>
            <TemplateEditor
              id={`${id}-body`}
              label={t("destinations.webhook.bodyOf", { request: title })}
              value={value.body}
              disabled={disabled}
              describedBy={`${id}-body-hint`}
              errors={
                bodyProblem === undefined
                  ? []
                  : [
                      shownProblem(
                        t,
                        `${base}/body`,
                        bodyProblem,
                        notes.unreadable(`${base}/body`, value.body),
                      ),
                    ]
              }
              placeholder={'{"text": {{ .AlertGroup.Title | toJson }}}'}
              onChange={(body) => set({ body })}
            />
            <p id={`${id}-body-hint`} className="text-sm text-muted-foreground">
              {t("destinations.webhook.bodyHint")}
            </p>
          </div>
          {extract && (
            <ExtractionRules
              id={`${id}-extract`}
              base={`${base}/extract`}
              value={value.extract}
              notes={notes}
              disabled={disabled}
              onChange={(rules) => set({ extract: rules })}
            />
          )}
        </>
      )}
    </fieldset>
  );
}
