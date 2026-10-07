// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Note box under the Timeline (C-10.FR-8, C-09.FR-14): a Note of up to alert_group.note_max_length characters,
// counted as the API counts them, with a counter; the Note then shows in the Timeline with its author and Transport.
// It shows only while allowed_commands lists add_note. The Resolve dialog takes its optional Note through the same
// field.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { createAlertGroupNote } from "../api/gen/endpoints/alert-groups/alert-groups";
import { fieldErrorText, isApiError } from "../lib/api";
import {
  NOTE_MAX_LENGTH,
  characterCount,
  invalidateAfterCommand,
  refusalText,
} from "../lib/commands";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { Label } from "./ui/label";
import { cn } from "./ui/utils";

/** Whether a Note may be sent: not only white space, and not longer than the limit. */
export function noteValid(text: string): boolean {
  return text.trim() !== "" && characterCount(text) <= NOTE_MAX_LENGTH;
}

/** A Note's text area with its counter; the counter and the error are its description. While a request is under way the
 * text is read-only, so that the focus stays in it. */
export function NoteField({
  id,
  label,
  value,
  onChange,
  disabled = false,
  error,
  rows = 3,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (next: string) => void;
  disabled?: boolean;
  /** A refusal of the server about the text. */
  error?: string;
  rows?: number;
}) {
  const { t } = useTranslation();
  const length = characterCount(value);
  const over = length > NOTE_MAX_LENGTH;
  const message = over ? t("notes.tooLong", { max: NOTE_MAX_LENGTH }) : error;
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <textarea
        id={id}
        rows={rows}
        value={value}
        readOnly={disabled}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={message !== undefined}
        aria-describedby={`${id}-counter${message === undefined ? "" : ` ${id}-error`}`}
        className="w-full min-w-0 resize-y rounded-lg border border-input bg-transparent px-2.5 py-1.5 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 read-only:opacity-70 aria-invalid:border-destructive dark:bg-input/30"
      />
      <div className="flex flex-wrap items-start justify-between gap-x-3 gap-y-1">
        {message === undefined ? (
          <span />
        ) : (
          <p id={`${id}-error`} className="text-sm text-destructive">
            {message}
          </p>
        )}
        <span
          id={`${id}-counter`}
          className={cn(
            "ml-auto text-xs whitespace-nowrap tabular-nums",
            over ? "text-destructive" : "text-muted-foreground",
          )}
          data-testid="note-counter"
        >
          {t("notes.counter", { length, max: NOTE_MAX_LENGTH })}
        </span>
      </div>
    </div>
  );
}

/** The text of a Note refused by the server: a field error on body, or the refusal as a whole. */
export function noteErrorText(
  t: Parameters<typeof refusalText>[0],
  err: unknown,
): string | undefined {
  if (!isApiError(err)) {
    return err === null || err === undefined ? undefined : refusalText(t, err);
  }
  const field = err.errors?.find((e) => e.pointer === "/body" || e.pointer === "/note");
  if (field?.code === "too_long") {
    return t("notes.tooLong", { max: NOTE_MAX_LENGTH });
  }
  return field === undefined ? refusalText(t, err) : fieldErrorText(t, field.code);
}

export function NoteBox({ alertGroupId }: { alertGroupId: string }) {
  const { t } = useTranslation();
  const id = useId();
  const queryClient = useQueryClient();
  const [text, setText] = useState("");
  const inFlight = useRef(false);
  const add = useMutation({
    mutationFn: (body: string) => createAlertGroupNote(alertGroupId, { body }),
    onSuccess: () => {
      setText("");
      invalidateAfterCommand(queryClient, alertGroupId);
    },
    onSettled: () => {
      inFlight.current = false;
    },
  });
  return (
    <Card data-testid="note-box">
      <CardHeader>
        <CardTitle>
          <h2>{t("notes.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <form
          noValidate
          className="flex min-w-0 flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (noteValid(text) && !add.isPending && !inFlight.current) {
              inFlight.current = true;
              add.mutate(text);
            }
          }}
        >
          <NoteField
            id={`${id}-note`}
            label={t("notes.label")}
            value={text}
            onChange={(next) => {
              setText(next);
              if (add.isError) {
                add.reset();
              }
            }}
            disabled={add.isPending}
            error={noteErrorText(t, add.error)}
          />
          <div>
            <Button
              type="submit"
              disabled={!noteValid(text) || add.isPending}
              aria-busy={add.isPending}
            >
              {t("notes.add")}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
