// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// What each type of Destination offers for a new Mention entry (C-28.FR-2), the words of targets, events and reasons in
// English and Russian, what an entry resolves to now, and the conversions between the form's entries and the API's.

import { afterEach, describe, expect, test } from "vitest";

import type { MentionEntry, MentionTarget } from "../api/gen/model";
import i18n from "../i18n";
import { bridgeConnection, slackConnection } from "../test/fixtures/connections";
import {
  MENTION_EVENTS,
  entryIndexOf,
  entryInputs,
  entryValues,
  eventName,
  isEntriesPointer,
  isGroupName,
  mentionErrorText,
  reasonText,
  resolvesToText,
  targetKey,
  targetLabel,
  unavailableReason,
} from "./mention-entries";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

function entry(target: MentionTarget, resolves_to: MentionEntry["resolves_to"]): MentionEntry {
  return {
    target,
    events: [...MENTION_EVENTS],
    available: true,
    unavailable_reason: null,
    resolves_to,
  };
}

const TARGETS = [
  { kind: "everyone", everyone: "channel" },
  { kind: "everyone", everyone: "here" },
  { kind: "everyone", everyone: "all" },
  { kind: "everyone", everyone: "everyone" },
  { kind: "group" },
  { kind: "user" },
  { kind: "team" },
  { kind: "teams_from_label" },
  { kind: "on_call_of_team" },
  { kind: "on_call_of_teams_from_label" },
] as const;

function reasons(
  type: Parameters<typeof unavailableReason>[0]["type"],
  connection?: Parameters<typeof unavailableReason>[0]["connection"],
) {
  return TARGETS.map((target) => unavailableReason({ type, connection }, target));
}

function bridgeWith(capabilities: object) {
  return bridgeConnection({
    info: {
      protocol_version: "1",
      bridge: { name: "b", version: "1" },
      messenger: { name: "chat" },
      capabilities,
      limits: { max_body_length: 4000, edit_max_age_seconds: null },
      user_lookup: "login",
      event_delivery: ["poll"],
    },
  });
}

describe("what each type of Destination offers", () => {
  test("Mattermost offers everything but @everyone", () => {
    expect(reasons("mattermost")).toEqual([
      null,
      null,
      null,
      "messenger_has_no_token",
      null,
      null,
      null,
      null,
      null,
      null,
    ]);
  });

  test("Telegram offers people and the On-call only", () => {
    expect(reasons("telegram")).toEqual([
      "telegram_no_groups",
      "telegram_no_groups",
      "telegram_no_groups",
      "telegram_no_groups",
      "telegram_no_groups",
      null,
      "telegram_no_groups",
      "telegram_no_groups",
      null,
      null,
    ]);
  });

  test("Slack has no @all and no @everyone, and takes groups and Teams once its check found usergroups:read", () => {
    const without = slackConnection({ usergroups_read: false });
    const unchecked = slackConnection({ usergroups_read: null });
    const withScope = slackConnection({ usergroups_read: true });
    const lacking = [
      null,
      null,
      "messenger_has_no_token",
      "messenger_has_no_token",
      "usergroups_read_missing",
      null,
      "usergroups_read_missing",
      "usergroups_read_missing",
      null,
      null,
    ];
    expect(reasons("slack", without)).toEqual(lacking);
    expect(reasons("slack", unchecked)).toEqual(lacking);
    expect(reasons("slack", withScope)).toEqual([
      null,
      null,
      "messenger_has_no_token",
      "messenger_has_no_token",
      null,
      null,
      null,
      null,
      null,
      null,
    ]);
  });

  test("a Bridge offers what its info declares", () => {
    expect(reasons("bridge", bridgeWith({}))).toEqual([
      "bridge_not_declared",
      "bridge_not_declared",
      "bridge_not_declared",
      "bridge_not_declared",
      "bridge_no_group_mentions",
      null,
      "bridge_no_group_mentions",
      "bridge_no_group_mentions",
      null,
      null,
    ]);
    expect(
      reasons(
        "bridge",
        bridgeWith({ group_mentions: true, everyone_mentions: ["channel", "here"] }),
      ),
    ).toEqual([
      null,
      null,
      "bridge_not_declared",
      "bridge_not_declared",
      null,
      null,
      null,
      null,
      null,
      null,
    ]);
    expect(reasons("bridge", slackConnection())).toEqual(reasons("bridge"));
  });

  test("an outgoing webhook and a Connection not read yet offer everything the type allows", () => {
    expect(reasons("webhook")).toEqual(TARGETS.map(() => null));
    expect(reasons("bridge")).toEqual(TARGETS.map(() => null));
    expect(reasons("slack")).toEqual([
      null,
      null,
      "messenger_has_no_token",
      "messenger_has_no_token",
      null,
      null,
      null,
      null,
      null,
      null,
    ]);
  });
});

