// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Timeline in a real browser, against a stubbed API: a text for every event and system entry (the timer entries of
// C-17 among them), the actor with the Transport, the Loud or Quiet mark and the Mentions, the details, a Note with
// markup shown as text, Russian plurals, the kind filter and the order sent to the API. A delivery entry names its
// Destination and, with destinations:read, carries the Destination's full label as its tooltip and description.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  Connection,
  DeliveryEventKind,
  Destination,
  LifecycleEvent,
  Permission,
  Session,
  TimelineAlertsEntry,
  TimelineDeliveryEntry,
  TimelineEntry,
  TimelineNoteEntry,
  TimelineStatusEntry,
  TimelineSystemEntry,
  TimelineSystemEntrySystemEvent,
  TimelineTimersEntry,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { formatDateTime } from "../lib/time";
import { telegramConnection } from "../test/fixtures/connections";
import { telegramDestination } from "../test/fixtures/destinations";
import { Timeline, actorText, entryText } from "./timeline";

const AT = "2026-10-07T09:00:00Z";
const ZONE = "Europe/Berlin";
const SYSTEM = { kind: "system", transport: "system", reason: "ingestion" } as const;
const ALICE = {
  kind: "user",
  id: "US0000000000AL",
  name: "Alice Smith",
  transport: "mattermost",
} as const;
const BOB = { id: "US0000000000BB", name: "Bob", deactivated: false };
const MARKUP = "<img src=x onerror=alert(1)> {{count}} $t(errors.generic)";

function status(event: LifecycleEvent, extra: Partial<TimelineStatusEntry> = {}): TimelineEntry {
  const entry: TimelineStatusEntry = {
    id: `TE${event}`,
    at: AT,
    kind: "status",
    actor: SYSTEM,
    event,
    loudness: "quiet",
    mentions: [],
    ...extra,
  };
  return entry;
}

function alerts(event: LifecycleEvent, extra: Partial<TimelineAlertsEntry> = {}): TimelineEntry {
  const entry: TimelineAlertsEntry = {
    id: `TA${event}`,
    at: AT,
    kind: "alerts",
    actor: SYSTEM,
    event,
    loudness: "quiet",
    mentions: [],
    ...extra,
  };
  return entry;
}

function timers(event: LifecycleEvent, extra: Partial<TimelineTimersEntry> = {}): TimelineEntry {
  const entry: TimelineTimersEntry = {
    id: `TT${event}`,
    at: AT,
    kind: "timers",
    actor: SYSTEM,
    event,
    loudness: "loud",
    mentions: ["owner"],
    ...extra,
  };
  return entry;
}

function system(
  systemEvent: TimelineSystemEntrySystemEvent,
  extra: Partial<TimelineSystemEntry> = {},
): TimelineEntry {
  const entry: TimelineSystemEntry = {
    id: "TS",
    at: AT,
    kind: "system",
    actor: SYSTEM,
    system_event: systemEvent,
    ...extra,
  };
  return entry;
}

function delivery(event: DeliveryEventKind): TimelineEntry {
  const entry: TimelineDeliveryEntry = {
    id: "DE",
    at: AT,
    kind: "delivery",
    actor: SYSTEM,
    delivery_event: event,
    destination: { id: "DS0000000000OP", name: "ops" },
    loudness: "quiet",
    mentions: [],
  };
  return entry;
}

function noteEntry(body: string): TimelineEntry {
  const entry: TimelineNoteEntry = {
    id: "NE1",
    at: AT,
    kind: "notes",
    actor: ALICE,
    event: "note_added",
    loudness: "quiet",
    mentions: [],
    note: {
      id: "NE1",
      body,
      author: { kind: "user", ...BOB },
      transport: "mattermost",
      created_at: AT,
    },
  };
  return entry;
}

const dateTime = (iso: string) => formatDateTime(iso, ZONE, "en");
const text = (entry: TimelineEntry) => entryText(i18n.t, entry, dateTime);

