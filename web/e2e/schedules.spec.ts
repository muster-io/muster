// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Schedule pages against `muster dev` (Verification of the Schedule page story): the Schedules list, reached from
// the Teams page, with the filter "Team" and "My Teams"; the page of the Schedule "Main" of the Team "payments" with
// two Rotation layers — "Around the clock", weekly Shifts handed over on Monday at 10:00 among three people, and
// "Working hours 11-19" above it, Monday to Friday 11:00 to 19:00 — in which one person is disabled, so that the second
// week of its rotation has gaps and hatched Shifts: its header, "On call now" with the unreachable mark, "Warnings",
// the two-week view with the row "Result" and the mark for now; an Override of exactly a segment of the row "Result"
// made as a Responder of the Team in the mode "I give my Shift to…", refused for a disabled person and changed and
// deleted; "Rotation layers" without controls for a Responder and with them for an Admin; "New Schedule"; and the
// page in Russian at 360 px. The rotation starts at the Monday 10:00 before the business clock's now, so the specs
// need no move of the development clock; the page is compared with what the API says about the same moment.

import { type Locator, type Page, expect, test } from "@playwright/test";

import {
  type Api,
  TEAM_SPEC_PASSWORD,
  adminApi,
  confirmContact,
  devClockOffset,
  ensureGateway,
  ensureLocalUser,
  ensureTeam,
  expectNoHorizontalScroll,
  removeConnection,
  removeOnCall,
  seedOnCall,
  removeTeamsBySlug,
  setTeamDestinations,
  shot,
  signIn,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const ZONE = "Europe/Berlin";
const SLUG = "payments";
const PETR = "sch-petr";
const SERGEY = "sch-sergey";
const OLGA = "sch-olga";
const ALICE = "sch-alice";
const EVE = "sch-eve";
const RU_ADMIN = "sch-ru";
const CONNECTION = "sch-mm";
const DESTINATION = "sch-notice";
const DOCS_ORIGIN = "https://muster-io.github.io";
const DOCS_PATH = "/muster/latest/";
const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

const ids = { team: "", main: "", users: new Map<string, string>() };

interface Named {
  id: string;
  name: string;
}

interface Segment {
  start: string;
  end: string;
  user: { login: string } | null;
  source: { kind: string; rotation_layer?: Named } | null;
}

interface OnCall {
  from: string;
  to: string;
  segments: Segment[];
}

/** The wall clock of an instant in the Schedule's time zone. */
function wall(at: Date): { day: string; hour: number; minute: number; weekday: string } {
  const p = Object.fromEntries(
    new Intl.DateTimeFormat("en-GB", {
      timeZone: ZONE,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
      weekday: "short",
    })
      .formatToParts(at)
      .map((x) => [x.type, x.value]),
  );
  return {
    day: `${p.year}-${p.month}-${p.day}`,
    hour: Number(p.hour),
    minute: Number(p.minute),
    weekday: String(p.weekday),
  };
}

function shiftDay(day: string, days: number): string {
  const [y = 1970, m = 1, d = 1] = day.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d + days)).toISOString().slice(0, 10);
}

