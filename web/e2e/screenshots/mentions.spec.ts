// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Whom a message mentions" (docs/concepts/mentions.md), all in
// docs/assets/screenshots/concepts/mentions/. The spec sets up scenario 4 of C-28 through the API: the Teams "payments",
// "search" and "devops" with their Schedules, the Mattermost Connection with the Team mention payments_team for
// payments, the Destination "payments-notice" with the entries "On-call of the Team from the label" and `@here` for
// three events, and an Alert Group whose Loud message could not mention petr, who has no Mattermost account link. It
// then adds a Telegram Connection and its Destination "alerts". The Admin reads the Mentions of the Mattermost
// Destination with its warning, the list of targets that the Telegram Destination offers, and the section "How to
// mention the Team" of payments. It deletes what it created.

import { expect, test } from "@playwright/test";

import {
  adminApi,
  ftg,
  removeConnection,
  removeMentions,
  seedMentions,
  setMentionEntries,
  signInAdmin,
  TELEGRAM_BOT_API,
  TG_SECOND_TOKEN,
} from "../support";
import { capture } from "./capture";

const MENTIONS = "concepts/mentions";
const PREFIX = "";
const TELEGRAM = "Telegram";
/** The fake's channel with comments enabled, its discussion group, and the second bot. */
const CHANNEL_ID = -1001000000001;
const GROUP_ID = -1001000000002;
const SECOND_BOT_ID = 234567;

test.describe.configure({ mode: "serial" });

async function cleanUp(): Promise<void> {
  const admin = await adminApi();
  try {
    await removeConnection(admin, TELEGRAM);
    await removeMentions(admin, PREFIX);
  } finally {
    await admin.dispose();
  }
}

/** The channel with comments enabled and the second bot an admin of the channel and of its discussion group. */
async function prepareChannel(): Promise<void> {
  await ftg("PUT", `/chats/${CHANNEL_ID}`, { linked_chat_id: GROUP_ID });
  for (const chat of [CHANNEL_ID, GROUP_ID]) {
    await ftg("PUT", `/chats/${chat}/members/${SECOND_BOT_ID}`, {
      status: "administrator",
      can_post_messages: true,
      can_edit_messages: true,
    });
  }
}

let mattermostDestination = "";
let telegramDestination = "";
let paymentsTeam = "";

test.beforeAll(async () => {
  test.setTimeout(240_000);
  await cleanUp();
  await prepareChannel();
  const seeded = await seedMentions(PREFIX);
  mattermostDestination = seeded.destinationId;
  paymentsTeam = seeded.teams.payments;
  const admin = await adminApi();
  try {
    // The entries of the example "infra-critical": the On-call of the Team from the label on every Loud event, and
    // `@here` only for a new Alert Group, a rise to critical and the start of a Storm.
    await setMentionEntries(admin, mattermostDestination, [
      { target: { kind: "on_call_of_teams_from_label" } },
      {
        target: { kind: "everyone", everyone: "here" },
        events: ["new_alert_group", "rise_to_urgent", "storm_started"],
      },
    ]);
    const connection = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
      type: "telegram",
      name: TELEGRAM,
      bot_token: TG_SECOND_TOKEN,
      bot_api_base_url: TELEGRAM_BOT_API,
      update_mode: "long_polling",
      proxy: { enabled: false },
      limiter: { limit: 20, per_seconds: 1 },
    });
    const created = await admin.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "telegram",
        name: "alerts",
        connection_id: connection.id,
        channel_id: "@muster_alerts",
        limiter: { limit: 20, per_seconds: 1 },
      },
    );
    telegramDestination = created.destination.id;
  } finally {
    await admin.dispose();
  }
});

test.afterAll(cleanUp);

test("shows the Mentions of a Destination, the targets of Telegram and the Team mention", async ({
  page,
}) => {
  test.setTimeout(180_000);
  await signInAdmin(page);

  // The Mattermost Destination: the entries with their Loud events, and the warning about petr.
  await page.goto(`/destinations/${mattermostDestination}`);
  const section = page.getByTestId("mention-settings");
  await expect(section.getByTestId("mention-entry")).toHaveCount(2);
  await expect(section.getByTestId("mention-warning")).toHaveText(
    "Mention not delivered: @petr, On-call of the Team from the label, has no linked Mattermost account",
  );
  // The header of the columns is hidden for the shot: it sits a few pixels off the boxes of the rows, and a reader would
  // read the boxes under the wrong events. The page names the events in the order of the boxes.
  await capture(page, MENTIONS, "01-mentions-grid", {
    locator: section,
    padding: 16,
    hide: section.getByTestId("mention-header"),
  });

  // The Telegram Destination: the list that "Add" opens, with what Telegram cannot mention greyed and its reason.
  await page.goto(`/destinations/${telegramDestination}`);
  const telegram = page.getByTestId("mention-settings");
  await expect(telegram.getByTestId("mention-empty")).toBeVisible();
  await telegram.getByRole("button", { name: "Add", exact: true }).click();
  await expect(
    telegram.getByTestId("mention-option-group").getByTestId("mention-option-reason"),
  ).toHaveText("Telegram cannot mention everyone or groups");
  await capture(page, MENTIONS, "02-telegram-mentions", {
    locator: telegram.getByTestId("mention-add"),
    padding: 4,
  });

  // The Team page of payments: the Mattermost Connection with its group, and the Connections that cannot mention a group, each with its reason.
  await page.goto(`/teams/${paymentsTeam}`);
  const team = page.getByTestId("team-mentions-section");
  await expect(team.getByTestId("team-mention-reason").first()).toBeVisible();
  await capture(page, MENTIONS, "03-team-mentions", { locator: team, padding: 16 });
});
