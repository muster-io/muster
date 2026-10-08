// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The team and channel pickers of a Mattermost Destination (C-13.FR-9): both lists come from listConnectionChannels,
// which asks the Mattermost server through the Connection's bot and so needs connections:write. They are native
// selects (D250). Without connections:write, or while the form only shows the Destination, the stored team and
// channel names are shown instead. A busy messenger answers 503 with Retry-After, which the picker names, with "Try
// again".

import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import { useListConnectionChannels } from "../api/gen/endpoints/connections/connections";
import type { MattermostChannel } from "../api/gen/model";
import { useCan } from "./app-shell";
import { checkErrorText } from "./connection-check";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** A channel as its option shows it: the display name, and the name when they differ more than in case. */
export function channelLabel(t: TFunction, channel: MattermostChannel): string {
  const label =
    channel.display_name === "" || channel.display_name.toLowerCase() === channel.name.toLowerCase()
      ? channel.name
      : t("destinations.mattermost.channelWithName", {
          display: channel.display_name,
          name: channel.name,
        });
  return channel.archived === true ? t("destinations.mattermost.archived", { name: label }) : label;
}

/** The channels a Destination can post to: public and private ones, not archived, of the team. */
export function teamChannels(
  channels: readonly MattermostChannel[],
  teamId: string,
): MattermostChannel[] {
  return channels
    .filter(
      (c) =>
        c.team_id === teamId && (c.type === "open" || c.type === "private") && c.archived !== true,
    )
    .toSorted((a, b) => a.name.localeCompare(b.name));
}

/** The teams of the channels, each once, by name. */
export function teamsOf(channels: readonly MattermostChannel[]): { id: string; name: string }[] {
  const teams = new Map<string, string>();
  for (const c of channels) {
    if (c.team_id !== "" && !teams.has(c.team_id)) {
      teams.set(c.team_id, c.team_name || c.team_id);
    }
  }
  return [...teams.entries()]
    .map(([id, name]) => ({ id, name }))
    .toSorted((a, b) => a.name.localeCompare(b.name));
}

export interface ChannelPickerProps {
  id: string;
  connectionId: string;
  teamId: string;
  channelId: string;
  onChange: (next: { teamId: string; channelId: string; channel?: MattermostChannel }) => void;
  /** The names of the stored team and channel, shown when the lists cannot be loaded. */
  storedTeamName?: string | null;
  storedChannelName?: string | null;
  errors: { team?: string; channel?: string };
  disabled: boolean;
}

function FieldError({ id, text }: { id: string; text?: string }) {
  if (text === undefined) {
    return null;
  }
  return (
    <p id={id} className="text-sm wrap-anywhere text-destructive" data-testid={`${id}-text`}>
      {text}
    </p>
  );
}