const clock = (at: Date) =>
  new Intl.DateTimeFormat("en-GB", {
    timeZone: ZONE,
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(at);

const stamp = (at: Date) =>
  `${new Intl.DateTimeFormat("en-US", { timeZone: ZONE, weekday: "short" }).format(at)} ${clock(at)}`;

/** The time alone on the day of the reference, else the weekday with it. */
const stampNear = (at: Date, reference: Date) =>
  wall(at).day === wall(reference).day ? clock(at) : stamp(at);

/** "from 11:00 to 19:00" of the hint of the Override form: times alone within a day, weekdays across midnight. */
function hintTimes(start: Date, end: Date): { from: string; to: string } {
  return wall(start).day === wall(end).day
    ? { from: clock(start), to: clock(end) }
    : { from: stamp(start), to: stamp(end) };
}

/** The Monday at 10:00 in the zone that the business clock's now has passed: where the rotation starts. */
async function rotationStart(): Promise<string> {
  const now = new Date(Date.now() + (await devClockOffset()) * 1000);
  const w = wall(now);
  let back = WEEKDAYS.indexOf(w.weekday);
  if (back === 0 && w.hour < 10) {
    back = 7;
  }
  return shiftDay(w.day, -back);
}

async function schedulesOf(admin: Api): Promise<Named[]> {
  const list = await admin.call<{ items: Named[] }>("GET", `/api/v1/schedules?team=${ids.team}`);
  return list.items;
}

/** Deletes the Schedules of the Team, the Team, the Destination and the Connection, whatever an earlier run left. */
async function cleanUp(admin: Api): Promise<void> {
  const teams = await admin.call<{ items: { id: string; slug: string }[] }>(
    "GET",
    "/api/v1/teams?limit=500",
  );
  for (const team of teams.items.filter((t) => t.slug === SLUG)) {
    const schedules = await admin.call<{ items: Named[] }>(
      "GET",
      `/api/v1/schedules?team=${team.id}`,
    );
    for (const s of schedules.items) {
      await admin.call("DELETE", `/api/v1/schedules/${s.id}`);
    }
  }
  await removeTeamsBySlug(admin, [SLUG]);
  await removeConnection(admin, CONNECTION);
}

test.beforeAll(async () => {
  test.setTimeout(120_000);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    for (const login of [PETR, SERGEY, OLGA, ALICE]) {
      const id = await ensureLocalUser(admin, login, "responder");
      ids.users.set(login, id);
      await admin.call("POST", `/api/v1/users/${id}/enable`);
    }
    ids.users.set(EVE, await ensureLocalUser(admin, EVE, "responder"));
    ids.users.set(RU_ADMIN, await ensureLocalUser(admin, RU_ADMIN, "admin"));
    // sch-eve is a Responder who is in no Team.
    ids.team = await ensureTeam(
      admin,
      SLUG,
      [...ids.users.entries()].filter(([login]) => login !== EVE).map(([, id]) => id),
    );
    const connection = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
      type: "mattermost",
      name: CONNECTION,
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
        name: DESTINATION,
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
    await setTeamDestinations(admin, SLUG, [{ destination_id: destination.destination.id }]);
    const start = await rotationStart();
    const user = (login: string) => ids.users.get(login) ?? "";
    const main = await admin.call<{ id: string }>("POST", "/api/v1/schedules", {
      team_id: ids.team,
      name: "Main",
      time_zone: ZONE,
      rotation_layers: [
        {
          name: "Around the clock",
          user_ids: [user(PETR), user(SERGEY), user(OLGA)],
          shift_length: "week",
          handover_time: "10:00",
          handover_day: "monday",
          start_date: start,
          limits: null,
        },
        {
          name: "Working hours 11-19",
          user_ids: [user(OLGA), user(PETR)],
          shift_length: "week",
          handover_time: "10:00",
          handover_day: "monday",
          start_date: start,
          limits: {
            days: ["monday", "tuesday", "wednesday", "thursday", "friday"],
            start: "11:00",
            end: "19:00",
          },
        },
      ],
    });
    ids.main = main.id;
    // The second week of the rotation is the disabled person's: its nights are gaps, and its Shifts are hatched.
    await admin.call("POST", `/api/v1/users/${user(SERGEY)}/disable`);
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    for (const login of [PETR, SERGEY, OLGA, ALICE, EVE, RU_ADMIN]) {
      const id = ids.users.get(login);
      if (id !== undefined) {
        await admin.context.fetch(`/api/v1/users/${id}`, { method: "DELETE" });
      }
    }
  } finally {
    await admin.dispose();
  }
});

async function onCallNow(): Promise<OnCall> {
  const admin = await adminApi();
  try {
    return await admin.call<OnCall>("GET", `/api/v1/schedules/${ids.main}/on-call`);
  } finally {
    await admin.dispose();
  }
}

function docsLink(scope: Page | Locator, kind: string, about: string): Locator {
  return scope.getByRole("link", { name: `${kind}: ${about} (opens in a new tab)`, exact: true });
}

async function expectTarget(link: Locator, path: string, hash = ""): Promise<void> {
  await expect(link).toBeVisible();
  const url = new URL((await link.getAttribute("href")) ?? "");
  expect(url.origin).toBe(DOCS_ORIGIN);
  expect(url.pathname).toBe(DOCS_PATH + path);
  expect(url.hash).toBe(hash);
}

/** The first segment of a person that starts later than ten minutes from the business clock's now, in the two weeks. */
async function futureSegment(
  page: Page,
  now: Date,
): Promise<{ start: string; end: string; label: string }> {
  const candidates = await page
    .locator('[data-testid="schedule-segment"]:not([data-gap])')
    .evaluateAll((els) =>
      els.map((el) => ({
        start: el.getAttribute("data-start") ?? "",
        end: el.getAttribute("data-end") ?? "",
        label: el.getAttribute("aria-label") ?? "",
      })),
    );
  const picked = candidates.find((c) => new Date(c.start).getTime() > now.getTime() + 10 * 60_000);
  expect(picked, "a future segment of a person in the two weeks").toBeDefined();
  return picked ?? { start: "", end: "", label: "" };
}

async function openMain(page: Page): Promise<void> {
  await page.goto(`/schedules/${ids.main}`);
  await expect(
    page.getByRole("heading", { name: "Team payments · Schedule Main", level: 1 }),
  ).toBeVisible();
  await expect(page.getByTestId("schedule-view")).toBeVisible();
}

test("the Teams page links to the Schedules list, which shows each Schedule with its Team and main and filters by Team", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signIn(page, "admin@example.org", "muster-dev-password");
  await page.goto("/teams");
  await page.getByRole("link", { name: "All Schedules" }).click();
  await expect(page).toHaveURL(/\/schedules$/);
  await expect(page.getByRole("heading", { name: "Schedules", level: 1 })).toBeVisible();
  await expect(docsLink(page, "Guide", "Schedules")).toBeVisible();
  const row = page.getByRole("row", { name: /^Main payments main/ });
  await expect(row).toContainText("payments");
  await expect(row.getByTestId("schedule-main")).toHaveText("main");
  await shot(page, "schedules-list");

  await page.getByLabel("Team", { exact: true }).selectOption({ label: "My Teams" });
  await expect(page).toHaveURL(/team=mine/);
  await expect(page.getByText("No Schedules for this filter.")).toBeVisible();

  await page.getByLabel("Team", { exact: true }).selectOption({ label: "payments" });
  await expect(page.getByRole("link", { name: "Main", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Main", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/schedules/${ids.main}$`));
  expect(csp).toEqual([]);
});

test("the Schedule page shows its header, the two-week view with the row Result, gaps, hatched Shifts and the mark for now, and Warnings", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signIn(page, "admin@example.org", "muster-dev-password");
  await openMain(page);
  await expect(page.getByTestId("schedule-main")).toHaveText("main");
  await expect(
    page.getByText("2 Rotation layers: the upper, narrower one wins. Overrides send no messages."),
  ).toBeVisible();
  await expectTarget(docsLink(page, "Guide", "Schedules"), "concepts/schedules/");

  const labels = page.getByTestId("schedule-row-label");
  await expect(labels.nth(0)).toHaveText("Working hours 11-19");
  await expect(labels.nth(1)).toHaveText("Around the clock");
  await expect(labels.nth(2)).toHaveText("Overrides");
  await expect(labels.nth(3)).toHaveText("Result — who is On-call");
  await expect(page.getByTestId("schedule-now")).toBeVisible();

  // A person who is disabled: the Shifts of the second week are hatched, and its nights are gaps in red.
  const hatched = page.locator('[data-testid="schedule-shift"][data-skipped="disabled"]');
  await expect(hatched.first()).toBeVisible();
  expect(await hatched.first().evaluate((el) => getComputedStyle(el).backgroundImage)).toContain(
    "repeating-linear-gradient",
  );
  const gap = page.locator('[data-testid="schedule-segment"][data-gap="true"]').first();
  await expect(gap).toBeVisible();
  const colours = await page.evaluate(() => {
    const gapEl = document.querySelector('[data-testid="schedule-segment"][data-gap="true"]');
    const personEl = document.querySelector('[data-testid="schedule-segment"]:not([data-gap])');
    return [gapEl, personEl].map((el) => (el === null ? "" : getComputedStyle(el).backgroundColor));
  });
  expect(colours[0]).not.toBe(colours[1]);

  const warnings = page.getByTestId("schedule-warnings");
  await expect(warnings).toContainText(`Disabled: @${SERGEY} in Around the clock`);
  const firstGap = warnings.locator('li[data-kind="gap"]').first();
  await expect(firstGap).toBeVisible();
  await expect(firstGap).toContainText(/^Gap: \w{3} \d{2}:\d{2} – /);

  await shot(page, "schedule-page-admin");
  const range = page.getByTestId("schedule-view-range");
  const today = await range.textContent();
  await page.getByRole("button", { name: "Next week" }).click();
  await expect(range).not.toHaveText(today ?? "");
  await expect(page.getByTestId("schedule-now")).toHaveCount(0);
  await page.getByRole("button", { name: "Previous week" }).click();
  await expect(range).toHaveText(today ?? "");
  await page.getByRole("button", { name: "Next week" }).click();
  await page.getByRole("button", { name: "Today" }).click();
  await expect(range).toHaveText(today ?? "");
  await expect(page.getByTestId("schedule-now")).toBeVisible();

  await expectTarget(
    docsLink(page, "Learn more", "Overrides"),
    "concepts/schedules/",
    "#overrides",
  );
  await expectTarget(
    docsLink(page, "Learn more", "Gap alert"),
    "concepts/schedules/",
    "#gaps-and-the-internal-alert",
  );
  expect(csp).toEqual([]);
});

test("On call now shows the person, the Rotation layer, until when, the unreachable mark and what comes next", async ({
  page,
}) => {
  const oracle = await onCallNow();
  const current = oracle.segments[0];
  const next = oracle.segments[1];
  expect(current?.user, "the rotation has an On-call now").not.toBeNull();
  const login = current?.user?.login ?? "";
  const now = new Date(oracle.from);
  await signIn(page, login, TEAM_SPEC_PASSWORD);
  await openMain(page);
  const person = page.getByTestId("schedule-on-call-person");
  await expect(person).toContainText(`@${login}`);
  await expect(person).toContainText(current?.source?.rotation_layer?.name ?? "");
  if (current !== undefined && current.end !== oracle.to) {
    await expect(person).toContainText(`until ${stampNear(new Date(current.end), now)}`);
  }
  // Neither person has a Mattermost Account link, and the Team has a Mattermost Destination.
  await expect(page.getByTestId("schedule-unreachable")).toContainText(
    "Unreachable in Mattermost: no linked account.",
  );
  await expectTarget(
    docsLink(page, "Learn more", "An unreachable On-call"),
    "concepts/schedules/",
    "#unreachable-on-calls",
  );
  if (next !== undefined) {
    const time = stampNear(new Date(next.start), now);
    await expect(page.getByTestId("schedule-next")).toHaveText(
      next.user === null ? `Next: gap from ${time}` : `Next: @${next.user.login} from ${time}`,
    );
  }
});

test("a Responder of the Team gives a Shift to another person: the Override is exactly that segment, a disabled person is refused in the form, and it can be changed and deleted", async ({
  page,
}) => {
  const csp = watchCsp(page);
  const oracle = await onCallNow();
  const now = new Date(oracle.from);
  await signIn(page, OLGA, TEAM_SPEC_PASSWORD);
  await openMain(page);

  const picked = await futureSegment(page, now);
  const start = picked.start;
  const end = picked.end;
  const owner = picked.label.split(",")[0] ?? "";
  const times = hintTimes(new Date(start), new Date(end));

  await expect(page.getByText("Any Responder of the Team; no messages.")).toBeVisible();
  await page.getByRole("button", { name: "Add Override" }).click();
  const form = page.getByTestId("override-form");
  await form.getByRole("radio", { name: "I give my Shift to…" }).check();
  await page.locator(`[data-testid="schedule-segment"][data-start="${start}"]`).click();
  await expect(form.getByTestId("override-hint")).toHaveText(
    `Choose a segment in the row 'Result' and the person who takes it; from ${times.from} to ${times.to} they are On-call on top of every Rotation layer.`,
  );

  // The disabled person is refused by the server, and the form says why.
  await form.getByLabel("Who takes it").selectOption({ label: `@${SERGEY}` });
  await form.getByRole("button", { name: "Save" }).click();
  await expect(form).toContainText("A disabled User cannot be On-call.");
  await shot(page, "schedule-override-refused");

  await form.getByLabel("Who takes it").selectOption({ label: `@${ALICE}` });
  await form.getByRole("button", { name: "Save" }).click();
  await expect(form).toHaveCount(0);
  await shot(page, "schedule-override-made");

  const row = page.getByTestId("schedule-override");
  await expect(row).toHaveCount(1);
  await expect(row.getByTestId("schedule-override-who")).toHaveText(`${owner} → @${ALICE}`);
  await expect(row.getByTestId("schedule-override-by")).toHaveText(`made by @${OLGA}`);
  await expect(
    page.locator(`[data-testid="schedule-segment"][data-start="${start}"]`),
  ).toContainText(`@${ALICE}`);

  const admin = await adminApi();
  try {
    const made = await admin.call<{
      items: { id: string; start: string; end: string; user: { login: string } }[];
    }>("GET", `/api/v1/schedules/${ids.main}/overrides`);
    expect(made.items).toHaveLength(1);
    expect(new Date(made.items[0]?.start ?? "").getTime()).toBe(new Date(start).getTime());
    expect(new Date(made.items[0]?.end ?? "").getTime()).toBe(new Date(end).getTime());
    expect(made.items[0]?.user.login).toBe(ALICE);
  } finally {
    await admin.dispose();
  }

  // Change the Override: its end moves an hour earlier.
  await row.getByRole("button", { name: "Change" }).click();
  const edit = page.getByTestId("override-edit-form");
  const earlier = new Date(new Date(end).getTime() - 60 * 60_000);
  const local = wall(earlier);
  await edit
    .getByLabel("To")
    .fill(
      `${local.day}T${String(local.hour).padStart(2, "0")}:${String(local.minute).padStart(2, "0")}`,
    );
  await edit.getByRole("button", { name: "Save" }).click();
  await expect(edit).toHaveCount(0);
  await expect(page.getByTestId("schedule-override-when")).toContainText(clock(earlier));
  // The Result follows the change: the segment of the Override now ends an hour earlier.
  await expect
    .poll(async () => {
      const shown = await page
        .locator(`[data-testid="schedule-segment"][data-start="${start}"]`)
        .getAttribute("data-end");
      return new Date(shown ?? "").getTime();
    })
    .toBe(earlier.getTime());

  await page.getByTestId("schedule-override").getByRole("button", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Delete Override" }).click();
  await expect(page.getByTestId("schedule-no-overrides")).toBeVisible();
  // And the Result takes the Shift back to its person.
  await expect(
    page.locator(`[data-testid="schedule-segment"][data-start="${start}"]`),
  ).toContainText(owner);
  expect(csp).toEqual([]);
});

test("a Responder outside the Team cannot add an Override, and an Admin outside it who takes a Shift is refused in the form", async ({
  browser,
}) => {
  const oracle = await onCallNow();
  const now = new Date(oracle.from);
  const outsider = await browser.newContext();
  const eve = await outsider.newPage();
  await signIn(eve, EVE, TEAM_SPEC_PASSWORD);
  await openMain(eve);
  await expect(eve.getByRole("button", { name: "Add Override" })).toHaveCount(0);
  await expect(eve.locator('button[data-testid="schedule-segment"]')).toHaveCount(0);
  await expect(eve.getByTestId("schedule-segment").first()).toBeVisible();
  await outsider.close();

  const adminContext = await browser.newContext();
  const page = await adminContext.newPage();
  await signIn(page, "admin@example.org", "muster-dev-password");
  await openMain(page);
  const picked = await futureSegment(page, now);
  await page.getByRole("button", { name: "Add Override" }).click();
  const form = page.getByTestId("override-form");
  await form.getByRole("radio", { name: "I take the Shift of…" }).check();
  await page.locator(`[data-testid="schedule-segment"][data-start="${picked.start}"]`).click();
  await expect(form.getByTestId("override-taker")).toContainText("you, @admin");
  await form.getByRole("button", { name: "Save" }).click();
  await expect(form).toContainText("This person is not in the Team.");
  await form.getByRole("button", { name: "Cancel" }).click();
  await expect(form).toHaveCount(0);
  await expect(page.getByTestId("schedule-no-overrides")).toBeVisible();
  await adminContext.close();
});

test("a Responder reads the Rotation layers without controls", async ({ page }) => {
  await signIn(page, OLGA, TEAM_SPEC_PASSWORD);
  await openMain(page);
  await expect(page.getByRole("heading", { name: "Rotation layers", level: 2 })).toBeVisible();
  const items = page.getByTestId("rotation-layer-summary");
  await expect(items.nth(0)).toContainText("Working hours 11-19");
  await expect(items.nth(0)).toContainText("Mon–Fri 11:00–19:00");
  await expect(items.nth(1)).toContainText(`@${PETR} → @${SERGEY} → @${OLGA}`);
  await expect(page.getByRole("button", { name: "Add Rotation layer" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Save", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Delete Schedule" })).toHaveCount(0);
  await page.goto("/schedules");
  await expect(page.getByRole("link", { name: "New Schedule" })).toHaveCount(0);
});

test("an Admin edits the Rotation layers and the gap alert, and a person the server refuses is marked at the person", async ({
  page,
}) => {
  await signIn(page, "admin@example.org", "muster-dev-password");
  await openMain(page);
  const form = page.getByTestId("schedule-form");
  await expect(form.getByRole("combobox", { name: "Gap alert", exact: true })).toHaveValue(
    "by_default",
  );
  await expect(form.getByRole("checkbox", { name: "Main Schedule" })).toBeChecked();
  await expect(form.getByRole("checkbox", { name: "Main Schedule" })).toBeDisabled();
  await expectTarget(
    docsLink(form, "Learn more", "Gap alert"),
    "concepts/schedules/",
    "#gaps-and-the-internal-alert",
  );

  await form
    .getByRole("combobox", { name: "Gap alert", exact: true })
    .selectOption({ label: "Off" });
  await form.getByRole("button", { name: "Add Rotation layer" }).click();
  const cards = form.getByTestId("rotation-layer");
  await expect(cards).toHaveCount(3);
  await expect(cards.nth(0).getByLabel("Rotation layer name")).toHaveValue("Rotation layer 3");
  await cards
    .nth(0)
    .getByLabel("People in turn")
    .selectOption({ label: `@${SERGEY}` });
  await form.getByRole("button", { name: "Save" }).click();
  await expect(cards.nth(0).getByTestId("rotation-layer-people")).toContainText(
    "A disabled User cannot be On-call.",
  );

  await cards
    .nth(0)
    .getByRole("button", { name: `Remove @${SERGEY} from the turn` })
    .click();
  await cards
    .nth(0)
    .getByLabel("People in turn")
    .selectOption({ label: `@${ALICE}` });
  await cards.nth(0).getByLabel("Shift length").selectOption({ label: "Hours" });
  await cards.nth(0).getByLabel("Hours per Shift").fill("12");
  await form.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("schedule-status")).toHaveText("Saved.");

  const admin = await adminApi();
  try {
    const stored = await admin.call<{
      gap_alert: string;
      rotation_layers: { name: string; shift_length: string; shift_seconds?: number }[];
    }>("GET", `/api/v1/schedules/${ids.main}`);
    expect(stored.gap_alert).toBe("off");
    expect(stored.rotation_layers.map((l) => l.name)).toEqual([
      "Around the clock",
      "Working hours 11-19",
      "Rotation layer 3",
    ]);
    expect(stored.rotation_layers[2]).toMatchObject({
      shift_length: "hours",
      shift_seconds: 43200,
    });
  } finally {
    await admin.dispose();
  }

  // Take the new Rotation layer away again and turn the alert back to its default.
  await cards.nth(0).getByRole("button", { name: "Remove Rotation layer 3" }).click();
  await form
    .getByRole("combobox", { name: "Gap alert", exact: true })
    .selectOption({ label: "By default" });
  await form.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("schedule-status")).toHaveText("Saved.");
  await expect(cards).toHaveCount(2);
  await page.reload();
  await expect(page.getByTestId("rotation-layer")).toHaveCount(2);
});

test("a Schedule another person changed meanwhile: Save says so, and Reload shows the new version", async ({
  page,
}) => {
  await signIn(page, "admin@example.org", "muster-dev-password");
  await openMain(page);
  const form = page.getByTestId("schedule-form");
  const gapAlert = form.getByRole("combobox", { name: "Gap alert", exact: true });
  await expect(gapAlert).toHaveValue("by_default");
  // Another person turns the gap alert on, behind the page's back.
  const admin = await adminApi();
  try {
    const body = await admin.call<
      Record<string, unknown> & { etag: string; rotation_layers: Record<string, unknown>[] }
    >("GET", `/api/v1/schedules/${ids.main}`);
    await admin.call(
      "PUT",
      `/api/v1/schedules/${ids.main}`,
      {
        team_id: ids.team,
        name: body.name,
        time_zone: body.time_zone,
        main: true,
        gap_alert: "on",
        rotation_layers: body.rotation_layers.map((l) => ({
          id: l.id,
          name: l.name,
          user_ids: l.user_ids,
          shift_length: l.shift_length,
          handover_time: l.handover_time,
          handover_day: l.handover_day,
          start_date: l.start_date,
          limits: l.limits,
        })),
      },
      { "If-Match": body.etag },
    );
  } finally {
    await admin.dispose();
  }
  await form.getByLabel("Name", { exact: true }).fill("Main renamed");
  await form.getByRole("button", { name: "Save" }).click();
  await expect(
    form.getByText("Someone else changed this Schedule. Reload to see the changes."),
  ).toBeVisible();
  await form.getByRole("button", { name: "Reload" }).click();
  await expect(gapAlert).toHaveValue("on");
  await expect(form.getByLabel("Name", { exact: true })).toHaveValue("Main");
  await gapAlert.selectOption({ label: "By default" });
  await form.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("schedule-status")).toHaveText("Saved.");
});

