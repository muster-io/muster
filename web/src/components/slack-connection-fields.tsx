// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The fields of a Slack Connection in the type slot of the Connection form (C-24.FR-1, FR-9, AC-18): the event mode as
// two radio buttons, each with what it needs, and only the secret of the chosen mode — the write-only app-level token
// in Socket Mode, the write-only Slack signing secret in HTTP mode, with the callback address and its hint, which a new
// Connection gets once it is saved. Slack's address is fixed, so the form has no address field; the bot token, the
// proxy and the limiter are the form's own. The page of a saved Connection shows what its last check learned — the
// bot, the workspace and the app — and, while the Leader's last Socket Mode hello counted another connection of the
// app, the warning that another program shares its presses (C-24.FR-2, FR-9). What Slack sends is untrusted text,
// shown as text only.

import type { TFunction } from "i18next";
import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { SecretStatus, SlackConnection, SlackEventMode } from "../api/gen/model";
import { CallbackAddress } from "./callback-hint";
import { unreachable } from "./destination-test-panel";
import { type SecretChange, SecretField } from "./secret-field";

/** connection.slack.event_mode: the Leader holds a Socket Mode connection. */
export const DEFAULT_EVENT_MODE: SlackEventMode = "socket";

const EVENT_MODES: readonly SlackEventMode[] = ["socket", "http"];

/** The prefix of a Slack bot token. */
export const BOT_TOKEN_PREFIX = "xoxb-";

/** The prefix of a Slack app-level token. */
export const APP_TOKEN_PREFIX = "xapp-";

export function eventModeName(t: TFunction, mode: SlackEventMode): string {
  switch (mode) {
    case "socket":
      return t("connections.slack.modes.socket");
    case "http":
      return t("connections.slack.modes.http");
    default:
      return unreachable(mode);
  }
}

function eventModeHint(t: TFunction, mode: SlackEventMode): string {
  switch (mode) {
    case "socket":
      return t("connections.slack.socketHint");
    case "http":
      return t("connections.slack.httpModeHint");
    default:
      return unreachable(mode);
  }
}

export interface SlackConnectionFieldsProps {
  /** The base of the ids of the inputs, unique on the page. */
  id: string;
  eventMode: SlackEventMode;
  onEventModeChange: (value: SlackEventMode) => void;
  appToken: SecretChange;
  onAppTokenChange: (value: SecretChange) => void;
  signingSecret: SecretChange;
  onSigningSecretChange: (value: SecretChange) => void;
  /** Whether the secrets are stored, of an edit. */
  appTokenStatus?: SecretStatus;
  signingSecretStatus?: SecretStatus;
  /** The callback address of a saved Connection; a new one has none yet. */
  callbackUrl?: string;
  /** The texts of the problems of the event mode and the two secrets. */
  errors: { eventMode?: string; appToken?: string; signingSecret?: string };
  disabled: boolean;
}

