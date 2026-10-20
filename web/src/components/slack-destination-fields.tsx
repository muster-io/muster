// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The fields of a Slack Destination (C-24.FR-3, AC-18): a Slack Connection, then a channel from the channels its bot is
// a member of — public and private, not archived — read with listConnectionChannels and filtered by name on the server
// (a leading # of the filter is dropped, so "#alerts" finds alerts), with a note when Slack had more channels than the
// list shows; or the channel id typed by hand, C… or G…, which the list also fills. The list needs connections:write,
// as it calls Slack with the bot token; without it only the id is typed. Saving runs the Destination check, the item
// channel: a channel without the bot is refused with "Invite the bot to the channel: /invite @{bot}" next to the
// channel, naming the bot of the Connection, and the saved Destination shows "#" and the channel's name the check
// found, as text only. A new Destination takes the picked channel's name until the user changes it. Slack mentions the
// whole channel with @channel or @here, never @all, and Muster names no groups (C-24.FR-6).

import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getListConnectionChannelsQueryKey,
  useListConnectionChannels,
  useListConnections,
} from "../api/gen/endpoints/connections/connections";
import type {
  ConnectionChannelList,
  Destination,
  SlackChannel,
  SlackConnection,
} from "../api/gen/model";
import { useCan } from "./app-shell";
import { checkErrorText } from "./connection-check";
import { destinationCheckDetail, destinationCheckText } from "./destination-check";
import type { DestinationKind, TypeFieldsProps } from "./destination-form";
import { PREVIEW_DELAY_MS, useDebounced } from "./template-preview";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

export interface SlackFieldValues {
  connection_id: string;
  channel_id: string;
}

/** destination.slack.limiter: 30 requests per minute, sends and edits together. */
export const DEFAULT_SLACK_DESTINATION_LIMITER = { limit: 30, per_seconds: 60 } as const;

/** A Slack channel id as the server takes it: C or G, then upper-case letters and digits. */
const CHANNEL_ID = /^[CG][A-Z0-9]{2,63}$/;

export function isSlackChannelId(text: string): boolean {
  return CHANNEL_ID.test(text.trim());
}