test("an Admin creates a Schedule from New Schedule: it is not main and its gap alert is By default", async ({
  page,
}) => {
  await signIn(page, "admin@example.org", "muster-dev-password");
  await page.goto("/teams");
  await page.getByRole("link", { name: "All Schedules" }).click();
  await page.getByLabel("Team", { exact: true }).selectOption({ label: "payments" });
  await page.getByRole("link", { name: "New Schedule" }).click();
  await expect(page.getByRole("heading", { name: "New Schedule", level: 1 })).toBeVisible();
  const form = page.getByTestId("schedule-form");
  await expect(form.getByLabel("Team", { exact: true })).toHaveValue(ids.team);
  await form.getByLabel("Name", { exact: true }).fill("Second line");
  await form.getByLabel("People in turn").selectOption({ label: `@${OLGA}` });
  await form.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByRole("heading", { name: "Team payments · Schedule Second line", level: 1 }),
  ).toBeVisible();
  await expect(page.getByTestId("schedule-main")).toHaveCount(0);
  await expect(
    page.getByTestId("schedule-form").getByRole("combobox", { name: "Gap alert", exact: true }),
  ).toHaveValue("by_default");
  await expect(page.getByRole("heading", { name: "Rotation layers", level: 2 })).toBeVisible();

  // Its name is taken in the Team: the form says so at the name.
  await page.goto(`/schedules/new?team=${ids.team}`);
  const again = page.getByTestId("schedule-form");
  await again.getByLabel("Name", { exact: true }).fill("second line");
  await again.getByLabel("People in turn").selectOption({ label: `@${OLGA}` });
  await again.getByRole("button", { name: "Save" }).click();
  await expect(again).toContainText("The Team has a Schedule with this name.");

  const admin = await adminApi();
  try {
    const second = (await schedulesOf(admin)).find((s) => s.name === "Second line");
    expect(second).toBeDefined();
    await admin.call("DELETE", `/api/v1/schedules/${second?.id}`);
  } finally {
    await admin.dispose();
  }
});

