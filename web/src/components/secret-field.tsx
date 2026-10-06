// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A write-only Secret field (C-03.FR-21): a read shows only whether a value is set and when it changed, never the
// value. "Replace" opens an empty input for a new value, "Clear" removes the stored one where the API allows it, and
// without either the stored value is kept.

import { useEffect, useId, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { SecretStatus } from "../api/gen/model";
import { useTimeFormat } from "../lib/time";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** What a save does with a Secret: keep the stored value, replace it with a new one, or clear it. */
export type SecretChange =
  | { mode: "keep" }
  | { mode: "replace"; value: string }
  | { mode: "clear" };

export const KEEP_SECRET: SecretChange = { mode: "keep" };

/** The value of a Secret in an update: omitted (undefined) keeps it, a string replaces it, null clears it. */
export function secretPayload(change: SecretChange): string | null | undefined {
  switch (change.mode) {
    case "keep":
      return undefined;
    case "replace":
      return change.value;
    default:
      return null;
  }
}

export interface SecretFieldProps {
  /** The id of the input, also the base of the ids of its texts. */
  id: string;
  label: string;
  status: SecretStatus | undefined;
  value: SecretChange;
  onChange: (change: SecretChange) => void;
  /** The API accepts null for this Secret, so it can be removed. */
  clearable?: boolean;
  disabled?: boolean;
  error?: string;
}

export function SecretField({
  id,
  label,
  status,
  value,
  onChange,
  clearable = false,
  disabled = false,
  error,
}: SecretFieldProps) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const statusId = useId();
  const errorId = `${id}-error`;
  const set = status?.set === true;
  // A button that goes away with the click hands the focus to "Replace", so that keyboard users keep their place.
  const replaceButton = useRef<HTMLButtonElement>(null);
  const refocus = useRef(false);
  const change = (next: SecretChange) => {
    refocus.current = next.mode !== "replace";
    onChange(next);
  };
  useEffect(() => {
    if (refocus.current) {
      refocus.current = false;
      replaceButton.current?.focus();
    }
  });
  let state: string;
  if (value.mode === "clear") {
    state = t("secret.cleared");
  } else if (!set) {
    state = t("secret.notSet");
  } else if (status?.updated_at) {
    state = t("secret.setChanged", { time: dateTime(status.updated_at) });
  } else {
    state = t("secret.set");
  }
  return (
    <div className="flex flex-col gap-2" data-testid={`secret-${id}`}>
      <Label htmlFor={value.mode === "replace" ? id : undefined} id={`${id}-label`}>
        {label}
      </Label>
      {value.mode === "replace" ? (
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <Input
            id={id}
            type="password"
            className="sm:max-w-sm"
            autoComplete="new-password"
            spellCheck={false}
            autoFocus
            value={value.value}
            disabled={disabled}
            aria-invalid={error !== undefined}
            aria-describedby={error ? `${statusId} ${errorId}` : statusId}
            onChange={(e) => onChange({ mode: "replace", value: e.target.value })}
          />
          <Button variant="outline" disabled={disabled} onClick={() => change(KEEP_SECRET)}>
            {set ? t("secret.keep") : t("common.cancel")}
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <span
            id={statusId}
            role="status"
            className="mr-1 text-sm text-muted-foreground"
            data-testid={`secret-${id}-status`}
          >
            {state}
          </span>
          {!disabled && (
            <Button
              variant="outline"
              size="sm"
              ref={replaceButton}
              aria-describedby={`${id}-label`}
              onClick={() => change({ mode: "replace", value: "" })}
            >
              {set ? t("secret.replace") : t("secret.enter")}
            </Button>
          )}
          {!disabled && clearable && set && value.mode !== "clear" && (
            <Button
              variant="outline"
              size="sm"
              aria-describedby={`${id}-label`}
              onClick={() => change({ mode: "clear" })}
            >
              {t("secret.clear")}
            </Button>
          )}
          {!disabled && value.mode === "clear" && (
            <Button variant="ghost" size="sm" onClick={() => change(KEEP_SECRET)}>
              {t("secret.undo")}
            </Button>
          )}
        </div>
      )}
      {value.mode === "replace" && (
        <span id={statusId} className="text-sm text-muted-foreground">
          {t("secret.writeOnly")}
        </span>
      )}
      {error && (
        <p id={errorId} className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
