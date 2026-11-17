// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Schedules" (docs/concepts/schedules.md), all in docs/assets/screenshots/concepts/schedules/. As a
// clock spec it runs alone, on a muster dev of its own, and moves the development clock first, to a Tuesday at 15:00 in
// Berlin in January, so that the weeks the images show have the same days in every run; the Tuesday is the first of
// the series of 52 weeks from 2027-01-12 that lies after the real now. It then makes, through the API as the Admin,
// scenario 1 of C-27: the Team "payments" with petr, sergey and olga and the Schedule "Main" with the Rotation layers
// "Around the clock" (weekly, handed over on Monday at 10:00 among petr, sergey and olga) and "Working hours 11–19"
// (Monday to Friday 11:00 to 19:00 among olga and petr), both starting on the Monday six weeks before, a whole turn of both cycles, with a Mattermost
// Destination whose people have no Account link; sergey is disabled, so that the second week has gaps and a warning.
// The Team "devops" with ivan, whose Schedule has him On-call, and the Team "search" without a Schedule complete the
// Home page. The Admin reads the Schedule page and the Home page, and olga, a Responder of the Team, gives her Shift of
// Wednesday 11:00 to 19:00 to petr in the Override form, which is shown before it is saved.

import { expect, test } from "@playwright/test";

import {
  adminApi,
  advance,
  Api,
  devClockOffset,
  ensureLocalUser,
  ensureTeam,
  onCallNames,
  removeOnCall,
  setTeamDestinations,
  signIn,
  signInAdmin,
  TEAM_SPEC_PASSWORD,
} from "../support";
import { capture } from "./capture";

const SCHEDULES = "concepts/schedules";
const ZONE = "Europe/Berlin";
const DAY = 24 * 60 * 60_000;
/** A Tuesday; the target is this day, or the same weekday 52 weeks later, as often as the real now needs. */
const ANCHOR = Date.UTC(2027, 0, 12);
const NAMES = onCallNames();

test.describe.configure({ mode: "serial" });

let scheduleId = "";

let at = 0;

/**
 * The Tuesday of the series that lies after now: the date six weeks before its Monday, where the rotations start, so
 * that the first week has no gap before the first handover and the people are those of the first week of both cycles,
 * and the instant of its 15:00 in Berlin, in winter time.
 */
function target(now: number): { start: string; at: number } {
  let day = ANCHOR;
  while (day + 15 * 3600_000 < now + DAY) {
    day += 364 * DAY;
  }
  return {
    start: new Date(day - DAY - 42 * DAY).toISOString().slice(0, 10),
    at: day + 14 * 3600_000,
  };
}

async function createUsers(admin: Api): Promise<Record<string, string>> {
  const ids: Record<string, string> = {};
  for (const login of ["petr", "sergey", "olga", "ivan"]) {
    ids[login] = await ensureLocalUser(admin, login, "responder");
    await admin.call("POST", `/api/v1/users/${ids[login]}/enable`);
  }
  return ids;
}

test.beforeAll(async () => {
  test.setTimeout(240_000);
  const now = Date.now() + (await devClockOffset()) * 1000;
  const goal = target(now);
  const start = goal.start;
  at = goal.at;
  await advance(Math.round((at - now) / 1000));

  const admin = await adminApi();
  try {
    await removeOnCall(admin);
    const users = await createUsers(admin);
    const me = await admin.call<{ user: { id: string } }>("GET", "/api/v1/sessions/current");
    const payments = await ensureTeam(admin, NAMES.payments, [
      users.petr ?? "",
      users.sergey ?? "",
      users.olga ?? "",
    ]);
    const devops = await ensureTeam(admin, NAMES.devops, [me.user.id, users.ivan ?? ""]);
    await ensureTeam(admin, NAMES.search);

    const connection = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
      type: "mattermost",
      name: NAMES.connection,
      server_url: "http://127.0.0.1:18065",
      bot_token: "mm-dev-token",
      proxy: { enabled: false },
      limiter: { limit: 50, per_seconds: 1 },
    });
    const nobody = { everyone: "none", user_ids: [], groups: [] };
    const destination = await admin.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "mattermost",
        name: NAMES.destination,
        connection_id: connection.id,
        team_id: "team-dev",
        channel_id: "ch-payments-notice",
        mentions: Object.fromEntries(
          [
            "new_alert_group",
            "new_alerts",
            "reopen",
            "ack_timeout",
            "snooze_ended",
            "rise_to_urgent",
          ].map((k) => [k, nobody]),
        ),
        limiter: { limit: 50, per_seconds: 1 },
      },
    );
    await setTeamDestinations(admin, NAMES.payments, [
      { destination_id: destination.destination.id },
    ]);

    const weekly = {
      shift_length: "week",
      handover_time: "10:00",
      handover_day: "monday",
      start_date: start,
    };
    const main = await admin.call<{ id: string }>("POST", "/api/v1/schedules", {
      team_id: payments,
      name: "Main",
      time_zone: ZONE,
      rotation_layers: [
        {
          name: "Around the clock",
          user_ids: [users.petr, users.sergey, users.olga],
          ...weekly,
          limits: null,
        },
        {
          name: "Working hours 11–19",
          user_ids: [users.olga, users.petr],
          ...weekly,
          limits: {
            days: ["monday", "tuesday", "wednesday", "thursday", "friday"],
            start: "11:00",
            end: "19:00",
          },
        },
      ],
    });
    scheduleId = main.id;
    await admin.call("POST", "/api/v1/schedules", {
      team_id: devops,
      name: "Main",
      time_zone: ZONE,
      rotation_layers: [{ name: "Weekly", user_ids: [users.ivan], ...weekly, limits: null }],
    });
    // The second week of the lower Rotation layer is sergey's: its nights and the weekend are gaps.
    await admin.call("POST", `/api/v1/users/${users.sergey}/disable`);
  } finally {
    await admin.dispose();
  }
});

