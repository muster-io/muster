// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Mention section of the Destination form (C-12.FR-8): for each kind of Loud event whom its message mentions —
// nobody (the default), the chat-wide mention the Destination type allows (@channel, @all or @here for Mattermost, none
// for Telegram), chosen Muster users, rendered through their Account links, and messenger groups (Mattermost group
// names). The pickers are native selects (D250); each chosen user or group is a chip that removes it again.

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { XIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { getListUserDirectoryQueryKey, listUserDirectory } from "../api/gen/endpoints/users/users";
import type {
  MentionSetting,
  MentionSettingEveryone,
  MentionSettings,
  UserRef,
} from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { directoryLabel } from "./owner-filter";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The kinds of Loud events with Mention settings, in the order of the form. */
export const MENTION_KINDS = [
  "new_alert_group",
  "new_alerts",
  "reopen",
  "ack_timeout",
  "snooze_ended",
  "rise_to_urgent",
] as const satisfies readonly (keyof MentionSettings)[];
export type MentionKind = (typeof MENTION_KINDS)[number];

/** Nobody: the default of every kind. */
export const NOBODY: MentionSetting = { everyone: "none", user_ids: [], groups: [] };

/** The settings of a new Destination: nobody for every kind. */
export function defaultMentions(): MentionSettings {
  return {
    new_alert_group: { ...NOBODY },
    new_alerts: { ...NOBODY },
    reopen: { ...NOBODY },
    ack_timeout: { ...NOBODY },
    snooze_ended: { ...NOBODY },
    rise_to_urgent: { ...NOBODY },
  };
}

/** A Mattermost group name, as the server checks it. */
const GROUP_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

export function isGroupName(text: string): boolean {
  return GROUP_NAME.test(text);
}

export function mentionKindName(t: TFunction, kind: MentionKind): string {
  switch (kind) {
    case "new_alert_group":
      return t("mentions.kinds.newAlertGroup");
    case "new_alerts":
      return t("mentions.kinds.newAlerts");
    case "reopen":
      return t("mentions.kinds.reopen");
    case "ack_timeout":
      return t("mentions.kinds.ackTimeout");
    case "snooze_ended":
      return t("mentions.kinds.snoozeEnded");
    default:
      return t("mentions.kinds.riseToUrgent");
  }
}

function everyoneLabel(t: TFunction, everyone: MentionSettingEveryone): string {
  return everyone === "none" ? t("mentions.nobody") : `@${everyone}`;
}

/** The text of a refused Mention setting. */
export function mentionErrorText(t: TFunction, code: string): string {
  switch (code) {
    case "unknown_id":
      return t("mentions.errors.unknownUser");
    case "duplicate":
      return t("mentions.errors.duplicateUser");
    case "unsupported":
      return t("mentions.errors.unsupported");
    case "invalid_format":
      return t("mentions.errors.invalidGroup");
    default:
      return fieldErrorText(t, code);
  }
}

/** The users of the directory the pickers offer; api.page_size allows at most 500 in one page. */
const DIRECTORY_LIMIT = 500;

function useDirectory(enabled: boolean) {
  const params = { limit: DIRECTORY_LIMIT };
  return useQuery({
    queryKey: getListUserDirectoryQueryKey(params),
    queryFn: ({ signal }) => listUserDirectory(params, { signal }),
    placeholderData: keepPreviousData,
    staleTime: 60_000,
    enabled,
  });
}

function Chip({
  label,
  removeLabel,
  onRemove,
  disabled,
}: {
  label: string;
  removeLabel: string;
  onRemove: () => void;
  disabled: boolean;
}) {
  return (
    <li className="inline-flex max-w-full items-center gap-0.5 rounded-md border bg-muted py-0.5 pr-0.5 pl-2 text-xs">
      <span className="min-w-0 wrap-anywhere">{label}</span>
      {!disabled && (
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={removeLabel}
          onClick={onRemove}
        >
          <XIcon aria-hidden="true" />
        </Button>
      )}
    </li>
  );
}

function KindRow({
  id,
  kind,
  value,
  onChange,
  everyone,
  groups,
  users,
  error,
  disabled,
  groupPlaceholder,
}: {
  id: string;
  kind: MentionKind;
  value: MentionSetting;
  onChange: (next: MentionSetting) => void;
  everyone: readonly MentionSettingEveryone[];
  groups: boolean;
  users: readonly UserRef[];
  error?: string;
  disabled: boolean;
  groupPlaceholder?: string;
}) {
  const { t } = useTranslation();
  const [group, setGroup] = useState("");
  const [groupProblem, setGroupProblem] = useState(false);
  const name = mentionKindName(t, kind);
  const base = `${id}-${kind.replaceAll("_", "-")}`;
  const userName = (userId: string) => {
    const user = users.find((u) => u.id === userId);
    return user === undefined ? t("alertGroups.filters.ownerUnknown") : directoryLabel(t, user);
  };
  const rest = users.filter((u) => !u.deactivated && !value.user_ids.includes(u.id));
  const addGroup = () => {
    const text = group.trim().replace(/^@/, "");
    if (!isGroupName(text)) {
      setGroupProblem(true);
      return;
    }
    setGroupProblem(false);
    setGroup("");
    if (!value.groups.includes(text)) {
      onChange({ ...value, groups: [...value.groups, text] });
    }
  };
  const choices: MentionSettingEveryone[] = ["none", ...everyone.filter((e) => e !== "none")];
  const message = groupProblem ? t("mentions.errors.invalidGroup") : error;
  const chosen = value.user_ids.length + value.groups.length;
  return (
    <div
      className="flex min-w-0 flex-col gap-2 rounded-lg border p-3"
      data-testid="mention-kind"
      data-kind={kind}
    >
      <div className="flex min-w-0 flex-col gap-1.5">
        <Label htmlFor={`${base}-everyone`}>{name}</Label>
        <NativeSelect
          id={`${base}-everyone`}
          className="w-full"
          value={value.everyone}
          disabled={disabled || choices.length === 1}
          aria-invalid={error !== undefined}
          aria-describedby={
            message === undefined ? `${id}-everyone-hint` : `${id}-everyone-hint ${base}-error`
          }
          onChange={(e) => {
            const next = choices.find((c) => c === e.target.value);
            if (next !== undefined) {
              onChange({ ...value, everyone: next });
            }
          }}
        >
          {choices.map((c) => (
            <NativeSelectOption key={c} value={c}>
              {everyoneLabel(t, c)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>
      {chosen > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label={t("mentions.chosen", { kind: name })}>
          {value.user_ids.map((userId) => (
            <Chip
              key={`u-${userId}`}
              label={userName(userId)}
              removeLabel={t("mentions.remove", { value: userName(userId) })}
              disabled={disabled}
              onRemove={() =>
                onChange({ ...value, user_ids: value.user_ids.filter((u) => u !== userId) })
              }
            />
          ))}
          {value.groups.map((g) => (
            <Chip
              key={`g-${g}`}
              label={`@${g}`}
              removeLabel={t("mentions.remove", { value: `@${g}` })}
              disabled={disabled}
              onRemove={() => onChange({ ...value, groups: value.groups.filter((x) => x !== g) })}
            />
          ))}
        </ul>
      )}
      {!disabled && (
        <NativeSelect
          id={`${base}-users`}
          className="w-full"
          value=""
          aria-label={t("mentions.addUserTo", { kind: name })}
          disabled={rest.length === 0}
          onChange={(e) => {
            if (e.target.value !== "") {
              onChange({ ...value, user_ids: [...value.user_ids, e.target.value] });
            }
          }}
        >
          <NativeSelectOption value="">{t("mentions.addUser")}</NativeSelectOption>
          {rest.map((u) => (
            <NativeSelectOption key={u.id} value={u.id}>
              {directoryLabel(t, u)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      )}
      {groups && !disabled && (
        <div className="flex min-w-0 items-center gap-2">
          <Input
            id={`${base}-group`}
            value={group}
            autoComplete="off"
            spellCheck={false}
            className="min-w-0 flex-1"
            placeholder={groupPlaceholder ?? t("mentions.groupPlaceholder")}
            aria-label={t("mentions.addGroupTo", { kind: name })}
            aria-invalid={groupProblem}
            aria-describedby={message === undefined ? undefined : `${base}-error`}
            onChange={(e) => {
              setGroup(e.target.value);
              setGroupProblem(false);
            }}
            onKeyDown={(e) => {
              // Enter adds the group instead of saving the form.
              if (e.key === "Enter") {
                e.preventDefault();
                addGroup();
              }
            }}
          />
          <Button type="button" variant="outline" onClick={addGroup}>
            {t("mentions.addGroup")}
          </Button>
        </div>
      )}
      {message !== undefined && (
        <p id={`${base}-error`} className="text-sm text-destructive">
          {message}
        </p>
      )}
    </div>
  );
}

export interface MentionSettingsFieldProps {
  /** The base of the ids of the inputs, unique on the page. */
  id: string;
  value: MentionSettings;
  onChange: (next: MentionSettings) => void;
  /** The chat-wide mentions the Destination type allows besides nobody. */
  everyone: readonly MentionSettingEveryone[];
  /** Whether the type takes messenger groups. */
  groups: boolean;
  /** The help text of the choices, already translated; the chat-wide one of Mattermost by default. */
  everyoneHint?: string;
  /** Error texts by kind, already translated. */
  errors?: Partial<Record<MentionKind, string>>;
  disabled?: boolean;
  /** The placeholder of a group's name, already translated; a Mattermost group by default. */
  groupPlaceholder?: string;
}

export function MentionSettingsField({
  id,
  value,
  onChange,
  everyone,
  groups,
  everyoneHint,
  errors = {},
  disabled = false,
  groupPlaceholder,
}: MentionSettingsFieldProps) {
  const { t } = useTranslation();
  const directory = useDirectory(true);
  const users = directory.data?.items ?? [];
  return (
    <fieldset className="flex min-w-0 flex-col gap-3" data-testid="mention-settings">
      <legend className="mb-2 text-base font-semibold">{t("mentions.title")}</legend>
      <p className="text-sm text-muted-foreground">{t("mentions.hint")}</p>
      <p id={`${id}-everyone-hint`} className="sr-only">
        {everyoneHint ?? t("mentions.everyoneHint")}
      </p>
      <div className="grid min-w-0 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {MENTION_KINDS.map((kind) => (
          <KindRow
            key={kind}
            id={id}
            kind={kind}
            value={value[kind]}
            onChange={(next) => onChange({ ...value, [kind]: next })}
            everyone={everyone}
            groups={groups}
            users={users}
            error={errors[kind]}
            disabled={disabled}
            groupPlaceholder={groupPlaceholder}
          />
        ))}
      </div>
      <p className="text-sm text-muted-foreground" data-testid="mention-note">
        {t("mentions.always")}
      </p>
    </fieldset>
  );
}
