// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The fields of a Mattermost Destination (C-13.FR-9, FR-2): the Connection, then the team and the channel from the
// lists the bot sees. A new Destination takes the channel's name and the Connection's limiter until the user changes
// them (destination.mattermost.limiter). Saving runs the Destination check: a channel without the bot is refused with
// the check's message next to the channel.

import { useTranslation } from "react-i18next";

import { useListConnections } from "../api/gen/endpoints/connections/connections";
import type { Destination, MattermostConnection } from "../api/gen/model";
import { useCan } from "./app-shell";
import { ChannelPicker } from "./channel-picker";
import type { DestinationKind, TypeFieldsProps } from "./destination-form";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

export interface MattermostFieldValues {
  connection_id: string;
  team_id: string;
  channel_id: string;
}

function isMattermostConnection(c: { type: string }): c is MattermostConnection {
  return c.type === "mattermost";
}

export function mattermostValues(d: Destination | undefined): MattermostFieldValues {
  return d?.type === "mattermost"
    ? { connection_id: d.connection_id, team_id: d.team_id, channel_id: d.channel_id }
    : { connection_id: "", team_id: "", channel_id: "" };
}

export function MattermostDestinationFields({
  id,
  value,
  onChange,
  errors,
  disabled,
  destination,
  suggest,
}: TypeFieldsProps<MattermostFieldValues>) {
  const { t } = useTranslation();
  const canConnections = useCan("connections:read");
  const connections = useListConnections({ limit: 500 }, { query: { enabled: canConnections } });
  const items = (connections.data?.items ?? []).filter(isMattermostConnection);
  const stored = destination?.type === "mattermost" ? destination : undefined;
  const connectionError = errors["/connection_id"];
  const options = items.map((c) => ({ id: c.id, name: c.name }));
  if (value.connection_id !== "" && !options.some((o) => o.id === value.connection_id)) {
    options.unshift({ id: value.connection_id, name: value.connection_id });
  }
  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="mattermost-fields">
      <div className="flex max-w-md min-w-0 flex-col gap-2">
        <Label htmlFor={`${id}-connection`}>{t("destinations.mattermost.connection")}</Label>
        <NativeSelect
          id={`${id}-connection`}
          className="w-full"
          value={value.connection_id}
          aria-invalid={connectionError !== undefined}
          aria-describedby={connectionError === undefined ? undefined : `${id}-connection-error`}
          onChange={(e) => {
            const next = e.target.value;
            onChange({ connection_id: next, team_id: "", channel_id: "" });
            const chosen = items.find((c) => c.id === next);
            if (chosen !== undefined) {
              suggest({ limiter: chosen.limiter });
            }
          }}
        >
          <NativeSelectOption value="">
            {connections.isPending && canConnections
              ? t("common.loading")
              : t("destinations.mattermost.chooseConnection")}
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
            {t("destinations.mattermost.noConnections")}
          </p>
        )}
      </div>
      <ChannelPicker
        id={id}
        connectionId={value.connection_id}
        teamId={value.team_id}
        channelId={value.channel_id}
        storedTeamName={stored?.team_id === value.team_id ? stored.team_name : undefined}
        storedChannelName={
          stored?.channel_id === value.channel_id ? stored.channel_name : undefined
        }
        errors={{ team: errors["/team_id"], channel: errors["/channel_id"] }}
        disabled={disabled}
        onChange={({ teamId, channelId, channel }) => {
          onChange({ ...value, team_id: teamId, channel_id: channelId });
          if (channel !== undefined) {
            suggest({ name: channel.name });
          }
        }}
      />
    </div>
  );
}

/** The Mattermost type of the Destination form: @channel, @all and @here, groups, and the Connection's limiter. */
export const MATTERMOST_KIND: DestinationKind<MattermostFieldValues> = {
  type: "mattermost",
  everyone: ["channel", "all", "here"],
  groups: true,
  defaultLimiter: { limit: 5, per_seconds: 1 },
  limiterHint: (t) => t("destinations.mattermost.limiterHint"),
  values: mattermostValues,
  check: (v) => {
    const errors: Record<string, string> = {};
    if (v.connection_id === "") {
      errors["/connection_id"] = "required";
    }
    if (v.team_id === "") {
      errors["/team_id"] = "required";
    }
    if (v.channel_id === "") {
      errors["/channel_id"] = "required";
    }
    return errors;
  },
  input: (common, v) => ({
    type: "mattermost",
    ...common,
    connection_id: v.connection_id,
    team_id: v.team_id,
    channel_id: v.channel_id,
  }),
  pointers: ["/connection_id", "/team_id", "/channel_id"],
  Fields: MattermostDestinationFields,
};
