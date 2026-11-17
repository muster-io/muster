// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Mentions grid in a real browser (C-28.FR-1, FR-2, FR-3): one row per entry with its target and what it resolves
// to now, a tick box per Loud event, "Remove"; "Add" with every target and each unavailable one greyed with its reason;
// a new row ticked for every event; a stored entry whose Connection lost an ability, greyed with the reason, and the
// warning above the grid; the warnings of undelivered Mentions; the three notes; and the Russian texts.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  Permission,
  Session,
  DestinationType,
  MentionEntry,
  MentionTarget,
  MentionWarning,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { MENTION_EVENTS, type MentionEntryValue, entryValues } from "../lib/mention-entries";
import { bridgeConnection, slackConnection } from "../test/fixtures/connections";
import { reasonOf } from "../test/text";
import { mattermostDestination } from "../test/fixtures/destinations";
import { MentionEntriesEditor } from "./mention-entries-editor";
import { MentionWarnings } from "./mention-warnings";

const PAYMENTS = { id: "TM0000000000PA", name: "payments", slug: "payments", deleted: false };
const DEVOPS = { id: "TM0000000000DV", name: "devops", slug: "devops", deleted: false };
const SEARCH = { id: "TM0000000000SE", name: "search", slug: "search", deleted: false };
const IVAN = { id: "US0000000000IV", name: "Ivan", login: "ivan", deactivated: false };
const BOB = { id: "US0000000000BB", name: "Bob", login: "bob", deactivated: false };

const SLACK = slackConnection({ id: "CN0000000000SL", usergroups_read: false });

function entry(target: MentionTarget, extra: Partial<MentionEntry> = {}): MentionEntry {
  return {
    target,
    events: [...MENTION_EVENTS],
    available: true,
    unavailable_reason: null,
    resolves_to: [],
    ...extra,
  };
}

const STORED: MentionEntry[] = [
  entry(
    { kind: "user", user_id: IVAN.id, user: IVAN },
    { resolves_to: [{ team: null, mentions: [{ kind: "user", name: "ivan", linked: true }] }] },
  ),
  entry(
    { kind: "teams_from_label" },
    {
      resolves_to: [
        { team: PAYMENTS, mentions: [{ kind: "group", name: "payments_team", linked: true }] },
        { team: DEVOPS, mentions: [] },
        { team: SEARCH, mentions: [] },
      ],
      events: ["new_alert_group", "reopen"],
    },
  ),
];

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function mockReads(connection: unknown = SLACK) {
  return vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.includes("/groups")) {
      return Promise.resolve(
        json({
          items: [
            {
              id: "S0FAKEPAYTEAM",
              name: "payments-team",
              display_name: "Payments",
              member_count: 3,
            },
          ],
          truncated: false,
        }),
      );
    }
    if (url.startsWith("/api/v1/connections/")) {
      return Promise.resolve(json(connection));
    }
    if (url.startsWith("/api/v1/user-directory")) {
      return Promise.resolve(json({ items: [IVAN, BOB], next_cursor: null }));
    }
    if (url.startsWith("/api/v1/teams")) {
      return Promise.resolve(json({ items: [PAYMENTS, DEVOPS], next_cursor: null }));
    }
    return Promise.resolve(new Response("{}", { status: 404 }));
  });
}

function providers(children: ReactNode, permissions: Permission[] = ["connections:write"]) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the grid reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-11-16T09:00:00Z",
      idle_expires_at: "2026-11-16T09:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
}

