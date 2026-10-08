// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Timeline in a real browser, against a stubbed API: a text for every event and system entry, the actor with the
// Transport, the Loud mark and the Mentions, the details, a Note with markup shown as text, Russian plurals, the kind
// filter and the order sent to the API.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  DeliveryEventKind,
  LifecycleEvent,
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

beforeEach(async () => {
  entries = [];
  requests = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    requests.push(url);
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
      [status("resolved", { reason: "resolved" }), "Resolved: Resolved by Alertmanager"],
      [status("resolved", { reason: "gone" }), "Resolved: Gone"],
      [status("resolved"), "Resolved"],
      [status("acknowledged"), "Acknowledged"],
      [status("takeover", { previous_owner: BOB }), "Taken over from Bob"],
      [
        status("unacknowledged", { reason: "owner_disabled" }),
        "Unacknowledged: the owner was disabled",
      ],
      [status("unresolved"), "Unresolved"],
      [status("snoozed", { snooze_until: AT }), `Snoozed until ${dateTime(AT)}`],
      [status("snoozed", { snooze_until: null }), "Snoozed with no end"],
      [status("unsnoozed"), "Unsnoozed"],
      [status("auto_unacknowledged"), "Acknowledgement released: Reminders went unanswered"],
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
      [timers("ack_timeout", { notice_number: 2 }), "Not acknowledged in time, notice 2"],
      [timers("unclaimed"), "Nobody has taken this"],
      [timers("reminder", { notice_number: 1 }), "Reminder 1"],
      [timers("reminder_answered"), "Reminder answered: still on it"],
      [
        timers("notices_missed", { missed_count: 3 }),
        "3 notices missed while Muster was unavailable",
      ],
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
  });

  test("names the actor with the Transport, and Muster for the system", () => {
    expect(actorText(i18n.t, SYSTEM)).toBe("Muster");
    expect(actorText(i18n.t, ALICE)).toBe("Alice Smith via Mattermost");
    expect(actorText(i18n.t, { ...ALICE, transport: "api", token_name: "ci" })).toBe(
      "Alice Smith via the API, token ci",
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

async function renderTimeline() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = { session: null, ended: false };
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
});
