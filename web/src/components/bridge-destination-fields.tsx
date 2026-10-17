// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The fields of a Bridge Destination (C-22.FR-3, FR-18): a Bridge Connection and the channel id, as the messenger names
// it. Saving runs the Destination check, checkChannel, which refuses a channel the bridge forbids with the bridge's
// text next to the channel, and the saved Destination shows the channel's name the check found, read-only and as text
// only. Choosing a Connection proposes half of the rate per channel its bridge declares as the limiter, or the default,
// while the user has not changed the limiter. A Bridge has no chat-wide mention and no groups.

import { useTranslation } from "react-i18next";

import { useListConnections } from "../api/gen/endpoints/connections/connections";
import type { BridgeConnection, BridgeInfo, Destination, Limiter } from "../api/gen/model";
import { useCan } from "./app-shell";
import { halfRate } from "./bridge-connection-fields";
import type { DestinationKind, TypeFieldsProps } from "./destination-form";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

export interface BridgeFieldValues {
  connection_id: string;
  channel_id: string;
}

/** destination.bridge.limiter: 30 requests per minute, sends and edits together. */
export const DEFAULT_BRIDGE_DESTINATION_LIMITER = { limit: 30, per_seconds: 60 } as const;

/** The longest channel id, in characters, that the server takes. */
export const CHANNEL_ID_MAX = 512;

function isBridgeConnection(c: { type: string }): c is BridgeConnection {
  return c.type === "bridge";
}

export function bridgeValues(d: Destination | undefined): BridgeFieldValues {
  return d?.type === "bridge"
    ? { connection_id: d.connection_id, channel_id: d.channel_id }
    : { connection_id: "", channel_id: "" };
}

/**
 * destination.bridge.limiter: half of the bridge's limits.send_per_second_per_channel per minute when the info of its
 * Connection has it, else the default.
 */
export function destinationLimiterProposal(info: BridgeInfo | null | undefined): Limiter {
  const rate = info?.limits.send_per_second_per_channel;
  return rate !== undefined && rate > 0 ? halfRate(rate, 60) : DEFAULT_BRIDGE_DESTINATION_LIMITER;
}

export function BridgeDestinationFields({
  id,
  value,
  onChange,
  errors,
  disabled,
  destination,
  suggest,
}: TypeFieldsProps<BridgeFieldValues>) {
  const { t } = useTranslation();
  const canConnections = useCan("connections:read");
  const connections = useListConnections({ limit: 500 }, { query: { enabled: canConnections } });
  const items = (connections.data?.items ?? []).filter(isBridgeConnection);
  const stored = destination?.type === "bridge" ? destination : undefined;
  const connectionError = errors["/connection_id"];
  const channelError = errors["/channel_id"];
  const options = items.map((c) => ({ id: c.id, name: c.name }));
  if (value.connection_id !== "" && !options.some((o) => o.id === value.connection_id)) {
    options.unshift({ id: value.connection_id, name: value.connection_id });
  }
  const channelName = stored?.channel_name ?? "";
  const showFound =
    stored !== undefined &&
    channelName !== "" &&
    stored.connection_id === value.connection_id &&
    stored.channel_id === value.channel_id.trim();
  const channelId = `${id}-channel`;
  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="bridge-fields">
      <div className="flex max-w-md min-w-0 flex-col gap-2">
        <Label htmlFor={`${id}-connection`}>{t("destinations.bridge.connection")}</Label>
        <NativeSelect
          id={`${id}-connection`}
          className="w-full"
          value={value.connection_id}
          aria-invalid={connectionError !== undefined}
          aria-describedby={connectionError === undefined ? undefined : `${id}-connection-error`}
          onChange={(e) => {
            const next = e.target.value;
            onChange({ ...value, connection_id: next });
            // Another Connection's proposal never stays: one without rates proposes the default.
            const chosen = items.find((c) => c.id === next);
            suggest({ limiter: destinationLimiterProposal(chosen?.info) });
          }}
        >
          <NativeSelectOption value="">
            {connections.isPending && canConnections
              ? t("common.loading")
              : t("destinations.bridge.chooseConnection")}
          </NativeSelectOption>
          {options.map((c) => (
            <NativeSelectOption key={c.id} value={c.id}>
              {c.name}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        {connectionError !== undefined && (
          <p
            id={`${id}-connection-error`}
            className="text-sm wrap-anywhere text-destructive"
            data-testid={`${id}-connection-error-text`}
          >
            {connectionError}
          </p>
        )}
        {!disabled && connections.isSuccess && items.length === 0 && (
          <p className="text-sm text-muted-foreground">{t("destinations.bridge.noConnections")}</p>
        )}
      </div>
      <div className="flex max-w-md min-w-0 flex-col gap-2">
        <Label htmlFor={channelId}>{t("destinations.bridge.channel")}</Label>
        <Input
          id={channelId}
          autoComplete="off"
          spellCheck={false}
          value={value.channel_id}
          aria-invalid={channelError !== undefined}
          aria-describedby={
            channelError === undefined
              ? `${channelId}-hint`
              : `${channelId}-hint ${channelId}-error`
          }
          onChange={(e) => onChange({ ...value, channel_id: e.target.value })}
        />
        <p id={`${channelId}-hint`} className="text-sm text-muted-foreground">
          {t("destinations.bridge.channelHint")}
        </p>
        {channelError !== undefined && (
          <p
            id={`${channelId}-error`}
            className="text-sm wrap-anywhere text-destructive"
            data-testid={`${channelId}-error-text`}
          >
            {channelError}
          </p>
        )}
      </div>
      {showFound && (
        <dl className="flex min-w-0 flex-col gap-1 text-sm" data-testid="bridge-found">
          <div className="min-w-0 wrap-anywhere">
            <dt className="inline font-medium">{t("destinations.bridge.channelFound")}</dt>{" "}
            <dd className="inline" data-testid="bridge-channel-name">
              {channelName}
            </dd>
          </div>
        </dl>
      )}
    </div>
  );
}

/** The Bridge type of the Destination form: no chat-wide mention, no groups, and its own limiter per channel. */
export const BRIDGE_KIND: DestinationKind<BridgeFieldValues> = {
  type: "bridge",
  everyone: [],
  groups: false,
  defaultLimiter: DEFAULT_BRIDGE_DESTINATION_LIMITER,
  limiterHint: (t) => t("destinations.bridge.limiterHint"),
  mentionsHint: (t) => t("mentions.everyoneHintBridge"),
  values: bridgeValues,
  check: (v) => {
    const errors: Record<string, string> = {};
    if (v.connection_id === "") {
      errors["/connection_id"] = "required";
    }
    const channel = v.channel_id.trim();
    if (channel === "") {
      errors["/channel_id"] = "required";
    } else if (Array.from(channel).length > CHANNEL_ID_MAX) {
      errors["/channel_id"] = "too_long";
    }
    return errors;
  },
  input: (common, v) => ({
    type: "bridge",
    ...common,
    connection_id: v.connection_id,
    channel_id: v.channel_id.trim(),
  }),
  pointers: ["/connection_id", "/channel_id"],
  Fields: BridgeDestinationFields,
};