function Harness({
  type = "mattermost",
  stored = STORED,
  connectionId = "CN0000000000MM",
  onChange,
  errors,
  warnings = [],
}: {
  type?: DestinationType;
  stored?: MentionEntry[];
  connectionId?: string;
  onChange?: (next: MentionEntryValue[]) => void;
  errors?: Record<number, string>;
  warnings?: MentionWarning[];
}) {
  const [value, setValue] = useState(() =>
    entryValues(mattermostDestination({ mention_entries: stored })),
  );
  return (
    <>
      <MentionWarnings type={type} warnings={warnings} entries={stored} />
      <MentionEntriesEditor
        id="m"
        type={type}
        connectionId={connectionId}
        value={value}
        stored={stored}
        errors={errors}
        onChange={(next) => {
          setValue(next);
          onChange?.(next);
        }}
      />
    </>
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
  mockReads();
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

const add = () => page.getByRole("button", { name: "Add", exact: true });

describe("the Mentions grid", () => {
  test("says that nobody is mentioned without entries, and has the three notes", async () => {
    await render(providers(<Harness stored={[]} />));
    await expect
      .element(page.getByTestId("mention-empty"))
      .toHaveTextContent("Nobody is mentioned: messages arrive without Mentions.");
    const notes = page.getByTestId("mention-note");
    await expect
      .element(notes.nth(0))
      .toHaveTextContent(
        "'Team' is the Team mention for this Connection, if it is set; otherwise nobody. The On-call is not mentioned instead.",
      );
    await expect
      .element(notes.nth(1))
      .toHaveTextContent(
        "A person without a linked account is not mentioned; the Alert Group's Timeline and this page say so.",
      );
    await expect
      .element(notes.nth(2))
      .toHaveTextContent(
        "Reminders and auto-unacknowledge always mention the Owner, and a Takeover the previous Owner.",
      );
  });

  test("shows a row per entry with what it resolves to now and a tick box per event", async () => {
    await render(providers(<Harness />));
    const rows = page.getByTestId("mention-entry");
    await expect
      .element(rows.nth(0).getByTestId("mention-entry-target"))
      .toHaveTextContent("@ivan");
    await expect
      .element(rows.nth(0).getByTestId("mention-entry-now"))
      .toHaveTextContent("now: @ivan");
    await expect
      .element(rows.nth(1).getByTestId("mention-entry-now"))
      .toHaveTextContent("now: payments — @payments_team; devops and search — nobody");
    for (const event of MENTION_EVENTS) {
      await expect.element(rows.nth(0).getByTestId(`mention-event-${event}`)).toBeChecked();
    }
    await expect.element(rows.nth(1).getByTestId("mention-event-new_alert_group")).toBeChecked();
    await expect.element(rows.nth(1).getByTestId("mention-event-ack_timeout")).not.toBeChecked();
    await expect
      .element(rows.nth(1).getByRole("button", { name: "Remove: Team from the label" }))
      .toBeVisible();
  });

  test("ticks and unticks events, and removes a row", async () => {
    const onChange = vi.fn();
    await render(providers(<Harness onChange={onChange} />));
    await userEvent.click(page.getByLabelText("Ack timeout: Team from the label", { exact: true }));
    expect(onChange.mock.lastCall?.[0][1].events).toEqual([
      "new_alert_group",
      "reopen",
      "ack_timeout",
    ]);
    await userEvent.click(page.getByLabelText("New Alert Group: @ivan", { exact: true }));
    expect(onChange.mock.lastCall?.[0][0].events).toEqual(MENTION_EVENTS.slice(1));
    await userEvent.click(page.getByRole("button", { name: "Remove: @ivan" }));
    expect(onChange.mock.lastCall?.[0]).toHaveLength(1);
    expect(page.getByTestId("mention-entry").elements()).toHaveLength(1);
  });

  test("adds @here as a row ticked for every event", async () => {
    const onChange = vi.fn();
    await render(providers(<Harness onChange={onChange} />));
    await userEvent.click(add());
    await userEvent.click(page.getByTestId("mention-option-everyone-here"));
    const added = onChange.mock.lastCall?.[0][2];
    expect(added.target).toEqual({ kind: "everyone", everyone: "here" });
    expect(added.events).toEqual([...MENTION_EVENTS]);
    const row = page.getByTestId("mention-entry").nth(2);
    await expect.element(row.getByTestId("mention-entry-target")).toHaveTextContent("@here");
    for (const event of MENTION_EVENTS) {
      await expect.element(row.getByTestId(`mention-event-${event}`)).toBeChecked();
    }
    // A target that can be named once is greyed once it is in the list.
    await userEvent.click(add());
    for (const id of ["everyone-here", "teams-from-label"]) {
      await expect
        .element(page.getByTestId(`mention-option-${id}`).getByTestId("mention-option-added"))
        .toHaveTextContent("Already in the list");
    }
  });

  test("offers every target, and greys what a Slack Connection without usergroups:read cannot mention", async () => {
    await render(providers(<Harness type="slack" connectionId={SLACK.id} stored={[]} />));
    await userEvent.click(add());
    await expect.element(page.getByTestId("mention-add-panel")).toBeVisible();
    expect(page.getByTestId("mention-add-panel").getByRole("button").elements()).toHaveLength(10);
    await expect
      .element(page.getByTestId("mention-option-group").getByTestId("mention-option-label"))
      .toHaveTextContent("Group…");
    await expect.element(reasonOf("everyone-all")).toHaveTextContent("Slack has no @all");
    await expect.element(reasonOf("team")).toHaveTextContent("Needs the scope usergroups:read");
    // A greyed target adds nothing.
    await page.getByTestId("mention-option-team").click({ force: true });
    await expect.element(page.getByTestId("mention-empty")).toBeVisible();
    // The labels are the ones of the story, in English and in Russian.
    for (const label of [
      "@channel",
      "@here",
      "@all",
      "@everyone",
      "Group…",
      "Person…",
      "Team…",
      "Team from the label",
      "On-call of a Team…",
      "On-call of the Team from the label",
    ]) {
      await expect
        .element(page.getByTestId("mention-add-panel").getByText(label, { exact: true }))
        .toBeVisible();
    }
    await i18n.changeLanguage("ru");
    await expect.element(reasonOf("team")).toHaveTextContent("Нужно право usergroups:read");
    for (const label of [
      "Группа…",
      "Человек…",
      "Команда…",
      "Команда из метки",
      "Дежурный команды…",
      "Дежурный команды из метки",
    ]) {
      await expect
        .element(page.getByTestId("mention-add-panel").getByText(label, { exact: true }))
        .toBeVisible();
    }
  });

  test("picks a person and a Team from the lists, and a user group of a Slack Connection with its id", async () => {
    const onChange = vi.fn();
    mockReads(slackConnection({ id: SLACK.id, usergroups_read: true }));
    await render(
      providers(<Harness type="slack" connectionId={SLACK.id} stored={[]} onChange={onChange} />),
    );
    await userEvent.click(add());
    await userEvent.click(page.getByTestId("mention-option-user"));
    await userEvent.selectOptions(page.getByLabelText("Person", { exact: true }), IVAN.id);
    expect(onChange.mock.lastCall?.[0][0].target).toMatchObject({ kind: "user", user_id: IVAN.id });

    await userEvent.click(add());
    await userEvent.click(page.getByTestId("mention-option-team"));
    await userEvent.selectOptions(page.getByLabelText("Team", { exact: true }), PAYMENTS.id);
    expect(onChange.mock.lastCall?.[0][1].target).toMatchObject({
      kind: "team",
      team_id: PAYMENTS.id,
    });
    await expect
      .element(page.getByTestId("mention-entry").nth(1).getByTestId("mention-entry-target"))
      .toHaveTextContent("Team payments");

    await userEvent.click(add());
    await userEvent.click(page.getByTestId("mention-option-on-call-of-team"));
    await userEvent.selectOptions(
      page.getByLabelText("On-call of the Team", { exact: true }),
      DEVOPS.id,
    );
    expect(onChange.mock.lastCall?.[0][2].target).toMatchObject({
      kind: "on_call_of_team",
      team_id: DEVOPS.id,
    });

    await userEvent.click(add());
    await userEvent.click(page.getByTestId("mention-option-group"));
    await userEvent.selectOptions(page.getByLabelText("Group", { exact: true }), "S0FAKEPAYTEAM");
    expect(onChange.mock.lastCall?.[0][3].target).toEqual({
      kind: "group",
      group: "payments-team",
      group_id: "S0FAKEPAYTEAM",
    });
  });

  test("never takes a Slack user group by a typed name: without connections:write it says what is needed", async () => {
    mockReads(slackConnection({ id: SLACK.id, usergroups_read: true }));
    await render(
      providers(<Harness type="slack" connectionId={SLACK.id} stored={[]} />, [
        "destinations:write",
      ]),
    );
    await userEvent.click(add());
    await userEvent.click(page.getByTestId("mention-option-group"));
    await expect
      .element(page.getByTestId("mention-groups-unlisted"))
      .toHaveTextContent("Choosing a group needs the permission to manage Connections.");
    await expect.element(page.getByLabelText("Group name")).not.toBeInTheDocument();
  });

  test("greys groups and Teams on a bridge that declares no group mentions", async () => {
    mockReads(bridgeConnection({ id: "CN0000000000BR" }));
    await render(providers(<Harness type="bridge" connectionId="CN0000000000BR" stored={[]} />));
    await userEvent.click(add());
    await expect
      .element(reasonOf("group"))
      .toHaveTextContent("The bridge declares no group mentions");
  });

  test("keeps a stored entry whose Connection lost an ability, greyed with the reason and a warning above the grid", async () => {
    const lost = entry(
      { kind: "group", group: "payments-team", group_id: "S0FAKEPAYTEAM" },
      { available: false, unavailable_reason: "usergroups_read_missing" },
    );
    await render(providers(<Harness type="slack" connectionId={SLACK.id} stored={[lost]} />));
    const row = page.getByTestId("mention-entry");
    await expect.element(row).toHaveAttribute("data-available", "false");
    await expect
      .element(row.getByTestId("mention-entry-unavailable"))
      .toHaveTextContent("Needs the scope usergroups:read");
    await expect.element(row.getByTestId("mention-entry-now")).not.toBeInTheDocument();
    await expect
      .element(page.getByTestId("mention-warning"))
      .toHaveTextContent("Group @payments-team mentions nobody: Needs the scope usergroups:read");
  });

  test("warns about undelivered Mentions above the grid, in English and Russian", async () => {
    const warnings: MentionWarning[] = [
      {
        user: BOB,
        entry: { position: 1, target: { kind: "on_call_of_teams_from_label" } },
        messenger: "mattermost",
        reason: "no_account_link",
        since: "2026-11-16T10:00:00Z",
      },
      {
        user: BOB,
        entry: { position: 0, target: { kind: "user", user_id: BOB.id, user: BOB } },
        messenger: "bridge",
        reason: "bridge_cannot_mention_people",
        since: "2026-11-16T10:00:00Z",
      },
    ];
    await render(providers(<Harness warnings={warnings} />));
    const shown = page.getByTestId("mention-warning");
    await expect
      .element(shown.nth(0))
      .toHaveTextContent(
        "Mention not delivered: @bob, On-call of the Team from the label, has no linked Mattermost account",
      );
    await expect
      .element(shown.nth(1))
      .toHaveTextContent("Mention not delivered: @bob, @bob, the bridge cannot mention people");
    await i18n.changeLanguage("ru");
    await expect
      .element(shown.nth(0))
      .toHaveTextContent(
        "Упоминание не доставлено: @bob, Дежурный команды из метки, нет привязки к Mattermost",
      );
    await expect
      .element(shown.nth(1))
      .toHaveTextContent("Упоминание не доставлено: @bob, @bob, мост не умеет упоминать людей");
  });

  test("shows the refusal of an entry at its row", async () => {
    await render(
      providers(<Harness errors={{ 1: "This Connection cannot mention this target." }} />),
    );
    await expect
      .element(page.getByTestId("mention-entry").nth(1).getByTestId("mention-entry-error"))
      .toHaveTextContent("This Connection cannot mention this target.");
  });

  test("has the Russian texts of the grid", async () => {
    await i18n.changeLanguage("ru");
    await render(providers(<Harness />));
    await expect
      .element(page.getByTestId("mention-entry").nth(0).getByTestId("mention-entry-now"))
      .toHaveTextContent("сейчас: @ivan");
    await expect
      .element(page.getByTestId("mention-entry").nth(1).getByTestId("mention-entry-now"))
      .toHaveTextContent("сейчас: payments — @payments_team; devops и search — никого");
    await expect.element(page.getByRole("button", { name: "Убрать: @ivan" })).toBeVisible();
    await expect
      .element(page.getByLabelText("Нет подтверждения: @ivan", { exact: true }))
      .toBeVisible();
    await expect
      .element(page.getByTestId("mention-note").nth(0))
      .toHaveTextContent(
        "«Команда» — это упоминание команды для этого подключения, если оно задано; иначе никого. Дежурного вместо команды не зовём.",
      );
  });
});