test("an On-call with a confirmed contact at a Notification gateway: the Schedule page and On call now follow the unreachable mark with the call that works", async ({
  page,
}) => {
  test.setTimeout(150_000);
  const phone = "+10000000005";
  const admin = await adminApi();
  let petr = "";
  let contact = "";
  try {
    const seeded = await seedOnCall(admin, "gwr-");
    petr = seeded.users.petr;
    const gateway = await ensureGateway(admin);
    await signIn(page, "petr", TEAM_SPEC_PASSWORD);
    const mark = page.getByTestId("schedule-unreachable");
    const homeRow = page.getByTestId("on-call-row").filter({ hasText: "gwr-payments" });

    // Without a contact the mark stands alone.
    await page.goto(`/schedules/${seeded.schedules.payments}`);
    await expect(mark).toContainText("Unreachable in Mattermost: no linked account.");
    await expect(mark.getByTestId("schedule-gateway-reaches")).toHaveCount(0);
    await page.goto("/");
    await expect(homeRow.getByTestId("on-call-unreachable")).toHaveText(
      "unreachable in Mattermost",
    );
    await expect(homeRow.getByTestId("on-call-gateway-reaches")).toHaveCount(0);

    // With a confirmed contact the call that works follows it, on the Schedule page and in On call now.
    contact = await confirmContact("petr", gateway, phone);
    await page.goto(`/schedules/${seeded.schedules.payments}`);
    await expect(mark).toContainText(
      "Unreachable in Mattermost: no linked account. A call through the Notification gateway works.",
    );
    await expect(
      page.getByTestId("schedule-warnings").getByTestId("schedule-gateway-reaches").first(),
    ).toHaveText("A call through the Notification gateway works.");
    await shot(page, "schedule-gateway-reaches");
    await page.goto("/");
    await expect(homeRow.getByTestId("on-call-unreachable")).toHaveText(
      "unreachable in Mattermost",
    );
    await expect(homeRow.getByTestId("on-call-gateway-reaches")).toHaveText(
      "A call through the Notification gateway works.",
    );
    await shot(page, "home-gateway-reaches");
  } finally {
    if (contact !== "") {
      await admin.call("DELETE", `/api/v1/users/${petr}/gateway-contacts/${contact}`);
    }
    await removeOnCall(admin, "gwr-");
    await admin.dispose();
  }
});

