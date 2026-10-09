// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The form of a Destination (C-13.FR-9, FR-2; C-12.FR-8), shared by every type: the name, the type's fields in a slot
// (Mattermost, Telegram or the outgoing webhook), the Mention section and the limiter. Saving a Mattermost or Telegram
// Destination runs its Destination check, and a failing check is refused with destination_check_failed at the field it
// concerns and the check's message, which the form shows there; a template of an outgoing webhook that fails is
// refused at its field with its line and column. Without destinations:write the form only shows the Destination.

import { useMutation } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ComponentType, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type {
  Destination,
  DestinationInput,
  DestinationType,
  Limiter,
  MentionSettingEveryone,
  MentionSettings,
} from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { checkErrorText } from "./connection-check";
import {
  LimiterField,
  type LimiterValues,
  limiterErrors,
  limiterInput,
  limiterValues,
} from "./limiter-field";
import {
  MENTION_KINDS,
  type MentionKind,
  MentionSettingsField,
  defaultMentions,
  mentionErrorText,
} from "./mention-settings";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

const ID = "destination";
const NAME_MAX = 200;

/**
 * A refusal of a field: its code; for a failing Destination check, the check's message; for a template, the server's
 * detail and the 1-based line and column of the error.
 */
export interface FieldProblem {
  code: string;
  detail?: string;
  line?: number;
  column?: number;
}

/** Field problems by JSON pointer, such as "/name", "/channel_id" or "/mentions/new_alerts/everyone". */
export type DestinationErrors = Record<string, FieldProblem>;

/** What a type's fields component gets from the form. */
export interface TypeFieldsProps<V> {
  /** The base of the ids of the inputs, unique on the page. */
  id: string;
  value: V;
  onChange: (next: V) => void;
  /** The texts of the problems of the type's fields, by pointer. */
  errors: Record<string, string>;
  /** The problems of the type's fields as refused, by pointer, for fields that show them with their own texts. */
  problems: DestinationErrors;
  disabled: boolean;
  /** The stored Destination of an edit. */
  destination?: Destination;
  /**
   * Offers values the form takes while the user has not changed them: the name of the chosen channel, and the limiter
   * of the chosen Connection (destination.mattermost.limiter is the same as its Connection's).
   */
  suggest: (s: { name?: string; limiter?: Limiter }) => void;
}

/** A type of Destination as the form needs it: its fields, their values and checks, and its Mentions. */
export interface DestinationKind<V> {
  type: DestinationType;
  /** The chat-wide mentions the type takes besides nobody. */
  everyone: readonly MentionSettingEveryone[];
  /** Whether the type takes messenger groups. */
  groups: boolean;
  /** The limiter of a new Destination before anything suggests another. */
  defaultLimiter: Limiter;
  /** The hint under the limiter. */
  limiterHint: (t: TFunction) => string;
  /** The help text of the Mention choices, when the type's differs from the chat-wide one of Mattermost. */
  mentionsHint?: (t: TFunction) => string;
  /** The placeholder of a group's name, when the type's groups are not Mattermost's. */
  groupPlaceholder?: (t: TFunction) => string;
  values: (d: Destination | undefined) => V;
  /** The checks the server repeats on the type's fields, as codes by pointer. */
  check: (v: V) => Record<string, string>;
  /** The request of a save. */
  input: (
    common: { name: string; mentions: MentionSettings; limiter: Limiter },
    v: V,
  ) => DestinationInput;
  /**
   * The pointers of the type's fields, which show their problems themselves; a pointer also takes every pointer below
   * it, such as /events for /events/headers/0/value.
   */
  pointers: readonly string[];
  /**
   * Whether a change of the type's values touches the field at a pointer, whose problem then goes away; without it
   * every change of the type's values clears all their problems.
   */
  changed?: (previous: V, next: V, pointer: string) => boolean;
  Fields: ComponentType<TypeFieldsProps<V>>;
}

interface Values<V> {
  name: string;
  mentions: MentionSettings;
  limiter: LimiterValues;
  fields: V;
}

/** Whether a pointer is one of the type's fields or below one. */
function isTypePointer(pointers: readonly string[], pointer: string): boolean {
  return pointers.some((p) => pointer === p || pointer.startsWith(`${p}/`));
}

function valuesOf<V>(kind: DestinationKind<V>, d: Destination | undefined): Values<V> {
  return {
    name: d?.name ?? "",
    mentions: d?.mentions ?? defaultMentions(),
    limiter: limiterValues(d?.limiter ?? kind.defaultLimiter),
    fields: kind.values(d),
  };
}