test("shows the Schedule page, the Override form and On call now on the Home page", async ({
  page,
  browser,
}) => {
  test.setTimeout(240_000);

  // The Home page: who is On-call in each Team, with the mark for a person whom a Mention does not reach.
  await signInAdmin(page);
  await expect(page.getByTestId("on-call-now")).toBeVisible();
  const payments = page.locator('[data-testid="on-call-row"][data-team="payments"]');
  await expect(payments).toContainText("@olga");
  await expect(payments).toContainText("until Tue 19:00");
  await expect(payments.getByTestId("on-call-unreachable")).toBeVisible();
  await expect(page.locator('[data-testid="on-call-row"][data-team="search"]')).toContainText(
    "No Schedule",
  );
  await capture(page, SCHEDULES, "04-on-call-now", {
    locator: page.getByTestId("on-call-now"),
    padding: 16,
  });

  // The Schedule page: the two-week view with the row Result, a gap in red, and the Warnings.
  await page.goto(`/schedules/${scheduleId}`);
  await expect(
    page.getByRole("heading", { name: "Team payments · Schedule Main", level: 1 }),
  ).toBeVisible();
  await expect(page.getByTestId("schedule-now")).toBeVisible();
  await expect(page.getByTestId("schedule-warnings")).toContainText(
    "Disabled: @sergey in Around the clock",
  );
  await expect(
    page.locator('[data-testid="schedule-segment"][data-gap="true"]').first(),
  ).toBeVisible();
  await capture(page, SCHEDULES, "01-schedule-page");
  const view = page.getByTestId("schedule-view");
  await view.scrollIntoViewIfNeeded();
  await capture(page, SCHEDULES, "02-schedule-view", { locator: view, padding: 16 });

  // The Override form, as olga: she gives her Shift of Wednesday 11:00 to 19:00 to petr.
  const olga = await browser.newContext();
  const as = await olga.newPage();
  await signIn(as, "olga", TEAM_SPEC_PASSWORD);
  await as.goto(`/schedules/${scheduleId}`);
  await expect(
    as.getByRole("heading", { name: "Team payments · Schedule Main", level: 1 }),
  ).toBeVisible();
  await expect(as.getByTestId("schedule-segment").first()).toBeVisible();
  const wednesday = at + 20 * 3600_000;
  const segments = await as
    .locator('[data-testid="schedule-segment"]:not([data-gap])')
    .evaluateAll((els) =>
      els.map((el) => ({
        start: el.getAttribute("data-start") ?? "",
        end: el.getAttribute("data-end") ?? "",
      })),
    );
  const picked = segments.find((s) => new Date(s.start).getTime() === wednesday);
  expect(picked, `olga's Shift of Wednesday 11:00 among ${JSON.stringify(segments)}`).toBeDefined();
  await as.getByRole("button", { name: "Add Override" }).click();
  const form = as.getByTestId("override-form");
  await form.getByRole("radio", { name: "I give my Shift to…" }).check();
  await as.locator(`[data-testid="schedule-segment"][data-start="${picked?.start ?? ""}"]`).click();
  await form.getByLabel("Who takes it").selectOption({ label: "@petr" });
  await expect(form.getByTestId("override-hint")).toBeVisible();
  await capture(as, SCHEDULES, "03-override-form", { locator: form, padding: 16 });
  await olga.close();
});