test("the Schedule page reads in Russian and fits 360 px", async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, RU_ADMIN, TEAM_SPEC_PASSWORD);
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/schedules");
  await expect(page.getByRole("heading", { name: "Расписания", level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: "Новое расписание" })).toBeVisible();
  await expectNoHorizontalScroll(page);

  await page.goto(`/schedules/${ids.main}`);
  await expect(
    page.getByRole("heading", { name: "Команда payments · Расписание Main", level: 1 }),
  ).toBeVisible();
  await expect(page.getByTestId("schedule-main")).toHaveText("главное");
  await expect(page.getByText("Итог — кто дежурит")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Сейчас дежурит" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Предупреждения" })).toBeVisible();
  await expect(page.getByTestId("schedule-warnings")).toContainText(
    `Отключён: @${SERGEY} в слое «Around the clock»`,
  );
  await expect(page.getByRole("button", { name: "Предыдущая неделя" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Сегодня" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Следующая неделя" })).toBeVisible();
  await expect(page.getByTestId("schedule-now")).toContainText("Сейчас");
  await expect(page.getByRole("heading", { name: "Слои ротации", level: 2 })).toBeVisible();
  await expect(page.getByRole("button", { name: "Добавить слой" })).toBeVisible();
  await expect(page.getByRole("link", { name: /^Руководство: Расписания/ })).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "schedule-page-ru-360");
  await context.close();
  expect(csp).toEqual([]);
});

