// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The form of a Connection (C-13.FR-1, C-14.FR-1, C-22.FR-1, C-24.FR-1): its name, where it calls — the server URL of
// Mattermost, the Bot API base URL and the update mode of Telegram, or the base URL, the event delivery mode and, in
// push mode, the push secret and the callback address of a Bridge in their type slot; Slack's address is fixed, and its
// slot holds the event mode with the app-level token in Socket Mode or the Slack signing secret and the callback
// address in HTTP mode — the write-only bot token, the proxy form and the limiter, which a Bridge Connection may take
// from the rate its bridge declares (C-22.FR-18). The tokens and the secrets are never shown: a read gives only whether
// they are set and when they changed, and a save sends them only when they were replaced, a Slack Connection only the
// secret of its event mode. The stored token is sent only to the address it was entered for, so a new server URL or base
// URL needs the token again. The form reports its values as they are typed, which the check of a Connection sends as a
// draft before anything is saved (C-13.FR-14): the type's address, the proxy, the token only when one is typed — and for
// Slack the event mode and, in Socket Mode, the app-level token when one is typed —, and the saved Connection an edit
// holds. Without connections:write the form only shows the Connection. The delete dialog
// refuses a Connection that Destinations use (in_use). A Slack Connection shows the hint about its manifest above the
// token fields, and its bot token "Learn more".

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useRef, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";

import {
  deleteConnection,
  getGetConnectionQueryKey,
  getListConnectionsQueryKey,
} from "../api/gen/endpoints/connections/connections";
import type {
  BridgeConnectionInput,
  BridgeEventDelivery,
  BridgeInfo,
  Connection,
  ConnectionDraft,
  ConnectionInput,
  SlackConnectionInput,
  SlackEventMode,
  TelegramUpdateMode,
} from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import {
  BridgeConnectionFields,
  DEFAULT_BRIDGE_LIMITER,
  DEFAULT_EVENT_DELIVERY,
  LimiterProposal,
  isPushSecret,
} from "./bridge-connection-fields";
import { unreachable } from "./destination-test-panel";
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
  APP_TOKEN_PREFIX,
  BOT_TOKEN_PREFIX,
  DEFAULT_EVENT_MODE,
  SlackConnectionFields,
  SlackManifestHint,
  SlackTokenDocs,
} from "./slack-connection-fields";
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

/** Slack's Web API, the fixed address of every Slack Connection (C-24.FR-1). */
const SLACK_API_URL = "https://slack.com/api/";

/** The limiter of a new Slack Connection: connection.slack.limiter, 50 requests per minute. */
export const DEFAULT_SLACK_LIMITER = { limit: 50, per_seconds: 60 } as const;

export interface ConnectionValues {
  type: ConnectionType;
  name: string;
  /** Mattermost. */
  server_url: string;
  /** Telegram. */
  bot_api_base_url: string;
  /** Telegram. */
  update_mode: TelegramUpdateMode;
  /** Bridge. */
  base_url: string;
  /** Bridge. */
  event_delivery: BridgeEventDelivery;
  /** Bridge, in push mode. */
  push_secret: SecretChange;
  /** Slack. */
  event_mode: SlackEventMode;
  /** Slack, in Socket Mode. */
  app_token: SecretChange;
  /** Slack, in HTTP mode. */
  slack_signing_secret: SecretChange;
  bot_token: SecretChange;
  proxy: ProxyValues;
  limiter: LimiterValues;
}

/** The limiter of a new Connection of the type. */
function defaultLimiter(type: ConnectionType) {
  switch (type) {
    case "telegram":
      return DEFAULT_TELEGRAM_LIMITER;
    case "bridge":
      return DEFAULT_BRIDGE_LIMITER;
    case "slack":
      return DEFAULT_SLACK_LIMITER;
    default:
      return DEFAULT_MATTERMOST_LIMITER;
  }
}

