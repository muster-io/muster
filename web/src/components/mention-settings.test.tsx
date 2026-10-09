// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Mention section of the Destination form in a real browser: one row per kind of Loud event, nobody by default;
// the chat-wide mentions the type allows (@channel, @all and @here for Mattermost, none for Telegram); users from the
// user directory and Mattermost groups, each a chip that removes it; a group name the server would refuse; the note
// about Reminders, auto-unacknowledge and Takeovers; and the Russian texts.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { MentionSettingEveryone, MentionSettings } from "../api/gen/model";
import i18n from "../i18n";
import {
  MENTION_KINDS,
  MentionSettingsField,
  defaultMentions,
  isGroupName,
  mentionKindName,
} from "./mention-settings";

const USERS = [
  { id: "US0000000000AA", name: "Alice", login: "alice", deactivated: false },
  { id: "US0000000000AB", name: "Bob", login: "bob", deactivated: false },
  { id: "US0000000000AC", name: "Carol", login: "carol", deactivated: true },
];

function Harness({
  everyone,
  groups,
  everyoneHint,
  onChange,
  initial = defaultMentions(),
  errors,
}: {
  everyone: readonly MentionSettingEveryone[];
  groups: boolean;
  everyoneHint?: string;
  onChange: (v: MentionSettings) => void;
  initial?: MentionSettings;
  errors?: Partial<Record<(typeof MENTION_KINDS)[number], string>>;
}) {
  const [value, setValue] = useState(initial);
  return (
    <MentionSettingsField
      id="m"
      value={value}
      everyone={everyone}
      groups={groups}
      everyoneHint={everyoneHint}
      errors={errors}
      onChange={(next) => {
        setValue(next);
        onChange(next);
      }}
    />
  );
}

async function renderSection(
  props: Omit<Parameters<typeof Harness>[0], "onChange"> = {
    everyone: ["channel", "all", "here"],
    groups: true,
  },
) {
  vi.spyOn(window, "fetch").mockImplementation(() =>
    Promise.resolve(
      new Response(JSON.stringify({ items: USERS, next_cursor: null }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
  const onChange = vi.fn((_v: MentionSettings) => {});
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Harness {...props} onChange={onChange} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return onChange;
}

/** The texts of the options of a native select. */
function optionTexts(select: { element: () => Element }): (string | null)[] {
  const element = select.element();
  return element instanceof HTMLSelectElement
    ? Array.from(element.options, (o) => o.textContent)
    : [];
}

function last(onChange: ReturnType<typeof vi.fn>): MentionSettings {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the harness passes MentionSettings
  return onChange.mock.calls.at(-1)?.[0] as MentionSettings;
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the Mention section", () => {
  test("has one row per kind of Loud event, nobody by default, and the note", async () => {
    await renderSection();
    expect(MENTION_KINDS.map((k) => mentionKindName(i18n.t, k))).toEqual([
      "New Alert Group",
      "New Alerts",
      "Reopen",
      "Ack timeout notice",
      "Snooze ended",
      "Rise to Urgent",
    ]);
    for (const kind of MENTION_KINDS) {
      await expect
        .element(page.getByLabelText(mentionKindName(i18n.t, kind), { exact: true }))
        .toHaveValue("none");
    }
    await expect
      .element(page.getByTestId("mention-note"))
      .toHaveTextContent(
        "Reminders and auto-unacknowledge always mention the Owner; a Takeover mentions the previous Owner.",
      );
  });

  test("offers @channel, @all and @here for Mattermost and sends the choice", async () => {
    const onChange = await renderSection();
    const select = page.getByLabelText("New Alerts", { exact: true });
    const options = optionTexts(select);
    expect(options).toEqual(["Nobody", "@channel", "@all", "@here"]);
    await expect
      .element(select)
      .toHaveAccessibleDescription(
        "Whom in the chat to mention as a whole: nobody, @channel, @all or @here. Chosen users and groups are mentioned as well.",
      );
    await userEvent.selectOptions(select, "@here");
    expect(last(onChange).new_alerts).toEqual({ everyone: "here", user_ids: [], groups: [] });
    expect(last(onChange).new_alert_group.everyone).toBe("none");
  });

  test("adds users from the directory, leaves out deleted ones, and removes a chip", async () => {
    const onChange = await renderSection();
    const users = page.getByLabelText("Add a user to mention for Reopen", { exact: true });
    await expect
      .element(page.getByRole("option", { name: "Alice (alice)" }).first())
      .toBeInTheDocument();
    const offered = optionTexts(users);
    expect(offered).toEqual(["Add a user…", "Alice (alice)", "Bob (bob)"]);
    await userEvent.selectOptions(users, "Bob (bob)");
    expect(last(onChange).reopen.user_ids).toEqual(["US0000000000AB"]);
    await page.getByRole("button", { name: "Remove Bob (bob)" }).click();
    expect(last(onChange).reopen.user_ids).toEqual([]);
  });

  test("adds a Mattermost group with Enter, without @, and refuses a name the server would", async () => {
    const onChange = await renderSection();
    const group = page.getByLabelText("Group to mention for Snooze ended", { exact: true });
    await userEvent.fill(group, "@oncall-db");
    await userEvent.keyboard("{Enter}");
    expect(last(onChange).snooze_ended.groups).toEqual(["oncall-db"]);
    await expect.element(page.getByRole("button", { name: "Remove @oncall-db" })).toBeVisible();
    await userEvent.fill(group, "on call!");
    await page.getByRole("button", { name: "Add group" }).nth(4).click();
    await expect.element(group).toHaveAttribute("aria-invalid", "true");
    await expect
      .element(page.getByText('A group name has letters, digits, ".", "_" and "-".'))
      .toBeVisible();
    expect(isGroupName("db.team_1")).toBe(true);
    expect(isGroupName("-db")).toBe(false);
  });

  test("offers only nobody and users for Telegram, which has no chat-wide mention and no groups", async () => {
    await renderSection({
      everyone: [],
      groups: false,
      everyoneHint: i18n.t("mentions.everyoneHintTelegram"),
    });
    await expect.element(page.getByLabelText("New Alert Group", { exact: true })).toBeDisabled();
    await expect
      .element(page.getByLabelText("New Alert Group", { exact: true }))
      .toHaveAccessibleDescription(
        "Telegram has no mention of the whole chat and no groups: only the chosen users are mentioned, through their linked Telegram accounts.",
      );
    await expect
      .element(page.getByLabelText("Group to mention for New Alert Group", { exact: true }))
      .not.toBeInTheDocument();
    await expect
      .element(page.getByLabelText("Add a user to mention for New Alert Group", { exact: true }))
      .toBeVisible();
  });

  test("shows a refusal at its kind", async () => {
    await renderSection({
      everyone: ["channel", "all", "here"],
      groups: true,
      errors: { rise_to_urgent: "A chosen user no longer exists." },
    });
    await expect
      .element(
        page
          .getByTestId("mention-kind")
          .nth(5)
          .getByText("A chosen user no longer exists.", { exact: true }),
      )
      .toBeVisible();
    await expect
      .element(page.getByLabelText("Rise to Urgent", { exact: true }))
      .toHaveAttribute("aria-invalid", "true");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderSection();
    await expect.element(page.getByLabelText("Переход в срочные", { exact: true })).toBeVisible();
    await expect
      .element(page.getByTestId("mention-note"))
      .toHaveTextContent(
        "Напоминания и автоматическая отмена подтверждения всегда упоминают владельца, а перехват — прежнего владельца.",
      );
    const options = optionTexts(page.getByLabelText("Новые алерты", { exact: true }));
    expect(options[0]).toBe("Никого");
  });
});