/** A write-only proxy password is not a value of the form; only its status tells that another save changed it. */
function proxyPassword(d: Destination): unknown {
  return d.type === "webhook" ? d.proxy.password_status : undefined;
}

/**
 * Whether two versions of a Destination hold the same settings, the values of the form: a change of the Secrets or
 * the Signing secret gives a Destination a new version and leaves its settings as they were.
 */
export function sameSettings<V>(kind: DestinationKind<V>, a: Destination, b: Destination): boolean {
  return (
    JSON.stringify(valuesOf(kind, a)) === JSON.stringify(valuesOf(kind, b)) &&
    JSON.stringify(proxyPassword(a)) === JSON.stringify(proxyPassword(b))
  );
}

/**
 * What a form read at version base does when version current of its Destination arrives: "adopt" it silently when
 * only the Secrets or the Signing secret changed, so that the next save sends it; "replace" an untouched form with it;
 * "keep" a form with changes, whose save is then refused (412); or nothing ("same") when the version is the form's.
 */
export function versionAction<V>(
  kind: DestinationKind<V>,
  current: Destination,
  base: Destination,
  dirty: boolean,
): "same" | "adopt" | "replace" | "keep" {
  if (current.etag === base.etag) {
    return "same";
  }
  if (sameSettings(kind, current, base)) {
    return "adopt";
  }
  return dirty ? "keep" : "replace";
}

/** The checks the server repeats, before the form is sent. */
export function destinationErrors<V>(kind: DestinationKind<V>, v: Values<V>): DestinationErrors {
  const errors: DestinationErrors = {};
  const name = v.name.trim();
  if (name === "") {
    errors["/name"] = { code: "required" };
  } else if (Array.from(name).length > NAME_MAX) {
    errors["/name"] = { code: "too_long" };
  }
  for (const [pointer, code] of Object.entries(kind.check(v.fields))) {
    errors[pointer] = { code };
  }
  for (const [field, code] of Object.entries(limiterErrors(v.limiter))) {
    errors[`/limiter/${field}`] = { code };
  }
  return errors;
}

/** The field problems of a refused save by pointer; the server names the whole limiter as /limiter. */
export function serverErrors(err: unknown): DestinationErrors {
  if (!isApiError(err)) {
    return {};
  }
  if (err.status === 409 && err.code === "name_taken") {
    return { "/name": { code: "name_taken" } };
  }
  const errors: DestinationErrors = {};
  for (const item of err.errors ?? []) {
    const problem: FieldProblem = { code: item.code };
    if (item.detail !== undefined) {
      problem.detail = item.detail;
    }
    if (item.line !== undefined) {
      problem.line = item.line;
    }
    if (item.column !== undefined) {
      problem.column = item.column;
    }
    if (item.pointer === "/limiter") {
      errors["/limiter/limit"] = problem;
      errors["/limiter/per_seconds"] = problem;
    } else {
      const earlier = errors[item.pointer];
      if (earlier === undefined) {
        errors[item.pointer] = problem;
      } else if (earlier.code === problem.code && earlier.detail && problem.detail) {
        // Several failing checks at one field show every message.
        errors[item.pointer] = { ...earlier, detail: `${earlier.detail} ${problem.detail}` };
      }
    }
  }
  return errors;
}

export function destinationErrorText(t: TFunction, pointer: string, problem: FieldProblem): string {
  if (problem.code === "destination_check_failed") {
    return problem.detail ?? t("destinations.errors.checkFailed");
  }
  if (problem.code === "name_taken") {
    return t("destinations.errors.nameTaken");
  }
  if (pointer.startsWith("/mentions/")) {
    return mentionErrorText(t, problem.code);
  }
  if (problem.code === "unknown_id" && pointer === "/connection_id") {
    return t("destinations.errors.unknownConnection");
  }
  return fieldErrorText(t, problem.code);
}

/** The kind of Loud event a Mention pointer names, such as /mentions/new_alerts/user_ids/0. */
function mentionKindOf(pointer: string): MentionKind | undefined {
  const name = pointer.split("/")[2];
  return MENTION_KINDS.find((k) => k === name);
}

function isOutdated(err: unknown): boolean {
  return isStale(err) || (isApiError(err) && err.status === 428);
}

function isBusy(err: unknown): boolean {
  return isApiError(err) && err.status === 503;
}