export function SlackConnectionFields({
  id,
  eventMode,
  onEventModeChange,
  appToken,
  onAppTokenChange,
  signingSecret,
  onSigningSecretChange,
  appTokenStatus,
  signingSecretStatus,
  callbackUrl,
  errors,
  disabled,
}: SlackConnectionFieldsProps) {
  const { t } = useTranslation();
  const modeId = `${id}-event-mode`;
  const appId = `${id}-app-token`;
  const signingId = `${id}-slack-signing-secret`;
  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="slack-connection-fields">
      <fieldset
        className="flex min-w-0 flex-col gap-2"
        disabled={disabled}
        aria-describedby={errors.eventMode === undefined ? undefined : `${modeId}-error`}
      >
        <legend className="mb-2 text-sm leading-none font-medium">
          {t("connections.slack.eventMode")}
        </legend>
        {EVENT_MODES.map((mode) => (
          <div key={mode} className="flex min-w-0 items-start gap-2">
            <input
              type="radio"
              id={`${modeId}-${mode}`}
              name={modeId}
              value={mode}
              className="mt-0.5 size-4 shrink-0 accent-primary"
              checked={eventMode === mode}
              aria-describedby={`${modeId}-${mode}-hint`}
              onChange={() => onEventModeChange(mode)}
            />
            <div className="flex min-w-0 flex-col gap-0.5">
              <label htmlFor={`${modeId}-${mode}`} className="text-sm font-medium">
                {eventModeName(t, mode)}
              </label>
              <p
                id={`${modeId}-${mode}-hint`}
                className="text-sm wrap-anywhere text-muted-foreground"
                data-testid={`slack-mode-hint-${mode}`}
              >
                {eventModeHint(t, mode)}
              </p>
            </div>
          </div>
        ))}
        {errors.eventMode !== undefined && (
          <p id={`${modeId}-error`} className="text-sm wrap-anywhere text-destructive">
            {errors.eventMode}
          </p>
        )}
      </fieldset>
      {eventMode === "socket" ? (
        <div className="flex min-w-0 flex-col gap-2" data-testid="slack-socket">
          <SecretField
            id={appId}
            label={t("connections.slack.appToken")}
            status={appTokenStatus}
            value={appToken}
            disabled={disabled}
            error={errors.appToken}
            describedBy={`${appId}-hint`}
            onChange={onAppTokenChange}
          />
          <p id={`${appId}-hint`} className="text-sm wrap-anywhere text-muted-foreground">
            {t("connections.slack.appTokenHint")}
          </p>
        </div>
      ) : (
        <div className="flex min-w-0 flex-col gap-3" data-testid="slack-http">
          <SecretField
            id={signingId}
            label={t("connections.slack.signingSecret")}
            status={signingSecretStatus}
            value={signingSecret}
            disabled={disabled}
            error={errors.signingSecret}
            describedBy={`${signingId}-hint`}
            onChange={onSigningSecretChange}
          />
          <p id={`${signingId}-hint`} className="text-sm wrap-anywhere text-muted-foreground">
            {t("connections.slack.signingSecretHint")}
          </p>
          {callbackUrl === undefined ? (
            <p
              className="text-sm wrap-anywhere text-muted-foreground"
              data-testid="slack-callback-after-save"
            >
              {t("connections.bridge.callbackAfterSave")}
            </p>
          ) : (
            <CallbackAddress
              callbackUrl={callbackUrl}
              hint={t("connections.slack.httpHint", { address: callbackUrl })}
              testId="slack-callback"
            />
          )}
        </div>
      )}
    </div>
  );
}

/**
 * What the last check of a saved Slack Connection learned: the bot, the workspace and, in Socket Mode, the app; one
 * line until a check found them.
 */
export function SlackConnectionInfo({ connection }: { connection: SlackConnection }) {
  const { t } = useTranslation();
  const parts = [
    connection.bot_name
      ? t("connections.edit.bot", { bot: connection.bot_name })
      : t("connections.edit.botUnknown"),
    connection.team_name ? t("connections.slack.workspace", { name: connection.team_name }) : "",
    connection.app_id ? t("connections.slack.app", { id: connection.app_id }) : "",
  ].filter((part) => part !== "");
  return (
    <p className="text-sm wrap-anywhere text-muted-foreground" data-testid="connection-bot">
      {parts.join(" · ")}
    </p>
  );
}

/**
 * Whether the Leader's last Socket Mode hello counted another connection of the app: a second program, usually a
 * second installation, takes some of the presses (C-24.FR-9).
 */
export function sharesApp(connection: SlackConnection): boolean {
  return (
    connection.event_mode === "socket" &&
    connection.socket !== undefined &&
    connection.socket !== null &&
    connection.socket.other_connections > 0
  );
}

/** The warning of a Slack app that another program shares through Socket Mode, on the Connection's page. */
export function SlackSharedAppWarning({ connection }: { connection: SlackConnection }) {
  const { t } = useTranslation();
  if (!sharesApp(connection)) {
    return null;
  }
  return (
    <div
      className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm"
      role="alert"
      data-testid="slack-shared-app"
    >
      <TriangleAlertIcon
        aria-hidden="true"
        className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
      />
      <p className="min-w-0 wrap-anywhere">
        {t("connections.check.warnings.anotherClientConnected")}
      </p>
    </div>
  );
}
