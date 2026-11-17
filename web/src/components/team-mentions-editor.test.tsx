// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "How to mention the Team" in a real browser (C-25.FR-8, C-25.AC-10): a field per Connection — the Mattermost group's
// name, the Slack user group picked from the Connection's groups, the bridge's group name — or the reason the
// Connection has none; a Team mention that lost its Connection's ability, greyed with the reason and removable; the hint;
// what a save sends; the refusals at their fields; the read-only page; and the Russian texts.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  Permission,
  Session,
  Team,
  TeamMention,
  TeamMentionConnection,
  TeamMentionInput,
} from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { team } from "../test/fixtures/teams";
import { TeamMentionsEditor } from "./team-mentions-editor";

function mention(
  connection: { id: string; name: string; type: "mattermost" | "slack" | "bridge" | "telegram" },
  group: string,
  extra: Partial<TeamMention> = {},
): TeamMention {
  return {
    connection_id: connection.id,
    connection,
    group,
    available: true,
    unavailable_reason: null,
    ...extra,
  };
}

const MM = { id: "CN0000000000MM", name: "mm-lab", type: "mattermost" } as const;
const SL = { id: "CN0000000000SL", name: "slack-lab", type: "slack" } as const;
const SL_OLD = { id: "CN0000000000SO", name: "slack-old", type: "slack" } as const;
const SL_NO = { id: "CN0000000000SN", name: "slack-noscope", type: "slack" } as const;
const TG = { id: "CN0000000000TG", name: "tg-lab", type: "telegram" } as const;
const BR = { id: "CN0000000000BR", name: "bridge-lab", type: "bridge" } as const;

function connectionOf(
  c: { id: string; name: string; type: "mattermost" | "slack" | "bridge" | "telegram" },
  extra: Partial<TeamMentionConnection> = {},
): TeamMentionConnection {
  return {
    connection: { id: c.id, name: c.name },
    type: c.type,
    available: true,
    unavailable_reason: null,
    team_mention: null,
    ...extra,
  };
}

const PAYMENTS: Team = team({
  id: "TM0000000000PA",
  name: "payments",
  slug: "payments",
  mention_connections: [
    connectionOf(BR, { available: false, unavailable_reason: "bridge_no_group_mentions" }),
    connectionOf(MM, { team_mention: mention(MM, "payments_team") }),
    connectionOf(SL),
    connectionOf(SL_NO, { available: false, unavailable_reason: "usergroups_read_missing" }),
    connectionOf(SL_OLD, {
      available: false,
      unavailable_reason: "usergroups_read_missing",
      team_mention: {
        ...mention(SL_OLD, "payments-team", {
          available: false,
          unavailable_reason: "usergroups_read_missing",
        }),
        group_id: "S0FAKEPAYTEAM",
      },
    }),
    connectionOf(TG, { available: false, unavailable_reason: "telegram_no_groups" }),
  ],
});

const WRITE: Permission[] = ["teams:read", "teams:write", "connections:write"];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function mockReads() {
  return vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith(`/api/v1/connections/${SL.id}/groups`)) {
      return Promise.resolve(
        json({
          items: [
            {
              id: "S0FAKEPAYTEAM",
              name: "payments-team",
              display_name: "Payments",
              member_count: 4,
            },
            { id: "S0FAKEOPS", name: "ops", display_name: "ops", member_count: null },
          ],
          truncated: false,
        }),
      );
    }
    if (url.startsWith(`/api/v1/connections/${MM.id}/groups`)) {
      return Promise.resolve(
        json({
          items: [{ id: "g1", name: "payments_team", display_name: "Payments", member_count: 2 }],
          truncated: false,
        }),
      );
    }
    return Promise.resolve(json({ title: "not mocked" }, 404));
  });
}