export interface DestinationFormProps<V> {
  kind: DestinationKind<V>;
  /** The stored Destination of an edit. */
  destination?: Destination;
  readOnly?: boolean;
  submitLabel: string;
  /**
   * Sends the request; the form shows a refusal. An edit resolves with the saved Destination, which the form then
   * shows without being mounted again, so that the focus stays on "Save".
   */
  save: (input: DestinationInput) => Promise<Destination | undefined>;
  /** A newer version of the Destination was read; a save would be refused. */
  stale?: boolean;
  onReload?: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  /** Shown beside the submit button once a save has gone through. */
  saved?: boolean;
  onCancel?: () => void;
}

export function DestinationForm<V>({
  kind,
  destination,
  readOnly = false,
  submitLabel,
  save,
  stale = false,
  onReload,
  onDirtyChange,
  saved = false,
  onCancel,
}: DestinationFormProps<V>) {
  const { t } = useTranslation();
  const [initial, setInitial] = useState(() => valuesOf(kind, destination));
  const [values, setValues] = useState(initial);
  // A new Destination takes the channel's name and the Connection's limiter until the user changes them.
  const touched = useRef({ name: destination !== undefined, limiter: destination !== undefined });
  const [errors, setErrors] = useState<DestinationErrors>({});
  const formElement = useRef<HTMLFormElement>(null);
  const [focusRequest, setFocusRequest] = useState(0);
  // The refusal of the last save was shown on the fields; it is not shown again as a whole once they are edited.
  const [fieldHandled, setFieldHandled] = useState(false);
  const dirty = JSON.stringify(values) !== JSON.stringify(initial);
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  // After a refused save, the focus goes to the first marked field.
  useEffect(() => {
    if (focusRequest > 0) {
      formElement.current
        ?.querySelector<HTMLElement>('[aria-invalid="true"], [data-focus-problem="true"]')
        ?.focus();
    }
  }, [focusRequest]);

  /** Changes a part of the values; the problems of that part go away. */
  const change = <K extends keyof Values<V>>(key: K, next: Values<V>[K], prefixes: string[]) => {
    setValues((current) => ({ ...current, [key]: next }));
    setErrors((current) => {
      const kept = Object.entries(current).filter(
        ([pointer]) => !prefixes.some((p) => pointer === p || pointer.startsWith(`${p}/`)),
      );
      return kept.length === Object.keys(current).length ? current : Object.fromEntries(kept);
    });
  };

  const showErrors = (found: DestinationErrors) => {
    setErrors(found);
    setFocusRequest((n) => n + 1);
  };

  const submit = useMutation({
    mutationFn: (input: DestinationInput) => save(input),
    onMutate: () => setFieldHandled(false),
    onSuccess: (updated) => {
      if (updated !== undefined) {
        const next = valuesOf(kind, updated);
        setInitial(next);
        setValues(next);
      }
    },
    onError: (err) => {
      const found = serverErrors(err);
      if (Object.keys(found).length > 0) {
        showErrors(found);
        setFieldHandled(true);
      }
    },
  });

  const text = (pointer: string) => {
    const problem = errors[pointer];
    return problem === undefined ? undefined : destinationErrorText(t, pointer, problem);
  };
  const typeErrors: Record<string, string> = {};
  const typeProblems: DestinationErrors = {};
  for (const [pointer, problem] of Object.entries(errors)) {
    if (isTypePointer(kind.pointers, pointer)) {
      typeErrors[pointer] = destinationErrorText(t, pointer, problem);
      typeProblems[pointer] = problem;
    }
  }
  const mentionErrors: Partial<Record<MentionKind, string>> = {};
  for (const [pointer, problem] of Object.entries(errors)) {
    const mentionKind = mentionKindOf(pointer);
    if (mentionKind !== undefined && mentionErrors[mentionKind] === undefined) {
      mentionErrors[mentionKind] = destinationErrorText(t, pointer, problem);
    }
  }
  const limiterFieldErrors: Partial<Record<keyof LimiterValues, string>> = {};
  for (const field of ["limit", "per_seconds"] as const) {
    const message = text(`/limiter/${field}`);
    if (message !== undefined) {
      limiterFieldErrors[field] = message;
    }
  }
  const shown = (pointer: string) =>
    pointer === "/name" ||
    isTypePointer(kind.pointers, pointer) ||
    mentionKindOf(pointer) !== undefined ||
    pointer === "/limiter/limit" ||
    pointer === "/limiter/per_seconds";
  const unshown = Object.entries(errors).filter(([pointer]) => !shown(pointer));
  const showStale = (stale && !submit.isPending) || isOutdated(submit.error);
  const otherError = submit.isError && !isOutdated(submit.error) && !fieldHandled;
  const nameError = text("/name");

  return (
    <form
      ref={formElement}
      noValidate
      className="flex min-w-0 flex-col gap-6"
      onSubmit={(e) => {
        e.preventDefault();
        const found = destinationErrors(kind, values);
        showErrors(found);
        if (Object.keys(found).length === 0) {
          submit.mutate(
            kind.input(
              {
                name: values.name.trim(),
                mentions: values.mentions,
                limiter: limiterInput(values.limiter),
              },
              values.fields,
            ),
          );
        }
      }}
    >
      {readOnly && (
        <p className="text-sm text-muted-foreground" data-testid="destination-read-only">
          {t("destinations.form.readOnly")}
        </p>
      )}
      <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-6">
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>{t("destinations.form.settings")}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <kind.Fields
              id={`${ID}-type`}
              value={values.fields}
              onChange={(next) => {
                const changed = kind.changed;
                const cleared =
                  changed === undefined
                    ? [...kind.pointers]
                    : Object.keys(errors).filter(
                        (pointer) =>
                          isTypePointer(kind.pointers, pointer) &&
                          changed(values.fields, next, pointer),
                      );
                change("fields", next, cleared);
              }}
              errors={typeErrors}
              problems={typeProblems}
              disabled={readOnly}
              destination={destination}
              suggest={({ name, limiter }) => {
                if (name !== undefined && !touched.current.name) {
                  change("name", name, ["/name"]);
                }
                if (limiter !== undefined && !touched.current.limiter) {
                  change("limiter", limiterValues(limiter), ["/limiter"]);
                }
              }}
            />
            <div className="flex max-w-md min-w-0 flex-col gap-2">
              <Label htmlFor={`${ID}-name`}>{t("destinations.fields.name")}</Label>
              <Input
                id={`${ID}-name`}
                autoComplete="off"
                spellCheck={false}
                maxLength={NAME_MAX}
                value={values.name}
                aria-invalid={nameError !== undefined}
                aria-describedby={nameError === undefined ? undefined : `${ID}-name-error`}
                onChange={(e) => {
                  touched.current.name = true;
                  change("name", e.target.value, ["/name"]);
                }}
              />
              {nameError !== undefined && (
                <p id={`${ID}-name-error`} className="text-sm text-destructive">
                  {nameError}
                </p>
              )}
            </div>
            <LimiterField
              id={`${ID}-limiter`}
              value={values.limiter}
              hint={kind.limiterHint(t)}
              errors={limiterFieldErrors}
              disabled={readOnly}
              onChange={(next) => {
                touched.current.limiter = true;
                change("limiter", next, ["/limiter"]);
              }}
            />
          </CardContent>
        </Card>
        <Card>
          <CardContent>
            <MentionSettingsField
              id={`${ID}-mentions`}
              value={values.mentions}
              everyone={kind.everyone}
              groups={kind.groups}
              everyoneHint={kind.mentionsHint?.(t)}
              groupPlaceholder={kind.groupPlaceholder?.(t)}
              errors={mentionErrors}
              disabled={readOnly}
              onChange={(next) => change("mentions", next, ["/mentions"])}
            />
          </CardContent>
        </Card>
      </fieldset>
      {!readOnly && (
        <div className="flex flex-col gap-3">
          {showStale && (
            <Alert variant="destructive" data-testid="destination-stale">
              <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
                <span>{t("destinations.errors.stale")}</span>
                {onReload !== undefined && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      submit.reset();
                      setErrors({});
                      onReload();
                    }}
                  >
                    {t("common.reload")}
                  </Button>
                )}
              </AlertDescription>
            </Alert>
          )}
          {otherError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current" data-testid="destination-error">
                {isBusy(submit.error)
                  ? checkErrorText(t, submit.error)
                  : problemText(t, submit.error)}
              </AlertDescription>
            </Alert>
          )}
          {Object.keys(errors).length > 0 && (
            <div className="text-sm text-destructive" role="alert">
              <p>{t("destinations.form.fixErrors")}</p>
              {unshown.length > 0 && (
                <ul className="mt-1 list-disc pl-5">
                  {unshown.map(([pointer, problem]) => (
                    <li key={pointer}>
                      <span className="font-mono">{pointer}</span>:{" "}
                      {destinationErrorText(t, pointer, problem)}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" disabled={submit.isPending}>
              {submit.isPending ? t("destinations.form.saving") : submitLabel}
            </Button>
            {onCancel !== undefined && (
              <Button type="button" variant="outline" onClick={onCancel}>
                {t("common.cancel")}
              </Button>
            )}
            <span
              role="status"
              className="text-sm text-muted-foreground"
              data-testid="destination-status"
            >
              {saved && !dirty ? t("common.saved") : ""}
            </span>
          </div>
        </div>
      )}
    </form>
  );
}
