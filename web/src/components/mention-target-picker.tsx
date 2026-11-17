// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Add" of the Mentions grid (C-28.FR-1, FR-2): the list of targets a new entry can name — `@channel`, `@here`, `@all`,
// `@everyone`, "Group…", "Person…", "Team…", "Team from the label", "On-call of a Team…" and "On-call of the Team from
// the label" — with each target the Destination's Connection cannot mention greyed and giving its reason. A target that
// needs a choice opens it below the list: a user group of a Slack Connection or a group of a Mattermost one from the
// Connection's groups, a group of a bridge or of an outgoing webhook by name, a person from the directory and a Team
// from the Teams, in native selects (D250). Choosing adds the entry, which the grid shows ticked for every event.

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { PlusIcon } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { useListConnectionGroups } from "../api/gen/endpoints/connections/connections";
import { getListUserDirectoryQueryKey, listUserDirectory } from "../api/gen/endpoints/users/users";
import type { DestinationType, MentionTarget, MentionTargetKind } from "../api/gen/model";
import {
  EVERYONE_WORDS,
  MAX_MENTION_ENTRIES,
  type MentionAbilities,
  isGroupName,
  mentionErrorText,
  reasonText,
  targetKey,
  unavailableReason,
} from "../lib/mention-entries";
import { useCan } from "./app-shell";
import { directoryLabel } from "./owner-filter";
import { useTeamList } from "./team-filter";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The users of the directory the picker offers; api.page_size allows at most 500 in one page. */
const DIRECTORY_LIMIT = 500;

type Step = "group" | "user" | "team" | "on_call_of_team";

interface Option {
  id: string;
  kind: MentionTargetKind;
  everyone?: (typeof EVERYONE_WORDS)[number];
  /** The text of the option. */
  label: string;
  /** The step the option opens, or none when choosing it adds the entry. */
  step?: Step;
}

function GroupChoice({
  id,
  type,
  connectionId,
  onAdd,
}: {
  id: string;
  type: DestinationType;
  connectionId: string | undefined;
  onAdd: (group: string, groupId?: string) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [problem, setProblem] = useState(false);
  // The groups of a Slack or a Mattermost Connection are read from it; a bridge's and a webhook's are typed.
  const listed = type === "slack" || type === "mattermost";
  const canList = useCan("connections:write");
  const groups = useListConnectionGroups(connectionId ?? "", undefined, {
    query: {
      enabled: listed && canList && connectionId !== undefined && connectionId !== "",
      retry: false,
      staleTime: 30_000,
    },
  });
  const items = groups.data?.items ?? [];
  // A Slack user group needs its id from the list, so it is never typed.
  const slackUnlisted = type === "slack" && (!canList || groups.isError);
  const typed = !listed || (type !== "slack" && (!canList || groups.isError));
  const add = () => {
    const text = name.trim().replace(/^@/, "");
    if (type === "webhook" ? text === "" : !isGroupName(text)) {
      setProblem(true);
      return;
    }
    setName("");
    setProblem(false);
    onAdd(text);
  };
  if (slackUnlisted) {
    return (
      <p className="text-sm text-muted-foreground" data-testid="mention-groups-unlisted">
        {canList ? t("teams.mentions.groupsNotListed") : t("teams.mentions.needsConnectionsWrite")}
      </p>
    );
  }
  if (typed) {
    return (
      <div className="flex min-w-0 flex-col gap-1.5">
        <Label htmlFor={`${id}-group-name`}>{t("mentions.picker.groupName")}</Label>
        <div className="flex min-w-0 items-center gap-2">
          <Input
            id={`${id}-group-name`}
            value={name}
            autoComplete="off"
            spellCheck={false}
            className="min-w-0 flex-1"
            placeholder={t("mentions.picker.groupPlaceholder")}
            aria-invalid={problem}
            aria-describedby={problem ? `${id}-group-error` : undefined}
            onChange={(e) => {
              setName(e.target.value);
              setProblem(false);
            }}
            onKeyDown={(e) => {
              // Enter adds the group instead of saving the form.
              if (e.key === "Enter") {
                e.preventDefault();
                add();
              }
            }}
          />
          <Button type="button" variant="outline" onClick={add}>
            {t("mentions.picker.addGroup")}
          </Button>
        </div>
        {problem && (
          <p id={`${id}-group-error`} className="text-sm text-destructive">
            {mentionErrorText(t, "invalid_format")}
          </p>
        )}
        {listed && groups.isError && (
          <p className="text-sm text-muted-foreground">{t("mentions.picker.groupsNotListed")}</p>
        )}
      </div>
    );
  }
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={`${id}-group`}>{t("mentions.picker.group")}</Label>
      <NativeSelect
        id={`${id}-group`}
        className="w-full"
        value=""
        disabled={groups.isPending || items.length === 0}
        onChange={(e) => {
          const group = items.find((g) => g.id === e.target.value);
          if (group !== undefined) {
            onAdd(group.name, type === "slack" ? group.id : undefined);
          }
        }}
      >
        <NativeSelectOption value="">
          {groups.isPending ? t("common.loading") : t("mentions.picker.chooseGroup")}
        </NativeSelectOption>
        {items.map((g) => (
          <NativeSelectOption key={g.id} value={g.id}>
            {g.display_name === "" || g.display_name === g.name
              ? `@${g.name}`
              : `${g.display_name} (@${g.name})`}
          </NativeSelectOption>
        ))}
      </NativeSelect>
      {!groups.isPending && items.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("mentions.picker.noGroups")}</p>
      )}
      {groups.data?.truncated === true && (
        <p className="text-sm text-muted-foreground">{t("mentions.picker.groupsTruncated")}</p>
      )}
    </div>
  );
}

