// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The form of a Destination (C-13.FR-9, FR-2; C-12.FR-8), shared by every type: the name, the type's fields in a slot
// (Mattermost here; Telegram and the outgoing webhook add theirs), the Mention section and the limiter. Saving a
// Mattermost or Telegram Destination runs its Destination check, and a failing check is refused with
// destination_check_failed at the field it concerns and the check's message, which the form shows there. Without
// destinations:write the form only shows the Destination. The delete dialog says what deleting does: the Destination
// leaves its Routes and its open Root messages get one final edit.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { type ComponentType, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteDestination,
  getGetDestinationQueryKey,
  getListDestinationsQueryKey,
} from "../api/gen/endpoints/destinations/destinations";
import { getListConnectionsQueryKey } from "../api/gen/endpoints/connections/connections";
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
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

const ID = "destination";
const NAME_MAX = 200;

/** A refusal of a field: its code and, for a failing Destination check, the check's message. */
export interface FieldProblem {
  code: string;
  detail?: string;
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
  values: (d: Destination | undefined) => V;
  /** The checks the server repeats on the type's fields, as codes by pointer. */
  check: (v: V) => Record<string, string>;
  /** The request of a save. */
  input: (
    common: { name: string; mentions: MentionSettings; limiter: Limiter },
    v: V,
  ) => DestinationInput;
  /** The pointers of the type's fields, which show their problems themselves. */
  pointers: readonly string[];
  Fields: ComponentType<TypeFieldsProps<V>>;
}

interface Values<V> {
  name: string;
  mentions: MentionSettings;
  limiter: LimiterValues;
  fields: V;
}

function valuesOf<V>(kind: DestinationKind<V>, d: Destination | undefined): Values<V> {
  return {
    name: d?.name ?? "",
    mentions: d?.mentions ?? defaultMentions(),
    limiter: limiterValues(d?.limiter ?? kind.defaultLimiter),
    fields: kind.values(d),
  };
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
    const problem: FieldProblem =
      item.detail === undefined ? { code: item.code } : { code: item.code, detail: item.detail };
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
      formElement.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
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
  for (const pointer of kind.pointers) {
    const message = text(pointer);
    if (message !== undefined) {
      typeErrors[pointer] = message;
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
    kind.pointers.includes(pointer) ||
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
              onChange={(next) => change("fields", next, [...kind.pointers])}
              errors={typeErrors}
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

const CANCEL_ID = "destination-delete-cancel";

/**
 * "Delete" with its dialog. Deleting is always allowed: the Destination leaves every Route and its open Root messages
 * get one final edit; a Destination changed since it was read is refused (412) and read again.
 */
export function DestinationDeleteDialog({ destination }: { destination: Destination }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () =>
      deleteDestination(destination.id, { headers: { "If-Match": destination.etag } }),
    onSuccess: async () => {
      await navigate({ to: "/destinations" });
      queryClient.removeQueries({ queryKey: getGetDestinationQueryKey(destination.id) });
      void queryClient.invalidateQueries({ queryKey: getListDestinationsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getListConnectionsQueryKey() });
    },
    onError: (err) => {
      if (isStale(err)) {
        void queryClient.invalidateQueries({ queryKey: getGetDestinationQueryKey(destination.id) });
      }
    },
  });
  return (
    <>
      <Button
        variant="destructive"
        onClick={() => {
          remove.reset();
          setOpen(true);
        }}
      >
        {t("destinations.delete.action")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("destinations.delete.title", { name: destination.name })}
            </DialogTitle>
            <DialogDescription>{t("destinations.delete.description")}</DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription
                className="flex flex-wrap items-center gap-3 text-current"
                data-testid="destination-delete-error"
              >
                {isStale(remove.error) ? (
                  <>
                    <span>{t("destinations.errors.stale")}</span>
                    {/* The page has read the newer version; the dialog closes to show it. */}
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => setOpen(false)}
                    >
                      {t("common.reload")}
                    </Button>
                  </>
                ) : (
                  problemText(t, remove.error)
                )}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" id={CANCEL_ID} />}>
              {t("common.cancel")}
            </DialogClose>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("destinations.delete.action")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