/** The values of a stored Connection, or of a new one of the type. */
export function connectionValues(
  c: Connection | undefined,
  type: ConnectionType = c?.type ?? "mattermost",
): ConnectionValues {
  const telegram = c?.type === "telegram" ? c : undefined;
  const bridge = c?.type === "bridge" ? c : undefined;
  return {
    type,
    name: c?.name ?? "",
    server_url: c?.type === "mattermost" ? c.server_url : "",
    bot_api_base_url: telegram?.bot_api_base_url ?? DEFAULT_BOT_API_BASE_URL,
    update_mode: telegram?.update_mode ?? DEFAULT_UPDATE_MODE,
    base_url: bridge?.base_url ?? "",
    event_delivery: bridge?.event_delivery ?? DEFAULT_EVENT_DELIVERY,
    push_secret: KEEP_SECRET,
    event_mode: c?.type === "slack" ? c.event_mode : DEFAULT_EVENT_MODE,
    app_token: KEEP_SECRET,
    slack_signing_secret: KEEP_SECRET,
    bot_token: KEEP_SECRET,
    proxy: proxyValues(c?.proxy),
    limiter: limiterValues(c?.limiter ?? defaultLimiter(type)),
  };
}

/** The fields of a save that depend on the type. */
function typeInput(v: ConnectionValues): ConnectionInput {
  const common = {
    name: v.name.trim(),
    proxy: proxyInput(v.proxy),
    limiter: limiterInput(v.limiter),
  };
  if (v.type === "telegram") {
    return {
      type: "telegram",
      ...common,
      bot_api_base_url: v.bot_api_base_url.trim(),
      update_mode: v.update_mode,
    };
  }
  if (v.type === "bridge") {
    const bridge: BridgeConnectionInput = {
      type: "bridge",
      ...common,
      base_url: v.base_url.trim(),
      event_delivery: v.event_delivery,
    };
    // Only push takes a push secret; a save in poll clears the stored one.
    if (v.event_delivery === "push" && v.push_secret.mode === "replace") {
      bridge.push_secret = v.push_secret.value;
    }
    return bridge;
  }
  if (v.type === "slack") {
    const slack: SlackConnectionInput = { type: "slack", ...common, event_mode: v.event_mode };
    // Only the secret of the event mode goes; the server keeps the other one.
    if (v.event_mode === "socket" && v.app_token.mode === "replace") {
      slack.app_token = v.app_token.value;
    }
    if (v.event_mode === "http" && v.slack_signing_secret.mode === "replace") {
      slack.slack_signing_secret = v.slack_signing_secret.value;
    }
    return slack;
  }
  return { type: "mattermost", ...common, server_url: v.server_url.trim() };
}

/**
 * The request of a save: the bot token and the push secret only when they were replaced, so that an update keeps the
 * stored ones.
 */
export function connectionInput(v: ConnectionValues): ConnectionInput {
  const input = typeInput(v);
  if (v.bot_token.mode === "replace") {
    input.bot_token = v.bot_token.value;
  }
  return input;
}

/**
 * The draft that the check of the form's values sends (C-13.FR-14): the type's address as a save sends it, the proxy, the
 * bot token only when one is typed, and the saved Connection of an edit, whose stored token the server uses only for its
 * saved address and proxy. A Slack draft has no address: it sends its event mode and, in Socket Mode, the app-level
 * token only when one is typed (C-24.FR-2).
 */
export function connectionDraft(
  v: ConnectionValues,
  stored: Connection | undefined,
): ConnectionDraft {
  const common: { proxy: ConnectionDraft["proxy"]; bot_token?: string; connection_id?: string } = {
    proxy: proxyInput(v.proxy),
  };
  if (v.bot_token.mode === "replace" && v.bot_token.value !== "") {
    common.bot_token = v.bot_token.value;
  }
  if (stored !== undefined) {
    common.connection_id = stored.id;
  }
  switch (v.type) {
    case "telegram":
      return {
        type: "telegram",
        bot_api_base_url: v.bot_api_base_url.trim(),
        update_mode: v.update_mode,
        ...common,
      };
    case "bridge":
      return {
        type: "bridge",
        base_url: v.base_url.trim(),
        event_delivery: v.event_delivery,
        ...common,
      };
    case "slack": {
      const app = v.app_token;
      return {
        type: "slack",
        event_mode: v.event_mode,
        ...(v.event_mode === "socket" && app.mode === "replace" && app.value !== ""
          ? { app_token: app.value }
          : {}),
        ...common,
      };
    }
    default:
      return { type: "mattermost", server_url: v.server_url.trim(), ...common };
  }
}