let entries: TimelineEntry[] = [];
let requests: URL[] = [];
// The Destinations and Connections lists the labels of delivery entries come from.
let lists: { destinations: Destination[]; connections: Connection[] } = {
  destinations: [],
  connections: [],
};

beforeEach(async () => {
  entries = [];
  requests = [];
  lists = { destinations: [], connections: [] };
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    requests.push(url);
    if (url.pathname === "/api/v1/destinations" || url.pathname === "/api/v1/connections") {
      const items =
        url.pathname === "/api/v1/destinations" ? lists.destinations : lists.connections;
      return Promise.resolve(
        new Response(JSON.stringify({ items, next_cursor: null }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    const kinds = url.searchParams.getAll("kind");
    const items = entries.filter((e) => kinds.length === 0 || kinds.includes(e.kind));
    return Promise.resolve(
      new Response(JSON.stringify({ items, next_cursor: null }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("entryText", () => {
  test("has a text for every lifecycle event of each kind", () => {
    const cases: [TimelineEntry, string][] = [
      [status("created"), "Alert Group created"],
      [status("urgency_raised"), "Became Urgent"],
      [status("reopened"), "Reopened"],
      [status("snooze_ended"), "Snooze ended while alerts still fire"],
      [
        status("resolved", { reason: "resolved", reason_code: "resolved" }),
        "Resolved: Resolved by Alertmanager",
      ],
      [status("resolved", { reason: "gone", reason_code: "gone" }), "Resolved: Gone"],
      [status("resolved"), "Resolved"],
      [status("acknowledged"), "Acknowledged"],
      [status("takeover", { previous_owner: BOB }), "Taken over from Bob"],
      [
        status("unacknowledged", { reason: "owner_disabled", reason_code: "owner_disabled" }),
        "Unacknowledged: the owner was disabled",
      ],
      [
        status("unacknowledged", { reason: "A reason without a code", reason_code: null }),
        "Unacknowledged: A reason without a code",
      ],
      [status("unresolved"), "Unresolved"],
      [status("snoozed", { snooze_until: AT }), `Snoozed until ${dateTime(AT)}`],
      [status("snoozed", { snooze_until: null }), "Snoozed with no end"],
      [status("unsnoozed"), "Unsnoozed"],
      [status("auto_unacknowledged"), "Unacknowledged automatically"],
      [
        status("auto_unacknowledged", {
          reason: "No answer to the last two Reminders",
          reason_code: "reminders_unanswered",
        }),
        "Unacknowledged automatically: No answer to the last two Reminders",
      ],
      [alerts("alerts_added", { fingerprints: ["a"] }), "1 alert added"],
      [alerts("alerts_added", { fingerprints: ["a", "b"] }), "2 alerts added"],
      [alerts("alert_replaced", { replaced_label: "pod" }), "Alert replaced"],
      [alerts("alert_resolved", { fingerprints: ["a"] }), "1 alert resolved"],
      [
        alerts("alert_continued", { fingerprints: ["a"] }),
        "1 alert continued with a new start time",
      ],
      [alerts("annotations_changed"), "Annotations changed"],
      [alerts("severity_raised"), "Severity raised"],
      [timers("ack_timeout", { notice_number: 2 }), "Ack timeout notice 2 of 3"],
      [timers("unclaimed"), "Unclaimed"],
      [timers("reminder", { notice_number: 1 }), "Reminder 1"],
      [timers("reminder_answered"), "Still on it"],
      [timers("notices_missed", { missed_count: 1 }), "1 notice missed"],
      [timers("notices_missed", { missed_count: 3 }), "3 notices missed"],
    ];
    for (const [entry, expected] of cases) {
      expect(text(entry), entry.kind).toBe(expected);
    }
  });

  test("has a text for system entries and delivery events", () => {
    const from = "2026-10-07T08:00:00Z";
    const to = "2026-10-07T08:10:00Z";
    expect(text(system("muster_unavailable", { period_from: from, period_to: to }))).toBe(
      `Muster was unavailable from ${dateTime(from)} to ${dateTime(to)}`,
    );
    expect(text(system("moved_to_default_route", { event: "moved_to_default_route" }))).toBe(
      "Moved to the Default route",
    );
    expect(text(system("fallback_template_used"))).toBe("Messages used the fallback template");
    expect(
      text(
        system("fallback_template_used", {
          detail: 'line template failed: map has no entry for key "pod"',
        }),
      ),
    ).toBe('Fallback template used: Alert line failed — map has no entry for key "pod"');
    expect(
      text(
        system("fallback_template_used", {
          detail: "root_message template failed: line 4, column 7: <b>x</b>",
        }),
      ),
    ).toBe("Fallback template used: Root message failed — line 4, column 7: <b>x</b>");
    expect(text(system("template_value_missing"))).toBe("A template value was missing");
    expect(text(delivery("publication"))).toBe("Published in ops");
    expect(text(delivery("not_delivered"))).toBe("Not delivered to ops");
    expect(text(delivery("final_edit"))).toBe("No longer updated in ops");
    expect(text(delivery("republished_past_edit_window"))).toBe(
      "Published again in ops: past the edit window",
    );
  });

  test("names the actor with the Transport, and Muster for the system", () => {
    expect(actorText(i18n.t, SYSTEM)).toBe("Muster");
    expect(actorText(i18n.t, ALICE)).toBe("Alice Smith via Mattermost");
    expect(actorText(i18n.t, { ...ALICE, transport: "api", token_name: "ci" })).toBe(
      "Alice Smith via the API, token ci",
    );
  });

  test("names the Transport slack Slack, in English and Russian", async () => {
    expect(actorText(i18n.t, { ...ALICE, transport: "slack" })).toBe("Alice Smith via Slack");
    await i18n.changeLanguage("ru");
    expect(actorText(i18n.t, { ...ALICE, transport: "slack" })).toBe("Alice Smith через Slack");
  });

  test("names the Transport bridge Bridge, and Мост in Russian", async () => {
    expect(actorText(i18n.t, { ...ALICE, transport: "bridge" })).toBe("Alice Smith via Bridge");
    await i18n.changeLanguage("ru");
    expect(actorText(i18n.t, { ...ALICE, transport: "bridge" })).toBe("Alice Smith через Мост");
    expect(text(delivery("republished_past_edit_window"))).toBe(
      "Опубликована заново в ops: окно правки истекло",
    );
  });

  test("uses the Russian plural forms", async () => {
    await i18n.changeLanguage("ru");
    const added = (n: number) =>
      text(alerts("alerts_added", { fingerprints: Array.from({ length: n }, (_, i) => `f${i}`) }));
    expect(added(1)).toBe("Добавлен 1 алерт");
    expect(added(3)).toBe("Добавлены 3 алерта");
    expect(added(5)).toBe("Добавлено 5 алертов");
    expect(added(21)).toBe("Добавлен 21 алерт");
    expect(text(status("reopened"))).toBe("Переоткрыта");
    const missed = (n: number) => text(timers("notices_missed", { missed_count: n }));
    expect(missed(1)).toBe("Пропущено 1 уведомление");
    expect(missed(3)).toBe("Пропущено 3 уведомления");
    expect(missed(5)).toBe("Пропущено 5 уведомлений");
    expect(missed(21)).toBe("Пропущено 21 уведомление");
    expect(text(timers("ack_timeout", { notice_number: 3 }))).toBe(
      "Уведомление о тайм-ауте подтверждения 3 из 3",
    );
    expect(text(timers("reminder_answered"))).toBe("Ещё работаю");
    // Translated by its code, whatever the text of the reason says.
    expect(
      text(
        status("auto_unacknowledged", {
          reason: "Any English text",
          reason_code: "reminders_unanswered",
        }),
      ),
    ).toBe("Подтверждение снято автоматически: нет ответа на два последних напоминания");
    expect(
      text(status("unacknowledged", { reason: "owner_deleted", reason_code: "owner_deleted" })),
    ).toBe("Подтверждение снято: владелец удалён");
    expect(
      text(
        system("fallback_template_used", {
          detail: "ack_timeout_notice template failed: boom",
        }),
      ),
    ).toBe(
      "Использован запасной шаблон: ошибка в шаблоне «Уведомление о тайм-ауте подтверждения» — boom",
    );
  });
});

async function renderTimeline(permissions: Permission[] = []) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead =
    permissions.length === 0
      ? { session: null, ended: false }
      : {
          session: {
            state: "active",
            // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the Timeline reads only the Permissions
            user: { id: "US0000000000ME", name: "Me" } as Session["user"],
            csrf_token: "csrf",
            expires_at: "2026-10-07T19:00:00Z",
            idle_expires_at: "2026-10-07T19:00:00Z",
            docs_version: "latest",
            method: "local",
            permissions,
          },
          ended: false,
        };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Timeline alertGroupId="AG0000000000AA" />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const item = (testText: string) => page.getByTestId("timeline-entry").filter({ hasText: testText });

describe("Timeline", () => {
  test("shows each entry with its actor, loudness, Mentions and details", async () => {
    entries = [
      status("reopened", { loudness: "loud", mentions: ["reopen"], fingerprints: ["f1"] }),
      alerts("alert_replaced", { replaced_label: "pod", fingerprints: ["f2"] }),
      noteEntry(MARKUP),
    ];
    const alerted = vi.spyOn(window, "alert").mockImplementation(() => {});
    await renderTimeline();

    const reopened = item("Reopened");
    await expect.element(reopened.getByTestId("timeline-actor")).toHaveTextContent("Muster");
    await expect.element(reopened.getByTestId("timeline-loud")).toHaveTextContent("Loud");
    await expect
      .element(reopened.getByTestId("timeline-mentions"))
      .toHaveTextContent("Mentions: reopen");
    await expect.element(reopened.getByText("1 fingerprint")).toBeVisible();

    const replaced = item("Alert replaced");
    await expect.element(replaced.getByTestId("replaced-label")).toHaveTextContent("pod");
    expect(replaced.getByTestId("timeline-loud").elements()).toHaveLength(0);
    expect(replaced.getByTestId("timeline-mentions").elements()).toHaveLength(0);

    const note = item("Note");
    await expect
      .element(note.getByTestId("timeline-actor"))
      .toHaveTextContent("Alice Smith via Mattermost");
    await expect.element(note.getByTestId("note-body")).toHaveTextContent(MARKUP);
    expect(document.querySelector("[data-testid=timeline] img")).toBeNull();
    expect(alerted).not.toHaveBeenCalled();
    expect(requests[0]?.searchParams.get("order")).toBe("desc");
  });

  test("shows the timer entries with their loudness, Mentions and the previous Owner", async () => {
    entries = [
      timers("ack_timeout", { id: "TT1", notice_number: 3, mentions: ["ack_timeout"] }),
      timers("unclaimed", { id: "TT2", loudness: "quiet", mentions: [] }),
      timers("reminder_answered", {
        id: "TT3",
        loudness: "quiet",
        mentions: [],
        actor: { kind: "user", id: BOB.id, name: "Bob", transport: "ui" },
      }),
      status("auto_unacknowledged", {
        loudness: "loud",
        mentions: ["owner"],
        reason: "No answer to the last two Reminders",
        reason_code: "reminders_unanswered",
        previous_owner: BOB,
      }),
      delivery("publication"),
    ];
    await renderTimeline();

    const notice = item("Ack timeout notice 3 of 3");
    await expect.element(notice.getByTestId("timeline-loud")).toHaveTextContent("Loud");
    await expect
      .element(notice.getByTestId("timeline-mentions"))
      .toHaveTextContent("Mentions: ack_timeout");
    expect(notice.getByTestId("timeline-quiet").elements()).toHaveLength(0);

    const unclaimed = item("Unclaimed");
    await expect.element(unclaimed.getByTestId("timeline-quiet")).toHaveTextContent("Quiet");
    expect(unclaimed.getByTestId("timeline-loud").elements()).toHaveLength(0);

    const answered = item("Still on it");
    await expect
      .element(answered.getByTestId("timeline-actor"))
      .toHaveTextContent("Bob via the UI");
    await expect.element(answered.getByTestId("timeline-quiet")).toBeVisible();

    const auto = item("Unacknowledged automatically: No answer to the last two Reminders");
    await expect.element(auto.getByTestId("timeline-loud")).toHaveTextContent("Loud");
    await expect
      .element(auto.getByTestId("timeline-mentions"))
      .toHaveTextContent("Mentions: owner");
    await expect.element(auto.getByText("Previous owner:")).toBeVisible();
    await expect.element(auto.getByText("Bob", { exact: true })).toBeVisible();

    // A delivery event marks only what was Loud.
    const published = item("Published in ops");
    await expect.element(published).toBeVisible();
    expect(published.getByTestId("timeline-quiet").elements()).toHaveLength(0);
  });

  test("filters by kind and turns the order", async () => {
    entries = [status("created"), alerts("alerts_added", { fingerprints: ["a"] })];
    const screen = await renderTimeline();
    await expect.element(item("1 alert added")).toBeVisible();
    await screen.getByRole("button", { name: "Status" }).click();
    await expect
      .element(screen.getByRole("button", { name: "Status" }))
      .toHaveAttribute("aria-pressed", "true");
    await expect.poll(() => item("1 alert added").elements().length).toBe(0);
    await expect.element(item("Alert Group created")).toBeVisible();
    expect(requests.at(-1)?.searchParams.getAll("kind")).toEqual(["status"]);

    await screen.getByLabelText("Order").selectOptions("asc");
    await expect.poll(() => requests.at(-1)?.searchParams.get("order")).toBe("asc");
  });
  test("gives a delivery entry the full label of its Destination as tooltip and description", async () => {
    lists = {
      destinations: [
        telegramDestination({
          id: "DS0000000000OP",
          name: "ops",
          connection_id: "CN000000000TGB",
          channel_id: "@muster_alerts",
          channel_title: "Muster alerts",
          created_at: AT,
        }),
      ],
      connections: [telegramConnection({ id: "CN000000000TGB", name: "tg-b" })],
    };
    const gone: TimelineDeliveryEntry = {
      id: "DE2",
      at: AT,
      kind: "delivery",
      actor: SYSTEM,
      delivery_event: "not_delivered",
      destination: { id: "DS00000000GONE", name: "gone" },
      loudness: "quiet",
      mentions: [],
    };
    entries = [delivery("publication"), gone];
    await renderTimeline(["alert-groups:read", "destinations:read", "connections:read"]);
    const published = item("Published in ops").getByTestId("timeline-text");
    await expect.element(published).toHaveTextContent("Published in ops");
    await expect
      .element(published)
      .toHaveAttribute("title", "ops · Telegram · via tg-b · Muster alerts");
    await expect
      .element(published)
      .toHaveAccessibleDescription("ops · Telegram · via tg-b · Muster alerts");
    const missing = item("Not delivered to gone").getByTestId("timeline-text");
    await expect.element(missing).toHaveTextContent("Not delivered to gone");
    await expect.element(missing).not.toHaveAttribute("title");
    await expect.element(missing).not.toHaveAttribute("aria-describedby");
  });

  test("names the Destination alone without destinations:read", async () => {
    entries = [delivery("publication")];
    await renderTimeline(["alert-groups:read"]);
    const published = item("Published in ops").getByTestId("timeline-text");
    await expect.element(published).toHaveTextContent("Published in ops");
    await expect.element(published).not.toHaveAttribute("title");
    expect(requests.some((u) => u.pathname === "/api/v1/destinations")).toBe(false);
  });
});
