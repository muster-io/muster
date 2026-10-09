// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The fields of a Telegram Destination (C-14.FR-2, FR-14): the Connection and the channel only, as @username or chat
// id. Muster finds the channel's discussion group itself: saving runs the Destination check, which refuses a channel
// without comments or a discussion group where the bot is not an admin, with the check's message next to the channel,
// and the saved Destination shows the channel and the discussion group it found, read-only. A new Destination takes
// the channel's username as its name until the user changes it. Telegram has no chat-wide mention and no groups.

import { useTranslation } from "react-i18next";

import { useListConnections } from "../api/gen/endpoints/connections/connections";
import type { Destination, TelegramConnection, TelegramDestination } from "../api/gen/model";
import { useCan } from "./app-shell";
import type { DestinationKind, TypeFieldsProps } from "./destination-form";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

export interface TelegramFieldValues {
  connection_id: string;
  channel_id: string;
}

/** destination.telegram.limiter: 10 requests per minute, sends and edits together. */
export const DEFAULT_TELEGRAM_DESTINATION_LIMITER = { limit: 10, per_seconds: 60 } as const;

function isTelegramConnection(c: { type: string }): c is TelegramConnection {
  return c.type === "telegram";
}

export function telegramValues(d: Destination | undefined): TelegramFieldValues {
  return d?.type === "telegram"
    ? { connection_id: d.connection_id, channel_id: d.channel_id }
    : { connection_id: "", channel_id: "" };
}

/** The name a channel suggests: its username without the @, or its chat id. */
export function channelName(channel: string): string {
  return channel.trim().replace(/^@/, "");
}

/** What the saved Destination found, while the form still names its Connection and channel. */
function Found({ destination }: { destination: TelegramDestination }) {
  const { t } = useTranslation();
  const channel = destination.channel_title ?? "";
  const groupId = destination.discussion_group_id ?? "";
  return (
    <dl className="flex min-w-0 flex-col gap-1 text-sm" data-testid="telegram-found">
      {channel !== "" && (
        <div className="min-w-0 wrap-anywhere" data-testid="telegram-channel-title">
          <dt className="inline font-medium">{t("destinations.telegram.channelFound")}</dt>{" "}
          <dd className="inline">{channel}</dd>
        </div>
      )}
      <div className="min-w-0 wrap-anywhere" data-testid="telegram-discussion-group">
        <dt className="inline font-medium">{t("destinations.telegram.discussionGroup")}</dt>{" "}
        <dd className="inline">
          {groupId === ""
            ? t("destinations.telegram.discussionGroupUnknown")
            : t("destinations.telegram.discussionGroupValue", {
                title: destination.discussion_group_title ?? groupId,
                id: groupId,
              })}
        </dd>
      </div>
    </dl>
  );
}

export function TelegramDestinationFields({
  id,
  value,
  onChange,
  errors,
  disabled,
  destination,
  suggest,
}: TypeFieldsProps<TelegramFieldValues>) {
  const { t } = useTranslation();
  const canConnections = useCan("connections:read");
  const connections = useListConnections({ limit: 500 }, { query: { enabled: canConnections } });
  const items = (connections.data?.items ?? []).filter(isTelegramConnection);
  const stored = destination?.type === "telegram" ? destination : undefined;
  const connectionError = errors["/connection_id"];
  const channelError = errors["/channel_id"];
  const options = items.map((c) => ({ id: c.id, name: c.name }));
  if (value.connection_id !== "" && !options.some((o) => o.id === value.connection_id)) {
    options.unshift({ id: value.connection_id, name: value.connection_id });
  }
  const showFound =
    stored !== undefined &&
    stored.connection_id === value.connection_id &&
    stored.channel_id === value.channel_id.trim();
  const channelId = `${id}-channel`;
  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="telegram-fields">
      <div className="flex max-w-md min-w-0 flex-col gap-2">
        <Label htmlFor={`${id}-connection`}>{t("destinations.telegram.connection")}</Label>
        <NativeSelect
          id={`${id}-connection`}
          className="w-full"
          value={value.connection_id}
          aria-invalid={connectionError !== undefined}
          aria-describedby={connectionError === undefined ? undefined : `${id}-connection-error`}
          onChange={(e) => onChange({ ...value, connection_id: e.target.value })}
        >
          <NativeSelectOption value="">
            {connections.isPending && canConnections
              ? t("common.loading")
              : t("destinations.telegram.chooseConnection")}
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
          <p className="text-sm text-muted-foreground">
            {t("destinations.telegram.noConnections")}
          </p>
        )}
      </div>
      <div className="flex max-w-md min-w-0 flex-col gap-2">
        <Label htmlFor={channelId}>{t("destinations.telegram.channel")}</Label>
        <Input
          id={channelId}
          autoComplete="off"
          spellCheck={false}
          placeholder={t("destinations.telegram.channelPlaceholder")}
          value={value.channel_id}
          aria-invalid={channelError !== undefined}
          aria-describedby={
            channelError === undefined
              ? `${channelId}-hint`
              : `${channelId}-hint ${channelId}-error`
          }
          onChange={(e) => {
            const next = e.target.value;
            onChange({ ...value, channel_id: next });
            if (channelName(next) !== "") {
              suggest({ name: channelName(next) });
            }
          }}
        />
        <p id={`${channelId}-hint`} className="text-sm text-muted-foreground">
          {t("destinations.telegram.channelHint")}
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
      {showFound && <Found destination={stored} />}
    </div>
  );
}

/** The Telegram type of the Destination form: no chat-wide mention, no groups, and its own limiter per chat. */
export const TELEGRAM_KIND: DestinationKind<TelegramFieldValues> = {
  type: "telegram",
  everyone: [],
  groups: false,
  defaultLimiter: DEFAULT_TELEGRAM_DESTINATION_LIMITER,
  limiterHint: (t) => t("destinations.telegram.limiterHint"),
  mentionsHint: (t) => t("mentions.everyoneHintTelegram"),
  values: telegramValues,
  check: (v) => {
    const errors: Record<string, string> = {};
    if (v.connection_id === "") {
      errors["/connection_id"] = "required";
    }
    if (v.channel_id.trim() === "") {
      errors["/channel_id"] = "required";
    }
    return errors;
  },
  input: (common, v) => ({
    type: "telegram",
    ...common,
    connection_id: v.connection_id,
    channel_id: v.channel_id.trim(),
  }),
  pointers: ["/connection_id", "/channel_id"],
  Fields: TelegramDestinationFields,
};