/** The filter of the channel list as listConnectionChannels takes it: trimmed, without a leading #. */
export function channelQuery(filter: string): string {
  return filter.trim().replace(/^#+/, "").trim();
}

function isSlackConnection(c: { type: string }): c is SlackConnection {
  return c.type === "slack";
}

/** The channels of a Slack Connection's list; a Mattermost Connection's list has no `truncated`. */
function slackList(list: ConnectionChannelList | undefined): {
  items: SlackChannel[];
  truncated: boolean;
} {
  return list === undefined || !("truncated" in list)
    ? { items: [], truncated: false }
    : { items: list.items, truncated: list.truncated };
}

export function slackValues(d: Destination | undefined): SlackFieldValues {
  return d?.type === "slack"
    ? { connection_id: d.connection_id, channel_id: d.channel_id }
    : { connection_id: "", channel_id: "" };
}

/** The channel as Slack names it: "#name", or "#id" while no check read its name. */
export function slackChannelLabel(channel: { id: string; name?: string | null }): string {
  return `#${channel.name || channel.id}`;
}

export function SlackDestinationFields({
  id,
  value,
  onChange,
  errors,
  problems,
  disabled,
  destination,
  suggest,
}: TypeFieldsProps<SlackFieldValues>) {
  const { t } = useTranslation();
  const canConnections = useCan("connections:read");
  const canList = useCan("connections:write");
  const connections = useListConnections({ limit: 500 }, { query: { enabled: canConnections } });
  const items = (connections.data?.items ?? []).filter(isSlackConnection);
  const stored = destination?.type === "slack" ? destination : undefined;
  const chosen = items.find((c) => c.id === value.connection_id);
  const [filter, setFilter] = useState("");
  const query = channelQuery(filter);
  const settled = useDebounced(query, query, PREVIEW_DELAY_MS);
  const live = canList && !disabled && value.connection_id !== "";
  const channels = useListConnectionChannels(
    value.connection_id,
    settled === "" ? undefined : { q: settled },
    {
      query: {
        enabled: live,
        staleTime: 30_000,
        retry: false,
        // Another filter keeps the channels shown until its list comes, but never another Connection's.
        placeholderData: (previous, previousQuery) =>
          previousQuery?.queryKey[0] === getListConnectionChannelsQueryKey(value.connection_id)[0]
            ? previous
            : undefined,
      },
    },
  );
  const list = slackList(channels.data);
  const connectionError = errors["/connection_id"];
  // A channel without the bot names the bot of the Connection, in the reader's language (C-24.FR-3).
  const channelProblem = problems["/channel_id"];
  const context = { slackBot: chosen?.bot_name ?? null };
  const channelError =
    channelProblem?.code === "destination_check_failed" && channelProblem.checkCode !== undefined
      ? destinationCheckText(t, channelProblem.checkCode, context)
      : channelProblem?.code === "invalid_format"
        ? t("destinations.slack.channelIdFormat")
        : errors["/channel_id"];
  const channelDetail =
    channelProblem?.code === "destination_check_failed" && channelProblem.checkCode !== undefined
      ? destinationCheckDetail(channelProblem.checkCode, channelProblem.detail, context)
      : null;
  const options = items.map((c) => ({ id: c.id, name: c.name }));
  if (value.connection_id !== "" && !options.some((o) => o.id === value.connection_id)) {
    options.unshift({ id: value.connection_id, name: value.connection_id });
  }
  const typed = value.channel_id.trim();
  const picked = list.items.some((c) => c.id === typed) ? typed : "";
  const channelName = stored?.channel_name ?? "";
  const showFound =
    stored !== undefined &&
    channelName !== "" &&
    stored.connection_id === value.connection_id &&
    stored.channel_id === typed;
  const pickerId = `${id}-channel-list`;
  const filterId = `${id}-channel-filter`;
  const channelId = `${id}-channel`;
  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="slack-fields">
      <div className="flex max-w-md min-w-0 flex-col gap-2">
        <Label htmlFor={`${id}-connection`}>{t("destinations.slack.connection")}</Label>
        <NativeSelect
          id={`${id}-connection`}
          className="w-full"
          value={value.connection_id}
          aria-invalid={connectionError !== undefined}
          aria-describedby={connectionError === undefined ? undefined : `${id}-connection-error`}
          onChange={(e) => {
            setFilter("");
            onChange({ connection_id: e.target.value, channel_id: "" });
          }}
        >
          <NativeSelectOption value="">
            {connections.isPending && canConnections
              ? t("common.loading")
              : t("destinations.slack.chooseConnection")}
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
          <p className="text-sm text-muted-foreground">{t("destinations.slack.noConnections")}</p>
        )}
      </div>
      {live && (
        <div className="flex min-w-0 flex-col gap-2" data-testid="slack-channel-picker">
          <div className="grid min-w-0 gap-4 md:grid-cols-2">
            <div className="flex min-w-0 flex-col gap-2">
              <Label htmlFor={filterId}>{t("destinations.slack.filter")}</Label>
              <Input
                id={filterId}
                type="search"
                autoComplete="off"
                spellCheck={false}
                placeholder={t("destinations.slack.filterPlaceholder")}
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
            </div>
            <div className="flex min-w-0 flex-col gap-2">
              <Label htmlFor={pickerId}>{t("destinations.slack.channel")}</Label>
              <NativeSelect
                id={pickerId}
                className="w-full"
                value={picked}
                disabled={channels.isPending}
                aria-describedby={`${pickerId}-hint`}
                onChange={(e) => {
                  const channel = list.items.find((c) => c.id === e.target.value);
                  onChange({ ...value, channel_id: e.target.value });
                  if (channel !== undefined) {
                    suggest({ name: channel.name });
                  }
                }}
              >
                <NativeSelectOption value="">
                  {channels.isPending ? t("common.loading") : t("destinations.slack.chooseChannel")}
                </NativeSelectOption>
                {list.items.map((c) => (
                  <NativeSelectOption key={c.id} value={c.id}>
                    {c.is_private
                      ? t("destinations.slack.privateChannel", { name: slackChannelLabel(c) })
                      : slackChannelLabel(c)}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </div>
          </div>
          <p id={`${pickerId}-hint`} className="text-sm text-muted-foreground">
            {t("destinations.slack.channelHint")}
          </p>
          {list.truncated && (
            <p className="text-sm text-muted-foreground" data-testid="slack-channels-truncated">
              {t("destinations.slack.truncated")}
            </p>
          )}
          {channels.isSuccess && list.items.length === 0 && (
            <p className="text-sm text-muted-foreground" role="status">
              {query === ""
                ? t("destinations.slack.noChannels")
                : t("destinations.slack.noMatch", { filter: query })}
            </p>
          )}
          {channels.isError && (
            <div
              className="flex flex-wrap items-center gap-3 text-sm text-destructive"
              role="alert"
              data-testid="channels-error"
            >
              <span className="min-w-0 wrap-anywhere">{checkErrorText(t, channels.error)}</span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => void channels.refetch()}
              >
                {t("destinations.mattermost.retry")}
              </Button>
            </div>
          )}
        </div>
      )}
      <div className="flex max-w-md min-w-0 flex-col gap-2">
        <Label htmlFor={channelId}>{t("destinations.slack.channelId")}</Label>
        <Input
          id={channelId}
          autoComplete="off"
          spellCheck={false}
          placeholder="C0123456789"
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
          {t("destinations.slack.channelIdHint")}
        </p>
        {channelError !== undefined && (
          <p
            id={`${channelId}-error`}
            className="flex flex-col gap-0.5 text-sm wrap-anywhere text-destructive"
            data-testid={`${channelId}-error-text`}
          >
            <span>{channelError}</span>
            {channelDetail !== null && (
              <span className="text-xs text-muted-foreground">
                {t("checks.detail", { detail: channelDetail })}
              </span>
            )}
          </p>
        )}
        {!disabled && !canList && (
          <p className="text-sm text-muted-foreground">
            {t("destinations.slack.needsConnectionsWrite")}
          </p>
        )}
      </div>
      {showFound && (
        <dl className="flex min-w-0 flex-col gap-1 text-sm" data-testid="slack-found">
          <div className="min-w-0 wrap-anywhere">
            <dt className="inline font-medium">{t("destinations.bridge.channelFound")}</dt>{" "}
            <dd className="inline" data-testid="slack-channel-name">
              {slackChannelLabel({ id: typed, name: channelName })}
            </dd>
          </div>
        </dl>
      )}
    </div>
  );
}

/**
 * The Slack type of the Destination form: @channel and @here, no groups, and its own limiter per channel
 * (destination.slack.limiter).
 */
export const SLACK_KIND: DestinationKind<SlackFieldValues> = {
  type: "slack",
  everyone: ["channel", "here"],
  groups: false,
  defaultLimiter: DEFAULT_SLACK_DESTINATION_LIMITER,
  limiterHint: (t) => t("destinations.slack.limiterHint"),
  mentionsHint: (t) => t("mentions.everyoneHintSlack"),
  values: slackValues,
  check: (v) => {
    const errors: Record<string, string> = {};
    if (v.connection_id === "") {
      errors["/connection_id"] = "required";
    }
    const channel = v.channel_id.trim();
    if (channel === "") {
      errors["/channel_id"] = "required";
    } else if (!isSlackChannelId(channel)) {
      errors["/channel_id"] = "invalid_format";
    }
    return errors;
  },
  input: (common, v) => ({
    type: "slack",
    ...common,
    connection_id: v.connection_id,
    channel_id: v.channel_id.trim(),
  }),
  pointers: ["/connection_id", "/channel_id"],
  Fields: SlackDestinationFields,
};