export function ChannelPicker({
  id,
  connectionId,
  teamId,
  channelId,
  onChange,
  storedTeamName,
  storedChannelName,
  errors,
  disabled,
}: ChannelPickerProps) {
  const { t } = useTranslation();
  const canList = useCan("connections:write");
  const live = canList && !disabled && connectionId !== "";
  const channels = useListConnectionChannels(connectionId, undefined, {
    query: { enabled: live, staleTime: 30_000, retry: false },
  });
  const teamErrorId = `${id}-team-error`;
  const channelErrorId = `${id}-channel-error`;

  if (!live) {
    return (
      <div className="grid min-w-0 gap-4 md:grid-cols-2" data-testid="channel-stored">
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-sm font-medium">{t("destinations.mattermost.team")}</span>
          <span className="text-sm wrap-anywhere" data-testid="stored-team">
            {storedTeamName || teamId || "—"}
          </span>
          <FieldError id={teamErrorId} text={errors.team} />
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-sm font-medium">{t("destinations.mattermost.channel")}</span>
          <span className="text-sm wrap-anywhere" data-testid="stored-channel">
            {storedChannelName || channelId || "—"}
          </span>
          <FieldError id={channelErrorId} text={errors.channel} />
        </div>
        {!disabled && !canList && (
          <p className="text-sm text-muted-foreground md:col-span-2">
            {t("destinations.mattermost.needsConnectionsWrite")}
          </p>
        )}
        {!disabled && canList && connectionId === "" && (
          <p className="text-sm text-muted-foreground md:col-span-2">
            {t("destinations.mattermost.connectionFirst")}
          </p>
        )}
      </div>
    );
  }

  const items = channels.data?.items ?? [];
  const teams = teamsOf(items);
  if (teamId !== "" && !teams.some((tm) => tm.id === teamId)) {
    teams.push({ id: teamId, name: storedTeamName || teamId });
  }
  const options: { id: string; label: string; channel?: MattermostChannel }[] = teamChannels(
    items,
    teamId,
  ).map((c) => ({ id: c.id, label: channelLabel(t, c), channel: c }));
  if (channelId !== "" && !options.some((o) => o.id === channelId)) {
    const known = items.find((c) => c.id === channelId);
    options.unshift({
      id: channelId,
      label: known === undefined ? storedChannelName || channelId : channelLabel(t, known),
      channel: known,
    });
  }
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="grid min-w-0 gap-4 md:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-2">
          <Label htmlFor={`${id}-team`}>{t("destinations.mattermost.team")}</Label>
          <NativeSelect
            id={`${id}-team`}
            className="w-full"
            value={teamId}
            disabled={channels.isPending}
            aria-invalid={errors.team !== undefined}
            aria-describedby={errors.team === undefined ? undefined : teamErrorId}
            onChange={(e) => onChange({ teamId: e.target.value, channelId: "" })}
          >
            <NativeSelectOption value="">
              {channels.isPending ? t("common.loading") : t("destinations.mattermost.chooseTeam")}
            </NativeSelectOption>
            {teams.map((tm) => (
              <NativeSelectOption key={tm.id} value={tm.id}>
                {tm.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <FieldError id={teamErrorId} text={errors.team} />
        </div>
        <div className="flex min-w-0 flex-col gap-2">
          <Label htmlFor={`${id}-channel`}>{t("destinations.mattermost.channel")}</Label>
          <NativeSelect
            id={`${id}-channel`}
            className="w-full"
            value={channelId}
            disabled={teamId === "" || channels.isPending}
            aria-invalid={errors.channel !== undefined}
            aria-describedby={
              errors.channel === undefined
                ? `${id}-channel-hint`
                : `${id}-channel-hint ${channelErrorId}`
            }
            onChange={(e) => {
              const option = options.find((o) => o.id === e.target.value);
              onChange({ teamId, channelId: e.target.value, channel: option?.channel });
            }}
          >
            <NativeSelectOption value="">
              {t("destinations.mattermost.chooseChannel")}
            </NativeSelectOption>
            {options.map((o) => (
              <NativeSelectOption key={o.id} value={o.id}>
                {o.label}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <p id={`${id}-channel-hint`} className="text-sm text-muted-foreground">
            {t("destinations.mattermost.channelHint")}
          </p>
          <FieldError id={channelErrorId} text={errors.channel} />
        </div>
      </div>
      {channels.isError && (
        <div
          className="flex flex-wrap items-center gap-3 text-sm text-destructive"
          role="alert"
          data-testid="channels-error"
        >
          <span className="min-w-0 wrap-anywhere">{checkErrorText(t, channels.error)}</span>
          <Button type="button" variant="outline" size="sm" onClick={() => void channels.refetch()}>
            {t("destinations.mattermost.retry")}
          </Button>
        </div>
      )}
      {channels.isSuccess && teamId !== "" && options.length === 0 && (
        <p className="text-sm text-muted-foreground" role="status">
          {t("destinations.mattermost.noChannels")}
        </p>
      )}
    </div>
  );
}
