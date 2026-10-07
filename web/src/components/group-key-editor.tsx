// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Group key of a Route (C-08.FR-4): the label names whose values split the Route's Alerts into Alert Groups, a
// missing label counting as an empty value. Each name is a chip with a remove button; a name is added from the field.
// The Group key preview sits beside it.

import { XIcon } from "lucide-react";
import { type KeyboardEvent, useRef } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "./ui/button";
import { Input } from "./ui/input";

export function GroupKeyEditor({
  id,
  value,
  onChange,
  error,
  readOnly = false,
  labelledBy,
  draft = "",
  onDraftChange = () => {},
}: {
  /** The id of the name field; the hint and the errors hang off it. */
  id: string;
  value: readonly string[];
  onChange: (key: string[]) => void;
  /** An error of the Group key from the form or the server. */
  error?: string;
  readOnly?: boolean;
  /** The id of the element that names the Group key. */
  labelledBy: string;
  /** The label name typed in the field and not added yet; the form owns it, so that a save can take it. */
  draft?: string;
  onDraftChange?: (draft: string) => void;
}) {
  const { t } = useTranslation();
  const setDraft = onDraftChange;
  const field = useRef<HTMLInputElement>(null);
  const name = draft.trim();
  const duplicate = name !== "" && value.includes(name);
  const add = () => {
    if (name === "" || duplicate) {
      return;
    }
    onChange([...value, name]);
    setDraft("");
  };
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      // Enter adds the name instead of submitting the form.
      e.preventDefault();
      add();
    }
  };
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const message = duplicate ? t("routes.groupKey.duplicate") : (error ?? null);
  return (
    <div className="flex min-w-0 flex-col gap-2">
      {value.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="group-key-empty">
          {t("routes.groupKey.empty")}
        </p>
      ) : (
        <ul className="flex flex-wrap gap-1.5" aria-labelledby={labelledBy}>
          {value.map((label) => (
            <li
              key={label}
              className="inline-flex max-w-full items-center gap-0.5 rounded-md border bg-muted py-0.5 pr-0.5 pl-2 font-mono text-xs"
              data-testid="group-key-label"
            >
              <span className={readOnly ? "min-w-0 pr-1.5 wrap-anywhere" : "min-w-0 wrap-anywhere"}>
                {label}
              </span>
              {!readOnly && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  aria-label={t("routes.groupKey.remove", { label })}
                  onClick={() => {
                    onChange(value.filter((l) => l !== label));
                    field.current?.focus();
                  }}
                >
                  <XIcon aria-hidden="true" />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {!readOnly && (
        <div className="flex gap-2">
          <Input
            ref={field}
            id={id}
            className="max-w-xs min-w-0 flex-1 font-mono"
            value={draft}
            autoComplete="off"
            spellCheck={false}
            placeholder={t("routes.groupKey.placeholder")}
            aria-invalid={message !== null}
            aria-describedby={message === null ? hintId : `${hintId} ${errorId}`}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={onKeyDown}
          />
          <Button type="button" variant="outline" disabled={name === "" || duplicate} onClick={add}>
            {t("routes.groupKey.add")}
          </Button>
        </div>
      )}
      <p id={hintId} className="text-sm text-muted-foreground">
        {t("routes.groupKey.hint")}
      </p>
      {message !== null && (
        <p id={errorId} className="text-sm break-words text-destructive">
          {message}
        </p>
      )}
    </div>
  );
}
