// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The form of a Connection (C-13.FR-1, C-14.FR-1): its name, where it calls — the server URL of Mattermost, or the
// Bot API base URL and the update mode of Telegram in their type slot — the write-only bot token, the proxy form and
// the limiter. The bot token is never shown: a read gives only whether it is set and when it changed, and a save sends it
// only when it was replaced. The stored token is sent only to the address it was entered for, so a new server URL or
// base URL needs the token again. Without connections:write the form only shows the Connection. The delete dialog
// refuses a Connection that Destinations use (in_use).

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useRef, useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";

import {
  deleteConnection,
  getGetConnectionQueryKey,
  getListConnectionsQueryKey,
} from "../api/gen/endpoints/connections/connections";
import type { Connection, ConnectionInput, TelegramUpdateMode } from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import {
  LimiterField,
  type LimiterValues,
  limiterErrors,
  limiterInput,
  limiterValues,
} from "./limiter-field";
import {
  type ProxyField,
  ProxyForm,
  type ProxyValues,
  proxyErrors,
  proxyInput,
  proxyValues,
} from "./proxy-form";
import { KEEP_SECRET, type SecretChange, SecretField } from "./secret-field";
import {
  DEFAULT_BOT_API_BASE_URL,
  DEFAULT_UPDATE_MODE,
  TelegramConnectionFields,
  isBaseUrl,
  normalizeBaseUrl,
} from "./telegram-connection-fields";
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

const ID = "connection";
const NAME_MAX = 200;

export type ConnectionType = Connection["type"];

/** The limiter of a new Mattermost Connection: connection.mattermost.limiter, 5 requests per second. */
export const DEFAULT_MATTERMOST_LIMITER = { limit: 5, per_seconds: 1 } as const;

/** The limiter of a new Telegram Connection: connection.telegram.limiter, 15 messages per second. */
export const DEFAULT_TELEGRAM_LIMITER = { limit: 15, per_seconds: 1 } as const;

export interface ConnectionValues {
  type: ConnectionType;
  name: string;
  /** Mattermost. */
  server_url: string;
  /** Telegram. */
  bot_api_base_url: string;
  /** Telegram. */
  update_mode: TelegramUpdateMode;
  bot_token: SecretChange;
  proxy: ProxyValues;
  limiter: LimiterValues;
}

/** The values of a stored Connection, or of a new one of the type. */
export function connectionValues(
  c: Connection | undefined,
  type: ConnectionType = c?.type ?? "mattermost",
): ConnectionValues {
  const telegram = c?.type === "telegram" ? c : undefined;
  return {
    type,
    name: c?.name ?? "",
    server_url: c?.type === "mattermost" ? c.server_url : "",
    bot_api_base_url: telegram?.bot_api_base_url ?? DEFAULT_BOT_API_BASE_URL,
    update_mode: telegram?.update_mode ?? DEFAULT_UPDATE_MODE,
    bot_token: KEEP_SECRET,
    proxy: proxyValues(c?.proxy),
    limiter: limiterValues(
      c?.limiter ?? (type === "telegram" ? DEFAULT_TELEGRAM_LIMITER : DEFAULT_MATTERMOST_LIMITER),
    ),
  };
}

/** The request of a save: the bot token only when it was replaced, so that an update keeps the stored one. */
export function connectionInput(v: ConnectionValues): ConnectionInput {
  const input: ConnectionInput =
    v.type === "telegram"
      ? {
          type: "telegram",
          name: v.name.trim(),
          bot_api_base_url: v.bot_api_base_url.trim(),
          update_mode: v.update_mode,
          proxy: proxyInput(v.proxy),
          limiter: limiterInput(v.limiter),
        }
      : {
          type: "mattermost",
          name: v.name.trim(),
          server_url: v.server_url.trim(),
          proxy: proxyInput(v.proxy),
          limiter: limiterInput(v.limiter),
        };
  if (v.bot_token.mode === "replace") {
    input.bot_token = v.bot_token.value;
  }
  return input;
}

/** An absolute http or https URL without user information, query or fragment, as the server checks it. */
export function isServerUrl(text: string): boolean {
  const value = text.trim();
  if (value.includes("#") || value.includes("?")) {
    return false;
  }
  try {
    const url = new URL(value);
    return (
      (url.protocol === "http:" || url.protocol === "https:") &&
      url.host !== "" &&
      url.username === "" &&
      url.password === ""
    );
  } catch {
    return false;
  }
}