function PersonChoice({
  id,
  onAdd,
}: {
  id: string;
  onAdd: (user: { id: string; name: string; login?: string; deactivated: boolean }) => void;
}) {
  const { t } = useTranslation();
  const params = { limit: DIRECTORY_LIMIT };
  const directory = useQuery({
    queryKey: getListUserDirectoryQueryKey(params),
    queryFn: ({ signal }) => listUserDirectory(params, { signal }),
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  });
  const users = (directory.data?.items ?? []).filter((u) => !u.deactivated);
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={`${id}-person`}>{t("mentions.picker.person")}</Label>
      <NativeSelect
        id={`${id}-person`}
        className="w-full"
        value=""
        disabled={users.length === 0}
        onChange={(e) => {
          const user = users.find((u) => u.id === e.target.value);
          if (user !== undefined) {
            onAdd(user);
          }
        }}
      >
        <NativeSelectOption value="">
          {directory.isPending ? t("common.loading") : t("mentions.picker.choosePerson")}
        </NativeSelectOption>
        {users.map((u) => (
          <NativeSelectOption key={u.id} value={u.id}>
            {directoryLabel(t, u)}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </div>
  );
}

function TeamChoice({
  id,
  step,
  onAdd,
}: {
  id: string;
  step: "team" | "on_call_of_team";
  onAdd: (team: { id: string; name: string; slug: string }) => void;
}) {
  const { t } = useTranslation();
  const teams = useTeamList();
  const items = teams.data ?? [];
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={`${id}-team`}>
        {step === "team" ? t("mentions.picker.team") : t("mentions.picker.onCallTeam")}
      </Label>
      <NativeSelect
        id={`${id}-team`}
        className="w-full"
        value=""
        disabled={items.length === 0}
        onChange={(e) => {
          const team = items.find((x) => x.id === e.target.value);
          if (team !== undefined) {
            onAdd(team);
          }
        }}
      >
        <NativeSelectOption value="">
          {teams.isPending ? t("common.loading") : t("mentions.picker.chooseTeam")}
        </NativeSelectOption>
        {items.map((team) => (
          <NativeSelectOption key={team.id} value={team.id}>
            {team.name}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </div>
  );
}

export interface MentionTargetPickerProps {
  /** The base of the ids, unique on the page. */
  id: string;
  abilities: MentionAbilities;
  /** The id of the Destination's Connection, for the groups it lists; none for an outgoing webhook. */
  connectionId?: string;
  /** The targets the entries have already, which a target of one kind cannot name twice. */
  present: ReadonlySet<string>;
  /** Whether the Destination already has the most entries it takes. */
  full: boolean;
  disabled?: boolean;
  onAdd: (target: MentionTarget) => void;
}

export function MentionTargetPicker({
  id,
  abilities,
  connectionId,
  present,
  full,
  disabled = false,
  onAdd,
}: MentionTargetPickerProps) {
  const { t } = useTranslation();
  const panel = useId();
  const [open, setOpen] = useState(false);
  const [step, setStep] = useState<Step | null>(null);
  const { type } = abilities;

  const options: Option[] = [
    ...EVERYONE_WORDS.map((word): Option => ({
      id: `everyone-${word}`,
      kind: "everyone",
      everyone: word,
      label: `@${word}`,
    })),
    { id: "group", kind: "group", label: t("mentions.add.group"), step: "group" },
    { id: "user", kind: "user", label: t("mentions.add.user"), step: "user" },
    { id: "team", kind: "team", label: t("mentions.add.team"), step: "team" },
    { id: "teams-from-label", kind: "teams_from_label", label: t("mentions.add.teamsFromLabel") },
    {
      id: "on-call-of-team",
      kind: "on_call_of_team",
      label: t("mentions.add.onCallOfTeam"),
      step: "on_call_of_team",
    },
    {
      id: "on-call-of-teams-from-label",
      kind: "on_call_of_teams_from_label",
      label: t("mentions.add.onCallOfTeamsFromLabel"),
    },
  ];

  const finish = (target: MentionTarget) => {
    onAdd(target);
    setOpen(false);
    setStep(null);
  };

  const choose = (option: Option) => {
    if (option.step !== undefined) {
      setStep(option.step);
      return;
    }
    finish(
      option.kind === "everyone"
        ? { kind: "everyone", everyone: option.everyone }
        : { kind: option.kind },
    );
  };

  return (
    <div className="flex min-w-0 flex-col gap-2" data-testid="mention-add">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          variant="outline"
          disabled={disabled || full}
          aria-expanded={open}
          aria-controls={panel}
          onClick={() => {
            setOpen(!open);
            setStep(null);
          }}
        >
          <PlusIcon aria-hidden="true" />
          {t("mentions.add.button")}
        </Button>
        {full && (
          <span className="text-sm text-muted-foreground">
            {t("mentions.add.full", { limit: MAX_MENTION_ENTRIES })}
          </span>
        )}
      </div>
      {open && (
        <div
          id={panel}
          role="group"
          aria-label={t("mentions.add.button")}
          className="flex min-w-0 flex-col gap-3 rounded-lg border p-3"
          data-testid="mention-add-panel"
        >
          <ul className="grid min-w-0 gap-1.5 sm:grid-cols-2">
            {options.map((option) => {
              const reason = unavailableReason(abilities, {
                kind: option.kind,
                everyone: option.everyone,
              });
              const added =
                reason === null &&
                option.step === undefined &&
                present.has(
                  targetKey(
                    option.kind === "everyone"
                      ? { kind: "everyone", everyone: option.everyone }
                      : { kind: option.kind },
                  ),
                );
              const blocked = reason !== null || added;
              return (
                <li key={option.id} className="min-w-0">
                  <button
                    type="button"
                    aria-disabled={blocked}
                    data-testid={`mention-option-${option.id}`}
                    className={
                      blocked
                        ? "flex w-full min-w-0 cursor-not-allowed flex-col items-start rounded-md border border-dashed px-3 py-1.5 text-left text-sm text-muted-foreground"
                        : "flex w-full min-w-0 flex-col items-start rounded-md border px-3 py-1.5 text-left text-sm outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring aria-pressed:bg-muted"
                    }
                    aria-pressed={option.step !== undefined && step === option.step}
                    onClick={() => {
                      if (!blocked) {
                        choose(option);
                      }
                    }}
                  >
                    <span className="wrap-anywhere" data-testid="mention-option-label">
                      {option.label}
                    </span>
                    {reason !== null && (
                      <span
                        className="text-xs wrap-anywhere text-amber-700 dark:text-amber-300"
                        data-testid="mention-option-reason"
                      >
                        {reasonText(t, reason, type, option.everyone)}
                      </span>
                    )}
                    {added && (
                      <span className="text-xs" data-testid="mention-option-added">
                        {t("mentions.add.alreadyAdded")}
                      </span>
                    )}
                  </button>
                </li>
              );
            })}
          </ul>
          {step === "group" && (
            <GroupChoice
              id={id}
              type={type}
              connectionId={connectionId}
              onAdd={(group, groupId) =>
                finish(
                  groupId === undefined
                    ? { kind: "group", group }
                    : { kind: "group", group, group_id: groupId },
                )
              }
            />
          )}
          {step === "user" && (
            <PersonChoice
              id={id}
              onAdd={(user) =>
                finish({
                  kind: "user",
                  user_id: user.id,
                  user: { id: user.id, name: user.name, login: user.login, deactivated: false },
                })
              }
            />
          )}
          {(step === "team" || step === "on_call_of_team") && (
            <TeamChoice
              id={id}
              step={step}
              onAdd={(team) =>
                finish({
                  kind: step,
                  team_id: team.id,
                  team: { id: team.id, name: team.name, slug: team.slug, deleted: false },
                })
              }
            />
          )}
        </div>
      )}
    </div>
  );
}