/** The address a draft calls: the server URL, the Bot API base URL or the base URL of its type, Slack's for Slack. */
export function draftAddress(draft: ConnectionDraft): string {
  switch (draft.type) {
    case "telegram":
      return draft.bot_api_base_url;
    case "bridge":
      return draft.base_url;
    case "slack":
      return SLACK_API_URL;
    default:
      return draft.server_url;
  }
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
  switch (stored.type) {
    case "telegram":
      return normalizeBaseUrl(v.bot_api_base_url) !== stored.bot_api_base_url;
    case "bridge":
      return normalizeBaseUrl(v.base_url) !== stored.base_url;
    case "slack":
      // Slack's address is fixed, and a change of the proxy asks for no token again, as for the other types.
      return false;
    default:
      return v.server_url.trim() !== stored.server_url;
  }
}

/** Field errors by JSON pointer, such as "/server_url" or "/proxy/address", with their codes. */
export type ConnectionErrors = Record<string, string>;

/** The code of a bot token that a new server URL or base URL needs again. */
export const TOKEN_FOR_NEW_SERVER = "token_for_new_server";

/** The code of a Bot API base URL that breaks the rules of C-14.FR-10. */
export const BASE_URL_FORMAT = "base_url_format";

/** The code of a save that setWebhook or deleteWebhook refused; its detail is the step's message. */
export const WEBHOOK_CALL_FAILED = "webhook_call_failed";

/** The code of a Bridge base URL that breaks the rules of C-22.FR-1. */
export const BRIDGE_BASE_URL_FORMAT = "bridge_base_url_format";

/** The code of a push secret that is not whsec_ followed by base64. */
export const PUSH_SECRET_FORMAT = "push_secret_format";

/** The code of a Slack bot token that does not start with xoxb-. */
export const SLACK_BOT_TOKEN_FORMAT = "slack_bot_token_format";

/** The code of a Slack app-level token that does not start with xapp-. */
export const SLACK_APP_TOKEN_FORMAT = "slack_app_token_format";

/** A Secret that a mode needs: typed now and not empty, or stored and kept. */
function secretMissing(change: SecretChange, stored: boolean): string | undefined {
  if (change.mode === "replace") {
    return change.value === "" ? "required" : undefined;
  }
  return change.mode === "keep" && stored ? undefined : "required";
}

/**
 * The checks of a Slack Connection's tokens and secrets (C-24.FR-1): the bot token starts with xoxb-, the app-level
 * token with xapp-; Socket Mode needs an app-level token and HTTP mode the Slack signing secret, sent now or stored.
 */
function slackErrors(v: ConnectionValues, stored: Connection | undefined): ConnectionErrors {
  const errors: ConnectionErrors = {};
  const saved = stored?.type === "slack" ? stored : undefined;
  if (
    v.bot_token.mode === "replace" &&
    v.bot_token.value !== "" &&
    !v.bot_token.value.startsWith(BOT_TOKEN_PREFIX)
  ) {
    errors["/bot_token"] = SLACK_BOT_TOKEN_FORMAT;
  }
  if (v.event_mode === "socket") {
    const app = v.app_token;
    const missing = secretMissing(app, saved?.app_token_status.set === true);
    if (missing !== undefined) {
      errors["/app_token"] = missing;
    } else if (app.mode === "replace" && !app.value.startsWith(APP_TOKEN_PREFIX)) {
      errors["/app_token"] = SLACK_APP_TOKEN_FORMAT;
    }
  } else {
    const missing = secretMissing(
      v.slack_signing_secret,
      saved?.slack_signing_secret_status.set === true,
    );
    if (missing !== undefined) {
      errors["/slack_signing_secret"] = missing;
    }
  }
  return errors;
}

