// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The extraction rules of the "Create" and "Open thread" requests of an outgoing webhook (C-15.FR-3, FR-4): each takes
// a value from the JSON response by a JSONPath, such as $.data.id, and keeps it under its name for the Alert Group and
// the Destination; the later requests read it as {{ .Response.<name> }}.

import { PlusIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ExtractionRule } from "../api/gen/model";
import { type FieldNotes, ListProblem, problemText, reference, useRowKeys } from "./header-editor";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

export interface ExtractionRulesProps {
  id: string;
  /** The JSON pointer of the rules, such as /template/create/extract. */
  base: string;
  value: ExtractionRule[];
  onChange: (value: ExtractionRule[]) => void;
  notes: FieldNotes;
  disabled?: boolean;
}

export function ExtractionRules({
  id,
  base,
  value,
  onChange,
  notes,
  disabled = false,
}: ExtractionRulesProps) {
  const { t } = useTranslation();
  const rows = useRowKeys(value.length);
  const set = (index: number, patch: Partial<ExtractionRule>) =>
    onChange(value.map((r, i) => (i === index ? { ...r, ...patch } : r)));
  const field = (
    index: number,
    key: keyof ExtractionRule,
    label: string,
    placeholder: string,
    rule: ExtractionRule,
  ) => {
    const fieldId = `${id}-${index}-${key}`;
    const pointer = `${base}/${index}/${key}`;
    const problem = notes.problem(pointer);
    return (
      <div className="flex min-w-0 flex-col gap-2">
        <Input
          id={fieldId}
          className="font-mono"
          autoComplete="off"
          autoCapitalize="off"
          spellCheck={false}
          placeholder={placeholder}
          value={rule[key]}
          disabled={disabled}
          aria-label={label}
          aria-invalid={problem !== undefined}
          aria-describedby={problem === undefined ? `${id}-hint` : `${id}-hint ${fieldId}-error`}
          onChange={(e) => set(index, { [key]: e.target.value })}
        />
        {problem !== undefined && (
          <p
            id={`${fieldId}-error`}
            className="text-sm wrap-anywhere text-destructive"
            data-testid={`${fieldId}-error-text`}
          >
            {problemText(t, pointer, problem, rule[key])}
          </p>
        )}
      </div>
    );
  };
  return (
    <fieldset className="flex min-w-0 flex-col gap-3" data-testid={id}>
      <legend className="mb-2 text-sm font-medium">
        {t("destinations.webhook.extract.title")}
      </legend>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {t("destinations.webhook.extract.hint", { ref: reference("Response", "<name>") })}
      </p>
      {value.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("destinations.webhook.extract.none")}</p>
      )}
      {value.map((rule, index) => {
        const number = index + 1;
        return (
          <div
            key={rows.keys[index] ?? index}
            className="grid min-w-0 gap-2 rounded-lg border p-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto] sm:border-0 sm:p-0"
            data-testid="extraction-rule"
          >
            {field(index, "name", t("destinations.webhook.extract.name", { number }), "id", rule)}
            {field(
              index,
              "path",
              t("destinations.webhook.extract.path", { number }),
              "$.data.id",
              rule,
            )}
            {!disabled && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="justify-self-end"
                aria-label={t("destinations.webhook.extract.remove", { number })}
                onClick={() => {
                  rows.remove(index);
                  onChange(value.filter((_, i) => i !== index));
                }}
              >
                <XIcon aria-hidden="true" />
              </Button>
            )}
          </div>
        );
      })}
      <ListProblem id={`${id}-error`} pointer={base} notes={notes} />
      {!disabled && (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="w-fit"
          onClick={() => {
            rows.add();
            onChange([...value, { name: "", path: "" }]);
          }}
        >
          <PlusIcon aria-hidden="true" />
          {t("destinations.webhook.extract.add")}
        </Button>
      )}
    </fieldset>
  );
}