describe("the words", () => {
  test("name the reasons, in English and Russian", async () => {
    expect(reasonText(i18n.t, "telegram_no_groups", "telegram")).toBe(
      "Telegram cannot mention everyone or groups",
    );
    expect(reasonText(i18n.t, "usergroups_read_missing", "slack")).toBe(
      "Needs the scope usergroups:read",
    );
    expect(reasonText(i18n.t, "bridge_no_group_mentions", "bridge")).toBe(
      "The bridge declares no group mentions",
    );
    expect(reasonText(i18n.t, "bridge_not_declared", "bridge", "all")).toBe(
      "The bridge does not declare @all",
    );
    expect(reasonText(i18n.t, "messenger_has_no_token", "slack", "everyone")).toBe(
      "Slack has no @everyone",
    );
    await i18n.changeLanguage("ru");
    expect(reasonText(i18n.t, "telegram_no_groups", "telegram")).toBe(
      "В Telegram нет упоминания всех и групп",
    );
    expect(reasonText(i18n.t, "usergroups_read_missing", "slack")).toBe(
      "Нужно право usergroups:read",
    );
    expect(reasonText(i18n.t, "bridge_no_group_mentions", "bridge")).toBe(
      "Мост не объявил групповые упоминания",
    );
    expect(reasonText(i18n.t, "bridge_not_declared", "bridge", "all")).toBe("Мост не объявил @all");
    expect(reasonText(i18n.t, "messenger_has_no_token", "mattermost", "everyone")).toBe(
      "В Mattermost нет @everyone",
    );
  });

  test("name the seven events in the order of the grid", async () => {
    expect(MENTION_EVENTS.map((e) => eventName(i18n.t, e))).toEqual([
      "New Alert Group",
      "New Alerts",
      "Reopen",
      "Ack timeout",
      "End of Snooze",
      "Rise to critical",
      "Start of a Storm",
    ]);
    await i18n.changeLanguage("ru");
    expect(MENTION_EVENTS.map((e) => eventName(i18n.t, e))).toEqual([
      "Новая группа",
      "Новые алерты",
      "Переоткрытие",
      "Нет подтверждения",
      "Конец отложения",
      "Рост до critical",
      "Начало шторма",
    ]);
  });

  test("name the targets", () => {
    const team = { id: "TM0000000000PA", name: "payments", slug: "payments", deleted: false };
    const labels: [MentionTarget, string][] = [
      [{ kind: "everyone", everyone: "here" }, "@here"],
      [{ kind: "group", group: "payments_team" }, "Group @payments_team"],
      [
        {
          kind: "user",
          user_id: "US0000000000BB",
          user: { id: "US0000000000BB", name: "Bob", login: "bob", deactivated: false },
        },
        "@bob",
      ],
      [{ kind: "team", team_id: team.id, team }, "Team payments"],
      [{ kind: "teams_from_label" }, "Team from the label"],
      [{ kind: "on_call_of_team", team_id: team.id, team }, "On-call of payments"],
      [
        { kind: "on_call_of_team", team_id: team.id, team: { ...team, deleted: true } },
        "On-call of payments (deleted)",
      ],
      [{ kind: "on_call_of_teams_from_label" }, "On-call of the Team from the label"],
    ];
    for (const [target, expected] of labels) {
      expect(targetLabel(i18n.t, target), expected).toBe(expected);
    }
  });

  test("tell what an entry resolves to now", async () => {
    const payments = { id: "TM0000000000PA", name: "payments", slug: "payments", deleted: false };
    const devops = { id: "TM0000000000DV", name: "devops", slug: "devops", deleted: false };
    const search = { id: "TM0000000000SE", name: "search", slug: "search", deleted: false };
    const user = { kind: "user" as const, name: "ivan", linked: true };
    expect(
      resolvesToText(
        i18n.t,
        "en",
        entry({ kind: "on_call_of_team", team: payments }, [{ team: payments, mentions: [user] }]),
      ),
    ).toBe("@ivan");
    expect(
      resolvesToText(i18n.t, "en", entry({ kind: "team" }, [{ team: payments, mentions: [] }])),
    ).toBe("nobody");
    const fromLabel = entry({ kind: "teams_from_label" }, [
      { team: payments, mentions: [{ kind: "group", name: "payments_team", linked: true }] },
      { team: devops, mentions: [] },
      { team: search, mentions: [] },
    ]);
    expect(resolvesToText(i18n.t, "en", fromLabel)).toBe(
      "payments — @payments_team; devops and search — nobody",
    );
    await i18n.changeLanguage("ru");
    expect(resolvesToText(i18n.t, "ru", fromLabel)).toBe(
      "payments — @payments_team; devops и search — никого",
    );
    expect(resolvesToText(i18n.t, "ru", entry({ kind: "everyone", everyone: "here" }, []))).toBe(
      "никого",
    );
  });

  test("word the refusals of an entry", () => {
    expect(mentionErrorText(i18n.t, "mention_unsupported")).toBe(
      "This Connection cannot mention this target.",
    );
    expect(mentionErrorText(i18n.t, "duplicate")).toBe("This target is listed twice.");
    expect(mentionErrorText(i18n.t, "unknown_id")).toBe(
      "The chosen person or Team no longer exists.",
    );
    expect(mentionErrorText(i18n.t, "invalid_format")).toContain("A group name has letters");
    expect(mentionErrorText(i18n.t, "something_else")).toBe(i18n.t("fieldErrors.invalid"));
  });
});