/** Whether the address in the form differs from the stored one, so that the stored token is not sent there. */
export function addressChanged(v: ConnectionValues, stored: Connection | undefined): boolean {
  if (stored === undefined) {
    return false;
  }
  return stored.type === "telegram"
    ? normalizeBaseUrl(v.bot_api_base_url) !== stored.bot_api_base_url
    : v.server_url.trim() !== stored.server_url;
}

/** Field errors by JSON pointer, such as "/server_url" or "/proxy/address", with their codes. */
export type ConnectionErrors = Record<string, string>;

/** The code of a bot token that a new server URL or base URL needs again. */
export const TOKEN_FOR_NEW_SERVER = "token_for_new_server";

/** The code of a Bot API base URL that breaks the rules of C-14.FR-10. */
export const BASE_URL_FORMAT = "base_url_format";

/** The code of a save that setWebhook or deleteWebhook refused; its detail is the step's message. */
export const WEBHOOK_CALL_FAILED = "webhook_call_failed";

/** The checks the server repeats, before the form is sent; stored is the Connection of an edit. */
export function connectionErrors(
  v: ConnectionValues,
  stored: Connection | undefined,
): ConnectionErrors {
  const errors: ConnectionErrors = {};
  const name = v.name.trim();
  if (name === "") {
    errors["/name"] = "required";
  } else if (Array.from(name).length > NAME_MAX) {
    errors["/name"] = "too_long";
  }
  if (v.type === "telegram") {
    if (v.bot_api_base_url.trim() === "") {
      errors["/bot_api_base_url"] = "required";
    } else if (!isBaseUrl(v.bot_api_base_url)) {
      errors["/bot_api_base_url"] = BASE_URL_FORMAT;
    }
  } else if (v.server_url.trim() === "") {
    errors["/server_url"] = "required";
  } else if (!isServerUrl(v.server_url)) {
    errors["/server_url"] = "invalid_format";
  }
  if (v.bot_token.mode === "replace") {
    if (v.bot_token.value === "") {
      errors["/bot_token"] = "required";
    }
  } else if (stored === undefined) {
    errors["/bot_token"] = "required";
  } else if (addressChanged(v, stored)) {
    errors["/bot_token"] = TOKEN_FOR_NEW_SERVER;
  }
  for (const [field, code] of Object.entries(proxyErrors(v.proxy))) {
    errors[`/proxy/${field}`] = code;
  }
  for (const [field, code] of Object.entries(limiterErrors(v.limiter))) {
    errors[`/limiter/${field}`] = code;
  }
  return errors;
}

/**
 * The field errors of a refused save by pointer. The server names the whole limiter as /limiter, a bot token that a
 * new address needs as /bot_token required, which the form explains, and a base URL it refuses as invalid_format.
 */
export function serverErrors(err: unknown, serverUrlChanged: boolean): ConnectionErrors {
  if (!isApiError(err)) {
    return {};
  }
  if (err.status === 409 && err.code === "name_taken") {
    return { "/name": "name_taken" };
  }
  const errors: ConnectionErrors = {};
  for (const item of err.errors ?? []) {
    if (item.pointer === "/limiter") {
      errors["/limiter/limit"] = item.code;
      errors["/limiter/per_seconds"] = item.code;
    } else if (item.pointer === "/bot_token" && item.code === "required" && serverUrlChanged) {
      errors["/bot_token"] = TOKEN_FOR_NEW_SERVER;
    } else if (item.pointer === "/bot_api_base_url" && item.code === "invalid_format") {
      errors["/bot_api_base_url"] = BASE_URL_FORMAT;
    } else {
      errors[item.pointer] = item.code;
    }
  }
  return errors;
}

/** The messages of a refused save that the form shows with their codes: a failed webhook call, by pointer. */
export function serverDetails(err: unknown): Record<string, string> {
  const details: Record<string, string> = {};
  if (isApiError(err)) {
    for (const item of err.errors ?? []) {
      if (item.code === WEBHOOK_CALL_FAILED && item.detail !== undefined && item.detail !== "") {
        details[item.pointer] = item.detail;
      }
    }
  }
  return details;
}

