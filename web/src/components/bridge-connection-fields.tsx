// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The fields of a Bridge Connection in the type slot of the Connection form (C-22.FR-1, FR-2, FR-18): the base URL,
// the event delivery mode as two radio buttons with what each needs, and in push mode the write-only push secret and
// the callback address with its hint, which a new Connection gets once it is saved. The limiter proposal offers half of
// the rate the bridge declares, never by itself; the panel of what the bridge offers shows the info the last check read.
// The token, the proxy and the limiter are the form's own. What the bridge sends is untrusted text, shown as text only.

import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import type {
  BridgeCapabilities,
  BridgeEventDelivery,
  BridgeInfo,
  BridgeInfoUserLookup,
  Limiter,
  SecretStatus,
} from "../api/gen/model";
import { formatDuration, useTimeFormat } from "../lib/time";
import { unreachable } from "./destination-test-panel";
import { CopyBlock } from "./integration-token-dialog";
import { type LimiterValues, limiterValues } from "./limiter-field";
import { type SecretChange, SecretField } from "./secret-field";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** The limiter of a new Bridge Connection: connection.bridge.limiter, 10 requests per second. */
export const DEFAULT_BRIDGE_LIMITER = { limit: 10, per_seconds: 1 } as const;

/** connection.bridge.event_delivery: the Leader polls the bridge. */
export const DEFAULT_EVENT_DELIVERY: BridgeEventDelivery = "poll";

const EVENT_DELIVERIES: readonly BridgeEventDelivery[] = ["poll", "push"];

/** The largest number of the limiter's fields, which take up to 9 digits. */
const LIMITER_MAX = 999_999_999;

const PUSH_SECRET_PREFIX = "whsec_";

/** A push secret in the Standard Webhooks form, as the server checks it: whsec_ followed by base64 of 1 byte or more. */
export function isPushSecret(text: string): boolean {
  if (!text.startsWith(PUSH_SECRET_PREFIX)) {
    return false;
  }
  const key = text.slice(PUSH_SECRET_PREFIX.length);
  if (key.length % 4 !== 0 || !/^[A-Za-z0-9+/]+={0,2}$/.test(key)) {
    return false;
  }
  try {
    return atob(key).length > 0;
  } catch {
    return false;
  }
}

/**
 * Half of a rate per second as a limiter in whole numbers over unitSeconds: as many requests per unit as half the rate
 * allows, or one request per as many seconds as it takes when that is less than one, never above half the rate.
 */
export function halfRate(rate: number, unitSeconds: number): Limiter {
  const half = rate / 2;
  const perUnit = half * unitSeconds;
  if (perUnit >= 1) {
    return { limit: Math.min(Math.floor(perUnit), LIMITER_MAX), per_seconds: unitSeconds };
  }
  // The small epsilon keeps a rate such as 0.1, which is not exact in binary, at one request per 20 s.
  return { limit: 1, per_seconds: Math.min(Math.ceil(1 / half - 1e-9), LIMITER_MAX) };
}

/** connection.bridge.limiter: half of the bridge's limits.send_per_second when its info has it, else the default. */
export function connectionLimiterProposal(info: BridgeInfo | null | undefined): Limiter {
  const rate = info?.limits.send_per_second;
  return rate !== undefined && rate > 0 ? halfRate(rate, 1) : DEFAULT_BRIDGE_LIMITER;
}

export function eventDeliveryName(t: TFunction, mode: BridgeEventDelivery): string {
  switch (mode) {
    case "poll":
      return t("connections.bridge.modes.poll");
    case "push":
      return t("connections.bridge.modes.push");
    default:
      return unreachable(mode);
  }
}

/** A rate the bridge declares, in the reader's number format. */
function rateText(language: string, rate: number): string {
  return new Intl.NumberFormat(language, { maximumFractionDigits: 3 }).format(rate);
}

