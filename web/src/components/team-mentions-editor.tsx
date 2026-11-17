// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "How to mention the Team" on the Team page (C-25.FR-8, C-25.AC-10): one field per Connection that may have a Destination
// — the name of the Mattermost group, the Slack user group picked from the Connection's groups, the group name of a
// bridge — or the reason the Connection has none ("Needs the scope usergroups:read", "The bridge declares no group
// mentions", "Telegram cannot mention everyone or groups"). A Team mention whose Connection lost the ability stays, greyed
// with the reason, and is sent back unchanged, which the API keeps; it can be removed. The entry "Team" of a Destination's
// Mentions is this Team mention: without one nobody is mentioned, and the On-call is not mentioned instead. Saving does
// not call the messenger; without teams:write the section only shows the Team mentions.

import { useMutation } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { useListConnectionGroups } from "../api/gen/endpoints/connections/connections";
import type { Team, TeamMentionConnection, TeamMentionInput } from "../api/gen/model";
import { isApiError, isStale, problemText } from "../lib/api";
import { isGroupName, mentionErrorText, messengerName, reasonText } from "../lib/mention-entries";
import { useCan } from "./app-shell";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The group a Team is mentioned by through one Connection, as the field holds it. */
interface Value {
  group: string;
  group_id?: string;
}

type Values = Record<string, Value>;

function valuesOf(team: Team): Values {
  const values: Values = {};
  for (const mc of team.mention_connections) {
    const m = mc.team_mention;
    if (m !== null) {
      values[mc.connection.id] =
        m.group_id === undefined ? { group: m.group } : { group: m.group, group_id: m.group_id };
    }
  }
  return values;
}

function sameValues(a: Values, b: Values): boolean {
  const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
  return [...keys].every(
    (k) => a[k]?.group === b[k]?.group && (a[k]?.group_id ?? "") === (b[k]?.group_id ?? ""),
  );
}

/** The request: the Team mentions of the Connections that have one, in the order of the fields. */
function inputsOf(team: Team, values: Values): TeamMentionInput[] {
  const out: TeamMentionInput[] = [];
  for (const mc of team.mention_connections) {
    const v = values[mc.connection.id];
    if (v !== undefined && v.group !== "") {
      out.push(
        v.group_id === undefined
          ? { connection_id: mc.connection.id, group: v.group }
          : { connection_id: mc.connection.id, group: v.group, group_id: v.group_id },
      );
    }
  }
  // A Team mention of a Connection the fields do not list stays as it is: the request replaces them as a whole.
  const listed = new Set(team.mention_connections.map((mc) => mc.connection.id));
  for (const m of team.team_mentions) {
    if (!listed.has(m.connection_id)) {
      out.push(
        m.group_id === undefined
          ? { connection_id: m.connection_id, group: m.group }
          : { connection_id: m.connection_id, group: m.group, group_id: m.group_id },
      );
    }
  }
  return out;
}

/** The checks the server repeats, as a code by Connection id. */
function checksOf(values: Values): Map<string, string> {
  const found = new Map<string, string>();
  for (const [id, v] of Object.entries(values)) {
    if (v.group !== "" && !isGroupName(v.group)) {
      found.set(id, "invalid_format");
    }
  }
  return found;
}

/** The codes the API refused with, by the Connection id of the Team mention they were sent at. */
function refusalsOf(err: unknown, sent: readonly TeamMentionInput[]): Map<string, string> {
  const found = new Map<string, string>();
  if (!isApiError(err)) {
    return found;
  }
  for (const item of err.errors ?? []) {
    const match = /^\/team_mentions\/(\d+)(?:\/|$)/.exec(item.pointer);
    const id = match?.[1] === undefined ? undefined : sent[Number(match[1])]?.connection_id;
    if (id !== undefined && !found.has(id)) {
      found.set(id, item.code);
    }
  }
  return found;
}

function refusalText(t: ReturnType<typeof useTranslation>["t"], code: string): string {
  return code === "group_mentions_unsupported"
    ? t("teams.mentions.errors.unsupported")
    : mentionErrorText(t, code);
}