export function connectionErrorText(t: TFunction, code: string, detail?: string): string {
  switch (code) {
    case "name_taken":
      return t("connections.errors.nameTaken");
    case TOKEN_FOR_NEW_SERVER:
      return t("connections.errors.tokenForNewServer");
    case BASE_URL_FORMAT:
      return t("connections.errors.baseUrlFormat");
    case WEBHOOK_CALL_FAILED:
      return detail === undefined
        ? t("connections.errors.webhookCallFailedNoDetail")
        : t("connections.errors.webhookCallFailed", { detail });
    default:
      return fieldErrorText(t, code);
  }
}

/** The pointers of the fields that show their errors themselves. */
const SHOWN =
  /^\/(name|server_url|bot_api_base_url|update_mode|bot_token|limiter\/(limit|per_seconds)|proxy\/(type|address|username|password))$/;

function isOutdated(err: unknown): boolean {
  return isStale(err) || (isApiError(err) && err.status === 428);
}

export interface ConnectionFormProps {
  /** The type of a new Connection; an edit takes the type of the stored one. */
  type?: ConnectionType;
  /** The stored Connection of an edit. */
  connection?: Connection;
  readOnly?: boolean;
  submitLabel: string;
  /**
   * Sends the request; the form shows a refusal. An edit resolves with the saved Connection, which the form then shows
   * without being mounted again, so that the focus stays on "Save".
   */
  save: (input: ConnectionInput) => Promise<Connection | undefined>;
  /** The Bot API base URL in the form of a Telegram Connection, as it is typed: the check explains an unsaved one. */
  onBaseUrlChange?: (value: string) => void;
  /** A newer version of the Connection was read; a save would be refused. */
  stale?: boolean;
  onReload?: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  /** Shown beside the submit button once a save has gone through. */
  saved?: boolean;
  onCancel?: () => void;
}