export interface BridgeConnectionFieldsProps {
  /** The base of the ids of the inputs, unique on the page. */
  id: string;
  baseUrl: string;
  onBaseUrlChange: (value: string) => void;
  eventDelivery: BridgeEventDelivery;
  onEventDeliveryChange: (value: BridgeEventDelivery) => void;
  pushSecret: SecretChange;
  onPushSecretChange: (value: SecretChange) => void;
  /** Whether a push secret is stored, of an edit. */
  pushSecretStatus?: SecretStatus;
  /** The callback address of a saved Connection; a new one has none yet. */
  callbackUrl?: string;
  /** The texts of the problems of the base URL, the event delivery mode and the push secret. */
  errors: { baseUrl?: string; eventDelivery?: string; pushSecret?: string };
  disabled: boolean;
}

export function BridgeConnectionFields({
  id,
  baseUrl,
  onBaseUrlChange,
  eventDelivery,
  onEventDeliveryChange,
  pushSecret,
  onPushSecretChange,
  pushSecretStatus,
  callbackUrl,
  errors,
  disabled,
}: BridgeConnectionFieldsProps) {
  const { t } = useTranslation();
  const baseId = `${id}-base-url`;
  const modeId = `${id}-event-delivery`;
  const secretId = `${id}-push-secret`;
  const baseDescribedBy = [
    `${baseId}-hint`,
    errors.baseUrl === undefined ? null : `${baseId}-error`,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="bridge-connection-fields">
      <div className="flex min-w-0 flex-col gap-2">
        <Label htmlFor={baseId}>{t("connections.bridge.baseUrl")}</Label>
        <Input
          id={baseId}
          type="url"
          autoComplete="off"
          spellCheck={false}
          placeholder="https://bridge.example.org"
          value={baseUrl}
          disabled={disabled}
          aria-invalid={errors.baseUrl !== undefined}
          aria-describedby={baseDescribedBy}
          onChange={(e) => onBaseUrlChange(e.target.value)}
        />
        <p id={`${baseId}-hint`} className="text-sm wrap-anywhere text-muted-foreground">
          {t("connections.bridge.baseUrlHint")}
        </p>
        {errors.baseUrl !== undefined && (
          <p id={`${baseId}-error`} className="text-sm wrap-anywhere text-destructive">
            {errors.baseUrl}
          </p>
        )}
      </div>
      <fieldset
        className="flex min-w-0 flex-col gap-2"
        disabled={disabled}
        aria-describedby={errors.eventDelivery === undefined ? undefined : `${modeId}-error`}
      >
        <legend className="mb-2 text-sm leading-none font-medium">
          {t("connections.bridge.eventDelivery")}
        </legend>
        {EVENT_DELIVERIES.map((mode) => (
          <div key={mode} className="flex min-w-0 items-start gap-2">
            <input
              type="radio"
              id={`${modeId}-${mode}`}
              name={modeId}
              value={mode}
              className="mt-0.5 size-4 shrink-0 accent-primary"
              checked={eventDelivery === mode}
              aria-describedby={`${modeId}-${mode}-hint`}
              onChange={() => onEventDeliveryChange(mode)}
            />
            <div className="flex min-w-0 flex-col gap-0.5">
              <label htmlFor={`${modeId}-${mode}`} className="text-sm font-medium">
                {eventDeliveryName(t, mode)}
              </label>
              <p
                id={`${modeId}-${mode}-hint`}
                className="text-sm wrap-anywhere text-muted-foreground"
                data-testid={`bridge-mode-hint-${mode}`}
              >
                {mode === "push"
                  ? t("connections.bridge.pushHint")
                  : t("connections.bridge.pollHint")}
              </p>
            </div>
          </div>
        ))}
        {errors.eventDelivery !== undefined && (
          <p id={`${modeId}-error`} className="text-sm wrap-anywhere text-destructive">
            {errors.eventDelivery}
          </p>
        )}
      </fieldset>
      {eventDelivery === "push" && (
        <div className="flex min-w-0 flex-col gap-3" data-testid="bridge-push">
          <SecretField
            id={secretId}
            label={t("connections.bridge.pushSecret")}
            status={pushSecretStatus}
            value={pushSecret}
            disabled={disabled}
            error={errors.pushSecret}
            describedBy={`${secretId}-hint`}
            onChange={onPushSecretChange}
          />
          <p id={`${secretId}-hint`} className="text-sm wrap-anywhere text-muted-foreground">
            {t("connections.bridge.pushSecretHint")}
          </p>
          {callbackUrl === undefined ? (
            <p
              className="text-sm wrap-anywhere text-muted-foreground"
              data-testid="bridge-callback-after-save"
            >
              {t("connections.bridge.callbackAfterSave")}
            </p>
          ) : (
            <>
              <CopyBlock
                id="connection-callback-url"
                label={t("connections.callback.label")}
                text={callbackUrl}
                testId="bridge-callback-url"
              />
              <p
                className="text-sm wrap-anywhere text-muted-foreground"
                data-testid="bridge-callback-hint"
              >
                {t("connections.bridge.callbackHint", { address: callbackUrl })}
              </p>
            </>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * The limiter half of the rate the bridge declares in limits.send_per_second, offered while the limiter differs from
 * it: "Use the proposal" sets the field, and the Admin saves. Nothing without the rate.
 */
export function LimiterProposal({
  info,
  value,
  onUse,
  disabled = false,
}: {
  info: BridgeInfo | null | undefined;
  value: LimiterValues;
  onUse: (next: LimiterValues) => void;
  disabled?: boolean;
}) {
  const { t, i18n } = useTranslation();
  const rate = info?.limits.send_per_second;
  if (rate === undefined || rate <= 0) {
    return null;
  }
  const proposal = halfRate(rate, 1);
  const proposed = limiterValues(proposal);
  if (value.limit.trim() === proposed.limit && value.per_seconds.trim() === proposed.per_seconds) {
    return null;
  }
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2">
      <p
        className="min-w-0 text-sm wrap-anywhere text-muted-foreground"
        data-testid="limiter-proposal"
      >
        {t("connections.bridge.limiterProposal", {
          rate: rateText(i18n.resolvedLanguage ?? "en", rate),
          limit: proposal.limit,
          seconds: proposal.per_seconds,
        })}
      </p>
      {!disabled && (
        <Button type="button" variant="outline" size="sm" onClick={() => onUse(proposed)}>
          {t("connections.bridge.useProposal")}
        </Button>
      )}
    </div>
  );
}

function lookupText(t: TFunction, lookup: BridgeInfoUserLookup): string {
  switch (lookup) {
    case "email":
      return t("connections.bridge.info.lookup.email");
    case "login":
      return t("connections.bridge.info.lookup.login");
    case "username":
      return t("connections.bridge.info.lookup.username");
    case "user_id":
      return t("connections.bridge.info.lookup.userId");
    case "none":
      return t("connections.bridge.info.lookup.none");
    default:
      return unreachable(lookup);
  }
}

/** What the bridge offers besides posting and editing, as the reader names it. */
export function bridgeFeatures(t: TFunction, c: BridgeCapabilities): string[] {
  const features: string[] = [];
  if (c.buttons !== undefined) {
    features.push(t("connections.bridge.features.buttons"));
  }
  if (c.thread_messages === "all") {
    features.push(t("connections.bridge.features.threadMessages"));
  } else if (c.thread_messages === "mentions_only") {
    features.push(t("connections.bridge.features.threadMessagesMentions"));
  }
  if (c.direct_messages !== undefined) {
    features.push(t("connections.bridge.features.directMessages"));
  }
  if (c.private_answers !== undefined && c.private_answers.length > 0) {
    features.push(t("connections.bridge.features.privateAnswers"));
  }
  if (c.native_mentions === true) {
    features.push(t("connections.bridge.features.nativeMentions"));
  }
  if (c.quiet_messages === true) {
    features.push(t("connections.bridge.features.quietMessages"));
  }
  if (c.status_color === true) {
    features.push(t("connections.bridge.features.statusColor"));
  }
  if (c.delete !== undefined) {
    features.push(t("connections.bridge.features.delete"));
  }
  if (c.deletion_events === true) {
    features.push(t("connections.bridge.features.deletionEvents"));
  }
  return features;
}

/** The lines of a bridge's limits. */
function limitLines(t: TFunction, language: string, info: BridgeInfo): string[] {
  const limits = info.limits;
  const lines = [
    t("connections.bridge.limits.maxBody", { count: limits.max_body_length }),
    limits.edit_max_age_seconds === null
      ? t("connections.bridge.limits.editForever")
      : t("connections.bridge.limits.editWindow", {
          duration: formatDuration(t, limits.edit_max_age_seconds),
        }),
  ];
  if (limits.send_per_second !== undefined) {
    lines.push(
      t("connections.bridge.limits.sendPerSecond", {
        rate: rateText(language, limits.send_per_second),
      }),
    );
  }
  if (limits.send_per_second_per_channel !== undefined) {
    lines.push(
      t("connections.bridge.limits.sendPerSecondPerChannel", {
        rate: rateText(language, limits.send_per_second_per_channel),
      }),
    );
  }
  return lines;
}

/**
 * What the bridge offers, from the info the last Connection check read (C-22.FR-2): the bridge's name and version, the
 * messenger, the protocol version, its features, its limits, whom it finds for an Account link by code and its event
 * delivery modes, and when Muster read it.
 */
export function BridgeInfoPanel({
  info,
  readAt,
}: {
  info: BridgeInfo | null;
  readAt?: string | null;
}) {
  const { t, i18n } = useTranslation();
  const { dateTime } = useTimeFormat();
  const language = i18n.resolvedLanguage ?? "en";
  const features = info === null ? [] : bridgeFeatures(t, info.capabilities);
  return (
    <Card data-testid="bridge-info">
      <CardHeader>
        <CardTitle>
          <h2>{t("connections.bridge.info.title")}</h2>
        </CardTitle>
        {info !== null && readAt !== undefined && readAt !== null && (
          <CardDescription data-testid="bridge-info-read-at">
            {t("connections.bridge.info.readAt", { time: dateTime(readAt) })}
          </CardDescription>
        )}
      </CardHeader>
      <CardContent className="flex min-w-0 flex-col gap-3 text-sm">
        {info === null ? (
          <p className="text-muted-foreground" data-testid="bridge-info-none">
            {t("connections.bridge.info.none")}
          </p>
        ) : (
          <>
            <ul
              className="flex min-w-0 flex-col gap-1 wrap-anywhere"
              data-testid="bridge-info-lines"
            >
              <li data-testid="bridge-info-bridge">
                {t("connections.bridge.info.bridge", {
                  name: info.bridge.name,
                  version: info.bridge.version,
                })}
              </li>
              <li data-testid="bridge-info-messenger">
                {t("connections.bridge.info.messenger", { name: info.messenger.name })}
              </li>
              <li data-testid="bridge-info-protocol">
                {t("connections.bridge.info.protocolVersion", { version: info.protocol_version })}
              </li>
              {limitLines(t, language, info).map((line) => (
                <li key={line} data-testid="bridge-info-limit">
                  {line}
                </li>
              ))}
              <li data-testid="bridge-info-lookup">{lookupText(t, info.user_lookup)}</li>
              <li data-testid="bridge-info-event-delivery">
                {t("connections.bridge.info.eventDelivery", {
                  modes: info.event_delivery.map((m) => eventDeliveryName(t, m)).join(", "),
                })}
              </li>
            </ul>
            <div className="flex min-w-0 flex-col gap-1">
              <h3 id="bridge-info-features" className="font-medium">
                {t("connections.bridge.info.features")}
              </h3>
              {features.length === 0 ? (
                <p className="text-muted-foreground" data-testid="bridge-info-no-features">
                  {t("connections.bridge.info.noFeatures")}
                </p>
              ) : (
                <ul
                  className="flex min-w-0 list-disc flex-col gap-0.5 pl-5 wrap-anywhere"
                  aria-labelledby="bridge-info-features"
                  data-testid="bridge-info-features"
                >
                  {features.map((feature) => (
                    <li key={feature}>{feature}</li>
                  ))}
                </ul>
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
