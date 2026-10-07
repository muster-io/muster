// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The label columns of the Alert Group list (C-09.FR-13): label names whose value shows in a column of their own when
// all the Alert Group's Alerts share it. None by default; the chosen names live in the URL with the rest of the view.

import { XIcon } from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

export function LabelColumnsPicker({
  value,
  onChange,
}: {
  value: readonly string[];
  onChange: (columns: string[]) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const [draft, setDraft] = useState("");
  const name = draft.trim();
  const duplicate = value.includes(name);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (name === "" || duplicate) {
      return;
    }
    onChange([...value, name]);
    setDraft("");
  };
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{t("alertGroups.columns.labelColumns")}</Label>
      {value.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label={t("alertGroups.columns.chosen")}>
          {value.map((column) => (
            <li
              key={column}
              className="inline-flex max-w-full items-center gap-0.5 rounded-md border bg-muted py-0.5 pr-0.5 pl-2 font-mono text-xs"
              data-testid="label-column-chip"
            >
              <span className="min-w-0 wrap-anywhere">{column}</span>
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={t("alertGroups.columns.remove", { label: column })}
                onClick={() => onChange(value.filter((c) => c !== column))}
              >
                <XIcon aria-hidden="true" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <form className="flex gap-2" onSubmit={submit}>
        <Input
          id={id}
          className="min-w-0 flex-1 font-mono"
          value={draft}
          autoComplete="off"
          spellCheck={false}
          placeholder="pod"
          aria-describedby={`${id}-hint`}
          onChange={(e) => setDraft(e.target.value)}
        />
        <Button type="submit" variant="outline" disabled={name === "" || duplicate}>
          {t("alertGroups.columns.add")}
        </Button>
      </form>
      <p id={`${id}-hint`} className="text-xs text-muted-foreground">
        {duplicate ? t("alertGroups.columns.duplicate") : t("alertGroups.columns.hint")}
      </p>
    </div>
  );
}