export function ConnectionForm({
  type,
  connection,
  readOnly = false,
  submitLabel,
  save,
  stale = false,
  onReload,
  onDirtyChange,
  saved = false,
  onCancel,
  onBaseUrlChange,
}: ConnectionFormProps) {
  const { t } = useTranslation();
  const [defaultValues] = useState(() => connectionValues(connection, type ?? connection?.type));
  const form = useForm<ConnectionValues>({ defaultValues });
  const kind = defaultValues.type;
  const [errors, setErrors] = useState<ConnectionErrors>({});
  const [details, setDetails] = useState<Record<string, string>>({});
  const baseUrl = useWatch({ control: form.control, name: "bot_api_base_url" });
  useEffect(() => {
    if (kind === "telegram") {
      onBaseUrlChange?.(baseUrl);
    }
  }, [kind, baseUrl, onBaseUrlChange]);
  const formElement = useRef<HTMLFormElement>(null);
  const [focusRequest, setFocusRequest] = useState(0);
  // The refusal of the last save was shown on the fields; it is not shown again as a whole once they are edited.
  const [fieldHandled, setFieldHandled] = useState(false);
  const dirty = form.formState.isDirty;
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  // A field's errors go away once it is edited.
  useEffect(() => {
    return form.subscribe({
      formState: { values: true },
      callback: ({ name }) => {
        if (name === undefined) {
          return;
        }
        const prefix = `/${name.split(".")[0]}`;
        setErrors((current) => {
          const kept = Object.entries(current).filter(([pointer]) => !pointer.startsWith(prefix));
          return kept.length === Object.keys(current).length ? current : Object.fromEntries(kept);
        });
      },
    });
  }, [form]);
  // After a refused save, the focus goes to the first marked field.
  useEffect(() => {
    if (focusRequest > 0) {
      formElement.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
    }
  }, [focusRequest]);

  /**
   * Marks the fields of errors and moves the focus to the first. A bot token that a new server URL needs opens its
   * input, which takes the focus, so that the refusal is found by keyboard and screen reader.
   */
  const showErrors = (found: ConnectionErrors) => {
    if (
      found["/bot_token"] === TOKEN_FOR_NEW_SERVER &&
      form.getValues("bot_token").mode !== "replace"
    ) {
      form.setValue("bot_token", { mode: "replace", value: "" }, { shouldDirty: true });
    }
    setErrors(found);
    setFocusRequest((n) => n + 1);
  };

  const submit = useMutation({
    mutationFn: ({ input }: { input: ConnectionInput; serverUrlChanged: boolean }) => save(input),
    onMutate: () => setFieldHandled(false),
    onSuccess: (updated) => {
      if (updated !== undefined) {
        form.reset(connectionValues(updated));
      }
    },
    onError: (err, { serverUrlChanged }) => {
      const found = serverErrors(err, serverUrlChanged);
      if (Object.keys(found).length > 0) {
        setDetails(serverDetails(err));
        showErrors(found);
        setFieldHandled(true);
      }
    },
  });

  const err = (pointer: string) =>
    errors[pointer] === undefined
      ? undefined
      : connectionErrorText(t, errors[pointer], details[pointer]);
  const proxyFieldErrors: Partial<Record<ProxyField, string>> = {};
  for (const field of ["type", "address", "username", "password"] as const) {
    const text = err(`/proxy/${field}`);
    if (text !== undefined) {
      proxyFieldErrors[field] = text;
    }
  }
  const limiterFieldErrors: Partial<Record<keyof LimiterValues, string>> = {};
  for (const field of ["limit", "per_seconds"] as const) {
    const text = err(`/limiter/${field}`);
    if (text !== undefined) {
      limiterFieldErrors[field] = text;
    }
  }
  const unshown = Object.entries(errors).filter(([pointer]) => !SHOWN.test(pointer));
  const showStale = (stale && !submit.isPending) || isOutdated(submit.error);
  const otherError = submit.isError && !isOutdated(submit.error) && !fieldHandled;

  const text = (name: "name" | "server_url", label: string, hint?: string) => {
    const id = `${ID}-${name.replace("_", "-")}`;
    const error = err(`/${name}`);
    const describedBy =
      [hint === undefined ? null : `${id}-hint`, error === undefined ? null : `${id}-error`]
        .filter(Boolean)
        .join(" ") || undefined;
    return (
      <div className="flex min-w-0 flex-col gap-2">
        <Label htmlFor={id}>{label}</Label>
        <Input
          id={id}
          type={name === "server_url" ? "url" : "text"}
          autoComplete="off"
          spellCheck={false}
          placeholder={name === "server_url" ? "https://mattermost.example.org" : undefined}
          aria-invalid={error !== undefined}
          aria-describedby={describedBy}
          {...form.register(name)}
        />
        {hint !== undefined && (
          <p id={`${id}-hint`} className="text-sm text-muted-foreground">
            {hint}
          </p>
        )}
        {error !== undefined && (
          <p id={`${id}-error`} className="text-sm text-destructive">
            {error}
          </p>
        )}
      </div>
    );
  };

  return (
    <form
      ref={formElement}
      noValidate
      className="flex min-w-0 flex-col gap-6"
      onSubmit={form.handleSubmit((values) => {
        const found = connectionErrors(values, connection);
        showErrors(found);
        if (Object.keys(found).length === 0) {
          submit.mutate({
            input: connectionInput(values),
            serverUrlChanged: addressChanged(values, connection),
          });
        }
      })}
    >
      {readOnly && (
        <p className="text-sm text-muted-foreground" data-testid="connection-read-only">
          {t("connections.form.readOnly")}
        </p>
      )}
      <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-6">
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>
                {kind === "telegram"
                  ? t("connections.form.telegram")
                  : t("connections.form.mattermost")}
              </h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="grid gap-4 md:grid-cols-2">
              {text("name", t("connections.fields.name"))}
              {kind === "mattermost" &&
                text(
                  "server_url",
                  t("connections.fields.serverUrl"),
                  t("connections.form.serverUrlHint"),
                )}
              <Controller
                control={form.control}
                name="bot_token"
                render={({ field }) => (
                  <SecretField
                    id={`${ID}-bot-token`}
                    label={t("connections.fields.botToken")}
                    status={connection?.bot_token_status}
                    value={field.value}
                    disabled={readOnly}
                    error={err("/bot_token")}
                    onChange={field.onChange}
                  />
                )}
              />
            </div>
            {kind === "telegram" && (
              <Controller
                control={form.control}
                name="bot_api_base_url"
                render={({ field: base }) => (
                  <Controller
                    control={form.control}
                    name="update_mode"
                    render={({ field: mode }) => (
                      <TelegramConnectionFields
                        id={ID}
                        baseUrl={base.value}
                        onBaseUrlChange={base.onChange}
                        updateMode={mode.value}
                        onUpdateModeChange={mode.onChange}
                        errors={{
                          baseUrl: err("/bot_api_base_url"),
                          updateMode: err("/update_mode"),
                        }}
                        disabled={readOnly}
                        saved={
                          connection?.type === "telegram"
                            ? {
                                baseUrl: connection.bot_api_base_url,
                                warnings: connection.warnings,
                              }
                            : undefined
                        }
                      />
                    )}
                  />
                )}
              />
            )}
            <Controller
              control={form.control}
              name="limiter"
              render={({ field }) => (
                <LimiterField
                  id={`${ID}-limiter`}
                  value={field.value}
                  hint={
                    kind === "telegram"
                      ? t("connections.telegram.limiterHint")
                      : t("connections.form.limiterHint")
                  }
                  errors={limiterFieldErrors}
                  disabled={readOnly}
                  onChange={field.onChange}
                />
              )}
            />
          </CardContent>
        </Card>
        <Card>
          <CardContent>
            <Controller
              control={form.control}
              name="proxy"
              render={({ field }) => (
                <ProxyForm
                  idPrefix={`${ID}-proxy`}
                  value={field.value}
                  passwordStatus={connection?.proxy.password_status}
                  errors={proxyFieldErrors}
                  disabled={readOnly}
                  onChange={field.onChange}
                />
              )}
            />
          </CardContent>
        </Card>
      </fieldset>
      {!readOnly && (
        <div className="flex flex-col gap-3">
          {showStale && (
            <Alert variant="destructive" data-testid="connection-stale">
              <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
                <span>{t("connections.errors.stale")}</span>
                {onReload !== undefined && (
                  <Button
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
              <AlertDescription className="text-current" data-testid="connection-error">
                {problemText(t, submit.error)}
              </AlertDescription>
            </Alert>
          )}
          {Object.keys(errors).length > 0 && (
            <div className="text-sm text-destructive" role="alert">
              <p>{t("connections.form.fixErrors")}</p>
              {unshown.length > 0 && (
                <ul className="mt-1 list-disc pl-5">
                  {unshown.map(([pointer, code]) => (
                    <li key={pointer}>
                      <span className="font-mono">{pointer}</span>:{" "}
                      {connectionErrorText(t, code, details[pointer])}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" disabled={submit.isPending}>
              {submitLabel}
            </Button>
            {onCancel !== undefined && (
              <Button variant="outline" onClick={onCancel}>
                {t("common.cancel")}
              </Button>
            )}
            <span
              role="status"
              className="text-sm text-muted-foreground"
              data-testid="connection-status"
            >
              {saved && !dirty ? t("common.saved") : ""}
            </span>
          </div>
        </div>
      )}
    </form>
  );
}

/**
 * The text of the in_use refusal, in its plural forms. One Destination has a sentence of its own, since the plural form
 * "one" of Russian also covers 21, 31, … Destinations, which are deleted as "them".
 */
export function inUseText(t: TFunction, count: number): string {
  return count <= 1
    ? t("connections.delete.inUseSingle")
    : t("connections.delete.inUse", { count });
}

const CANCEL_ID = "connection-delete-cancel";

function isInUse(err: unknown): boolean {
  return isApiError(err) && err.status === 409 && err.code === "in_use";
}

/** "Delete" with its dialog; a Connection that Destinations use is refused and the dialog says by how many. */
export function ConnectionDeleteDialog({ connection }: { connection: Connection }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteConnection(connection.id, { headers: { "If-Match": connection.etag } }),
    onSuccess: async () => {
      await navigate({ to: "/connections" });
      queryClient.removeQueries({ queryKey: getGetConnectionQueryKey(connection.id) });
      void queryClient.invalidateQueries({ queryKey: getListConnectionsQueryKey() });
    },
    onError: (err) => {
      // The number of Destinations or the version may have changed: read the Connection again.
      if (isInUse(err) || isStale(err)) {
        void queryClient.invalidateQueries({ queryKey: getGetConnectionQueryKey(connection.id) });
      }
      // "Delete" is disabled after in_use; the focus moves to "Cancel" instead of being lost.
      if (isInUse(err)) {
        requestAnimationFrame(() => document.getElementById(CANCEL_ID)?.focus());
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
        {t("connections.delete.action")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("connections.delete.title", { name: connection.name })}
            </DialogTitle>
            <DialogDescription>{t("connections.delete.description")}</DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription
                className="flex flex-wrap items-center gap-3 text-current"
                data-testid="connection-delete-error"
              >
                {isInUse(remove.error) ? (
                  inUseText(t, connection.destination_count)
                ) : isStale(remove.error) ? (
                  <>
                    <span>{t("connections.errors.stale")}</span>
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
              disabled={remove.isPending || isInUse(remove.error)}
              onClick={() => remove.mutate()}
            >
              {t("connections.delete.action")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