describe("the entries of the form and of the API", () => {
  test("write each target with the fields of its kind only and the events in the order of the grid", () => {
    const team = { id: "TM0000000000PA", name: "payments", slug: "payments", deleted: false };
    const values = [
      {
        target: { kind: "everyone" as const, everyone: "here" as const },
        events: ["storm_started" as const, "reopen" as const],
      },
      {
        target: {
          kind: "user" as const,
          user_id: "US0000000000BB",
          user: { id: "US0000000000BB", name: "Bob", deactivated: false },
        },
        events: [...MENTION_EVENTS],
      },
      {
        target: { kind: "group" as const, group: "payments-team", group_id: "S0FAKEPAYTEAM" },
        events: ["reopen" as const],
      },
      { target: { kind: "group" as const, group: "payments_team" }, events: ["reopen" as const] },
      {
        target: { kind: "on_call_of_team" as const, team_id: team.id, team },
        events: ["reopen" as const],
      },
      { target: { kind: "teams_from_label" as const }, events: ["reopen" as const] },
    ];
    expect(entryInputs(values)).toEqual([
      { target: { kind: "everyone", everyone: "here" }, events: ["reopen", "storm_started"] },
      { target: { kind: "user", user_id: "US0000000000BB" }, events: [...MENTION_EVENTS] },
      {
        target: { kind: "group", group: "payments-team", group_id: "S0FAKEPAYTEAM" },
        events: ["reopen"],
      },
      { target: { kind: "group", group: "payments_team" }, events: ["reopen"] },
      { target: { kind: "on_call_of_team", team_id: team.id }, events: ["reopen"] },
      { target: { kind: "teams_from_label" }, events: ["reopen"] },
    ]);
    expect(entryValues(undefined)).toEqual([]);
  });

  test("tell one target from another", () => {
    expect(targetKey({ kind: "everyone", everyone: "here" })).not.toBe(
      targetKey({ kind: "everyone", everyone: "all" }),
    );
    expect(targetKey({ kind: "group", group: "a", group_id: "S1" })).not.toBe(
      targetKey({ kind: "group", group: "a" }),
    );
    expect(targetKey({ kind: "teams_from_label" })).toBe("teams_from_label");
  });

  test("find the entry a refusal's pointer names", () => {
    expect(entryIndexOf("/mention_entries/3/target/user_id")).toBe(3);
    expect(entryIndexOf("/mention_entries/12/events")).toBe(12);
    expect(entryIndexOf("/mention_entries")).toBeUndefined();
    expect(entryIndexOf("/mentions/reopen")).toBeUndefined();
    expect(isEntriesPointer("/mention_entries")).toBe(true);
    expect(isEntriesPointer("/mention_entries/0/target")).toBe(true);
    expect(isEntriesPointer("/mentions")).toBe(false);
  });

  test("check a group name as the server does", () => {
    expect(isGroupName("payments_team")).toBe(true);
    expect(isGroupName("a.b-c_d")).toBe(true);
    expect(isGroupName("_a")).toBe(false);
    expect(isGroupName("")).toBe(false);
    expect(isGroupName("a".repeat(65))).toBe(false);
  });
});