test("the iCal subscription shows the link once, a reload does not show it again, Revoke ends it and a new link replaces the old one", async ({
  page,
}) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  try {
    await admin.call("DELETE", "/api/v1/me/ical-feed");
  } finally {
    await admin.dispose();
  }
  await signIn(page, "admin@example.org", "muster-dev-password");
  await openMain(page);
  const section = page.getByTestId("ical-subscription");
  await expect(section.getByRole("heading", { name: "iCal subscription", level: 2 })).toBeVisible();
  await expect(
    section.getByText(
      "A personal link for your calendar application. It is shown once; a revoked link stops working at once.",
    ),
  ).toBeVisible();
  await expectTarget(
    docsLink(section, "Learn more", "iCal subscription"),
    "concepts/schedules/",
    "#ical-feed",
  );
  await expect(section.getByRole("button", { name: "New link" })).toBeVisible();
  await expect(section.getByRole("button", { name: "Revoke" })).toHaveCount(0);

  await section.getByRole("button", { name: "New link" }).click();
  const link = section.getByTestId("ical-link");
  await expect(link).toContainText("Copy the link now: it will not be shown again");
  const first = ((await section.getByTestId("ical-url").textContent()) ?? "").trim();
  expect(first).toMatch(/^http:\/\/localhost:8080\/ical\/mstr_cal_[A-Za-z0-9_-]+\.ics$/);
  await shot(page, "ical-link-shown");
  const feed = await page.request.get(first);
  expect(feed.status()).toBe(200);
  expect(await feed.text()).toContain("BEGIN:VCALENDAR");

  // Reloading shows no link: only that one exists, with New link and Revoke.
  await page.reload();
  await expect(section.getByRole("button", { name: "Revoke" })).toBeVisible();
  await expect(section.getByRole("button", { name: "New link" })).toBeVisible();
  await expect(section.getByTestId("ical-link")).toHaveCount(0);
  await expect(page.getByText("mstr_cal_")).toHaveCount(0);
  await shot(page, "ical-link-hidden");

  // A new link asks first, and the old one stops working at once.
  await section.getByRole("button", { name: "New link" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Issue a new link?")).toBeVisible();
  await dialog.getByRole("button", { name: "New link" }).click();
  const second = ((await section.getByTestId("ical-url").textContent()) ?? "").trim();
  expect(second).not.toBe(first);
  expect((await page.request.get(first)).status()).toBe(404);
  expect((await page.request.get(second)).status()).toBe(200);

  await section.getByRole("button", { name: "Revoke" }).click();
  await expect(section.getByRole("button", { name: "Revoke" })).toHaveCount(0);
  await expect(section.getByRole("button", { name: "New link" })).toBeVisible();
  await expect(section.getByTestId("ical-link")).toHaveCount(0);
  expect((await page.request.get(second)).status()).toBe(404);
  expect(csp).toEqual([]);
});

test("the iCal subscription reads in Russian", async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, RU_ADMIN, TEAM_SPEC_PASSWORD);
  await page.goto("/profile");
  await page.getByLabel(/^(Language|Язык)$/).selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
  await page.goto(`/schedules/${ids.main}`);
  const section = page.getByTestId("ical-subscription");
  await expect(section.getByRole("heading", { name: "Подписка iCal", level: 2 })).toBeVisible();
  await expect(
    section.getByText(
      "Личная ссылка для календаря. Показывается один раз; отозванная перестаёт работать сразу.",
    ),
  ).toBeVisible();
  await section.getByRole("button", { name: "Новая ссылка" }).click();
  await expect(section.getByText("Скопируйте ссылку сейчас — больше её не покажем")).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "ical-ru-360");
  await section.getByRole("button", { name: "Отозвать" }).click();
  await expect(section.getByRole("button", { name: "Отозвать" })).toHaveCount(0);
  await expect(section.getByRole("button", { name: "Новая ссылка" })).toBeVisible();
  await context.close();
  expect(csp).toEqual([]);
});