function ConnectionField({
  mc,
  value,
  onChange,
  error,
  disabled,
}: {
  mc: TeamMentionConnection;
  value: Value | undefined;
  onChange: (next: Value | undefined) => void;
  error?: string;
  disabled: boolean;
}) {
  const { t } = useTranslation();
  const id = useId();
  const canList = useCan("connections:write");
  const stored = mc.team_mention;
  const lost = stored !== null && !stored.available;
  const usable = mc.available && !lost;
  const groups = useListConnectionGroups(mc.connection.id, undefined, {
    query: { enabled: usable && canList && !disabled && mc.type === "slack", retry: false },
  });
  const items = groups.data?.items ?? [];
  const reason = lost ? stored.unavailable_reason : mc.unavailable_reason;
  const describedBy = error === undefined ? undefined : `${id}-error`;
  let control;
  if (!usable) {
    control = (
      <div className="flex min-w-0 flex-col gap-1 text-sm" data-testid="team-mention-unavailable">
        {value !== undefined && value.group !== "" && (
          <span
            className="font-mono wrap-anywhere text-muted-foreground"
            data-testid="team-mention-value"
          >
            @{value.group}
          </span>
        )}
        {reason !== null && (
          <span
            className="wrap-anywhere text-amber-700 dark:text-amber-300"
            data-testid="team-mention-reason"
          >
            {reasonText(t, reason, mc.type)}
          </span>
        )}
        {lost && !disabled && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit"
            onClick={() => onChange(undefined)}
          >
            {t("teams.mentions.remove")}
          </Button>
        )}
      </div>
    );
  } else if (mc.type === "slack") {
    const listed = items.some((g) => g.id === value?.group_id);
    control = (
      <div className="flex min-w-0 flex-col gap-1">
        <NativeSelect
          id={id}
          className="w-full sm:w-80"
          value={value?.group_id ?? ""}
          disabled={disabled || !canList}
          aria-invalid={error !== undefined}
          aria-describedby={describedBy}
          onChange={(e) => {
            const group = items.find((g) => g.id === e.target.value);
            onChange(group === undefined ? undefined : { group: group.name, group_id: group.id });
          }}
        >
          <NativeSelectOption value="">
            {groups.isPending && canList ? t("common.loading") : t("teams.mentions.noMention")}
          </NativeSelectOption>
          {value?.group_id !== undefined && !listed && (
            <NativeSelectOption value={value.group_id}>@{value.group}</NativeSelectOption>
          )}
          {items.map((g) => (
            <NativeSelectOption key={g.id} value={g.id}>
              {g.display_name === "" || g.display_name === g.name
                ? `@${g.name}`
                : `${g.display_name} (@${g.name})`}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        {!canList && !disabled && (
          <span className="text-sm text-muted-foreground">
            {t("teams.mentions.needsConnectionsWrite")}
          </span>
        )}
        {groups.isError && (
          <span className="text-sm text-muted-foreground">
            {t("teams.mentions.groupsNotListed")}
          </span>
        )}
      </div>
    );
  } else {
    control = (
      <Input
        id={id}
        value={value?.group ?? ""}
        autoComplete="off"
        spellCheck={false}
        disabled={disabled}
        className="w-full sm:w-80"
        placeholder={t("teams.mentions.placeholder")}
        aria-invalid={error !== undefined}
        aria-describedby={describedBy}
        onChange={(e) => {
          const text = e.target.value.trim().replace(/^@/, "");
          onChange(text === "" ? undefined : { group: text });
        }}
      />
    );
  }
  return (
    <div className="flex min-w-0 flex-col gap-1.5" data-testid="team-mention-field">
      <Label htmlFor={usable ? id : undefined}>
        <span className="wrap-anywhere">{mc.connection.name}</span>
        <span className="font-normal text-muted-foreground"> · {messengerName(t, mc.type)}</span>
      </Label>
      {control}
      {error !== undefined && (
        <p id={`${id}-error`} className="text-sm text-destructive" data-testid="team-mention-error">
          {error}
        </p>
      )}
    </div>
  );
}

export interface TeamMentionsEditorProps {
  /** The Team as the page was read, or as it saved last. */
  team: Team;
  canWrite: boolean;
  /** Sends the Team mentions, replacing them as a whole. */
  save: (mentions: TeamMentionInput[]) => Promise<unknown>;
}

export function TeamMentionsEditor({ team, canWrite, save }: TeamMentionsEditorProps) {
  const { t } = useTranslation();
  const [initial, setInitial] = useState(() => valuesOf(team));
  const [values, setValues] = useState(initial);
  const [saved, setSaved] = useState(false);
  const [checked, setChecked] = useState<Map<string, string>>(new Map());
  const submit = useMutation({ mutationFn: (mentions: TeamMentionInput[]) => save(mentions) });
  const dirty = !sameValues(values, initial);
  const sent = submit.variables ?? [];
  const refused = refusalsOf(submit.error, sent);
  const shown = refused.size > 0;
  const code = (id: string) => checked.get(id) ?? refused.get(id);

  const change = (id: string, next: Value | undefined) => {
    setValues((current) => {
      const copy = { ...current };
      if (next === undefined) {
        delete copy[id];
      } else {
        copy[id] = next;
      }
      return copy;
    });
    setChecked((current) => {
      if (!current.has(id)) {
        return current;
      }
      const copy = new Map(current);
      copy.delete(id);
      return copy;
    });
    setSaved(false);
    submit.reset();
  };

  const body = (
    <>
      {team.mention_connections.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="team-mentions-empty">
          {t("teams.mentions.noConnections")}
        </p>
      ) : (
        <div className="flex min-w-0 flex-col gap-4">
          {team.mention_connections.map((mc) => {
            const problem = code(mc.connection.id);
            return (
              <ConnectionField
                key={mc.connection.id}
                mc={mc}
                value={values[mc.connection.id]}
                onChange={(next) => change(mc.connection.id, next)}
                error={problem === undefined ? undefined : refusalText(t, problem)}
                disabled={!canWrite}
              />
            );
          })}
        </div>
      )}
      <p className="text-sm text-muted-foreground" data-testid="team-mentions-hint">
        {t("teams.mentions.hint")}
      </p>
    </>
  );
  if (!canWrite) {
    return <div className="flex min-w-0 flex-col gap-3">{body}</div>;
  }
  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        setSaved(false);
        const found = checksOf(values);
        setChecked(found);
        if (found.size > 0) {
          return;
        }
        submit.mutate(inputsOf(team, values), {
          onSuccess: () => {
            setInitial(values);
            setSaved(true);
          },
        });
      }}
    >
      {body}
      {submit.isError && !isStale(submit.error) && !shown && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, submit.error)}
          </AlertDescription>
        </Alert>
      )}
      <div className="flex items-center gap-3">
        <Button type="submit" disabled={submit.isPending || team.mention_connections.length === 0}>
          {t("common.save")}
        </Button>
        <span
          role="status"
          className="text-sm text-muted-foreground"
          data-testid="team-mentions-status"
        >
          {saved && !dirty ? t("common.saved") : ""}
        </span>
      </div>
    </form>
  );
}