/** The checks of a Bridge's base URL and, in push mode, of its push secret: one sent now, or one stored. */
function bridgeErrors(v: ConnectionValues, stored: Connection | undefined): ConnectionErrors {
  const errors: ConnectionErrors = {};
  if (v.base_url.trim() === "") {
    errors["/base_url"] = "required";
  } else if (!isBaseUrl(v.base_url)) {
    errors["/base_url"] = BRIDGE_BASE_URL_FORMAT;
  }
  if (v.event_delivery === "push") {
    const secret = v.push_secret;
    if (secret.mode === "replace") {
      if (secret.value === "") {
        errors["/push_secret"] = "required";
      } else if (!isPushSecret(secret.value)) {
        errors["/push_secret"] = PUSH_SECRET_FORMAT;
      }
    } else if (!(stored?.type === "bridge" && stored.push_secret_status.set)) {
      errors["/push_secret"] = "required";
    }
  }
  return errors;
}

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
  } else if (v.type === "bridge") {
    Object.assign(errors, bridgeErrors(v, stored));
  } else if (v.type === "slack") {
    Object.assign(errors, slackErrors(v, stored));
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

/** The pointer of the address of a type; Slack's address is fixed, so a Slack Connection has none. */
function addressPointer(type: ConnectionType): string | undefined {
  switch (type) {
    case "telegram":
      return "/bot_api_base_url";
    case "bridge":
      return "/base_url";
    case "slack":
      return undefined;
    default:
      return "/server_url";
  }
}

/** The codes of a typed token that the check of a Slack draft refuses as a save does. */
const DRAFT_TOKEN_CODES: ReadonlySet<string> = new Set([
  SLACK_BOT_TOKEN_FORMAT,
  SLACK_APP_TOKEN_FORMAT,
]);

/**
 * The errors of connectionErrors that a check of the form's values would be refused for: the type's address, the proxy
 * and a Slack token typed in the wrong form. The name, the limiter, a missing secret and a token the new address needs
 * again are left to the save.
 */
export function draftErrors(v: ConnectionValues, stored: Connection | undefined): ConnectionErrors {
  const address = addressPointer(v.type);
  return Object.fromEntries(
    Object.entries(connectionErrors(v, stored)).filter(
      ([pointer, code]) =>
        pointer === address || pointer.startsWith("/proxy/") || DRAFT_TOKEN_CODES.has(code),
    ),
  );
}

/** The name of a field the check of the form's values names in a refusal; undefined for a field it does not send. */
export function draftFieldLabel(
  t: TFunction,
  pointer: string,
  kind: ConnectionType,
): string | undefined {
  if (pointer.startsWith("/proxy/")) {
    return t("proxy.title");
  }
  switch (pointer) {
    case "/server_url":
      return t("connections.fields.serverUrl");
    case "/bot_api_base_url":
      return t("connections.fields.botApiBaseUrl");
    case "/base_url":
      return kind === "bridge" ? t("connections.bridge.baseUrl") : undefined;
    case "/bot_token":
      return kind === "slack" ? t("connections.fields.botToken") : undefined;
    case "/app_token":
      return t("connections.slack.appToken");
    default:
      return undefined;
  }
}

/** The text of a field error of the check of the form's values, after the name of its field. */
export function draftFieldErrorText(
  t: TFunction,
  pointer: string,
  code: string,
  kind: ConnectionType,
): string {
  const error = connectionErrorText(t, code, undefined, kind);
  const field = draftFieldLabel(t, pointer, kind);
  return field === undefined ? error : t("connections.check.fieldError", { field, error });
}

/** The text of the first problem of the form's values that the check would be refused for; undefined for none. */
export function draftErrorText(
  t: TFunction,
  v: ConnectionValues,
  stored: Connection | undefined,
): string | undefined {
  const first = Object.entries(draftErrors(v, stored))[0];
  return first === undefined ? undefined : draftFieldErrorText(t, first[0], first[1], v.type);
}

/**
 * The field errors of a refused save by pointer. The server names the whole limiter as /limiter, a bot token that a
 * new address needs as /bot_token required, which the form explains, and a base URL or a push secret it refuses as
 * invalid_format.
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
    } else if (item.pointer === "/base_url" && item.code === "invalid_format") {
      errors["/base_url"] = BRIDGE_BASE_URL_FORMAT;
    } else if (item.pointer === "/push_secret" && item.code === "invalid_format") {
      errors["/push_secret"] = PUSH_SECRET_FORMAT;
    } else if (item.pointer === "/bot_token" && item.code === "invalid_format") {
      // Only a Slack bot token has a form the server checks.
      errors["/bot_token"] = SLACK_BOT_TOKEN_FORMAT;
    } else if (item.pointer === "/app_token" && item.code === "invalid_format") {
      errors["/app_token"] = SLACK_APP_TOKEN_FORMAT;
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

/** The text of an error code; kind names the token a new address needs again: a bot's, or a bridge's. */
export function connectionErrorText(
  t: TFunction,
  code: string,
  detail?: string,
  kind?: ConnectionType,
): string {
  switch (code) {
    case "name_taken":
      return t("connections.errors.nameTaken");
    case TOKEN_FOR_NEW_SERVER:
      return kind === "bridge"
        ? t("connections.bridge.tokenForNewBaseUrl")
        : t("connections.errors.tokenForNewServer");
    case BASE_URL_FORMAT:
      return t("connections.errors.baseUrlFormat");
    case BRIDGE_BASE_URL_FORMAT:
      return t("connections.bridge.baseUrlFormat");
    case PUSH_SECRET_FORMAT:
      return t("connections.bridge.pushSecretFormat");
    case SLACK_BOT_TOKEN_FORMAT:
      return t("connections.slack.botTokenFormat");
    case SLACK_APP_TOKEN_FORMAT:
      return t("connections.slack.appTokenFormat");
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
  /^\/(name|server_url|bot_api_base_url|update_mode|base_url|event_delivery|push_secret|event_mode|app_token|slack_signing_secret|bot_token|limiter\/(limit|per_seconds)|proxy\/(type|address|username|password))$/;

function isOutdated(err: unknown): boolean {
  return isStale(err) || (isApiError(err) && err.status === 428);
}

function formTitle(t: TFunction, kind: ConnectionType): string {
  switch (kind) {
    case "mattermost":
      return t("connections.form.mattermost");
    case "telegram":
      return t("connections.form.telegram");
    case "bridge":
      return t("connections.types.bridge");
    case "slack":
      return t("connections.types.slack");
    default:
      return unreachable(kind);
  }
}

function limiterHint(t: TFunction, kind: ConnectionType): string {
  switch (kind) {
    case "mattermost":
      return t("connections.form.limiterHint");
    case "telegram":
      return t("connections.telegram.limiterHint");
    case "bridge":
      return t("connections.bridge.limiterHint");
    case "slack":
      return t("connections.slack.limiterHint");
    default:
      return unreachable(kind);
  }
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
  /** The values in the form, as they are typed: the check of the Connection sends them as a draft. */
  onValuesChange?: (values: ConnectionValues) => void;
  /** The info the last check of a Bridge Connection read, whose rate the limiter proposal halves. */
  info?: BridgeInfo | null;
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
  onValuesChange,
  info,
}: ConnectionFormProps) {
  const { t } = useTranslation();
  const [defaultValues] = useState(() => connectionValues(connection, type ?? connection?.type));
  const form = useForm<ConnectionValues>({ defaultValues });
  const kind = defaultValues.type;
  const [errors, setErrors] = useState<ConnectionErrors>({});
  const [details, setDetails] = useState<Record<string, string>>({});
  // The values as they are typed, and after a reset: a save, or a newer version.
  useEffect(() => {
    if (onValuesChange === undefined) {
      return undefined;
    }
    onValuesChange(form.getValues());
    return form.subscribe({
      formState: { values: true },
      callback: () => onValuesChange(form.getValues()),
    });
  }, [form, onValuesChange]);
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
        const field = name.split(".")[0];
        // Another event delivery mode takes the push secret's problem with it: polling has no push secret; another
        // Slack event mode the problem of the other mode's secret.
        const prefixes =
          field === "event_delivery"
            ? ["/event_delivery", "/push_secret"]
            : field === "event_mode"
              ? ["/event_mode", "/app_token", "/slack_signing_secret"]
              : [`/${field}`];
        setErrors((current) => {
          const kept = Object.entries(current).filter(
            ([pointer]) => !prefixes.some((prefix) => pointer.startsWith(prefix)),
          );
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
      : connectionErrorText(t, errors[pointer], details[pointer], kind);
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
              <h2>{formTitle(t, kind)}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            {kind === "slack" && !readOnly && <SlackManifestHint />}
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
                    label={
                      kind === "bridge"
                        ? t("connections.bridge.token")
                        : t("connections.fields.botToken")
                    }
                    status={connection?.bot_token_status}
                    value={field.value}
                    disabled={readOnly}
                    error={err("/bot_token")}
                    docs={
                      kind === "slack" ? (
                        <SlackTokenDocs about={t("docs.fields.slackBotToken")} />
                      ) : undefined
                    }
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
            {kind === "slack" && (
              <Controller
                control={form.control}
                name="event_mode"
                render={({ field: mode }) => (
                  <Controller
                    control={form.control}
                    name="app_token"
                    render={({ field: app }) => (
                      <Controller
                        control={form.control}
                        name="slack_signing_secret"
                        render={({ field: signing }) => (
                          <SlackConnectionFields
                            id={ID}
                            eventMode={mode.value}
                            onEventModeChange={mode.onChange}
                            appToken={app.value}
                            onAppTokenChange={app.onChange}
                            signingSecret={signing.value}
                            onSigningSecretChange={signing.onChange}
                            appTokenStatus={
                              connection?.type === "slack" ? connection.app_token_status : undefined
                            }
                            signingSecretStatus={
                              connection?.type === "slack"
                                ? connection.slack_signing_secret_status
                                : undefined
                            }
                            callbackUrl={
                              connection?.type === "slack" ? connection.callback_url : undefined
                            }
                            errors={{
                              eventMode: err("/event_mode"),
                              appToken: err("/app_token"),
                              signingSecret: err("/slack_signing_secret"),
                            }}
                            disabled={readOnly}
                          />
                        )}
                      />
                    )}
                  />
                )}
              />
            )}
            {kind === "bridge" && (
              <Controller
                control={form.control}
                name="base_url"
                render={({ field: base }) => (
                  <Controller
                    control={form.control}
                    name="event_delivery"
                    render={({ field: mode }) => (
                      <Controller
                        control={form.control}
                        name="push_secret"
                        render={({ field: secret }) => (
                          <BridgeConnectionFields
                            id={ID}
                            baseUrl={base.value}
                            onBaseUrlChange={base.onChange}
                            eventDelivery={mode.value}
                            onEventDeliveryChange={mode.onChange}
                            pushSecret={secret.value}
                            onPushSecretChange={secret.onChange}
                            pushSecretStatus={
                              connection?.type === "bridge"
                                ? connection.push_secret_status
                                : undefined
                            }
                            callbackUrl={
                              connection?.type === "bridge" ? connection.callback_url : undefined
                            }
                            errors={{
                              baseUrl: err("/base_url"),
                              eventDelivery: err("/event_delivery"),
                              pushSecret: err("/push_secret"),
                            }}
                            disabled={readOnly}
                          />
                        )}
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
                <div className="flex min-w-0 flex-col gap-2">
                  <LimiterField
                    id={`${ID}-limiter`}
                    value={field.value}
                    hint={limiterHint(t, kind)}
                    errors={limiterFieldErrors}
                    disabled={readOnly}
                    onChange={field.onChange}
                  />
                  {kind === "bridge" && (
                    <LimiterProposal
                      info={info}
                      value={field.value}
                      disabled={readOnly}
                      onUse={field.onChange}
                    />
                  )}
                </div>
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
                      {connectionErrorText(t, code, details[pointer], kind)}
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

/**
 * "Delete" with its dialog; a Connection that Destinations use is refused and the dialog says by how many. The page
 * stops reading the Connection through onDeletingChange from the moment the deletion starts, as a read once it is
 * deleted, such as the one the live hint of the deletion starts, would answer 404; a refused deletion lets it read
 * again.
 */
export function ConnectionDeleteDialog({
  connection,
  onDeletingChange,
}: {
  connection: Connection;
  onDeletingChange?: (deleting: boolean) => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteConnection(connection.id, { headers: { "If-Match": connection.etag } }),
    onMutate: async () => {
      onDeletingChange?.(true);
      await queryClient.cancelQueries({ queryKey: getGetConnectionQueryKey(connection.id) });
    },
    onSuccess: async () => {
      await navigate({ to: "/connections" });
      queryClient.removeQueries({ queryKey: getGetConnectionQueryKey(connection.id) });
      void queryClient.invalidateQueries({ queryKey: getListConnectionsQueryKey() });
    },
    onError: (err) => {
      onDeletingChange?.(false);
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