async function mount(
  save: (mentions: TeamMentionInput[]) => Promise<unknown>,
  options: { canWrite?: boolean; permissions?: Permission[]; team?: Team } = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the section reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-11-16T09:00:00Z",
      idle_expires_at: "2026-11-16T09:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions: options.permissions ?? WRITE,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <TeamMentionsEditor
          team={options.team ?? PAYMENTS}
          canWrite={options.canWrite ?? true}
          save={save}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

function refusal(pointer: string, code: string): ApiError {
  return new ApiError(
    422,
    {
      type: "https://muster-io.github.io/muster/problems/validation-failed",
      code: "validation_failed",
      errors: [{ pointer, code }],
    },
    undefined,
  );
}

const reason = (name: string) =>
  page
    .getByTestId("team-mention-field")
    .filter({ hasText: name })
    .getByTestId("team-mention-reason");

const field = (name: string) => page.getByTestId("team-mention-field").filter({ hasText: name });

beforeEach(async () => {
  await i18n.changeLanguage("en");
  mockReads();
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("How to mention the Team", () => {
  test("has a field per Connection, or the reason it has none, and the hint", async () => {
    await mount(() => Promise.resolve(undefined));
    await expect.element(field("mm-lab").getByRole("textbox")).toHaveValue("payments_team");
    await expect.element(field("slack-lab").getByRole("combobox")).toBeVisible();
    await expect
      .element(reason("slack-noscope"))
      .toHaveTextContent("Needs the scope usergroups:read");
    await expect
      .element(reason("bridge-lab"))
      .toHaveTextContent("The bridge declares no group mentions");
    await expect
      .element(reason("tg-lab"))
      .toHaveTextContent("Telegram cannot mention everyone or groups");
    await expect
      .element(page.getByTestId("team-mentions-hint"))
      .toHaveTextContent(
        "The entry 'Team' of a Destination's Mentions is this Team mention. Without one, nobody is mentioned, and the On-call is not mentioned instead.",
      );
  });

  test("saves the Mattermost group by name and the Slack user group with its id", async () => {
    const save = vi.fn((_mentions: TeamMentionInput[]) => Promise.resolve(undefined));
    await mount(save);
    await userEvent.fill(field("mm-lab").getByRole("textbox"), "@payments_ops");
    await userEvent.selectOptions(field("slack-lab").getByRole("combobox"), "S0FAKEOPS");
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    // The Team mention that lost its ability goes back unchanged, which the API keeps.
    expect(save.mock.calls[0]?.[0]).toEqual([
      { connection_id: MM.id, group: "payments_ops" },
      { connection_id: SL.id, group: "ops", group_id: "S0FAKEOPS" },
      { connection_id: SL_OLD.id, group: "payments-team", group_id: "S0FAKEPAYTEAM" },
    ]);
    await expect.element(page.getByTestId("team-mentions-status")).toHaveTextContent("Saved.");
  });

  test("shows a Team mention that lost its ability greyed with the reason, and removes it", async () => {
    const save = vi.fn((_mentions: TeamMentionInput[]) => Promise.resolve(undefined));
    await mount(save);
    const old = field("slack-old");
    await expect.element(old.getByTestId("team-mention-value")).toHaveTextContent("@payments-team");
    await expect.element(reason("slack-old")).toHaveTextContent("Needs the scope usergroups:read");
    await old.getByRole("button", { name: "Remove" }).click();
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toEqual([{ connection_id: MM.id, group: "payments_team" }]);
  });

  test("clears a Team mention by emptying its field, and refuses a group name the server would", async () => {
    const save = vi.fn((_mentions: TeamMentionInput[]) => Promise.resolve(undefined));
    await mount(save);
    await userEvent.fill(field("mm-lab").getByRole("textbox"), "bad name!");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(field("mm-lab").getByTestId("team-mention-error"))
      .toHaveTextContent(
        'A group name has letters, digits, ".", "_" and "-", and starts with a letter or a digit.',
      );
    expect(save).not.toHaveBeenCalled();
    await userEvent.fill(field("mm-lab").getByRole("textbox"), "");
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toEqual([
      { connection_id: SL_OLD.id, group: "payments-team", group_id: "S0FAKEPAYTEAM" },
    ]);
  });

  test("shows the refusal of the API at the Connection it names", async () => {
    await mount(() =>
      Promise.reject(refusal("/team_mentions/0/connection_id", "group_mentions_unsupported")),
    );
    await userEvent.fill(field("mm-lab").getByRole("textbox"), "other");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(field("mm-lab").getByTestId("team-mention-error"))
      .toHaveTextContent("This Connection cannot mention a group.");
  });

  test("sends back a Team mention of a Connection the fields do not list", async () => {
    const save = vi.fn((_mentions: TeamMentionInput[]) => Promise.resolve(undefined));
    const gone = { id: "CN0000000000GO", name: "gateway-only", type: "bridge" } as const;
    await mount(save, {
      team: { ...PAYMENTS, team_mentions: [mention(gone, "old-group")] },
    });
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toContainEqual({
      connection_id: gone.id,
      group: "old-group",
    });
  });

  test("only shows the Team mentions without teams:write", async () => {
    await mount(() => Promise.resolve(undefined), {
      canWrite: false,
      permissions: ["teams:read"],
    });
    await expect.element(field("mm-lab").getByRole("textbox")).toBeDisabled();
    await expect.element(page.getByRole("button", { name: "Save" })).not.toBeInTheDocument();
    await expect.element(page.getByTestId("team-mentions-hint")).toBeVisible();
  });

  test("has the Russian texts", async () => {
    await i18n.changeLanguage("ru");
    await mount(() => Promise.resolve(undefined));
    await expect.element(reason("slack-noscope")).toHaveTextContent("Нужно право usergroups:read");
    await expect
      .element(reason("bridge-lab"))
      .toHaveTextContent("Мост не объявил групповые упоминания");
    await expect
      .element(reason("tg-lab"))
      .toHaveTextContent("В Telegram нет упоминания всех и групп");
    await expect
      .element(page.getByTestId("team-mentions-hint"))
      .toHaveTextContent(
        "Строка «Команда» в упоминаниях места доставки — это упоминание команды для этого подключения. Если его нет, команду не упоминают и дежурного вместо неё не зовут.",
      );
  });
});
