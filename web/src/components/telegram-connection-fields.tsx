// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The fields of a Telegram Connection in the type slot of the Connection form (C-14.FR-1, FR-10): the Bot API base URL
// with the hint about self-hosted Bot API servers, the warning of an http address, and the update mode with what each
// mode needs. The bot token, the proxy and the limiter are the form's own.

import type { TFunction } from "i18next";
import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { TelegramConnectionWarningsItem, TelegramUpdateMode } from "../api/gen/model";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** connection.telegram.bot_api_base_url: Telegram's cloud Bot API. */
export const DEFAULT_BOT_API_BASE_URL = "https://api.telegram.org";

/** connection.telegram.update_mode. */
export const DEFAULT_UPDATE_MODE: TelegramUpdateMode = "long_polling";

const UPDATE_MODES: readonly TelegramUpdateMode[] = ["long_polling", "webhook"];

/** A base URL as the server stores it: trimmed, its scheme in lower case, without the trailing slashes of its path. */
export function normalizeBaseUrl(text: string): string {
  return text
    .trim()
    .replace(/^[a-z][a-z0-9+.-]*:/i, (scheme) => scheme.toLowerCase())
    .replace(/\/+$/, "");
}

/**
 * An absolute http or https URL with a host and without user information, query or fragment, as the server checks a
 * Bot API base URL; a path prefix is allowed.
 */
export function isBaseUrl(text: string): boolean {
  const value = text.trim();
  if (value.includes("#") || value.includes("?") || !/^https?:\/\//i.test(value)) {
    return false;
  }
  try {
    const url = new URL(value);
    return (
      (url.protocol === "http:" || url.protocol === "https:") &&
      url.hostname !== "" &&
      url.username === "" &&
      url.password === ""
    );
  } catch {
    return false;
  }
}

/** Whether the bot token would travel in clear text to this address. */
export function usesHttp(text: string): boolean {
  return text.trim().toLowerCase().startsWith("http://");
}

export function updateModeName(t: TFunction, mode: TelegramUpdateMode): string {
  return mode === "webhook"
    ? t("connections.telegram.modes.webhook")
    : t("connections.telegram.modes.longPolling");
}

export interface TelegramConnectionFieldsProps {
  /** The base of the ids of the inputs, unique on the page. */
  id: string;
  baseUrl: string;
  onBaseUrlChange: (value: string) => void;
  updateMode: TelegramUpdateMode;
  onUpdateModeChange: (value: TelegramUpdateMode) => void;
  /** The texts of the problems of the base URL and of the update mode. */
  errors: { baseUrl?: string; updateMode?: string };
  disabled: boolean;
  /** The stored base URL and its warnings, of an edit. */
  saved?: { baseUrl: string; warnings: readonly TelegramConnectionWarningsItem[] };
}

export function TelegramConnectionFields({
  id,
  baseUrl,
  onBaseUrlChange,
  updateMode,
  onUpdateModeChange,
  errors,
  disabled,
  saved,
}: TelegramConnectionFieldsProps) {
  const { t } = useTranslation();
  const baseId = `${id}-bot-api-base-url`;
  const modeId = `${id}-update-mode`;
  // The stored address shows the server's warnings; an address being typed shows the same warning at once.
  const httpWarning =
    saved !== undefined && normalizeBaseUrl(baseUrl) === saved.baseUrl
      ? saved.warnings.includes("base_url_uses_http")
      : usesHttp(baseUrl);
  const baseDescribedBy = [
    `${baseId}-hint`,
    httpWarning ? `${baseId}-warning` : null,
    errors.baseUrl === undefined ? null : `${baseId}-error`,
  ]
    .filter(Boolean)
    .join(" ");
  const modeDescribedBy = [
    `${modeId}-hint`,
    errors.updateMode === undefined ? null : `${modeId}-error`,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="telegram-connection-fields">
      <div className="flex min-w-0 flex-col gap-2">
        <Label htmlFor={baseId}>{t("connections.fields.botApiBaseUrl")}</Label>
        <Input
          id={baseId}
          type="url"
          autoComplete="off"
          spellCheck={false}
          placeholder={DEFAULT_BOT_API_BASE_URL}
          value={baseUrl}
          disabled={disabled}
          aria-invalid={errors.baseUrl !== undefined}
          aria-describedby={baseDescribedBy}
          onChange={(e) => onBaseUrlChange(e.target.value)}
        />
        <p
          id={`${baseId}-hint`}
          className="text-sm text-muted-foreground"
          data-testid="telegram-base-url-hint"
        >
          {t("connections.telegram.baseUrlHint")}
        </p>
        {httpWarning && (
          <div
            id={`${baseId}-warning`}
            className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm"
            data-testid="telegram-http-warning"
          >
            <TriangleAlertIcon
              aria-hidden="true"
              className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
            />
            <p className="min-w-0 wrap-anywhere">{t("connections.telegram.httpWarning")}</p>
          </div>
        )}
        {errors.baseUrl !== undefined && (
          <p id={`${baseId}-error`} className="text-sm wrap-anywhere text-destructive">
            {errors.baseUrl}
          </p>
        )}
      </div>
      <div className="flex max-w-md min-w-0 flex-col gap-2">
        <Label htmlFor={modeId}>{t("connections.fields.updateMode")}</Label>
        <NativeSelect
          id={modeId}
          className="w-full"
          value={updateMode}
          disabled={disabled}
          aria-invalid={errors.updateMode !== undefined}
          aria-describedby={modeDescribedBy}
          onChange={(e) => {
            const next = UPDATE_MODES.find((m) => m === e.target.value);
            if (next !== undefined) {
              onUpdateModeChange(next);
            }
          }}
        >
          {UPDATE_MODES.map((mode) => (
            <NativeSelectOption key={mode} value={mode}>
              {updateModeName(t, mode)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <p
          id={`${modeId}-hint`}
          className="text-sm text-muted-foreground"
          data-testid="telegram-update-mode-hint"
        >
          {updateMode === "webhook"
            ? t("connections.telegram.webhookHint")
            : t("connections.telegram.longPollingHint")}
        </p>
        {errors.updateMode !== undefined && (
          <p id={`${modeId}-error`} className="text-sm wrap-anywhere text-destructive">
            {errors.updateMode}
          </p>
        )}
      </div>
    </div>
  );
}
